//go:build windows && (amd64 || arm64)

package desktopbot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	pollInterval = 250 * time.Millisecond
	// cursorPause lets the person at the computer see where the bot acts.
	cursorPause  = 350 * time.Millisecond
	inputUnicode = 0x0004
)

// player replays one recording. Element handles never leave the UI
// Automation thread: each attempt finds and acts in one call to u.do.
type player struct {
	u       *uiaThread
	policy  *Policy
	allowed map[string]bool
	show    *screenOverlay
	outputs map[string]string
}

// errRetry means the element is not ready yet; the step polls again.
var errRetry = errors.New("not ready yet")

func (p *player) replay(ctx context.Context, rec *Recording) (res ReplayResult) {
	for i := range rec.Steps {
		step := &rec.Steps[i]
		skipped, err := p.runStep(ctx, step)
		if err != nil {
			failed := i
			res.FailedStep = &failed
			res.Error = err.Error()
			res.ErrorCode = ErrorCode(err, CodeDesktopError)
			if ctx.Err() != nil && res.ErrorCode == CodeDesktopError {
				res.ErrorCode = CodeTimeout
			}
			if screenshotFitsFailure(res.ErrorCode) {
				shot, err := p.failureShot(step)
				if err != nil {
					res.Warnings = append(res.Warnings, fmt.Sprintf("no screenshot of the failed step: %v", err))
				}
				res.Screenshot = shot
			}
			res.Outputs = p.outputs
			return res
		}
		if skipped {
			res.SkippedSteps = append(res.SkippedSteps, i)
		} else {
			res.StepsExecuted++
		}
	}
	res.Success = true
	res.Outputs = p.outputs
	return res
}

// runStep polls until the step's window and element are there and the action
// is done, the step's time runs out or ctx ends.
func (p *player) runStep(ctx context.Context, step *Step) (skipped bool, err error) {
	limit := time.Duration(step.timeoutMS()) * time.Millisecond
	if step.Optional {
		limit = optionalStepProbeMS * time.Millisecond
	}
	deadline := time.Now().Add(limit)
	var last error
	for {
		if err := ctx.Err(); err != nil {
			return false, botErr(CodeCancelled, "run ended: %w", err)
		}
		if workstationLocked() {
			return false, botErr(CodeWorkstationLocked, "the computer is locked; the bot cannot see the program")
		}
		last = p.attempt(step)
		if last == nil {
			return false, nil
		}
		if !errors.Is(last, errRetry) {
			return false, last
		}
		if time.Now().After(deadline) {
			if step.Optional {
				return true, nil
			}
			return false, timeoutError(step, last)
		}
		select {
		case <-ctx.Done():
		case <-time.After(pollInterval):
		}
	}
}

// waiting wraps why an attempt has to be repeated; the last reason becomes
// the step's error when its time runs out.
type waiting struct {
	code string
	msg  string
}

func (w *waiting) Error() string        { return w.msg }
func (w *waiting) Is(target error) bool { return target == errRetry }

func retry(code, format string, args ...any) error {
	return &waiting{code: code, msg: fmt.Sprintf(format, args...)}
}

func timeoutError(step *Step, last error) error {
	var w *waiting
	if errors.As(last, &w) {
		return botErr(w.code, "%s", w.msg)
	}
	return botErr(CodeTimeout, "step %s did not finish in time: %v", step.Type, last)
}

func (p *player) attempt(step *Step) error {
	win, ok, err := findTopWindow(step.Window)
	if err != nil {
		return err
	}
	if !ok {
		title := ""
		if step.Window.Title != "" {
			title = fmt.Sprintf(" titled %q", step.Window.Title)
		}
		return retry(CodeAppNotRunning, "no window of %s%s is open", step.Window.App, title)
	}
	if err := p.policy.check(win.exe, p.allowed); err != nil {
		return err
	}
	if processElevated(win.pid) {
		return botErr(CodeAppBlocked, "%s runs as administrator; the bot never acts in elevated programs", baseName(win.exe))
	}
	return p.u.do(func() error {
		root, err := p.u.elementFromHandle(win.hwnd)
		if err != nil {
			return retry(CodeAppNotRunning, "window of %s is not ready: %v", step.Window.App, err)
		}
		defer root.release()
		if step.Type == StepFocusWindow {
			return p.focusWindow(win, root)
		}
		nodes, err := p.u.descendants(root)
		if err != nil {
			return retry(CodeSelectorNotFound, "%v", err)
		}
		defer releaseNodes(nodes)
		n, err := pick(nodes, step.Selectors)
		if err != nil {
			return retry(CodeSelectorNotFound, "%v", err)
		}
		if n == nil {
			return retry(CodeSelectorNotFound, "no single control matches %s in %s", describeSelectors(step.Selectors), step.Window.App)
		}
		// Every action checks that the control belongs to the window's own
		// program: a control hosted by another process is refused.
		pid, err := n.el.processID()
		if err != nil {
			return retry(CodeSelectorNotFound, "read control's program: %v", err)
		}
		if pid != win.pid {
			return botErr(CodeWrongWindow, "the control belongs to another program (process %d, window %d)", pid, win.pid)
		}
		return p.act(step, win, n)
	})
}

func describeSelectors(sels []Selector) string {
	parts := make([]string, 0, len(sels))
	for _, s := range sels {
		parts = append(parts, s.Kind+"="+s.Value)
	}
	return strings.Join(parts, ", ")
}

func (p *player) point(step *Step, e element) {
	if p.show == nil {
		return
	}
	r, err := e.bounds()
	if err != nil {
		// The cursor stays where it was; the action itself reports real failures.
		p.show.warn(fmt.Sprintf("could not place the cursor for %s: %v", step.Type, err))
		return
	}
	p.show.pointAt((r.Left+r.Right)/2, (r.Top+r.Bottom)/2, stepLabel(step))
	time.Sleep(cursorPause)
}

func (p *player) focusWindow(win topWindow, root element) error {
	if foregroundPID() == win.pid {
		return nil
	}
	if err := root.setFocus(); err != nil {
		bringToFront(win.hwnd)
	}
	if foregroundPID() != win.pid {
		return retry(CodeWrongWindow, "%s did not come to the front", baseName(win.exe))
	}
	return nil
}

func (p *player) act(step *Step, win topWindow, n *node) error {
	e := n.el
	if requiresEnabled(step.Type) {
		enabled, err := e.isEnabled()
		if err != nil {
			return retry(CodeSelectorNotFound, "read control state: %v", err)
		}
		if !enabled {
			return retry(CodeSelectorNotFound, "the control %s is disabled", n.path)
		}
	}
	switch step.Type {
	case StepWaitFor:
		return nil
	case StepClick:
		p.point(step, e)
		return click(e)
	case StepFill:
		p.point(step, e)
		return p.fill(step, win, e)
	case StepSelect:
		p.point(step, e)
		return p.selectItem(step, e)
	case StepPress:
		p.point(step, e)
		if err := e.setFocus(); err != nil {
			return fmt.Errorf("focus control: %w", err)
		}
		if foregroundPID() != win.pid {
			return retry(CodeWrongWindow, "%s does not have the keyboard", baseName(win.exe))
		}
		return sendKey(step.Key)
	case StepAssertText, StepExtractText:
		text, err := textOf(e)
		if err != nil {
			return retry(CodeSelectorNotFound, "read text: %v", err)
		}
		if step.Type == StepExtractText {
			p.outputs[step.OutputKey] = text
			return nil
		}
		if !strings.Contains(text, step.Value) {
			return retry(CodeAssertionFailed, "expected %q in %q", step.Value, text)
		}
		return nil
	}
	return botErr(CodeInvalidRecording, "unknown step type %q", step.Type)
}

func requiresEnabled(stepType string) bool {
	return stepType == StepClick || stepType == StepFill || stepType == StepSelect || stepType == StepPress
}

func click(e element) error {
	for _, pat := range []struct {
		id  int
		iid *windows.GUID
	}{{patternInvoke, &iidInvokePattern}, {patternToggle, &iidTogglePattern}, {patternSelectionItem, &iidSelectionItemPattern}} {
		ok, err := e.act(pat.id, pat.iid)
		if isNotReady(err) {
			// The control is still appearing; nothing was clicked.
			return retry(CodeSelectorNotFound, "the control did not take the click yet: %v", err)
		}
		if err != nil {
			return fmt.Errorf("click: %w", err)
		}
		if ok {
			return nil
		}
	}
	return botErr(CodeDesktopError, "the control cannot be clicked (it has no Invoke, Toggle or SelectionItem action)")
}

// textOf is what a person reads in the control: its value, else its name.
func textOf(e element) (string, error) {
	v, ok, err := e.valueOf()
	if err != nil {
		return "", err
	}
	if ok {
		return v, nil
	}
	return e.name()
}

// fill sets the value through UI Automation and reads it back. A control
// without a writable value gets typed input, only while its program has the
// keyboard.
func (p *player) fill(step *Step, win topWindow, e element) error {
	secret, err := e.isPassword()
	if err != nil {
		return retry(CodeSelectorNotFound, "read control state: %v", err)
	}
	ok, err := e.setValue(step.Value)
	if err != nil {
		return botErr(CodeValueNotSet, "%v", err)
	}
	if !ok {
		if err := e.setFocus(); err != nil {
			return botErr(CodeValueNotSet, "the control takes no value and cannot be focused: %v", err)
		}
		if foregroundPID() != win.pid {
			return retry(CodeWrongWindow, "%s does not have the keyboard for typing", baseName(win.exe))
		}
		if err := typeText(step.Value); err != nil {
			return botErr(CodeValueNotSet, "%v", err)
		}
	}
	if secret {
		return nil // a password box never shows its value
	}
	got, readable, err := e.valueOf()
	if err != nil {
		return botErr(CodeValueNotSet, "read back: %v", err)
	}
	if readable && !sameText(got, step.Value) {
		return botErr(CodeValueNotSet, "the control shows a different value after writing")
	}
	return nil
}

// selectItem picks the list item named step.Value in a list or drop-down.
func (p *player) selectItem(step *Step, e element) error {
	if _, err := e.act(patternExpandCollapse, &iidExpandCollapsePattern); err != nil {
		return fmt.Errorf("open list: %w", err)
	}
	items, err := p.u.descendants(e)
	if err != nil {
		return retry(CodeSelectorNotFound, "%v", err)
	}
	defer releaseNodes(items)
	choice, err := pick(items, []Selector{{Kind: SelectorName, Value: step.Value, ControlType: "ListItem"}})
	if err != nil {
		return retry(CodeSelectorNotFound, "%v", err)
	}
	if choice == nil {
		return retry(CodeSelectorNotFound, "the list has no single item %q", step.Value)
	}
	// Invoking an item is a click on it: the list takes the choice and
	// closes. Closing a Win32 list through ExpandCollapse instead cancels the
	// choice, so a list without Invoke is selected and then closed, and the
	// read-back below tells whether the choice stayed.
	ok, err := choice.el.act(patternInvoke, &iidInvokePattern)
	if isNotReady(err) {
		// The list is still opening; nothing was chosen.
		return retry(CodeValueNotSet, "the list did not take %q: %v", step.Value, err)
	}
	if err != nil {
		return fmt.Errorf("choose %q: %w", step.Value, err)
	}
	if !ok {
		ok, err = choice.el.act(patternSelectionItem, &iidSelectionItemPattern)
		if err != nil {
			return fmt.Errorf("select %q: %w", step.Value, err)
		}
		if !ok {
			return botErr(CodeDesktopError, "the item %q cannot be selected", step.Value)
		}
		if err := e.collapse(); err != nil {
			return fmt.Errorf("close list: %w", err)
		}
	}
	got, readable, err := e.valueOf()
	if err != nil {
		return botErr(CodeValueNotSet, "read back: %v", err)
	}
	if readable && !sameText(got, step.Value) {
		return botErr(CodeValueNotSet, "the list shows %q after selecting %q", got, step.Value)
	}
	return nil
}

// typeText replaces the focused control's text with Ctrl+A and Unicode key
// events. The caller checked that the target program has the keyboard.
func typeText(text string) error {
	in := []keyboardInput{
		{inputType: inputKeyboard, vk: vkControl},
		{inputType: inputKeyboard, vk: vkA},
		{inputType: inputKeyboard, vk: vkA, flags: keyeventfKeyUp},
		{inputType: inputKeyboard, vk: vkControl, flags: keyeventfKeyUp},
	}
	for _, c := range windows.StringToUTF16(text) {
		if c == 0 {
			break
		}
		in = append(in,
			keyboardInput{inputType: inputKeyboard, scan: c, flags: inputUnicode},
			keyboardInput{inputType: inputKeyboard, scan: c, flags: inputUnicode | keyeventfKeyUp})
	}
	n, _, err := procSendInput.Call(uintptr(len(in)), uintptr(unsafe.Pointer(&in[0])), unsafe.Sizeof(in[0]))
	if int(n) != len(in) {
		return fmt.Errorf("type text: %w", err)
	}
	return nil
}

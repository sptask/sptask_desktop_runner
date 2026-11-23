//go:build windows && (amd64 || arm64)

package desktopbot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows"
)

// The recorder runs two UI Automation threads. The capture thread follows
// the keyboard focus, reads field values and answers the hooks, so it stays
// quick. The selector thread searches the window of every capture for
// selectors that find the control again, which can take a while in a big
// window. Element references go from the first to the second as references
// of their own (addRef); the selector thread releases them.

const (
	capturePoll = 100 * time.Millisecond
	// hookWait bounds how long a click or key waits for the capture thread
	// to read the focused field first: Windows drops a hook that is slow.
	hookWait = 150 * time.Millisecond
	// settleDelay is how long the program gets to react to Enter, Escape or
	// a click before the recorder looks at the field again.
	settleDelay   = 150 * time.Millisecond
	maxClickClimb = 6
	eventQueue    = 64
	resolveTries  = 4
	resolveRetry  = 100 * time.Millisecond
	captureQueue  = 256
)

var keyNames = map[keyClass]string{keyEnter: "Enter", keyEscape: "Escape"}

type inputEvent struct {
	hookEvent
	at   time.Time
	done chan struct{} // closed once the capture thread has handled it
}

// pendingCapture goes from the capture thread to the selector thread.
type pendingCapture struct {
	warn string // only a warning

	el       element // the selector thread's own reference
	ctype    int32
	hwnd     windows.HWND
	window   Window
	stepType string
	value    string
	key      string
	// Read while the control was there, for a window that has closed since.
	props controlProps
}

type controlProps struct{ aid, name, class string }

type recorder struct {
	id      string
	apps    []string
	allowed map[string]bool
	policy  *Policy
	ctx     context.Context
	cancel  context.CancelFunc

	capU, selU *uiaThread
	show       *screenOverlay

	events      chan inputEvent
	caps        chan pendingCapture
	stopCapture chan struct{}
	capDone     chan struct{}
	selDone     chan struct{}
	dropped     atomic.Int64
	capErr      error // set by the capture thread before capDone closes
	selErr      error // set by the selector thread before selDone closes

	asm *assembler // owned by the selector thread until selDone closes
}

// startRecorder starts recording the approved programs among apps until ctx
// ends; cancel ends it early (Shift+Esc calls it). wait returns the result.
func startRecorder(ctx context.Context, cancel context.CancelFunc, policy *Policy, id string, apps []string) (*recorder, error) {
	if workstationLocked() {
		return nil, botErr(CodeWorkstationLocked, "the computer is locked; nothing can be recorded")
	}
	allowed := make(map[string]bool, len(apps))
	for _, a := range apps {
		allowed[a] = true
	}
	r := &recorder{
		id: id, apps: apps, allowed: allowed, policy: policy, ctx: ctx, cancel: cancel,
		events: make(chan inputEvent, eventQueue), caps: make(chan pendingCapture, captureQueue),
		stopCapture: make(chan struct{}), capDone: make(chan struct{}), selDone: make(chan struct{}),
		asm: newAssembler(),
	}
	var err error
	if r.capU, err = startUIA(); err != nil {
		return nil, botErr(CodeDesktopError, "start UI Automation: %w", err)
	}
	if r.selU, err = startUIA(); err != nil {
		r.capU.close()
		return nil, botErr(CodeDesktopError, "start UI Automation: %w", err)
	}
	if r.show, err = startRecordingOverlay(strings.Join(apps, ", "), cancel, r.onInput); err != nil {
		r.capU.close()
		r.selU.close()
		return nil, botErr(ErrorCode(err, CodeDesktopError), "show the recording badge: %w", err)
	}
	go func() {
		defer close(r.capDone)
		defer close(r.caps)
		r.capErr = r.capU.do(func() error { return newCaptureState(r).loop() })
	}()
	go func() {
		defer close(r.selDone)
		r.selErr = r.selU.do(func() error {
			for p := range r.caps {
				r.resolve(p)
			}
			return nil
		})
	}()
	return r, nil
}

// wait blocks until the recording ends and returns what was recorded.
func (r *recorder) wait() Recording {
	<-r.ctx.Done()
	// The hooks go first, so no input arrives after the last flush.
	r.show.close()
	close(r.stopCapture)
	<-r.capDone
	<-r.selDone
	r.capU.close()
	r.selU.close()
	for _, w := range r.show.takeWarnings() {
		r.asm.warn(w)
	}
	for _, err := range []error{r.capErr, r.selErr} {
		if err != nil {
			r.asm.warn(fmt.Sprintf("Kayıt bir hata yüzünden erken bitti: %v", err))
		}
	}
	if r.dropped.Load() > 0 {
		r.asm.warn(warnDroppedInput)
	}
	if errors.Is(r.ctx.Err(), context.DeadlineExceeded) {
		r.asm.warn(warnTimeLimit)
	}
	return r.asm.recording(r.id, r.apps)
}

// onInput runs on the hook thread. A click or Enter waits, bounded, until
// the capture thread has read the focused field: the program has not seen
// the input yet, so a Save that clears the form cannot erase the value first.
func (r *recorder) onInput(ev hookEvent) {
	in := inputEvent{hookEvent: ev, at: time.Now()}
	wait := ev.kind == hookClick || ev.kind == hookKey
	if wait {
		in.done = make(chan struct{})
	}
	select {
	case r.events <- in:
	default:
		r.dropped.Add(1)
		return
	}
	if !wait {
		return
	}
	t := time.NewTimer(hookWait)
	defer t.Stop()
	select {
	case <-in.done:
	case <-t.C:
	}
}

// tracked is the control that has the keyboard focus.
type tracked struct {
	el       element
	hwnd     windows.HWND
	window   Window
	ctype    int32
	stepType string // "" when its value is not recorded
	password bool
	props    controlProps
	// combo is the drop-down the control belongs to: itself, or the
	// drop-down around an edit box that takes typed text. Released by drop.
	combo element
	// initial is the value already accounted for; last is the value now.
	initial, last string
	rebaselineAt  time.Time
}

type pendingPress struct {
	el        element
	hwnd      windows.HWND
	window    Window
	ctype     int32
	key       string
	props     controlProps
	before    string
	hasBefore bool
	expanded  bool
	at        time.Time
}

type appInfo struct {
	name string
	err  error  // why the recorder skips the program's windows
	warn string // told once to the person who recorded
}

// captureState lives on the capture thread only.
type captureState struct {
	r      *recorder
	u      *uiaThread
	cur    *tracked
	press  *pendingPress
	last   element // the control of the last recorded click
	lastAt time.Time
	dbl    time.Duration
	apps   map[string]appInfo
	warned map[string]bool
}

func newCaptureState(r *recorder) *captureState {
	return &captureState{r: r, u: r.capU, dbl: doubleClickTime(), apps: map[string]appInfo{}, warned: map[string]bool{}}
}

func (c *captureState) loop() error {
	tick := time.NewTicker(capturePoll)
	defer tick.Stop()
	defer c.finish()
	for {
		select {
		case <-c.r.stopCapture:
			// Input that arrived before the end still counts.
			for {
				select {
				case ev := <-c.r.events:
					c.handle(ev)
				default:
					return nil
				}
			}
		case ev := <-c.r.events:
			c.handle(ev)
		case now := <-tick.C:
			c.poll(now)
		}
	}
}

func (c *captureState) finish() {
	if c.press != nil {
		c.resolvePress()
	}
	c.flush()
	c.drop()
	c.last.release()
}

func (c *captureState) warn(msg string) {
	// Polls repeat a warning ten times a second; it is sent once.
	if msg == "" || c.warned[msg] {
		return
	}
	c.warned[msg] = true
	c.emit(pendingCapture{warn: msg})
}

func (c *captureState) emit(p pendingCapture) {
	select {
	case c.r.caps <- p:
	default:
		p.el.release()
		c.r.dropped.Add(1)
	}
}

// app decides whether the windows of process pid are recorded. The program
// path is read every time: a process id can be reused by another program.
func (c *captureState) app(pid uint32) appInfo {
	if pid == 0 {
		return appInfo{err: errors.New("no window")}
	}
	exe := processPath(pid)
	key := fmt.Sprintf("%d|%s", pid, exe)
	if a, ok := c.apps[key]; ok {
		return a
	}
	a := appInfo{name: strings.ToLower(baseName(exe))}
	if err := c.r.policy.check(exe, c.r.allowed); err != nil {
		a.err = err
		if c.r.allowed[a.name] && ErrorCode(err, "") == CodeAppNotApproved {
			// On the list but not approved here: the person should know why
			// their work in it is missing.
			a.warn = fmt.Sprintf(warnNotApproved, a.name)
		}
	} else if processElevated(pid) {
		a.err = botErr(CodeAppBlocked, "%s runs as administrator", a.name)
		a.warn = warnElevated
	}
	c.apps[key] = a
	return a
}

func (c *captureState) handle(ev inputEvent) {
	if ev.done != nil {
		defer close(ev.done)
	}
	switch ev.kind {
	case hookClick:
		c.click(ev)
	case hookKey:
		c.keyPress(ev)
	case hookRightClick:
		// Only what happens in a recorded program is reported.
		if c.app(windowPID(windowAt(ev.x, ev.y))).err == nil {
			c.warn(warnRightClick)
		}
	case hookShortcut:
		if c.app(windowPID(rootWindow(windows.GetForegroundWindow()))).err == nil {
			c.warn(warnShortcut)
		}
	}
}

// poll follows the keyboard focus inside the recorded programs.
func (c *captureState) poll(now time.Time) {
	if c.press != nil && now.Sub(c.press.at) >= settleDelay {
		c.resolvePress()
	}
	fg := rootWindow(windows.GetForegroundWindow())
	a := c.app(windowPID(fg))
	if a.err != nil {
		c.warn(a.warn)
		c.flush()
		c.drop()
		return
	}
	el, ok := c.focused(windowPID(fg))
	if !ok {
		c.update(now)
		return
	}
	if c.cur != nil {
		if same, err := c.u.sameElement(c.cur.el, el); err == nil && same {
			el.release()
			c.update(now)
			return
		}
	}
	c.flush()
	c.drop()
	c.cur = c.track(el, fg, a)
}

// focused is the control with the keyboard focus when it belongs to pid.
// While the focus moves there may briefly be none; the next poll looks again.
func (c *captureState) focused(pid uint32) (element, bool) {
	el, err := c.u.focusedElement()
	if err != nil || !el.valid() {
		return element{}, false
	}
	epid, err := el.processID()
	if err != nil || epid != pid {
		el.release()
		return element{}, false
	}
	return c.listOwner(el), true
}

// listOwner returns the tracked control when el is an item of its open
// drop-down list, and el otherwise. Some programs (WPF) move the keyboard
// focus into the open list, which can be a popup window of its own; the
// person is still choosing a value of the drop-down.
func (c *captureState) listOwner(el element) element {
	t := c.cur
	if t == nil || !t.combo.valid() {
		return el
	}
	if ct, err := el.controlType(); err != nil || ct != ctListItem {
		return el
	}
	if open, err := t.combo.expanded(); err != nil || !open {
		return el
	}
	el.release()
	return t.el.addRef()
}

// props reads what identifies a control. A control that vanished while it
// was read leaves them empty; the selector thread then reports the step.
func props(el element) controlProps {
	var p controlProps
	var err error
	if p.aid, err = el.automationID(); err != nil {
		return controlProps{}
	}
	if p.name, err = el.name(); err != nil {
		return controlProps{}
	}
	if p.class, err = el.className(); err != nil {
		return controlProps{}
	}
	return p
}

func (c *captureState) track(el element, hwnd windows.HWND, a appInfo) *tracked {
	t := &tracked{el: el, hwnd: hwnd, window: Window{App: a.name, Title: windowTitle(hwnd)}}
	ct, err := el.controlType()
	if err != nil {
		return t
	}
	t.ctype = ct
	t.combo = c.comboOf(el, ct)
	pw, err := el.isPassword()
	if err != nil {
		return t
	}
	if pw {
		// A password field is never read.
		t.password = true
		c.warn(warnPassword)
		return t
	}
	v, readOnly, hasValue, err := el.valueState()
	if err != nil {
		return t
	}
	stepType, warn := valueStepType(ct, hasValue, readOnly)
	if warn {
		c.warn(warnNoValue)
	}
	t.stepType, t.initial, t.last = stepType, v, v
	if stepType != "" {
		t.props = props(el)
	}
	return t
}

// update reads the tracked field's value again.
func (c *captureState) update(now time.Time) {
	t := c.cur
	if t == nil || t.password || t.stepType == "" {
		return
	}
	v, _, ok, err := t.el.valueState()
	if err != nil || !ok {
		return
	}
	t.last = v
	if t.rebaselineAt.IsZero() {
		return
	}
	// The person's input was recorded when they clicked or pressed a key;
	// what changes while the program answers it (a form it empties) is not
	// theirs.
	t.initial = v
	if !now.Before(t.rebaselineAt) {
		t.rebaselineAt = time.Time{}
	}
}

// flush records the tracked field's value when the person changed it.
func (c *captureState) flush() {
	t := c.cur
	if t == nil || t.password || t.stepType == "" {
		return
	}
	c.update(time.Now())
	if sameText(t.last, t.initial) {
		return
	}
	c.emit(pendingCapture{el: t.el.addRef(), ctype: t.ctype, hwnd: t.hwnd, window: t.window,
		stepType: t.stepType, value: t.last, props: t.props})
	t.initial = t.last
}

func (c *captureState) drop() {
	if c.cur != nil {
		c.cur.el.release()
		c.cur.combo.release()
		c.cur = nil
	}
}

func (c *captureState) click(ev inputEvent) {
	if c.press != nil {
		c.resolvePress()
	}
	c.flush()
	root := windowAt(ev.x, ev.y)
	if c.choosingFromList(root) {
		// An item of an open drop-down: the choice is recorded as the
		// drop-down's value when the focus leaves it.
		return
	}
	pid := windowPID(root)
	a := c.app(pid)
	if a.err != nil {
		c.warn(a.warn)
		return
	}
	el, err := c.u.elementFromPoint(ev.x, ev.y)
	if err != nil || !el.valid() {
		c.warn(warnNoSelector)
		return
	}
	var chain []element
	var types []int32
	defer func() {
		for i := range chain {
			chain[i].release()
		}
	}()
	for cur := el; cur.valid(); {
		ct, err := cur.controlType()
		epid, perr := cur.processID()
		if err != nil || perr != nil || ct == ctWindow || epid != pid || len(chain) > maxClickClimb {
			cur.release()
			break
		}
		chain = append(chain, cur)
		types = append(types, ct)
		next, err := c.u.parent(cur)
		if err != nil {
			break
		}
		cur = next
	}
	idx, outcome := clickTarget(types)
	switch outcome {
	case clickMenu:
		c.warn(warnMenu)
	case clickUnknown:
		c.warn(warnUnknownClick)
	case clickUse:
		c.recordClick(ev, chain[idx], types[idx], root, a)
	}
	c.settle(ev.at)
}

// settle marks the tracked field's changes of the next moment as the
// program's answer to a click or key.
func (c *captureState) settle(at time.Time) {
	if c.cur != nil {
		c.cur.rebaselineAt = at.Add(settleDelay)
	}
}

// choosingFromList reports whether a click in window root picks from the
// list of the focused drop-down: the list is a window of its own, and the
// drop-down keeps the focus while it is open.
func (c *captureState) choosingFromList(root windows.HWND) bool {
	t := c.cur
	if t == nil || !t.combo.valid() || root == t.hwnd {
		return false
	}
	open, err := t.combo.expanded()
	return err == nil && open
}

// comboOf returns a reference to the drop-down that el is, or whose edit box
// it is; an invalid element otherwise.
func (c *captureState) comboOf(el element, ctype int32) element {
	switch ctype {
	case ctComboBox:
		return el.addRef()
	case ctEdit:
		p, err := c.u.parent(el)
		if err != nil || !p.valid() {
			return element{}
		}
		if pct, err := p.controlType(); err == nil && pct == ctComboBox {
			return p
		}
		p.release()
	}
	return element{}
}

func (c *captureState) recordClick(ev inputEvent, target element, ctype int32, root windows.HWND, a appInfo) {
	if c.last.valid() && ev.at.Sub(c.lastAt) <= c.dbl {
		if same, err := c.u.sameElement(c.last, target); err == nil && same {
			c.warn(warnDoubleClick)
			c.lastAt = ev.at
			return
		}
	}
	c.last.release()
	c.last, c.lastAt = target.addRef(), ev.at
	c.emit(pendingCapture{el: target.addRef(), ctype: ctype, hwnd: root,
		window: Window{App: a.name, Title: windowTitle(root)}, stepType: StepClick, props: props(target)})
}

func (c *captureState) keyPress(ev inputEvent) {
	if c.press != nil {
		c.resolvePress()
	}
	c.flush()
	// What the program changes right after a key is its answer, not the
	// person's input; but a key that closes an open drop-down list takes the
	// person's choice (WPF sets the value only then).
	closesList := false
	defer func() {
		if !closesList {
			c.settle(ev.at)
		}
	}()
	fg := rootWindow(windows.GetForegroundWindow())
	pid := windowPID(fg)
	a := c.app(pid)
	if a.err != nil {
		c.warn(a.warn)
		return
	}
	el, ok := c.focused(pid)
	if !ok {
		return
	}
	ct, err := el.controlType()
	if err != nil {
		el.release()
		return
	}
	p := &pendingPress{el: el, hwnd: fg, window: Window{App: a.name, Title: windowTitle(fg)}, ctype: ct,
		key: keyNames[ev.key], props: props(el), at: ev.at}
	// Enter or Escape in an open drop-down list (also from the edit box of an
	// editable one) only closes the list.
	if combo := c.comboOf(el, ct); combo.valid() {
		p.expanded, err = combo.expanded()
		combo.release()
		if err != nil {
			el.release()
			return
		}
		closesList = p.expanded
	}
	if ev.key == keyEnter && (ct == ctEdit || ct == ctDocument) {
		if pw, err := el.isPassword(); err == nil && !pw {
			if v, _, ok, err := el.valueState(); err == nil && ok {
				p.before, p.hasBefore = v, true
			}
		}
	}
	c.press = p
}

// resolvePress records a pending Enter or Escape once the program has seen
// it, unless the control took it: Escape that closed a drop-down, or Enter
// that started a new line in a text box.
func (c *captureState) resolvePress() {
	p := c.press
	c.press = nil
	consumed := p.expanded
	if !consumed && p.hasBefore {
		if v, _, ok, err := p.el.valueState(); err == nil && ok && lineBreaks(v) > lineBreaks(p.before) {
			consumed = true
		}
	}
	if consumed {
		p.el.release()
		return
	}
	c.emit(pendingCapture{el: p.el, ctype: p.ctype, hwnd: p.hwnd, window: p.window, stepType: StepPress, key: p.key, props: p.props})
	// The program may have answered the key by changing the field (a
	// cleared form); that is not the person's input.
	if t := c.cur; t != nil && !t.password && t.stepType != "" {
		if v, _, ok, err := t.el.valueState(); err == nil && ok {
			t.initial, t.last = v, v
		}
	}
}

// resolve runs on the selector thread: it finds selectors for a capture and
// hands the step to the assembler.
func (r *recorder) resolve(p pendingCapture) {
	if p.warn != "" {
		r.asm.warn(p.warn)
		return
	}
	defer p.el.release()
	// The walk through the window fails while a part of it changes (a list
	// opening, a form being emptied); it is tried again before falling back.
	sels, label, ok := r.verifiedSelectors(p)
	for try := 1; !ok && try < resolveTries && windowAlive(p.hwnd); try++ {
		time.Sleep(resolveRetry)
		sels, label, ok = r.verifiedSelectors(p)
	}
	if !ok {
		sels, label = fallbackSelectors(p)
		if p.props.aid == "" && len(sels) > 0 {
			r.asm.warn(warnUnverified)
		}
	}
	target := ""
	if len(sels) > 0 {
		target = sels[0].Kind + "=" + sels[0].Value
	}
	r.asm.add(capture{stepType: p.stepType, hwnd: uintptr(p.hwnd), window: p.window, target: target,
		selectors: sels, value: p.value, key: p.key, label: label})
}

// verifiedSelectors finds the control in its window and keeps the
// selectors that find only it there, then its structural path.
func (r *recorder) verifiedSelectors(p pendingCapture) ([]Selector, string, bool) {
	u := r.selU
	if !windowAlive(p.hwnd) {
		return nil, "", false
	}
	root, err := u.elementFromHandle(p.hwnd)
	if err != nil {
		return nil, "", false
	}
	defer root.release()
	nodes, err := u.descendants(root)
	if err != nil {
		return nil, "", false
	}
	defer releaseNodes(nodes)
	var n *node
	for _, cand := range nodes {
		if cand.ctype != p.ctype {
			continue
		}
		if same, err := u.sameElement(cand.el, p.el); err == nil && same {
			n = cand
			break
		}
	}
	if n == nil {
		return nil, "", false
	}
	ctName := controlTypeName(n.ctype)
	var sels []Selector
	unique := func(sel Selector) {
		if one, err := matchOne(nodes, sel); err == nil && one == n {
			sels = append(sels, sel)
		}
	}
	label := ""
	if aid, err := n.automationID(); err == nil && aid != "" {
		unique(Selector{Kind: SelectorAutomationID, Value: aid})
		label = aid
	}
	// A control whose name is its value (a cell, a typed text) is not named
	// by it: the name would change with the data.
	if name, err := n.elementName(); err == nil && strings.TrimSpace(name) != "" && !sameText(name, p.value) {
		unique(Selector{Kind: SelectorName, Value: name, ControlType: ctName})
		label = name
	}
	if class, err := n.elementClass(); err == nil && class != "" {
		unique(Selector{Kind: SelectorClassName, Value: class, ControlType: ctName})
	}
	sels = append(sels, Selector{Kind: SelectorPath, Value: n.path})
	if combo := comboLabel(nodes, n); combo != "" {
		label = combo
	}
	return sels, label, true
}

// comboLabel names the edit box of an editable drop-down after the
// drop-down: the box itself only has an internal id ("1001").
func comboLabel(nodes []*node, n *node) string {
	i := strings.LastIndex(n.path, "/")
	if n.ctype != ctEdit || i < 0 {
		return ""
	}
	parent := n.path[:i]
	for _, c := range nodes {
		if c.path != parent || c.ctype != ctComboBox {
			continue
		}
		if name, err := c.elementName(); err == nil && strings.TrimSpace(name) != "" {
			return name
		}
		if aid, err := c.automationID(); err == nil && aid != "" {
			return aid
		}
	}
	return ""
}

// fallbackSelectors are read while the control was there, for a window that
// closed (an OK button of a dialog) before the selector thread got to it.
// The player skips a selector that turns out to be ambiguous.
func fallbackSelectors(p pendingCapture) ([]Selector, string) {
	var sels []Selector
	label := ""
	if p.props.aid != "" {
		sels = append(sels, Selector{Kind: SelectorAutomationID, Value: p.props.aid})
		label = p.props.aid
	}
	if name := p.props.name; strings.TrimSpace(name) != "" && !sameText(name, p.value) {
		sels = append(sels, Selector{Kind: SelectorName, Value: name, ControlType: controlTypeName(p.ctype)})
		label = name
	}
	return sels, label
}

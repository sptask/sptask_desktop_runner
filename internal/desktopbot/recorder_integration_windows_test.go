//go:build windows && (amd64 || arm64)

package desktopbot

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/sptask/sptask_desktop_runner/internal/joblock"
)

// The recording tests teach the fixture with injected mouse and keyboard
// input, the way a person would, and then replay what was recorded. They
// move the mouse and type: run them with SPTASK_DESKTOP_TESTS=1 and leave
// the computer alone meanwhile.

var procSetCursorPos = modUser32.NewProc("SetCursorPos")

const (
	inputMouse         = 0
	mouseeventfLeftDn  = 0x0002
	mouseeventfLeftUp  = 0x0004
	humanPause         = 400 * time.Millisecond
	recordTestDuration = 2 * time.Minute
)

// mouseInput mirrors INPUT with a MOUSEINPUT (40 bytes on 64-bit Windows).
type mouseInput struct {
	inputType uint32
	_         uint32
	dx, dy    int32
	mouseData uint32
	flags     uint32
	time      uint32
	_         uint32
	extra     uintptr
}

var (
	_ [40 - unsafe.Sizeof(mouseInput{})]byte
	_ [unsafe.Sizeof(mouseInput{}) - 40]byte
)

// recorderSlots checks the UI Automation calls only the recorder makes.
func recorderSlots(u *uiaThread, byID map[string]*node) error {
	name, combo, save := byID["101"].el, byID["104"].el, byID["201"].el
	if v, ro, ok, err := name.valueState(); err != nil || !ok || ro {
		return fmt.Errorf("ValuePattern.get_CurrentIsReadOnly of the name box: %q %v %v %v", v, ro, ok, err)
	}
	if _, _, ok, err := combo.valueState(); err != nil || !ok {
		return fmt.Errorf("ValuePattern of the drop-down: %v %v", ok, err)
	}
	if open, err := combo.expanded(); err != nil || open {
		return fmt.Errorf("ExpandCollapsePattern.get_CurrentExpandCollapseState: %v %v", open, err)
	}
	ref := name.addRef()
	same, err := u.sameElement(name, ref)
	ref.release()
	if err != nil || !same {
		return fmt.Errorf("AddRef / CompareElements of one control: %v %v", same, err)
	}
	if same, err := u.sameElement(name, save); err != nil || same {
		return fmt.Errorf("CompareElements of two controls: %v %v", same, err)
	}
	r, err := save.bounds()
	if err != nil {
		return err
	}
	at, err := u.elementFromPoint((r.Left+r.Right)/2, (r.Top+r.Bottom)/2)
	if err != nil {
		return fmt.Errorf("ElementFromPoint: %w", err)
	}
	defer at.release()
	if same, err := u.sameElement(at, save); err != nil || !same {
		return fmt.Errorf("ElementFromPoint (POINT by value) did not find the Save button: %v", err)
	}
	if err := name.setFocus(); err != nil {
		return fmt.Errorf("SetFocus: %w", err)
	}
	focus, err := u.focusedElement()
	if err != nil {
		return fmt.Errorf("GetFocusedElement: %w", err)
	}
	defer focus.release()
	if same, err := u.sameElement(focus, name); err != nil || !same {
		return fmt.Errorf("GetFocusedElement is not the focused name box: %v", err)
	}
	return nil
}

// teacher plays the person at the computer.
type teacher struct {
	t *testing.T
	u *uiaThread
	f *fixture
}

func newTeacher(t *testing.T, f *fixture) *teacher {
	t.Helper()
	makeDPIAware()
	u, err := startUIA()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(u.close)
	tc := &teacher{t: t, u: u, f: f}
	if err := u.do(func() error {
		root, err := u.elementFromHandle(f.win.hwnd)
		if err != nil {
			return err
		}
		defer root.release()
		return root.setFocus()
	}); err != nil {
		t.Fatalf("bring the fixture to the front: %v", err)
	}
	time.Sleep(humanPause)
	return tc
}

// center finds a control of the fixture and returns the middle of it.
func (tc *teacher) center(match func(*node) (bool, error)) (int32, int32) {
	tc.t.Helper()
	var x, y int32
	err := tc.u.do(func() error {
		root, err := tc.u.elementFromHandle(tc.f.win.hwnd)
		if err != nil {
			return err
		}
		defer root.release()
		nodes, err := tc.u.descendants(root)
		if err != nil {
			return err
		}
		defer releaseNodes(nodes)
		for _, n := range nodes {
			ok, err := match(n)
			if err != nil {
				return err
			}
			if ok {
				r, err := n.el.bounds()
				if err != nil {
					return err
				}
				x, y = (r.Left+r.Right)/2, (r.Top+r.Bottom)/2
				return nil
			}
		}
		return errNoElement
	})
	if err != nil {
		tc.t.Fatalf("find control: %v", err)
	}
	return x, y
}

func (tc *teacher) click(aid string) {
	tc.t.Helper()
	x, y := tc.center(func(n *node) (bool, error) {
		v, err := n.automationID()
		return v == aid, err
	})
	tc.clickAt(x, y)
}

// openList clicks the arrow of a drop-down that also takes typed text.
func (tc *teacher) openList(aid string) {
	tc.t.Helper()
	var x, y int32
	err := tc.u.do(func() error {
		root, err := tc.u.elementFromHandle(tc.f.win.hwnd)
		if err != nil {
			return err
		}
		defer root.release()
		nodes, err := tc.u.descendants(root)
		if err != nil {
			return err
		}
		defer releaseNodes(nodes)
		n, err := matchOne(nodes, Selector{Kind: SelectorAutomationID, Value: aid})
		if err != nil || n == nil {
			return fmt.Errorf("drop-down %s: %w", aid, errNoElement)
		}
		r, err := n.el.bounds()
		x, y = r.Right-(r.Bottom-r.Top)/2, (r.Top+r.Bottom)/2
		return err
	})
	if err != nil {
		tc.t.Fatal(err)
	}
	tc.clickAt(x, y)
}

func (tc *teacher) clickItem(name string) {
	tc.t.Helper()
	x, y := tc.center(func(n *node) (bool, error) {
		if n.ctype != ctListItem {
			return false, nil
		}
		v, err := n.elementName()
		return v == name, err
	})
	tc.clickAt(x, y)
}

func (tc *teacher) clickAt(x, y int32) {
	tc.t.Helper()
	if ok, _, err := procSetCursorPos.Call(uintptr(x), uintptr(y)); ok == 0 {
		tc.t.Fatalf("move the mouse: %v", err)
	}
	in := []mouseInput{{inputType: inputMouse, flags: mouseeventfLeftDn}, {inputType: inputMouse, flags: mouseeventfLeftUp}}
	if n, _, err := procSendInput.Call(uintptr(len(in)), uintptr(unsafe.Pointer(&in[0])), unsafe.Sizeof(in[0])); int(n) != len(in) {
		tc.t.Fatalf("click: %v", err)
	}
	time.Sleep(humanPause)
}

// typeText types characters without selecting what is already there.
func (tc *teacher) typeText(text string) {
	tc.t.Helper()
	var in []keyboardInput
	for _, c := range windows.StringToUTF16(text) {
		if c == 0 {
			break
		}
		in = append(in,
			keyboardInput{inputType: inputKeyboard, scan: c, flags: inputUnicode},
			keyboardInput{inputType: inputKeyboard, scan: c, flags: inputUnicode | keyeventfKeyUp})
	}
	tc.keys(in)
}

func (tc *teacher) press(key string) {
	tc.t.Helper()
	if err := sendKey(key); err != nil {
		tc.t.Fatal(err)
	}
	time.Sleep(humanPause)
}

func (tc *teacher) shiftEscape() {
	tc.t.Helper()
	tc.keys([]keyboardInput{
		{inputType: inputKeyboard, vk: vkShift},
		{inputType: inputKeyboard, vk: vkEscape},
		{inputType: inputKeyboard, vk: vkEscape, flags: keyeventfKeyUp},
		{inputType: inputKeyboard, vk: vkShift, flags: keyeventfKeyUp},
	})
}

func (tc *teacher) keys(in []keyboardInput) {
	tc.t.Helper()
	if n, _, err := procSendInput.Call(uintptr(len(in)), uintptr(unsafe.Pointer(&in[0])), unsafe.Sizeof(in[0])); int(n) != len(in) {
		tc.t.Fatalf("type: %v", err)
	}
	time.Sleep(humanPause)
}

func waitRecording(t *testing.T, done <-chan Recording) Recording {
	t.Helper()
	select {
	case rec := <-done:
		return rec
	case <-time.After(30 * time.Second):
		t.Fatal("the recording did not end")
		return Recording{}
	}
}

func describeStep(s Step) string {
	target := ""
	for _, sel := range s.Selectors {
		if sel.Kind == SelectorAutomationID {
			target = sel.Value
		}
	}
	parts := []string{s.Type}
	for _, p := range []string{target, s.Value, s.Key} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " ")
}

func TestDesktopRecordingReplaysWhatWasTaught(t *testing.T) {
	f := startFixture(t, "-clear-on-save")
	p := approvedPolicy(t, f.exe)
	app := strings.ToLower(filepath.Base(f.exe))
	m := NewManager(context.Background(), p, &joblock.Lock{})
	done := make(chan Recording, 1)
	if err := m.StartRecording("teach", []string{app}, RecordOptions{MaxDuration: recordTestDuration}, func(r Recording) { done <- r }); err != nil {
		t.Fatal(err)
	}
	tc := newTeacher(t, f)
	tc.click("101")
	tc.typeText("Ayşe Yılmaz")
	tc.click("102")
	tc.typeText("Satır 1")
	tc.press("Enter") // a new line in the note, not a step
	tc.typeText("Satır 2")
	tc.click("103")
	tc.typeText("gizli") // a password is never recorded
	tc.click("104")
	tc.press("Escape") // closes the list, not a step
	tc.click("104")
	tc.clickItem("Kart")
	tc.openList("105") // a drop-down that also takes typed text
	tc.clickItem("İzmir")
	tc.click("201") // Kaydet: the form saves and empties itself
	tc.click("101")
	tc.typeText("Mehmet")
	tc.press("Enter") // saves again; the emptied form is not recorded
	tc.shiftEscape()
	rec := waitRecording(t, done)
	m.Wait()
	if activeOverlay.Load() != nil {
		t.Fatal("the recording badge is still showing")
	}

	got := make([]string, 0, len(rec.Steps))
	for _, s := range rec.Steps {
		got = append(got, describeStep(s))
		if s.Type != StepFocusWindow && s.Selectors[len(s.Selectors)-1].Kind != SelectorPath {
			t.Errorf("step %s has no structural path: %+v", s.Type, s.Selectors)
		}
		if strings.Contains(s.Value, "gizli") {
			t.Fatalf("a password was recorded: %+v", s)
		}
	}
	want := []string{"focus_window", "fill 101 Ayşe Yılmaz", "fill 102 Satır 1\r\nSatır 2", "select 104 Kart", "fill 1001 İzmir", "click 201", "fill 101 Mehmet", "press 101 Enter"}
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Fatalf("recorded steps:\n%q\nwant:\n%q\nwarnings: %v", got, want, rec.Warnings)
	}
	if !contains(rec.Warnings, warnPassword) {
		t.Fatalf("the password field was not reported: %v", rec.Warnings)
	}
	if rec.ID != "teach" || len(rec.AllowedApps) != 1 || rec.AllowedApps[0] != app {
		t.Fatalf("recording = %+v", rec)
	}
	taught := waitSaves(t, f, 2)

	res := replayNow(t, p, rec, time.Minute)
	if !res.Success {
		t.Fatalf("replay of the taught recording failed: %+v", res)
	}
	saves := waitSaves(t, f, 4)
	for i, want := range taught {
		got := saves[2+i]
		for _, field := range []string{"name", "note", "payment", "city"} {
			if !sameText(fmt.Sprint(got[field]), fmt.Sprint(want[field])) {
				t.Errorf("save %d %s: replay gave %q, teaching gave %q", i+1, field, got[field], want[field])
			}
		}
	}
}

func TestDesktopRecordingEndsByItself(t *testing.T) {
	f := startFixture(t)
	p := approvedPolicy(t, f.exe)
	app := strings.ToLower(filepath.Base(f.exe))
	m := NewManager(context.Background(), p, &joblock.Lock{})

	done := make(chan Recording, 1)
	if err := m.StartRecording("limit", []string{app}, RecordOptions{MaxDuration: time.Second}, func(r Recording) { done <- r }); err != nil {
		t.Fatal(err)
	}
	if rec := waitRecording(t, done); !contains(rec.Warnings, warnTimeLimit) || !contains(rec.Warnings, warnNothingCaught) {
		t.Fatalf("time limit: %+v", rec)
	}

	if err := m.StartRecording("stop", []string{app}, RecordOptions{}, func(r Recording) { done <- r }); err != nil {
		t.Fatal(err)
	}
	if !m.Stop("stop") {
		t.Fatal("the recording could not be stopped")
	}
	if rec := waitRecording(t, done); rec.ID != "stop" || contains(rec.Warnings, warnTimeLimit) {
		t.Fatalf("stopped recording: %+v", rec)
	}
	m.Wait()
	if activeOverlay.Load() != nil {
		t.Fatal("the recording badge is still showing")
	}
}

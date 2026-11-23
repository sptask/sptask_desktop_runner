//go:build windows && (amd64 || arm64)

package desktopbot

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/sptask/sptask_desktop_runner/internal/joblock"
	"github.com/sptask/sptask_desktop_runner/internal/operator"
)

// These tests open a small form on the desktop (testdata/fixture) and act on
// it only through UI Automation. They are opt-in because a window appears:
//
//	SPTASK_DESKTOP_TESTS=1 go test ./internal/desktopbot/
const fixtureClass = "SpartaskFixtureForm"

type fixture struct {
	exe, out, title string
	cmd             *exec.Cmd
	win             topWindow
}

func startFixture(t *testing.T, args ...string) *fixture {
	t.Helper()
	if os.Getenv("SPTASK_DESKTOP_TESTS") != "1" {
		t.Skip("set SPTASK_DESKTOP_TESTS=1 to open the fixture window")
	}
	dir := t.TempDir()
	f := &fixture{
		exe:   filepath.Join(dir, "spartask-fixture.exe"),
		out:   filepath.Join(dir, "saves.jsonl"),
		title: fmt.Sprintf("SparTask Fikstür %d", time.Now().UnixNano()),
	}
	build := exec.Command("go", "build", "-o", f.exe, "./testdata/fixture")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, out)
	}
	f.cmd = exec.Command(f.exe, append([]string{"-out", f.out, "-title", f.title}, args...)...)
	if err := f.cmd.Start(); err != nil {
		t.Fatalf("start fixture: %v", err)
	}
	t.Cleanup(func() {
		if err := f.cmd.Process.Kill(); err != nil {
			t.Logf("stop fixture: %v", err)
		}
		if err := f.cmd.Wait(); err != nil {
			t.Logf("fixture ended: %v", err)
		}
	})
	deadline := time.Now().Add(15 * time.Second)
	for {
		w, ok, err := findTopWindow(Window{App: filepath.Base(f.exe), Title: f.title})
		if err != nil {
			t.Fatal(err)
		}
		if ok && uiaReady(w.hwnd, "301") {
			f.win = w
			return f
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture window did not appear")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (f *fixture) saves(t *testing.T) []map[string]any {
	t.Helper()
	file, err := os.Open(f.out)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var out []map[string]any
	sc := bufio.NewScanner(file)
	for sc.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatal(err)
		}
		out = append(out, rec)
	}
	return out
}

// TestUIAVtableSlots checks every vtable slot the bot uses against answers
// the fixture is known to give. A wrong slot shows up here, by name.
func TestUIAVtableSlots(t *testing.T) {
	f := startFixture(t)
	u, err := startUIA()
	if err != nil {
		t.Fatal(err)
	}
	defer u.close()
	err = u.do(func() error {
		root, err := u.elementFromHandle(f.win.hwnd)
		if err != nil {
			return fmt.Errorf("ElementFromHandle: %w", err)
		}
		defer root.release()
		checks := []struct {
			slot string
			get  func() (any, error)
			want any
		}{
			{"get_CurrentNativeWindowHandle", func() (any, error) { return root.nativeWindow() }, f.win.hwnd},
			{"get_CurrentName", func() (any, error) { return root.name() }, f.title},
			{"get_CurrentClassName", func() (any, error) { return root.className() }, fixtureClass},
			{"get_CurrentProcessId", func() (any, error) { return root.processID() }, f.win.pid},
			{"get_CurrentControlType", func() (any, error) { return root.controlType() }, int32(50032)},
		}
		for _, c := range checks {
			got, err := c.get()
			if err != nil || got != c.want {
				return fmt.Errorf("%s: got %v (%v), want %v", c.slot, got, err, c.want)
			}
		}
		nodes, err := u.descendants(root)
		if err != nil {
			return fmt.Errorf("tree walker: %w", err)
		}
		defer releaseNodes(nodes)
		byID := map[string]*node{}
		for _, n := range nodes {
			id, err := n.automationID()
			if err != nil {
				return fmt.Errorf("get_CurrentAutomationId: %w", err)
			}
			byID[id] = n
		}
		for _, id := range []string{"101", "102", "103", "104", "201", "202", "301"} {
			if byID[id] == nil {
				return fmt.Errorf("get_CurrentAutomationId / walker: control %s missing (found %d controls)", id, len(nodes))
			}
		}
		if ct := byID["101"].ctype; ct != 50004 {
			return fmt.Errorf("get_CurrentControlType of the name box: %d", ct)
		}
		for id, want := range map[string]bool{"101": false, "103": true} {
			got, err := byID[id].el.isPassword()
			if err != nil || got != want {
				return fmt.Errorf("get_CurrentIsPassword of %s: %v (%v)", id, got, err)
			}
		}
		for id, want := range map[string]bool{"201": true, "202": false} {
			got, err := byID[id].el.isEnabled()
			if err != nil || got != want {
				return fmt.Errorf("get_CurrentIsEnabled of %s: %v (%v)", id, got, err)
			}
		}
		if off, err := byID["201"].el.isOffscreen(); err != nil || off {
			return fmt.Errorf("get_CurrentIsOffscreen: %v (%v)", off, err)
		}
		r, err := byID["201"].el.bounds()
		if err != nil || r.Right-r.Left < 50 || r.Bottom-r.Top < 15 {
			return fmt.Errorf("get_CurrentBoundingRectangle: %+v (%v)", r, err)
		}
		if ok, err := byID["101"].el.setValue("Deneme"); !ok || err != nil {
			return fmt.Errorf("ValuePattern.SetValue: %v %v", ok, err)
		}
		if v, ok, err := byID["101"].el.valueOf(); !ok || err != nil || v != "Deneme" {
			return fmt.Errorf("ValuePattern.get_CurrentValue: %q %v %v", v, ok, err)
		}
		if ok, err := byID["301"].el.setValue("x"); ok || err != nil {
			return fmt.Errorf("a label took a value: %v %v", ok, err)
		}
		if err := recorderSlots(u, byID); err != nil {
			return err
		}
		if ok, err := byID["201"].el.act(patternInvoke, &iidInvokePattern); !ok || err != nil {
			return fmt.Errorf("InvokePattern.Invoke: %v %v", ok, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	waitSaves(t, f, 1)
}

func waitSaves(t *testing.T, f *fixture, n int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := f.saves(t)
		if len(got) >= n || time.Now().After(deadline) {
			if len(got) != n {
				t.Fatalf("want %d saves, got %d: %v", n, len(got), got)
			}
			return got
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func approvedPolicy(t *testing.T, exe string) *Policy {
	t.Helper()
	p, err := LoadPolicy(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Approve(exe); err != nil {
		t.Fatal(err)
	}
	return p
}

func fixtureRecording(f *fixture, steps ...Step) Recording {
	app := strings.ToLower(filepath.Base(f.exe))
	for i := range steps {
		steps[i].Window = Window{App: app, Title: f.title}
	}
	return Recording{ID: "fixture", AllowedApps: []string{app}, Steps: steps}
}

func byAutomationID(id string) []Selector {
	return []Selector{{Kind: SelectorClassName, Value: "Edit"}, {Kind: SelectorAutomationID, Value: id}}
}

func replayNow(t *testing.T, p *Policy, rec Recording, timeout time.Duration) ReplayResult {
	t.Helper()
	m := NewManager(context.Background(), p, &joblock.Lock{})
	done := make(chan ReplayResult, 1)
	if err := m.StartReplay("t", rec, ReplayOptions{Timeout: timeout, Operator: operator.Operator{Name: "Ada (AI)", Color: "#0ea5e9"}}, func(r ReplayResult) { done <- r }); err != nil {
		t.Fatal(err)
	}
	res := <-done
	m.Wait()
	return res
}

func TestDesktopReplayFillsTheForm(t *testing.T) {
	f := startFixture(t)
	rec := fixtureRecording(f,
		Step{Type: StepFocusWindow, Optional: true},
		// class_name "Edit" matches every box and is skipped as ambiguous.
		Step{Type: StepFill, Selectors: byAutomationID("101"), Value: "Ayşe Yılmaz", Label: "Müşteri adı"},
		Step{Type: StepFill, Selectors: byAutomationID("102"), Value: "Satır 1\nSatır 2", Label: "Not"},
		Step{Type: StepFill, Selectors: byAutomationID("103"), Value: "gizli", Label: "Şifre", Sensitive: true},
		Step{Type: StepSelect, Selectors: []Selector{{Kind: SelectorAutomationID, Value: "104"}}, Value: "Kart", Label: "Ödeme"},
		Step{Type: StepClick, Selectors: []Selector{{Kind: SelectorName, Value: "Kaydet", ControlType: "Button"}}, Label: "Kaydet"},
		Step{Type: StepAssertText, Selectors: []Selector{{Kind: SelectorAutomationID, Value: "301"}}, Value: "Kaydedildi: 1"},
		Step{Type: StepExtractText, Selectors: []Selector{{Kind: SelectorAutomationID, Value: "301"}}, OutputKey: "durum"},
	)
	res := replayNow(t, approvedPolicy(t, f.exe), rec, time.Minute)
	if !res.Success {
		t.Fatalf("replay failed: %+v", res)
	}
	got := waitSaves(t, f, 1)[0]
	if got["name"] != "Ayşe Yılmaz" || !sameText(fmt.Sprint(got["note"]), "Satır 1\nSatır 2") || got["secret_length"] != float64(5) || got["payment"] != "Kart" {
		t.Fatalf("the program received %v", got)
	}
	if res.Outputs["durum"] != "Kaydedildi: 1" {
		t.Fatalf("outputs %v", res.Outputs)
	}
}

func TestDesktopReplayRefuses(t *testing.T) {
	f := startFixture(t)
	fill := Step{Type: StepFill, Selectors: byAutomationID("101"), Value: "x", TimeoutMS: 1500}

	unapproved, err := LoadPolicy(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if res := replayNow(t, unapproved, fixtureRecording(f, fill), time.Minute); res.ErrorCode != CodeAppNotApproved || res.Screenshot != "" {
		t.Fatalf("an unapproved program was automated or photographed: %+v", res)
	}

	p := approvedPolicy(t, f.exe)
	disabled := Step{Type: StepClick, Selectors: []Selector{{Kind: SelectorAutomationID, Value: "202"}}, TimeoutMS: 1500}
	if res := replayNow(t, p, fixtureRecording(f, disabled), time.Minute); res.ErrorCode != CodeSelectorNotFound {
		t.Fatalf("a disabled button was clicked: %+v", res)
	}
	missing := Step{Type: StepFill, Selectors: byAutomationID("999"), Value: "x", TimeoutMS: 1500}
	res := replayNow(t, p, fixtureRecording(f, missing), time.Minute)
	if res.ErrorCode != CodeSelectorNotFound {
		t.Fatalf("a missing control: %+v", res)
	}
	// The failure comes with a picture of the program's own window: no larger
	// than the window on the screen and without black, unpainted areas (the
	// form is light grey and white).
	var r windows.Rect
	if ok, _, err := procGetWindowRect.Call(uintptr(f.win.hwnd), uintptr(unsafe.Pointer(&r))); ok == 0 {
		t.Fatalf("read the form's size: %v", err)
	}
	shot := decodeScreenshot(t, res.Screenshot)
	if size := shot.Bounds().Size(); size.X <= 0 || size.Y <= 0 || size.X > int(r.Right-r.Left) || size.Y > int(r.Bottom-r.Top) {
		t.Fatalf("the screenshot is %v, the form is %v; warnings: %v", size, r, res.Warnings)
	}
	if dark := darkShare(shot); dark > 0.15 {
		t.Fatalf("%.0f%% of the screenshot is black", dark*100)
	}
	closed := fixtureRecording(f, fill)
	closed.Steps[0].Window.Title = "Kapalı pencere"
	if res := replayNow(t, p, closed, time.Minute); res.ErrorCode != CodeAppNotRunning || res.Screenshot != "" {
		t.Fatalf("a closed window: %+v", res)
	}
	if got := f.saves(t); len(got) != 0 {
		t.Fatalf("nothing should have been saved: %v", got)
	}
}

// decodeScreenshot decodes a failure screenshot.
func decodeScreenshot(t *testing.T, shot string) image.Image {
	t.Helper()
	if shot == "" {
		t.Fatal("no screenshot")
	}
	raw, err := base64.StdEncoding.DecodeString(shot)
	if err != nil {
		t.Fatalf("screenshot is not base64: %v", err)
	}
	img, err := jpeg.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("screenshot is not a JPEG: %v", err)
	}
	return img
}

// darkShare is the share of nearly black pixels in img.
func darkShare(img image.Image) float64 {
	b := img.Bounds()
	dark := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if r>>8 < 24 && g>>8 < 24 && bl>>8 < 24 {
				dark++
			}
		}
	}
	return float64(dark) / float64(b.Dx()*b.Dy())
}

func TestDesktopReplayStopsWhenThePersonTakesOver(t *testing.T) {
	f := startFixture(t)
	wait := Step{Type: StepWaitFor, Selectors: byAutomationID("999"), TimeoutMS: 30000}
	go func() {
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if o := activeOverlay.Load(); o != nil {
				o.takeOver() // what a physical click or Shift+Esc does
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()
	res := replayNow(t, approvedPolicy(t, f.exe), fixtureRecording(f, wait), time.Minute)
	if res.ErrorCode != CodeStoppedByUser {
		t.Fatalf("want STOPPED_BY_USER, got %+v", res)
	}
	if activeOverlay.Load() != nil {
		t.Fatal("the overlay is still showing")
	}
}

// uiaReady reports whether UI Automation already sees the form's controls:
// a window that has just appeared may not have its element yet. statusID is
// the automation id of the form's status line, its last control.
func uiaReady(hwnd windows.HWND, statusID string) bool {
	u, err := startUIA()
	if err != nil {
		return false
	}
	defer u.close()
	return u.do(func() error {
		root, err := u.elementFromHandle(hwnd)
		if err != nil {
			return err
		}
		defer root.release()
		nodes, err := u.descendants(root)
		if err != nil {
			return err
		}
		defer releaseNodes(nodes)
		for _, n := range nodes {
			if id, err := n.automationID(); err == nil && id == statusID {
				return nil
			}
		}
		return errNoElement
	}) == nil
}

package desktopbot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sptask/sptask_desktop_runner/internal/joblock"
)

func TestClickTarget(t *testing.T) {
	cases := []struct {
		name  string
		chain []int32
		idx   int
		want  clickOutcome
	}{
		{"button", []int32{ctButton, ctPane}, 0, clickUse},
		{"text inside a button", []int32{ctText, ctButton, ctPane}, 1, clickUse},
		{"check box", []int32{ctCheckBox}, 0, clickUse},
		{"grid row", []int32{ctText, ctDataItem, ctPane}, 1, clickUse},
		{"menu item", []int32{ctMenuItem, ctMenu}, -1, clickMenu},
		{"button on a toolbar menu", []int32{ctButton, ctMenuBar}, -1, clickMenu},
		{"drop-down item", []int32{ctListItem, ctPane, ctComboBox}, -1, clickIgnore},
		{"drop-down arrow", []int32{ctButton, ctComboBox}, -1, clickIgnore},
		{"close button", []int32{ctButton, ctTitleBar}, -1, clickIgnore},
		{"scroll button", []int32{ctButton, ctScrollBar}, -1, clickIgnore},
		{"text box", []int32{ctEdit, ctPane}, -1, clickIgnore},
		{"label", []int32{ctText, ctPane}, -1, clickPassive},
		{"empty window", nil, -1, clickPassive},
		{"custom control", []int32{50025, ctPane}, -1, clickUnknown},
	}
	for _, c := range cases {
		idx, got := clickTarget(c.chain)
		if idx != c.idx || got != c.want {
			t.Errorf("%s: got (%d, %v), want (%d, %v)", c.name, idx, got, c.idx, c.want)
		}
	}
}

func TestValueStepType(t *testing.T) {
	cases := []struct {
		ctype              int32
		hasValue, readOnly bool
		want               string
		warn               bool
	}{
		{ctEdit, true, false, StepFill, false},
		{ctEdit, true, true, "", false},
		{ctEdit, false, false, "", true},
		{ctDocument, false, false, "", true},
		{ctComboBox, true, true, StepSelect, false},
		{ctComboBox, true, false, StepSelect, false},
		{ctButton, false, false, "", false},
		{ctSpinner, true, false, StepFill, false},
	}
	for _, c := range cases {
		got, warn := valueStepType(c.ctype, c.hasValue, c.readOnly)
		if got != c.want || warn != c.warn {
			t.Errorf("valueStepType(%d, %v, %v) = %q, %v", c.ctype, c.hasValue, c.readOnly, got, warn)
		}
	}
}

func TestClassifyKey(t *testing.T) {
	cases := []struct {
		name             string
		vk               uint32
		shift, ctrl, alt bool
		want             keyClass
	}{
		{"letter", 'K', false, false, false, keyIgnore},
		{"capital", 'K', true, false, false, keyIgnore},
		{"AltGr @", 'Q', false, true, true, keyIgnore},
		{"paste", vkV, false, true, false, keyIgnore},
		{"undo", vkZ, false, true, false, keyIgnore},
		{"control alone", vkControl, false, true, false, keyIgnore},
		{"tab", 0x09, false, false, false, keyIgnore},
		{"enter", vkReturn, false, false, false, keyEnter},
		{"shift+enter", vkReturn, true, false, false, keyEnter},
		{"escape", vkEscape, false, false, false, keyEscape},
		{"shift+escape", vkEscape, true, false, false, keyStop},
		{"save shortcut", 'S', false, true, false, keyShortcut},
		{"alt menu key", 'D', false, false, true, keyShortcut},
		{"F2", vkF1 + 1, false, false, false, keyShortcut},
		{"ctrl+enter", vkReturn, false, true, false, keyShortcut},
	}
	for _, c := range cases {
		if got := classifyKey(c.vk, c.shift, c.ctrl, c.alt); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func fillCapture(hwnd uintptr, target, value string) capture {
	return capture{stepType: StepFill, hwnd: hwnd, window: Window{App: "eczane.exe", Title: "Reçete"}, target: target,
		selectors: []Selector{{Kind: SelectorAutomationID, Value: target}}, value: value, label: "Hasta"}
}

func TestAssemblerBuildsAReplayableRecording(t *testing.T) {
	a := newAssembler()
	a.add(fillCapture(1, "101", "Ay"))
	a.add(fillCapture(1, "101", "Ayşe")) // came back to the same field
	a.add(capture{stepType: StepClick, hwnd: 1, window: Window{App: "eczane.exe", Title: "Reçete"}, target: "201",
		selectors: []Selector{{Kind: SelectorName, Value: "Kaydet", ControlType: "Button"}}})
	a.add(capture{stepType: StepClick, hwnd: 2, window: Window{App: "eczane.exe", Title: "Onay"}, target: "1",
		selectors: []Selector{{Kind: SelectorName, Value: "Tamam", ControlType: "Button"}}})
	a.add(fillCapture(1, "101", "Mehmet"))
	rec := a.recording("r1", []string{"eczane.exe"})

	types := make([]string, 0, len(rec.Steps))
	for _, s := range rec.Steps {
		types = append(types, s.Type)
	}
	want := "focus_window fill click focus_window click focus_window fill"
	if got := strings.Join(types, " "); got != want {
		t.Fatalf("steps = %s, want %s", got, want)
	}
	if rec.Steps[1].Value != "Ayşe" || rec.Steps[6].Value != "Mehmet" {
		t.Fatalf("values = %q, %q", rec.Steps[1].Value, rec.Steps[6].Value)
	}
	if err := rec.Validate(); err != nil {
		t.Fatalf("the player would refuse the recording: %v", err)
	}
	if len(rec.Warnings) != 0 {
		t.Fatalf("warnings = %v", rec.Warnings)
	}
}

func TestAssemblerKeepsTheLimits(t *testing.T) {
	a := newAssembler()
	long := capture{stepType: StepClick, hwnd: 1, window: Window{App: "eczane.exe", Title: strings.Repeat("ş", MaxTitleLength)},
		target: "x", selectors: []Selector{
			{Kind: SelectorName, Value: strings.Repeat("a", MaxSelectorLength+1)},
			{Kind: SelectorAutomationID, Value: "1"}, {Kind: SelectorAutomationID, Value: "2"}, {Kind: SelectorAutomationID, Value: "3"},
			{Kind: SelectorAutomationID, Value: "4"}, {Kind: SelectorAutomationID, Value: "5"}, {Kind: SelectorAutomationID, Value: "6"},
			{Kind: SelectorPath, Value: "Button[0]"},
		}}
	a.add(long)
	a.add(fillCapture(1, "101", strings.Repeat("x", MaxValueLength+1)))
	a.add(capture{stepType: StepClick, hwnd: 1, window: Window{App: "eczane.exe"}})
	for i := 0; i < MaxSteps; i++ {
		a.add(capture{stepType: StepClick, hwnd: 1, window: Window{App: "eczane.exe"}, target: "b",
			selectors: []Selector{{Kind: SelectorAutomationID, Value: "b"}}})
	}
	rec := a.recording("r1", []string{"eczane.exe"})
	if err := rec.Validate(); err != nil {
		t.Fatalf("the player would refuse the recording: %v", err)
	}
	if len(rec.Steps) != MaxSteps {
		t.Fatalf("%d steps, want the limit %d", len(rec.Steps), MaxSteps)
	}
	first := rec.Steps[1]
	if len(first.Selectors) != MaxSelectorsPerStep || first.Selectors[0].Value != "1" {
		t.Fatalf("selectors = %+v", first.Selectors)
	}
	if len(first.Window.Title) > MaxTitleLength || !strings.HasPrefix(strings.Repeat("ş", MaxTitleLength), first.Window.Title) {
		t.Fatalf("title cut badly: %d bytes", len(first.Window.Title))
	}
	for _, w := range []string{warnSelectorLong, warnValueLong, warnNoSelector, warnStepLimit} {
		if !contains(rec.Warnings, w) {
			t.Errorf("missing warning %q in %v", w, rec.Warnings)
		}
	}
}

func TestAssemblerWarnsOnceAndCapsWarnings(t *testing.T) {
	a := newAssembler()
	a.warn(warnMenu)
	a.warn(warnMenu)
	for i := 0; i < maxRecordWarnings*2; i++ {
		a.warn(strings.Repeat("w", i+1))
	}
	rec := a.recording("r1", []string{"eczane.exe"})
	if len(rec.Warnings) != maxRecordWarnings || rec.Warnings[0] != warnMenu || rec.Warnings[1] == warnMenu {
		t.Fatalf("warnings: %d, first %q, %q", len(rec.Warnings), rec.Warnings[0], rec.Warnings[1])
	}
	empty := newAssembler().recording("r2", []string{"eczane.exe"})
	if len(empty.Steps) != 0 || !contains(empty.Warnings, warnNothingCaught) {
		t.Fatalf("empty recording = %+v", empty)
	}
}

func TestTruncateBytes(t *testing.T) {
	for in, want := range map[string]string{"abc": "ab", "aş": "a", "şş": "ş", "": ""} {
		if got := truncateBytes(in, 2); got != want {
			t.Errorf("truncateBytes(%q, 2) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeApps(t *testing.T) {
	got, err := normalizeApps([]string{"Muhasebe.EXE", "eczane.exe", "ECZANE.exe"})
	if err != nil || strings.Join(got, ",") != "eczane.exe,muhasebe.exe" {
		t.Fatalf("normalizeApps = %v, %v", got, err)
	}
	for _, bad := range [][]string{nil, {`C:\eczane.exe`}, {"eczane"}, make([]string, MaxAllowedApps+1)} {
		if _, err := normalizeApps(bad); err == nil {
			t.Errorf("normalizeApps(%q) accepted", bad)
		}
	}
}

func TestStartRecordingChecksBeforeRecording(t *testing.T) {
	dir := t.TempDir()
	policy, err := LoadPolicy(dir)
	if err != nil {
		t.Fatal(err)
	}
	exe := writeProgram(t, dir, "eczane.exe")
	if err := policy.Approve(exe); err != nil {
		t.Fatal(err)
	}
	jobs := &joblock.Lock{}
	m := NewManager(context.Background(), policy, jobs)
	never := func(Recording) { t.Error("a refused recording reported a result") }
	cases := map[string]struct {
		apps []string
		code string
	}{
		"no apps":          {nil, CodeInvalidRecording},
		"path instead":     {[]string{exe}, CodeInvalidRecording},
		"a shell":          {[]string{"eczane.exe", "cmd.exe"}, CodeAppBlocked},
		"nothing approved": {[]string{"muhasebe.exe"}, CodeAppNotApproved},
	}
	for name, c := range cases {
		err := m.StartRecording("r1", c.apps, RecordOptions{}, never)
		if got := ErrorCode(err, ""); got != c.code {
			t.Errorf("%s: code %q (%v), want %s", name, got, err, c.code)
		}
	}
	if err := m.StartRecording(" ", []string{"eczane.exe"}, RecordOptions{}, never); ErrorCode(err, "") != CodeInvalidRecording {
		t.Errorf("empty id: %v", err)
	}
	release, _, _ := jobs.TryAcquire("browser replay x")
	defer release()
	if err := m.StartRecording("r1", []string{"eczane.exe"}, RecordOptions{}, never); ErrorCode(err, "") != CodeBusy {
		t.Errorf("while another job runs: %v", err)
	}
	m.Wait()
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func writeProgram(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

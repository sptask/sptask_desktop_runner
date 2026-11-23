package desktopbot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validRecording() Recording {
	win := Window{App: "Eczane.exe", Title: "Reçete"}
	return Recording{ID: "r1", AllowedApps: []string{"eczane.exe"}, Steps: []Step{
		{Type: StepFocusWindow, Window: win},
		{Type: StepFill, Window: win, Selectors: []Selector{{Kind: SelectorAutomationID, Value: "txtHasta"}}, Value: "Ali"},
		{Type: StepClick, Window: win, Selectors: []Selector{{Kind: SelectorName, Value: "Kaydet", ControlType: "Button"}}},
	}}
}

func TestValidate(t *testing.T) {
	rec := validRecording()
	if err := rec.Validate(); err != nil {
		t.Fatalf("valid recording rejected: %v", err)
	}
	cases := map[string]func(r *Recording){
		"no allowed apps":      func(r *Recording) { r.AllowedApps = nil },
		"app outside the list": func(r *Recording) { r.Steps[1].Window.App = "muhasebe.exe" },
		"app with a path":      func(r *Recording) { r.AllowedApps = []string{`C:\x\eczane.exe`} },
		"not a program":        func(r *Recording) { r.AllowedApps[0] = "eczane.bat"; r.Steps[0].Window.App = "eczane.bat" },
		"free key": func(r *Recording) {
			r.Steps = append(r.Steps, Step{Type: StepPress, Window: r.Steps[0].Window, Key: "Delete"})
		},
		"element step, no target": func(r *Recording) { r.Steps[2].Selectors = nil },
		"unknown selector":        func(r *Recording) { r.Steps[2].Selectors[0].Kind = "xpath" },
		"script step":             func(r *Recording) { r.Steps[0].Type = "run" },
	}
	for name, mutate := range cases {
		r := validRecording()
		mutate(&r)
		if err := r.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestStepLabelNeverShowsTheValue(t *testing.T) {
	step := &Step{Type: StepFill, Label: "Hasta adı", Value: "Ali Veli"}
	got := stepLabel(step)
	if got != "Yazıyor: Hasta adı" || strings.Contains(got, "Ali") {
		t.Fatalf("label %q", got)
	}
}

func TestPolicy(t *testing.T) {
	dir := t.TempDir()
	app := filepath.Join(dir, "Eczane.exe")
	if err := os.WriteFile(app, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadPolicy(dir)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"eczane.exe": true}
	if err := p.check(app, allowed); ErrorCode(err, "") != CodeAppNotApproved {
		t.Fatalf("an unapproved program was accepted: %v", err)
	}
	if err := p.Approve("Eczane.exe"); err == nil {
		t.Fatal("a relative path was approved")
	}
	if err := p.Approve(app); err != nil {
		t.Fatal(err)
	}
	if err := p.check(strings.ToUpper(app), allowed); err != nil {
		t.Fatalf("the approved program was refused: %v", err)
	}
	if err := p.check(app, map[string]bool{"muhasebe.exe": true}); ErrorCode(err, "") != CodeAppBlocked {
		t.Fatalf("a recording widened the approval: %v", err)
	}
	if err := p.check("", allowed); ErrorCode(err, "") != CodeAppBlocked {
		t.Fatalf("an unidentified program was accepted: %v", err)
	}

	// Approvals persist and only the person at the computer can add them.
	again, err := LoadPolicy(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := again.Approved(); len(got) != 1 || !strings.EqualFold(got[0], app) {
		t.Fatalf("approvals not kept: %v", got)
	}
	if ok, err := again.Revoke(app); !ok || err != nil {
		t.Fatalf("revoke: %v %v", ok, err)
	}
	if err := again.check(app, allowed); ErrorCode(err, "") != CodeAppNotApproved {
		t.Fatalf("a revoked program was accepted: %v", err)
	}

	shell := filepath.Join(dir, "PowerShell.exe")
	if err := os.WriteFile(shell, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := p.Approve(shell); ErrorCode(err, "") != CodeAppBlocked {
		t.Fatalf("a shell was approved: %v", err)
	}
	if err := p.check(shell, map[string]bool{"powershell.exe": true}); ErrorCode(err, "") != CodeAppBlocked {
		t.Fatalf("a shell was accepted: %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.check(self, map[string]bool{strings.ToLower(filepath.Base(self)): true}); ErrorCode(err, "") != CodeAppBlocked {
		t.Fatalf("the runner itself was accepted: %v", err)
	}
}

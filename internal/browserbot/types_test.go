package browserbot

import (
	"context"
	"strings"
	"testing"
)

func TestNormalizeOrigin(t *testing.T) {
	cases := map[string]string{
		"https://Example.com/a?b=1": "https://example.com",
		"https://example.com:443/":  "https://example.com",
		"http://example.com:80":     "http://example.com",
		"http://127.0.0.1:8080/x":   "http://127.0.0.1:8080",
		"http://[::1]:9000/":        "http://[::1]:9000",
	}
	for in, want := range cases {
		got, err := NormalizeOrigin(in)
		if err != nil || got != want {
			t.Errorf("NormalizeOrigin(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"file:///c:/x", "javascript:alert(1)", "chrome://settings", "https://"} {
		if _, err := NormalizeOrigin(bad); err == nil {
			t.Errorf("NormalizeOrigin(%q) accepted a non-web url", bad)
		}
	}
}

func validRecording() Recording {
	return Recording{
		StartURL:       "https://erp.example.com/login",
		AllowedOrigins: []string{"https://erp.example.com"},
		Steps: []Step{
			{Type: StepNavigate, URL: "https://erp.example.com/login"},
			{Type: StepFill, Selectors: []Selector{{Kind: SelectorCSS, Value: "#user"}}, Value: "ali"},
			{Type: StepPress, Key: "Enter"},
		},
	}
}

func TestValidateRejectsUnsafeRecordings(t *testing.T) {
	if err := (&Recording{}).Validate(); err == nil {
		t.Fatal("empty recording was accepted")
	}
	r := validRecording()
	if err := r.Validate(); err != nil {
		t.Fatalf("valid recording rejected: %v", err)
	}
	mutations := map[string]func(*Recording){
		"other origin":     func(r *Recording) { r.Steps[0].URL = "https://evil.example.net/" },
		"file scheme":      func(r *Recording) { r.Steps[0].URL = "file:///etc/passwd" },
		"unknown type":     func(r *Recording) { r.Steps[1].Type = "evaluate" },
		"no selector":      func(r *Recording) { r.Steps[1].Selectors = nil },
		"bad selector":     func(r *Recording) { r.Steps[1].Selectors[0].Kind = "xpath" },
		"free key":         func(r *Recording) { r.Steps[2].Key = "Control+W" },
		"no allowed list":  func(r *Recording) { r.AllowedOrigins = nil },
		"long value":       func(r *Recording) { r.Steps[1].Value = strings.Repeat("a", MaxValueLength+1) },
		"extract no key":   func(r *Recording) { r.Steps[1].Type = StepExtractText },
		"timeout too long": func(r *Recording) { r.Steps[1].TimeoutMS = MaxStepTimeoutMS + 1 },
	}
	for name, mutate := range mutations {
		r := validRecording()
		mutate(&r)
		if err := r.Validate(); err == nil {
			t.Errorf("%s: recording was accepted", name)
		}
	}
}

func TestSanitizeDropsSensitiveValues(t *testing.T) {
	s, ok := sanitize(Step{Type: StepFill, Sensitive: true, Value: "hunter2", Selectors: []Selector{{Kind: SelectorCSS, Value: "#p"}}})
	if !ok || s.Value != "" {
		t.Fatalf("sensitive value kept: %+v", s)
	}
	if _, ok := sanitize(Step{Type: StepNavigate, URL: "https://x.example"}); ok {
		t.Fatal("the page script must not be able to add navigate steps")
	}
	if _, ok := sanitize(Step{Type: StepClick, Selectors: []Selector{{Kind: "xpath", Value: "//a"}}}); ok {
		t.Fatal("a step without a usable selector was kept")
	}
}

func TestMarkLoginSteps(t *testing.T) {
	steps := []Step{
		{Type: StepNavigate, URL: "https://a.example/login"},
		{Type: StepFill, Value: "ali", PageURL: "https://a.example/login"},
		{Type: StepFill, Sensitive: true, PageURL: "https://a.example/login"},
		{Type: StepClick, PageURL: "https://a.example/login"},
		{Type: StepFill, Value: "x", PageURL: "https://a.example/form"},
	}
	markLoginSteps(steps)
	want := []bool{false, true, true, true, false}
	for i, w := range want {
		if steps[i].Optional != w {
			t.Errorf("step %d optional = %v, want %v", i, steps[i].Optional, w)
		}
	}
}

func TestManagerRunsOneJobAtATime(t *testing.T) {
	m := NewManager(context.Background(), Config{})
	job, err := m.claim("a", JobReplay)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.claim("b", JobRecord); ErrorCode(err, "") != CodeBusy {
		t.Fatalf("second job got %v, want BUSY", err)
	}
	if m.Stop("other") {
		t.Fatal("Stop matched a different job id")
	}
	m.release(job)
	if _, err := m.claim("b", JobRecord); err != nil {
		t.Fatalf("slot was not released: %v", err)
	}
}

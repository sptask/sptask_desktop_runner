package browserbot

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

const testSecret = "s3cret-value"

func newTestSite(t *testing.T, otherOrigin string) *httptest.Server {
	t.Helper()
	page := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<!doctype html><html><body>%s</body></html>", body)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if r.FormValue("user") == "ali" && r.FormValue("pass") == testSecret {
				// A persistent ("remember me") cookie survives the browser restart
				// between recording and replay; a session-only cookie would not.
				http.SetCookie(w, &http.Cookie{Name: "session", Value: "ok", Path: "/", MaxAge: 3600})
				http.Redirect(w, r, "/form", http.StatusSeeOther)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
		}
		page(w, `<form method="post" action="/login">
<label for="u">Kullanıcı adı</label><input id="u" name="user">
<label for="p">Şifre</label><input id="p" name="pass" type="password">
<button type="submit">Giriş</button></form>`)
	})
	mux.HandleFunc("/form", func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("session"); err != nil || c.Value != "ok" {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		page(w, `<form method="post" action="/save">
<label for="c">Müşteri</label><input id="c" name="customer">
<select id="city" name="city"><option value="ist">İstanbul</option><option value="ank">Ankara</option></select>
<button type="submit" data-testid="save">Kaydet</button></form>`)
	})
	mux.HandleFunc("/save", func(w http.ResponseWriter, r *http.Request) {
		page(w, `<p id="result">Kayıt tamam: `+html.EscapeString(r.FormValue("customer"))+` / `+html.EscapeString(r.FormValue("city"))+`</p>`)
	})
	mux.HandleFunc("/away", func(w http.ResponseWriter, r *http.Request) {
		page(w, `<a id="away" href="`+html.EscapeString(otherOrigin)+`/">Dışarı</a>`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func requireBrowser(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("browser test skipped in -short mode")
	}
	if _, err := FindBrowser(""); err != nil {
		t.Skipf("no browser installed: %v", err)
	}
}

func waitCSS(t *testing.T, ctx context.Context, css string) resolveResult {
	t.Helper()
	step := &Step{Selectors: []Selector{{Kind: SelectorCSS, Value: css}}}
	res, err := waitElement(ctx, step, 10*time.Second, resolveActionNone, "", func(r resolveResult) bool { return r.Hit })
	if err != nil {
		t.Fatalf("wait for %s: %v", css, err)
	}
	return res
}

// userClick and userType send trusted input, as a person at the keyboard would.
func userClick(t *testing.T, ctx context.Context, css string) {
	t.Helper()
	res := waitCSS(t, ctx, css)
	if err := chromedp.Do(ctx, chromedp.MouseClickXY(res.X, res.Y)); err != nil {
		t.Fatalf("click %s: %v", css, err)
	}
}

func userType(t *testing.T, ctx context.Context, css, text string) {
	t.Helper()
	userClick(t, ctx, css)
	if _, err := chromedp.Call(ctx, input.InsertText, input.InsertTextParams{Text: text}); err != nil {
		t.Fatalf("type into %s: %v", css, err)
	}
}

func activeTab(t *testing.T, m *Manager) context.Context {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active == nil || m.active.tabCtx == nil {
		t.Fatal("no active recording tab")
	}
	return m.active.tabCtx
}

func record(t *testing.T, m *Manager, startURL string, drive func(ctx context.Context)) Recording {
	t.Helper()
	done := make(chan Recording, 1)
	if err := m.StartRecording("rec-1", startURL, time.Minute, func(r Recording) { done <- r }); err != nil {
		t.Fatalf("start recording: %v", err)
	}
	drive(activeTab(t, m))
	// Give the page time to report the last events before stopping.
	time.Sleep(500 * time.Millisecond)
	if !m.Stop("rec-1") {
		t.Fatal("stop did not find the recording")
	}
	select {
	case r := <-done:
		return r
	case <-time.After(30 * time.Second):
		t.Fatal("recording did not finish")
	}
	return Recording{}
}

func replaySync(t *testing.T, m *Manager, rec Recording) ReplayResult {
	t.Helper()
	done := make(chan ReplayResult, 1)
	if err := m.StartReplay("run-1", rec, ReplayOptions{Headless: true, Timeout: time.Minute}, func(r ReplayResult) { done <- r }); err != nil {
		t.Fatalf("start replay: %v", err)
	}
	select {
	case r := <-done:
		return r
	case <-time.After(90 * time.Second):
		t.Fatal("replay did not finish")
	}
	return ReplayResult{}
}

func TestRecordAndReplayLoginAndForm(t *testing.T) {
	requireBrowser(t)
	site := newTestSite(t, "http://127.0.0.1:1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager(ctx, Config{ProfileDir: t.TempDir(), headlessRecording: true})
	defer m.Wait()

	rec := record(t, m, site.URL+"/form", func(tab context.Context) {
		userType(t, tab, "#u", "ali")
		userType(t, tab, "#p", testSecret)
		userClick(t, tab, "button")
		userType(t, tab, "#c", "ACME")
		userClick(t, tab, "#city")
		if err := chromedp.Do(tab, chromedp.KeyEvent(kb.ArrowDown), chromedp.KeyEvent(kb.Enter)); err != nil {
			t.Fatalf("choose city: %v", err)
		}
		userClick(t, tab, `[data-testid="save"]`)
		waitCSS(t, tab, "#result")
	})

	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal recording: %v", err)
	}
	if strings.Contains(string(raw), testSecret) {
		t.Fatalf("the password left the machine: %s", raw)
	}
	var types []string
	for _, s := range rec.Steps {
		types = append(types, s.Type)
	}
	t.Logf("recorded steps: %v", types)
	t.Logf("recording: %s", raw)
	if err := rec.Validate(); err != nil {
		t.Fatalf("recording is not valid: %v", err)
	}

	findFill := func(value string) int {
		for i, s := range rec.Steps {
			if s.Type == StepFill && s.Value == value {
				return i
			}
		}
		return -1
	}
	passIdx := -1
	for i, s := range rec.Steps {
		if s.Type == StepFill && s.Sensitive {
			passIdx = i
		}
	}
	if passIdx < 0 || !rec.Steps[passIdx].Optional {
		t.Fatalf("expected an optional sensitive password step, got %+v", rec.Steps)
	}
	if i := findFill("ali"); i < 0 || !rec.Steps[i].Optional {
		t.Fatalf("expected the user name step to be optional")
	}
	customer := findFill("ACME")
	if customer < 0 {
		t.Fatalf("customer fill was not recorded")
	}
	selIdx := -1
	for i, s := range rec.Steps {
		if s.Type == StepSelect {
			selIdx = i
		}
	}
	if selIdx < 0 || rec.Steps[selIdx].Value != "ank" {
		t.Fatalf("expected select of ank, got %+v", rec.Steps)
	}

	// Replay with another customer. The session cookie from the recording is
	// still in the profile, so the login steps are skipped.
	rec.Steps[customer].Value = "Beta & Co"
	rec.Steps = append(rec.Steps, Step{Type: StepExtractText, Selectors: []Selector{{Kind: SelectorCSS, Value: "#result"}}, OutputKey: "result"})
	res := replaySync(t, m, rec)
	if !res.Success {
		t.Fatalf("replay failed: %s %s (step %s)", res.ErrorCode, res.Error, stepNo(res.FailedStep))
	}
	if got := res.Outputs["result"]; got != "Kayıt tamam: Beta & Co / ank" {
		t.Fatalf("unexpected result %q", got)
	}
	if len(res.SkippedSteps) == 0 {
		t.Fatalf("expected login steps to be skipped with a valid session")
	}

	// A fresh profile has no session: the replay must stop at the login form.
	fresh := NewManager(ctx, Config{ProfileDir: t.TempDir()})
	defer fresh.Wait()
	res = replaySync(t, fresh, rec)
	if res.Success || res.ErrorCode != CodeLoginRequired {
		t.Fatalf("expected LOGIN_REQUIRED, got %s %s (step %s)", res.ErrorCode, res.Error, stepNo(res.FailedStep))
	}
	if res.Screenshot == "" {
		t.Fatalf("expected a screenshot of the failed step")
	}
}

func TestReplayBlocksOtherOrigins(t *testing.T) {
	requireBrowser(t)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<p id=other>other site</p>")
	}))
	defer other.Close()
	site := newTestSite(t, other.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager(ctx, Config{ProfileDir: t.TempDir()})
	defer m.Wait()

	rec := Recording{
		ID:             "guard",
		StartURL:       site.URL + "/away",
		AllowedOrigins: []string{site.URL},
		Steps: []Step{
			{Type: StepNavigate, URL: site.URL + "/away"},
			{Type: StepClick, Selectors: []Selector{{Kind: SelectorCSS, Value: "#away"}}},
			{Type: StepWaitFor, Selectors: []Selector{{Kind: SelectorCSS, Value: "#other"}}, TimeoutMS: 3000},
		},
	}
	res := replaySync(t, m, rec)
	if res.Success || res.ErrorCode != CodeOriginBlocked {
		t.Fatalf("expected ORIGIN_BLOCKED, got %s %s (step %s)", res.ErrorCode, res.Error, stepNo(res.FailedStep))
	}

	rec.Steps = []Step{{Type: StepNavigate, URL: other.URL + "/"}}
	if err := m.StartReplay("bad", rec, ReplayOptions{Headless: true}, func(ReplayResult) {}); ErrorCode(err, "") != CodeInvalidRecording {
		t.Fatalf("expected a navigate step outside the allowed origins to be rejected, got %v", err)
	}
}

// The browser exiting, as it does when the user closes its last window, finishes the
// recording with the steps made so far.
func TestRecordingEndsWhenTheBrowserQuits(t *testing.T) {
	requireBrowser(t)
	site := newTestSite(t, "http://127.0.0.1:1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager(ctx, Config{ProfileDir: t.TempDir(), headlessRecording: true})
	defer m.Wait()
	done := make(chan Recording, 1)
	if err := m.StartRecording("quit", site.URL+"/login", time.Minute, func(r Recording) { done <- r }); err != nil {
		t.Fatalf("start recording: %v", err)
	}
	tab := activeTab(t, m)
	userType(t, tab, "#u", "ali")
	userClick(t, tab, "button")
	time.Sleep(500 * time.Millisecond)
	if err := chromedp.FromContext(tab).Browser.Process().Kill(); err != nil {
		t.Fatalf("kill browser: %v", err)
	}
	select {
	case r := <-done:
		if len(r.Steps) < 3 {
			t.Fatalf("expected the steps made before quitting, got %+v", r.Steps)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the recording did not finish after the browser quit")
	}
}

func stepNo(i *int) string {
	if i == nil {
		return "none"
	}
	return fmt.Sprint(*i)
}

package browserbot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/chromedp"

	"github.com/sptask/sptask_desktop_runner/internal/operator"
)

// The operator overlay must not change what the bot does: clicks pass through
// it, text steps do not see it, and only the user's Shift+Esc stops the run.
func TestOperatorOverlayStaysOutOfTheWay(t *testing.T) {
	requireBrowser(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// A strict policy: no inline style or script from anyone but the page.
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'")
		if _, err := w.Write([]byte(`<!doctype html><html><body><button id="b">Onayla</button><p id="out"></p><script src="/app.js"></script></body></html>`)); err != nil {
			t.Errorf("write page: %v", err)
		}
	})
	mux.HandleFunc("/app.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript")
		if _, err := w.Write([]byte(`document.getElementById('b').addEventListener('click', () => { document.getElementById('out').textContent = 'tıklandı'; });`)); err != nil {
			t.Errorf("write script: %v", err)
		}
	})
	site := httptest.NewServer(mux)
	defer site.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tab, closeBrowser, err := newBrowser(ctx, Config{ProfileDir: t.TempDir()}, true)
	if err != nil {
		t.Fatalf("start browser: %v", err)
	}
	defer func() {
		if err := closeBrowser(); err != nil {
			t.Errorf("close browser: %v", err)
		}
	}()
	guard, err := newOriginGuard([]string{site.URL})
	if err != nil {
		t.Fatalf("guard: %v", err)
	}
	if err := guard.install(tab); err != nil {
		t.Fatalf("install guard: %v", err)
	}
	defer guard.wait()
	nav := newNavWatcher()
	if err := nav.install(tab); err != nil {
		t.Fatalf("install watcher: %v", err)
	}
	var stops atomic.Int32
	if err := installStopKey(tab, func() { stops.Add(1) }); err != nil {
		t.Fatalf("install stop key: %v", err)
	}

	show := newOverlay(operator.Operator{Name: "Ada (AI)", Color: "#0e7490"})
	rec := &Recording{Steps: []Step{
		{Type: StepNavigate, URL: site.URL + "/"},
		{Type: StepClick, Label: "Onayla", Selectors: []Selector{{Kind: SelectorCSS, Value: "#b"}}},
		{Type: StepAssertText, Value: "tıklandı", Selectors: []Selector{{Kind: SelectorCSS, Value: "body"}}},
		{Type: StepExtractText, OutputKey: "body", Selectors: []Selector{{Kind: SelectorCSS, Value: "body"}}},
	}}
	res := replay(tab, rec, guard, nav, show)
	if !res.Success {
		t.Fatalf("replay failed: %s %s (step %s)", res.ErrorCode, res.Error, stepNo(res.FailedStep))
	}
	if len(res.Warnings) > 0 {
		t.Fatalf("the overlay was not drawn: %v", res.Warnings)
	}
	if got := res.Outputs["body"]; got != "Onayla\ntıklandı" && got != "Onayla tıklandı" {
		t.Fatalf("the page text includes something else: %q", got)
	}
	drawn, err := chromedp.Run(tab, chromedp.Evaluate[bool](`document.querySelector('spartask-operator') !== null`))
	if err != nil || !drawn {
		t.Fatalf("expected the overlay on the page (err %v)", err)
	}

	// The bot's own Escape does not stop the run; the user's Shift+Esc does.
	const shift = 8 // CDP modifier bit for Shift
	press := func(mods int64) {
		t.Helper()
		for _, typ := range []input.DispatchKeyEventType{input.DispatchKeyEventTypeKeyDown, input.DispatchKeyEventTypeKeyUp} {
			p := input.DispatchKeyEventParams{Type: typ, Key: "Escape", Code: "Escape", WindowsVirtualKeyCode: 27, Modifiers: mods}
			if _, err := chromedp.Call(tab, input.DispatchKeyEvent, p); err != nil {
				t.Fatalf("press escape: %v", err)
			}
		}
	}
	press(0)
	press(shift)
	deadline := time.Now().Add(5 * time.Second)
	for stops.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := stops.Load(); n != 1 {
		t.Fatalf("expected exactly one stop from Shift+Esc, got %d", n)
	}
}

package browserbot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestStepAfterNavigationWaitsForTheNewPage replays a click that loads a slow
// page, followed by a click whose only matching selector is a structural path
// that the old page also has. The second click must land on the new page.
func TestStepAfterNavigationWaitsForTheNewPage(t *testing.T) {
	requireBrowser(t)
	var nextLoads, finishes atomic.Int32
	mux := http.NewServeMux()
	page := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if _, err := w.Write([]byte("<!doctype html><html><body>" + body + "</body></html>")); err != nil {
			t.Errorf("write page: %v", err)
		}
	}
	mux.HandleFunc("/first", func(w http.ResponseWriter, r *http.Request) {
		page(w, `<form method="get" action="/next"><button>İleri</button></form>`)
	})
	mux.HandleFunc("/next", func(w http.ResponseWriter, r *http.Request) {
		nextLoads.Add(1)
		time.Sleep(300 * time.Millisecond)
		page(w, `<form method="post" action="/finish"><button>Bitir</button></form>`)
	})
	mux.HandleFunc("/finish", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			finishes.Add(1)
		}
		page(w, `<p id="finished">Bitti</p>`)
	})
	site := httptest.NewServer(mux)
	defer site.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager(ctx, Config{ProfileDir: t.TempDir()})
	defer m.Wait()

	samePath := Selector{Kind: SelectorCSS, Value: "body > form > button"}
	rec := Recording{
		ID:             "nav-1",
		StartURL:       site.URL + "/first",
		AllowedOrigins: []string{site.URL},
		Steps: []Step{
			{Type: StepNavigate, URL: site.URL + "/first"},
			{Type: StepClick, Selectors: []Selector{{Kind: SelectorText, Value: "İleri", Tag: "button"}}},
			{Type: StepClick, Selectors: []Selector{{Kind: SelectorCSS, Value: "#only-on-next"}, samePath}},
			{Type: StepWaitFor, Selectors: []Selector{{Kind: SelectorCSS, Value: "#finished"}}},
		},
	}
	res := replaySync(t, m, rec)
	if !res.Success {
		t.Fatalf("replay failed: %s %s (step %s)", res.ErrorCode, res.Error, stepNo(res.FailedStep))
	}
	if n := nextLoads.Load(); n != 1 {
		t.Fatalf("expected the next page to load once, got %d", n)
	}
	if n := finishes.Load(); n != 1 {
		t.Fatalf("expected exactly one finish post, got %d", n)
	}
}

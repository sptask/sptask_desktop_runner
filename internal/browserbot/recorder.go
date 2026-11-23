package browserbot

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

//go:embed recorder.js
var recorderSource string

const recorderBinding = "__sptRecord"

// Navigations the user started from the address bar or a bookmark become
// navigate steps; navigations caused by clicks and form posts do not, because
// the click or press that caused them is already a step.
var userNavigation = map[page.TransitionType]bool{
	page.TransitionTypeTyped:            true,
	page.TransitionTypeAddressBar:       true,
	page.TransitionTypeAutoBookmark:     true,
	page.TransitionTypeGenerated:        true,
	page.TransitionTypeKeyword:          true,
	page.TransitionTypeKeywordGenerated: true,
}

type seqStep struct {
	seq  int
	step Step
}

// recorder collects steps from the injected script and from navigations.
type recorder struct {
	mu       sync.Mutex
	seq      int
	steps    []seqStep
	warnings []string
	skipNav  bool
	work     sync.WaitGroup
}

func (r *recorder) nextSeq() int {
	r.seq++
	return r.seq
}

func (r *recorder) add(seq int, s Step) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.steps) >= MaxSteps {
		if len(r.warnings) == 0 || r.warnings[len(r.warnings)-1] != "step limit reached" {
			r.warnings = append(r.warnings, "step limit reached")
		}
		return
	}
	r.steps = append(r.steps, seqStep{seq: seq, step: s})
}

func (r *recorder) warn(msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.warnings = append(r.warnings, msg)
}

// sanitize enforces on this machine what the script promised: sensitive
// values never leave it, and every field stays inside the limits.
func sanitize(s Step) (Step, bool) {
	switch s.Type {
	case StepClick, StepFill, StepSelect, StepPress:
	default:
		return Step{}, false
	}
	if s.Type == StepPress && !allowedKeys[s.Key] {
		return Step{}, false
	}
	if s.Sensitive {
		s.Value = ""
	}
	if len(s.Value) > MaxValueLength {
		s.Value = s.Value[:MaxValueLength]
	}
	kept := s.Selectors[:0]
	for _, sel := range s.Selectors {
		if sel.Value == "" || len(sel.Value) > MaxSelectorLength {
			continue
		}
		switch sel.Kind {
		case SelectorCSS, SelectorLabel, SelectorText, SelectorPath:
			kept = append(kept, sel)
		}
		if len(kept) == MaxSelectorsPerStep {
			break
		}
	}
	if len(kept) == 0 {
		return Step{}, false
	}
	s.Selectors = kept
	s.URL, s.OutputKey, s.TimeoutMS, s.Optional = "", "", 0, false
	return s, true
}

func (r *recorder) onBinding(ev *runtime.EventBindingCalled) {
	if ev.Name != recorderBinding {
		return
	}
	var s Step
	if err := json.Unmarshal([]byte(ev.Payload), &s); err != nil {
		r.warn(fmt.Sprintf("ignored an unreadable recorder message: %v", err))
		return
	}
	clean, ok := sanitize(s)
	if !ok {
		return
	}
	r.mu.Lock()
	seq := r.nextSeq()
	r.mu.Unlock()
	r.add(seq, clean)
}

// onNavigated runs in the listener; the history lookup must run elsewhere.
func (r *recorder) onNavigated(ctx context.Context, ev *page.EventFrameNavigated) {
	if ev.Frame == nil || ev.Frame.ParentID != cdp.FrameID("") {
		return
	}
	frameURL := ev.Frame.URL
	if _, err := NormalizeOrigin(frameURL); err != nil {
		return // about:blank, error pages and other non-web documents
	}
	r.mu.Lock()
	if r.skipNav {
		// The runner's own navigation to the start URL is already step 0.
		r.skipNav = false
		r.mu.Unlock()
		return
	}
	seq := r.nextSeq()
	r.mu.Unlock()
	r.work.Add(1)
	go func() {
		defer r.work.Done()
		hist, err := navigationHistory(ctx)
		if err != nil {
			if ctx.Err() == nil {
				r.warn(fmt.Sprintf("could not classify navigation to %s: %v", frameURL, err))
			}
			return
		}
		if hist.CurrentIndex < 0 || int(hist.CurrentIndex) >= len(hist.Entries) {
			return
		}
		if userNavigation[hist.Entries[hist.CurrentIndex].TransitionType] {
			r.add(seq, Step{Type: StepNavigate, URL: frameURL})
		}
	}()
}

const (
	historyAttempts = 10
	historyRetry    = 100 * time.Millisecond
)

// navigationHistory retries for a moment: right after a navigation commits,
// Chrome may answer that the page is not attached yet.
func navigationHistory(ctx context.Context) (page.GetNavigationHistoryResult, error) {
	var lastErr error
	for range historyAttempts {
		hist, err := chromedp.Call(ctx, page.GetNavigationHistory, cdp.Empty{})
		if err == nil {
			return hist, nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return page.GetNavigationHistoryResult{}, ctx.Err()
		case <-time.After(historyRetry):
		}
	}
	return page.GetNavigationHistoryResult{}, lastErr
}

// markLoginSteps makes every step on a page that has a sensitive field
// optional, so a replay with a still-valid session skips the login form.
func markLoginSteps(steps []Step) {
	loginPages := map[string]bool{}
	for _, s := range steps {
		if s.Sensitive && s.PageURL != "" {
			loginPages[s.PageURL] = true
		}
	}
	for i := range steps {
		if steps[i].Type != StepNavigate && loginPages[steps[i].PageURL] {
			steps[i].Optional = true
		}
	}
}

func (r *recorder) result(id, startURL string, origins []string) Recording {
	r.work.Wait()
	r.mu.Lock()
	defer r.mu.Unlock()
	sort.SliceStable(r.steps, func(i, j int) bool { return r.steps[i].seq < r.steps[j].seq })
	steps := make([]Step, 0, len(r.steps)+1)
	steps = append(steps, Step{Type: StepNavigate, URL: startURL})
	for _, s := range r.steps {
		steps = append(steps, s.step)
	}
	markLoginSteps(steps)
	return Recording{
		ID:             id,
		StartURL:       startURL,
		Steps:          steps,
		AllowedOrigins: origins,
		Warnings:       append([]string(nil), r.warnings...),
	}
}

// install wires the recorder into the tab before the first navigation.
func (r *recorder) install(ctx context.Context) error {
	err := chromedp.Listen(ctx, func(ev any) {
		switch e := ev.(type) {
		case *runtime.EventBindingCalled:
			r.onBinding(e)
		case *page.EventFrameNavigated:
			r.onNavigated(ctx, e)
		}
	})
	if err != nil {
		return botErr(CodeBrowserError, "listen for recorder events: %w", err)
	}
	if _, err := chromedp.Call(ctx, runtime.AddBinding, runtime.AddBindingParams{Name: recorderBinding}); err != nil {
		return botErr(CodeBrowserError, "expose recorder binding: %w", err)
	}
	if _, err := chromedp.Call(ctx, page.Enable, page.EnableParams{}); err != nil {
		return botErr(CodeBrowserError, "enable page events: %w", err)
	}
	if _, err := chromedp.Call(ctx, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{Source: recorderSource}); err != nil {
		return botErr(CodeBrowserError, "inject recorder: %w", err)
	}
	return nil
}

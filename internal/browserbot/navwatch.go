package browserbot

import (
	"context"
	"sync"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// navigationStartWindow is how long an action is given to start loading a new
// document before the player moves on. A click that submits a form or follows
// a link starts loading well inside it.
const navigationStartWindow = 400 * time.Millisecond

// navWatcher follows the main frame's loading, so a step after a click that
// changes page never runs against the old document. Without it a structural
// selector of the next step could match an element of the page being left.
type navWatcher struct {
	mainFrameID string

	mu      sync.Mutex
	starts  int
	loading bool
	changed chan struct{} // closed and replaced on every change
}

func newNavWatcher() *navWatcher {
	return &navWatcher{changed: make(chan struct{})}
}

func (w *navWatcher) install(ctx context.Context) error {
	c := chromedp.FromContext(ctx)
	if c == nil || c.Target == nil {
		return botErr(CodeBrowserError, "no browser target in context")
	}
	w.mainFrameID = string(c.Target.TargetID)
	err := chromedp.Listen(ctx, func(ev any) {
		switch e := ev.(type) {
		case *page.EventFrameStartedLoading:
			if string(e.FrameID) == w.mainFrameID {
				w.set(true)
			}
		case *page.EventFrameStoppedLoading:
			if string(e.FrameID) == w.mainFrameID {
				w.set(false)
			}
		}
	})
	if err != nil {
		return botErr(CodeBrowserError, "listen for page loads: %w", err)
	}
	return nil
}

func (w *navWatcher) set(loading bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if loading {
		w.starts++
	}
	w.loading = loading
	close(w.changed)
	w.changed = make(chan struct{})
}

func (w *navWatcher) state() (starts int, loading bool, changed <-chan struct{}) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.starts, w.loading, w.changed
}

// mark is taken right before an action that may change page.
func (w *navWatcher) mark() int {
	starts, _, _ := w.state()
	return starts
}

// settle waits until a page load the action started has finished. It returns
// at once when no load started within navigationStartWindow.
func (w *navWatcher) settle(ctx context.Context, before int, wait time.Duration) error {
	startBy := time.NewTimer(navigationStartWindow)
	defer startBy.Stop()
	done := time.NewTimer(wait)
	defer done.Stop()
	for {
		starts, loading, changed := w.state()
		started := starts != before
		if started && !loading {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		case <-startBy.C:
			if !started {
				return nil
			}
		case <-done.C:
			return botErr(CodeTimeout, "the next page did not finish loading")
		}
	}
}

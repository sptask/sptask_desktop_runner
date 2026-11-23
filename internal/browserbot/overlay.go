package browserbot

import (
	"context"
	_ "embed"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"github.com/sptask/sptask_desktop_runner/internal/operator"
)

//go:embed overlay.js
var overlaySource string

var overlayFn = stripLeadingComments(overlaySource)

const (
	// cursorTravel lets the drawn cursor reach its target before the bot acts,
	// so the person at the computer can follow what happens.
	cursorTravel = 400 * time.Millisecond
	// maxOverlayLabel caps the element label shown next to the action.
	maxOverlayLabel = 60
	// stopBinding is the page function the overlay calls on Shift+Esc.
	stopBinding = "__sptStop"
	// noPosition keeps the cursor where it is.
	noPosition = -1.0
)

// overlay draws the operator over a visible replay. A nil overlay (headless
// replays) draws nothing. Drawing never fails a step: the first error is kept
// and reported as a warning of the run.
type overlay struct {
	op      operator.Operator
	drawErr error
}

func newOverlay(op operator.Operator) *overlay {
	return &overlay{op: op.Normalized()}
}

func (o *overlay) draw(ctx context.Context, label string, x, y float64) {
	if o == nil {
		return
	}
	if _, err := callOnPage[bool](ctx, overlayFn, o.op.Name, o.op.Color, label, operator.StopHint, x, y); err != nil && o.drawErr == nil && ctx.Err() == nil {
		o.drawErr = err
	}
}

// pointAt moves the cursor to the element and waits until it arrives.
func (o *overlay) pointAt(ctx context.Context, label string, x, y float64) error {
	if o == nil {
		return nil
	}
	o.draw(ctx, label, x, y)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(cursorTravel):
		return nil
	}
}

func (o *overlay) warnings() []string {
	if o == nil || o.drawErr == nil {
		return nil
	}
	return []string{fmt.Sprintf("could not draw the operator cursor: %v", o.drawErr)}
}

// stepLabel says in the run's language what the bot is doing. It never shows
// the value being typed: it may be personal data.
func stepLabel(step *Step) string {
	name := step.Label
	if utf8.RuneCountInString(name) > maxOverlayLabel {
		name = string([]rune(name)[:maxOverlayLabel]) + "…"
	}
	with := func(verb string) string {
		if name == "" {
			return verb
		}
		return verb + ": " + name
	}
	switch step.Type {
	case StepNavigate:
		return "Sayfayı açıyor"
	case StepClick:
		return with("Tıklıyor")
	case StepFill:
		return with("Yazıyor")
	case StepSelect:
		return with("Seçiyor")
	case StepPress:
		return "Tuşa basıyor: " + step.Key
	case StepWaitFor:
		return with("Bekliyor")
	case StepAssertText:
		return with("Kontrol ediyor")
	case StepExtractText:
		return with("Okuyor")
	}
	return ""
}

// installStopKey calls stop when the overlay reports Shift+Esc. The binding
// survives navigations; the listener only records the request, because CDP
// calls must not run inside it.
func installStopKey(ctx context.Context, stop func()) error {
	err := chromedp.Listen(ctx, func(ev any) {
		if e, ok := ev.(*runtime.EventBindingCalled); ok && e.Name == stopBinding {
			stop()
		}
	})
	if err != nil {
		return botErr(CodeBrowserError, "listen for the stop key: %w", err)
	}
	if _, err := chromedp.Call(ctx, runtime.AddBinding, runtime.AddBindingParams{Name: stopBinding}); err != nil {
		return botErr(CodeBrowserError, "expose the stop key: %w", err)
	}
	return nil
}

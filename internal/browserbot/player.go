package browserbot

import (
	"context"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"
)

//go:embed resolver.js
var resolverSource string

// resolverFn is resolver.js without its leading comment lines, so the
// browser receives a bare function expression.
var resolverFn = stripLeadingComments(resolverSource)

const (
	resolverObjectGroup = "spartask-resolver"
	pollInterval        = 150 * time.Millisecond
	screenshotMaxBytes  = 256 * 1024
	screenshotTimeout   = 5 * time.Second
	resolveActionNone   = "none"
	resolveActionFocus  = "focus"
	resolveActionBlur   = "blur"
	resolveActionSelect = "select"
	resolveActionText   = "text"
)

var pressKeys = map[string]string{"Enter": kb.Enter, "Tab": kb.Tab, "Escape": kb.Escape}

var errStepSkipped = errors.New("optional step skipped")

func stripLeadingComments(src string) string {
	lines := strings.Split(src, "\n")
	i := 0
	for i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "//") {
		i++
	}
	return strings.TrimSpace(strings.Join(lines[i:], "\n"))
}

type resolveResult struct {
	Found           bool    `json:"found"`
	Index           int     `json:"index"`
	X               float64 `json:"x"`
	Y               float64 `json:"y"`
	Hit             bool    `json:"hit"`
	Disabled        bool    `json:"disabled"`
	OK              bool    `json:"ok"`
	Text            string  `json:"text"`
	PasswordVisible bool    `json:"password_visible"`
	PageURL         string  `json:"page_url"`
}

// resolveOnce calls the resolver on the page's global object. The selectors
// and the value travel as call arguments, so page data never becomes script.
func resolveOnce(ctx context.Context, sels []Selector, action, value string) (resolveResult, error) {
	return chromedp.Run(ctx, func(ctx context.Context, t *chromedp.Target) (resolveResult, error) {
		global, err := cdp.Call(ctx, t, runtime.Evaluate, runtime.EvaluateParams{Expression: "globalThis", ObjectGroup: resolverObjectGroup})
		if err != nil {
			return resolveResult{}, fmt.Errorf("get page global: %w", err)
		}
		if global.Result == nil || global.Result.ObjectID == "" {
			return resolveResult{}, errors.New("page global has no object id")
		}
		onGlobal := func(p *runtime.CallFunctionOnParams) { p.ObjectID = global.Result.ObjectID }
		res, callErr := chromedp.CallFunctionOn[resolveResult](resolverFn, onGlobal, sels, action, value)(ctx, t)
		_, releaseErr := cdp.Call(ctx, t, runtime.ReleaseObjectGroup, runtime.ReleaseObjectGroupParams{ObjectGroup: resolverObjectGroup})
		if callErr != nil {
			return resolveResult{}, callErr
		}
		if releaseErr != nil {
			return resolveResult{}, fmt.Errorf("release page global: %w", releaseErr)
		}
		return res, nil
	})
}

// waitElement polls until the step's element is usable or the wait ends.
// accept decides whether a found element is ready.
func waitElement(ctx context.Context, step *Step, wait time.Duration, action, value string, accept func(resolveResult) bool) (resolveResult, error) {
	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	var last resolveResult
	var lastErr error
	for {
		res, err := resolveOnce(waitCtx, step.Selectors, action, value)
		if err == nil {
			if res.Found && pathMatchElsewhere(step, res) {
				res.Found = false
			}
			last, lastErr = res, nil
			if res.Found && accept(res) {
				return res, nil
			}
		} else {
			// The page may be between documents; keep polling until the wait ends.
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-waitCtx.Done():
			if last.PasswordVisible {
				return last, botErr(CodeLoginRequired, "the page shows a login form instead of the expected element")
			}
			if last.Found {
				return last, botErr(CodeSelectorNotFound, "the element was found but stayed covered or disabled")
			}
			if lastErr != nil {
				return last, botErr(CodeSelectorNotFound, "the element was not found: %w", lastErr)
			}
			return last, botErr(CodeSelectorNotFound, "the element was not found")
		case <-time.After(pollInterval):
		}
	}
}

func anyFound(resolveResult) bool { return true }

// pathMatchElsewhere reports a match by the structural path on a page other
// than the one the step was recorded on; such a match is not trusted.
func pathMatchElsewhere(step *Step, res resolveResult) bool {
	if step.PageURL == "" || res.Index < 0 || res.Index >= len(step.Selectors) {
		return false
	}
	return step.Selectors[res.Index].Kind == SelectorPath && res.PageURL != step.PageURL
}

type player struct {
	guard   *originGuard
	nav     *navWatcher
	outputs map[string]string
}

// pageAction runs an action that may load another page and waits for that
// page, so the next step never looks at the document being left.
func (p *player) pageAction(ctx context.Context, wait time.Duration, action func() error) error {
	before := p.nav.mark()
	if err := action(); err != nil {
		return err
	}
	return p.nav.settle(ctx, before, wait)
}

func (p *player) execStep(ctx context.Context, step *Step) error {
	wait := time.Duration(step.timeoutMS()) * time.Millisecond
	probe := step.Optional || (step.Sensitive && step.Value == "")
	if probe && requiresElement(step.Type) {
		// An optional step runs only on the page it was recorded on: a
		// structural selector from the login page can match another page.
		onRecordedPage := func(r resolveResult) bool { return step.PageURL == "" || r.PageURL == step.PageURL }
		_, err := waitElement(ctx, step, optionalStepProbeMS*time.Millisecond, resolveActionNone, "", onRecordedPage)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errStepSkipped
		}
		if step.Sensitive && step.Value == "" {
			return botErr(CodeLoginRequired, "the saved browser session has expired; sign in again by recording or opening the runner browser")
		}
	}

	switch step.Type {
	case StepNavigate:
		navCtx, cancel := context.WithTimeout(ctx, wait)
		defer cancel()
		if err := chromedp.Do(navCtx, chromedp.Navigate(step.URL)); err != nil {
			if blocked := p.guard.blockedOrigin(); blocked != "" {
				return botErr(CodeOriginBlocked, "navigation to %s is not allowed", blocked)
			}
			return botErr(CodeBrowserError, "navigate to %s: %w", step.URL, err)
		}
		return nil

	case StepClick:
		res, err := waitElement(ctx, step, wait, resolveActionNone, "", func(r resolveResult) bool { return r.Hit && !r.Disabled })
		if err != nil {
			return err
		}
		return p.pageAction(ctx, wait, func() error { return chromedp.Do(ctx, chromedp.MouseClickXY(res.X, res.Y)) })

	case StepFill:
		if _, err := waitElement(ctx, step, wait, resolveActionFocus, "", func(r resolveResult) bool { return !r.Disabled }); err != nil {
			return err
		}
		if step.Value == "" {
			if err := chromedp.Do(ctx, chromedp.KeyEvent(kb.Backspace)); err != nil {
				return fmt.Errorf("clear field: %w", err)
			}
		} else if _, err := chromedp.Call(ctx, input.InsertText, input.InsertTextParams{Text: step.Value}); err != nil {
			return fmt.Errorf("type value: %w", err)
		}
		// Blur commits the value so the page fires its change handlers. It
		// polls like any lookup: the page may swap its JavaScript context.
		if _, err := waitElement(ctx, step, wait, resolveActionBlur, "", anyFound); err != nil {
			return fmt.Errorf("commit value: %w", err)
		}
		return nil

	case StepSelect:
		// A change handler may load another page.
		before := p.nav.mark()
		res, err := waitElement(ctx, step, wait, resolveActionSelect, step.Value, anyFound)
		if err != nil {
			return err
		}
		if !res.OK {
			return botErr(CodeSelectorNotFound, "option %q was not found in the list", step.Value)
		}
		return p.nav.settle(ctx, before, wait)

	case StepPress:
		if len(step.Selectors) > 0 {
			if _, err := waitElement(ctx, step, wait, resolveActionFocus, "", anyFound); err != nil {
				return err
			}
		}
		return p.pageAction(ctx, wait, func() error { return chromedp.Do(ctx, chromedp.KeyEvent(pressKeys[step.Key])) })

	case StepWaitFor:
		_, err := waitElement(ctx, step, wait, resolveActionNone, "", anyFound)
		return err

	case StepAssertText:
		_, err := waitElement(ctx, step, wait, resolveActionText, "", func(r resolveResult) bool {
			return strings.Contains(r.Text, step.Value)
		})
		if err != nil && ErrorCode(err, "") == CodeSelectorNotFound {
			return botErr(CodeAssertionFailed, "the text %q did not appear", step.Value)
		}
		return err

	case StepExtractText:
		res, err := waitElement(ctx, step, wait, resolveActionText, "", anyFound)
		if err != nil {
			return err
		}
		p.outputs[step.OutputKey] = res.Text
		return nil
	}
	return botErr(CodeInvalidRecording, "unknown step type %q", step.Type)
}

// captureScreenshot returns a base64 JPEG no larger than screenshotMaxBytes,
// or an empty string when the page cannot be captured within that size.
func captureScreenshot(ctx context.Context) string {
	shotCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), screenshotTimeout)
	defer cancel()
	for _, quality := range []int64{50, 25} {
		q := quality
		res, err := chromedp.Call(shotCtx, page.CaptureScreenshot, page.CaptureScreenshotParams{Format: page.CaptureScreenshotFormatJpeg, Quality: &q})
		if err != nil {
			return ""
		}
		if len(res.Data) <= screenshotMaxBytes {
			return base64.StdEncoding.EncodeToString(res.Data)
		}
	}
	return ""
}

// replay runs a validated recording in the tab of ctx.
func replay(ctx context.Context, rec *Recording, guard *originGuard, nav *navWatcher) ReplayResult {
	p := &player{guard: guard, nav: nav, outputs: map[string]string{}}
	result := ReplayResult{Outputs: p.outputs}
	for i := range rec.Steps {
		step := &rec.Steps[i]
		err := p.execStep(ctx, step)
		// A blocked navigation explains a later failure better than the
		// missing element it leaves behind, so it wins.
		if blocked := guard.blockedOrigin(); blocked != "" {
			err = botErr(CodeOriginBlocked, "navigation to %s is not allowed", blocked)
		} else if ierr := guard.interceptionError(); ierr != nil && err == nil {
			err = botErr(CodeBrowserError, "%w", ierr)
		}
		if errors.Is(err, errStepSkipped) {
			result.SkippedSteps = append(result.SkippedSteps, i)
			continue
		}
		if err != nil {
			failed := i
			result.FailedStep = &failed
			result.ErrorCode = ErrorCode(err, CodeBrowserError)
			switch {
			case errors.Is(err, context.Canceled):
				result.ErrorCode = CodeCancelled
			case errors.Is(err, context.DeadlineExceeded):
				result.ErrorCode = CodeTimeout
			}
			result.Error = err.Error()
			result.Screenshot = captureScreenshot(ctx)
			return result
		}
		result.StepsExecuted++
	}
	result.Success = true
	return result
}

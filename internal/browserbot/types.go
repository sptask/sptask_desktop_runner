// Package browserbot records a user's work in a real browser as a declarative
// list of steps and replays that list deterministically. Recordings are data,
// never code: the player only interprets the step types allowed below.
package browserbot

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Step types the player understands. There is deliberately no step that runs
// arbitrary script in the page.
const (
	StepNavigate    = "navigate"
	StepClick       = "click"
	StepFill        = "fill"
	StepSelect      = "select"
	StepPress       = "press"
	StepWaitFor     = "wait_for"
	StepAssertText  = "assert_text"
	StepExtractText = "extract_text"
)

// Selector kinds produced by the recorder and resolved by the player.
const (
	SelectorCSS   = "css"
	SelectorLabel = "label"
	SelectorText  = "text"
	// SelectorPath is the element's structural CSS path. It is the last resort:
	// another page can have an element at the same path, so it only counts on
	// the page the step was recorded on (Step.PageURL).
	SelectorPath = "path"
)

// Error codes returned to the server. They let a process branch on the cause.
const (
	CodeBusy               = "BUSY"
	CodeBrowserUnavailable = "BROWSER_UNAVAILABLE"
	CodeOriginBlocked      = "ORIGIN_BLOCKED"
	CodeSelectorNotFound   = "SELECTOR_NOT_FOUND"
	CodeAssertionFailed    = "ASSERTION_FAILED"
	CodeLoginRequired      = "LOGIN_REQUIRED"
	CodeTimeout            = "TIMEOUT"
	CodeCancelled          = "CANCELLED"
	// CodeStoppedByUser: the person at the computer pressed Shift+Esc.
	CodeStoppedByUser    = "STOPPED_BY_USER"
	CodeInvalidRecording = "INVALID_RECORDING"
	CodeBrowserError     = "BROWSER_ERROR"
)

// Limits keep a recording small enough for one WebSocket/NATS message.
const (
	MaxSteps             = 500
	MaxSelectorsPerStep  = 6
	MaxSelectorLength    = 500
	MaxValueLength       = 4000
	MaxAllowedOrigins    = 20
	DefaultStepTimeoutMS = 15000
	MaxStepTimeoutMS     = 120000
	optionalStepProbeMS  = 3000
)

// Allowed keys for press steps. Free-form key names are not accepted.
var allowedKeys = map[string]bool{"Enter": true, "Tab": true, "Escape": true}

type Selector struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
	Tag   string `json:"tag,omitempty"`
}

type Step struct {
	Type      string     `json:"type"`
	URL       string     `json:"url,omitempty"`
	Selectors []Selector `json:"selectors,omitempty"`
	Value     string     `json:"value,omitempty"`
	Key       string     `json:"key,omitempty"`
	OutputKey string     `json:"output_key,omitempty"`
	Label     string     `json:"label,omitempty"`
	PageURL   string     `json:"page_url,omitempty"`
	TimeoutMS int        `json:"timeout_ms,omitempty"`
	// Sensitive marks a value that was removed on this machine before upload.
	Sensitive bool `json:"sensitive,omitempty"`
	// Optional steps are skipped when their element never appears, such as a
	// login form that does not show because the saved session is still valid.
	Optional bool `json:"optional,omitempty"`
}

type Recording struct {
	ID             string   `json:"recording_id"`
	StartURL       string   `json:"start_url"`
	Steps          []Step   `json:"steps"`
	AllowedOrigins []string `json:"allowed_origins"`
	Warnings       []string `json:"warnings,omitempty"`
}

type ReplayResult struct {
	Success       bool              `json:"success"`
	StepsExecuted int               `json:"steps_executed"`
	SkippedSteps  []int             `json:"skipped_steps,omitempty"`
	Outputs       map[string]string `json:"outputs,omitempty"`
	FailedStep    *int              `json:"failed_step,omitempty"`
	ErrorCode     string            `json:"error_code,omitempty"`
	Error         string            `json:"error,omitempty"`
	// Screenshot is a base64 JPEG of the page at the failed step, size capped.
	Screenshot string   `json:"screenshot,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
}

// BotError carries a machine-readable code next to the human message.
type BotError struct {
	Code string
	Err  error
}

func (e *BotError) Error() string { return fmt.Sprintf("%s: %v", e.Code, e.Err) }
func (e *BotError) Unwrap() error { return e.Err }

func botErr(code string, format string, args ...any) error {
	return &BotError{Code: code, Err: fmt.Errorf(format, args...)}
}

// ErrorCode extracts a BotError code, falling back to fallback.
func ErrorCode(err error, fallback string) string {
	var be *BotError
	if errors.As(err, &be) {
		return be.Code
	}
	return fallback
}

// NormalizeOrigin returns scheme://host[:port] for http/https URLs only.
func NormalizeOrigin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("invalid url %q: %w", raw, err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("only http and https are allowed, got %q", u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", fmt.Errorf("url %q has no host", raw)
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]" // IPv6 literal
	}
	port := u.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host = host + ":" + port
	}
	return scheme + "://" + host, nil
}

func requiresElement(stepType string) bool {
	switch stepType {
	case StepClick, StepFill, StepSelect, StepWaitFor, StepAssertText, StepExtractText:
		return true
	}
	return false
}

// Validate checks a recording before it is replayed. The server validates the
// same rules; the runner checks again because it is the one acting.
func (r *Recording) Validate() error {
	if len(r.Steps) == 0 {
		return errors.New("recording has no steps")
	}
	if len(r.Steps) > MaxSteps {
		return fmt.Errorf("recording has %d steps, the limit is %d", len(r.Steps), MaxSteps)
	}
	if len(r.AllowedOrigins) == 0 || len(r.AllowedOrigins) > MaxAllowedOrigins {
		return fmt.Errorf("recording needs between 1 and %d allowed origins", MaxAllowedOrigins)
	}
	allowed := make(map[string]bool, len(r.AllowedOrigins))
	for _, o := range r.AllowedOrigins {
		n, err := NormalizeOrigin(o)
		if err != nil {
			return fmt.Errorf("allowed origin: %w", err)
		}
		allowed[n] = true
	}
	for i, s := range r.Steps {
		if err := s.validate(allowed); err != nil {
			return fmt.Errorf("step %d (%s): %w", i, s.Type, err)
		}
	}
	return nil
}

func (s *Step) validate(allowed map[string]bool) error {
	if s.TimeoutMS < 0 || s.TimeoutMS > MaxStepTimeoutMS {
		return fmt.Errorf("timeout_ms must be between 0 and %d", MaxStepTimeoutMS)
	}
	if len(s.Value) > MaxValueLength {
		return fmt.Errorf("value is longer than %d characters", MaxValueLength)
	}
	switch s.Type {
	case StepNavigate:
		origin, err := NormalizeOrigin(s.URL)
		if err != nil {
			return err
		}
		if !allowed[origin] {
			return fmt.Errorf("origin %s is not in the allowed origins", origin)
		}
		return nil
	case StepPress:
		if !allowedKeys[s.Key] {
			return fmt.Errorf("key %q is not allowed", s.Key)
		}
	case StepExtractText:
		if strings.TrimSpace(s.OutputKey) == "" {
			return errors.New("output_key is required")
		}
	case StepClick, StepFill, StepSelect, StepWaitFor, StepAssertText:
	default:
		return fmt.Errorf("unknown step type %q", s.Type)
	}
	if requiresElement(s.Type) && len(s.Selectors) == 0 {
		return errors.New("at least one selector is required")
	}
	if len(s.Selectors) > MaxSelectorsPerStep {
		return fmt.Errorf("at most %d selectors are allowed", MaxSelectorsPerStep)
	}
	for _, sel := range s.Selectors {
		switch sel.Kind {
		case SelectorCSS, SelectorLabel, SelectorText, SelectorPath:
		default:
			return fmt.Errorf("unknown selector kind %q", sel.Kind)
		}
		if sel.Value == "" || len(sel.Value) > MaxSelectorLength {
			return fmt.Errorf("selector value must be 1-%d characters", MaxSelectorLength)
		}
	}
	return nil
}

func (s *Step) timeoutMS() int {
	if s.TimeoutMS > 0 {
		return s.TimeoutMS
	}
	return DefaultStepTimeoutMS
}

// Package desktopbot replays work in Windows programs through UI Automation.
// A recording is data, never code: it names windows by their program and
// title, and controls by their UI Automation properties. No step starts a
// program or runs a command line, and the runner acts only on programs the
// person at this computer approved here (see Policy).
package desktopbot

import (
	"errors"
	"fmt"
	"strings"
)

// Step types the player understands.
const (
	StepFocusWindow = "focus_window"
	StepClick       = "click"
	StepFill        = "fill"
	StepSelect      = "select"
	StepPress       = "press"
	StepWaitFor     = "wait_for"
	StepAssertText  = "assert_text"
	StepExtractText = "extract_text"
)

// Selector kinds, tried in order. Path is the weakest: another window of the
// same program can have a control at the same place.
const (
	SelectorAutomationID = "automation_id"
	SelectorName         = "name"
	SelectorClassName    = "class_name"
	SelectorPath         = "uia_path"
)

// Error codes returned to the server.
const (
	CodeBusy                = "BUSY"
	CodeUnsupportedPlatform = "UNSUPPORTED_PLATFORM"
	CodeAppNotRunning       = "APP_NOT_RUNNING"
	CodeAppBlocked          = "APP_BLOCKED"
	CodeAppNotApproved      = "APP_NOT_APPROVED"
	CodeSelectorNotFound    = "SELECTOR_NOT_FOUND"
	CodeAssertionFailed     = "ASSERTION_FAILED"
	CodeValueNotSet         = "VALUE_NOT_SET"
	CodeWrongWindow         = "WRONG_WINDOW"
	CodeWorkstationLocked   = "WORKSTATION_LOCKED"
	CodeTimeout             = "TIMEOUT"
	CodeCancelled           = "CANCELLED"
	CodeStoppedByUser       = "STOPPED_BY_USER"
	CodeInvalidRecording    = "INVALID_RECORDING"
	CodeDesktopError        = "DESKTOP_ERROR"
)

// Limits keep a recording small enough for one WebSocket/NATS message.
const (
	MaxSteps             = 500
	MaxSelectorsPerStep  = 6
	MaxSelectorLength    = 500
	MaxValueLength       = 4000
	MaxAllowedApps       = 10
	MaxTitleLength       = 300
	DefaultStepTimeoutMS = 15000
	MaxStepTimeoutMS     = 120000
	optionalStepProbeMS  = 3000
)

// Keys a press step may send. Free-form key names are not accepted.
var allowedKeys = map[string]bool{"Enter": true, "Tab": true, "Escape": true}

// Window names the window a step acts in. App is the program's file name
// ("eczane.exe"); Title, when set, must appear in the window title.
type Window struct {
	App   string `json:"app"`
	Title string `json:"title,omitempty"`
}

type Selector struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
	// ControlType narrows name and class_name matches ("Edit", "Button").
	ControlType string `json:"control_type,omitempty"`
}

type Step struct {
	Type      string     `json:"type"`
	Window    Window     `json:"window"`
	Selectors []Selector `json:"selectors,omitempty"`
	Value     string     `json:"value,omitempty"`
	Key       string     `json:"key,omitempty"`
	OutputKey string     `json:"output_key,omitempty"`
	Label     string     `json:"label,omitempty"`
	TimeoutMS int        `json:"timeout_ms,omitempty"`
	Sensitive bool       `json:"sensitive,omitempty"`
	Optional  bool       `json:"optional,omitempty"`
}

type Recording struct {
	ID          string   `json:"recording_id"`
	Steps       []Step   `json:"steps"`
	AllowedApps []string `json:"allowed_apps"`
	Warnings    []string `json:"warnings,omitempty"`
}

type ReplayResult struct {
	Success       bool              `json:"success"`
	StepsExecuted int               `json:"steps_executed"`
	SkippedSteps  []int             `json:"skipped_steps,omitempty"`
	Outputs       map[string]string `json:"outputs,omitempty"`
	FailedStep    *int              `json:"failed_step,omitempty"`
	ErrorCode     string            `json:"error_code,omitempty"`
	Error         string            `json:"error,omitempty"`
	// Screenshot is a base64 JPEG of the program window at the failed step,
	// size capped. Other programs on the screen are never in it.
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

// NormalizeApp returns the lower-case program file name of an app entry.
func NormalizeApp(app string) (string, error) {
	app = strings.ToLower(strings.TrimSpace(app))
	if app == "" || strings.ContainsAny(app, `\/:*?"<>|`) || !strings.HasSuffix(app, ".exe") || len(app) > 100 {
		return "", fmt.Errorf("app %q must be a program file name such as program.exe", app)
	}
	return app, nil
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
	apps, err := normalizeApps(r.AllowedApps)
	if err != nil {
		return fmt.Errorf("recording: %w", err)
	}
	allowed := make(map[string]bool, len(apps))
	for _, a := range apps {
		allowed[a] = true
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
	app, err := NormalizeApp(s.Window.App)
	if err != nil {
		return fmt.Errorf("window: %w", err)
	}
	if !allowed[app] {
		return fmt.Errorf("app %s is not in the allowed apps", app)
	}
	if len(s.Window.Title) > MaxTitleLength {
		return fmt.Errorf("window title is longer than %d characters", MaxTitleLength)
	}
	switch s.Type {
	case StepPress:
		if !allowedKeys[s.Key] {
			return fmt.Errorf("key %q is not allowed", s.Key)
		}
	case StepExtractText:
		if strings.TrimSpace(s.OutputKey) == "" {
			return errors.New("output_key is required")
		}
	case StepFocusWindow, StepClick, StepFill, StepSelect, StepWaitFor, StepAssertText:
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
		case SelectorAutomationID, SelectorName, SelectorClassName, SelectorPath:
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

package desktopbot

import (
	"unicode/utf8"

	"github.com/sptask/sptask_desktop_runner/internal/operator"
)

const maxOverlayLabel = 60

// stepLabel is what the overlay says the bot is doing. It names the control,
// never the value: a typed value can be personal data.
func stepLabel(step *Step) string {
	verb := map[string]string{
		StepFocusWindow: "Pencereye geçiyor",
		StepClick:       "Tıklıyor",
		StepFill:        "Yazıyor",
		StepSelect:      "Seçiyor",
		StepPress:       "Tuşa basıyor",
		StepWaitFor:     "Bekliyor",
		StepAssertText:  "Kontrol ediyor",
		StepExtractText: "Okuyor",
	}[step.Type]
	if verb == "" {
		verb = "Çalışıyor"
	}
	target := operator.FieldName(step.Label)
	if target == "" {
		return verb
	}
	label := verb + ": " + target
	if utf8.RuneCountInString(label) > maxOverlayLabel {
		label = string([]rune(label)[:maxOverlayLabel-1]) + "…"
	}
	return label
}

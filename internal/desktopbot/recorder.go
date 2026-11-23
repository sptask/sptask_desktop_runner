package desktopbot

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Teaching ("göster-öğret"): the person at the computer does the work once in
// an approved program and the recorder writes it down as steps the player can
// replay. It records only what can be replayed without guessing: values of
// fields (read through UI Automation when the focus leaves them), clicks on
// buttons and other clickable controls, and Enter or Escape. Password fields
// are never read; shortcuts, menus and right clicks are reported as warnings
// so the process designer adds them by hand.

const (
	DefaultMaxRecording  = 30 * time.Minute
	MaxRecordingDuration = 2 * time.Hour

	maxRecordWarnings = 50
	maxLabelLength    = 200
)

// RecordOptions bound one teaching session.
type RecordOptions struct {
	MaxDuration time.Duration
}

// UI Automation control type ids (UIA_*ControlTypeId) the recorder decides on.
const (
	ctButton      int32 = 50000
	ctCheckBox    int32 = 50002
	ctComboBox    int32 = 50003
	ctEdit        int32 = 50004
	ctHyperlink   int32 = 50005
	ctImage       int32 = 50006
	ctListItem    int32 = 50007
	ctMenu        int32 = 50009
	ctMenuBar     int32 = 50010
	ctMenuItem    int32 = 50011
	ctRadioButton int32 = 50013
	ctScrollBar   int32 = 50014
	ctSpinner     int32 = 50016
	ctStatusBar   int32 = 50017
	ctTab         int32 = 50018
	ctTabItem     int32 = 50019
	ctText        int32 = 50020
	ctToolBar     int32 = 50021
	ctTreeItem    int32 = 50024
	ctGroup       int32 = 50026
	ctThumb       int32 = 50027
	ctDataItem    int32 = 50029
	ctDocument    int32 = 50030
	ctSplitButton int32 = 50031
	ctWindow      int32 = 50032
	ctPane        int32 = 50033
	ctHeader      int32 = 50034
	ctTitleBar    int32 = 50037
	ctSeparator   int32 = 50038
)

// Warnings the recorder gives. They name no value and no control text: both
// can be personal data.
const (
	warnPassword      = "Şifre alanlarına yazılanlar kaydedilmez; gerekiyorsa süreç tasarımında parametre olarak ekleyin."
	warnNoValue       = "Değeri okunamayan bir metin alanı kullanıldı; ona yazılanlar kaydedilmez."
	warnMenu          = "Menü seçimleri kaydedilmez; aynı işi yapan bir düğmeyi kullanın veya adımı süreç tasarımında ekleyin."
	warnRightClick    = "Sağ tık menüleri kaydedilmez."
	warnShortcut      = "Klavye kısayolları ve F tuşları kaydedilmez; aynı işi yapan düğmeye tıklayın."
	warnDoubleClick   = "Çift tıklama tek tıklama olarak kaydedildi; oynatmada kontrol bir kez tıklanır."
	warnUnknownClick  = "Tıklanabilir olmayan bir kontrole yapılan tıklama kaydedilmedi."
	warnElevated      = "Yönetici olarak çalışan bir programda yapılanlar kaydedilmedi."
	warnNotApproved   = "%s bu bilgisayarda onaylı değil; orada yapılanlar kaydedilmedi (sptask-runner --approve-app)."
	warnNoSelector    = "Bir kontrol tanınamadığı için adımı kaydedilmedi."
	warnUnverified    = "Bir kontrol penceresinde yeniden bulunamadı; adımı oynatmadan önce kontrol edin."
	warnSelectorLong  = "Çok uzun bir seçici atlandı."
	warnValueLong     = "Çok uzun bir değer kaydedilmedi."
	warnStepLimit     = "Kayıt adım sınırına ulaştı; sonrası kaydedilmedi."
	warnTimeLimit     = "Kayıt süre sınırına ulaştı ve kendiliğinden bitti."
	warnDroppedInput  = "Çok hızlı yapılan bazı tıklama ve tuşlar kaydedilemedi."
	warnNothingCaught = "Kayıtta adım yok: izinli bir programda alan doldurulmadı ve düğmeye tıklanmadı."
)

// clickOutcome is what the recorder does with a click.
type clickOutcome int

const (
	clickUse     clickOutcome = iota // record a click on chain[index]
	clickIgnore                      // covered by value tracking, or window chrome
	clickPassive                     // empty space, a label: nothing to record
	clickMenu                        // a menu: warn
	clickUnknown                     // a control the player cannot click: warn
)

// clickTarget decides on a click from the control types of the element under
// the pointer and its ancestors (nearest first, below the window).
func clickTarget(chain []int32) (int, clickOutcome) {
	if len(chain) == 0 {
		return -1, clickPassive
	}
	for _, ct := range chain {
		switch ct {
		case ctMenu, ctMenuBar, ctMenuItem:
			return -1, clickMenu
		case ctComboBox:
			// Opening a drop-down or choosing its item: the chosen value is
			// recorded as a select step when the focus leaves it.
			return -1, clickIgnore
		case ctTitleBar, ctScrollBar:
			// Minimize, close and scroll buttons move the window, not the work.
			return -1, clickIgnore
		}
	}
	switch chain[0] {
	case ctEdit, ctDocument, ctSpinner:
		return -1, clickIgnore
	}
	for i, ct := range chain {
		switch ct {
		case ctButton, ctCheckBox, ctRadioButton, ctHyperlink, ctListItem, ctTabItem, ctTreeItem, ctDataItem, ctSplitButton:
			return i, clickUse
		}
	}
	switch chain[0] {
	case ctText, ctPane, ctWindow, ctGroup, ctSeparator, ctThumb, ctStatusBar, ctToolBar, ctImage, ctHeader, ctTab:
		return -1, clickPassive
	}
	return -1, clickUnknown
}

// valueStepType is the step a changed value becomes: select for a drop-down,
// fill for anything else that takes text. A drop-down that takes typed text
// gives the focus to its own edit box, which is a fill. Read-only controls
// are not tracked. warn is set for a text field whose value cannot be read.
func valueStepType(ctype int32, hasValue, readOnly bool) (stepType string, warn bool) {
	if !hasValue {
		return "", ctype == ctEdit || ctype == ctDocument
	}
	if ctype == ctComboBox {
		return StepSelect, false
	}
	if readOnly {
		return "", false
	}
	return StepFill, false
}

// keyClass is what a key press means to the recorder.
type keyClass int

const (
	keyIgnore   keyClass = iota // typing: the value is read from the field
	keyEnter                    // a press step, unless the field took it as text
	keyEscape                   // a press step, unless it closed a drop-down
	keyStop                     // Shift+Esc ends the recording
	keyShortcut                 // a shortcut or F key: warn
)

// Virtual-key codes the recorder classifies.
const (
	vkReturn  = 0x0D
	vkShift   = 0x10
	vkControl = 0x11
	vkMenu    = 0x12 // Alt
	vkEscape  = 0x1B
	vkLWin    = 0x5B
	vkRWin    = 0x5C
	vkF1      = 0x70
	vkF24     = 0x87
	vkLShift  = 0xA0
	vkRMenu   = 0xA5
	vkA       = 0x41
	vkC       = 0x43
	vkV       = 0x56
	vkX       = 0x58
	vkY       = 0x59
	vkZ       = 0x5A
)

// classifyKey decides on a key press from its virtual-key code and the
// modifiers held. Only the class leaves the keyboard hook, never the key.
func classifyKey(vk uint32, shift, ctrl, alt bool) keyClass {
	switch {
	case vk == vkEscape && shift:
		return keyStop
	case vk == vkEscape:
		return keyEscape
	case vk == vkReturn && !ctrl && !alt:
		return keyEnter
	case vk >= vkF1 && vk <= vkF24:
		return keyShortcut
	}
	if (vk >= vkShift && vk <= vkMenu) || (vk >= vkLShift && vk <= vkRMenu) || vk == vkLWin || vk == vkRWin {
		return keyIgnore
	}
	// Ctrl+Alt is AltGr on many keyboards: it types characters such as @.
	if ctrl && alt {
		return keyIgnore
	}
	if ctrl {
		switch vk {
		case vkA, vkC, vkV, vkX, vkY, vkZ:
			return keyIgnore // editing: the field's value shows the result
		}
		return keyShortcut
	}
	if alt {
		return keyShortcut
	}
	return keyIgnore
}

// capture is one thing the person did, with the selectors the recorder
// found for its control.
type capture struct {
	stepType  string
	hwnd      uintptr // the top-level window: a new one gets a focus_window step
	window    Window
	target    string // identifies the control, to merge two fills of it
	selectors []Selector
	value     string
	key       string
	label     string
}

// assembler turns captures into a recording the server accepts: it applies
// the same limits as Recording.Validate and the server, so a long teaching
// session ends in fewer steps and a warning instead of a rejected recording.
type assembler struct {
	steps    []Step
	warnings []string
	seen     map[string]bool
	lastHwnd uintptr
	lastTgt  string
	full     bool
}

func newAssembler() *assembler { return &assembler{seen: map[string]bool{}} }

// warn adds a warning once; the list is capped like the server's.
func (a *assembler) warn(msg string) {
	if a.seen[msg] || len(a.warnings) >= maxRecordWarnings {
		return
	}
	a.seen[msg] = true
	a.warnings = append(a.warnings, msg)
}

func (a *assembler) add(c capture) {
	if a.full {
		return
	}
	if len(c.value) > MaxValueLength {
		a.warn(warnValueLong)
		return
	}
	sels := make([]Selector, 0, len(c.selectors))
	for _, s := range c.selectors {
		if len(s.Value) > MaxSelectorLength {
			a.warn(warnSelectorLong)
			continue
		}
		if s.Value == "" || len(sels) == MaxSelectorsPerStep {
			continue
		}
		sels = append(sels, s)
	}
	if len(sels) == 0 {
		a.warn(warnNoSelector)
		return
	}
	win := Window{App: c.window.App, Title: truncateBytes(c.window.Title, MaxTitleLength)}
	step := Step{Type: c.stepType, Window: win, Selectors: sels, Value: c.value, Key: c.key,
		Label: truncateBytes(c.label, maxLabelLength)}

	// Two values in a row for one field: the person came back to it. Only
	// the last one counts.
	if (c.stepType == StepFill || c.stepType == StepSelect) && c.hwnd == a.lastHwnd && c.target == a.lastTgt && len(a.steps) > 0 {
		if last := &a.steps[len(a.steps)-1]; last.Type == c.stepType {
			last.Value = c.value
			return
		}
	}
	need := 1
	if c.hwnd != a.lastHwnd {
		need = 2
	}
	if len(a.steps)+need > MaxSteps {
		a.full = true
		a.warn(warnStepLimit)
		return
	}
	if c.hwnd != a.lastHwnd {
		a.steps = append(a.steps, Step{Type: StepFocusWindow, Window: win})
		a.lastHwnd = c.hwnd
	}
	a.steps = append(a.steps, step)
	a.lastTgt = c.target
}

// recording is the result for the server.
func (a *assembler) recording(id string, apps []string) Recording {
	if len(a.steps) == 0 {
		a.warn(warnNothingCaught)
	}
	return Recording{ID: id, Steps: a.steps, AllowedApps: apps, Warnings: a.warnings}
}

// truncateBytes cuts s to at most n bytes without splitting a character.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// normalizeApps checks a list of program names and returns it lower-cased,
// sorted and without repeats.
func normalizeApps(apps []string) ([]string, error) {
	if len(apps) == 0 || len(apps) > MaxAllowedApps {
		return nil, fmt.Errorf("between 1 and %d allowed apps are needed", MaxAllowedApps)
	}
	set := make(map[string]bool, len(apps))
	for _, a := range apps {
		n, err := NormalizeApp(a)
		if err != nil {
			return nil, fmt.Errorf("allowed app: %w", err)
		}
		set[n] = true
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// sameText compares values the way an edit box stores them.
func sameText(a, b string) bool {
	return strings.ReplaceAll(a, "\r\n", "\n") == strings.ReplaceAll(b, "\r\n", "\n")
}

// lineBreaks counts the line breaks of a value.
func lineBreaks(s string) int { return strings.Count(strings.ReplaceAll(s, "\r\n", "\n"), "\n") }

// Package operator describes who is working on this computer during an
// automated run: the AI employee (or process) the run belongs to. The browser
// and desktop players draw its name and colour over what they control, so the
// person at the computer always sees that a bot is acting and for whom.
package operator

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// MaxNameLength caps the name drawn on the screen, in characters.
	MaxNameLength = 60
	// DefaultName is shown when the server sends no usable name.
	DefaultName = "SparTask"
	// DefaultColor is SparTask's accent colour.
	DefaultColor = "#7c3aed"
	// StopHint tells the person at the computer how to stop the run. The bot
	// itself never sends Shift with a key, so the combination is the user's.
	StopHint = "Durdurmak için Shift+Esc"
)

var colorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Operator is decoded from the server's command; treat both fields as
// untrusted text: Normalized bounds them before anything is drawn.
type Operator struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// Normalized returns a copy that is safe to draw: a single line of at most
// MaxNameLength characters and a #rrggbb colour, with defaults for the rest.
func (o Operator) Normalized() Operator {
	name := strings.Join(strings.FieldsFunc(o.Name, func(r rune) bool {
		return r == '\n' || r == '\r' || r == '\t' || !utf8.ValidRune(r) || r < ' '
	}), " ")
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > MaxNameLength {
		name = string([]rune(name)[:MaxNameLength])
	}
	if name == "" {
		name = DefaultName
	}
	color := strings.TrimSpace(o.Color)
	if !colorPattern.MatchString(color) {
		color = DefaultColor
	}
	return Operator{Name: name, Color: color}
}

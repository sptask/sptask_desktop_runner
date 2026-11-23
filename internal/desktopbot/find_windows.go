//go:build windows && (amd64 || arm64)

package desktopbot

import (
	"fmt"
	"strconv"
	"strings"
)

// Limits of one search below a window, so a huge grid cannot stall a step.
const (
	maxSearchNodes = 3000
	maxSearchDepth = 40
)

// node is one element found below a window. Properties are read on demand:
// every read is a call into the other program.
type node struct {
	el    element
	path  string
	ctype int32

	aid, name, class string
	hasAID, hasName  bool
	hasClass         bool
}

func (n *node) automationID() (string, error) {
	if !n.hasAID {
		v, err := n.el.automationID()
		if err != nil {
			return "", err
		}
		n.aid, n.hasAID = v, true
	}
	return n.aid, nil
}

func (n *node) elementName() (string, error) {
	if !n.hasName {
		v, err := n.el.name()
		if err != nil {
			return "", err
		}
		n.name, n.hasName = v, true
	}
	return n.name, nil
}

func (n *node) elementClass() (string, error) {
	if !n.hasClass {
		v, err := n.el.className()
		if err != nil {
			return "", err
		}
		n.class, n.hasClass = v, true
	}
	return n.class, nil
}

// descendants lists the control view below root, breadth first, with the
// structural path of each element ("Pane[0]/Edit[1]": the n-th child of that
// control type). The whole tree or an error: a partial tree could make an
// ambiguous selector look unique. When a part disappears while it is read (a
// list opening or closing), the step looks again on its next attempt.
// The caller releases every returned element.
func (u *uiaThread) descendants(root element) ([]*node, error) {
	type frame struct {
		el    element
		path  string
		depth int
	}
	rootPID, err := root.processID()
	if err != nil {
		return nil, fmt.Errorf("read window's program: %w", err)
	}
	var out []*node
	queue := []frame{{el: root}}
	for len(queue) > 0 && len(out) < maxSearchNodes {
		f := queue[0]
		queue = queue[1:]
		if f.depth >= maxSearchDepth {
			continue
		}
		counts := map[int32]int{}
		child, err := u.firstChild(f.el)
		if err != nil {
			releaseNodes(out)
			return nil, fmt.Errorf("walk controls: %w", err)
		}
		for child.valid() && len(out) < maxSearchNodes {
			ctype, err := child.controlType()
			if err != nil {
				child.release()
				releaseNodes(out)
				return nil, fmt.Errorf("read control type: %w", err)
			}
			// An open drop-down list is a popup window that UI Automation
			// places under its control; the walker's next sibling after it is
			// the rest of the desktop. Controls of another program are never
			// entered, and the walk stops after a popup.
			pid, err := child.processID()
			if err != nil {
				child.release()
				releaseNodes(out)
				return nil, fmt.Errorf("read control's program: %w", err)
			}
			if pid != rootPID {
				child.release()
				child = element{}
				break
			}
			popup, err := isPopupWindow(child)
			if err != nil {
				child.release()
				releaseNodes(out)
				return nil, fmt.Errorf("read control's window: %w", err)
			}
			seg := controlTypeName(ctype) + "[" + strconv.Itoa(counts[ctype]) + "]"
			counts[ctype]++
			path := seg
			if f.path != "" {
				path = f.path + "/" + seg
			}
			n := &node{el: child, path: path, ctype: ctype}
			out = append(out, n)
			queue = append(queue, frame{el: child, path: path, depth: f.depth + 1})
			if popup {
				child = element{}
				break
			}
			next, err := u.nextSibling(child)
			if err != nil {
				releaseNodes(out)
				return nil, fmt.Errorf("walk controls: %w", err)
			}
			child = next
		}
		if child.valid() {
			child.release()
		}
	}
	return out, nil
}

func releaseNodes(nodes []*node) {
	for _, n := range nodes {
		n.el.release()
	}
}

// matches reports whether n fits one selector.
func (n *node) matches(sel Selector) (bool, error) {
	if sel.ControlType != "" && !strings.EqualFold(controlTypeName(n.ctype), sel.ControlType) {
		return false, nil
	}
	switch sel.Kind {
	case SelectorAutomationID:
		v, err := n.automationID()
		return err == nil && v == sel.Value, err
	case SelectorName:
		v, err := n.elementName()
		return err == nil && strings.TrimSpace(v) == strings.TrimSpace(sel.Value), err
	case SelectorClassName:
		v, err := n.elementClass()
		return err == nil && v == sel.Value, err
	case SelectorPath:
		return n.path == sel.Value, nil
	}
	return false, fmt.Errorf("unknown selector kind %q", sel.Kind)
}

// pick returns the element of the first selector that matches exactly one
// element, or nil. A selector that matches several elements is ambiguous and
// skipped, so the bot never guesses between two fields.
func pick(nodes []*node, sels []Selector) (*node, error) {
	for _, sel := range sels {
		var found *node
		count := 0
		for _, n := range nodes {
			ok, err := n.matches(sel)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", sel.Kind, err)
			}
			if ok {
				count++
				found = n
				if count > 1 {
					break
				}
			}
		}
		if count == 1 {
			return found, nil
		}
	}
	return nil, nil
}

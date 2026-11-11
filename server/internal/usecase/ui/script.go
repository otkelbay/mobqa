package ui

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/mobqa/backend/internal/fault"
)

// Kinds that appear only in scripts and send nothing to the device. KindAssert checks that the selected
// element is on screen; KindCall performs the API action named by the op's text.
const (
	KindAssert Kind = "assert"
	KindCall   Kind = "call"
)

// maxOps bounds a script: a test has at most 200 steps and a step rarely needs more than a few actions.
const maxOps = 1000

var variablePattern = regexp.MustCompile(`\{\{([A-Za-z_][A-Za-z0-9_]{0,63})\}\}`)

// ErrUnknownVariable is text that names a variable the run does not define.
var ErrUnknownVariable = errors.New("unknown variable")

// Selector finds an element by what it shows rather than where it is, so a script survives layout
// changes. Every field that is set must match exactly; Index picks among several matches.
type Selector struct {
	ID    string
	Text  string
	Label string
	Index int
}

func (s Selector) empty() bool {
	return s.ID == "" && s.Text == "" && s.Label == ""
}

func (s Selector) matches(e Element) bool {
	return (s.ID == "" || s.ID == e.ID) && (s.Text == "" || s.Text == e.Text) && (s.Label == "" || s.Label == e.Label)
}

// Find returns the element the selector picks on the screen.
func (s Selector) Find(screen Screen) (Element, bool) {
	seen := 0
	for _, e := range screen.Elements {
		if !s.matches(e) {
			continue
		}
		if seen == s.Index {
			return e, true
		}
		seen++
	}
	return Element{}, false
}

// SelectorFor describes an element by the fewest fields that tell it apart on the screen, preferring
// the identifier, which translations do not change.
func SelectorFor(screen Screen, e Element) Selector {
	candidates := []Selector{{ID: e.ID}, {Text: e.Text}, {Label: e.Label}, {ID: e.ID, Text: e.Text, Label: e.Label}}
	for _, candidate := range candidates {
		if !candidate.empty() && candidate.count(screen) == 1 {
			return candidate
		}
	}
	full := candidates[len(candidates)-1]
	for _, other := range screen.Elements {
		if other == e {
			break
		}
		if full.matches(other) {
			full.Index++
		}
	}
	return full
}

func (s Selector) count(screen Screen) int {
	n := 0
	for _, e := range screen.Elements {
		if s.matches(e) {
			n++
		}
	}
	return n
}

func (s Selector) String() string {
	var parts []string
	for _, field := range [][2]string{{"id", s.ID}, {"text", s.Text}, {"label", s.Label}} {
		if field[1] != "" {
			parts = append(parts, fmt.Sprintf("%s=%q", field[0], field[1]))
		}
	}
	if s.Index > 0 {
		parts = append(parts, fmt.Sprintf("index=%d", s.Index))
	}
	return strings.Join(parts, " ")
}

// Direction is where the finger moves in a swipe: swiping up scrolls the content down.
type Direction string

const (
	DirectionUp    Direction = "up"
	DirectionDown  Direction = "down"
	DirectionLeft  Direction = "left"
	DirectionRight Direction = "right"
)

func (d Direction) validate() error {
	switch d {
	case DirectionUp, DirectionDown, DirectionLeft, DirectionRight:
		return nil
	default:
		return fault.WithDetail(fault.ErrInvalidInput, "direction must be up, down, left or right")
	}
}

// SwipePoints spans the middle of area from one side to the other, keeping clear of its edges, where
// system gestures live.
func SwipePoints(area Rect, direction Direction) (Point, Point, error) {
	if err := direction.validate(); err != nil {
		return Point{}, Point{}, err
	}
	center := area.Center()
	dx, dy := area.Width*3/10, area.Height*3/10
	switch direction {
	case DirectionUp:
		return Point{X: center.X, Y: center.Y + dy}, Point{X: center.X, Y: center.Y - dy}, nil
	case DirectionDown:
		return Point{X: center.X, Y: center.Y - dy}, Point{X: center.X, Y: center.Y + dy}, nil
	case DirectionLeft:
		return Point{X: center.X + dx, Y: center.Y}, Point{X: center.X - dx, Y: center.Y}, nil
	default:
		return Point{X: center.X - dx, Y: center.Y}, Point{X: center.X + dx, Y: center.Y}, nil
	}
}

// Op is one action of a script, compiled from the manual step at Step (counted from 1; 0 prepares the
// app). Selector picks the element to act on; Text may name variables as {{NAME}}, except in a call op.
type Op struct {
	Step      int
	Kind      Kind
	Selector  *Selector
	Direction Direction
	Text      string
}

func (o Op) validate(steps int) error {
	if o.Step < 0 || o.Step > steps {
		return fault.WithDetail(fault.ErrInvalidInput, "an op names a step the test does not have")
	}
	if o.Selector != nil && o.Selector.empty() {
		return fault.WithDetail(fault.ErrInvalidInput, "a selector needs an id, a text or a label")
	}
	switch o.Kind {
	case KindAssert:
		if o.Selector == nil {
			return fault.WithDetail(fault.ErrInvalidInput, "an assert op needs a selector")
		}
		return nil
	case KindCall:
		if strings.TrimSpace(o.Text) == "" || o.Selector != nil {
			return fault.WithDetail(fault.ErrInvalidInput, "a call op names an API action and no element")
		}
		return nil
	case KindTap, KindLongPress, KindDoubleTap:
		if o.Selector == nil {
			return fault.WithDetail(fault.ErrInvalidInput, fmt.Sprintf("a %s op needs a selector", o.Kind))
		}
	case KindSwipe:
		if err := o.Direction.validate(); err != nil {
			return err
		}
	}
	return validateKind(o.Kind, o.Text)
}

// ValidateScript checks ops compiled from a test of steps steps.
func ValidateScript(ops []Op, steps int) error {
	if len(ops) > maxOps {
		return fault.WithDetail(fault.ErrInvalidInput, "a script has at most 1000 ops")
	}
	for _, op := range ops {
		if err := op.validate(steps); err != nil {
			return err
		}
	}
	return nil
}

// Expand replaces {{NAME}} with the variable's value.
func Expand(text string, variables map[string]string) (string, error) {
	var missing error
	expanded := variablePattern.ReplaceAllStringFunc(text, func(match string) string {
		name := match[2 : len(match)-2]
		value, ok := variables[name]
		if !ok && missing == nil {
			missing = fmt.Errorf("%w: %s", ErrUnknownVariable, name)
		}
		return value
	})
	return expanded, missing
}

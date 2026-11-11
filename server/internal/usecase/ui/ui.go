// Package ui is the shared vocabulary for driving an app on a device: what a screen shows and the
// commands that act on it. The hub turns commands into device calls; authoring and runs decide which
// commands to send.
package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/project"
)

const (
	maxTextLength = 2000
	maxDuration   = 10 * time.Second
)

type Point struct {
	X int
	Y int
}

type Rect struct {
	X      int
	Y      int
	Width  int
	Height int
}

func (r Rect) Center() Point {
	return Point{X: r.X + r.Width/2, Y: r.Y + r.Height/2}
}

// Element is one node of the screen's accessibility tree that a person could see or use.
type Element struct {
	// ID is the platform's identifier for the element: a resource id on Android, an accessibility
	// identifier on iOS.
	ID         string
	Text       string
	Label      string
	Class      string
	Bounds     Rect
	Clickable  bool
	Editable   bool
	Scrollable bool
	Enabled    bool
	Focused    bool
	Checked    bool
}

// Screen is what a device shows at one moment. App is the package or bundle in the foreground.
type Screen struct {
	App      string
	Width    int
	Height   int
	Keyboard bool
	Elements []Element
}

type Kind string

const (
	KindTap          Kind = "tap"
	KindLongPress    Kind = "long_press"
	KindDoubleTap    Kind = "double_tap"
	KindType         Kind = "type"
	KindClear        Kind = "clear"
	KindBack         Kind = "back"
	KindEnter        Kind = "enter"
	KindHideKeyboard Kind = "hide_keyboard"
	KindSwipe        Kind = "swipe"
	KindOpenURL      Kind = "open_url"
	KindLaunch       Kind = "launch"
	KindStop         Kind = "stop"
)

// DeviceError is a command or an observation the hub received but could not carry out, such as text
// the device cannot type. Unlike a lost connection, it is an answer about the request.
type DeviceError struct {
	Message string
}

func (e *DeviceError) Error() string {
	return e.Message
}

// Target is the device a command or an observation is for.
type Target struct {
	Serial   string
	Platform project.Platform
}

func (t Target) Validate() error {
	if t.Serial == "" {
		return fault.WithDetail(fault.ErrInvalidInput, "a device serial is required")
	}
	if t.Platform != project.PlatformAndroid && t.Platform != project.PlatformIOS {
		return fault.WithDetail(fault.ErrInvalidInput, "platform must be android or ios")
	}
	return nil
}

// Command is one action on a device. Which fields matter depends on Kind: Point for taps, Point and To
// for swipes, Text for typing, URLs and package names.
type Command struct {
	Target   Target
	Kind     Kind
	Point    Point
	To       Point
	Text     string
	Duration time.Duration
}

func (c Command) Validate() error {
	if err := c.Target.Validate(); err != nil {
		return err
	}
	if c.Point.X < 0 || c.Point.Y < 0 || c.To.X < 0 || c.To.Y < 0 {
		return fault.WithDetail(fault.ErrInvalidInput, "points must be on the screen")
	}
	if c.Duration < 0 || c.Duration > maxDuration {
		return fault.WithDetail(fault.ErrInvalidInput, "durations must be at most 10 seconds")
	}
	if utf8.RuneCountInString(c.Text) > maxTextLength {
		return fault.WithDetail(fault.ErrInvalidInput, "command text must be at most 2000 characters")
	}
	return validateKind(c.Kind, c.Text)
}

func validateKind(kind Kind, text string) error {
	switch kind {
	case KindTap, KindLongPress, KindDoubleTap, KindClear, KindBack, KindEnter, KindHideKeyboard, KindSwipe:
		return nil
	case KindType, KindOpenURL, KindLaunch, KindStop:
		if strings.TrimSpace(text) == "" {
			return fault.WithDetail(fault.ErrInvalidInput, fmt.Sprintf("a %s needs text", kind))
		}
		return nil
	default:
		return fault.WithDetail(fault.ErrInvalidInput, "unknown command kind")
	}
}

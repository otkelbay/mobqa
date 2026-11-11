package adb

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"image/jpeg"
	"image/png"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mobqa/backend/internal/usecase/ui"
)

const (
	dumpPath          = "/sdcard/mobqa-window.xml"
	longPressDuration = 800 * time.Millisecond
	swipeDuration     = 300 * time.Millisecond
	// clearKeystrokes deletes a field of any length a test is likely to type into.
	clearKeystrokes = 120
	// captureQuality keeps a full-resolution phone screen well under the hub link's message limit.
	captureQuality = 70
)

var (
	errNonASCII    = errors.New("adb can type only ASCII text")
	errNoScreen    = errors.New("the screen dump has no window")
	packagePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z0-9_]+)+$`)
	boundsPattern  = regexp.MustCompile(`^\[(\d+),(\d+)\]\[(\d+),(\d+)\]$`)
)

// Controller drives Android devices with adb: the accessibility dump for what is on screen and input
// events for actions.
type Controller struct {
	run     runner
	install runner
}

// NewController uses the adb binary at path.
func NewController(path string) *Controller {
	return &Controller{
		run: execRunner{path: path, timeout: commandTimeout}, install: execRunner{path: path, timeout: installTimeout},
	}
}

// Install replaces the app with the package at path, allowing a lower version, so that any build of the
// app can be tested.
func (c *Controller) Install(ctx context.Context, serial, path string) error {
	out, err := c.install.output(ctx, "-s", serial, "install", "-r", "-d", path)
	if err != nil {
		return err
	}
	if !strings.Contains(string(out), "Success") {
		return fmt.Errorf("adb install: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// Capture takes a screenshot and returns it as a JPEG.
func (c *Controller) Capture(ctx context.Context, serial string) ([]byte, error) {
	shot, err := c.run.output(ctx, "-s", serial, "exec-out", "screencap", "-p")
	if err != nil {
		return nil, err
	}
	img, err := png.Decode(bytes.NewReader(shot))
	if err != nil {
		return nil, fmt.Errorf("decode the screenshot: %w", err)
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: captureQuality}); err != nil {
		return nil, fmt.Errorf("encode the screenshot: %w", err)
	}
	return out.Bytes(), nil
}

func (c *Controller) Observe(ctx context.Context, serial string) (ui.Screen, error) {
	if _, err := c.run.output(ctx, "-s", serial, "shell", "uiautomator", "dump", dumpPath); err != nil {
		return ui.Screen{}, err
	}
	dump, err := c.run.output(ctx, "-s", serial, "exec-out", "cat", dumpPath)
	if err != nil {
		return ui.Screen{}, err
	}
	screen, err := parseHierarchy(dump)
	if err != nil {
		return ui.Screen{}, err
	}
	methods, err := c.run.output(ctx, "-s", serial, "shell", "dumpsys", "input_method")
	if err != nil {
		return ui.Screen{}, err
	}
	screen.Keyboard = strings.Contains(string(methods), "mInputShown=true")
	return screen, nil
}

func (c *Controller) Act(ctx context.Context, command ui.Command) error {
	script, err := shellScript(command)
	if err != nil {
		return err
	}
	if command.Kind == ui.KindHideKeyboard {
		screen, err := c.Observe(ctx, command.Target.Serial)
		if err != nil || !screen.Keyboard {
			return err
		}
	}
	_, err = c.run.output(ctx, "-s", command.Target.Serial, "shell", script)
	return err
}

// shellScript is the device shell command for a command; adb runs it through sh, so every value is
// either a number or quoted.
func shellScript(command ui.Command) (string, error) {
	p, to := command.Point, command.To
	switch command.Kind {
	case ui.KindTap:
		return fmt.Sprintf("input tap %d %d", p.X, p.Y), nil
	case ui.KindDoubleTap:
		return fmt.Sprintf("input tap %d %d && input tap %d %d", p.X, p.Y, p.X, p.Y), nil
	case ui.KindLongPress:
		duration := milliseconds(command.Duration, longPressDuration)
		return fmt.Sprintf("input swipe %d %d %d %d %d", p.X, p.Y, p.X, p.Y, duration), nil
	case ui.KindSwipe:
		duration := milliseconds(command.Duration, swipeDuration)
		return fmt.Sprintf("input swipe %d %d %d %d %d", p.X, p.Y, to.X, to.Y, duration), nil
	case ui.KindType:
		return typeScript(command.Text)
	case ui.KindClear:
		return "input keyevent KEYCODE_MOVE_END" + strings.Repeat(" KEYCODE_DEL", clearKeystrokes), nil
	case ui.KindBack, ui.KindHideKeyboard:
		return "input keyevent KEYCODE_BACK", nil
	case ui.KindEnter:
		return "input keyevent KEYCODE_ENTER", nil
	case ui.KindOpenURL:
		return "am start -a android.intent.action.VIEW -d " + quote(command.Text), nil
	case ui.KindLaunch, ui.KindStop:
		return appScript(command)
	default:
		return "", fmt.Errorf("adb cannot %s", command.Kind)
	}
}

func appScript(command ui.Command) (string, error) {
	if !packagePattern.MatchString(command.Text) {
		return "", fmt.Errorf("%q is not an Android package name", command.Text)
	}
	if command.Kind == ui.KindStop {
		return "am force-stop " + command.Text, nil
	}
	return "monkey -p " + command.Text + " -c android.intent.category.LAUNCHER 1", nil
}

// typeScript escapes text for `input text`, which reads %s as a space and cannot type beyond ASCII.
func typeScript(text string) (string, error) {
	for _, r := range text {
		if r > 0x7e || r < 0x20 {
			return "", errNonASCII
		}
	}
	return "input text " + quote(strings.ReplaceAll(text, " ", "%s")), nil
}

func quote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func milliseconds(value, fallback time.Duration) int64 {
	if value <= 0 {
		value = fallback
	}
	return value.Milliseconds()
}

type node struct {
	Text       string `xml:"text,attr"`
	ResourceID string `xml:"resource-id,attr"`
	Class      string `xml:"class,attr"`
	Package    string `xml:"package,attr"`
	Desc       string `xml:"content-desc,attr"`
	Clickable  bool   `xml:"clickable,attr"`
	Scrollable bool   `xml:"scrollable,attr"`
	Enabled    bool   `xml:"enabled,attr"`
	Focused    bool   `xml:"focused,attr"`
	Checked    bool   `xml:"checked,attr"`
	Bounds     string `xml:"bounds,attr"`
	Children   []node `xml:"node"`
}

type hierarchy struct {
	Nodes []node `xml:"node"`
}

// parseHierarchy reads a uiautomator dump. The first window's bounds are the screen; nodes with nothing
// to read or use, and nodes outside the screen, are left out.
func parseHierarchy(dump []byte) (ui.Screen, error) {
	var tree hierarchy
	if err := xml.Unmarshal(dump, &tree); err != nil {
		return ui.Screen{}, fmt.Errorf("parse screen dump: %w", err)
	}
	if len(tree.Nodes) == 0 {
		return ui.Screen{}, errNoScreen
	}
	root, _ := parseBounds(tree.Nodes[0].Bounds)
	screen := ui.Screen{
		App: tree.Nodes[0].Package, Width: root.X + root.Width, Height: root.Y + root.Height, Elements: []ui.Element{},
	}
	for _, n := range tree.Nodes {
		collect(n, &screen)
	}
	return screen, nil
}

func collect(n node, screen *ui.Screen) {
	if element, ok := n.element(); ok && element.Bounds.X+element.Bounds.Width <= screen.Width &&
		element.Bounds.Y+element.Bounds.Height <= screen.Height {
		screen.Elements = append(screen.Elements, element)
	}
	for _, child := range n.Children {
		collect(child, screen)
	}
}

func (n node) element() (ui.Element, bool) {
	bounds, ok := parseBounds(n.Bounds)
	if !ok || bounds.Width == 0 || bounds.Height == 0 {
		return ui.Element{}, false
	}
	class := n.Class[strings.LastIndex(n.Class, ".")+1:]
	element := ui.Element{
		ID: n.ResourceID, Text: n.Text, Label: n.Desc, Class: class, Bounds: bounds, Clickable: n.Clickable,
		Editable: strings.HasSuffix(class, "EditText"), Scrollable: n.Scrollable, Enabled: n.Enabled,
		Focused: n.Focused, Checked: n.Checked,
	}
	useful := element.Text != "" || element.Label != "" || element.ID != "" || element.Clickable ||
		element.Editable || element.Scrollable
	return element, useful
}

// parseBounds reads "[left,top][right,bottom]".
func parseBounds(value string) (ui.Rect, bool) {
	match := boundsPattern.FindStringSubmatch(value)
	if match == nil {
		return ui.Rect{}, false
	}
	numbers := make([]int, 4)
	for i := range numbers {
		numbers[i], _ = strconv.Atoi(match[i+1])
	}
	if numbers[2] < numbers[0] || numbers[3] < numbers[1] {
		return ui.Rect{}, false
	}
	return ui.Rect{X: numbers[0], Y: numbers[1], Width: numbers[2] - numbers[0], Height: numbers[3] - numbers[1]}, true
}

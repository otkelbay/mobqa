// Package hubws carries the link between the API and its hubs: one WebSocket per hub, dialled by the hub,
// framing JSON envelopes. An envelope with an ID is a request and gets exactly one reply naming it in
// ReplyTo; an envelope with neither is an event, handled in arrival order.
package hubws

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/mobqa/backend/internal/usecase/action"
	"github.com/mobqa/backend/internal/usecase/device"
	"github.com/mobqa/backend/internal/usecase/hub"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/run"
	"github.com/mobqa/backend/internal/usecase/test"
	"github.com/mobqa/backend/internal/usecase/ui"
)

// Path is where the API accepts hub connections.
const Path = "/hub/connect"

// Event types sent by the hub. The first frame of every connection is an inventory.
const (
	TypeInventory = "inventory"
	TypeHeartbeat = "heartbeat"
)

// TypeCancel is an event either side sends to stop serving a request it is no longer waiting for.
const TypeCancel = "cancel"

// Requests the API sends to a hub.
const (
	// TypeExecute runs one attempt of a test on a device.
	TypeExecute = "test.execute"
	// TypeObserve reads what a device shows.
	TypeObserve = "device.observe"
	// TypeAct carries out one command on a device.
	TypeAct = "device.act"
	// TypeCapture takes a screenshot of a device.
	TypeCapture = "device.capture"
	// TypePerform sends an API action's request from the hub's network.
	TypePerform = "action.perform"
	// TypePublish hands the hub a phone's offer to stream its screen.
	TypePublish = "stream.publish"
	// TypeWatch hands the hub a browser's offer to receive a device's screen.
	TypeWatch = "stream.watch"
)

type Envelope struct {
	Type    string          `json:"type,omitempty"`
	ID      string          `json:"id,omitempty"`
	ReplyTo string          `json:"reply_to,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   string          `json:"error,omitempty"`
}

func (e Envelope) decode(v any) error {
	if err := json.Unmarshal(e.Payload, v); err != nil {
		return fmt.Errorf("decode %s payload: %w", e.Type, err)
	}
	return nil
}

type Inventory struct {
	Version string   `json:"version"`
	Devices []Device `json:"devices"`
}

type Device struct {
	Serial    string `json:"serial"`
	Name      string `json:"name"`
	Model     string `json:"model"`
	Platform  string `json:"platform"`
	OSVersion string `json:"os_version"`
	Physical  bool   `json:"physical"`
	State     string `json:"state"`
}

func newInventory(inv hub.Inventory) Inventory {
	devices := make([]Device, len(inv.Devices))
	for i, d := range inv.Devices {
		devices[i] = Device{
			Serial: d.Serial, Name: d.Name, Model: d.Model, Platform: string(d.Platform), OSVersion: d.OSVersion,
			Physical: d.Physical, State: string(d.State),
		}
	}
	return Inventory{Version: inv.Version, Devices: devices}
}

func (inv Inventory) hub() hub.Inventory {
	devices := make([]hub.ReportedDevice, len(inv.Devices))
	for i, d := range inv.Devices {
		devices[i] = hub.ReportedDevice{
			Serial: d.Serial, Name: d.Name, Model: d.Model, Platform: project.Platform(d.Platform),
			OSVersion: d.OSVersion, Physical: d.Physical, State: device.State(d.State),
		}
	}
	return hub.Inventory{Version: inv.Version, Devices: devices}
}

type Cancel struct {
	RequestID string `json:"request_id"`
}

type Execution struct {
	ID        string            `json:"id"`
	Serial    string            `json:"serial"`
	Platform  string            `json:"platform"`
	Package   *Package          `json:"package,omitempty"`
	Title     string            `json:"title"`
	Steps     []Step            `json:"steps"`
	Script    []Op              `json:"script"`
	Variables map[string]string `json:"variables"`
	Actions   map[string]Action `json:"actions,omitempty"`
	TimeoutMS int64             `json:"timeout_ms"`
}

type Package struct {
	BuildID string `json:"build_id"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

type Step struct {
	Action         string `json:"action"`
	Target         string `json:"target,omitempty"`
	Description    string `json:"description,omitempty"`
	ExpectedResult string `json:"expected_result,omitempty"`
}

type Op struct {
	Step      int       `json:"step"`
	Kind      string    `json:"kind"`
	Selector  *Selector `json:"selector,omitempty"`
	Direction string    `json:"direction,omitempty"`
	Text      string    `json:"text,omitempty"`
}

type Selector struct {
	ID    string `json:"id,omitempty"`
	Text  string `json:"text,omitempty"`
	Label string `json:"label,omitempty"`
	Index int    `json:"index,omitempty"`
}

type Action struct {
	Method         string       `json:"method"`
	URL            string       `json:"url"`
	Headers        []Header     `json:"headers,omitempty"`
	Body           string       `json:"body,omitempty"`
	ExpectedStatus int          `json:"expected_status"`
	Extract        []Extraction `json:"extract,omitempty"`
}

type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type Extraction struct {
	Variable string `json:"variable"`
	Pattern  string `json:"pattern"`
}

// Capture is a screenshot as JPEG.
type Capture struct {
	JPEG []byte `json:"jpeg"`
}

// Offer is a WebRTC peer's session description for a device's stream, with every ICE candidate in it.
type Offer struct {
	Serial   string `json:"serial"`
	Platform string `json:"platform"`
	SDP      string `json:"sdp"`
}

func (o Offer) target() ui.Target {
	return Target{Serial: o.Serial, Platform: o.Platform}.ui()
}

type Answer struct {
	SDP string `json:"sdp"`
}

// Perform asks the hub to send an action's request with the variables filled in.
type Perform struct {
	Action    Action            `json:"action"`
	Variables map[string]string `json:"variables"`
}

// Performed carries the variables an action extracted.
type Performed struct {
	Variables map[string]string `json:"variables"`
}

func newAction(r action.Request) Action {
	headers := make([]Header, len(r.Headers))
	for i, h := range r.Headers {
		headers[i] = Header{Name: h.Name, Value: h.Value}
	}
	extract := make([]Extraction, len(r.Extract))
	for i, e := range r.Extract {
		extract[i] = Extraction{Variable: e.Variable, Pattern: e.Pattern}
	}
	return Action{
		Method: string(r.Method), URL: r.URL, Headers: headers, Body: r.Body, ExpectedStatus: r.ExpectedStatus,
		Extract: extract,
	}
}

func (a Action) request() action.Request {
	headers := make([]action.Header, len(a.Headers))
	for i, h := range a.Headers {
		headers[i] = action.Header{Name: h.Name, Value: h.Value}
	}
	extract := make([]action.Extraction, len(a.Extract))
	for i, e := range a.Extract {
		extract[i] = action.Extraction{Variable: e.Variable, Pattern: e.Pattern}
	}
	return action.Request{
		Method: action.Method(a.Method), URL: a.URL, Headers: headers, Body: a.Body, ExpectedStatus: a.ExpectedStatus,
		Extract: extract,
	}
}

type Outcome struct {
	Status     string        `json:"status"`
	FailedStep int           `json:"failed_step,omitempty"`
	Error      string        `json:"error,omitempty"`
	Steps      []StepOutcome `json:"steps"`
	Video      *Video        `json:"video,omitempty"`
}

// Video tells how long an attempt's uploaded video runs, or why there is none.
type Video struct {
	DurationMS int64  `json:"duration_ms,omitempty"`
	Problem    string `json:"problem,omitempty"`
}

type StepOutcome struct {
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

func newExecution(e run.Execution) Execution {
	steps := make([]Step, len(e.Steps))
	for i, step := range e.Steps {
		steps[i] = Step{
			Action: string(step.Action), Target: step.Target, Description: step.Description,
			ExpectedResult: step.ExpectedResult,
		}
	}
	script := make([]Op, len(e.Script))
	for i, op := range e.Script {
		script[i] = Op{Step: op.Step, Kind: string(op.Kind), Direction: string(op.Direction), Text: op.Text}
		if sel := op.Selector; sel != nil {
			script[i].Selector = &Selector{ID: sel.ID, Text: sel.Text, Label: sel.Label, Index: sel.Index}
		}
	}
	actions := make(map[string]Action, len(e.Actions))
	for name, request := range e.Actions {
		actions[name] = newAction(request)
	}
	execution := Execution{
		ID: e.ID, Serial: e.Serial, Platform: string(e.Platform), Title: e.Title, Steps: steps, Script: script,
		Variables: e.Variables, Actions: actions, TimeoutMS: e.Timeout.Milliseconds(),
	}
	if e.Package != nil {
		execution.Package = &Package{BuildID: e.Package.BuildID, SHA256: e.Package.SHA256, Size: e.Package.Size}
	}
	return execution
}

func (e Execution) run() run.Execution {
	steps := make([]test.Step, len(e.Steps))
	for i, step := range e.Steps {
		steps[i] = test.Step{
			Action: test.Action(step.Action), Target: step.Target, Description: step.Description,
			ExpectedResult: step.ExpectedResult,
		}
	}
	script := make([]ui.Op, len(e.Script))
	for i, op := range e.Script {
		script[i] = ui.Op{Step: op.Step, Kind: ui.Kind(op.Kind), Direction: ui.Direction(op.Direction), Text: op.Text}
		if sel := op.Selector; sel != nil {
			script[i].Selector = &ui.Selector{ID: sel.ID, Text: sel.Text, Label: sel.Label, Index: sel.Index}
		}
	}
	actions := make(map[string]action.Request, len(e.Actions))
	for name, a := range e.Actions {
		actions[name] = a.request()
	}
	execution := run.Execution{
		ID: e.ID, Serial: e.Serial, Platform: project.Platform(e.Platform), Title: e.Title, Steps: steps,
		Script: script, Variables: e.Variables, Actions: actions, Timeout: time.Duration(e.TimeoutMS) * time.Millisecond,
	}
	if e.Package != nil {
		execution.Package = &run.Package{BuildID: e.Package.BuildID, SHA256: e.Package.SHA256, Size: e.Package.Size}
	}
	return execution
}

func newOutcome(o run.Outcome) Outcome {
	steps := make([]StepOutcome, len(o.Steps))
	for i, step := range o.Steps {
		steps[i] = StepOutcome{Status: string(step.Status), Error: step.Error, DurationMS: step.Duration.Milliseconds()}
	}
	outcome := Outcome{Status: string(o.Status), FailedStep: o.FailedStep, Error: o.Error, Steps: steps}
	if o.Video != nil {
		outcome.Video = &Video{DurationMS: o.Video.Duration.Milliseconds(), Problem: o.Video.Problem}
	}
	return outcome
}

func (o Outcome) run() run.Outcome {
	steps := make([]run.StepOutcome, len(o.Steps))
	for i, step := range o.Steps {
		steps[i] = run.StepOutcome{
			Status: run.StepStatus(step.Status), Error: step.Error,
			Duration: time.Duration(step.DurationMS) * time.Millisecond,
		}
	}
	outcome := run.Outcome{Status: run.AttemptStatus(o.Status), FailedStep: o.FailedStep, Error: o.Error, Steps: steps}
	if o.Video != nil {
		outcome.Video = &run.Video{Duration: time.Duration(o.Video.DurationMS) * time.Millisecond, Problem: o.Video.Problem}
	}
	return outcome
}

type Target struct {
	Serial   string `json:"serial"`
	Platform string `json:"platform"`
}

type Rect struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type Element struct {
	ID         string `json:"id,omitempty"`
	Text       string `json:"text,omitempty"`
	Label      string `json:"label,omitempty"`
	Class      string `json:"class,omitempty"`
	Bounds     Rect   `json:"bounds"`
	Clickable  bool   `json:"clickable,omitempty"`
	Editable   bool   `json:"editable,omitempty"`
	Scrollable bool   `json:"scrollable,omitempty"`
	Enabled    bool   `json:"enabled,omitempty"`
	Focused    bool   `json:"focused,omitempty"`
	Checked    bool   `json:"checked,omitempty"`
}

type Screen struct {
	App      string    `json:"app"`
	Width    int       `json:"width"`
	Height   int       `json:"height"`
	Keyboard bool      `json:"keyboard"`
	Elements []Element `json:"elements"`
}

type Command struct {
	Target     Target `json:"target"`
	Kind       string `json:"kind"`
	X          int    `json:"x,omitempty"`
	Y          int    `json:"y,omitempty"`
	ToX        int    `json:"to_x,omitempty"`
	ToY        int    `json:"to_y,omitempty"`
	Text       string `json:"text,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

func newTarget(t ui.Target) Target {
	return Target{Serial: t.Serial, Platform: string(t.Platform)}
}

func (t Target) ui() ui.Target {
	return ui.Target{Serial: t.Serial, Platform: project.Platform(t.Platform)}
}

func newScreen(s ui.Screen) Screen {
	elements := make([]Element, len(s.Elements))
	for i, e := range s.Elements {
		elements[i] = Element{
			ID: e.ID, Text: e.Text, Label: e.Label, Class: e.Class,
			Bounds:    Rect{X: e.Bounds.X, Y: e.Bounds.Y, Width: e.Bounds.Width, Height: e.Bounds.Height},
			Clickable: e.Clickable, Editable: e.Editable, Scrollable: e.Scrollable, Enabled: e.Enabled,
			Focused: e.Focused, Checked: e.Checked,
		}
	}
	return Screen{App: s.App, Width: s.Width, Height: s.Height, Keyboard: s.Keyboard, Elements: elements}
}

func (s Screen) ui() ui.Screen {
	elements := make([]ui.Element, len(s.Elements))
	for i, e := range s.Elements {
		elements[i] = ui.Element{
			ID: e.ID, Text: e.Text, Label: e.Label, Class: e.Class,
			Bounds:    ui.Rect{X: e.Bounds.X, Y: e.Bounds.Y, Width: e.Bounds.Width, Height: e.Bounds.Height},
			Clickable: e.Clickable, Editable: e.Editable, Scrollable: e.Scrollable, Enabled: e.Enabled,
			Focused: e.Focused, Checked: e.Checked,
		}
	}
	return ui.Screen{App: s.App, Width: s.Width, Height: s.Height, Keyboard: s.Keyboard, Elements: elements}
}

func newCommand(c ui.Command) Command {
	return Command{
		Target: newTarget(c.Target), Kind: string(c.Kind), X: c.Point.X, Y: c.Point.Y, ToX: c.To.X, ToY: c.To.Y,
		Text: c.Text, DurationMS: c.Duration.Milliseconds(),
	}
}

func (c Command) ui() ui.Command {
	return ui.Command{
		Target: c.Target.ui(), Kind: ui.Kind(c.Kind), Point: ui.Point{X: c.X, Y: c.Y}, To: ui.Point{X: c.ToX, Y: c.ToY},
		Text: c.Text, Duration: time.Duration(c.DurationMS) * time.Millisecond,
	}
}

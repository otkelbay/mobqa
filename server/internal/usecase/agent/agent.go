// Package agent is the hub's side of the platform: it finds the devices attached to the hub machine and
// carries out what the API sends to them: executions, observations and commands, one at a time per device,
// and the requests of API actions.
package agent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/mobqa/backend/internal/usecase/action"
	"github.com/mobqa/backend/internal/usecase/device"
	"github.com/mobqa/backend/internal/usecase/hub"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/run"
	"github.com/mobqa/backend/internal/usecase/ui"
)

var (
	errNoRunner   = errors.New("this hub cannot run tests yet")
	errNoStreamer = errors.New("this hub cannot stream screens")
)

// UnsupportedError is a request for a platform this hub has no controller for.
type UnsupportedError struct {
	Platform project.Platform
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("this hub cannot drive %s devices", e.Platform)
}

// Discoverer lists the devices one tool can see, such as adb for Android or devicectl for iOS.
type Discoverer interface {
	Discover(ctx context.Context) ([]hub.ReportedDevice, error)
}

// Controller observes and drives the devices of one platform.
type Controller interface {
	Observe(ctx context.Context, serial string) (ui.Screen, error)
	Act(ctx context.Context, command ui.Command) error
}

// Capturer takes screenshots of the devices of one platform, as JPEG.
type Capturer interface {
	Capture(ctx context.Context, serial string) ([]byte, error)
}

// Streamer relays live screens: a phone publishes its device's screen and browsers watch it. Both sides
// offer and get back an answer, as SDP.
type Streamer interface {
	Publish(ctx context.Context, serial, offer string) (string, error)
	Watch(ctx context.Context, serial, offer string) (string, error)
}

// Runner carries out one execution on a device. A test that fails is an outcome, not an error.
type Runner interface {
	Run(ctx context.Context, execution run.Execution) (run.Outcome, error)
}

type Config struct {
	Version     string
	Discoverers []Discoverer
	Controllers map[project.Platform]Controller
	Capturers   map[project.Platform]Capturer
	// Runner is optional; without one the hub reports devices but refuses executions.
	Runner Runner
	Sender action.Sender
	// Streamer is optional; without one the hub refuses live screens.
	Streamer Streamer
}

type Agent struct {
	version     string
	discoverers []Discoverer
	controllers map[project.Platform]Controller
	capturers   map[project.Platform]Capturer
	runner      Runner
	sender      action.Sender
	streamer    Streamer
	// devices lets each device do one thing at a time.
	devices *device.Locks
}

func New(cfg Config) *Agent {
	return &Agent{
		version: cfg.Version, discoverers: cfg.Discoverers, controllers: cfg.Controllers, capturers: cfg.Capturers,
		runner: cfg.Runner, sender: cfg.Sender, streamer: cfg.Streamer, devices: device.NewLocks(),
	}
}

// Inventory asks every discoverer and fails if any does: a partial inventory would mark the missing
// devices disconnected.
func (a *Agent) Inventory(ctx context.Context) (hub.Inventory, error) {
	inventory := hub.Inventory{Version: a.version, Devices: []hub.ReportedDevice{}}
	for _, d := range a.discoverers {
		devices, err := d.Discover(ctx)
		if err != nil {
			return hub.Inventory{}, fmt.Errorf("discover devices: %w", err)
		}
		inventory.Devices = append(inventory.Devices, devices...)
	}
	slices.SortFunc(inventory.Devices, func(a, b hub.ReportedDevice) int { return cmp.Compare(a.Serial, b.Serial) })
	return inventory, nil
}

// Execute runs an execution once the device is free; a device never runs two at once.
func (a *Agent) Execute(ctx context.Context, execution run.Execution) (run.Outcome, error) {
	if a.runner == nil {
		return run.Outcome{}, errNoRunner
	}
	release, err := a.devices.Lock(ctx, execution.Serial)
	if err != nil {
		return run.Outcome{}, err
	}
	defer release()
	return a.runner.Run(ctx, execution)
}

// Observe reads what a device shows once the device is free.
func (a *Agent) Observe(ctx context.Context, target ui.Target) (ui.Screen, error) {
	if err := target.Validate(); err != nil {
		return ui.Screen{}, err
	}
	controller, ok := a.controllers[target.Platform]
	if !ok {
		return ui.Screen{}, &UnsupportedError{Platform: target.Platform}
	}
	release, err := a.devices.Lock(ctx, target.Serial)
	if err != nil {
		return ui.Screen{}, err
	}
	defer release()
	return controller.Observe(ctx, target.Serial)
}

// Act carries out a command once the device is free.
func (a *Agent) Act(ctx context.Context, command ui.Command) error {
	if err := command.Validate(); err != nil {
		return err
	}
	controller, ok := a.controllers[command.Target.Platform]
	if !ok {
		return &UnsupportedError{Platform: command.Target.Platform}
	}
	release, err := a.devices.Lock(ctx, command.Target.Serial)
	if err != nil {
		return err
	}
	defer release()
	return controller.Act(ctx, command)
}

// Capture takes a screenshot of a device once the device is free.
func (a *Agent) Capture(ctx context.Context, target ui.Target) ([]byte, error) {
	if err := target.Validate(); err != nil {
		return nil, err
	}
	capturer, ok := a.capturers[target.Platform]
	if !ok {
		return nil, &UnsupportedError{Platform: target.Platform}
	}
	release, err := a.devices.Lock(ctx, target.Serial)
	if err != nil {
		return nil, err
	}
	defer release()
	return capturer.Capture(ctx, target.Serial)
}

// Perform sends an API action's request; it needs no device, so it waits for none.
func (a *Agent) Perform(ctx context.Context, request action.Request, variables map[string]string,
) (map[string]string, error) {
	return action.Perform(ctx, a.sender, request, variables)
}

// Publish answers a phone's offer to stream its device's screen. Streaming never touches the device through
// its controller, so it waits for no run.
func (a *Agent) Publish(ctx context.Context, target ui.Target, offer string) (string, error) {
	if err := target.Validate(); err != nil {
		return "", err
	}
	if a.streamer == nil {
		return "", errNoStreamer
	}
	return a.streamer.Publish(ctx, target.Serial, offer)
}

// Watch answers a browser's offer to receive a device's screen.
func (a *Agent) Watch(ctx context.Context, target ui.Target, offer string) (string, error) {
	if err := target.Validate(); err != nil {
		return "", err
	}
	if a.streamer == nil {
		return "", errNoStreamer
	}
	return a.streamer.Watch(ctx, target.Serial, offer)
}

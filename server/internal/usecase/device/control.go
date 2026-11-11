package device

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mobqa/backend/internal/clock"
	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/ui"
)

// controlWait is how long a person's command waits for a device that is busy with a run or an authoring
// before it is turned away; a step of a test rarely holds a device longer.
const controlWait = 2 * time.Second

// Hubs reaches devices through their hubs. It reports ErrUnavailable when the hub cannot be reached and a
// *ui.DeviceError when the device could not do what was asked.
type Hubs interface {
	Capture(ctx context.Context, hubID string, target ui.Target) ([]byte, error)
	Act(ctx context.Context, hubID string, command ui.Command) error
}

type ControlDependencies struct {
	Store  store
	Access access
	Hubs   Hubs
	Locks  *Locks
	Clock  clock.Clock
}

// Control lets people watch and drive a device from the browser. It takes the device's lock for each
// look and each command, so a person never acts in the middle of a run's step.
type Control struct {
	deps ControlDependencies
}

func NewControl(deps ControlDependencies) *Control {
	return &Control{deps: deps}
}

// Screen returns a screenshot of the device as JPEG.
func (c *Control) Screen(ctx context.Context, ref Ref) ([]byte, error) {
	d, release, err := c.reach(ctx, ref, project.RoleViewer)
	if err != nil {
		return nil, err
	}
	defer release()
	shot, err := c.deps.Hubs.Capture(ctx, d.HubID, ui.Target{Serial: d.Serial, Platform: d.Platform})
	if err != nil {
		return nil, refusal(err, "take a screenshot")
	}
	return shot, nil
}

// ActInput is a command for a device; its target is filled in from the device.
type ActInput struct {
	Ref     Ref
	Command ui.Command
}

func (c *Control) Act(ctx context.Context, in ActInput) error {
	d, release, err := c.reach(ctx, in.Ref, project.RoleEngineer)
	if err != nil {
		return err
	}
	defer release()
	command := in.Command
	command.Target = ui.Target{Serial: d.Serial, Platform: d.Platform}
	if err := command.Validate(); err != nil {
		return err
	}
	return refusal(c.deps.Hubs.Act(ctx, d.HubID, command), "carry out the command")
}

// reach checks the person's role, loads the device and takes its lock.
func (c *Control) reach(ctx context.Context, ref Ref, need project.Role) (Device, func(), error) {
	if _, err := c.deps.Access.Require(ctx, ref.ProjectID, ref.UserID, need); err != nil {
		return Device{}, nil, err
	}
	d, err := c.deps.Store.Device(ctx, ref.ProjectID, ref.DeviceID)
	if errors.Is(err, fault.ErrNotFound) {
		return Device{}, nil, fault.WithDetail(fault.ErrNotFound, "device not found")
	}
	if err != nil {
		return Device{}, nil, fmt.Errorf("load device: %w", err)
	}
	if d.StatusAt(c.deps.Clock.Now()) != StatusOnline {
		return Device{}, nil, fault.WithDetail(fault.ErrConflict, "the device is not online")
	}
	waitCtx, cancel := context.WithTimeout(ctx, controlWait)
	defer cancel()
	release, err := c.deps.Locks.Lock(waitCtx, d.ID)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return Device{}, nil, fault.WithDetail(fault.ErrConflict, "the device is busy with a run or an authoring")
	}
	if err != nil {
		return Device{}, nil, err
	}
	return d, release, nil
}

// refusal turns what a device could not do into a conflict the person can read.
func refusal(err error, doing string) error {
	var refused *ui.DeviceError
	if errors.As(err, &refused) {
		return fault.WithDetail(fault.ErrConflict, fmt.Sprintf("the device could not %s: %s", doing, refused.Message))
	}
	if err != nil {
		return fmt.Errorf("reach the hub: %w", err)
	}
	return nil
}

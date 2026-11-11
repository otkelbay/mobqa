package device_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/clock/clocktest"
	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/device"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/ui"
)

type roleAccess map[string]project.Role

func (a roleAccess) Require(_ context.Context, _, userID string, need project.Role) (project.Role, error) {
	role, ok := a[userID]
	if !ok {
		return "", fault.ErrNotFound
	}
	if !role.Includes(need) {
		return "", fault.ErrForbidden
	}
	return role, nil
}

// fakeHubs answers for hub-1 and refuses typing, as adb refuses text it cannot type.
type fakeHubs struct {
	commands []ui.Command
	err      error
}

func (h *fakeHubs) Capture(_ context.Context, hubID string, target ui.Target) ([]byte, error) {
	if h.err != nil {
		return nil, h.err
	}
	return []byte(hubID + " " + target.Serial), nil
}

func (h *fakeHubs) Act(_ context.Context, _ string, command ui.Command) error {
	if command.Kind == ui.KindType {
		return &ui.DeviceError{Message: "adb can type only ASCII text"}
	}
	h.commands = append(h.commands, command)
	return nil
}

type controlled struct {
	control *device.Control
	hubs    *fakeHubs
	locks   *device.Locks
}

func newControl() controlled {
	online := testStart().Add(time.Minute)
	store := &fakeStore{devices: []device.Device{
		{ID: "pixel", ProjectID: projID, HubID: "hub-1", Serial: "R58", Platform: project.PlatformAndroid,
			State: device.StateConnected, HubOnlineUntil: &online},
		{ID: "unplugged", ProjectID: projID, HubID: "hub-1", Serial: "R59", Platform: project.PlatformAndroid,
			State: device.StateDisconnected, HubOnlineUntil: &online},
	}}
	c := controlled{hubs: &fakeHubs{}, locks: device.NewLocks()}
	c.control = device.NewControl(device.ControlDependencies{
		Store: store, Access: roleAccess{"viewer": project.RoleViewer, "engineer": project.RoleEngineer},
		Hubs: c.hubs, Locks: c.locks, Clock: clocktest.New(testStart()),
	})
	return c
}

func TestControlShowsAndDrivesTheDevice(t *testing.T) {
	c := newControl()
	ctx := context.Background()
	tap := ui.Command{Kind: ui.KindTap, Point: ui.Point{X: 10, Y: 20}}

	shot, err := c.control.Screen(ctx, device.Ref{UserID: "viewer", ProjectID: projID, DeviceID: "pixel"})
	if err != nil || string(shot) != "hub-1 R58" {
		t.Fatalf("Screen = %q, %v", shot, err)
	}
	err = c.control.Act(ctx, device.ActInput{Ref: device.Ref{UserID: "engineer", ProjectID: projID,
		DeviceID: "pixel"}, Command: tap})
	want := ui.Command{Target: ui.Target{Serial: "R58", Platform: project.PlatformAndroid}, Kind: ui.KindTap,
		Point: ui.Point{X: 10, Y: 20}}
	if err != nil || len(c.hubs.commands) != 1 || c.hubs.commands[0] != want {
		t.Fatalf("Act = %v, commands %+v", err, c.hubs.commands)
	}
}

func TestControlTurnsAwayWhatItCannotDo(t *testing.T) {
	ctx := context.Background()
	pixel := device.Ref{UserID: "engineer", ProjectID: projID, DeviceID: "pixel"}
	tests := []struct {
		name    string
		ref     device.Ref
		command ui.Command
		kind    error
		detail  string
	}{
		{name: "viewer", ref: device.Ref{UserID: "viewer", ProjectID: projID, DeviceID: "pixel"},
			command: ui.Command{Kind: ui.KindBack}, kind: fault.ErrForbidden},
		{name: "offline", ref: device.Ref{UserID: "engineer", ProjectID: projID, DeviceID: "unplugged"},
			command: ui.Command{Kind: ui.KindBack}, kind: fault.ErrConflict, detail: "the device is not online"},
		{name: "missing", ref: device.Ref{UserID: "engineer", ProjectID: projID, DeviceID: "gone"},
			command: ui.Command{Kind: ui.KindBack}, kind: fault.ErrNotFound, detail: "device not found"},
		{name: "invalid", ref: pixel, command: ui.Command{Kind: "shake"}, kind: fault.ErrInvalidInput,
			detail: "unknown command kind"},
		{name: "refused", ref: pixel, command: ui.Command{Kind: ui.KindType, Text: "Привет"}, kind: fault.ErrConflict,
			detail: "the device could not carry out the command: adb can type only ASCII text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newControl()

			err := c.control.Act(ctx, device.ActInput{Ref: tt.ref, Command: tt.command})

			detail, _ := fault.Detail(err)
			if !errors.Is(err, tt.kind) || detail != tt.detail || len(c.hubs.commands) != 0 {
				t.Fatalf("Act = %v", err)
			}
		})
	}
}

func TestControlWaitsBrieflyForABusyDevice(t *testing.T) {
	c := newControl()
	release, err := c.locks.Lock(context.Background(), "pixel")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	_, err = c.control.Screen(context.Background(), device.Ref{UserID: "viewer", ProjectID: projID, DeviceID: "pixel"})

	if detail, _ := fault.Detail(err); !errors.Is(err, fault.ErrConflict) ||
		detail != "the device is busy with a run or an authoring" {
		t.Fatalf("Screen = %v", err)
	}
}

func TestControlPassesOnAnUnreachableHub(t *testing.T) {
	c := newControl()
	c.hubs.err = fault.WithDetail(fault.ErrUnavailable, "the hub is not connected")

	_, err := c.control.Screen(context.Background(), device.Ref{UserID: "viewer", ProjectID: projID, DeviceID: "pixel"})

	if !errors.Is(err, fault.ErrUnavailable) {
		t.Fatalf("Screen = %v", err)
	}
}

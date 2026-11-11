// Package device lists the phones a project's hubs report. A device is usable only while its hub is
// online and the hub sees it connected, so its status is derived on every read rather than stored.
package device

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mobqa/backend/internal/clock"
	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/project"
)

// State is what the hub last reported about a device.
type State string

const (
	StateConnected State = "connected"
	// StateUnauthorized is an Android device that has not accepted the hub's debugging key.
	StateUnauthorized State = "unauthorized"
	// StateDisconnected is a device missing from the hub's latest inventory.
	StateDisconnected State = "disconnected"
)

// Status is whether a device can take work now.
type Status string

const (
	StatusOnline       Status = "online"
	StatusUnauthorized Status = "unauthorized"
	StatusOffline      Status = "offline"
)

type Device struct {
	ID        string
	ProjectID string
	HubID     string
	HubName   string
	// Serial is the hub's own id for the device: an adb serial or an iOS UDID.
	Serial         string
	Name           string
	Model          string
	Platform       project.Platform
	OSVersion      string
	Physical       bool
	State          State
	Status         Status
	HubOnlineUntil *time.Time
	LastSeenAt     time.Time
}

// StatusAt derives whether the device can take work at now from its hub's presence and its reported state.
func (d Device) StatusAt(now time.Time) Status {
	if d.HubOnlineUntil == nil || !now.Before(*d.HubOnlineUntil) {
		return StatusOffline
	}
	switch d.State {
	case StateConnected:
		return StatusOnline
	case StateUnauthorized:
		return StatusUnauthorized
	default:
		return StatusOffline
	}
}

type Query struct {
	ProjectID string
	Platform  project.Platform
}

type store interface {
	Devices(ctx context.Context, query Query) ([]Device, error)
	Device(ctx context.Context, projectID, id string) (Device, error)
}

type access interface {
	Require(ctx context.Context, projectID, userID string, need project.Role) (project.Role, error)
}

type Service struct {
	store  store
	access access
	clock  clock.Clock
}

func NewService(devices store, projects access, now clock.Clock) *Service {
	return &Service{store: devices, access: projects, clock: now}
}

type ListInput struct {
	UserID    string
	ProjectID string
	Platform  project.Platform
}

func (in ListInput) Validate() error {
	if in.Platform != "" && in.Platform != project.PlatformAndroid && in.Platform != project.PlatformIOS {
		return fault.WithDetail(fault.ErrInvalidInput, "platform must be android or ios")
	}
	return nil
}

// List returns the devices of all the project's hubs, ordered by hub and name.
func (s *Service) List(ctx context.Context, in ListInput) ([]Device, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	if _, err := s.access.Require(ctx, in.ProjectID, in.UserID, project.RoleViewer); err != nil {
		return nil, err
	}
	devices, err := s.store.Devices(ctx, Query{ProjectID: in.ProjectID, Platform: in.Platform})
	if err != nil {
		return nil, fmt.Errorf("list devices: %w", err)
	}
	now := s.clock.Now()
	for i := range devices {
		devices[i].Status = devices[i].StatusAt(now)
	}
	return devices, nil
}

type Ref struct {
	UserID    string
	ProjectID string
	DeviceID  string
}

func (s *Service) Get(ctx context.Context, ref Ref) (Device, error) {
	if _, err := s.access.Require(ctx, ref.ProjectID, ref.UserID, project.RoleViewer); err != nil {
		return Device{}, err
	}
	found, err := s.store.Device(ctx, ref.ProjectID, ref.DeviceID)
	if errors.Is(err, fault.ErrNotFound) {
		return Device{}, fault.WithDetail(fault.ErrNotFound, "device not found")
	}
	if err != nil {
		return Device{}, fmt.Errorf("load device: %w", err)
	}
	found.Status = found.StatusAt(s.clock.Now())
	return found, nil
}

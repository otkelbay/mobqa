// Package hub registers the machines phones are attached to. A hub signs in with a token shown once at
// registration, stays online through heartbeats and reports the devices it sees, which become the
// project's device list.
package hub

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/device"
	"github.com/mobqa/backend/internal/usecase/project"
)

const (
	// HeartbeatInterval is how often a connected hub confirms it is alive.
	HeartbeatInterval = 15 * time.Second
	// presenceTTL tolerates two missed heartbeats before a hub counts as offline.
	presenceTTL = 3 * HeartbeatInterval

	maxNameLength    = 60
	maxVersionLength = 60
	maxDevices       = 64
	maxSerialLength  = 128
	maxLabelLength   = 120
)

type Hub struct {
	ID          string
	ProjectID   string
	Name        string
	Version     string
	CreatedBy   string
	CreatedAt   time.Time
	LastSeenAt  *time.Time
	OnlineUntil *time.Time
	Online      bool
}

func (h Hub) online(now time.Time) bool {
	return h.OnlineUntil != nil && now.Before(*h.OnlineUntil)
}

// Registration carries a hub's token, which is shown only here and stored as a hash.
type Registration struct {
	Hub   Hub
	Token string
}

// Presence moves a hub's online window: a heartbeat extends it, a disconnect closes it.
type Presence struct {
	SeenAt      time.Time
	OnlineUntil time.Time
}

type ReportedDevice struct {
	Serial    string
	Name      string
	Model     string
	Platform  project.Platform
	OSVersion string
	Physical  bool
	State     device.State
}

func (d ReportedDevice) validate() error {
	if d.Serial == "" || len(d.Serial) > maxSerialLength {
		return fault.WithDetail(fault.ErrInvalidInput, "device serials must be 1 to 128 bytes")
	}
	if d.Platform != project.PlatformAndroid && d.Platform != project.PlatformIOS {
		return fault.WithDetail(fault.ErrInvalidInput, "device platform must be android or ios")
	}
	if d.State != device.StateConnected && d.State != device.StateUnauthorized {
		return fault.WithDetail(fault.ErrInvalidInput, "device state must be connected or unauthorized")
	}
	for _, label := range []string{d.Name, d.Model, d.OSVersion} {
		if utf8.RuneCountInString(label) > maxLabelLength {
			return fault.WithDetail(fault.ErrInvalidInput, "device labels are limited to 120 characters")
		}
	}
	return nil
}

// Inventory is the full set of devices a hub sees; devices it no longer lists become disconnected.
type Inventory struct {
	Version string
	Devices []ReportedDevice
}

func (inv Inventory) Validate() error {
	if utf8.RuneCountInString(inv.Version) > maxVersionLength {
		return fault.WithDetail(fault.ErrInvalidInput, "the hub version is limited to 60 characters")
	}
	if len(inv.Devices) > maxDevices {
		return fault.WithDetail(fault.ErrInvalidInput, "a hub reports at most 64 devices")
	}
	seen := make(map[string]bool, len(inv.Devices))
	for _, d := range inv.Devices {
		if err := d.validate(); err != nil {
			return err
		}
		if seen[d.Serial] {
			return fault.WithDetail(fault.ErrInvalidInput, "device serials must be unique")
		}
		seen[d.Serial] = true
	}
	return nil
}

func validateName(name string) error {
	if name == "" || strings.TrimSpace(name) != name || utf8.RuneCountInString(name) > maxNameLength {
		return fault.WithDetail(fault.ErrInvalidInput, "hub names must be 1 to 60 characters without outer spaces")
	}
	return nil
}

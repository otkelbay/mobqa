// Package devicectl discovers iOS devices through Xcode's devicectl, which writes its listing as JSON to
// a file.
package devicectl

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/mobqa/backend/internal/usecase/device"
	"github.com/mobqa/backend/internal/usecase/hub"
	"github.com/mobqa/backend/internal/usecase/project"
)

// commandTimeout is generous because devicectl probes network-paired devices before answering.
const commandTimeout = 30 * time.Second

type lister interface {
	list(ctx context.Context) ([]byte, error)
}

type xcrun struct{}

func (xcrun) list(ctx context.Context) ([]byte, error) {
	file, err := os.CreateTemp("", "devicectl-*.json")
	if err != nil {
		return nil, fmt.Errorf("create devicectl output: %w", err)
	}
	path := file.Name()
	_ = file.Close()
	defer func() { _ = os.Remove(path) }()
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "xcrun", "devicectl", "list", "devices", "--quiet", "--json-output", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("devicectl list devices: %w: %s", err, out)
	}
	listing, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read devicectl output: %w", err)
	}
	return listing, nil
}

type Discoverer struct {
	devices lister
}

func New() *Discoverer {
	return &Discoverer{devices: xcrun{}}
}

type listing struct {
	Result struct {
		Devices []struct {
			Hardware struct {
				UDID          string `json:"udid"`
				Platform      string `json:"platform"`
				Reality       string `json:"reality"`
				MarketingName string `json:"marketingName"`
			} `json:"hardwareProperties"`
			Properties struct {
				Name      string `json:"name"`
				OSVersion string `json:"osVersionNumber"`
			} `json:"deviceProperties"`
			Connection struct {
				PairingState string `json:"pairingState"`
				TunnelState  string `json:"tunnelState"`
			} `json:"connectionProperties"`
		} `json:"devices"`
	} `json:"result"`
}

// Discover lists reachable iPhones and iPads. devicectl remembers every device ever paired; those it
// cannot reach report the tunnel state "unavailable" and are left out.
func (d *Discoverer) Discover(ctx context.Context) ([]hub.ReportedDevice, error) {
	raw, err := d.devices.list(ctx)
	if err != nil {
		return nil, err
	}
	var parsed listing
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("decode devicectl output: %w", err)
	}
	devices := []hub.ReportedDevice{}
	for _, d := range parsed.Result.Devices {
		if d.Hardware.Platform != "iOS" || d.Hardware.UDID == "" || d.Connection.TunnelState == "unavailable" {
			continue
		}
		state := device.StateConnected
		if d.Connection.PairingState != "paired" {
			state = device.StateUnauthorized
		}
		devices = append(devices, hub.ReportedDevice{
			Serial: d.Hardware.UDID, Name: d.Properties.Name, Model: d.Hardware.MarketingName,
			Platform: project.PlatformIOS, OSVersion: d.Properties.OSVersion, Physical: d.Hardware.Reality == "physical",
			State: state,
		})
	}
	return devices, nil
}

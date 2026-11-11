// Package adb discovers Android devices through the Android Debug Bridge.
package adb

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/mobqa/backend/internal/usecase/device"
	"github.com/mobqa/backend/internal/usecase/hub"
	"github.com/mobqa/backend/internal/usecase/project"
)

const (
	// commandTimeout bounds one adb call; a wedged adb server must not stall the hub's inventory.
	commandTimeout = 15 * time.Second
	// installTimeout covers pushing a large build over USB.
	installTimeout = 5 * time.Minute
)

type runner interface {
	output(ctx context.Context, args ...string) ([]byte, error)
}

type execRunner struct {
	path    string
	timeout time.Duration
}

// output runs adb and, when it fails, reports what adb printed on stderr, which names the cause.
func (r execRunner) output(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, r.path, args...).Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(bytes.TrimSpace(exit.Stderr)) > 0 {
		return nil, fmt.Errorf("adb %s: %w: %s", args[0], err, bytes.TrimSpace(exit.Stderr))
	}
	if err != nil {
		return nil, fmt.Errorf("adb %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

type Discoverer struct {
	run runner
}

// New uses the adb binary at path.
func New(path string) *Discoverer {
	return &Discoverer{run: execRunner{path: path, timeout: commandTimeout}}
}

// Discover lists connected and unauthorized devices; devices adb reports offline are left out, so the
// API marks them disconnected.
func (d *Discoverer) Discover(ctx context.Context) ([]hub.ReportedDevice, error) {
	out, err := d.run.output(ctx, "devices", "-l")
	if err != nil {
		return nil, err
	}
	devices := parseDevices(out)
	for i := range devices {
		if devices[i].State != device.StateConnected {
			continue
		}
		release, err := d.run.output(ctx, "-s", devices[i].Serial, "shell", "getprop", "ro.build.version.release")
		if err != nil {
			return nil, err
		}
		devices[i].OSVersion = strings.TrimSpace(string(release))
	}
	return devices, nil
}

// parseDevices reads `adb devices -l`, whose lines look like
// "R58M123 device usb:1-1 product:a52q model:SM_A525F device:a52q transport_id:1".
func parseDevices(out []byte) []hub.ReportedDevice {
	devices := []hub.ReportedDevice{}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 || fields[0] == "List" {
			continue
		}
		serial, state := fields[0], fields[1]
		reported := hub.ReportedDevice{
			Serial: serial, Platform: project.PlatformAndroid, Physical: !strings.HasPrefix(serial, "emulator-"),
		}
		switch state {
		case "device":
			reported.State = device.StateConnected
		case "unauthorized":
			reported.State = device.StateUnauthorized
		default:
			continue
		}
		for _, field := range fields[2:] {
			if model, ok := strings.CutPrefix(field, "model:"); ok {
				reported.Model = strings.ReplaceAll(model, "_", " ")
			}
		}
		reported.Name = reported.Model
		devices = append(devices, reported)
	}
	return devices
}

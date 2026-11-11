package adb

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mobqa/backend/internal/usecase/device"
	"github.com/mobqa/backend/internal/usecase/hub"
	"github.com/mobqa/backend/internal/usecase/project"
)

const listing = `List of devices attached
R58M123ABC             device usb:1-1 product:a52qnsxx model:SM_A525F device:a52q transport_id:1
emulator-5554          device product:sdk_gphone64_arm64 model:sdk_gphone64_arm64 device:emu64a transport_id:2
0123456789ABCDEF       unauthorized usb:1-2 transport_id:3
ZY22GHK                offline transport_id:4

`

type fakeRunner struct {
	releases map[string]string
	calls    []string
}

func (f *fakeRunner) output(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, strings.Join(args, " "))
	if args[0] == "devices" {
		return []byte(listing), nil
	}
	release, ok := f.releases[args[1]]
	if !ok {
		return nil, errors.New("device not found")
	}
	return []byte(release + "\n"), nil
}

func TestDiscoverReadsStatesModelsAndVersions(t *testing.T) {
	run := &fakeRunner{releases: map[string]string{"R58M123ABC": "14", "emulator-5554": "15"}}

	devices, err := (&Discoverer{run: run}).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	want := []hub.ReportedDevice{
		{
			Serial: "R58M123ABC", Name: "SM A525F", Model: "SM A525F", Platform: project.PlatformAndroid,
			OSVersion: "14", Physical: true, State: device.StateConnected,
		},
		{
			Serial: "emulator-5554", Name: "sdk gphone64 arm64", Model: "sdk gphone64 arm64",
			Platform: project.PlatformAndroid, OSVersion: "15", State: device.StateConnected,
		},
		{Serial: "0123456789ABCDEF", Platform: project.PlatformAndroid, Physical: true, State: device.StateUnauthorized},
	}
	if len(devices) != len(want) {
		t.Fatalf("devices = %+v", devices)
	}
	for i := range want {
		if devices[i] != want[i] {
			t.Errorf("device %d = %+v, want %+v", i, devices[i], want[i])
		}
	}
	if len(run.calls) != 3 {
		t.Fatalf("adb calls = %q, want the listing and one getprop per connected device", run.calls)
	}
}

func TestDiscoverFailsWhenVersionLookupFails(t *testing.T) {
	run := &fakeRunner{releases: map[string]string{"R58M123ABC": "14"}}

	if _, err := (&Discoverer{run: run}).Discover(context.Background()); err == nil {
		t.Fatal("Discover succeeded although a device did not answer")
	}
}

func TestParseDevicesWithNoDevices(t *testing.T) {
	if devices := parseDevices([]byte("List of devices attached\n\n")); devices == nil || len(devices) != 0 {
		t.Fatalf("devices = %+v, want an empty list", devices)
	}
}

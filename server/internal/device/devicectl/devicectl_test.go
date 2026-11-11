package devicectl

import (
	"context"
	"testing"

	"github.com/mobqa/backend/internal/usecase/device"
	"github.com/mobqa/backend/internal/usecase/hub"
	"github.com/mobqa/backend/internal/usecase/project"
)

const output = `{"info": {"jsonVersion": 3, "outcome": "success"}, "result": {"devices": [
  {"hardwareProperties": {"udid": "00008120-000A1B2C3D4E5F60", "platform": "iOS", "reality": "physical",
     "marketingName": "iPhone 15"},
   "deviceProperties": {"name": "Test iPhone", "osVersionNumber": "18.1"},
   "connectionProperties": {"pairingState": "paired", "tunnelState": "disconnected"}},
  {"hardwareProperties": {"udid": "00008030-0011223344556677", "platform": "iOS", "reality": "physical",
     "marketingName": "iPad Air"},
   "deviceProperties": {"name": "Lab iPad", "osVersionNumber": "17.5"},
   "connectionProperties": {"pairingState": "unpaired", "tunnelState": "connected"}},
  {"hardwareProperties": {"udid": "00008110-AAAABBBBCCCCDDDD", "platform": "iOS", "reality": "physical",
     "marketingName": "iPhone 13"},
   "deviceProperties": {"name": "Drawer iPhone", "osVersionNumber": "16.0"},
   "connectionProperties": {"pairingState": "paired", "tunnelState": "unavailable"}},
  {"hardwareProperties": {"udid": "00008006-0000000000000001", "platform": "watchOS", "reality": "physical",
     "marketingName": "Apple Watch"},
   "deviceProperties": {"name": "Watch", "osVersionNumber": "11.0"},
   "connectionProperties": {"pairingState": "paired", "tunnelState": "connected"}}
]}}`

type fakeLister struct {
	raw string
}

func (f fakeLister) list(context.Context) ([]byte, error) {
	return []byte(f.raw), nil
}

func TestDiscoverKeepsReachableIOSDevices(t *testing.T) {
	devices, err := (&Discoverer{devices: fakeLister{raw: output}}).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	want := []hub.ReportedDevice{
		{
			Serial: "00008120-000A1B2C3D4E5F60", Name: "Test iPhone", Model: "iPhone 15", Platform: project.PlatformIOS,
			OSVersion: "18.1", Physical: true, State: device.StateConnected,
		},
		{
			Serial: "00008030-0011223344556677", Name: "Lab iPad", Model: "iPad Air", Platform: project.PlatformIOS,
			OSVersion: "17.5", Physical: true, State: device.StateUnauthorized,
		},
	}
	if len(devices) != len(want) {
		t.Fatalf("devices = %+v", devices)
	}
	for i := range want {
		if devices[i] != want[i] {
			t.Errorf("device %d = %+v, want %+v", i, devices[i], want[i])
		}
	}
}

func TestDiscoverRejectsMalformedOutput(t *testing.T) {
	if _, err := (&Discoverer{devices: fakeLister{raw: "not json"}}).Discover(context.Background()); err == nil {
		t.Fatal("Discover accepted malformed output")
	}
}

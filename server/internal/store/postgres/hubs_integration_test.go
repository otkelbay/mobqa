//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/store/postgres"
	"github.com/mobqa/backend/internal/usecase/device"
	"github.com/mobqa/backend/internal/usecase/hub"
	"github.com/mobqa/backend/internal/usecase/project"
)

type hubFixture struct {
	projectFixture
	hubs      *postgres.Hubs
	devices   *postgres.Devices
	projectID string
	userID    string
}

func newHubs(t *testing.T) hubFixture {
	t.Helper()
	f := newProjects(t)
	owner := f.account(t, "ada")
	return hubFixture{
		projectFixture: f, hubs: postgres.NewHubs(f.db), devices: postgres.NewDevices(f.db),
		projectID: f.create(t, owner.ID, "Shop").ID, userID: owner.ID,
	}
}

func (f hubFixture) register(t *testing.T, projectID, token string) hub.Hub {
	t.Helper()
	created, err := f.hubs.CreateHub(context.Background(), hub.Hub{
		ID: uuid.Must(uuid.NewV7()).String(), ProjectID: projectID, Name: "Office Mac", CreatedBy: f.userID,
	}, []byte(token))
	if err != nil {
		t.Fatalf("CreateHub: %v", err)
	}
	return created
}

func (f hubFixture) report(t *testing.T, hubID string, devices ...hub.ReportedDevice) {
	t.Helper()
	now := f.now.Now()
	presence := hub.Presence{SeenAt: now, OnlineUntil: now.Add(time.Minute)}
	err := f.hubs.ReplaceInventory(context.Background(), hubID, hub.Inventory{Version: "1.2.0", Devices: devices},
		presence)
	if err != nil {
		t.Fatalf("ReplaceInventory: %v", err)
	}
}

func reported(serial string, platform project.Platform) hub.ReportedDevice {
	return hub.ReportedDevice{
		Serial: serial, Name: serial + " phone", Model: "Model " + serial, Platform: platform, OSVersion: "17",
		Physical: true, State: device.StateConnected,
	}
}

func TestHubsTokenLifecycle(t *testing.T) {
	f := newHubs(t)
	created := f.register(t, f.projectID, "token-1")

	found, err := f.hubs.HubByToken(context.Background(), []byte("token-1"))
	if err != nil || found.ID != created.ID || found.Version != "" || found.OnlineUntil != nil {
		t.Fatalf("HubByToken = %+v, %v", found, err)
	}
	if _, err := f.hubs.SetTokenHash(context.Background(), f.projectID, created.ID, []byte("token-2")); err != nil {
		t.Fatalf("SetTokenHash: %v", err)
	}
	if _, err := f.hubs.HubByToken(context.Background(), []byte("token-1")); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("HubByToken(old) error = %v, want ErrNotFound", err)
	}
	if err := f.hubs.DeleteHub(context.Background(), f.projectID, created.ID); err != nil {
		t.Fatalf("DeleteHub: %v", err)
	}
	if err := f.hubs.DeleteHub(context.Background(), f.projectID, created.ID); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("DeleteHub(again) error = %v, want ErrNotFound", err)
	}
}

func TestHubsNameUniquePerProject(t *testing.T) {
	f := newHubs(t)
	other := f.create(t, f.userID, "Bank").ID
	f.register(t, f.projectID, "token-1")
	f.register(t, other, "token-2")

	_, err := f.hubs.CreateHub(context.Background(), hub.Hub{
		ID: uuid.Must(uuid.NewV7()).String(), ProjectID: f.projectID, Name: "Office Mac", CreatedBy: f.userID,
	}, []byte("token-3"))

	if !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("CreateHub(duplicate) error = %v, want ErrConflict", err)
	}
}

func TestHubsInventoryReconciles(t *testing.T) {
	f := newHubs(t)
	mac := f.register(t, f.projectID, "token-1")
	f.report(t, mac.ID, reported("pixel", project.PlatformAndroid), reported("iphone", project.PlatformIOS))
	first, err := f.devices.Devices(context.Background(), device.Query{ProjectID: f.projectID})
	if err != nil || len(first) != 2 {
		t.Fatalf("Devices = %+v, %v", first, err)
	}

	f.now.Advance(time.Minute)
	f.report(t, mac.ID, reported("iphone", project.PlatformIOS))

	second, err := f.devices.Devices(context.Background(), device.Query{ProjectID: f.projectID})
	if err != nil || len(second) != 2 {
		t.Fatalf("Devices = %+v, %v", second, err)
	}
	byName := map[string]device.Device{}
	for _, d := range second {
		byName[d.Serial] = d
	}
	iphone, pixel := byName["iphone"], byName["pixel"]
	if iphone.ID != first[0].ID || iphone.State != device.StateConnected || !iphone.LastSeenAt.Equal(f.now.Now()) {
		t.Fatalf("iphone = %+v, want the same row, connected and seen now", iphone)
	}
	if pixel.State != device.StateDisconnected || iphone.HubName != "Office Mac" || iphone.HubOnlineUntil == nil {
		t.Fatalf("pixel = %+v, iphone = %+v", pixel, iphone)
	}

	f.report(t, mac.ID)
	none, err := f.devices.Devices(context.Background(),
		device.Query{ProjectID: f.projectID, Platform: project.PlatformIOS})
	if err != nil || len(none) != 1 || none[0].State != device.StateDisconnected {
		t.Fatalf("Devices after empty inventory = %+v, %v", none, err)
	}
	hubs, err := f.hubs.Hubs(context.Background(), f.projectID)
	if err != nil || len(hubs) != 1 || hubs[0].Version != "1.2.0" || hubs[0].LastSeenAt == nil {
		t.Fatalf("Hubs = %+v, %v", hubs, err)
	}
}

func TestHubsPresenceAndScope(t *testing.T) {
	f := newHubs(t)
	other := f.create(t, f.userID, "Bank").ID
	mac := f.register(t, f.projectID, "token-1")
	f.report(t, mac.ID, reported("pixel", project.PlatformAndroid))

	closed := hub.Presence{SeenAt: f.now.Now(), OnlineUntil: f.now.Now()}
	if err := f.hubs.SetPresence(context.Background(), mac.ID, closed); err != nil {
		t.Fatalf("SetPresence: %v", err)
	}
	missing := uuid.Must(uuid.NewV7()).String()
	if err := f.hubs.SetPresence(context.Background(), missing, closed); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("SetPresence(missing) error = %v, want ErrNotFound", err)
	}

	devices, err := f.devices.Devices(context.Background(), device.Query{ProjectID: f.projectID})
	if err != nil || len(devices) != 1 || !devices[0].HubOnlineUntil.Equal(f.now.Now()) {
		t.Fatalf("Devices = %+v, %v", devices, err)
	}
	if _, err := f.devices.Device(context.Background(), other, devices[0].ID); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("Device(other project) error = %v, want ErrNotFound", err)
	}
	if _, err := f.devices.Device(context.Background(), f.projectID, "nope"); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("Device(malformed id) error = %v, want ErrNotFound", err)
	}
	if err := f.hubs.DeleteHub(context.Background(), f.projectID, mac.ID); err != nil {
		t.Fatalf("DeleteHub: %v", err)
	}
	if left, _ := f.devices.Devices(context.Background(), device.Query{ProjectID: f.projectID}); len(left) != 0 {
		t.Fatalf("devices left after hub deletion = %+v", left)
	}
}

func TestDevicesStreamKeys(t *testing.T) {
	f := newHubs(t)
	other := f.create(t, f.userID, "Bank").ID
	mac := f.register(t, f.projectID, "token-1")
	f.report(t, mac.ID, reported("iphone", project.PlatformIOS), reported("pixel", project.PlatformAndroid))
	devices, err := f.devices.Devices(context.Background(), device.Query{ProjectID: f.projectID})
	if err != nil || len(devices) != 2 {
		t.Fatalf("Devices = %+v, %v", devices, err)
	}
	iphone := devices[0]

	updated, err := f.devices.SetStreamKeyHash(context.Background(), f.projectID, iphone.ID, []byte("key-1"))
	if err != nil || updated.ID != iphone.ID || updated.HubName != "Office Mac" {
		t.Fatalf("SetStreamKeyHash = %+v, %v", updated, err)
	}
	f.report(t, mac.ID, reported("iphone", project.PlatformIOS))
	found, err := f.devices.DeviceByStreamKey(context.Background(), []byte("key-1"))
	if err != nil || found.ID != iphone.ID || found.ProjectID != f.projectID {
		t.Fatalf("DeviceByStreamKey after a new inventory = %+v, %v", found, err)
	}
	if _, err := f.devices.SetStreamKeyHash(context.Background(), f.projectID, iphone.ID, []byte("key-2")); err != nil {
		t.Fatalf("SetStreamKeyHash again: %v", err)
	}
	if _, err := f.devices.DeviceByStreamKey(context.Background(), []byte("key-1")); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("DeviceByStreamKey(old key) error = %v, want ErrNotFound", err)
	}
	if _, err := f.devices.SetStreamKeyHash(context.Background(), other, iphone.ID, []byte("key-3")); !errors.Is(err,
		fault.ErrNotFound) {
		t.Fatalf("SetStreamKeyHash(other project) error = %v, want ErrNotFound", err)
	}
}

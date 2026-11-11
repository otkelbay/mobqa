package device_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/clock/clocktest"
	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/device"
	"github.com/mobqa/backend/internal/usecase/project"
)

const projID = "project-1"

type fakeAccess struct{}

func (fakeAccess) Require(_ context.Context, projectID, userID string, _ project.Role) (project.Role, error) {
	if userID != "viewer" || projectID != projID {
		return "", fault.ErrNotFound
	}
	return project.RoleViewer, nil
}

type fakeStore struct {
	devices []device.Device
	query   device.Query
	// keys holds each device's stream key hash.
	keys map[string][]byte
}

func (s *fakeStore) Devices(_ context.Context, query device.Query) ([]device.Device, error) {
	s.query = query
	return append([]device.Device(nil), s.devices...), nil
}

func (s *fakeStore) Device(_ context.Context, projectID, id string) (device.Device, error) {
	for _, d := range s.devices {
		if d.ProjectID == projectID && d.ID == id {
			return d, nil
		}
	}
	return device.Device{}, fault.ErrNotFound
}

func (s *fakeStore) SetStreamKeyHash(ctx context.Context, projectID, id string, hash []byte) (device.Device, error) {
	found, err := s.Device(ctx, projectID, id)
	if err != nil {
		return device.Device{}, err
	}
	if s.keys == nil {
		s.keys = map[string][]byte{}
	}
	s.keys[id] = hash
	return found, nil
}

func (s *fakeStore) DeviceByStreamKey(_ context.Context, hash []byte) (device.Device, error) {
	for _, d := range s.devices {
		if bytes.Equal(s.keys[d.ID], hash) {
			return d, nil
		}
	}
	return device.Device{}, fault.ErrNotFound
}

func testStart() time.Time {
	return time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
}

func TestListDerivesStatus(t *testing.T) {
	now := testStart()
	online, lapsed := now.Add(time.Minute), now.Add(-time.Second)
	store := &fakeStore{devices: []device.Device{
		{ID: "connected", State: device.StateConnected, HubOnlineUntil: &online},
		{ID: "unauthorized", State: device.StateUnauthorized, HubOnlineUntil: &online},
		{ID: "unplugged", State: device.StateDisconnected, HubOnlineUntil: &online},
		{ID: "hub lapsed", State: device.StateConnected, HubOnlineUntil: &lapsed},
		{ID: "hub never connected", State: device.StateConnected},
	}}
	svc := device.NewService(store, fakeAccess{}, clocktest.New(now))

	devices, err := svc.List(context.Background(),
		device.ListInput{UserID: "viewer", ProjectID: projID, Platform: project.PlatformIOS})
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	want := map[string]device.Status{
		"connected": device.StatusOnline, "unauthorized": device.StatusUnauthorized,
		"unplugged": device.StatusOffline, "hub lapsed": device.StatusOffline,
		"hub never connected": device.StatusOffline,
	}
	for _, d := range devices {
		if d.Status != want[d.ID] {
			t.Errorf("status of %q = %q, want %q", d.ID, d.Status, want[d.ID])
		}
	}
	if store.query != (device.Query{ProjectID: projID, Platform: project.PlatformIOS}) {
		t.Fatalf("query = %+v", store.query)
	}
}

func TestListRejections(t *testing.T) {
	tests := []struct {
		name string
		in   device.ListInput
		want error
	}{
		{
			name: "platform",
			in:   device.ListInput{UserID: "viewer", ProjectID: projID, Platform: "web"}, want: fault.ErrInvalidInput,
		},
		{name: "outsider", in: device.ListInput{UserID: "stranger", ProjectID: projID}, want: fault.ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := device.NewService(&fakeStore{}, fakeAccess{}, clocktest.New(testStart()))

			_, err := svc.List(context.Background(), tt.in)

			if !errors.Is(err, tt.want) {
				t.Fatalf("List error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestGet(t *testing.T) {
	online := testStart().Add(time.Minute)
	store := &fakeStore{devices: []device.Device{
		{ID: "device-1", ProjectID: projID, State: device.StateConnected, HubOnlineUntil: &online},
	}}
	svc := device.NewService(store, fakeAccess{}, clocktest.New(testStart()))

	found, err := svc.Get(context.Background(), device.Ref{UserID: "viewer", ProjectID: projID, DeviceID: "device-1"})
	if err != nil || found.Status != device.StatusOnline {
		t.Fatalf("Get = %+v, %v", found, err)
	}
	_, err = svc.Get(context.Background(), device.Ref{UserID: "viewer", ProjectID: projID, DeviceID: "missing"})
	if detail, _ := fault.Detail(err); !errors.Is(err, fault.ErrNotFound) || detail != "device not found" {
		t.Fatalf("Get(missing) error = %v", err)
	}
}

func TestLocksHandADeviceToOneHolderAtATime(t *testing.T) {
	locks := device.NewLocks()
	unlock, err := locks.Lock(context.Background(), "device-1")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	other, err := locks.Lock(context.Background(), "device-2")
	if err != nil {
		t.Fatalf("Lock another device: %v", err)
	}
	other()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	if _, err := locks.Lock(ctx, "device-1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Lock of a held device = %v, want deadline exceeded", err)
	}
	unlock()
	again, err := locks.Lock(context.Background(), "device-1")
	if err != nil {
		t.Fatalf("Lock after unlock: %v", err)
	}
	again()
}

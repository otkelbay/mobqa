package hub_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/clock/clocktest"
	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/device"
	"github.com/mobqa/backend/internal/usecase/hub"
	"github.com/mobqa/backend/internal/usecase/project"
)

const projID = "project-1"

type fakeAccess struct{}

func (fakeAccess) Require(_ context.Context, projectID, userID string, need project.Role) (project.Role, error) {
	roles := map[string]project.Role{"viewer": project.RoleViewer, "admin": project.RoleAdmin}
	role, ok := roles[userID]
	if !ok || projectID != projID {
		return "", fault.ErrNotFound
	}
	if !role.Includes(need) {
		return "", fault.ErrForbidden
	}
	return role, nil
}

type fakeStore struct {
	hubs      map[string]hub.Hub
	tokens    map[string][]byte
	presence  map[string]hub.Presence
	inventory map[string]hub.Inventory
}

func newStore() *fakeStore {
	return &fakeStore{
		hubs: map[string]hub.Hub{}, tokens: map[string][]byte{},
		presence: map[string]hub.Presence{}, inventory: map[string]hub.Inventory{},
	}
}

func (s *fakeStore) CreateHub(_ context.Context, h hub.Hub, tokenHash []byte) (hub.Hub, error) {
	for _, existing := range s.hubs {
		if existing.ProjectID == h.ProjectID && existing.Name == h.Name {
			return hub.Hub{}, fault.ErrConflict
		}
	}
	s.hubs[h.ID], s.tokens[h.ID] = h, tokenHash
	return h, nil
}

func (s *fakeStore) Hubs(_ context.Context, projectID string) ([]hub.Hub, error) {
	var found []hub.Hub
	for _, h := range s.hubs {
		if h.ProjectID == projectID {
			if p, ok := s.presence[h.ID]; ok {
				h.LastSeenAt, h.OnlineUntil = &p.SeenAt, &p.OnlineUntil
			}
			found = append(found, h)
		}
	}
	return found, nil
}

func (s *fakeStore) HubByToken(_ context.Context, tokenHash []byte) (hub.Hub, error) {
	for id, hash := range s.tokens {
		if bytes.Equal(hash, tokenHash) {
			return s.hubs[id], nil
		}
	}
	return hub.Hub{}, fault.ErrNotFound
}

func (s *fakeStore) SetTokenHash(_ context.Context, projectID, id string, tokenHash []byte) (hub.Hub, error) {
	h, ok := s.hubs[id]
	if !ok || h.ProjectID != projectID {
		return hub.Hub{}, fault.ErrNotFound
	}
	s.tokens[id] = tokenHash
	return h, nil
}

func (s *fakeStore) DeleteHub(_ context.Context, projectID, id string) error {
	if h, ok := s.hubs[id]; !ok || h.ProjectID != projectID {
		return fault.ErrNotFound
	}
	delete(s.hubs, id)
	delete(s.tokens, id)
	return nil
}

func (s *fakeStore) SetPresence(_ context.Context, id string, presence hub.Presence) error {
	s.presence[id] = presence
	return nil
}

func (s *fakeStore) ReplaceInventory(_ context.Context, id string, inv hub.Inventory, presence hub.Presence) error {
	s.inventory[id], s.presence[id] = inv, presence
	return nil
}

func testStart() time.Time {
	return time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
}

func newService(store *fakeStore) (*hub.Service, *clocktest.Manual) {
	now := clocktest.New(testStart())
	return hub.NewService(store, fakeAccess{}, now), now
}

func register(t *testing.T, svc *hub.Service) hub.Registration {
	t.Helper()
	in := hub.RegisterInput{UserID: "admin", ProjectID: projID, Name: "Office Mac"}
	reg, err := svc.Register(context.Background(), in)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	return reg
}

func TestRegisterIssuesTokenThatAuthenticates(t *testing.T) {
	store := newStore()
	svc, _ := newService(store)

	reg := register(t, svc)

	if !strings.HasPrefix(reg.Token, "mqh_") || reg.Hub.Name != "Office Mac" || reg.Hub.CreatedBy != "admin" {
		t.Fatalf("registration = %+v", reg)
	}
	if bytes.Contains(store.tokens[reg.Hub.ID], []byte(reg.Token)) {
		t.Fatal("the token is stored in plain text")
	}
	found, err := svc.Authenticate(context.Background(), reg.Token)
	if err != nil || found.ID != reg.Hub.ID {
		t.Fatalf("Authenticate = %+v, %v", found, err)
	}
	if _, err := svc.Authenticate(context.Background(), "mqh_wrong"); !errors.Is(err, fault.ErrUnauthorized) {
		t.Fatalf("Authenticate(wrong) error = %v, want ErrUnauthorized", err)
	}
}

func TestRegisterRejections(t *testing.T) {
	tests := []struct {
		name string
		in   hub.RegisterInput
		want error
	}{
		{name: "empty name", in: hub.RegisterInput{UserID: "admin", ProjectID: projID}, want: fault.ErrInvalidInput},
		{
			name: "padded name",
			in:   hub.RegisterInput{UserID: "admin", ProjectID: projID, Name: " Mac"}, want: fault.ErrInvalidInput,
		},
		{
			name: "viewer",
			in:   hub.RegisterInput{UserID: "viewer", ProjectID: projID, Name: "Mac"}, want: fault.ErrForbidden,
		},
		{
			name: "outsider",
			in:   hub.RegisterInput{UserID: "stranger", ProjectID: projID, Name: "Mac"}, want: fault.ErrNotFound,
		},
		{
			name: "taken name",
			in:   hub.RegisterInput{UserID: "admin", ProjectID: projID, Name: "Office Mac"}, want: fault.ErrConflict,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, _ := newService(newStore())
			register(t, svc)

			_, err := svc.Register(context.Background(), tt.in)

			if !errors.Is(err, tt.want) {
				t.Fatalf("Register error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestRotateTokenInvalidatesOldToken(t *testing.T) {
	svc, _ := newService(newStore())
	reg := register(t, svc)

	rotated, err := svc.RotateToken(context.Background(), hub.Ref{UserID: "admin", ProjectID: projID, HubID: reg.Hub.ID})
	if err != nil {
		t.Fatalf("RotateToken: %v", err)
	}

	if _, err := svc.Authenticate(context.Background(), reg.Token); !errors.Is(err, fault.ErrUnauthorized) {
		t.Fatalf("Authenticate(old) error = %v, want ErrUnauthorized", err)
	}
	if found, err := svc.Authenticate(context.Background(), rotated.Token); err != nil || found.ID != reg.Hub.ID {
		t.Fatalf("Authenticate(new) = %+v, %v", found, err)
	}
	ref := hub.Ref{UserID: "admin", ProjectID: projID, HubID: "missing"}
	if _, err := svc.RotateToken(context.Background(), ref); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("RotateToken(missing) error = %v, want ErrNotFound", err)
	}
}

func TestPresenceDecidesOnline(t *testing.T) {
	store := newStore()
	svc, now := newService(store)
	reg := register(t, svc)
	online := func() bool {
		t.Helper()
		hubs, err := svc.List(context.Background(), hub.ListInput{UserID: "viewer", ProjectID: projID})
		if err != nil || len(hubs) != 1 {
			t.Fatalf("List = %+v, %v", hubs, err)
		}
		return hubs[0].Online
	}

	if online() {
		t.Fatal("a hub that never connected is online")
	}
	if err := svc.Heartbeat(context.Background(), reg.Hub.ID); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if !online() {
		t.Fatal("a hub that just sent a heartbeat is offline")
	}
	now.Advance(2 * hub.HeartbeatInterval)
	if !online() {
		t.Fatal("one missed heartbeat took the hub offline")
	}
	now.Advance(hub.HeartbeatInterval)
	if online() {
		t.Fatal("a hub silent for three intervals is online")
	}
	if err := svc.Heartbeat(context.Background(), reg.Hub.ID); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if err := svc.Disconnect(context.Background(), reg.Hub.ID); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if online() {
		t.Fatal("a disconnected hub is online")
	}
}

func phone(serial string) hub.ReportedDevice {
	return hub.ReportedDevice{
		Serial: serial, Name: "Pixel", Model: "Pixel 8a", Platform: project.PlatformAndroid, OSVersion: "15",
		Physical: true, State: device.StateConnected,
	}
}

func TestReportInventory(t *testing.T) {
	store := newStore()
	svc, _ := newService(store)
	inventory := hub.Inventory{Version: "1.0.0", Devices: []hub.ReportedDevice{phone("R58M1"), phone("R58M2")}}

	if err := svc.ReportInventory(context.Background(), "hub-1", inventory); err != nil {
		t.Fatalf("ReportInventory: %v", err)
	}

	if got := store.inventory["hub-1"]; len(got.Devices) != 2 || got.Version != "1.0.0" {
		t.Fatalf("stored inventory = %+v", got)
	}
	if p := store.presence["hub-1"]; !p.OnlineUntil.After(p.SeenAt) {
		t.Fatalf("presence = %+v, want an open online window", p)
	}
}

func TestReportInventoryRejections(t *testing.T) {
	disconnected := phone("R58M1")
	disconnected.State = device.StateDisconnected
	web := phone("R58M1")
	web.Platform = "web"
	tooMany := make([]hub.ReportedDevice, 65)
	for i := range tooMany {
		tooMany[i] = phone(strings.Repeat("s", i+1))
	}
	tests := []struct {
		name    string
		devices []hub.ReportedDevice
	}{
		{name: "empty serial", devices: []hub.ReportedDevice{phone("")}},
		{name: "duplicate serial", devices: []hub.ReportedDevice{phone("R58M1"), phone("R58M1")}},
		{name: "reported disconnected", devices: []hub.ReportedDevice{disconnected}},
		{name: "unknown platform", devices: []hub.ReportedDevice{web}},
		{name: "too many devices", devices: tooMany},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newStore()
			svc, _ := newService(store)

			err := svc.ReportInventory(context.Background(), "hub-1", hub.Inventory{Devices: tt.devices})

			if !errors.Is(err, fault.ErrInvalidInput) {
				t.Fatalf("ReportInventory error = %v, want ErrInvalidInput", err)
			}
			if _, ok := store.inventory["hub-1"]; ok {
				t.Fatal("a rejected inventory was stored")
			}
		})
	}
}

func TestDelete(t *testing.T) {
	svc, _ := newService(newStore())
	reg := register(t, svc)

	viewer := hub.Ref{UserID: "viewer", ProjectID: projID, HubID: reg.Hub.ID}
	if err := svc.Delete(context.Background(), viewer); !errors.Is(err, fault.ErrForbidden) {
		t.Fatalf("Delete(viewer) error = %v, want ErrForbidden", err)
	}
	admin := hub.Ref{UserID: "admin", ProjectID: projID, HubID: reg.Hub.ID}
	if err := svc.Delete(context.Background(), admin); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := svc.Authenticate(context.Background(), reg.Token); !errors.Is(err, fault.ErrUnauthorized) {
		t.Fatalf("Authenticate(deleted) error = %v, want ErrUnauthorized", err)
	}
	if err := svc.Delete(context.Background(), admin); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("Delete(again) error = %v, want ErrNotFound", err)
	}
}

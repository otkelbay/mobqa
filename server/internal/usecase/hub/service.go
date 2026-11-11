package hub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/mobqa/backend/internal/clock"
	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/project"
)

// tokenPrefix makes a leaked hub token recognisable to secret scanners.
const tokenPrefix = "mqh_"

type store interface {
	// CreateHub reports ErrConflict when the project already has a hub with the name.
	CreateHub(ctx context.Context, h Hub, tokenHash []byte) (Hub, error)
	Hubs(ctx context.Context, projectID string) ([]Hub, error)
	HubByToken(ctx context.Context, tokenHash []byte) (Hub, error)
	SetTokenHash(ctx context.Context, projectID, id string, tokenHash []byte) (Hub, error)
	DeleteHub(ctx context.Context, projectID, id string) error
	SetPresence(ctx context.Context, id string, presence Presence) error
	// ReplaceInventory records the hub's version and devices and moves its presence in one transaction.
	ReplaceInventory(ctx context.Context, id string, inventory Inventory, presence Presence) error
}

type access interface {
	Require(ctx context.Context, projectID, userID string, need project.Role) (project.Role, error)
}

type Service struct {
	store  store
	access access
	clock  clock.Clock
}

func NewService(hubs store, projects access, now clock.Clock) *Service {
	return &Service{store: hubs, access: projects, clock: now}
}

type RegisterInput struct {
	UserID    string
	ProjectID string
	Name      string
}

// Register adds a hub to a project and returns its token.
func (s *Service) Register(ctx context.Context, in RegisterInput) (Registration, error) {
	if err := validateName(in.Name); err != nil {
		return Registration{}, err
	}
	if _, err := s.access.Require(ctx, in.ProjectID, in.UserID, project.RoleAdmin); err != nil {
		return Registration{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Registration{}, fmt.Errorf("generate hub id: %w", err)
	}
	token := tokenPrefix + rand.Text()
	created, err := s.store.CreateHub(ctx,
		Hub{ID: id.String(), ProjectID: in.ProjectID, Name: in.Name, CreatedBy: in.UserID}, hashToken(token))
	if errors.Is(err, fault.ErrConflict) {
		return Registration{}, fault.WithDetail(fault.ErrConflict, "another hub already has this name")
	}
	if err != nil {
		return Registration{}, fmt.Errorf("create hub: %w", err)
	}
	return Registration{Hub: created, Token: token}, nil
}

type ListInput struct {
	UserID    string
	ProjectID string
}

func (s *Service) List(ctx context.Context, in ListInput) ([]Hub, error) {
	if _, err := s.access.Require(ctx, in.ProjectID, in.UserID, project.RoleViewer); err != nil {
		return nil, err
	}
	hubs, err := s.store.Hubs(ctx, in.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("list hubs: %w", err)
	}
	now := s.clock.Now()
	for i := range hubs {
		hubs[i].Online = hubs[i].online(now)
	}
	return hubs, nil
}

type Ref struct {
	UserID    string
	ProjectID string
	HubID     string
}

// RotateToken replaces a hub's token. The old token stops working at once, so a connected hub keeps its
// connection but cannot reconnect until it is given the new one.
func (s *Service) RotateToken(ctx context.Context, ref Ref) (Registration, error) {
	if _, err := s.access.Require(ctx, ref.ProjectID, ref.UserID, project.RoleAdmin); err != nil {
		return Registration{}, err
	}
	token := tokenPrefix + rand.Text()
	updated, err := s.store.SetTokenHash(ctx, ref.ProjectID, ref.HubID, hashToken(token))
	if errors.Is(err, fault.ErrNotFound) {
		return Registration{}, fault.WithDetail(fault.ErrNotFound, "hub not found")
	}
	if err != nil {
		return Registration{}, fmt.Errorf("rotate hub token: %w", err)
	}
	updated.Online = updated.online(s.clock.Now())
	return Registration{Hub: updated, Token: token}, nil
}

// Delete removes a hub and its devices.
func (s *Service) Delete(ctx context.Context, ref Ref) error {
	if _, err := s.access.Require(ctx, ref.ProjectID, ref.UserID, project.RoleAdmin); err != nil {
		return err
	}
	err := s.store.DeleteHub(ctx, ref.ProjectID, ref.HubID)
	if errors.Is(err, fault.ErrNotFound) {
		return fault.WithDetail(fault.ErrNotFound, "hub not found")
	}
	if err != nil {
		return fmt.Errorf("delete hub: %w", err)
	}
	return nil
}

// Authenticate returns the hub a token belongs to.
func (s *Service) Authenticate(ctx context.Context, token string) (Hub, error) {
	found, err := s.store.HubByToken(ctx, hashToken(token))
	if errors.Is(err, fault.ErrNotFound) {
		return Hub{}, fault.WithDetail(fault.ErrUnauthorized, "invalid hub token")
	}
	if err != nil {
		return Hub{}, fmt.Errorf("load hub by token: %w", err)
	}
	return found, nil
}

// ReportInventory records what a connected hub sees and keeps it online.
func (s *Service) ReportInventory(ctx context.Context, hubID string, inventory Inventory) error {
	if err := inventory.Validate(); err != nil {
		return err
	}
	if err := s.store.ReplaceInventory(ctx, hubID, inventory, s.presence(presenceTTL)); err != nil {
		return fmt.Errorf("replace inventory: %w", err)
	}
	return nil
}

func (s *Service) Heartbeat(ctx context.Context, hubID string) error {
	if err := s.store.SetPresence(ctx, hubID, s.presence(presenceTTL)); err != nil {
		return fmt.Errorf("record heartbeat: %w", err)
	}
	return nil
}

// Disconnect marks a hub offline as soon as its connection closes instead of waiting for its presence to
// lapse.
func (s *Service) Disconnect(ctx context.Context, hubID string) error {
	if err := s.store.SetPresence(ctx, hubID, s.presence(0)); err != nil {
		return fmt.Errorf("record disconnect: %w", err)
	}
	return nil
}

func (s *Service) presence(ttl time.Duration) Presence {
	now := s.clock.Now()
	return Presence{SeenAt: now, OnlineUntil: now.Add(ttl)}
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

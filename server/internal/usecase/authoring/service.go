package authoring

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/mobqa/backend/internal/clock"
	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/device"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/test"
	"github.com/mobqa/backend/internal/worker"
)

// JobKind is the job that runs one authoring session.
const JobKind = "authoring"

// jobAttempts rides out a hub or a model that is away for a moment; each attempt starts over.
const jobAttempts = 3

// Pin is an environment an authoring is tied to, with the name it is shown by.
type Pin struct {
	ID   string
	Name string
}

type NewAuthoring struct {
	Authoring Authoring
	HubID     string
	Serial    string
	Job       worker.EnqueueInput
}

// store reports ErrNotFound for anything outside the project.
type store interface {
	Test(ctx context.Context, projectID, id string) (test.Test, error)
	Device(ctx context.Context, projectID, id string) (device.Device, error)
	Environment(ctx context.Context, projectID, id string) (Pin, error)
	DefaultEnvironment(ctx context.Context, projectID string) (Pin, error)
	// CreateAuthoring stores the authoring and queues its job in one transaction. It reports ErrConflict
	// when the test already has an authoring that has not finished.
	CreateAuthoring(ctx context.Context, in NewAuthoring) (Authoring, error)
	// Authorings lists a test's latest authorings, newest first.
	Authorings(ctx context.Context, projectID, testID string) ([]Authoring, error)
	Authoring(ctx context.Context, projectID, id string) (Authoring, error)
	// CancelAuthoring cancels the authoring and its job in one transaction. It reports ErrConflict when
	// the authoring has finished.
	CancelAuthoring(ctx context.Context, projectID, id string) (Authoring, error)
}

type access interface {
	Require(ctx context.Context, projectID, userID string, need project.Role) (project.Role, error)
}

type Service struct {
	store  store
	access access
	clock  clock.Clock
}

func NewService(authorings store, projects access, now clock.Clock) *Service {
	return &Service{store: authorings, access: projects, clock: now}
}

type StartInput struct {
	UserID    string
	ProjectID string
	TestID    string
	DeviceID  string
	// EnvironmentID defaults to the project's default environment.
	EnvironmentID string
}

func (in StartInput) Validate() error {
	if in.DeviceID == "" {
		return fault.WithDetail(fault.ErrInvalidInput, "device_id is required")
	}
	return nil
}

type jobPayload struct {
	AuthoringID string `json:"authoring_id"`
}

// Start queues an authoring of the test's current version on a device of the test's platform that is
// online now.
func (s *Service) Start(ctx context.Context, in StartInput) (Authoring, error) {
	if err := in.Validate(); err != nil {
		return Authoring{}, err
	}
	if _, err := s.access.Require(ctx, in.ProjectID, in.UserID, project.RoleEngineer); err != nil {
		return Authoring{}, err
	}
	subject, err := s.store.Test(ctx, in.ProjectID, in.TestID)
	if errors.Is(err, fault.ErrNotFound) {
		return Authoring{}, fault.WithDetail(fault.ErrNotFound, "test not found")
	}
	if err != nil {
		return Authoring{}, fmt.Errorf("load test: %w", err)
	}
	if subject.StepCount == 0 {
		return Authoring{}, fault.WithDetail(fault.ErrInvalidInput, "a test needs steps to be authored")
	}
	target, err := s.device(ctx, in, subject.Platform)
	if err != nil {
		return Authoring{}, err
	}
	environment, err := s.environment(ctx, in)
	if err != nil {
		return Authoring{}, err
	}
	plan, err := newAuthoring(in, subject, target, environment)
	if err != nil {
		return Authoring{}, err
	}
	created, err := s.store.CreateAuthoring(ctx, plan)
	if errors.Is(err, fault.ErrConflict) {
		return Authoring{}, fault.WithDetail(fault.ErrConflict, "the test is already being authored")
	}
	if err != nil {
		return Authoring{}, fmt.Errorf("create authoring: %w", err)
	}
	return created, nil
}

func (s *Service) device(ctx context.Context, in StartInput, platform project.Platform) (device.Device, error) {
	found, err := s.store.Device(ctx, in.ProjectID, in.DeviceID)
	if errors.Is(err, fault.ErrNotFound) {
		return device.Device{}, fault.WithDetail(fault.ErrNotFound, "device not found")
	}
	if err != nil {
		return device.Device{}, fmt.Errorf("load device: %w", err)
	}
	if found.Platform != platform {
		return device.Device{}, fault.WithDetail(fault.ErrInvalidInput, "the device must match the test's platform")
	}
	if found.StatusAt(s.clock.Now()) != device.StatusOnline {
		return device.Device{}, fault.WithDetail(fault.ErrConflict, "the device is not online")
	}
	return found, nil
}

// environment resolves the environment to use; a project without a default environment needs none.
func (s *Service) environment(ctx context.Context, in StartInput) (Pin, error) {
	if in.EnvironmentID == "" {
		pin, err := s.store.DefaultEnvironment(ctx, in.ProjectID)
		if errors.Is(err, fault.ErrNotFound) {
			return Pin{}, nil
		}
		if err != nil {
			return Pin{}, fmt.Errorf("load default environment: %w", err)
		}
		return pin, nil
	}
	pin, err := s.store.Environment(ctx, in.ProjectID, in.EnvironmentID)
	if errors.Is(err, fault.ErrNotFound) {
		return Pin{}, fault.WithDetail(fault.ErrNotFound, "environment not found")
	}
	if err != nil {
		return Pin{}, fmt.Errorf("load environment: %w", err)
	}
	return pin, nil
}

func newAuthoring(in StartInput, subject test.Test, target device.Device, environment Pin) (NewAuthoring, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return NewAuthoring{}, fmt.Errorf("generate authoring id: %w", err)
	}
	payload, err := json.Marshal(jobPayload{AuthoringID: id.String()})
	if err != nil {
		return NewAuthoring{}, fmt.Errorf("encode authoring payload: %w", err)
	}
	return NewAuthoring{
		Authoring: Authoring{
			ID: id.String(), ProjectID: in.ProjectID, TestID: subject.ID, TestVersion: subject.Version,
			Platform: subject.Platform, DeviceID: target.ID, DeviceName: target.Name, EnvironmentID: environment.ID,
			EnvironmentName: environment.Name, Status: StatusQueued, CreatedBy: in.UserID,
		},
		HubID:  target.HubID,
		Serial: target.Serial,
		Job:    worker.EnqueueInput{Kind: JobKind, Payload: payload, MaxAttempts: jobAttempts},
	}, nil
}

type TestRef struct {
	UserID    string
	ProjectID string
	TestID    string
}

func (s *Service) List(ctx context.Context, ref TestRef) ([]Authoring, error) {
	if _, err := s.access.Require(ctx, ref.ProjectID, ref.UserID, project.RoleViewer); err != nil {
		return nil, err
	}
	found, err := s.store.Authorings(ctx, ref.ProjectID, ref.TestID)
	if err != nil {
		return nil, fmt.Errorf("list authorings: %w", err)
	}
	return found, nil
}

type Ref struct {
	UserID      string
	ProjectID   string
	AuthoringID string
}

func (s *Service) Get(ctx context.Context, ref Ref) (Authoring, error) {
	if _, err := s.access.Require(ctx, ref.ProjectID, ref.UserID, project.RoleViewer); err != nil {
		return Authoring{}, err
	}
	found, err := s.store.Authoring(ctx, ref.ProjectID, ref.AuthoringID)
	if errors.Is(err, fault.ErrNotFound) {
		return Authoring{}, fault.WithDetail(fault.ErrNotFound, "authoring not found")
	}
	if err != nil {
		return Authoring{}, fmt.Errorf("load authoring: %w", err)
	}
	return found, nil
}

// Cancel stops an authoring that has not finished; the agent stops at its next command.
func (s *Service) Cancel(ctx context.Context, ref Ref) (Authoring, error) {
	if _, err := s.access.Require(ctx, ref.ProjectID, ref.UserID, project.RoleEngineer); err != nil {
		return Authoring{}, err
	}
	cancelled, err := s.store.CancelAuthoring(ctx, ref.ProjectID, ref.AuthoringID)
	switch {
	case errors.Is(err, fault.ErrNotFound):
		return Authoring{}, fault.WithDetail(fault.ErrNotFound, "authoring not found")
	case errors.Is(err, fault.ErrConflict):
		return Authoring{}, fault.WithDetail(fault.ErrConflict, "the authoring has already finished")
	case err != nil:
		return Authoring{}, fmt.Errorf("cancel authoring: %w", err)
	}
	return cancelled, nil
}

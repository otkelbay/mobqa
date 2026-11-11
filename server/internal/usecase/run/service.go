package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"

	"github.com/mobqa/backend/internal/clock"
	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/device"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/test"
	"github.com/mobqa/backend/internal/worker"
)

const (
	defaultPageSize = 20
	maxPageSize     = 100
)

// Pin is a build or an environment a run is tied to, with the label it is shown by.
type Pin struct {
	ID       string
	Label    string
	Platform project.Platform
}

type NewRun struct {
	Run     Run
	Results []NewResult
	// Jobs holds one job per device, keyed by device ID.
	Jobs map[string]worker.EnqueueInput
}

type NewResult struct {
	Position     int
	TestID       string
	TestVersion  int
	Title        string
	DeviceID     string
	DeviceName   string
	DeviceSerial string
	HubID        string
}

type Query struct {
	ProjectID string
	Status    Status
	Limit     int
	Offset    int
}

// store reports ErrNotFound for anything outside the project. Lookups by many IDs return only the rows
// they find.
type store interface {
	Tests(ctx context.Context, projectID string, ids []string) ([]test.Test, error)
	// Scripted returns the IDs of the tests whose version has a script from a succeeded authoring.
	Scripted(ctx context.Context, tests []test.Test) ([]string, error)
	Devices(ctx context.Context, projectID string, ids []string) ([]device.Device, error)
	Build(ctx context.Context, projectID, id string) (Pin, error)
	CurrentBuild(ctx context.Context, projectID string, platform project.Platform) (Pin, error)
	Environment(ctx context.Context, projectID, id string) (Pin, error)
	DefaultEnvironment(ctx context.Context, projectID string) (Pin, error)
	// CreateRun stores the run with its results, copying each test version's steps and latest script, and
	// queues the device jobs in one transaction. It reports ErrNotFound when a test version or its script
	// disappeared meanwhile.
	CreateRun(ctx context.Context, in NewRun) (Run, error)
	Runs(ctx context.Context, query Query) ([]Run, int, error)
	Run(ctx context.Context, projectID, id string) (Run, error)
	Results(ctx context.Context, runID string) ([]Result, error)
	Attempts(ctx context.Context, runID, resultID string) ([]Attempt, error)
	// CancelRun cancels the run, its unfinished results and their jobs in one transaction. It reports
	// ErrConflict when the run has already finished.
	CancelRun(ctx context.Context, projectID, id string) (Run, error)
}

type access interface {
	Require(ctx context.Context, projectID, userID string, need project.Role) (project.Role, error)
}

type Service struct {
	store  store
	access access
	clock  clock.Clock
}

func NewService(runs store, projects access, now clock.Clock) *Service {
	return &Service{store: runs, access: projects, clock: now}
}

type CreateInput struct {
	UserID    string
	ProjectID string
	TestIDs   []string
	DeviceIDs []string
	// BuildID defaults to the platform's current build; with neither, the app must already be installed.
	BuildID string
	// EnvironmentID defaults to the project's default environment.
	EnvironmentID string
	Retries       int
}

func (in CreateInput) Validate() error {
	if len(in.TestIDs) == 0 || len(in.TestIDs) > maxTests || duplicated(in.TestIDs) {
		return fault.WithDetail(fault.ErrInvalidInput, "a run needs 1 to 50 distinct tests")
	}
	if len(in.DeviceIDs) == 0 || len(in.DeviceIDs) > maxDevices || duplicated(in.DeviceIDs) {
		return fault.WithDetail(fault.ErrInvalidInput, "a run needs 1 to 10 distinct devices")
	}
	if in.Retries < 0 || in.Retries > maxRetries {
		return fault.WithDetail(fault.ErrInvalidInput, "retries must be 0 to 2")
	}
	return nil
}

func duplicated(ids []string) bool {
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	return len(slices.Compact(sorted)) != len(ids)
}

type devicePayload struct {
	RunID    string `json:"run_id"`
	DeviceID string `json:"device_id"`
}

// Create queues a run of every test on every device. All of them must target one platform, and every
// device must be online now.
func (s *Service) Create(ctx context.Context, in CreateInput) (Run, error) {
	if err := in.Validate(); err != nil {
		return Run{}, err
	}
	if _, err := s.access.Require(ctx, in.ProjectID, in.UserID, project.RoleEngineer); err != nil {
		return Run{}, err
	}
	tests, err := s.tests(ctx, in)
	if err != nil {
		return Run{}, err
	}
	platform := tests[0].Platform
	devices, err := s.devices(ctx, in, platform)
	if err != nil {
		return Run{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Run{}, fmt.Errorf("generate run id: %w", err)
	}
	r := Run{
		ID: id.String(), ProjectID: in.ProjectID, Platform: platform, Status: StatusQueued, Retries: in.Retries,
		CreatedBy: in.UserID,
	}
	if err := s.pin(ctx, in, &r); err != nil {
		return Run{}, err
	}
	plan, err := newPlan(r, tests, devices)
	if err != nil {
		return Run{}, err
	}
	created, err := s.store.CreateRun(ctx, plan)
	if errors.Is(err, fault.ErrNotFound) {
		return Run{}, fault.WithDetail(fault.ErrConflict, "a test changed while the run was created; try again")
	}
	if err != nil {
		return Run{}, fmt.Errorf("create run: %w", err)
	}
	return created, nil
}

// tests loads the run's tests in the order they were asked for.
func (s *Service) tests(ctx context.Context, in CreateInput) ([]test.Test, error) {
	found, err := s.store.Tests(ctx, in.ProjectID, in.TestIDs)
	if err != nil {
		return nil, fmt.Errorf("load tests: %w", err)
	}
	ordered := make([]test.Test, 0, len(in.TestIDs))
	for _, id := range in.TestIDs {
		i := slices.IndexFunc(found, func(t test.Test) bool { return t.ID == id })
		if i < 0 {
			return nil, fault.WithDetail(fault.ErrNotFound, "test not found")
		}
		if found[i].Platform != found[0].Platform {
			return nil, fault.WithDetail(fault.ErrInvalidInput, "all tests of a run must target one platform")
		}
		ordered = append(ordered, found[i])
	}
	scripted, err := s.store.Scripted(ctx, ordered)
	if err != nil {
		return nil, fmt.Errorf("load scripted tests: %w", err)
	}
	for _, t := range ordered {
		if !slices.Contains(scripted, t.ID) {
			return nil, fault.WithDetail(fault.ErrConflict,
				fmt.Sprintf("test %q has no script for its current version; author it on a device first", t.Content.Title))
		}
	}
	return ordered, nil
}

func (s *Service) devices(ctx context.Context, in CreateInput, platform project.Platform) ([]device.Device, error) {
	found, err := s.store.Devices(ctx, in.ProjectID, in.DeviceIDs)
	if err != nil {
		return nil, fmt.Errorf("load devices: %w", err)
	}
	if len(found) != len(in.DeviceIDs) {
		return nil, fault.WithDetail(fault.ErrNotFound, "device not found")
	}
	now := s.clock.Now()
	for _, d := range found {
		if d.Platform != platform {
			return nil, fault.WithDetail(fault.ErrInvalidInput, "every device must match the tests' platform")
		}
		if d.StatusAt(now) != device.StatusOnline {
			return nil, fault.WithDetail(fault.ErrConflict, "every device must be online")
		}
	}
	return found, nil
}

// pin ties the run to the build and environment it will use.
func (s *Service) pin(ctx context.Context, in CreateInput, r *Run) error {
	build, err := s.build(ctx, in, r.Platform)
	if err != nil {
		return err
	}
	environment, err := s.environment(ctx, in)
	if err != nil {
		return err
	}
	r.BuildID, r.BuildLabel = build.ID, build.Label
	r.EnvironmentID, r.EnvironmentName = environment.ID, environment.Label
	return nil
}

func (s *Service) build(ctx context.Context, in CreateInput, platform project.Platform) (Pin, error) {
	if in.BuildID == "" {
		current, err := s.store.CurrentBuild(ctx, in.ProjectID, platform)
		if errors.Is(err, fault.ErrNotFound) {
			return Pin{}, nil
		}
		if err != nil {
			return Pin{}, fmt.Errorf("load current build: %w", err)
		}
		return current, nil
	}
	chosen, err := s.store.Build(ctx, in.ProjectID, in.BuildID)
	if errors.Is(err, fault.ErrNotFound) {
		return Pin{}, fault.WithDetail(fault.ErrNotFound, "build not found")
	}
	if err != nil {
		return Pin{}, fmt.Errorf("load build: %w", err)
	}
	if chosen.Platform != platform {
		return Pin{}, fault.WithDetail(fault.ErrInvalidInput, "the build must match the tests' platform")
	}
	return chosen, nil
}

func (s *Service) environment(ctx context.Context, in CreateInput) (Pin, error) {
	if in.EnvironmentID == "" {
		fallback, err := s.store.DefaultEnvironment(ctx, in.ProjectID)
		if errors.Is(err, fault.ErrNotFound) {
			return Pin{}, nil
		}
		if err != nil {
			return Pin{}, fmt.Errorf("load default environment: %w", err)
		}
		return fallback, nil
	}
	chosen, err := s.store.Environment(ctx, in.ProjectID, in.EnvironmentID)
	if errors.Is(err, fault.ErrNotFound) {
		return Pin{}, fault.WithDetail(fault.ErrNotFound, "environment not found")
	}
	if err != nil {
		return Pin{}, fmt.Errorf("load environment: %w", err)
	}
	return chosen, nil
}

// newPlan lays out one result per test and device and one job per device.
func newPlan(r Run, tests []test.Test, devices []device.Device) (NewRun, error) {
	plan := NewRun{Run: r, Jobs: make(map[string]worker.EnqueueInput, len(devices))}
	for _, d := range devices {
		payload, err := json.Marshal(devicePayload{RunID: r.ID, DeviceID: d.ID})
		if err != nil {
			return NewRun{}, fmt.Errorf("encode device payload: %w", err)
		}
		plan.Jobs[d.ID] = worker.EnqueueInput{Kind: DeviceJobKind, Payload: payload, MaxAttempts: deviceAttempts}
		for position, t := range tests {
			plan.Results = append(plan.Results, NewResult{
				Position: position, TestID: t.ID, TestVersion: t.Version, Title: t.Content.Title, DeviceID: d.ID,
				DeviceName: d.Name, DeviceSerial: d.Serial, HubID: d.HubID,
			})
		}
	}
	return plan, nil
}

type ListInput struct {
	UserID    string
	ProjectID string
	Status    Status
	Page      int
	Limit     int
}

func (in ListInput) Validate() error {
	switch in.Status {
	case "", StatusQueued, StatusRunning, StatusPassed, StatusFailed, StatusCancelled:
	default:
		return fault.WithDetail(fault.ErrInvalidInput, "unknown run status")
	}
	if in.Page < 0 || in.Limit < 0 || in.Limit > maxPageSize {
		return fault.WithDetail(fault.ErrInvalidInput, "page must be positive and limit at most 100")
	}
	return nil
}

type Page struct {
	Items []Run
	Total int
	Page  int
	Limit int
}

func (p Page) HasMore() bool {
	return p.Page*p.Limit < p.Total
}

// List returns a project's runs, newest first.
func (s *Service) List(ctx context.Context, in ListInput) (Page, error) {
	if err := in.Validate(); err != nil {
		return Page{}, err
	}
	if _, err := s.access.Require(ctx, in.ProjectID, in.UserID, project.RoleViewer); err != nil {
		return Page{}, err
	}
	page, limit := max(in.Page, 1), in.Limit
	if limit == 0 {
		limit = defaultPageSize
	}
	items, total, err := s.store.Runs(ctx, Query{
		ProjectID: in.ProjectID, Status: in.Status, Limit: limit, Offset: (page - 1) * limit,
	})
	if err != nil {
		return Page{}, fmt.Errorf("list runs: %w", err)
	}
	return Page{Items: items, Total: total, Page: page, Limit: limit}, nil
}

type Ref struct {
	UserID    string
	ProjectID string
	RunID     string
}

// Get returns a run with its results, grouped by device in test order.
func (s *Service) Get(ctx context.Context, ref Ref) (Run, []Result, error) {
	if _, err := s.access.Require(ctx, ref.ProjectID, ref.UserID, project.RoleViewer); err != nil {
		return Run{}, nil, err
	}
	found, err := s.find(ctx, ref.ProjectID, ref.RunID)
	if err != nil {
		return Run{}, nil, err
	}
	results, err := s.store.Results(ctx, found.ID)
	if err != nil {
		return Run{}, nil, fmt.Errorf("load results: %w", err)
	}
	return found, results, nil
}

func (s *Service) find(ctx context.Context, projectID, id string) (Run, error) {
	found, err := s.store.Run(ctx, projectID, id)
	if errors.Is(err, fault.ErrNotFound) {
		return Run{}, fault.WithDetail(fault.ErrNotFound, "run not found")
	}
	if err != nil {
		return Run{}, fmt.Errorf("load run: %w", err)
	}
	return found, nil
}

// Attempts returns every attempt of one result, oldest first.
func (s *Service) Attempts(ctx context.Context, ref Ref, resultID string) ([]Attempt, error) {
	if _, err := s.access.Require(ctx, ref.ProjectID, ref.UserID, project.RoleViewer); err != nil {
		return nil, err
	}
	if _, err := s.find(ctx, ref.ProjectID, ref.RunID); err != nil {
		return nil, err
	}
	attempts, err := s.store.Attempts(ctx, ref.RunID, resultID)
	if errors.Is(err, fault.ErrNotFound) {
		return nil, fault.WithDetail(fault.ErrNotFound, "result not found")
	}
	if err != nil {
		return nil, fmt.Errorf("load attempts: %w", err)
	}
	return attempts, nil
}

// Cancel stops a run: queued work is dropped and the hubs running its tests are told to stop.
func (s *Service) Cancel(ctx context.Context, ref Ref) (Run, error) {
	if _, err := s.access.Require(ctx, ref.ProjectID, ref.UserID, project.RoleEngineer); err != nil {
		return Run{}, err
	}
	cancelled, err := s.store.CancelRun(ctx, ref.ProjectID, ref.RunID)
	switch {
	case errors.Is(err, fault.ErrNotFound):
		return Run{}, fault.WithDetail(fault.ErrNotFound, "run not found")
	case errors.Is(err, fault.ErrConflict):
		return Run{}, fault.WithDetail(fault.ErrConflict, "the run has already finished")
	case err != nil:
		return Run{}, fmt.Errorf("cancel run: %w", err)
	}
	return cancelled, nil
}

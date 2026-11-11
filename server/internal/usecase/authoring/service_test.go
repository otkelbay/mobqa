package authoring_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/clock/clocktest"
	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/authoring"
	"github.com/mobqa/backend/internal/usecase/device"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/test"
)

const projectID = "project-1"

type fakeAccess struct{}

func (fakeAccess) Require(_ context.Context, _, userID string, need project.Role) (project.Role, error) {
	roles := map[string]project.Role{"viewer": project.RoleViewer, "engineer": project.RoleEngineer}
	role, ok := roles[userID]
	if !ok {
		return "", fault.ErrNotFound
	}
	if !role.Includes(need) {
		return "", fault.ErrForbidden
	}
	return role, nil
}

type fakeStore struct {
	steps       int
	busy        bool
	environment bool
	created     []authoring.NewAuthoring
	cancelled   []string
}

func now() time.Time {
	return time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
}

func (s *fakeStore) Test(_ context.Context, _, id string) (test.Test, error) {
	if id != "test-1" {
		return test.Test{}, fault.ErrNotFound
	}
	return test.Test{ID: id, Platform: project.PlatformAndroid, Version: 3, StepCount: s.steps}, nil
}

func (s *fakeStore) Device(_ context.Context, _, id string) (device.Device, error) {
	online := now().Add(time.Minute)
	devices := map[string]device.Device{
		"pixel":   {ID: "pixel", HubID: "hub-1", Serial: "R58", Name: "Pixel", Platform: project.PlatformAndroid},
		"iphone":  {ID: "iphone", HubID: "hub-1", Serial: "0008", Name: "iPhone", Platform: project.PlatformIOS},
		"offline": {ID: "offline", HubID: "hub-1", Serial: "R59", Name: "Old", Platform: project.PlatformAndroid},
	}
	found, ok := devices[id]
	if !ok {
		return device.Device{}, fault.ErrNotFound
	}
	if id != "offline" {
		found.State, found.HubOnlineUntil = device.StateConnected, &online
	}
	return found, nil
}

func (s *fakeStore) Environment(_ context.Context, _, id string) (authoring.Pin, error) {
	if id != "env-2" {
		return authoring.Pin{}, fault.ErrNotFound
	}
	return authoring.Pin{ID: id, Name: "Production"}, nil
}

func (s *fakeStore) DefaultEnvironment(context.Context, string) (authoring.Pin, error) {
	if !s.environment {
		return authoring.Pin{}, fault.ErrNotFound
	}
	return authoring.Pin{ID: "env-1", Name: "Staging"}, nil
}

func (s *fakeStore) CreateAuthoring(_ context.Context, in authoring.NewAuthoring) (authoring.Authoring, error) {
	if s.busy {
		return authoring.Authoring{}, fault.ErrConflict
	}
	s.created = append(s.created, in)
	return in.Authoring, nil
}

func (s *fakeStore) Authorings(context.Context, string, string) ([]authoring.Authoring, error) {
	return []authoring.Authoring{{ID: "authoring-1"}}, nil
}

func (s *fakeStore) Authoring(_ context.Context, _, id string) (authoring.Authoring, error) {
	if id != "authoring-1" {
		return authoring.Authoring{}, fault.ErrNotFound
	}
	return authoring.Authoring{ID: id, Status: authoring.StatusRunning}, nil
}

func (s *fakeStore) CancelAuthoring(_ context.Context, _, id string) (authoring.Authoring, error) {
	if id == "finished" {
		return authoring.Authoring{}, fault.ErrConflict
	}
	s.cancelled = append(s.cancelled, id)
	return authoring.Authoring{ID: id, Status: authoring.StatusCancelled}, nil
}

func service(store *fakeStore) *authoring.Service {
	return authoring.NewService(store, fakeAccess{}, clocktest.New(now()))
}

func start(userID, deviceID string) authoring.StartInput {
	return authoring.StartInput{UserID: userID, ProjectID: projectID, TestID: "test-1", DeviceID: deviceID}
}

func TestStartQueuesAuthoringOfTheCurrentVersion(t *testing.T) {
	store := &fakeStore{steps: 2, environment: true}

	created, err := service(store).Start(context.Background(), start("engineer", "pixel"))

	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	plan := store.created[0]
	a := plan.Authoring
	if created.ID == "" || a.ID != created.ID || a.ProjectID != projectID || a.TestID != "test-1" ||
		a.TestVersion != 3 || a.Platform != project.PlatformAndroid || a.DeviceID != "pixel" ||
		a.DeviceName != "Pixel" || a.EnvironmentID != "env-1" || a.EnvironmentName != "Staging" ||
		a.Status != authoring.StatusQueued || a.CreatedBy != "engineer" || plan.HubID != "hub-1" ||
		plan.Serial != "R58" {
		t.Fatalf("plan = %+v", plan)
	}
	var payload struct {
		AuthoringID string `json:"authoring_id"`
	}
	if err := json.Unmarshal(plan.Job.Payload, &payload); err != nil || payload.AuthoringID != created.ID ||
		plan.Job.Kind != authoring.JobKind || plan.Job.MaxAttempts != 3 {
		t.Fatalf("job = %+v (%v)", plan.Job, err)
	}
}

func TestStartUsesTheChosenEnvironmentOrNone(t *testing.T) {
	store := &fakeStore{steps: 1}
	in := start("engineer", "pixel")

	if _, err := service(store).Start(context.Background(), in); err != nil {
		t.Fatalf("Start without environments: %v", err)
	}
	in.EnvironmentID = "env-2"
	if _, err := service(store).Start(context.Background(), in); err != nil {
		t.Fatalf("Start with an environment: %v", err)
	}

	if store.created[0].Authoring.EnvironmentID != "" || store.created[1].Authoring.EnvironmentName != "Production" {
		t.Fatalf("created = %+v", store.created)
	}
}

func TestStartRejects(t *testing.T) {
	tests := []struct {
		name   string
		in     authoring.StartInput
		steps  int
		busy   bool
		want   error
		detail string
	}{
		{
			name: "no device", in: start("engineer", ""), steps: 1, want: fault.ErrInvalidInput,
			detail: "device_id is required",
		},
		{name: "viewer", in: start("viewer", "pixel"), steps: 1, want: fault.ErrForbidden},
		{
			name: "missing test", in: authoring.StartInput{UserID: "engineer", ProjectID: projectID, TestID: "x",
				DeviceID: "pixel"}, steps: 1, want: fault.ErrNotFound, detail: "test not found",
		},
		{
			name: "no steps", in: start("engineer", "pixel"), want: fault.ErrInvalidInput,
			detail: "a test needs steps to be authored",
		},
		{
			name: "missing device", in: start("engineer", "nokia"), steps: 1, want: fault.ErrNotFound,
			detail: "device not found",
		},
		{
			name: "other platform", in: start("engineer", "iphone"), steps: 1, want: fault.ErrInvalidInput,
			detail: "the device must match the test's platform",
		},
		{
			name: "offline device", in: start("engineer", "offline"), steps: 1, want: fault.ErrConflict,
			detail: "the device is not online",
		},
		{
			name: "missing environment", steps: 1, want: fault.ErrNotFound, detail: "environment not found",
			in: authoring.StartInput{UserID: "engineer", ProjectID: projectID, TestID: "test-1", DeviceID: "pixel",
				EnvironmentID: "env-9"},
		},
		{
			name: "already authoring", in: start("engineer", "pixel"), steps: 1, busy: true, want: fault.ErrConflict,
			detail: "the test is already being authored",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := service(&fakeStore{steps: tt.steps, busy: tt.busy}).Start(context.Background(), tt.in)

			detail, _ := fault.Detail(err)
			if !errors.Is(err, tt.want) || (tt.detail != "" && detail != tt.detail) {
				t.Fatalf("Start = %v (%q), want %v (%q)", err, detail, tt.want, tt.detail)
			}
		})
	}
}

func TestViewersReadAuthorings(t *testing.T) {
	s := service(&fakeStore{})

	list, err := s.List(context.Background(), authoring.TestRef{UserID: "viewer", ProjectID: projectID, TestID: "t"})
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %+v, %v", list, err)
	}
	found, err := s.Get(context.Background(), authoring.Ref{UserID: "viewer", ProjectID: projectID,
		AuthoringID: "authoring-1"})
	if err != nil || found.Status != authoring.StatusRunning {
		t.Fatalf("Get = %+v, %v", found, err)
	}
	_, err = s.Get(context.Background(), authoring.Ref{UserID: "viewer", ProjectID: projectID, AuthoringID: "x"})
	if detail, _ := fault.Detail(err); !errors.Is(err, fault.ErrNotFound) || detail != "authoring not found" {
		t.Fatalf("Get missing = %v", err)
	}
}

func TestCancelStopsUnfinishedAuthorings(t *testing.T) {
	store := &fakeStore{}
	s := service(store)
	ref := authoring.Ref{UserID: "engineer", ProjectID: projectID, AuthoringID: "authoring-1"}

	cancelled, err := s.Cancel(context.Background(), ref)
	if err != nil || cancelled.Status != authoring.StatusCancelled || len(store.cancelled) != 1 {
		t.Fatalf("Cancel = %+v, %v", cancelled, err)
	}
	ref.AuthoringID = "finished"
	if _, err := s.Cancel(context.Background(), ref); !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("Cancel finished = %v", err)
	}
	ref.UserID, ref.AuthoringID = "viewer", "authoring-1"
	if _, err := s.Cancel(context.Background(), ref); !errors.Is(err, fault.ErrForbidden) {
		t.Fatalf("Cancel by viewer = %v", err)
	}
}

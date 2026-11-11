package run_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/clock/clocktest"
	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/device"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/run"
	"github.com/mobqa/backend/internal/usecase/test"
)

const projID = "project-1"

func now() time.Time {
	return time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
}

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
	tests        []test.Test
	devices      []device.Device
	builds       map[string]run.Pin
	current      *run.Pin
	environments map[string]run.Pin
	fallback     *run.Pin
	created      []run.NewRun
	cancelled    []string
	finished     bool
	unscripted   []string
}

func online() time.Time {
	return now().Add(time.Minute)
}

func newStore() *fakeStore {
	until := online()
	return &fakeStore{
		tests: []test.Test{
			{ID: "test-1", Platform: project.PlatformAndroid, Version: 3, Content: test.Content{Title: "Sign in"}},
			{ID: "test-2", Platform: project.PlatformAndroid, Version: 1, Content: test.Content{Title: "Pay"}},
			{ID: "test-ios", Platform: project.PlatformIOS, Version: 1, Content: test.Content{Title: "Pay"}},
		},
		devices: []device.Device{
			{ID: "pixel", HubID: "hub-1", Serial: "R58", Name: "Pixel", Platform: project.PlatformAndroid,
				State: device.StateConnected, HubOnlineUntil: &until},
			{ID: "galaxy", HubID: "hub-2", Serial: "S23", Name: "Galaxy", Platform: project.PlatformAndroid,
				State: device.StateDisconnected, HubOnlineUntil: &until},
			{ID: "iphone", HubID: "hub-1", Serial: "0000", Name: "iPhone", Platform: project.PlatformIOS,
				State: device.StateConnected, HubOnlineUntil: &until},
		},
		builds: map[string]run.Pin{
			"apk": {ID: "apk", Label: "1.2 (40)", Platform: project.PlatformAndroid},
			"ipa": {ID: "ipa", Label: "1.2 (40)", Platform: project.PlatformIOS},
		},
		environments: map[string]run.Pin{"staging": {ID: "staging", Label: "Staging"}},
	}
}

func (s *fakeStore) Tests(_ context.Context, _ string, ids []string) ([]test.Test, error) {
	var found []test.Test
	for _, t := range s.tests {
		if slices.Contains(ids, t.ID) {
			found = append(found, t)
		}
	}
	return found, nil
}

func (s *fakeStore) Scripted(_ context.Context, tests []test.Test) ([]string, error) {
	var scripted []string
	for _, t := range tests {
		if !slices.Contains(s.unscripted, t.ID) {
			scripted = append(scripted, t.ID)
		}
	}
	return scripted, nil
}

func (s *fakeStore) Devices(_ context.Context, _ string, ids []string) ([]device.Device, error) {
	var found []device.Device
	for _, d := range s.devices {
		if slices.Contains(ids, d.ID) {
			found = append(found, d)
		}
	}
	return found, nil
}

func (s *fakeStore) Build(_ context.Context, _, id string) (run.Pin, error) {
	if pin, ok := s.builds[id]; ok {
		return pin, nil
	}
	return run.Pin{}, fault.ErrNotFound
}

func (s *fakeStore) CurrentBuild(context.Context, string, project.Platform) (run.Pin, error) {
	if s.current == nil {
		return run.Pin{}, fault.ErrNotFound
	}
	return *s.current, nil
}

func (s *fakeStore) Environment(_ context.Context, _, id string) (run.Pin, error) {
	if pin, ok := s.environments[id]; ok {
		return pin, nil
	}
	return run.Pin{}, fault.ErrNotFound
}

func (s *fakeStore) DefaultEnvironment(context.Context, string) (run.Pin, error) {
	if s.fallback == nil {
		return run.Pin{}, fault.ErrNotFound
	}
	return *s.fallback, nil
}

func (s *fakeStore) CreateRun(_ context.Context, in run.NewRun) (run.Run, error) {
	s.created = append(s.created, in)
	return in.Run, nil
}

func (s *fakeStore) Runs(context.Context, run.Query) ([]run.Run, int, error) {
	return nil, 0, nil
}

func (s *fakeStore) Run(_ context.Context, _, id string) (run.Run, error) {
	if id != "run-1" {
		return run.Run{}, fault.ErrNotFound
	}
	return run.Run{ID: id}, nil
}

func (s *fakeStore) Results(context.Context, string) ([]run.Result, error) {
	return []run.Result{{ID: "result-1"}}, nil
}

func (s *fakeStore) Attempts(_ context.Context, _, resultID string) ([]run.Attempt, error) {
	if resultID != "result-1" {
		return nil, fault.ErrNotFound
	}
	return []run.Attempt{{Number: 1}}, nil
}

func (s *fakeStore) CancelRun(_ context.Context, _, id string) (run.Run, error) {
	if s.finished {
		return run.Run{}, fault.ErrConflict
	}
	s.cancelled = append(s.cancelled, id)
	return run.Run{ID: id, Status: run.StatusCancelled}, nil
}

func newService(store *fakeStore) *run.Service {
	return run.NewService(store, fakeAccess{}, clocktest.New(now()))
}

func requireKind(t *testing.T, err, kind error, detail string) {
	t.Helper()
	if !errors.Is(err, kind) {
		t.Fatalf("error = %v, want %v", err, kind)
	}
	if got, _ := fault.Detail(err); got != detail {
		t.Fatalf("detail = %q, want %q", got, detail)
	}
}

func TestCreatePlansEveryTestOnEveryDevice(t *testing.T) {
	store := newStore()
	store.current = &run.Pin{ID: "apk", Label: "1.2 (40)", Platform: project.PlatformAndroid}
	store.fallback = &run.Pin{ID: "staging", Label: "Staging"}
	store.devices[1].State = device.StateConnected

	created, err := newService(store).Create(context.Background(), run.CreateInput{
		UserID: "engineer", ProjectID: projID, TestIDs: []string{"test-2", "test-1"},
		DeviceIDs: []string{"pixel", "galaxy"}, Retries: 1,
	})

	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" || created.Status != run.StatusQueued || created.Platform != project.PlatformAndroid ||
		created.BuildID != "apk" || created.EnvironmentName != "Staging" || created.Retries != 1 {
		t.Fatalf("run = %+v", created)
	}
	plan := store.created[0]
	if len(plan.Results) != 4 || len(plan.Jobs) != 2 {
		t.Fatalf("plan = %+v", plan)
	}
	first := plan.Results[0]
	if first.TestID != "test-2" || first.Position != 0 || first.DeviceSerial != "R58" || first.HubID != "hub-1" ||
		plan.Results[1].TestVersion != 3 || plan.Results[1].Title != "Sign in" {
		t.Fatalf("results = %+v", plan.Results)
	}
	var payload struct {
		RunID    string `json:"run_id"`
		DeviceID string `json:"device_id"`
	}
	job := plan.Jobs["galaxy"]
	if err := json.Unmarshal(job.Payload, &payload); err != nil || payload.RunID != created.ID ||
		payload.DeviceID != "galaxy" || job.Kind != run.DeviceJobKind {
		t.Fatalf("job = %+v, payload %+v", job, payload)
	}
}

func TestCreateWithoutBuildOrEnvironment(t *testing.T) {
	store := newStore()

	created, err := newService(store).Create(context.Background(), run.CreateInput{
		UserID: "engineer", ProjectID: projID, TestIDs: []string{"test-ios"}, DeviceIDs: []string{"iphone"},
	})

	if err != nil || created.BuildID != "" || created.BuildLabel != "" || created.EnvironmentID != "" {
		t.Fatalf("Create = %+v, %v", created, err)
	}
}

func TestCreateRejects(t *testing.T) {
	tests := []struct {
		name   string
		in     run.CreateInput
		kind   error
		detail string
	}{
		{
			name: "no tests", in: run.CreateInput{DeviceIDs: []string{"pixel"}},
			kind: fault.ErrInvalidInput, detail: "a run needs 1 to 50 distinct tests",
		},
		{
			name: "repeated device", in: run.CreateInput{TestIDs: []string{"test-1"}, DeviceIDs: []string{"pixel", "pixel"}},
			kind: fault.ErrInvalidInput, detail: "a run needs 1 to 10 distinct devices",
		},
		{
			name: "too many retries", in: run.CreateInput{TestIDs: []string{"test-1"}, DeviceIDs: []string{"pixel"}, Retries: 3},
			kind: fault.ErrInvalidInput, detail: "retries must be 0 to 2",
		},
		{
			name: "unknown test", in: run.CreateInput{TestIDs: []string{"test-9"}, DeviceIDs: []string{"pixel"}},
			kind: fault.ErrNotFound, detail: "test not found",
		},
		{
			name: "mixed platforms", in: run.CreateInput{TestIDs: []string{"test-1", "test-ios"}, DeviceIDs: []string{"pixel"}},
			kind: fault.ErrInvalidInput, detail: "all tests of a run must target one platform",
		},
		{
			name: "unknown device", in: run.CreateInput{TestIDs: []string{"test-1"}, DeviceIDs: []string{"nokia"}},
			kind: fault.ErrNotFound, detail: "device not found",
		},
		{
			name: "device of another platform", in: run.CreateInput{TestIDs: []string{"test-1"}, DeviceIDs: []string{"iphone"}},
			kind: fault.ErrInvalidInput, detail: "every device must match the tests' platform",
		},
		{
			name: "offline device", in: run.CreateInput{TestIDs: []string{"test-1"}, DeviceIDs: []string{"galaxy"}},
			kind: fault.ErrConflict, detail: "every device must be online",
		},
		{
			name: "build of another platform",
			in:   run.CreateInput{TestIDs: []string{"test-1"}, DeviceIDs: []string{"pixel"}, BuildID: "ipa"},
			kind: fault.ErrInvalidInput, detail: "the build must match the tests' platform",
		},
		{
			name: "unknown environment",
			in:   run.CreateInput{TestIDs: []string{"test-1"}, DeviceIDs: []string{"pixel"}, EnvironmentID: "prod"},
			kind: fault.ErrNotFound, detail: "environment not found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.in.UserID, tt.in.ProjectID = "engineer", projID

			_, err := newService(newStore()).Create(context.Background(), tt.in)

			requireKind(t, err, tt.kind, tt.detail)
		})
	}
}

func TestCreateNeedsScriptedTests(t *testing.T) {
	store := newStore()
	store.unscripted = []string{"test-2"}

	_, err := newService(store).Create(context.Background(), run.CreateInput{
		UserID: "engineer", ProjectID: projID, TestIDs: []string{"test-1", "test-2"}, DeviceIDs: []string{"pixel"},
	})

	requireKind(t, err, fault.ErrConflict, `test "Pay" has no script for its current version; author it on a device first`)
}

func TestCreateNeedsEngineer(t *testing.T) {
	_, err := newService(newStore()).Create(context.Background(), run.CreateInput{
		UserID: "viewer", ProjectID: projID, TestIDs: []string{"test-1"}, DeviceIDs: []string{"pixel"},
	})

	if !errors.Is(err, fault.ErrForbidden) {
		t.Fatalf("error = %v, want ErrForbidden", err)
	}
}

func TestDeviceGoesOfflineWhenHubPresenceLapses(t *testing.T) {
	store := newStore()
	lapsed := now()
	store.devices[0].HubOnlineUntil = &lapsed

	_, err := newService(store).Create(context.Background(), run.CreateInput{
		UserID: "engineer", ProjectID: projID, TestIDs: []string{"test-1"}, DeviceIDs: []string{"pixel"},
	})

	requireKind(t, err, fault.ErrConflict, "every device must be online")
}

func TestGetAndAttemptsAreScopedToTheRun(t *testing.T) {
	service := newService(newStore())
	ref := run.Ref{UserID: "viewer", ProjectID: projID, RunID: "run-1"}

	found, results, err := service.Get(context.Background(), ref)
	if err != nil || found.ID != "run-1" || len(results) != 1 {
		t.Fatalf("Get = %+v, %+v, %v", found, results, err)
	}
	_, err = service.Attempts(context.Background(), ref, "result-9")
	requireKind(t, err, fault.ErrNotFound, "result not found")
	_, _, err = service.Get(context.Background(), run.Ref{UserID: "viewer", ProjectID: projID, RunID: "run-2"})
	requireKind(t, err, fault.ErrNotFound, "run not found")
}

func TestCancel(t *testing.T) {
	store := newStore()
	ref := run.Ref{UserID: "engineer", ProjectID: projID, RunID: "run-1"}

	cancelled, err := newService(store).Cancel(context.Background(), ref)
	if err != nil || cancelled.Status != run.StatusCancelled || !slices.Equal(store.cancelled, []string{"run-1"}) {
		t.Fatalf("Cancel = %+v, %v", cancelled, err)
	}
	store.finished = true
	_, err = newService(store).Cancel(context.Background(), ref)
	requireKind(t, err, fault.ErrConflict, "the run has already finished")
	_, err = newService(store).Cancel(context.Background(), run.Ref{UserID: "viewer", ProjectID: projID, RunID: "run-1"})
	if !errors.Is(err, fault.ErrForbidden) {
		t.Fatalf("viewer cancel error = %v", err)
	}
}

func TestListRejectsUnknownStatus(t *testing.T) {
	_, err := newService(newStore()).List(context.Background(),
		run.ListInput{UserID: "viewer", ProjectID: projID, Status: "exploded"})

	requireKind(t, err, fault.ErrInvalidInput, "unknown run status")
}

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
	"github.com/mobqa/backend/internal/usecase/authoring"
	"github.com/mobqa/backend/internal/usecase/build"
	"github.com/mobqa/backend/internal/usecase/device"
	"github.com/mobqa/backend/internal/usecase/hub"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/run"
	"github.com/mobqa/backend/internal/usecase/ui"
	"github.com/mobqa/backend/internal/worker"
)

type runFixture struct {
	testFixture
	runs    *postgres.Runs
	jobs    *postgres.Jobs
	devices []device.Device
	testIDs []string
	build   build.Build
}

// newRuns sets up a project with two Android tests, two devices on one hub and a current build.
func newRuns(t *testing.T) runFixture {
	t.Helper()
	f := runFixture{testFixture: newTests(t)}
	f.runs, f.jobs = postgres.NewRuns(f.db), postgres.NewJobs(f.db)
	f.testIDs = []string{f.add(t, "Sign in").ID, f.add(t, "Pay").ID}
	ctx := context.Background()
	hubs := postgres.NewHubs(f.db)
	h, err := hubs.CreateHub(ctx, hub.Hub{
		ID: uuid.Must(uuid.NewV7()).String(), ProjectID: f.projectID, Name: "Office Mac", CreatedBy: f.userID,
	}, []byte("token"))
	if err != nil {
		t.Fatalf("CreateHub: %v", err)
	}
	now := f.now.Now()
	err = hubs.ReplaceInventory(ctx, h.ID, hub.Inventory{Version: "1", Devices: []hub.ReportedDevice{
		reported("A1", project.PlatformAndroid), reported("B2", project.PlatformAndroid),
	}}, hub.Presence{SeenAt: now, OnlineUntil: now.Add(time.Hour)})
	if err != nil {
		t.Fatalf("ReplaceInventory: %v", err)
	}
	if f.devices, err = postgres.NewDevices(f.db).Devices(ctx, device.Query{ProjectID: f.projectID}); err != nil {
		t.Fatalf("Devices: %v", err)
	}
	for _, testID := range f.testIDs {
		f.script(t, testID, f.devices[0])
	}
	builds := postgres.NewBuilds(f.db)
	id := uuid.Must(uuid.NewV7()).String()
	f.build, err = builds.CreateBuild(ctx, build.Build{
		ID: id, ProjectID: f.projectID, Platform: project.PlatformAndroid, Version: "1.2", BuildNumber: "40",
		FileName: "shop.apk", Size: 2048, SHA256: "cafe", BlobKey: "builds/" + id, UploadedBy: f.userID,
		Status: build.StatusProcessing,
	}, worker.EnqueueInput{Kind: "build_analysis", Payload: []byte(`{}`), MaxAttempts: 1})
	if err != nil {
		t.Fatalf("CreateBuild: %v", err)
	}
	if _, err := builds.SetCurrent(ctx, f.projectID, id); err != nil {
		t.Fatalf("SetCurrent: %v", err)
	}
	return f
}

// script records a succeeded authoring of the test's first version that taps its title.
func (f runFixture) script(t *testing.T, testID string, d device.Device) {
	t.Helper()
	ctx := context.Background()
	authorings := postgres.NewAuthorings(f.db)
	created, err := authorings.CreateAuthoring(ctx, authoring.NewAuthoring{
		Authoring: authoring.Authoring{
			ID: uuid.Must(uuid.NewV7()).String(), ProjectID: f.projectID, TestID: testID, TestVersion: 1,
			Platform: project.PlatformAndroid, DeviceID: d.ID, DeviceName: d.Name, Status: authoring.StatusQueued,
			CreatedBy: f.userID,
		},
		HubID: d.HubID, Serial: d.Serial,
		Job: worker.EnqueueInput{Kind: authoring.JobKind, Payload: []byte(`{}`), MaxAttempts: 1},
	})
	if err != nil {
		t.Fatalf("CreateAuthoring: %v", err)
	}
	if _, err := authorings.StartAuthoring(ctx, created.ID); err != nil {
		t.Fatalf("StartAuthoring: %v", err)
	}
	err = authorings.FinishAuthoring(ctx, created.ID, authoring.Finish{Status: authoring.StatusSucceeded, Script: []ui.Op{
		{Step: 1, Kind: ui.KindTap, Selector: &ui.Selector{Text: testID}},
	}})
	if err != nil {
		t.Fatalf("FinishAuthoring: %v", err)
	}
}

// start queues a run of both tests on both devices, pinned to the current build.
func (f runFixture) start(t *testing.T, retries int) run.Run {
	t.Helper()
	pin, err := f.runs.CurrentBuild(context.Background(), f.projectID, project.PlatformAndroid)
	if err != nil {
		t.Fatalf("CurrentBuild: %v", err)
	}
	r := run.Run{
		ID: uuid.Must(uuid.NewV7()).String(), ProjectID: f.projectID, Platform: project.PlatformAndroid,
		BuildID: pin.ID, BuildLabel: pin.Label, Status: run.StatusQueued, Retries: retries, CreatedBy: f.userID,
	}
	plan := run.NewRun{Run: r, Jobs: map[string]worker.EnqueueInput{}}
	for _, d := range f.devices {
		plan.Jobs[d.ID] = worker.EnqueueInput{Kind: run.DeviceJobKind, Payload: []byte(`{}`), MaxAttempts: 3}
		for position, testID := range f.testIDs {
			plan.Results = append(plan.Results, run.NewResult{
				Position: position, TestID: testID, TestVersion: 1, Title: "title", DeviceID: d.ID,
				DeviceName: d.Name, DeviceSerial: d.Serial, HubID: d.HubID,
			})
		}
	}
	created, err := f.runs.CreateRun(context.Background(), plan)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return created
}

func (f runFixture) work(t *testing.T, runID string, d device.Device) run.Work {
	t.Helper()
	work, err := f.runs.Work(context.Background(), runID, d.ID)
	if err != nil {
		t.Fatalf("Work: %v", err)
	}
	return work
}

func (f runFixture) finish(t *testing.T, resultID string, status run.ResultStatus) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.runs.StartResult(ctx, resultID); err != nil {
		t.Fatalf("StartResult: %v", err)
	}
	if err := f.runs.FinishResult(ctx, resultID, run.Finish{Status: status}); err != nil {
		t.Fatalf("FinishResult: %v", err)
	}
}

func TestRunsLookups(t *testing.T) {
	f := newRuns(t)
	ctx := context.Background()

	tests, err := f.runs.Tests(ctx, f.projectID, []string{f.testIDs[1], "not-a-uuid", uuid.NewString()})
	if err != nil || len(tests) != 1 || tests[0].Content.Title != "Pay" || tests[0].Version != 1 {
		t.Fatalf("Tests = %+v, %v", tests, err)
	}
	devices, err := f.runs.Devices(ctx, f.projectID, []string{f.devices[0].ID})
	if err != nil || len(devices) != 1 || devices[0].Serial != "A1" || devices[0].HubOnlineUntil == nil {
		t.Fatalf("Devices = %+v, %v", devices, err)
	}
	pin, err := f.runs.Build(ctx, f.projectID, f.build.ID)
	if err != nil || pin.Label != "1.2 (40)" || pin.Platform != project.PlatformAndroid {
		t.Fatalf("Build = %+v, %v", pin, err)
	}
	if _, err := f.runs.CurrentBuild(ctx, f.projectID, project.PlatformIOS); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("CurrentBuild ios error = %v", err)
	}
	if _, err := f.runs.DefaultEnvironment(ctx, f.projectID); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("DefaultEnvironment error = %v", err)
	}
	unscripted := f.add(t, "Refund")
	scripted, err := f.runs.Scripted(ctx, append(tests, unscripted))
	if err != nil || len(scripted) != 1 || scripted[0] != f.testIDs[1] {
		t.Fatalf("Scripted = %v, %v", scripted, err)
	}
}

func TestRunsCreateSnapshotsStepsAndQueuesJobs(t *testing.T) {
	f := newRuns(t)

	created := f.start(t, 1)

	if created.Status != run.StatusQueued || created.Summary.Total != 4 || created.Summary.Queued != 4 ||
		created.BuildLabel != "1.2 (40)" || !created.CreatedAt.Equal(f.now.Now()) {
		t.Fatalf("created = %+v", created)
	}
	work := f.work(t, created.ID, f.devices[0])
	if len(work.Results) != 2 || work.Serial != "A1" || work.HubID != f.devices[0].HubID ||
		work.Package == nil || work.Package.SHA256 != "cafe" || work.Package.Size != 2048 ||
		len(work.Results[0].Steps) != 2 || work.Results[0].Steps[0].Target != "Sign in" ||
		len(work.Results[0].Script) != 1 || work.Results[0].Script[0].Selector.Text != f.testIDs[0] {
		t.Fatalf("work = %+v", work)
	}
	for range 2 {
		if _, ok, err := f.jobs.Claim(context.Background(), worker.ClaimInput{
			Kind: run.DeviceJobKind, Owner: "test", Lease: time.Minute,
		}); err != nil || !ok {
			t.Fatalf("Claim = %v, %v", ok, err)
		}
	}
}

func TestRunsCreateFailsForVanishedTestVersion(t *testing.T) {
	f := newRuns(t)
	if err := f.tests.DeleteTest(context.Background(), f.projectID, f.testIDs[1]); err != nil {
		t.Fatalf("DeleteTest: %v", err)
	}

	_, err := f.runs.CreateRun(context.Background(), run.NewRun{
		Run: run.Run{
			ID: uuid.NewString(), ProjectID: f.projectID, Platform: project.PlatformAndroid, Status: run.StatusQueued,
			CreatedBy: f.userID,
		},
		Results: []run.NewResult{{TestID: f.testIDs[1], TestVersion: 1, DeviceID: f.devices[0].ID,
			HubID: f.devices[0].HubID}},
	})

	if !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("CreateRun error = %v, want ErrNotFound", err)
	}
}

func TestRunsCreateFailsForUnscriptedTestVersion(t *testing.T) {
	f := newRuns(t)
	unscripted := f.add(t, "Refund")

	_, err := f.runs.CreateRun(context.Background(), run.NewRun{
		Run: run.Run{
			ID: uuid.NewString(), ProjectID: f.projectID, Platform: project.PlatformAndroid, Status: run.StatusQueued,
			CreatedBy: f.userID,
		},
		Results: []run.NewResult{{TestID: unscripted.ID, TestVersion: 1, DeviceID: f.devices[0].ID,
			HubID: f.devices[0].HubID}},
	})

	if !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("CreateRun error = %v, want ErrNotFound", err)
	}
}

func TestRunsAttemptsAndResultLifecycle(t *testing.T) {
	f := newRuns(t)
	created := f.start(t, 1)
	ctx := context.Background()
	first := f.work(t, created.ID, f.devices[0]).Results[0]

	if err := f.runs.RecordAttempt(ctx, first.ID, run.Attempt{Number: 1, Status: run.AttemptFailed}); !errors.Is(err,
		fault.ErrConflict) {
		t.Fatalf("RecordAttempt before start error = %v", err)
	}
	started, err := f.runs.StartResult(ctx, first.ID)
	if err != nil || !started {
		t.Fatalf("StartResult = %v, %v", started, err)
	}
	attempt := run.Attempt{
		Number: 1, Status: run.AttemptFailed, FailedStep: 2, Error: "Home is not shown",
		Steps:     []run.StepOutcome{{Status: run.StepPassed, Duration: 1500 * time.Millisecond}, {Status: run.StepFailed}},
		StartedAt: f.now.Now(), FinishedAt: f.now.Now().Add(time.Second),
	}
	if err := f.runs.RecordAttempt(ctx, first.ID, attempt); err != nil {
		t.Fatalf("RecordAttempt: %v", err)
	}
	if err := f.runs.RecordAttempt(ctx, first.ID, attempt); !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("repeated RecordAttempt error = %v", err)
	}
	attempts, err := f.runs.Attempts(ctx, created.ID, first.ID)
	if err != nil || len(attempts) != 1 || attempts[0].FailedStep != 2 ||
		attempts[0].Steps[0].Duration != 1500*time.Millisecond || attempts[0].Steps[1].Status != run.StepFailed {
		t.Fatalf("Attempts = %+v, %v", attempts, err)
	}
	resumed := f.work(t, created.ID, f.devices[0]).Results[0]
	if resumed.Status != run.ResultRunning || resumed.Attempts != 1 || resumed.FailedStep != 2 {
		t.Fatalf("resumed = %+v", resumed)
	}
	found, err := f.runs.Run(ctx, f.projectID, created.ID)
	if err != nil || found.Status != run.StatusRunning || found.StartedAt == nil || found.Summary.Running != 1 {
		t.Fatalf("Run = %+v, %v", found, err)
	}
	if _, err := f.runs.Attempts(ctx, uuid.NewString(), first.ID); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("Attempts of another run error = %v", err)
	}
}

func TestRunsKeepWhatHubsFilmed(t *testing.T) {
	f := newRuns(t)
	created := f.start(t, 1)
	ctx := context.Background()
	first := f.work(t, created.ID, f.devices[0]).Results[0]
	hubID := f.devices[0].HubID

	if _, err := f.runs.RunningResult(ctx, hubID, first.ID); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("RunningResult before start error = %v", err)
	}
	if _, err := f.runs.StartResult(ctx, first.ID); err != nil {
		t.Fatalf("StartResult: %v", err)
	}
	running, err := f.runs.RunningResult(ctx, hubID, first.ID)
	if err != nil || running.ID != first.ID || running.Attempts != 0 {
		t.Fatalf("RunningResult = %+v, %v", running, err)
	}
	if _, err := f.runs.RunningResult(ctx, uuid.NewString(), first.ID); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("RunningResult of another hub error = %v", err)
	}
	for number, video := range []*run.Video{{Duration: 9 * time.Second}, {Problem: "no frames arrived"}, nil} {
		attempt := run.Attempt{Number: number + 1, Status: run.AttemptFailed, Steps: []run.StepOutcome{}, Video: video}
		if err := f.runs.RecordAttempt(ctx, first.ID, attempt); err != nil {
			t.Fatalf("RecordAttempt %d: %v", number+1, err)
		}
	}

	attempts, err := f.runs.Attempts(ctx, created.ID, first.ID)
	if err != nil || len(attempts) != 3 || *attempts[0].Video != (run.Video{Duration: 9 * time.Second}) ||
		*attempts[1].Video != (run.Video{Problem: "no frames arrived"}) || attempts[2].Video != nil {
		t.Fatalf("Attempts = %+v, %v", attempts, err)
	}
}

func TestRunsFinishWhenLastResultFinishes(t *testing.T) {
	f := newRuns(t)
	created := f.start(t, 0)
	ctx := context.Background()
	var ids []string
	for _, d := range f.devices {
		for _, r := range f.work(t, created.ID, d).Results {
			ids = append(ids, r.ID)
		}
	}

	f.finish(t, ids[0], run.ResultPassed)
	f.finish(t, ids[1], run.ResultFlaky)
	f.finish(t, ids[2], run.ResultError)
	midway, _ := f.runs.Run(ctx, f.projectID, created.ID)
	f.finish(t, ids[3], run.ResultPassed)

	if midway.Status != run.StatusRunning || midway.FinishedAt != nil {
		t.Fatalf("midway = %+v", midway)
	}
	done, err := f.runs.Run(ctx, f.projectID, created.ID)
	if err != nil || done.Status != run.StatusFailed || done.FinishedAt == nil || done.Summary.Errored != 1 ||
		done.Summary.Flaky != 1 || done.Summary.Passed != 2 {
		t.Fatalf("done = %+v, %v", done, err)
	}
	if err := f.runs.FinishResult(ctx, ids[0], run.Finish{Status: run.ResultFailed}); !errors.Is(err,
		fault.ErrConflict) {
		t.Fatalf("finishing twice error = %v", err)
	}
	results, err := f.runs.Results(ctx, created.ID)
	if err != nil || len(results) != 4 || results[0].DeviceName != "A1 phone" || results[0].Position != 0 {
		t.Fatalf("Results = %+v, %v", results, err)
	}
}

func TestRunsCancelStopsResultsAndJobs(t *testing.T) {
	f := newRuns(t)
	created := f.start(t, 0)
	ctx := context.Background()
	job, ok, err := f.jobs.Claim(ctx, worker.ClaimInput{Kind: run.DeviceJobKind, Owner: "test", Lease: time.Minute})
	if err != nil || !ok {
		t.Fatalf("Claim = %v, %v", ok, err)
	}
	first := f.work(t, created.ID, f.devices[0]).Results[0]
	f.finish(t, first.ID, run.ResultPassed)

	cancelled, err := f.runs.CancelRun(ctx, f.projectID, created.ID)

	if err != nil || cancelled.Status != run.StatusCancelled || cancelled.FinishedAt == nil ||
		cancelled.Summary.Cancelled != 3 || cancelled.Summary.Passed != 1 {
		t.Fatalf("CancelRun = %+v, %v", cancelled, err)
	}
	lease, err := f.jobs.Heartbeat(ctx, worker.HeartbeatInput{JobID: job.ID, Owner: "test", Lease: time.Minute})
	if err != nil || !lease.CancelRequested {
		t.Fatalf("Heartbeat = %+v, %v", lease, err)
	}
	if _, ok, _ := f.jobs.Claim(ctx, worker.ClaimInput{Kind: run.DeviceJobKind, Owner: "test", Lease: time.Minute}); ok {
		t.Fatal("the queued device job is still claimable")
	}
	if started, _ := f.runs.StartResult(ctx, first.ID); started {
		t.Fatal("a finished result started again")
	}
	if _, err := f.runs.CancelRun(ctx, f.projectID, created.ID); !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("second CancelRun error = %v", err)
	}
	if _, err := f.runs.CancelRun(ctx, f.projectID, uuid.NewString()); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("CancelRun of unknown run error = %v", err)
	}
}

func TestRunsListNewestFirst(t *testing.T) {
	f := newRuns(t)
	older := f.start(t, 0)
	f.now.Advance(time.Minute)
	newer := f.start(t, 0)
	if _, err := f.runs.CancelRun(context.Background(), f.projectID, older.ID); err != nil {
		t.Fatalf("CancelRun: %v", err)
	}

	all, total, err := f.runs.Runs(context.Background(), run.Query{ProjectID: f.projectID, Limit: 10})
	if err != nil || total != 2 || all[0].ID != newer.ID || all[1].Summary.Cancelled != 4 {
		t.Fatalf("Runs = %+v, %d, %v", all, total, err)
	}
	queued, total, err := f.runs.Runs(context.Background(),
		run.Query{ProjectID: f.projectID, Status: run.StatusQueued, Limit: 10})
	if err != nil || total != 1 || queued[0].ID != newer.ID {
		t.Fatalf("queued = %+v, %d, %v", queued, total, err)
	}
}

func TestRunsWorkWithoutPackageAfterBuildDeleted(t *testing.T) {
	f := newRuns(t)
	created := f.start(t, 0)
	if _, err := postgres.NewBuilds(f.db).DeleteBuild(context.Background(), f.projectID, f.build.ID); err != nil {
		t.Fatalf("DeleteBuild: %v", err)
	}

	work := f.work(t, created.ID, f.devices[0])

	if work.Package != nil || work.Run.BuildID != "" || work.Run.BuildLabel != "1.2 (40)" {
		t.Fatalf("work = %+v", work)
	}
}

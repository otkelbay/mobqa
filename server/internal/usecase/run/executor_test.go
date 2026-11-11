package run_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/clock/clocktest"
	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/action"
	"github.com/mobqa/backend/internal/usecase/device"
	"github.com/mobqa/backend/internal/usecase/environment"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/run"
	"github.com/mobqa/backend/internal/usecase/test"
	"github.com/mobqa/backend/internal/usecase/ui"
	"github.com/mobqa/backend/internal/worker"
)

type fakeExecutions struct {
	work     run.Work
	statuses map[string]run.ResultStatus
	attempts map[string][]run.Attempt
	finished map[string]run.Finish
}

func newExecutions(retries int, ids ...string) *fakeExecutions {
	f := &fakeExecutions{
		work: run.Work{
			Run: run.Run{
				ID: "run-1", ProjectID: projID, Platform: project.PlatformAndroid, Retries: retries,
				EnvironmentID: "staging", EnvironmentName: "Staging",
			},
			HubID: "hub-1", Serial: "R58",
		},
		statuses: map[string]run.ResultStatus{}, attempts: map[string][]run.Attempt{}, finished: map[string]run.Finish{},
	}
	for i, id := range ids {
		f.work.Results = append(f.work.Results, run.Pending{
			Result: run.Result{ID: id, Position: i, Title: "Sign in", Status: run.ResultQueued},
			Steps:  []test.Step{{Action: test.ActionTap, Target: "Sign in"}, {Action: test.ActionAssert, Target: "Home"}},
			Script: []ui.Op{
				{Step: 1, Kind: ui.KindTap, Selector: &ui.Selector{Text: "Sign in"}},
				{Step: 2, Kind: ui.KindAssert, Selector: &ui.Selector{Text: "Home"}},
			},
		})
		f.statuses[id] = run.ResultQueued
	}
	return f
}

func (f *fakeExecutions) Work(context.Context, string, string) (run.Work, error) {
	return f.work, nil
}

func (f *fakeExecutions) StartResult(_ context.Context, id string) (bool, error) {
	if f.statuses[id] != run.ResultQueued && f.statuses[id] != run.ResultRunning {
		return false, nil
	}
	f.statuses[id] = run.ResultRunning
	return true, nil
}

func (f *fakeExecutions) RecordAttempt(_ context.Context, id string, attempt run.Attempt) error {
	if f.statuses[id] != run.ResultRunning {
		return fault.ErrConflict
	}
	f.attempts[id] = append(f.attempts[id], attempt)
	return nil
}

func (f *fakeExecutions) FinishResult(_ context.Context, id string, finish run.Finish) error {
	if f.statuses[id] != run.ResultQueued && f.statuses[id] != run.ResultRunning {
		return fault.ErrConflict
	}
	f.statuses[id] = finish.Status
	f.finished[id] = finish
	return nil
}

type fakeEnvironments struct{}

func (fakeEnvironments) Resolve(_ context.Context, _, id string) (environment.Environment, error) {
	if id != "staging" {
		return environment.Environment{}, fault.ErrNotFound
	}
	return environment.Environment{ID: id, Variables: []environment.Variable{
		{Name: "PHONE", Value: "+77001234567"}, {Name: "OTP", Value: "0000"},
	}}, nil
}

type fakeActions struct{}

func (fakeActions) Actions(context.Context, string) ([]action.Action, error) {
	return []action.Action{
		{Name: "Fetch OTP", Request: action.Request{Method: action.MethodGet, URL: "{{API_URL}}/otp"}},
		{Name: "Reset user", Request: action.Request{Method: action.MethodPost, URL: "{{API_URL}}/reset"}},
	}, nil
}

// fakeHubs answers executions with scripted replies in order; an empty script fails as unreachable.
type fakeHubs struct {
	replies    []reply
	executions []run.Execution
	onExecute  func()
}

type reply struct {
	outcome run.Outcome
	err     error
}

func passed() reply {
	return reply{outcome: run.Outcome{Status: run.AttemptPassed, Steps: []run.StepOutcome{
		{Status: run.StepPassed}, {Status: run.StepPassed},
	}}}
}

func failed() reply {
	return reply{outcome: run.Outcome{Status: run.AttemptFailed, FailedStep: 2, Error: "Home is not shown"}}
}

func unreachable() reply {
	return reply{err: fault.WithDetail(fault.ErrUnavailable, "the hub is not connected")}
}

func (h *fakeHubs) Execute(_ context.Context, _ string, execution run.Execution) (run.Outcome, error) {
	h.executions = append(h.executions, execution)
	if h.onExecute != nil {
		h.onExecute()
	}
	if len(h.replies) == 0 {
		return run.Outcome{}, unreachable().err
	}
	next := h.replies[0]
	h.replies = h.replies[1:]
	return next.outcome, next.err
}

func newExecutor(store *fakeExecutions, hubs *fakeHubs) *run.Executor {
	return lockedExecutor(store, hubs, device.NewLocks())
}

func lockedExecutor(store *fakeExecutions, hubs *fakeHubs, locks *device.Locks) *run.Executor {
	return run.NewExecutor(run.ExecutorDependencies{
		Store: store, Environments: fakeEnvironments{}, Actions: fakeActions{}, Hubs: hubs,
		Locks: locks, Clock: clocktest.New(now()),
	})
}

func deviceJob(attempt int) worker.Job {
	return worker.Job{
		Kind: run.DeviceJobKind, Payload: []byte(`{"run_id":"run-1","device_id":"pixel"}`), Attempt: attempt,
		MaxAttempts: 3,
	}
}

func TestExecutorRunsResultsInOrderWithRetries(t *testing.T) {
	store := newExecutions(1, "first", "second", "third")
	hubs := &fakeHubs{replies: []reply{passed(), failed(), passed(), failed(), failed()}}

	if err := newExecutor(store, hubs).Run(context.Background(), deviceJob(1)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	want := map[string]run.ResultStatus{"first": run.ResultPassed, "second": run.ResultFlaky, "third": run.ResultFailed}
	for id, status := range want {
		if store.statuses[id] != status {
			t.Fatalf("%s = %s, want %s", id, store.statuses[id], status)
		}
	}
	third := store.finished["third"]
	if third.FailedStep != 2 || third.Error != "Home is not shown" || len(store.attempts["third"]) != 2 {
		t.Fatalf("third = %+v, attempts %+v", third, store.attempts["third"])
	}
	ids := make([]string, len(hubs.executions))
	for i, e := range hubs.executions {
		ids[i] = e.ID
	}
	if !slices.Equal(ids, []string{"first/1", "second/1", "second/2", "third/1", "third/2"}) {
		t.Fatalf("executions = %v", ids)
	}
}

func TestExecutorSendsEnvironmentAndProfileVariables(t *testing.T) {
	store := newExecutions(0, "first")
	store.work.Package = &run.Package{BuildID: "apk", SHA256: "abc", Size: 42}
	store.work.Run.BuildID, store.work.Run.BuildLabel = "apk", "1.2 (40)"
	hubs := &fakeHubs{replies: []reply{passed()}}

	if err := newExecutor(store, hubs).Run(context.Background(), deviceJob(1)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	sent := hubs.executions[0]
	if len(sent.Script) != 2 || sent.Script[1].Selector.Text != "Home" {
		t.Fatalf("script = %+v", sent.Script)
	}
	if sent.Serial != "R58" || sent.Package.SHA256 != "abc" || sent.Variables["PHONE"] != "+77001234567" ||
		sent.Variables["OTP"] != "0000" ||
		len(sent.Steps) != 2 || sent.Title != "Sign in" {
		t.Fatalf("execution = %+v", sent)
	}
}

func TestExecutorSendsOnlyTheActionsTheScriptCalls(t *testing.T) {
	store := newExecutions(0, "first")
	pending := &store.work.Results[0]
	pending.Steps = append(pending.Steps, test.Step{Action: test.ActionCall, Target: "Fetch OTP"})
	pending.Script = append(pending.Script, ui.Op{Step: 3, Kind: ui.KindCall, Text: "Fetch OTP"},
		ui.Op{Step: 3, Kind: ui.KindCall, Text: "Deleted action"})
	hubs := &fakeHubs{}

	_ = newExecutor(store, hubs).Run(context.Background(), deviceJob(1))

	actions := hubs.executions[0].Actions
	if len(actions) != 1 || actions["Fetch OTP"].URL != "{{API_URL}}/otp" {
		t.Fatalf("actions = %+v", actions)
	}
}

func TestExecutorWaitsForTheDeviceToBeFree(t *testing.T) {
	store := newExecutions(0, "first")
	hubs := &fakeHubs{replies: []reply{passed()}}
	locks := device.NewLocks()
	unlock, err := locks.Lock(context.Background(), "pixel")
	if err != nil {
		t.Fatalf("Lock: %v", err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	err = lockedExecutor(store, hubs, locks).Run(ctx, deviceJob(1))

	if !errors.Is(err, context.DeadlineExceeded) || len(hubs.executions) != 0 {
		t.Fatalf("Run = %v, executions %d", err, len(hubs.executions))
	}
}

func TestExecutorRetriesJobWhileHubIsUnreachable(t *testing.T) {
	store := newExecutions(0, "first", "second")
	hubs := &fakeHubs{replies: []reply{passed(), unreachable()}}

	err := newExecutor(store, hubs).Run(context.Background(), deviceJob(1))

	if !errors.Is(err, fault.ErrUnavailable) || store.statuses["first"] != run.ResultPassed ||
		store.statuses["second"] != run.ResultRunning {
		t.Fatalf("Run = %v, statuses %v", err, store.statuses)
	}
}

func TestExecutorAbandonsResultsOnLastAttempt(t *testing.T) {
	store := newExecutions(0, "first", "second")

	err := newExecutor(store, &fakeHubs{}).Run(context.Background(), deviceJob(3))

	if !errors.Is(err, fault.ErrUnavailable) {
		t.Fatalf("Run error = %v", err)
	}
	for _, id := range []string{"first", "second"} {
		if f := store.finished[id]; f.Status != run.ResultError || f.Error != "the device's hub stayed unreachable" {
			t.Fatalf("%s = %+v", id, f)
		}
	}
}

func TestExecutorClosesResultsWhoseBuildWasDeleted(t *testing.T) {
	store := newExecutions(0, "first")
	store.work.Run.BuildLabel = "1.2 (40)"
	hubs := &fakeHubs{}

	if err := newExecutor(store, hubs).Run(context.Background(), deviceJob(1)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if f := store.finished["first"]; f.Error != "the run's build was deleted" || len(hubs.executions) != 0 {
		t.Fatalf("first = %+v, executions %d", f, len(hubs.executions))
	}
}

func TestExecutorTreatsInvalidOutcomeAsError(t *testing.T) {
	store := newExecutions(0, "first")
	hubs := &fakeHubs{replies: []reply{{outcome: run.Outcome{Status: run.AttemptFailed, FailedStep: 7}}}}

	if err := newExecutor(store, hubs).Run(context.Background(), deviceJob(1)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if f := store.finished["first"]; f.Status != run.ResultError || f.Error != "the hub reported an invalid outcome" {
		t.Fatalf("first = %+v", f)
	}
}

func TestExecutorRecordsHubRefusalAsError(t *testing.T) {
	store := newExecutions(1, "first")
	refusal := reply{err: &run.HubError{Message: "this hub cannot run tests yet"}}
	hubs := &fakeHubs{replies: []reply{refusal, refusal}}

	if err := newExecutor(store, hubs).Run(context.Background(), deviceJob(1)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if f := store.finished["first"]; f.Status != run.ResultError || f.Error != "this hub cannot run tests yet" ||
		len(store.attempts["first"]) != 2 {
		t.Fatalf("first = %+v, attempts %d", f, len(store.attempts["first"]))
	}
}

func TestExecutorSkipsResultsCancelledMeanwhile(t *testing.T) {
	store := newExecutions(0, "first", "second")
	hubs := &fakeHubs{replies: []reply{passed(), passed()}}
	hubs.onExecute = func() { store.statuses["second"] = run.ResultCancelled }

	if err := newExecutor(store, hubs).Run(context.Background(), deviceJob(1)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(hubs.executions) != 1 || store.statuses["second"] != run.ResultCancelled {
		t.Fatalf("executions %d, statuses %v", len(hubs.executions), store.statuses)
	}
}

func TestExecutorResumesAfterRecordedAttempts(t *testing.T) {
	store := newExecutions(1, "first")
	store.work.Results[0].Status, store.work.Results[0].Attempts = run.ResultRunning, 1
	store.statuses["first"] = run.ResultRunning
	hubs := &fakeHubs{replies: []reply{passed()}}

	if err := newExecutor(store, hubs).Run(context.Background(), deviceJob(2)); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if store.statuses["first"] != run.ResultFlaky || hubs.executions[0].ID != "first/2" {
		t.Fatalf("status %s, execution %s", store.statuses["first"], hubs.executions[0].ID)
	}
}

package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"time"

	"github.com/mobqa/backend/internal/clock"
	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/action"
	"github.com/mobqa/backend/internal/usecase/environment"
	"github.com/mobqa/backend/internal/usecase/test"
	"github.com/mobqa/backend/internal/usecase/ui"
	"github.com/mobqa/backend/internal/worker"
)

// DeviceJobKind is the job that works through one device's results of a run.
const DeviceJobKind = "run_device"

const (
	// deviceAttempts rides out a hub that drops its link for a moment.
	deviceAttempts = 3
	// attemptTimeout bounds one execution, including installing the build.
	attemptTimeout = 30 * time.Minute

	problemBuildDeleted       = "the run's build was deleted"
	problemEnvironmentDeleted = "the run's environment was deleted"
	problemHubUnreachable     = "the device's hub stayed unreachable"
	problemInterrupted        = "the run was interrupted after the last attempt"
	problemInvalidOutcome     = "the hub reported an invalid outcome"
)

// Work is what a device job needs: the run, where the device is and its unfinished results in order.
type Work struct {
	Run     Run
	Package *Package
	HubID   string
	Serial  string
	Results []Pending
}

type Pending struct {
	Result
	Steps  []test.Step
	Script []ui.Op
}

// Finish closes a result.
type Finish struct {
	Status     ResultStatus
	FailedStep int
	Error      string
}

type executions interface {
	// Work reports ErrNotFound when the run is gone.
	Work(ctx context.Context, runID, deviceID string) (Work, error)
	// StartResult marks an unfinished result and its run running, and reports false when the result has
	// already finished, as a cancelled one has.
	StartResult(ctx context.Context, resultID string) (bool, error)
	// RecordAttempt reports ErrConflict when the result is no longer running.
	RecordAttempt(ctx context.Context, resultID string, attempt Attempt) error
	// FinishResult closes an unfinished result and, when it was the run's last, the run: failed if any
	// result failed or errored, passed otherwise. It reports ErrConflict when the result has finished.
	FinishResult(ctx context.Context, resultID string, finish Finish) error
}

type environments interface {
	Resolve(ctx context.Context, projectID, environmentID string) (environment.Environment, error)
}

type actions interface {
	Actions(ctx context.Context, projectID string) ([]action.Action, error)
}

// Hubs carries executions to the hub a device is attached to. It reports ErrUnavailable when the hub
// cannot be reached and a *HubError when the hub received the execution but could not carry it out.
type Hubs interface {
	Execute(ctx context.Context, hubID string, execution Execution) (Outcome, error)
}

// HubError is a hub's own account of why it could not carry out an execution, such as a missing runner.
type HubError struct {
	Message string
}

func (e *HubError) Error() string {
	return "hub: " + e.Message
}

// locks keeps a device to one job at a time across runs and authorings.
type locks interface {
	Lock(ctx context.Context, key string) (func(), error)
}

type ExecutorDependencies struct {
	Store        executions
	Environments environments
	Actions      actions
	Hubs         Hubs
	Locks        locks
	Clock        clock.Clock
}

// Executor fulfils device jobs.
type Executor struct {
	store        executions
	environments environments
	actions      actions
	hubs         Hubs
	locks        locks
	clock        clock.Clock
}

func NewExecutor(deps ExecutorDependencies) *Executor {
	return &Executor{
		store: deps.Store, environments: deps.Environments, actions: deps.Actions,
		hubs: deps.Hubs, locks: deps.Locks, clock: deps.Clock,
	}
}

// Run handles a device job. A job that is retried resumes at the first unfinished result; when the hub
// stays unreachable through the last attempt, the device's remaining results are closed as errors.
func (e *Executor) Run(ctx context.Context, job worker.Job) error {
	var payload devicePayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return worker.Permanent(fmt.Errorf("decode device payload: %w", err))
	}
	work, err := e.store.Work(ctx, payload.RunID, payload.DeviceID)
	if errors.Is(err, fault.ErrNotFound) || (err == nil && len(work.Results) == 0) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load work: %w", err)
	}
	base, problem, err := e.prepare(ctx, work)
	if err != nil {
		return err
	}
	if problem != "" {
		return e.abandon(ctx, work.Results, problem)
	}
	unlock, err := e.locks.Lock(ctx, payload.DeviceID)
	if err != nil {
		return fmt.Errorf("lock device: %w", err)
	}
	defer unlock()
	for i, pending := range work.Results {
		err := e.execute(ctx, work, pending, base)
		if errors.Is(err, fault.ErrUnavailable) && job.Attempt >= job.MaxAttempts && ctx.Err() == nil {
			return worker.Permanent(errors.Join(err, e.abandon(ctx, work.Results[i:], problemHubUnreachable)))
		}
		if err != nil {
			return err
		}
	}
	return nil
}

// prepare builds what every execution of the device shares, or names the problem that stops all of them.
func (e *Executor) prepare(ctx context.Context, work Work) (Execution, string, error) {
	base := Execution{
		Serial: work.Serial, Platform: work.Run.Platform, Package: work.Package, Variables: map[string]string{},
		Timeout: attemptTimeout,
	}
	if work.Run.BuildLabel != "" && work.Package == nil {
		return Execution{}, problemBuildDeleted, nil
	}
	if work.Run.EnvironmentName != "" {
		if work.Run.EnvironmentID == "" {
			return Execution{}, problemEnvironmentDeleted, nil
		}
		env, err := e.environments.Resolve(ctx, work.Run.ProjectID, work.Run.EnvironmentID)
		if errors.Is(err, fault.ErrNotFound) {
			return Execution{}, problemEnvironmentDeleted, nil
		}
		if err != nil {
			return Execution{}, "", fmt.Errorf("resolve environment: %w", err)
		}
		for _, variable := range env.Variables {
			base.Variables[variable.Name] = variable.Value
		}
	}
	library, err := e.actions.Actions(ctx, work.Run.ProjectID)
	if err != nil {
		return Execution{}, "", fmt.Errorf("load actions: %w", err)
	}
	base.Actions = make(map[string]action.Request, len(library))
	for _, a := range library {
		base.Actions[a.Name] = a.Request
	}
	return base, "", nil
}

// execute runs one result's attempts until one passes or the retries run out.
func (e *Executor) execute(ctx context.Context, work Work, pending Pending, base Execution) error {
	started, err := e.store.StartResult(ctx, pending.ID)
	if err != nil {
		return fmt.Errorf("start result: %w", err)
	}
	if !started {
		return nil
	}
	execution := base
	execution.Title, execution.Steps, execution.Script = pending.Title, pending.Steps, pending.Script
	execution.Variables = maps.Clone(base.Variables)
	execution.Actions = map[string]action.Request{}
	for _, op := range pending.Script {
		if request, ok := base.Actions[op.Text]; ok && op.Kind == ui.KindCall {
			execution.Actions[op.Text] = request
		}
	}
	finish := Finish{Status: ResultError, Error: problemInterrupted}
	for number := pending.Attempts + 1; number <= 1+work.Run.Retries; number++ {
		execution.ID = pending.ID + "/" + strconv.Itoa(number)
		attempt, err := e.attempt(ctx, work.HubID, execution, number)
		if err != nil {
			return err
		}
		err = e.store.RecordAttempt(ctx, pending.ID, attempt)
		if errors.Is(err, fault.ErrConflict) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("record attempt: %w", err)
		}
		finish = Finish{Status: finalStatus(attempt.Status, number), FailedStep: attempt.FailedStep, Error: attempt.Error}
		if attempt.Status == AttemptPassed {
			break
		}
	}
	if err := e.store.FinishResult(ctx, pending.ID, finish); err != nil && !errors.Is(err, fault.ErrConflict) {
		return fmt.Errorf("finish result: %w", err)
	}
	return nil
}

func (e *Executor) attempt(ctx context.Context, hubID string, execution Execution, number int) (Attempt, error) {
	startedAt := e.clock.Now()
	attemptCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	outcome, err := e.hubs.Execute(attemptCtx, hubID, execution)
	var refused *HubError
	switch {
	case ctx.Err() != nil:
		return Attempt{}, context.Cause(ctx)
	case errors.As(err, &refused):
		outcome = Outcome{Status: AttemptError, Error: refused.Message}
	case errors.Is(err, context.DeadlineExceeded):
		outcome = Outcome{Status: AttemptError, Error: "the attempt did not finish within " + attemptTimeout.String()}
	case err != nil:
		return Attempt{}, fmt.Errorf("execute on hub: %w", err)
	case outcome.validate(len(execution.Steps)) != nil:
		outcome = Outcome{Status: AttemptError, Error: problemInvalidOutcome}
	}
	return outcome.attempt(number, startedAt, e.clock.Now()), nil
}

// abandon closes results that cannot run, skipping any that finished meanwhile.
func (e *Executor) abandon(ctx context.Context, results []Pending, problem string) error {
	for _, pending := range results {
		err := e.store.FinishResult(ctx, pending.ID, Finish{Status: ResultError, Error: problem})
		if err != nil && !errors.Is(err, fault.ErrConflict) {
			return fmt.Errorf("abandon result: %w", err)
		}
	}
	return nil
}

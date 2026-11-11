// Package run executes tests on the devices of a project's hubs. A run fans out into one result per
// test and device; each device works through its results in order, retrying failures up to the run's
// retry budget, and the run finishes when its last result does.
package run

import (
	"time"
	"unicode/utf8"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/action"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/test"
	"github.com/mobqa/backend/internal/usecase/ui"
)

type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusPassed    Status = "passed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

type ResultStatus string

const (
	ResultQueued  ResultStatus = "queued"
	ResultRunning ResultStatus = "running"
	ResultPassed  ResultStatus = "passed"
	// ResultFlaky passed only after at least one failed attempt.
	ResultFlaky     ResultStatus = "flaky"
	ResultFailed    ResultStatus = "failed"
	ResultError     ResultStatus = "error"
	ResultCancelled ResultStatus = "cancelled"
)

// AttemptStatus separates a test that failed an assertion from one that could not be carried out.
type AttemptStatus string

const (
	AttemptPassed AttemptStatus = "passed"
	AttemptFailed AttemptStatus = "failed"
	AttemptError  AttemptStatus = "error"
)

type StepStatus string

const (
	StepPassed  StepStatus = "passed"
	StepFailed  StepStatus = "failed"
	StepSkipped StepStatus = "skipped"
)

const (
	maxTests   = 50
	maxDevices = 10
	maxRetries = 2
	// maxErrorLength keeps a hub's error readable in a list; full logs belong to artifacts.
	maxErrorLength = 2000
)

type Run struct {
	ID              string
	ProjectID       string
	Platform        project.Platform
	BuildID         string
	BuildLabel      string
	EnvironmentID   string
	EnvironmentName string
	Status          Status
	Retries         int
	Summary         Summary
	CreatedBy       string
	CreatedAt       time.Time
	StartedAt       *time.Time
	FinishedAt      *time.Time
}

// Summary counts a run's results by status.
type Summary struct {
	Total     int
	Queued    int
	Running   int
	Passed    int
	Flaky     int
	Failed    int
	Errored   int
	Cancelled int
}

type Result struct {
	ID          string
	RunID       string
	Position    int
	TestID      string
	TestVersion int
	Title       string
	DeviceID    string
	DeviceName  string
	Status      ResultStatus
	Attempts    int
	// FailedStep is the 1-based step the last attempt stopped at, or zero.
	FailedStep int
	Error      string
	StartedAt  *time.Time
	FinishedAt *time.Time
}

type Attempt struct {
	Number     int
	Status     AttemptStatus
	FailedStep int
	Error      string
	Steps      []StepOutcome
	// Video is set when the hub filmed the attempt, which it does on devices that stream their screen.
	Video      *Video
	StartedAt  time.Time
	FinishedAt time.Time
}

// Video is an attempt's recording: how long it runs, or why the hub could not keep it.
type Video struct {
	Duration time.Duration
	Problem  string
}

type StepOutcome struct {
	Status   StepStatus
	Error    string
	Duration time.Duration
}

// Package is the build a hub installs before running, identified by content so the hub can skip an
// install it already has.
type Package struct {
	BuildID string
	SHA256  string
	Size    int64
}

// Execution is one attempt of one test on one device, as sent to the device's hub.
type Execution struct {
	ID        string
	Serial    string
	Platform  project.Platform
	Package   *Package
	Title     string
	Steps     []test.Step
	Script    []ui.Op
	Variables map[string]string
	// Actions holds the API actions the script calls, by name.
	Actions map[string]action.Request
	Timeout time.Duration
}

// Outcome is what a hub reports about an execution it carried out.
type Outcome struct {
	Status     AttemptStatus
	FailedStep int
	Error      string
	Steps      []StepOutcome
	Video      *Video
}

func (o Outcome) validate(steps int) error {
	switch o.Status {
	case AttemptPassed, AttemptFailed, AttemptError:
	default:
		return fault.WithDetail(fault.ErrInvalidInput, "unknown attempt status")
	}
	if o.FailedStep < 0 || o.FailedStep > steps || len(o.Steps) > steps {
		return fault.WithDetail(fault.ErrInvalidInput, "the outcome names steps the test does not have")
	}
	if o.Video != nil && o.Video.Duration < 0 {
		return fault.WithDetail(fault.ErrInvalidInput, "a video cannot run for a negative time")
	}
	for _, step := range o.Steps {
		if step.Status != StepPassed && step.Status != StepFailed && step.Status != StepSkipped {
			return fault.WithDetail(fault.ErrInvalidInput, "unknown step status")
		}
	}
	return nil
}

func (o Outcome) attempt(number int, startedAt, finishedAt time.Time) Attempt {
	steps := make([]StepOutcome, len(o.Steps))
	for i, step := range o.Steps {
		steps[i] = StepOutcome{Status: step.Status, Error: truncate(step.Error), Duration: step.Duration}
	}
	var video *Video
	if o.Video != nil {
		video = &Video{Duration: o.Video.Duration, Problem: truncate(o.Video.Problem)}
	}
	return Attempt{
		Number: number, Status: o.Status, FailedStep: o.FailedStep, Error: truncate(o.Error), Steps: steps,
		Video: video, StartedAt: startedAt, FinishedAt: finishedAt,
	}
}

func truncate(text string) string {
	if utf8.RuneCountInString(text) <= maxErrorLength {
		return text
	}
	return string([]rune(text)[:maxErrorLength-1]) + "…"
}

// finalStatus is the result of a test whose attempt number passed or ran out its retries.
func finalStatus(last AttemptStatus, number int) ResultStatus {
	switch {
	case last == AttemptPassed && number == 1:
		return ResultPassed
	case last == AttemptPassed:
		return ResultFlaky
	case last == AttemptFailed:
		return ResultFailed
	default:
		return ResultError
	}
}

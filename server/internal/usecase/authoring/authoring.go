// Package authoring turns a manual test into a script by having an agent carry it out on a real device:
// at every turn the agent sees the screen, picks one action for the current step and the action is
// recorded with a selector that finds its element again. Runs replay the script without a model.
package authoring

import (
	"time"

	"github.com/mobqa/backend/internal/usecase/knowledge"
	"github.com/mobqa/backend/internal/worker"

	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/test"
	"github.com/mobqa/backend/internal/usecase/ui"
)

type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

func (s Status) Finished() bool {
	return s == StatusSucceeded || s == StatusFailed || s == StatusCancelled
}

// Authoring is one attempt to script a test version on a device. Device and environment are kept by
// name too, so the record still reads after they are removed.
type Authoring struct {
	ID              string
	ProjectID       string
	TestID          string
	TestVersion     int
	Platform        project.Platform
	DeviceID        string
	DeviceName      string
	EnvironmentID   string
	EnvironmentName string
	Status          Status
	// Script is the compiled test once the authoring succeeded; listings leave it and Log empty.
	Script     []ui.Op
	Log        []Turn
	Failure    string
	CreatedBy  string
	CreatedAt  time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
	// ReviewedAt is when the agent wrote down what the authoring taught it.
	ReviewedAt *time.Time
	// Knowledge is the notes the agent was given, so a review can tell which of them misled it.
	Knowledge []string
}

// Turn is one decision of the agent, kept so a person can follow how the script came about. Text keeps
// variables as {{NAME}}, never their values.
type Turn struct {
	Step    int
	Action  string
	Element string
	Text    string
	Thought string
	Error   string
}

// Work is what authoring jobs need: the authoring with its script and log, the version's steps, where
// the device is and which app to start.
type Work struct {
	Authoring Authoring
	Title     string
	Steps     []test.Step
	HubID     string
	Serial    string
	App       string
}

// Finish closes an authoring.
type Finish struct {
	Status    Status
	Script    []ui.Op
	Log       []Turn
	Failure   string
	Knowledge []string
}

// Lesson is a note the agent writes after reviewing an authoring. NoteID is a new note, or one of the
// agent's earlier notes that the lesson rewrites; Index is the job that indexes it.
type Lesson struct {
	NoteID    string
	ProjectID string
	Kind      knowledge.Kind
	Title     string
	Content   string
	CreatedBy string
	Index     worker.EnqueueInput
}

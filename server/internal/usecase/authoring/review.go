package authoring

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/chat"
	"github.com/mobqa/backend/internal/usecase/knowledge"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/worker"
)

// ReviewJobKind is the job that writes down what a finished authoring taught the agent.
const ReviewJobKind = "authoring_review"

const (
	// reviewAttempts rides out a model that is away for a moment.
	reviewAttempts = 3
	// reviewMaxTokens fits three lessons at their longest.
	reviewMaxTokens = 2000
	// reviewReplies lets a second reply repair a malformed first one.
	reviewReplies = 2
	maxLessons    = 3
	// knownLessons is how many of the agent's notes a review sees, newest first, so that it rewrites a
	// lesson instead of repeating it.
	knownLessons     = 30
	maxLessonTitle   = 200
	maxLessonContent = 1500
	knownExcerpt     = 300
)

var errUnusableReview = errors.New("unusable review reply")

type reviews interface {
	// Work reports ErrNotFound when the authoring is gone.
	Work(ctx context.Context, id string) (Work, error)
	// AgentNotes returns the notes the agent wrote for the project, newest first.
	AgentNotes(ctx context.Context, projectID string, limit int) ([]knowledge.Note, error)
	// SaveLessons stores the lessons, queues their index jobs, deletes the retired notes and marks the
	// authoring reviewed in one transaction. It reports ErrConflict when the authoring has been reviewed
	// already. Notes people wrote are never rewritten or deleted.
	SaveLessons(ctx context.Context, authoringID string, lessons []Lesson, retired []string) error
}

// Reviewer fulfils review jobs: it shows the model how an authoring went and keeps the lessons it draws
// as agent notes, which later authorings retrieve with the rest of the project's knowledge. Agent notes
// the session proved wrong are retired, so a bad lesson does not mislead every authoring after it.
type Reviewer struct {
	store    reviews
	projects projects
	model    model
}

func NewReviewer(store reviews, catalog projects, llm model) *Reviewer {
	return &Reviewer{store: store, projects: catalog, model: llm}
}

func reviewJob(authoringID string) (worker.EnqueueInput, error) {
	payload, err := json.Marshal(jobPayload{AuthoringID: authoringID})
	if err != nil {
		return worker.EnqueueInput{}, fmt.Errorf("encode review payload: %w", err)
	}
	return worker.EnqueueInput{Kind: ReviewJobKind, Payload: payload, MaxAttempts: reviewAttempts}, nil
}

// Run handles a review job. An unavailable model is retried until the last attempt; a model that is not
// configured or keeps replying with nothing usable fails the job, and the authoring stays unreviewed.
func (r *Reviewer) Run(ctx context.Context, job worker.Job) error {
	var payload jobPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return worker.Permanent(fmt.Errorf("decode review payload: %w", err))
	}
	w, err := r.store.Work(ctx, payload.AuthoringID)
	if errors.Is(err, fault.ErrNotFound) || (err == nil && w.Authoring.ReviewedAt != nil) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load authoring: %w", err)
	}
	app, err := r.projects.Project(ctx, w.Authoring.ProjectID)
	if err != nil {
		return fmt.Errorf("load project: %w", err)
	}
	known, err := r.store.AgentNotes(ctx, w.Authoring.ProjectID, knownLessons)
	if err != nil {
		return fmt.Errorf("load agent notes: %w", err)
	}
	verdict, err := r.distil(ctx, app, w, known)
	if errors.Is(err, chat.ErrNotConfigured) || errors.Is(err, errUnusableReview) {
		return worker.Permanent(err)
	}
	if err != nil {
		return err
	}
	err = r.store.SaveLessons(ctx, w.Authoring.ID, verdict.lessons, verdict.retired)
	if err != nil && !errors.Is(err, fault.ErrConflict) {
		return fmt.Errorf("save lessons: %w", err)
	}
	return nil
}

// verdict is what a review concluded: lessons to keep and agent notes to retire.
type verdict struct {
	lessons []Lesson
	retired []string
}

func (r *Reviewer) distil(ctx context.Context, app project.Project, w Work, known []knowledge.Note,
) (verdict, error) {
	messages := []chat.Message{
		{Role: chat.RoleSystem, Text: reviewPrompt},
		{Role: chat.RoleUser, Text: sessionReport(app, w, known)},
	}
	ids := make(map[string]bool, len(known))
	for _, note := range known {
		ids[note.ID] = true
	}
	var cause error
	for range reviewReplies {
		response, err := r.model.Chat(ctx, chat.Request{Messages: messages, MaxTokens: reviewMaxTokens, JSON: true})
		if err != nil {
			return verdict{}, fmt.Errorf("ask the model: %w", err)
		}
		parsed, err := parseVerdict(response, w.Authoring, ids)
		if err == nil {
			return parsed, nil
		}
		cause = err
		messages = append(messages,
			chat.Message{Role: chat.RoleAssistant, Text: response.Text},
			chat.Message{Role: chat.RoleUser, Text: reviewRepairPrompt},
		)
	}
	return verdict{}, cause
}

const reviewPrompt = `You review how a mobile QA agent scripted a manual test on a real device, and keep what ` +
	`future sessions on the same app should know.
Reply with one JSON object and nothing else:
{"lessons":[{"kind":"flow|gotcha|selector","title":"...","content":"...","replaces":"..."}],"retired":["..."]}
Rules:
- 0 to 3 lessons. An empty list is right when the session taught nothing new.
- flow: how to reach a screen or finish a task in this app. gotcha: what misled the agent or failed a step, ` +
	`and what worked instead. selector: how to find an element reliably.
- Only facts the session shows about this app; no general advice about testing or mobile apps.
- Write variables as {{NAME}}, as the session does; never write the values typed into the app.
- When a known lesson covers the same thing, rewrite it: put its id in replaces and give the whole new text. ` +
	`Otherwise leave replaces empty.
- When the session shows a known lesson is wrong and there is nothing right to say instead, put its id in ` +
	`retired. Lessons marked as given to the agent are the ones it acted on.
- title is at most 200 characters, content at most 1500.`

const reviewRepairPrompt = "That reply was not a JSON object in the required shape, or none of its lessons were " +
	"valid. Reply again with only the JSON object."

// sessionReport tells the model how the authoring went: the test, the outcome, every turn of the agent,
// the script it recorded and the lessons already known.
func sessionReport(app project.Project, w Work, known []knowledge.Note) string {
	var b strings.Builder
	fmt.Fprintf(&b, "App: %s\nPlatform: %s\n", app.Name, w.Authoring.Platform)
	if app.PackageName != "" {
		fmt.Fprintf(&b, "Package: %s\n", app.PackageName)
	}
	fmt.Fprintf(&b, "\nTest: %s\nSteps:\n", w.Title)
	for i, step := range w.Steps {
		fmt.Fprintf(&b, "%d. %s\n", i+1, describeStep(step))
	}
	outcome := "succeeded"
	if w.Authoring.Status != StatusSucceeded {
		outcome = "failed: " + w.Authoring.Failure
	}
	fmt.Fprintf(&b, "\nOutcome: %s\n\nTurns:\n", outcome)
	for _, turn := range w.Authoring.Log {
		fmt.Fprintf(&b, "- step %d: %s", turn.Step, turn.Action)
		for _, part := range [][2]string{{"", turn.Element}, {"text ", turn.Text}, {"thought: ", turn.Thought},
			{"error: ", turn.Error}} {
			if part[1] != "" {
				fmt.Fprintf(&b, "; %s%s", part[0], part[1])
			}
		}
		b.WriteString("\n")
	}
	if len(w.Authoring.Script) > 0 {
		b.WriteString("\nRecorded script:\n")
		for _, op := range w.Authoring.Script {
			fmt.Fprintf(&b, "- step %d: %s", op.Step, op.Kind)
			if op.Selector != nil {
				fmt.Fprintf(&b, " %s", op.Selector)
			}
			b.WriteString("\n")
		}
	}
	if len(known) > 0 {
		b.WriteString("\nKnown lessons:\n")
		for _, note := range known {
			given := ""
			if slices.Contains(w.Authoring.Knowledge, note.ID) {
				given = ", given to the agent"
			}
			fmt.Fprintf(&b, "- id %s, %s%s: %s. %s\n", note.ID, note.Kind, given, note.Title,
				excerpt(note.Content, knownExcerpt))
		}
	}
	return b.String()
}

func excerpt(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit]) + "…"
}

type lessonReply struct {
	Lessons []struct {
		Kind     string `json:"kind"`
		Title    string `json:"title"`
		Content  string `json:"content"`
		Replaces string `json:"replaces"`
	} `json:"lessons"`
	Retired []string `json:"retired"`
}

// parseVerdict keeps the valid lessons of a reply. A lesson replaces a known note only when it names one,
// and only known notes that no lesson rewrites can be retired. A reply whose lessons are all invalid is
// unusable, while a reply with none means nothing was learned.
func parseVerdict(response chat.Response, a Authoring, known map[string]bool) (verdict, error) {
	if response.Truncated {
		return verdict{}, fmt.Errorf("%w: the reply hit the token limit", errUnusableReview)
	}
	var reply lessonReply
	if err := json.Unmarshal([]byte(response.Text), &reply); err != nil {
		return verdict{}, fmt.Errorf("%w: %w", errUnusableReview, err)
	}
	lessons := []Lesson{}
	used := map[string]bool{}
	for _, candidate := range reply.Lessons {
		kind := knowledge.Kind(candidate.Kind)
		title, content := strings.TrimSpace(candidate.Title), strings.TrimSpace(candidate.Content)
		if len(lessons) == maxLessons || (kind != knowledge.KindFlow && kind != knowledge.KindGotcha &&
			kind != knowledge.KindSelector) || title == "" || content == "" ||
			utf8.RuneCountInString(title) > maxLessonTitle || utf8.RuneCountInString(content) > maxLessonContent {
			continue
		}
		noteID := candidate.Replaces
		if !known[noteID] || used[noteID] {
			id, err := uuid.NewV7()
			if err != nil {
				return verdict{}, fmt.Errorf("generate note id: %w", err)
			}
			noteID = id.String()
		}
		used[noteID] = true
		index, err := knowledge.IndexJob(noteID)
		if err != nil {
			return verdict{}, err
		}
		lessons = append(lessons, Lesson{
			NoteID: noteID, ProjectID: a.ProjectID, Kind: kind, Title: title, Content: content, CreatedBy: a.CreatedBy,
			Index: index,
		})
	}
	if len(lessons) == 0 && len(reply.Lessons) > 0 {
		return verdict{}, fmt.Errorf("%w: no valid lessons", errUnusableReview)
	}
	retired := []string{}
	for _, id := range reply.Retired {
		if known[id] && !used[id] {
			used[id] = true
			retired = append(retired, id)
		}
	}
	return verdict{lessons: lessons, retired: retired}, nil
}

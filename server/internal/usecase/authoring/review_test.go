package authoring_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/authoring"
	"github.com/mobqa/backend/internal/usecase/chat"
	"github.com/mobqa/backend/internal/usecase/knowledge"
	"github.com/mobqa/backend/internal/usecase/ui"
	"github.com/mobqa/backend/internal/worker"
)

type fakeReviews struct {
	work    authoring.Work
	notes   []knowledge.Note
	saved   [][]authoring.Lesson
	retired []string
}

func (f *fakeReviews) Work(_ context.Context, id string) (authoring.Work, error) {
	if id != f.work.Authoring.ID {
		return authoring.Work{}, fault.ErrNotFound
	}
	return f.work, nil
}

func (f *fakeReviews) AgentNotes(_ context.Context, _ string, limit int) ([]knowledge.Note, error) {
	return f.notes[:min(limit, len(f.notes))], nil
}

func (f *fakeReviews) SaveLessons(_ context.Context, _ string, lessons []authoring.Lesson, retired []string) error {
	f.saved = append(f.saved, lessons)
	f.retired = append(f.retired, retired...)
	return nil
}

// failedSignIn is an authoring that failed on the second step after the agent tapped the wrong button.
func failedSignIn() *fakeReviews {
	w := signIn().work
	w.Authoring.Status, w.Authoring.Failure, w.Authoring.CreatedBy = authoring.StatusFailed, "the agent used all of "+
		"its turns", "user-1"
	w.Authoring.Log = []authoring.Turn{
		{Step: 1, Action: "type", Element: "[0]", Text: "{{PHONE}}", Thought: "Enter the phone"},
		{Step: 2, Action: "tap", Element: "[4]", Thought: "Looks like Continue", Error: "nothing changed"},
	}
	w.Authoring.Script = []ui.Op{{Step: 1, Kind: ui.KindTap, Selector: &ui.Selector{ID: "app:id/phone"}}}
	w.Authoring.Knowledge = []string{"note-1"}
	return &fakeReviews{work: w, notes: []knowledge.Note{
		{ID: "note-1", Kind: knowledge.KindGotcha, Title: "Continue is disabled", Content: "Until the phone is valid."},
		{ID: "note-2", Kind: knowledge.KindFlow, Title: "Skip onboarding", Content: "Swipe left three times."},
	}}
}

func review(store *fakeReviews, model *fakeModel) error {
	return authoring.NewReviewer(store, fakeProjects{}, model).Run(context.Background(), worker.Job{
		Kind: authoring.ReviewJobKind, Payload: []byte(`{"authoring_id":"authoring-1"}`), Attempt: 1, MaxAttempts: 3,
	})
}

func TestReviewerKeepsLessonsAsAgentNotes(t *testing.T) {
	store := failedSignIn()
	model := &fakeModel{replies: []string{`{"lessons":[
		{"kind":"gotcha","title":"Continue needs a valid phone","content":"Type {{PHONE}} first.","replaces":"note-1"},
		{"kind":"poem","title":"Ode","content":"Roses"},
		{"kind":"selector","title":"Phone field","content":"Use app:id/phone.","replaces":"note-9"},
		{"kind":"flow","title":"Sign in","content":"Phone, then Continue.","replaces":"note-1"},
		{"kind":"flow","title":"Extra","content":"One lesson too many."}
	],"retired":["note-2","note-1","note-7","note-2"]}`}}

	if err := review(store, model); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(store.saved) != 1 || len(store.saved[0]) != 3 {
		t.Fatalf("saved = %+v, want three lessons", store.saved)
	}
	if len(store.retired) != 1 || store.retired[0] != "note-2" {
		t.Fatalf("retired = %v, want only the known note no lesson rewrites", store.retired)
	}
	lessons := store.saved[0]
	if lessons[0].NoteID != "note-1" || lessons[0].Kind != knowledge.KindGotcha || lessons[0].ProjectID != projectID ||
		lessons[0].CreatedBy != "user-1" || lessons[0].Content != "Type {{PHONE}} first." {
		t.Fatalf("rewritten lesson = %+v", lessons[0])
	}
	for _, fresh := range lessons[1:] {
		if _, err := uuid.Parse(fresh.NoteID); err != nil || fresh.NoteID == "note-1" {
			t.Fatalf("new lesson id = %q", fresh.NoteID)
		}
	}
	for _, lesson := range lessons {
		if lesson.Index.Kind != knowledge.IndexJobKind || !strings.Contains(string(lesson.Index.Payload), lesson.NoteID) {
			t.Fatalf("index job = %+v for %s", lesson.Index, lesson.NoteID)
		}
	}
	report := model.requests[0].Messages[1].Text
	for _, want := range []string{
		"Outcome: failed: the agent used all of its turns", "step 2: tap; [4]; thought: Looks like Continue; " +
			"error: nothing changed", "text {{PHONE}}", `step 1: tap id="app:id/phone"`,
		"id note-1, gotcha, given to the agent: Continue is disabled", "id note-2, flow: Skip onboarding",
	} {
		if !strings.Contains(report, want) {
			t.Fatalf("report lacks %q:\n%s", want, report)
		}
	}
	if !model.requests[0].JSON || strings.Contains(report, phone) {
		t.Fatalf("request = %+v", model.requests[0])
	}
}

func TestReviewerRepairsAMalformedReply(t *testing.T) {
	store := failedSignIn()
	model := &fakeModel{replies: []string{"I learned a lot", `{"lessons":[]}`}}

	if err := review(store, model); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(model.requests) != 2 || len(store.saved) != 1 || len(store.saved[0]) != 0 {
		t.Fatalf("requests %d, saved %+v", len(model.requests), store.saved)
	}
}

func TestReviewerGivesUpOnWhatRetryingCannotFix(t *testing.T) {
	tests := []struct {
		name      string
		model     *fakeModel
		permanent bool
	}{
		{name: "unusable replies", model: &fakeModel{replies: []string{"no", `{"lessons":[{"kind":"poem"}]}`}},
			permanent: true},
		{name: "no model", model: &fakeModel{err: chat.ErrNotConfigured}, permanent: true},
		{name: "unavailable model", model: &fakeModel{err: fault.ErrUnavailable}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := failedSignIn()

			err := review(store, tt.model)

			if err == nil || worker.IsPermanent(err) != tt.permanent || len(store.saved) != 0 {
				t.Fatalf("Run = %v, saved %+v", err, store.saved)
			}
		})
	}
}

func TestReviewerSkipsReviewedOrMissingAuthorings(t *testing.T) {
	reviewed := failedSignIn()
	at := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	reviewed.work.Authoring.ReviewedAt = &at
	missing := failedSignIn()
	missing.work.Authoring.ID = "other"
	for name, store := range map[string]*fakeReviews{"reviewed": reviewed, "missing": missing} {
		t.Run(name, func(t *testing.T) {
			model := &fakeModel{}
			if err := review(store, model); err != nil || len(model.requests) != 0 || len(store.saved) != 0 {
				t.Fatalf("Run = %v, model asked %d times", err, len(model.requests))
			}
		})
	}
}

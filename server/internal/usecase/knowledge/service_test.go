package knowledge_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/chat"
	"github.com/mobqa/backend/internal/usecase/knowledge"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/worker"
)

const projID = "project-1"

type fakeAccess struct {
	roles map[string]project.Role
}

func (a fakeAccess) Require(_ context.Context, _, userID string, need project.Role) (project.Role, error) {
	role, ok := a.roles[userID]
	if !ok {
		return "", fault.ErrNotFound
	}
	if !role.Includes(need) {
		return "", fault.ErrForbidden
	}
	return role, nil
}

func access() fakeAccess {
	return fakeAccess{roles: map[string]project.Role{"viewer": project.RoleViewer, "engineer": project.RoleEngineer}}
}

type fakeStore struct {
	created []knowledge.NewNote
	changes []knowledge.Change
	filters []knowledge.Filter
	jobs    []worker.EnqueueInput
	pinned  []knowledge.Note
	matches []knowledge.Match
	limits  []int
}

func (s *fakeStore) CreateNote(_ context.Context, in knowledge.NewNote, job worker.EnqueueInput,
) (knowledge.Note, error) {
	s.created, s.jobs = append(s.created, in), append(s.jobs, job)
	return knowledge.Note{ID: in.ID, ProjectID: in.ProjectID, Kind: in.Kind, Title: in.Title, Content: in.Content}, nil
}

func (s *fakeStore) Notes(_ context.Context, filter knowledge.Filter) ([]knowledge.Note, int, error) {
	s.filters = append(s.filters, filter)
	return []knowledge.Note{{ID: "note-1"}}, 21, nil
}

func (s *fakeStore) Note(_ context.Context, _, id string) (knowledge.Note, error) {
	if id != "note-1" {
		return knowledge.Note{}, fault.ErrNotFound
	}
	return knowledge.Note{ID: id}, nil
}

func (s *fakeStore) UpdateNote(_ context.Context, change knowledge.Change, job worker.EnqueueInput,
) (knowledge.Note, error) {
	if change.NoteID != "note-1" {
		return knowledge.Note{}, fault.ErrNotFound
	}
	s.changes, s.jobs = append(s.changes, change), append(s.jobs, job)
	return knowledge.Note{ID: change.NoteID}, nil
}

func (s *fakeStore) DeleteNote(_ context.Context, _, id string) error {
	if id != "note-1" {
		return fault.ErrNotFound
	}
	return nil
}

func (s *fakeStore) Pinned(context.Context, string, int) ([]knowledge.Note, error) {
	return s.pinned, nil
}

func (s *fakeStore) Nearest(_ context.Context, _ string, vector []float32, limit int) ([]knowledge.Match, error) {
	if len(vector) != knowledge.Dimensions {
		return nil, errors.New("wrong vector")
	}
	s.limits = append(s.limits, limit)
	return s.matches, nil
}

// fakeModel embeds every text as a valid vector, or fails with err when it is set.
type fakeModel struct {
	err        error
	dimensions int
	texts      []string
}

func (m *fakeModel) Embed(_ context.Context, texts []string) ([][]float32, error) {
	m.texts = append(m.texts, texts...)
	if m.err != nil {
		return nil, m.err
	}
	size := m.dimensions
	if size == 0 {
		size = knowledge.Dimensions
	}
	vectors := make([][]float32, len(texts))
	for i := range texts {
		vectors[i] = make([]float32, size)
	}
	return vectors, nil
}

func unavailable() error {
	return errors.Join(fault.ErrUnavailable, chat.ErrNotConfigured)
}

func newService(store *fakeStore, model *fakeModel) *knowledge.Service {
	return knowledge.NewService(store, access(), model)
}

func TestCreateQueuesIndexJobForTheNote(t *testing.T) {
	store := &fakeStore{}

	created, err := newService(store, &fakeModel{}).Create(context.Background(), knowledge.CreateInput{
		UserID: "engineer", ProjectID: projID, Kind: knowledge.KindGotcha, Title: " OTP ",
		Content: " The code arrives by SMS after 30 seconds. ", Pinned: true,
	})

	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	in := store.created[0]
	if in.Title != "OTP" || in.Content != "The code arrives by SMS after 30 seconds." ||
		in.Source != knowledge.SourceUser || !in.Pinned || in.CreatedBy != "engineer" || in.ID != created.ID {
		t.Fatalf("created = %+v", in)
	}
	var payload struct {
		NoteID string `json:"note_id"`
	}
	job := store.jobs[0]
	if err := json.Unmarshal(job.Payload, &payload); err != nil || job.Kind != knowledge.IndexJobKind ||
		payload.NoteID != created.ID {
		t.Fatalf("job = %+v (%s), %v", job, job.Payload, err)
	}
}

func TestCreateRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		in   knowledge.CreateInput
	}{
		{name: "unknown kind", in: knowledge.CreateInput{Kind: "secret", Title: "OTP", Content: "SMS"}},
		{name: "blank title", in: knowledge.CreateInput{Kind: knowledge.KindNote, Title: " ", Content: "SMS"}},
		{name: "long title", in: knowledge.CreateInput{Kind: knowledge.KindNote, Title: strings.Repeat("a", 201),
			Content: "SMS"}},
		{name: "blank content", in: knowledge.CreateInput{Kind: knowledge.KindNote, Title: "OTP"}},
		{name: "long content", in: knowledge.CreateInput{Kind: knowledge.KindNote, Title: "OTP",
			Content: strings.Repeat("a", 20001)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.in.UserID, tt.in.ProjectID = "engineer", projID
			if _, err := newService(&fakeStore{}, &fakeModel{}).Create(context.Background(), tt.in); !errors.Is(err,
				fault.ErrInvalidInput) {
				t.Fatalf("Create error = %v, want invalid input", err)
			}
		})
	}
}

func TestViewerReadsButCannotChangeNotes(t *testing.T) {
	service := newService(&fakeStore{}, &fakeModel{})
	ctx := context.Background()
	ref := knowledge.Ref{UserID: "viewer", ProjectID: projID, NoteID: "note-1"}

	if _, err := service.Get(ctx, ref); err != nil {
		t.Fatalf("Get: %v", err)
	}
	_, createErr := service.Create(ctx, knowledge.CreateInput{
		UserID: "viewer", ProjectID: projID, Kind: knowledge.KindNote, Title: "OTP", Content: "SMS",
	})
	_, updateErr := service.Update(ctx, knowledge.UpdateInput{Ref: ref, Pinned: new(bool)})
	for _, err := range []error{createErr, updateErr, service.Delete(ctx, ref)} {
		if !errors.Is(err, fault.ErrForbidden) {
			t.Fatalf("error = %v, want forbidden", err)
		}
	}
}

func TestListPagesByKind(t *testing.T) {
	store := &fakeStore{}

	page, err := newService(store, &fakeModel{}).List(context.Background(), knowledge.ListInput{
		UserID: "viewer", ProjectID: projID, Kind: knowledge.KindFlow, Page: 2, Limit: 10,
	})

	want := knowledge.Filter{ProjectID: projID, Kind: knowledge.KindFlow, Limit: 10, Offset: 10}
	if err != nil || store.filters[0] != want || !page.HasMore() {
		t.Fatalf("List = %+v, %v; filter %+v", page, err, store.filters)
	}
}

func TestUpdateTrimsTextAndQueuesIndexJob(t *testing.T) {
	store := &fakeStore{}
	title := " Checkout "

	_, err := newService(store, &fakeModel{}).Update(context.Background(), knowledge.UpdateInput{
		Ref: knowledge.Ref{UserID: "engineer", ProjectID: projID, NoteID: "note-1"}, Title: &title,
	})

	if err != nil || *store.changes[0].Title != "Checkout" || store.changes[0].Content != nil ||
		store.jobs[0].Kind != knowledge.IndexJobKind {
		t.Fatalf("Update = %v, change %+v", err, store.changes)
	}
}

func TestUnknownNoteIsNotFound(t *testing.T) {
	service := newService(&fakeStore{}, &fakeModel{})
	ctx := context.Background()
	ref := knowledge.Ref{UserID: "engineer", ProjectID: projID, NoteID: "note-9"}

	_, getErr := service.Get(ctx, ref)
	_, updateErr := service.Update(ctx, knowledge.UpdateInput{Ref: ref, Pinned: new(bool)})
	for _, err := range []error{getErr, updateErr, service.Delete(ctx, ref)} {
		if !errors.Is(err, fault.ErrNotFound) {
			t.Fatalf("error = %v, want not found", err)
		}
	}
}

func TestSearchEmbedsQuery(t *testing.T) {
	store := &fakeStore{matches: []knowledge.Match{{NoteID: "note-1", Excerpt: "SMS", Similarity: 0.8}}}
	model := &fakeModel{}

	matches, err := newService(store, model).Search(context.Background(), knowledge.SearchInput{
		UserID: "viewer", ProjectID: projID, Query: " how does sign in work ",
	})

	if err != nil || len(matches) != 1 || model.texts[0] != "how does sign in work" || store.limits[0] != 10 {
		t.Fatalf("Search = %+v, %v; texts %v, limits %v", matches, err, model.texts, store.limits)
	}
}

func TestSearchFailures(t *testing.T) {
	tests := []struct {
		name  string
		query string
		model *fakeModel
		want  error
	}{
		{name: "blank query", query: " ", model: &fakeModel{}, want: fault.ErrInvalidInput},
		{name: "model unavailable", query: "otp", model: &fakeModel{err: unavailable()}, want: fault.ErrUnavailable},
		{name: "wrong dimensions", query: "otp", model: &fakeModel{dimensions: 768}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newService(&fakeStore{}, tt.model).Search(context.Background(), knowledge.SearchInput{
				UserID: "viewer", ProjectID: projID, Query: tt.query,
			})
			if err == nil || (tt.want != nil && !errors.Is(err, tt.want)) {
				t.Fatalf("Search error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestRetrievePutsPinnedFirstAndSkipsWeakOrDuplicateMatches(t *testing.T) {
	store := &fakeStore{
		pinned: []knowledge.Note{{ID: "pinned", Kind: knowledge.KindGotcha, Title: "OTP", Content: "Use 0000"}},
		matches: []knowledge.Match{
			{NoteID: "pinned", Title: "OTP", Excerpt: "Use 0000", Similarity: 0.9},
			{NoteID: "flow", Kind: knowledge.KindFlow, Title: "Checkout", Excerpt: "Cart, then pay", Similarity: 0.7},
			{NoteID: "huge", Title: "Catalog", Excerpt: strings.Repeat("x", 100), Similarity: 0.6},
			{NoteID: "flow", Kind: knowledge.KindFlow, Title: "Checkout", Excerpt: "Pay", Similarity: 0.5},
			{NoteID: "weak", Title: "Theme", Excerpt: "Dark", Similarity: 0.1},
		},
	}

	got, err := newService(store, &fakeModel{}).Retrieve(context.Background(), knowledge.Query{
		ProjectID: projID, Text: "pay for the cart", Budget: 45,
	})

	if err != nil || got.Partial || len(got.Items) != 3 || !got.Items[0].Pinned || got.Items[1].NoteID != "flow" {
		t.Fatalf("Retrieve = %+v, %v", got, err)
	}
	if ids := got.NoteIDs(); len(ids) != 2 || ids[0] != "pinned" || ids[1] != "flow" {
		t.Fatalf("NoteIDs = %v", ids)
	}
	if text := got.Render(); !strings.Contains(text, "[gotcha] OTP\nUse 0000") ||
		!strings.Contains(text, "[flow] Checkout") {
		t.Fatalf("Render = %q", text)
	}
}

func TestRetrieveFallsBackToPinnedWhenModelIsUnavailable(t *testing.T) {
	store := &fakeStore{pinned: []knowledge.Note{{ID: "pinned", Title: "OTP", Content: "Use 0000"}}}

	got, err := newService(store, &fakeModel{err: unavailable()}).Retrieve(context.Background(), knowledge.Query{
		ProjectID: projID, Text: "sign in",
	})

	if err != nil || !got.Partial || len(got.Items) != 1 {
		t.Fatalf("Retrieve = %+v, %v", got, err)
	}
}

func TestRetrieveWithoutTextSkipsTheModel(t *testing.T) {
	model := &fakeModel{}

	got, err := newService(&fakeStore{}, model).Retrieve(context.Background(), knowledge.Query{ProjectID: projID})

	if err != nil || len(got.Items) != 0 || got.Render() != "" || len(model.texts) != 0 {
		t.Fatalf("Retrieve = %+v, %v; texts %v", got, err, model.texts)
	}
}

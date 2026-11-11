package http

import (
	"context"
	"net/http"
	"testing"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/knowledge"
)

type fakeKnowledge struct {
	Knowledge
	created  []knowledge.CreateInput
	listed   []knowledge.ListInput
	updated  []knowledge.UpdateInput
	searched []knowledge.SearchInput
}

func otpNote() knowledge.Note {
	return knowledge.Note{
		ID: "note-1", Kind: knowledge.KindGotcha, Title: "OTP", Content: "Use 0000", Source: knowledge.SourceUser,
		Pinned: true, CreatedBy: "user-1", CreatedAt: shopTime(), UpdatedAt: shopTime(),
	}
}

const otpNoteJSON = `{"id":"note-1","kind":"gotcha","title":"OTP","content":"Use 0000","source":"user","pinned":true,` +
	`"indexed":false,"created_by":"user-1","created_at":"2026-11-11T09:00:00Z","updated_at":"2026-11-11T09:00:00Z"}`

func (f *fakeKnowledge) Create(_ context.Context, in knowledge.CreateInput) (knowledge.Note, error) {
	f.created = append(f.created, in)
	return otpNote(), nil
}

func (f *fakeKnowledge) List(_ context.Context, in knowledge.ListInput) (knowledge.Page, error) {
	f.listed = append(f.listed, in)
	return knowledge.Page{Items: []knowledge.Note{otpNote()}, Total: 1, Page: 1, Limit: 20}, nil
}

func (f *fakeKnowledge) Get(_ context.Context, ref knowledge.Ref) (knowledge.Note, error) {
	if ref.NoteID != "note-1" {
		return knowledge.Note{}, fault.WithDetail(fault.ErrNotFound, "note not found")
	}
	return otpNote(), nil
}

func (f *fakeKnowledge) Update(_ context.Context, in knowledge.UpdateInput) (knowledge.Note, error) {
	f.updated = append(f.updated, in)
	return otpNote(), nil
}

func (f *fakeKnowledge) Delete(context.Context, knowledge.Ref) error {
	return nil
}

func (f *fakeKnowledge) Search(_ context.Context, in knowledge.SearchInput) ([]knowledge.Match, error) {
	f.searched = append(f.searched, in)
	return []knowledge.Match{{NoteID: "note-1", Kind: knowledge.KindGotcha, Title: "OTP", Excerpt: "Use 0000",
		Similarity: 0.75}}, nil
}

func TestCreateNote(t *testing.T) {
	notes := &fakeKnowledge{}

	recorder := serveAuthorized(t, Dependencies{Knowledge: notes}, jsonRequest(http.MethodPost,
		"/projects/project-1/knowledge", `{"kind":"gotcha","title":"OTP","content":"Use 0000","pinned":true}`))

	requireResponse(t, recorder, http.StatusCreated, otpNoteJSON)
	want := knowledge.CreateInput{
		UserID: "user-1", ProjectID: "project-1", Kind: knowledge.KindGotcha, Title: "OTP", Content: "Use 0000",
		Pinned: true,
	}
	if notes.created[0] != want {
		t.Fatalf("create input = %+v", notes.created[0])
	}
}

func TestListNotesPassesKindAndPaging(t *testing.T) {
	notes := &fakeKnowledge{}

	recorder := serveAuthorized(t, Dependencies{Knowledge: notes},
		jsonRequest(http.MethodGet, "/projects/project-1/knowledge?kind=flow&page=2&limit=5", ""))

	requireResponse(t, recorder, http.StatusOK, `{"items":[`+otpNoteJSON+`],"total":1,"page":1,"limit":20,`+
		`"has_more":false}`)
	want := knowledge.ListInput{UserID: "user-1", ProjectID: "project-1", Kind: knowledge.KindFlow, Page: 2, Limit: 5}
	if notes.listed[0] != want {
		t.Fatalf("list input = %+v", notes.listed[0])
	}
}

func TestUpdateNoteKeepsAbsentFields(t *testing.T) {
	notes := &fakeKnowledge{}

	recorder := serveAuthorized(t, Dependencies{Knowledge: notes},
		jsonRequest(http.MethodPatch, "/projects/project-1/knowledge/note-1", `{"pinned":false}`))

	in := notes.updated[0]
	if recorder.Code != http.StatusOK || in.Ref.NoteID != "note-1" || in.Pinned == nil || *in.Pinned ||
		in.Title != nil || in.Content != nil || in.Kind != nil {
		t.Fatalf("update = %d, input %+v", recorder.Code, in)
	}
}

func TestGetUnknownNoteIsNotFound(t *testing.T) {
	recorder := serveAuthorized(t, Dependencies{Knowledge: &fakeKnowledge{}},
		jsonRequest(http.MethodGet, "/projects/project-1/knowledge/note-9", ""))

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestDeleteNote(t *testing.T) {
	recorder := serveAuthorized(t, Dependencies{Knowledge: &fakeKnowledge{}},
		jsonRequest(http.MethodDelete, "/projects/project-1/knowledge/note-1", ""))

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestSearchNotes(t *testing.T) {
	notes := &fakeKnowledge{}

	recorder := serveAuthorized(t, Dependencies{Knowledge: notes},
		jsonRequest(http.MethodGet, "/projects/project-1/knowledge/search?q=sign+in&limit=3", ""))

	requireResponse(t, recorder, http.StatusOK, `[{"note_id":"note-1","kind":"gotcha","title":"OTP",`+
		`"excerpt":"Use 0000","similarity":0.75}]`)
	want := knowledge.SearchInput{UserID: "user-1", ProjectID: "project-1", Query: "sign in", Limit: 3}
	if notes.searched[0] != want {
		t.Fatalf("search input = %+v", notes.searched[0])
	}
}

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
	"github.com/mobqa/backend/internal/usecase/knowledge"
	"github.com/mobqa/backend/internal/worker"
)

type knowledgeFixture struct {
	projectFixture
	notes     *postgres.Knowledge
	jobs      *postgres.Jobs
	projectID string
	userID    string
}

func newKnowledge(t *testing.T) knowledgeFixture {
	t.Helper()
	f := newProjects(t)
	owner := f.account(t, "ada")
	return knowledgeFixture{
		projectFixture: f, notes: postgres.NewKnowledge(f.db), jobs: postgres.NewJobs(f.db),
		projectID: f.create(t, owner.ID, "Shop").ID, userID: owner.ID,
	}
}

func (f knowledgeFixture) write(t *testing.T, projectID, title string, pinned bool) knowledge.Note {
	t.Helper()
	created, err := f.notes.CreateNote(context.Background(), knowledge.NewNote{
		ID: uuid.Must(uuid.NewV7()).String(), ProjectID: projectID, Kind: knowledge.KindFlow, Title: title,
		Content: "How " + title + " works", Source: knowledge.SourceUser, Pinned: pinned, CreatedBy: f.userID,
	}, worker.EnqueueInput{Kind: knowledge.IndexJobKind, Payload: []byte(`{}`), MaxAttempts: 5})
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	f.now.Advance(time.Minute)
	return created
}

// queued claims the next index job and reports whether there was one.
func (f knowledgeFixture) queued(t *testing.T) bool {
	t.Helper()
	_, found, err := f.jobs.Claim(context.Background(), worker.ClaimInput{
		Kind: knowledge.IndexJobKind, Owner: "test", Lease: time.Minute,
	})
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	return found
}

// axis is a unit vector along dimension i, so two axes are orthogonal and one axis is identical to itself.
func axis(i int) []float32 {
	vector := make([]float32, knowledge.Dimensions)
	vector[i] = 1
	return vector
}

func (f knowledgeFixture) index(t *testing.T, note knowledge.Note, dimension int) {
	t.Helper()
	err := f.notes.SaveChunks(context.Background(), note.ID, 1, []knowledge.Chunk{
		{Position: 0, Content: note.Content, Embedding: axis(dimension)},
	})
	if err != nil {
		t.Fatalf("SaveChunks: %v", err)
	}
}

func TestKnowledgeCreateQueuesIndexJob(t *testing.T) {
	f := newKnowledge(t)

	created := f.write(t, f.projectID, "Checkout", true)

	if created.Indexed || !created.Pinned || created.Source != knowledge.SourceUser ||
		!created.CreatedAt.Equal(testStart()) || !f.queued(t) {
		t.Fatalf("created = %+v", created)
	}
	source, err := f.notes.Indexable(context.Background(), created.ID)
	if err != nil || source.Revision != 1 || source.IndexedRevision != 0 || source.Title != "Checkout" {
		t.Fatalf("Indexable = %+v, %v", source, err)
	}
}

func TestKnowledgeListFiltersByKindAndPages(t *testing.T) {
	f := newKnowledge(t)
	f.write(t, f.projectID, "Checkout", false)
	newest := f.write(t, f.projectID, "Sign in", false)
	gotcha := knowledge.KindGotcha
	if _, err := f.notes.UpdateNote(context.Background(), knowledge.Change{
		ProjectID: f.projectID, NoteID: newest.ID, Kind: &gotcha,
	}, worker.EnqueueInput{}); err != nil {
		t.Fatalf("UpdateNote: %v", err)
	}

	all, total, err := f.notes.Notes(context.Background(), knowledge.Filter{ProjectID: f.projectID, Limit: 1})
	if err != nil || total != 2 || len(all) != 1 || all[0].ID != newest.ID {
		t.Fatalf("Notes = %+v, %d, %v", all, total, err)
	}
	flows, total, err := f.notes.Notes(context.Background(), knowledge.Filter{
		ProjectID: f.projectID, Kind: knowledge.KindFlow, Limit: 10,
	})
	if err != nil || total != 1 || flows[0].Title != "Checkout" {
		t.Fatalf("flows = %+v, %d, %v", flows, total, err)
	}
}

func TestKnowledgeUpdateReindexesOnlyTextChanges(t *testing.T) {
	f := newKnowledge(t)
	note := f.write(t, f.projectID, "Checkout", false)
	f.queued(t)
	f.index(t, note, 0)
	ctx := context.Background()
	job := worker.EnqueueInput{Kind: knowledge.IndexJobKind, Payload: []byte(`{}`), MaxAttempts: 5}
	pinned := true

	updated, err := f.notes.UpdateNote(ctx, knowledge.Change{ProjectID: f.projectID, NoteID: note.ID, Pinned: &pinned},
		job)
	if err != nil || !updated.Pinned || !updated.Indexed || f.queued(t) {
		t.Fatalf("metadata update = %+v, %v", updated, err)
	}
	content := "Cart, address, then pay"
	updated, err = f.notes.UpdateNote(ctx, knowledge.Change{ProjectID: f.projectID, NoteID: note.ID, Content: &content},
		job)
	if err != nil || updated.Content != content || updated.Title != "Checkout" || updated.Indexed || !f.queued(t) {
		t.Fatalf("text update = %+v, %v", updated, err)
	}
}

func TestKnowledgeSaveChunksRejectsStaleRevisionAndMissingNote(t *testing.T) {
	f := newKnowledge(t)
	note := f.write(t, f.projectID, "Checkout", false)
	ctx := context.Background()
	title := "Payment"
	if _, err := f.notes.UpdateNote(ctx, knowledge.Change{ProjectID: f.projectID, NoteID: note.ID, Title: &title},
		worker.EnqueueInput{Kind: knowledge.IndexJobKind, Payload: []byte(`{}`), MaxAttempts: 5}); err != nil {
		t.Fatalf("UpdateNote: %v", err)
	}
	chunks := []knowledge.Chunk{{Content: "Pay", Embedding: axis(0)}}

	if err := f.notes.SaveChunks(ctx, note.ID, 1, chunks); !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("stale SaveChunks = %v, want conflict", err)
	}
	if err := f.notes.SaveChunks(ctx, uuid.Must(uuid.NewV7()).String(), 1, chunks); !errors.Is(err,
		fault.ErrNotFound) {
		t.Fatalf("missing SaveChunks = %v, want not found", err)
	}
}

func TestKnowledgeNearestRanksProjectChunksBySimilarity(t *testing.T) {
	f := newKnowledge(t)
	checkout := f.write(t, f.projectID, "Checkout", false)
	signIn := f.write(t, f.projectID, "Sign in", true)
	other := f.write(t, f.create(t, f.userID, "Bank").ID, "Transfer", false)
	f.index(t, checkout, 0)
	f.index(t, signIn, 1)
	f.index(t, other, 0)
	ctx := context.Background()

	matches, err := f.notes.Nearest(ctx, f.projectID, axis(0), 5)

	if err != nil || len(matches) != 2 || matches[0].NoteID != checkout.ID || matches[0].Similarity < 0.999 ||
		matches[0].Kind != knowledge.KindFlow || matches[0].Excerpt != "How Checkout works" ||
		matches[1].Similarity > 0.001 {
		t.Fatalf("Nearest = %+v, %v", matches, err)
	}
	found, err := f.notes.Note(ctx, f.projectID, checkout.ID)
	if err != nil || !found.Indexed {
		t.Fatalf("Note = %+v, %v", found, err)
	}
	pinned, err := f.notes.Pinned(ctx, f.projectID, 10)
	if err != nil || len(pinned) != 1 || pinned[0].ID != signIn.ID {
		t.Fatalf("Pinned = %+v, %v", pinned, err)
	}
}

func TestKnowledgeDeleteRemovesChunksAndIsScoped(t *testing.T) {
	f := newKnowledge(t)
	note := f.write(t, f.projectID, "Checkout", false)
	f.index(t, note, 0)
	ctx := context.Background()
	other := f.create(t, f.userID, "Bank").ID

	if err := f.notes.DeleteNote(ctx, other, note.ID); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("DeleteNote from another project = %v, want not found", err)
	}
	if _, err := f.notes.Note(ctx, other, note.ID); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("Note from another project = %v, want not found", err)
	}
	if err := f.notes.DeleteNote(ctx, f.projectID, note.ID); err != nil {
		t.Fatalf("DeleteNote: %v", err)
	}
	matches, err := f.notes.Nearest(ctx, f.projectID, axis(0), 5)
	if err != nil || len(matches) != 0 {
		t.Fatalf("Nearest after delete = %+v, %v", matches, err)
	}
}

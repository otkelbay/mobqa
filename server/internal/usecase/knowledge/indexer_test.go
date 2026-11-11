package knowledge_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/knowledge"
	"github.com/mobqa/backend/internal/worker"
)

type fakeSources struct {
	note     knowledge.Indexable
	missing  bool
	saveErr  error
	saved    []knowledge.Chunk
	revision int
}

func (s *fakeSources) Indexable(context.Context, string) (knowledge.Indexable, error) {
	if s.missing {
		return knowledge.Indexable{}, fault.ErrNotFound
	}
	return s.note, nil
}

func (s *fakeSources) SaveChunks(_ context.Context, _ string, revision int, chunks []knowledge.Chunk) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	s.saved, s.revision = chunks, revision
	return nil
}

func indexJob() worker.Job {
	return worker.Job{Kind: knowledge.IndexJobKind, Payload: []byte(`{"note_id":"note-1"}`), Attempt: 1, MaxAttempts: 5}
}

func pendingNote(content string) knowledge.Indexable {
	return knowledge.Indexable{NoteID: "note-1", Revision: 3, IndexedRevision: 2, Title: "Checkout", Content: content}
}

func TestIndexerChunksLongNotesAtParagraphsAndWords(t *testing.T) {
	long := strings.TrimSpace(strings.Repeat("word ", 300))
	content := "Open the cart.\r\n\r\nTap Pay.\n\n" + long + "\n\n" + strings.Repeat("y", 1200)
	store := &fakeSources{note: pendingNote(content)}
	model := &fakeModel{}

	if err := knowledge.NewIndexer(store, model).Run(context.Background(), indexJob()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if store.revision != 3 || len(store.saved) != 5 {
		t.Fatalf("saved revision %d, %d chunks", store.revision, len(store.saved))
	}
	if first := store.saved[0].Content; first != "Open the cart.\n\nTap Pay." {
		t.Fatalf("first chunk = %q", first)
	}
	for i, c := range store.saved {
		if c.Position != i || utf8.RuneCountInString(c.Content) > 1000 || strings.HasSuffix(c.Content, " ") {
			t.Fatalf("chunk %d = position %d, %d runes", i, c.Position, utf8.RuneCountInString(c.Content))
		}
	}
	if !strings.HasPrefix(model.texts[0], "Checkout\n\nOpen the cart.") {
		t.Fatalf("embedded text = %q", model.texts[0][:30])
	}
}

func TestIndexerSkipsWhatNeedsNoWork(t *testing.T) {
	tests := []struct {
		name  string
		store *fakeSources
	}{
		{name: "deleted note", store: &fakeSources{missing: true}},
		{name: "already indexed", store: &fakeSources{note: knowledge.Indexable{Revision: 2, IndexedRevision: 2}}},
		{name: "changed meanwhile", store: &fakeSources{note: pendingNote("Tap Pay."), saveErr: fault.ErrConflict}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := knowledge.NewIndexer(tt.store, &fakeModel{}).Run(context.Background(), indexJob()); err != nil {
				t.Fatalf("Run: %v", err)
			}
			if len(tt.store.saved) != 0 {
				t.Fatalf("saved %d chunks", len(tt.store.saved))
			}
		})
	}
}

func TestIndexerRetriesOnlyAnUnreachableModel(t *testing.T) {
	tests := []struct {
		name      string
		model     *fakeModel
		permanent bool
	}{
		{name: "unreachable", model: &fakeModel{err: fault.ErrUnavailable}},
		{name: "not configured", model: &fakeModel{err: unavailable()}, permanent: true},
		{name: "wrong dimensions", model: &fakeModel{dimensions: 768}, permanent: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := knowledge.NewIndexer(&fakeSources{note: pendingNote("Tap Pay.")}, tt.model).Run(
				context.Background(), indexJob())
			if err == nil || worker.IsPermanent(err) != tt.permanent {
				t.Fatalf("Run error = %v, permanent %v", err, worker.IsPermanent(err))
			}
		})
	}
}

func TestIndexerRejectsBrokenPayload(t *testing.T) {
	err := knowledge.NewIndexer(&fakeSources{}, &fakeModel{}).Run(context.Background(), worker.Job{Payload: []byte("{")})

	if !worker.IsPermanent(err) {
		t.Fatalf("Run error = %v, want permanent", err)
	}
	if errors.Is(err, fault.ErrNotFound) {
		t.Fatal("a broken payload is not a missing note")
	}
}

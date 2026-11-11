package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/chat"
	"github.com/mobqa/backend/internal/worker"
)

// IndexJobKind is the job that embeds a note's current text.
const IndexJobKind = "knowledge_index"

// Indexable is a note's text at a revision. Revision grows with every change of the title or content;
// IndexedRevision is the revision the stored chunks were made from.
type Indexable struct {
	NoteID          string
	Revision        int
	IndexedRevision int
	Title           string
	Content         string
}

type Chunk struct {
	Position  int
	Content   string
	Embedding []float32
}

type sources interface {
	// Indexable reports ErrNotFound when the note is gone.
	Indexable(ctx context.Context, noteID string) (Indexable, error)
	// SaveChunks replaces the note's chunks with ones made from revision. It reports ErrConflict when the
	// note has moved past revision, whose own job will index it, and ErrNotFound when it is gone.
	SaveChunks(ctx context.Context, noteID string, revision int, chunks []Chunk) error
}

// Indexer fulfils index jobs. Jobs for one note may overlap or repeat; the revision check makes the
// newest text win and every repeat a no-op.
type Indexer struct {
	store sources
	model embedder
}

func NewIndexer(notes sources, model embedder) *Indexer {
	return &Indexer{store: notes, model: model}
}

// Run handles an index job. Each chunk is embedded with its note's title so that it carries its topic.
// A model that is not configured, or returns vectors the store cannot keep, fails the job at once.
func (x *Indexer) Run(ctx context.Context, job worker.Job) error {
	var payload indexPayload
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return worker.Permanent(fmt.Errorf("decode index payload: %w", err))
	}
	note, err := x.store.Indexable(ctx, payload.NoteID)
	if errors.Is(err, fault.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load note: %w", err)
	}
	if note.IndexedRevision == note.Revision {
		return nil
	}
	pieces := chunk(note.Content)
	inputs := make([]string, len(pieces))
	for i, piece := range pieces {
		inputs[i] = note.Title + separator + piece
	}
	vectors, err := embed(ctx, x.model, inputs)
	if errors.Is(err, chat.ErrNotConfigured) || errors.Is(err, errDimensions) {
		return worker.Permanent(err)
	}
	if err != nil {
		return err
	}
	chunks := make([]Chunk, len(pieces))
	for i, piece := range pieces {
		chunks[i] = Chunk{Position: i, Content: piece, Embedding: vectors[i]}
	}
	err = x.store.SaveChunks(ctx, note.NoteID, note.Revision, chunks)
	if errors.Is(err, fault.ErrConflict) || errors.Is(err, fault.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("save chunks: %w", err)
	}
	return nil
}

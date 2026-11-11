package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/knowledge"
	"github.com/mobqa/backend/internal/worker"
)

const noteColumns = `id, project_id, kind, title, content, source, pinned, indexed_revision = revision, created_by,
	created_at, updated_at`

type noteRow struct {
	id        string
	projectID string
	kind      string
	title     string
	content   string
	source    string
	pinned    bool
	indexed   bool
	createdBy string
	createdAt time.Time
	updatedAt time.Time
}

func (r *noteRow) targets() []any {
	return []any{
		&r.id, &r.projectID, &r.kind, &r.title, &r.content, &r.source, &r.pinned, &r.indexed, &r.createdBy,
		&r.createdAt, &r.updatedAt,
	}
}

func (r noteRow) note() knowledge.Note {
	return knowledge.Note{
		ID:        r.id,
		ProjectID: r.projectID,
		Kind:      knowledge.Kind(r.kind),
		Title:     r.title,
		Content:   r.content,
		Source:    knowledge.Source(r.source),
		Pinned:    r.pinned,
		Indexed:   r.indexed,
		CreatedBy: r.createdBy,
		CreatedAt: r.createdAt,
		UpdatedAt: r.updatedAt,
	}
}

func collectNotes(rows pgx.Rows) ([]knowledge.Note, error) {
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (knowledge.Note, error) {
		var r noteRow
		err := row.Scan(r.targets()...)
		return r.note(), err
	})
}

type Knowledge struct {
	db *DB
}

func NewKnowledge(db *DB) *Knowledge {
	return &Knowledge{db: db}
}

func (s *Knowledge) CreateNote(ctx context.Context, in knowledge.NewNote, job worker.EnqueueInput,
) (knowledge.Note, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	now := s.db.clock.Now()
	var row noteRow
	err := pgx.BeginFunc(ctx, s.db.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			INSERT INTO knowledge_notes (id, project_id, kind, title, content, source, pinned, revision,
				indexed_revision, created_by, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, 1, 0, $8, $9, $9)
			RETURNING `+noteColumns,
			in.ID, in.ProjectID, string(in.Kind), in.Title, in.Content, string(in.Source), in.Pinned, in.CreatedBy, now,
		).Scan(row.targets()...)
		if err != nil {
			return fmt.Errorf("insert note: %w", err)
		}
		_, err = s.db.enqueue(ctx, tx, job)
		return err
	})
	if err != nil {
		return knowledge.Note{}, fmt.Errorf("create note: %w", err)
	}
	return row.note(), nil
}

func (s *Knowledge) Notes(ctx context.Context, filter knowledge.Filter) ([]knowledge.Note, int, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	const where = `WHERE project_id = $1 AND ($2 = '' OR kind = $2)`
	var total int
	if err := s.db.pool.QueryRow(ctx, `SELECT count(*) FROM knowledge_notes `+where,
		filter.ProjectID, string(filter.Kind)).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count notes: %w", err)
	}
	rows, err := s.db.pool.Query(ctx, `
		SELECT `+noteColumns+` FROM knowledge_notes `+where+`
		ORDER BY updated_at DESC, id DESC
		LIMIT $3 OFFSET $4`,
		filter.ProjectID, string(filter.Kind), filter.Limit, filter.Offset)
	if err != nil {
		return nil, 0, fmt.Errorf("select notes: %w", err)
	}
	notes, err := collectNotes(rows)
	if err != nil {
		return nil, 0, fmt.Errorf("read notes: %w", err)
	}
	return notes, total, nil
}

func (s *Knowledge) Note(ctx context.Context, projectID, id string) (knowledge.Note, error) {
	if !validID(id) {
		return knowledge.Note{}, fmt.Errorf("note %s: %w", id, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var row noteRow
	err := s.db.pool.QueryRow(ctx, `SELECT `+noteColumns+` FROM knowledge_notes WHERE project_id = $1 AND id = $2`,
		projectID, id).Scan(row.targets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return knowledge.Note{}, fmt.Errorf("note %s: %w", id, fault.ErrNotFound)
	}
	if err != nil {
		return knowledge.Note{}, fmt.Errorf("query note: %w", err)
	}
	return row.note(), nil
}

// UpdateNote bumps the revision when the title or content changes, which makes chunks of the earlier
// text stale and lets the queued job replace them.
func (s *Knowledge) UpdateNote(ctx context.Context, change knowledge.Change, job worker.EnqueueInput,
) (knowledge.Note, error) {
	if !validID(change.NoteID) {
		return knowledge.Note{}, fmt.Errorf("note %s: %w", change.NoteID, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var row noteRow
	err := pgx.BeginFunc(ctx, s.db.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			UPDATE knowledge_notes SET kind = coalesce($3, kind), title = coalesce($4, title),
				content = coalesce($5, content), pinned = coalesce($6, pinned),
				revision = revision + CASE WHEN $4::text IS NULL AND $5::text IS NULL THEN 0 ELSE 1 END,
				updated_at = $7
			WHERE project_id = $1 AND id = $2
			RETURNING `+noteColumns,
			change.ProjectID, change.NoteID, (*string)(change.Kind), change.Title, change.Content, change.Pinned,
			s.db.clock.Now(),
		).Scan(row.targets()...)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("note %s: %w", change.NoteID, fault.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("update note: %w", err)
		}
		if !change.ChangesText() {
			return nil
		}
		_, err = s.db.enqueue(ctx, tx, job)
		return err
	})
	if err != nil {
		return knowledge.Note{}, fmt.Errorf("update note: %w", err)
	}
	return row.note(), nil
}

func (s *Knowledge) DeleteNote(ctx context.Context, projectID, id string) error {
	if !validID(id) {
		return fmt.Errorf("note %s: %w", id, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	tag, err := s.db.pool.Exec(ctx, `DELETE FROM knowledge_notes WHERE project_id = $1 AND id = $2`, projectID, id)
	if err != nil {
		return fmt.Errorf("delete note: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("note %s: %w", id, fault.ErrNotFound)
	}
	return nil
}

func (s *Knowledge) Pinned(ctx context.Context, projectID string, limit int) ([]knowledge.Note, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	rows, err := s.db.pool.Query(ctx, `
		SELECT `+noteColumns+` FROM knowledge_notes
		WHERE project_id = $1 AND pinned
		ORDER BY updated_at DESC, id DESC
		LIMIT $2`,
		projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("select pinned notes: %w", err)
	}
	notes, err := collectNotes(rows)
	if err != nil {
		return nil, fmt.Errorf("read pinned notes: %w", err)
	}
	return notes, nil
}

// Nearest runs the HNSW search with an iterative scan, so filtering by project still fills the limit
// instead of returning the few project chunks among the index's first candidates.
func (s *Knowledge) Nearest(ctx context.Context, projectID string, vector []float32, limit int,
) ([]knowledge.Match, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var matches []knowledge.Match
	err := pgx.BeginTxFunc(ctx, s.db.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL hnsw.iterative_scan = strict_order`); err != nil {
			return fmt.Errorf("enable iterative scan: %w", err)
		}
		rows, err := tx.Query(ctx, `
			SELECT n.id, n.kind, n.title, c.content, 1 - (c.embedding <=> $2)
			FROM knowledge_chunks c JOIN knowledge_notes n ON n.id = c.note_id
			WHERE c.project_id = $1
			ORDER BY c.embedding <=> $2
			LIMIT $3`,
			projectID, pgvector.NewVector(vector), limit)
		if err != nil {
			return fmt.Errorf("select nearest chunks: %w", err)
		}
		matches, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (knowledge.Match, error) {
			var m knowledge.Match
			err := row.Scan(&m.NoteID, &m.Kind, &m.Title, &m.Excerpt, &m.Similarity)
			return m, err
		})
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("search chunks: %w", err)
	}
	return matches, nil
}

func (s *Knowledge) Indexable(ctx context.Context, noteID string) (knowledge.Indexable, error) {
	if !validID(noteID) {
		return knowledge.Indexable{}, fmt.Errorf("note %s: %w", noteID, fault.ErrNotFound)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	note := knowledge.Indexable{NoteID: noteID}
	err := s.db.pool.QueryRow(ctx, `
		SELECT revision, indexed_revision, title, content FROM knowledge_notes WHERE id = $1`, noteID,
	).Scan(&note.Revision, &note.IndexedRevision, &note.Title, &note.Content)
	if errors.Is(err, pgx.ErrNoRows) {
		return knowledge.Indexable{}, fmt.Errorf("note %s: %w", noteID, fault.ErrNotFound)
	}
	if err != nil {
		return knowledge.Indexable{}, fmt.Errorf("query note: %w", err)
	}
	return note, nil
}

// SaveChunks locks the note so that a concurrent edit either lands before, and fails the revision check,
// or waits until the chunks of this revision are in place and then makes them stale.
func (s *Knowledge) SaveChunks(ctx context.Context, noteID string, revision int, chunks []knowledge.Chunk) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	err := pgx.BeginFunc(ctx, s.db.pool, func(tx pgx.Tx) error {
		var projectID string
		var current int
		err := tx.QueryRow(ctx, `
			SELECT project_id, revision FROM knowledge_notes WHERE id = $1 FOR UPDATE`, noteID,
		).Scan(&projectID, &current)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("note %s: %w", noteID, fault.ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("lock note: %w", err)
		}
		if current != revision {
			return fmt.Errorf("note %s is at revision %d: %w", noteID, current, fault.ErrConflict)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM knowledge_chunks WHERE note_id = $1`, noteID); err != nil {
			return fmt.Errorf("delete chunks: %w", err)
		}
		batch := &pgx.Batch{}
		for _, c := range chunks {
			batch.Queue(`
				INSERT INTO knowledge_chunks (note_id, position, project_id, content, embedding)
				VALUES ($1, $2, $3, $4, $5)`,
				noteID, c.Position, projectID, c.Content, pgvector.NewVector(c.Embedding))
		}
		batch.Queue(`UPDATE knowledge_notes SET indexed_revision = $2 WHERE id = $1`, noteID, revision)
		if err := tx.SendBatch(ctx, batch).Close(); err != nil {
			return fmt.Errorf("insert chunks: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("save chunks: %w", err)
	}
	return nil
}

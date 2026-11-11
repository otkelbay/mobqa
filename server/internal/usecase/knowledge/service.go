package knowledge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/worker"
)

const (
	defaultPageSize  = 20
	maxPageSize      = 100
	maxQueryLength   = 1000
	defaultMatches   = 10
	maxMatches       = 50
	maxPinned        = 20
	retrieveMatches  = 20
	defaultBudget    = 6000
	minSimilarity    = 0.3
	indexJobAttempts = 5
)

type NewNote struct {
	ID        string
	ProjectID string
	Kind      Kind
	Title     string
	Content   string
	Source    Source
	Pinned    bool
	CreatedBy string
}

// Change updates a note; nil fields stay as they are. A new title or content is indexed again.
type Change struct {
	ProjectID string
	NoteID    string
	Kind      *Kind
	Title     *string
	Content   *string
	Pinned    *bool
}

func (c Change) ChangesText() bool {
	return c.Title != nil || c.Content != nil
}

type Filter struct {
	ProjectID string
	Kind      Kind
	Limit     int
	Offset    int
}

// store reports ErrNotFound for a note outside the project. Listings are newest first.
type store interface {
	// CreateNote stores the note and queues its index job in one transaction.
	CreateNote(ctx context.Context, in NewNote, job worker.EnqueueInput) (Note, error)
	Notes(ctx context.Context, filter Filter) ([]Note, int, error)
	Note(ctx context.Context, projectID, id string) (Note, error)
	// UpdateNote queues job in the same transaction when the change touches the title or content.
	UpdateNote(ctx context.Context, change Change, job worker.EnqueueInput) (Note, error)
	DeleteNote(ctx context.Context, projectID, id string) error
	Pinned(ctx context.Context, projectID string, limit int) ([]Note, error)
	// Nearest returns the project's chunks closest to vector, most similar first.
	Nearest(ctx context.Context, projectID string, vector []float32, limit int) ([]Match, error)
}

type access interface {
	Require(ctx context.Context, projectID, userID string, need project.Role) (project.Role, error)
}

type Service struct {
	store  store
	access access
	model  embedder
}

func NewService(notes store, projects access, model embedder) *Service {
	return &Service{store: notes, access: projects, model: model}
}

type indexPayload struct {
	NoteID string `json:"note_id"`
}

// IndexJob is the job that indexes a note after its text changes.
func IndexJob(noteID string) (worker.EnqueueInput, error) {
	payload, err := json.Marshal(indexPayload{NoteID: noteID})
	if err != nil {
		return worker.EnqueueInput{}, fmt.Errorf("encode index payload: %w", err)
	}
	return worker.EnqueueInput{Kind: IndexJobKind, Payload: payload, MaxAttempts: indexJobAttempts}, nil
}

type CreateInput struct {
	UserID    string
	ProjectID string
	Kind      Kind
	Title     string
	Content   string
	Pinned    bool
}

func (in CreateInput) Validate() error {
	if err := validateKind(in.Kind); err != nil {
		return err
	}
	if err := validateTitle(in.Title); err != nil {
		return err
	}
	return validateContent(in.Content)
}

// Create stores a note people wrote and queues it for indexing; it can be found by meaning once indexed.
func (s *Service) Create(ctx context.Context, in CreateInput) (Note, error) {
	if err := in.Validate(); err != nil {
		return Note{}, err
	}
	if _, err := s.access.Require(ctx, in.ProjectID, in.UserID, project.RoleEngineer); err != nil {
		return Note{}, err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Note{}, fmt.Errorf("generate note id: %w", err)
	}
	job, err := IndexJob(id.String())
	if err != nil {
		return Note{}, err
	}
	created, err := s.store.CreateNote(ctx, NewNote{
		ID: id.String(), ProjectID: in.ProjectID, Kind: in.Kind, Title: strings.TrimSpace(in.Title),
		Content: strings.TrimSpace(in.Content), Source: SourceUser, Pinned: in.Pinned, CreatedBy: in.UserID,
	}, job)
	if err != nil {
		return Note{}, fmt.Errorf("create note: %w", err)
	}
	return created, nil
}

type ListInput struct {
	UserID    string
	ProjectID string
	Kind      Kind
	Page      int
	Limit     int
}

func (in ListInput) Validate() error {
	if in.Kind != "" {
		if err := validateKind(in.Kind); err != nil {
			return err
		}
	}
	if in.Page < 0 || in.Limit < 0 || in.Limit > maxPageSize {
		return fault.WithDetail(fault.ErrInvalidInput, "page must be positive and limit at most 100")
	}
	return nil
}

type Page struct {
	Items []Note
	Total int
	Page  int
	Limit int
}

func (p Page) HasMore() bool {
	return p.Page*p.Limit < p.Total
}

func (s *Service) List(ctx context.Context, in ListInput) (Page, error) {
	if err := in.Validate(); err != nil {
		return Page{}, err
	}
	if _, err := s.access.Require(ctx, in.ProjectID, in.UserID, project.RoleViewer); err != nil {
		return Page{}, err
	}
	page, limit := max(in.Page, 1), in.Limit
	if limit == 0 {
		limit = defaultPageSize
	}
	items, total, err := s.store.Notes(ctx, Filter{
		ProjectID: in.ProjectID, Kind: in.Kind, Limit: limit, Offset: (page - 1) * limit,
	})
	if err != nil {
		return Page{}, fmt.Errorf("list notes: %w", err)
	}
	return Page{Items: items, Total: total, Page: page, Limit: limit}, nil
}

type Ref struct {
	UserID    string
	ProjectID string
	NoteID    string
}

func (s *Service) Get(ctx context.Context, ref Ref) (Note, error) {
	if _, err := s.access.Require(ctx, ref.ProjectID, ref.UserID, project.RoleViewer); err != nil {
		return Note{}, err
	}
	found, err := s.store.Note(ctx, ref.ProjectID, ref.NoteID)
	if errors.Is(err, fault.ErrNotFound) {
		return Note{}, fault.WithDetail(fault.ErrNotFound, "note not found")
	}
	if err != nil {
		return Note{}, fmt.Errorf("load note: %w", err)
	}
	return found, nil
}

type UpdateInput struct {
	Ref     Ref
	Kind    *Kind
	Title   *string
	Content *string
	Pinned  *bool
}

func (in UpdateInput) Validate() error {
	if in.Kind != nil {
		if err := validateKind(*in.Kind); err != nil {
			return err
		}
	}
	if in.Title != nil {
		if err := validateTitle(*in.Title); err != nil {
			return err
		}
	}
	if in.Content != nil {
		return validateContent(*in.Content)
	}
	return nil
}

func (s *Service) Update(ctx context.Context, in UpdateInput) (Note, error) {
	if err := in.Validate(); err != nil {
		return Note{}, err
	}
	if _, err := s.access.Require(ctx, in.Ref.ProjectID, in.Ref.UserID, project.RoleEngineer); err != nil {
		return Note{}, err
	}
	job, err := IndexJob(in.Ref.NoteID)
	if err != nil {
		return Note{}, err
	}
	updated, err := s.store.UpdateNote(ctx, Change{
		ProjectID: in.Ref.ProjectID, NoteID: in.Ref.NoteID, Kind: in.Kind, Title: trimmed(in.Title),
		Content: trimmed(in.Content), Pinned: in.Pinned,
	}, job)
	if errors.Is(err, fault.ErrNotFound) {
		return Note{}, fault.WithDetail(fault.ErrNotFound, "note not found")
	}
	if err != nil {
		return Note{}, fmt.Errorf("update note: %w", err)
	}
	return updated, nil
}

func trimmed(text *string) *string {
	if text == nil {
		return nil
	}
	value := strings.TrimSpace(*text)
	return &value
}

func (s *Service) Delete(ctx context.Context, ref Ref) error {
	if _, err := s.access.Require(ctx, ref.ProjectID, ref.UserID, project.RoleEngineer); err != nil {
		return err
	}
	err := s.store.DeleteNote(ctx, ref.ProjectID, ref.NoteID)
	if errors.Is(err, fault.ErrNotFound) {
		return fault.WithDetail(fault.ErrNotFound, "note not found")
	}
	if err != nil {
		return fmt.Errorf("delete note: %w", err)
	}
	return nil
}

type SearchInput struct {
	UserID    string
	ProjectID string
	Query     string
	Limit     int
}

func (in SearchInput) Validate() error {
	if strings.TrimSpace(in.Query) == "" || utf8.RuneCountInString(in.Query) > maxQueryLength {
		return fault.WithDetail(fault.ErrInvalidInput, "query must be 1 to 1000 characters")
	}
	if in.Limit < 0 || in.Limit > maxMatches {
		return fault.WithDetail(fault.ErrInvalidInput, "limit must be at most 50")
	}
	return nil
}

// Search finds the indexed chunks closest in meaning to the query, most similar first.
func (s *Service) Search(ctx context.Context, in SearchInput) ([]Match, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	if _, err := s.access.Require(ctx, in.ProjectID, in.UserID, project.RoleViewer); err != nil {
		return nil, err
	}
	vectors, err := embed(ctx, s.model, []string{strings.TrimSpace(in.Query)})
	if errors.Is(err, fault.ErrUnavailable) {
		return nil, fault.WithDetail(fault.ErrUnavailable, "the embedding model is unavailable; try again later")
	}
	if err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit == 0 {
		limit = defaultMatches
	}
	matches, err := s.store.Nearest(ctx, in.ProjectID, vectors[0], limit)
	if err != nil {
		return nil, fmt.Errorf("search notes: %w", err)
	}
	return matches, nil
}

// Query asks for the knowledge that fits a prompt about Text within Budget runes; zero means the default.
type Query struct {
	ProjectID string
	Text      string
	Budget    int
}

// Retrieve chooses knowledge for a prompt: pinned notes first, then the chunks closest in meaning to the
// query, skipping weak matches and whatever does not fit the budget. Callers check access themselves.
// An unavailable embedding model degrades to pinned notes only rather than failing the prompt.
func (s *Service) Retrieve(ctx context.Context, q Query) (Context, error) {
	budget := q.Budget
	if budget <= 0 {
		budget = defaultBudget
	}
	pinned, err := s.store.Pinned(ctx, q.ProjectID, maxPinned)
	if err != nil {
		return Context{}, fmt.Errorf("load pinned notes: %w", err)
	}
	var chosen Context
	seen := make(map[string]bool, len(pinned))
	for _, note := range pinned {
		seen[note.ID] = true
		chosen.add(Item{NoteID: note.ID, Kind: note.Kind, Title: note.Title, Text: note.Content, Pinned: true}, &budget)
	}
	if strings.TrimSpace(q.Text) == "" {
		return chosen, nil
	}
	vectors, err := embed(ctx, s.model, []string{strings.TrimSpace(q.Text)})
	if errors.Is(err, fault.ErrUnavailable) {
		chosen.Partial = true
		return chosen, nil
	}
	if err != nil {
		return Context{}, err
	}
	matches, err := s.store.Nearest(ctx, q.ProjectID, vectors[0], retrieveMatches)
	if err != nil {
		return Context{}, fmt.Errorf("find matching notes: %w", err)
	}
	for _, match := range matches {
		if seen[match.NoteID] || match.Similarity < minSimilarity {
			continue
		}
		chosen.add(Item{NoteID: match.NoteID, Kind: match.Kind, Title: match.Title, Text: match.Excerpt}, &budget)
	}
	return chosen, nil
}

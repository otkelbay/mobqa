package http

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/mobqa/backend/internal/usecase/knowledge"
)

type Knowledge interface {
	Create(ctx context.Context, in knowledge.CreateInput) (knowledge.Note, error)
	List(ctx context.Context, in knowledge.ListInput) (knowledge.Page, error)
	Get(ctx context.Context, ref knowledge.Ref) (knowledge.Note, error)
	Update(ctx context.Context, in knowledge.UpdateInput) (knowledge.Note, error)
	Delete(ctx context.Context, ref knowledge.Ref) error
	Search(ctx context.Context, in knowledge.SearchInput) ([]knowledge.Match, error)
}

type createNoteRequest struct {
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	Content string `json:"content"`
	Pinned  bool   `json:"pinned"`
}

type updateNoteRequest struct {
	Kind    *string `json:"kind"`
	Title   *string `json:"title"`
	Content *string `json:"content"`
	Pinned  *bool   `json:"pinned"`
}

type noteResponse struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	Source    string    `json:"source"`
	Pinned    bool      `json:"pinned"`
	Indexed   bool      `json:"indexed"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type notePageResponse struct {
	Items   []noteResponse `json:"items"`
	Total   int            `json:"total"`
	Page    int            `json:"page"`
	Limit   int            `json:"limit"`
	HasMore bool           `json:"has_more"`
}

type matchResponse struct {
	NoteID     string  `json:"note_id"`
	Kind       string  `json:"kind"`
	Title      string  `json:"title"`
	Excerpt    string  `json:"excerpt"`
	Similarity float64 `json:"similarity"`
}

func newNoteResponse(n knowledge.Note) noteResponse {
	return noteResponse{
		ID:        n.ID,
		Kind:      string(n.Kind),
		Title:     n.Title,
		Content:   n.Content,
		Source:    string(n.Source),
		Pinned:    n.Pinned,
		Indexed:   n.Indexed,
		CreatedBy: n.CreatedBy,
		CreatedAt: n.CreatedAt,
		UpdatedAt: n.UpdatedAt,
	}
}

type knowledgeHandler struct {
	knowledge Knowledge
	logger    *slog.Logger
}

func (h knowledgeHandler) ref(r *http.Request) knowledge.Ref {
	return knowledge.Ref{
		UserID: currentUserID(r.Context()), ProjectID: r.PathValue("projectID"), NoteID: r.PathValue("noteID"),
	}
}

func (h knowledgeHandler) create(w http.ResponseWriter, r *http.Request) {
	var request createNoteRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	created, err := h.knowledge.Create(r.Context(), knowledge.CreateInput{
		UserID:    currentUserID(r.Context()),
		ProjectID: r.PathValue("projectID"),
		Kind:      knowledge.Kind(request.Kind),
		Title:     request.Title,
		Content:   request.Content,
		Pinned:    request.Pinned,
	})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusCreated, newNoteResponse(created))
}

func (h knowledgeHandler) list(w http.ResponseWriter, r *http.Request) {
	page, err := queryInt(r, "page")
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	limit, err := queryInt(r, "limit")
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	found, err := h.knowledge.List(r.Context(), knowledge.ListInput{
		UserID: currentUserID(r.Context()), ProjectID: r.PathValue("projectID"),
		Kind: knowledge.Kind(r.URL.Query().Get("kind")), Page: page, Limit: limit,
	})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	items := make([]noteResponse, len(found.Items))
	for i, item := range found.Items {
		items[i] = newNoteResponse(item)
	}
	writeJSON(w, http.StatusOK, notePageResponse{
		Items: items, Total: found.Total, Page: found.Page, Limit: found.Limit, HasMore: found.HasMore(),
	})
}

func (h knowledgeHandler) get(w http.ResponseWriter, r *http.Request) {
	found, err := h.knowledge.Get(r.Context(), h.ref(r))
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, newNoteResponse(found))
}

func (h knowledgeHandler) update(w http.ResponseWriter, r *http.Request) {
	var request updateNoteRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	updated, err := h.knowledge.Update(r.Context(), knowledge.UpdateInput{
		Ref:     h.ref(r),
		Kind:    (*knowledge.Kind)(request.Kind),
		Title:   request.Title,
		Content: request.Content,
		Pinned:  request.Pinned,
	})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, newNoteResponse(updated))
}

func (h knowledgeHandler) remove(w http.ResponseWriter, r *http.Request) {
	if err := h.knowledge.Delete(r.Context(), h.ref(r)); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h knowledgeHandler) search(w http.ResponseWriter, r *http.Request) {
	limit, err := queryInt(r, "limit")
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	found, err := h.knowledge.Search(r.Context(), knowledge.SearchInput{
		UserID: currentUserID(r.Context()), ProjectID: r.PathValue("projectID"), Query: r.URL.Query().Get("q"),
		Limit: limit,
	})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	matches := make([]matchResponse, len(found))
	for i, m := range found {
		matches[i] = matchResponse{
			NoteID: m.NoteID, Kind: string(m.Kind), Title: m.Title, Excerpt: m.Excerpt, Similarity: m.Similarity,
		}
	}
	writeJSON(w, http.StatusOK, matches)
}

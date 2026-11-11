package http

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/mobqa/backend/internal/usecase/action"
)

type Actions interface {
	List(ctx context.Context, userID, projectID string) ([]action.Action, error)
	Get(ctx context.Context, ref action.Ref) (action.Action, error)
	Create(ctx context.Context, in action.CreateInput) (action.Action, error)
	Replace(ctx context.Context, in action.ReplaceInput) (action.Action, error)
	Delete(ctx context.Context, ref action.Ref) error
}

type headerBody struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type extractionBody struct {
	Variable string `json:"variable"`
	Pattern  string `json:"pattern"`
}

type actionRequestBody struct {
	Method         string           `json:"method"`
	URL            string           `json:"url"`
	Headers        []headerBody     `json:"headers"`
	Body           string           `json:"body"`
	ExpectedStatus int              `json:"expected_status"`
	Extract        []extractionBody `json:"extract"`
}

type actionDefinitionRequest struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Request     actionRequestBody `json:"request"`
}

type actionResponse struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Request     actionRequestBody `json:"request"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

func (r actionDefinitionRequest) definition() action.Definition {
	headers := make([]action.Header, len(r.Request.Headers))
	for i, h := range r.Request.Headers {
		headers[i] = action.Header{Name: h.Name, Value: h.Value}
	}
	extract := make([]action.Extraction, len(r.Request.Extract))
	for i, e := range r.Request.Extract {
		extract[i] = action.Extraction{Variable: e.Variable, Pattern: e.Pattern}
	}
	return action.Definition{
		Name:        r.Name,
		Description: r.Description,
		Request: action.Request{
			Method: action.Method(r.Request.Method), URL: r.Request.URL, Headers: headers, Body: r.Request.Body,
			ExpectedStatus: r.Request.ExpectedStatus, Extract: extract,
		},
	}
}

func newActionResponse(a action.Action) actionResponse {
	headers := make([]headerBody, len(a.Request.Headers))
	for i, h := range a.Request.Headers {
		headers[i] = headerBody{Name: h.Name, Value: h.Value}
	}
	extract := make([]extractionBody, len(a.Request.Extract))
	for i, e := range a.Request.Extract {
		extract[i] = extractionBody{Variable: e.Variable, Pattern: e.Pattern}
	}
	return actionResponse{
		ID:          a.ID,
		Name:        a.Name,
		Description: a.Description,
		Request: actionRequestBody{
			Method: string(a.Request.Method), URL: a.Request.URL, Headers: headers, Body: a.Request.Body,
			ExpectedStatus: a.Request.ExpectedStatus, Extract: extract,
		},
		CreatedAt: a.CreatedAt,
		UpdatedAt: a.UpdatedAt,
	}
}

type actionHandler struct {
	actions Actions
	logger  *slog.Logger
}

func (h actionHandler) ref(r *http.Request) action.Ref {
	return action.Ref{
		UserID: currentUserID(r.Context()), ProjectID: r.PathValue("projectID"), ActionID: r.PathValue("actionID"),
	}
}

func (h actionHandler) list(w http.ResponseWriter, r *http.Request) {
	found, err := h.actions.List(r.Context(), currentUserID(r.Context()), r.PathValue("projectID"))
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	items := make([]actionResponse, len(found))
	for i, item := range found {
		items[i] = newActionResponse(item)
	}
	writeJSON(w, http.StatusOK, items)
}

func (h actionHandler) get(w http.ResponseWriter, r *http.Request) {
	found, err := h.actions.Get(r.Context(), h.ref(r))
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, newActionResponse(found))
}

func (h actionHandler) create(w http.ResponseWriter, r *http.Request) {
	var request actionDefinitionRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	created, err := h.actions.Create(r.Context(), action.CreateInput{
		UserID: currentUserID(r.Context()), ProjectID: r.PathValue("projectID"), Definition: request.definition(),
	})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusCreated, newActionResponse(created))
}

func (h actionHandler) replace(w http.ResponseWriter, r *http.Request) {
	var request actionDefinitionRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	replaced, err := h.actions.Replace(r.Context(), action.ReplaceInput{Ref: h.ref(r),
		Definition: request.definition()})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, newActionResponse(replaced))
}

func (h actionHandler) remove(w http.ResponseWriter, r *http.Request) {
	if err := h.actions.Delete(r.Context(), h.ref(r)); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

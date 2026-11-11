package http

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/mobqa/backend/internal/usecase/environment"
)

type Environments interface {
	List(ctx context.Context, userID, projectID string) ([]environment.Environment, error)
	Create(ctx context.Context, in environment.CreateInput) (environment.Environment, error)
	Update(ctx context.Context, in environment.UpdateInput) (environment.Environment, error)
	Delete(ctx context.Context, userID, projectID, environmentID string) error
}

type variableBody struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Label string `json:"label,omitempty"`
	Rule  string `json:"rule,omitempty"`
	Value string `json:"value"`
}

type createEnvironmentRequest struct {
	Name      string         `json:"name"`
	IsDefault bool           `json:"is_default"`
	Variables []variableBody `json:"variables"`
}

type updateEnvironmentRequest struct {
	Name      *string        `json:"name"`
	IsDefault *bool          `json:"is_default"`
	Position  *int           `json:"position"`
	Variables []variableBody `json:"variables"`
}

type environmentResponse struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	IsDefault bool           `json:"is_default"`
	Position  int            `json:"position"`
	Variables []variableBody `json:"variables"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

// variables keeps nil apart from empty: an update without "variables" leaves them, [] clears them.
func variables(bodies []variableBody) []environment.Variable {
	if bodies == nil {
		return nil
	}
	result := make([]environment.Variable, len(bodies))
	for i, b := range bodies {
		result[i] = environment.Variable{
			Name: b.Name, Type: environment.VariableType(b.Type), Label: b.Label, Rule: b.Rule, Value: b.Value,
		}
	}
	return result
}

func newEnvironmentResponse(e environment.Environment) environmentResponse {
	bodies := make([]variableBody, len(e.Variables))
	for i, v := range e.Variables {
		bodies[i] = variableBody{Name: v.Name, Type: string(v.Type), Label: v.Label, Rule: v.Rule, Value: v.Value}
	}
	return environmentResponse{
		ID:        e.ID,
		Name:      e.Name,
		IsDefault: e.IsDefault,
		Position:  e.Position,
		Variables: bodies,
		CreatedAt: e.CreatedAt,
		UpdatedAt: e.UpdatedAt,
	}
}

type environmentHandler struct {
	environments Environments
	logger       *slog.Logger
}

func (h environmentHandler) list(w http.ResponseWriter, r *http.Request) {
	found, err := h.environments.List(r.Context(), currentUserID(r.Context()), r.PathValue("projectID"))
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	items := make([]environmentResponse, len(found))
	for i, item := range found {
		items[i] = newEnvironmentResponse(item)
	}
	writeJSON(w, http.StatusOK, items)
}

func (h environmentHandler) create(w http.ResponseWriter, r *http.Request) {
	var request createEnvironmentRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	created, err := h.environments.Create(r.Context(), environment.CreateInput{
		UserID:    currentUserID(r.Context()),
		ProjectID: r.PathValue("projectID"),
		Name:      request.Name,
		IsDefault: request.IsDefault,
		Variables: variables(request.Variables),
	})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusCreated, newEnvironmentResponse(created))
}

func (h environmentHandler) update(w http.ResponseWriter, r *http.Request) {
	var request updateEnvironmentRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	updated, err := h.environments.Update(r.Context(), environment.UpdateInput{
		UserID: currentUserID(r.Context()),
		Change: environment.Change{
			ProjectID:     r.PathValue("projectID"),
			EnvironmentID: r.PathValue("environmentID"),
			Name:          request.Name,
			IsDefault:     request.IsDefault,
			Position:      request.Position,
			Variables:     variables(request.Variables),
		},
	})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, newEnvironmentResponse(updated))
}

func (h environmentHandler) remove(w http.ResponseWriter, r *http.Request) {
	err := h.environments.Delete(r.Context(), currentUserID(r.Context()), r.PathValue("projectID"),
		r.PathValue("environmentID"))
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

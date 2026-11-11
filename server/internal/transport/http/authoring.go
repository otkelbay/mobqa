package http

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/mobqa/backend/internal/usecase/authoring"
)

type Authorings interface {
	Start(ctx context.Context, in authoring.StartInput) (authoring.Authoring, error)
	List(ctx context.Context, ref authoring.TestRef) ([]authoring.Authoring, error)
	Get(ctx context.Context, ref authoring.Ref) (authoring.Authoring, error)
	Cancel(ctx context.Context, ref authoring.Ref) (authoring.Authoring, error)
}

type startAuthoringRequest struct {
	DeviceID      string `json:"device_id"`
	EnvironmentID string `json:"environment_id"`
}

type selectorResponse struct {
	ID    string `json:"id,omitempty"`
	Text  string `json:"text,omitempty"`
	Label string `json:"label,omitempty"`
	Index int    `json:"index,omitempty"`
}

type opResponse struct {
	Step      int               `json:"step"`
	Kind      string            `json:"kind"`
	Selector  *selectorResponse `json:"selector,omitempty"`
	Direction string            `json:"direction,omitempty"`
	Text      string            `json:"text,omitempty"`
}

type turnResponse struct {
	Step    int    `json:"step"`
	Action  string `json:"action"`
	Element string `json:"element,omitempty"`
	Text    string `json:"text,omitempty"`
	Thought string `json:"thought,omitempty"`
	Error   string `json:"error,omitempty"`
}

type authoringResponse struct {
	ID              string     `json:"id"`
	TestID          string     `json:"test_id"`
	TestVersion     int        `json:"test_version"`
	Platform        string     `json:"platform"`
	DeviceID        string     `json:"device_id"`
	DeviceName      string     `json:"device_name"`
	EnvironmentID   string     `json:"environment_id,omitempty"`
	EnvironmentName string     `json:"environment_name,omitempty"`
	Status          string     `json:"status"`
	Failure         string     `json:"failure,omitempty"`
	CreatedBy       string     `json:"created_by"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	ReviewedAt      *time.Time `json:"reviewed_at,omitempty"`
}

type authoringDetailResponse struct {
	authoringResponse
	Script []opResponse   `json:"script"`
	Log    []turnResponse `json:"log"`
}

func newAuthoringResponse(a authoring.Authoring) authoringResponse {
	return authoringResponse{
		ID:              a.ID,
		TestID:          a.TestID,
		TestVersion:     a.TestVersion,
		Platform:        string(a.Platform),
		DeviceID:        a.DeviceID,
		DeviceName:      a.DeviceName,
		EnvironmentID:   a.EnvironmentID,
		EnvironmentName: a.EnvironmentName,
		Status:          string(a.Status),
		Failure:         a.Failure,
		CreatedBy:       a.CreatedBy,
		CreatedAt:       a.CreatedAt,
		StartedAt:       a.StartedAt,
		FinishedAt:      a.FinishedAt,
		ReviewedAt:      a.ReviewedAt,
	}
}

func newAuthoringDetailResponse(a authoring.Authoring) authoringDetailResponse {
	response := authoringDetailResponse{
		authoringResponse: newAuthoringResponse(a),
		Script:            make([]opResponse, len(a.Script)),
		Log:               make([]turnResponse, len(a.Log)),
	}
	for i, op := range a.Script {
		response.Script[i] = opResponse{Step: op.Step, Kind: string(op.Kind), Direction: string(op.Direction), Text: op.Text}
		if s := op.Selector; s != nil {
			response.Script[i].Selector = &selectorResponse{ID: s.ID, Text: s.Text, Label: s.Label, Index: s.Index}
		}
	}
	for i, t := range a.Log {
		response.Log[i] = turnResponse{
			Step: t.Step, Action: t.Action, Element: t.Element, Text: t.Text, Thought: t.Thought, Error: t.Error,
		}
	}
	return response
}

type authoringHandler struct {
	authorings Authorings
	logger     *slog.Logger
}

func (h authoringHandler) ref(r *http.Request) authoring.Ref {
	return authoring.Ref{
		UserID: currentUserID(r.Context()), ProjectID: r.PathValue("projectID"), AuthoringID: r.PathValue("authoringID"),
	}
}

// start answers 202: the agent works on the device in the background and its progress is read back through get.
func (h authoringHandler) start(w http.ResponseWriter, r *http.Request) {
	var request startAuthoringRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	started, err := h.authorings.Start(r.Context(), authoring.StartInput{
		UserID:        currentUserID(r.Context()),
		ProjectID:     r.PathValue("projectID"),
		TestID:        r.PathValue("testID"),
		DeviceID:      request.DeviceID,
		EnvironmentID: request.EnvironmentID,
	})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusAccepted, newAuthoringResponse(started))
}

func (h authoringHandler) list(w http.ResponseWriter, r *http.Request) {
	found, err := h.authorings.List(r.Context(), authoring.TestRef{
		UserID: currentUserID(r.Context()), ProjectID: r.PathValue("projectID"), TestID: r.PathValue("testID"),
	})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	response := make([]authoringResponse, len(found))
	for i, a := range found {
		response[i] = newAuthoringResponse(a)
	}
	writeJSON(w, http.StatusOK, response)
}

func (h authoringHandler) get(w http.ResponseWriter, r *http.Request) {
	found, err := h.authorings.Get(r.Context(), h.ref(r))
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, newAuthoringDetailResponse(found))
}

func (h authoringHandler) cancel(w http.ResponseWriter, r *http.Request) {
	cancelled, err := h.authorings.Cancel(r.Context(), h.ref(r))
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, newAuthoringResponse(cancelled))
}

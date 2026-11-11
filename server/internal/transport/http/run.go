package http

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/build"
	"github.com/mobqa/backend/internal/usecase/run"
)

type Runs interface {
	Create(ctx context.Context, in run.CreateInput) (run.Run, error)
	List(ctx context.Context, in run.ListInput) (run.Page, error)
	Get(ctx context.Context, ref run.Ref) (run.Run, []run.Result, error)
	Attempts(ctx context.Context, ref run.Ref, resultID string) ([]run.Attempt, error)
	Cancel(ctx context.Context, ref run.Ref) (run.Run, error)
}

type Videos interface {
	Save(ctx context.Context, in run.SaveVideoInput) error
	Open(ctx context.Context, ref run.VideoRef) (build.File, error)
}

type createRunRequest struct {
	TestIDs       []string `json:"test_ids"`
	DeviceIDs     []string `json:"device_ids"`
	BuildID       string   `json:"build_id"`
	EnvironmentID string   `json:"environment_id"`
	Retries       int      `json:"retries"`
}

type summaryResponse struct {
	Total     int `json:"total"`
	Queued    int `json:"queued"`
	Running   int `json:"running"`
	Passed    int `json:"passed"`
	Flaky     int `json:"flaky"`
	Failed    int `json:"failed"`
	Errored   int `json:"errored"`
	Cancelled int `json:"cancelled"`
}

type runResponse struct {
	ID              string          `json:"id"`
	Platform        string          `json:"platform"`
	BuildID         string          `json:"build_id,omitempty"`
	BuildLabel      string          `json:"build_label,omitempty"`
	EnvironmentID   string          `json:"environment_id,omitempty"`
	EnvironmentName string          `json:"environment_name,omitempty"`
	Status          string          `json:"status"`
	Retries         int             `json:"retries"`
	Summary         summaryResponse `json:"summary"`
	CreatedBy       string          `json:"created_by"`
	CreatedAt       time.Time       `json:"created_at"`
	StartedAt       *time.Time      `json:"started_at,omitempty"`
	FinishedAt      *time.Time      `json:"finished_at,omitempty"`
}

type resultResponse struct {
	ID          string     `json:"id"`
	TestID      string     `json:"test_id,omitempty"`
	TestVersion int        `json:"test_version"`
	Title       string     `json:"title"`
	DeviceID    string     `json:"device_id"`
	DeviceName  string     `json:"device_name"`
	Status      string     `json:"status"`
	Attempts    int        `json:"attempts"`
	FailedStep  int        `json:"failed_step,omitempty"`
	Error       string     `json:"error,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

type runDetailResponse struct {
	runResponse
	Results []resultResponse `json:"results"`
}

type runPageResponse struct {
	Items   []runResponse `json:"items"`
	Total   int           `json:"total"`
	Page    int           `json:"page"`
	Limit   int           `json:"limit"`
	HasMore bool          `json:"has_more"`
}

type stepOutcomeResponse struct {
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

type attemptResponse struct {
	Number     int                   `json:"number"`
	Status     string                `json:"status"`
	FailedStep int                   `json:"failed_step,omitempty"`
	Error      string                `json:"error,omitempty"`
	Steps      []stepOutcomeResponse `json:"steps"`
	Video      *videoResponse        `json:"video,omitempty"`
	StartedAt  time.Time             `json:"started_at"`
	FinishedAt time.Time             `json:"finished_at"`
}

type videoResponse struct {
	DurationMS int64  `json:"duration_ms,omitempty"`
	Problem    string `json:"problem,omitempty"`
}

func newRunResponse(r run.Run) runResponse {
	s := r.Summary
	return runResponse{
		ID:              r.ID,
		Platform:        string(r.Platform),
		BuildID:         r.BuildID,
		BuildLabel:      r.BuildLabel,
		EnvironmentID:   r.EnvironmentID,
		EnvironmentName: r.EnvironmentName,
		Status:          string(r.Status),
		Retries:         r.Retries,
		Summary: summaryResponse{
			Total: s.Total, Queued: s.Queued, Running: s.Running, Passed: s.Passed, Flaky: s.Flaky, Failed: s.Failed,
			Errored: s.Errored, Cancelled: s.Cancelled,
		},
		CreatedBy:  r.CreatedBy,
		CreatedAt:  r.CreatedAt,
		StartedAt:  r.StartedAt,
		FinishedAt: r.FinishedAt,
	}
}

func newResultResponse(r run.Result) resultResponse {
	return resultResponse{
		ID:          r.ID,
		TestID:      r.TestID,
		TestVersion: r.TestVersion,
		Title:       r.Title,
		DeviceID:    r.DeviceID,
		DeviceName:  r.DeviceName,
		Status:      string(r.Status),
		Attempts:    r.Attempts,
		FailedStep:  r.FailedStep,
		Error:       r.Error,
		StartedAt:   r.StartedAt,
		FinishedAt:  r.FinishedAt,
	}
}

func newAttemptResponse(a run.Attempt) attemptResponse {
	steps := make([]stepOutcomeResponse, len(a.Steps))
	for i, step := range a.Steps {
		steps[i] = stepOutcomeResponse{
			Status: string(step.Status), Error: step.Error, DurationMS: step.Duration.Milliseconds(),
		}
	}
	response := attemptResponse{
		Number: a.Number, Status: string(a.Status), FailedStep: a.FailedStep, Error: a.Error, Steps: steps,
		StartedAt: a.StartedAt, FinishedAt: a.FinishedAt,
	}
	if a.Video != nil {
		response.Video = &videoResponse{DurationMS: a.Video.Duration.Milliseconds(), Problem: a.Video.Problem}
	}
	return response
}

type runHandler struct {
	runs   Runs
	videos Videos
	hubs   HubTokens
	logger *slog.Logger
}

func (h runHandler) ref(r *http.Request) run.Ref {
	return run.Ref{UserID: currentUserID(r.Context()), ProjectID: r.PathValue("projectID"), RunID: r.PathValue("runID")}
}

// create answers 202: the run is queued and its progress is read back through get.
func (h runHandler) create(w http.ResponseWriter, r *http.Request) {
	var request createRunRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	created, err := h.runs.Create(r.Context(), run.CreateInput{
		UserID:        currentUserID(r.Context()),
		ProjectID:     r.PathValue("projectID"),
		TestIDs:       request.TestIDs,
		DeviceIDs:     request.DeviceIDs,
		BuildID:       request.BuildID,
		EnvironmentID: request.EnvironmentID,
		Retries:       request.Retries,
	})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusAccepted, newRunResponse(created))
}

func (h runHandler) list(w http.ResponseWriter, r *http.Request) {
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
	found, err := h.runs.List(r.Context(), run.ListInput{
		UserID: currentUserID(r.Context()), ProjectID: r.PathValue("projectID"),
		Status: run.Status(r.URL.Query().Get("status")), Page: page, Limit: limit,
	})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	items := make([]runResponse, len(found.Items))
	for i, item := range found.Items {
		items[i] = newRunResponse(item)
	}
	writeJSON(w, http.StatusOK, runPageResponse{
		Items: items, Total: found.Total, Page: found.Page, Limit: found.Limit, HasMore: found.HasMore(),
	})
}

func (h runHandler) get(w http.ResponseWriter, r *http.Request) {
	found, results, err := h.runs.Get(r.Context(), h.ref(r))
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	response := runDetailResponse{runResponse: newRunResponse(found), Results: make([]resultResponse, len(results))}
	for i, result := range results {
		response.Results[i] = newResultResponse(result)
	}
	writeJSON(w, http.StatusOK, response)
}

func (h runHandler) attempts(w http.ResponseWriter, r *http.Request) {
	found, err := h.runs.Attempts(r.Context(), h.ref(r), r.PathValue("resultID"))
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	response := make([]attemptResponse, len(found))
	for i, attempt := range found {
		response[i] = newAttemptResponse(attempt)
	}
	writeJSON(w, http.StatusOK, response)
}

func (h runHandler) cancel(w http.ResponseWriter, r *http.Request) {
	cancelled, err := h.runs.Cancel(r.Context(), h.ref(r))
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, newRunResponse(cancelled))
}

// saveVideo takes the video a hub filmed of the attempt it is finishing.
func (h runHandler) saveVideo(w http.ResponseWriter, r *http.Request) {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	caller, err := h.hubs.Authenticate(r.Context(), token)
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	number, err := attemptNumber(r)
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	err = h.videos.Save(r.Context(), run.SaveVideoInput{
		HubID: caller.ID, ResultID: r.PathValue("resultID"), Number: number, Content: r.Body,
	})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// video serves an attempt's video with range support, so a player can seek without downloading it all.
func (h runHandler) video(w http.ResponseWriter, r *http.Request) {
	number, err := attemptNumber(r)
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	file, err := h.videos.Open(r.Context(), run.VideoRef{Ref: h.ref(r), ResultID: r.PathValue("resultID"), Number: number})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	defer func() { _ = file.Close() }()
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeContent(w, r, "", time.Time{}, file)
}

func attemptNumber(r *http.Request) (int, error) {
	number, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		return 0, fault.WithDetail(fault.ErrNotFound, "attempt not found")
	}
	return number, nil
}

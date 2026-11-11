package http

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/chat"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/test"
)

type Tests interface {
	Create(ctx context.Context, in test.CreateInput) (test.Test, error)
	List(ctx context.Context, in test.ListInput) (test.Page, error)
	Get(ctx context.Context, ref test.Ref) (test.Test, error)
	Update(ctx context.Context, in test.UpdateInput) (test.Test, error)
	Delete(ctx context.Context, ref test.Ref) error
	Duplicate(ctx context.Context, ref test.Ref) (test.Test, error)
	Versions(ctx context.Context, ref test.Ref) ([]test.Version, error)
	Version(ctx context.Context, ref test.Ref, number int) (test.Version, error)
	Restore(ctx context.Context, ref test.Ref, number int) (test.Test, error)
	Generate(ctx context.Context, in test.GenerateInput) (test.Generation, error)
	Generation(ctx context.Context, ref test.GenerationRef) (test.Generation, error)
}

type Editor interface {
	Propose(ctx context.Context, in test.EditInput) (test.Proposal, error)
}

type stepBody struct {
	Action         string `json:"action"`
	Target         string `json:"target"`
	Description    string `json:"description"`
	ExpectedResult string `json:"expected_result"`
}

type createTestRequest struct {
	Platform    string     `json:"platform"`
	Priority    string     `json:"priority"`
	Tags        []string   `json:"tags"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Steps       []stepBody `json:"steps"`
}

type updateTestRequest struct {
	BaseVersion int        `json:"base_version"`
	Title       *string    `json:"title"`
	Description *string    `json:"description"`
	Steps       []stepBody `json:"steps"`
	Priority    *string    `json:"priority"`
	Tags        []string   `json:"tags"`
	Note        string     `json:"note"`
}

type testSummaryResponse struct {
	ID          string    `json:"id"`
	Platform    string    `json:"platform"`
	Priority    string    `json:"priority"`
	Tags        []string  `json:"tags"`
	Source      string    `json:"source"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Version     int       `json:"version"`
	StepCount   int       `json:"step_count"`
	CreatedBy   string    `json:"created_by"`
	UpdatedBy   string    `json:"updated_by"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type testResponse struct {
	testSummaryResponse
	Steps []stepBody `json:"steps"`
}

type testPageResponse struct {
	Items   []testSummaryResponse `json:"items"`
	Total   int                   `json:"total"`
	Page    int                   `json:"page"`
	Limit   int                   `json:"limit"`
	HasMore bool                  `json:"has_more"`
}

type versionSummaryResponse struct {
	Version     int       `json:"version"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	StepCount   int       `json:"step_count"`
	Source      string    `json:"source"`
	Note        string    `json:"note"`
	CreatedBy   string    `json:"created_by"`
	CreatedAt   time.Time `json:"created_at"`
}

type versionResponse struct {
	versionSummaryResponse
	Steps []stepBody `json:"steps"`
}

type generateTestsRequest struct {
	Platform string `json:"platform"`
	Brief    string `json:"brief"`
}

type generationResponse struct {
	ID         string     `json:"id"`
	Platform   string     `json:"platform"`
	Brief      string     `json:"brief"`
	Status     string     `json:"status"`
	TestIDs    []string   `json:"test_ids"`
	Failure    string     `json:"failure,omitempty"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

type turnBody struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type agentEditRequest struct {
	Instruction string     `json:"instruction"`
	History     []turnBody `json:"history"`
}

type proposalResponse struct {
	Reply       string     `json:"reply"`
	Changed     bool       `json:"changed"`
	Steps       []stepBody `json:"steps"`
	BaseVersion int        `json:"base_version"`
}

func newGenerationResponse(g test.Generation) generationResponse {
	return generationResponse{
		ID:         g.ID,
		Platform:   string(g.Platform),
		Brief:      g.Brief,
		Status:     string(g.Status),
		TestIDs:    g.TestIDs,
		Failure:    g.Failure,
		CreatedBy:  g.CreatedBy,
		CreatedAt:  g.CreatedAt,
		FinishedAt: g.FinishedAt,
	}
}

// steps keeps nil apart from empty: an update without "steps" leaves them, [] removes them all.
func steps(bodies []stepBody) []test.Step {
	if bodies == nil {
		return nil
	}
	result := make([]test.Step, len(bodies))
	for i, b := range bodies {
		result[i] = test.Step{
			Action: test.Action(b.Action), Target: b.Target, Description: b.Description, ExpectedResult: b.ExpectedResult,
		}
	}
	return result
}

func stepBodies(steps []test.Step) []stepBody {
	bodies := make([]stepBody, len(steps))
	for i, s := range steps {
		bodies[i] = stepBody{
			Action: string(s.Action), Target: s.Target, Description: s.Description, ExpectedResult: s.ExpectedResult,
		}
	}
	return bodies
}

func newTestSummaryResponse(t test.Test) testSummaryResponse {
	return testSummaryResponse{
		ID:          t.ID,
		Platform:    string(t.Platform),
		Priority:    string(t.Priority),
		Tags:        t.Tags,
		Source:      string(t.Source),
		Title:       t.Content.Title,
		Description: t.Content.Description,
		Version:     t.Version,
		StepCount:   t.StepCount,
		CreatedBy:   t.CreatedBy,
		UpdatedBy:   t.UpdatedBy,
		CreatedAt:   t.CreatedAt,
		UpdatedAt:   t.UpdatedAt,
	}
}

func newTestResponse(t test.Test) testResponse {
	return testResponse{testSummaryResponse: newTestSummaryResponse(t), Steps: stepBodies(t.Content.Steps)}
}

func newVersionSummaryResponse(v test.Version) versionSummaryResponse {
	return versionSummaryResponse{
		Version:     v.Number,
		Title:       v.Content.Title,
		Description: v.Content.Description,
		StepCount:   v.StepCount,
		Source:      string(v.Source),
		Note:        v.Note,
		CreatedBy:   v.CreatedBy,
		CreatedAt:   v.CreatedAt,
	}
}

type testHandler struct {
	tests  Tests
	editor Editor
	logger *slog.Logger
}

func (h testHandler) ref(r *http.Request) test.Ref {
	return test.Ref{
		UserID: currentUserID(r.Context()), ProjectID: r.PathValue("projectID"), TestID: r.PathValue("testID"),
	}
}

func versionNumber(r *http.Request) (int, error) {
	number, err := strconv.Atoi(r.PathValue("version"))
	if err != nil {
		return 0, fault.WithDetail(fault.ErrNotFound, "version not found")
	}
	return number, nil
}

func (h testHandler) create(w http.ResponseWriter, r *http.Request) {
	var request createTestRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	created, err := h.tests.Create(r.Context(), test.CreateInput{
		UserID:      currentUserID(r.Context()),
		ProjectID:   r.PathValue("projectID"),
		Platform:    project.Platform(request.Platform),
		Priority:    test.Priority(request.Priority),
		Tags:        request.Tags,
		Title:       request.Title,
		Description: request.Description,
		Steps:       steps(request.Steps),
	})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusCreated, newTestResponse(created))
}

func (h testHandler) list(w http.ResponseWriter, r *http.Request) {
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
	query := r.URL.Query()
	found, err := h.tests.List(r.Context(), test.ListInput{
		UserID: currentUserID(r.Context()), ProjectID: r.PathValue("projectID"), Search: query.Get("search"),
		Tag: query.Get("tag"), Priority: test.Priority(query.Get("priority")),
		Platform: project.Platform(query.Get("platform")), Page: page, Limit: limit,
	})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	items := make([]testSummaryResponse, len(found.Items))
	for i, item := range found.Items {
		items[i] = newTestSummaryResponse(item)
	}
	writeJSON(w, http.StatusOK, testPageResponse{
		Items: items, Total: found.Total, Page: found.Page, Limit: found.Limit, HasMore: found.HasMore(),
	})
}

func (h testHandler) get(w http.ResponseWriter, r *http.Request) {
	found, err := h.tests.Get(r.Context(), h.ref(r))
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, newTestResponse(found))
}

func (h testHandler) update(w http.ResponseWriter, r *http.Request) {
	var request updateTestRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	updated, err := h.tests.Update(r.Context(), test.UpdateInput{
		Ref:         h.ref(r),
		BaseVersion: request.BaseVersion,
		Title:       request.Title,
		Description: request.Description,
		Steps:       steps(request.Steps),
		Priority:    (*test.Priority)(request.Priority),
		Tags:        request.Tags,
		Note:        request.Note,
	})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, newTestResponse(updated))
}

func (h testHandler) remove(w http.ResponseWriter, r *http.Request) {
	if err := h.tests.Delete(r.Context(), h.ref(r)); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h testHandler) duplicate(w http.ResponseWriter, r *http.Request) {
	created, err := h.tests.Duplicate(r.Context(), h.ref(r))
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusCreated, newTestResponse(created))
}

func (h testHandler) versions(w http.ResponseWriter, r *http.Request) {
	found, err := h.tests.Versions(r.Context(), h.ref(r))
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	items := make([]versionSummaryResponse, len(found))
	for i, item := range found {
		items[i] = newVersionSummaryResponse(item)
	}
	writeJSON(w, http.StatusOK, items)
}

func (h testHandler) version(w http.ResponseWriter, r *http.Request) {
	number, err := versionNumber(r)
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	found, err := h.tests.Version(r.Context(), h.ref(r), number)
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, versionResponse{
		versionSummaryResponse: newVersionSummaryResponse(found), Steps: stepBodies(found.Content.Steps),
	})
}

func (h testHandler) restore(w http.ResponseWriter, r *http.Request) {
	number, err := versionNumber(r)
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	restored, err := h.tests.Restore(r.Context(), h.ref(r), number)
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, newTestResponse(restored))
}

// generate answers 202: the tests are drafted in the background and read back through generation.
func (h testHandler) generate(w http.ResponseWriter, r *http.Request) {
	var request generateTestsRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	created, err := h.tests.Generate(r.Context(), test.GenerateInput{
		UserID:    currentUserID(r.Context()),
		ProjectID: r.PathValue("projectID"),
		Platform:  project.Platform(request.Platform),
		Brief:     request.Brief,
	})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusAccepted, newGenerationResponse(created))
}

func (h testHandler) generation(w http.ResponseWriter, r *http.Request) {
	found, err := h.tests.Generation(r.Context(), test.GenerationRef{
		UserID: currentUserID(r.Context()), ProjectID: r.PathValue("projectID"),
		GenerationID: r.PathValue("generationID"),
	})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, newGenerationResponse(found))
}

// agentEdit only proposes: the client shows the steps and saves them through update with base_version.
func (h testHandler) agentEdit(w http.ResponseWriter, r *http.Request) {
	var request agentEditRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	history := make([]test.Turn, len(request.History))
	for i, turn := range request.History {
		history[i] = test.Turn{Role: chat.Role(turn.Role), Text: turn.Content}
	}
	proposal, err := h.editor.Propose(r.Context(), test.EditInput{
		Ref: h.ref(r), Instruction: request.Instruction, History: history,
	})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, proposalResponse{
		Reply: proposal.Reply, Changed: proposal.Changed, Steps: stepBodies(proposal.Steps),
		BaseVersion: proposal.BaseVersion,
	})
}

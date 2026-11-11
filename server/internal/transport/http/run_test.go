package http

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/build"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/run"
)

type fakeRuns struct {
	Runs
	created  []run.CreateInput
	listed   []run.ListInput
	attempts []string
}

func queuedRun() run.Run {
	return run.Run{
		ID: "run-1", Platform: project.PlatformAndroid, BuildID: "apk", BuildLabel: "1.2 (40)", Status: run.StatusQueued,
		Summary: run.Summary{Total: 2, Queued: 2}, CreatedBy: "user-1", CreatedAt: shopTime(),
	}
}

const queuedRunJSON = `"id":"run-1","platform":"android","build_id":"apk","build_label":"1.2 (40)","status":"queued",` +
	`"retries":0,"summary":{"total":2,"queued":2,"running":0,"passed":0,"flaky":0,"failed":0,"errored":0,` +
	`"cancelled":0},"created_by":"user-1","created_at":"2026-11-11T09:00:00Z"`

func (f *fakeRuns) Create(_ context.Context, in run.CreateInput) (run.Run, error) {
	f.created = append(f.created, in)
	return queuedRun(), nil
}

func (f *fakeRuns) List(_ context.Context, in run.ListInput) (run.Page, error) {
	f.listed = append(f.listed, in)
	return run.Page{Items: []run.Run{queuedRun()}, Total: 1, Page: 1, Limit: 20}, nil
}

func (f *fakeRuns) Get(_ context.Context, ref run.Ref) (run.Run, []run.Result, error) {
	started := shopTime().Add(time.Second)
	return queuedRun(), []run.Result{{
		ID: "result-1", TestID: "test-1", TestVersion: 3, Title: "Sign in", DeviceID: "pixel", DeviceName: "Pixel",
		Status: run.ResultFailed, Attempts: 2, FailedStep: 2, Error: "Home is not shown", StartedAt: &started,
	}}, nil
}

func (f *fakeRuns) Attempts(_ context.Context, _ run.Ref, resultID string) ([]run.Attempt, error) {
	f.attempts = append(f.attempts, resultID)
	return []run.Attempt{{
		Number: 1, Status: run.AttemptFailed, FailedStep: 1, Error: "no button",
		Steps: []run.StepOutcome{{Status: run.StepFailed, Error: "no button", Duration: 1500 * time.Millisecond}},
		Video: &run.Video{Duration: 2 * time.Second}, StartedAt: shopTime(), FinishedAt: shopTime().Add(2 * time.Second),
	}}, nil
}

func (f *fakeRuns) Cancel(context.Context, run.Ref) (run.Run, error) {
	cancelled := queuedRun()
	cancelled.Status = run.StatusCancelled
	return cancelled, nil
}

func TestCreateRunIsAccepted(t *testing.T) {
	runs := &fakeRuns{}

	recorder := serveAuthorized(t, Dependencies{Runs: runs}, jsonRequest(http.MethodPost, "/projects/project-1/runs",
		`{"test_ids":["test-1","test-2"],"device_ids":["pixel"],"environment_id":"staging","retries":1}`))

	requireResponse(t, recorder, http.StatusAccepted, `{`+queuedRunJSON+`}`)
	in := runs.created[0]
	if in.UserID != "user-1" || in.ProjectID != "project-1" || !slices.Equal(in.TestIDs, []string{"test-1", "test-2"}) ||
		!slices.Equal(in.DeviceIDs, []string{"pixel"}) || in.EnvironmentID != "staging" || in.Retries != 1 {
		t.Fatalf("create input = %+v", in)
	}
}

func TestListRunsPassesStatusAndPaging(t *testing.T) {
	runs := &fakeRuns{}

	recorder := serveAuthorized(t, Dependencies{Runs: runs},
		jsonRequest(http.MethodGet, "/projects/project-1/runs?status=queued&page=2&limit=5", ""))

	requireResponse(t, recorder, http.StatusOK, `{"items":[{`+queuedRunJSON+`}],"total":1,"page":1,"limit":20,`+
		`"has_more":false}`)
	want := run.ListInput{UserID: "user-1", ProjectID: "project-1", Status: run.StatusQueued, Page: 2, Limit: 5}
	if runs.listed[0] != want {
		t.Fatalf("list input = %+v", runs.listed[0])
	}
}

func TestGetRunIncludesResults(t *testing.T) {
	recorder := serveAuthorized(t, Dependencies{Runs: &fakeRuns{}},
		jsonRequest(http.MethodGet, "/projects/project-1/runs/run-1", ""))

	requireResponse(t, recorder, http.StatusOK, `{`+queuedRunJSON+`,"results":[{"id":"result-1","test_id":"test-1",`+
		`"test_version":3,"title":"Sign in","device_id":"pixel","device_name":"Pixel","status":"failed","attempts":2,`+
		`"failed_step":2,"error":"Home is not shown","started_at":"2026-11-11T09:00:01Z"}]}`)
}

func TestListResultAttempts(t *testing.T) {
	runs := &fakeRuns{}

	recorder := serveAuthorized(t, Dependencies{Runs: runs},
		jsonRequest(http.MethodGet, "/projects/project-1/runs/run-1/results/result-7/attempts", ""))

	requireResponse(t, recorder, http.StatusOK, `[{"number":1,"status":"failed","failed_step":1,"error":"no button",`+
		`"steps":[{"status":"failed","error":"no button","duration_ms":1500}],"video":{"duration_ms":2000},`+
		`"started_at":"2026-11-11T09:00:00Z","finished_at":"2026-11-11T09:00:02Z"}]`)
	if !slices.Equal(runs.attempts, []string{"result-7"}) {
		t.Fatalf("attempts asked for %v", runs.attempts)
	}
}

func TestCancelRun(t *testing.T) {
	recorder := serveAuthorized(t, Dependencies{Runs: &fakeRuns{}},
		jsonRequest(http.MethodPost, "/projects/project-1/runs/run-1/cancel", ""))

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"status":"cancelled"`) {
		t.Fatalf("cancel = %d %s", recorder.Code, recorder.Body.String())
	}
}

type fakeVideos struct {
	saved []run.SaveVideoInput
	body  string
}

func (f *fakeVideos) Save(_ context.Context, in run.SaveVideoInput) error {
	body, err := io.ReadAll(in.Content)
	f.saved, f.body = append(f.saved, in), string(body)
	return err
}

func (f *fakeVideos) Open(_ context.Context, ref run.VideoRef) (build.File, error) {
	if ref.RunID != "run-1" || ref.ResultID != "result-1" || ref.Number != 2 {
		return nil, fault.WithDetail(fault.ErrNotFound, "video not found")
	}
	return readSeekNopCloser{bytes.NewReader([]byte("ftyp mp4"))}, nil
}

func TestHubSendsTheVideoOfAnAttempt(t *testing.T) {
	videos := &fakeVideos{}
	deps := Dependencies{Videos: videos, HubTokens: fakeHubTokens{}}
	upload := func(token, path string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPut, path, strings.NewReader("ftyp"))
		request.Header.Set("Authorization", "Bearer "+token)
		return serve(t, deps, request)
	}

	if recorder := upload("mqh_lab", "/hub/videos/result-1/2"); recorder.Code != http.StatusNoContent {
		t.Fatalf("upload status = %d %s", recorder.Code, recorder.Body.String())
	}
	want := run.SaveVideoInput{HubID: "hub-1", ResultID: "result-1", Number: 2}
	if got := videos.saved[0]; got.HubID != want.HubID || got.ResultID != want.ResultID || got.Number != want.Number ||
		videos.body != "ftyp" {
		t.Fatalf("saved %+v with %q", got, videos.body)
	}
	if recorder := upload("mqh_other", "/hub/videos/result-1/2"); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unknown hub status = %d", recorder.Code)
	}
	if recorder := upload("mqh_lab", "/hub/videos/result-1/last"); recorder.Code != http.StatusNotFound {
		t.Fatalf("unnumbered attempt status = %d", recorder.Code)
	}
}

func TestPeopleWatchAttemptVideosInRanges(t *testing.T) {
	deps := Dependencies{Videos: &fakeVideos{}}
	request := jsonRequest(http.MethodGet, "/projects/project-1/runs/run-1/results/result-1/attempts/2/video", "")
	request.Header.Set("Range", "bytes=0-3")

	recorder := serveAuthorized(t, deps, request)

	requireResponse(t, recorder, http.StatusPartialContent, "ftyp")
	if recorder.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("Content-Type = %q", recorder.Header().Get("Content-Type"))
	}
	missing := serveAuthorized(t, deps,
		jsonRequest(http.MethodGet, "/projects/project-1/runs/run-1/results/result-1/attempts/3/video", ""))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing video status = %d", missing.Code)
	}
}

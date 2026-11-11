package http

import (
	"context"
	"net/http"
	"testing"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/authoring"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/ui"
)

type fakeAuthorings struct {
	Authorings
	started []authoring.StartInput
	listed  []authoring.TestRef
	refs    []authoring.Ref
}

func queuedAuthoring() authoring.Authoring {
	return authoring.Authoring{
		ID: "authoring-1", TestID: "test-1", TestVersion: 3, Platform: project.PlatformAndroid, DeviceID: "pixel",
		DeviceName: "Pixel", Status: authoring.StatusQueued, CreatedBy: "user-1", CreatedAt: shopTime(),
	}
}

const queuedAuthoringJSON = `"id":"authoring-1","test_id":"test-1","test_version":3,"platform":"android",` +
	`"device_id":"pixel","device_name":"Pixel","status":"queued","created_by":"user-1",` +
	`"created_at":"2026-11-11T09:00:00Z"`

func (f *fakeAuthorings) Start(_ context.Context, in authoring.StartInput) (authoring.Authoring, error) {
	f.started = append(f.started, in)
	return queuedAuthoring(), nil
}

func (f *fakeAuthorings) List(_ context.Context, ref authoring.TestRef) ([]authoring.Authoring, error) {
	f.listed = append(f.listed, ref)
	return []authoring.Authoring{queuedAuthoring()}, nil
}

func (f *fakeAuthorings) Get(_ context.Context, ref authoring.Ref) (authoring.Authoring, error) {
	f.refs = append(f.refs, ref)
	if ref.AuthoringID != "authoring-1" {
		return authoring.Authoring{}, fault.WithDetail(fault.ErrNotFound, "authoring not found")
	}
	found := queuedAuthoring()
	found.Status, found.Failure = authoring.StatusFailed, "step 2: no field"
	found.Script = []ui.Op{
		{Step: 1, Kind: ui.KindTap, Selector: &ui.Selector{ID: "app:id/sign_in"}},
		{Step: 2, Kind: ui.KindSwipe, Direction: ui.DirectionUp},
	}
	found.Log = []authoring.Turn{{Step: 2, Action: "fail", Text: "no field", Thought: "nothing to type into"}}
	return found, nil
}

func (f *fakeAuthorings) Cancel(_ context.Context, ref authoring.Ref) (authoring.Authoring, error) {
	f.refs = append(f.refs, ref)
	cancelled := queuedAuthoring()
	cancelled.Status = authoring.StatusCancelled
	return cancelled, nil
}

func TestStartAuthoringIsAccepted(t *testing.T) {
	authorings := &fakeAuthorings{}

	recorder := serveAuthorized(t, Dependencies{Authorings: authorings}, jsonRequest(http.MethodPost,
		"/projects/project-1/tests/test-1/authorings", `{"device_id":"pixel","environment_id":"staging"}`))

	requireResponse(t, recorder, http.StatusAccepted, `{`+queuedAuthoringJSON+`}`)
	in := authorings.started[0]
	if in.UserID != "user-1" || in.ProjectID != "project-1" || in.TestID != "test-1" || in.DeviceID != "pixel" ||
		in.EnvironmentID != "staging" {
		t.Fatalf("start input = %+v", in)
	}
}

func TestListAuthoringsOfATest(t *testing.T) {
	authorings := &fakeAuthorings{}

	recorder := serveAuthorized(t, Dependencies{Authorings: authorings}, jsonRequest(http.MethodGet,
		"/projects/project-1/tests/test-1/authorings", ""))

	requireResponse(t, recorder, http.StatusOK, `[{`+queuedAuthoringJSON+`}]`)
	if ref := authorings.listed[0]; ref.UserID != "user-1" || ref.ProjectID != "project-1" || ref.TestID != "test-1" {
		t.Fatalf("list ref = %+v", ref)
	}
}

func TestGetAuthoringShowsScriptAndLog(t *testing.T) {
	deps := Dependencies{Authorings: &fakeAuthorings{}}

	recorder := serveAuthorized(t, deps, jsonRequest(http.MethodGet, "/projects/project-1/authorings/authoring-1", ""))

	requireResponse(t, recorder, http.StatusOK, `{"id":"authoring-1","test_id":"test-1","test_version":3,`+
		`"platform":"android","device_id":"pixel","device_name":"Pixel","status":"failed",`+
		`"failure":"step 2: no field","created_by":"user-1","created_at":"2026-11-11T09:00:00Z",`+
		`"script":[{"step":1,"kind":"tap","selector":{"id":"app:id/sign_in"}},{"step":2,"kind":"swipe",`+
		`"direction":"up"}],"log":[{"step":2,"action":"fail","text":"no field","thought":"nothing to type into"}]}`)
	missing := serveAuthorized(t, deps, jsonRequest(http.MethodGet, "/projects/project-1/authorings/x", ""))
	requireResponse(t, missing, http.StatusNotFound, `{"error":"authoring not found"}`)
}

func TestCancelAuthoring(t *testing.T) {
	authorings := &fakeAuthorings{}

	recorder := serveAuthorized(t, Dependencies{Authorings: authorings}, jsonRequest(http.MethodPost,
		"/projects/project-1/authorings/authoring-1/cancel", ""))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body)
	}
	if ref := authorings.refs[0]; ref.UserID != "user-1" || ref.ProjectID != "project-1" ||
		ref.AuthoringID != "authoring-1" {
		t.Fatalf("cancel ref = %+v", ref)
	}
}

package http

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/chat"
	"github.com/mobqa/backend/internal/usecase/project"
	"github.com/mobqa/backend/internal/usecase/test"
)

type fakeTests struct {
	Tests
	created  []test.CreateInput
	listed   []test.ListInput
	updates  []test.UpdateInput
	restored []int
	requests []test.GenerateInput
}

func checkoutTest() test.Test {
	return test.Test{
		ID: "test-1", Platform: project.PlatformAndroid, Priority: test.PriorityHigh, Tags: []string{"smoke"},
		Source: test.SourceManual, Version: 2, StepCount: 1, CreatedBy: "user-1", UpdatedBy: "user-1",
		CreatedAt: shopTime(), UpdatedAt: shopTime(),
		Content: test.Content{Title: "Checkout", Steps: []test.Step{{Action: test.ActionTap, Target: "Pay"}}},
	}
}

func (f *fakeTests) Create(_ context.Context, in test.CreateInput) (test.Test, error) {
	f.created = append(f.created, in)
	return checkoutTest(), nil
}

func (f *fakeTests) List(_ context.Context, in test.ListInput) (test.Page, error) {
	f.listed = append(f.listed, in)
	return test.Page{Items: []test.Test{checkoutTest()}, Total: 3, Page: 1, Limit: 1}, nil
}

func (f *fakeTests) Update(_ context.Context, in test.UpdateInput) (test.Test, error) {
	f.updates = append(f.updates, in)
	return checkoutTest(), nil
}

func (f *fakeTests) Version(_ context.Context, _ test.Ref, number int) (test.Version, error) {
	return test.Version{Number: number, Content: test.Content{Title: "Pay", Steps: []test.Step{
		{Action: test.ActionAssert, Target: "Thank you"},
	}}, StepCount: 1, Source: test.ChangeRestore, Note: "restored version 1", CreatedAt: shopTime()}, nil
}

func (f *fakeTests) Restore(_ context.Context, _ test.Ref, number int) (test.Test, error) {
	f.restored = append(f.restored, number)
	return checkoutTest(), nil
}

const checkoutJSON = `"id":"test-1","platform":"android","priority":"high","tags":["smoke"],"source":"manual",` +
	`"title":"Checkout","description":"","version":2,"step_count":1,"created_by":"user-1","updated_by":"user-1",` +
	`"created_at":"2026-11-11T09:00:00Z","updated_at":"2026-11-11T09:00:00Z"`

func TestCreateTest(t *testing.T) {
	tests := &fakeTests{}

	recorder := serveAuthorized(t, Dependencies{Tests: tests}, jsonRequest(http.MethodPost, "/projects/project-1/tests",
		`{"platform":"android","title":"Checkout","tags":["smoke"],"steps":[{"action":"tap","target":"Pay"}]}`))

	requireResponse(t, recorder, http.StatusCreated, `{`+checkoutJSON+
		`,"steps":[{"action":"tap","target":"Pay","description":"","expected_result":""}]}`)
	in := tests.created[0]
	if in.UserID != "user-1" || in.ProjectID != "project-1" || in.Platform != project.PlatformAndroid ||
		in.Priority != "" || !slices.Equal(in.Steps, []test.Step{{Action: test.ActionTap, Target: "Pay"}}) {
		t.Fatalf("create input = %+v", in)
	}
}

func TestListTestsPassesFilters(t *testing.T) {
	tests := &fakeTests{}

	recorder := serveAuthorized(t, Dependencies{Tests: tests}, jsonRequest(http.MethodGet,
		"/projects/project-1/tests?search=pay&tag=smoke&priority=high&platform=ios&page=1&limit=1", ""))

	requireResponse(t, recorder, http.StatusOK,
		`{"items":[{`+checkoutJSON+`}],"total":3,"page":1,"limit":1,"has_more":true}`)
	want := test.ListInput{
		UserID: "user-1", ProjectID: "project-1", Search: "pay", Tag: "smoke", Priority: test.PriorityHigh,
		Platform: project.PlatformIOS, Page: 1, Limit: 1,
	}
	if tests.listed[0] != want {
		t.Fatalf("list input = %+v", tests.listed[0])
	}
}

func TestUpdateTestKeepsAbsentFieldsApartFromEmpty(t *testing.T) {
	tests := &fakeTests{}
	deps := Dependencies{Tests: tests}

	serveAuthorized(t, deps, jsonRequest(http.MethodPatch, "/projects/p/tests/test-1", `{"priority":"low"}`))
	serveAuthorized(t, deps, jsonRequest(http.MethodPatch, "/projects/p/tests/test-1",
		`{"base_version":2,"steps":[],"tags":[],"note":"clear"}`))

	metadata, content := tests.updates[0], tests.updates[1]
	if *metadata.Priority != test.PriorityLow || metadata.Steps != nil || metadata.Tags != nil || metadata.Title != nil {
		t.Fatalf("metadata update = %+v", metadata)
	}
	if content.BaseVersion != 2 || content.Steps == nil || len(content.Steps) != 0 || content.Tags == nil ||
		content.Note != "clear" || content.Ref.TestID != "test-1" {
		t.Fatalf("content update = %+v", content)
	}
}

func TestGetTestVersion(t *testing.T) {
	recorder := serveAuthorized(t, Dependencies{Tests: &fakeTests{}},
		jsonRequest(http.MethodGet, "/projects/p/tests/test-1/versions/3", ""))

	requireResponse(t, recorder, http.StatusOK,
		`{"version":3,"title":"Pay","description":"","step_count":1,"source":"restore","note":"restored version 1",`+
			`"created_by":"","created_at":"2026-11-11T09:00:00Z",`+
			`"steps":[{"action":"assert","target":"Thank you","description":"","expected_result":""}]}`)
}

func TestRestoreTestVersion(t *testing.T) {
	tests := &fakeTests{}

	restored := serveAuthorized(t, Dependencies{Tests: tests},
		jsonRequest(http.MethodPost, "/projects/p/tests/test-1/versions/1/restore", ""))
	malformed := serveAuthorized(t, Dependencies{Tests: tests},
		jsonRequest(http.MethodPost, "/projects/p/tests/test-1/versions/first/restore", ""))

	if restored.Code != http.StatusOK || !slices.Equal(tests.restored, []int{1}) {
		t.Fatalf("restore = %d, restored %v", restored.Code, tests.restored)
	}
	requireResponse(t, malformed, http.StatusNotFound, `{"error":"version not found"}`)
}

func (f *fakeTests) Generate(_ context.Context, in test.GenerateInput) (test.Generation, error) {
	f.requests = append(f.requests, in)
	return test.Generation{
		ID: "gen-1", Platform: in.Platform, Brief: in.Brief, Status: test.GenerationPending, TestIDs: []string{},
		CreatedBy: in.UserID, CreatedAt: shopTime(),
	}, nil
}

func (f *fakeTests) Generation(_ context.Context, ref test.GenerationRef) (test.Generation, error) {
	finished := shopTime().Add(time.Minute)
	return test.Generation{
		ID: ref.GenerationID, Platform: project.PlatformIOS, Brief: "Sign in", Status: test.GenerationFailed,
		TestIDs: []string{}, Failure: "the model returned no usable tests", CreatedBy: "user-1",
		CreatedAt: shopTime(), FinishedAt: &finished,
	}, nil
}

func TestGenerateTestsIsAccepted(t *testing.T) {
	tests := &fakeTests{}

	recorder := serveAuthorized(t, Dependencies{Tests: tests}, jsonRequest(http.MethodPost,
		"/projects/project-1/test-generations", `{"platform":"ios","brief":"Sign in"}`))

	requireResponse(t, recorder, http.StatusAccepted,
		`{"id":"gen-1","platform":"ios","brief":"Sign in","status":"pending","test_ids":[],"created_by":"user-1",`+
			`"created_at":"2026-11-11T09:00:00Z"}`)
	want := test.GenerateInput{UserID: "user-1", ProjectID: "project-1", Platform: project.PlatformIOS, Brief: "Sign in"}
	if tests.requests[0] != want {
		t.Fatalf("generate input = %+v", tests.requests[0])
	}
}

func TestGetGenerationShowsFailure(t *testing.T) {
	recorder := serveAuthorized(t, Dependencies{Tests: &fakeTests{}},
		jsonRequest(http.MethodGet, "/projects/project-1/test-generations/gen-7", ""))

	requireResponse(t, recorder, http.StatusOK,
		`{"id":"gen-7","platform":"ios","brief":"Sign in","status":"failed","test_ids":[],`+
			`"failure":"the model returned no usable tests","created_by":"user-1",`+
			`"created_at":"2026-11-11T09:00:00Z","finished_at":"2026-11-11T09:01:00Z"}`)
}

type fakeEditor struct {
	inputs []test.EditInput
	err    error
}

func (f *fakeEditor) Propose(_ context.Context, in test.EditInput) (test.Proposal, error) {
	f.inputs = append(f.inputs, in)
	if f.err != nil {
		return test.Proposal{}, f.err
	}
	return test.Proposal{Reply: "Added a check.", Changed: true, BaseVersion: 2, Steps: []test.Step{
		{Action: test.ActionAssert, Target: "Home", ExpectedResult: "Home opens"},
	}}, nil
}

func TestAgentEditProposesSteps(t *testing.T) {
	editor := &fakeEditor{}

	recorder := serveAuthorized(t, Dependencies{Editor: editor}, jsonRequest(http.MethodPost,
		"/projects/project-1/tests/test-1/agent-edit",
		`{"instruction":"Check home","history":[{"role":"user","content":"Hi"},{"role":"assistant","content":"Hello"}]}`))

	requireResponse(t, recorder, http.StatusOK, `{"reply":"Added a check.","changed":true,`+
		`"steps":[{"action":"assert","target":"Home","description":"","expected_result":"Home opens"}],"base_version":2}`)
	in := editor.inputs[0]
	want := []test.Turn{{Role: chat.RoleUser, Text: "Hi"}, {Role: chat.RoleAssistant, Text: "Hello"}}
	if in.Ref != (test.Ref{UserID: "user-1", ProjectID: "project-1", TestID: "test-1"}) ||
		in.Instruction != "Check home" || !slices.Equal(in.History, want) {
		t.Fatalf("edit input = %+v", in)
	}
}

func TestAgentEditReportsUnavailableAgent(t *testing.T) {
	editor := &fakeEditor{err: fault.WithDetail(fault.ErrUnavailable, "the agent is not configured")}

	recorder := serveAuthorized(t, Dependencies{Editor: editor}, jsonRequest(http.MethodPost,
		"/projects/project-1/tests/test-1/agent-edit", `{"instruction":"Check home"}`))

	requireResponse(t, recorder, http.StatusServiceUnavailable, `{"error":"the agent is not configured"}`)
}

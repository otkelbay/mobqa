package http

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/action"
)

type fakeActions struct {
	Actions
	created  []action.CreateInput
	replaced []action.ReplaceInput
	deleted  []action.Ref
}

func fetchOTPAction() action.Action {
	return action.Action{
		ID: "action-1", ProjectID: "project-1", Name: "Fetch OTP", Description: "Last code",
		Request: action.Request{
			Method: action.MethodGet, URL: "{{API_URL}}/otp", ExpectedStatus: 200,
			Headers: []action.Header{{Name: "Authorization", Value: "Bearer {{TOKEN}}"}},
			Extract: []action.Extraction{{Variable: "OTP", Pattern: `"code":"(\d+)"`}},
		},
		CreatedAt: shopTime(), UpdatedAt: shopTime(),
	}
}

const fetchOTPJSON = `{"id":"action-1","name":"Fetch OTP","description":"Last code","request":{"method":"GET",` +
	`"url":"{{API_URL}}/otp","headers":[{"name":"Authorization","value":"Bearer {{TOKEN}}"}],"body":"",` +
	`"expected_status":200,"extract":[{"variable":"OTP","pattern":"\"code\":\"(\\d+)\""}]},` +
	`"created_at":"2026-11-11T09:00:00Z","updated_at":"2026-11-11T09:00:00Z"}`

const fetchOTPRequest = `{"name":"Fetch OTP","description":"Last code","request":{"method":"GET",` +
	`"url":"{{API_URL}}/otp","headers":[{"name":"Authorization","value":"Bearer {{TOKEN}}"}],` +
	`"extract":[{"variable":"OTP","pattern":"\"code\":\"(\\d+)\""}]}}`

func (f *fakeActions) List(context.Context, string, string) ([]action.Action, error) {
	return []action.Action{fetchOTPAction()}, nil
}

func (f *fakeActions) Get(_ context.Context, ref action.Ref) (action.Action, error) {
	if ref.ActionID != "action-1" {
		return action.Action{}, fault.WithDetail(fault.ErrNotFound, "action not found")
	}
	return fetchOTPAction(), nil
}

func (f *fakeActions) Create(_ context.Context, in action.CreateInput) (action.Action, error) {
	f.created = append(f.created, in)
	return fetchOTPAction(), nil
}

func (f *fakeActions) Replace(_ context.Context, in action.ReplaceInput) (action.Action, error) {
	f.replaced = append(f.replaced, in)
	return fetchOTPAction(), nil
}

func (f *fakeActions) Delete(_ context.Context, ref action.Ref) error {
	f.deleted = append(f.deleted, ref)
	return nil
}

func TestCreateAction(t *testing.T) {
	actions := &fakeActions{}

	recorder := serveAuthorized(t, Dependencies{Actions: actions},
		jsonRequest(http.MethodPost, "/projects/project-1/actions", fetchOTPRequest))

	requireResponse(t, recorder, http.StatusCreated, fetchOTPJSON)
	want := action.CreateInput{UserID: "user-1", ProjectID: "project-1", Definition: action.Definition{
		Name: "Fetch OTP", Description: "Last code", Request: fetchOTPAction().Request,
	}}
	want.Definition.Request.ExpectedStatus = 0
	if !reflect.DeepEqual(actions.created[0], want) {
		t.Fatalf("create input = %+v", actions.created[0])
	}
}

func TestListAndGetActions(t *testing.T) {
	actions := &fakeActions{}

	listed := serveAuthorized(t, Dependencies{Actions: actions},
		jsonRequest(http.MethodGet, "/projects/project-1/actions", ""))
	found := serveAuthorized(t, Dependencies{Actions: actions},
		jsonRequest(http.MethodGet, "/projects/project-1/actions/action-1", ""))
	missing := serveAuthorized(t, Dependencies{Actions: actions},
		jsonRequest(http.MethodGet, "/projects/project-1/actions/action-9", ""))

	requireResponse(t, listed, http.StatusOK, "["+fetchOTPJSON+"]")
	requireResponse(t, found, http.StatusOK, fetchOTPJSON)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d", missing.Code)
	}
}

func TestReplaceAndDeleteAction(t *testing.T) {
	actions := &fakeActions{}

	replaced := serveAuthorized(t, Dependencies{Actions: actions},
		jsonRequest(http.MethodPut, "/projects/project-1/actions/action-1", fetchOTPRequest))
	deleted := serveAuthorized(t, Dependencies{Actions: actions},
		jsonRequest(http.MethodDelete, "/projects/project-1/actions/action-1", ""))

	ref := action.Ref{UserID: "user-1", ProjectID: "project-1", ActionID: "action-1"}
	if replaced.Code != http.StatusOK || actions.replaced[0].Ref != ref ||
		actions.replaced[0].Definition.Request.URL != "{{API_URL}}/otp" {
		t.Fatalf("replace = %d, input %+v", replaced.Code, actions.replaced)
	}
	if deleted.Code != http.StatusNoContent || actions.deleted[0] != ref {
		t.Fatalf("delete = %d, refs %+v", deleted.Code, actions.deleted)
	}
}

package action_test

import (
	"context"
	"errors"
	"testing"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/action"
	"github.com/mobqa/backend/internal/usecase/project"
)

const projID = "project-1"

type fakeAccess struct {
	roles map[string]project.Role
}

func (a fakeAccess) Require(_ context.Context, _, userID string, need project.Role) (project.Role, error) {
	role, ok := a.roles[userID]
	if !ok {
		return "", fault.ErrNotFound
	}
	if !role.Includes(need) {
		return "", fault.ErrForbidden
	}
	return role, nil
}

func access() fakeAccess {
	return fakeAccess{roles: map[string]project.Role{"viewer": project.RoleViewer, "engineer": project.RoleEngineer}}
}

type fakeStore struct {
	saved   []action.Definition
	taken   string
	deleted []string
}

func (s *fakeStore) Actions(context.Context, string) ([]action.Action, error) {
	return []action.Action{{ID: "action-1", Name: "Fetch OTP"}}, nil
}

func (s *fakeStore) Action(_ context.Context, _, id string) (action.Action, error) {
	if id != "action-1" {
		return action.Action{}, fault.ErrNotFound
	}
	return action.Action{ID: id, Name: "Fetch OTP"}, nil
}

func (s *fakeStore) CreateAction(_ context.Context, projectID string, d action.Definition) (action.Action, error) {
	if d.Name == s.taken {
		return action.Action{}, fault.ErrConflict
	}
	s.saved = append(s.saved, d)
	return action.Action{ID: "action-2", ProjectID: projectID, Name: d.Name, Request: d.Request}, nil
}

func (s *fakeStore) UpdateAction(_ context.Context, _, id string, d action.Definition) (action.Action, error) {
	if id != "action-1" {
		return action.Action{}, fault.ErrNotFound
	}
	s.saved = append(s.saved, d)
	return action.Action{ID: id, Name: d.Name, Request: d.Request}, nil
}

func (s *fakeStore) DeleteAction(_ context.Context, _, id string) error {
	if id != "action-1" {
		return fault.ErrNotFound
	}
	s.deleted = append(s.deleted, id)
	return nil
}

func fetchOTP() action.Definition {
	return action.Definition{
		Name: "Fetch OTP", Description: "  Last code sent to the phone  ",
		Request: action.Request{
			Method: action.MethodGet, URL: "{{API_URL}}/test/otp?phone={{PHONE}}",
			Headers: []action.Header{{Name: "Authorization", Value: "Bearer {{API_TOKEN}}"}},
			Extract: []action.Extraction{{Variable: "OTP", Pattern: `"code":"(\d+)"`}},
		},
	}
}

func TestCreateNormalizesTheDefinition(t *testing.T) {
	store := &fakeStore{}

	created, err := action.NewService(store, access()).Create(context.Background(), action.CreateInput{
		UserID: "engineer", ProjectID: projID, Definition: fetchOTP(),
	})

	if err != nil || created.ID != "action-2" || len(store.saved) != 1 {
		t.Fatalf("Create = %+v, %v", created, err)
	}
	saved := store.saved[0]
	if saved.Description != "Last code sent to the phone" || saved.Request.ExpectedStatus != 200 {
		t.Fatalf("saved = %+v", saved)
	}
	post := fetchOTP()
	post.Name, post.Request.Method, post.Request.Headers, post.Request.Extract = "Seed", action.MethodPost, nil, nil
	if _, err := action.NewService(store, access()).Create(context.Background(), action.CreateInput{
		UserID: "engineer", ProjectID: projID, Definition: post,
	}); err != nil || store.saved[1].Request.Headers == nil || store.saved[1].Request.Extract == nil {
		t.Fatalf("Create without headers = %v, saved %+v", err, store.saved[1])
	}
}

func TestCreateRejectsInvalidDefinitions(t *testing.T) {
	tests := []struct {
		name   string
		change func(*action.Definition)
	}{
		{name: "padded name", change: func(d *action.Definition) { d.Name = " OTP" }},
		{name: "unknown method", change: func(d *action.Definition) { d.Request.Method = "TRACE" }},
		{name: "relative url", change: func(d *action.Definition) { d.Request.URL = "/test/otp" }},
		{name: "file url", change: func(d *action.Definition) { d.Request.URL = "file:///etc/passwd" }},
		{name: "header injection", change: func(d *action.Definition) {
			d.Request.Headers = []action.Header{{Name: "X-Seed", Value: "1\r\nHost: evil"}}
		}},
		{name: "bad header name", change: func(d *action.Definition) {
			d.Request.Headers = []action.Header{{Name: "X Seed", Value: "1"}}
		}},
		{name: "status", change: func(d *action.Definition) { d.Request.ExpectedStatus = 42 }},
		{name: "no capture group", change: func(d *action.Definition) { d.Request.Extract[0].Pattern = `\d+` }},
		{name: "two capture groups", change: func(d *action.Definition) { d.Request.Extract[0].Pattern = `(\d)(\d)` }},
		{name: "broken pattern", change: func(d *action.Definition) { d.Request.Extract[0].Pattern = `(` }},
		{name: "bad variable", change: func(d *action.Definition) { d.Request.Extract[0].Variable = "one-time" }},
		{name: "duplicate variable", change: func(d *action.Definition) {
			d.Request.Extract = append(d.Request.Extract, d.Request.Extract[0])
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			definition := fetchOTP()
			tt.change(&definition)
			store := &fakeStore{}

			_, err := action.NewService(store, access()).Create(context.Background(), action.CreateInput{
				UserID: "engineer", ProjectID: projID, Definition: definition,
			})

			if !errors.Is(err, fault.ErrInvalidInput) || len(store.saved) != 0 {
				t.Fatalf("Create error = %v", err)
			}
		})
	}
}

func TestActionsNeedTheRightRole(t *testing.T) {
	service := action.NewService(&fakeStore{}, access())
	ctx := context.Background()
	ref := action.Ref{UserID: "viewer", ProjectID: projID, ActionID: "action-1"}

	if list, err := service.List(ctx, "viewer", projID); err != nil || len(list) != 1 {
		t.Fatalf("List = %+v, %v", list, err)
	}
	if found, err := service.Get(ctx, ref); err != nil || found.Name != "Fetch OTP" {
		t.Fatalf("Get = %+v, %v", found, err)
	}
	if _, err := service.Create(ctx, action.CreateInput{UserID: "viewer", ProjectID: projID,
		Definition: fetchOTP()}); !errors.Is(err, fault.ErrForbidden) {
		t.Fatalf("viewer Create error = %v", err)
	}
	if _, err := service.Replace(ctx, action.ReplaceInput{Ref: ref, Definition: fetchOTP()}); !errors.Is(err,
		fault.ErrForbidden) {
		t.Fatalf("viewer Replace error = %v", err)
	}
	if err := service.Delete(ctx, ref); !errors.Is(err, fault.ErrForbidden) {
		t.Fatalf("viewer Delete error = %v", err)
	}
	if _, err := service.List(ctx, "stranger", projID); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("stranger List error = %v", err)
	}
}

func TestActionErrorsCarryDetails(t *testing.T) {
	store := &fakeStore{taken: "Fetch OTP"}
	service := action.NewService(store, access())
	ctx := context.Background()
	missing := action.Ref{UserID: "engineer", ProjectID: projID, ActionID: "action-9"}

	_, err := service.Create(ctx, action.CreateInput{UserID: "engineer", ProjectID: projID, Definition: fetchOTP()})
	if detail, _ := fault.Detail(err); !errors.Is(err, fault.ErrConflict) ||
		detail != "another action already has this name" {
		t.Fatalf("Create taken = %v", err)
	}
	_, getErr := service.Get(ctx, missing)
	_, replaceErr := service.Replace(ctx, action.ReplaceInput{Ref: missing, Definition: fetchOTP()})
	for name, err := range map[string]error{"Get": getErr, "Replace": replaceErr, "Delete": service.Delete(ctx, missing)} {
		if detail, _ := fault.Detail(err); !errors.Is(err, fault.ErrNotFound) || detail != "action not found" {
			t.Fatalf("%s missing = %v", name, err)
		}
	}
	replaced, err := service.Replace(ctx, action.ReplaceInput{
		Ref: action.Ref{UserID: "engineer", ProjectID: projID, ActionID: "action-1"}, Definition: fetchOTP(),
	})
	if err != nil || replaced.Request.ExpectedStatus != 200 {
		t.Fatalf("Replace = %+v, %v", replaced, err)
	}
}

type fakeSender struct {
	reply action.Reply
	sent  []action.Call
}

func (s *fakeSender) Send(_ context.Context, call action.Call) (action.Reply, error) {
	s.sent = append(s.sent, call)
	return s.reply, nil
}

func TestPerformFillsVariablesAndExtracts(t *testing.T) {
	sender := &fakeSender{reply: action.Reply{Status: 200, Body: `{"phone":"+7701","code":"4821"}`}}
	request := fetchOTP().Request
	request.ExpectedStatus = 200

	extracted, err := action.Perform(context.Background(), sender, request, map[string]string{
		"API_URL": "https://staging.example.com", "PHONE": "+7 701", "API_TOKEN": "t0ken",
	})

	if err != nil || extracted["OTP"] != "4821" || len(extracted) != 1 {
		t.Fatalf("Perform = %v, %v", extracted, err)
	}
	sent := sender.sent[0]
	if sent.Method != action.MethodGet || sent.URL != "https://staging.example.com/test/otp?phone=%2B7%20701" ||
		sent.Headers[0].Value != "Bearer t0ken" {
		t.Fatalf("sent = %+v", sent)
	}
}

func TestPerformFailures(t *testing.T) {
	variables := map[string]string{"API_URL": "https://staging.example.com", "PHONE": "7701", "API_TOKEN": "t"}
	tests := []struct {
		name      string
		reply     action.Reply
		variables map[string]string
		want      string
	}{
		{name: "status", reply: action.Reply{Status: 500}, variables: variables, want: "expected status 200, got 500"},
		{name: "no match", reply: action.Reply{Status: 200, Body: `{}`}, variables: variables,
			want: "the reply has nothing that matches the pattern for OTP"},
		{name: "unknown variable", variables: map[string]string{"API_URL": "https://x"},
			want: "unknown variable: PHONE"},
		{name: "scheme from a variable", variables: map[string]string{"API_URL": "file://", "PHONE": "1",
			"API_TOKEN": "t"}, want: "the url is not http or https once its variables are filled in"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := fetchOTP().Request
			request.ExpectedStatus = 200

			_, err := action.Perform(context.Background(), &fakeSender{reply: tt.reply}, request, tt.variables)

			if err == nil || err.Error() != tt.want {
				t.Fatalf("Perform error = %v, want %q", err, tt.want)
			}
		})
	}
}

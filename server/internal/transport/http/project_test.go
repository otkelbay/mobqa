package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/environment"
	"github.com/mobqa/backend/internal/usecase/project"
)

type fakeProjects struct {
	Projects
	project  project.Project
	page     project.Page
	err      error
	lists    []project.ListInput
	updates  []project.UpdateInput
	profiles []project.SetAuthProfileInput
	roles    []project.MemberRoleInput
}

func (f *fakeProjects) List(_ context.Context, in project.ListInput) (project.Page, error) {
	f.lists = append(f.lists, in)
	return f.page, f.err
}

func (f *fakeProjects) Get(_ context.Context, _, projectID string) (project.Detail, error) {
	if projectID != f.project.ID {
		return project.Detail{}, fault.WithDetail(fault.ErrNotFound, "project not found")
	}
	return project.Detail{Project: f.project, Role: project.RoleViewer, Members: []project.Member{
		{UserID: "user-1", Email: "ada@example.com", Name: "Ada", Role: project.RoleOwner, JoinedAt: shopTime()},
	}}, f.err
}

func (f *fakeProjects) Update(_ context.Context, in project.UpdateInput) (project.Project, error) {
	f.updates = append(f.updates, in)
	return f.project, f.err
}

func (f *fakeProjects) SetAuthProfile(_ context.Context, in project.SetAuthProfileInput) (project.AuthProfile, error) {
	f.profiles = append(f.profiles, in)
	return in.Profile, f.err
}

func (f *fakeProjects) SetMemberRole(_ context.Context, in project.MemberRoleInput) error {
	f.roles = append(f.roles, in)
	return f.err
}

type fakeEnvironments struct {
	Environments
	updates []environment.UpdateInput
}

func (f *fakeEnvironments) Update(_ context.Context, in environment.UpdateInput) (environment.Environment, error) {
	f.updates = append(f.updates, in)
	return environment.Environment{ID: "env-1", Name: "Staging", CreatedAt: shopTime(), UpdatedAt: shopTime()}, nil
}

func shopTime() time.Time {
	return time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
}

func shop() project.Project {
	return project.Project{
		ID: "project-1", Name: "Shop", Platforms: []project.Platform{project.PlatformAndroid},
		PackageName: "com.example.shop",
		Appearance:  project.Appearance{Initial: "S", Color: "#EFF4FF", TextColor: "#1D4ED8"},
		AuthProfile: project.AuthProfile{Method: project.AuthPassword, LoginVar: "LOGIN", PasswordVar: "PASSWORD"},
		CreatedAt:   shopTime(), UpdatedAt: shopTime(),
	}
}

func authorized(request *http.Request) *http.Request {
	request.Header.Set("Authorization", "Bearer access-1")
	return request
}

func serveAuthorized(t *testing.T, deps Dependencies, request *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	deps.Sessions = &fakeSessions{validToken: "access-1"}
	return serve(t, deps, authorized(request))
}

func TestGetProjectReturnsDetail(t *testing.T) {
	recorder := serveAuthorized(t, Dependencies{Projects: &fakeProjects{project: shop()}},
		httptest.NewRequest(http.MethodGet, "/projects/project-1", nil))

	requireResponse(t, recorder, http.StatusOK, `{"id":"project-1","name":"Shop","description":"",`+
		`"platforms":["android"],"package_name":"com.example.shop",`+
		`"appearance":{"initial":"S","color":"#EFF4FF","text_color":"#1D4ED8"},`+
		`"created_at":"2026-11-11T09:00:00Z","updated_at":"2026-11-11T09:00:00Z",`+
		`"auth_profile":{"method":"password","login_var":"LOGIN","password_var":"PASSWORD"},"role":"viewer",`+
		`"members":[{"user_id":"user-1","email":"ada@example.com","name":"Ada","role":"owner",`+
		`"joined_at":"2026-11-11T09:00:00Z"}]}`)
}

func TestGetProjectShowsNotFoundDetail(t *testing.T) {
	recorder := serveAuthorized(t, Dependencies{Projects: &fakeProjects{project: shop()}},
		httptest.NewRequest(http.MethodGet, "/projects/other", nil))

	requireResponse(t, recorder, http.StatusNotFound, `{"error":"project not found"}`)
}

func TestListProjectsReadsQuery(t *testing.T) {
	projects := &fakeProjects{page: project.Page{Items: []project.Project{shop()}, Total: 3, Page: 2, Limit: 1}}

	recorder := serveAuthorized(t, Dependencies{Projects: projects},
		httptest.NewRequest(http.MethodGet, "/projects?search=sh&page=2&limit=1", nil))

	if recorder.Code != http.StatusOK || projects.lists[0] != (project.ListInput{
		UserID: "user-1", Search: "sh", Page: 2, Limit: 1,
	}) {
		t.Fatalf("status %d, lists %+v", recorder.Code, projects.lists)
	}
	if body := recorder.Body.String(); !strings.Contains(body, `"total":3,"page":2,"limit":1,"has_more":true`) {
		t.Fatalf("body = %s", body)
	}
}

func TestListProjectsRejectsNonNumericPage(t *testing.T) {
	projects := &fakeProjects{}

	recorder := serveAuthorized(t, Dependencies{Projects: projects},
		httptest.NewRequest(http.MethodGet, "/projects?page=two", nil))

	requireResponse(t, recorder, http.StatusBadRequest, `{"error":"page and limit must be integers"}`)
	if len(projects.lists) != 0 {
		t.Fatal("bad query reached the usecase")
	}
}

func TestUpdateProjectPassesOnlyGivenFields(t *testing.T) {
	projects := &fakeProjects{project: shop()}

	recorder := serveAuthorized(t, Dependencies{Projects: projects}, jsonRequest(http.MethodPatch, "/projects/project-1",
		`{"name":"Store","appearance":{"initial":"St","color":"#000000","text_color":"#FFFFFF"}}`))

	if recorder.Code != http.StatusOK || len(projects.updates) != 1 {
		t.Fatalf("status %d, updates %+v", recorder.Code, projects.updates)
	}
	change := projects.updates[0].Change
	if change.ProjectID != "project-1" || *change.Name != "Store" || change.Platforms != nil ||
		change.Description != nil || change.Appearance.Initial != "St" {
		t.Fatalf("change = %+v", change)
	}
}

func TestSetAuthProfileMapsNestedSources(t *testing.T) {
	projects := &fakeProjects{}

	recorder := serveAuthorized(t, Dependencies{Projects: projects}, jsonRequest(http.MethodPut,
		"/projects/project-1/auth-profile",
		`{"method":"phone_otp","login_var":"PHONE","otp":{"action_id":"action-1"}}`))

	requireResponse(t, recorder, http.StatusOK,
		`{"method":"phone_otp","login_var":"PHONE","otp":{"action_id":"action-1"}}`)
	got := projects.profiles[0]
	if got.ProjectID != "project-1" || got.Profile.OTP.ActionID != "action-1" || got.Profile.SSO != nil {
		t.Fatalf("profile = %+v", got)
	}
}

func TestSetMemberRoleReadsPath(t *testing.T) {
	projects := &fakeProjects{}

	recorder := serveAuthorized(t, Dependencies{Projects: projects},
		jsonRequest(http.MethodPatch, "/projects/project-1/members/user-2", `{"role":"admin"}`))

	requireResponse(t, recorder, http.StatusNoContent, "")
	if projects.roles[0] != (project.MemberRoleInput{
		UserID: "user-1", ProjectID: "project-1", MemberID: "user-2", Role: project.RoleAdmin,
	}) {
		t.Fatalf("roles = %+v", projects.roles)
	}
}

func TestUpdateEnvironmentTellsAbsentFromEmptyVariables(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "absent", body: `{"name":"QA"}`, want: false},
		{name: "empty", body: `{"variables":[]}`, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			environments := &fakeEnvironments{}

			recorder := serveAuthorized(t, Dependencies{Environments: environments},
				jsonRequest(http.MethodPatch, "/projects/project-1/environments/env-1", tt.body))

			if recorder.Code != http.StatusOK {
				t.Fatalf("status %d", recorder.Code)
			}
			change := environments.updates[0].Change
			if change.EnvironmentID != "env-1" || (change.Variables != nil) != tt.want {
				t.Fatalf("change = %+v", change)
			}
		})
	}
}

func TestProjectRoutesRequireBearerToken(t *testing.T) {
	recorder := serve(t, Dependencies{Projects: &fakeProjects{}},
		httptest.NewRequest(http.MethodGet, "/projects", nil))

	requireResponse(t, recorder, http.StatusUnauthorized, `{"error":"unauthorized"}`)
}

package http

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/auth"
	"github.com/mobqa/backend/internal/usecase/user"
)

const sessionJSON = `{"access_token":"access-1","access_expires_at":"2026-11-11T09:15:00Z",` +
	`"refresh_token":"refresh-1","user":{"id":"user-1","email":"ada@example.com","name":"Ada",` +
	`"timezone":"UTC","language":"en","created_at":"2026-11-11T09:00:00Z","updated_at":"2026-11-11T09:00:00Z"}}`

func ada() user.User {
	created := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	return user.User{
		ID: "user-1", Email: "ada@example.com", Name: "Ada", Timezone: "UTC", Language: "en",
		CreatedAt: created, UpdatedAt: created,
	}
}

func adaSession() auth.Session {
	return auth.Session{
		AccessToken:     "access-1",
		AccessExpiresAt: time.Date(2026, 11, 11, 9, 15, 0, 0, time.UTC),
		RefreshToken:    "refresh-1",
		User:            ada(),
	}
}

type fakeSessions struct {
	session      auth.Session
	err          error
	validToken   string
	registered   []auth.RegisterInput
	loggedIn     []auth.LoginInput
	refreshed    []string
	loggedOut    []string
	authenticate []string
}

func (f *fakeSessions) Register(_ context.Context, in auth.RegisterInput) (auth.Session, error) {
	f.registered = append(f.registered, in)
	return f.session, f.err
}

func (f *fakeSessions) Login(_ context.Context, in auth.LoginInput) (auth.Session, error) {
	f.loggedIn = append(f.loggedIn, in)
	return f.session, f.err
}

func (f *fakeSessions) Refresh(_ context.Context, refreshToken string) (auth.Session, error) {
	f.refreshed = append(f.refreshed, refreshToken)
	return f.session, f.err
}

func (f *fakeSessions) Logout(_ context.Context, refreshToken string) error {
	f.loggedOut = append(f.loggedOut, refreshToken)
	return f.err
}

func (f *fakeSessions) Authenticate(accessToken string) (string, error) {
	f.authenticate = append(f.authenticate, accessToken)
	if accessToken != f.validToken {
		return "", fault.ErrUnauthorized
	}
	return "user-1", nil
}

type fakeProfiles struct {
	user    user.User
	err     error
	updates []user.ProfileUpdate
}

func (f *fakeProfiles) Profile(_ context.Context, userID string) (user.User, error) {
	if userID != f.user.ID {
		return user.User{}, fault.ErrNotFound
	}
	return f.user, f.err
}

func (f *fakeProfiles) UpdateProfile(_ context.Context, update user.ProfileUpdate) (user.User, error) {
	f.updates = append(f.updates, update)
	return f.user, f.err
}

func serve(t *testing.T, deps Dependencies, request *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	deps.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	if deps.Sessions == nil {
		deps.Sessions = &fakeSessions{}
	}
	if deps.Profiles == nil {
		deps.Profiles = &fakeProfiles{}
	}
	if deps.HubLink == nil {
		deps.HubLink = fakeHubLink{}
	}
	deps.Health = fakePinger{}
	recorder := httptest.NewRecorder()
	NewServer(Config{Address: ":0"}, deps).Handler().ServeHTTP(recorder, request)
	return recorder
}

func jsonRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func requireResponse(t *testing.T, recorder *httptest.ResponseRecorder, status int, body string) {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d (body %s)", recorder.Code, status, recorder.Body.String())
	}
	if got := strings.TrimSpace(recorder.Body.String()); got != body {
		t.Fatalf("body = %s, want %s", got, body)
	}
}

func TestRegisterReturnsSession(t *testing.T) {
	sessions := &fakeSessions{session: adaSession()}

	recorder := serve(t, Dependencies{Sessions: sessions}, jsonRequest(http.MethodPost, "/auth/register",
		`{"name":"Ada","email":"ada@example.com","password":"correct horse"}`))

	requireResponse(t, recorder, http.StatusCreated, sessionJSON)
	want := auth.RegisterInput{Name: "Ada", Email: "ada@example.com", Password: "correct horse"}
	if len(sessions.registered) != 1 || sessions.registered[0] != want {
		t.Fatalf("registered %+v, want %+v", sessions.registered, want)
	}
}

func TestLoginAndRefreshReturnSession(t *testing.T) {
	sessions := &fakeSessions{session: adaSession()}
	deps := Dependencies{Sessions: sessions}

	login := serve(t, deps, jsonRequest(http.MethodPost, "/auth/login",
		`{"email":"ada@example.com","password":"correct horse"}`))
	refresh := serve(t, deps, jsonRequest(http.MethodPost, "/auth/refresh", `{"refresh_token":"refresh-0"}`))

	requireResponse(t, login, http.StatusOK, sessionJSON)
	requireResponse(t, refresh, http.StatusOK, sessionJSON)
	if sessions.loggedIn[0] != (auth.LoginInput{Email: "ada@example.com", Password: "correct horse"}) ||
		sessions.refreshed[0] != "refresh-0" {
		t.Fatalf("login %+v, refresh %v", sessions.loggedIn, sessions.refreshed)
	}
}

func TestLogoutHasNoContent(t *testing.T) {
	sessions := &fakeSessions{}

	recorder := serve(t, Dependencies{Sessions: sessions},
		jsonRequest(http.MethodPost, "/auth/logout", `{"refresh_token":"refresh-1"}`))

	requireResponse(t, recorder, http.StatusNoContent, "")
	if len(sessions.loggedOut) != 1 || sessions.loggedOut[0] != "refresh-1" {
		t.Fatalf("logged out %v", sessions.loggedOut)
	}
}

func TestRejectsMalformedBodies(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "not JSON", body: `email=ada`},
		{name: "unknown field", body: `{"email":"ada@example.com","password":"x","admin":true}`},
		{name: "two objects", body: `{"email":"a"}{"email":"b"}`},
		{name: "wrong type", body: `{"email":42}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessions := &fakeSessions{}

			recorder := serve(t, Dependencies{Sessions: sessions}, jsonRequest(http.MethodPost, "/auth/login", tt.body))

			requireResponse(t, recorder, http.StatusBadRequest,
				`{"error":"request body must be one JSON object with known fields"}`)
			if len(sessions.loggedIn) != 0 {
				t.Fatal("malformed body reached the usecase")
			}
		})
	}
}

func TestMapsUsecaseErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		body   string
	}{
		{
			name:   "detail is shown",
			err:    fault.WithDetail(fault.ErrConflict, "email is already registered"),
			status: http.StatusConflict,
			body:   `{"error":"email is already registered"}`,
		},
		{
			name:   "wrapped kind without detail",
			err:    errors.Join(errors.New("users_email_key"), fault.ErrUnauthorized),
			status: http.StatusUnauthorized,
			body:   `{"error":"unauthorized"}`,
		},
		{
			name:   "internal error is hidden",
			err:    errors.New("pq: relation users does not exist"),
			status: http.StatusInternalServerError,
			body:   `{"error":"internal error"}`,
		},
		{
			name:   "unavailable",
			err:    fault.ErrUnavailable,
			status: http.StatusServiceUnavailable,
			body:   `{"error":"temporarily unavailable"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessions := &fakeSessions{err: tt.err}

			recorder := serve(t, Dependencies{Sessions: sessions}, jsonRequest(http.MethodPost, "/auth/register",
				`{"email":"ada@example.com","password":"correct horse"}`))

			requireResponse(t, recorder, tt.status, tt.body)
		})
	}
}

func TestProfileRequiresBearerToken(t *testing.T) {
	tests := []struct {
		name   string
		header string
	}{
		{name: "no header"},
		{name: "not bearer", header: "Basic YWRhOnB3"},
		{name: "invalid token", header: "Bearer forged"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/user/profile", nil)
			if tt.header != "" {
				request.Header.Set("Authorization", tt.header)
			}

			recorder := serve(t, Dependencies{Sessions: &fakeSessions{validToken: "access-1"}}, request)

			requireResponse(t, recorder, http.StatusUnauthorized, `{"error":"unauthorized"}`)
		})
	}
}

func TestProfileReturnsCurrentUser(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/user/profile", nil)
	request.Header.Set("Authorization", "Bearer access-1")

	recorder := serve(t, Dependencies{
		Sessions: &fakeSessions{validToken: "access-1"},
		Profiles: &fakeProfiles{user: ada()},
	}, request)

	requireResponse(t, recorder, http.StatusOK, `{"id":"user-1","email":"ada@example.com","name":"Ada",`+
		`"timezone":"UTC","language":"en","created_at":"2026-11-11T09:00:00Z","updated_at":"2026-11-11T09:00:00Z"}`)
}

func TestUpdateProfilePassesOnlyGivenFields(t *testing.T) {
	profiles := &fakeProfiles{user: ada()}
	request := jsonRequest(http.MethodPatch, "/user/profile", `{"name":"Ada Lovelace","avatar_url":null}`)
	request.Header.Set("Authorization", "Bearer access-1")

	recorder := serve(t, Dependencies{Sessions: &fakeSessions{validToken: "access-1"}, Profiles: profiles}, request)

	if recorder.Code != http.StatusOK || len(profiles.updates) != 1 {
		t.Fatalf("status %d, updates %+v", recorder.Code, profiles.updates)
	}
	update := profiles.updates[0]
	if update.UserID != "user-1" || update.Name == nil || *update.Name != "Ada Lovelace" ||
		update.AvatarURL != nil || update.Timezone != nil || update.Language != nil {
		t.Fatalf("update = %+v", update)
	}
}

//go:build e2e

package e2e

import (
	"net/http"
	"testing"
	"time"
)

type session struct {
	AccessToken  string  `json:"access_token"`
	RefreshToken string  `json:"refresh_token"`
	User         profile `json:"user"`
}

type profile struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Timezone string `json:"timezone"`
	Language string `json:"language"`
}

type errorBody struct {
	Error string `json:"error"`
}

func register(a *api) session {
	a.t.Helper()
	return registerAs(a, "Ada@Example.com")
}

func registerAs(a *api, email string) session {
	a.t.Helper()
	var registered session
	a.do(call{method: http.MethodPost, path: "/auth/register",
		body: map[string]string{"email": email, "password": "correct horse"}},
		http.StatusCreated, &registered)
	return registered
}

func refresh(token string) call {
	return call{method: http.MethodPost, path: "/auth/refresh", body: map[string]string{"refresh_token": token}}
}

func TestAccountLifecycle(t *testing.T) {
	api := newAPI(t)
	credentials := map[string]string{"email": "ada@example.com", "password": "correct horse"}

	registered := register(api)
	if registered.User.Email != "ada@example.com" || registered.User.Name != "ada" {
		t.Fatalf("registered user = %+v", registered.User)
	}

	var updated, current profile
	api.do(call{method: http.MethodPatch, path: "/user/profile", token: registered.AccessToken,
		body: map[string]string{"name": "Ada Lovelace", "timezone": "Asia/Almaty"}}, http.StatusOK, &updated)
	api.do(call{method: http.MethodGet, path: "/user/profile", token: registered.AccessToken}, http.StatusOK, &current)
	if current != updated || current.Name != "Ada Lovelace" || current.Timezone != "Asia/Almaty" {
		t.Fatalf("profile = %+v, updated %+v", current, updated)
	}

	var duplicate errorBody
	api.do(call{method: http.MethodPost, path: "/auth/register", body: credentials}, http.StatusConflict, &duplicate)
	if duplicate.Error != "email is already registered" {
		t.Fatalf("duplicate error = %q", duplicate.Error)
	}

	var loggedIn session
	api.do(call{method: http.MethodPost, path: "/auth/login", body: credentials}, http.StatusOK, &loggedIn)
	api.do(call{method: http.MethodPost, path: "/auth/logout",
		body: map[string]string{"refresh_token": loggedIn.RefreshToken}}, http.StatusNoContent, nil)
	api.do(refresh(loggedIn.RefreshToken), http.StatusUnauthorized, nil)
}

func TestReplayedRefreshTokenEndsTheSession(t *testing.T) {
	api := newAPI(t)
	first := register(api)

	var rotated session
	api.do(refresh(first.RefreshToken), http.StatusOK, &rotated)
	api.do(refresh(first.RefreshToken), http.StatusUnauthorized, nil)

	api.do(refresh(rotated.RefreshToken), http.StatusUnauthorized, nil)
}

func TestAccessTokenExpires(t *testing.T) {
	api := newAPI(t)
	registered := register(api)

	api.clock.Advance(15 * time.Minute)

	api.do(call{method: http.MethodGet, path: "/user/profile", token: registered.AccessToken},
		http.StatusUnauthorized, nil)
	var refreshed session
	api.do(refresh(registered.RefreshToken), http.StatusOK, &refreshed)
	api.do(call{method: http.MethodGet, path: "/user/profile", token: refreshed.AccessToken}, http.StatusOK, nil)
}

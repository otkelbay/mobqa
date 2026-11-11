package http

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/auth"
	"github.com/mobqa/backend/internal/usecase/user"
)

type Sessions interface {
	Register(ctx context.Context, in auth.RegisterInput) (auth.Session, error)
	Login(ctx context.Context, in auth.LoginInput) (auth.Session, error)
	Refresh(ctx context.Context, refreshToken string) (auth.Session, error)
	Logout(ctx context.Context, refreshToken string) error
	Authenticate(accessToken string) (string, error)
}

type registerRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type sessionResponse struct {
	AccessToken     string       `json:"access_token"`
	AccessExpiresAt time.Time    `json:"access_expires_at"`
	RefreshToken    string       `json:"refresh_token"`
	User            userResponse `json:"user"`
}

type userResponse struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	AvatarURL string    `json:"avatar_url,omitempty"`
	Timezone  string    `json:"timezone"`
	Language  string    `json:"language"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func newSessionResponse(session auth.Session) sessionResponse {
	return sessionResponse{
		AccessToken:     session.AccessToken,
		AccessExpiresAt: session.AccessExpiresAt,
		RefreshToken:    session.RefreshToken,
		User:            newUserResponse(session.User),
	}
}

func newUserResponse(u user.User) userResponse {
	return userResponse{
		ID:        u.ID,
		Email:     u.Email,
		Name:      u.Name,
		AvatarURL: u.AvatarURL,
		Timezone:  u.Timezone,
		Language:  u.Language,
		CreatedAt: u.CreatedAt,
		UpdatedAt: u.UpdatedAt,
	}
}

type authHandler struct {
	sessions Sessions
	logger   *slog.Logger
}

func (h authHandler) register(w http.ResponseWriter, r *http.Request) {
	var request registerRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	session, err := h.sessions.Register(r.Context(),
		auth.RegisterInput{Name: request.Name, Email: request.Email, Password: request.Password})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusCreated, newSessionResponse(session))
}

func (h authHandler) login(w http.ResponseWriter, r *http.Request) {
	var request loginRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	session, err := h.sessions.Login(r.Context(), auth.LoginInput{Email: request.Email, Password: request.Password})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, newSessionResponse(session))
}

func (h authHandler) refresh(w http.ResponseWriter, r *http.Request) {
	var request refreshRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	session, err := h.sessions.Refresh(r.Context(), request.RefreshToken)
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, newSessionResponse(session))
}

func (h authHandler) logout(w http.ResponseWriter, r *http.Request) {
	var request refreshRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	if err := h.sessions.Logout(r.Context(), request.RefreshToken); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type userIDKey struct{}

// authenticated admits requests that carry a valid bearer access token and records whose it is.
func (h authHandler) authenticated(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !found {
			writeFault(w, r, h.logger, fault.ErrUnauthorized)
			return
		}
		userID, err := h.sessions.Authenticate(token)
		if err != nil {
			writeFault(w, r, h.logger, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userIDKey{}, userID)))
	})
}

// currentUserID is only meaningful behind authenticated, which guarantees it is set.
func currentUserID(ctx context.Context) string {
	id, _ := ctx.Value(userIDKey{}).(string)
	return id
}

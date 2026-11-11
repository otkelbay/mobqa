package http

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/mobqa/backend/internal/usecase/user"
)

type Profiles interface {
	Profile(ctx context.Context, userID string) (user.User, error)
	UpdateProfile(ctx context.Context, update user.ProfileUpdate) (user.User, error)
}

type updateProfileRequest struct {
	Name      *string `json:"name"`
	AvatarURL *string `json:"avatar_url"`
	Timezone  *string `json:"timezone"`
	Language  *string `json:"language"`
}

type userHandler struct {
	profiles Profiles
	logger   *slog.Logger
}

func (h userHandler) profile(w http.ResponseWriter, r *http.Request) {
	found, err := h.profiles.Profile(r.Context(), currentUserID(r.Context()))
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, newUserResponse(found))
}

func (h userHandler) updateProfile(w http.ResponseWriter, r *http.Request) {
	var request updateProfileRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	updated, err := h.profiles.UpdateProfile(r.Context(), user.ProfileUpdate{
		UserID:    currentUserID(r.Context()),
		Name:      request.Name,
		AvatarURL: request.AvatarURL,
		Timezone:  request.Timezone,
		Language:  request.Language,
	})
	if err != nil {
		writeFault(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, newUserResponse(updated))
}

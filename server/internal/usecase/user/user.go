// Package user owns the user profile: what a signed-in user can read and change about themselves.
package user

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/language"

	"github.com/mobqa/backend/internal/fault"
)

const (
	DefaultTimezone = "UTC"
	DefaultLanguage = "en"

	maxNameLength = 100
	// maxAvatarURLLength matches what browsers reliably accept in an img src.
	maxAvatarURLLength = 2048
)

type User struct {
	ID        string
	Email     string
	Name      string
	AvatarURL string
	Timezone  string
	Language  string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ProfileUpdate changes only the fields that are set.
type ProfileUpdate struct {
	UserID    string
	Name      *string
	AvatarURL *string
	Timezone  *string
	Language  *string
}

func (u ProfileUpdate) Validate() error {
	if u.UserID == "" {
		return fmt.Errorf("empty user id: %w", fault.ErrInvalidInput)
	}
	if u.Name != nil {
		if err := ValidateName(*u.Name); err != nil {
			return err
		}
	}
	if u.AvatarURL != nil && *u.AvatarURL != "" {
		if err := validateAvatarURL(*u.AvatarURL); err != nil {
			return err
		}
	}
	if u.Timezone != nil {
		if _, err := time.LoadLocation(*u.Timezone); err != nil || *u.Timezone == "" || *u.Timezone == "Local" {
			return fault.WithDetail(fault.ErrInvalidInput, "timezone must be an IANA name such as Europe/Berlin")
		}
	}
	if u.Language != nil {
		if _, err := language.Parse(*u.Language); err != nil {
			return fault.WithDetail(fault.ErrInvalidInput, "language must be a BCP 47 tag such as en or pt-BR")
		}
	}
	return nil
}

func ValidateName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || utf8.RuneCountInString(trimmed) > maxNameLength || trimmed != name {
		return fault.WithDetail(fault.ErrInvalidInput, "name must be 1 to 100 characters without surrounding spaces")
	}
	return nil
}

func validateAvatarURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || len(raw) > maxAvatarURLLength || parsed.Host == "" ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") {
		return fault.WithDetail(fault.ErrInvalidInput, "avatar url must be an http or https address")
	}
	return nil
}

type store interface {
	User(ctx context.Context, id string) (User, error)
	UpdateUser(ctx context.Context, update ProfileUpdate) (User, error)
}

type Service struct {
	users store
}

func NewService(users store) *Service {
	return &Service{users: users}
}

func (s *Service) Profile(ctx context.Context, userID string) (User, error) {
	found, err := s.users.User(ctx, userID)
	if err != nil {
		return User{}, fmt.Errorf("load profile: %w", err)
	}
	return found, nil
}

func (s *Service) UpdateProfile(ctx context.Context, update ProfileUpdate) (User, error) {
	if err := update.Validate(); err != nil {
		return User{}, err
	}
	updated, err := s.users.UpdateUser(ctx, update)
	if err != nil {
		return User{}, fmt.Errorf("update profile: %w", err)
	}
	return updated, nil
}

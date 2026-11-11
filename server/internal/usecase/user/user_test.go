package user_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/user"
)

type fakeStore struct {
	user    user.User
	updates []user.ProfileUpdate
}

func (s *fakeStore) User(_ context.Context, id string) (user.User, error) {
	if id != s.user.ID {
		return user.User{}, fault.ErrNotFound
	}
	return s.user, nil
}

func (s *fakeStore) UpdateUser(_ context.Context, update user.ProfileUpdate) (user.User, error) {
	s.updates = append(s.updates, update)
	if update.Name != nil {
		s.user.Name = *update.Name
	}
	return s.user, nil
}

func ada() user.User {
	return user.User{ID: "user-1", Email: "ada@example.com", Name: "Ada", Timezone: "UTC", Language: "en"}
}

func pointer(value string) *string {
	return &value
}

func TestProfileReturnsUser(t *testing.T) {
	service := user.NewService(&fakeStore{user: ada()})

	got, err := service.Profile(context.Background(), "user-1")

	if err != nil || got != ada() {
		t.Fatalf("Profile = %+v, %v", got, err)
	}
}

func TestProfileOfUnknownUser(t *testing.T) {
	service := user.NewService(&fakeStore{user: ada()})

	_, err := service.Profile(context.Background(), "user-2")

	if !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("Profile = %v, want ErrNotFound", err)
	}
}

func TestUpdateProfileStoresValidUpdate(t *testing.T) {
	store := &fakeStore{user: ada()}
	service := user.NewService(store)

	got, err := service.UpdateProfile(context.Background(), user.ProfileUpdate{
		UserID: "user-1", Name: pointer("Ada Lovelace"), AvatarURL: pointer(""),
		Timezone: pointer("Asia/Almaty"), Language: pointer("pt-BR"),
	})

	if err != nil || got.Name != "Ada Lovelace" || len(store.updates) != 1 {
		t.Fatalf("UpdateProfile = %+v, %v, updates %d", got, err, len(store.updates))
	}
}

func TestUpdateProfileRejectsInvalidFields(t *testing.T) {
	tests := []struct {
		name   string
		update user.ProfileUpdate
		detail string
	}{
		{
			name:   "empty name",
			update: user.ProfileUpdate{UserID: "user-1", Name: pointer("")},
			detail: "name must be 1 to 100 characters without surrounding spaces",
		},
		{
			name:   "long name",
			update: user.ProfileUpdate{UserID: "user-1", Name: pointer(strings.Repeat("a", 101))},
			detail: "name must be 1 to 100 characters without surrounding spaces",
		},
		{
			name:   "avatar not http",
			update: user.ProfileUpdate{UserID: "user-1", AvatarURL: pointer("javascript:alert(1)")},
			detail: "avatar url must be an http or https address",
		},
		{
			name:   "unknown timezone",
			update: user.ProfileUpdate{UserID: "user-1", Timezone: pointer("Mars/Olympus")},
			detail: "timezone must be an IANA name such as Europe/Berlin",
		},
		{
			name:   "server local timezone",
			update: user.ProfileUpdate{UserID: "user-1", Timezone: pointer("Local")},
			detail: "timezone must be an IANA name such as Europe/Berlin",
		},
		{
			name:   "bad language",
			update: user.ProfileUpdate{UserID: "user-1", Language: pointer("english!")},
			detail: "language must be a BCP 47 tag such as en or pt-BR",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{user: ada()}
			service := user.NewService(store)

			_, err := service.UpdateProfile(context.Background(), tt.update)

			if detail, _ := fault.Detail(err); !errors.Is(err, fault.ErrInvalidInput) || detail != tt.detail {
				t.Fatalf("UpdateProfile = %v, want invalid input %q", err, tt.detail)
			}
			if len(store.updates) != 0 {
				t.Fatal("invalid update reached the store")
			}
		})
	}
}

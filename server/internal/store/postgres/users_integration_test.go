//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mobqa/backend/internal/clock/clocktest"
	"github.com/mobqa/backend/internal/dbtest"
	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/store/postgres"
	"github.com/mobqa/backend/internal/usecase/auth"
	"github.com/mobqa/backend/internal/usecase/user"
)

const familyID = "0190f3c4-0000-7000-8000-00000000f001"

func testStart() time.Time {
	return time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
}

func newUsers(t *testing.T) (*postgres.Users, *clocktest.Manual) {
	t.Helper()
	now := clocktest.New(testStart())
	return postgres.NewUsers(dbtest.New(t, now)), now
}

func createAda(t *testing.T, users *postgres.Users) user.User {
	t.Helper()
	created, err := users.CreateUser(context.Background(), auth.NewUser{
		Email: "ada@example.com", Name: "Ada", PasswordHash: []byte("hash"), Timezone: "UTC", Language: "en",
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return created
}

func pointer(value string) *string {
	return &value
}

func TestUsersCreateAndRead(t *testing.T) {
	users, _ := newUsers(t)
	ctx := context.Background()

	created := createAda(t, users)

	want := user.User{
		ID: created.ID, Email: "ada@example.com", Name: "Ada", Timezone: "UTC", Language: "en",
		CreatedAt: testStart(), UpdatedAt: testStart(),
	}
	if created != want {
		t.Fatalf("CreateUser = %+v, want %+v", created, want)
	}
	if got, err := users.User(ctx, created.ID); err != nil || got != want {
		t.Fatalf("User = %+v, %v", got, err)
	}
	credentials, err := users.Credentials(ctx, "ada@example.com")
	if err != nil || credentials.User != want || string(credentials.PasswordHash) != "hash" {
		t.Fatalf("Credentials = %+v, %v", credentials, err)
	}
}

func TestUsersRejectDuplicateEmail(t *testing.T) {
	users, _ := newUsers(t)
	createAda(t, users)

	_, err := users.CreateUser(context.Background(), auth.NewUser{
		Email: "ada@example.com", Name: "Other", PasswordHash: []byte("hash"), Timezone: "UTC", Language: "en",
	})

	if !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("CreateUser = %v, want ErrConflict", err)
	}
}

func TestUsersMissing(t *testing.T) {
	users, _ := newUsers(t)
	ctx := context.Background()

	if _, err := users.User(ctx, "0190f3c4-0000-7000-8000-000000000000"); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("User = %v, want ErrNotFound", err)
	}
	if _, err := users.Credentials(ctx, "nobody@example.com"); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("Credentials = %v, want ErrNotFound", err)
	}
	_, err := users.UpdateUser(ctx, user.ProfileUpdate{UserID: "0190f3c4-0000-7000-8000-000000000000"})
	if !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("UpdateUser = %v, want ErrNotFound", err)
	}
}

func TestUsersUpdateChangesOnlyGivenFields(t *testing.T) {
	users, now := newUsers(t)
	ctx := context.Background()
	created := createAda(t, users)
	now.Advance(time.Hour)

	withAvatar, err := users.UpdateUser(ctx, user.ProfileUpdate{
		UserID: created.ID, AvatarURL: pointer("https://example.com/ada.png"), Timezone: pointer("Asia/Almaty"),
	})
	if err != nil {
		t.Fatalf("UpdateUser: %v", err)
	}
	want := created
	want.AvatarURL = "https://example.com/ada.png"
	want.Timezone = "Asia/Almaty"
	want.UpdatedAt = testStart().Add(time.Hour)
	if withAvatar != want {
		t.Fatalf("UpdateUser = %+v, want %+v", withAvatar, want)
	}

	cleared, err := users.UpdateUser(ctx, user.ProfileUpdate{UserID: created.ID, AvatarURL: pointer("")})
	if err != nil || cleared.AvatarURL != "" || cleared.Timezone != "Asia/Almaty" {
		t.Fatalf("clearing avatar = %+v, %v", cleared, err)
	}
}

func TestRefreshTokenConsumedOnce(t *testing.T) {
	users, _ := newUsers(t)
	ctx := context.Background()
	ada := createAda(t, users)
	expires := testStart().Add(time.Hour)
	err := users.CreateRefreshToken(ctx,
		auth.NewRefreshToken{UserID: ada.ID, FamilyID: familyID, Hash: []byte("token-1"), ExpiresAt: expires})
	if err != nil {
		t.Fatalf("CreateRefreshToken: %v", err)
	}

	first, err := users.ConsumeRefreshToken(ctx, []byte("token-1"))
	if err != nil {
		t.Fatalf("ConsumeRefreshToken: %v", err)
	}
	second, err := users.ConsumeRefreshToken(ctx, []byte("token-1"))
	if err != nil {
		t.Fatalf("ConsumeRefreshToken again: %v", err)
	}

	want := auth.ConsumedRefreshToken{UserID: ada.ID, FamilyID: familyID, ExpiresAt: expires}
	if first != want {
		t.Fatalf("first use = %+v, want %+v", first, want)
	}
	want.WasRevoked = true
	if second != want {
		t.Fatalf("second use = %+v, want %+v", second, want)
	}
}

func TestRefreshTokenConcurrentUseHasOneWinner(t *testing.T) {
	const attempts = 8
	users, _ := newUsers(t)
	ctx := context.Background()
	ada := createAda(t, users)
	err := users.CreateRefreshToken(ctx,
		auth.NewRefreshToken{UserID: ada.ID, FamilyID: familyID, Hash: []byte("token-1"), ExpiresAt: testStart()})
	if err != nil {
		t.Fatalf("CreateRefreshToken: %v", err)
	}

	var mu sync.Mutex
	fresh := 0
	var wg sync.WaitGroup
	wg.Add(attempts)
	for range attempts {
		go func() {
			defer wg.Done()
			consumed, err := users.ConsumeRefreshToken(ctx, []byte("token-1"))
			if err != nil || consumed.WasRevoked {
				return
			}
			mu.Lock()
			fresh++
			mu.Unlock()
		}()
	}
	wg.Wait()

	if fresh != 1 {
		t.Fatalf("%d uses saw a fresh token, want 1", fresh)
	}
}

func TestRevokeRefreshFamily(t *testing.T) {
	users, _ := newUsers(t)
	ctx := context.Background()
	ada := createAda(t, users)
	expires := testStart().Add(time.Hour)
	tokens := []auth.NewRefreshToken{
		{UserID: ada.ID, FamilyID: familyID, Hash: []byte("family-1"), ExpiresAt: expires},
		{UserID: ada.ID, FamilyID: familyID, Hash: []byte("family-2"), ExpiresAt: expires},
		{UserID: ada.ID, FamilyID: "0190f3c4-0000-7000-8000-00000000f002", Hash: []byte("other"), ExpiresAt: expires},
	}
	for _, token := range tokens {
		if err := users.CreateRefreshToken(ctx, token); err != nil {
			t.Fatalf("CreateRefreshToken: %v", err)
		}
	}

	if err := users.RevokeRefreshFamily(ctx, familyID); err != nil {
		t.Fatalf("RevokeRefreshFamily: %v", err)
	}

	for hash, wantRevoked := range map[string]bool{"family-1": true, "family-2": true, "other": false} {
		consumed, err := users.ConsumeRefreshToken(ctx, []byte(hash))
		if err != nil || consumed.WasRevoked != wantRevoked {
			t.Fatalf("%s: consumed %+v, %v, want revoked %v", hash, consumed, err, wantRevoked)
		}
	}
}

func TestConsumeUnknownRefreshToken(t *testing.T) {
	users, _ := newUsers(t)

	_, err := users.ConsumeRefreshToken(context.Background(), []byte("missing"))

	if !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("ConsumeRefreshToken = %v, want ErrNotFound", err)
	}
}

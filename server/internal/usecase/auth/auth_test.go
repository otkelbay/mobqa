package auth_test

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/mobqa/backend/internal/clock/clocktest"
	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/auth"
	"github.com/mobqa/backend/internal/usecase/user"
)

const (
	secret     = "0123456789abcdef0123456789abcdef"
	accessTTL  = 15 * time.Minute
	refreshTTL = 30 * 24 * time.Hour
	password   = "correct horse"
)

type storedToken struct {
	auth.NewRefreshToken
	revoked bool
}

type fakeStore struct {
	mu     sync.Mutex
	users  map[string]auth.Credentials
	tokens map[string]*storedToken
}

func newFakeStore() *fakeStore {
	return &fakeStore{users: map[string]auth.Credentials{}, tokens: map[string]*storedToken{}}
}

func (s *fakeStore) CreateUser(_ context.Context, in auth.NewUser) (user.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, taken := s.users[in.Email]; taken {
		return user.User{}, fault.ErrConflict
	}
	created := user.User{
		ID: fmt.Sprintf("user-%d", len(s.users)+1), Email: in.Email, Name: in.Name,
		Timezone: in.Timezone, Language: in.Language,
	}
	s.users[in.Email] = auth.Credentials{User: created, PasswordHash: in.PasswordHash}
	return created, nil
}

func (s *fakeStore) Credentials(_ context.Context, email string) (auth.Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	found, ok := s.users[email]
	if !ok {
		return auth.Credentials{}, fault.ErrNotFound
	}
	return found, nil
}

func (s *fakeStore) User(_ context.Context, id string) (user.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, credentials := range s.users {
		if credentials.User.ID == id {
			return credentials.User, nil
		}
	}
	return user.User{}, fault.ErrNotFound
}

func (s *fakeStore) CreateRefreshToken(_ context.Context, in auth.NewRefreshToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[hex.EncodeToString(in.Hash)] = &storedToken{NewRefreshToken: in}
	return nil
}

func (s *fakeStore) ConsumeRefreshToken(_ context.Context, hash []byte) (auth.ConsumedRefreshToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	token, ok := s.tokens[hex.EncodeToString(hash)]
	if !ok {
		return auth.ConsumedRefreshToken{}, fault.ErrNotFound
	}
	consumed := auth.ConsumedRefreshToken{
		UserID: token.UserID, FamilyID: token.FamilyID, ExpiresAt: token.ExpiresAt, WasRevoked: token.revoked,
	}
	token.revoked = true
	return consumed, nil
}

func (s *fakeStore) RevokeRefreshFamily(_ context.Context, familyID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, token := range s.tokens {
		if token.FamilyID == familyID {
			token.revoked = true
		}
	}
	return nil
}

func newService(t *testing.T) (*auth.Service, *clocktest.Manual) {
	t.Helper()
	store := newFakeStore()
	now := clocktest.New(time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC))
	cfg := auth.Config{Secret: []byte(secret), AccessTTL: accessTTL, RefreshTTL: refreshTTL}
	service, err := auth.NewService(cfg, store, now, auth.WithPasswordCost(bcrypt.MinCost))
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return service, now
}

func register(t *testing.T, service *auth.Service) auth.Session {
	t.Helper()
	session, err := service.Register(context.Background(),
		auth.RegisterInput{Email: "ada@example.com", Password: password})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	return session
}

func requireDetail(t *testing.T, err, kind error, detail string) {
	t.Helper()
	if !errors.Is(err, kind) {
		t.Fatalf("error = %v, want %v", err, kind)
	}
	if got, _ := fault.Detail(err); got != detail {
		t.Fatalf("detail = %q, want %q", got, detail)
	}
}

func TestRegisterStartsSession(t *testing.T) {
	service, now := newService(t)

	session := register(t, service)

	wantUser := user.User{ID: "user-1", Email: "ada@example.com", Name: "ada", Timezone: "UTC", Language: "en"}
	if session.User != wantUser {
		t.Fatalf("user = %+v, want %+v", session.User, wantUser)
	}
	if !session.AccessExpiresAt.Equal(now.Now().Add(accessTTL)) || session.RefreshToken == "" {
		t.Fatalf("session = %+v", session)
	}
	userID, err := service.Authenticate(session.AccessToken)
	if err != nil || userID != "user-1" {
		t.Fatalf("Authenticate = %q, %v", userID, err)
	}
}

func TestRegisterKeepsGivenName(t *testing.T) {
	service, _ := newService(t)

	session, err := service.Register(context.Background(),
		auth.RegisterInput{Name: "Ada Lovelace", Email: "ada@example.com", Password: password})

	if err != nil || session.User.Name != "Ada Lovelace" {
		t.Fatalf("Register = %+v, %v", session.User, err)
	}
}

func TestRegisterRejectsTakenEmail(t *testing.T) {
	service, _ := newService(t)
	register(t, service)

	_, err := service.Register(context.Background(), auth.RegisterInput{Email: "ada@example.com", Password: password})

	requireDetail(t, err, fault.ErrConflict, "email is already registered")
}

func TestRegisterValidatesInput(t *testing.T) {
	tests := []struct {
		name   string
		input  auth.RegisterInput
		detail string
	}{
		{
			name:   "malformed email",
			input:  auth.RegisterInput{Email: "ada", Password: password},
			detail: "email must be a plain address such as name@example.com",
		},
		{
			name:   "display name in email",
			input:  auth.RegisterInput{Email: "Ada <ada@example.com>", Password: password},
			detail: "email must be a plain address such as name@example.com",
		},
		{
			name:   "short password",
			input:  auth.RegisterInput{Email: "ada@example.com", Password: "1234567"},
			detail: "password must be 8 to 72 bytes long",
		},
		{
			name:   "password past bcrypt limit",
			input:  auth.RegisterInput{Email: "ada@example.com", Password: strings.Repeat("p", 73)},
			detail: "password must be 8 to 72 bytes long",
		},
		{
			name:   "padded name",
			input:  auth.RegisterInput{Name: " Ada", Email: "ada@example.com", Password: password},
			detail: "name must be 1 to 100 characters without surrounding spaces",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, _ := newService(t)

			_, err := service.Register(context.Background(), tt.input)

			requireDetail(t, err, fault.ErrInvalidInput, tt.detail)
		})
	}
}

func TestLoginAcceptsAnyEmailCase(t *testing.T) {
	service, _ := newService(t)
	register(t, service)

	session, err := service.Login(context.Background(), auth.LoginInput{Email: " ADA@example.com", Password: password})

	if err != nil || session.User.ID != "user-1" {
		t.Fatalf("Login = %+v, %v", session.User, err)
	}
}

func TestLoginRejectsBadCredentials(t *testing.T) {
	tests := []struct {
		name  string
		input auth.LoginInput
	}{
		{name: "wrong password", input: auth.LoginInput{Email: "ada@example.com", Password: "wrong password"}},
		{name: "unknown email", input: auth.LoginInput{Email: "bob@example.com", Password: password}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service, _ := newService(t)
			register(t, service)

			_, err := service.Login(context.Background(), tt.input)

			requireDetail(t, err, fault.ErrUnauthorized, "invalid email or password")
		})
	}
}

func TestRefreshRotatesToken(t *testing.T) {
	service, _ := newService(t)
	first := register(t, service)

	second, err := service.Refresh(context.Background(), first.RefreshToken)

	if err != nil || second.User.ID != "user-1" || second.RefreshToken == first.RefreshToken {
		t.Fatalf("Refresh = %+v, %v", second, err)
	}
	if _, err := service.Refresh(context.Background(), second.RefreshToken); err != nil {
		t.Fatalf("Refresh with the rotated token: %v", err)
	}
}

func TestRefreshReplayRevokesFamily(t *testing.T) {
	service, _ := newService(t)
	ctx := context.Background()
	first := register(t, service)
	other, err := service.Login(ctx, auth.LoginInput{Email: "ada@example.com", Password: password})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	second, err := service.Refresh(ctx, first.RefreshToken)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	_, err = service.Refresh(ctx, first.RefreshToken)

	requireDetail(t, err, fault.ErrUnauthorized, "session expired, sign in again")
	if _, err := service.Refresh(ctx, second.RefreshToken); !errors.Is(err, fault.ErrUnauthorized) {
		t.Fatalf("descendant token still works after replay: %v", err)
	}
	if _, err := service.Refresh(ctx, other.RefreshToken); err != nil {
		t.Fatalf("an unrelated sign-in was revoked: %v", err)
	}
}

func TestRefreshRejectsExpiredToken(t *testing.T) {
	service, now := newService(t)
	session := register(t, service)

	now.Advance(refreshTTL)
	_, err := service.Refresh(context.Background(), session.RefreshToken)

	requireDetail(t, err, fault.ErrUnauthorized, "session expired, sign in again")
}

func TestRefreshRejectsUnknownToken(t *testing.T) {
	service, _ := newService(t)

	_, err := service.Refresh(context.Background(), "not-a-token")

	requireDetail(t, err, fault.ErrUnauthorized, "session expired, sign in again")
}

func TestLogoutRevokesRefreshToken(t *testing.T) {
	service, _ := newService(t)
	ctx := context.Background()
	session := register(t, service)

	if err := service.Logout(ctx, session.RefreshToken); err != nil {
		t.Fatalf("Logout: %v", err)
	}

	if _, err := service.Refresh(ctx, session.RefreshToken); !errors.Is(err, fault.ErrUnauthorized) {
		t.Fatalf("Refresh after logout = %v, want ErrUnauthorized", err)
	}
	if err := service.Logout(ctx, "unknown"); err != nil {
		t.Fatalf("Logout of unknown token: %v", err)
	}
}

func TestAuthenticateRejectsExpiredAccessToken(t *testing.T) {
	service, now := newService(t)
	session := register(t, service)

	now.Advance(accessTTL)

	if _, err := service.Authenticate(session.AccessToken); !errors.Is(err, fault.ErrUnauthorized) {
		t.Fatalf("Authenticate = %v, want ErrUnauthorized", err)
	}
}

func TestAuthenticateRejectsForeignTokens(t *testing.T) {
	expires := jwt.NewNumericDate(time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC))
	foreignKey := func(t *testing.T) string {
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256,
			jwt.RegisteredClaims{Issuer: "mobqa", Subject: "user-1", ExpiresAt: expires}).
			SignedString([]byte("another-secret-another-secret-00"))
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return token
	}
	unsigned := func(t *testing.T) string {
		token, err := jwt.NewWithClaims(jwt.SigningMethodNone,
			jwt.RegisteredClaims{Issuer: "mobqa", Subject: "user-1", ExpiresAt: expires}).
			SignedString(jwt.UnsafeAllowNoneSignatureType)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return token
	}
	service, _ := newService(t)

	for name, token := range map[string]string{
		"garbage":     "abc.def.ghi",
		"foreign key": foreignKey(t),
		"unsigned":    unsigned(t),
	} {
		if _, err := service.Authenticate(token); !errors.Is(err, fault.ErrUnauthorized) {
			t.Fatalf("%s: Authenticate = %v, want ErrUnauthorized", name, err)
		}
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		config  auth.Config
		wantErr bool
	}{
		{name: "valid", config: auth.Config{Secret: []byte(secret), AccessTTL: accessTTL, RefreshTTL: refreshTTL}},
		{name: "short secret", config: auth.Config{Secret: []byte("short"), AccessTTL: accessTTL, RefreshTTL: refreshTTL},
			wantErr: true},
		{name: "refresh not longer than access", config: auth.Config{Secret: []byte(secret), AccessTTL: time.Hour,
			RefreshTTL: time.Hour}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.config.Validate(); tt.wantErr != (err != nil) {
				t.Fatalf("Validate() = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

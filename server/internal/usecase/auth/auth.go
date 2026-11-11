// Package auth signs users up and in. Access tokens are short-lived signed JWTs checked without storage;
// refresh tokens are random, stored only as hashes, rotated on every use and grouped in families so that
// replaying a used token revokes every session descended from the same sign-in.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/mobqa/backend/internal/clock"
	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/user"
)

const (
	issuer = "mobqa"
	// minSecretLength is the HS256 key size: a shorter key weakens the signature.
	minSecretLength   = 32
	minPasswordLength = 8
	// maxPasswordLength is where bcrypt stops reading; longer passwords would be silently truncated.
	maxPasswordLength = 72
	maxEmailLength    = 254
)

var (
	errBadCredentials = fault.WithDetail(fault.ErrUnauthorized, "invalid email or password")
	errBadSession     = fault.WithDetail(fault.ErrUnauthorized, "session expired, sign in again")
)

type Config struct {
	Secret     []byte
	AccessTTL  time.Duration
	RefreshTTL time.Duration
}

func (c Config) Validate() error {
	if len(c.Secret) < minSecretLength {
		return fmt.Errorf("token secret shorter than %d bytes: %w", minSecretLength, fault.ErrInvalidInput)
	}
	if c.AccessTTL <= 0 || c.RefreshTTL <= c.AccessTTL {
		return fmt.Errorf("access ttl %s, refresh ttl %s: %w", c.AccessTTL, c.RefreshTTL, fault.ErrInvalidInput)
	}
	return nil
}

type RegisterInput struct {
	Name     string
	Email    string
	Password string
}

func (in RegisterInput) Validate() error {
	if err := validateEmail(in.Email); err != nil {
		return err
	}
	if in.Name != "" {
		if err := user.ValidateName(in.Name); err != nil {
			return err
		}
	}
	return validatePassword(in.Password)
}

type LoginInput struct {
	Email    string
	Password string
}

func (in LoginInput) Validate() error {
	if in.Email == "" || in.Password == "" {
		return fault.WithDetail(fault.ErrInvalidInput, "email and password are required")
	}
	return nil
}

type Session struct {
	AccessToken     string
	AccessExpiresAt time.Time
	RefreshToken    string
	User            user.User
}

type NewUser struct {
	Email        string
	Name         string
	PasswordHash []byte
	Timezone     string
	Language     string
}

type Credentials struct {
	User         user.User
	PasswordHash []byte
}

type NewRefreshToken struct {
	UserID    string
	FamilyID  string
	Hash      []byte
	ExpiresAt time.Time
}

// ConsumedRefreshToken describes a token that has just been revoked by its use. WasRevoked reports that
// it had been used before, which means it was stolen or replayed.
type ConsumedRefreshToken struct {
	UserID     string
	FamilyID   string
	ExpiresAt  time.Time
	WasRevoked bool
}

type store interface {
	CreateUser(ctx context.Context, in NewUser) (user.User, error)
	Credentials(ctx context.Context, email string) (Credentials, error)
	User(ctx context.Context, id string) (user.User, error)
	CreateRefreshToken(ctx context.Context, in NewRefreshToken) error
	ConsumeRefreshToken(ctx context.Context, hash []byte) (ConsumedRefreshToken, error)
	RevokeRefreshFamily(ctx context.Context, familyID string) error
}

type Service struct {
	store        store
	clock        clock.Clock
	cfg          Config
	passwordCost int
	// decoyHash is compared against when the email is unknown, so a login takes as long either way.
	decoyHash []byte
}

type Option func(*Service)

// WithPasswordCost lowers the bcrypt cost; tests use it to stay fast.
func WithPasswordCost(cost int) Option {
	return func(s *Service) { s.passwordCost = cost }
}

func NewService(cfg Config, users store, now clock.Clock, opts ...Option) (*Service, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	s := &Service{store: users, clock: now, cfg: cfg, passwordCost: bcrypt.DefaultCost}
	for _, opt := range opts {
		opt(s)
	}
	decoy, err := bcrypt.GenerateFromPassword([]byte(rand.Text()), s.passwordCost)
	if err != nil {
		return nil, fmt.Errorf("hash decoy password: %w", err)
	}
	s.decoyHash = decoy
	return s, nil
}

func (s *Service) Register(ctx context.Context, in RegisterInput) (Session, error) {
	if err := in.Validate(); err != nil {
		return Session{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), s.passwordCost)
	if err != nil {
		return Session{}, fmt.Errorf("hash password: %w", err)
	}
	email := normalizeEmail(in.Email)
	name := in.Name
	if name == "" {
		name, _, _ = strings.Cut(email, "@")
	}
	created, err := s.store.CreateUser(ctx, NewUser{
		Email: email, Name: name, PasswordHash: hash, Timezone: user.DefaultTimezone, Language: user.DefaultLanguage,
	})
	if errors.Is(err, fault.ErrConflict) {
		return Session{}, fault.WithDetail(fault.ErrConflict, "email is already registered")
	}
	if err != nil {
		return Session{}, fmt.Errorf("create user: %w", err)
	}
	return s.startSession(ctx, created, newFamilyID())
}

func (s *Service) Login(ctx context.Context, in LoginInput) (Session, error) {
	if err := in.Validate(); err != nil {
		return Session{}, err
	}
	credentials, err := s.store.Credentials(ctx, normalizeEmail(in.Email))
	if errors.Is(err, fault.ErrNotFound) {
		_ = bcrypt.CompareHashAndPassword(s.decoyHash, []byte(in.Password))
		return Session{}, errBadCredentials
	}
	if err != nil {
		return Session{}, fmt.Errorf("load credentials: %w", err)
	}
	if bcrypt.CompareHashAndPassword(credentials.PasswordHash, []byte(in.Password)) != nil {
		return Session{}, errBadCredentials
	}
	return s.startSession(ctx, credentials.User, newFamilyID())
}

// Refresh exchanges a refresh token for a new session in the same family. Presenting a token that was
// already exchanged revokes the whole family.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (Session, error) {
	consumed, err := s.store.ConsumeRefreshToken(ctx, hashToken(refreshToken))
	if errors.Is(err, fault.ErrNotFound) {
		return Session{}, errBadSession
	}
	if err != nil {
		return Session{}, fmt.Errorf("consume refresh token: %w", err)
	}
	if consumed.WasRevoked {
		if err := s.store.RevokeRefreshFamily(ctx, consumed.FamilyID); err != nil {
			return Session{}, fmt.Errorf("revoke replayed token family: %w", err)
		}
		return Session{}, errBadSession
	}
	if !s.clock.Now().Before(consumed.ExpiresAt) {
		return Session{}, errBadSession
	}
	owner, err := s.store.User(ctx, consumed.UserID)
	if err != nil {
		return Session{}, fmt.Errorf("load session user: %w", err)
	}
	return s.startSession(ctx, owner, consumed.FamilyID)
}

// Logout revokes a refresh token. Unknown and already used tokens are accepted, so logging out twice
// is harmless.
func (s *Service) Logout(ctx context.Context, refreshToken string) error {
	_, err := s.store.ConsumeRefreshToken(ctx, hashToken(refreshToken))
	if err != nil && !errors.Is(err, fault.ErrNotFound) {
		return fmt.Errorf("revoke refresh token: %w", err)
	}
	return nil
}

// Authenticate returns the user an access token was issued to.
func (s *Service) Authenticate(accessToken string) (string, error) {
	var claims jwt.RegisteredClaims
	_, err := jwt.ParseWithClaims(accessToken, &claims,
		func(*jwt.Token) (any, error) { return s.cfg.Secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(issuer),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(s.clock.Now),
	)
	if err != nil || claims.Subject == "" {
		return "", fault.ErrUnauthorized
	}
	return claims.Subject, nil
}

func (s *Service) startSession(ctx context.Context, owner user.User, familyID string) (Session, error) {
	now := s.clock.Now()
	accessExpiresAt := now.Add(s.cfg.AccessTTL)
	access, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Issuer:    issuer,
		Subject:   owner.ID,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(accessExpiresAt),
	}).SignedString(s.cfg.Secret)
	if err != nil {
		return Session{}, fmt.Errorf("sign access token: %w", err)
	}
	refresh := rand.Text()
	err = s.store.CreateRefreshToken(ctx, NewRefreshToken{
		UserID: owner.ID, FamilyID: familyID, Hash: hashToken(refresh), ExpiresAt: now.Add(s.cfg.RefreshTTL),
	})
	if err != nil {
		return Session{}, fmt.Errorf("store refresh token: %w", err)
	}
	return Session{AccessToken: access, AccessExpiresAt: accessExpiresAt, RefreshToken: refresh, User: owner}, nil
}

func validateEmail(email string) error {
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || len(email) > maxEmailLength {
		return fault.WithDetail(fault.ErrInvalidInput, "email must be a plain address such as name@example.com")
	}
	return nil
}

func validatePassword(password string) error {
	if len(password) < minPasswordLength || len(password) > maxPasswordLength {
		return fault.WithDetail(fault.ErrInvalidInput, "password must be 8 to 72 bytes long")
	}
	return nil
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func newFamilyID() string {
	return uuid.Must(uuid.NewV7()).String()
}

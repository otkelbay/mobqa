package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/mobqa/backend/internal/fault"
	"github.com/mobqa/backend/internal/usecase/auth"
	"github.com/mobqa/backend/internal/usecase/user"
)

const userColumns = `id, email, name, avatar_url, timezone, language, created_at, updated_at`

type userRow struct {
	id        string
	email     string
	name      string
	avatarURL *string
	timezone  string
	language  string
	createdAt time.Time
	updatedAt time.Time
}

func (r *userRow) targets() []any {
	return []any{&r.id, &r.email, &r.name, &r.avatarURL, &r.timezone, &r.language, &r.createdAt, &r.updatedAt}
}

func (r userRow) user() user.User {
	result := user.User{
		ID:        r.id,
		Email:     r.email,
		Name:      r.name,
		Timezone:  r.timezone,
		Language:  r.language,
		CreatedAt: r.createdAt,
		UpdatedAt: r.updatedAt,
	}
	if r.avatarURL != nil {
		result.AvatarURL = *r.avatarURL
	}
	return result
}

type Users struct {
	db *DB
}

func NewUsers(db *DB) *Users {
	return &Users{db: db}
}

func (u *Users) CreateUser(ctx context.Context, in auth.NewUser) (user.User, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return user.User{}, fmt.Errorf("generate user id: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	now := u.db.clock.Now()
	var row userRow
	err = u.db.pool.QueryRow(ctx, `
		INSERT INTO users (id, email, name, password_hash, timezone, language, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
		RETURNING `+userColumns,
		id, in.Email, in.Name, in.PasswordHash, in.Timezone, in.Language, now,
	).Scan(row.targets()...)
	if isUniqueViolation(err) {
		return user.User{}, fmt.Errorf("email taken: %w", fault.ErrConflict)
	}
	if err != nil {
		return user.User{}, fmt.Errorf("insert user: %w", err)
	}
	return row.user(), nil
}

func (u *Users) Credentials(ctx context.Context, email string) (auth.Credentials, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var row userRow
	var hash []byte
	err := u.db.pool.QueryRow(ctx, `SELECT `+userColumns+`, password_hash FROM users WHERE email = $1`, email).
		Scan(append(row.targets(), &hash)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.Credentials{}, fmt.Errorf("user by email: %w", fault.ErrNotFound)
	}
	if err != nil {
		return auth.Credentials{}, fmt.Errorf("select credentials: %w", err)
	}
	return auth.Credentials{User: row.user(), PasswordHash: hash}, nil
}

func (u *Users) User(ctx context.Context, id string) (user.User, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var row userRow
	err := u.db.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id).Scan(row.targets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return user.User{}, fmt.Errorf("user %s: %w", id, fault.ErrNotFound)
	}
	if err != nil {
		return user.User{}, fmt.Errorf("select user: %w", err)
	}
	return row.user(), nil
}

// UpdateUser leaves fields that are nil in the update unchanged; an empty avatar URL clears it.
func (u *Users) UpdateUser(ctx context.Context, update user.ProfileUpdate) (user.User, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var row userRow
	err := u.db.pool.QueryRow(ctx, `
		UPDATE users SET
			name = coalesce($2, name),
			avatar_url = CASE WHEN $3::text IS NULL THEN avatar_url ELSE nullif($3, '') END,
			timezone = coalesce($4, timezone),
			language = coalesce($5, language),
			updated_at = $6
		WHERE id = $1
		RETURNING `+userColumns,
		update.UserID, update.Name, update.AvatarURL, update.Timezone, update.Language, u.db.clock.Now(),
	).Scan(row.targets()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return user.User{}, fmt.Errorf("user %s: %w", update.UserID, fault.ErrNotFound)
	}
	if err != nil {
		return user.User{}, fmt.Errorf("update user: %w", err)
	}
	return row.user(), nil
}

func (u *Users) CreateRefreshToken(ctx context.Context, in auth.NewRefreshToken) error {
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("generate refresh token id: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	_, err = u.db.pool.Exec(ctx, `
		INSERT INTO refresh_tokens (id, user_id, family_id, token_hash, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		id, in.UserID, in.FamilyID, in.Hash, in.ExpiresAt, u.db.clock.Now())
	if err != nil {
		return fmt.Errorf("insert refresh token: %w", err)
	}
	return nil
}

// ConsumeRefreshToken revokes a token and reports whether it had been revoked already. The row lock
// makes two concurrent uses of one token see each other: exactly one of them gets WasRevoked false.
func (u *Users) ConsumeRefreshToken(ctx context.Context, hash []byte) (auth.ConsumedRefreshToken, error) {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	var consumed auth.ConsumedRefreshToken
	err := u.db.pool.QueryRow(ctx, `
		WITH previous AS (
			SELECT id, revoked_at FROM refresh_tokens WHERE token_hash = $1 FOR UPDATE
		)
		UPDATE refresh_tokens AS token SET revoked_at = coalesce(token.revoked_at, $2)
		FROM previous
		WHERE token.id = previous.id
		RETURNING token.user_id, token.family_id, token.expires_at, previous.revoked_at IS NOT NULL`,
		hash, u.db.clock.Now(),
	).Scan(&consumed.UserID, &consumed.FamilyID, &consumed.ExpiresAt, &consumed.WasRevoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.ConsumedRefreshToken{}, fmt.Errorf("refresh token: %w", fault.ErrNotFound)
	}
	if err != nil {
		return auth.ConsumedRefreshToken{}, fmt.Errorf("consume refresh token: %w", err)
	}
	return consumed, nil
}

func (u *Users) RevokeRefreshFamily(ctx context.Context, familyID string) error {
	ctx, cancel := context.WithTimeout(ctx, operationTimeout)
	defer cancel()
	_, err := u.db.pool.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = $2 WHERE family_id = $1 AND revoked_at IS NULL`,
		familyID, u.db.clock.Now())
	if err != nil {
		return fmt.Errorf("revoke refresh token family: %w", err)
	}
	return nil
}

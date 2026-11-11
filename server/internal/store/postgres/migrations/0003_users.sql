CREATE TABLE users (
    id            uuid PRIMARY KEY,
    email         text NOT NULL UNIQUE CHECK (email = lower(email)),
    name          text NOT NULL,
    password_hash bytea NOT NULL,
    avatar_url    text,
    timezone      text NOT NULL,
    language      text NOT NULL,
    created_at    timestamptz NOT NULL,
    updated_at    timestamptz NOT NULL
);

CREATE TABLE refresh_tokens (
    id         uuid PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    family_id  uuid NOT NULL,
    token_hash bytea NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL,
    revoked_at timestamptz
);

CREATE INDEX refresh_tokens_family ON refresh_tokens (family_id) WHERE revoked_at IS NULL;

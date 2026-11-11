CREATE TABLE projects (
    id                     uuid PRIMARY KEY,
    name                   text NOT NULL,
    description            text NOT NULL,
    platforms              text[] NOT NULL CHECK (cardinality(platforms) > 0 AND platforms <@ ARRAY['android', 'ios']),
    package_name           text NOT NULL,
    initial                text NOT NULL,
    color                  text NOT NULL,
    text_color             text NOT NULL,
    auth_profile           jsonb NOT NULL,
    created_at             timestamptz NOT NULL,
    updated_at             timestamptz NOT NULL,
    archived_at            timestamptz
);

CREATE TABLE project_members (
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role       text NOT NULL CHECK (role IN ('owner', 'admin', 'engineer', 'viewer')),
    joined_at  timestamptz NOT NULL,
    PRIMARY KEY (project_id, user_id)
);

CREATE INDEX project_members_user ON project_members (user_id);
CREATE UNIQUE INDEX project_members_one_owner ON project_members (project_id) WHERE role = 'owner';

CREATE TABLE environments (
    id         uuid PRIMARY KEY,
    project_id uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name       text NOT NULL,
    is_default boolean NOT NULL,
    position   integer NOT NULL CHECK (position >= 0),
    variables  jsonb NOT NULL,
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    UNIQUE (project_id, name)
);

CREATE UNIQUE INDEX environments_one_default ON environments (project_id) WHERE is_default;

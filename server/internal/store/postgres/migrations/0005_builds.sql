CREATE TABLE builds (
    id                 uuid PRIMARY KEY,
    project_id         uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    platform           text NOT NULL CHECK (platform IN ('android', 'ios')),
    version            text NOT NULL,
    build_number       text NOT NULL,
    file_name          text NOT NULL,
    size               bigint NOT NULL CHECK (size > 0),
    sha256             text NOT NULL,
    blob_key           text NOT NULL UNIQUE,
    uploaded_by        uuid NOT NULL REFERENCES users (id),
    status             text NOT NULL CHECK (status IN ('processing', 'ready')),
    analysis           jsonb,
    is_current         boolean NOT NULL,
    profile_name       text,
    team_id            text,
    bundle_id          text,
    profile_expires_at timestamptz,
    uploaded_at        timestamptz NOT NULL,
    CHECK ((status = 'ready') = (analysis IS NOT NULL))
);

CREATE INDEX builds_project ON builds (project_id, platform, uploaded_at DESC);
CREATE UNIQUE INDEX builds_one_current ON builds (project_id, platform) WHERE is_current;

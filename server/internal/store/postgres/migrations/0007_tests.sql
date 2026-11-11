CREATE TABLE tests (
    id              uuid PRIMARY KEY,
    project_id      uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    platform        text NOT NULL CHECK (platform IN ('android', 'ios')),
    priority        text NOT NULL CHECK (priority IN ('critical', 'high', 'medium', 'low')),
    tags            text[] NOT NULL,
    source          text NOT NULL CHECK (source IN ('manual', 'generated', 'duplicate')),
    current_version integer NOT NULL CHECK (current_version > 0),
    created_by      uuid NOT NULL REFERENCES users (id),
    updated_by      uuid NOT NULL REFERENCES users (id),
    created_at      timestamptz NOT NULL,
    updated_at      timestamptz NOT NULL
);

CREATE INDEX tests_project ON tests (project_id, updated_at DESC);
CREATE INDEX tests_tags ON tests USING gin (tags);

CREATE TABLE test_versions (
    test_id       uuid NOT NULL REFERENCES tests (id) ON DELETE CASCADE,
    version       integer NOT NULL CHECK (version > 0),
    title         text NOT NULL,
    description   text NOT NULL,
    steps         jsonb NOT NULL CHECK (jsonb_typeof(steps) = 'array'),
    change_source text NOT NULL CHECK (change_source IN ('manual', 'agent', 'generate', 'restore')),
    note          text NOT NULL,
    created_by    uuid NOT NULL REFERENCES users (id),
    created_at    timestamptz NOT NULL,
    PRIMARY KEY (test_id, version)
);

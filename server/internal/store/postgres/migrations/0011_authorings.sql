-- An authoring snapshots its device and environment by name, like run results, so it still reads after
-- they are removed; device_id and hub_id are deliberately not foreign keys.
CREATE TABLE authorings (
    id               uuid PRIMARY KEY,
    project_id       uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    test_id          uuid NOT NULL REFERENCES tests (id) ON DELETE CASCADE,
    test_version     integer NOT NULL CHECK (test_version > 0),
    platform         text NOT NULL CHECK (platform IN ('android', 'ios')),
    device_id        uuid NOT NULL,
    device_name      text NOT NULL,
    device_serial    text NOT NULL,
    hub_id           uuid NOT NULL,
    environment_id   uuid REFERENCES environments (id) ON DELETE SET NULL,
    environment_name text NOT NULL,
    status           text NOT NULL CHECK (status IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    script           jsonb NOT NULL CHECK (jsonb_typeof(script) = 'array'),
    log              jsonb NOT NULL CHECK (jsonb_typeof(log) = 'array'),
    failure          text NOT NULL,
    job_id           uuid REFERENCES jobs (id) ON DELETE SET NULL,
    created_by       uuid NOT NULL REFERENCES users (id),
    created_at       timestamptz NOT NULL,
    started_at       timestamptz,
    finished_at      timestamptz,
    FOREIGN KEY (test_id, test_version) REFERENCES test_versions (test_id, version) ON DELETE CASCADE,
    CHECK ((finished_at IS NOT NULL) = (status IN ('succeeded', 'failed', 'cancelled')))
);

CREATE INDEX authorings_test ON authorings (test_id, created_at DESC, id DESC);

-- A test is authored by one session at a time.
CREATE UNIQUE INDEX authorings_active ON authorings (test_id) WHERE status IN ('queued', 'running');

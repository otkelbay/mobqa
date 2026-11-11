CREATE TABLE runs (
    id               uuid PRIMARY KEY,
    project_id       uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    platform         text NOT NULL CHECK (platform IN ('android', 'ios')),
    build_id         uuid REFERENCES builds (id) ON DELETE SET NULL,
    build_label      text NOT NULL,
    environment_id   uuid REFERENCES environments (id) ON DELETE SET NULL,
    environment_name text NOT NULL,
    status           text NOT NULL CHECK (status IN ('queued', 'running', 'passed', 'failed', 'cancelled')),
    retries          integer NOT NULL CHECK (retries BETWEEN 0 AND 2),
    created_by       uuid NOT NULL REFERENCES users (id),
    created_at       timestamptz NOT NULL,
    started_at       timestamptz,
    finished_at      timestamptz,
    CHECK ((finished_at IS NOT NULL) = (status IN ('passed', 'failed', 'cancelled')))
);

CREATE INDEX runs_project ON runs (project_id, created_at DESC, id DESC);

-- Results snapshot the test version and the device, so a run's history survives later edits and the
-- removal of tests, devices and hubs. device_id and hub_id are deliberately not foreign keys.
CREATE TABLE run_results (
    id            uuid PRIMARY KEY,
    run_id        uuid NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    position      integer NOT NULL CHECK (position >= 0),
    test_id       uuid REFERENCES tests (id) ON DELETE SET NULL,
    test_version  integer NOT NULL CHECK (test_version > 0),
    title         text NOT NULL,
    steps         jsonb NOT NULL CHECK (jsonb_typeof(steps) = 'array'),
    device_id     uuid NOT NULL,
    device_name   text NOT NULL,
    device_serial text NOT NULL,
    hub_id        uuid NOT NULL,
    job_id        uuid REFERENCES jobs (id) ON DELETE SET NULL,
    status        text NOT NULL
        CHECK (status IN ('queued', 'running', 'passed', 'flaky', 'failed', 'error', 'cancelled')),
    attempts      integer NOT NULL CHECK (attempts >= 0),
    failed_step   integer CHECK (failed_step > 0),
    error         text NOT NULL,
    started_at    timestamptz,
    finished_at   timestamptz,
    UNIQUE (run_id, device_id, position)
);

CREATE INDEX run_results_run ON run_results (run_id, device_name, position);

CREATE TABLE run_attempts (
    result_id   uuid NOT NULL REFERENCES run_results (id) ON DELETE CASCADE,
    number      integer NOT NULL CHECK (number > 0),
    status      text NOT NULL CHECK (status IN ('passed', 'failed', 'error')),
    failed_step integer CHECK (failed_step > 0),
    error       text NOT NULL,
    steps       jsonb NOT NULL CHECK (jsonb_typeof(steps) = 'array'),
    started_at  timestamptz NOT NULL,
    finished_at timestamptz NOT NULL,
    PRIMARY KEY (result_id, number)
);

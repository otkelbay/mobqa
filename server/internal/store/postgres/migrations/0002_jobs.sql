CREATE TABLE jobs (
    id                  uuid PRIMARY KEY,
    kind                text NOT NULL,
    payload             jsonb NOT NULL,
    state               text NOT NULL CHECK (state IN ('queued', 'running', 'succeeded', 'failed', 'cancelled')),
    attempt             integer NOT NULL CHECK (attempt >= 0),
    max_attempts        integer NOT NULL CHECK (max_attempts > 0),
    run_after           timestamptz NOT NULL,
    lease_owner         text,
    lease_until         timestamptz,
    cancel_requested_at timestamptz,
    last_error          text,
    created_at          timestamptz NOT NULL,
    updated_at          timestamptz NOT NULL,
    finished_at         timestamptz,
    CHECK ((state = 'running') = (lease_owner IS NOT NULL AND lease_until IS NOT NULL))
);

CREATE INDEX jobs_runnable ON jobs (kind, run_after) WHERE state = 'queued';
CREATE INDEX jobs_leased ON jobs (kind, lease_until) WHERE state = 'running';

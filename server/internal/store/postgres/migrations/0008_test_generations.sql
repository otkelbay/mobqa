CREATE TABLE test_generations (
    id          uuid PRIMARY KEY,
    project_id  uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    platform    text NOT NULL CHECK (platform IN ('android', 'ios')),
    brief       text NOT NULL,
    status      text NOT NULL CHECK (status IN ('pending', 'succeeded', 'failed')),
    test_ids    uuid[] NOT NULL,
    failure     text NOT NULL,
    created_by  uuid NOT NULL REFERENCES users (id),
    created_at  timestamptz NOT NULL,
    finished_at timestamptz,
    CHECK ((status = 'pending') = (finished_at IS NULL))
);

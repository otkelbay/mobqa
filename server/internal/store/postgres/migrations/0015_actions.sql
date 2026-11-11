CREATE TABLE actions (
    id              uuid PRIMARY KEY,
    project_id      uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name            text NOT NULL,
    description     text NOT NULL,
    method          text NOT NULL CHECK (method IN ('GET', 'POST', 'PUT', 'PATCH', 'DELETE')),
    url             text NOT NULL,
    headers         jsonb NOT NULL CHECK (jsonb_typeof(headers) = 'array'),
    body            text NOT NULL,
    expected_status integer NOT NULL CHECK (expected_status BETWEEN 100 AND 599),
    extract         jsonb NOT NULL CHECK (jsonb_typeof(extract) = 'array'),
    created_at      timestamptz NOT NULL,
    updated_at      timestamptz NOT NULL,
    UNIQUE (project_id, name)
);

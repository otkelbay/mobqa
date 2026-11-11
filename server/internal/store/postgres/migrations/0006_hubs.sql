CREATE TABLE hubs (
    id           uuid PRIMARY KEY,
    project_id   uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    name         text NOT NULL,
    token_hash   bytea NOT NULL UNIQUE,
    version      text NOT NULL,
    created_by   uuid NOT NULL REFERENCES users (id),
    created_at   timestamptz NOT NULL,
    last_seen_at timestamptz,
    online_until timestamptz,
    UNIQUE (project_id, name)
);

CREATE TABLE devices (
    id            uuid PRIMARY KEY,
    hub_id        uuid NOT NULL REFERENCES hubs (id) ON DELETE CASCADE,
    serial        text NOT NULL,
    name          text NOT NULL,
    model         text NOT NULL,
    platform      text NOT NULL CHECK (platform IN ('android', 'ios')),
    os_version    text NOT NULL,
    physical      boolean NOT NULL,
    state         text NOT NULL CHECK (state IN ('connected', 'unauthorized', 'disconnected')),
    first_seen_at timestamptz NOT NULL,
    last_seen_at  timestamptz NOT NULL,
    UNIQUE (hub_id, serial)
);

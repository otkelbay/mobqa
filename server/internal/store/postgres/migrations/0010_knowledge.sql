CREATE TABLE knowledge_notes (
    id               uuid PRIMARY KEY,
    project_id       uuid NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    kind             text NOT NULL CHECK (kind IN ('note', 'flow', 'gotcha', 'selector')),
    title            text NOT NULL,
    content          text NOT NULL,
    source           text NOT NULL CHECK (source IN ('user', 'agent')),
    pinned           boolean NOT NULL,
    revision         integer NOT NULL CHECK (revision > 0),
    indexed_revision integer NOT NULL CHECK (indexed_revision BETWEEN 0 AND revision),
    created_by       uuid NOT NULL REFERENCES users (id),
    created_at       timestamptz NOT NULL,
    updated_at       timestamptz NOT NULL
);

CREATE INDEX knowledge_notes_project ON knowledge_notes (project_id, updated_at DESC);
CREATE INDEX knowledge_notes_pinned ON knowledge_notes (project_id, updated_at DESC) WHERE pinned;

CREATE TABLE knowledge_chunks (
    note_id    uuid NOT NULL REFERENCES knowledge_notes (id) ON DELETE CASCADE,
    position   integer NOT NULL CHECK (position >= 0),
    project_id uuid NOT NULL,
    content    text NOT NULL,
    embedding  vector(1536) NOT NULL,
    PRIMARY KEY (note_id, position)
);

CREATE INDEX knowledge_chunks_embedding ON knowledge_chunks USING hnsw (embedding vector_cosine_ops);

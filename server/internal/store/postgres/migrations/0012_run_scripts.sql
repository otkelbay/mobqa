ALTER TABLE run_results ADD COLUMN script jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(script) = 'array');

CREATE INDEX authorings_succeeded ON authorings (test_id, test_version, finished_at DESC, id DESC)
    WHERE status = 'succeeded';

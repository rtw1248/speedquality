ALTER TABLE sessions
  ADD COLUMN community_node_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS sessions_community_node_idx
  ON sessions (community_node_id, expires_at);

CREATE TABLE IF NOT EXISTS reports (
  id TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  tested_at INTEGER,
  regions TEXT NOT NULL,
  mode TEXT NOT NULL CHECK (mode = 's'),
  ip_mode TEXT NOT NULL CHECK (ip_mode IN ('v4', 'v6')),
  source_ip_masked TEXT NOT NULL DEFAULT '',
  speed_url TEXT NOT NULL DEFAULT '',
  speed_text TEXT NOT NULL DEFAULT '',
  speed_data TEXT NOT NULL DEFAULT '',
  duration_seconds INTEGER NOT NULL CHECK (duration_seconds = 5),
  target_mbps INTEGER NOT NULL CHECK (target_mbps IN (100, 200, 400)),
  traffic_rx_bytes INTEGER CHECK (traffic_rx_bytes IS NULL OR traffic_rx_bytes >= 0),
  traffic_tx_bytes INTEGER CHECK (traffic_tx_bytes IS NULL OR traffic_tx_bytes >= 0),
  nq_url TEXT NOT NULL DEFAULT '',
  nq_tested_at INTEGER,
  nq_time_source TEXT NOT NULL DEFAULT '',
  time_gap_seconds INTEGER,
  nq_identity_reason TEXT NOT NULL DEFAULT '',
  bind_status TEXT NOT NULL CHECK (
    bind_status IN ('standalone', 'verified', 'verified_stale', 'verified_time_unknown')
  ),
  version TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS reports_expires_at_idx ON reports (expires_at);

CREATE TABLE IF NOT EXISTS usage_counters (
  key TEXT PRIMARY KEY,
  count INTEGER NOT NULL CHECK (count >= 0),
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS rate_limits (
  day TEXT NOT NULL,
  client_hash TEXT NOT NULL,
  count INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (day, client_hash)
);

CREATE INDEX IF NOT EXISTS rate_limits_updated_at_idx ON rate_limits (updated_at);

CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  client_hash TEXT NOT NULL,
  regions TEXT NOT NULL,
  mode TEXT NOT NULL CHECK (mode = 's'),
  ip_mode TEXT NOT NULL CHECK (ip_mode IN ('v4', 'v6')),
  duration_seconds INTEGER NOT NULL CHECK (duration_seconds = 5),
  target_mbps INTEGER NOT NULL CHECK (target_mbps IN (100, 200, 400)),
  lease_limit INTEGER NOT NULL CHECK (lease_limit BETWEEN 1 AND 136),
  completed_at INTEGER
);

CREATE INDEX IF NOT EXISTS sessions_expires_at_idx ON sessions (expires_at);

CREATE TABLE IF NOT EXISTS session_leases (
  token_hash TEXT NOT NULL,
  region TEXT NOT NULL,
  family TEXT NOT NULL CHECK (family IN ('v4', 'v6')),
  attempts INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (token_hash, region, family),
  FOREIGN KEY (token_hash) REFERENCES sessions(token_hash) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS session_rate_limits (
  day TEXT NOT NULL,
  client_hash TEXT NOT NULL,
  count INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (day, client_hash)
);

CREATE INDEX IF NOT EXISTS session_rate_limits_updated_at_idx
  ON session_rate_limits (updated_at);

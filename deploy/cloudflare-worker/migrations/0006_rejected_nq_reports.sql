-- A rejected/unreadable NQ candidate must not prevent saving the SQ results.
-- SQLite requires rebuilding the table to extend this CHECK constraint.
-- The migration runner executes the copy and swap in one transaction.
CREATE TABLE reports_with_rejected_nq (
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
    bind_status IN ('standalone', 'verified', 'verified_stale', 'verified_time_unknown', 'mismatch', 'unverified')
  ),
  version TEXT NOT NULL DEFAULT '',
  source_asn INTEGER CHECK (source_asn BETWEEN 1 AND 4294967295),
  source_as_organization TEXT NOT NULL DEFAULT '',
  source_identity_hashes TEXT NOT NULL DEFAULT '[]',
  sq_source_id TEXT NOT NULL DEFAULT '',
  sq_source_tested_at INTEGER
);

INSERT INTO reports_with_rejected_nq (
  id, created_at, expires_at, tested_at, regions, mode, ip_mode,
  source_ip_masked, speed_url, speed_text, speed_data, duration_seconds, target_mbps,
  traffic_rx_bytes, traffic_tx_bytes, nq_url, nq_tested_at, nq_time_source,
  time_gap_seconds, nq_identity_reason, bind_status, version,
  source_asn, source_as_organization, source_identity_hashes, sq_source_id, sq_source_tested_at
)
SELECT
  id, created_at, expires_at, tested_at, regions, mode, ip_mode,
  source_ip_masked, speed_url, speed_text, speed_data, duration_seconds, target_mbps,
  traffic_rx_bytes, traffic_tx_bytes, nq_url, nq_tested_at, nq_time_source,
  time_gap_seconds, nq_identity_reason, bind_status, version,
  source_asn, source_as_organization, source_identity_hashes, sq_source_id, sq_source_tested_at
FROM reports;

DROP TABLE reports;
ALTER TABLE reports_with_rejected_nq RENAME TO reports;
CREATE INDEX reports_expires_at_idx ON reports (expires_at);

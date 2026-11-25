-- Web New task starts (broker-confirmed handoff): the broker files a handoff's issue through the
-- factory App, so the App applies factory/ready; this row records which signed-in human the broker
-- started it for, and the factory's intake, reading it through the system API, accepts the App's
-- label on that issue alone. One row per filed issue, never rewritten; retention deletes expired
-- rows.
CREATE TABLE handoff_starts (
  repository text        NOT NULL CHECK (length(repository) BETWEEN 3 AND 140),
  issue      integer     NOT NULL CHECK (issue > 0),
  handoff    text        NOT NULL CHECK (length(handoff) BETWEEN 1 AND 128),
  principal  text        NOT NULL CHECK (length(principal) BETWEEN 1 AND 256),
  sub        text        NOT NULL CHECK (length(sub) BETWEEN 1 AND 128),
  login      text        NOT NULL CHECK (login ~ '^[A-Za-z0-9][A-Za-z0-9_-]{0,99}$'),
  labels     text[]      NOT NULL,
  started_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  PRIMARY KEY (repository, issue)
);
CREATE INDEX handoff_starts_expires ON handoff_starts (expires_at);

GRANT SELECT, INSERT ON handoff_starts TO rooms_broker;
GRANT DELETE ON handoff_starts TO rooms_retention;
GRANT SELECT (expires_at) ON handoff_starts TO rooms_retention;
ALTER TABLE handoff_starts ENABLE ROW LEVEL SECURITY;
CREATE POLICY broker_read_handoff_starts     ON handoff_starts FOR SELECT TO rooms_broker    USING (true);
CREATE POLICY broker_record_handoff_starts   ON handoff_starts FOR INSERT TO rooms_broker    WITH CHECK (true);
CREATE POLICY retention_read_handoff_starts  ON handoff_starts FOR SELECT TO rooms_retention USING (expires_at <= now());
CREATE POLICY retention_purge_handoff_starts ON handoff_starts FOR DELETE TO rooms_retention USING (expires_at <= now());

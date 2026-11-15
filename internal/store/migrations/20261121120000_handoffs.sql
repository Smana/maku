-- Stored handoff plans (UX2): the broker holds each plan it shows for 24 hours, bound to the
-- principal that asked for it, so no client holds or edits a plan envelope. Retention deletes
-- expired rows without reading their plans or arguments.
CREATE TABLE handoffs (
  id         text        PRIMARY KEY CHECK (length(id) BETWEEN 1 AND 128),
  principal  text        NOT NULL CHECK (length(principal) BETWEEN 1 AND 256),
  args       jsonb       NOT NULL,
  plan       jsonb       NOT NULL,
  hash       text        NOT NULL CHECK (length(hash) BETWEEN 1 AND 128),
  created_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL
);
CREATE INDEX handoffs_expires ON handoffs (expires_at);

GRANT SELECT, INSERT, UPDATE, DELETE ON handoffs TO rooms_broker;
GRANT DELETE ON handoffs TO rooms_retention;
GRANT SELECT (expires_at) ON handoffs TO rooms_retention;
ALTER TABLE handoffs ENABLE ROW LEVEL SECURITY;
CREATE POLICY broker_handoffs          ON handoffs FOR ALL    TO rooms_broker    USING (true) WITH CHECK (true);
CREATE POLICY retention_read_handoffs  ON handoffs FOR SELECT TO rooms_retention USING (expires_at <= now());
CREATE POLICY retention_purge_handoffs ON handoffs FOR DELETE TO rooms_retention USING (expires_at <= now());

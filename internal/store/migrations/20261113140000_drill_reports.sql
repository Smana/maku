-- Drill reports (untrusted-input hardening HR38, R6): each injection drill the factory scored, once
-- per id. The factory is the only writer, through the broker's system route (system:factory): the
-- drill planter only posts a manifest to the factory, so no submitter can forge a report. Readiness
-- reads the newest passing report of the running Maku version and hardening configuration hash.
-- The report is the scorer's JSON: counts, scenario ids, roles and reasons, never planted text.
CREATE TABLE drill_reports (
  drill_id    text        PRIMARY KEY CHECK (drill_id ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
  version     text        NOT NULL CHECK (version ~ '^[A-Za-z0-9._+-]{1,64}$'),
  config_hash text        NOT NULL CHECK (config_hash ~ '^[0-9a-f]{64}$'),
  at          timestamptz NOT NULL,
  pass        boolean     NOT NULL,
  report      jsonb       NOT NULL CHECK (octet_length(report::text) <= 262144),
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX drill_reports_last_pass ON drill_reports (version, config_hash, at DESC) WHERE pass;

-- The broker stores and reads reports; it cannot rewrite or delete one. A report is a weekly row of
-- counts, kept: readiness only ever reads the newest pass.
GRANT SELECT, INSERT ON drill_reports TO rooms_broker;
ALTER TABLE drill_reports ENABLE ROW LEVEL SECURITY;
CREATE POLICY broker_drill_reports ON drill_reports FOR ALL TO rooms_broker USING (true) WITH CHECK (true);

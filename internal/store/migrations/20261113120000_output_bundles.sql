-- Push bundles (untrusted-input hardening HR22): the git bundle a run's output_push carries, kept
-- beside the room log, which records only its digest and size (output_requested). Keyed by run and
-- digest: a retried push of the same bytes is the same row. At most 8 MiB, the design's cap. The
-- factory reads a bundle back through the broker's system route to inspect and push it.
CREATE TABLE output_bundles (
  room_id    text        NOT NULL REFERENCES rooms (room_id),
  run_id     text        NOT NULL CHECK (run_id ~ '^[a-z2-7]{8}$'),
  digest     text        NOT NULL CHECK (digest ~ '^[0-9a-f]{64}$'),
  size       integer     NOT NULL CHECK (size BETWEEN 1 AND 8388608),
  bundle     bytea       NOT NULL CHECK (octet_length(bundle) = size),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (run_id, digest)
);
CREATE INDEX output_bundles_created ON output_bundles (created_at);
CREATE INDEX output_bundles_room ON output_bundles (room_id);

-- A sealed room takes no new bundle, as it takes no new event; a bundle is never rewritten.
CREATE FUNCTION output_bundles_open_room() RETURNS trigger
  LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF (SELECT sealed FROM public.rooms WHERE room_id = NEW.room_id) THEN
    RAISE EXCEPTION 'room log: room % is sealed and takes no bundle', NEW.room_id USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $$;

CREATE TRIGGER output_bundles_open_room BEFORE INSERT ON output_bundles
  FOR EACH ROW EXECUTE FUNCTION output_bundles_open_room();

-- The sweep (SweepBundles): bundles older than the cutoff go, unless a pending approval still binds
-- one (20261113130000_approval_digest.sql replaces this function with that join). A function, so
-- retention deletes bundles without reading their bytes. It runs as its owner, so the caller's
-- cutoff is floored at the 14-day retention: no caller sweeps a younger bundle.
CREATE FUNCTION sweep_output_bundles(p_before timestamptz) RETURNS bigint
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE swept bigint;
BEGIN
  DELETE FROM public.output_bundles WHERE created_at < least(p_before, now() - interval '14 days');
  GET DIAGNOSTICS swept = ROW_COUNT;
  RETURN swept;
END $$;
REVOKE ALL ON FUNCTION sweep_output_bundles(timestamptz) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION sweep_output_bundles(timestamptz) TO rooms_retention;

-- The broker stores and reads bundles; it cannot rewrite or delete one. Retention deletes an
-- expired room's bundles with the room, and the sweep's old ones through the function.
GRANT SELECT, INSERT ON output_bundles TO rooms_broker;
GRANT DELETE ON output_bundles TO rooms_retention;
GRANT SELECT (room_id) ON output_bundles TO rooms_retention;
ALTER TABLE output_bundles ENABLE ROW LEVEL SECURITY;
CREATE POLICY broker_output_bundles          ON output_bundles FOR ALL    TO rooms_broker    USING (true) WITH CHECK (true);
CREATE POLICY retention_read_output_bundles  ON output_bundles FOR SELECT TO rooms_retention
  USING (room_id IN (SELECT room_id FROM rooms WHERE sealed AND closed_at < now() - retention));
CREATE POLICY retention_purge_output_bundles ON output_bundles FOR DELETE TO rooms_retention
  USING (room_id IN (SELECT room_id FROM rooms WHERE sealed AND closed_at < now() - retention));

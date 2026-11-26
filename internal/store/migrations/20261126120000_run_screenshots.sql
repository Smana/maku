-- Run screenshots (harness screenshots phase B, C13): the images the browser gate captured, which
-- the run's bridge relays and the broker stores re-encoded, beside the room log. The log records
-- only a screenshot event with the meta, never the pixels. Copied from output_bundles' shape: a
-- sealed room takes none, the broker cannot rewrite or delete one, and retention deletes them with
-- the room (no shorter sweep: the gallery is the record of what the agent saw, OQ4).
--
-- source 'browser' is a run's screenshot, keyed by its run and the gate's number n. source
-- 'request' is an issue's image (B3), which belongs to no run: its run_id is '' so the key stays
-- (room_id, run_id, n). A digest is not unique: an unchanged page shot twice re-encodes to the
-- same bytes, and both shots are kept; a read by digest returns either, the same image.
CREATE TABLE run_screenshots (
  room_id      text        NOT NULL REFERENCES rooms (room_id),
  run_id       text        NOT NULL,
  n            integer     NOT NULL CHECK (n BETWEEN 1 AND 100),
  digest       text        NOT NULL CHECK (digest ~ '^[0-9a-f]{64}$'),
  mime         text        NOT NULL CHECK (mime IN ('image/png', 'image/jpeg')),
  width        integer     NOT NULL CHECK (width BETWEEN 1 AND 1920),
  height       integer     NOT NULL CHECK (height BETWEEN 1 AND 1920),
  size         integer     NOT NULL CHECK (size BETWEEN 1 AND 1048576),
  bytes        bytea       NOT NULL CHECK (octet_length(bytes) = size),
  source       text        NOT NULL CHECK (source IN ('browser', 'request')),
  page_url     text        NOT NULL DEFAULT '' CHECK (length(page_url) <= 2048),
  vision       boolean     NOT NULL DEFAULT false,
  published_at timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (room_id, run_id, n),
  CHECK ((source = 'browser' AND run_id ~ '^[a-z2-7]{8}$') OR (source = 'request' AND run_id = ''))
);
CREATE INDEX run_screenshots_digest ON run_screenshots (room_id, digest);

-- A sealed room takes no new screenshot, as it takes no new event.
CREATE FUNCTION run_screenshots_open_room() RETURNS trigger
  LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF (SELECT sealed FROM public.rooms WHERE room_id = NEW.room_id) THEN
    RAISE EXCEPTION 'room log: room % is sealed and takes no screenshot', NEW.room_id USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $$;

CREATE TRIGGER run_screenshots_open_room BEFORE INSERT ON run_screenshots
  FOR EACH ROW EXECUTE FUNCTION run_screenshots_open_room();

-- The broker stores and reads screenshots and may mark one published (phase B publishing); it
-- cannot rewrite or delete one. Retention deletes an expired room's screenshots with the room.
GRANT SELECT, INSERT ON run_screenshots TO rooms_broker;
GRANT UPDATE (published_at) ON run_screenshots TO rooms_broker;
GRANT DELETE ON run_screenshots TO rooms_retention;
GRANT SELECT (room_id) ON run_screenshots TO rooms_retention;
ALTER TABLE run_screenshots ENABLE ROW LEVEL SECURITY;
CREATE POLICY broker_run_screenshots          ON run_screenshots FOR ALL    TO rooms_broker    USING (true) WITH CHECK (true);
CREATE POLICY retention_read_run_screenshots  ON run_screenshots FOR SELECT TO rooms_retention
  USING (room_id IN (SELECT room_id FROM rooms WHERE sealed AND closed_at < now() - retention));
CREATE POLICY retention_purge_run_screenshots ON run_screenshots FOR DELETE TO rooms_retention
  USING (room_id IN (SELECT room_id FROM rooms WHERE sealed AND closed_at < now() - retention));

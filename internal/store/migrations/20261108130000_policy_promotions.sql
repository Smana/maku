-- Merge policy stage 6, the promotion workflow (merge policy self-service design, "Storage"): the
-- audit of each request to promote a shadow class to live and of each operator exception below
-- the evidence minimum. Git owns the class's mode; these rows record who asked, on what evidence,
-- and what became of the request. Insert, then one constrained move to a terminal state.

-- An exception lowers the clean decisions one class needs (MP7), nothing else: bad outcomes and
-- the drill still block. A maintainer asks with a written justification; an operator approves or
-- refuses it once, and an approval lasts until expires_at.
CREATE TABLE operator_exceptions (
  exception_id  text        PRIMARY KEY CHECK (exception_id ~ '^[a-z2-7]{8}$'),
  repository    text        NOT NULL CHECK (repository ~ '^[A-Za-z0-9-]{1,39}/[A-Za-z0-9._-]{1,100}$'),
  class         text        NOT NULL CHECK (class ~ '^[a-z0-9]([-a-z0-9]{0,38}[a-z0-9])?$'),
  minimum       integer     NOT NULL CHECK (minimum BETWEEN 1 AND 1000),
  justification text        NOT NULL CHECK (length(justification) BETWEEN 20 AND 2000),
  requested_by  text        NOT NULL CHECK (requested_by ~ '^[A-Za-z0-9][A-Za-z0-9-]{0,38}$'),
  requested_at  timestamptz NOT NULL DEFAULT now(),
  state         text        NOT NULL CHECK (state IN ('requested', 'approved', 'refused')),
  decided_by    text        CHECK (decided_by IS NULL OR decided_by ~ '^[A-Za-z0-9][A-Za-z0-9-]{0,38}$'),
  decided_at    timestamptz,
  note          text        CHECK (note IS NULL OR length(note) <= 2000),
  expires_at    timestamptz
);
CREATE INDEX operator_exceptions_repository ON operator_exceptions (repository, requested_at DESC);

CREATE FUNCTION operator_exceptions_move_once() RETURNS trigger
  LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' AND (NEW.state <> 'requested' OR NEW.decided_by IS NOT NULL OR NEW.decided_at IS NOT NULL
      OR NEW.note IS NOT NULL OR NEW.expires_at IS NOT NULL OR NEW.requested_at <> now()) THEN
    RAISE EXCEPTION 'policy: exception % enters requested, undecided, at now()', NEW.exception_id USING ERRCODE = 'check_violation';
  END IF;
  IF TG_OP = 'UPDATE' AND (OLD.state <> 'requested' OR NEW.state = 'requested') THEN
    RAISE EXCEPTION 'policy: exception % moves once, out of requested (% to %)', OLD.exception_id, OLD.state, NEW.state
      USING ERRCODE = 'check_violation';
  END IF;
  IF TG_OP = 'UPDATE' AND (NEW.decided_by IS NULL OR NEW.decided_at IS DISTINCT FROM now()
      OR (NEW.state = 'approved') <> (NEW.expires_at IS NOT NULL AND NEW.expires_at > now())) THEN
    RAISE EXCEPTION 'policy: exception % is decided at now(), by someone, and only an approval expires', OLD.exception_id
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $$;

CREATE TRIGGER operator_exceptions_move_once BEFORE INSERT OR UPDATE ON operator_exceptions
  FOR EACH ROW EXECUTE FUNCTION operator_exceptions_move_once();

-- A promotion request: the evidence it was granted on, the minimum applied (the configured one,
-- or an approved exception's), and who asked. requested moves to pr_opened when the maintainer
-- names their pull request, then to merged or abandoned; requested may also be abandoned. One open
-- request per class at a time.
CREATE TABLE policy_promotions (
  promotion_id  text        PRIMARY KEY CHECK (promotion_id ~ '^[a-z2-7]{8}$'),
  repository    text        NOT NULL CHECK (repository ~ '^[A-Za-z0-9-]{1,39}/[A-Za-z0-9._-]{1,100}$'),
  class         text        NOT NULL CHECK (class ~ '^[a-z0-9]([-a-z0-9]{0,38}[a-z0-9])?$'),
  threshold     integer     NOT NULL CHECK (threshold BETWEEN 1 AND 1000),
  evidence      jsonb       NOT NULL CHECK (jsonb_typeof(evidence) = 'object' AND octet_length(evidence::text) <= 65536),
  exception_id  text        REFERENCES operator_exceptions (exception_id),
  requested_by  text        NOT NULL CHECK (requested_by ~ '^[A-Za-z0-9][A-Za-z0-9-]{0,38}$'),
  requested_at  timestamptz NOT NULL DEFAULT now(),
  state         text        NOT NULL CHECK (state IN ('requested', 'pr_opened', 'merged', 'abandoned')),
  pull_request  integer     CHECK (pull_request IS NULL OR pull_request > 0),
  closed_by     text        CHECK (closed_by IS NULL OR closed_by ~ '^[A-Za-z0-9][A-Za-z0-9-]{0,38}$'),
  closed_at     timestamptz
);
CREATE INDEX policy_promotions_repository ON policy_promotions (repository, requested_at DESC);
CREATE UNIQUE INDEX policy_promotions_one_open ON policy_promotions (repository, class)
  WHERE state IN ('requested', 'pr_opened');

CREATE FUNCTION policy_promotions_move() RETURNS trigger
  LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' AND (NEW.state <> 'requested' OR NEW.pull_request IS NOT NULL OR NEW.closed_by IS NOT NULL
      OR NEW.closed_at IS NOT NULL OR NEW.requested_at <> now()) THEN
    RAISE EXCEPTION 'policy: promotion % enters requested, open, at now()', NEW.promotion_id USING ERRCODE = 'check_violation';
  END IF;
  -- An exception carries a promotion only while approved and unexpired, for its own class.
  IF TG_OP = 'INSERT' AND NEW.exception_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM public.operator_exceptions e
      WHERE e.exception_id = NEW.exception_id AND e.state = 'approved' AND e.expires_at > now()
      AND e.repository = NEW.repository AND e.class = NEW.class AND e.minimum = NEW.threshold) THEN
    RAISE EXCEPTION 'policy: promotion % names no approved exception of its class', NEW.promotion_id USING ERRCODE = 'check_violation';
  END IF;
  IF TG_OP = 'UPDATE' AND NOT ((OLD.state = 'requested' AND NEW.state IN ('pr_opened', 'merged', 'abandoned'))
      OR (OLD.state = 'pr_opened' AND NEW.state IN ('merged', 'abandoned'))) THEN
    RAISE EXCEPTION 'policy: promotion % cannot move % to %', OLD.promotion_id, OLD.state, NEW.state USING ERRCODE = 'check_violation';
  END IF;
  IF TG_OP = 'UPDATE' AND (NEW.closed_by IS NULL OR NEW.closed_at IS DISTINCT FROM now()
      OR (NEW.state IN ('pr_opened', 'merged') AND NEW.pull_request IS NULL)
      OR (OLD.pull_request IS NOT NULL AND NEW.pull_request IS DISTINCT FROM OLD.pull_request)) THEN
    RAISE EXCEPTION 'policy: promotion % moves at now(), by someone, and keeps the pull request it names', OLD.promotion_id
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $$;

CREATE TRIGGER policy_promotions_move BEFORE INSERT OR UPDATE ON policy_promotions
  FOR EACH ROW EXECUTE FUNCTION policy_promotions_move();

-- The broker records and moves them, never rewrites what was asked or on what evidence. No
-- retention role reads them: they are the audit record, kept with the Git history they explain.
GRANT SELECT, INSERT ON operator_exceptions, policy_promotions TO rooms_broker;
GRANT UPDATE (state, decided_by, decided_at, note, expires_at) ON operator_exceptions TO rooms_broker;
GRANT UPDATE (state, pull_request, closed_by, closed_at) ON policy_promotions TO rooms_broker;
ALTER TABLE operator_exceptions ENABLE ROW LEVEL SECURITY;
ALTER TABLE policy_promotions ENABLE ROW LEVEL SECURITY;
CREATE POLICY broker_operator_exceptions ON operator_exceptions FOR ALL TO rooms_broker USING (true) WITH CHECK (true);
CREATE POLICY broker_policy_promotions   ON policy_promotions   FOR ALL TO rooms_broker USING (true) WITH CHECK (true);

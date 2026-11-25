-- A factory-opened promotion names its branch and URL. Opening the pull request is not closing
-- the audit record: only a served live binding or an unmerged close ends it.
ALTER TABLE policy_promotions
  ADD COLUMN pull_request_url text CHECK (pull_request_url IS NULL OR length(pull_request_url) <= 2048),
  ADD COLUMN branch text CHECK (branch IS NULL OR branch ~ '^factory/promote-[A-Za-z0-9._/-]{1,240}$');

CREATE OR REPLACE FUNCTION policy_promotions_move() RETURNS trigger
  LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' AND (NEW.state <> 'requested' OR NEW.pull_request IS NOT NULL OR NEW.closed_by IS NOT NULL
      OR NEW.closed_at IS NOT NULL OR NEW.requested_at <> now()) THEN
    RAISE EXCEPTION 'policy: promotion % enters requested, open, at now()', NEW.promotion_id USING ERRCODE = 'check_violation';
  END IF;
  IF TG_OP = 'INSERT' AND NEW.exception_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM public.operator_exceptions e
      WHERE e.exception_id = NEW.exception_id AND e.state = 'approved' AND e.expires_at > now()
      AND e.repository = NEW.repository AND e.class = NEW.class AND e.minimum = NEW.threshold) THEN
    RAISE EXCEPTION 'policy: promotion % names no approved exception of its class', NEW.promotion_id USING ERRCODE = 'check_violation';
  END IF;
  IF TG_OP = 'UPDATE' AND NOT ((OLD.state = 'requested' AND NEW.state IN ('pr_opened', 'merged', 'abandoned'))
      OR (OLD.state = 'pr_opened' AND NEW.state IN ('merged', 'abandoned'))) THEN
    RAISE EXCEPTION 'policy: promotion % cannot move % to %', OLD.promotion_id, OLD.state, NEW.state USING ERRCODE = 'check_violation';
  END IF;
  IF TG_OP = 'UPDATE' AND ((NEW.state IN ('merged', 'abandoned') AND (NEW.closed_by IS NULL OR NEW.closed_at IS DISTINCT FROM now()))
      OR (NEW.state IN ('pr_opened', 'merged') AND NEW.pull_request IS NULL)
      OR (OLD.pull_request IS NOT NULL AND NEW.pull_request IS DISTINCT FROM OLD.pull_request)) THEN
    RAISE EXCEPTION 'policy: promotion % moves at now(), by someone, and keeps the pull request it names', OLD.promotion_id
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $$;

GRANT UPDATE (pull_request_url, branch) ON policy_promotions TO rooms_broker;

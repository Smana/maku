-- v3.x Slack push approval D5: the durable binding of a forge.push approval is the branch the run
-- may push to, and deliberately no commit — no pushed commit exists before the push, so any SHA
-- shown at request time is one the person did not approve. Defaulted, so every row an older
-- broker wrote stays valid and a code rollback needs no database change.
ALTER TABLE approvals ADD COLUMN binding_branch text NOT NULL DEFAULT ''
  CHECK (binding_branch = '' OR binding_branch ~ '^agent/[a-z0-9][a-z0-9._/-]{0,100}$');

-- The row stays its approval_requested event, down to the new field: CREATE OR REPLACE keeps
-- the trigger and the function's other clauses exactly as 20261005120000_approvals.sql left them,
-- and only the insert check gains the branch comparison.
CREATE OR REPLACE FUNCTION approvals_are_their_events() RETURNS trigger
  LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF (SELECT sealed FROM public.rooms WHERE room_id = NEW.room_id) THEN
    RAISE EXCEPTION 'room log: room % is sealed and its approvals stay', NEW.room_id USING ERRCODE = 'check_violation';
  END IF;
  IF TG_OP = 'INSERT' AND (NEW.state <> 'pending' OR NEW.decided_by IS NOT NULL OR NEW.decided_at IS NOT NULL
      OR NEW.reason IS NOT NULL OR NEW.requested_at <> now()) THEN
    RAISE EXCEPTION 'room log: approval % enters pending, undecided, at now()', NEW.approval_id USING ERRCODE = 'check_violation';
  END IF;
  IF TG_OP = 'INSERT' AND NOT EXISTS (SELECT 1 FROM public.events WHERE room_id = NEW.room_id AND seq = NEW.requested_seq
      AND type = 'approval_requested' AND run_id = NEW.run_id AND payload->>'approvalId' = NEW.approval_id
      AND payload->>'callId' = NEW.call_id AND payload->>'class' = NEW.class AND payload->'action' = NEW.action
      AND coalesce(payload->>'branch', '') = NEW.binding_branch) THEN
    RAISE EXCEPTION 'room log: approval % is not its approval_requested event', NEW.approval_id
      USING ERRCODE = 'check_violation';
  END IF;
  IF TG_OP = 'UPDATE' AND (OLD.state <> 'pending' OR NEW.state = 'pending') THEN
    RAISE EXCEPTION 'room log: approval % moves once, out of pending (% to %)', OLD.approval_id, OLD.state, NEW.state
      USING ERRCODE = 'check_violation';
  END IF;
  IF TG_OP = 'UPDATE' AND (NEW.decided_by IS NULL OR NEW.decided_at IS DISTINCT FROM now()) THEN
    RAISE EXCEPTION 'room log: approval % is closed at now(), by someone', OLD.approval_id USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $$;

-- Digest-bound approvals (untrusted-input hardening HR27, HR28; amends the Slack push approval's
-- D5): a push that touches a protected path waits for a maintainer's approval of its bundle's
-- digest. The factory requests the row over a system route, with no bridge call behind it, so the
-- row also names the output it holds. Both default to '', so every row an older broker wrote stays
-- valid and a code rollback needs no database change.
ALTER TABLE approvals ADD COLUMN binding_digest text NOT NULL DEFAULT ''
  CHECK (binding_digest = '' OR binding_digest ~ '^[0-9a-f]{64}$');
ALTER TABLE approvals ADD COLUMN binding_output text NOT NULL DEFAULT ''
  CHECK (binding_output = '' OR binding_output ~ '^[0-9a-f]{16}$');
-- A digest binds a push: both or neither, on a forge.push row that also binds the branch.
ALTER TABLE approvals ADD CONSTRAINT approvals_digest_binds_a_push CHECK (
  (binding_digest = '') = (binding_output = '')
  AND (binding_digest = '' OR (class = 'forge.push' AND binding_branch <> '')));
-- One approval per output: a retried request finds the first.
CREATE UNIQUE INDEX approvals_binding_output ON approvals (run_id, binding_output) WHERE binding_output <> '';

-- The row stays its approval_requested event, down to the new fields: CREATE OR REPLACE keeps the
-- trigger and every other clause exactly as 20261105120000_approval_binding.sql left them, and only
-- the insert check gains the digest and output comparisons.
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
      AND coalesce(payload->>'branch', '') = NEW.binding_branch
      AND coalesce(payload->>'digest', '') = NEW.binding_digest
      AND coalesce(payload->>'output', '') = NEW.binding_output) THEN
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

-- The bundle sweep keeps a bundle an approval still needs, whatever its age: a pending approval's,
-- which a maintainer may yet approve, and an approved one's until the factory has answered its
-- output, since the push is applied from the bundle after the decision. The cutoff stays floored at
-- the 14-day retention, as 20261113120000_output_bundles.sql has it.
CREATE OR REPLACE FUNCTION sweep_output_bundles(p_before timestamptz) RETURNS bigint
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE swept bigint;
BEGIN
  DELETE FROM public.output_bundles b WHERE b.created_at < least(p_before, now() - interval '14 days')
    AND NOT EXISTS (SELECT 1 FROM public.approvals a WHERE a.run_id = b.run_id AND a.binding_digest = b.digest
      AND (a.state = 'pending' OR (a.state = 'approved' AND NOT EXISTS (SELECT 1 FROM public.events e
        WHERE e.room_id = a.room_id AND e.run_id = a.run_id AND e.type = 'message'
          AND e.actor_kind = 'system' AND e.actor_id = 'system:factory'
          AND e.payload->>'kind' IN ('output_applied', 'output_refused')
          AND e.payload->'output'->>'id' = a.binding_output))));
  GET DIAGNOSTICS swept = ROW_COUNT;
  RETURN swept;
END $$;

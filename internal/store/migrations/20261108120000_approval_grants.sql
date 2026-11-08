-- Saved approval grants (merge policy stage 5, MP9, MP10): a maintainer's approval turned into an
-- exact predicate inside one live class's requiresApproval subset. approval_grants is the current
-- state, which moves only along its transitions; approval_grant_events is the audit, insert-only.
-- Repository-wide audit lives here, never in a room's log.
CREATE TABLE approval_grants (
  grant_id     text        PRIMARY KEY CHECK (grant_id ~ '^[a-z2-7]{26}$'),
  version      integer     NOT NULL CHECK (version >= 1),
  repository   text        NOT NULL CHECK (repository ~ '^[a-z0-9_.-]{1,100}/[a-z0-9_.-]{1,100}$'),
  class        text        NOT NULL CHECK (class ~ '^[a-z0-9]([-a-z0-9]{0,38}[a-z0-9])?$'),
  files        text[]      NOT NULL CHECK (cardinality(files) BETWEEN 1 AND 32
                             AND array_position(files, NULL) IS NULL),
  diff_shape   text        NOT NULL DEFAULT '' CHECK (diff_shape IN ('', 'links-only', 'semver-patch', 'semver-minor')),
  policy_hash  text        NOT NULL CHECK (policy_hash ~ '^[0-9a-f]{12,64}$'),
  origin_pr    integer     NOT NULL CHECK (origin_pr >= 1),
  created_by   text        NOT NULL CHECK (length(created_by) BETWEEN 1 AND 128),
  created_at   timestamptz NOT NULL,
  renewed_at   timestamptz NOT NULL,
  expires_at   timestamptz NOT NULL,
  status       text        NOT NULL CHECK (status IN ('active', 'suspended', 'revoked', 'expired', 'disabled')),
  status_at    timestamptz NOT NULL,
  last_used_at timestamptz,
  last_used_pr integer     CHECK (last_used_pr >= 1)
);
CREATE INDEX approval_grants_lookup ON approval_grants (repository, class, status);

CREATE TABLE approval_grant_events (
  event_id    bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  grant_id    text        NOT NULL REFERENCES approval_grants (grant_id),
  version     integer     NOT NULL,
  event       text        NOT NULL CHECK (event IN ('created', 'used', 'suspended', 'reconfirmed', 'revoked',
                            'expired', 'disabled', 'renewed')),
  actor       text        NOT NULL CHECK (length(actor) BETWEEN 1 AND 128),
  reason      text        NOT NULL DEFAULT '' CHECK (length(reason) <= 200),
  policy_hash text        NOT NULL DEFAULT '' CHECK (policy_hash ~ '^([0-9a-f]{12,64})?$'),
  pr          integer     CHECK (pr >= 1),
  head        text        NOT NULL DEFAULT '' CHECK (head ~ '^([0-9a-f]{40})?$'),
  at          timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX approval_grant_events_grant ON approval_grant_events (grant_id, event_id);

-- A grant enters active, version 1, at now(). Its predicate, origin and creator never change. It
-- moves only along these transitions, at now():
--   active    -> suspended | revoked | expired | disabled
--   suspended -> active (reconfirmed: version + 1, its policy hash rebound) | revoked | expired | disabled
--   active    -> active (renewed: version + 1, a later expiry, renewed_at now(); or used: last use only)
--   expired   -> active (renewed: version + 1) | revoked | disabled
-- revoked and disabled are terminal. Only a reconfirmation rebinds the policy hash.
CREATE FUNCTION approval_grants_transition() RETURNS trigger
  LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.status <> 'active' OR NEW.version <> 1 OR NEW.created_at <> now() OR NEW.renewed_at <> now()
        OR NEW.status_at <> now() OR NEW.expires_at <= now() OR NEW.last_used_at IS NOT NULL OR NEW.last_used_pr IS NOT NULL THEN
      RAISE EXCEPTION 'approval grant % enters active, version 1, at now(), unused, unexpired', NEW.grant_id
        USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  IF NEW.grant_id <> OLD.grant_id OR NEW.repository <> OLD.repository OR NEW.class <> OLD.class OR NEW.files <> OLD.files
      OR NEW.diff_shape <> OLD.diff_shape OR NEW.origin_pr <> OLD.origin_pr OR NEW.created_by <> OLD.created_by
      OR NEW.created_at <> OLD.created_at THEN
    RAISE EXCEPTION 'approval grant %: its predicate, origin and creator never change', OLD.grant_id
      USING ERRCODE = 'check_violation';
  END IF;
  IF OLD.status IN ('revoked', 'disabled') THEN
    RAISE EXCEPTION 'approval grant % is % for good', OLD.grant_id, OLD.status USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status = OLD.status AND NEW.status <> 'active' THEN
    RAISE EXCEPTION 'approval grant % is already %', OLD.grant_id, OLD.status USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.status = 'active' AND OLD.status = 'active' THEN
    IF NEW.version = OLD.version THEN -- used: the last use moves, nothing else
      IF NEW.policy_hash <> OLD.policy_hash OR NEW.expires_at <> OLD.expires_at OR NEW.renewed_at <> OLD.renewed_at
          OR NEW.status_at <> OLD.status_at OR NEW.last_used_at IS DISTINCT FROM now() OR NEW.last_used_pr IS NULL THEN
        RAISE EXCEPTION 'approval grant %: a use moves its last use, at now(), alone', OLD.grant_id USING ERRCODE = 'check_violation';
      END IF;
      RETURN NEW;
    END IF;
  END IF;
  IF NEW.status = 'active' AND OLD.status IN ('active', 'expired') THEN -- renewed
    IF NEW.version <> OLD.version + 1 OR NEW.policy_hash <> OLD.policy_hash OR NEW.renewed_at <> now()
        OR NEW.expires_at <= now() OR NEW.status_at <> now() THEN
      RAISE EXCEPTION 'approval grant %: a renewal is version + 1, at now(), to a later expiry, under the same policy', OLD.grant_id
        USING ERRCODE = 'check_violation';
    END IF;
  ELSIF NEW.status = 'active' AND OLD.status = 'suspended' THEN -- reconfirmed
    IF NEW.version <> OLD.version + 1 OR NEW.expires_at <> OLD.expires_at OR NEW.renewed_at <> OLD.renewed_at
        OR NEW.status_at <> now() THEN
      RAISE EXCEPTION 'approval grant %: a reconfirmation is version + 1, at now(), its expiry unchanged', OLD.grant_id
        USING ERRCODE = 'check_violation';
    END IF;
  ELSIF (NEW.status IN ('revoked', 'disabled') AND OLD.status IN ('active', 'suspended', 'expired'))
      OR (NEW.status IN ('suspended', 'expired') AND OLD.status IN ('active', 'suspended')) THEN
    IF NEW.version <> OLD.version OR NEW.policy_hash <> OLD.policy_hash OR NEW.expires_at <> OLD.expires_at
        OR NEW.renewed_at <> OLD.renewed_at OR NEW.status_at <> now() THEN
      RAISE EXCEPTION 'approval grant %: % moves its status, at now(), alone', OLD.grant_id, NEW.status
        USING ERRCODE = 'check_violation';
    END IF;
  ELSE
    RAISE EXCEPTION 'approval grant % does not move from % to %', OLD.grant_id, OLD.status, NEW.status
      USING ERRCODE = 'check_violation';
  END IF;
  IF NEW.last_used_at IS DISTINCT FROM OLD.last_used_at OR NEW.last_used_pr IS DISTINCT FROM OLD.last_used_pr THEN
    RAISE EXCEPTION 'approval grant %: only a use moves its last use', OLD.grant_id USING ERRCODE = 'check_violation';
  END IF;
  RETURN NEW;
END $$;

CREATE TRIGGER approval_grants_transition BEFORE INSERT OR UPDATE ON approval_grants
  FOR EACH ROW EXECUTE FUNCTION approval_grants_transition();

-- Every change has its event by commit, at the same now(), of the grant's new version, so no grant
-- moves off the record. Deferred, like approvals_decision_has_event.
CREATE FUNCTION approval_grants_have_events() RETURNS trigger
  LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE
  want text;
BEGIN
  want := CASE
    WHEN TG_OP = 'INSERT' THEN 'created'
    WHEN NEW.status = 'active' AND OLD.status = 'active' AND NEW.version = OLD.version THEN 'used'
    WHEN NEW.status = 'active' AND OLD.status = 'suspended' THEN 'reconfirmed'
    WHEN NEW.status = 'active' THEN 'renewed'
    ELSE NEW.status END;
  IF NOT EXISTS (SELECT 1 FROM public.approval_grant_events WHERE grant_id = NEW.grant_id AND version = NEW.version
      AND event = want AND at = now()) THEN
    RAISE EXCEPTION 'approval grant % has no % event for version %', NEW.grant_id, want, NEW.version
      USING ERRCODE = 'check_violation';
  END IF;
  RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER approval_grants_have_events AFTER INSERT OR UPDATE ON approval_grants
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION approval_grants_have_events();

-- The audit is insert-only, for every role: an event at now(), never rewritten or removed.
CREATE FUNCTION approval_grant_events_insert_only() RETURNS trigger
  LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.at <> now() THEN
      RAISE EXCEPTION 'approval grant event of % is recorded at now()', NEW.grant_id USING ERRCODE = 'check_violation';
    END IF;
    RETURN NEW;
  END IF;
  RAISE EXCEPTION 'approval grant events are insert-only' USING ERRCODE = 'insufficient_privilege';
END $$;

CREATE TRIGGER approval_grant_events_insert_only BEFORE INSERT OR UPDATE OR DELETE ON approval_grant_events
  FOR EACH ROW EXECUTE FUNCTION approval_grant_events_insert_only();
CREATE TRIGGER approval_grant_events_no_truncate BEFORE TRUNCATE ON approval_grant_events
  FOR EACH STATEMENT EXECUTE FUNCTION approval_grant_events_insert_only();

-- The broker moves a grant along its transitions and appends its audit; it never deletes either.
GRANT SELECT, INSERT ON approval_grants TO rooms_broker;
GRANT UPDATE (version, policy_hash, renewed_at, expires_at, status, status_at, last_used_at, last_used_pr)
  ON approval_grants TO rooms_broker;
GRANT SELECT, INSERT ON approval_grant_events TO rooms_broker;
ALTER TABLE approval_grants ENABLE ROW LEVEL SECURITY;
ALTER TABLE approval_grant_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY broker_approval_grants             ON approval_grants       FOR ALL    TO rooms_broker USING (true) WITH CHECK (true);
CREATE POLICY broker_read_approval_grant_events  ON approval_grant_events FOR SELECT TO rooms_broker USING (true);
CREATE POLICY broker_write_approval_grant_events ON approval_grant_events FOR INSERT TO rooms_broker WITH CHECK (true);

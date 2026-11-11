-- Intent samples (docs/superpowers/specs/2026-11-11-slack-intents-design.md, SI19): the replies of a
-- channel that opted in with collectReplies, stored with the classifier's label so the shadow stage
-- can be scored, and deleted once expires_at passes. The broker stamps expires_at at insert from
-- human.slackIntents.retentionDays, so the retention job needs no configuration. Nothing else in the
-- platform holds thread text; a channel without the flag never reaches this table. The text arrives
-- already redacted (internal/redact).
CREATE TABLE intent_samples (
  sample_id  bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  channel    text        NOT NULL CHECK (channel ~ '^[CGD][A-Z0-9]{2,20}$'),
  reply      text        NOT NULL CHECK (length(reply) BETWEEN 1 AND 8000),
  intent     text        NOT NULL CHECK (intent IN ('status', 'needs', 'changes', 'pr', 'steer', 'amend', 'stop', 'retry', 'unknown')),
  confidence real        NOT NULL CHECK (confidence BETWEEN 0 AND 1),
  outcome    text        NOT NULL DEFAULT 'shadowed' CHECK (outcome IN ('shadowed', 'confirmed', 'cancelled', 'expired')),
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  CHECK (expires_at > created_at)
);
CREATE INDEX intent_samples_created ON intent_samples (created_at DESC);
CREATE INDEX intent_samples_expires ON intent_samples (expires_at);

-- The one move a sample makes: out of shadowed, once, to what became of the confirm. A function, so
-- the broker holds no UPDATE on the table and no column but outcome can ever change; true when this
-- call moved it.
CREATE FUNCTION intent_sample_outcome(p_sample_id bigint, p_outcome text) RETURNS boolean
  LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE moved integer;
BEGIN
  IF p_outcome NOT IN ('confirmed', 'cancelled', 'expired') THEN
    RAISE EXCEPTION 'intent sample: outcome % is confirmed, cancelled or expired', p_outcome USING ERRCODE = 'check_violation';
  END IF;
  UPDATE public.intent_samples SET outcome = p_outcome WHERE sample_id = p_sample_id AND outcome = 'shadowed';
  GET DIAGNOSTICS moved = ROW_COUNT;
  RETURN moved = 1;
END $$;
REVOKE ALL ON FUNCTION intent_sample_outcome(bigint, text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION intent_sample_outcome(bigint, text) TO rooms_broker;

-- The broker records and reads samples; it cannot rewrite or delete one. Retention deletes the
-- expired ones and reads nothing but their expiry.
GRANT SELECT, INSERT ON intent_samples TO rooms_broker;
GRANT DELETE ON intent_samples TO rooms_retention;
GRANT SELECT (expires_at) ON intent_samples TO rooms_retention;
ALTER TABLE intent_samples ENABLE ROW LEVEL SECURITY;
CREATE POLICY broker_intent_samples          ON intent_samples FOR ALL    TO rooms_broker    USING (true) WITH CHECK (true);
CREATE POLICY retention_read_intent_samples  ON intent_samples FOR SELECT TO rooms_retention USING (expires_at < now());
CREATE POLICY retention_purge_intent_samples ON intent_samples FOR DELETE TO rooms_retention USING (expires_at < now());

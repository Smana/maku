-- The GitHub account link (v4 D8, step 5 PR C): each human links their GitHub account once, through
-- GitHub's OAuth flow, and the broker keeps the numeric GitHub id it proved, never a token. The id
-- resolves to the current login at each use, through the factory App, so a rename is harmless.
-- One GitHub id per sub and one sub per GitHub id.
CREATE TABLE github_links (
  sub           text        PRIMARY KEY CHECK (length(sub) BETWEEN 1 AND 128),
  github_id     bigint      NOT NULL UNIQUE CHECK (github_id > 0),
  -- The login GitHub named at link time: display only, stale after a rename.
  login_at_link text        NOT NULL CHECK (login_at_link ~ '^[A-Za-z0-9-]{1,39}$'),
  linked_at     timestamptz NOT NULL,
  -- Refreshed at most hourly by the human's own authenticated requests: "active" without an IdP
  -- admin API (human.githubLink.activeWindow).
  last_seen_at  timestamptz NOT NULL
);

-- A flow in progress, from the start POST to the confirm POST, ten minutes at most. The OAuth state
-- and the browser cookie's nonce are stored as SHA-256 only, and the PKCE verifier not at all: it is
-- derived from the nonce and the state at the callback. github_id and login are filled by the
-- callback, which links nothing; only the confirm, under the same sub's web session, does.
CREATE TABLE github_link_states (
  state_sha256 bytea       PRIMARY KEY CHECK (length(state_sha256) = 32),
  sub          text        NOT NULL CHECK (length(sub) BETWEEN 1 AND 128),
  nonce_sha256 bytea       NOT NULL CHECK (length(nonce_sha256) = 32),
  expires_at   timestamptz NOT NULL,
  github_id    bigint      CHECK (github_id > 0),
  login        text        CHECK (login ~ '^[A-Za-z0-9-]{1,39}$'),
  CHECK ((github_id IS NULL) = (login IS NULL))
);
CREATE INDEX github_link_states_sub ON github_link_states (sub);

-- Every link act, kept: who linked or unlinked which id, and who did it (the human, an admin, or
-- the break-glass command).
CREATE TABLE github_link_events (
  id        bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  sub       text        NOT NULL CHECK (length(sub) BETWEEN 1 AND 128),
  github_id bigint      NOT NULL CHECK (github_id > 0),
  act       text        NOT NULL CHECK (act IN ('linked', 'relinked', 'unlinked', 'admin_unlinked')),
  actor     text        NOT NULL CHECK (length(actor) BETWEEN 1 AND 200),
  at        timestamptz NOT NULL
);
CREATE INDEX github_link_events_sub ON github_link_events (sub, at);

GRANT SELECT, INSERT, UPDATE, DELETE ON github_links, github_link_states TO rooms_broker;
-- The history is append-only: no UPDATE or DELETE.
GRANT SELECT, INSERT ON github_link_events TO rooms_broker;
ALTER TABLE github_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE github_link_states ENABLE ROW LEVEL SECURITY;
ALTER TABLE github_link_events ENABLE ROW LEVEL SECURITY;
CREATE POLICY broker_github_links       ON github_links       FOR ALL TO rooms_broker USING (true) WITH CHECK (true);
CREATE POLICY broker_github_link_states ON github_link_states FOR ALL TO rooms_broker USING (true) WITH CHECK (true);
CREATE POLICY broker_github_link_events ON github_link_events FOR ALL TO rooms_broker USING (true) WITH CHECK (true);

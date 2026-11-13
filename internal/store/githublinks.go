// SPDX-License-Identifier: Apache-2.0

package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The GitHub account link (v4 D8, plan step 5 PR4): a pending state per flow, the link, and its
// append-only history. Nothing here holds a token, a code, a state or a verifier: the state and the
// cookie's nonce are stored as SHA-256 only.

// MaxGitHubPending bounds one sub's live flows.
const MaxGitHubPending = 3

// The link acts, github_link_events.act: a closed list.
const (
	GitHubLinked        = "linked"
	GitHubRelinked      = "relinked"
	GitHubUnlinked      = "unlinked"
	GitHubAdminUnlinked = "admin_unlinked"
)

// The link's refusals, each a reason the page names.
var (
	ErrTooManyPending = errors.New("too many GitHub links are in progress for the account")
	// ErrStateInvalid is a state that is unknown, expired, already used or, at the confirm, not yet
	// through the callback.
	ErrStateInvalid = errors.New("the GitHub link state is unknown, expired or used")
	// ErrSubMismatch is a confirm under another sub than the one that started the flow.
	ErrSubMismatch = errors.New("the GitHub link was started by another account")
	// ErrGitHubTaken is a GitHub id linked to another sub: an admin unlinks it first.
	ErrGitHubTaken = errors.New("the GitHub account is linked to another account")
)

// GitHubState is a flow in progress: who started it, the SHA-256 of the cookie nonce that must come
// back with it, and, once the callback ran, the GitHub user it proved.
type GitHubState struct {
	Sub         string
	NonceSHA256 []byte
	Expires     time.Time
	GitHubID    int64
	Login       string
}

// GitHubLink is a sub's link: the numeric GitHub id, the login at link time (display only), and
// when the human last authenticated.
type GitHubLink struct {
	Sub        string
	GitHubID   int64
	Login      string
	LinkedAt   time.Time
	LastSeenAt time.Time
}

// StartGitHubLink stores a new flow for sub, after purging every expired one. It refuses once sub
// holds MaxGitHubPending live flows.
func (s *Store) StartGitHubLink(ctx context.Context, stateSHA256 []byte, sub string, nonceSHA256 []byte, expires, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// One start per sub at a time, so the bound holds against concurrent starts.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('github_link_states:' || $1, 0))`, sub); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM github_link_states WHERE expires_at <= $1`, now); err != nil {
		return err
	}
	var live int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM github_link_states WHERE sub = $1`, sub).Scan(&live); err != nil {
		return err
	}
	if live >= MaxGitHubPending {
		return ErrTooManyPending
	}
	if _, err := tx.Exec(ctx, `INSERT INTO github_link_states (state_sha256, sub, nonce_sha256, expires_at) VALUES ($1, $2, $3, $4)`,
		stateSHA256, sub, nonceSHA256, expires); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// GitHubState reads a live flow: ErrStateInvalid when it is unknown or expired.
func (s *Store) GitHubState(ctx context.Context, stateSHA256 []byte, now time.Time) (GitHubState, error) {
	var st GitHubState
	var id *int64
	var login *string
	err := s.pool.QueryRow(ctx, `SELECT sub, nonce_sha256, expires_at, github_id, login FROM github_link_states
		WHERE state_sha256 = $1 AND expires_at > $2`, stateSHA256, now).Scan(&st.Sub, &st.NonceSHA256, &st.Expires, &id, &login)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, ErrStateInvalid
	}
	if id != nil {
		st.GitHubID = *id
	}
	if login != nil {
		st.Login = *login
	}
	return st, err
}

// SetGitHubStateUser records the GitHub user the callback proved on a live flow, once: a flow that
// already holds one is ErrStateInvalid, so a replayed callback cannot swap the account.
func (s *Store) SetGitHubStateUser(ctx context.Context, stateSHA256 []byte, githubID int64, login string, now time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE github_link_states SET github_id = $2, login = $3
		WHERE state_sha256 = $1 AND expires_at > $4 AND github_id IS NULL`, stateSHA256, githubID, login, now)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrStateInvalid
	}
	return nil
}

// ConfirmGitHubLink links sub to the GitHub user its flow proved, consuming the flow, and appends
// the act, in one transaction. A flow that is unknown, expired or not through the callback is
// ErrStateInvalid; another sub's is ErrSubMismatch; an id linked to another sub ErrGitHubTaken.
// The refusals write nothing. It answers GitHubLinked, or GitHubRelinked when sub had a link.
func (s *Store) ConfirmGitHubLink(ctx context.Context, stateSHA256 []byte, sub string, now time.Time) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var owner string
	var id *int64
	var login *string
	err = tx.QueryRow(ctx, `SELECT sub, github_id, login FROM github_link_states WHERE state_sha256 = $1 AND expires_at > $2 FOR UPDATE`,
		stateSHA256, now).Scan(&owner, &id, &login)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", ErrStateInvalid
	case err != nil:
		return "", err
	case id == nil || login == nil:
		return "", ErrStateInvalid
	case owner != sub:
		return "", ErrSubMismatch
	}
	// The id's lock: two confirms of one GitHub account see each other.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('github_links:' || $1::bigint::text, 0))`, *id); err != nil {
		return "", err
	}
	var holder string
	err = tx.QueryRow(ctx, `SELECT sub FROM github_links WHERE github_id = $1 FOR UPDATE`, *id).Scan(&holder)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return "", err
	case holder != sub:
		return "", ErrGitHubTaken
	}
	act := GitHubLinked
	var had bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM github_links WHERE sub = $1)`, sub).Scan(&had); err != nil {
		return "", err
	}
	if had {
		act = GitHubRelinked
	}
	if _, err := tx.Exec(ctx, `INSERT INTO github_links (sub, github_id, login_at_link, linked_at, last_seen_at) VALUES ($1, $2, $3, $4, $4)
		ON CONFLICT (sub) DO UPDATE SET github_id = EXCLUDED.github_id, login_at_link = EXCLUDED.login_at_link,
		linked_at = EXCLUDED.linked_at, last_seen_at = EXCLUDED.last_seen_at`, sub, *id, *login, now); err != nil {
		if pg, ok := errors.AsType[*pgconn.PgError](err); ok && pg.Code == "23505" { // a racing first link of the same id by another sub
			return "", ErrGitHubTaken
		}
		return "", err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO github_link_events (sub, github_id, act, actor, at) VALUES ($1, $2, $3, $1, $4)`, sub, *id, act, now); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM github_link_states WHERE state_sha256 = $1`, stateSHA256); err != nil {
		return "", err
	}
	return act, tx.Commit(ctx)
}

// GitHubLinkOf is sub's link; found is false when it has none.
func (s *Store) GitHubLinkOf(ctx context.Context, sub string) (GitHubLink, bool, error) {
	l := GitHubLink{Sub: sub}
	err := s.pool.QueryRow(ctx, `SELECT github_id, login_at_link, linked_at, last_seen_at FROM github_links WHERE sub = $1`, sub).
		Scan(&l.GitHubID, &l.Login, &l.LinkedAt, &l.LastSeenAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return GitHubLink{}, false, nil
	}
	return l, err == nil, err
}

// UnlinkGitHub removes sub's link and appends the act: unlinked by the human, admin_unlinked when
// admin (an admin's page, or the break-glass command), with actor naming who. gone is false when
// there was nothing to remove, and then nothing is written.
func (s *Store) UnlinkGitHub(ctx context.Context, sub, actor string, admin bool, now time.Time) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var id int64
	err = tx.QueryRow(ctx, `DELETE FROM github_links WHERE sub = $1 RETURNING github_id`, sub).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	act := GitHubUnlinked
	if admin {
		act = GitHubAdminUnlinked
	}
	if _, err := tx.Exec(ctx, `INSERT INTO github_link_events (sub, github_id, act, actor, at) VALUES ($1, $2, $3, $4, $5)`, sub, id, act, actor, now); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// TouchGitHubLink refreshes sub's last_seen_at when it is over an hour old, so each human writes at
// most once an hour. moved is whether it did; an unlinked sub moves nothing.
func (s *Store) TouchGitHubLink(ctx context.Context, sub string, now time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE github_links SET last_seen_at = $2 WHERE sub = $1 AND last_seen_at < $2::timestamptz - interval '1 hour'`, sub, now)
	return tag.RowsAffected() > 0, err
}

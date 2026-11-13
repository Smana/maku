// SPDX-License-Identifier: Apache-2.0

package ghidentity

import (
	"context"
	"strconv"
	"time"
)

// The GitHub account link as the identity source (v4 D8, plan step 5 PR5): the broker's own
// github_links, read in the broker from the store and in the factory through the broker. A link is
// proof made at GitHub by the person, at link time; each use still resolves its numeric id to the
// current login (LoginOf) and checks the repository permission.

// LinkRecord is a stored link as the resolver reads it: the numeric GitHub id, and when its human
// last authenticated.
type LinkRecord struct {
	GitHubID   int64
	LastSeenAt time.Time
}

// LinkReader reads sub's link; found is false when it has none.
type LinkReader func(ctx context.Context, sub string) (LinkRecord, bool, error)

// StoreLinks is a Resolver.Links over read: sub's link, labelled idp (the resolver's IDPID), or none.
func StoreLinks(read LinkReader, idp string) func(context.Context, string) ([]Link, error) {
	return func(ctx context.Context, sub string) ([]Link, error) {
		r, found, err := read(ctx, sub)
		if err != nil || !found {
			return nil, err
		}
		return []Link{{IDPID: idp, UserID: strconv.FormatInt(r.GitHubID, 10)}}, nil
	}
}

// HolderReader answers which sub holds an account link to a GitHub id; found is false when none does.
type HolderReader func(ctx context.Context, githubID int64) (sub string, found bool, err error)

// Chain is Links from primary, and from fallback (the ZITADEL reader, during the move off it) only
// for a sub primary has no link for. A nil fallback is no fallback. With holder, a GitHub id the
// fallback names that another sub holds through primary is dropped: one GitHub id is one account,
// across both sources, and the account link wins.
func Chain(primary, fallback func(context.Context, string) ([]Link, error), holder HolderReader) func(context.Context, string) ([]Link, error) {
	return func(ctx context.Context, sub string) ([]Link, error) {
		links, err := primary(ctx, sub)
		if err != nil || len(links) > 0 || fallback == nil {
			return links, err
		}
		links, err = fallback(ctx, sub)
		if err != nil || holder == nil {
			return links, err
		}
		kept := links[:0:0]
		for _, l := range links {
			id, perr := strconv.ParseInt(l.UserID, 10, 64)
			if perr != nil {
				kept = append(kept, l) // the resolver refuses a non-numeric id itself
				continue
			}
			other, found, err := holder(ctx, id)
			if err != nil {
				return nil, err
			}
			if !found || other == sub {
				kept = append(kept, l)
			}
		}
		return kept, nil
	}
}

// StoreActive is a Resolver.Active without an IdP admin API: a linked sub is active while its human
// authenticated within window (each of their requests refreshes the link's last_seen_at), so the
// Slack App acting for a person who stopped signing in is refused once window passes. While fallback
// (the reader's user state) is configured it must say active too, linked or not, so a deactivation in
// the IdP still applies at once (#126). A sub with no link and no fallback is active: it holds no
// GitHub identity to act with, and it must be able to sign in to link one.
func StoreActive(read LinkReader, window time.Duration, now func() time.Time, fallback func(context.Context, string) (bool, error)) func(context.Context, string) (bool, error) {
	return func(ctx context.Context, sub string) (bool, error) {
		r, found, err := read(ctx, sub)
		switch {
		case err != nil:
			return false, err
		case found && now().Sub(r.LastSeenAt) > window:
			return false, nil
		case fallback != nil:
			return fallback(ctx, sub)
		}
		return true, nil
	}
}

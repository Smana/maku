// SPDX-License-Identifier: Apache-2.0

package ghidentity

import (
	"context"
	"strconv"
	"time"
)

// The GitHub account link, the only identity source (v4 D8): the broker's own
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

// StoreActive is a Resolver.Active without an IdP admin API: a linked sub is active while its human
// authenticated within window (each of their requests refreshes the link's last_seen_at), so the
// Slack App acting for a person who stopped signing in is refused once window passes. A sub with no
// link is active: it holds no GitHub identity to act with, and it must be able to sign in to link one.
func StoreActive(read LinkReader, window time.Duration, now func() time.Time) func(context.Context, string) (bool, error) {
	return func(ctx context.Context, sub string) (bool, error) {
		r, found, err := read(ctx, sub)
		switch {
		case err != nil:
			return false, err
		case found && now().Sub(r.LastSeenAt) > window:
			return false, nil
		}
		return true, nil
	}
}

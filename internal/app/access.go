// SPDX-License-Identifier: Apache-2.0

package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Smana/maku/internal/config"
	"github.com/Smana/maku/internal/ghidentity"
	"github.com/Smana/maku/internal/github"
	"github.com/Smana/maku/internal/httpx"
	"github.com/Smana/maku/internal/intake"
	"github.com/Smana/maku/internal/repoaccess"
	"github.com/Smana/maku/internal/store"
)

// zitadelTimeout bounds one ZITADEL link read.
const zitadelTimeout = 10 * time.Second

// gitHubUsers is what D7 asks the factory App; *github.App has it.
type gitHubUsers interface {
	UserLogin(ctx context.Context, owner, repo string, id int64) (string, error)
	Permission(ctx context.Context, owner, repo, login string) (string, error)
}

// gitHubVisibilities is what the planner's public-only guard asks the factory App (CD5): one
// repository's visibility. *github.App has it, and it is a second interface beside gitHubUsers so a
// consumer names the one method it reads.
type gitHubVisibilities interface {
	Repository(ctx context.Context, owner, repo string) (github.Repository, error)
}

// factoryApp is the factory App mounted at dir (ROOMS_GITHUB_APP_DIR); nil when unset.
func factoryApp(dir string) *github.App {
	if dir == "" {
		return nil
	}
	return &github.App{Dir: dir, API: githubAPIURL(), HC: httpx.New(posterGitHubTimeout, nil), Now: time.Now}
}

// accessApp is the factory App D7 reads through, mounted at dir (ROOMS_GITHUB_APP_DIR); nil when
// unset. Its own instance: an access check never waits on the verdict poster's token mint.
func accessApp(dir string) gitHubUsers {
	if a := factoryApp(dir); a != nil {
		return a
	}
	return nil
}

// visibilityApp is the factory App the planner's public-only read goes through, mounted at dir
// (ROOMS_GITHUB_APP_DIR); nil when unset. Its own instance too: a visibility read never waits behind
// a permission check's token mint.
func visibilityApp(dir string) gitHubVisibilities {
	if a := factoryApp(dir); a != nil {
		return a
	}
	return nil
}

// accessTTL is the window a GitHub answer is trusted: human.access's TTL, which config.Load defaults
// to MaxAccessTTL. The planner's visibility cache reads through the same window, so a repository gone
// private and a permission revoked lapse together.
func accessTTL(h config.HumanConfig) time.Duration {
	if h.Access != nil && h.Access.TTL.Duration > 0 {
		return h.Access.TTL.Duration
	}
	return config.MaxAccessTTL
}

// intakePublicity is the planner's public-only guard over the GitHub App (CD5, §security): one
// visibility read per repository per ttl, cached as repoaccess caches a permission, so the delay
// between a repository going private and the planner refusing it is the one already documented for
// a revoked permission. A repository the App cannot see, or a reply with no visibility word, is an
// error — never cached — and the guard then refuses rather than planning on a guess.
//
// Nil gh is no visibility read at all: the broker refuses to start a planner without it, since a
// guard that can verify nothing refuses every plan.
func intakePublicity(gh gitHubVisibilities, ttl time.Duration) intake.Publicity {
	if gh == nil {
		return nil
	}
	lookup := func(ctx context.Context, repository string) (bool, error) {
		// intake.CachedPublicity checks owner/name before it calls, so the split holds here.
		owner, repo, _ := strings.Cut(repository, "/")
		r, err := gh.Repository(ctx, owner, repo)
		if err != nil {
			return false, err
		}
		return !r.Private && r.Visibility == "public", nil
	}
	return intake.NewPublicity(lookup, ttl, time.Now)
}

// roomAccess is D7's check from human.access: the caller's GitHub login, then its permission on the
// room's repository, both through gh and cached for the config's TTL. The login comes from the
// caller's GitHub account link (human.githubLink, read through links) and, during the move off
// ZITADEL, from their ZITADEL IdP link (human.access.readerFile) for a sub with no account link
// (plan step 5 PR5). Without human.access it returns nils, and the human API shows rooms to admins
// only.
func roomAccess(h config.HumanConfig, gh gitHubUsers, zitadel *http.Client, now func() time.Time, links ghidentity.LinkReader) (*ghidentity.Resolver, *repoaccess.Checker, error) {
	if h.Access == nil {
		return nil, nil, nil
	}
	if gh == nil {
		return nil, nil, errors.New("human.access needs the factory App: ROOMS_GITHUB_APP_DIR is not set")
	}
	ttl := h.Access.TTL.Duration
	id := &ghidentity.Resolver{IDPID: githubIDP, TTL: ttl, Now: now,
		LoginOf: func(ctx context.Context, repo string, id int64) (string, error) {
			owner, name, _ := strings.Cut(repo, "/")
			login, err := gh.UserLogin(ctx, owner, name, id)
			return login, unseen(err)
		}}
	var readerActiveOf func(context.Context, string) (bool, error)
	var readerLinksOf func(context.Context, string) ([]ghidentity.Link, error)
	if h.Access.ReaderFile != "" {
		readerActiveOf, readerLinksOf = readerActive(zitadel, h.Issuer, h.Access.ReaderFile), readerLinks(zitadel, h.Issuer, h.Access.ReaderFile)
	}
	if l := h.GitHubLink; l != nil {
		if links == nil {
			return nil, nil, errors.New("human.githubLink needs the rooms database to read the links from")
		}
		id.Links = ghidentity.Chain(ghidentity.StoreLinks(links, githubIDP), readerLinksOf)
		id.Active = ghidentity.StoreActive(links, l.ActiveWindow.Duration, now, readerActiveOf)
	} else {
		id.Links, id.Active = readerLinksOf, readerActiveOf
	}
	perm := func(ctx context.Context, owner, repo, login string) (string, error) {
		p, err := gh.Permission(ctx, owner, repo, login)
		return p, unseen(err)
	}
	return id, &repoaccess.Checker{Perm: perm, TTL: ttl, Now: now}, nil
}

// storeLinks reads the broker's github_links for the resolver.
func storeLinks(st interface {
	GitHubLinkOf(ctx context.Context, sub string) (store.GitHubLink, bool, error)
}) ghidentity.LinkReader {
	return func(ctx context.Context, sub string) (ghidentity.LinkRecord, bool, error) {
		l, found, err := st.GitHubLinkOf(ctx, sub)
		return ghidentity.LinkRecord{GitHubID: l.GitHubID, LastSeenAt: l.LastSeenAt}, found, err
	}
}

// unseen marks a repository the App cannot see, or cannot name in a path, as one nobody reads
// through it (repoaccess.ErrNoRepository): a typo or an uninstalled repository is a 404, never a
// 503 that a retry would not heal.
func unseen(err error) error {
	if errors.Is(err, github.ErrNoInstallation) || errors.Is(err, github.ErrNotAPullRequest) {
		return fmt.Errorf("%w: %w", repoaccess.ErrNoRepository, err)
	}
	return err
}

// githubIDP names the GitHub IdP inside the resolver. ZITADEL mints its real id anew on every
// build, so the id comes from the reader secret at use, and readerLinks relabels the matching
// links with this constant.
const githubIDP = "github"

// zitadelReader is the reader secret: the read-only machine user's PAT and the GitHub IdP's id.
// Its tokenId is the provisioning script's, unused here.
type zitadelReader struct {
	PAT         string `json:"pat"`
	GitHubIDPID string `json:"githubIdpId"`
}

// readReader reads the reader secret at path, on every call, so a rotated PAT or a rebuilt IdP
// needs no restart. A secret that is missing or incomplete is an error: the check fails closed.
func readReader(path string) (zitadelReader, error) {
	var r zitadelReader
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return r, fmt.Errorf("zitadel reader: %w", err)
	}
	if json.Unmarshal(raw, &r) != nil || r.PAT == "" || r.GitHubIDPID == "" {
		// Never the decoder's text: it may quote the PAT.
		return r, errors.New(`zitadel reader: not {"pat", "githubIdpId"} JSON`)
	}
	return r, nil
}

// readerActive reads whether a ZITADEL user is active with the reader secret at path (#126).
func readerActive(hc *http.Client, issuer, path string) func(ctx context.Context, user string) (bool, error) {
	return func(ctx context.Context, user string) (bool, error) {
		r, err := readReader(path)
		if err != nil {
			return false, err
		}
		return ghidentity.ZitadelActive(hc, issuer, r.PAT)(ctx, user)
	}
}

// readerLinks lists a user's links to the GitHub IdP with the reader secret at path.
func readerLinks(hc *http.Client, issuer, path string) func(ctx context.Context, user string) ([]ghidentity.Link, error) {
	return func(ctx context.Context, user string) ([]ghidentity.Link, error) {
		r, err := readReader(path)
		if err != nil {
			return nil, err
		}
		links, err := ghidentity.ZitadelLinks(hc, issuer, r.PAT)(ctx, user)
		if err != nil {
			return nil, err
		}
		var out []ghidentity.Link
		for _, l := range links {
			if l.IDPID == r.GitHubIDPID {
				out = append(out, ghidentity.Link{IDPID: githubIDP, UserID: l.UserID})
			}
		}
		return out, nil
	}
}

// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/sanitize"
	"github.com/Smana/maku/internal/repoaccess"
)

// The reasons given when canStart is false (with startSponsored, intake.go's), each with its fix in
// the client's form text and maku doctor (internal/maku's StartWhyFix).
const (
	startNotOffered    = "repository_not_offered" // human.mcp.start, or the planner's served repositories, leave it out
	startAccessUnwired = "access_unwired"         // no GitHub access check (human.access) is wired
	startNotLinked     = "not_linked"             // the caller has no GitHub link to check
	startNoWrite       = "no_write"               // the caller cannot write to the repository, or the App cannot see it
	startUnverified    = "unverified"             // ZITADEL, GitHub or the factory's catalogue could not answer
	startNotMaintainer = "not_maintainer"         // web start: the catalogue says the caller maintains no repository it starts on
)

// startOffered reports whether the broker offers to start factory work on repo for p (UX3): false
// for a repository in StartExcept; else true when StartDefault is true and, when a planner is
// configured, the planner serves repo for p (planRepositories: the factory's catalogue, narrowed by
// human.intake.plan.repositories, or that list alone without a factory). The reason is "" when
// offered, else startNotOffered, or startUnverified when the catalogue could not answer.
func (s *Server) startOffered(ctx context.Context, p authn.Principal, repo string) string {
	r := strings.ToLower(strings.TrimSpace(repo))
	if r == "" || s.StartExcept[r] || !s.StartDefault {
		return startNotOffered
	}
	if s.Intake == nil {
		return ""
	}
	served, err := s.planRepositories(ctx, p)
	switch {
	case errors.Is(err, errCatalogueUnavailable) && s.Catalogue == nil:
		return startNotOffered // no factory and no repositories list: the planner serves nothing
	case err != nil:
		return startUnverified
	case !slices.Contains(served, r):
		return startNotOffered
	}
	return ""
}

// startRefusals remembers which caller, repository and reason the broker has logged, so a refused
// start is logged once per caller and repository rather than on every task call.
type startRefusals struct {
	mu   sync.Mutex
	seen map[string]bool
}

// startRefusalCap bounds startRefusals: past it the set starts over, logging each pair once more.
const startRefusalCap = 4096

func (s *Server) logStartRefused(p authn.Principal, repo, why string) {
	key := p.ID + "\x00" + strings.ToLower(repo) + "\x00" + why
	s.startRefused.mu.Lock()
	if s.startRefused.seen == nil || len(s.startRefused.seen) >= startRefusalCap {
		s.startRefused.seen = map[string]bool{}
	}
	already := s.startRefused.seen[key]
	s.startRefused.seen[key] = true
	s.startRefused.mu.Unlock()
	if !already {
		s.log().Info("start not offered", "principal", p.ID, "repository", repo, "reason", why)
	}
}

// canStart is true when startOffered(repo) holds and the caller writes to repo on GitHub,
// following gate's lookup with CanWrite for CanRead. A sponsored caller never starts a session. It
// fails closed: an answer that cannot be had is false.
func (s *Server) canStart(ctx context.Context, p authn.Principal, repo string) (bool, string) {
	ok, why := s.canStartFor(ctx, p, repo)
	if !ok {
		s.logStartRefused(p, repo, why)
	}
	return ok, why
}

func (s *Server) canStartFor(ctx context.Context, p authn.Principal, repo string) (bool, string) {
	if p.Sponsored {
		return false, startSponsored
	}
	if why := s.startOffered(ctx, p, repo); why != "" {
		return false, why
	}
	if s.Identity == nil || s.Access == nil {
		return false, startAccessUnwired
	}
	ctx, cancel := context.WithTimeout(ctx, accessWait) // as the gate's lookup is: a hung one is unverified
	defer cancel()
	login, err := s.Identity.Login(ctx, p.Sub, repo)
	switch {
	case errors.Is(err, repoaccess.ErrNoRepository):
		return false, startNoWrite
	case err != nil:
		return false, startUnverified
	case login == "":
		return false, startNotLinked
	}
	ok, err := s.Access.CanWrite(ctx, repo, login)
	switch {
	case errors.Is(err, repoaccess.ErrNoRepository):
		return false, startNoWrite
	case err != nil:
		return false, startUnverified
	case !ok:
		return false, startNoWrite
	}
	return true, ""
}

// checkDraft is the intermediate template and redaction check. Planner revisions use it while their
// markers are still legal; external draft doors call checkFinalDraft instead. Both fail closed
// without a redactor, and their refusals are toolErrors the model reads as they are.
func (s *Server) checkDraft(ctx context.Context, title, body string) (sanitize.Draft, error) {
	return s.checkDraftWith(ctx, title, body, sanitize.CheckDraft)
}

// checkFinalDraft applies the external filing rules before the same redactor: valid Decisions and no
// live or malformed clarification marker, in addition to the ordinary template.
func (s *Server) checkFinalDraft(ctx context.Context, title, body string) (sanitize.Draft, error) {
	return s.checkDraftWith(ctx, title, body, sanitize.CheckFinalDraft)
}

func (s *Server) checkDraftWith(ctx context.Context, title, body string,
	check func(string, string) (sanitize.Draft, error)) (sanitize.Draft, error) {
	d, err := check(title, body)
	if err != nil {
		return sanitize.Draft{}, toolError(err.Error())
	}
	if s.Actor == nil || s.Actor.Redactor == nil {
		return sanitize.Draft{}, errors.New("humanapi: no redactor") // fail closed: a secret would go unseen
	}
	_, rules, err := s.Actor.Redactor.Payload(ctx, envelope.Must(map[string]string{"title": d.Title, "body": d.Body}))
	if err != nil {
		return sanitize.Draft{}, err
	}
	if len(rules) > 0 {
		return sanitize.Draft{}, toolError("invalid_draft: it holds what looks like a secret (" + strings.Join(rules, ", ") +
			"): remove it, since the factory's agents read the issue in full")
	}
	return d, nil
}

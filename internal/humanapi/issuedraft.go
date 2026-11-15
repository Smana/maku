// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"errors"
	"strings"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/sanitize"
	"github.com/Smana/maku/internal/repoaccess"
)

// The reasons given when canStart is false.
const (
	startOff        = "off"        // human.mcp.start does not offer start for the repository, or no check is wired
	startNotLinked  = "not_linked" // the caller has no GitHub link to check
	startNoWrite    = "no_write"   // the caller cannot write to the repository, or the App cannot see it
	startUnverified = "unverified" // ZITADEL or GitHub could not answer
)

// startOffered reports whether the broker offers to start factory work on repo (UX3): false for a
// repository in StartExcept; else true when StartDefault is true (on planner-served repositories
// when a planner is configured).
func (s *Server) startOffered(repo string) bool {
	r := strings.ToLower(repo)
	if r == "" || s.StartExcept[r] {
		return false
	}
	return s.StartDefault && (s.Intake == nil || s.intakeAllows(r))
}

func (s *Server) intakeAllows(repoLower string) bool {
	if s.Intake.Allow[repoLower] {
		return true
	}
	for k, on := range s.Intake.Allow {
		if on && strings.ToLower(k) == repoLower {
			return true
		}
	}
	return false
}

// canStart is true when startOffered(repo) holds and the caller writes to repo on GitHub,
// following gate's lookup with CanWrite for CanRead. A sponsored caller never starts a session. It
// fails closed: an answer that cannot be had is false.
func (s *Server) canStart(ctx context.Context, p authn.Principal, repo string) (bool, string) {
	if p.Sponsored || !s.startOffered(repo) || s.Identity == nil || s.Access == nil {
		return false, startOff
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

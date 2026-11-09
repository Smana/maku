// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Smana/maku/internal/authn"
	"github.com/Smana/maku/internal/envelope"
	"github.com/Smana/maku/internal/factory/sanitize"
	"github.com/Smana/maku/internal/mcpwire"
	"github.com/Smana/maku/internal/repoaccess"
)

// issueDraft is issue_draft's result: what maku mcp's issue_file shows the developer, then files.
type issueDraft struct {
	Repo  string `json:"repo"`
	Title string `json:"title"`
	Body  string `json:"body"`
	// Stripped counts the hidden and control characters removed (F8): what is filed is what is shown.
	Stripped int `json:"stripped"`
	// CanStart says whether the handoff may start the factory on Repo for the caller (R11);
	// CanStartWhy is the reason when not: off, not_linked, no_write or unverified.
	CanStart    bool   `json:"canStart"`
	CanStartWhy string `json:"canStartWhy,omitempty"`
}

// issue_draft validates a factory issue in the skill's shape: hidden characters stripped, the
// template's title and sections, no secret. It files nothing: issue_file, maku mcp's own tool,
// files it with the developer's gh after they confirm (F3).
func (s *Server) issueDraftTool(ctx context.Context, p authn.Principal, args json.RawMessage) (any, error) {
	var a struct {
		Repo  string `json:"repo"`
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if mcpwire.DecodeArgs(args, &a) != nil || !repositoryRE.MatchString(a.Repo) {
		return nil, toolError("invalid_arguments: repo is owner/name; title and body are strings")
	}
	d, err := s.checkFinalDraft(ctx, a.Title, a.Body)
	if err != nil {
		return nil, err
	}
	can, why := s.canStart(ctx, p, a.Repo)
	return issueDraft{Repo: a.Repo, Title: d.Title, Body: d.Body, Stripped: d.Stripped, CanStart: can, CanStartWhy: why}, nil
}

// The reasons issue_draft gives when canStart is false.
const (
	startOff        = "off"        // the repository is not in human.mcp.sessionStart, or no check is wired
	startNotLinked  = "not_linked" // the caller has no GitHub link to check
	startNoWrite    = "no_write"   // the caller cannot write to the repository, or the App cannot see it
	startUnverified = "unverified" // ZITADEL or GitHub could not answer
)

// canStart is true when repo is in human.mcp.sessionStart and the caller writes to it on GitHub,
// following gate's lookup with CanWrite for CanRead. A sponsored caller never starts a session. It
// fails closed: an answer that cannot be had is false.
func (s *Server) canStart(ctx context.Context, p authn.Principal, repo string) (bool, string) {
	if p.Sponsored || !s.SessionStart[strings.ToLower(repo)] || s.Identity == nil || s.Access == nil {
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

// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/forge"
	"github.com/Smana/maku/internal/ghidentity"
)

// HandoffStart is the broker's record of a web New task start (broker-confirmed handoff): the
// broker filed Issue on Repository through the factory App, which applied Labels, for the signed-in
// human whose ZITADEL sub is Sub, at StartedAt.
type HandoffStart struct {
	Handoff    string
	Repository string
	Issue      int
	Sub        string
	Labels     []string
	StartedAt  time.Time
}

// HandoffStarts reads the broker's record of the web start that filed issue on repository; found
// is false when the broker filed none.
type HandoffStarts func(ctx context.Context, repository string, issue int) (h HandoffStart, found bool, err error)

// Logins resolves a ZITADEL sub to its current GitHub login on repository, as the API's linked
// logins do: "" when the sub has no GitHub link, ghidentity.ErrInactive when it is not active.
type Logins interface {
	Login(ctx context.Context, sub, repository string) (string, error)
}

const (
	// handoffGrace is how long the App's label waits for the broker's record: the broker records
	// the start once GitHub has answered the filing, so a poll can read the label first.
	handoffGrace = 2 * time.Minute
	// handoffWindow bounds the label's distance from the record's time: the record follows the
	// filing that applied the label, within one request.
	handoffWindow = 10 * time.Minute
)

// handoffConfirmation is a broker-confirmed web start: the human it was started for and the
// workflow the App pinned for them.
type handoffConfirmation struct {
	Handoff, Login, Workflow string
}

var workflowName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// confirmedHandoff answers a trigger label that is not a maintainer's (broker-confirmed handoff):
// it starts a task only when the factory App applied it as the issue's first factory/ready label,
// the broker recorded that
// it filed this very issue from a web start within handoffWindow of that label, and the record's
// sub resolves, through the factory's own link resolver, to a login that maintains and writes on
// the repository. Nothing on the issue confirms anything: its body, its labels or an issue editor's
// label never do. wait is true while the record may still be in flight, or a check went
// unanswered (err).
func (p *IssuePoller) confirmedHandoff(ctx context.Context, cfg *config.Config, n, gen int, last forge.LabelEvent) (c *handoffConfirmation, wait bool, err error) {
	if p.HandoffStarts == nil || p.Logins == nil || gen != 1 || cfg.FactoryLogin == "" || !forge.SameLogin(last.Actor, cfg.FactoryLogin) {
		return nil, false, nil // any other labeller is refused at once, as before
	}
	repo := p.repo(cfg).Name
	h, found, err := p.HandoffStarts(ctx, repo, n)
	switch {
	case err != nil:
		return nil, true, fmt.Errorf("handoff start of %d: %w", n, err) // no answer is no refusal
	case !found:
		return nil, p.now().Sub(last.At) < handoffGrace, nil
	case h.Issue != n || !strings.EqualFold(h.Repository, repo) || h.Sub == "":
		return nil, false, nil
	}
	if d := last.At.Sub(h.StartedAt); d > handoffWindow || d < -handoffWindow {
		p.log().Info("a handoff start far from the label: not confirmed", "issue", n, "handoff", h.Handoff, "label_at", last.At, "started_at", h.StartedAt)
		return nil, false, nil
	}
	login, err := p.Logins.Login(ctx, h.Sub, repo)
	switch {
	case errors.Is(err, ghidentity.ErrInactive):
		return nil, false, nil
	case err != nil:
		return nil, true, fmt.Errorf("the handoff starter's login on %d: %w", n, err)
	case login == "" || !p.repo(cfg).IsMaintainer(login):
		p.log().Info("a handoff start for a non-maintainer: not confirmed", "issue", n, "handoff", h.Handoff, "login", login)
		return nil, false, nil
	}
	switch w, err := p.writer(ctx, login); {
	case err != nil:
		return nil, true, err
	case !w:
		return nil, false, nil
	}
	c = &handoffConfirmation{Handoff: h.Handoff, Login: login}
	for _, l := range h.Labels {
		if name, ok := strings.CutPrefix(l, "workflow:"); ok && workflowName.MatchString(name) {
			c.Workflow = name
			break
		}
	}
	p.log().Info("broker-confirmed handoff start", "issue", n, "handoff", h.Handoff, "requester", login, "labeller", last.Actor)
	return c, false, nil
}

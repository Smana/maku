// SPDX-License-Identifier: Apache-2.0

package intake

import (
	"context"
	"sort"
	"strings"
)

// maxCandidates is the contract's bound on the candidate list (`candidates ≤ 20`): a question about
// more repositories than this is not one plan, and the model is never shown a list it cannot hold.
const maxCandidates = 20

// Requester is the effective requester of a plan: who is asking, resolved once by the API and
// threaded to the guard so that the guard never re-derives it. The spec's rule is that the
// candidate set is the allowlist ∩ what *this* requester may write, so the identity behind the
// write check is decided in one place — the request's authentication — and not here.
type Requester struct {
	// Key is the requester's stable identity: the daily cap's key and the audit line's field. It is
	// the principal's own key, never a display name, so a rename does not reset a cap.
	Key string
	// Login is the requester's GitHub login, the one `repoaccess` checks write access for. Empty
	// means the account is not linked to GitHub, and no write access can be established.
	Login string
}

// Guard bounds what a plan may name (CD5, §security). Its two answers fail closed: a repository
// whose write access or visibility cannot be established is refused rather than planned on.
type Guard struct {
	// Allow is the repository allowlist the planner serves (`human.intake.plan.repositories`).
	// Membership is the value being true: a key set false is a repository the allowlist does not
	// serve, so an operator can switch one off without deleting the key.
	Allow map[string]bool
	// MayWrite answers whether the effective requester may write repository. It closes over the
	// Requester, which is why the guard is not told who is asking: one indirection keeps the
	// identity out of every signature below it. A nil MayWrite can verify nothing, so the guard
	// plans nothing.
	MayWrite func(ctx context.Context, repository string) (bool, error)
	// Public answers whether GitHub reports repository as public, read once per repository and
	// cached (`CachedPublicity`). A nil Public can verify nothing, so the guard plans nothing.
	Public Publicity
}

// Candidates is the set of repositories one plan may name: the allowlist ∩ what was asked ∩ what the
// requester may write ∩ what GitHub reports public, lowercased, de-duplicated, at most
// `maxCandidates`, and in the allowlist's order (sorted: a map has no order, and two processes must
// agree on one).
//
// It answers in the security order — allowlist, asked, write, public — so that no repository
// reaches the model before every check it can fail has been run.
//
// An empty asked list narrows nothing: the request named no repository, so the candidate set is the
// allowlist ∩ write ∩ public that CD5 describes, and the model picks from it with its evidence.
//
// Refusals, and what each one is allowed to say:
//   - One distinct repository was asked and the allowlist does not serve it: `repository_not_allowed`,
//     whose one sentence lists nothing and names nothing.
//   - It was asked, and the requester cannot write it: `no_write`.
//   - It was asked, and GitHub reports it private: `private_repository_unsupported`, with the two
//     ways forward — the requester named it, so refusing it is the whole answer.
//   - Write access or visibility errored: `repository_unverified`. Never planned on a guess.
//
// With several repositories asked, a candidate that fails the write or public check is dropped
// silently: naming it back would confirm a repository the requester did not name, and the spec's
// rule is that such a repository is never named, not even as an alternative. When that leaves
// nothing, the refusal is `repository_unverified` if any check errored (an outage is the reason, and
// "not allowed" would send an operator after the wrong problem) and `repository_not_allowed`
// otherwise.
func (g Guard) Candidates(ctx context.Context, asked []string) ([]string, *Refusal) {
	if g.MayWrite == nil || g.Public == nil {
		// A guard wired without its checks cannot verify anything: it refuses rather than planning
		// on a request nobody checked, and rather than panicking inside a tool.
		return nil, refuse(ReasonRepositoryUnverified, "")
	}

	// The asked set, lowercased and de-duplicated: GitHub's names are case-insensitive, so one
	// repository in three spellings is one repository — and one write check.
	askedSet := make(map[string]bool, len(asked))
	for _, repository := range asked {
		if key := strings.ToLower(strings.TrimSpace(repository)); key != "" {
			askedSet[key] = true
		}
	}
	narrowed := len(askedSet) > 0
	single := len(askedSet) == 1

	// The allowlist, in the one order it has: a map has none, and the candidate list is a value the
	// model's reply is checked against.
	allowed := make([]string, 0, len(g.Allow))
	seen := make(map[string]bool, len(g.Allow))
	for repository, served := range g.Allow {
		key := strings.ToLower(strings.TrimSpace(repository))
		if !served || key == "" || seen[key] {
			continue
		}
		seen[key] = true
		allowed = append(allowed, key)
	}
	sort.Strings(allowed)

	candidates := make([]string, 0, len(allowed))
	unverified := false
	for _, repository := range allowed {
		if narrowed && !askedSet[repository] {
			// Not asked: the intersection, not a candidate and not a refusal either — the asked
			// set is what the requester brought, and the allowlist is what the planner serves.
			continue
		}
		write, err := g.MayWrite(ctx, repository)
		if err != nil {
			if single {
				return nil, refuse(ReasonRepositoryUnverified, "")
			}
			unverified = true
			continue
		}
		if !write {
			if single {
				return nil, refuse(ReasonNoWrite, "")
			}
			continue
		}
		public, err := g.Public.Public(ctx, repository)
		if err != nil {
			if single {
				return nil, refuse(ReasonRepositoryUnverified, "")
			}
			unverified = true
			continue
		}
		if !public {
			if single {
				return nil, refuse(ReasonPrivateRepository, "")
			}
			continue
		}
		candidates = append(candidates, repository)
		if len(candidates) == maxCandidates {
			break
		}
	}

	if len(candidates) == 0 {
		if unverified {
			return nil, refuse(ReasonRepositoryUnverified, "")
		}
		return nil, refuse(ReasonRepositoryNotAllowed, "")
	}
	return candidates, nil
}

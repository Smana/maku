// SPDX-License-Identifier: Apache-2.0

package forge

import (
	"context"
	"net/http"

	"github.com/google/go-github/v92/github"
)

// maxRulePages bounds BranchRules' read: GitHub pages the rules a branch's rulesets apply.
const maxRulePages = 10

// RequiredStatus is a status check a ruleset requires on a branch: its context, and the App that
// must post it, 0 when any App's (or anyone's) status of that context counts.
type RequiredStatus struct {
	Context       string
	IntegrationID int64
}

// BranchRules is the status checks the rulesets on branch require (GET
// repos/{o}/{r}/rules/branches/{b}, metadata read). A branch no ruleset covers (GitHub's 404) has
// none. Classic branch protection is not read: that needs administration read (D11).
func (g *GitHub) BranchRules(ctx context.Context, branch string) ([]RequiredStatus, error) {
	var out []RequiredStatus
	opts := &github.ListOptions{PerPage: 100}
	for range maxRulePages {
		rules, resp, err := g.rest.Repositories.ListRulesForBranch(ctx, g.owner, g.name, branch, opts)
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			g.mark(nil)
			return nil, nil
		}
		g.mark(err)
		if err != nil {
			return nil, wrap("branch rules", err)
		}
		if rules != nil {
			for _, rule := range rules.RequiredStatusChecks {
				if rule == nil {
					continue
				}
				for _, c := range rule.Parameters.RequiredStatusChecks {
					if c != nil {
						out = append(out, RequiredStatus{Context: c.Context, IntegrationID: c.GetIntegrationID()})
					}
				}
			}
		}
		if resp == nil || resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return out, nil
}

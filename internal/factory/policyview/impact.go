// SPDX-License-Identifier: Apache-2.0

package policyview

import (
	"cmp"
	"encoding/json"
	"maps"
	"slices"

	"github.com/Smana/maku/internal/factory/config"
	"github.com/Smana/maku/internal/factory/mergepolicy"
	"github.com/Smana/maku/internal/factory/resources"
)

// PolicyImpact is one changed MergePolicy's blast radius (B11): each consumer's structural change
// and whether its binding still resolves.
type PolicyImpact struct {
	Policy                     string
	BaseGeneration, Generation int64
	Consumers                  []ConsumerImpact
}

// ConsumerImpact is one binding of a changed policy. Change is a mergepolicy verdict, "new" for a
// binding the base did not resolve, or "-" when the binding no longer resolves.
type ConsumerImpact struct {
	Repository, Mode, Change string
	Why                      []string
	Resolves                 bool
	Reason                   string // "Reason: message", when !Resolves
}

// Impact compares two assemblies, a base and a proposal, policy by policy: every class policy
// whose definition differs (added, edited or removed), with each binding of it in next, live ones
// first (Q5), then by repository. A consumer's change is mergepolicy.ClassChange on its resolved
// classes before and after.
func Impact(base, next resources.Result) []PolicyImpact {
	bp, np := policies(base), policies(next)
	names := map[string]bool{}
	for n := range bp {
		names[n] = true
	}
	for n := range np {
		names[n] = true
	}
	var out []PolicyImpact
	for _, name := range slices.Sorted(maps.Keys(names)) {
		b, inB := bp[name]
		n, inN := np[name]
		if inB && inN && specJSON(b.Spec) == specJSON(n.Spec) {
			continue
		}
		pi := PolicyImpact{Policy: name, BaseGeneration: b.Generation, Generation: n.Generation}
		for _, o := range next.Bindings {
			if o.Name != name {
				continue
			}
			ci := ConsumerImpact{Repository: o.Repository, Mode: o.Mode, Change: "-", Resolves: o.Accepted}
			key := resources.BindingKey{Repository: o.Repository, Name: name}
			switch was, ok := base.Resolved[key]; {
			case !o.Accepted:
				ci.Reason = o.Reason + ": " + o.Message
			case !ok:
				ci.Change = "new"
			default:
				c := mergepolicy.ClassChange(was.Class, next.Resolved[key].Class)
				ci.Change, ci.Why = c.Kind, c.Why
			}
			pi.Consumers = append(pi.Consumers, ci)
		}
		slices.SortStableFunc(pi.Consumers, func(x, y ConsumerImpact) int {
			return cmp.Or(cmp.Compare(rank(x.Mode), rank(y.Mode)), cmp.Compare(x.Repository, y.Repository))
		})
		out = append(out, pi)
	}
	return out
}

func rank(mode string) int {
	if mode == config.ModeLive {
		return 0
	}
	return 1
}

func policies(r resources.Result) map[string]config.ServedPolicy {
	if r.Config == nil {
		return nil
	}
	return r.Config.Policies
}

func specJSON(p config.PolicySpec) string {
	b, err := json.Marshal(p)
	if err != nil { // strings, ints, bools and Durations
		return err.Error()
	}
	return string(b)
}

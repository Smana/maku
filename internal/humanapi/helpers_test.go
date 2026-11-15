// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/Smana/maku/internal/factory/catalogue"
	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/summary"
)

func withCatalogue(src CatalogueSource) option {
	return func(s *Server, _ *fanout.Hub, _ *hubView) { s.Catalogue = NewCatalogueCache(src, s.Metrics, nil) }
}

// fakeFactoryCatalogue answers by repository; onBehalfOf "slack:T01ABC/U02DEF" is the maintainer.
type fakeFactoryCatalogue struct {
	byRepo   map[string]error
	repos    []string
	reposErr error
	asked    []string
}

func (f *fakeFactoryCatalogue) Repositories(_ context.Context, _ string) ([]string, error) {
	if f.reposErr != nil {
		return nil, f.reposErr
	}
	if f.repos != nil {
		return slices.Clone(f.repos), nil
	}
	return []string{"Smana/ai-native-ref"}, nil
}

// worldWithSplitAndMerge is that fixture, under the plan's name for it: the world, the split and the
// merge in one option.
func worldWithSplitAndMerge(t *testing.T) splitAndMergeGate {
	t.Helper()
	return splitAndMergeGate{gate: needsWorld(t, nil)}
}

// needKeysOf is a needs_list answer's items' identities, in the order the tool gave them (CD21).
func needKeysOf(t *testing.T, got toolCall) []string {
	t.Helper()
	var data struct{ Items []summary.Need }
	if err := json.Unmarshal([]byte(got.structured), &data); err != nil {
		t.Fatalf("%s: %v", got.structured, err)
	}
	return needKeys(data.Items)
}

// aiNativeRef is the catalogue shaped like ai-native-ref's live config: docs-links live, review off,
// and the workflows every class offers (WF4), review-floor (no template) the default.
func aiNativeRef(maintainer bool) catalogue.Catalogue {
	return catalogue.Catalogue{
		Repository: "Smana/ai-native-ref", Maintainer: maintainer, ApproveToMerge: true, DefaultWorkflow: "review-floor",
		Evaluation: catalogue.Evaluation{Mode: "observe", ControlPercent: 20},
		Classes: []catalogue.Class{
			{Name: "docs-links", Description: "Dead links in docs", Mode: "live", CeilingUSD: 2},
			{Name: "review", Description: "Anything else: a maintainer merges", Mode: "off", CeilingUSD: 8},
		},
		Profiles: map[string]catalogue.Profile{
			"links-solo":   {Template: "solo", Roles: []string{"implementer"}, Tier: "light", Offered: true, Description: "One file of links, no logic"},
			"review-floor": {Checks: true, Description: "The default: triage picks the template", NotOffered: catalogue.NoTemplate},
			"review-frontier": {Template: "trio", Roles: []string{"implementer", "tester", "reviewer"}, Tier: "frontier",
				Checks: true, TestFirst: true, Offered: true, Description: "Changes across several files",
				Escalation: &catalogue.Escalation{NotConverged: "review-floor"}},
		},
	}
}

// splitAndMergeGate is PR 3's list fixture as a tool call takes it: the split proposal on 26zfnuxm and
// the pull request waiting for dev's merge on 3kq7x2ma (needsWorld), with the GitHub on which dev writes
// Smana/a — injected as one option, since a tool call names no route of its own.
type splitAndMergeGate struct{ gate option }

func (f *fakeFactoryCatalogue) Catalogue(_ context.Context, _, repository, onBehalfOf string) (catalogue.Catalogue, error) {
	f.asked = append(f.asked, repository+"|"+onBehalfOf)
	if err := f.byRepo[repository]; err != nil {
		return catalogue.Catalogue{}, err
	}
	return aiNativeRef(onBehalfOf == "slack:T01ABC/U02DEF" || onBehalfOf == ""), nil
}

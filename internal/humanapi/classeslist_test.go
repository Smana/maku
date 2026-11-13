// SPDX-License-Identifier: Apache-2.0

package humanapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Smana/maku/internal/factory/catalogue"
	"github.com/Smana/maku/internal/fanout"
	"github.com/Smana/maku/internal/runrequest"
)

// aiNativeRef is the catalogue shaped like ai-native-ref's live config: docs-links live, review off
// with a variant and an escalation.
func aiNativeRef(maintainer bool) catalogue.Catalogue {
	return catalogue.Catalogue{
		Repository: "Smana/ai-native-ref", Maintainer: maintainer, ApproveToMerge: true,
		Evaluation: catalogue.Evaluation{Mode: "observe", ControlPercent: 20},
		Classes: []catalogue.Class{
			{Name: "docs-links", Description: "Dead links in docs", Mode: "live", CeilingUSD: 2, Floor: "links-solo", Allowed: []string{}},
			{Name: "review", Description: "Anything else: a maintainer merges", Mode: "off", CeilingUSD: 8, Floor: "review-floor",
				Allowed:    []string{"review-frontier"},
				Escalation: &catalogue.Escalation{OutOfBudget: "review-frontier", NotConverged: "review-frontier"}},
		},
		Profiles: map[string]catalogue.Profile{
			"links-solo":   {Template: "solo", Roles: []string{"implementer"}, Tier: "light", Offered: true, Description: "One file of links, no logic"},
			"review-floor": {Checks: true, Description: "The default: triage picks pair or trio", NotOffered: catalogue.NoTemplate},
			"review-frontier": {Template: "trio", Roles: []string{"implementer", "tester", "reviewer"}, Tier: "frontier",
				Checks: true, TestFirst: true, Offered: true, Description: "Changes across several files"},
		},
	}
}

// fakeFactoryCatalogue answers by repository; onBehalfOf "slack:T01ABC/U02DEF" is the maintainer.
type fakeFactoryCatalogue struct {
	byRepo map[string]error
	asked  []string
}

func (f *fakeFactoryCatalogue) Catalogue(_ context.Context, _, repository, onBehalfOf string) (catalogue.Catalogue, error) {
	f.asked = append(f.asked, repository+"|"+onBehalfOf)
	if err := f.byRepo[repository]; err != nil {
		return catalogue.Catalogue{}, err
	}
	return aiNativeRef(onBehalfOf == "slack:T01ABC/U02DEF" || onBehalfOf == ""), nil
}

func withCatalogue(src CatalogueSource) option {
	return func(s *Server, _ *fanout.Hub, _ *hubView) { s.Catalogue = NewCatalogueCache(src, s.Metrics, nil) }
}

func TestClassesListSummary(t *testing.T) {
	e := setup(t, mcpOn, withCatalogue(&fakeFactoryCatalogue{}))
	got := call(t, e, viaRoomctl("dev"), ClassesList, map[string]any{"repository": "Smana/ai-native-ref"})
	if got.isError {
		t.Fatalf("%s", got.text)
	}
	for _, want := range []string{
		"docs-links · live · merges on its own when the gate's conditions hold · profile links-solo\n",
		"review · off · a maintainer's approval merges it · profile chosen at triage · variants review-frontier\n",
	} {
		if !strings.Contains(got.text, want) {
			t.Errorf("text lacks %q:\n%s", want, got.text)
		}
	}
	var c catalogue.Catalogue
	if err := json.Unmarshal(got.structured, &c); err != nil || len(c.Classes) != 2 || !c.Profiles["review-frontier"].Offered {
		t.Fatalf("%v %s", err, got.structured)
	}
	// The call is counted like every tool's.
	if body := scrape(t, e); !strings.Contains(body, `tool="classes_list"`) {
		t.Errorf("rooms_human_mcp_calls_total lacks classes_list:\n%s", body)
	}
}

func TestClassesListDetail(t *testing.T) {
	e := setup(t, mcpOn, withCatalogue(&fakeFactoryCatalogue{}))
	got := call(t, e, viaRoomctl("dev"), ClassesList, map[string]any{"repository": "Smana/ai-native-ref", "class": "review"})
	if got.isError {
		t.Fatalf("%s", got.text)
	}
	for _, want := range []string{
		"Floor review-floor: roles chosen at triage, not pinnable",
		"- review-frontier: roles implementer, tester, reviewer · tier frontier · tests first · checks on",
		"Escalation: out of budget goes to review-frontier; not converged goes to review-frontier.",
		"You may pin review-frontier.", // the caller is a maintainer
	} {
		if !strings.Contains(got.text, want) {
			t.Errorf("detail lacks %q:\n%s", want, got.text)
		}
	}
	var c catalogue.Catalogue
	if json.Unmarshal(got.structured, &c) != nil || len(c.Classes) != 1 || c.Classes[0].Name != "review" || len(c.Profiles) != 2 {
		t.Fatalf("the structured answer is the class and its profiles: %s", got.structured)
	}

	// The App reads for a Slack user who is no maintainer: no pin is offered.
	e = setup(t, mcpOn, withSponsored(sponsoredP), withCatalogue(&fakeFactoryCatalogue{}))
	got = call(t, e, bearer("slack-app"), ClassesList, map[string]any{"repository": "Smana/ai-native-ref", "class": "review",
		"on_behalf_of": "slack:T01ABC/U09XYZ"})
	if got.isError || strings.Contains(got.text, "You may pin") || !strings.Contains(got.text, "Only a maintainer of this repository can pin a profile") {
		t.Fatalf("%s", got.text)
	}
}

func TestClassesListUnknownClass(t *testing.T) {
	e := setup(t, mcpOn, withCatalogue(&fakeFactoryCatalogue{}))
	got := call(t, e, viaRoomctl("dev"), ClassesList, map[string]any{"repository": "Smana/ai-native-ref", "class": "nope"})
	if !got.isError || !strings.HasPrefix(got.text, "no_such_class") {
		t.Fatalf("%+v", got)
	}
	got = call(t, e, viaRoomctl("dev"), ClassesList, map[string]any{"repository": "not a repo"})
	if !got.isError || !strings.HasPrefix(got.text, "invalid_arguments") {
		t.Fatalf("%+v", got)
	}
}

func TestClassesListAccess(t *testing.T) {
	src := &fakeFactoryCatalogue{byRepo: map[string]error{
		"Smana/private": runrequest.ErrRefused{Status: http.StatusNotFound, Reason: "not_found"},
		"Smana/down":    errors.New("factory down"),
		"Smana/nolink":  runrequest.ErrRefused{Status: http.StatusForbidden, Reason: "not_linked"},
	}}
	e := setup(t, mcpOn, withSponsored(sponsoredP), withCatalogue(src))
	for name, tc := range map[string]struct {
		h    http.Header
		args map[string]any
		want string
	}{
		"a human who cannot read":      {viaRoomctl("dev"), map[string]any{"repository": "Smana/private"}, "not_found"},
		"the factory is down":          {viaRoomctl("dev"), map[string]any{"repository": "Smana/down"}, "catalogue_unavailable"},
		"a Slack user with no link":    {bearer("slack-app"), map[string]any{"repository": "Smana/nolink", "on_behalf_of": "slack:T01ABC/U09XYZ"}, "not_linked"},
		"the App without on_behalf_of": {bearer("slack-app"), map[string]any{"repository": "Smana/ai-native-ref"}, "invalid_arguments: on_behalf_of is required"},
		"a human with on_behalf_of":    {viaRoomctl("dev"), map[string]any{"repository": "Smana/ai-native-ref", "on_behalf_of": "slack:T01ABC/U02DEF"}, "invalid_arguments: on_behalf_of is a sponsored principal's"},
	} {
		t.Run(name, func(t *testing.T) {
			before := len(src.asked)
			got := call(t, e, tc.h, ClassesList, tc.args)
			if !got.isError || !strings.HasPrefix(got.text, tc.want) {
				t.Fatalf("%+v", got)
			}
			if strings.HasPrefix(tc.want, "invalid_arguments") && len(src.asked) != before {
				t.Fatal("a refused argument still reached the factory")
			}
		})
	}
	// The App reads for the user it names: the factory is asked with it.
	got := call(t, e, bearer("slack-app"), ClassesList, map[string]any{"repository": "Smana/ai-native-ref", "on_behalf_of": "slack:T01ABC/U02DEF"})
	if got.isError || src.asked[len(src.asked)-1] != "Smana/ai-native-ref|slack:T01ABC/U02DEF" {
		t.Fatalf("%+v %q", got, src.asked)
	}
}

// With no catalogue reader the tool is on no list: a list that advertises a tool the broker cannot
// serve is worse than a short one.
func TestClassesListIsListedOnlyWithACatalogue(t *testing.T) {
	for name, tc := range map[string]struct {
		opts []option
		want bool
	}{
		"with a reader":    {[]option{mcpOn, withCatalogue(&fakeFactoryCatalogue{})}, true},
		"without a reader": {[]option{mcpOn}, false},
	} {
		t.Run(name, func(t *testing.T) {
			e := setup(t, tc.opts...)
			_, out := rpc(t, e, viaRoomctl("dev"), "tools/list", nil)
			b, _ := json.Marshal(out)
			if got := strings.Contains(string(b), `"classes_list"`); got != tc.want {
				t.Fatalf("listed = %t: %s", got, b)
			}
		})
	}
}

// B19: the declared output names the per-class policy generations a bound repository carries.
func TestClassesListOutputNamesMergePolicies(t *testing.T) {
	var schema struct {
		Properties map[string]struct {
			Properties map[string]map[string]any `json:"properties"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(classesListOutput, &schema); err != nil {
		t.Fatal(err)
	}
	mp, ok := schema.Properties["generations"].Properties["mergePolicies"]
	if !ok || mp["type"] != "object" {
		t.Fatalf("generations.mergePolicies: %v", mp)
	}
}

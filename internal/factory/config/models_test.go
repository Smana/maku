// SPDX-License-Identifier: Apache-2.0

package config

import (
	"strings"
	"testing"
)

// byModel is good priced by served model, as the host's meter is: every provider alias's pinned
// model has its row (v3 F15, F5).
// gatewayIdentity lets loops name models and split (GW10): the runs call the gateway as themselves.
const gatewayIdentity = "runs: {gatewayIdentity: true}\n"

var byModel = strings.Replace(strings.Replace(good, "sum by (ar_agent)", "sum by (ar_agent, gen_ai_request_model, gen_ai_token_type)", 1),
	"  logsURL:", "  prices: {glm-5.3: {input: 1.40, cacheRead: 0.26, output: 4.40}, k3.1-pro: {input: 2, cacheRead: 0.20, cacheWrite: 2, output: 10}, "+
		"mistral-medium-2604: {input: 1.50, cacheRead: 0.15, output: 7.50}, mistral-small-2603: {input: 0.15, cacheRead: 0.015, output: 0.60}}\n  logsURL:", 1)

// v3 F15, F3: loops.<class>.models picks a provider alias per role and tier, for public tasks
// only; a role or tier without an entry, and every internal task, keep today's model.
func TestModelFor(t *testing.T) {
	cfg, err := Parse([]byte(byModel + gatewayIdentity + `loops:
  default: {models: {reviewer: {light: mistral-light}}}
  standard: {models: {implementer: {standard: mistral-standard, frontier: kimi-frontier}, triager: {light: mistral-light}}, judge: mistral-light}
`))
	if err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct{ class, dataClass, role, tier, want string }{
		"public standard, standard implementer": {"standard", "public", "implementer", "standard", "mistral-standard"},
		"public standard, frontier implementer": {"standard", "public", "implementer", "frontier", "kimi-frontier"},
		"public standard, the scoper's light":   {"standard", "public", "triager", "light", "mistral-light"},
		"internal standard: never an alias":     {"standard", "internal", "implementer", "standard", ""},
		"an unknown data class: never":          {"standard", "", "implementer", "standard", ""},
		"a tier without an entry":               {"standard", "public", "implementer", "light", ""},
		"a role without an entry":               {"standard", "public", "reviewer", "frontier", ""},
		"a row inherits default's (WF10)":       {"standard", "public", "reviewer", "light", "mistral-light"},
		"default's row reaches review":          {"review", "public", "reviewer", "light", "mistral-light"},
		"default's row, internal":               {"review", "internal", "reviewer", "light", ""},
	} {
		t.Run(name, func(t *testing.T) {
			if got := cfg.ModelFor(c.class, c.dataClass, c.role, c.tier); got != c.want {
				t.Fatalf("ModelFor = %q, want %q", got, c.want)
			}
		})
	}
	if cfg.LoopFor("default") != nil {
		t.Error("a models row switched the loop on")
	}
	if p := cfg.LoopFor("standard"); p == nil || p.JudgeModel != "mistral-light" {
		t.Errorf("standard' judge %+v, want mistral-light", p)
	}
	none, err := Parse([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if got := none.ModelFor("standard", "public", "implementer", "standard"); got != "" {
		t.Errorf("no loops: %q, want today's model (D4)", got)
	}
}

func TestEffortForReadsTheWorkflowsRole(t *testing.T) {
	cfg := mustParse(t, clean+"workflows: {standard: {template: pair, effort: {reviewer: medium}}}\n")
	for role, want := range map[string]string{"reviewer": "medium", "implementer": "", "triager": ""} {
		if got := cfg.EffortFor("standard", role); got != want {
			t.Errorf("EffortFor standard %s = %q, want %q", role, got, want)
		}
	}
	if got := mustParse(t, clean).EffortFor("standard", "reviewer"); got != "" {
		t.Errorf("no effort row: %q, want the harness default", got)
	}
}

func TestWorkflowModelsApplyPerRole(t *testing.T) {
	cfg := mustParse(t, clean+gatewayIdentity+"workflows: {standard: {template: pair, models: {reviewer: tier-standard}}}\n")
	for _, dataClass := range []string{"public", "internal"} {
		if got := cfg.ModelFor("standard", dataClass, "reviewer", "frontier"); got != "tier-standard" {
			t.Errorf("%s reviewer model = %q, want tier-standard", dataClass, got)
		}
		if got := cfg.ModelFor("standard", dataClass, "implementer", "frontier"); got != "" {
			t.Errorf("%s implementer model = %q, want the tier's model", dataClass, got)
		}
	}
}

func TestWorkflowProviderModelIsPublicOnly(t *testing.T) {
	cfg := mustParse(t, byModel+gatewayIdentity+"workflows: {standard: {template: pair, models: {reviewer: kimi-frontier}}}\n")
	if got := cfg.ModelFor("standard", "public", "reviewer", "frontier"); got != "kimi-frontier" {
		t.Fatalf("public reviewer model = %q, want kimi-frontier", got)
	}
	if got := cfg.ModelFor("standard", "internal", "reviewer", "frontier"); got != "" {
		t.Fatalf("internal reviewer model = %q, want the tier's model", got)
	}
}

func TestWorkflowEffortAndModelsValidation(t *testing.T) {
	for name, c := range map[string]struct{ raw, want string }{
		"unknown effort":      {clean + "workflows: {standard: {template: pair, effort: {reviewer: max}}}\n", `workflows.standard.effort.reviewer "max" is low, medium or high`},
		"unknown effort role": {clean + "workflows: {standard: {template: pair, effort: {merger: low}}}\n", `workflows.standard.effort: unknown role "merger"`},
		"implementer effort":  {clean + "workflows: {standard: {template: pair, effort: {implementer: low}}}\n", "workflows.standard.effort.implementer: the implementer's effort is not settable"},
		"unknown model role":  {clean + "workflows: {standard: {template: pair, models: {merger: tier-light}}}\n", `workflows.standard.models: unknown role "merger"`},
		"implementer model":   {clean + "workflows: {standard: {template: pair, models: {implementer: tier-light}}}\n", "workflows.standard.models.implementer: the implementer's model is not settable"},
		"unknown model alias": {clean + "workflows: {standard: {template: pair, models: {reviewer: huge}}}\n", `workflows.standard.models.reviewer: "huge" is not a model alias`},
		"internal provider":   {strings.Replace(byModel, "dataClass: public", "dataClass: internal", 1) + "workflows: {standard: {template: pair, models: {reviewer: kimi-frontier}}}\n", "workflows.standard.models.reviewer kimi-frontier: an internal task takes tier-light, tier-standard or tier-frontier"},
		"none runs no effort": {clean + "workflows: {none: {template: none, effort: {reviewer: low}}}\n", "workflows.none: template none runs nothing, so it takes no other key"},
		"none runs no models": {clean + "workflows: {none: {template: none, models: {reviewer: tier-light}}}\n", "workflows.none: template none runs nothing, so it takes no other key"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(c.raw)); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

func TestPublicOnly(t *testing.T) {
	for _, m := range []string{"kimi-frontier", "mistral-standard", "mistral-light"} {
		if !PublicOnly(m) {
			t.Errorf("%s is a public-only provider alias", m)
		}
	}
	for _, m := range []string{"", "agent-default", "tier-light", "tier-frontier", "k3.1-pro", "none"} {
		if PublicOnly(m) {
			t.Errorf("%q is not a provider alias", m)
		}
	}
}

// Config validation (F2, F3, F4): a key the factory cannot honour fails startup, and nothing that
// also reaches an internal task (a tier's model, a template's reviewerModel) may name a provider alias.
func TestProviderAliasesAreRefused(t *testing.T) {
	for name, c := range map[string]struct{ base, edit, want string }{
		"an unknown role": {byModel, "loops: {standard: {models: {merger: {standard: mistral-standard}}}}",
			`loops.standard.models: unknown role "merger"`},
		"an unknown tier": {byModel, "loops: {standard: {models: {implementer: {huge: mistral-standard}}}}",
			`loops.standard.models.implementer: "huge" is light, standard or frontier`},
		"another tier's alias": {byModel, "loops: {standard: {models: {implementer: {frontier: mistral-light}}}}",
			"loops.standard.models.implementer.frontier: mistral-light serves the light tier only"},
		"standard on frontier": {byModel, "loops: {standard: {models: {implementer: {frontier: mistral-standard}}}}",
			"loops.standard.models.implementer.frontier: mistral-standard serves the standard tier only"},
		"an unknown alias": {byModel, "loops: {standard: {models: {implementer: {standard: mistral-large}}}}",
			`loops.standard.models.implementer.standard: "mistral-large" is not a provider alias`},
		"a C5 name is not a provider alias": {byModel, "loops: {standard: {models: {implementer: {standard: tier-standard}}}}",
			`loops.standard.models.implementer.standard: "tier-standard" is not a provider alias`},
		"unpriced, prices by served model": {strings.Replace(byModel, "mistral-medium-2604: {input: 1.50, cacheRead: 0.15, output: 7.50}, ", "", 1),
			"loops: {standard: {models: {implementer: {standard: mistral-standard}}}}",
			"loops.standard.models.implementer.standard: mistral-standard needs meter.prices.mistral-medium-2604"},
		// A by-model meter prices the served model: a row under the alias alone never reaches it.
		"unpriced by served model, an alias-only row": {strings.Replace(strings.Replace(byModel, "mistral-medium-2604: {input: 1.50, cacheRead: 0.15, output: 7.50}, ", "", 1),
			"  prices: {", "  prices: {mistral-standard: {input: 1.50, cacheRead: 0.15, output: 7.50}, ", 1),
			"loops: {standard: {models: {implementer: {standard: mistral-standard}}}}",
			"loops.standard.models.implementer.standard: mistral-standard needs meter.prices.mistral-medium-2604"},
		"unpriced, prices by logical model": {priced, "loops: {standard: {models: {implementer: {standard: mistral-standard}}}}",
			"loops.standard.models.implementer.standard: mistral-standard needs meter.prices.mistral-standard"},
		"an unknown class": {byModel, "loops: {nope: {models: {implementer: {standard: mistral-standard}}}}",
			"loops.nope: a row is named default or a workflow"},
		"a tier's model": {strings.Replace(byModel, "light:    {model: agent-default", "light:    {model: mistral-light", 1), "",
			`tier light: model "mistral-light" is not a C5 logical name`},
		"a template's reviewerModel": {strings.Replace(byModel, "maxReviewRounds: 2}\n  investigate", "maxReviewRounds: 2, reviewerModel: kimi-frontier}\n  investigate", 1), "",
			`template pair: reviewerModel "kimi-frontier" is not a C5 logical name`},
		"a judge off the light tier": {byModel, "loops: {standard: {judge: kimi-frontier}}",
			`loops.standard.judge "kimi-frontier" is none or one of`},
		"an unpriced judge": {strings.Replace(byModel, "mistral-small-2603: {input: 0.15, cacheRead: 0.015, output: 0.60}", "", 1),
			"loops: {standard: {judge: mistral-light}}", "loops.standard.judge: mistral-light needs meter.prices.mistral-small-2603"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(c.base + c.edit + "\n"))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
	// Without meter.prices a tier keeps its token caps, and so does an alias: accepted.
	if _, err := Parse([]byte(good + gatewayIdentity + "loops: {standard: {models: {implementer: {standard: mistral-standard}}, judge: mistral-light}}\n")); err != nil {
		t.Fatalf("no prices, token caps: %v", err)
	}
	// A price row under the alias itself is the logical-model meter's (a query not by model).
	aliasPriced := strings.Replace(priced, "  prices: {", "  prices: {mistral-standard: {input: 1.50, cacheRead: 0.15, output: 7.50}, ", 1)
	if _, err := Parse([]byte(aliasPriced + gatewayIdentity + "loops: {standard: {models: {implementer: {standard: mistral-standard}}}}\n")); err != nil {
		t.Fatalf("priced under the alias: %v", err)
	}
}

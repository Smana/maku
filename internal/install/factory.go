// SPDX-License-Identifier: Apache-2.0

package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/Smana/maku/internal/factory/resources"
)

// factoryConfig is the factory's whole config, as one file: what the factory needs to start outside
// the demo (step 6 PR G's finding): its logins and files, the broker, the humans' API, a meter, the
// catalogue and one entry per repository. FactoryObjects splits it into the file and the Factory,
// Repository and MergePolicy objects (D8), as `maku config migrate` does.
func factoryConfig(in Inputs) map[string]any {
	a := in.Answers
	ns := in.ns()
	checks := anys(a.List("checks"))
	if len(checks) == 0 {
		checks = []any{"ci"}
	}
	factoryLogin := in.factorySlug() + "[bot]"
	repos := make([]any, 0, len(a.List("repositories")))
	for _, r := range repoNames(a) {
		repos = append(repos, map[string]any{
			"name":         r,
			"maintainers":  anys(a.List("maintainers")),
			"triggerLabel": "factory/ready",
			// Every class starts in shadow: the factory judges and posts its gate, and merges nothing on
			// its own until a maintainer promotes a class (docs/merge-policies.md).
			"classes": map[string]any{
				"docs-links": map[string]any{"shadow": true, "allowedFiles": []any{`^.+\.(md|mdx|adoc|rst)$`}},
				"revert":     map[string]any{"shadow": true, "allowedFiles": []any{`^.+$`}},
			},
			"merge": map[string]any{
				"requiredChecks": checks, "verifyChecks": checks, "leakScanCheck": checks[0],
				"gateStatus": true, "policyBot": false, "mergerLogin": factoryLogin,
				"autoMergesPerDay": 5, "fixRuns": 2, "checkReruns": 1,
				"verifyFor": "30m", "revertWindow": "168h", "breaker": map[string]any{"window": 10, "maxReverts": 1},
			},
		})
	}
	runsSubject := `system:serviceaccount:agents:run-.+`
	api := map[string]any{
		"listen":        ":8082",
		"clientIDFiles": []any{"/etc/agent-factory-oidc/web-client-id", "/etc/agent-factory-oidc/cli-client-id"},
	}
	if a.Bool("slack.enabled") {
		api["sponsored"] = []any{map[string]any{
			"name": slackPrincipal, "clientIDFile": "/etc/agent-factory-sponsored/slack-app-client-id",
			"sponsor": a.List("maintainers")[0], "repositories": anys(repoNames(a)), "dailyUSD": 5, "maxCostUSD": 2,
		}}
	}
	cfg := map[string]any{
		"factory":      in.Answers.String("release"),
		"factoryLogin": factoryLogin,
		"agentsLogin":  in.agentsSlug() + "[bot]",
		"roomsURL":     strings.TrimSuffix(a.String("web.url"), "/"),
		"broker": map[string]any{ //nolint:gosec // file paths and a URL, no credential
			"url":       fmt.Sprintf("https://room-broker.%s.svc.cluster.local:8443", ns),
			"caFile":    "/etc/agent-factory/openbao-ca/ca.crt",
			"tokenFile": "/var/run/secrets/agents/rooms/token",
		},
		"runs": map[string]any{"group": "maku.ogenki.io", "namespace": "agents", "namePrefix": "run-", "gatewayIdentity": true},
		"github": map[string]any{
			"appIDFile": "/etc/agent-factory-github/app_id", "privateKeyFile": "/etc/agent-factory-github/private_key",
		},
		"api":  api,
		"poll": map[string]any{"issues": "60s", "tasks": "30s", "meter": "30s"},
		// The meter reads agentgateway's own series for the runs (GW10), and its 429s.
		"meter": map[string]any{
			"url":   a.String("meter.url"),
			"query": fmt.Sprintf(`sum by (ar_agent, gen_ai_request_model, gen_ai_token_type, pod) (agentgateway_gen_ai_client_token_usage_sum{ar_agent=~%q})`, runsSubject),
			"throttle": map[string]any{
				"promQuery": fmt.Sprintf(`sum by (ar_agent) (increase(agentgateway_requests_total{status="429", reason="RateLimit", ar_agent=~%q}[5m])) > 0`, runsSubject),
			},
		},
		"defaults": map[string]any{"template": "solo", "tier": "standard", "dataClass": "public", "predictedClass": "review"},
		"loops": map[string]any{
			"review":     map[string]any{"scope": "scoper", "judge": "tier-light"},
			"docs-links": map[string]any{"scope": "scoper", "judge": "tier-light"},
		},
		"tiers": map[string]any{
			"light":    map[string]any{"model": "tier-light", "runTokens": 300000, "taskTokens": 600000, "runMinutes": 20},
			"standard": map[string]any{"model": "agent-default", "runTokens": 1500000, "taskTokens": 3000000, "runMinutes": 45},
			"frontier": map[string]any{"model": "agent-default", "runTokens": 4000000, "taskTokens": 8000000, "runMinutes": 90},
		},
		"templates": map[string]any{
			"solo":        map[string]any{"roles": []any{"implementer", "reviewer"}, "maxReviewRounds": 2},
			"pair":        map[string]any{"roles": []any{"implementer", "reviewer"}, "maxReviewRounds": 2},
			"trio":        map[string]any{"roles": []any{"implementer", "reviewer"}, "maxReviewRounds": 2},
			"investigate": map[string]any{"roles": []any{"triager"}},
		},
		"caps":         map[string]any{"activeTasks": 3, "concurrentRuns": 2, "tasksPerDay": 20, "maxTextBytes": 14336, "awaitingHumanWIP": 5},
		"repositories": repos,
	}
	return cfg
}

// chartWritten are the config keys the maku-factory chart writes itself from apps.*: a values file
// that sets them fails the render.
var chartWritten = []string{"mergerAppIDFile", "mergerKeyFile", "agentsAppIDFile", "agentsKeyFile"}

// FactoryObjects is the factory's file and its objects (D8): the file goes into the values
// (maku-factory.config), the objects into factory/. Before the split the file must parse as the
// factory's own config, so what is written starts.
func FactoryObjects(in Inputs) (file map[string]any, manifests []byte, err error) {
	cfg := mergeMaps(factoryConfig(in), digMap(questionValues(in.Answers), "maku-factory.config"))
	gh := cfg["github"].(map[string]any)
	// The chart writes the folded merger's files; Parse wants them to judge the merge block.
	gh["mergerAppIDFile"], gh["mergerKeyFile"] = gh["appIDFile"], gh["privateKeyFile"]
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, nil, err
	}
	manifests, residual, err := resources.Migrate(raw, in.ns())
	if err != nil {
		return nil, nil, fmt.Errorf("the factory's config: %w", err)
	}
	if err := yaml.Unmarshal(residual, &file); err != nil {
		return nil, nil, err
	}
	if g, ok := file["github"].(map[string]any); ok {
		for _, k := range chartWritten {
			delete(g, k)
		}
	}
	return file, manifests, nil
}

func factoryResidual(in Inputs) (map[string]any, error) {
	f, _, err := FactoryObjects(in)
	return f, err
}

// Ruleset is the merge gate's ruleset (docs/factory.md#branch-protection-for-the-merge-gate): required
// for agent pull requests, bypassed on pull requests by the maintain, write and admin roles and by the
// factory App, which posts maku/merge-gate and merges (the merger folded in).
func Ruleset(appID string) ([]byte, error) {
	id, err := jsonNumber(appID)
	if err != nil {
		return nil, err
	}
	r := map[string]any{
		"name":        "default branch: the merge gate, for agent pull requests",
		"target":      "branch",
		"enforcement": "active",
		"conditions":  map[string]any{"ref_name": map[string]any{"include": []any{"~DEFAULT_BRANCH"}, "exclude": []any{}}},
		"bypass_actors": []any{
			map[string]any{"actor_id": 2, "actor_type": "RepositoryRole", "bypass_mode": "pull_request"},
			map[string]any{"actor_id": 4, "actor_type": "RepositoryRole", "bypass_mode": "pull_request"},
			map[string]any{"actor_id": 5, "actor_type": "RepositoryRole", "bypass_mode": "pull_request"},
			map[string]any{"actor_id": id, "actor_type": "Integration", "bypass_mode": "pull_request"},
		},
		"rules": []any{map[string]any{"type": "required_status_checks", "parameters": map[string]any{
			"strict_required_status_checks_policy": false,
			"required_status_checks":               []any{map[string]any{"context": "maku/merge-gate", "integration_id": id}},
		}}},
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func jsonNumber(s string) (json.Number, error) {
	if s == "" {
		return "", fmt.Errorf("the factory App's id is unknown")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return "", fmt.Errorf("the factory App's id %q is not a number", s)
		}
	}
	return json.Number(s), nil
}

// splitManifests is the factory objects, one file each: factory.yaml, repository-<name>.yaml and
// mergepolicy-<name>.yaml.
func splitManifests(manifests []byte) (map[string][]byte, error) {
	objs, err := resources.Decode(manifests)
	if err != nil {
		return nil, err
	}
	docs := bytes.Split(manifests, []byte("\n---\n"))
	if len(docs) != len(objs) {
		return nil, fmt.Errorf("the factory's manifests: %d documents, %d objects", len(docs), len(objs))
	}
	out := map[string][]byte{}
	for i, o := range objs {
		var name string
		switch o.Kind {
		case resources.KindFactory:
			name = "factory.yaml"
		case resources.KindRepository:
			name = "repository-" + o.Name + ".yaml"
		default:
			name = "mergepolicy-" + o.Name + ".yaml"
		}
		out[name] = append(bytes.TrimSpace(docs[i]), '\n')
	}
	return out, nil
}

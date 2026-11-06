#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Unit tests of lib.sh's functions (task demo:check). No cluster, no docker, no network.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
# shellcheck source=hack/demo/lib.sh
. hack/demo/lib.sh
fail=0
ok() { "$@" 2>/dev/null || { echo "FAIL: expected success: $*" >&2; fail=1; }; }
no() { if "$@" 2>/dev/null; then echo "FAIL: expected failure: $*" >&2; fail=1; fi; }
eq() { [[ $1 == "$2" ]] || { echo "FAIL: $3: got '$1', want '$2'" >&2; fail=1; }; }
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT

# the state file
no demo_state_done "$tmp" cluster
demo_state_mark "$tmp" cluster; demo_state_mark "$tmp" cluster; demo_state_mark "$tmp" app
ok demo_state_done "$tmp" cluster; ok demo_state_done "$tmp" app
no demo_state_done "$tmp" clus
eq "$(wc -l <"$tmp/state" | tr -d ' ')" 2 "a step is recorded once"

# .env parsing
cat >"$tmp/.env" <<'ENV'
# a comment
MODEL_NAME=glm-5
export MODEL_BASE_URL="https://api.example.com/v1"
MODEL_API_KEY='sk-a=b c'
EMPTY=
MODEL_NAME_X=other
ENV
eq "$(demo_env_get MODEL_NAME "$tmp/.env")" glm-5 "plain value (a longer name does not match)"
eq "$(demo_env_get MODEL_BASE_URL "$tmp/.env")" https://api.example.com/v1 "export and double quotes"
eq "$(demo_env_get MODEL_API_KEY "$tmp/.env")" 'sk-a=b c' "single quotes, = and a space kept"
eq "$(demo_env_get EMPTY "$tmp/.env")" '' "an empty value"
eq "$(demo_env_get ABSENT "$tmp/.env")" '' "an absent key"
eq "$(demo_env_get MODEL_NAME "$tmp/none")" '' "an absent file"

# the CLUSTER choice
eq "$(demo_pick_cluster '' 1 1)" k3d "default prefers k3d"
eq "$(demo_pick_cluster '' 0 1)" kind "default falls back to kind"
no demo_pick_cluster '' 0 0
eq "$(demo_pick_cluster kind 1 1)" kind "kind when asked"
no demo_pick_cluster k3d 0 1
no demo_pick_cluster kind 1 0
eq "$(demo_pick_cluster current 0 0)" current "current needs no tool"
no demo_pick_cluster minikube 1 1

# repository names and the private-repository refusal
ok demo_repo_valid Smana/maku-demo; ok demo_repo_valid octocat/Hello-World; ok demo_repo_valid a/b.c_d
no demo_repo_valid nope; no demo_repo_valid a/b/c; no demo_repo_valid 'a b/c'; no demo_repo_valid ''
ok demo_public_only false
no demo_public_only true
no demo_public_only ''
[[ $(demo_public_only true 2>&1) == *"public repositories only"* ]] || { echo "FAIL: the private refusal names the reason" >&2; fail=1; }

# the seeded issues
n="$(demo_issue_count examples/demo-repo/ISSUES.md)"
eq "$n" 2 "two seeded issues"
eq "$(demo_issue_field examples/demo-repo/ISSUES.md 1 title)" "Fix the broken link in docs/guide.md" "issue 1 title"
eq "$(demo_issue_field examples/demo-repo/ISSUES.md 1 labels)" "class:docs-links" "issue 1 labels"
eq "$(demo_issue_field examples/demo-repo/ISSUES.md 2 labels)" "documentation" "issue 2 labels"
[[ $(demo_issue_field examples/demo-repo/ISSUES.md 1 body) == "The second link in"*"Change only that file." ]] || { echo "FAIL: issue 1 body" >&2; fail=1; }
[[ $(demo_issue_field examples/demo-repo/ISSUES.md 1 body) != *"Add a sentence"* ]] || { echo "FAIL: issue 1 body runs into issue 2" >&2; fail=1; }
[[ $(demo_issue_field examples/demo-repo/ISSUES.md 2 body) == *"says what the repository is"*"small issues." ]] || { echo "FAIL: issue 2 body" >&2; fail=1; }
printf '## T\n\nLabels: a, b\n\nbody\n' >"$tmp/i.md"
eq "$(demo_issue_field "$tmp/i.md" 1 labels | paste -sd, -)" "a,b" "two labels"

# the ruleset
rs="$(demo_ruleset_json)"
yq -e -p json '.name == "maku-demo" and .conditions.ref_name.include[0] == "~DEFAULT_BRANCH"' - <<<"$rs" >/dev/null 2>&1 || { echo "FAIL: ruleset name or target" >&2; fail=1; }
yq -e -p json '[.rules[].type] | contains(["pull_request", "required_status_checks"])' - <<<"$rs" >/dev/null 2>&1 || { echo "FAIL: ruleset rules" >&2; fail=1; }
yq -e -p json '.rules[] | select(.type == "required_status_checks") | .parameters.required_status_checks | (length == 1 and .[0].context == "ci" and .[0].integration_id == 15368)' - <<<"$rs" >/dev/null 2>&1 || { echo "FAIL: ruleset requires ci from integration 15368" >&2; fail=1; }
[[ $rs != *merge-gate* ]] || { echo "FAIL: the ruleset names maku/merge-gate" >&2; fail=1; }

# pid guard
no demo_pid_is_forward 'abc'
no demo_pid_is_forward $$

# the values file renders, is 0600, leaks nothing odd, and the chart's config parses (plan B's Parse test)
openssl genrsa -out "$tmp/app.pem" 2048 2>/dev/null
export DEMO_SLUG=maku-demo-it DEMO_REPO=octocat/it DEMO_LOGIN=octocat MODEL_BASE_URL=http://model.example.test/v1 \
  MODEL_NAME="m'1" MODEL_API_KEY=sk-test GITHUB_APP_ID=123 GITHUB_APP_KEY_FILE="$tmp/app.pem"
d='v0.0.0@sha256:0000'
export DEMO_TAG_BROKER=$d DEMO_TAG_RUN_CONTROLLER=$d DEMO_TAG_HARNESS=$d DEMO_TAG_BRIDGE=$d DEMO_TAG_FACTORY=$d
no env GITHUB_APP_KEY_FILE=/nonexistent bash -c '. hack/demo/lib.sh; demo_render_values /dev/null'
demo_render_values "$tmp/values.yaml"
eq "$(stat -c %a "$tmp/values.yaml" 2>/dev/null || stat -f %Lp "$tmp/values.yaml")" 600 "values file mode"
yq -e '.maku-factory.config.factoryLogin == "maku-demo-it[bot]" and .maku-factory.config.repositories[0].merge.mergerLogin == "maku-demo-it[bot]"' "$tmp/values.yaml" >/dev/null 2>&1 || { echo "FAIL: values: the bot logins" >&2; fail=1; }
yq -e '.maku-factory.config.repositories[0].name == "octocat/it" and .maku-factory.config.repositories[0].maintainers[0] == "octocat"' "$tmp/values.yaml" >/dev/null 2>&1 || { echo "FAIL: values: repository and maintainer" >&2; fail=1; }
yq -e '.demo.model.name == "m'"'"'1" and (.demo.githubApp.privateKey | test("BEGIN"))' "$tmp/values.yaml" >/dev/null 2>&1 || { echo "FAIL: values: quoting and the key" >&2; fail=1; }
yq -e '.maku-factory.config.repositories[0].classes.docs-links.shadow == true' "$tmp/values.yaml" >/dev/null 2>&1 || { echo "FAIL: values: shadow by default" >&2; fail=1; }
DEMO_SHADOW=false demo_render_values "$tmp/live.yaml"
yq -e '.maku-factory.config.repositories[0].classes.docs-links.shadow == false and .maku-factory.config.repositories[0].classes.revert.shadow == true' "$tmp/live.yaml" >/dev/null 2>&1 || { echo "FAIL: values: DEMO_SHADOW=false" >&2; fail=1; }
# the renderer's repositories[0] entry is the example's, key for key (apart from what the reader sets)
norm() { yq -o=json -I0 'del(.name, .maintainers, .merge.mergerLogin) | sort_keys(..)' - ; }
[[ "$(yq '.maku-factory.config.repositories[0]' "$tmp/values.yaml" | norm)" == "$(yq '.maku-factory.config.repositories[0]' charts/agent-factory/values-mine.example.yaml | norm)" ]] || { echo "FAIL: the rendered repositories[0] drifted from values-mine.example.yaml" >&2; fail=1; }
helm dependency build charts/agent-factory >/dev/null
if out="$(helm template af charts/agent-factory -n maku -f "$tmp/values.yaml")"; then
  yq ea 'select(.kind == "ConfigMap" and .metadata.name == "agent-factory-config") | .data."config.yaml"' - <<<"$out" >"$tmp/factory-config.yaml"
  eq "$(yq '.repositories[0].name' "$tmp/factory-config.yaml")" octocat/it "rendered chart config names the repository"
  CHART_DEMO_FACTORY_CONFIG="$tmp/factory-config.yaml" go test -count=1 -run TestRenderedDemoChartConfigParses ./internal/factory/config >"$tmp/parse.log" 2>&1 || { echo "FAIL: the rendered config does not parse:" >&2; cat "$tmp/parse.log" >&2; fail=1; }
else
  echo "FAIL: the values file does not render with the chart" >&2; fail=1
fi

[[ $fail == 0 ]] || exit 1
echo "demo lib tests passed"

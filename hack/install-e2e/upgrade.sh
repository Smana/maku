#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# The upgrade job (v4 step 7, PR G; D23, IN11): on kind-calico.sh's cluster, the newest release as a
# user installed it, upgraded in place to this checkout.
#   1. the release's own `maku install --print` (its binary, checked against the release's maku.sha256)
#      writes the tree from the release's own answers file; its two charts come from ghcr.io, each
#      cosign-verified as the release workflow's, with the release's overlay.yaml and agentgateway's CRDs;
#   2. `maku preflight --strict` passes; an issue on the e2e stub becomes a Task, a room and a run;
#   3. this checkout's images are loaded (built here), then `helm upgrade` the CRD chart,
#      agentgateway's CRDs and the umbrella, with the release's values, this overlay.yaml and the images.
# It asserts:
#   - every Deployment keeps its UID, and every Task, AgentRun and room keeps its own;
#   - the broker's migrate applied only the new revisions: every revision the release recorded keeps
#     its checksum, and the ledger then holds exactly this checkout's migrations;
#   - `maku preflight --strict` (this checkout's) passes.
# UPGRADE_FROM picks the release: latest (the default), a tag, or head, which installs this checkout
# and upgrades it to itself (the harness's own test: it proves no upgrade). A release that predates the
# install jobs (no hack/install-e2e/kind-calico.answers.yaml at its tag, or no `maku install`) cannot
# be installed this way: the job then skips, and says so in its summary.
set -euo pipefail
KIND_MINOR="${KIND_MINOR:-1.37}"
TAG="${TAG:-install-upgrade-$$}"
# shellcheck source=hack/install-e2e/kind-calico.sh
. "$(dirname "${BASH_SOURCE[0]}")/kind-calico.sh"

UPGRADE_FROM="${UPGRADE_FROM:-latest}"
FROM=""            # the release's tag; empty for UPGRADE_FROM=head
FROM_DIR="$work/from" # the release's binary, charts, answers, overlay and agentgateway CRDs

skip() {
  echo "SKIP: $*"
  [[ -z ${GITHUB_STEP_SUMMARY:-} ]] || printf '### install upgrade skipped: nothing ran\n\nA green check here is **not** a pass. %s\n' "$*" >>"$GITHUB_STEP_SUMMARY"
  exit 0
}

# resolve_from: the release to start from, and its files; skip when it cannot be installed this way.
resolve_from() {
  if [[ $UPGRADE_FROM == head ]]; then
    step "UPGRADE_FROM=head: this checkout, upgraded to itself (the harness's own test, not D23's proof)"
    return
  fi
  FROM=$UPGRADE_FROM
  if [[ $FROM == latest ]]; then
    FROM=$(curl -fsSL ${GH_TOKEN:+-H "Authorization: Bearer $GH_TOKEN"} https://api.github.com/repos/Smana/maku/releases/latest | jq -er .tag_name)
  fi
  [[ $FROM =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "UPGRADE_FROM=$UPGRADE_FROM: $FROM is not a release tag"
  step "from $FROM to this checkout"
  mkdir -p "$FROM_DIR"
  git fetch -q --depth=1 origin "refs/tags/$FROM:refs/tags/$FROM" 2>/dev/null || git rev-parse -q --verify "$FROM^{commit}" >/dev/null ||
    die "cannot fetch the tag $FROM"
  git cat-file -e "$FROM:hack/install-e2e/$LEG.answers.yaml" 2>/dev/null ||
    skip "$FROM predates the install jobs: its tag has no hack/install-e2e/$LEG.answers.yaml. The first release this job can start from is the first that ships them with \`maku install --print\`."
  git archive "$FROM" hack/install-e2e config/agentgateway | tar -x -C "$FROM_DIR"
  local arch
  case $(uname -m) in x86_64) arch=amd64 ;; aarch64 | arm64) arch=arm64 ;; *) die "no maku release binary for $(uname -m)" ;; esac
  curl -fsSL -o "$FROM_DIR/maku-linux-$arch" "https://github.com/Smana/maku/releases/download/$FROM/maku-linux-$arch"
  curl -fsSL -o "$FROM_DIR/maku.sha256" "https://github.com/Smana/maku/releases/download/$FROM/maku.sha256"
  (cd "$FROM_DIR" && grep -E " \*?maku-linux-$arch\$" maku.sha256 | sha256sum -c --quiet -) || die "$FROM's maku-linux-$arch does not match its maku.sha256"
  mv "$FROM_DIR/maku-linux-$arch" "$FROM_DIR/maku"
  chmod +x "$FROM_DIR/maku"
  "$FROM_DIR/maku" install --help >/dev/null 2>&1 || skip "$FROM's maku has no install command."
}

# from_wizard: the release's wizard, from the release's answers; the images' digests come from ghcr.io.
from_wizard() {
  [[ -n $FROM ]] || { wizard; return; }
  step "$FROM's maku install --print --answers $LEG.answers.yaml (its own)"
  "$FROM_DIR/maku" install --print --out "$OUT" --answers "$FROM_DIR/hack/install-e2e/$LEG.answers.yaml" >/dev/null
}

# pull_verified CHART: the release's chart from ghcr.io, its digest cosign-verified as the release
# workflow's (docs/security.md's identity).
pull_verified() {
  local out d
  out=$(h pull "oci://ghcr.io/smana/charts/$1" --version "${FROM#v}" -d "$FROM_DIR" 2>&1) || die "helm pull $1 ${FROM#v}: $out"
  d=$(sed -nE 's/^Digest: (sha256:[0-9a-f]{64})$/\1/p' <<<"$out")
  [[ -n $d ]] || die "helm pull $1 printed no digest: $out"
  cosign verify "ghcr.io/smana/charts/$1@$d" --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    --certificate-identity-regexp '^https://github\.com/Smana/maku/\.github/workflows/release\.yaml@refs/tags/v' >/dev/null 2>&1 ||
    die "ghcr.io/smana/charts/$1@$d is not signed by maku's release workflow"
  echo "ok: $1 ${FROM#v} ($d) is signed by the release workflow"
}

# from_install: the release, in README.md's order, from its own charts, overlay and CRDs.
from_install() {
  [[ -n $FROM ]] || { install; return; }
  step "install $FROM: its CRD chart, agentgateway's CRDs, the Secrets, its umbrella, factory/"
  pull_verified agent-factory-crds
  pull_verified agent-factory
  h install "$RELEASE-crds" "$FROM_DIR/agent-factory-crds-${FROM#v}.tgz" -n "$NS" -f "$OUT/crds/values.yaml" --timeout 2m >/dev/null
  k apply --server-side -f "$FROM_DIR/config/agentgateway" >/dev/null
  wait_crds -l app.kubernetes.io/part-of=agent-factory crd
  secrets
  leg_before_umbrella
  local args=(-f "$OUT/values.yaml" -f "$FROM_DIR/hack/install-e2e/overlay.yaml")
  [[ ! -f $FROM_DIR/hack/install-e2e/$LEG-values.yaml ]] || args+=(-f "$FROM_DIR/hack/install-e2e/$LEG-values.yaml")
  h install "$RELEASE" "$FROM_DIR/agent-factory-${FROM#v}.tgz" -n "$NS" "${args[@]}" --timeout 5m >/dev/null
  k apply -n "$NS" -f "$OUT/factory/" >/dev/null
}

stub_api() { # METHOD PATH JSON: the e2e stub's GitHub API, through STUB_PORT's port-forward
  curl -fsS -X "$1" -H 'Content-Type: application/json' --data "$3" "http://127.0.0.1:$STUB_PORT$2"
}

# drive_task: an issue on the e2e stub, labelled by a maintainer, until the factory has made its Task,
# its room and its run.
drive_task() {
  step "an issue becomes a Task, a room and a run, before the upgrade"
  port_forward e2e svc/e2e-stub 8080
  STUB_PORT=$PF_PORT
  local issue task="" runs="" rooms=""
  issue=$(stub_api POST /repos/owner/repo/issues '{"title":"docs: fix the broken links","author":"dev","body":"The README links to a page that moved.\n\n## Acceptance check\nEvery link in README.md resolves."}' | jq -er .number)
  stub_api POST "/repos/owner/repo/issues/$issue/labels" '{"actor":"dev","labels":["class:docs","factory/ready"]}' >/dev/null
  for _ in $(seq 150); do
    task=$(k -n "$NS" get tasks -o json | jq -r --argjson n "$issue" '[.items[] | select(.spec.issue == $n)][0].metadata.name // empty')
    runs=$(k get agentruns -A -o name 2>/dev/null | wc -l)
    rooms=$(psql_rooms -c 'SELECT count(*) FROM public.rooms')
    [[ -z $task || $runs == 0 || $rooms == 0 ]] || break
    sleep 4
  done
  [[ -n $task ]] || die "issue #$issue never became a Task"
  [[ $rooms != 0 ]] || die "the Task $task has no room in the broker's database"
  [[ $runs != 0 ]] || die "the Task $task never started a run"
  echo "ok: issue #$issue is Task $task, with $rooms room(s) and $runs run(s)"
}

# leg_diagnostics: every factory replica's log (the leader is the one that polls), the Tasks, and the
# stub's view of the issues.
leg_diagnostics() {
  k -n "$NS" logs -l app.kubernetes.io/name=agent-factory --prefix --all-containers --tail=120 | grep -v readiness || true
  k -n "$NS" get tasks -o wide || true
  [[ -z ${STUB_PORT:-} ]] || curl -fsS "http://127.0.0.1:$STUB_PORT/repos/owner/repo/issues" | jq -c '.[] | {number, labels: [.labels[].name], state}' || true
}

psql_rooms() { k -n "$PG_NS" exec deploy/pg -- psql -qAt -v ON_ERROR_STOP=1 -U postgres -d rooms "$@"; }

# snapshot: what an upgrade in place must keep, one line each.
snapshot() {
  k -n "$NS" get deploy -o jsonpath='{range .items[*]}deploy {.metadata.name} {.metadata.uid}{"\n"}{end}'
  k -n "$NS" get tasks -o jsonpath='{range .items[*]}task {.metadata.name} {.metadata.uid}{"\n"}{end}'
  k get agentruns -A -o jsonpath='{range .items[*]}agentrun {.metadata.namespace}/{.metadata.name} {.metadata.uid}{"\n"}{end}'
  psql_rooms -c 'SELECT room_id FROM public.rooms ORDER BY 1' | sed 's/^/room /'
}

revisions() { psql_rooms -c 'SELECT version, checksum FROM public.room_broker_revisions ORDER BY 1'; }

upgrade() {
  step "upgrade to this checkout: the CRD chart, agentgateway's CRDs, then the umbrella"
  h upgrade "$RELEASE-crds" charts/agent-factory-crds -n "$NS" -f "$OUT/crds/values.yaml" --timeout 2m >/dev/null
  k apply --server-side -f config/agentgateway >/dev/null
  wait_crds -l app.kubernetes.io/part-of=agent-factory crd
  # The migration a user makes (docs/upgrading.md#the-chart-contract): profile is removed in v4 step 8,
  # and the schema refuses a values file that still sets it. A release before step 8 wrote it.
  yq -i 'del(.profile)' "$OUT/values.yaml"
  local args
  mapfile -t args < <(umbrella_args)
  h dependency build charts/agent-factory >/dev/null
  h upgrade "$RELEASE" charts/agent-factory -n "$NS" "${args[@]}" --timeout 5m >/dev/null
}

upgrade_checks() {
  step "the upgrade kept the install"
  local line
  while IFS= read -r line; do
    grep -qxF "$line" <<<"$after" || die "the upgrade lost or replaced: $line"
  done <<<"$before"
  echo "ok: every Deployment, Task, AgentRun and room keeps its UID ($(wc -l <<<"$before") objects)"

  local want got_after
  while IFS= read -r line; do
    grep -qxF "$line" <<<"$revs_after" || die "the migrate rewrote or dropped the release's revision: $line"
  done <<<"$revs_before"
  want=$(find internal/store/migrations -name '*.sql' -printf '%f\n' | sed -E 's/_.*//' | sort)
  got_after=$(cut -d'|' -f1 <<<"$revs_after" | sort)
  [[ $want == "$got_after" ]] || die "the ledger does not hold exactly this checkout's migrations: $(diff <(echo "$want") <(echo "$got_after"))"
  echo "ok: the migrate kept the release's $(wc -l <<<"$revs_before") revisions and applied $(($(wc -l <<<"$revs_after") - $(wc -l <<<"$revs_before"))) new one(s)"
}

upgrade_main() {
  trap onexit EXIT
  resolve_from
  build_images
  from_wizard
  step "create the cluster"
  created=true
  cluster_create
  for i in "${IMAGES[@]}"; do cluster_load "ghcr.io/smana/$i:$TAG"; done
  stub_up
  prereqs
  from_install
  ready
  if [[ -n $FROM ]]; then MAKU="$FROM_DIR/maku" preflight; else preflight; fi
  drive_task
  before=$(snapshot)
  revs_before=$(revisions)
  upgrade
  ready
  after=$(snapshot)
  revs_after=$(revisions)
  upgrade_checks
  preflight
  echo "upgrade OK: ${FROM:-this checkout} -> this checkout"
}

upgrade_main

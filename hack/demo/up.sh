#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# task demo: the Agent Factory on a laptop, in one command. A kind or k3d cluster, a GitHub App, your
# repository from the template, the umbrella chart with profile=demo, and the room page.
# Resumes where it stopped (.demo/state), prints the time per step, never prints a key.
#
# Environment, then .env, for every variable below; the six asked ones (MODEL_BASE_URL, MODEL_NAME,
# MODEL_API_KEY, DEMO_REPO, DEMO_LOGIN, DEMO_SLUG) then fall back to the answers of an earlier run, else ask:
#   MODEL_BASE_URL MODEL_NAME MODEL_API_KEY   an OpenAI-compatible model
#   DEMO_REPO=owner/name                      your repository (public); created from Smana/maku-demo
#   GITHUB_APP_ID GITHUB_APP_KEY_FILE         an existing App (with DEMO_SLUG): skips the browser
#   CLUSTER=k3d|kind|current                  default k3d if installed, else kind
#   MAKU_VERSION                              the images' tag; default: this checkout's tag, else the last answer.
#                                             A checkout ahead of a release needs a matching one (a PR pre-release tag)
#   DEMO_LIVE=1                               turn the docs-links class live (Q13); kept for the next run, DEMO_LIVE=0 undoes it
#   DEMO_MERGE_POLICY=1                       the merge-policy switches: /settings is served, DEMO_LOGIN is the operator;
#                                             kept for the next run, DEMO_MERGE_POLICY=0 undoes it
#   DEMO_NO_GH=1                              never use gh, never write to GitHub
#   DEMO_LOGIN                                the GitHub login allowed to file work
# .demo/answers.env (0600) keeps your answers, the model key included, so a re-run does not ask again;
# task demo:down removes it. .demo/values.yaml (0600) holds the model key and the App key too.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
ROOT="$PWD"
# shellcheck source=hack/quickstart/versions.env
. hack/quickstart/versions.env
# shellcheck source=hack/demo/lib.sh
. hack/demo/lib.sh
demo_env_load "$ROOT/.env" MODEL_BASE_URL MODEL_NAME MODEL_API_KEY DEMO_REPO DEMO_LOGIN DEMO_SLUG \
  GITHUB_APP_ID GITHUB_APP_KEY_FILE CLUSTER MAKU_VERSION DEMO_LIVE DEMO_MERGE_POLICY DEMO_NO_GH DEMO_CONFIRM_CONTEXT \
  K3S_IMAGE KIND_NODE_IMAGE

DEMO_DIR="$ROOT/.demo"
NS=maku
NAME=maku-demo
K3S_IMAGE="${K3S_IMAGE:-rancher/k3s:v1.35.0-k3s1}"
TEMPLATE=Smana/maku-demo
PORT=8080
umask 077
mkdir -p "$DEMO_DIR"
chmod 700 "$DEMO_DIR"

KCTX=''
# kc ARGS: kubectl against the confirmed context when CLUSTER=current (never the default's drift).
kc() { if [[ -n $KCTX ]]; then kubectl --context "$KCTX" "$@"; else kubectl "$@"; fi; }
hl() { if [[ -n $KCTX ]]; then helm --kube-context "$KCTX" "$@"; else helm "$@"; fi; }
die() { echo "FAIL: $*" >&2; exit 1; }
warn() { echo "WARN: $*" >&2; }
interactive() { [[ -t 0 ]]; }
step() { printf '\n==> %s\n' "$1"; STEP_START=$SECONDS; }
took() { printf '    done in %ds\n' $((SECONDS - STEP_START)); }

# save_answer VAR: keep an answer for the next run (.demo/answers.env, 0600).
save_answer() {
  local f="$DEMO_DIR/answers.env" v=${!1}
  { grep -v "^$1=" "$f" 2>/dev/null || true; printf '%s=%s\n' "$1" "$v"; } >"$f.tmp"
  mv "$f.tmp" "$f"
}
# resolve VAR PROMPT [secret]: the environment, .env, the last run's answer, else ask.
resolve() {
  local var=$1 v=${!1:-}
  [[ -n $v ]] || v=$(demo_env_get "$var" "$ROOT/.env")
  [[ -n $v ]] || v=$(demo_env_get "$var" "$DEMO_DIR/answers.env")
  if [[ -z $v ]]; then
    interactive || die "$var is not set: export it, put it in .env, or run in a terminal to be asked"
    if [[ ${3:-} == secret ]]; then read -rsp "$2: " v; echo; else read -rp "$2: " v; fi
  fi
  [[ -n $v ]] || die "$var is required"
  printf -v "$var" %s "$v"
  save_answer "$var"
}

# ---------------------------------------------------------------------------------------------
preflight() {
  step "preflight"
  case "$(uname -s)" in
  Linux) ;;
  Darwin) echo "    macOS is best effort: the demo is proven on Linux with Docker." ;;
  *) die "Linux or macOS only" ;;
  esac
  for t in docker helm kubectl curl; do command -v "$t" >/dev/null || die "$t is not installed"; done
  docker info >/dev/null 2>&1 || die "the Docker daemon is not reachable"
  docker buildx version >/dev/null 2>&1 || die "docker buildx is not installed (it resolves the images' digests)"
  MAKU_VERSION=${MAKU_VERSION:-}
  if [[ -z $MAKU_VERSION ]]; then
    MAKU_VERSION=$(git describe --tags --exact-match 2>/dev/null || true)
    [[ -n $MAKU_VERSION ]] || MAKU_VERSION=$(demo_env_get MAKU_VERSION "$DEMO_DIR/answers.env")
    [[ -n $MAKU_VERSION ]] || die "this checkout is not at a release tag, and the chart here may need images newer than the latest release (the release's run-controller refuses a chart it does not know). Set MAKU_VERSION to images that match this checkout, e.g. a pull request pre-release tag vX.Y.Z-prN.<sha8> that exists in ghcr.io/smana (docker buildx imagetools inspect ghcr.io/smana/maku-broker:<tag>), or git checkout a release tag."
  fi
  save_answer MAKU_VERSION
  echo "    maku $MAKU_VERSION"
  local have_k3d=0 have_kind=0
  command -v k3d >/dev/null && have_k3d=1
  command -v kind >/dev/null && have_kind=1
  if [[ -f $DEMO_DIR/cluster ]]; then CLUSTER_KIND=$(<"$DEMO_DIR/cluster")
  else CLUSTER_KIND=$(demo_pick_cluster "${CLUSTER:-}" "$have_k3d" "$have_kind") || exit 1; fi
  HAVE_GH=0
  if [[ -z ${DEMO_NO_GH:-} ]] && command -v gh >/dev/null && gh auth status >/dev/null 2>&1; then HAVE_GH=1; fi
  echo "    cluster: $CLUSTER_KIND; gh: $([[ $HAVE_GH == 1 ]] && echo logged in || echo "not used (you will be told what to do by hand)")"
  took
}

# ---------------------------------------------------------------------------------------------
model() {
  step "model"
  resolve MODEL_BASE_URL "Model base URL (OpenAI-compatible, e.g. https://api.example.com/v1)"
  resolve MODEL_NAME "Model name"
  resolve MODEL_API_KEY "Model API key (not echoed)" secret
  demo_state_mark "$DEMO_DIR" model
  took
}

# maku_demo_app ARGS: `maku demo app` from the release's maku (fetched into .demo/bin, checksum-verified)
# when it has the command, else go run ./cmd/maku if go is on PATH, else die naming the ways out.
maku_demo_app() {
  local bin="$DEMO_DIR/bin/maku" bin_ok=0 go_ok=0
  if [[ $(cat "$DEMO_DIR/bin/maku.version" 2>/dev/null) != "$MAKU_VERSION" || ! -x $bin ]]; then
    rm -f "$bin"
    if demo_fetch_maku "$MAKU_VERSION" "$bin"; then printf '%s' "$MAKU_VERSION" >"$DEMO_DIR/bin/maku.version"; fi
  fi
  if demo_app_probe "$bin"; then bin_ok=1; fi
  command -v go >/dev/null && [[ -d cmd/maku ]] && go_ok=1
  case $(demo_maku_choice "$bin_ok" "$go_ok") in
  bin) "$bin" demo app "$@" ;;
  go) go run ./cmd/maku demo app "$@" ;;
  *) exit 1 ;;
  esac
}

github_app() {
  step "GitHub App"
  local app="$DEMO_DIR/app"
  APP_FROM_FLOW=0
  if [[ -n ${GITHUB_APP_ID:-} && -n ${GITHUB_APP_KEY_FILE:-} ]]; then
    [[ -r $GITHUB_APP_KEY_FILE ]] || die "GITHUB_APP_KEY_FILE is not readable"
    resolve DEMO_SLUG "The App's slug (its name in the URL github.com/apps/<slug>)"
    demo_login_valid "$DEMO_SLUG" || die "DEMO_SLUG must be an App slug (letters, digits, dashes), not '$DEMO_SLUG'"
    if ! demo_state_done "$DEMO_DIR" app || [[ $(cat "$app/app_id" 2>/dev/null) != "$GITHUB_APP_ID" ]]; then
      if [[ -s $app/slug ]]; then
        echo "    replacing the App from an earlier run; delete the old one on GitHub: https://github.com/settings/apps/$(<"$app/slug")/advanced"
      fi
      rm -rf "$app"; mkdir -p "$app"
      printf '%s' "$GITHUB_APP_ID" >"$app/app_id"
      printf '%s' "$DEMO_SLUG" >"$app/slug"
      install -m 600 "$GITHUB_APP_KEY_FILE" "$app/private_key"
    fi
    echo "    using the App $GITHUB_APP_ID from the environment (slug $DEMO_SLUG)"
  elif [[ -s $app/html_url && -s $app/app_id && -s $app/private_key && -s $app/slug ]]; then
    echo "    the App from the earlier run: $(cat "$app/html_url")"
    APP_FROM_FLOW=1
  else
    if [[ -d $app ]]; then
      echo "    a previous run left a half-made App: a NEW App will be created."
      [[ ! -s $app/slug ]] || echo "    Delete the old one on GitHub: https://github.com/settings/apps/$(cat "$app/slug")/advanced"
      [[ -s $app/slug ]] || echo "    Delete it on GitHub: https://github.com/settings/apps"
    fi
    rm -rf "$app"
    interactive || die "no GITHUB_APP_ID and GITHUB_APP_KEY_FILE, and no terminal for the browser flow"
    maku_demo_app --dir "$app"
    [[ -s $app/html_url ]] || die "the App was not created ($app/html_url is missing)"
    APP_FROM_FLOW=1
  fi
  DEMO_SLUG=$(<"$app/slug")
  GITHUB_APP_ID=$(<"$app/app_id")
  GITHUB_APP_KEY_FILE="$app/private_key"
  export GITHUB_APP_ID GITHUB_APP_KEY_FILE DEMO_SLUG
  demo_state_mark "$DEMO_DIR" app
  took
}

# ---------------------------------------------------------------------------------------------
repo_private() { # prints true/false/empty (on a 404, gh api prints the error body to stdout despite --jq)
  local v
  if [[ $HAVE_GH == 1 ]]; then v=$(gh api "repos/$DEMO_REPO" --jq .private 2>/dev/null || true)
  else v=$(demo_gh_curl "https://api.github.com/repos/$DEMO_REPO" 2>/dev/null | sed -n -E 's/.*"private": *(true|false).*/\1/p' | head -1 || true); fi
  demo_bool "$v"
}

seed_github() {
  local open i title body
  local labels=(factory/ready class:docs-links documentation)
  local existing
  existing=$(gh label list -R "$DEMO_REPO" -L 200 --json name --jq '.[].name' 2>/dev/null || gh label list -R "$DEMO_REPO" -L 200 | cut -f1)
  for l in "${labels[@]}"; do
    grep -qxF "$l" <<<"$existing" || gh label create "$l" -R "$DEMO_REPO" >/dev/null 2>&1 || warn "could not create the label $l"
  done
  open=$(gh issue list -R "$DEMO_REPO" --state open --limit 1) || { warn "could not list the issues of $DEMO_REPO: none seeded"; open=skip; }
  if [[ $open == skip ]]; then :
  elif [[ -z $open ]]; then
    local f="$ROOT/examples/demo-repo/ISSUES.md"
    for ((i = 1; i <= $(demo_issue_count "$f"); i++)); do
      title=$(demo_issue_field "$f" "$i" title)
      body=$(demo_issue_field "$f" "$i" body)
      local args=()
      while IFS= read -r l; do args+=(-l "$l"); done < <(demo_issue_field "$f" "$i" labels)
      gh issue create -R "$DEMO_REPO" -t "$title" -b "$body" "${args[@]}" >/dev/null || warn "could not create the issue '$title'"
    done
    echo "    seeded the issues from the template"
  else
    echo "    the repository has open issues: none seeded"
  fi
  if gh api "repos/$DEMO_REPO/rulesets" --jq '.[].name' 2>/dev/null | grep -qxF maku-demo; then
    echo "    the ruleset maku-demo exists"
  else
    if demo_ruleset_json | gh api -X POST "repos/$DEMO_REPO/rulesets" --input - >/dev/null 2>&1; then
      echo "    added the ruleset maku-demo: pull requests and the check ci (GitHub Actions)"
    else
      warn "could not add the ruleset (admin rights needed): require pull requests and the check 'ci' from GitHub Actions on the default branch by hand"
    fi
  fi
}

repository() {
  step "repository"
  resolve DEMO_REPO "Your repository, owner/name (public; created from $TEMPLATE if absent)"
  demo_repo_valid "$DEMO_REPO" || die "DEMO_REPO must be owner/name, not '$DEMO_REPO'"
  local priv
  priv=$(repo_private)
  if [[ -z $priv ]]; then
    if [[ $HAVE_GH == 1 ]]; then
      echo "    creating $DEMO_REPO from $TEMPLATE"
      gh repo create "$DEMO_REPO" --public --template "$TEMPLATE" >/dev/null || die "could not create $DEMO_REPO"
      sleep 3
    else
      echo "    $DEMO_REPO does not exist (or gh is not in use). Create it from the template:"
      echo "      https://github.com/$TEMPLATE/generate   (owner and name as above, Public)"
      interactive || die "$DEMO_REPO does not exist"
      read -rp "    Press Enter once it exists: " _
    fi
    priv=$(repo_private)
  fi
  demo_public_only "${priv:-}" || exit 1
  if [[ -z ${DEMO_LOGIN:-} && $HAVE_GH == 1 ]]; then DEMO_LOGIN=$(gh api user --jq .login 2>/dev/null || true); fi
  resolve DEMO_LOGIN "Your GitHub login (who may file work for the factory)"
  demo_login_valid "$DEMO_LOGIN" || die "DEMO_LOGIN must be a GitHub login, not '$DEMO_LOGIN'"
  if [[ $HAVE_GH == 1 ]] && ! gh api "repos/$DEMO_REPO/contents/.github/agent-factory.yaml" >/dev/null 2>&1; then
    warn "$DEMO_REPO has no .github/agent-factory.yaml (it is not from the template): the factory will ignore it."
    warn "add that file on the default branch, containing exactly:  factory: demo"
  fi
  local admin=false
  [[ $HAVE_GH != 1 ]] || admin=$(gh api "repos/$DEMO_REPO" --jq .permissions.admin 2>/dev/null || echo false)
  if [[ $admin == true ]]; then seed_github
  else
    [[ $HAVE_GH != 1 ]] || echo "    gh is logged in but has no admin rights on $DEMO_REPO."
    cat <<MSG
    By hand on https://github.com/$DEMO_REPO:
      - create the labels factory/ready, class:docs-links and documentation
      - add .github/agent-factory.yaml containing exactly  factory: demo  (the factory only works on a
        repository that names it; the template already has it)
      - open the issues of examples/demo-repo/ISSUES.md if the repository has none
      - add a ruleset on the default branch: require pull requests and the status check "ci"
        from GitHub Actions (never maku/merge-gate)
MSG
  fi
  if [[ $APP_FROM_FLOW == 1 ]]; then
    echo "    Install the App on $DEMO_REPO: $(cat "$DEMO_DIR/app/html_url")/installations/new"
    if interactive && ! demo_state_done "$DEMO_DIR" repository; then read -rp "    Press Enter once it is installed: " _; fi
  else
    echo "    Make sure the App is installed on $DEMO_REPO (without it the factory cannot find its installation)."
  fi
  demo_state_mark "$DEMO_DIR" repository
  took
}

# ---------------------------------------------------------------------------------------------
cluster() {
  step "cluster"
  case $CLUSTER_KIND in
  current)
    local ctx stored=''
    ctx=$(kubectl config current-context) || die "no current kubectl context"
    [[ ! -s $DEMO_DIR/context ]] || stored=$(<"$DEMO_DIR/context")
    if ! demo_context_ok "$stored" "$ctx"; then
      [[ -z $stored ]] || echo "    the current context is now '$ctx', not the '$stored' you confirmed."
      echo "    CLUSTER=current: the demo will install into the context '$ctx'."
      if interactive; then
        read -rp "    Type the context name to confirm: " a
        [[ $a == "$ctx" ]] || die "not confirmed"
      else
        [[ ${DEMO_CONFIRM_CONTEXT:-} == "$ctx" ]] || die "set DEMO_CONFIRM_CONTEXT=$ctx to confirm the context without a terminal"
      fi
      printf '%s' "$ctx" >"$DEMO_DIR/context"
    fi
    KCTX=$ctx
    ;;
  k3d | kind)
    export KUBECONFIG="$DEMO_DIR/kubeconfig"
    local exists=0
    case $CLUSTER_KIND in
    k3d) k3d cluster get "$NAME" >/dev/null 2>&1 && exists=1 ;;
    kind) kind get clusters 2>/dev/null | grep -qx "$NAME" && exists=1 ;;
    esac
    if [[ $exists == 1 ]]; then
      demo_state_done "$DEMO_DIR" cluster || die "a $CLUSTER_KIND cluster named $NAME exists and this script did not create it: delete it or use another CLUSTER"
      case $CLUSTER_KIND in
      k3d) k3d kubeconfig get "$NAME" >"$KUBECONFIG" ;;
      kind) kind export kubeconfig --name "$NAME" --kubeconfig "$KUBECONFIG" ;;
      esac
      echo "    reusing the cluster $NAME"
    else
      rm -f "$KUBECONFIG"
      case $CLUSTER_KIND in
      k3d)
        k3d cluster create "$NAME" --image "$K3S_IMAGE" --no-lb --wait \
          --k3s-arg '--disable=traefik@server:0' --k3s-arg '--disable=servicelb@server:0' \
          --kubeconfig-update-default=false --kubeconfig-switch-context=false
        k3d kubeconfig get "$NAME" >"$KUBECONFIG" ;;
      kind) kind create cluster --name "$NAME" --image "$KIND_NODE_IMAGE" --wait 120s --kubeconfig "$KUBECONFIG" ;;
      esac
    fi
    ;;
  esac
  printf '%s' "$CLUSTER_KIND" >"$DEMO_DIR/cluster"
  kc wait --for=condition=Ready node --all --timeout=120s >/dev/null 2>&1 || die "the cluster's nodes are not Ready after 120s (kubectl get nodes)"
  demo_state_mark "$DEMO_DIR" cluster
  took
}

# ---------------------------------------------------------------------------------------------
# rollout KIND/NAME: wait for it; when it fails, show its last logs and die.
rollout() {
  kc -n "$NS" rollout status "$1" --timeout=240s && return 0
  warn "$1 did not roll out; its last log lines:"
  kc -n "$NS" logs "$1" --tail=40 --all-containers 2>&1 | sed 's/^/      /' >&2 || true
  die "$1 did not roll out (kubectl -n $NS get pods; with a MAKU_VERSION older than this chart, the images refuse its values)"
}

install_chart() {
  step "install"
  local v=$MAKU_VERSION
  pin() {
    local d
    d=$(docker buildx imagetools inspect "ghcr.io/smana/$1:$v" --format '{{.Manifest.Digest}}') || die "ghcr.io/smana/$1:$v not found"
    demo_digest_ok "$d" || die "ghcr.io/smana/$1:$v gave '$d', not a sha256 digest"
    printf '%s@%s' "$v" "$d"
  }
  DEMO_TAG_BROKER=$(pin maku-broker)
  DEMO_TAG_RUN_CONTROLLER=$(pin maku-run-controller)
  DEMO_TAG_HARNESS=$(pin maku-harness)
  DEMO_TAG_BRIDGE=$(pin maku-bridge)
  DEMO_TAG_FACTORY=$(pin maku-factory)
  export DEMO_TAG_BROKER DEMO_TAG_RUN_CONTROLLER DEMO_TAG_HARNESS DEMO_TAG_BRIDGE DEMO_TAG_FACTORY
  DEMO_LIVE=${DEMO_LIVE:-$(demo_env_get DEMO_LIVE "$DEMO_DIR/answers.env")}
  save_answer DEMO_LIVE
  DEMO_MERGE_POLICY=${DEMO_MERGE_POLICY:-$(demo_env_get DEMO_MERGE_POLICY "$DEMO_DIR/answers.env")}
  DEMO_MERGE_POLICY=${DEMO_MERGE_POLICY:-0}
  save_answer DEMO_MERGE_POLICY
  DEMO_SHADOW=true
  [[ $DEMO_LIVE != 1 ]] || DEMO_SHADOW=false
  export DEMO_SHADOW DEMO_MERGE_POLICY DEMO_REPO DEMO_LOGIN MODEL_BASE_URL MODEL_NAME
  demo_render_values "$DEMO_DIR/values.yaml"
  hl dependency build charts/agent-factory >/dev/null
  hl upgrade --install af charts/agent-factory -n "$NS" --create-namespace -f "$DEMO_DIR/values.yaml" --timeout 5m >/dev/null
  echo "    waiting for the workloads"
  rollout sts/rooms-db
  rollout deploy/room-broker
  rollout deploy/run-controller
  FACTORY_OK=0
  local i
  for ((i = 0; i < 40; i++)); do
    if [[ $(kc -n "$NS" get deploy agent-factory -o jsonpath='{.status.readyReplicas}' 2>/dev/null) == 1 ]]; then FACTORY_OK=1; break; fi
    sleep 3
  done
  if [[ $FACTORY_OK == 1 ]]; then echo "    the factory is Ready"
  else
    warn "the factory is not Ready; its last log lines:"
    kc -n "$NS" logs deploy/agent-factory --tail=8 2>&1 | sed 's/^/      /' >&2 || true
    warn "if it says it cannot find the App's installation, install the App on $DEMO_REPO, then run task demo again"
  fi
  demo_state_mark "$DEMO_DIR" install
  took
}

# ---------------------------------------------------------------------------------------------
room_page() {
  step "room page"
  local pidf="$DEMO_DIR/port-forward.pid" pid='' i
  [[ ! -s $pidf ]] || pid=$(<"$pidf")
  if demo_pid_is_forward "$pid" && curl -fs -o /dev/null --max-time 3 "http://127.0.0.1:$PORT/"; then
    echo "    the port-forward is running (pid $pid)"
  else
    if demo_pid_is_forward "$pid"; then
      kill "$pid" 2>/dev/null || true
      for ((i = 0; i < 50; i++)); do demo_pid_is_forward "$pid" || break; sleep 0.1; done
    fi
    if (exec 3<>"/dev/tcp/127.0.0.1/$PORT") 2>/dev/null; then die "port $PORT is in use by something else: free it, the room page needs it"; fi
    local start=(kubectl)
    [[ -z $KCTX ]] || start+=(--context "$KCTX")
    start+=(-n "$NS" port-forward --address 127.0.0.1 deploy/room-broker "$PORT:8080")
    if command -v setsid >/dev/null; then setsid nohup "${start[@]}" </dev/null >"$DEMO_DIR/port-forward.log" 2>&1 &
    else nohup "${start[@]}" </dev/null >"$DEMO_DIR/port-forward.log" 2>&1 & fi
    echo $! >"$pidf"
    local ok=0
    for ((i = 0; i < 30; i++)); do
      if curl -fs -o /dev/null --max-time 2 "http://127.0.0.1:$PORT/"; then ok=1; break; fi
      sleep 1
    done
    [[ $ok == 1 ]] || { cat "$DEMO_DIR/port-forward.log" >&2; die "the room page does not answer on port $PORT"; }
    echo "    port-forward started (pid $(<"$pidf"))"
  fi
  demo_state_mark "$DEMO_DIR" room
  took
}

next_step() {
  local issue=''
  if [[ $HAVE_GH == 1 ]]; then
    issue=$(gh issue list -R "$DEMO_REPO" --label class:docs-links --state open --limit 1 --json url --jq '.[0].url' 2>/dev/null || true)
    [[ -n $issue ]] || warn "could not look up the issue to label: use the issues page"
  fi
  [[ -n $issue ]] || issue="https://github.com/$DEMO_REPO/issues"
  cat <<MSG

==> ready ($((SECONDS - T0))s in all)

  Room page:   http://localhost:$PORT$([[ $DEMO_MERGE_POLICY == 1 ]] && printf '\n  Settings:    http://localhost:%s/settings/merge-policies/%s' "$PORT" "$DEMO_REPO")
  Label this issue factory/ready:  $issue

You will see the factory pick it up in the room, open a pull request on $DEMO_REPO, and post the status
maku/merge-gate on it with what it would do ($([[ $DEMO_SHADOW == true ]] && echo "shadow: it merges nothing" || echo "LIVE: it merges")).
To let it merge for real, run:  DEMO_LIVE=1 task demo
The room page's port-forward keeps running in the background; task demo:down stops it and deletes the cluster.
MSG
}

T0=$SECONDS
preflight
model
github_app
repository
cluster
install_chart
room_page
next_step

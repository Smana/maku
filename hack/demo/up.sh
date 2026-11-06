#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# task demo: the Agent Factory on a laptop, in one command. A kind or k3d cluster, a GitHub App, your
# repository from the template, the umbrella chart with profile=demo, and the room page.
# Resumes where it stopped (.demo/state), prints the time per step, never prints a key.
#
# Environment (each also read from .env, then from the answers of an earlier run, else asked):
#   MODEL_BASE_URL MODEL_NAME MODEL_API_KEY   an OpenAI-compatible model
#   DEMO_REPO=owner/name                      your repository (public); created from Smana/maku-demo
#   GITHUB_APP_ID GITHUB_APP_KEY_FILE         an existing App (with DEMO_SLUG): skips the browser
#   CLUSTER=k3d|kind|current                  default k3d if installed, else kind
#   MAKU_VERSION                              default: the latest release
#   DEMO_LIVE=1                               turn the docs-links class live (Q13)
#   DEMO_NO_GH=1                              never use gh, never write to GitHub
#   DEMO_LOGIN                                the GitHub login allowed to file work
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
ROOT="$PWD"
# shellcheck source=hack/quickstart/versions.env
. hack/quickstart/versions.env
# shellcheck source=hack/demo/lib.sh
. hack/demo/lib.sh

DEMO_DIR="$ROOT/.demo"
NS=maku
NAME=maku-demo
K3S_IMAGE="${K3S_IMAGE:-rancher/k3s:v1.35.0-k3s1}"
TEMPLATE=Smana/maku-demo
PORT=8080
umask 077
mkdir -p "$DEMO_DIR"
chmod 700 "$DEMO_DIR"

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

# maku_demo_app: this checkout's `maku demo app`, from a binary that has it, else go run.
maku_demo_app() {
  if command -v maku >/dev/null && maku demo app --help 2>&1 | grep -q -- '-dir'; then maku demo app "$@"
  elif command -v go >/dev/null; then go run ./cmd/maku demo app "$@"
  else die "no maku with 'demo app' on PATH and no go to build one: install maku, or set GITHUB_APP_ID and GITHUB_APP_KEY_FILE"; fi
}

github_app() {
  step "GitHub App"
  local app="$DEMO_DIR/app"
  APP_FROM_FLOW=0
  if [[ -n ${GITHUB_APP_ID:-} && -n ${GITHUB_APP_KEY_FILE:-} ]]; then
    [[ -r $GITHUB_APP_KEY_FILE ]] || die "GITHUB_APP_KEY_FILE is not readable"
    resolve DEMO_SLUG "The App's slug (its name in the URL github.com/apps/<slug>)"
    if ! demo_state_done "$DEMO_DIR" app || [[ $(cat "$app/app_id" 2>/dev/null) != "$GITHUB_APP_ID" ]]; then
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
repo_private() { # prints true/false/empty
  if [[ $HAVE_GH == 1 ]]; then gh api "repos/$DEMO_REPO" --jq .private 2>/dev/null || true
  else curl -fsS "https://api.github.com/repos/$DEMO_REPO" 2>/dev/null | sed -n 's/.*"private": *\(true\|false\).*/\1/p' | head -1 || true; fi
}

seed_github() {
  local n i title body
  local labels=(factory/ready class:docs-links documentation)
  local existing
  existing=$(gh label list -R "$DEMO_REPO" -L 200 --json name --jq '.[].name' 2>/dev/null || gh label list -R "$DEMO_REPO" -L 200 | cut -f1)
  for l in "${labels[@]}"; do
    grep -qxF "$l" <<<"$existing" || gh label create "$l" -R "$DEMO_REPO" >/dev/null 2>&1 || warn "could not create the label $l"
  done
  n=$(gh issue list -R "$DEMO_REPO" --state open --limit 1 | wc -l | tr -d ' ')
  if [[ $n == 0 ]]; then
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
  if [[ $HAVE_GH == 1 ]]; then seed_github
  else
    cat <<MSG
    By hand on https://github.com/$DEMO_REPO (no gh in use):
      - create the labels factory/ready, class:docs-links and documentation
      - open the issues of examples/demo-repo/ISSUES.md if the repository has none
      - add a ruleset on the default branch: require pull requests and the status check "ci"
        from GitHub Actions (never maku/merge-gate)
MSG
  fi
  if [[ $APP_FROM_FLOW == 1 ]]; then
    echo "    Install the App on $DEMO_REPO: $(cat "$DEMO_DIR/app/html_url")/installations/new"
    if interactive; then read -rp "    Press Enter once it is installed: " _; fi
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
    local ctx
    ctx=$(kubectl config current-context) || die "no current kubectl context"
    if ! demo_state_done "$DEMO_DIR" cluster; then
      echo "    CLUSTER=current: the demo will install into the context '$ctx'."
      if interactive; then
        read -rp "    Type the context name to confirm: " a
        [[ $a == "$ctx" ]] || die "not confirmed"
      else
        [[ ${DEMO_CONFIRM_CONTEXT:-} == "$ctx" ]] || die "set DEMO_CONFIRM_CONTEXT=$ctx to confirm the context without a terminal"
      fi
    fi
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
  kubectl wait --for=condition=Ready node --all --timeout=120s >/dev/null 2>&1
  demo_state_mark "$DEMO_DIR" cluster
  took
}

# ---------------------------------------------------------------------------------------------
install_chart() {
  step "install"
  local v=${MAKU_VERSION:-}
  if [[ -z $v ]]; then
    v=$(curl -fsS https://api.github.com/repos/Smana/maku/releases/latest | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1) || true
    [[ -n $v ]] || die "could not find the latest release: set MAKU_VERSION"
  fi
  echo "    maku $v"
  pin() {
    local d
    d=$(docker buildx imagetools inspect "ghcr.io/smana/$1:$v" --format '{{.Manifest.Digest}}') || die "ghcr.io/smana/$1:$v not found"
    printf '%s@%s' "$v" "$d"
  }
  DEMO_TAG_BROKER=$(pin maku-broker)
  DEMO_TAG_RUN_CONTROLLER=$(pin maku-run-controller)
  DEMO_TAG_HARNESS=$(pin maku-harness)
  DEMO_TAG_BRIDGE=$(pin maku-bridge)
  DEMO_TAG_FACTORY=$(pin maku-factory)
  export DEMO_TAG_BROKER DEMO_TAG_RUN_CONTROLLER DEMO_TAG_HARNESS DEMO_TAG_BRIDGE DEMO_TAG_FACTORY
  DEMO_SHADOW=true
  [[ ${DEMO_LIVE:-} != 1 ]] || DEMO_SHADOW=false
  export DEMO_SHADOW DEMO_REPO DEMO_LOGIN MODEL_BASE_URL MODEL_NAME MODEL_API_KEY
  demo_render_values "$DEMO_DIR/values.yaml"
  helm dependency build charts/agent-factory >/dev/null
  helm upgrade --install af charts/agent-factory -n "$NS" --create-namespace -f "$DEMO_DIR/values.yaml" --timeout 5m >/dev/null
  echo "    waiting for the workloads"
  kubectl -n "$NS" rollout status sts/rooms-db --timeout=240s
  kubectl -n "$NS" rollout status deploy/room-broker --timeout=240s
  kubectl -n "$NS" rollout status deploy/run-controller --timeout=240s
  FACTORY_OK=0
  local i
  for ((i = 0; i < 40; i++)); do
    if [[ $(kubectl -n "$NS" get deploy agent-factory -o jsonpath='{.status.readyReplicas}' 2>/dev/null) == 1 ]]; then FACTORY_OK=1; break; fi
    sleep 3
  done
  if [[ $FACTORY_OK == 1 ]]; then echo "    the factory is Ready"
  else
    warn "the factory is not Ready; its last log lines:"
    kubectl -n "$NS" logs deploy/agent-factory --tail=8 2>&1 | sed 's/^/      /' >&2 || true
    warn "if it says it cannot find the App's installation, install the App on $DEMO_REPO, then run task demo again"
  fi
  demo_state_mark "$DEMO_DIR" install
  took
}

# ---------------------------------------------------------------------------------------------
room_page() {
  step "room page"
  local pidf="$DEMO_DIR/port-forward.pid" pid=''
  [[ ! -s $pidf ]] || pid=$(<"$pidf")
  if demo_pid_is_forward "$pid" && curl -fs -o /dev/null --max-time 3 "http://127.0.0.1:$PORT/"; then
    echo "    the port-forward is running (pid $pid)"
  else
    demo_pid_is_forward "$pid" && kill "$pid" 2>/dev/null || true
    if (exec 3<>"/dev/tcp/127.0.0.1/$PORT") 2>/dev/null; then die "port $PORT is in use by something else: free it, the room page needs it"; fi
    local start=(kubectl -n "$NS" port-forward --address 127.0.0.1 deploy/room-broker "$PORT:8080")
    if command -v setsid >/dev/null; then setsid nohup "${start[@]}" </dev/null >"$DEMO_DIR/port-forward.log" 2>&1 &
    else nohup "${start[@]}" </dev/null >"$DEMO_DIR/port-forward.log" 2>&1 & fi
    echo $! >"$pidf"
    local i ok=0
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
  fi
  [[ -n $issue ]] || issue="https://github.com/$DEMO_REPO/issues"
  cat <<MSG

==> ready ($((SECONDS - T0))s in all)

  Room page:   http://localhost:$PORT
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

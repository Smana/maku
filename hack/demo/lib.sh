#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# The laptop demo's shared functions, sourced by up.sh, down.sh and lib_test.sh.
# Functions only: sourcing it runs nothing. Nothing here prints a secret.

# demo_state_done DIR STEP: has STEP finished (one line per finished step in DIR/state)?
demo_state_done() { [[ -f $1/state ]] && grep -qxF -- "$2" "$1/state"; }

# demo_state_mark DIR STEP: record STEP as finished (once).
demo_state_mark() {
  mkdir -p "$1"
  demo_state_done "$1" "$2" || printf '%s\n' "$2" >>"$1/state"
}

# demo_env_get KEY FILE: the value of KEY in a .env file (KEY=value, optional `export `, one pair of
# surrounding quotes dropped, # lines ignored). Prints nothing and succeeds when absent.
demo_env_get() {
  local key=$1 file=$2 line v last=''
  [[ -f $file ]] || return 0
  while IFS= read -r line || [[ -n $line ]]; do
    line=${line#"${line%%[![:space:]]*}"}
    [[ $line == '#'* ]] && continue
    line=${line#export }
    [[ $line == "$key="* ]] || continue
    v=${line#"$key="}
    v=${v%$'\r'}
    if [[ $v == \"*\" && ${#v} -ge 2 ]] || [[ $v == \'*\' && ${#v} -ge 2 ]]; then v=${v:1:${#v}-2}; fi
    last=$v
  done <"$file"
  printf '%s' "$last"
}

# demo_pick_cluster WANT HAVE_K3D HAVE_KIND: k3d, kind or current. WANT empty means k3d if installed
# (HAVE_K3D=1), else kind. A named tool that is missing is an error.
demo_pick_cluster() {
  local want=$1 k3d=$2 kind=$3
  case $want in
  "")
    if [[ $k3d == 1 ]]; then echo k3d; return 0; fi
    if [[ $kind == 1 ]]; then echo kind; return 0; fi
    echo "neither k3d nor kind is installed: install one, or set CLUSTER=current" >&2; return 1 ;;
  k3d) [[ $k3d == 1 ]] && { echo k3d; return 0; }; echo "CLUSTER=k3d but k3d is not installed" >&2; return 1 ;;
  kind) [[ $kind == 1 ]] && { echo kind; return 0; }; echo "CLUSTER=kind but kind is not installed" >&2; return 1 ;;
  current) echo current ;;
  *) echo "CLUSTER is k3d, kind or current, not '$want'" >&2; return 1 ;;
  esac
}

# demo_repo_valid REPO: owner/name.
demo_repo_valid() { [[ $1 =~ ^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9._-]+$ ]]; }

# demo_public_only PRIVATE: PRIVATE is the repository's "private" field. The demo takes public
# repositories only (Q15): anything but "false" refuses.
demo_public_only() {
  case $1 in
  false) return 0 ;;
  true) echo "the repository is private: the demo takes public repositories only" >&2; return 1 ;;
  *) echo "could not tell whether the repository is public ('$1'): the demo takes public repositories only" >&2; return 1 ;;
  esac
}

# demo_yq_str VALUE: a single-quoted YAML scalar.
demo_yq_str() { local v=${1//\'/\'\'}; printf "'%s'" "$v"; }

# demo_render_values OUT: the demo's values file, mode 0600. From the environment: DEMO_SLUG, DEMO_REPO,
# DEMO_LOGIN, MODEL_BASE_URL, MODEL_NAME, MODEL_API_KEY, GITHUB_APP_ID, GITHUB_APP_KEY_FILE, and
# optionally DEMO_SHADOW (true, the default, or false: live merging for docs-links), and the five image
# tags DEMO_TAG_BROKER, DEMO_TAG_RUN_CONTROLLER, DEMO_TAG_HARNESS, DEMO_TAG_BRIDGE, DEMO_TAG_FACTORY
# (`<version>@sha256:<digest>`). Helm replaces a list whole, so the whole repositories[0] entry is here.
demo_render_values() {
  local out=$1 q bot shadow=${DEMO_SHADOW:-true}
  [[ $shadow == true || $shadow == false ]] || { echo "DEMO_SHADOW is true or false" >&2; return 1; }
  [[ -r ${GITHUB_APP_KEY_FILE:-} ]] || { echo "the App key file is not readable" >&2; return 1; }
  q=demo_yq_str
  bot="${DEMO_SLUG}[bot]"
  (
    umask 077
    {
      echo "# Written by task demo; holds the model key and the App key: keep it out of Git."
      echo "profile: demo"
      echo "demo:"
      echo "  model:"
      echo "    baseURL: $($q "$MODEL_BASE_URL")"
      echo "    name: $($q "$MODEL_NAME")"
      echo "    apiKey: $($q "$MODEL_API_KEY")"
      echo "  githubApp:"
      echo "    id: $($q "$GITHUB_APP_ID")"
      echo "    privateKey: |"
      sed 's/^/      /' "$GITHUB_APP_KEY_FILE"
      echo
      if [[ -n ${DEMO_TAG_BROKER:-} ]]; then
        echo "maku:"
        echo "  broker: {image: {tag: $($q "$DEMO_TAG_BROKER")}}"
        echo "  runController:"
        echo "    image: {tag: $($q "$DEMO_TAG_RUN_CONTROLLER")}"
        echo "    images:"
        echo "      harness: {tag: $($q "$DEMO_TAG_HARNESS")}"
        echo "      bridge: {tag: $($q "$DEMO_TAG_BRIDGE")}"
      fi
      echo "maku-factory:"
      [[ -z ${DEMO_TAG_FACTORY:-} ]] || echo "  image: {tag: $($q "$DEMO_TAG_FACTORY")}"
      echo "  config:"
      echo "    factoryLogin: $($q "$bot")"
      echo "    agentsLogin: $($q "$bot")"
      echo "    repositories:"
      echo "      - name: $($q "$DEMO_REPO")"
      echo "        maintainers: [$($q "$DEMO_LOGIN")]"
      echo "        triggerLabel: factory/ready"
      echo "        classes:"
      echo "          docs-links: {shadow: $shadow, allowedFiles: ['^.+\\.(md|mdx|adoc|rst)\$']}"
      echo "          revert: {shadow: true, allowedFiles: ['^.+\$']}"
      echo "        merge:"
      echo "          requiredChecks: [ci]"
      echo "          verifyChecks: [ci]"
      echo "          leakScanCheck: ci"
      echo "          gateStatus: true"
      echo "          policyBot: false"
      echo "          mergerLogin: $($q "$bot")"
      echo "          autoMergesPerDay: 5"
      echo "          fixRuns: 2"
      echo "          checkReruns: 1"
      echo "          verifyFor: 30m"
      echo "          revertWindow: 168h"
      echo "          breaker: {window: 10, maxReverts: 1}"
    } >"$out"
  )
  chmod 600 "$out"
}

# demo_issue_count FILE: how many seeded issues (## headings) ISSUES.md lists.
demo_issue_count() { grep -c '^## ' "$1" || true; }

# demo_issue_field FILE N title|labels|body: field of the Nth (1-based) seeded issue. Labels come out
# one per line. The body is everything after the Labels line and its blank line, up to the next ##.
demo_issue_field() {
  awk -v n="$2" -v want="$3" '
    /^## / { i++; if (i == n) { title = substr($0, 4); inbody = 0; seen = 0 } else if (i > n) exit; next }
    i != n { next }
    !seen && /^Labels:/ { l = substr($0, 8); gsub(/^ +| +$/, "", l); labels = l; seen = 1; skip = 1; next }
    seen { if (skip) { skip = 0; if ($0 == "") next } body = body $0 "\n" }
    END {
      if (want == "title") print title
      else if (want == "labels") { m = split(labels, a, /, */); for (k = 1; k <= m; k++) if (a[k] != "") print a[k] }
      else { sub(/\n+$/, "", body); print body }
    }' "$1"
}

# demo_ruleset_json: the ruleset the demo adds (owner decision D2): pull requests, and the template's
# GitHub Actions check `ci` (integration 15368) on the default branch. Never maku/merge-gate.
demo_ruleset_json() {
  cat <<'JSON'
{"name":"maku-demo","target":"branch","enforcement":"active",
 "conditions":{"ref_name":{"include":["~DEFAULT_BRANCH"],"exclude":[]}},
 "rules":[
  {"type":"pull_request","parameters":{"required_approving_review_count":0,"dismiss_stale_reviews_on_push":false,"require_code_owner_review":false,"require_last_push_approval":false,"required_review_thread_resolution":false}},
  {"type":"required_status_checks","parameters":{"strict_required_status_checks_policy":false,"required_status_checks":[{"context":"ci","integration_id":15368}]}}
 ]}
JSON
}

# demo_pid_is_forward PID: is PID alive and a kubectl port-forward (guards against pid reuse)?
demo_pid_is_forward() {
  [[ $1 =~ ^[0-9]+$ ]] || return 1
  local cmd
  cmd=$(ps -p "$1" -o args= 2>/dev/null) || return 1
  [[ $cmd == *kubectl*port-forward*deploy/room-broker*8080:8080* ]]
}

# demo_sha256_check FILE: `sha256sum -c` on Linux, `shasum -a 256 -c` on macOS (reads the sums on stdin).
demo_sha256_check() {
  if command -v sha256sum >/dev/null; then sha256sum -c - >/dev/null; else shasum -a 256 -c - >/dev/null; fi
}

# demo_fetch_maku VERSION DEST: the release's maku for this OS and CPU, checked against the release's
# maku.sha256, installed 0755 at DEST. Linux and macOS, amd64 and arm64.
demo_fetch_maku() {
  local os arch tmp base rc=0
  case $(uname -s) in Linux) os=linux ;; Darwin) os=darwin ;; *) echo "no maku release for $(uname -s)" >&2; return 1 ;; esac
  case $(uname -m) in
  x86_64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) echo "no maku release for $(uname -m)" >&2; return 1 ;;
  esac
  base="https://github.com/Smana/maku/releases/download/$1"
  tmp=$(mktemp -d)
  if ! {
    curl -fsSL -o "$tmp/maku-$os-$arch" "$base/maku-$os-$arch" &&
      curl -fsSL -o "$tmp/maku.sha256" "$base/maku.sha256" &&
      (cd "$tmp" && grep " maku-$os-$arch\$" maku.sha256 | demo_sha256_check) &&
      mkdir -p "$(dirname "$2")" && install -m 0755 "$tmp/maku-$os-$arch" "$2"
  }; then
    rc=1
  fi
  rm -rf "$tmp" # this function's own temp dir
  ((rc == 0)) || echo "could not fetch maku $1 for $os-$arch (or its checksum did not match)" >&2
  return "$rc"
}

# demo_maku_choice BIN_OK GO_OK: how to run `maku demo app`. BIN_OK=1 when the fetched maku has it,
# GO_OK=1 when go is on PATH. Prints bin, go, or fails naming the two ways out.
demo_maku_choice() {
  if [[ $1 == 1 ]]; then echo bin; return 0; fi
  if [[ $2 == 1 ]]; then echo go; return 0; fi
  echo "this MAKU_VERSION's maku has no 'demo app' and go is not installed: use a MAKU_VERSION whose maku has 'demo app', or set GITHUB_APP_ID and GITHUB_APP_KEY_FILE" >&2
  return 1
}

# demo_context_ok STORED CURRENT: a CLUSTER=current run may go on only against the context the reader
# confirmed (STORED empty means none confirmed yet: ask).
demo_context_ok() { [[ -n $1 && $1 == "$2" ]]; }

# demo_digest_ok VALUE: a manifest digest.
demo_digest_ok() { [[ $1 =~ ^sha256:[0-9a-f]{64}$ ]]; }

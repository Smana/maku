#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# lib.sh's own test, with no cluster: kubectl, helm and maku are recorders, so the test reads the
# wizard's call and the order of the installs (README.md's: the CRD chart, agentgateway's CRDs, the
# Secrets, the umbrella, factory/), and that every DSN Secret is filled.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../.."
t="$(mktemp -d "${TMPDIR:-/tmp}/install-e2e-test.XXXXXX")"
trap 'rm -rf "$t"' EXIT
log="$t/calls.log"
mkdir -p "$t/bin"
# kubectl: records its argv and its stdin's object names; a Secret's uri reads back non-empty.
cat >"$t/bin/kubectl" <<'SH'
#!/usr/bin/env bash
printf 'kubectl %s\n' "$*" >>"$CALLS"
if [[ " $* " == *" -f - "* ]]; then
  in="$(cat)"
  grep -E '^\s+uri: ' <<<"$in" | grep -q '""' && echo "kubectl: an empty uri was applied" >>"$CALLS.err"
fi
[[ " $* " != *"jsonpath={.data.uri}"* ]] || printf 'dXJp'
exit 0
SH
cat >"$t/bin/helm" <<'SH'
#!/usr/bin/env bash
printf 'helm %s\n' "$*" >>"$CALLS"
SH
# maku: records its argv; install --print writes the wizard's tree from the golden generic output.
cat >"$t/bin/maku" <<SH
#!/usr/bin/env bash
printf 'maku %s\n' "\$*" >>"\$CALLS"
out=""; prev=""
for a in "\$@"; do [[ \$prev == --out ]] && out=\$a; prev=\$a; done
mkdir -p "\$out/secrets/templates" "\$out/crds" "\$out/factory"
for f in values.yaml crds/values.yaml factory/factory.yaml secrets/client-ids.yaml secrets/web-proxy.yaml \
  secrets/templates/database.yaml secrets/templates/github-link.yaml secrets/templates/model-provider.yaml; do
  cp "$PWD/internal/install/testdata/golden/generic/\$f" "\$out/\$f" 2>/dev/null || echo "kind: X" >"\$out/\$f"
done
SH
chmod +x "$t/bin/"*
fail=0
(
  export CALLS="$log" KUBECTL="$t/bin/kubectl" HELM="$t/bin/helm" MAKU="$t/bin/maku" PREBUILT="" TMPDIR="$t"
  LEG=kind-calico
  # shellcheck source=hack/install-e2e/lib.sh
  . hack/install-e2e/lib.sh
  leg_before_umbrella() { echo "leg_before_umbrella" >>"$CALLS"; }
  wizard
  postgres
  install
) >/dev/null 2>"$t/stderr" || { echo "FAIL: the harness failed: $(cat "$t/stderr")" >&2; exit 1; }

grep -q -- "^maku install --print --out .* --answers .*/answers.yaml$" "$log" || { echo "FAIL: the wizard was not called with --print --out --answers" >&2; fail=1; }
order() { grep -n -m1 -E -- "$1" "$log" | cut -d: -f1; }
prev=0
for want in '^helm install agent-factory-crds charts/agent-factory-crds -n maku -f .*/crds/values.yaml' \
  '^kubectl apply --server-side -f config/agentgateway' \
  '^kubectl apply -n maku -f .*/secrets/client-ids.yaml' \
  '^leg_before_umbrella' \
  '^helm install agent-factory charts/agent-factory -n maku -f .*/values.yaml -f hack/install-e2e/overlay.yaml' \
  '^kubectl apply -n maku -f .*/factory/'; do
  n="$(order "$want")"
  [[ -n $n ]] || { echo "FAIL: no call matches $want" >&2; fail=1; continue; }
  ((n > prev)) || { echo "FAIL: $want ran before the step that must precede it" >&2; fail=1; }
  prev=$n
done
[[ ! -s $log.err ]] || { echo "FAIL: $(cat "$log.err")" >&2; fail=1; }
[[ $fail == 0 ]] || { cat "$log" >&2; exit 1; }
echo "install-e2e lib checks passed"

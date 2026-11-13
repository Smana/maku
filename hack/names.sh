#!/usr/bin/env bash
# The maku rebrand's old-name gate (phase 1): an artifact name from before v0.30.0 (the repository,
# an image, a chart, a cmd/ or images/ path, the roomctl CLI) outside the historical docs fails the
# check. Identities keep their names in phase 1 (ServiceAccounts, audiences, the room-broker
# Service and lease, rooms_* roles and metrics, routes and config keys), so the patterns name
# artifacts only. Phase 2 (v0.31.0) adds the CRD group as a group (never a label or annotation key)
# and the old metric names outside Go.
set -euo pipefail
cd "${1:-$(dirname "$0")/..}"

# History and recorded data keep the old names.
allow=(':!docs/superpowers' ':!web/test/fixtures' ':!docs/naming.md' ':!hack/names.sh' ':!hack/names_test.sh'
  ':!internal/maku/legacy.go' ':!internal/maku/legacy_test.go' ':!internal/maku/legacymcp_test.go'
  ':!internal/app/maku_import_test.go' ':!internal/humanapi/ui/dist'
  # web/src/strip.ts:38's comment names roomctl's tokens(); the file is frozen by another branch,
  # which renames it. Drop this entry once that lands.
  ':!web/src/strip.ts')
# roomctl's identifiers that stay on the wire or in config (plan D1).
# The pre-rebrand OIDC client id and its audiences also keep roomctl (the k3d stub's signed token).
# The web front door's skip-auth route is a regex of the wire path: /\.well-known/roomctl (one or two
# backslashes, as a Helm string literal spells it).
legacy='/api/roomctl|/\.well-known/roomctl|/\\{1,2}\.well-known/roomctl|mcp_needs_roomctl|ViaRoomctl|roomctlClientIDFile|RoomctlClient|roomctl-client-id|room-broker-roomctl|/etc/room-broker/roomctl|roomctl-version|machine-sent via roomctl|formerly `?roomctl|still runs? roomctl|roomctl.s config|isRoomctl|roomctl",|roomctl"[]})]|--client-id roomctl|ClientID != "roomctl"'
# The signer identity releases before v0.30.0 carry, which the cosign examples still accept.
history='carry the `Smana/agent-platform` identity'
old='room-broker|room-bridge|agent-factory|agent-harness|run-controller|factory-slack-app'
# charts/agent-factory is the umbrella chart again (maku + maku-factory + a demo database), not the pre-v0.30.0 chart. From v4 rollout step 1 it is published again at ghcr.io/smana/charts/agent-factory (the pre-v0.30.0 package is gone from the registry), so only charts/agent-platform stays guarded.
artifacts="Smana/agent-platform|ghcr\\.io/smana/($old)([^a-z-]|\$)|charts/agent-platform([^a-z-]|\$)|ghcr\\.io/smana/charts/agent-platform|(cmd|images)/(roomctl|$old)([^a-z-]|\$)"
# Phase 2: agents.ogenki.io as an API group, never as a key prefix (agents.ogenki.io/<key> stays, plan D1).
group='(^|[^.a-z-])agents\.ogenki\.io(/v1alpha1|_[a-z]+\.yaml|`)|(rooms|tasks|agentruns)\.agents\.ogenki\.io|groupName=agents\.ogenki\.io|apiGroups:.*agents\.ogenki\.io|group: *"?agents\.ogenki\.io|Group: "agents\.ogenki\.io"'
# The migration must name the group it moves from.
group_allow=(':!docs/maku-group-migration.md' ':!internal/migrategroup' ':!internal/app/migrategroup_test.go'
  ':!hack/quickstart/migrate-e2e.sh')
# The instruments keep their old strings in Go; the exporter serves maku_ names only (v0.31.0). The
# list is read from the source, so a new instrument joins the gate; a histogram's series are refused too.
metric_args=()
while IFS= read -r n; do
  for s in '' _bucket _sum _count; do metric_args+=(-e "$n$s"); done
done < <(git grep --untracked -hoE '"(rooms|agent_factory)_[a-z_]+"' -- \
  internal/metrics internal/factory/fmetrics internal/bridge ':!*_test.go' 2>/dev/null | tr -d '"' | sort -u)
if [ ${#metric_args[@]} -eq 0 ]; then
  echo "FAIL: no instrument name found in internal/metrics, internal/factory/fmetrics or internal/bridge: if they moved, point hack/names.sh at them" >&2
  exit 1
fi

hits="$( { git grep --untracked -nIE "$artifacts" -- . "${allow[@]}" | grep -vF "$history" || true
          git grep --untracked -nIw roomctl -- . "${allow[@]}" | grep -vE "$legacy" || true
          git grep --untracked -nIE "$group" -- . "${allow[@]}" "${group_allow[@]}" || true
          git grep --untracked -nIwF "${metric_args[@]}" -- . ':!*.go' "${allow[@]}" || true; } )"
if [ -n "$hits" ]; then
  printf '%s\n' "$hits" >&2
  echo "FAIL: an old name (above): rename it, or, for an identity that keeps its name, add it to hack/names.sh's legacy set with its reason" >&2
  exit 1
fi
echo "==> names: no old artifact name outside the historical docs"

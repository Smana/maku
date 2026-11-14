#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# hack/values-contract.sh on fixture charts: each change is classified as CL7 says, and the report never
# fails unless --strict.
# shellcheck disable=SC2016 # the backquotes are markdown, not expansions
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/old" "$tmp/new" "$tmp/same"

cat >"$tmp/old/values.schema.json" <<'EOF'
{
  "type": "object",
  "properties": {
    "gone": {"type": "string"},
    "marked": {"type": "object", "properties": {"inner": {"type": "string"}}},
    "mode": {"type": "string", "enum": ["a", "b"]},
    "level": {"type": "string", "enum": ["low", "high"]},
    "count": {"type": ["integer", "string"]},
    "name": {"type": "string"},
    "block": {"type": "object", "properties": {"x": {"type": "string"}}},
    "route": {"$ref": "#/definitions/route"},
    "kept": {"type": "string"},
    "tag": {"type": "string", "pattern": "^v"}
  },
  "definitions": {"route": {"type": "object", "properties": {"port": {"type": ["integer", "string"]}}}}
}
EOF
cat >"$tmp/old/values.yaml" <<'EOF'
gone: x
marked:
  inner: y
mode: a
level: low
count: 1
name: n
kept: k
free: f
untyped: u
EOF
cat >"$tmp/new/values.schema.json" <<'EOF'
{
  "type": "object",
  "required": ["needed"],
  "properties": {
    "marked": {"description": "Removed.", "enum": ["removed in v4 step 8: set mode (docs/upgrading.md#x)"]},
    "mode": {"type": "string", "enum": ["a", "b", "c"]},
    "level": {"type": "string", "enum": ["high"]},
    "count": {"type": "integer"},
    "name": {"type": "string", "pattern": "^[a-z]+$"},
    "block": {"type": "object", "additionalProperties": false, "properties": {"x": {"type": "string"}}},
    "route": {"$ref": "#/definitions/route"},
    "kept": {"type": "string"},
    "tag": {"type": "string"},
    "needed": {"type": "string"},
    "extra": {"type": "string"}
  },
  "definitions": {"route": {"type": "object", "properties": {"port": {"type": "integer"}}}}
}
EOF
cat >"$tmp/new/values.yaml" <<'EOF'
mode: b
level: high
count: 1
name: n
untyped: u
needed: z
EOF
cp "$tmp/new/values.schema.json" "$tmp/new/values.yaml" "$tmp/same/"

run() { GITHUB_STEP_SUMMARY='' bash "$here/values-contract.sh" "$@"; }
out=$(run diff fixture "$tmp/old" "$tmp/new")

# want CLASS KEY CHANGE: the table has that row (the change as a prefix).
want() {
  grep -qF "| $1 | fixture | \`$2\` | $3" <<<"$out" || { echo "values-contract: want $1 $2 \"$3\", got:"; echo "$out"; exit 1; }
}
# none KEY: no row names KEY.
none() {
  ! grep -qF "\`$1\`" <<<"$out" || { echo "values-contract: want no row for $1, got:"; echo "$out"; exit 1; }
}

want breaking gone "removed from the schema"
want breaking marked "removed: v4 step 8: set mode"
none marked.inner # a removed key is one row
want breaking level 'enum narrowed, drops ["low"]'
want breaking count "type narrowed: integer\\|string to integer"
want breaking route.port "type narrowed: integer\\|string to integer" # through the $ref
want breaking name 'pattern added: "^[a-z]+$"'
want breaking block "unknown keys now refused"
want breaking needed "new required key"
want breaking free "removed from values.yaml"
want behaviour mode 'default "a" to "b"'
want behaviour level 'default "low" to "high"'
want behaviour kept 'default "k" to none'
want compatible mode 'enum widened: ["c"]'
want compatible tag "pattern dropped"
want compatible extra "added"
none untyped # unchanged
grep -q '^9 breaking, 3 behaviour, 3 compatible\.' <<<"$out" || { echo "values-contract: wrong count:"; echo "$out"; exit 1; }
# A widened enum is neither breaking nor behaviour.
! grep -qE '^\| (breaking|behaviour) \| fixture \| `mode` \| enum' <<<"$out" || { echo "values-contract: a widened enum is breaking"; exit 1; }

# Report-only: breaking rows exit 0; --strict exits 1 on them and 0 without them.
run diff fixture "$tmp/old" "$tmp/new" >/dev/null || { echo "values-contract: report-only failed"; exit 1; }
if run --strict diff fixture "$tmp/old" "$tmp/new" >/dev/null 2>&1; then echo "values-contract: --strict passed a breaking diff"; exit 1; fi
same=$(run --strict diff fixture "$tmp/new" "$tmp/same") || { echo "values-contract: --strict failed an identical chart"; exit 1; }
grep -q '^0 breaking, 0 behaviour, 0 compatible\.$' <<<"$same" || { echo "values-contract: identical charts: $same"; exit 1; }

# The job summary gets the same table.
GITHUB_STEP_SUMMARY="$tmp/summary" bash "$here/values-contract.sh" diff fixture "$tmp/old" "$tmp/new" >/dev/null
grep -qF '| breaking | fixture | `gone` |' "$tmp/summary" || { echo "values-contract: no job summary"; exit 1; }
echo "==> values-contract_test: ok"

#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# The chart values contract (v4 step 8, CL7; spec V17, D13): each chart's values.schema.json and
# values.yaml, compared with the same chart in a release, as a table of changes:
#   breaking    a removed or renamed key (gone from the schema, refused by a "removed in" marker, or
#               dropped from values.yaml with no schema entry left); a narrowed type, enum or pattern;
#               a key that becomes required; an object that stops accepting unknown keys
#   behaviour   a changed default (values.yaml)
#   compatible  an added key, a widened type or enum, a dropped pattern
#
#   values-contract.sh [--strict] [TAG]       every chart against TAG (default: the latest release),
#                                             pulled from oci://ghcr.io/smana/charts and cosign-verified
#                                             as the release workflow's
#   values-contract.sh [--strict] diff CHART OLD_DIR NEW_DIR
#                                             two directories, each with values.yaml and (optionally)
#                                             values.schema.json
#
# Report-only (step 8 R8): it exits 0 whatever it finds. --strict exits 1 on a breaking row; arming it in
# CI is part of cutting v4.0.0, the owner's go. The table goes to stdout, and to $GITHUB_STEP_SUMMARY
# when set.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

die() { echo "values-contract: $*" >&2; exit 2; }

strict=false
if [[ ${1:-} == --strict ]]; then strict=true; shift; fi

# flat_schema FILE: {path: {types, enum, pattern, required, addl, removed}} for every node of the schema,
# local $refs resolved. Paths are dotted; an array's items are `key[]`, a map's values `key.*`.
flat_schema() {
  [[ -f $1 ]] || { echo '{}'; return; }
  jq -c '
    def resolve($root):
      if type == "object" and has("$ref") and (.["$ref"] | startswith("#/"))
      then (.["$ref"] | ltrimstr("#/") | split("/")) as $at | ($root | getpath($at)) + del(.["$ref"])
      else . end;
    def join($p; $k): if $p == "" then $k else $p + "." + $k end;
    def nodes($root; $p; $req; $d):
      resolve($root) as $n
      | if $d > 40 or ($n | type) != "object" then empty else
          {key: (if $p == "" then "." else $p end), value: {
            types: ($n.type // null | if . == null then null elif type == "array" then sort else [.] end),
            enum: ($n.enum // null),
            pattern: ($n.pattern // null),
            required: $req,
            addl: (if $n | has("additionalProperties") then ($n.additionalProperties != false) else null end),
            removed: (($n.enum // []) as $e | ($e | length) == 1 and ($e[0] | type) == "string" and ($e[0] | startswith("removed in")))
          }},
          (($n.required // []) as $r | ($n.properties // {}) | to_entries[]
            | .key as $k | .value | nodes($root; join($p; $k); ($r | index($k)) != null; $d + 1)),
          (if ($n.items | type) == "object" then $n.items | nodes($root; $p + "[]"; false; $d + 1) else empty end),
          (if ($n.additionalProperties | type) == "object" then $n.additionalProperties | nodes($root; join($p; "*"); false; $d + 1) else empty end)
        end;
    . as $root | [nodes($root; ""; false; 0)] | from_entries' "$1"
}

# flat_values FILE: {path: default} for every leaf of values.yaml; an array or an empty map is a leaf.
flat_values() {
  [[ -f $1 ]] || { echo '{}'; return; }
  yq -o=json '.' "$1" | jq -c '
    def leaves($p):
      if type == "object" and length > 0
      then to_entries[] | .key as $k | .value | leaves(if $p == "" then $k else $p + "." + $k end)
      else {key: $p, value: .} end;
    if . == null then {} else [leaves("")] | from_entries end'
}

# compare CHART OLD_DIR NEW_DIR: one TSV row per change: class, chart, key, change.
compare() {
  jq -nr --arg chart "$1" \
    --argjson os "$(flat_schema "$2/values.schema.json")" --argjson ns "$(flat_schema "$3/values.schema.json")" \
    --argjson ov "$(flat_values "$2/values.yaml")" --argjson nv "$(flat_values "$3/values.yaml")" '
    def show: tojson | if length > 60 then .[:57] + "..." else . end;
    def parent: if test("\\.") then sub("\\.[^.]*$"; "") elif endswith("[]") then .[:-2] else "." end;
    def row($c; $k; $what): [$c, $chart, $k, $what];
    # The schema: every key the release declared, against this checkout.
    ([ $os | to_entries[] | select(.value.removed | not) | .key as $k | .value as $o | $ns[$k] as $n
       | if $n == null then row("breaking"; $k; "removed from the schema")
         elif $n.removed then row("breaking"; $k; "removed: " + ($n.enum[0] | ltrimstr("removed in ")))
         else
           (if $o.types != null and $n.types != null then
              ($o.types - $n.types) as $lost | ($n.types - $o.types) as $won
              | (if ($lost | length) > 0 then row("breaking"; $k; "type narrowed: " + ($o.types | join("|")) + " to " + ($n.types | join("|"))) else empty end),
                (if ($won | length) > 0 and ($lost | length) == 0 then row("compatible"; $k; "type widened: " + ($n.types | join("|"))) else empty end)
            elif $o.types == null and $n.types != null and $k != "." then row("breaking"; $k; "type narrowed: any to " + ($n.types | join("|")))
            else empty end),
           (if $o.enum == null and $n.enum != null then row("breaking"; $k; "enum added: " + ($n.enum | show))
            elif $o.enum != null and $n.enum != null then
              ($o.enum - $n.enum) as $lost | ($n.enum - $o.enum) as $won
              | (if ($lost | length) > 0 then row("breaking"; $k; "enum narrowed, drops " + ($lost | show)) else empty end),
                (if ($won | length) > 0 then row("compatible"; $k; "enum widened: " + ($won | show)) else empty end)
            elif $o.enum != null and $n.enum == null then row("compatible"; $k; "enum dropped")
            else empty end),
           (if $o.pattern == $n.pattern then empty
            elif $n.pattern == null then row("compatible"; $k; "pattern dropped")
            elif $o.pattern == null then row("breaking"; $k; "pattern added: " + ($n.pattern | show))
            else row("breaking"; $k; "pattern changed: " + ($n.pattern | show)) end),
           (if $n.required and ($o.required | not) then row("breaking"; $k; "now required") else empty end),
           (if $o.addl != false and $n.addl == false then row("breaking"; $k; "unknown keys now refused") else empty end)
         end ]) as $schema_old
    # Keys this checkout adds: a removal marker is breaking, a new required key under a parent the
    # release had is breaking, any other new key is compatible (the topmost one only).
    | ([ $ns | to_entries[] | .key as $k | .value as $n | select($os[$k] == null)
       | if $n.removed then row("breaking"; $k; "removed: " + ($n.enum[0] | ltrimstr("removed in ")))
         elif ($k | parent) as $p | $os[$p] == null then empty
         elif $n.required then row("breaking"; $k; "new required key")
         elif $nv | has($k) then empty # the defaults row below names it, with its value
         else row("compatible"; $k; "added") end ]) as $schema_new
    | ([ $schema_old[], $schema_new[] | select(.[0] == "breaking" and (.[3] | startswith("removed"))) | .[2] ]) as $gone
    | def under: . as $k | any($gone[]; . as $g | ($k | startswith($g + ".")) or ($k | startswith($g + "[]")));
    def covered: . as $k | ($gone | index([$k]) != null) or under;
    # The defaults: values.yaml, leaf by leaf.
    ([ ($ov | keys[]), ($nv | keys[]) ] | unique | map(select(covered | not))) as $vkeys
    | ([ $vkeys[] | . as $k
       | if ($ov | has($k)) and ($nv | has($k)) then
           (if $ov[$k] != $nv[$k] then row("behaviour"; $k; "default " + ($ov[$k] | show) + " to " + ($nv[$k] | show)) else empty end)
         elif $ov | has($k) then
           (if $ns[$k] != null and ($ns[$k].removed | not) then row("behaviour"; $k; "default " + ($ov[$k] | show) + " to none")
            else row("breaking"; $k; "removed from values.yaml") end)
         elif (($k | parent) as $p | $p == "." or any($ov | keys[]; startswith($p + "."))) and $os[$k] == null and ($ns[$k].required | not) then
           row("compatible"; $k; "added, default " + ($nv[$k] | show))
         else empty end ]) as $values
    # A removed key is one row: its descendants are not listed again.
    | ($schema_old + $schema_new | map(select(.[2] | under | not))) + $values | unique | .[] | @tsv'
}

# render ROWS_FILE TITLE: the markdown table, breaking rows first, and a one-line count.
render() {
  local b h c
  b=$(awk -F'\t' '$1=="breaking"' "$1" | wc -l)
  h=$(awk -F'\t' '$1=="behaviour"' "$1" | wc -l)
  c=$(awk -F'\t' '$1=="compatible"' "$1" | wc -l)
  printf '### The values contract: %s\n\n' "$2"
  printf '%s breaking, %s behaviour, %s compatible.' "$b" "$h" "$c"
  if [[ $b -gt 0 ]]; then
    printf ' Report-only (step 8 R8): from v4.0.0 a breaking row needs a major and a migration note (docs/upgrading.md).'
  fi
  printf '\n\n'
  [[ -s $1 ]] || return 0
  printf '| Class | Chart | Key | Change |\n|---|---|---|---|\n'
  { awk -F'\t' '$1=="breaking"' "$1"; awk -F'\t' '$1=="behaviour"' "$1"; awk -F'\t' '$1=="compatible"' "$1"; } |
    awk -F'\t' '{ gsub(/\|/, "\\|"); printf "| %s | %s | `%s` | %s |\n", $1, $2, $3, $4 }'
}

# report ROWS_FILE TITLE: print, copy to the job summary, exit 1 only under --strict with a breaking row.
report() {
  local out
  out=$(render "$1" "$2")
  printf '%s\n' "$out"
  [[ -z ${GITHUB_STEP_SUMMARY:-} ]] || printf '%s\n' "$out" >>"$GITHUB_STEP_SUMMARY"
  if $strict && grep -q '^breaking' "$1"; then
    echo "values-contract: breaking changes against $2 (--strict)" >&2
    exit 1
  fi
}

work=$(mktemp -d); trap 'rm -rf "$work"' EXIT

if [[ ${1:-} == diff ]]; then
  [[ $# -eq 4 ]] || die "usage: values-contract.sh [--strict] diff CHART OLD_DIR NEW_DIR"
  compare "$2" "$3" "$4" >"$work/rows"
  report "$work/rows" "$2, $3 to $4"
  exit 0
fi

[[ $# -le 1 ]] || die "usage: values-contract.sh [--strict] [TAG]"
tag=${1:-latest}
if [[ $tag == latest ]]; then
  tag=$(curl -fsSL ${GH_TOKEN:+-H "Authorization: Bearer $GH_TOKEN"} https://api.github.com/repos/Smana/maku/releases/latest | jq -er .tag_name) ||
    die "cannot read the latest release"
fi
[[ $tag =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "$tag is not a release tag"

# pull CHART: the release's chart, its digest cosign-verified as the release workflow's (docs/security.md's
# identity), its values.yaml and values.schema.json unpacked to $work/old/CHART. A chart the release did
# not publish leaves an empty directory: every key is then "added".
pull() {
  local out d
  mkdir -p "$work/old/$1" "$work/tgz"
  if ! out=$(helm pull "oci://ghcr.io/smana/charts/$1" --version "${tag#v}" -d "$work/tgz" 2>&1); then
    grep -qiE 'not found|manifest unknown' <<<"$out" || die "helm pull $1 ${tag#v}: $out"
    echo "values-contract: $tag published no $1 chart" >&2
    return
  fi
  d=$(sed -nE 's/^Digest: (sha256:[0-9a-f]{64})$/\1/p' <<<"$out")
  [[ -n $d ]] || die "helm pull $1 printed no digest: $out"
  cosign verify "ghcr.io/smana/charts/$1@$d" --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    --certificate-identity-regexp '^https://github\.com/Smana/maku/\.github/workflows/release\.yaml@refs/tags/v' >/dev/null 2>&1 ||
    die "ghcr.io/smana/charts/$1@$d is not signed by maku's release workflow"
  tar -xzf "$work/tgz/$1-${tag#v}.tgz" -C "$work/old/$1" --strip-components=1 --wildcards \
    "$1/values.yaml" "$1/values.schema.json" 2>/dev/null ||
    tar -xzf "$work/tgz/$1-${tag#v}.tgz" -C "$work/old/$1" --strip-components=1 "$1/values.yaml"
}

: >"$work/rows"
for dir in charts/*/; do
  c=$(basename "$dir")
  [[ -f $dir/values.yaml ]] || continue
  pull "$c"
  compare "$c" "$work/old/$c" "$dir" >>"$work/rows"
done
report "$work/rows" "this checkout against $tag"

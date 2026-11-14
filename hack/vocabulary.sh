#!/usr/bin/env bash
# The reader pages' vocabulary (workflows WF1): one noun, workflow, for what a harness profile, a
# template or a class run is to a person, and the other meanings of "profile" renamed: role
# permissions, approval mode, egress allow-list. A reader page (docs/*.md) that says "harness
# profile", "capability profile", "egress profile" or "trio" in prose fails. Text in backticks is
# a config key or value and passes; the reference and history pages keep the code's names.
set -euo pipefail
cd "${1:-$(dirname "$0")/..}"

skip='^docs/(api|upgrading|event-envelope|room-log)\.md$'
hits=""
out=""
for f in docs/*.md; do
  [[ "$f" =~ $skip ]] && continue
  out=$(awk -v f="$f" '{
    line = $0
    gsub(/`[^`]*`/, "", line)
    if (tolower(line) ~ /(harness|capability|egress) profile|(^|[^a-z])trio([^a-z]|$)/) print f ":" NR ": " $0
  }' "$f")
  [ -n "$out" ] && hits+="$out"$'\n'
done
if [ -n "$hits" ]; then
  printf '%s\n' "$hits" >&2
  echo "FAIL: an old noun on a reader page (above): workflow, role permissions, approval mode or egress allow-list (docs/concepts.md)" >&2
  exit 1
fi
echo "==> vocabulary: the reader pages say workflow"

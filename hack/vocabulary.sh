#!/usr/bin/env bash
# The reader pages' vocabulary (workflows WF1): one noun, workflow, for what a harness profile, a
# template or a class run is to a person, and the other meanings of "profile" renamed: role
# permissions, approval mode, egress allow-list. A reader page (docs/*.md) that says "harness
# profile", "capability profile", "egress profile", "trio" or the profile:<name> label in prose
# fails, and so does the removed key harnessProfiles anywhere (WF13). Other text in backticks is a
# config key or value and passes; the reference and history pages keep the code's names.
set -euo pipefail
cd "${1:-$(dirname "$0")/..}"

skip='^(docs/(api|upgrading|event-envelope|room-log)\.md|website/content/docs/(reference/|contributing/|decisions/|operate/upgrading\.md))'
hits=""
out=""
files=()
while IFS= read -r f; do files+=("$f"); done < <(find docs website/content/docs -maxdepth 3 -name '*.md' -not -path 'docs/superpowers/*' -not -path 'docs/assets/*' 2>/dev/null | sort)
for f in "${files[@]}"; do
  [[ "$f" =~ $skip ]] && continue
  out=$(awk -v f="$f" '{
    line = $0
    gsub(/`[^`]*`/, "", line)
    if (tolower(line) ~ /(harness|capability|egress) profile|(^|[^a-z])trio([^a-z]|$)|(^|[^a-z`])profile:</ || $0 ~ /harnessProfiles/) print f ":" NR ": " $0
  }' "$f")
  [ -n "$out" ] && hits+="$out"$'\n'
done
if [ -n "$hits" ]; then
  printf '%s\n' "$hits" >&2
  echo "FAIL: an old noun on a reader page (above): workflow (workflows, workflow:<name>), role permissions, approval mode or egress allow-list (website/content/docs/concepts/glossary.md)" >&2
  exit 1
fi
echo "==> vocabulary: the reader pages say workflow"

# The merge-policy pages (correctness signals, decision 3): a maintainer's merge of a shadow decision
# is "agreed", a monitor window that ended with nothing is "quiet". "clean decisions" and "healthy"
# in prose fail; the wire values (`clean`, `healthy`, the Healthy condition) stay in code spans.
hits=""
for f in docs/merge-policies.md docs/factory.md docs/reference/web-ui-settings.md \
         website/content/docs/use/merge-policies.md website/content/docs/concepts/factory.md website/content/docs/reference/web-ui-settings.md; do
  [ -f "$f" ] || continue
  out=$(awk -v f="$f" '
    /^ ? ? ?(```|~~~)/ { fence = !fence; next }
    fence { next }
    {
      line = $0
      gsub(/`[^`]*`/, "", line)
      l = tolower(line)
      if (l ~ /(^|[^a-z])clean (decision|merge|shadow merge)s?([^a-z]|$)/ || l ~ /(^|[^a-z])healthy([^a-z]|$)/) print f ":" NR ": " $0
    }' "$f")
  [ -n "$out" ] && hits+="$out"$'\n'
done
if [ -n "$hits" ]; then
  printf '%s\n' "$hits" >&2
  echo "FAIL: say \"agreed\" or \"quiet\" (docs/merge-policies.md#holds-agreed-signal-checked); wire values stay in backticks" >&2
  exit 1
fi
echo "==> vocabulary: the merge-policy pages say agreed and quiet"

#!/usr/bin/env bash
# hack/vocabulary.sh refuses an old noun on a reader page, names the line, and passes a page
# that names a config value in code or one of the pages the gate leaves out (workflows WF1).
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/docs" "$tmp/website/content/docs/decisions"
printf 'A **workflow** names the roles; `templates.trio` and `workflows` are keys; a `profile:<name>` label pins nothing.\n' > "$tmp/docs/ok.md"
printf 'harness profile and trio and egress profile and capability profile and harnessProfiles, all kept\n' > "$tmp/docs/api.md"
cp "$tmp/docs/api.md" "$tmp/docs/upgrading.md"; cp "$tmp/docs/api.md" "$tmp/docs/event-envelope.md"
cp "$tmp/docs/api.md" "$tmp/docs/room-log.md"
cp "$tmp/docs/api.md" "$tmp/website/content/docs/decisions/0041-history.md"
bash "$here/vocabulary.sh" "$tmp"
n=0
for phrase in 'harness profile' 'Capability profiles' 'egress profile' 'the trio template' '`harnessProfiles`' 'a profile:<name> label'; do
  n=$((n + 1))
  printf 'line one\nThis page says %s.\n' "$phrase" > "$tmp/docs/bad$n.md"
done
if out=$(bash "$here/vocabulary.sh" "$tmp" 2>&1); then echo "vocabulary.sh passed an old noun: $out"; exit 1; fi
for i in 1 2 3 4 5 6; do
  grep -q "^docs/bad$i.md:2:" <<<"$out" || { echo "vocabulary.sh missed docs/bad$i.md: $out"; exit 1; }
done
if grep -qE 'ok\.md|api\.md|upgrading\.md|event-envelope\.md|room-log\.md' <<<"$out"; then echo "unexpected match: $out"; exit 1; fi
# The merge-policy pages say agreed and quiet (correctness signals, decision 3): "clean decisions"
# and "healthy" in prose fail; the wire values in code pass.
tmp2=$(mktemp -d); trap 'rm -rf "$tmp" "$tmp2"' EXIT
mkdir -p "$tmp2/docs/reference"
printf 'Agreed decisions; the monitor reads `verdict=healthy` and `Healthy`; an Unhealthy GitHub connection.\n' > "$tmp2/docs/merge-policies.md"
printf '```\nclean decisions, healthy\n```\n' > "$tmp2/docs/factory.md"
bash "$here/vocabulary.sh" "$tmp2"
printf 'line one\nIt needs 20 clean decisions.\n' > "$tmp2/docs/merge-policies.md"
printf 'line one\nline two\nThe verdict is healthy.\n' > "$tmp2/docs/factory.md"
printf 'Five clean shadow merges.\n' > "$tmp2/docs/reference/web-ui-settings.md"
if out=$(bash "$here/vocabulary.sh" "$tmp2" 2>&1); then echo "vocabulary.sh passed clean or healthy: $out"; exit 1; fi
for want in docs/merge-policies.md:2: docs/factory.md:3: docs/reference/web-ui-settings.md:1:; do
  grep -q "^$want" <<<"$out" || { echo "vocabulary.sh missed $want: $out"; exit 1; }
done
echo "==> vocabulary_test: ok"

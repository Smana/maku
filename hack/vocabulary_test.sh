#!/usr/bin/env bash
# hack/vocabulary.sh refuses an old noun on a reader page, names the line, and passes a page
# that names a config value in code or one of the pages the gate leaves out (workflows WF1).
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/docs"
printf 'A **workflow** names the roles; `templates.trio` and `harnessProfiles` are keys.\n' > "$tmp/docs/ok.md"
printf 'harness profile and trio and egress profile and capability profile, all kept\n' > "$tmp/docs/api.md"
cp "$tmp/docs/api.md" "$tmp/docs/upgrading.md"; cp "$tmp/docs/api.md" "$tmp/docs/event-envelope.md"
cp "$tmp/docs/api.md" "$tmp/docs/room-log.md"
bash "$here/vocabulary.sh" "$tmp"
n=0
for phrase in 'harness profile' 'Capability profiles' 'egress profile' 'the trio template'; do
  n=$((n + 1))
  printf 'line one\nThis page says %s.\n' "$phrase" > "$tmp/docs/bad$n.md"
done
if out=$(bash "$here/vocabulary.sh" "$tmp" 2>&1); then echo "vocabulary.sh passed an old noun: $out"; exit 1; fi
for i in 1 2 3 4; do
  grep -q "^docs/bad$i.md:2:" <<<"$out" || { echo "vocabulary.sh missed docs/bad$i.md: $out"; exit 1; }
done
if grep -qE 'ok\.md|api\.md|upgrading\.md|event-envelope\.md|room-log\.md' <<<"$out"; then echo "unexpected match: $out"; exit 1; fi
echo "==> vocabulary_test: ok"

#!/usr/bin/env bash
# hack/docs.sh refuses the status vocabulary and dead relative links, and ignores docs/superpowers.
set -euo pipefail
cd "$(dirname "$0")/.."
tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
# Negative greps read the findings without the temporary directory, whose random name can hold any pattern (#637).
findings() { printf '%s\n' "${out//"$tmp/"/}"; }
mkdir -p "$tmp/docs/superpowers"
printf '# A\n\nSee [b](b.md#there) and [gone](nope.md). Status: AP-1. Planned, phase 2.\n' > "$tmp/docs/a.md"
printf '# B\n\n## There\n' > "$tmp/docs/b.md"
printf '# Spec\n\nPhase 1 is planned (AP-1).\n' > "$tmp/docs/superpowers/spec.md"
out=$(hack/docs.sh "$tmp/docs/a.md" "$tmp/docs/b.md" "$tmp/docs/superpowers/spec.md" 2>&1 || true)
grep -q 'a.md:3: status vocabulary: AP-1' <<<"$out"
grep -q 'a.md:3: status vocabulary: Planned' <<<"$out"
grep -q 'a.md:3: dead link: nope.md' <<<"$out"
if grep -q 'b.md' <<<"$(findings)"; then echo "unexpected match: 'b.md'"; exit 1; fi
if grep -q 'superpowers' <<<"$(findings)"; then echo "unexpected match: 'superpowers'"; exit 1; fi
printf '# C\n\nSee [b](b.md#nowhere). Since v0.7 the broker serves it.\n' > "$tmp/docs/c.md"
out=$(hack/docs.sh "$tmp/docs/c.md" "$tmp/docs/b.md" 2>&1 || true)
grep -q 'c.md:3: dead anchor: b.md#nowhere' <<<"$out"
if grep -q 'status vocabulary' <<<"$(findings)"; then echo "unexpected match: 'status vocabulary'"; exit 1; fi

# Anchors: backticks and punctuation dropped, duplicates numbered, same-file fragments.
printf '# T\n\n## The `Room` CRD: v1, (beta)!\n\n## Metrics\n\n## Metrics\n\n```\n## Fenced\n```\n' > "$tmp/docs/t.md"
printf '# D\n\n[a](t.md#the-room-crd-v1-beta) [b](t.md#metrics-1) [c](t.md#metrics) [d](#d)\n' > "$tmp/docs/d.md"
hack/docs.sh "$tmp/docs/d.md"
printf '# E\n\n[x](t.md#metrics-2) [y](t.md#fenced) [z](#nope) [W](t.md#The-Room-CRD-v1-beta)\n' > "$tmp/docs/e.md"
out=$(hack/docs.sh "$tmp/docs/e.md" 2>&1 || true)
grep -q 'e.md:3: dead anchor: t.md#metrics-2' <<<"$out"
grep -q 'e.md:3: dead anchor: t.md#fenced' <<<"$out"
grep -q 'e.md:3: dead anchor: #nope' <<<"$out"
if grep -q 'The-Room' <<<"$(findings)"; then echo "unexpected match: 'The-Room'"; exit 1; fi

# Ignored: fenced code, inline code, absolute URLs, mailto.
printf '# F\n\n`[x](gone.md)` [u](https://example.com/x.md) [m](mailto:a@b.c)\n\n```md\n[y](gone.md)\n```\n' > "$tmp/docs/f.md"
hack/docs.sh "$tmp/docs/f.md"

# Vocabulary is whole-word and code-free.
printf '# G\n\nUnplanned, in reviewer, not built-in, MAP-1, `in review`, x-AP-1.\n\n```\nPlanned AP-1\n```\n' > "$tmp/docs/g.md"
hack/docs.sh "$tmp/docs/g.md"
printf '# H\n\nAP-12 is (phase 3) and in review.\n' > "$tmp/docs/h.md"
out=$(hack/docs.sh "$tmp/docs/h.md" 2>&1 || true)
grep -q 'h.md:3: status vocabulary: AP-12' <<<"$out"
grep -q 'h.md:3: status vocabulary: (phase 3' <<<"$out"
grep -q 'h.md:3: status vocabulary: in review' <<<"$out"

# Design ids and cluster names: whole words, case-sensitive, outside code; docs/contributing skipped.
printf '# I\n\nThe gate (D11) posts a status. See AM6, HR13, MP15, OD-8 and rollout step 7.\nRuns on aws-0. Release N+1 refuses it. The B1 budget and a `D4` flag stay.\n\n```\nD12 on gcp-0\n```\n' > "$tmp/docs/i.md"
mkdir -p "$tmp/docs/contributing"; cp "$tmp/docs/i.md" "$tmp/docs/contributing/i.md"
out=$(hack/docs.sh "$tmp/docs/i.md" "$tmp/docs/contributing/i.md" 2>&1 || true)
for id in D11 AM6 HR13 MP15 OD-8 'rollout step 7'; do
  grep -q "^$tmp/docs/i.md:3: design id: $id\$" <<<"$out" || { echo "missing: design id $id"; exit 1; }
done
grep -q "^$tmp/docs/i.md:4: design id: Release N+1\$" <<<"$out" || { echo "missing: design id Release N+1"; exit 1; }
grep -q "^$tmp/docs/i.md:4: cluster name: aws-0\$" <<<"$out" || { echo "missing: cluster name aws-0"; exit 1; }
if grep -q 'B1\|D4\|D12\|gcp-0\|contributing' <<<"$(findings)"; then echo "unexpected match: $out"; exit 1; fi
printf '# J\n\nThen step 8 PR F, step 7 PR E1 and step 7 R9 shipped; Live proof 5 and release N, V12, C4, R10, FA3, SI2, GW1.\nNot ids: AM, D-day, MD5, D100, R2D2, aws-01, v4.\n' > "$tmp/docs/j.md"
out=$(hack/docs.sh "$tmp/docs/j.md" 2>&1 || true)
for id in 'step 8 PR F' 'step 7 PR E1' 'step 7 R9' 'Live proof 5' 'release N' V12 C4 R10 FA3 SI2 GW1; do
  grep -q "j.md:3: design id: $id\$" <<<"$out" || { echo "missing: design id $id"; exit 1; }
done
if grep -q 'j.md:4' <<<"$(findings)"; then echo "unexpected match: $out"; exit 1; fi

# Exit codes: 1 on a hit, 0 clean.
rc=0; hack/docs.sh "$tmp/docs/h.md" >/dev/null 2>&1 || rc=$?; [ "$rc" = 1 ]
rc=0; hack/docs.sh "$tmp/docs/g.md" >/dev/null 2>&1 || rc=$?; [ "$rc" = 0 ]

# Unicode anchors and Goldmark explicit heading/span ids.
printf '# U\n\n## 🚀 Démarrage rapide\n\n## Écrire\n\n## Ça va ?\n\n## Pick your path {#paths}\n\n<span id="review"></span>\n' > "$tmp/docs/u.md"
printf '# V\n\n[a](u.md#-démarrage-rapide) [b](u.md#écrire) [c](u.md#ça-va-) [d](u.md#paths) [e](u.md#review)\n' > "$tmp/docs/v.md"
hack/docs.sh "$tmp/docs/v.md"

# Historical ADRs under website/content/docs/decisions/ are exempt from status, design-id, and cluster-name scans.
mkdir -p "$tmp/website/content/docs/decisions" "$tmp/website/content/docs/concepts"
printf '# 0041\n\nPlanned on aws-0 (D11, R3).\n' > "$tmp/website/content/docs/decisions/0041-test.md"
printf '# Bad\n\nPlanned on aws-0 (D11, R3).\n' > "$tmp/website/content/docs/concepts/bad.md"
out=$(hack/docs.sh "$tmp/website/content/docs/decisions/0041-test.md" "$tmp/website/content/docs/concepts/bad.md" 2>&1 || true)
grep -q 'concepts/bad.md:3: status vocabulary: Planned' <<<"$out"
grep -q 'concepts/bad.md:3: design id: D11' <<<"$out"
grep -q 'concepts/bad.md:3: cluster name: aws-0' <<<"$out"
if grep -q 'decisions/0041-test.md' <<<"$(findings)"; then echo "unexpected match in decisions/: $out"; exit 1; fi

# Page-type ceilings: marker within first 5 lines; unknown type; moved stub <= 40 words; untyped ignored.
awk 'BEGIN{printf "<!-- page-type: how-to -->\n# H\n\n"; for(i=0;i<1600;i++) printf "word "; printf "\n"}' > "$tmp/docs/h.md"
awk 'BEGIN{printf "<!-- page-type: reference -->\n# R\n\n"; for(i=0;i<9000;i++) printf "word "; printf "\n"}' > "$tmp/docs/r.md"
printf '<!-- page-type: essay -->\n# X\n' > "$tmp/docs/x.md"
awk 'BEGIN{printf "<!-- page-type: moved -->\n# M\n\n"; for(i=0;i<40;i++) printf "word "; printf "\n"}' > "$tmp/docs/m-bad.md"
printf '<!-- page-type: moved -->\n# M\n\nMoved to [B](b.md).\n' > "$tmp/docs/m-ok.md"
out=$(hack/docs.sh "$tmp/docs/h.md" "$tmp/docs/r.md" "$tmp/docs/x.md" "$tmp/docs/m-bad.md" "$tmp/docs/m-ok.md" "$tmp/docs/b.md" 2>&1 || true)
grep -q 'h.md: over ceiling: 160[0-9] words, how-to allows 1500' <<<"$out"
grep -q 'x.md: unknown page-type: essay' <<<"$out"
grep -q 'm-bad.md: moved stub over 40 words' <<<"$out"
if grep -q 'r.md\|m-ok.md\|b.md' <<<"$(findings)"; then echo "unexpected match: $out"; exit 1; fi
# YAML front-matter diataxis ceiling works the same way, excluding front matter from word count.
awk 'BEGIN{printf "---\ntitle: \"Y\"\ndiataxis: how-to\n---\n\n"; for(i=0;i<1600;i++) printf "word "; printf "\n"}' > "$tmp/docs/y.md"
out=$(hack/docs.sh "$tmp/docs/y.md" 2>&1 || true)
grep -q 'y.md: over ceiling: 1600 words, how-to allows 1500' <<<"$out"
echo ok

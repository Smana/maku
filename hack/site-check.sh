#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Static contract checks for website/ (maku.ogenki.io):
#   1. website/static/CNAME is bare `maku.ogenki.io` (no trailing dot, no newline).
#   2. Fonts are self-hosted from web/src/assets/fonts (no external font CDNs).
#   3. Ogenki Day & Night tokens in website/assets/css/custom.css match web/src/app.css verbatim
#      and pass WCAG AA contrast pairs in both themes.
#   4. No `transition: all` in website/assets/css/custom.css.
set -euo pipefail

root=${1:-$(cd "$(dirname "$0")/.." && pwd)}

# 1. Bare CNAME check (no trailing dot, no trailing newline).
cname="$root/website/static/CNAME"
if [ ! -f "$cname" ]; then
  echo "FAIL: missing $cname" >&2
  exit 1
fi
want_bytes=$(printf 'maku.ogenki.io' | od -An -tx1 | tr -d ' \n')
got_bytes=$(od -An -tx1 < "$cname" | tr -d ' \n')
if [ "$got_bytes" != "$want_bytes" ]; then
  echo "FAIL: website/static/CNAME must be bare 'maku.ogenki.io' with no trailing dot or newline" >&2
  exit 1
fi

# 2. Local fonts mount check + no external font URLs.
if ! grep -q 'source: \.\./web/src/assets/fonts' "$root/website/hugo.yaml"; then
  echo "FAIL: website/hugo.yaml must mount ../web/src/assets/fonts into static/fonts" >&2
  exit 1
fi
for f in SpaceGrotesk-Variable.woff2 Inter-Variable.woff2 Inter-Variable-Italic.woff2 JetBrainsMono-Variable.woff2; do
  if [ ! -f "$root/web/src/assets/fonts/$f" ]; then
    echo "FAIL: missing mounted font web/src/assets/fonts/$f" >&2
    exit 1
  fi
done
if grep -rnE 'fonts\.(googleapis|gstatic)\.com|@import[[:space:]]+url\(|url\(["'\'']?https?://' \
     "$root/website/assets/css" "$root/website/layouts" 2>/dev/null; then
  echo "FAIL: external font or remote CSS URL found in website/assets/css or website/layouts" >&2
  exit 1
fi

# 3. No `transition: all`.
if grep -rnE 'transition(-property)?[[:space:]]*:[[:space:]]*all\b' "$root/website/assets/css" 2>/dev/null; then
  echo "FAIL: transition: all is banned in website/assets/css; name explicit properties" >&2
  exit 1
fi

# 4. Exact Day & Night Ogenki token parity with web/src/app.css + WCAG AA contrast audit.
python3 - "$root/web/src/app.css" "$root/website/assets/css/custom.css" <<'PY'
import re, sys

app_path, site_path = sys.argv[1], sys.argv[2]
app_css = open(app_path, encoding="utf-8").read()
site_css = open(site_path, encoding="utf-8").read()

def parse_tokens(css: str, selector: str) -> dict[str, str]:
    pattern = re.compile(rf"(?:^|\n)\s*{re.escape(selector)}\s*\{{([^}}]*)\}}", re.S)
    tokens: dict[str, str] = {}
    for m in pattern.finditer(css):
        for k, v in re.findall(r"(--[\w-]+)\s*:\s*(#[0-9a-fA-F]{6})\b", m.group(1)):
            tokens[k] = v.lower()
    return tokens

REQUIRED = [
    "--surface-000", "--surface-100", "--surface-200", "--line",
    "--foreground", "--muted-foreground",
    "--blue", "--violet", "--pink", "--green", "--amber", "--ink-on-accent",
]

errors: list[str] = []
for theme_name, sel in (("Day", ":root"), ("Night", ".dark")):
    want = parse_tokens(app_css, sel)
    got = parse_tokens(site_css, sel)
    for k in REQUIRED:
        if k not in want:
            errors.append(f"{theme_name}: {k} missing in web/src/app.css")
        elif got.get(k) != want[k]:
            errors.append(
                f"{theme_name} token drift for {k}: custom.css has {got.get(k)!r}, web/src/app.css has {want[k]!r}"
            )

def lum(h: str) -> float:
    r, g, b = (int(h[i:i+2], 16) / 255.0 for i in (1, 3, 5))
    lin = [c / 12.92 if c <= 0.03928 else ((c + 0.055) / 1.055) ** 2.4 for c in (r, g, b)]
    return 0.2126 * lin[0] + 0.7152 * lin[1] + 0.0722 * lin[2]

def ratio(a: str, b: str) -> float:
    la, lb = sorted((lum(a), lum(b)), reverse=True)
    return (la + 0.05) / (lb + 0.05)

if not errors:
    for theme_name, sel in (("Day", ":root"), ("Night", ".dark")):
        t = parse_tokens(site_css, sel)
        checks = [
            ("body text on page", t["--foreground"], t["--surface-000"], 4.5),
            ("body text on card", t["--foreground"], t["--surface-100"], 4.5),
            ("body text on raised", t["--foreground"], t["--surface-200"], 4.5),
            ("muted text on page", t["--muted-foreground"], t["--surface-000"], 4.5),
            ("muted text on card", t["--muted-foreground"], t["--surface-100"], 4.5),
            ("muted text on raised", t["--muted-foreground"], t["--surface-200"], 4.5),
            ("link blue on page", t["--blue"], t["--surface-000"], 4.5),
            ("link blue on card", t["--blue"], t["--surface-100"], 4.5),
            ("accent ink on primary button", t["--ink-on-accent"], t["--blue"], 4.5),
            ("violet accent on page", t["--violet"], t["--surface-000"], 4.5),
            ("green status on card", t["--green"], t["--surface-100"], 4.5),
            ("amber status on card", t["--amber"], t["--surface-100"], 4.5),
            ("pink accent on card", t["--pink"], t["--surface-100"], 4.5),
        ]
        for label, fg, bg, minimum in checks:
            r = ratio(fg, bg)
            if r < minimum:
                errors.append(f"{theme_name} contrast fail ({label}): {fg} on {bg} = {r:.2f}:1 < {minimum}:1")

if errors:
    for e in errors:
        print("FAIL:", e, file=sys.stderr)
    sys.exit(1)
PY

echo "==> site-check: CNAME, fonts, Ogenki tokens, and WCAG AA contrast hold"

#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# Unit tests for hack/site-check.sh against synthetic repositories in mktemp.
set -euo pipefail
repo=$(cd "$(dirname "$0")/.." && pwd)
check="$repo/hack/site-check.sh"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

make_good() {
  local d=$1
  rm -rf "$d"
  mkdir -p "$d/web/src/assets/fonts" "$d/website/static" "$d/website/assets/css" "$d/website/layouts"
  cp "$repo/web/src/app.css" "$d/web/src/app.css"
  for f in SpaceGrotesk-Variable.woff2 Inter-Variable.woff2 Inter-Variable-Italic.woff2 JetBrainsMono-Variable.woff2; do
    printf 'font' > "$d/web/src/assets/fonts/$f"
  done
  printf 'maku.ogenki.io' > "$d/website/static/CNAME"
  cat > "$d/website/hugo.yaml" <<'EOF'
module:
  mounts:
    - source: ../web/src/assets/fonts
      target: static/fonts
EOF
  cat > "$d/website/assets/css/custom.css" <<'EOF'
@font-face { font-family: "Space Grotesk"; src: url("/fonts/SpaceGrotesk-Variable.woff2") format("woff2"); font-display: swap; }
:root {
  --surface-000: #f5f6fc;
  --surface-100: #fdfdff;
  --surface-200: #eceef8;
  --line: #b9bfdc;
  --foreground: #12163a;
  --muted-foreground: #545b85;
  --blue: #1d63b0;
  --violet: #6a4bd8;
  --pink: #c2306f;
  --green: #11795a;
  --amber: #965800;
  --ink-on-accent: #fdfdff;
}
.dark {
  --surface-000: #0d1330;
  --surface-100: #161c42;
  --surface-200: #1f2754;
  --line: #3a4478;
  --foreground: #eef0fa;
  --muted-foreground: #a9b1d6;
  --blue: #5aa9f0;
  --violet: #a78bfa;
  --pink: #ff6fb5;
  --green: #3ddc97;
  --amber: #ffb547;
  --ink-on-accent: #0d1330;
}
EOF
}

expect_fail() {
  local name=$1 d=$2 pat=$3
  local out
  if out=$(bash "$check" "$d" 2>&1); then
    echo "FAIL [$name]: expected failure, got exit 0" >&2
    exit 1
  fi
  if ! grep -qE "$pat" <<<"$out"; then
    echo "FAIL [$name]: output did not match /$pat/:" >&2
    printf '%s\n' "$out" >&2
    exit 1
  fi
}

# 1. Valid site passes.
make_good "$tmp/ok"
bash "$check" "$tmp/ok" >/dev/null

# 2. Trailing dot in CNAME fails.
make_good "$tmp/dot"
printf 'maku.ogenki.io.' > "$tmp/dot/website/static/CNAME"
expect_fail "cname-trailing-dot" "$tmp/dot" "CNAME"

# 3. Trailing newline in CNAME fails.
make_good "$tmp/nl"
printf 'maku.ogenki.io\n' > "$tmp/nl/website/static/CNAME"
expect_fail "cname-newline" "$tmp/nl" "CNAME"

# 4. External font CDN fails.
make_good "$tmp/cdn"
printf '@import url("https://fonts.googleapis.com/css2?family=Inter");\n' >> "$tmp/cdn/website/assets/css/custom.css"
expect_fail "external-font-cdn" "$tmp/cdn" "external font"

# 5. Token drift from web/src/app.css fails.
make_good "$tmp/drift"
sed -i 's/#0d1330/#111111/' "$tmp/drift/website/assets/css/custom.css"
expect_fail "token-drift" "$tmp/drift" "token drift"

# 6. transition: all fails.
make_good "$tmp/trans"
printf 'a { transition: all 0.2s ease; }\n' >> "$tmp/trans/website/assets/css/custom.css"
expect_fail "transition-all" "$tmp/trans" "transition: all"

echo "==> site-check_test: ok"

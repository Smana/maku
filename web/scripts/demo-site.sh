#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
# demo-site.sh OUT: the live demo as a flat static site, every path relative, so it serves
# from a domain root or from GitHub Pages' /<repo>/ prefix alike.
set -euo pipefail
out=${1:?usage: demo-site.sh OUT}
web=$(cd "$(dirname "$0")/.." && pwd)
mkdir -p "$out/fixtures"
out=$(cd "$out" && pwd)
cd "$web"
[ -d node_modules ] || npm ci --no-audit --no-fund
npx esbuild preview/demo.ts --bundle --minify --format=esm --target=es2022 --outfile="$out/demo.js"
# The landing (the screencast of a real run) is the site's front page; the replay sits under
# it in a frame and on its own at replay.html.
cp preview/landing.html "$out/index.html"
cp preview/demo.html "$out/replay.html"
# The same source tree the broker embeds: src/assets as assets/, and app.css with its
# root-absolute /assets/ font URLs made relative to the page.
rm -rf "$out/assets"
cp -R src/assets "$out/assets"
sed 's#/assets/#assets/#g' src/app.css >"$out/app.css"
# The landing's screencast, captions and poster: kept out of src/assets, which the broker embeds.
cp -R preview/media "$out/media"
cp test/fixtures/demo-session.jsonl test/fixtures/demo-snapshot.json test/fixtures/demo-summary.json "$out/fixtures/"
echo "demo site: $out"

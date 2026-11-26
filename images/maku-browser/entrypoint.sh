#!/bin/bash
# The maku-browser sidecar's two processes under tini (PID 1, which reaps every orphan Chromium
# leaves): Playwright MCP on 127.0.0.1:9224 and the Go gate on 127.0.0.1:9223 in front of it.
# The script stays their parent rather than exec'ing the gate: when either one exits it stops the
# other and exits too, so the kubelet restarts the sidecar instead of keeping a gate whose /healthz
# still answers in front of a dead browser. SIGTERM goes to both; the gate drains for up to 10 s.
set -euo pipefail

mkdir -p /tmp/xdg /tmp/out /tmp/gate

node /opt/browser/node_modules/@playwright/mcp/cli.js --config /etc/maku-browser/config.json --no-webmcp &
mcp=$!
/usr/local/bin/maku-browser-gate -listen 127.0.0.1:9223 -upstream http://127.0.0.1:9224/mcp \
  -count /tmp/gate/count -shots /tmp/gate/shots &
gate=$!

stop() { kill -TERM "$gate" "$mcp" 2>/dev/null || true; }
trap stop TERM INT

# Whichever exits first (or a signal interrupts the wait), stop the other and wait for both.
rc=0
wait -n "$mcp" "$gate" || rc=$?
stop
wait "$mcp" "$gate" 2>/dev/null || true
exit "$rc"

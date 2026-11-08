#!/usr/local/bin/python
# SPDX-License-Identifier: Apache-2.0
"""fetch_release.py OWNER/REPO TAG ASSET DEST — download one GitHub release asset.

Tries github.com/<repo>/releases/download first, then the REST API's asset endpoint. On 2026-11-08 the
first returned 504 for every asset for hours while the API path served the same bytes. The caller
checks the pinned sha256, so the route changes nothing about what is trusted.
"""
import json
import sys
import time
import urllib.request

repo, tag, asset, dest = sys.argv[1:5]


def get(url, headers=None):
    req = urllib.request.Request(url, headers={"User-Agent": "maku-harness-build", **(headers or {})})
    with urllib.request.urlopen(req, timeout=120) as r:
        return r.read()


def direct():
    return get(f"https://github.com/{repo}/releases/download/{tag}/{asset}")


def api():
    rel = json.loads(get(f"https://api.github.com/repos/{repo}/releases/tags/{tag}",
                         {"Accept": "application/vnd.github+json"}))
    aid = next(a["id"] for a in rel["assets"] if a["name"] == asset)
    return get(f"https://api.github.com/repos/{repo}/releases/assets/{aid}",
               {"Accept": "application/octet-stream"})


err = None
for attempt in range(3):
    for route in (direct, api):
        try:
            data = route()
            with open(dest, "wb") as f:
                f.write(data)
            print(f"fetched {asset} via {route.__name__}", file=sys.stderr)
            sys.exit(0)
        except Exception as e:  # noqa: BLE001 - any failure falls through to the next route
            err = e
            print(f"{route.__name__} {asset}: {e}", file=sys.stderr)
    time.sleep(5 * (attempt + 1))
sys.exit(f"fetch_release: {asset}: {err}")

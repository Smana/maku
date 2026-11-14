#!/usr/bin/env bash
# The merge gate's ruleset on each repository (docs/factory.md#branch-protection-for-the-merge-gate).
# Review it, then run it with a token that administers the repositories. maku never runs it.
set -euo pipefail
cd "$(dirname "$0")"
gh api --hostname 'ghe.example.com' 'repos/platform/service/rulesets' --method POST --input ruleset-merge-gate.json

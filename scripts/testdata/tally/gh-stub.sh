#!/usr/bin/env bash
# A stand-in for gh for scripts/shadow-merge-tally_test.sh: logs its arguments to $GH_LOG and
# serves the fixtures next to it, so nothing reaches GitHub.
d="$(dirname "$0")"
echo "$*" >> "${GH_LOG:-/dev/null}"
case "$*" in
  # Fixture "drill" has reverts: reverts-drill.json, one of them a factory/drill revert.
  *"Revert in:title"*) f="$d/reverts-${TALLY_FIXTURE:-}.json"; [ -f "$f" ] && cat "$f" || echo '[]' ;;
  # A later pull request carries factory/follow-up-of:#<n>: only fixture "followup" has one, of #100.
  *'label:"factory/follow-up-of:#100"'*) [ "${TALLY_FIXTURE:-}" = followup ] && echo '[{"number":900}]' || echo '[]' ;;
  *'label:"factory/follow-up-of:#'*) echo '[]' ;;
  # Fixture "searchfail": the auto-merged search fails as gh does on a 502; the merged one answers.
  *'label:"factory/auto-merged:'*) [ "${TALLY_FIXTURE:-}" = searchfail ] && { echo 'HTTP 502: Bad Gateway' >&2; exit 1; }; echo '[]' ;;
  *'label:"factory/merged:'*) [ "${TALLY_FIXTURE:-}" = searchfail ] && TALLY_FIXTURE=; cat "$d/prs${TALLY_FIXTURE:+-$TALLY_FIXTURE}.json" ;;
  *) echo '[]' ;;
esac

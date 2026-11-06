**Resume: 2vq7m4xa · #2251 feat: the factory posts the release note it drafts**

Asked: #2251, the factory posts the release note it drafts
Acceptance: `go test ./internal/factory/reconciler/ -run TestReleaseNote`
Plan: scope ready · budget $2.00 ceiling

Changed since 2026-11-04 21:10 UTC (11 h):
- 21:26 the implementer started
- 21:48 note `gh: Resource not accessible by integration (HTTP 403): the note cannot be posted on the pull request`
- 21:49 the run ended: the agent stopped on an error

Blocked on: `recovery` — a maintainer comments \`/factory retry\` on issue \#2251 to run it again, or closes the room
Fix: the factory App is missing a permission on this repository: read the 403's scope in the run's note and grant it
Next: retry the task or close the room → https://rooms.example/r/2vq7m4xa
Links: room https://rooms.example/r/2vq7m4xa · issue https://github.com/Smana/ai-native-ref/issues/2251 · PR https://github.com/Smana/ai-native-ref/pull/2252

**Resume: 6hq3tv9c · #2264 docs: the runbook's rollback step names the old bucket**

Asked: #2264, the runbook's rollback step names the old bucket
Acceptance: `docs/runbooks/rollback.md names the bucket the manifest declares`
Plan: scope ready · budget $1.50 ceiling

Changed since 2026-11-04 22:40 UTC (10 h):
- 23:02 the implementer started
- 23:19 CI `docs` failed
- 23:20 note `the CI log is not readable from the sandbox: do not try to fetch it; read the check's name and your diff instead`

Blocked on: `failed_check` — a maintainer checks the failure and re\-runs it, or retries the task
Fix: the CI log's host is outside the factory's egress policy: quote the failing check's annotations instead, or add the host
Next: re-run the check or retry the task → https://github.com/Smana/ai-native-ref/pull/2265
Links: room https://rooms.example/r/6hq3tv9c · issue https://github.com/Smana/ai-native-ref/issues/2264 · PR https://github.com/Smana/ai-native-ref/pull/2265

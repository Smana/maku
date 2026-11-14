# Onboarding acme/my-service to agent-factory

Written by `maku repo add`. Nothing here has been applied.

| Path | What it is |
|---|---|
| `factory/repository-my-service.yaml` | the `Repository`: commit it where your GitOps applies the factory's objects |
| `repo/.github/agent-factory.yaml` | the fence: commit it to acme/my-service's default branch |
| `github/ruleset-merge-gate.json`, `github/apply-ruleset.sh` | the merge gate's ruleset and the `gh` command that creates it |

In order:

1. Commit `repo/.github/agent-factory.yaml` to acme/my-service's default branch.
2. Install both Apps on acme/my-service:
   - https://github.com/apps/acme-factory/installations/new
   - https://github.com/apps/acme-agents/installations/new
3. Review `github/apply-ruleset.sh`, then run it with a token that administers the repository.
4. Commit `factory/repository-my-service.yaml` to your GitOps repository.
5. Run `maku repo check acme/my-service` once the factory has applied it.

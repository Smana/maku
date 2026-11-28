# LinkedIn post draft

The open-source software factory for Kubernetes.

Own the entire issue-to-PR pipeline instead of renting a black-box SaaS. Maku is composed from open-source cloud-native tools (gVisor, agentgateway, OpenCode / OpenHands, Cilium, PostgreSQL, OIDC, Helm/Flux/Argo). One sentence from your coding agent or Slack becomes an issue a planner drafts and you confirm. Maku runs sandboxed agents on your cluster under hard external USD caps, streams every action into an auditable room, and gates pull requests through GitOps merge policies or maintainer review.

Apache-2.0, zero vendor lock-in → maku.ogenki.io

#Kubernetes #PlatformEngineering #AIAgents #OpenSource #CloudNative

---

# The roles clip (`agent-factory-v3`, docs)

`agent-factory-v3`, 34 s, 1080×1280, no title card. Embedded on [Roles](https://maku.ogenki.io/docs/concepts/roles/). What it says, beat by beat:

1. **0–4.5 s.** A **workflow** picks the roles: from the handoff form, a label, or the repository's default.
2. **4.5–9 s.** **quick**: the implementer alone for small, obvious fixes and scheduled upkeep.
3. **9–14 s.** **standard**: the implementer runs repo checks, then a fresh-context **reviewer**.
4. **14–20.5 s.** **careful**: a **scoper** bounds the task first (or asks the author one question), plus optional test-first **tester**.
5. **20.5–26 s.** The implementer **searches** the repo; a **judge** call checks acceptance criteria before the reviewer.
6. **26–34 s.** Policies judge the **diff**: a **live** policy merges under a daily cap, **shadow** only records, otherwise **one human approval** merges.

---
title: Concepts & architecture
weight: 30
description: "Vocabulary, service architecture, trust boundaries, factory lifecycle, security threat model, hardening, and multi-repository coordination."
---

{{< cards >}}
  {{< card link="glossary/" title="Concepts & glossary" subtitle="Vocabulary of rooms and the factory, room phases, and agent roles." >}}
  {{< card link="architecture/" title="Architecture" subtitle="Services, trust boundaries, one task's life, and runtime placement." >}}
  {{< card link="roles/" title="Agent roles" subtitle="Scoper, tester, implementer, completion judge, reviewer, and the merger App." >}}
  {{< card link="factory/" title="The factory" subtitle="Task lifecycle, triage, budgets, merge gate, escalation, kill switch, and splits." >}}
  {{< card link="security/" title="Security" subtitle="Threat model, trust boundaries, identities, authorization, and confinement." >}}
  {{< card link="hardening/" title="Hardening" subtitle="Untrusted-input hardening across stages H1–H5 and T5 promotion." >}}
  {{< card link="multi-repository/" title="Multi-repository coordination" subtitle="Cross-repository plans, confirmations, artifact dependencies, and recovery." >}}
  {{< card link="opencode-harness/" title="OpenCode harness" subtitle="Opt-in slim OpenCode harness adapter and parity evaluation." >}}
  {{< card link="runlore/" title="RunLore integration" subtitle="Agent-to-agent incident investigation, fix proposals, and post-merge alert checks." >}}
  {{< card link="providers/" title="Model providers" subtitle="Data-class separation, per-role provider overrides, and hosted vs self-hosted models." >}}
  {{< card link="routing-and-cost/" title="Model routing & cost" subtitle="Static per-run routing, prompt caching economics, metering, and layered USD budgets." >}}
  {{< card link="alternatives/" title="Alternatives considered" subtitle="Why each layer's choice won and concrete triggers to reconsider." >}}
{{< /cards >}}

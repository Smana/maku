# Factory issue template

**Title:** `<area>: <what is wrong>`, for example `docs(agents): user guide links a removed runbook`.

**Body:**

```text
## What is wrong
<file>:<line>: <what it says or does now>

## What it should be
<the expected text or behaviour>

## Acceptance check
<one command or observation that proves it is fixed>

Fix it; change nothing else.

## Decisions
D1 [g1]: <choice>, because <reason>
A2 [g2]: assumed <answer>, because <why>
```

One defect per issue. `## Decisions` is optional when there were no clarification gaps. Record IDs
ascend and stay unique within the issue; split issues repeat only the records they need, so numeric
gaps are allowed. Never paste secrets, tokens or customer data: the factory treats the issue as
untrusted input, and its agents read it in full.

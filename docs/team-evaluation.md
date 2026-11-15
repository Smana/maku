<!-- page-type: how-to -->
# Team evaluation

The factory can offer the [scoper](concepts.md#glossary) approved workflows and record which one each task used. This
walkthrough adds a default workflow and a frontier one to the [quickstart demo](quickstart.md). It changes no model or App secret.

Create `.demo/evaluation.yaml`, replacing the repository, maintainer and App login placeholders and
keeping the repository's existing values if you changed them. Helm replaces the repository list whole,
so the overlay must restate the complete entry:

```yaml
maku-factory:
  config:
    workflows:
      review-floor:
        description: Default implement-and-review loop.
        escalation: {outOfBudget: review-frontier, notConverged: review-frontier}
      review-frontier:
        tier: frontier
        description: For broad or multi-file documentation tasks that need a larger budget.
    evaluation:
      mode: apply
      controlPercent: 0
    repositories:
      - name: owner/repo
        maintainers: [owner]
        triggerLabel: factory/ready
        workflow: {default: review-floor}
        classes:
          docs-links: {shadow: true, allowedFiles: ['^.+\.(md|mdx|adoc|rst)$']}
          revert: {shadow: true, allowedFiles: ['^.+$']}
        merge:
          requiredChecks: [ci]
          verifyChecks: [ci]
          leakScanCheck: ci
          gateStatus: true
          policyBot: false
          approveToMerge: true
          mergerLogin: your-app[bot]
          maxPerDay: 5
          fixRuns: 2
          checkReruns: 1
          verifyFor: 30m
          revertWindow: 168h
          breaker: {window: 10, maxReverts: 1}
```

Upgrade the CRD chart, which carries the Task CRD, then apply both files on top of `values-demo.yaml`.

```bash
helm --kubeconfig .demo/kubeconfig upgrade af-crds charts/agent-factory-crds -n maku
helm --kubeconfig .demo/kubeconfig upgrade af charts/agent-factory -n maku \
  -f charts/agent-factory/values-demo.yaml -f .demo/values.yaml -f .demo/evaluation.yaml --timeout 5m
kubectl --kubeconfig .demo/kubeconfig rollout status -n maku deploy/agent-factory --timeout=240s
```

Create a bounded issue that touches two documentation files, label it `documentation` and
`factory/ready`, then inspect its workflow and the run's annotations:

```bash
kubectl --kubeconfig .demo/kubeconfig -n maku get tasks \
  -o custom-columns=NAME:.metadata.name,ISSUE:.spec.issue,PHASE:.status.phase,PROFILE:.status.profile.name,COHORT:.status.profile.cohort,RESOLVED:.status.profile.resolved
kubectl --kubeconfig .demo/kubeconfig -n maku get task <task-id> -o jsonpath='{.status.profile}{"\n"}'
kubectl --kubeconfig .demo/kubeconfig -n agents get agentrun <run-name> \
  -o jsonpath='{.metadata.annotations}{"\n"}'
```

Under `mode: apply`, a scoper recommendation of `review-frontier` resolves the task to that workflow
with `source: scoper` and `cohort: treatment`. Set `evaluation.mode: observe` in
`.demo/evaluation.yaml`, upgrade with the same two files, and submit another task. The same
recommendation now records `recommended: review-frontier` and `reason: observe` while `name` remains
`review-floor` and `source` remains `repository`.

Read the recommendation and outcome counters with a temporary port-forward in one shell:

```bash
kubectl --kubeconfig .demo/kubeconfig -n maku port-forward deploy/agent-factory 19090:9090 \
  >/tmp/maku-metrics.log 2>&1 &
pf=$!
trap 'kill "$pf" 2>/dev/null || true' EXIT
curl -fsS http://127.0.0.1:19090/metrics | \
  grep -E '^maku_factory_(profile_recommendations_total|task_outcomes_total)'
```

A scoper may legitimately keep the floor. That yields `reason: none` with no recommendation and is a
valid negative result, not proof that apply or observe mode is broken. Recommendations are model
outputs, so repeat a bounded task whose workflow fit is clear rather than changing the expected status.

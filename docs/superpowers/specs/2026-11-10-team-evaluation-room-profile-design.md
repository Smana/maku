# Team evaluation room profile design

## Purpose

Make a task's resolved harness profile and evaluation cohort visible in the room UI, and give laptop users a repeatable apply/observe walkthrough. Today this evidence requires Kubernetes reads and Prometheus queries, so a room viewer cannot see the evaluation decision the factory made.

## Constraints

- Preserve the existing `summary/v1` contract for current readers: every new field is optional and older brokers simply omit it.
- Keep profiles absent for tasks without `Task.Status.Profile`.
- Do not derive profile state from narration or scoper text. `Task.Status.Profile` is the source of truth.
- Render model-written `why` as text only, alongside the existing untrusted room content rules.
- Do not change evaluation decisions, merge policy, or the Settings workflow.
- Preserve profile evidence after terminal outcomes. The existing facts-key cleanup removes plan, parent, and merge-policy fields, not the new profile field.
- The quickstart must say that a live scoper may legitimately keep the floor instead of promising a recommendation.

## Contract

Add `profile` to `envelope.TaskFacts` and to `summary/v1.status` with the same shape:

```json
{
  "name": "review-frontier",
  "source": "scoper",
  "cohort": "treatment",
  "recommended": "review-frontier",
  "reason": "observe",
  "why": "The change spans several files."
}
```

| Field | Required | Meaning |
|---|---|---|
| `name` | yes | Active profile, from `Task.Status.Profile.Name` |
| `source` | yes | `label`, `floor`, `control`, `scoper`, or `escalation` |
| `cohort` | yes | `labeled`, `control`, or `treatment` |
| `recommended` | no | Scoper's recommended profile, including when observe mode keeps the floor |
| `reason` | no | Bounded floor/escalation reason such as `none`, `observe`, or `no_tests_paths` |
| `why` | no | Model-written recommendation explanation, at most 200 characters, rendered as text |

The envelope validates the same enums and bounds as the Task CRD. The reconciler copies the status fields without re-resolving them. The summary fold passes the object through without consulting the factory or Kubernetes.

## UI

Add a `Harness profile` details block beside the existing Merge policy block. Its summary reads `Harness profile · <name> · <cohort>`. The body shows `Source`, then `Recommended`, `Reason`, and `Why` only when present. Observe mode therefore reads as an active `review-floor` with a separate `review-frontier` recommendation instead of implying that the recommendation was applied.

The block is absent when `status.profile` is absent. Summary refreshes preserve the details element's open state, matching Merge policy. No new navigation or page is introduced.

## Quickstart

Extend `docs/quickstart.md` with a Team evaluation section containing:

1. A secret-free Helm overlay that declares `review-floor`, `review-frontier`, the review class, and `evaluation`.
2. The upgrade command using the existing `.demo/values.yaml` followed by `.demo/evaluation.yaml`.
3. The explicit Task CRD upgrade required because Helm does not upgrade chart `crds/` resources.
4. `kubectl` commands for `status.profile` and AgentRun annotations, plus a one-shot port-forward/curl for recommendation and outcome metrics.
5. Apply and observe examples, including the expected difference between `recommended`, `name`, `source`, and `reason`.
6. A caveat that scoper recommendations are model-dependent; `reason=none` is a valid negative result, not proof of a broken apply/observe path.

The local E2E evidence report remains an operational artifact and is not added to the documentation site.

## Failure and compatibility behavior

- An older room with no profile facts renders no block and loses no existing information.
- A malformed profile fails envelope validation instead of causing the factory to repeatedly post facts the broker would reject.
- A summary read never performs a new GitHub, factory, or Kubernetes call.
- Model-written `why` cannot create markup or links in the new block.

## Testing

- Envelope: profile enum and length-bound validation.
- Reconciler: task facts copy every profile field and preserve them in terminal facts.
- Summary: `summary/v1` includes the profile when present and omits it otherwise.
- UI: active-only, observe-recommendation, absent-profile, and untrusted-why cases; refresh preserves open state.
- Docs: repository link and style checks.
- Full gate: `task check`, including race-enabled Go tests, UI typecheck/tests, and UI bundle verification.
- Live smoke: after rebuilding PR images, a new floor-profile room shows `review-floor` with no applied recommendation. An older room whose sealed log predates the field keeps rendering without a profile block.

## Rollout

The change is additive and requires no migration. Broker and factory from the same PR images produce and render the new field; older factory output remains valid and renders no block. Rebuild the committed UI bundle and include it in the implementation PR.

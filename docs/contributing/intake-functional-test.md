<!-- page-type: reference -->
# Stubbed spec-intake functional test

`task chart:intake:k3d` exercises planner revisions, the scoper question handoff, `answer` and
status-comment generations without a real GitHub repository, model account or credential. It creates
and always deletes its own k3d cluster and throwaway keys. The opt-in test needs Docker with buildx,
k3d, Helm, kubectl, Go, OpenSSL, curl and jq; it is not part of `task check` or PR CI because importing
the release harness makes it slow. See the [chart test documentation](../../charts/agent-factory/README.md#spec-intake-k3d-test).

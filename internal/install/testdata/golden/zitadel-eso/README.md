# maku v0.48.0, agent-factory in namespace agent-system

Written by `maku install --print`. Nothing here has been applied.

| Path | What it is |
|---|---|
| `values.yaml` | the umbrella's values, `profile: production`: no secret value |
| `crds/values.yaml` | `agent-factory-crds`' values |
| `factory/` | the `Factory`, one `Repository` per repository, and the classes' `MergePolicy` objects |
| `github/` | the merge gate's ruleset, and the `gh` commands that create it |
| `secrets/` | git-ignored, 0700: what the wizard generated, and Secrets to fill |
| `gitops/` | the same install for Flux and for Argo CD |

## Before

- Postgres 16 or later with the three roles of docs/database.md, and the RuntimeClass `gvisor` on the nodes.
- Gateway API's standard CRDs (v1.2 or later), and the prometheus-operator CRDs.
- The two Apps installed on the repositories, and their Secrets in the namespace.
- The broker's TLS Secrets `room-broker-tls` and `room-broker-ca`, and the factory's `openbao-ca` (docs/install-secrets.md).

## Then, in order

```bash
helm install agent-factory-crds oci://ghcr.io/smana/charts/agent-factory-crds --version 0.48.0 -n agent-system --create-namespace -f crds/values.yaml
# the DSNs, the provider key and Slack's come from your store, at maku/<Secret name>
kubectl apply -n agent-system -f secrets/client-ids.yaml
kubectl apply -n agent-system -f secrets/slack-webhook.yaml
kubectl apply -n agent-system -f secrets/templates/github-link.yaml
kubectl apply -n agent-system -f secrets/web-proxy.yaml
helm install agent-factory oci://ghcr.io/smana/charts/agent-factory --version 0.48.0 -n agent-system -f values.yaml
kubectl apply -n agent-system -f factory/
bash github/apply-ruleset.sh
helm test agent-factory -n agent-system   # the preflight (D24): every seam checked from inside the cluster
```

With GitOps, `gitops/flux.yaml` or `gitops/argocd.yaml` replaces the two `helm install` lines.

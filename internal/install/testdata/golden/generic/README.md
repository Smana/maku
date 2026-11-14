# maku v0.48.0, agent-factory in namespace maku

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
- The CA of the ClusterIssuer `internal-ca`, as key `ca.crt`, in the Secrets `room-broker-ca` (the run-controller's), `openbao-ca` (the factory's, for the broker) and `agent-factory-ca` (the broker's, for the factory): cert-manager writes only the leaf Secrets `room-broker-tls` and `agent-factory-api-tls` (trust-manager or an ExternalSecret can fill them; docs/install-secrets.md).

## Then, in order

```bash
helm install agent-factory-crds oci://ghcr.io/smana/charts/agent-factory-crds --version 0.48.0 -n maku --create-namespace -f crds/values.yaml
git clone -q --depth 1 --branch v0.48.0 https://github.com/Smana/maku.git maku-v0.48.0 && kubectl apply --server-side -f maku-v0.48.0/config/agentgateway
# fill every empty value under secrets/templates/ first
kubectl apply -n maku -f secrets/client-ids.yaml
kubectl apply -n maku -f secrets/templates/database.yaml
kubectl apply -n maku -f secrets/templates/github-link.yaml
kubectl apply -n maku -f secrets/templates/model-provider.yaml
kubectl apply -n maku -f secrets/web-proxy.yaml
helm install agent-factory oci://ghcr.io/smana/charts/agent-factory --version 0.48.0 -n maku -f values.yaml
kubectl apply -n maku -f factory/
bash github/apply-ruleset.sh
helm test agent-factory -n maku   # the preflight (D24): every seam checked from inside the cluster
```

With GitOps, `gitops/flux.yaml` or `gitops/argocd.yaml` replaces the two `helm install` lines.

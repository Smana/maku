<!-- page-type: how-to -->

# Tier-1 Pilot

Run Maku on your own Kubernetes cluster against real **private GitHub repositories** in Week 1,
without provisioning an external PostgreSQL cluster or rewiring your platform first.

Tier-1 Pilot relaxes only high availability on the database (one PostgreSQL replica instead of a
multi-replica cluster) and keeps every merge human-approved (`shadow` mode). It preserves every
isolation and audit boundary that matters on private company code: **gVisor per run**, default-deny
network policies, gateway-enforced USD caps, per-run GitHub App tokens and database-enforced
immutable room logs.

## Tier-1 Pilot vs. Tier-2 Hardened

| Control | Tier-1 Pilot (Week 1–4) | Tier-2 Hardened Production |
|---|---|---|
| **Sandbox isolation** | **gVisor strictly mandatory** on private repositories ([sandbox nodes](sandbox-nodes.md)) | Dedicated gVisor node pool |
| **Database** | Hardened bundled PostgreSQL 16+ StatefulSet (`charts/agent-factory/values-pilot.example.yaml`, 1 replica, `20Gi` PVC, TLS 1.3 `verify-full`, 3 separated T12 roles, optional S3/GCS backup or CSI `VolumeSnapshot`) | External HA PostgreSQL 16+ (CloudNativePG, Amazon RDS, Google Cloud SQL or Azure Flexible Server) with continuous WAL backups ([the database](database.md)) |
| **GitHub identity** | Factory App (`apps.merger.mode: factory`) + Agents App | Optional dedicated Merger App ([GitHub Apps](github-apps.md)) |
| **Merge posture** | **100% human-approved merges**; every `MergePolicy` class in `shadow` mode | Earned `live` auto-merge on proven classes with daily caps + revert breaker |
| **Ingress and identity** | Standard Kubernetes Ingress or Gateway API `HTTPRoute` + your existing OIDC issuer (Okta, Entra ID, Keycloak, ZITADEL) | Private gateway + PKI certificates + hardened org group mapping |
| **Network egress** | Default-deny `NetworkPolicy` + in-cluster egress proxy | Cilium FQDN egress policy |

## Prerequisites

- A Kubernetes 1.30+ cluster with a CNI that enforces `NetworkPolicy` and nodes providing the
  `gvisor` `RuntimeClass` ([sandbox nodes](sandbox-nodes.md)).
- Gateway API CRDs (v1.2+) or an `IngressClass`, plus an OIDC issuer (Okta, Entra ID, Keycloak or
  ZITADEL) for signing humans in ([identity](identity.md)).
- A GitHub organization or account (GitHub Cloud or GHES 3.14+) and a model provider API key
  (Anthropic, OpenAI, Mistral, Z.ai, Moonshot or any OpenAI-compatible self-hosted endpoint).
- A Prometheus-compatible metrics endpoint scraping `maku-gateway` (the factory's USD meter).

You do **not** need an external PostgreSQL database to start a Tier-1 Pilot.

## Bootstrap a Tier-1 Pilot

1. **Generate the install directory** with `maku install --print` ([install on your cluster](install.md)):

   ```bash
   maku install --print --out ./maku-pilot
   ```

   Answer each seam question. To use the hardened bundled PostgreSQL StatefulSet without
   provisioning an external database, layer `charts/agent-factory/values-pilot.example.yaml` over
   the generated `values.yaml` (or select bundled database mode when prompted).

2. **Install CRDs, Secrets and the umbrella chart** in the order printed in `./maku-pilot/README.md`:

   ```bash
   helm install agent-factory-crds oci://ghcr.io/smana/charts/agent-factory-crds \
     --version <version> -n maku --create-namespace -f maku-pilot/crds/values.yaml
   kubectl apply --server-side -f config/agentgateway
   kubectl apply -n maku -f maku-pilot/secrets/
   helm install agent-factory oci://ghcr.io/smana/charts/agent-factory \
     --version <version> -n maku -f maku-pilot/values.yaml \
     -f charts/agent-factory/values-pilot.example.yaml
   ```

   With `values-pilot.example.yaml`, the chart generates and keeps the three T12 database role
   passwords, TLS 1.3 certificates (`postgres.tls.mode: helm` by default, or `cert-manager` when
   `issuerRef` is set), and the three `sslmode=verify-full` DSN Secrets automatically. The install
   banner surfaces `db_single_replica` (and `db_backups_off` while scheduled S3/GCS backups are off)
   as operational notices.

3. **Onboard your first private repository** ([onboard a repository](onboard-a-repository.md)):

   ```bash
   maku repo add <owner>/<repo> --out ./repo-onboarding \
     --factory-app <factory-slug> --agents-app <agents-slug> \
     --acknowledge-private-code
   ```

   Review the generated `Repository` manifest (`spec.privateCode.acknowledged: true`), apply it
   alongside `Factory` and `MergePolicy`, add `.github/agent-factory.yaml` to the repository, and
   run the ruleset script.

4. **Verify every seam before running work:**

   ```bash
   maku preflight --namespace maku --release agent-factory
   maku repo check <owner>/<repo> --namespace maku
   ```

5. **Connect your coding agent** and hand off your first real task:

   ```bash
   maku setup claude    # or opencode, cursor, codex, copilot, gemini
   ```

## Graduating from Tier-1 Pilot to Tier-2 Hardened

When the pilot succeeds and you want multi-replica database high availability, zero schema
translation is required: both tiers run identical PostgreSQL 16+ schemas and roles. Follow
[Graduating from Tier-1 Pilot to Tier-2 HA PostgreSQL](database.md#graduating-from-tier-1-pilot-to-tier-2-ha-postgresql)
to move your room history onto CloudNativePG, RDS, Cloud SQL or Azure Flexible Server with
`pg_dump` and `pg_restore`, then switch `postgres.enabled: false` and re-run `maku preflight`.

## Collaborating on a pilot

Maku is Apache-2.0 open source and is not a commercial SaaS product. For an engineering team using
**GitHub + Kubernetes** ready to pilot Maku on real private repositories:

- **What your team owns:** your Kubernetes cluster (with gVisor), your GitHub repositories and your
  model API keys and usage bills.
- **What the maintainer provides:** best-effort open-source support to bootstrap Tier-1 Pilot and
  collaborate directly on friction, hardening and workflow improvements driven by your stack — with
  optional paid freelance days if your company wants dedicated hands-on integration work.
- **What both sides get:** you get a sovereign, zero-seat-tax coding-agent factory on your own
  infrastructure; Maku gets real-world private-repository validation and feedback.

Interested in running a pilot? [Get in touch](https://blog.ogenki.io/about/).

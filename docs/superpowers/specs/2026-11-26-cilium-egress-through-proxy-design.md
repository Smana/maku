# Cilium mode: run HTTPS through the egress proxy

**Status:** follow-up spec, owner decision 2 of the
[GitHub egress tightening](2026-11-26-github-egress-tightening-design.md) (2026-11-26). Not
scheduled; nothing here is built.

**Outcome.** A run's HTTPS goes through the egress proxy in `cilium` mode as it does in
`kubernetes` mode. Both modes then have one enforcement point for web egress: the install window
closes in both, and both get the proxy's per-run decision log and metrics.

## The gap

| | `kubernetes` mode | `cilium` mode today |
|---|---|---|
| Web egress enforced by | the egress proxy (CONNECT, by pod IP and annotation) | the run's CNP, `toFQDNs` on 443 |
| Install window | closes on the harness's `POST /v1/close-install`, or at the deadline | open for the whole run: a CNP cannot narrow itself on a signal from the pod |
| Per-run decision log | `allow run=<id> <host:port> phase=...` | none; Hubble flows, if Hubble is on |
| Refusal alert | `MakuEgressInstallClosedRefusal` | none |

## Design

1. **The CNP opens the proxy, not the web.** In `cilium` mode the run's CNP replaces its
   `toFQDNs` 443 rule with a `toEndpoints` rule to the egress proxy's pods on its port. DNS stays
   restricted to the names the run uses (`rules.dns` keeps the matchNames of today's web hosts, so
   a refused name still does not resolve), which keeps `cilium` mode's advantage over `kubernetes`
   mode: no DNS tunnelling.
2. **The harness gets the proxy env.** `proxyEnv` and the `maku.ogenki.io/egress*` annotations
   are written in both modes. The install-set env (`EGRESS_INSTALL_CLOSE_URL`, `POD_UID`) follows.
3. **The proxy's own egress.** The proxy's policy already allows 443 out; in `cilium` mode the
   chart renders it as a CNP with `toFQDNs` for the union of the configured profiles and install
   hosts, so the proxy cannot be used to reach a host no run may reach.
4. **The preflight.** The netprobe pod checks the proxy path in `cilium` mode too.

## Open points to settle before building

| # | Question | Leaning |
|---|---|---|
| 1 | Keep a per-host `toFQDNs` rule for the model host outside the cluster on a port other than 443, or send it through the proxy as well? | Through the proxy: the proxy already allows `host:port` pairs other than 443 from the annotation. |
| 2 | The proxy is then on every cluster's critical path. Replica count and PDB? | Keep 2 replicas and the PDB the chart has; the `EgressProxyDown` alert already covers it. |
| 3 | `networkPolicy: none` clusters? | Unchanged: nothing is enforced there, and the banner says so. |

## Plan

| # | Task |
|---|---|
| C1 | runctl: `cilium` mode writes the proxy env and annotations; the CNP's web rule targets the proxy pods; goldens |
| C2 | chart: the proxy's CNP in `cilium` mode; the preflight's proxy check in both modes |
| C3 | docs: the egress page and the threat model drop the `cilium`-mode residual |
| C4 | live drill: the GitHub egress drill, steps 1 to 4, on a `cilium`-mode cluster |

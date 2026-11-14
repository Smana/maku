// SPDX-License-Identifier: Apache-2.0

package install

import (
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"
)

const (
	chartRegistry = "oci://ghcr.io/smana/charts"
	makuRepoURL   = "https://github.com/Smana/maku"
)

// chartVersion is the charts' version for a maku release: its tag without the v.
func chartVersion(v string) string { return strings.TrimPrefix(v, "v") }

func docs(objs ...map[string]any) ([]byte, error) {
	var parts []string
	for _, o := range objs {
		b, err := yaml.Marshal(o)
		if err != nil {
			return nil, err
		}
		parts = append(parts, string(b))
	}
	return []byte(strings.Join(parts, "---\n")), nil
}

// fluxFiles is the install for Flux 2.4 or later (D26): the two charts from GHCR, agentgateway's CRDs
// from this maku's tag, the CRD chart first and the umbrella with crds: Skip.
func fluxFiles(in Inputs, vals map[string]any) ([]byte, error) {
	a := in.Answers
	ns, rel, ver := in.ns(), a.String("release"), a.String("version")
	oci := func(name string) map[string]any {
		return map[string]any{"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "OCIRepository",
			"metadata": map[string]any{"name": name, "namespace": ns},
			"spec": map[string]any{"interval": "10m", "url": chartRegistry + "/" + name, "ref": map[string]any{"tag": chartVersion(ver)},
				"layerSelector": map[string]any{"mediaType": "application/vnd.cncf.helm.chart.content.v1.tar+gzip", "operation": "copy"}}}
	}
	objs := []map[string]any{
		{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": ns}},
		oci("agent-factory-crds"),
		oci("agent-factory"),
		{"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
			"metadata": map[string]any{"name": rel + "-crds", "namespace": ns},
			"spec": map[string]any{"interval": "10m", "chartRef": map[string]any{"kind": "OCIRepository", "name": "agent-factory-crds"},
				"values": map[string]any{"keep": true}}},
	}
	deps := []any{map[string]any{"name": rel + "-crds"}}
	if a.Bool("models.controller") {
		objs = append(objs,
			map[string]any{"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "GitRepository",
				"metadata": map[string]any{"name": "maku", "namespace": ns},
				"spec":     map[string]any{"interval": "1h", "url": makuRepoURL, "ref": map[string]any{"tag": ver}}},
			// agentgateway's CRDs, apart from any Helm release (A11): never pruned.
			map[string]any{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
				"metadata": map[string]any{"name": "agentgateway-crds", "namespace": ns},
				"spec": map[string]any{"interval": "1h", "sourceRef": map[string]any{"kind": "GitRepository", "name": "maku"},
					"path": "./config/agentgateway", "prune": false}},
		)
	}
	objs = append(objs, map[string]any{"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]any{"name": rel, "namespace": ns},
		"spec": map[string]any{"interval": "10m", "chartRef": map[string]any{"kind": "OCIRepository", "name": "agent-factory"},
			"dependsOn": deps,
			"install":   map[string]any{"crds": "Skip"}, "upgrade": map[string]any{"crds": "Skip"},
			"values": vals}})
	b, err := docs(objs...)
	if err != nil {
		return nil, err
	}
	head := fmt.Sprintf("# Flux 2.4 or later: the CRD chart first, %s with crds: Skip. Apply secrets/ first (or let\n# ExternalSecrets fill them), and the Gateway API CRDs (v1.2 or later) are the cluster's.\n", rel)
	return append([]byte(head), b...), nil
}

// argoFiles is the install for Argo CD 2.12 or later (D26): Applications in sync waves, the CRD chart,
// agentgateway's CRDs (Prune=false), then the umbrella with skipCrds.
func argoFiles(in Inputs, vals map[string]any) ([]byte, error) {
	a := in.Answers
	ns, rel, ver := in.ns(), a.String("release"), a.String("version")
	app := func(name, wave string, source map[string]any, opts ...string) map[string]any {
		syncOpts := append([]any{"CreateNamespace=true", "ServerSideApply=true"}, anys(opts)...)
		return map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
			"metadata": map[string]any{"name": name, "namespace": "argocd", "annotations": map[string]any{"argocd.argoproj.io/sync-wave": wave}},
			"spec": map[string]any{"project": "default", "source": source,
				"destination": map[string]any{"server": "https://kubernetes.default.svc", "namespace": ns},
				"syncPolicy":  map[string]any{"automated": map[string]any{"prune": true, "selfHeal": true}, "syncOptions": syncOpts}}}
	}
	objs := []map[string]any{
		app(rel+"-crds", "0", map[string]any{"repoURL": strings.TrimPrefix(chartRegistry, "oci://"), "chart": "agent-factory-crds", "targetRevision": chartVersion(ver),
			"helm": map[string]any{"valuesObject": map[string]any{"keep": true}}}),
	}
	if a.Bool("models.controller") {
		objs = append(objs, app("agentgateway-crds", "1", map[string]any{"repoURL": makuRepoURL, "targetRevision": ver, "path": "config/agentgateway"}, "Prune=false"))
	}
	objs = append(objs, app(rel, "2", map[string]any{"repoURL": strings.TrimPrefix(chartRegistry, "oci://"), "chart": "agent-factory", "targetRevision": chartVersion(ver),
		"helm": map[string]any{"releaseName": rel, "skipCrds": true, "valuesObject": vals}}))
	b, err := docs(objs...)
	if err != nil {
		return nil, err
	}
	head := "# Argo CD 2.12 or later: register ghcr.io as a Helm OCI repository first. Argo CD runs no Helm\n# test hook: run helm test (the preflight) by hand once the umbrella is synced.\n"
	return append([]byte(head), b...), nil
}

// readme is the tree's README: what was written, and the commands to run next, in order: the CRDs,
// agentgateway's CRDs, the Secrets, the umbrella, the ruleset, then the preflight.
func readme(in Inputs, t Tree) []byte {
	a := in.Answers
	ns, rel, ver := in.ns(), a.String("release"), a.String("version")
	var b strings.Builder
	fmt.Fprintf(&b, "# maku %s, %s in namespace %s\n\nWritten by `maku install --print`. Nothing here has been applied.\n\n", ver, rel, ns)
	b.WriteString("| Path | What it is |\n|---|---|\n")
	b.WriteString("| `values.yaml` | the umbrella's values, `profile: production`: no secret value |\n")
	b.WriteString("| `crds/values.yaml` | `agent-factory-crds`' values |\n")
	b.WriteString("| `factory/` | the `Factory`, one `Repository` per repository, and the classes' `MergePolicy` objects |\n")
	b.WriteString("| `github/` | the merge gate's ruleset, and the `gh` commands that create it |\n")
	b.WriteString("| `secrets/` | git-ignored, 0700: what the wizard generated, and Secrets to fill |\n")
	b.WriteString("| `gitops/` | the same install for Flux and for Argo CD |\n\n")
	b.WriteString("## Before\n\n")
	fmt.Fprintf(&b, "- Postgres 16 or later with the three roles of docs/database.md, and the RuntimeClass `%s` on the nodes.\n", defaultStr(a, "sandbox.runtimeClassName", "gvisor"))
	b.WriteString("- Gateway API's standard CRDs (v1.2 or later)")
	if a.Bool("monitoring.enabled") {
		b.WriteString(", and the prometheus-operator CRDs")
	}
	b.WriteString(".\n")
	if InClusterIssuer(a.String("cluster.issuer")) {
		b.WriteString("- An API server that admits anonymous requests, for the ServiceAccount issuer's JWKS (the chart binds `system:service-account-issuer-discovery` to `system:unauthenticated`, docs/gateway.md). On k3s, start it with `--kube-apiserver-arg=anonymous-auth=true`.\n")
	}
	if !in.Factory.Created() || !in.Agents.Created() {
		b.WriteString("- The two Apps installed on the repositories, and their Secrets in the namespace.\n")
	} else {
		fmt.Fprintf(&b, "- Install both Apps on the repositories: %s and %s.\n", in.Agents.HTMLURL+"/installations/new", in.Factory.HTMLURL+"/installations/new")
	}
	if a.String("tls.issuer") == "" {
		b.WriteString("- The broker's TLS Secrets `room-broker-tls` and `room-broker-ca`, and the factory's `openbao-ca` (docs/install-secrets.md).\n")
	} else {
		fmt.Fprintf(&b, "- The CA of the ClusterIssuer `%s`, as key `ca.crt`, in the Secrets `room-broker-ca` (the run-controller's), `openbao-ca` (the factory's, for the broker) and `%s` (the broker's, for the factory): cert-manager writes only the leaf Secrets `room-broker-tls` and `agent-factory-api-tls` (trust-manager or an ExternalSecret can fill them; docs/install-secrets.md).\n", a.String("tls.issuer"), secretFactoryCA)
	}
	b.WriteString("\n## Then, in order\n\n```bash\n")
	fmt.Fprintf(&b, "helm install %s-crds %s/agent-factory-crds --version %s -n %s --create-namespace -f crds/values.yaml\n", rel, chartRegistry, chartVersion(ver), ns)
	if a.Bool("models.controller") {
		fmt.Fprintf(&b, "git clone -q --depth 1 --branch %s %s.git maku-%s && kubectl apply --server-side -f maku-%s/config/agentgateway\n", ver, makuRepoURL, ver, ver)
	}
	if a.String("secrets.mode") == "eso" {
		b.WriteString("# the DSNs, the provider key and Slack's come from your store, at maku/<Secret name>\n")
	} else {
		b.WriteString("# fill every empty value under secrets/templates/ first\n")
	}
	for _, p := range secretNames(t) {
		fmt.Fprintf(&b, "kubectl apply -n %s -f %s\n", ns, p)
	}
	fmt.Fprintf(&b, "helm install %s %s/agent-factory --version %s -n %s -f values.yaml\n", rel, chartRegistry, chartVersion(ver), ns)
	fmt.Fprintf(&b, "kubectl apply -n %s -f factory/\n", ns)
	b.WriteString("bash github/apply-ruleset.sh\n")
	fmt.Fprintf(&b, "helm test %s -n %s   # the preflight (D24): every seam checked from inside the cluster\n```\n", rel, ns)
	b.WriteString("\nWith GitOps, `gitops/flux.yaml` or `gitops/argocd.yaml` replaces the two `helm install` lines.\n")
	return []byte(b.String())
}

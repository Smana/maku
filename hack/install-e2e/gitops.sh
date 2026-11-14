# shellcheck shell=bash
# shellcheck disable=SC2034,SC2154 # its variables are flux.sh's and argocd.sh's; lib.sh's are its
# SPDX-License-Identifier: Apache-2.0
#
# The GitOps legs' shared part (v4 step 7, PR G; D23, D26, R6, R7), sourced by flux.sh and argocd.sh
# after kind-calico.sh. Each installs the kind-calico leg on Kubernetes 1.30, the support floor, from
# the wizard's own gitops/ file instead of `helm install`, changed only where a test must:
#   - the two charts come from an OCI registry in the cluster, which serves this checkout's charts under
#     the wizard's version, instead of ghcr.io;
#   - agentgateway's CRDs come from github.com/Smana/maku at this commit instead of the release's tag,
#     so the commit must be pushed (GITHUB_SHA in CI);
#   - the umbrella's values are what lib.sh gives `helm install`: the wizard's, overlay.yaml and this
#     run's images.
# Then, beside kind-calico.sh's own checks and `maku preflight --strict`:
#   1. the tool reconciled the CRD chart before the umbrella, and the umbrella owns no CRD;
#   2. a second reconcile changes nothing: no release revision, sync or Deployment generation moves;
#   3. agentgateway's CRDs survive a pruning reconcile of the umbrella and the removal of their own
#      source (A11).
# The caller sets GITOPS (its name) and defines:
#   gitops_up               the tool, from its release's install manifest;
#   gitops_apply FILE       apply the rewritten gitops file and wait until every object is reconciled,
#                           calling factory_objects once the CRD chart is;
#   gitops_order            assert the CRD chart was reconciled before the umbrella;
#   gitops_owner CRD        the release or application that tracks CRD;
#   gitops_state            one line per tool object that a no-op reconcile must leave unchanged;
#   gitops_reconcile        reconcile every object again and wait until the tool is done;
#   gitops_prune            reconcile the umbrella with pruning, then remove agentgateway's CRDs' source.

REGISTRY_NS=registry
REGISTRY_HOST=registry.$REGISTRY_NS.svc.cluster.local
REGISTRY="$REGISTRY_HOST:5000"
GIT_SHA="${GITHUB_SHA:-$(git rev-parse HEAD)}"
GIT_BRANCH="${GITHUB_REF_NAME:-$(git rev-parse --abbrev-ref HEAD)}"

# chart_version: the version the wizard's gitops files name, which this checkout's charts are pushed as.
chart_version() {
  yq -e 'select(.kind == "OCIRepository" and .metadata.name == "agent-factory") | .spec.ref.tag' "$OUT/gitops/flux.yaml"
}

# registry_up: a registry:3 behind the run's CA, and this checkout's two charts pushed into it.
registry_up() {
  step "an OCI registry in the cluster ($REGISTRY), serving this checkout's charts as $(chart_version)"
  cert registry "$REGISTRY_HOST" "DNS:$REGISTRY_HOST,DNS:registry.$REGISTRY_NS.svc,IP:127.0.0.1"
  k create namespace "$REGISTRY_NS" >/dev/null
  k -n "$REGISTRY_NS" create secret tls registry-tls --cert="$work/registry.crt" --key="$work/registry.key" >/dev/null
  k -n "$REGISTRY_NS" apply -f - >/dev/null <<YAML
apiVersion: apps/v1
kind: Deployment
metadata: {name: registry}
spec:
  replicas: 1
  selector: {matchLabels: {app.kubernetes.io/name: registry}}
  template:
    metadata: {labels: {app.kubernetes.io/name: registry}}
    spec:
      automountServiceAccountToken: false
      containers:
        - name: registry
          image: $REGISTRY_IMAGE
          env:
            - {name: REGISTRY_HTTP_ADDR, value: ":5000"}
            - {name: REGISTRY_HTTP_TLS_CERTIFICATE, value: /tls/tls.crt}
            - {name: REGISTRY_HTTP_TLS_KEY, value: /tls/tls.key}
          ports: [{name: registry, containerPort: 5000}]
          readinessProbe: {tcpSocket: {port: registry}, periodSeconds: 3}
          resources: {requests: {cpu: 50m, memory: 64Mi}, limits: {cpu: "1", memory: 256Mi}}
          volumeMounts: [{name: tls, mountPath: /tls, readOnly: true}, {name: data, mountPath: /var/lib/registry}]
      volumes: [{name: tls, secret: {secretName: registry-tls}}, {name: data, emptyDir: {}}]
---
apiVersion: v1
kind: Service
metadata: {name: registry}
spec:
  selector: {app.kubernetes.io/name: registry}
  ports: [{name: registry, port: 5000, targetPort: registry}]
YAML
  k -n "$REGISTRY_NS" rollout status deploy/registry --timeout=180s >/dev/null
  local v c
  v="$(chart_version)"
  mkdir -p "$work/charts"
  h dependency build charts/agent-factory >/dev/null
  h package charts/agent-factory-crds charts/agent-factory --version "$v" -d "$work/charts" >/dev/null
  port_forward "$REGISTRY_NS" svc/registry 5000
  for c in agent-factory-crds agent-factory; do
    h push "$work/charts/$c-$v.tgz" "oci://127.0.0.1:$PF_PORT/charts" --ca-file "$work/ca.crt" >/dev/null 2>&1 ||
      die "helm push $c-$v.tgz to the cluster's registry failed"
  done
  kill "$PF_PID" 2>/dev/null || true
}

# gitops_values FILE: the umbrella's values as lib.sh's umbrella_args gives them to `helm install`: its
# -f files merged in order (a list replaces, as in Helm), then its --set pairs.
gitops_values() {
  local args files=() sets=() i s p
  mapfile -t args < <(umbrella_args)
  for ((i = 0; i < ${#args[@]}; i += 2)); do
    case ${args[i]} in
      -f) files+=("${args[i + 1]}") ;;
      --set) sets+=("${args[i + 1]}") ;;
      *) die "umbrella_args gave ${args[i]}: gitops_values reads -f and --set only" ;;
    esac
  done
  # shellcheck disable=SC2016 # yq's own variable
  yq eval-all '. as $v ireduce ({}; . * $v)' "${files[@]}" >"$1"
  for s in "${sets[@]}"; do
    p="$(jq -rn --arg k "${s%%=*}" '"." + ($k | split(".") | map("[\"" + . + "\"]") | join(""))')"
    case ${s#*=} in
      true | false) yq -i "$p = ${s#*=}" "$1" ;;
      *) V="${s#*=}" yq -i "$p = strenv(V)" "$1" ;;
    esac
  done
}

# agentgateway_crds: the CRDs config/agentgateway holds, which no Helm release may own (A11).
agentgateway_crds() { yq -N 'select(.kind == "CustomResourceDefinition") | .metadata.name' config/agentgateway/*.yaml; }

# deploy_generations: each Deployment of the release's namespace and its generation.
deploy_generations() {
  k -n "$NS" get deploy -o jsonpath='{range .items[*]}{.metadata.name}={.metadata.generation}{"\n"}{end}'
}

# install, replacing lib.sh's: README.md's order, through the tool.
install() {
  step "install through $GITOPS, from the wizard's gitops/$GITOPS.yaml"
  gitops_up
  registry_up
  secrets
  leg_before_umbrella
  gitops_values "$work/umbrella-values.yaml"
  gitops_apply "$OUT/gitops/$GITOPS.yaml"
}

# factory_objects: the wizard's factory/, once the CRD chart is reconciled and before the umbrella
# must be Ready: the factory serves no readiness until its Factory and Repository exist, and both
# tools wait for the umbrella's readiness.
factory_objects() {
  wait_crds -l app.kubernetes.io/part-of=agent-factory crd
  k apply -n "$NS" -f "$OUT/factory/" >/dev/null
}

gitops_checks() {
  step "$GITOPS: the CRD chart before the umbrella, which owns no CRD"
  gitops_order
  local crd owner
  while IFS= read -r crd; do
    owner="$(gitops_owner "$crd")"
    [[ $owner != "$RELEASE" ]] || die "the umbrella $RELEASE tracks the CRD $crd: crds Skip did not hold"
  done < <(k get crd -o name | sed 's|^.*/||' | grep -E '\.(ogenki\.io|agentgateway\.dev)$')
  echo "ok: the CRD chart was reconciled first, and the umbrella tracks no CRD"

  step "$GITOPS: a second reconcile changes nothing"
  local before after
  before="$(gitops_state && deploy_generations)"
  gitops_reconcile
  after="$(gitops_state && deploy_generations)"
  [[ $before == "$after" ]] || die "a second reconcile changed the install: $(diff <(echo "$before") <(echo "$after"))"
  echo "ok: no release revision, sync or Deployment generation moved"

  step "$GITOPS: agentgateway's CRDs survive a prune (A11)"
  gitops_prune
  while IFS= read -r crd; do
    k get crd "$crd" >/dev/null 2>&1 || die "agentgateway's CRD $crd did not survive the prune"
  done < <(agentgateway_crds)
  echo "ok: agentgateway's CRDs survive a pruning reconcile of the umbrella and the removal of their source"
}

# leg_checks, extending kind-calico.sh's: the sandbox and the Gateway, then the GitOps checks.
if declare -F leg_checks >/dev/null; then
  eval "kind_$(declare -f leg_checks)"
  leg_checks() {
    kind_leg_checks
    gitops_checks
  }
fi

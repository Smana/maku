#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Proves the broker in preset generic against a generic OIDC issuer (v4 D8) on a throwaway k3d
# cluster. The issuer is the in-cluster e2e-stub: discovery, its JWKS, and ID tokens minted for
# any sub, client and groups. Nothing external is contacted but the images' registries.
#
# The broker is called as oauth2-proxy would call it, with the ID token in Authorization and a
# distinct access token in X-Forwarded-Access-Token:
#   - a member's web session lists the rooms;
#   - a token in no agents group is refused 403;
#   - the CLI client's ID token as a bearer reads, and is never the web UI, so it cannot decide (P18).
#
# EXPOSE=httproute|ingress (task chart:expose:k3d; plan v4 step 6, Task 12) installs the web exposed
# through the web front door (tests/expose-values.yaml) in networkPolicies.mode kubernetes, behind
# agentgateway (Gateway API) or k3s's Traefik (Ingress), and plays the browser from a client pod:
#   - GET / redirects to the stub issuer, and its code flow signs in; that session lists the rooms
#     as the web UI;
#   - GET /.well-known/roomctl answers without a session, and so does GET /link/github/callback when
#     the GitHub account link is on, while POST to it and GET /link/github still need a session;
#   - a CLI bearer reads /api/rooms, and the same bearer with a forged X-Forwarded-Access-Token is
#     still not the web UI (P18);
#   - the broker's :8080 refuses a pod that is not the front door;
#   - /v1/ws stays open EXPOSE_WS_SECONDS (360).
#
# Then the GitHub account link (v4 D8, step 5 PR C), with the stub as GitHub's OAuth web flow and the
# factory App's API, and no ZITADEL reader at all:
#   - a member with no link sees no room (X-Rooms-Access: unlinked);
#   - they start a link, follow GitHub's redirect, confirm, and /link/github shows @dev;
#   - the user token was revoked, and no ZITADEL endpoint was ever called;
#   - the room is now theirs to read, through the link;
#   - an admin's unlink makes them unlinked again.
#
# SLACK=1 (with EXPOSE=httproute; task chart:slack:k3d; plan v4 step 6, Task 16) adds the Slack App from
# its subchart (values-slack.example.yaml), the factory, and both default-deny policies, all in mode
# kubernetes:
#   - a Slack-signed url_verification through the Gateway's POST /slack/events reaches the App, a
#     badly signed one is 401, and GET /slack/events is the gateway's 404;
#   - the App's :8081 admits the broker's pods and nobody else;
#   - a pod under the factory's policy reaches DNS but not a private address outside its peers.
#
# PROFILE=production (with EXPOSE=httproute; task chart:production:k3d; plan v4 step 6, Task 21) installs
# profile production (tests/production-k3d-values.yaml), with no demo value: a test-only RuntimeClass
# gvisor whose handler is runc (sandbox.runtimeClassName resolves, unsafeAllowRunc stays refused; no run
# starts), a plain postgres:16 outside the chart bootstrapped with docs/database.md, the broker's TLS
# Secrets by name, the prometheus-operator CRDs alone (no operator), and the factory with the stub as its
# GitHub. It adopts the umbrella as docs/upgrading.md says:
#   - maku-factory alone, then `helm upgrade` to the umbrella with maku.enabled false: the factory's
#     Deployment keeps its UID;
#   - then maku and the rest on; the flows above run through the Gateway, the factory's ops /readyz is
#     200, and the PrometheusRule and the ServiceMonitors exist.
#
# The cluster, its kubeconfig and the images' tag are this run's own: the current kubectl context
# is never read or changed.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/../../.."

TAG="${TAG:-identity-$(date +%s)-$$}"
NAME="maku-${TAG}"
NS=maku
K3S_IMAGE="${K3S_IMAGE:-rancher/k3s:v1.35.0-k3s1}"
VALUES=charts/agent-factory/tests/identity-values.yaml
LINK_VALUES=charts/agent-factory/tests/identity-link-values.yaml
STUB=charts/agent-factory/tests/e2e-stub.yaml
# The images this run builds and loads. The expose leg adds the run-controller: networkPolicies.mode
# kubernetes is the run-controller's (its egress proxy), so the chart refuses it without one. No run
# starts, so the harness and the bridge are never pulled.
images=(maku-broker maku-e2e-stub)
[[ -z ${EXPOSE:-} ]] || images+=(maku-run-controller)
SLACK="${SLACK:-}"
[[ -z $SLACK || ${EXPOSE:-} == httproute ]] || { echo "FAIL: SLACK=1 needs EXPOSE=httproute" >&2; exit 2; }
[[ -z $SLACK ]] || images+=(maku-slack maku-factory)
EXPOSE="${EXPOSE:-}"
case "$EXPOSE" in
  "" | httproute | ingress) ;;
  *) echo "FAIL: EXPOSE=$EXPOSE is not httproute or ingress" >&2; exit 2 ;;
esac
PROFILE="${PROFILE:-demo}"
case "$PROFILE" in
  demo) ;;
  production)
    [[ $EXPOSE == httproute && -z $SLACK ]] || { echo "FAIL: PROFILE=production needs EXPOSE=httproute, without SLACK" >&2; exit 2; }
    images+=(maku-factory)
    ;;
  *) echo "FAIL: PROFILE=$PROFILE is not demo or production" >&2; exit 2 ;;
esac
# The external database's image, as install.sh's EXTERNAL_DB leg: postgres:16, the support floor (D26).
PG_IMAGE=docker.io/library/postgres:16-alpine@sha256:f377d590e5474681d4d7838635031a2b50647923b7abfa803ce9b5ff35d5c663
# The prometheus-operator CRDs (ServiceMonitor, PodMonitor, PrometheusRule), applied alone.
PROMETHEUS_OPERATOR=v0.81.0
EXPOSE_WS_SECONDS="${EXPOSE_WS_SECONDS:-360}"
GATEWAY_API=v1.2.1
PY_IMAGE=docker.io/library/python:3.15-alpine@sha256:ff4d3c9b1a48f646eedd93182aaea385c7a5452f4c8a8cd7cef686babacf4928
step() { printf '\n==> %s\n' "$*"; }
die() { echo "FAIL: $*" >&2; exit 1; }

umask 077
work="$(mktemp -d "${TMPDIR:-/tmp}/maku-identity-k3d.XXXXXX")"
export KUBECONFIG="$work/kubeconfig"
k() { kubectl --kubeconfig "$KUBECONFIG" --context "k3d-$NAME" "$@"; }
created=false
pf_pids=()
onexit() {
  local rc=$?
  trap - EXIT
  for pid in "${pf_pids[@]}"; do kill "$pid" >/dev/null 2>&1 || true; done
  if [[ $rc -ne 0 ]] && $created; then
    k get pods -A -o wide >&2 || true
    k -n "$NS" logs deploy/room-broker --all-containers --tail=120 >&2 || true
    k -n e2e logs deploy/e2e-stub --tail=60 >&2 || true
    if [[ -n $SLACK ]]; then
      k -n "$NS" logs deploy/factory-slack-app --tail=60 >&2 || true
      k -n "$NS" logs deploy/factory-slack-app --previous --tail=60 >&2 || true
    fi
  fi
  if $created; then
    step "delete the cluster $NAME"
    k3d cluster delete "$NAME" >/dev/null || true
  fi
  for image in "${images[@]}"; do docker rmi "ghcr.io/smana/$image:$TAG" >/dev/null 2>&1 || true; done
  rm -rf "$work"
  exit "$rc"
}
trap onexit EXIT

step "build the broker and the stub from this checkout, tag $TAG"
docker build -q -f images/maku-broker/Dockerfile -t "ghcr.io/smana/maku-broker:$TAG" . >/dev/null
[[ -z $EXPOSE ]] || docker build -q -f images/maku-run-controller/Dockerfile -t "ghcr.io/smana/maku-run-controller:$TAG" . >/dev/null
[[ -z $SLACK ]] || docker build -q -f images/maku-slack/Dockerfile -t "ghcr.io/smana/maku-slack:$TAG" . >/dev/null
[[ -z $SLACK && $PROFILE != production ]] || docker build -q -f images/maku-factory/Dockerfile -t "ghcr.io/smana/maku-factory:$TAG" . >/dev/null
docker build -q -f hack/e2e-stub/Dockerfile -t "ghcr.io/smana/maku-e2e-stub:$TAG" . >/dev/null

step "create the k3d cluster $NAME"
! k3d cluster get "$NAME" >/dev/null 2>&1 || die "cluster $NAME already exists; refusing to adopt or delete it"
created=true
k3s_args=(--k3s-arg '--disable=servicelb@server:0')
[[ $EXPOSE == ingress ]] || k3s_args+=(--k3s-arg '--disable=traefik@server:0')
k3d cluster create "$NAME" --image "$K3S_IMAGE" --no-lb --wait "${k3s_args[@]}" \
  --kubeconfig-update-default=false --kubeconfig-switch-context=false >/dev/null
k3d kubeconfig get "$NAME" >"$KUBECONFIG"
# k3d reports success even when the node fails to ingest the tarball: the image counts once containerd lists it.
for image in "${images[@]}"; do
  for _ in 1 2 3; do
    k3d image import -c "$NAME" "ghcr.io/smana/$image:$TAG" >/dev/null
    docker exec "k3d-$NAME-server-0" ctr -n k8s.io images ls -q | grep -qxF "ghcr.io/smana/$image:$TAG" && continue 2
    sleep 2
  done
  die "k3d image import: $image never reached the node"
done

step "a throwaway CA, the stub's certificate, the broker's OIDC ids"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj '/CN=maku-identity-e2e-ca' \
  -addext 'basicConstraints=critical,CA:TRUE' -addext 'keyUsage=critical,keyCertSign,cRLSign' \
  -keyout "$work/ca.key" -out "$work/ca.crt" >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes -subj '/CN=e2e-stub.e2e.svc.cluster.local' \
  -keyout "$work/stub.key" -out "$work/stub.csr" >/dev/null 2>&1
printf 'subjectAltName=DNS:e2e-stub.e2e.svc.cluster.local,IP:127.0.0.1\nextendedKeyUsage=serverAuth\nauthorityKeyIdentifier=keyid,issuer\n' >"$work/stub.ext"
openssl x509 -req -days 1 -in "$work/stub.csr" -CA "$work/ca.crt" -CAkey "$work/ca.key" \
  -CAcreateserial -extfile "$work/stub.ext" -out "$work/stub.crt" >/dev/null 2>&1
k create namespace e2e >/dev/null
k -n e2e create secret generic e2e-stub-tls --from-file=tls.crt="$work/stub.crt" --from-file=tls.key="$work/stub.key" >/dev/null
k create namespace "$NS" >/dev/null
k -n "$NS" create secret generic e2e-stub-ca --from-file=ca.crt="$work/ca.crt" >/dev/null
# slack-app-client-id is the Slack App's sponsored client (SLACK=1): a client of its own, since the
# broker never admits the web or the CLI client as a sponsored principal.
k -n "$NS" create secret generic e2e-broker-oidc --from-literal=web-client-id=rooms-web --from-literal=cli-client-id=maku-cli \
  --from-literal=slack-app-client-id=factory-slack >/dev/null
k -n "$NS" create secret generic e2e-github-link --from-literal=client_id=Iv1.e2e-link --from-literal=client_secret=e2e-throwaway-secret >/dev/null
# The factory App the broker resolves a GitHub id through (D7): a throwaway key; the stub never checks it.
openssl genrsa -out "$work/app.pem" 2048 2>/dev/null
sed "s|ghcr.io/smana/maku-e2e-stub:e2e|ghcr.io/smana/maku-e2e-stub:$TAG|" "$STUB" | k apply -f - >/dev/null
k -n e2e rollout status deploy/e2e-stub --timeout=180s

expose=()
if [[ -n $EXPOSE ]]; then
  step "expose ($EXPOSE): the front door's Secret, the client pod, the gateway"
  k -n "$NS" create secret generic e2e-rooms-proxy --from-literal=client-id=rooms-web --from-literal=client-secret=e2e-throwaway \
    --from-literal=cookie-secret="$(openssl rand -base64 32 | tr -- '+/' '-_')" >/dev/null
  k -n e2e create secret generic e2e-stub-ca --from-file=ca.crt="$work/ca.crt" >/dev/null
  k -n e2e create configmap e2e-client --from-file=charts/agent-factory/tests/expose-probe/client.py >/dev/null
  k apply -f - >/dev/null <<YAML
apiVersion: v1
kind: Pod
metadata: {name: e2e-client, namespace: e2e, labels: {app.kubernetes.io/name: e2e-client}}
spec:
  automountServiceAccountToken: false
  securityContext: {runAsNonRoot: true, runAsUser: 65532, runAsGroup: 65532, fsGroup: 65532, seccompProfile: {type: RuntimeDefault}}
  containers:
    - name: client
      image: $PY_IMAGE
      command: [sleep, infinity]
      securityContext: {allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: {drop: [ALL]}}
      volumeMounts: [{name: app, mountPath: /app}, {name: ca, mountPath: /etc/e2e/ca}]
  volumes:
    - {name: app, configMap: {name: e2e-client}}
    - {name: ca, secret: {secretName: e2e-stub-ca, defaultMode: 0440}}
YAML
  expose=(-f charts/agent-factory/tests/expose-values.yaml --set "expose.mode=$EXPOSE"
    --set "maku.runController.image.tag=$TAG" --set "maku.runController.images.harness.tag=$TAG" --set "maku.runController.images.bridge.tag=$TAG")
  if [[ $EXPOSE == httproute ]]; then
    curl -fsSL "https://github.com/kubernetes-sigs/gateway-api/releases/download/$GATEWAY_API/standard-install.yaml" |
      k apply --server-side -f - >/dev/null
    k apply --server-side -f config/agentgateway >/dev/null
    k wait --for=condition=Established crd --all --timeout=120s >/dev/null 2>&1
    helm --kubeconfig "$KUBECONFIG" --kube-context "k3d-$NAME" install agentgateway charts/agent-factory/vendor/agentgateway \
      -n agentgateway-system --create-namespace --set istio.enabled=false \
      --set controller.image.tag="$(yq '.agentgateway.controller.image.tag' charts/agent-factory/values.yaml)" >/dev/null
    k -n agentgateway-system rollout status deploy/agentgateway --timeout=180s >/dev/null
    k create namespace edge >/dev/null
    k apply -f - >/dev/null <<'YAML'
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata: {name: edge, namespace: edge}
spec:
  gatewayClassName: agentgateway
  listeners:
    - {name: http, protocol: HTTP, port: 8080, allowedRoutes: {namespaces: {from: All}}}
YAML
    expose+=(--set-json 'expose.httproute.parentRefs=[{"name":"edge","namespace":"edge","sectionName":"http"}]'
      --set-json 'expose.networkPolicy.from=[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"edge"}}}]')
    front=http://edge.edge.svc.cluster.local:8080
    if [[ -n $SLACK ]]; then
      # The App's three Secrets (throwaway), its subchart, the factory, both default-deny policies.
      k -n "$NS" create secret generic factory-slack-app --from-literal=signing-secret=e2e-slack-signing \
        --from-literal=bot-token=xoxb-e2e --from-literal=bot-user-id=U0E2EBOT >/dev/null
      # The App signs in as the sponsored principal whose client id the broker's and the factory's
      # sponsored entries read (factory-slack), for the audience human.audience pins (maku-api).
      k -n "$NS" create secret generic factory-slack-principal --from-literal=client-id=factory-slack --from-literal=client-secret=e2e-throwaway >/dev/null
      k -n "$NS" create secret generic agent-factory-sponsored --from-literal=slack-app-client-id=factory-slack >/dev/null
      # The webhook key is one base64 key of at least 32 bytes: the App refuses to start on anything else.
      k -n "$NS" create secret generic room-broker-webhook-slack-app --from-literal=signing-key="$(openssl rand -base64 32)" >/dev/null
      expose+=(-f charts/agent-factory/values-slack.example.yaml --set slack.e2e.unpinnedImages=true --set "slack.image.tag=$TAG"
        --set slack.issuerURL=https://e2e-stub.e2e.svc.cluster.local --set slack.roomsURL=https://rooms.e2e.test --set slack.preset=generic
        --set slack.networkPolicy.mode=kubernetes --set slack.audience=maku-api --set maku.broker.human.audience=maku-api
        --set-json 'slack.extraEnv=[{"name":"SSL_CERT_FILE","value":"/etc/e2e/ca/ca.crt"}]'
        --set-json 'slack.extraVolumes=[{"name":"e2e-ca","secret":{"secretName":"e2e-stub-ca","defaultMode":288}}]'
        --set-json 'slack.extraVolumeMounts=[{"name":"e2e-ca","mountPath":"/etc/e2e/ca","readOnly":true}]'
        --set-json 'slack.networkPolicy.idp.to=[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"e2e"}},"podSelector":{"matchLabels":{"app.kubernetes.io/name":"e2e-stub"}}}]'
        --set slack.networkPolicy.idp.port=8443
        --set-json 'slack.networkPolicy.from=[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"edge"}}}]'
        --set-json 'expose.slack.hostnames=["factory-slack.e2e.test"]'
        --set 'maku.broker.human.mcp.sponsored[0].clientIDFile=/etc/room-broker/oidc/slack-app-client-id'
        --set maku-factory.e2e.unpinnedImages=true --set "maku-factory.image.tag=$TAG"
        --set networkPolicies.plain=false --set networkPolicies.factory.enabled=true)
    fi
  else
    expose+=(--set expose.ingress.className=traefik
      --set-json 'expose.networkPolicy.from=[{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"kube-system"}},"podSelector":{"matchLabels":{"app.kubernetes.io/name":"traefik"}}}]')
    front=http://traefik.kube-system.svc.cluster.local
  fi
fi

h() { helm --kubeconfig "$KUBECONFIG" --kube-context "k3d-$NAME" "$@"; }
helm dependency build charts/agent-factory >/dev/null
h install identity-crds charts/agent-factory-crds -n "$NS" --timeout 2m >/dev/null
if [[ $PROFILE == production ]]; then
  PROD_VALUES=charts/agent-factory/tests/production-k3d-values.yaml
  step "production: the sandbox's RuntimeClass, the prometheus-operator CRDs, the broker's TLS, the factory's Secrets"
  # Test-only: a RuntimeClass named as production names its sandbox, on runc. No run starts here.
  k apply -f - >/dev/null <<'YAML'
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata: {name: gvisor}
handler: runc
YAML
  curl -fsSL "https://github.com/prometheus-operator/prometheus-operator/releases/download/$PROMETHEUS_OPERATOR/stripped-down-crds.yaml" |
    k apply --server-side -f - >/dev/null
  # Just after a server-side apply, `kubectl wait` can fail before the CRDs are served (an accessor
  # error): retried, as install.sh's wait_established.
  for _ in 1 2 3 4 5; do
    k wait --for=condition=Established crd/servicemonitors.monitoring.coreos.com crd/podmonitors.monitoring.coreos.com \
      crd/prometheusrules.monitoring.coreos.com --timeout=120s >/dev/null 2>&1 && break
    sleep 3
  done
  k wait --for=condition=Established crd/servicemonitors.monitoring.coreos.com crd/podmonitors.monitoring.coreos.com \
    crd/prometheusrules.monitoring.coreos.com --timeout=120s >/dev/null
  k create namespace monitoring >/dev/null
  # The runs namespace, the platform's (maku.createRunsNamespace false): the factory alone writes its Role there.
  k create namespace agents >/dev/null
  k label namespace agents pod-security.kubernetes.io/enforce=restricted >/dev/null
  # The broker's serving certificate, from the run's throwaway CA (production brings its own, or cert-manager's).
  openssl req -newkey rsa:2048 -nodes -subj '/CN=room-broker' -keyout "$work/broker.key" -out "$work/broker.csr" >/dev/null 2>&1
  printf 'subjectAltName=DNS:room-broker.%s.svc,DNS:room-broker.%s.svc.cluster.local\nextendedKeyUsage=serverAuth\n' "$NS" "$NS" >"$work/broker.ext"
  openssl x509 -req -days 1 -in "$work/broker.csr" -CA "$work/ca.crt" -CAkey "$work/ca.key" -CAcreateserial \
    -extfile "$work/broker.ext" -out "$work/broker.crt" >/dev/null 2>&1
  k -n "$NS" create secret tls room-broker-tls --cert="$work/broker.crt" --key="$work/broker.key" >/dev/null
  for s in room-broker-ca openbao-ca; do k -n "$NS" create secret generic "$s" --from-file=ca.crt="$work/ca.crt" >/dev/null; done
  k -n "$NS" create secret generic agent-factory-github --from-literal=app_id=1 --from-file=private_key="$work/app.pem" >/dev/null
  # The agents App (D12): a second id, as the factory refuses an agents App that is its own.
  k -n "$NS" create secret generic agent-factory-agents --from-literal=app_id=2 --from-file=private_key="$work/app.pem" >/dev/null
  k -n "$NS" create secret generic agent-factory-oidc --from-literal=web-client-id=rooms-web --from-literal=cli-client-id=maku-cli >/dev/null

  step "production: a plain postgres:16 outside the chart, bootstrapped with docs/database.md"
  k create namespace external-db >/dev/null
  k -n external-db apply -f - >/dev/null <<YAML
apiVersion: apps/v1
kind: Deployment
metadata: {name: pg}
spec:
  replicas: 1
  selector: {matchLabels: {app.kubernetes.io/name: pg}}
  template:
    metadata: {labels: {app.kubernetes.io/name: pg}}
    spec:
      automountServiceAccountToken: false
      securityContext: {runAsNonRoot: true, runAsUser: 70, runAsGroup: 70, fsGroup: 70, seccompProfile: {type: RuntimeDefault}}
      containers:
        - name: postgres
          image: $PG_IMAGE
          env:
            - {name: POSTGRES_PASSWORD, value: e2e-superuser-only}
            - {name: PGDATA, value: /var/lib/postgresql/data/pgdata}
          ports: [{name: postgres, containerPort: 5432}]
          readinessProbe: {exec: {command: [pg_isready, -h, 127.0.0.1, -U, postgres]}, periodSeconds: 3}
          resources: {requests: {cpu: 100m, memory: 128Mi}, limits: {cpu: "1", memory: 512Mi}}
          securityContext: {allowPrivilegeEscalation: false, capabilities: {drop: [ALL]}}
          volumeMounts: [{name: data, mountPath: /var/lib/postgresql/data}, {name: run, mountPath: /var/run/postgresql}]
      volumes: [{name: data, emptyDir: {}}, {name: run, emptyDir: {}}]
---
apiVersion: v1
kind: Service
metadata: {name: pg}
spec:
  selector: {app.kubernetes.io/name: pg}
  ports: [{name: postgres, port: 5432, targetPort: postgres}]
YAML
  k -n external-db rollout status deploy/pg --timeout=180s >/dev/null
  awk '/^```sql$/ {on=1; next} on && /^```$/ {exit} on' docs/database.md >"$work/bootstrap.sql"
  [[ $(grep -c '^CREATE ROLE' "$work/bootstrap.sql") == 3 ]] || die "docs/database.md's first sql block is not the bootstrap"
  k -n external-db exec -i deploy/pg -- psql -q -v ON_ERROR_STOP=1 -U postgres -d postgres <"$work/bootstrap.sql" >/dev/null
  for r in owner broker retention; do
    pw=$(sed -nE "s/^CREATE ROLE rooms_$r LOGIN PASSWORD '([^']+)';$/\1/p" "$work/bootstrap.sql")
    [[ -n $pw ]] || die "no password for rooms_$r in the bootstrap"
    k -n "$NS" create secret generic "rooms-$r-dsn" \
      --from-literal=uri="postgresql://rooms_$r:$pw@pg.external-db.svc:5432/rooms?sslmode=disable" >/dev/null
  done

  step "production: maku-factory alone, then the umbrella with maku.enabled false (docs/upgrading.md)"
  factory=(--set e2e.unpinnedImages=true --set "image.tag=$TAG")
  h install identity charts/maku-factory -n "$NS" --skip-crds -f <(yq '.maku-factory' "$PROD_VALUES") "${factory[@]}" --timeout 5m >/dev/null
  k -n "$NS" rollout status deploy/agent-factory --timeout=180s
  uid="$(k -n "$NS" get deploy agent-factory -o jsonpath='{.metadata.uid}')"
  h upgrade identity charts/agent-factory -n "$NS" --set profile=production -f <(yq '{"maku-factory": .maku-factory}' "$PROD_VALUES") \
    --set maku-factory.e2e.unpinnedImages=true --set "maku-factory.image.tag=$TAG" --timeout 5m >/dev/null
  k -n "$NS" rollout status deploy/agent-factory --timeout=180s
  [[ $(k -n "$NS" get deploy agent-factory -o jsonpath='{.metadata.uid}') == "$uid" ]] || die "the upgrade to the umbrella replaced the factory's Deployment"
  [[ $(h history identity -n "$NS" -o json | jq -r 'map(.chart) | join(" ")') == "maku-factory-0.0.0 agent-factory-0.0.0" ]] ||
    die "the release's history is not maku-factory then agent-factory: $(h history identity -n "$NS")"
  echo "ok   maku-factory alone upgraded to the umbrella (maku.enabled false): the same release, the factory's Deployment UID kept"

  step "production: maku and the rest on, profile production"
  h upgrade identity charts/agent-factory -n "$NS" -f "$VALUES" -f "$LINK_VALUES" "${expose[@]}" -f "$PROD_VALUES" \
    --set maku.e2e.unpinnedImages=true --set "maku.broker.image.tag=$TAG" \
    --set maku-factory.e2e.unpinnedImages=true --set "maku-factory.image.tag=$TAG" --timeout 5m >/dev/null
  [[ $(k -n "$NS" get deploy agent-factory -o jsonpath='{.metadata.uid}') == "$uid" ]] || die "turning maku on replaced the factory's Deployment"
  ! k -n "$NS" get sts rooms-db >/dev/null 2>&1 || die "profile production installed the bundled rooms-db"
else
  step "install the umbrella: the broker in preset generic, its ids by Secret"
  h install identity charts/agent-factory -n "$NS" \
    -f charts/agent-factory/values-demo.yaml -f charts/agent-factory/tests/values-demo-secret.yaml -f "$VALUES" -f "$LINK_VALUES" "${expose[@]}" \
    --set maku.e2e.unpinnedImages=true --set "maku.broker.image.tag=$TAG" \
    --set maku.runController.enabled="$([[ -n $EXPOSE ]] && echo true || echo false)" --set maku-factory.enabled="$([[ -n $SLACK ]] && echo true || echo false)" \
    --set demo.model.baseURL=http://model.e2e.test/v1 --set demo.model.name=m --set demo.model.apiKey=e2e-throwaway \
    --set demo.githubApp.id=1 --set-file demo.githubApp.privateKey="$work/app.pem" --timeout 5m >/dev/null
  k -n "$NS" rollout status sts/rooms-db --timeout=180s
fi
k -n "$NS" rollout status deploy/room-broker --timeout=180s

forward() { # forward NS RESOURCE REMOTE VAR: a port-forward, its local port in VAR
  local log="$work/pf-${2//\//-}.log" port=""
  k -n "$1" port-forward --address=127.0.0.1 "$2" ":$3" >"$log" 2>&1 &
  pf_pids+=("$!")
  for _ in $(seq 1 60); do
    port="$(sed -n 's/^Forwarding from 127.0.0.1:\([0-9]*\).*/\1/p' "$log" | head -1)"
    if [[ -n $port ]]; then printf -v "$4" '%s' "$port"; return; fi
    sleep 1
  done
  die "port-forward to $2 never started: $(cat "$log")"
}
stub='' broker=''
forward e2e svc/e2e-stub 443 stub
forward "$NS" deploy/room-broker 8080 broker

# id_token SUB AUD GROUPS: the stub's ID token, over its own TLS.
id_token() {
  curl -fsS --cacert "$work/ca.crt" --resolve "e2e-stub.e2e.svc.cluster.local:$stub:127.0.0.1" \
    "https://e2e-stub.e2e.svc.cluster.local:$stub/test/id-token?sub=$1&aud=$2&groups=$3" | jq -r .id_token
}
# rooms METHOD BEARER [ACCESS] [BODY]: the status and body of the broker's /api/rooms, as oauth2-proxy calls it.
rooms() {
  local args=(-sS -o "$work/body" -w '%{http_code}' -X "$1" -H "Authorization: Bearer $2")
  [[ -n ${3:-} ]] && args+=(-H "X-Forwarded-Access-Token: $3")
  [[ -n ${4:-} ]] && args+=(-H 'Content-Type: application/json' --data "$4")
  curl "${args[@]}" "http://127.0.0.1:$broker/api/rooms"
}

step "the broker discovers the stub's keys and admits a generic ID token"
admin_web="$(id_token admin rooms-web agents-admin)"
code="$(rooms POST "$admin_web" opaque-access-token '{"dataClass":"public","repository":"owner/repo"}')"
[[ $code == 201 ]] || die "an admin's web session creating a room: $code $(cat "$work/body")"
member_web="$(id_token alice rooms-web agents-member)"
code="$(rooms GET "$member_web" opaque-access-token)"
[[ $code == 200 ]] || die "a member's web session: $code $(cat "$work/body")"
echo "ok   a member's web session lists the rooms (200)"
code="$(rooms GET "$(id_token mallory rooms-web everyone)" opaque-access-token)"
[[ $code == 403 ]] || die "a token in no agents group: $code, want 403"
echo "ok   a token in no agents group is refused (403)"
code="$(rooms GET "$member_web")"
[[ $code == 401 ]] || die "the web client's ID token alone: $code, want 401"
echo "ok   the web client's ID token without its access token is refused (401)"

step "P18: the CLI client's ID token reads, and is never the web UI"
code="$(rooms GET "$admin_web" opaque-access-token)"
[[ $code == 200 && $(jq -r '[.[] | .you.webUI] | all' "$work/body") == true && $(jq length "$work/body") -ge 1 ]] ||
  die "the admin's web session: $code $(cat "$work/body")"
cli="$(id_token admin maku-cli agents-admin)"
code="$(rooms GET "$cli" "$cli")"
[[ $code == 200 && $(jq length "$work/body") -ge 1 ]] || die "the CLI bearer reading: $code $(cat "$work/body")"
[[ $(jq -r '[.[] | .you.webUI] | any' "$work/body") == false ]] || die "the CLI bearer passed as the web UI: $(cat "$work/body")"
echo "ok   the CLI client's ID token reads the room, and is not the web UI, which alone may decide"

step "the GitHub account link: a member links GitHub once, through the stub's OAuth flow"
origin=https://rooms.e2e.test
# link METHOD PATH BEARER [COOKIE] [FORM]: the broker's /link/github routes, as the web UI's session
# (oauth2-proxy's two headers, the broker's own Origin); headers to $work/headers, body to $work/body.
link() {
  local args=(-sS -o "$work/body" -D "$work/headers" -w '%{http_code}' -X "$1" -H "Origin: $origin")
  [[ -n ${3:-} ]] && args+=(-H "Authorization: Bearer $3" -H 'X-Forwarded-Access-Token: opaque-access-token')
  [[ -n ${4:-} ]] && args+=(-H "Cookie: __Host-maku-ghlink=$4")
  [[ -n ${5:-} ]] && args+=(-H 'Content-Type: application/x-www-form-urlencoded' --data "$5")
  curl "${args[@]}" "http://127.0.0.1:$broker$2"
}
page_has() { [[ $code == 200 ]] && grep -qF -- "$1" "$work/body"; } # the last answer is 200 and holds $1
header() { sed -n "s/^$1: //Ip" "$work/headers" | tr -d '\r' | head -1; }
access() { # access BEARER: the X-Rooms-Access of the member's room list, and its row count
  curl -sS -o "$work/body" -D "$work/headers" -H "Authorization: Bearer $1" -H 'X-Forwarded-Access-Token: opaque-access-token' \
    "http://127.0.0.1:$broker/api/rooms" >/dev/null
  echo "$(header X-Rooms-Access) $(jq length "$work/body")"
}
[[ "$(access "$member_web")" == "unlinked 0" ]] || die "a member with no link: $(access "$member_web"), want 'unlinked 0'"
echo "ok   a member with no GitHub link sees no room (X-Rooms-Access: unlinked)"
code="$(link GET /link/github "$member_web")"
page_has 'None yet.' || die "the link page: $code $(cat "$work/body")"
code="$(link POST /link/github/start "$member_web" '' 'x=1')"
location="$(header Location)"
nonce="$(header Set-Cookie | sed -n 's/^__Host-maku-ghlink=\([^;]*\);.*/\1/p')"
[[ $code == 303 && $location == https://e2e-stub.e2e.svc.cluster.local/login/oauth/authorize\?* && -n $nonce ]] ||
  die "the start: $code '$location' '$(header Set-Cookie)'"
# The browser at GitHub: the stub authorizes at once and redirects back with a code.
back="$(curl -sS -o /dev/null -w '%{redirect_url}' --cacert "$work/ca.crt" \
  --resolve "e2e-stub.e2e.svc.cluster.local:$stub:127.0.0.1" "${location/e2e-stub.e2e.svc.cluster.local/e2e-stub.e2e.svc.cluster.local:$stub}")"
[[ $back == "$origin/link/github/callback?"* ]] || die "the stub's authorize redirects to '$back'"
callback="${back#"$origin"}"
state="$(sed -n 's/.*[?&]state=\([^&]*\).*/\1/p' <<<"$callback")"
code="$(link GET "$callback" '' "$nonce")" # no session: the front door lets the callback through
page_has 'GitHub says you are <strong>@dev</strong> (id 123)' ||
  die "the callback: $code $(cat "$work/body")"
code="$(link POST /link/github/confirm "$member_web" "$nonce" "state=$state")"
page_has 'Linked: rooms and the factory now know you as @dev.' || die "the confirm: $code $(cat "$work/body")"
code="$(link GET /link/github "$member_web")"
page_has 'Linked to @dev (id 123)' || die "the link page after: $code $(cat "$work/body")"
echo "ok   start, GitHub's redirect and the confirm link the member to @dev; /link/github shows it"
counts="$(curl -fsS --cacert "$work/ca.crt" --resolve "e2e-stub.e2e.svc.cluster.local:$stub:127.0.0.1" \
  "https://e2e-stub.e2e.svc.cluster.local:$stub/__oauth")"
[[ $(jq -r '.revoked, .liveTokens, .zitadel' <<<"$counts" | paste -sd' ') == "1 0 0" ]] || die "the stub's OAuth counts: $counts"
echo "ok   the user token was revoked at once, and no ZITADEL endpoint was called"
[[ "$(access "$member_web")" == "ok 1" ]] || die "the linked member: $(access "$member_web"), want 'ok 1'"
echo "ok   the member reads the room through the link (X-Rooms-Access: ok)"

step "an admin unlinks the member"
code="$(link DELETE /api/links/github/alice "$admin_web")"
[[ $code == 200 && $(jq -r .unlinked "$work/body") == true ]] || die "the admin's unlink: $code $(cat "$work/body")"
[[ "$(access "$member_web")" == "unlinked 0" ]] || die "after the unlink: $(access "$member_web"), want 'unlinked 0'"
echo "ok   after an admin's unlink the member is unlinked again and sees no room"

if [[ -n $EXPOSE ]]; then
  step "expose ($EXPOSE): the web through $front and the web front door"
  k -n "$NS" rollout status deploy/rooms-web-proxy --timeout=180s
  k -n e2e wait --for=condition=Ready pod/e2e-client --timeout=180s >/dev/null
  if [[ $EXPOSE == httproute ]]; then
    k -n edge wait --for=condition=Programmed gateway/edge --timeout=180s >/dev/null
    k -n edge rollout status deploy/edge --timeout=180s >/dev/null
  else
    for _ in $(seq 100); do k -n kube-system get deploy/traefik >/dev/null 2>&1 && break; sleep 3; done
    k -n kube-system rollout status deploy/traefik --timeout=300s >/dev/null
  fi
  web=http://rooms.e2e.test
  # c ARGS...: the client's JSON answer (tests/expose-probe/client.py), through the front
  c() { k -n e2e exec e2e-client -- python3 /app/client.py --cacert /etc/e2e/ca/ca.crt --via "$front" "$@"; }
  status() { jq -r .status <<<"$1"; }
  out=''
  for _ in $(seq 40); do out="$(c http GET "$web/")"; [[ $(status "$out") == 302 ]] && break; sleep 3; done
  [[ $(status "$out") == 302 && $(jq -r .location <<<"$out") == https://e2e-stub.e2e.svc.cluster.local/oauth/v2/authorize?* ]] ||
    die "GET / without a session does not redirect to the stub issuer: $out"
  echo "ok   GET / without a session redirects to the issuer"
  login="$(c login "$web/oauth2/start?rd=/")"
  cookie="$(jq -r .cookie <<<"$login")"
  [[ -n $cookie ]] || die "the code flow did not sign in: $login"
  echo "ok   the code flow signs in: $(jq -r '.hops | join(" -> ")' <<<"$login")"
  out="$(c http GET "$web/api/rooms" -H "Cookie: $cookie" -H 'X-Forwarded-Access-Token: forged')"
  [[ $(status "$out") == 200 && $(jq '.body | length' <<<"$out") -ge 1 && $(jq -r '[.body[] | .you.webUI] | all' <<<"$out") == true ]] ||
    die "the session through the front door: $out"
  echo "ok   the session lists the rooms as the web UI (a client's X-Forwarded-Access-Token dropped)"
  room="$(jq -r '.body[0].id' <<<"$out")"
  out="$(c http GET "$web/.well-known/roomctl")"
  [[ $(status "$out") == 200 && $(jq -r '.body | type' <<<"$out") == object ]] || die "GET /.well-known/roomctl without a session: $out"
  echo "ok   GET /.well-known/roomctl answers without a session"
  if [[ -n $(yq '.maku.broker.githubLink.existingSecret // ""' "$LINK_VALUES") ]]; then
    out="$(c http GET "$web/link/github/callback?code=x&state=y")"
    [[ $(status "$out") != 302 && $(status "$out") -lt 500 ]] || die "GET /link/github/callback did not reach the broker: $out"
    echo "ok   GET /link/github/callback reaches the broker without a session ($(status "$out"), the broker's own refusal)"
    for probe in "POST /link/github/callback" "GET /link/github"; do
      out="$(c http "${probe%% *}" "$web${probe#* }")"
      [[ $(status "$out") == 302 || $(status "$out") == 401 || $(status "$out") == 403 ]] || die "$probe without a session: $out"
    done
    echo "ok   POST /link/github/callback and GET /link/github still need a session"
  fi
  # The security review of #545: a client's X-Forwarded-Uri must never select a skip-auth route.
  out="$(c http GET "$web/api/rooms" -H 'X-Forwarded-Uri: /.well-known/roomctl')"
  [[ $(status "$out") == 302 && $(jq -r .location <<<"$out") == https://e2e-stub.e2e.svc.cluster.local/oauth/v2/authorize?* ]] ||
    die "GET /api/rooms with X-Forwarded-Uri: /.well-known/roomctl skipped sign-in: $out"
  echo "ok   GET /api/rooms with a forged X-Forwarded-Uri still redirects to sign in"
  cli="$(id_token admin maku-cli agents-admin)"
  out="$(c http GET "$web/api/rooms" -H "Authorization: Bearer $cli")"
  [[ $(status "$out") == 200 && $(jq '.body | length' <<<"$out") -ge 1 ]] || die "a CLI bearer through the front door: $out"
  echo "ok   a CLI bearer reads /api/rooms through the front door"
  out="$(c http GET "$web/api/rooms" -H "Authorization: Bearer $cli" -H "X-Forwarded-Access-Token: forged-$RANDOM")"
  [[ $(status "$out") == 200 && $(jq -r '[.body[] | .you.webUI] | any' <<<"$out") == false ]] ||
    die "a CLI bearer with a forged X-Forwarded-Access-Token passed as the web UI: $out"
  echo "ok   the same bearer with a forged X-Forwarded-Access-Token is still not the web UI, so it cannot decide (P18)"
  out="$(k -n e2e exec e2e-client -- python3 /app/client.py http GET http://room-broker.maku.svc.cluster.local:8080/api/rooms \
    -H "Authorization: Bearer $cli" 2>&1 || true)"
  [[ $(jq -r .status <<<"$out" 2>/dev/null) != 200 ]] || die "a pod that is not the front door reached the broker's :8080: $out"
  echo "ok   the broker's :8080 refuses a pod that is not the front door"
  out="$(c ws "ws://rooms.e2e.test/v1/ws?room=$room" "$EXPOSE_WS_SECONDS" -H "Cookie: $cookie" -H 'Origin: https://rooms.e2e.test' \
    --hello "{\"type\":\"hello\",\"roomId\":\"$room\"}")"
  [[ $(jq -r .ok <<<"$out") == true ]] || die "/v1/ws through the front door: $out"
  echo "ok   /v1/ws stays open ${EXPOSE_WS_SECONDS}s through $EXPOSE and the front door ($(jq -r .pings <<<"$out") pings)"
fi


if [[ -n $SLACK ]]; then
  step "the Slack App through the Gateway, and both default-deny policies"
  k -n "$NS" rollout status deploy/factory-slack-app --timeout=180s
  # slack_post SIGN BODY: POST /slack/events through the Gateway, signed with SIGN (Slack's v0 scheme).
  slack_post() {
    k -n e2e exec e2e-client -- python3 -c '
import hashlib, hmac, http.client, json, sys, time
secret, body = sys.argv[1].encode(), sys.argv[2].encode()
ts = str(int(time.time()))
sig = "v0=" + hmac.new(secret, b"v0:" + ts.encode() + b":" + body, hashlib.sha256).hexdigest()
c = http.client.HTTPConnection("edge.edge.svc.cluster.local", 8080, timeout=30)
c.request("POST", "/slack/events", body, {"Host": "factory-slack.e2e.test", "Content-Type": "application/json",
  "X-Slack-Request-Timestamp": ts, "X-Slack-Signature": sig})
r = c.getresponse(); print(r.status, r.read().decode()[:200])' "$1" "$2"
  }
  out=''
  for _ in $(seq 40); do out="$(slack_post e2e-slack-signing '{"type":"url_verification","challenge":"e2e-challenge"}')"; [[ $out == 200* ]] && break; sleep 3; done
  [[ $out == 200* && $out == *e2e-challenge* ]] || die "a signed url_verification through the Gateway: $out"
  echo "ok   a Slack-signed POST /slack/events reaches the App through the Gateway (its challenge back)"
  out="$(slack_post wrong-secret '{"type":"url_verification","challenge":"x"}')"
  [[ $out == 401* ]] || die "a badly signed event: $out, want 401"
  echo "ok   a badly signed event is 401"
  # A mention: the App signs in with its client credentials and calls the broker's /mcp (before any
  # Slack post, which has no Slack to reach here).
  mention='{"type":"event_callback","team_id":"T0123ABCD","event_id":"Ev0E2E1","event_time":1,"event":{"type":"app_mention","channel":"C0123ABCD","user":"U0E2EUSER","text":"<@U0E2EBOT> fix the docs typo","ts":"1700000000.000100"}}'
  out="$(slack_post e2e-slack-signing "$mention")"
  [[ $out == 200* ]] || die "a signed app_mention: $out"
  grants='' calls=''
  metrics_port=''
  forward "$NS" deploy/room-broker 9090 metrics_port
  for _ in $(seq 40); do
    grants="$(curl -fsS --cacert "$work/ca.crt" --resolve "e2e-stub.e2e.svc.cluster.local:$stub:127.0.0.1" "https://e2e-stub.e2e.svc.cluster.local:$stub/__grants" | jq -r 'map(select(. == "factory-slack")) | length')"
    calls="$(curl -fsS "http://127.0.0.1:$metrics_port/metrics" | grep -E '^maku_rooms_human_mcp_calls_total\{' || true)"
    [[ $grants -ge 1 && -n $calls ]] && break
    sleep 3
  done
  [[ $grants -ge 1 ]] || die "the App never signed in with its client credentials (stub grants: $grants)"
  [[ -n $calls ]] || { k -n "$NS" logs deploy/factory-slack-app --tail=40 >&2 || true; die "the broker counted no /mcp tool call after the mention"; }
  echo "ok   a mention makes the App sign in with its client credentials and call the broker's /mcp ($(head -1 <<<"$calls"))"
  out="$(c http GET http://factory-slack.e2e.test/slack/events)"
  [[ $(status "$out") == 404 ]] || die "GET /slack/events: $out, want the gateway's 404"
  echo "ok   GET /slack/events is the gateway's 404"
  # stand-ins under each policy: a pod labelled as the broker (the broker's egress, the App's ingress),
  # a control under no policy (the stub admits it), and one labelled as the factory (the factory's egress).
  for p in broker-standin:room-broker plain:e2e-control factory-standin:agent-factory; do
    k -n "$NS" run "${p%%:*}" --image="$PY_IMAGE" --restart=Never --labels="app.kubernetes.io/name=${p#*:}" \
      --overrides='{"spec":{"automountServiceAccountToken":false,"securityContext":{"runAsNonRoot":true,"runAsUser":65532,"seccompProfile":{"type":"RuntimeDefault"}},"containers":[{"name":"c","image":"'"$PY_IMAGE"'","command":["sleep","infinity"],"securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]}}}]}}' >/dev/null
  done
  for p in broker-standin plain factory-standin; do k -n "$NS" wait --for=condition=Ready "pod/$p" --timeout=180s >/dev/null; done
  # reach POD HOST PORT: "open" or "closed" (a TCP connect within 5 s)
  reach() { k -n "$NS" exec "$1" -- python3 -c 'import socket,sys
try: socket.create_connection((sys.argv[1], int(sys.argv[2])), timeout=5); print("open")
except OSError: print("closed")' "$2" "$3"; }
  app=factory-slack-app.$NS.svc.cluster.local
  [[ $(reach broker-standin "$app" 8081) == open ]] || die "the broker's pods cannot reach the App's :8081"
  [[ $(reach plain "$app" 8081) == closed ]] || die "a pod that is not the broker reached the App's :8081"
  echo "ok   the App's :8081 admits the broker's pods, and no other"
  [[ $(reach factory-standin e2e-stub.e2e.svc.cluster.local 443) == closed ]] || die "a pod under the factory's policy reached a private address outside its peers"
  [[ $(reach plain e2e-stub.e2e.svc.cluster.local 443) == open ]] || die "the control pod cannot reach the stub: the check above proves nothing"
  [[ $(k -n "$NS" exec factory-standin -- python3 -c 'import socket; print(socket.gethostbyname("kubernetes.default.svc.cluster.local"))') =~ ^[0-9.]+$ ]] ||
    die "a pod under the factory's policy cannot resolve names"
  echo "ok   the factory's policy refuses a private address outside its peers, and allows DNS"
fi

if [[ $PROFILE == production ]]; then
  step "production: the factory serves, and the monitoring objects exist"
  k -n "$NS" rollout status deploy/agent-factory --timeout=180s >/dev/null
  ops=''
  forward "$NS" deploy/agent-factory 9090 ops
  code="$(curl -sS -o "$work/body" -w '%{http_code}' "http://127.0.0.1:$ops/readyz")"
  [[ $code == 200 ]] || die "the factory's /readyz: $code $(cat "$work/body")"
  echo "ok   the factory's ops /readyz is 200"
  k -n "$NS" get prometheusrule maku >/dev/null || die "no PrometheusRule maku"
  sms="$(k -n "$NS" get servicemonitors -o name | sort | paste -sd' ')"
  [[ $sms == *room-broker* && $sms == *agent-factory* ]] || die "the ServiceMonitors are '$sms', want the broker's and the factory's"
  echo "ok   the PrometheusRule maku and the ServiceMonitors ($sms) exist"
  unsafe="$(k -n "$NS" get configmap room-broker-config -o jsonpath='{.data.config\.yaml}' | yq '.install.unsafe // [] | join(",")')"
  [[ -z $unsafe ]] || die "profile production shows unsafe settings: $unsafe"
  echo "ok   profile production shows no unsafe setting"
fi

echo "identity k3d OK${EXPOSE:+ (expose $EXPOSE)}${SLACK:+ (slack)}$([[ $PROFILE == production ]] && echo " (production)")"

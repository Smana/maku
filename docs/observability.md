# Observability

Every component serves `/metrics`; the umbrella chart (`charts/agent-factory`) can also render the
objects a Prometheus stack needs to scrape, alert on and chart them. Each switch is off by default, and none
guesses at the cluster: with one on, the prometheus-operator CRDs (`monitoring.coreos.com/v1`) must
exist. The VictoriaMetrics operator converts `ServiceMonitor`, `PodMonitor` and `PrometheusRule` to its
own kinds.

## Metrics

| Component | Port | Scraped by (`monitoring.serviceMonitor.enabled`) | Job |
|---|---|---|---|
| Broker | `room-broker:metrics` (9090) | `ServiceMonitor` `room-broker`, with `maku.enabled` | `room-broker` |
| Factory | `agent-factory:metrics` (9090) | `ServiceMonitor` `agent-factory`, with `maku-factory.enabled` | `agent-factory` |
| Run-controller | pod port `metrics` (9090) | `PodMonitor` `maku-run-controller`, with `maku.runController.enabled` | `run-controller` |
| Egress proxy | pod port `metrics` (9090) | `PodMonitor` `maku-egress-proxy`, in `maku.networkPolicies.mode kubernetes` | `egress-proxy` |
| Model-gateway proxies | pod port `metrics` (15020) | `PodMonitor` `maku-gateway`, with `maku-gateway.enabled` | `maku-gateway.name` |
| agentgateway controller | its chart's | `agentgateway.monitoring.enabled` (set `agentgateway.monitoring.proxy.podMonitor.enabled: false`, or the proxies are scraped twice) | its chart's |

```yaml
monitoring:
  serviceMonitor:
    enabled: true
    interval: 30s
    labels: {release: kube-prometheus-stack}   # whatever your Prometheus's selector wants
```

The run-controller and the egress proxy have no metrics Service, so `PodMonitor`s scrape them and a
default render gains no Service. Each object sets the `job` label the alerts read.

A target behind a default-deny policy must admit the scraper, or its scrape reads only `up == 0` and
its `Down` alert fires for ever. So the render fails, naming the key, while the scraper is not admitted:

| Policy | The scraper is admitted by |
|---|---|
| `maku.networkPolicies.mode kubernetes` | `maku.networkPolicies.kubernetes.metricsFrom` (NetworkPolicy peers), on the broker, the run-controller and the egress proxy |
| `maku.networkPolicies.mode cilium` (maku's default) | `monitoring.serviceMonitor.cilium.fromEndpoints` (Cilium endpoint selectors), in the umbrella's CNP `maku-metrics` on the broker and the run-controller, rendered only with monitoring on |
| `maku-gateway.networkPolicy.mode` other than `none` | `maku-gateway.metrics.allowFrom`, on the proxies' `:15020` |

```yaml
monitoring:
  serviceMonitor:
    enabled: true
    cilium:
      fromEndpoints:
        - matchLabels: {io.kubernetes.pod.namespace: monitoring, app.kubernetes.io/name: prometheus}
```

The factory has no default-deny policy of maku's, so it needs no entry.

The metric families are listed in [Operations](operations.md#metrics) and
[Factory metrics](operations.md#factory-metrics).

## Alerts

`monitoring.prometheusRule.enabled` renders one `PrometheusRule`, `maku`: a group per enabled
component, every rule in PromQL with `runbook_url` and `dashboard` annotations. The release
namespace, `maku.runs.namespace`, `maku.broker.human.issuer` and `monitoring.prometheusRule.docsURL`
are substituted into it, so the schema refuses a double quote or a backslash in them.
[Operations](operations.md#alerts) lists each alert and the values that shape the rule.

## Dashboards

`monitoring.dashboards.enabled` renders one ConfigMap per dashboard, `maku-dashboard-<uid>`, holding
`<uid>.json`. Each carries `monitoring.dashboards.labels` (empty by default, which renders `grafana_dashboard: "1"`, the label
Grafana's dashboard sidecar watches) and `monitoring.dashboards.annotations`, such as
`grafana_folder: Agents`.

```yaml
monitoring:
  dashboards:
    enabled: true
    annotations: {grafana_folder: Agents}
```

| uid | Title | Rendered with |
|---|---|---|
| `agents-overview` | The fleet at a glance: health, spend, runs in flight, pull requests, firing alerts | always |
| `agent-factory` | Tasks, pull request outcomes, budgets, merge decisions, the ledger against the gateway | always |
| `agent-room` | One room (`var-room`): its runs, spend, approvals, events | always |
| `agent-run` | One run (`var-run`): phase, duration, models, tokens and cost, the ledger against the gateway | always |
| `agent-identity` | Spend per identity (`var-principal`): one principal at a time, never ranked | always |
| `agent-fleet` | Every run: phases, tokens, spend by model and principal | always |
| `agent-gateway` | The model gateway: requests, errors, latency, tokens, MCP calls | `maku-gateway.enabled` |

**grafana-operator.** Point a `GrafanaDashboard` at each ConfigMap:

```yaml
apiVersion: grafana.integreatly.org/v1beta1
kind: GrafanaDashboard
metadata: {name: agent-run, namespace: maku}
spec:
  instanceSelector: {matchLabels: {dashboards: grafana}}
  folder: Agents
  configMapRef: {name: maku-dashboard-agent-run, key: agent-run.json}
```

**Links.** The uids are cloud-native-ref's, so the dashboards' links to each other, and the room
page's [`human.dashboards`](integration.md#the-brokers-configuration) links, resolve by uid, for
example `https://grafana.example.com/d/agent-room/rooms?var-room={room}`. A room's links back to the
room page use `maku.broker.publicURL`.

**The datasource** is the `${datasource}` variable, of type `prometheus`. Every query is PromQL:
`tests/promql-only.sh` refuses MetricsQL-only functions in the chart's files, and
`tests/monitoring/dashboards.sh` runs every query through promtool.

**What the panels read.**

- **The broker's and the factory's metrics**, scraped as [Metrics](#metrics) says.
- **The model gateway's own series**, `agentgateway_*`, on `job="<maku-gateway.name>"` in the release
  namespace. A run's cost is `agentgateway_gen_ai_client_cost_usd_total`, priced by the gateway's
  catalog. A run's gateway series is born at its first call, already holding it, so a panel reads
  it with `max_over_time` over the range, or adds the series' first window to a per-interval
  `increase()`. `increase()` alone would drop that first call.
- **kube-state-metrics.** The run panels read `agentrun_info`, `agentrun_status_phase` and the other
  `agentrun_*` series, which kube-state-metrics exports from AgentRun objects only with a
  custom-resource configuration. The chart does not ship that configuration. Without it, those panels
  read no data and the rest still work. `kube_pod_status_phase` is kube-state-metrics' own.

**Left in cloud-native-ref.** The VictoriaLogs and VictoriaTraces panels (a run's steps, its errors,
its traces and the gateway's access log), the Karpenter pool panel, the trace collector's pipeline,
the Envoy gateway's cross-check, the rate-limit budgets and the prompt-cache recording rule all read
what cloud-native-ref runs, so they stay in one of its own dashboards.

## Traces

Two components export spans over OTLP. Each is off until you give it an endpoint:

| Emitter | Span | Endpoint | Protocol |
|---|---|---|---|
| The factory | One root span per task, from its acceptance to its end, exported when the task ends. It carries ids, the tier, the phase and a reason code, never issue text | `maku-factory.config.tracing.otlpEndpoint`, as `host:port` | OTLP/gRPC |
| Each run's harness | The run's root span, `agent-run`, a child of its task's span. It carries no attributes, events or links: its ids and its timing only | `maku.runController.tracing.otlpEndpoint`, as an `http(s)://` base URL | OTLP/HTTP, to `<endpoint>/v1/traces` |

One trace therefore holds a task and every run it started. The factory stamps each AgentRun with
its task's trace context, in the annotation `agents.ogenki.io/traceparent` (W3C Trace Context). The
run controller passes that value to the harness as `TRACEPARENT`. A missing or malformed annotation
does not stop the run: the harness then starts a trace of its own.

The broker, the run controller and the Slack App emit no spans.

### What spans contain

The factory's and the harness's spans carry ids and timing, never a prompt, an issue's text or a
tool's output. The agent itself (OpenHands' agent-server) is not traced by default.

Its own tracing (OpenHands' observability and lmnr's instrumentation) records the content of the
work: each prompt, each model answer, each tool call's input and output. That includes whatever the
agent read: a token, a canary, a private file. Nothing turns that capture off in the agent-server
stack, so the harness gives agent-server no trace endpoint at all. A run with
`AGENT_TRACE_CONTENT=1` in its harness's environment forwards it, and agent-server's spans then
become children of the run's span. The run controller never sets it. Set it in your own run
template only behind a collector that drops those attributes before storing or forwarding a span,
as cloud-native-ref's collector does with an attribute filter.

Whatever you set, run every span from the runs namespace through an attribute filter at the
collector. That namespace runs the agent's code, so the next section's rules apply to it.

### Turning run traces on

```yaml
maku:
  runController:
    tracing:
      otlpEndpoint: http://collector.observability.svc.cluster.local:4318
      collector:
        namespace: observability
        labels: {app.kubernetes.io/name: collector}
```

- **`otlpEndpoint`** is an in-cluster Service, named in full as
  `<service>.<namespace>.svc.<cluster domain>`, in the chart's `clusterDomain`: the harness's
  `NO_PROXY` keeps only that domain off the egress proxy. Its port is the collector pods' own OTLP/HTTP port,
  because network policies see the pod's port, not the Service's. A run's DNS answers only full
  names, so a short name does not resolve.
- **`collector`** selects the collector's pods, in the endpoint's namespace. It is required with
  `otlpEndpoint`.

The render fails when `collector` is missing, when it names another namespace than the endpoint's,
when `collector` is set without `otlpEndpoint`, or when `collector.labels` names
`io.kubernetes.pod.namespace`, which would replace the namespace in the run's CiliumNetworkPolicy. The run controller checks the same rules again
when it starts.

Each run's harness then gets two variables: `OTEL_EXPORTER_OTLP_ENDPOINT`, and `TRACEPARENT` when
its AgentRun carries a valid annotation. The bridge gets neither.

### What tracing opens on the network

Without `tracing`, a run's network policy is the same as before. With it, each run's own policy
gains one rule: egress to the collector's pods, on the endpoint's port, and nothing else.

| `networkPolicies.mode` | The added rule |
|---|---|
| `kubernetes` | One egress rule in the run's NetworkPolicy: the collector's namespace and labels, on the port |
| `cilium` | One `toEndpoints` rule in the run's CiliumNetworkPolicy on the port, and the collector's name in its DNS allow-list |
| `none` | Nothing: no policy confines a run |

This is the only destination tracing adds to a run's egress ([Security](security.md#the-run-paths-network-modes)).
A run reaches the collector directly, not through the egress proxy: the harness's `NO_PROXY` holds
the cluster's Services.

The collector must admit the runs namespace on that port itself; the chart does not render the
collector's policy.

**What a run can send there.** The run's code, which includes whatever the agent runs, can write
any span to the collector. A run can therefore fill the collector, or forge spans that claim to be
another run's, and it can put anything it read into a span's attributes. Treat spans from the runs
namespace as the run's own report, not as proof. At the collector, for that namespace:

- **filter attributes**: keep the ones you need, or delete content attributes (`gen_ai.*`, `lmnr.*`,
  `input`, `output`);
- **bound the rate**, with a rate limit or a memory limiter.

### The factory's endpoint

`maku-factory.config.tracing.otlpEndpoint` has not changed: a gRPC `host:port`, empty for off. The
factory gives up a task span the collector has refused for a day, and counts it in
`maku_factory_trace_export_abandoned_total`.

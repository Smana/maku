# Observability

Every component serves `/metrics`; the umbrella chart (`charts/agent-factory`) can also render the
objects a Prometheus stack needs to scrape and alert on them. Each switch is off by default, and none
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

## Traces

The factory exports its task spans over OTLP/gRPC to `maku-factory.config.tracing.otlpEndpoint`; unset,
it exports none.

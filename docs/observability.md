# Observability

What the platform emits, and how to collect it with the stack you already run. Metrics, alerts and
logs are in [Operations](operations.md#metrics). This page covers traces.

## Traces

Two components export spans over OTLP. Each is off until you give it an endpoint:

| Emitter | Span | Endpoint | Protocol |
|---|---|---|---|
| The factory | One root span per task, from its acceptance to its end, exported when the task ends. It carries ids, the tier, the phase and a reason code, never issue text | `maku-factory.config.tracing.otlpEndpoint`, as `host:port` | OTLP/gRPC |
| Each run's harness | The run's root span, a child of its task's span. The agent's own spans are its children | `maku.runController.tracing.otlpEndpoint`, as an `http(s)://` base URL | OTLP/HTTP, to `<endpoint>/v1/traces` |

One trace therefore holds a task and every run it started. The factory stamps each AgentRun with
its task's trace context, in the annotation `agents.ogenki.io/traceparent` (W3C Trace Context). The
run controller passes that value to the harness as `TRACEPARENT`. A missing or malformed annotation
does not stop the run: the harness then starts a trace of its own.

The broker, the run controller and the Slack App emit no spans.

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
  `<service>.<namespace>.svc.<cluster domain>`. Its port is the collector pods' own OTLP/HTTP port,
  because network policies see the pod's port, not the Service's. A run's DNS answers only full
  names, so a short name does not resolve.
- **`collector`** selects the collector's pods, in the endpoint's namespace. It is required with
  `otlpEndpoint`.

The render fails when `collector` is missing, when it names another namespace than the endpoint's,
or when `collector` is set without `otlpEndpoint`. The run controller checks the same rules again
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
another run's. Treat spans from the runs namespace as the run's own report, not as proof. Bound
what the collector accepts from that namespace, with a rate limit or a memory limiter.

### The factory's endpoint

`maku-factory.config.tracing.otlpEndpoint` has not changed: a gRPC `host:port`, empty for off. The
factory gives up a task span the collector has refused for a day, and counts it in
`maku_factory_trace_export_abandoned_total`.

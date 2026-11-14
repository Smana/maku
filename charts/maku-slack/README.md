# maku-slack

The factory's Slack App ([docs/slack.md](../../docs/slack.md)) as a chart. Install it through the
umbrella chart as `slack` (`slack.enabled: true`, [values-slack.example.yaml](../agent-factory/values-slack.example.yaml)),
or alone beside a broker and a factory that name it:

- the broker's `broker.webhooks` entry for its `:8081` (`existingSecret` = this chart's `webhookSecret`);
- `human.mcp.sponsored` on the broker and `config.api.sponsored` on the factory, both naming `principal`.

It renders one replica (R26), its Service (`events` 8080, `webhooks` 8081, `metrics` 9090), a
ServiceAccount, a Role with `get` and `update` on `factory-slack-threads` alone, the thread map
ConfigMap, and the config ConfigMap. The Secrets are named, never rendered (D14):

| Value | Keys |
|---|---|
| `slackSecret` (`factory-slack-app`) | `signing-secret`, `bot-token`, `bot-user-id` |
| `principalSecret` (`factory-slack-principal`) | `client-id`, `client-secret`, and `project-id` under the zitadel preset |
| `webhookSecret` (`room-broker-webhook-slack-app`) | `signing-key` |

## The thread map survives upgrades

The App writes `factory-slack-threads`, the map from a Slack thread to its room. The chart renders it
with no `data` and `helm.sh/resource-policy: keep`, so a Helm upgrade never touches the App's entries
and an uninstall leaves them. A GitOps controller that compares the live object with the render would
still reset it. Ignore its `data`:

```yaml
# Flux HelmRelease
spec:
  driftDetection:
    mode: enabled
    ignore:
      - paths: ["/data"]
        target: {kind: ConfigMap, name: factory-slack-threads}
```

```yaml
# Argo CD Application
spec:
  ignoreDifferences:
    - kind: ConfigMap
      name: factory-slack-threads
      jsonPointers: [/data]
```

An install that already has the ConfigMap hands it to Helm first: add the labels and annotations Helm
adopts (`app.kubernetes.io/managed-by: Helm`, `meta.helm.sh/release-name`,
`meta.helm.sh/release-namespace`), then install.

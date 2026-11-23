# Dashboards

Grafana dashboards used in the talk. Import them with **Dashboards → New →
Import** and pick a Prometheus data source when asked (`datasource` variable).
Metrics come from an OpenTelemetry Collector over OTLP, so names use the
Prometheus translation (`k8s.node.memory.usage` → `k8s_node_memory_usage_bytes`).

| File | Dashboard | Data |
|---|---|---|
| [`who-measures-memory.json`](./who-measures-memory.json) | Linux & Kubernetes Memory: Who Measures It? | `kubeletstats` receiver (node, pod, container), `hostmetrics` `memory` and `process` scrapers (`process.memory.linux.*` needs the `process_memory_metrics` branch) |

The reclaim demo dashboard lives next to its code: [`reclaim_demo/dashboard.json`](../reclaim_demo/dashboard.json).

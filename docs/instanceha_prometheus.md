# InstanceHA Prometheus Monitoring Guide

## Overview

InstanceHA exposes Prometheus metrics at `:8080/metrics` on the workload pod, covering the full evacuation lifecycle: host failure detection, fencing, evacuation, recovery, and poll loop health. These metrics complement the Kubernetes Events emitted on the InstanceHa CR — events provide human-readable audit records, while metrics provide numeric time-series data suitable for dashboards, alerting, and capacity planning.

The metrics are served by the `prometheus_client` Python library on the same HTTP server used for liveness and readiness probes. No sidecar or additional container is needed.

---

## Prerequisites

- **InstanceHA deployed** with the Prometheus metrics support (container image includes `python3-prometheus_client`)
- **Prometheus Operator** installed in the cluster (ships with OpenShift as the cluster monitoring stack, or install via [kube-prometheus-stack](https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack))
- **Alertmanager** configured for alert routing (email, Slack, PagerDuty, etc.)
- **Grafana** (optional) for dashboards

---

## Enabling Scraping

### Step 1: Deploy a PodMonitor

Create a `PodMonitor` to tell Prometheus to scrape the InstanceHA pod:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: PodMonitor
metadata:
  name: instanceha-metrics
  namespace: openstack
  labels:
    app: instanceha
spec:
  selector:
    matchLabels:
      service: instanceha
  podMetricsEndpoints:
    - port: metrics
      path: /metrics
      interval: 30s
```

```bash
oc apply -f instanceha-podmonitor.yaml
```

> **Note**: On OpenShift, user workload monitoring must be enabled for PodMonitors in user namespaces (like `openstack`) to be scraped. Enable it by applying:
>
> ```yaml
> apiVersion: v1
> kind: ConfigMap
> metadata:
>   name: cluster-monitoring-config
>   namespace: openshift-monitoring
> data:
>   config.yaml: |
>     enableUserWorkload: true
> ```
>
> See [Enabling monitoring for user-defined projects](https://docs.openshift.com/container-platform/latest/observability/monitoring/enabling-monitoring-for-user-defined-projects.html) for details. Once enabled, verify that pods appear in `openshift-user-workload-monitoring`:
>
> ```bash
> oc -n openshift-user-workload-monitoring get pods
> ```

### Step 2: Verify the Target

After applying the PodMonitor, verify that Prometheus has discovered the target:

```bash
# Check PodMonitor status
oc get podmonitor -n openstack instanceha-metrics

# In the Prometheus UI (or via API), check the target is UP:
# Targets page: Status → Targets → search for "instanceha"

# On OpenShift with user workload monitoring, query the user-workload Prometheus
# (not the cluster Prometheus in openshift-monitoring):
oc -n openshift-user-workload-monitoring exec prometheus-user-workload-0 -- \
  curl -s 'http://localhost:9090/api/v1/targets' | \
  python3 -c "import sys,json; targets=json.load(sys.stdin)['data']['activeTargets']; \
  [print(t['labels'].get('pod',''), t['health']) for t in targets if 'instanceha' in t['labels'].get('pod','')]"

# Alternatively, use the thanos-querier route (aggregates both cluster and user workload):
TOKEN=$(oc whoami -t)
THANOS_URL=$(oc -n openshift-monitoring get route thanos-querier -o jsonpath='{.spec.host}')
curl -sk -H "Authorization: Bearer $TOKEN" \
  "https://${THANOS_URL}/api/v1/targets" | \
  python3 -c "import sys,json; targets=json.load(sys.stdin)['data']['activeTargets']; \
  [print(t['labels'].get('pod',''), t['health']) for t in targets if 'instanceha' in t['labels'].get('pod','')]"
```

### Step 3: Verify Metrics Are Flowing

```bash
# Scrape metrics directly from the pod
oc exec -n openstack deployment/instanceha-instanceha -- \
  curl -s http://localhost:8080/metrics

# Query a specific metric in Prometheus
# (via Prometheus UI or API)
instanceha_poll_cycles_total
```

---

## Metric Reference

### Counters

Counters increase monotonically and reset to zero on pod restart.

| Metric | Labels | Description |
|--------|--------|-------------|
| `instanceha_fencing_total` | `host`, `result` | Fencing operations. `result`: `started`, `succeeded`, `failed` |
| `instanceha_evacuation_total` | `host`, `result` | Host-level evacuation operations. `result`: `started`, `succeeded`, `failed` |
| `instanceha_instance_evacuation_total` | `host`, `result` | Per-instance evacuation operations (smart/orchestrated mode). `result`: `started`, `succeeded`, `failed` |
| `instanceha_host_down_total` | `host` | Host-down detections (each poll cycle where a host is seen as down) |
| `instanceha_host_reachable_total` | `host` | Hosts reported down by Nova but still reachable via heartbeat — fencing skipped |
| `instanceha_host_reenabled_total` | `host` | Hosts re-enabled after successful evacuation |
| `instanceha_threshold_exceeded_total` | — | Evacuations skipped because the percentage of failed hosts exceeded the global threshold |
| `instanceha_aggregate_threshold_exceeded_total` | `aggregate` | Evacuations blocked for an aggregate due to per-aggregate `instanceha:max_failures` metadata threshold |
| `instanceha_recovery_completed_total` | `host` | Full recovery workflows completed (fence + evacuate + recovery) |
| `instanceha_processing_failed_total` | `host` | Unhandled exceptions during service processing |
| `instanceha_orphaned_host_recovered_total` | — | Orphaned fenced hosts recovered during startup reconciliation |
| `instanceha_poll_cycles_total` | `result` | Poll cycles executed. `result`: `success`, `error` |

### Gauges

Gauges represent current values that can go up or down.

| Metric | Description |
|--------|-------------|
| `instanceha_poll_consecutive_failures` | Current count of consecutive Nova API poll failures. Resets to 0 on success. |
| `instanceha_hosts_processing` | Number of hosts currently being fenced/evacuated. |

### Histograms

| Metric | Labels | Buckets (seconds) | Description |
|--------|--------|-------------------|-------------|
| `instanceha_instance_evacuation_duration_seconds` | `host` | 10, 30, 60, 120, 180, 300, 600 | Time from evacuation request to completion for individual instances |

---

## Alert Rules

### Deploying the PrometheusRule

Apply the following `PrometheusRule` CR to create alerting rules:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: PrometheusRule
metadata:
  name: instanceha-alerts
  namespace: openstack
  labels:
    app: instanceha
spec:
  groups:
    - name: instanceha.rules
      rules:

        # --- Critical: Fencing failure ---
        # A host could not be powered off. Evacuation is blocked.
        # Requires immediate operator intervention.
        - alert: InstanceHAFencingFailure
          expr: increase(instanceha_fencing_total{result="failed"}[5m]) > 0
          for: 0m
          labels:
            severity: critical
          annotations:
            summary: "Fencing failed for host {{ $labels.host }}"
            description: >-
              InstanceHA failed to fence (power off) compute host {{ $labels.host }}.
              Evacuation cannot proceed until the host is confirmed offline.
              Check BMC connectivity and credentials in fencing.yaml.
            runbook_url: "https://github.com/openstack-k8s-operators/infra-operator/blob/main/docs/instanceha_guide.md#investigating-a-failed-evacuation"

        # --- Critical: Evacuation failure ---
        # VM evacuation failed for a host. VMs may be stuck.
        - alert: InstanceHAEvacuationFailure
          expr: increase(instanceha_evacuation_total{result="failed"}[5m]) > 0
          for: 0m
          labels:
            severity: critical
          annotations:
            summary: "VM evacuation failed for host {{ $labels.host }}"
            description: >-
              InstanceHA failed to evacuate VMs from compute host {{ $labels.host }}.
              Check Nova API logs, instance status, and K8s events for details.

        # --- Critical: Threshold exceeded ---
        # Too many hosts are down. Evacuation is blocked to prevent cascading failures.
        - alert: InstanceHAThresholdExceeded
          expr: increase(instanceha_threshold_exceeded_total[5m]) > 0
          for: 0m
          labels:
            severity: critical
          annotations:
            summary: "InstanceHA evacuation threshold exceeded"
            description: >-
              The percentage of failed compute hosts exceeds the configured THRESHOLD.
              Evacuation has been blocked to prevent cascading failures.
              This typically indicates a datacenter-wide issue (network partition, power outage).

        # --- Critical: Per-aggregate threshold exceeded ---
        # Too many hosts are down in a specific aggregate.
        - alert: InstanceHAAggregateThresholdExceeded
          expr: increase(instanceha_aggregate_threshold_exceeded_total[5m]) > 0
          for: 0m
          labels:
            severity: critical
          annotations:
            summary: "Per-aggregate failure threshold exceeded for {{ $labels.aggregate }}"
            description: >-
              The number of failed compute hosts in aggregate {{ $labels.aggregate }}
              exceeds the instanceha:max_failures metadata limit.
              Evacuation has been blocked for hosts in this aggregate.

        # --- Critical: InstanceHA is down ---
        # No successful poll cycles for 5 minutes — the agent is not functioning.
        - alert: InstanceHADown
          expr: rate(instanceha_poll_cycles_total{result="success"}[5m]) == 0
          for: 5m
          labels:
            severity: critical
          annotations:
            summary: "InstanceHA is not polling Nova API"
            description: >-
              No successful poll cycles in the last 5 minutes.
              InstanceHA cannot detect or respond to compute host failures.
              Check pod status and logs.

        # --- Warning: Nova API unreachable ---
        # Multiple consecutive poll failures. The agent is retrying with backoff.
        - alert: InstanceHANovaAPIDown
          expr: instanceha_poll_consecutive_failures > 3
          for: 2m
          labels:
            severity: warning
          annotations:
            summary: "InstanceHA cannot reach Nova API"
            description: >-
              {{ $value }} consecutive Nova API poll failures.
              The agent is retrying with exponential backoff.
              Check Nova API health and network connectivity.

        # --- Warning: Slow evacuations ---
        # The 95th percentile evacuation duration exceeds 5 minutes.
        - alert: InstanceHAEvacuationSlow
          expr: histogram_quantile(0.95, rate(instanceha_instance_evacuation_duration_seconds_bucket[15m])) > 300
          for: 5m
          labels:
            severity: warning
          annotations:
            summary: "Instance evacuations taking longer than 5 minutes (p95)"
            description: >-
              The 95th percentile evacuation duration is {{ $value | humanizeDuration }}.
              This may indicate Nova scheduler contention, storage I/O pressure,
              or insufficient capacity on target hosts.

        # --- Warning: Processing failures ---
        # Unhandled exceptions in the service processing pipeline.
        - alert: InstanceHAProcessingFailure
          expr: increase(instanceha_processing_failed_total[10m]) > 0
          for: 0m
          labels:
            severity: warning
          annotations:
            summary: "InstanceHA processing failure for host {{ $labels.host }}"
            description: >-
              An unhandled exception occurred while processing compute host {{ $labels.host }}.
              Check the InstanceHA pod logs for the full traceback.
```

```bash
oc apply -f instanceha-prometheusrule.yaml
```

### Verifying Alert Rules

```bash
# Check the PrometheusRule was loaded
oc get prometheusrule -n openstack instanceha-alerts

# Verify rules appear in Prometheus
# Prometheus UI: Status → Rules → search for "instanceha"

# Lint the rules file locally (optional)
promtool check rules instanceha-prometheusrule.yaml
```

---

## Testing

### Verify Metrics Endpoint

```bash
# Scrape all metrics from the pod
oc exec -n openstack deployment/instanceha-instanceha -- \
  curl -s http://localhost:8080/metrics

# Check a specific metric family
oc exec -n openstack deployment/instanceha-instanceha -- \
  curl -s http://localhost:8080/metrics | grep instanceha_poll_cycles_total
```

Expected output (counters start at zero, increment over time):

```
# HELP instanceha_poll_cycles_total Total poll cycles executed
# TYPE instanceha_poll_cycles_total counter
instanceha_poll_cycles_total{result="success"} 42.0
instanceha_poll_cycles_total{result="error"} 0.0
```

### Verify Poll Loop Metrics

After the pod has been running for a few poll cycles:

```bash
# Should show increasing success count
oc exec -n openstack deployment/instanceha-instanceha -- \
  curl -s http://localhost:8080/metrics | grep poll_cycles

# Should show 0 consecutive failures (healthy state)
oc exec -n openstack deployment/instanceha-instanceha -- \
  curl -s http://localhost:8080/metrics | grep poll_consecutive_failures
```

### Simulate a Nova API Failure

To test the `InstanceHANovaAPIDown` alert, temporarily break the Nova API connection:

```bash
# Option 1: Use an invalid password in secure.yaml
# Edit the openstack-config-secret to use a wrong password, then restart the pod.
# The poll loop will start failing and incrementing consecutive_failures.

# Watch the metric increase
watch -n5 "oc exec -n openstack deployment/instanceha-instanceha -- \
  curl -s http://localhost:8080/metrics | grep poll_consecutive_failures"

# Restore the correct password when done
```

### Verify Fencing/Evacuation Metrics

Fencing and evacuation counters only increment during actual host failures. To verify the metrics are wired correctly without a real failure:

```bash
# Check that the metric families are registered (even if values are 0)
oc exec -n openstack deployment/instanceha-instanceha -- \
  curl -s http://localhost:8080/metrics | grep "^instanceha_" | grep "# TYPE"
```

Expected output:

```
# TYPE instanceha_fencing_total counter
# TYPE instanceha_evacuation_total counter
# TYPE instanceha_instance_evacuation_total counter
# TYPE instanceha_instance_evacuation_duration_seconds histogram
# TYPE instanceha_host_down_total counter
# TYPE instanceha_host_reachable_total counter
# TYPE instanceha_host_reenabled_total counter
# TYPE instanceha_threshold_exceeded_total counter
# TYPE instanceha_aggregate_threshold_exceeded_total counter
# TYPE instanceha_recovery_completed_total counter
# TYPE instanceha_processing_failed_total counter
# TYPE instanceha_orphaned_host_recovered_total counter
# TYPE instanceha_poll_consecutive_failures gauge
# TYPE instanceha_hosts_processing gauge
# TYPE instanceha_poll_cycles_total counter
```

### Test With Noop Fencing

For a full end-to-end test in a non-production environment, configure a compute host with `noop` fencing and simulate a failure:

```yaml
# In fencing.yaml
compute-test:
  agent: noop
```

Then stop `nova-compute` on the test host. InstanceHA will detect the failure, "fence" it (noop), and evacuate VMs. All metrics will increment accordingly.

---

## Grafana Dashboard

### Panel Layout

A useful InstanceHA dashboard contains these panels:

#### Row 1: Overview

| Panel | Type | Query |
|-------|------|-------|
| Poll Health | Stat | `instanceha_poll_consecutive_failures` |
| Hosts Processing | Stat | `instanceha_hosts_processing` |
| Poll Success Rate | Gauge | `rate(instanceha_poll_cycles_total{result="success"}[5m]) / rate(instanceha_poll_cycles_total[5m])` |

#### Row 2: Fencing

| Panel | Type | Query |
|-------|------|-------|
| Fencing Operations | Time series | `increase(instanceha_fencing_total[5m])` grouped by `result` |
| Fencing Success Rate | Stat | `increase(instanceha_fencing_total{result="succeeded"}[1h]) / increase(instanceha_fencing_total{result="started"}[1h])` |

#### Row 3: Evacuation

| Panel | Type | Query |
|-------|------|-------|
| Host Evacuations | Time series | `increase(instanceha_evacuation_total[5m])` grouped by `result` |
| Instance Evacuation Duration (p50/p95/p99) | Time series | `histogram_quantile(0.50, rate(instanceha_instance_evacuation_duration_seconds_bucket[15m]))` (repeat for 0.95, 0.99) |
| Evacuation Duration Heatmap | Heatmap | `increase(instanceha_instance_evacuation_duration_seconds_bucket[5m])` |

#### Row 4: Host State

| Panel | Type | Query |
|-------|------|-------|
| Host Down Detections | Time series | `increase(instanceha_host_down_total[5m])` grouped by `host` |
| Hosts Re-enabled | Time series | `increase(instanceha_host_reenabled_total[5m])` grouped by `host` |
| Threshold Exceeded | Time series | `increase(instanceha_threshold_exceeded_total[5m])` |
| Aggregate Threshold Exceeded | Time series | `increase(instanceha_aggregate_threshold_exceeded_total[5m])` grouped by `aggregate` |

#### Row 5: Errors

| Panel | Type | Query |
|-------|------|-------|
| Processing Failures | Time series | `increase(instanceha_processing_failed_total[5m])` grouped by `host` |
| Heartbeat Overrides | Time series | `increase(instanceha_host_reachable_total[5m])` grouped by `host` |

### Importing

To create this dashboard in Grafana:

1. Create a new dashboard and add panels using the queries above.
2. Set the data source to your Prometheus instance.
3. Use `$namespace` as a template variable (filter: `label_values(instanceha_poll_cycles_total, namespace)`) to support multi-namespace deployments.
4. Set the default time range to **Last 6 hours** — InstanceHA events are infrequent in healthy environments.

---

## Useful PromQL Queries

```promql
# Fencing failure rate over the last hour
increase(instanceha_fencing_total{result="failed"}[1h])

# Total VMs evacuated in the last 24 hours
increase(instanceha_instance_evacuation_total{result="succeeded"}[24h])

# Average evacuation duration over the last hour
rate(instanceha_instance_evacuation_duration_seconds_sum[1h])
  / rate(instanceha_instance_evacuation_duration_seconds_count[1h])

# Hosts that have been down in the last hour
count by (host) (increase(instanceha_host_down_total[1h]) > 0)

# Poll error ratio
rate(instanceha_poll_cycles_total{result="error"}[5m])
  / rate(instanceha_poll_cycles_total[5m])

# Time since last successful poll (requires recording rule or subquery)
time() - instanceha_poll_cycles_total{result="success"} @ end()
```

---

## Integration with telemetry-operator

When the [telemetry-operator](https://github.com/openstack-k8s-operators/telemetry-operator) is deployed, it provisions a **separate Prometheus instance** via the Cluster Observability Operator (COO). This Prometheus is independent from OpenShift's built-in user workload monitoring — the two stacks coexist without conflict, but metrics live in different places:

| Stack | Prometheus instance | Query endpoint |
|-------|-------------------|----------------|
| OpenShift user workload monitoring | `prometheus-user-workload` in `openshift-user-workload-monitoring` | `thanos-querier` route in `openshift-monitoring` |
| telemetry-operator (COO) | `prometheus-metric-storage` in `openstack` | `metric-storage-prometheus.openstack.svc:9090` |

The PodMonitor approach described above places InstanceHA metrics in the OpenShift user workload Prometheus. If you want InstanceHA metrics alongside other OpenStack metrics (Ceilometer, RabbitMQ, node-exporter, OVN) in the COO Prometheus, create a `ScrapeConfig` CR instead.

### Creating a ScrapeConfig for COO Prometheus

The COO Prometheus only picks up CRs with the label `service: metricStorage`. Create a `ScrapeConfig` targeting the InstanceHA pod:

```yaml
apiVersion: monitoring.rhobs/v1alpha1
kind: ScrapeConfig
metadata:
  name: instanceha-metrics
  namespace: openstack
  labels:
    service: metricStorage
spec:
  scrapeInterval: 30s
  metricsPath: /metrics
  staticConfigs:
    - targets:
        - "<instanceha-pod-ip>:8080"
```

To discover the pod IP dynamically:

```bash
POD_IP=$(oc get pod -n openstack -l service=instanceha -o jsonpath='{.items[0].status.podIP}')
echo "Target: ${POD_IP}:8080"
```

> **Note**: The COO `ScrapeConfig` uses static targets (IP:port), not label-based pod discovery like a `PodMonitor`. If the InstanceHA pod is rescheduled and gets a new IP, the `ScrapeConfig` must be updated. For automatic discovery, consider requesting native InstanceHA support in the telemetry-operator — the OVN metrics integration uses a label-based service discovery pattern that could be extended to InstanceHA.

### Alert Rules for COO Prometheus

The alert rules from the [Alert Rules](#alert-rules) section use the `monitoring.coreos.com/v1` API group, which is picked up by OpenShift's built-in Prometheus Operator. To use these alerts with the COO Prometheus instead, change the API group and add the `service: metricStorage` label:

```yaml
apiVersion: monitoring.rhobs/v1
kind: PrometheusRule
metadata:
  name: instanceha-alerts
  namespace: openstack
  labels:
    service: metricStorage
spec:
  # ... same groups/rules as above ...
```

### Which Approach to Use

- **OpenShift user workload monitoring only** (no telemetry-operator): Use the PodMonitor approach from [Enabling Scraping](#enabling-scraping). This is simpler and uses automatic pod discovery.
- **telemetry-operator deployed**: Use the ScrapeConfig approach if you want all OpenStack metrics in a single Prometheus. You can also use both approaches simultaneously — the PodMonitor and ScrapeConfig target different Prometheus instances and do not conflict.
- **Querying across both**: OpenShift's `thanos-querier` route aggregates the cluster and user workload Prometheus instances. The COO Prometheus is separate and must be queried directly at `metric-storage-prometheus.openstack.svc:9090`.

---

## References

- [InstanceHA Operator Guide](instanceha_guide.md) — Full deployment and configuration reference
- [InstanceHA Architecture](instanceha_architecture.md) — Internal architecture and event catalog
- [Prometheus Operator Documentation](https://prometheus-operator.dev/docs/getting-started/introduction/)
- [PromQL Basics](https://prometheus.io/docs/prometheus/latest/querying/basics/)

# InstanceHA Operator Guide

## What is InstanceHA?

InstanceHA provides automatic high availability for OpenStack instances running on bare-metal compute nodes. When a compute host fails, InstanceHA detects the failure, powers off the host via out-of-band management (fencing), and evacuates all eligible instances to healthy hosts using the Nova API.

InstanceHA runs as a Kubernetes-managed workload alongside the OpenStack control plane. A Go-based Kubernetes controller manages the lifecycle of the InstanceHA deployment, while a Python agent inside the pod continuously monitors compute services and responds to failures.

## Architecture Overview

```
┌──────────────────────────────────────────────────────────────────┐
│                    Kubernetes Cluster                            │
│                                                                  │
│  ┌─────────────────────┐        ┌──────────────────────────────┐ │
│  │  InstanceHa CR      │        │  InstanceHA Controller (Go)  │ │
│  │  (Custom Resource)  │◄──────►│  - Watches CR changes        │ │
│  │                     │        │  - Manages Deployment        │ │
│  │  K8s Events ◄───────┤        │  - Validates inputs          │ │
│  │  (evacuation        │        │  - Mounts config & secrets   │ │
│  │   lifecycle)        │        │  - Emits K8s events          │ │
│  └─────────────────────┘        └──────────────────────────────┘ │
│                                           │                      │
│                                           ▼                      │
│  ┌───────────────────────────────────────────────────────────┐   │
│  │  InstanceHA Pod                                           │   │
│  │  ┌──────────────────────────────────────────────────────┐ │   │
│  │  │  instanceha.py (Python Agent)                        │ │   │
│  │  │                                                      │ │   │
│  │  │  Poll Loop ──► Nova API ──► Detect Failures          │ │   │
│  │  │       │                           │                  │ │   │
│  │  │       │                    ┌──────┴──────┐           │ │   │
│  │  │       │                    ▼             ▼           │ │   │
│  │  │       │              Fence Host    Evacuate VMs      │ │   │
│  │  │       │             (IPMI/Redfish   (Nova API)       │ │   │
│  │  │       │              /Metal3)                        │ │   │
│  │  │       │                                              │ │   │
│  │  │  UDP Listener ◄── Kdump messages from compute nodes  │ │   │
│  │  │  UDP Listener ◄── Heartbeat packets from computes    │ │   │
│  │  │  Health Server ──► :8080 liveness + readiness probes │ │   │
│  │  │                 ──► :8080/metrics (Prometheus)       │ │   │
│  │  └──────────────────────────────────────────────────────┘ │   │
│  └───────────────────────────────────────────────────────────┘   │
│                                                                  │
│  ConfigMap: clouds.yaml    Secret: secure.yaml                   │
│  ConfigMap: config.yaml    Secret: fencing.yaml                  │
│                            Secret: ac-credentials (optional)     │
│                            Secret: heartbeat-hmac (auto-gen)     │
└──────────────────────────────────────────────────────────────────┘
```

### How It Works

1. **Detection**: The agent polls the Nova API every `POLL` seconds (default: 45). It queries all `nova-compute` services and checks each one's `updated_at` timestamp and `state` field. A service is considered failed if it is reported as `down` or if its heartbeat is staler than `DELTA` seconds (default: 30). When heartbeat verification is enabled (`CHECK_HEARTBEAT`), both the Nova service-list poll and the UDP heartbeat channel must agree that a host is unreachable before it is marked as failed.

2. **Safety checks**: Before acting, the agent verifies that the percentage of failed hosts does not exceed the `THRESHOLD` (default: 50%) — calculated against only active services, excluding already-disabled and already-forced-down hosts — and that at least one `nova-scheduler` is running. These checks prevent cascading evacuations during infrastructure-wide outages. A `ThresholdExceeded` K8s event is emitted when the limit is hit.

3. **Fencing**: The failed host is powered off via its baseboard management controller (BMC) using IPMI, Redfish, or the Metal3 Kubernetes API. Fencing ensures the host is truly offline before evacuation begins, preventing split-brain scenarios where the old and new copies of an instance run simultaneously. The server is locked in Nova (API microversion 2.73, reason `instanceha-evacuation`) to prevent concurrent operations.

4. **Evacuation**: The agent marks the compute service as `forced_down` and `disabled` in Nova, then calls the evacuate API for each eligible instance. Three strategies are available — traditional (fire-and-forget), smart (tracked to completion), and orchestrated (priority-ordered phases). All strategies use continue-on-failure semantics: a single failed evacuation does not abort the remaining ones.

5. **Recovery**: After evacuation completes, the agent updates the service's `disabled_reason` marker. When the host comes back online and its `nova-compute` service reports `up`, the agent automatically re-enables it and unlocks its servers (unless `LEAVE_DISABLED` is set). K8s events are emitted at each stage.

### Startup Reconciliation

When the InstanceHA pod starts (or restarts after a crash), it reconciles orphaned state before entering the main poll loop:

- Finds hosts with `forced_down=True` and `state=down` that are not disabled (hosts that were fenced but not fully processed before the pod died).
- Sets the evacuation marker so the normal resume logic picks them up.
- Unlocks any VMs left with an `instanceha-evacuation` lock from the previous run.
- Emits an `OrphanedHostRecovered` K8s event for each recovered host.

Resumed evacuations skip power-off fencing (the host was already fenced) but perform power-on after evacuation completes, ensuring orphaned hosts are not left powered off indefinitely.

### State Machine

Each compute service transitions through the following states during a failure and recovery cycle:

```
Normal ──► Failed ──► Fenced ──► Evacuating ──► Complete ──► Re-enabled
                                                    │
                                        (LEAVE_DISABLED=true)
                                                    │
                                                    ▼
                                            Remains Disabled
                                          (manual intervention)
```

The agent tracks state via the Nova service `disabled_reason` field:

| Marker | Meaning |
|--------|---------|
| `instanceha evacuation: <timestamp>` | Evacuation in progress |
| `instanceha evacuation complete: <timestamp>` | Evacuation finished, awaiting re-enable |
| `instanceha evacuation FAILED: <timestamp>` | Evacuation failed, requires manual intervention |
| `instanceha evacuation (kdump): <timestamp>` | Kdump-triggered evacuation in progress |
| `instanceha evacuation complete (kdump): <timestamp>` | Kdump evacuation finished |

If the InstanceHA agent is restarted mid-evacuation, it detects services with the `instanceha evacuation:` marker (without `complete` or `FAILED`) and resumes evacuation without re-fencing.

### Kubernetes Events

InstanceHA emits Kubernetes events on the InstanceHa CR to provide observability into the evacuation lifecycle:

| Event | Type | Description |
|-------|------|-------------|
| `HostDown` | Warning | Compute host detected as down |
| `FencingStarted` | Normal | Host fencing initiated |
| `FencingSucceeded` | Normal | Host fenced successfully |
| `FencingFailed` | Warning | Fencing operation failed |
| `EvacuationStarted` | Normal | Host-level VM evacuation started |
| `EvacuationSucceeded` | Normal | All VMs evacuated from host |
| `EvacuationFailed` | Warning | Host-level evacuation failed |
| `InstanceEvacuationStarted` | Normal | Individual VM evacuation started (smart/orchestrated mode) |
| `InstanceEvacuationSucceeded` | Normal | Individual VM evacuated successfully |
| `InstanceEvacuationFailed` | Warning | Individual VM evacuation failed |
| `HostReenabled` | Normal | Host re-enabled after recovery |
| `HostReachable` | Warning | Host reported down by Nova but still reachable via heartbeat |
| `RecoveryCompleted` | Normal | Full recovery cycle complete |
| `ProcessingFailed` | Warning | Unhandled exception during service processing |
| `ThresholdExceeded` | Warning | Too many hosts down simultaneously |
| `AggregateThresholdExceeded` | Warning | Per-aggregate failure threshold exceeded |
| `OrphanedHostRecovered` | Warning | Recovered fenced host from prior crash |

Events can be viewed with `oc describe instanceha <name>` or `oc get events --field-selector involvedObject.name=<name>`.

### Prometheus Metrics

In addition to Kubernetes Events, the agent exposes Prometheus metrics at `http://<pod-ip>:8080/metrics`. These provide time-series data suitable for dashboards, alerting, and capacity planning. See [instanceha_prometheus.md](instanceha_prometheus.md) for a complete operations guide covering scraping setup, alert rules, testing, and Grafana dashboards.

#### Counters

| Metric | Labels | Description |
|--------|--------|-------------|
| `instanceha_fencing_total` | `host`, `result` | Total fencing operations (result: `started`, `succeeded`, `failed`) |
| `instanceha_evacuation_total` | `host`, `result` | Host-level evacuation operations |
| `instanceha_instance_evacuation_total` | `host`, `result` | Per-instance evacuation operations (smart/orchestrated mode) |
| `instanceha_host_down_total` | `host` | Host-down detections |
| `instanceha_host_reachable_total` | `host` | Hosts reported down by Nova but still reachable via heartbeat |
| `instanceha_host_reenabled_total` | `host` | Hosts re-enabled after evacuation |
| `instanceha_threshold_exceeded_total` | | Evacuations skipped due to threshold |
| `instanceha_aggregate_threshold_exceeded_total` | `aggregate` | Evacuations blocked by per-aggregate threshold |
| `instanceha_recovery_completed_total` | `host` | Full recovery workflows completed |
| `instanceha_processing_failed_total` | `host` | Service processing failures |
| `instanceha_orphaned_host_recovered_total` | | Orphaned hosts recovered at startup |
| `instanceha_poll_cycles_total` | `result` | Poll cycles executed (result: `success`, `error`) |

#### Gauges

| Metric | Description |
|--------|-------------|
| `instanceha_poll_consecutive_failures` | Current consecutive Nova API poll failures (resets to 0 on success) |
| `instanceha_hosts_processing` | Number of hosts currently being processed |

#### Histograms

| Metric | Labels | Buckets (seconds) | Description |
|--------|--------|-------------------|-------------|
| `instanceha_instance_evacuation_duration_seconds` | `host` | 10, 30, 60, 120, 180, 300, 600 | Duration of individual instance evacuations |

#### Example Alert Rules

```yaml
groups:
  - name: instanceha
    rules:
      - alert: InstanceHAFencingFailure
        expr: increase(instanceha_fencing_total{result="failed"}[5m]) > 0
        for: 0m
        labels:
          severity: critical
        annotations:
          summary: "Fencing failed for host {{ $labels.host }}"

      - alert: InstanceHANovaAPIDown
        expr: instanceha_poll_consecutive_failures > 3
        for: 2m
        labels:
          severity: warning
        annotations:
          summary: "InstanceHA cannot reach Nova API"

      - alert: InstanceHAEvacuationSlow
        expr: histogram_quantile(0.95, rate(instanceha_instance_evacuation_duration_seconds_bucket[15m])) > 300
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Instance evacuations taking longer than 5 minutes (p95)"
```

#### Scraping Configuration

The InstanceHA pod exposes metrics on TCP port 8080. The infra-operator automatically creates a Kubernetes Service (`<instance-name>-metrics`) with the labels `metrics: enabled` and `service: instanceha`, which the telemetry-operator discovers and scrapes via the COO Prometheus. **No manual configuration is needed when the telemetry-operator is deployed.**

For environments using OpenShift user workload monitoring instead of (or in addition to) the telemetry-operator, create a `PodMonitor`:

```yaml
apiVersion: monitoring.coreos.com/v1
kind: PodMonitor
metadata:
  name: instanceha-metrics
  namespace: openstack
spec:
  selector:
    matchLabels:
      service: instanceha  # Replace with your CR name if different
  podMetricsEndpoints:
    - port: metrics
      path: /metrics
      interval: 30s
```

### Graceful Shutdown

When the pod receives SIGTERM (during scaling, rolling updates, or node drain), InstanceHA finishes any in-flight fencing or evacuation work before exiting. The Deployment is configured with a 30-second termination grace period. The agent's poll loop and evacuation workers check a shutdown flag between cycles, allowing timely response to shutdown signals.

---

## Deployment

### Prerequisites

- An OpenStack control plane deployed via openstack-k8s-operators
- OpenStack admin credentials (clouds.yaml and secure.yaml) stored as a ConfigMap and Secret
- Fencing credentials for each compute node's BMC stored as a Secret
- Network connectivity from the InstanceHA pod to Nova API, Keystone, and all compute BMCs

### Creating the InstanceHa Resource

```yaml
apiVersion: instanceha.openstack.org/v1beta1
kind: InstanceHa
metadata:
  name: instanceha
  namespace: openstack
spec:
  # OpenStack cloud name (must match clouds.yaml)
  openStackCloud: default

  # ConfigMap containing clouds.yaml
  openStackConfigMap: openstack-config

  # Secret containing secure.yaml (passwords)
  openStackConfigSecret: openstack-config-secret

  # Secret containing fencing.yaml (BMC credentials)
  fencingSecret: fencing-secret

  # ConfigMap containing InstanceHA configuration
  instanceHaConfigMap: instanceha-config

  # UDP port for kdump messages (default: 7410)
  instanceHaKdumpPort: 7410

  # UDP port for heartbeat messages (default: 7411)
  instanceHaHeartbeatPort: 7411

  # Optional: Application Credential authentication (see Authentication section)
  # auth:
  #   applicationCredentialSecret: instanceha-ac-secret

  # Optional: CA certificates for TLS connections
  caBundleSecretName: combined-ca-bundle

  # Optional: node placement constraints
  nodeSelector:
    node-role.kubernetes.io/worker: ""

  # Optional: additional network attachments
  networkAttachments:
    - internalapi

  # Optional: disable without removing the resource
  disabled: "False"
```

### Network Attachments

The `networkAttachments` field lists Multus NetworkAttachmentDefinition CRs to attach to the InstanceHA pod. Each entry adds an additional network interface. The UDP listeners (kdump and heartbeat) bind to `::` (dual-stack), so they accept both IPv4 and IPv6 packets on any attached interface.

A common pattern is to separate heartbeat traffic from the API network. Heartbeat packets are small and frequent — putting them on the same network as the Nova API can add noise, and a congested API network could delay heartbeats enough to trigger false positives. Using a dedicated network for heartbeat avoids both problems:

```yaml
spec:
  networkAttachments:
    - internalapi      # Nova API, Keystone, BMC management
    - heartbeat-net    # Dedicated network for compute heartbeats

  instanceHaHeartbeatPort: 7411
```

The corresponding NetworkAttachmentDefinition must exist in the same namespace:

```yaml
apiVersion: k8s.cni.cncf.io/v1
kind: NetworkAttachmentDefinition
metadata:
  name: heartbeat-net
  namespace: openstack
spec:
  config: |
    {
      "cniVersion": "0.3.1",
      "name": "heartbeat-net",
      "type": "macvlan",
      "master": "eth1",
      "ipam": {
        "type": "whereabouts",
        "range": "172.20.0.0/24"
      }
    }
```

Compute nodes then send their heartbeat packets to the InstanceHA pod's IP on the `heartbeat-net` network (e.g., `172.20.0.x:7411`), while the Nova API traffic flows through `internalapi` as usual.

When only a single `networkAttachments` entry is provided (the typical case), all traffic — Nova API, kdump, and heartbeat — shares that interface.

### Stable IP for UDP Listeners

By default, the InstanceHA pod receives a dynamic IP from the Multus IPAM pool. If the pod restarts, it may get a different IP, and compute nodes sending kdump or heartbeat packets to the old address will fail until reconfigured.

To provide a stable IP that survives pod restarts, create a MetalLB-backed LoadBalancer Service that fronts the InstanceHA UDP ports. Compute nodes send packets to the Service's external IP, and MetalLB routes them to the pod regardless of its current address.

```yaml
apiVersion: v1
kind: Service
metadata:
  name: instanceha-udp
  namespace: openstack
  annotations:
    metallb.universe.tf/address-pool: internalapi
    metallb.universe.tf/allow-shared-ip: internalapi
    metallb.universe.tf/loadBalancerIPs: 172.17.0.80
spec:
  type: LoadBalancer
  selector:
    service: instanceha  # Replace with your CR name if different
  ports:
  - name: kdump
    port: 7410
    targetPort: 7410
    protocol: UDP
  - name: heartbeat
    port: 7411
    targetPort: 7411
    protocol: UDP
```

Replace `172.17.0.80` with an IP from your `internalapi` MetalLB address pool. The `allow-shared-ip` annotation lets this Service share the same IP with other OpenStack services (e.g., keystone, nova, cinder) that already use the `internalapi` pool — since the InstanceHA ports (7410/UDP, 7411/UDP) don't conflict with any existing service ports, sharing is safe and avoids consuming an additional IP. If you prefer a dedicated IP, pick an unused address and remove the `allow-shared-ip` annotation.

> **Note:** The `address-pool` value must match the actual MetalLB IPAddressPool name. In a standard openstack-k8s-operators deployment this is `internalapi`. You can verify the pool name with `oc get ipaddresspools -n metallb-system`.

Then configure compute nodes to send kdump and heartbeat packets to that IP instead of the pod IP.

### Required Secrets and ConfigMaps

**clouds.yaml** (ConfigMap): OpenStack endpoint and authentication configuration.

```yaml
clouds:
  default:
    auth:
      username: admin
      project_name: admin
      auth_url: https://keystone-public.openstack.svc:5000/v3
      user_domain_name: Default
      project_domain_name: Default
    region_name: regionOne
```

**secure.yaml** (Secret): Password for the cloud defined in clouds.yaml.

```yaml
clouds:
  default:
    auth:
      password: <admin-password>
```

**fencing.yaml** (Secret): Per-host fencing credentials. Each key is the compute host's short hostname as reported by `nova-compute`.

```yaml
FencingConfig:
  compute-0:
    agent: ipmi
    ipaddr: 10.0.0.10
    ipport: "623"
    login: admin
    passwd: <ipmi-password>

  compute-1:
    agent: redfish
    ipaddr: 10.0.0.11
    ipport: "443"
    login: root
    passwd: <redfish-password>
    tls: "true"
    uuid: System.Embedded.1

  compute-2:
    agent: bmh
    host: compute-2-bmh
    namespace: openshift-machine-api
    token: <service-account-token>
```

**config.yaml** (ConfigMap): InstanceHA runtime parameters. All fields are optional; defaults are applied automatically. See the [Configuration Reference](#configuration-reference) section for details.

```yaml
config:
  POLL: "45"
  DELTA: "30"
  THRESHOLD: "50"
  SMART_EVACUATION: "true"
  WORKERS: "4"
  LOGLEVEL: "INFO"
```

---

## Authentication

### Password Authentication (Default)

By default, InstanceHA authenticates to Nova using the username and password from `clouds.yaml` and `secure.yaml`. This is the simplest setup and requires no additional configuration.

### Application Credentials

Application Credentials provide a more secure alternative: they can be scoped to a single project, rotated independently of the admin password, and revoked without affecting other services.

To use Application Credentials:

1. Create the credential in Keystone:

```bash
openstack application credential create instanceha-ac \
  --role admin --unrestricted
```

2. Store the credential ID and secret in a Kubernetes Secret:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: instanceha-ac-secret
  namespace: openstack
type: Opaque
stringData:
  AC_ID: <application-credential-id>
  AC_SECRET: <application-credential-secret>
```

3. Reference the secret in the InstanceHa CR:

```yaml
spec:
  auth:
    applicationCredentialSecret: instanceha-ac-secret
```

The controller mounts the secret at `/secrets/ac-credentials` and sets `AC_ENABLED=True`. The agent uses the `v3applicationcredential` keystoneauth plugin, with automatic fallback to password auth if the AC secret is unavailable.

See `docs/instanceha_application_credentials.md` for details on credential rotation and scoping.

---

## Fencing

Fencing (also called STONITH — Shoot The Other Node In The Head) ensures that a failed compute host is truly powered off before its instances are rebuilt elsewhere. Without fencing, a host that appears down but is actually partitioned could continue running instances, leading to data corruption or IP conflicts.

### Supported Fencing Agents

| Agent | Protocol | Use Case |
|-------|----------|----------|
| `ipmi` | IPMI over LAN | Standard BMC on most server hardware |
| `redfish` | Redfish (HTTPS) | Modern BMCs (iDRAC, iLO, OpenBMC) |
| `bmh` | Kubernetes API | Bare-metal hosts managed by Metal3/Ironic |
| `noop` | None | Testing and development only |

### IPMI Configuration

```yaml
compute-0:
  agent: ipmi
  ipaddr: 10.0.0.10    # BMC IP address
  ipport: "623"         # IPMI port (default: 623)
  login: admin          # BMC username
  passwd: <password>    # BMC password (passed via env var, not CLI)
```

### Redfish Configuration

```yaml
compute-1:
  agent: redfish
  ipaddr: 10.0.0.11     # BMC IP/hostname
  ipport: "443"          # Redfish port
  login: root            # BMC username
  passwd: <password>     # BMC password
  tls: "true"            # Enable TLS (recommended)
  uuid: System.Embedded.1  # Redfish system ID
```

Redfish operations attempt up to 3 times with per-request timeout of `FENCING_TIMEOUT` (configurable range: 5–120 seconds). All URLs are validated to prevent SSRF attacks (localhost and link-local addresses are blocked).

### Metal3 (BMH) Configuration

```yaml
compute-2:
  agent: bmh
  host: compute-2-bmh      # BareMetalHost CR name
  namespace: openshift-machine-api  # BMH namespace
  token: <sa-token>        # ServiceAccount bearer token
```

Metal3 fencing patches the `BareMetalHost` custom resource in the Kubernetes API to trigger a power state change, then waits for the power-off to be confirmed.

---

## Evacuability: Controlling Which Instances Are Protected

By default, InstanceHA evaluates three tag-based filters to decide which instances to evacuate. The filters use **OR logic**: an instance is evacuable if it matches any enabled filter.

### Tagging Methods

**Flavor tagging** (`TAGGED_FLAVORS: true`): Only evacuate instances whose flavor has the evacuable tag.

```bash
openstack flavor set --property evacuable=true m1.large
```

**Image tagging** (`TAGGED_IMAGES: true`): Only evacuate instances whose image has the evacuable tag.

```bash
openstack image set --property evacuable=true rhel-9.4
```

**Aggregate tagging** (`TAGGED_AGGREGATES: true`): Only evacuate instances on hosts that belong to an aggregate with the evacuable tag.

```bash
openstack aggregate set --property evacuable=true production-hosts
```

The tag name defaults to `evacuable` and can be changed with `EVACUABLE_TAG`.

### Excluding Instances by Name

The `SKIP_SERVERS_WITH_NAME` config option accepts a list of glob patterns (using `fnmatch` syntax). Instances whose name matches any pattern are excluded from evacuation regardless of tagging. This is useful for infrastructure VMs (e.g., network routers) that should not be moved. Supported wildcards: `*` (any characters), `?` (single character), `[seq]` (character set).

```yaml
config:
  SKIP_SERVERS_WITH_NAME: "router-*, dvr-*"
```

### Behavior Summary

| TAGGED_FLAVORS | TAGGED_IMAGES | TAGGED_AGGREGATES | Effect |
|:-:|:-:|:-:|--------|
| false | false | false | All instances are evacuated |
| true | false | false | Only instances with tagged flavors |
| false | true | false | Only instances with tagged images |
| true | true | false | Instances with tagged flavor OR tagged image |
| false | false | true | Only instances on hosts in tagged aggregates |
| true | true | true | Tagged flavor OR tagged image, AND host must be in tagged aggregate |

When `TAGGED_FLAVORS` and/or `TAGGED_IMAGES` are enabled but no flavors or images carry the tag, all instances pass the flavor/image filter. This fail-open behavior ensures evacuation works out of the box before tagging is configured. However, `TAGGED_AGGREGATES` is fail-closed: if no aggregates carry the evacuable tag, no hosts pass the aggregate filter and no evacuations occur.

---

## Evacuation Strategies

### Traditional (Default)

```yaml
config:
  SMART_EVACUATION: "false"
  ORCHESTRATED_RESTART: "false"
```

The agent submits evacuation requests to Nova and moves on. It does not track whether individual migrations succeed or fail. This mode has lower API overhead but provides less visibility.

### Smart Evacuation

```yaml
config:
  SMART_EVACUATION: "true"
  WORKERS: "8"
```

The agent tracks each migration to completion using a thread pool. It polls Nova for migration status every 5 seconds, retries on transient errors (including target-host retry on 409/500 conflicts), and times out after 300 seconds. Failed evacuations are recorded individually but do not abort remaining ones. This is the recommended mode for production.

### Orchestrated Evacuation

```yaml
config:
  ORCHESTRATED_RESTART: "true"
  WORKERS: "8"
```

Evacuates instances in priority-ordered phases, useful when applications have startup dependencies (e.g., databases before application servers). Phases are defined via instance metadata:

```bash
# Assign a server to restart group 1 (databases first — highest priority)
openstack server set --property instanceha:restart_group=1 db-server
openstack server set --property instanceha:restart_priority=1000 db-server

# Assign a server to restart group 2 (application servers second — lower priority)
openstack server set --property instanceha:restart_group=2 app-server
openstack server set --property instanceha:restart_priority=500 app-server
```

Servers within the same group are evacuated concurrently (up to `WORKERS` threads). Groups execute sequentially in **descending priority order** (highest priority number first). Servers without metadata are placed in individual phases with default priority 500. Like smart evacuation, a failure in one group does not prevent subsequent groups from being processed.

---

## Heartbeat Verification

When `CHECK_HEARTBEAT` is enabled, InstanceHA runs a UDP listener that receives periodic heartbeat packets from compute nodes. This provides a second failure-detection channel alongside the Nova service-list poll, reducing false-positive evacuations caused by transient Nova API issues.

A host is only treated as failed when **both channels agree** it is unreachable.

### Configuration

```yaml
# In the InstanceHa CR
spec:
  instanceHaHeartbeatPort: 7411

# In config.yaml
config:
  CHECK_HEARTBEAT: "true"
  HEARTBEAT_TIMEOUT: "120"   # Seconds without a heartbeat before marking host down
```

### HMAC Authentication

Heartbeat packets are authenticated with HMAC-SHA256 to prevent spoofing. The operator auto-generates a 32-byte HMAC key in a Kubernetes Secret (`<instance-name>-heartbeat-hmac`, e.g., `instanceha-heartbeat-hmac` for a CR named `instanceha`) during reconciliation. The key is mounted into the InstanceHA pod and distributed to compute nodes via the EDPM dataplane service.

The packet format includes a timestamp for replay protection (packets older than 120 seconds are rejected).

**Key rotation** is supported via annotation:

```bash
kubectl annotate instanceha instanceha instanceha.openstack.org/rotate-hmac-key=true
```

This copies the current key to `hmac-key-previous`, generates a new key, and removes the annotation. The receiver accepts both keys during the rotation window. After redeploying compute nodes with the new key, the previous key can be cleared:

```bash
kubectl patch secret instanceha-heartbeat-hmac -p '{"data":{"hmac-key-previous":""}}'
```

Replace `instanceha-heartbeat-hmac` with `<instance-name>-heartbeat-hmac` if your CR has a different name.

The secret name is published in `status.heartbeatHMACSecret` for integration with the EDPM dataplane operator.

### Compute Node Setup

Each compute node runs a heartbeat sender deployed via the `instanceha-monitoring` EDPM service. The sender signs packets with HMAC-SHA256 using the key automatically distributed from the HMAC Kubernetes Secret. See `docs/instanceha_heartbeat_performance.md` for scalability benchmarks (tested to 1000 nodes).

**1. Add the service to your OpenStackDataPlaneNodeSet:**

```yaml
apiVersion: dataplane.openstack.org/v1beta1
kind: OpenStackDataPlaneNodeSet
metadata:
  name: openstack-edpm
spec:
  services:
    - download-cache
    - bootstrap
    - configure-network
    # ... existing services ...
    - nova
    - instanceha-monitoring
```

The `instanceha-monitoring` OpenStackDataPlaneService is pre-defined and mounts the `instanceha-heartbeat-hmac` secret as a dataSource by default. This matches a CR named `instanceha`. For CRs with a different name, create a custom `OpenStackDataPlaneService` with the correct `secretRef` (`<instance-name>-heartbeat-hmac`).

**2. Set the required variables:**

Pass the variables via `nodeTemplate.ansible.ansibleVars` or per-node overrides in the nodeset:

```yaml
spec:
  nodeTemplate:
    ansible:
      ansibleVars:
        edpm_instanceha_monitoring_heartbeat_enabled: true
        edpm_instanceha_monitoring_heartbeat_ip: "<instanceha-ip>"
        edpm_instanceha_monitoring_heartbeat_port: 7411
        edpm_instanceha_monitoring_heartbeat_interval: 30
```

The `edpm_instanceha_monitoring_heartbeat_ip` must be the InstanceHA pod's IP on the heartbeat network. To ensure this IP remains stable across pod restarts, expose the heartbeat port via a MetalLB LoadBalancer Service and use the Service's external IP here. See [Stable IP for UDP Listeners](#stable-ip-for-udp-listeners).

For large deployments, consider placing heartbeat traffic on a dedicated network attachment to isolate it from Nova API traffic. See [Network Attachments](#network-attachments) for an example.

---

## Kdump Integration

When `CHECK_KDUMP` is enabled, InstanceHA can detect kernel crashes on compute hosts and coordinate evacuation with the kernel dump process.

### How It Works

1. A background thread listens for UDP packets on the configured port (default: 7410).
2. When the Linux kernel on a compute node panics, `fence_kdump` sends a magic-number packet to the InstanceHA pod.
3. InstanceHA identifies the sending host via reverse DNS lookup.
4. Instead of waiting for the host's heartbeat to expire, InstanceHA immediately marks the host for evacuation, skipping the power-off fencing step (the host is already crashing).
5. After evacuation, InstanceHA waits 60 seconds after the last kdump packet before re-enabling the host, giving the kernel dump process time to complete.

### Compute Node Configuration

Each compute node needs `fence_kdump` configured in `/etc/kdump.conf`:

```
fence_kdump_nodes <instanceha-pod-ip>
fence_kdump_args -p 7410
```

To ensure this IP remains stable across pod restarts, expose the kdump port via a MetalLB LoadBalancer Service. See [Stable IP for UDP Listeners](#stable-ip-for-udp-listeners).

### InstanceHA Configuration

```yaml
config:
  CHECK_KDUMP: "true"
  KDUMP_TIMEOUT: "300"   # Seconds to wait for kdump before normal evacuation
```

If a host goes down and no kdump packet arrives within `KDUMP_TIMEOUT` seconds, InstanceHA proceeds with the standard fencing and evacuation workflow.

---

## Reserved Hosts

Reserved hosts act as standby capacity. They are compute nodes that are pre-disabled in Nova with a `disabled_reason` containing "reserved". When a compute host fails, InstanceHA can automatically enable a matching reserved host to replace the lost capacity.

### Matching Strategies

- **Aggregate-based** (when `TAGGED_AGGREGATES: true`): The reserved host must be in the same host aggregate as the failed host, and the aggregate must have the evacuable tag.
- **Zone-based** (when `TAGGED_AGGREGATES: false`): The reserved host must be in the same availability zone as the failed host.

### Forced Evacuation to Reserved Host

When `FORCE_RESERVED_HOST_EVACUATION` is enabled, InstanceHA passes the reserved host as the explicit target to the Nova evacuate API, bypassing the Nova scheduler. When disabled, the scheduler chooses the best destination from all available hosts (including the newly-enabled reserved host).

```yaml
config:
  RESERVED_HOSTS: "true"
  FORCE_RESERVED_HOST_EVACUATION: "false"  # Let scheduler choose
  TAGGED_AGGREGATES: "true"
```

### Reserved Host Return on Failure

If evacuation fails and a reserved host was activated:
- If VMs landed on the reserved host (checked via `servers.list(host=target)`), the host is kept active — it has workload and cannot be a spare.
- If no VMs landed on it, the host is disabled again and returned to the pool.
- On API errors, the host is kept active as the safe default.

---

## Threshold Protection

To prevent runaway evacuations during datacenter-wide failures (network partitions, power outages), InstanceHA checks what percentage of compute services are down before proceeding.

```yaml
config:
  THRESHOLD: "50"         # Maximum percentage of failed hosts
  TAGGED_AGGREGATES: "true"
```

The threshold is calculated against active services only (already-disabled and already-forced-down hosts are excluded from the denominator). When `TAGGED_AGGREGATES` is enabled, the percentage is calculated against only the hosts in evacuable aggregates.

If the threshold is exceeded, InstanceHA logs an error, emits a `ThresholdExceeded` K8s event, and skips evacuation for that poll cycle, waiting for the situation to resolve or for an operator to intervene.

Setting `THRESHOLD` to `100` disables the check entirely (allows up to 100% of hosts to fail before blocking). Setting `THRESHOLD` to `0` blocks all evacuations since any failure exceeds a 0% threshold.

### Per-Aggregate Failure Limit

For more granular control, you can set an absolute failure limit per aggregate using the `instanceha:max_failures` metadata key on Nova aggregates. This is useful when certain aggregates have fewer hosts and the global percentage-based threshold is too coarse.

```bash
# Allow at most 2 compute failures in this aggregate before blocking evacuation
openstack aggregate set --property instanceha:max_failures=2 my-aggregate
```

When `TAGGED_AGGREGATES` is enabled and an aggregate has this metadata key, the agent counts how many failed hosts belong to that aggregate. If the count exceeds the limit, evacuation is blocked for all failed hosts in that aggregate — other aggregates are unaffected.

- The global `THRESHOLD` check runs first; the per-aggregate check is an additional filter
- If a host belongs to multiple aggregates, the most restrictive limit applies
- Setting `instanceha:max_failures=0` blocks evacuation on any failure in that aggregate
- Aggregates without the metadata key have no per-aggregate limit

---

## Multi-Region Deployments

InstanceHA operates within a single OpenStack region, scoped by the `region_name` in `clouds.yaml`. For deployments spanning multiple regions, deploy one InstanceHa CR per region, each with its own `clouds.yaml` pointing to the correct region.

Each instance is fully independent — no cross-region communication or coordination occurs. A failure in one region does not affect InstanceHA operations in other regions.

---

## Configuration Reference

### InstanceHa Custom Resource Fields

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `containerImage` | string | (env-based) | Container image for the InstanceHA agent |
| `openStackCloud` | string | `"default"` | Cloud name matching an entry in `clouds.yaml` |
| `openStackConfigMap` | string | `"openstack-config"` | ConfigMap with `clouds.yaml` |
| `openStackConfigSecret` | string | `"openstack-config-secret"` | Secret with `secure.yaml` |
| `fencingSecret` | string | `"fencing-secret"` | Secret with `fencing.yaml` |
| `instanceHaConfigMap` | string | `"instanceha-config"` | ConfigMap with `config.yaml` |
| `instanceHaKdumpPort` | int32 | `7410` | UDP port for kdump messages |
| `instanceHaHeartbeatPort` | int32 | `7411` | UDP port for heartbeat messages |
| `auth.applicationCredentialSecret` | string | none | Secret with `AC_ID` and `AC_SECRET` keys |
| `nodeSelector` | map | none | Kubernetes node placement constraints |
| `networkAttachments` | []string | none | Additional Multus network attachments |
| `caBundleSecretName` | string | none | Secret with CA certificates for TLS |
| `metricsTLS.minTLSVersion` | string | `"1.2"` | Minimum TLS version for the metrics endpoint (`"1.2"` or `"1.3"`) |
| `metricsTLS.cipherSuites` | string | `"HIGH:!aNULL:!MD5:!RC4:!3DES:!kRSA"` | Allowed TLS cipher suites (OpenSSL format) |
| `disabled` | string | `"False"` | `"True"` to disable fencing and evacuation |
| `topologyRef` | object | none | Reference to a Topology CR for pod placement |

### Agent Configuration (config.yaml)

All values are strings in the ConfigMap. The agent converts and validates them at startup.

#### Timing

| Parameter | Type | Default | Range | Description |
|-----------|------|---------|-------|-------------|
| `POLL` | int | 45 | 15–600 | Seconds between Nova API polls |
| `DELTA` | int | 30 | 10–300 | Seconds of heartbeat staleness before a service is considered failed |
| `DELAY` | int | 0 | 0–300 | Seconds to wait after fencing before starting evacuation (allows storage lock release) |
| `HASH_INTERVAL` | int | 60 | 30–300 | Seconds between health-check hash updates |
| `FENCING_TIMEOUT` | int | 30 | 5–120 | Seconds allowed for fencing operations |
| `KDUMP_TIMEOUT` | int | 30 | 5–300 | Seconds to wait for kdump messages before normal evacuation |
| `HEARTBEAT_TIMEOUT` | int | 120 | 30–600 | Seconds without a heartbeat before marking host down |

#### Evacuation Behavior

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `SMART_EVACUATION` | bool | false | Track migrations to completion instead of fire-and-forget |
| `ORCHESTRATED_RESTART` | bool | false | Enable priority-ordered evacuation phases |
| `WORKERS` | int | 4 (range: 1–50) | Thread pool size for concurrent smart/orchestrated evacuations |
| `THRESHOLD` | int | 50 (range: 0–100) | Max percentage of failed hosts before evacuation is blocked. Set to 100 to disable |
| `EVACUABLE_TAG` | string | `"evacuable"` | Metadata tag name used to identify evacuable resources |
| `TAGGED_IMAGES` | bool | true | Filter by image tag |
| `TAGGED_FLAVORS` | bool | true | Filter by flavor tag |
| `TAGGED_AGGREGATES` | bool | true | Filter by aggregate tag (also affects threshold calculation and reserved host matching) |
| `SKIP_SERVERS_WITH_NAME` | list | `[]` (empty) | Server name glob patterns (comma-separated) to exclude from evacuation |
| `EVACUATION_RETRIES` | int | 5 (range: 1–20) | Max per-instance evacuation retry attempts before giving up |

#### Host Recovery

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `LEAVE_DISABLED` | bool | false | Keep hosts disabled after evacuation (requires manual re-enable) |
| `FORCE_ENABLE` | bool | false | Re-enable hosts immediately without waiting for migrations to complete |

#### Reserved Hosts

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `RESERVED_HOSTS` | bool | false | Enable automatic reserved host management |
| `FORCE_RESERVED_HOST_EVACUATION` | bool | false | Direct evacuations to the reserved host instead of using the Nova scheduler |

#### Failure Detection

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `CHECK_KDUMP` | bool | false | Enable kdump detection via UDP listener |
| `CHECK_HEARTBEAT` | bool | false | Enable heartbeat verification via UDP listener |

#### Security

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `SSL_VERIFY` | bool | true | Verify TLS certificates for Redfish and Kubernetes API connections |

#### Service Control

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `DISABLED` | bool | false | Skip all evacuation logic (health checks continue). Overridden by CR `spec.disabled` |
| `LOGLEVEL` | string | `"INFO"` | Log verbosity: `DEBUG`, `INFO`, `WARNING`, `ERROR`, `CRITICAL` |

---

## Operational Procedures

### Disabling InstanceHA Temporarily

For planned maintenance, disable evacuations without removing the resource:

```bash
oc patch instanceha instanceha -n openstack --type merge \
  -p '{"spec": {"disabled": "True"}}'
```

The agent continues running and reporting health but takes no fencing or evacuation actions. Re-enable by setting `disabled` back to `"False"`.

Alternatively, set `DISABLED: "true"` in the ConfigMap for the same effect at the agent level.

### Investigating a Failed Evacuation

Services with `disabled_reason` containing `FAILED` require manual intervention:

```bash
# Find failed services
openstack compute service list --long | grep FAILED

# Check K8s events for details
oc get events -n openstack --field-selector involvedObject.name=instanceha

# After investigating and resolving the issue, re-enable:
openstack compute service set --enable <host> nova-compute
openstack compute service set --unset-forced-down <host> nova-compute
```

### Adding a New Compute Host

1. Add fencing credentials for the new host to `fencing.yaml` and update the Secret.
2. If using aggregate-based tagging, add the host to an aggregate with `evacuable=true`.
3. If using heartbeat verification, configure the heartbeat sender on the new host.
4. No InstanceHA restart is needed — the agent discovers new hosts automatically on its next poll.

### Tuning Detection Speed vs. False Positives

- **Faster detection**: Lower `DELTA` (minimum 10s) and `POLL` (minimum 15s). Risk: transient network issues may trigger false evacuations.
- **Fewer false positives**: Raise `DELTA` (up to 300s) and enable `CHECK_HEARTBEAT` for dual-channel verification. Risk: longer time before instances are recovered.
- A reasonable starting point for production is `DELTA: 30` with `POLL: 45` and `CHECK_HEARTBEAT: true`.

### Checking InstanceHA Health

The agent exposes three HTTP endpoints on port 8080:

- **Liveness** (`/`): Returns the latest health-check hash, updated every `HASH_INTERVAL` seconds. Used by the Kubernetes liveness probe.
- **Readiness** (`/healthz`): Reports whether the agent has completed its first successful Nova API poll. The pod is not marked ready until this check passes.
- **Metrics** (`/metrics`): Prometheus-format metrics covering fencing, evacuation, host state transitions, and poll loop health. See [Prometheus Metrics](#prometheus-metrics) for the full metric catalog.

```bash
# Check pod health
oc get pods -n openstack -l service=instanceha

# View agent logs
oc logs -n openstack deployment/instanceha

# View K8s events
oc describe instanceha instanceha -n openstack

# Scrape Prometheus metrics
oc exec deployment/instanceha -- curl -s http://localhost:8080/metrics
```

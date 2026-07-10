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

1. **Detection**: The agent polls the Nova API every `POLL` seconds (default: 45). It queries all `nova-compute` services and checks each one's `updated_at` timestamp. A service is considered failed if it is reported as `down` or if its `updated_at` timestamp is older than `DELTA` seconds (default: 30). When heartbeat verification is enabled (`CHECK_HEARTBEAT`), both the Nova service-list poll and the UDP heartbeat channel must agree that a host is unreachable before it is marked as failed.

2. **Safety checks**: Before acting, the agent verifies that the percentage of failed hosts does not exceed the `THRESHOLD` (default: 50%) -- calculated against only active services, excluding already-disabled and already-forced-down hosts -- and that at least one `nova-scheduler` is running. These checks prevent cascading evacuations during infrastructure-wide outages. A `ThresholdExceeded` K8s event is emitted when the limit is hit.

3. **Fencing**: The failed host is powered off via its baseboard management controller (BMC) using IPMI, Redfish, or the Metal3 Kubernetes API. Fencing ensures the host is truly offline before evacuation begins, preventing split-brain scenarios where the old and new copies of an instance run simultaneously. The server is locked in Nova (API microversion 2.73, reason `instanceha-evacuation`) to prevent concurrent operations.

4. **Evacuation**: The agent marks the compute service as `forced_down` and `disabled` in Nova, then calls the evacuate API for each eligible instance. Three strategies are available -- traditional (fire-and-forget), smart (tracked to completion), and orchestrated (priority-ordered phases). All strategies use continue-on-failure semantics: a single failed evacuation does not abort the remaining ones.

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
| `HeartbeatCliff` | Warning | Sudden heartbeat loss detected -- possible network partition, fencing skipped |
| `HeartbeatCliffExpired` | Warning | Cliff detection expired after N consecutive cycles -- proceeding with fencing |
| `FencingRateLimited` | Warning | Per-cycle fencing cap (`MAX_HOSTS_PER_CYCLE`) hit, excess hosts deferred to next cycle |
| `AllServicesStale` | Warning | All active compute services appear stale -- likely Nova API issue, fencing skipped |

Events can be viewed with `oc describe instanceha <name>` or `oc get events --field-selector involvedObject.name=<name>`.

### Prometheus Metrics

The agent exposes Prometheus metrics at `http://<pod-ip>:8080/metrics`, covering the full evacuation lifecycle: host failure detection, fencing, evacuation, recovery, and poll loop health. Key metric families:

- **Counters**: `instanceha_fencing_total`, `instanceha_evacuation_total`, `instanceha_host_down_total`, `instanceha_poll_cycles_total`, and others -- track cumulative event counts with `host` and `result` labels.
- **Gauges**: `instanceha_poll_consecutive_failures`, `instanceha_hosts_processing`, `instanceha_k8s_api_reachable` -- track current state.
- **Histograms**: `instanceha_instance_evacuation_duration_seconds` -- track per-instance evacuation latency.

The infra-operator automatically creates a Kubernetes Service (`<instance-name>-metrics`) that the telemetry-operator discovers and scrapes via the COO Prometheus. **No manual configuration is needed when the telemetry-operator is deployed.**

See [instanceha_prometheus.md](instanceha_prometheus.md) for the complete metric catalog, scraping setup (including PodMonitor for user workload monitoring), TLS configuration, alert rules, Grafana dashboard queries, and PromQL examples.

### Graceful Shutdown

When the pod receives SIGTERM (during scaling, rolling updates, or node drain), InstanceHA immediately marks itself as not-ready (`ready = False`), stops the heartbeat and kdump listeners, shuts down the health check HTTP server, and sets the shutdown flag. In-flight fencing and evacuation workers check this flag between operations and finish promptly. The Deployment is configured with a 45-second termination grace period.

> **Note:** The worst-case shutdown time depends on `FENCING_TIMEOUT` (default 30s) plus thread-pool drain time. If you increase `FENCING_TIMEOUT` beyond the default, consider increasing `terminationGracePeriodSeconds` in the Deployment spec accordingly to avoid SIGKILL during in-flight fencing or evacuation. If the pod is killed mid-operation, orphan recovery at the next startup handles any partially-processed hosts.

### Network Partition Detection

Two complementary mechanisms prevent the agent from fencing healthy hosts when the pod's worker node is network-isolated. See [instanceha_architecture.md -- Fail-Open vs Fail-Closed Semantics](instanceha_architecture.md#fail-open-vs-fail-closed-semantics) for the rationale behind each gate's behavior on error.

**K8s API connectivity check:**
- A background thread pings the K8s API every `K8S_API_CHECK_INTERVAL` seconds (default: 15) using the in-cluster service account.
- After 3 consecutive failures, the agent marks itself not-ready and blocks all fencing until connectivity restores and a Nova poll succeeds.
- The `instanceha_k8s_api_reachable` Prometheus gauge tracks the current state (1=ok, 0=isolated).

**Heartbeat cliff detection:**
- The agent tracks the count of active heartbeat hosts between poll cycles.
- If the count drops by more than `HEARTBEAT_CLIFF_THRESHOLD`% (default: 50%) in a single cycle (minimum 3 hosts previously active), fencing is skipped for that cycle. A sudden mass-silence is far more likely to indicate a listener-side network issue than simultaneous host failures.
- A `HeartbeatCliff` K8s event (Warning) is emitted and the `instanceha_heartbeat_cliff_total` counter is incremented.
- If the cliff persists for `HEARTBEAT_CLIFF_MAX_CYCLES` consecutive cycles (default: 3), the agent accepts the current state as a genuine failure, resets the baseline, and proceeds with fencing. This prevents a sustained infrastructure failure (e.g., a rack switch outage) from permanently suppressing fencing.

```yaml
config:
  K8S_API_CHECK_INTERVAL: "15"
  HEARTBEAT_CLIFF_THRESHOLD: "50"
  HEARTBEAT_CLIFF_MAX_CYCLES: "3"
```

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

A common pattern is to separate heartbeat traffic from the API network. Heartbeat packets are small and frequent -- putting them on the same network as the Nova API can add noise, and a congested API network could delay heartbeats enough to trigger false positives. Using a dedicated network for heartbeat avoids both problems:

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

When only a single `networkAttachments` entry is provided (the typical case), all traffic -- Nova API, kdump, and heartbeat -- shares that interface.

### Stable IP for UDP Listeners

By default, the InstanceHA pod receives a dynamic IP from the Multus IPAM pool. If the pod restarts, it may get a different IP, and compute nodes sending kdump or heartbeat packets to the old address will fail until reconfigured.

To provide a stable IP that survives pod restarts, create a MetalLB-backed LoadBalancer Service that fronts the InstanceHA UDP ports. Compute nodes send packets to the Service's external IP, and MetalLB routes them to the pod regardless of its current address.

#### Heartbeat only (no kdump)

When kdump detection is disabled (`CHECK_KDUMP: "false"`), the heartbeat Service can share an existing OpenStack IP because `externalTrafficPolicy: Local` is not needed -- the sender's hostname is embedded in the HMAC-authenticated packet payload, so source IP rewriting does not break heartbeat identification:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: instanceha-heartbeat
  namespace: openstack
  annotations:
    metallb.universe.tf/address-pool: internalapi
    metallb.universe.tf/allow-shared-ip: internalapi
    metallb.universe.tf/loadBalancerIPs: 172.17.0.80
spec:
  type: LoadBalancer
  selector:
    service: instanceha
  ports:
  - name: heartbeat
    port: 7411
    targetPort: 7411
    protocol: UDP
```

Replace `172.17.0.80` with the shared IP already used by your `internalapi` services. The `allow-shared-ip` annotation lets this Service share the same IP with other OpenStack services (e.g., keystone, nova, cinder) -- since port 7411/UDP doesn't conflict with any existing service ports, sharing is safe and avoids consuming an additional IP.

#### Heartbeat + kdump

When kdump detection is enabled (`CHECK_KDUMP: "true"`), the kdump listener identifies the crashing host via reverse DNS lookup on the packet's source IP. This requires `externalTrafficPolicy: Local` to preserve the original source IP -- without it, kube-proxy masquerades (SNATs) the source IP when forwarding traffic to the pod, replacing the compute node's real IP with an internal node address and causing the reverse DNS lookup to fail.

> **Important:** MetalLB requires all Services sharing the same IP to use the same `externalTrafficPolicy`. If the existing OpenStack services on `172.17.0.80` use `externalTrafficPolicy: Cluster` (the default), you **cannot** add a new Service with `externalTrafficPolicy: Local` on the same IP -- MetalLB will reject the configuration. You must use a **different IP** (and typically a different network) for the kdump+heartbeat Service.

When using a [dedicated heartbeat network](#dedicated-heartbeat-network), place the MetalLB Service on that network's address pool so UDP traffic stays off the API network entirely:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: instanceha-udp
  namespace: openstack
  annotations:
    metallb.universe.tf/address-pool: heartbeat-net
    metallb.universe.tf/loadBalancerIPs: 172.20.0.80
spec:
  type: LoadBalancer
  externalTrafficPolicy: Local
  selector:
    service: instanceha
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

Replace `172.20.0.80` with an IP from your heartbeat network's MetalLB address pool (the pool must cover the `172.20.0.0/24` range or whichever range your dedicated network uses). Because this is a separate pool from `internalapi`, there is no `externalTrafficPolicy` conflict with existing OpenStack services.

If you do not have a dedicated heartbeat network, you can still use the `internalapi` pool -- but you must pick an **unused** IP and omit the `allow-shared-ip` annotation:

```yaml
    metallb.universe.tf/address-pool: internalapi
    metallb.universe.tf/loadBalancerIPs: 172.17.0.85
```

> **Note:** The `address-pool` value must match the actual MetalLB IPAddressPool name. Verify pool names with `oc get ipaddresspools -n metallb-system`.

Then configure compute nodes to send kdump and heartbeat packets to the Service IP instead of the pod IP.

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

Fencing (also called STONITH -- Shoot The Other Node In The Head) ensures that a failed compute host is truly powered off before its instances are rebuilt elsewhere. Without fencing, a host that appears down but is actually partitioned could continue running instances, leading to data corruption or IP conflicts. See [instanceha_architecture.md -- Fencing Safety Model](instanceha_architecture.md#fencing-safety-model) for the 10-gate safety model and internal implementation details.

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

Redfish operations attempt up to 3 times with per-request timeout of `FENCING_TIMEOUT` (configurable range: 5-120 seconds). All URLs are validated to prevent SSRF attacks (localhost and link-local addresses are blocked).

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

See [instanceha_architecture.md -- Evacuation Mechanisms](instanceha_architecture.md#evacuation-mechanisms) for the internal implementation details, code flow, and retry logic.

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

When evacuating many VMs from a single host, concurrent API calls can overwhelm the control plane and cause timeouts. Use `EVACUATION_STAGGER` to spread the submissions:

```yaml
config:
  SMART_EVACUATION: "true"
  WORKERS: "8"
  EVACUATION_STAGGER: "2"   # 2-second delay between each VM's evacuation start
```

With `EVACUATION_STAGGER: 2` and 30 VMs, the submissions spread over 60 seconds, avoiding thundering-herd timeouts on the control plane.

### Orchestrated Evacuation

```yaml
config:
  ORCHESTRATED_RESTART: "true"
  WORKERS: "8"
```

Evacuates instances in priority-ordered phases, useful when applications have startup dependencies (e.g., databases before application servers). Phases are defined via instance metadata:

```bash
# Assign a server to restart group 1 (databases first -- highest priority)
openstack server set --property instanceha:restart_group=1 db-server
openstack server set --property instanceha:restart_priority=1000 db-server

# Assign a server to restart group 2 (application servers second -- lower priority)
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

This copies the current key to `hmac-key-previous`, generates a new key, and removes the annotation. The receiver accepts both keys during the rotation window.

After redeploying compute nodes with the new key (via `servicesOverride: [instanceha-monitoring]`), the controller **automatically clears `hmac-key-previous`** once the EDPM deployment completes and the secret hash is back in sync across all NodeSets. This uses a two-phase state machine tracked in `status.hmacKeyRotationSynced`.

If no `OpenStackDataPlaneNodeSets` exist in the namespace, the previous key is cleared immediately.

> **Prerequisite:** The `instanceha-monitoring` service must be listed in the NodeSet's `spec.services` so its secret hash is tracked. The service's `secretRef.name` must match the actual secret name (`<instance-name>-heartbeat-hmac`). For non-default CR names, create a custom `OpenStackDataPlaneService` with the correct `secretRef`.

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
- If VMs landed on the reserved host (checked via `servers.list(host=target)`), the host is kept active -- it has workload and cannot be a spare.
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

### Threshold Is Evaluated After Evacuability Filtering

The global `THRESHOLD` check does **not** count every host Nova reports as down. It runs **after** `_prepare_evacuation_resources()` has narrowed the candidate list. Only hosts that would actually be fenced and evacuated are included in the numerator. The denominator counts **all** active evacuable compute services (including healthy hosts), while the numerator counts only post-filter down hosts -- empty compute nodes are excluded from the numerator but still counted in the denominator, so idle host pools lower the failure percentage.

If evacuability filtering removes every stale host (for example, all down hosts had no VMs), the global threshold check is **skipped** entirely -- even when many hosts were stale upstream.

Hosts in the `to_resume` list (interrupted evacuations) are not counted in the threshold calculation and individually bypass per-aggregate filtering. However, if the global `THRESHOLD` is breached by new fencing candidates, the function returns early -- blocking `to_resume` processing as well. When no new candidates survive filtering, the threshold check is skipped and `to_resume` proceeds unimpeded.

**What is removed before the threshold check:**

| Filter | Effect on threshold numerator |
|--------|------------------------------|
| Hosts with no VMs | Excluded -- empty compute nodes are not counted |
| `TAGGED_FLAVORS` / `TAGGED_IMAGES` | Hosts with no evacuable VMs on them are excluded |
| `TAGGED_AGGREGATES` | Hosts outside evacuable aggregates are excluded |
| Heartbeat gate (Gate 4, `CHECK_HEARTBEAT=true` only) | Hosts still sending heartbeats are excluded earlier in the cycle |
| Nova API query failure (no-VM/flavor filters) | Host is kept -- query errors fail-open so hosts are not incorrectly skipped |

**Denominator behavior:**

| Setting | Denominator |
|---------|-------------|
| `TAGGED_AGGREGATES: false` | All active compute services (not disabled, not forced-down) |
| `TAGGED_AGGREGATES: true` | Active compute services in evacuable aggregates only |

**Example:** Nova reports 40 hosts as down across the region. Only 8 are in evacuable aggregates and carry evacuable workloads. With `TAGGED_AGGREGATES: true` and `THRESHOLD: 50`, the agent calculates `8 / total_evacuable x 100` -- not `40 / total_active x 100`. A datacenter-wide outage that mostly affects non-evacuable or untagged hosts may **not** trip the global threshold, even though many physical hosts are unhealthy.

**Why this design:** The threshold is a guard against **runaway evacuation**, not a general infrastructure health monitor. Counting hosts InstanceHA would never evacuate would cause false threshold trips during partial outages (for example, a cell where most hosts are intentionally untagged).

**Earlier gates still protect against infrastructure-wide anomalies:**

These run after `_filter_processing_hosts` (in-flight hosts excluded) but **before** evacuability filtering (`_prepare_evacuation_resources`):

- **All-services-stale** -- triggers when every active compute service appears stale in one cycle (>=3 active services, after excluding in-flight hosts). Catches Nova API or control-plane issues that make all services look down.
- **Heartbeat cliff detection** -- only when `CHECK_HEARTBEAT=true`; triggers on a sudden mass loss of heartbeat hosts, regardless of evacuability tags.

After all gates pass, **`MAX_HOSTS_PER_CYCLE`** caps how many hosts are fenced per cycle, limiting blast radius even when every gate allows fencing.

These gates do **not** catch partial infrastructure failures -- for example, an entire Nova cell going offline while other cells remain healthy. In that scenario only ~33% of services may appear stale (in a 3-cell deployment), which does not trigger the all-services-stale check.

**Operational mitigations for partial infrastructure failures:**

1. **Per-aggregate thresholds** -- set `instanceha:max_failures` on cell, rack, or row aggregates so absolute failure counts are enforced within each failure domain, independent of the global percentage.
2. **Lower `THRESHOLD`** -- when using `TAGGED_AGGREGATES`, tune against the evacuable host pool, not total cluster size. A 10% threshold on 100 evacuable hosts blocks at 10 simultaneous evacuations.
3. **Prometheus alerting** -- alert on `instanceha_host_down_total` and Nova service staleness independently of InstanceHA's threshold gate. The threshold protects against cascading evacuation; it is not a substitute for infrastructure monitoring.
4. **Cell maintenance** -- disable compute services in Nova before taking a cell offline. InstanceHA ignores disabled hosts in both the numerator and denominator.

> **Note:** A pre-filter "raw" threshold that counts all stale services before evacuability filtering is not implemented today. If your deployment has large non-evacuable host pools and cell-level failure domains, rely on per-aggregate `instanceha:max_failures` and external monitoring rather than the global `THRESHOLD` alone.

### Per-Aggregate Failure Limit

For more granular control, you can set an absolute failure limit per aggregate using the `instanceha:max_failures` metadata key on Nova aggregates. This is useful when certain aggregates have fewer hosts and the global percentage-based threshold is too coarse.

```bash
# Allow at most 2 compute failures in this aggregate before blocking evacuation
openstack aggregate set --property instanceha:max_failures=2 my-aggregate
```

When `TAGGED_AGGREGATES` is enabled and an aggregate has this metadata key, the agent counts how many failed hosts belong to that aggregate. If the count exceeds the limit, evacuation is blocked for all failed hosts in that aggregate -- other aggregates are unaffected.

- Both checks run after evacuability filtering; the global `THRESHOLD` check runs first, then the per-aggregate check is an additional filter
- If a host belongs to multiple aggregates, the most restrictive limit applies
- Setting `instanceha:max_failures=0` blocks evacuation on any failure in that aggregate
- Aggregates without the metadata key have no per-aggregate limit

---

## Multi-Region Deployments

InstanceHA operates within a single OpenStack region, scoped by the `region_name` in `clouds.yaml`. For deployments spanning multiple regions, deploy one InstanceHa CR per region, each with its own `clouds.yaml` pointing to the correct region.

Each instance is fully independent -- no cross-region communication or coordination occurs. A failure in one region does not affect InstanceHA operations in other regions.

---

## Configuration Reference

See [instanceha_architecture.md -- Configuration Options Reference](instanceha_architecture.md#configuration-options-reference) for developer-facing details including validation rules, code usage, and test coverage for each option.

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
| `POLL` | int | 45 | 15-600 | Seconds between Nova API polls |
| `DELTA` | int | 30 | 10-300 | Seconds of heartbeat staleness before a service is considered failed |
| `DELAY` | int | 0 | 0-300 | Seconds to wait after fencing before starting evacuation (allows storage lock release) |
| `HASH_INTERVAL` | int | 60 | 30-300 | Seconds between health-check hash updates |
| `FENCING_TIMEOUT` | int | 30 | 5-120 | Seconds allowed for fencing operations |
| `KDUMP_TIMEOUT` | int | 30 | 5-300 | Seconds to wait for kdump messages before normal evacuation |
| `HEARTBEAT_TIMEOUT` | int | 120 | 30-600 | Seconds without a heartbeat before marking host down |

#### Evacuation Behavior

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `SMART_EVACUATION` | bool | false | Track migrations to completion instead of fire-and-forget |
| `ORCHESTRATED_RESTART` | bool | false | Enable priority-ordered evacuation phases |
| `WORKERS` | int | 4 (range: 1-100) | Thread pool size for concurrent smart/orchestrated evacuations |
| `THRESHOLD` | int | 50 (range: 0-100) | Max percentage of failed hosts before evacuation is blocked. Set to 100 to disable |
| `EVACUABLE_TAG` | string | `"evacuable"` | Metadata tag name used to identify evacuable resources |
| `TAGGED_IMAGES` | bool | true | Filter by image tag |
| `TAGGED_FLAVORS` | bool | true | Filter by flavor tag |
| `TAGGED_AGGREGATES` | bool | true | Only process hosts in aggregates with the evacuable tag (also affects threshold calculation and reserved host matching) |
| `SKIP_SERVERS_WITH_NAME` | list | `[]` (empty) | Server name glob patterns (comma-separated) to exclude from evacuation |
| `EVACUATION_MAX_THREADS` | int | 32 (range: 1-512) | Total evacuation thread budget shared across all hosts. Per-host concurrency is `EVACUATION_MAX_THREADS / WORKERS`. Increase for faster single-host evacuation; decrease to reduce control plane pressure |
| `EVACUATION_RETRIES` | int | 5 (range: 1-20) | Max per-instance evacuation retry attempts before giving up |
| `EVACUATION_STAGGER` | int | 0 (range: 0-60) | Seconds between each VM's evacuation start (0 = all start immediately). Reduces control plane pressure when evacuating many VMs from a single host |

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
| `HEARTBEAT_CLIFF_THRESHOLD` | int | 50 (range: 10-100) | Percentage drop in active heartbeat hosts that triggers cliff detection (skips fencing for that cycle) |
| `HEARTBEAT_CLIFF_MAX_CYCLES` | int | 3 (range: 1-20) | Consecutive cliff detection cycles before accepting the state as genuine and proceeding with fencing |
| `K8S_API_CHECK_INTERVAL` | int | 15 (range: 5-120) | Seconds between Kubernetes API reachability checks |

#### Safety Limits

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `MAX_HOSTS_PER_CYCLE` | int | 10 (range: 1-200) | Maximum number of hosts that can be fenced in a single poll cycle. Excess hosts are deferred to the next cycle. |

`THRESHOLD` (documented above in Evacuation Behavior) also acts as a safety limit -- it blocks all fencing when the percentage of failed hosts exceeds the configured value.

#### Security

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `SSL_VERIFY` | bool | true | Verify TLS certificates for Redfish fencing connections |

#### Service Control

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `DISABLED` | bool | false | Skip fencing and evacuation (monitoring and host re-enabling continue). Overridden by CR `spec.disabled` |
| `LOGLEVEL` | string | `"INFO"` | Log verbosity: `DEBUG`, `INFO`, `WARNING`, `ERROR`, `CRITICAL` |

---

## Upgrading InstanceHA

### How Upgrades Work

InstanceHA uses a **Recreate** deployment strategy with a single replica. During an upgrade the old pod is terminated before the new one starts, creating a brief monitoring gap. This is intentional -- running two InstanceHA agents simultaneously would cause duplicate fencing and evacuation operations.

The upgrade sequence is:

1. Kubernetes sends SIGTERM to the running agent
2. The agent sets its shutdown flag, stops accepting new fencing/evacuation work, and waits for in-flight operations to complete (up to 45 seconds)
3. The pod terminates and the new pod starts
4. On startup, the agent runs **orphan reconciliation**: it scans for hosts left in an inconsistent state (fenced but not yet evacuated) and marks them for evacuation resume

### Monitoring Gap

The gap between the old pod stopping and the new pod completing its first poll cycle is typically 45-120 seconds. The gap is dominated by pod scheduling time and the first poll cycle (default 45 seconds). If in-flight fencing is running when SIGTERM arrives, the termination grace period (45 seconds) bounds how long the old pod lingers before Kubernetes kills it. During this window, host failures are not detected. This is acceptable for most deployments because:

- Failures that occur during the gap are detected on the first poll after startup
- Orphan reconciliation recovers any partially-completed fencing operations
- The heartbeat detection system (`CHECK_HEARTBEAT`) resumes with conservative cliff-detection defaults, avoiding false positives from the stale heartbeat state

If this gap is unacceptable for your SLA, schedule upgrades during a maintenance window with InstanceHA disabled.

### Upgrade Procedure

```bash
# 1. Verify current state -- no in-progress evacuations
oc get events -n openstack --field-selector involvedObject.name=instanceha | grep -i fenc

# 2. Update the container image (triggers rolling restart)
oc patch instanceha instanceha -n openstack --type merge \
  -p '{"spec": {"containerImage": "new-image:tag"}}'

# 3. Wait for the new pod to become ready
oc rollout status deployment/instanceha -n openstack --timeout=120s

# 4. Verify startup reconciliation ran cleanly
oc logs deployment/instanceha -n openstack | grep -i reconcil
```

### Configuration Changes Between Versions

New configuration keys added in newer versions use safe defaults and do not require manual intervention. The `ConfigManager` ignores unknown keys in `config.yaml`, so rolling back to an older version with newer config keys is also safe.

If you change configuration values at the same time as upgrading, the new values take effect when the new pod starts. There is no hot-reload -- all config is read at startup.

### EDPM Heartbeat Service Compatibility

The heartbeat sender on compute nodes (`instanceha-heartbeat.service`) and the InstanceHA agent are independently versioned. The heartbeat packet format is stable:

- Adding HMAC authentication is backwards-compatible -- unauthenticated packets are accepted when no HMAC keys are loaded
- When enabling HMAC for the first time, deploy the HMAC key to compute nodes via EDPM **before** loading HMAC keys on the agent (HMAC enforcement is automatic when keys are present -- there is no separate toggle)
- The dual-key rotation mechanism allows upgrading the HMAC key without downtime (see [HMAC Key Rotation](#hmac-key-rotation))

### Rollback

Since InstanceHA is stateless (all state lives in Nova's service list and the Kubernetes API), rollback is the same as an upgrade -- change the container image back. Orphan reconciliation handles any inconsistent state left by the interrupted version.

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
openstack compute service set --enable --up <host> nova-compute
```

### Responding to a False-Positive Evacuation

A false positive occurs when InstanceHA fences and evacuates a host that was actually healthy -- typically caused by a network partition between the control plane and the compute node. Use this procedure to identify, remediate, and prevent recurrence.

**Step 1: Identify the false positive**

```bash
# Check K8s events for recent fencing and evacuation actions
oc get events -n openstack --field-selector involvedObject.name=instanceha \
  --sort-by='.lastTimestamp' | tail -20

# Check if the host was actually reachable (e.g. via out-of-band)
# A host that was fenced but was actually up is a false positive
ipmitool -I lanplus -H <bmc-ip> -U <user> -E power status

# Look for cliff detection events -- if the cliff was triggered but
# then expired, multiple hosts may have been fenced simultaneously
oc get events -n openstack --field-selector reason=HeartbeatCliffExpired
```

**Step 2: Check Prometheus metrics**

```bash
# Scrape current metrics
oc exec deployment/instanceha -- curl -s http://localhost:8080/metrics

# Key indicators of false positives:
# - instanceha_heartbeat_cliff_total increasing without real failures
# - instanceha_fencing_total increasing faster than expected
# - instanceha_all_services_stale_total > 0 (suggests control-plane issue, not host failure)
# - instanceha_fencing_rate_limited_total > 0 (rate limiter firing -- many hosts detected down)
```

**Step 2.5: Check for cascading false-positive evacuation**

If multiple hosts were fenced in the same time window, evacuated VMs may have landed on hosts that are themselves being fenced -- creating a cascade.

```bash
# List all hosts fenced recently
oc get events -n openstack --field-selector reason=FencingSucceeded \
  --sort-by='.lastTimestamp' | tail -10

# If multiple hosts were fenced within the same few minutes,
# immediately disable InstanceHA to stop the cascade:
oc patch instanceha instanceha -n openstack --type merge \
  -p '{"spec": {"config": {"DISABLED": "True"}}}'

# Then verify evacuation targets are not themselves being fenced
# before proceeding to re-enable hosts
```

**Step 3: Immediate remediation**

```bash
# Re-enable the falsely fenced host
openstack compute service set --enable --up <host> nova-compute

# If VMs were evacuated, they are now running on other hosts.
# The fenced host was powered off. With shared storage (Ceph), VM disks
# are now attached to the new instances. With local ephemeral storage,
# the old disks remain on the powered-off host but VMs have been rebuilt.
# No manual VM migration back is needed, but verify VM health:
openstack server list --host <evacuation-target-host> --all-projects
```

**Step 4: Preventive tuning**

If false positives recur, adjust these settings:

| Setting | Effect | Recommended change |
|---------|--------|--------------------|
| `DELTA` | How long a service must be down before action | Increase (e.g. 30->60) |
| `CHECK_HEARTBEAT` | Require heartbeat loss before fencing | Enable (`True`) |
| `HEARTBEAT_TIMEOUT` | Heartbeat staleness threshold | Increase (e.g. 60->120) |
| `HEARTBEAT_CLIFF_MAX_CYCLES` | Cycles before cliff detection expires | Increase (e.g. 3->5) |
| `THRESHOLD` | Max % of hosts fenced per cycle | Lower (e.g. 50->10) |
| `MAX_HOSTS_PER_CYCLE` | Absolute cap on hosts fenced per cycle | Lower (e.g. 10->3) |

Enabling heartbeat verification (`CHECK_HEARTBEAT: "True"`) on a different network provides a second detection channel that significantly reduces false positives from control-plane network (usually internalapi) issues.

### Adding a New Compute Host

1. Add fencing credentials for the new host to `fencing.yaml` and update the Secret.
2. If using aggregate-based tagging, add the host to an aggregate with `evacuable=true`.
3. If using heartbeat verification, configure the heartbeat sender on the new host.
4. No InstanceHA restart is needed -- the agent discovers new hosts automatically on its next poll.

### Tuning Detection Speed vs. False Positives

- **Faster detection**: Lower `DELTA` (minimum 10s) and `POLL` (minimum 15s). Risk: transient network issues may trigger false evacuations.
- **Fewer false positives**: Raise `DELTA` (up to 300s) and enable `CHECK_HEARTBEAT` for dual-channel verification. Risk: longer time before instances are recovered.
- A reasonable starting point for production is `DELTA: 30` with `POLL: 45` and `CHECK_HEARTBEAT: true`.

### Transient Network Failures

When a network disruption occurs (switch reboot, spanning tree reconvergence, brief routing flap), multiple compute nodes may appear down simultaneously even though the hardware is healthy. InstanceHA has a layered defense against acting on these false signals.

The effectiveness of each gate depends on the size of the environment. The cliff detector uses a **percentage** threshold (`HEARTBEAT_CLIFF_THRESHOLD`, default 50%) -- it triggers when the percentage of heartbeat hosts that disappeared in a single cycle exceeds that value. This means the same 20-host ToR switch failure looks very different depending on cluster size:

| Cluster size | 20 hosts lost | % of active heartbeats | Cliff triggers? (at 50%) |
|-------------|---------------|----------------------|--------------------------|
| 30 nodes | 20 of 30 | 67% | Yes |
| 40 nodes | 20 of 40 | 50% | Yes (exactly at threshold) |
| 60 nodes | 20 of 60 | 33% | **No** -- below threshold |
| 200 nodes | 20 of 200 | 10% | **No** -- well below threshold |

In small clusters, cliff detection is the primary defense against transient events. In large clusters, the percentage drop from a single switch failure may not reach the threshold -- but the global `THRESHOLD` and `MAX_HOSTS_PER_CYCLE` gates take over as the safety net. See [Tuning by cluster size](#tuning-by-cluster-size) below.

**Timeline: ToR switch reboots in a 40-node cluster (affects 20 nodes for ~90 seconds)**

This example uses a 40-node cluster with default settings (`POLL: 45`, `DELTA: 30`, `HEARTBEAT_CLIFF_THRESHOLD: 50`, `HEARTBEAT_CLIFF_MAX_CYCLES: 3`). All 40 nodes have active heartbeats. A ToR switch serving 20 nodes reboots.

| Time | What happens | Gate |
|------|-------------|------|
| T+0s | Switch goes down. Heartbeat packets from 20 hosts stop arriving. Nova API may still report services as `up` (stale data within `DELTA`). | -- |
| T+30s | Nova services exceed `DELTA` (30s) and are reported as `down`. InstanceHA detects 20 stale services. | Gate 2: all-stale check |
| T+30s | Only 20 of 40 services are stale -- the all-services-stale circuit breaker does not fire. Processing continues. | |
| T+30s | `_filter_reachable_hosts()` runs. The cliff detector sees that 20 of 40 previously active heartbeat hosts disappeared -- exactly 50%, meeting the cliff threshold. Fencing is skipped for this cycle. | Gate 3: cliff detection |
| T+45s | Next poll cycle. Heartbeats still absent. Cliff detector fires again (consecutive count: 2 of 3). Fencing skipped. | Gate 3 |
| T+90s | Switch comes back. Heartbeat packets resume. | -- |
| T+90s | Next poll cycle. Nova services recover (within `DELTA`). Heartbeats are fresh. Cliff detector sees hosts returning -- consecutive cliff count resets to 0. No hosts are fenced. | -- |
| **Result** | Zero false fencing operations. | |

**What if the outage lasts longer than `HEARTBEAT_CLIFF_MAX_CYCLES * POLL`?**

After 3 consecutive cliff detections (3 x 45s = ~135s with default settings), the cliff detector expires -- it assumes the loss is genuine, not transient. At this point, remaining defenses take over:

1. **`THRESHOLD`** (default 50%): Blocks fencing when the percentage of post-filter failed hosts is **strictly greater than** the threshold (the check is `>`, not `>=`). In our 40-node example, 20/40 = 50% equals the default threshold and does **not** block -- fencing would proceed subject to the per-cycle cap. If 21 of 40 hosts were down (52.5%), the threshold would block fencing for that cycle.
2. **`MAX_HOSTS_PER_CYCLE`** (default 10): Even when the threshold passes, only 10 hosts are fenced per cycle. The remaining hosts wait for the next cycle, giving more time for recovery.
3. **Per-aggregate thresholds** (`instanceha:max_failures`): If the 20 hosts share a host aggregate (e.g., same rack), the per-aggregate limit can block fencing for that group independently.

These gates are evaluated in sequence -- a host must pass all of them before fencing begins.

**Key configuration knobs for network resilience**

| Setting | Default | Effect on transient events |
|---------|---------|---------------------------|
| `HEARTBEAT_CLIFF_MAX_CYCLES` | 3 | How many cycles to wait before accepting mass heartbeat loss as genuine. Higher = more tolerant of long network events, but slower response to real failures. |
| `HEARTBEAT_CLIFF_THRESHOLD` | 50% | Minimum drop percentage to trigger cliff detection. Lower = more sensitive to smaller disruptions. |
| `MAX_HOSTS_PER_CYCLE` | 10 | Caps fencing throughput after cliff expires. Limits blast radius if the outage was genuine. |
| `THRESHOLD` | 50% | Global percentage cap (`>` comparison). Blocks all fencing when failed percentage strictly exceeds the threshold. |
| `HEARTBEAT_TIMEOUT` | 120s | How long a heartbeat is considered fresh. At the default 30s send interval, tolerates up to 3 consecutive lost packets before a host appears stale. Raise if heartbeat packets traverse a congested or lossy network. |
| `K8S_API_CHECK_INTERVAL` | 15s | How often the K8s API reachability probe runs. 3 consecutive failures (3 x interval) block all fencing. On networks with intermittent K8s API latency, raising to 30-60s avoids flapping between reachable/unreachable states. |
| `FENCING_TIMEOUT` | 30s | Per-request timeout for IPMI/Redfish fencing calls (Redfish retries up to 3 times). If the BMC management network is slow or lossy, fencing may time out even though the host is reachable -- raise toward 60-90s. Also raise `terminationGracePeriodSeconds` accordingly. |
| `EVACUATION_MAX_THREADS` | 32 | Total evacuation thread budget. Per-host concurrency = `EVACUATION_MAX_THREADS / WORKERS`. With `WORKERS=10`, default gives 3 concurrent evacuations per host; set to 100 to get 10. Increase when single-host failures dominate; decrease to reduce control plane pressure. |
| `EVACUATION_RETRIES` | 5 | Max per-instance evacuation retry attempts. Each retry re-issues the evacuate API call. On a flaky Nova API, retries cover transient 500s; at large scale, lower to 3 to avoid piling up retry traffic. |
| `EVACUATION_STAGGER` | 0 | Seconds between each VM's evacuation start within a phase. Set to 2-3 to reduce control plane pressure during mass evacuation of hosts with many VMs. |

#### Tuning by cluster size

The defaults are designed for small-to-medium clusters (up to ~50 nodes). As the cluster grows, the percentage-based thresholds need adjustment because a single failure domain (rack, switch) represents a smaller fraction of the total. For clusters with 3-5 nodes, see also [instanceha_architecture.md -- Small Cluster Tuning](instanceha_architecture.md#small-cluster-tuning) for the detailed gate-by-gate impact analysis.

**Small clusters (3-20 nodes)**

A single switch or PDU can take down most or all of the cluster. The default `HEARTBEAT_CLIFF_THRESHOLD: 50` provides good coverage -- any event affecting more than half the heartbeat hosts triggers cliff detection. However, `THRESHOLD: 50` may be too generous: in a 10-node cluster, 50% allows fencing 5 hosts simultaneously, which could be the entire failure domain. Consider lowering it:

```yaml
THRESHOLD: "30"                      # 3 of 10 nodes -- limits blast radius in small clusters
HEARTBEAT_CLIFF_THRESHOLD: "40"      # Triggers on 4+ of 10 hosts lost
MAX_HOSTS_PER_CYCLE: "3"             # Fence at most 3 per cycle
```

**Medium clusters (20-100 nodes)**

Defaults work well. A 40-node rack behind one ToR switch is 50% of an 80-node cluster, which triggers cliff detection and threshold protection. Adjust if your failure domains are smaller:

```yaml
# If racks have 20 nodes in a 100-node cluster (20% per rack):
HEARTBEAT_CLIFF_THRESHOLD: "15"      # Triggers on 15%+ drop (15 of 100)
THRESHOLD: "25"                      # Blocks fencing if 25+ hosts are down
MAX_HOSTS_PER_CYCLE: "5"             # Conservative -- one rack over 4 cycles
```

**Large clusters (100+ nodes)**

A single ToR switch affecting 20-40 nodes may only represent 5-10% of heartbeat hosts -- well below the default 50% cliff threshold. Cliff detection will **not** trigger, and these 20 hosts will proceed directly to the threshold and per-cycle cap gates. Lower the cliff threshold so it catches rack-level events:

```yaml
# 500-node cluster, 40-node racks:
HEARTBEAT_CLIFF_THRESHOLD: "10"      # Triggers on 50+ hosts (10% of 500)
HEARTBEAT_CLIFF_MAX_CYCLES: "5"      # 5 x 120s POLL = 10 min buffer
THRESHOLD: "10"                      # Blocks if 50+ hosts down
MAX_HOSTS_PER_CYCLE: "10"            # One rack over 4 cycles
```

See [Large-Scale Deployment (500+ Compute Nodes)](#large-scale-deployment-500-compute-nodes) for a complete configuration example.

**General recommendations**

- Enable `CHECK_HEARTBEAT` on a [dedicated network](#dedicated-heartbeat-network) separate from `internalapi`. This way, a Nova API network disruption does not also kill heartbeats -- the two detection channels fail independently.
- Set `HEARTBEAT_CLIFF_MAX_CYCLES * POLL` to exceed your longest expected network reconvergence time. For example, if spanning tree reconvergence can take up to 5 minutes, use `HEARTBEAT_CLIFF_MAX_CYCLES: 7` with `POLL: 45` (315s coverage).
- Set `HEARTBEAT_CLIFF_THRESHOLD` so that a single failure domain (rack, PDU, ToR switch) exceeds it. If your largest rack is 40 nodes in a 200-node cluster (20%), set the threshold to 15% so a full rack loss triggers cliff detection.
- Do not raise `MAX_HOSTS_PER_CYCLE` above the size of your largest failure domain. If a rack has 40 nodes, `MAX_HOSTS_PER_CYCLE: 40` would fence the entire rack in one cycle after cliff expiry -- defeating the purpose.
- Use per-aggregate thresholds (`instanceha:max_failures` metadata on host aggregates) as the innermost defense, especially when racks vary in size.

#### Partial heartbeat loss (below cliff threshold)

When heartbeat loss affects fewer hosts than the `HEARTBEAT_CLIFF_THRESHOLD` percentage, cliff detection does not trigger. Per-host heartbeat filtering still applies: `_filter_reachable_hosts()` excludes hosts whose individual heartbeats are fresh, so only hosts that lost both Nova API reporting and heartbeat proceed to fencing. `THRESHOLD` and `MAX_HOSTS_PER_CYCLE` remain as final backstops.

If heartbeat packets are intermittently dropped (some arrive, some don't), hosts near the `HEARTBEAT_TIMEOUT` boundary may alternate between filtered and not-filtered across cycles. Raising `HEARTBEAT_TIMEOUT` widens the tolerance window:

```yaml
HEARTBEAT_TIMEOUT: "180"    # 5 missed packets (at 30s send interval) before declaring stale
```

#### BMC/management network isolation

When the BMC network (IPMI/Redfish) is unreachable but the data plane is healthy, fencing commands time out. InstanceHA does not evacuate without confirmed power-off, so the host remains `forced_down=True, disabled_reason="instanceha evacuation"` until an operator intervenes.

Indicators:
- `InstanceEvacuationFailed` K8s events with fencing timeout messages
- `instanceha_fencing_total{result="failed"}` counter increasing

Relevant configuration:
- `FENCING_TIMEOUT`: raise toward 60-90s if the BMC network has higher latency (also raise `terminationGracePeriodSeconds` accordingly).
- Separate the BMC network from the data plane physically. If both share the same ToR switch, a single switch failure causes both a false detection and an inability to fence.
- Monitor `instanceha_fencing_duration_seconds` to establish a baseline for BMC response times.

#### Nova API or Keystone unreachability

When the Nova API or Keystone is unreachable, behavior depends on when the failure occurs:

- **Main poll loop**: `services.list()` fails with `Unauthorized` or `DiscoveryFailure`. InstanceHA enters exponential backoff, skipping fencing for up to 5 consecutive failures, then logs a critical error and keeps retrying. No hosts are fenced during this period.
- **In-flight smart evacuation**: `_monitor_evacuation` has a separate `MAX_API_RETRIES` budget (10 attempts). After 10 consecutive failures, the evacuation for that instance is marked as failed. The host remains force-downed.
- **Token refresh**: `keystoneauth1.Session` handles token refresh transparently. If Keystone is unreachable at refresh time, the next Nova API call fails with `Unauthorized`, entering the same backoff logic.

Monitoring: `instanceha_poll_consecutive_failures` gauge and `instanceha_poll_cycle_total{result="error"}` counter. Alert on `instanceha_poll_consecutive_failures > 2` to catch connectivity issues before the retry budget is exhausted.

### Large-Scale Deployment (500+ Compute Nodes)

This section covers tuning InstanceHA for clusters with hundreds of compute nodes organized across multiple Nova cells. The example configuration targets a 1000-node cluster spread across 3 Nova cells (~333 nodes per cell), though the principles apply to any large deployment.

**Design philosophy**: At large scale, the blast radius of a false positive is far worse than the latency of a slow recovery. Fencing 10 healthy hosts disrupts hundreds of VMs; recovering 10 genuinely failed hosts 5 minutes later disrupts nothing additional. Every threshold and rate limit below errs on the side of caution -- operators can always raise limits for specific failure domains using per-aggregate thresholds.

#### Architecture Considerations

InstanceHA runs as a single pod and polls the Nova API for all compute services across all cells. At 1000 nodes, the poll cycle must process 1000 service records, fetch aggregate memberships, and potentially issue fencing and evacuation commands -- all within a reasonable time budget. The key constraints are:

- **Nova API latency**: `services.list(binary="nova-compute")` returns all 1000 services in a single call. Nova's `nova-manage cell_v2` maps cells transparently, so InstanceHA does not need per-cell configuration. However, cross-cell queries are slower -- expect 1-3 seconds per poll at this scale.
- **Heartbeat processing**: The UDP listener processes heartbeat packets individually. At 1000 nodes with the default `HEARTBEAT_TIMEOUT: 120`, the listener handles ~8 packets/second (1000 nodes / 120 seconds). The [heartbeat performance benchmarks](instanceha_heartbeat_performance.md) show the listener handles 5000+ packets/second.
- **Fencing concurrency**: With `WORKERS: 4`, up to 4 hosts are fenced in parallel. Each fencing operation (IPMI/Redfish) takes 5-30 seconds. At large scale, a correlated failure (e.g., a rack power event taking down 20 nodes) will be fenced across multiple poll cycles due to `MAX_HOSTS_PER_CYCLE`.
- **Detection latency during long cycles**: The main loop is synchronous -- it blocks on `_process_stale_services` until all fencing and evacuation completes before polling again. Cycles cannot overlap. However, a long cycle delays detection of new failures. With `WORKERS: 8` and `MAX_HOSTS_PER_CYCLE: 5`, a worst-case cycle runs ~30 seconds (5 fencing ops at 30s each, parallelized across 8 workers), plus evacuation. Any host that fails during this window won't be detected until the next poll.

#### Recommended Configuration

```yaml
apiVersion: instanceha.openstack.org/v1beta1
kind: InstanceHa
metadata:
  name: instanceha
  namespace: openstack
spec:
  config:
    # Detection timing
    POLL: "120"                      # 2 min poll -- balances detection latency vs API load at scale
    DELTA: "60"                      # 60s staleness -- tolerates cross-cell query lag

    # Heartbeat
    CHECK_HEARTBEAT: "True"          # Dual-channel detection at 1000 nodes
    HEARTBEAT_TIMEOUT: "180"         # 3 minutes -- tolerates transient network blips at scale
    HEARTBEAT_CLIFF_THRESHOLD: "15"  # 15% -- 150 simultaneous silent hosts is already a strong partition signal
    HEARTBEAT_CLIFF_MAX_CYCLES: "5"  # 5 cycles (10 minutes at POLL=120) before accepting as genuine

    # Safety thresholds
    THRESHOLD: "10"                  # 10% -- at 1000 nodes, blocks fencing when 100+ hosts report down simultaneously
    MAX_HOSTS_PER_CYCLE: "5"         # Fence at most 5 hosts per cycle -- conservative, observable, interruptible
    TAGGED_AGGREGATES: "True"        # Use aggregate-based host filtering

    # Evacuation
    SMART_EVACUATION: "True"         # Track migrations to completion -- essential for visibility at scale
    WORKERS: "8"                     # 8 parallel fencing workers (up from default 4)
    EVACUATION_RETRIES: "3"          # 3 retries per VM (lower than default 5 -- avoid piling up at scale)
    EVACUATION_STAGGER: "2"          # 2s between each VM -- reduces control plane pressure at scale

    # Security
    FENCING_TIMEOUT: "30"            # 30s timeout per fencing command
    SSL_VERIFY: "True"               # Always verify TLS in production
```

#### Why These Values

**`POLL: 120`** -- At 1000 nodes, a poll cycle that triggers fencing and evacuation can take 60+ seconds (cross-cell service list query + aggregate lookups + fencing with `WORKERS: 8`). A 120-second interval ensures the previous cycle completes before the next one starts. The detection latency trade-off (up to POLL + DELTA = 3 minutes) is offset by heartbeat verification, which provides near-real-time detection independent of the poll interval.

**`DELTA: 60`** -- Nova's `updated_at` timestamp for compute services can lag during cross-cell database queries, especially when cells are under load. A 60-second staleness window absorbs this variance. Combined with `POLL: 120`, worst-case detection time is ~3 minutes.

**`HEARTBEAT_CLIFF_THRESHOLD: 15`** -- At 1000 nodes, 15% means 150 hosts going silent simultaneously triggers cliff detection. The default 50% (500 hosts) is unlikely to trigger at this scale. 15% catches rack-level or spine-switch failures that affect a significant portion of the cluster.

**`HEARTBEAT_CLIFF_MAX_CYCLES: 5`** -- At `POLL: 120`, 5 cycles = 10 minutes before the cliff detector gives up and accepts the state as genuine. This is long enough for most transient network events (spanning tree convergence, switch firmware upgrades, brief routing flaps) to resolve, while still bounded -- a genuine spine switch failure will be acted on within 10 minutes. **Important**: when the cliff expires, `MAX_HOSTS_PER_CYCLE` is the primary defense against fencing too many hosts at once. These two settings are mutually dependent -- do not raise `MAX_HOSTS_PER_CYCLE` without also ensuring `HEARTBEAT_CLIFF_MAX_CYCLES * POLL` exceeds your longest expected network reconvergence time. Values below 10 for `HEARTBEAT_CLIFF_THRESHOLD` are silently clamped to 10 (the ConfigManager minimum).

**`THRESHOLD: 10`** -- At 1000 nodes, 10% allows up to 100 simultaneous failures. A single rack (~40 nodes) failing is 4%, two racks (80 nodes) is 8% -- both pass the threshold. 100+ hosts reporting down simultaneously indicates a Nova API issue, a cell outage, or a network partition. The per-aggregate thresholds (see below) provide finer-grained control within failure domains.

**`MAX_HOSTS_PER_CYCLE: 5`** -- Limits fencing throughput to 5 hosts per cycle. A 40-node rack failure takes 8 poll cycles (16 minutes at `POLL: 120`) to fully fence, during which operators can observe the `FencingRateLimited` events and confirm the failure is genuine. For faster recovery within specific failure domains, use per-aggregate thresholds rather than raising the global cap.

**`WORKERS: 8`** -- With `MAX_HOSTS_PER_CYCLE: 5`, 8 workers process the entire batch in a single round of parallel fencing. The inner evacuation thread pool is `EVACUATION_MAX_THREADS / WORKERS` = 32/8 = 4 concurrent evacuations per host (configurable via `EVACUATION_MAX_THREADS`). Peak concurrent evacuation requests: 5 x 4 = 20.

**`EVACUATION_RETRIES: 3`** -- At large scale, 5 retries per VM across 10 hosts with dozens of VMs each can create a long tail of retry traffic against the Nova API. 3 retries catches transient errors without piling up.

#### Per-Aggregate Thresholds

At 1000 nodes across 3 cells, use Nova host aggregates to model your failure domains (racks, rows, or cells). Set per-aggregate failure limits using aggregate metadata:

```bash
# Create aggregates per rack (example: 40 nodes per rack, 25 racks)
openstack aggregate create --property evacuable=true --property instanceha:max_failures=5 rack-01
openstack aggregate add host rack-01 compute-001.example.com
# ... repeat for all hosts in the rack

# Or per cell (example: ~333 nodes per cell)
openstack aggregate create --property evacuable=true --property instanceha:max_failures=30 cell-1
```

The `instanceha:max_failures` metadata sets an absolute cap on how many hosts in that aggregate can be fenced in a single cycle. For rack-level aggregates with 40 nodes, a limit of 5 means InstanceHA will not fence more than ~12% of a rack at once -- matching the conservative global `MAX_HOSTS_PER_CYCLE`. For cell-level aggregates, a limit of 30 (~9% of 333 nodes) aligns with the global `THRESHOLD: 10` while providing per-cell granularity -- a single cell losing 30 hosts triggers the aggregate gate even if the global threshold hasn't been reached.

Note that `instanceha:max_failures` and `MAX_HOSTS_PER_CYCLE` are independent gates -- both must pass for a host to be fenced. The effective per-cycle limit is the lower of the two.

#### Monitoring at Scale

At 1000 nodes, Prometheus alerting becomes critical. Key alerts to configure:

```yaml
# Alert if more than 20 hosts are fenced in a rolling 30-minute window
- alert: InstanceHAMassFencing
  expr: increase(instanceha_fencing_total[30m]) > 20
  for: 0m
  labels:
    severity: critical

# Alert if the rate limiter is hitting consistently (queued failures)
- alert: InstanceHAFencingBacklog
  expr: increase(instanceha_fencing_rate_limited_total[10m]) > 3
  for: 0m
  labels:
    severity: warning

# Alert if poll cycle is taking too long (Nova API under pressure)
- alert: InstanceHAPollSlow
  expr: histogram_quantile(0.95, rate(instanceha_poll_duration_seconds_bucket[5m])) > 30
  for: 5m
  labels:
    severity: warning

# Alert if all-services-stale fires (Nova API anomaly)
- alert: InstanceHAAllServicesStale
  expr: increase(instanceha_all_services_stale_total[5m]) > 0
  for: 0m
  labels:
    severity: critical
```

#### Nova Cell Considerations

InstanceHA is cell-unaware -- it queries the Nova API (which fans out to all cells) and operates on the unified service list. This means:

- **Cell outages**: If a cell's database or message queue goes down, Nova may report all services in that cell as stale. The all-services-stale circuit breaker will not catch this (only ~333 of 1000 services are affected). The `THRESHOLD: 10` protects here -- 333 failures would be 33%, far exceeding the 10% threshold, so fencing is blocked for the entire cycle. Per-cell aggregates with `instanceha:max_failures` provide even tighter protection.
- **Cell-specific maintenance**: Use Nova's host disable (`openstack compute service set --disable`) before taking a cell offline for maintenance. InstanceHA ignores disabled hosts.
- **Cross-cell evacuation**: When a host in cell-1 is fenced, Nova's scheduler may place evacuated VMs in cell-2 or cell-3. This is normal -- the scheduler respects its own placement rules. InstanceHA does not control which cell receives evacuated VMs.

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

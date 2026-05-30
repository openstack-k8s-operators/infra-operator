# InstanceHA Architecture Documentation

## Overview

InstanceHA is a high-availability service for OpenStack that automatically detects and evacuates instances from failed compute nodes.

**Version**: 2.5

## Table of Contents

1. [Core Components](#core-components)
2. [Architecture Patterns](#architecture-patterns)
3. [Service Workflow](#service-workflow)
4. [Critical Services Validation](#critical-services-validation)
5. [Configuration System](#configuration-system)
6. [Evacuation Mechanisms](#evacuation-mechanisms)
7. [Fencing Agents](#fencing-agents)
8. [Kubernetes Events and Prometheus Metrics](#kubernetes-events-and-prometheus-metrics)
9. [Advanced Features](#advanced-features)
10. [Region Handling](#region-handling)
11. [Authentication](#authentication)
12. [Startup Reconciliation](#startup-reconciliation)
13. [Graceful Shutdown](#graceful-shutdown)
14. [Security](#security)
15. [Performance](#performance)
16. [Testing](#testing)
17. [Deployment](#deployment)
18. [Configuration Options Reference](#configuration-options-reference)

---

## Core Components

### 0. Data Structures

**Dataclasses**:

```python
@dataclass
class ConfigItem:
    """Configuration item with type and validation constraints."""
    type: str
    default: Any
    min_val: Optional[int] = None
    max_val: Optional[int] = None

@dataclass
class EvacuationResult:
    """Result of a server evacuation request."""
    uuid: str
    accepted: bool
    reason: str
    status_code: Optional[int] = None  # HTTP status code for retry decisions

@dataclass
class EvacuationStatus:
    """Status of an ongoing server evacuation."""
    completed: bool
    error: bool

@dataclass
class NovaLoginCredentials:
    """Credentials for Nova API login."""
    username: str
    password: str
    project_name: str
    auth_url: str
    user_domain_name: str
    project_domain_name: str
    region_name: str

@dataclass
class ACLoginCredentials:
    """Credentials for Application Credential based login."""
    auth_url: str
    application_credential_id: str
    application_credential_secret: str
    region_name: str

@dataclass
class ReservedHostResult:
    """Result of reserved host management operation."""
    success: bool
    hostname: Optional[str] = None
```

---

### 1. ConfigManager

**Purpose**: Centralized configuration management with validation and secure access.

**Responsibilities**:
- Load and merge YAML configuration files
- Validate configuration values with type checking
- Provide type-safe accessors with defaults
- Manage SSL/TLS configuration
- Handle cloud credentials securely

**Key Features**:
- **Configuration Sources**: Main config, clouds.yaml, secure.yaml, fencing.yaml
- **Validation**: Type checking, range validation (min/max), enum validation
- **SSL Support**: CA bundle, client certificates, verification toggle
- **Environment Overrides**: OS_CLOUD, UDP_PORT (validated: 1-65535), SSL paths
- **Direct Access**: Configuration accessed via `get_config_value()` method

**Configuration Map**:
```python
_config_map: Dict[str, ConfigItem] = {
    'EVACUABLE_TAG': ConfigItem('str', 'evacuable'),
    'DELTA': ConfigItem('int', 30, 10, 300),
    'POLL': ConfigItem('int', 45, 15, 600),
    'THRESHOLD': ConfigItem('int', 50, 0, 100),
    'WORKERS': ConfigItem('int', 4, 1, 50),
    'DELAY': ConfigItem('int', 0, 0, 300),
    'LOGLEVEL': ConfigItem('str', 'INFO'),
    'SMART_EVACUATION': ConfigItem('bool', False),
    'RESERVED_HOSTS': ConfigItem('bool', False),
    'FORCE_RESERVED_HOST_EVACUATION': ConfigItem('bool', False),
    'TAGGED_IMAGES': ConfigItem('bool', True),
    'TAGGED_FLAVORS': ConfigItem('bool', True),
    'TAGGED_AGGREGATES': ConfigItem('bool', True),
    'LEAVE_DISABLED': ConfigItem('bool', False),
    'FORCE_ENABLE': ConfigItem('bool', False),
    'CHECK_KDUMP': ConfigItem('bool', False),
    'KDUMP_TIMEOUT': ConfigItem('int', 30, 5, 300),
    'CHECK_HEARTBEAT': ConfigItem('bool', False),
    'HEARTBEAT_TIMEOUT': ConfigItem('int', 120, 30, 600),
    'DISABLED': ConfigItem('bool', False),
    'SSL_VERIFY': ConfigItem('bool', True),
    'FENCING_TIMEOUT': ConfigItem('int', 30, 5, 120),
    'HASH_INTERVAL': ConfigItem('int', 60, 30, 300),
    'ORCHESTRATED_RESTART': ConfigItem('bool', False),
    'SKIP_SERVERS_WITH_NAME': ConfigItem('list', []),
    'EVACUATION_RETRIES': ConfigItem('int', DEFAULT_EVACUATION_RETRIES, 1, 20),  # DEFAULT_EVACUATION_RETRIES = 5
}
```

See the [Configuration Options Reference](#configuration-options-reference) section for detailed descriptions of each option.

**File Locations** (configurable via environment variables):
- Main config: `/var/lib/instanceha/config.yaml` (env: `INSTANCEHA_CONFIG_PATH`)
- Cloud auth: `/home/cloud-admin/.config/openstack/clouds.yaml` (env: `CLOUDS_CONFIG_PATH`)
- Passwords: `/home/cloud-admin/.config/openstack/secure.yaml` (env: `SECURE_CONFIG_PATH`)
- Fencing credentials: `/secrets/fencing.yaml` (env: `FENCING_CONFIG_PATH`)

---

### 2. InstanceHAService

**Purpose**: Main service orchestrator that coordinates all InstanceHA operations.

**Initialization Pattern**:
```python
def __init__(self, config_manager: ConfigManager, cloud_client: Optional[OpenStackClient] = None):
    self.config = config_manager
    self.cloud_client = cloud_client

    # Health monitoring
    self.current_hash = ""
    self.hash_update_successful = True
    self._last_hash_time = 0
    self._previous_hash = ""
    self.ready = False                              # Readiness flag (set after first poll)

    # Caching
    self._host_servers_cache = {}
    self._evacuable_flavors_cache = None
    self._evacuable_images_cache = None
    self._cache_timestamp = 0
    self._cache_lock = threading.Lock()
    self.evacuable_tag = self.config.get_config_value('EVACUABLE_TAG')

    # Threading
    self.health_check_thread = None
    self.udp_ip = ''
    self.shutdown_event = threading.Event()          # Graceful SIGTERM shutdown

    # Kdump state (protected by kdump_lock)
    self.kdump_lock = threading.Lock()               # Protects all kdump state
    self.kdump_hosts_timestamp = defaultdict(float)
    self.kdump_hosts_checking = defaultdict(float)
    self.kdump_listener_stop_event = threading.Event()
    self.kdump_fenced_hosts = set()

    # Heartbeat state (protected by heartbeat_lock)
    self.heartbeat_lock = threading.Lock()
    self.heartbeat_hosts_timestamp = defaultdict(float)
    self.heartbeat_listener_stop_event = threading.Event()
    self.heartbeat_listener_start_time = 0.0

    # Host processing tracking
    self.hosts_processing = defaultdict(float)
    self.processing_lock = threading.Lock()
    self.reserved_hosts_lock = threading.Lock()

    logging.info("InstanceHA service initialized successfully")
```

**State Management Notes**:

- `ready` flag starts `False` and is set to `True` after the first successful poll cycle. This drives the `/healthz` readiness probe endpoint — Kubernetes won't route traffic until the service has completed its first poll.
- `shutdown_event` is set by the SIGTERM handler to signal the main loop and all `wait()` calls to exit gracefully.
- `kdump_lock` protects all kdump-related state since the UDP listener thread writes concurrently with the main poll loop.
- `heartbeat_lock` similarly protects heartbeat state from concurrent UDP listener writes.

**Key Methods**:
- `get_connection()` - Get Nova client with dependency injection support
- `is_server_evacuable()` - Check evacuability based on tags
- `get_evacuable_flavors()` - Get cached evacuable flavor list
- `get_evacuable_images()` - Get cached evacuable image list
- `is_aggregate_evacuable()` - Check aggregate evacuability
- `refresh_evacuable_cache()` - Force cache refresh
- `update_health_hash()` - Update health monitoring hash

**Initialization Design**:
The initialization is split into five methods, each responsible for a specific domain (health, cache, kdump, processing, threading).

---

### 3. CloudConnectionProvider (ABC)

**Purpose**: Abstract interface for cloud connection management.

**Pattern**: ABC-based dependency injection for testability.

**Interface**:
```python
class CloudConnectionProvider(ABC):
    @abstractmethod
    def get_connection(self) -> Optional[OpenStackClient]:
        """Get a connection to the cloud provider."""
        pass

    @abstractmethod
    def create_connection(self) -> Optional[OpenStackClient]:
        """Create a new connection to the cloud provider."""
        pass
```

**Implementation**: InstanceHAService implements this protocol.

---

## Architecture Patterns

### 1. Dependency Injection

**Pattern**: Constructor injection with optional test doubles. No global state.

**Example**:
```python
class InstanceHAService(CloudConnectionProvider):
    def __init__(self, config_manager: ConfigManager,
                 cloud_client: Optional[OpenStackClient] = None):
        self.config = config_manager
        self.cloud_client = cloud_client  # None in production, mock in tests

# SSL/TLS functions accept config_manager as parameter (no globals)
def _make_ssl_request(method: str, url: str, auth: tuple, timeout: int,
                      config_mgr: ConfigManager, **kwargs):
    ssl_config = config_mgr.get_requests_ssl_config()
    # ...

def _redfish_reset(url, user, passwd, timeout, action, config_mgr):
    # Uses config_mgr parameter instead of global variable
```

**Key Characteristics**:
- All dependencies explicitly passed as parameters
- No global state
- Thread-safe

---

### 2. Context Managers

**Pattern**: Automatic resource cleanup using context managers.

**Examples**:

**Host Processing Tracking**:
```python
@contextmanager
def track_host_processing(service: 'InstanceHAService', hostname: str):
    """Context manager for tracking host processing with automatic cleanup."""
    try:
        yield
    finally:
        with service.processing_lock:
            service.hosts_processing.pop(hostname, None)
            logging.debug('Cleaned up processing tracking for %s', hostname)
```

**UDP Socket Management**:
```python
class UDPSocketManager:
    """Context manager for UDP socket with proper resource cleanup.
    Supports IPv4, IPv6, and dual-stack (::) binding."""
    def __init__(self, udp_ip, udp_port, label='UDP'):
        self.udp_ip = udp_ip
        self.udp_port = udp_port
        self.label = label
        self.socket = None

    def __enter__(self):
        info = socket.getaddrinfo(self.udp_ip or '::', self.udp_port,
                                  type=socket.SOCK_DGRAM)[0]
        self.socket = socket.socket(info[0], info[1])
        if info[0] == socket.AF_INET6:
            self.socket.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 0)
        self.socket.settimeout(1.0)
        self.socket.bind(info[4])
        return self.socket

    def __exit__(self, exc_type, exc_val, exc_tb):
        try:
            if self.socket:
                self.socket.close()
        except (OSError, AttributeError):
            pass
```

---

### 3. Thread Safety

**Cache Access**:
```python
# Pattern: read check → API call outside lock → write update → return local var
# Lock held only for dictionary operations, not during API calls

# 1. Check cache with lock (fast read)
with self._cache_lock:
    if self._evacuable_flavors_cache is not None:
        return self._evacuable_flavors_cache

# 2. Expensive API call outside lock (no blocking)
flavors = connection.flavors.list()
cache_data = [f.id for f in flavors if self._is_flavor_evacuable(f)]

# 3. Update cache with lock (fast write), return local variable
with self._cache_lock:
    self._evacuable_flavors_cache = cache_data
return cache_data  # Return local var, not self._evacuable_flavors_cache
```

**Implementation**:
- Lock held only for dictionary lookups
- API calls performed outside lock
- Reduces lock contention during I/O operations


**Host Processing Tracking**:
```python
with service.processing_lock:
    # Remove entries exceeding max processing time
    service._cleanup_dict_by_condition(
        service.hosts_processing,
        lambda h, t: current_time - t > max_processing_time
    )
    # Mark hosts as processing
    for svc in compute_nodes:
        hostname = _extract_hostname(svc.host)
        service.hosts_processing[hostname] = current_time
```

---

### 4. Unified Validation

**Pattern**: Centralized validation to prevent SSRF and injection attacks.

**Validation Types**:
- **URL**: Scheme, netloc, localhost/link-local blocking
- **IP Address**: IPv4/IPv6 validation
- **Port**: Range validation (1-65535)
- **Kubernetes Resources**: Regex validation
- **Power Actions**: Whitelist validation

**Implementation**:
```python
def _try_validate(validator_func: Callable[[], bool], error_msg: str, context: str, log_error: bool = True) -> bool:
    """Helper to execute validation with consistent error handling."""
    try:
        return validator_func()
    except (ValueError, AttributeError, TypeError) as e:
        if log_error:
            logging.error("%s for %s: %s", error_msg, context, e)
        return False

VALIDATION_PATTERNS = {
    'k8s_namespace': (r'^[a-z0-9]([-a-z0-9]*[a-z0-9])?$', 63),
    'k8s_resource': (r'^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$', 253),
    'power_action': (['on', 'off', 'status', 'ForceOff', ...], None),
    'ip_address': ('ip', None),
    'port': ('port', None),
    'username': (r'^[a-zA-Z0-9_-]{1,64}$', 64),
    'hostname': (r'^[a-zA-Z0-9]([a-zA-Z0-9._-]*[a-zA-Z0-9])?$', 64),
}

def validate_input(value: str, validation_type: str, context: str) -> bool:
    # Special validation for URLs (block localhost, link-local via ipaddress module)
    if validation_type == 'url':
        def _validate_url():
            p = urlparse(value)
            if p.scheme not in ['http', 'https'] or not p.netloc:
                return False
            h = p.hostname
            if not h:
                return False
            h_lower = h.lower()
            if h_lower in ['localhost', '0.0.0.0']:
                return False
            try:
                addr = ipaddress.ip_address(h_lower.strip('[]'))
                if addr.is_loopback or addr.is_link_local:
                    return False
            except ValueError:
                pass  # Not an IP literal (hostname) - allow through
            return True
        return _try_validate(_validate_url, "Invalid URL", context, log_error=False)

    # Unknown validation types are rejected (fail-closed)
    pattern_data = VALIDATION_PATTERNS.get(validation_type)
    if not pattern_data:
        logging.error("Unknown validation type '%s' - rejecting input", validation_type)
        return False
    # ... pattern-based and list-based validations using _try_validate helper
```

---

## Service Workflow

### Main Poll Loop

**Execution Flow**:

```
┌─────────────────────────────────────────────────────────┐
│                    Main Poll Loop                       │
└─────────────────────────────────────────────────────────┘
                            │
                            ▼
    ┌──────────────────────────────────────────────────┐
    │ 1. Update Health Hash                            │
    │    - SHA256 of current timestamp                 │
    │    - Update only if HASH_INTERVAL elapsed        │
    │    - Set hash_update_successful flag             │
    └──────────────────────────────────────────────────┘
                            │
                            ▼
    ┌──────────────────────────────────────────────────┐
    │ 2. Query Nova API                                │
    │    - Get all nova-compute services               │
    │    - Calculate target_date (now - DELTA)         │
    │    - Handle API failures gracefully              │
    └──────────────────────────────────────────────────┘
                            │
                            ▼
    ┌──────────────────────────────────────────────────┐
    │ 3. Categorize Services                           │
    │    - compute_nodes: down or stale                │
    │    - to_resume: 'evacuation:' marker + down      │
    │    - to_reenable: forced_down OR 'complete:'     │
    │      (excluding resume candidates)               │
    └──────────────────────────────────────────────────┘
                            │
                            ▼
    ┌──────────────────────────────────────────────────┐
    │ 4. Process Stale Services                        │
    │    - Filter processing hosts (deduplication)     │
    │    - Filter by servers, tags, aggregates         │
    │    - Filter reachable hosts (heartbeat check)    │
    │    - Check threshold (active services only)      │
    │    - Check critical services operational         │
    │    - Execute evacuations (with cleanup)          │
    └──────────────────────────────────────────────────┘
                            │
                            ▼
    ┌──────────────────────────────────────────────────┐
    │ 5. Process Re-enabling                           │
    │    - Filter by LEAVE_DISABLED                    │
    │    - Check migration status (or FORCE_ENABLE)    │
    │    - Kdump delay (60s after last message)        │
    │    - Unset force-down first                      │
    │    - Wait for service state='up'                 │
    │    - Then enable disabled services               │
    │    - Two-stage process for proper recovery       │
    └──────────────────────────────────────────────────┘
                            │
                            ▼
    ┌──────────────────────────────────────────────────┐
    │ 6. Handle Stale Connections                      │
    │    - Catch Unauthorized/DiscoveryFailure         │
    │    - Exponential backoff (capped at 300s)        │
    │    - Reconnect via _establish_nova_connection    │
    │      with fatal=False (returns None on failure,  │
    │      loop continues retrying)                    │
    │    - Generic exceptions also use backoff         │
    │    - Successful poll resets failure counter      │
    └──────────────────────────────────────────────────┘
                            │
                            ▼
    ┌──────────────────────────────────────────────────┐
    │ 7. Sleep (POLL seconds)                          │
    └──────────────────────────────────────────────────┘
                            │
                            └──────────────┐
                                           │
                                           ▼
                                       (repeat)
```

---

### Service Processing Pipeline

**Per-Host Evacuation Flow**:

```
process_service(failed_service, reserved_hosts, resume, service)
    │
    ├─ (if not resume)
    │   └─ 1. Fence Host (Power Off)
    │       └─ _host_fence(host, 'off')
    │           ├─ Look up fencing config
    │           ├─ Validate inputs (SSRF prevention)
    │           └─ Execute fencing operation
    │               ├─ IPMI: ipmitool with retries
    │               ├─ Redfish: HTTP POST with retries
    │               └─ BMH: Kubernetes PATCH with wait
    │
    ├─ 2. Disable Host in Nova
    │   └─ (if not (resume AND forced_down AND 'disabled' in status))
    │       └─ _host_disable(connection, service, instanceha_service)
    │           ├─ Force service down (forced_down=True)
    │           └─ Set disabled_reason:
    │               ├─ 'instanceha evacuation (kdump): {timestamp}' (if kdump-fenced)
    │               └─ 'instanceha evacuation: {timestamp}' (otherwise)
    │       Note: Skipped for resume evacuations (already disabled)
    │             Called for kdump-fenced (not yet disabled)
    │
    ├─ 3. Manage Reserved Hosts
    │   └─ _manage_reserved_hosts(conn, failed_service, reserved_hosts, service)
    │       ├─ Match by aggregate or zone
    │       └─ Enable matching reserved host
    │
    ├─ 4. Evacuate Servers
    │   └─ _host_evacuate(connection, failed_service, service, target_host=None)
    │       ├─ Get evacuable images/flavors
    │       ├─ List servers on host
    │       ├─ Filter evacuable servers (ACTIVE, ERROR, SHUTOFF)
    │       └─ Execute evacuation
    │           ├─ Orchestrated: _orchestrated_evacuate (priority-ordered phases)
    │           ├─ Smart: _server_evacuate_future (track to completion)
    │           └─ Traditional: fire-and-forget
    │       On failure: check if VMs landed on reserved host
    │           ├─ VMs present: keep reserved host (has workload)
    │           ├─ No VMs: _return_reserved_host() to pool
    │           └─ API error: keep reserved host (safe default)
    │
    └─ 5. Post-Evacuation Recovery
        └─ _post_evacuation_recovery(conn, failed_service, service, resume)
            ├─ Power on host (_host_fence(host, 'on'))
            │   └─ Skip if kdump-fenced (host is still dumping memory)
            ├─ Update disabled_reason (always, even if service object is stale):
            │   ├─ 'instanceha evacuation complete (kdump): {timestamp}' (if kdump-fenced)
            │   └─ 'instanceha evacuation complete: {timestamp}' (otherwise)
            │   └─ Excludes service from resume criteria
            │       Preserves kdump marker for re-enable delay
            └─ Service remains disabled until it comes back up
```

---

### Service Categorization Logic

**Implementation**:
```python
def _is_service_resume_candidate(svc) -> bool:
    """Check if a service is a candidate for resuming evacuation.

    A service qualifies if it:
    - Is forced down and down state
    - Is disabled with instanceha evacuation marker
    - Does not have failed or complete markers
    """
    if not (svc.forced_down and svc.state == 'down' and 'disabled' in svc.status):
        return False

    reason = getattr(svc, 'disabled_reason', '')
    return (DISABLED_REASON_EVACUATION in reason and
            DISABLED_REASON_EVACUATION_FAILED not in reason and
            DISABLED_REASON_EVACUATION_COMPLETE not in reason)

def _is_service_stale(svc, target_date: datetime) -> bool:
    """Check whether a service's updated_at timestamp is older than target_date.

    Handles both naive and timezone-aware datetime objects by normalizing
    both to naive UTC before comparison.
    """
    try:
        updated = svc.updated_at
        if isinstance(updated, datetime):
            updated_naive = updated.replace(tzinfo=None)
        else:
            updated_naive = datetime.fromisoformat(updated)
        return updated_naive < target_date.replace(tzinfo=None)
    except (ValueError, TypeError, AttributeError):
        logging.warning("Service %s has invalid updated_at: %r, skipping (not treating as stale)",
                       getattr(svc, 'host', 'unknown'), getattr(svc, 'updated_at', None))
        return False

def _categorize_services(services: List[Any], target_date: datetime) -> tuple:
    # Compute nodes: not disabled/forced-down, and (down OR stale)
    compute_nodes = (svc for svc in services
                     if not ('disabled' in svc.status or svc.forced_down)
                     and (svc.state == 'down' or _is_service_stale(svc, target_date)))

    # Resume candidates (forced down, disabled with instanceha marker, not failed, not complete)
    resume = (svc for svc in services if _is_service_resume_candidate(svc))

    # Re-enable candidates (forced down OR disabled with instanceha complete marker, but NOT resume candidates)
    reenable = (svc for svc in services
                if (('enabled' in svc.status and svc.forced_down)
                   or ('disabled' in svc.status and DISABLED_REASON_EVACUATION_COMPLETE in svc.disabled_reason))
                and not _is_service_resume_candidate(svc))

    return compute_nodes, resume, reenable
```

**States Explained**:
- **compute_nodes**: Fresh failures, need full evacuation workflow
- **resume**: Previous evacuation interrupted (still down+disabled+forced_down), skip fencing/disable
  - Matches services with `disabled_reason` containing "instanceha evacuation:"
  - Explicitly excludes "evacuation complete:" to prevent resume loops
  - Explicitly excludes "evacuation FAILED:" to prevent retry of failed evacuations
- **reenable**: Services needing re-enabling (two-stage process)
  - Stage 1: Unset force_down to allow service to report up
  - Stage 2: Once state='up', enable disabled services
  - Matches services with `disabled_reason` containing "instanceha evacuation complete:"
  - Excludes resume candidates to avoid double-processing

**State Transitions**:
1. Service fails → `compute_nodes` → evacuated → `disabled_reason` = "instanceha evacuation: {timestamp}"
2. Next poll → `disabled_reason` updated to "instanceha evacuation complete: {timestamp}"
   - Service now excluded from `resume` (has "complete" marker)
   - Service now included in `reenable` (has "complete" marker)
3. Subsequent polls → `reenable` → force_down unset → service reports up → enabled
4. Service returns to normal operation

**Disabled Reason Values**:

The `disabled_reason` field tracks evacuation state:

| Value | Set By | Categorization | Meaning |
|-------|--------|----------------|---------|
| `instanceha evacuation: {timestamp}` | `_host_disable()` during initial evacuation | `resume` | Evacuation in progress or interrupted |
| `instanceha evacuation (kdump): {timestamp}` | `_host_disable()` when host is kdump-fenced | `resume` | Kdump evacuation in progress, host rebooting |
| `instanceha evacuation complete: {timestamp}` | `_post_evacuation_recovery()` after successful evacuation | `reenable` | Evacuation complete, waiting for re-enable |
| `instanceha evacuation complete (kdump): {timestamp}` | `_post_evacuation_recovery()` after kdump-fenced evacuation | `reenable` | Kdump evacuation complete, waiting 60s for kdump messages to stop before re-enable |
| `instanceha evacuation FAILED: {timestamp}` | `_update_service_disable_reason()` on evacuation failure | (none) | Evacuation failed, requires manual intervention |

**Key Design Points**:
- The `resume` check explicitly excludes "complete" and "FAILED" to prevent loops
- The `reenable` exclusion also excludes "complete" to prevent excluding completed evacuations
- Services transition from `resume` → `reenable` via the disabled_reason update
- Failed evacuations remain disabled until manually resolved
- Both checks use substring matching with explicit exclusions to prevent incorrect categorization

---

## Critical Services Validation

**Function**: `_check_critical_services(conn, services, compute_nodes)`

**Returns**: `(bool, str)` - can_evacuate flag and error message

### Scheduler Check
```python
schedulers = [s for s in services if s.binary == 'nova-scheduler']
if schedulers and not any(s.state == 'up' for s in schedulers):
    return False, "All nova-scheduler services are down"
```

Checks if at least one `nova-scheduler` service has `state == 'up'`.

### Integration

```python
can_evacuate, error_msg = _check_critical_services(conn, services, compute_nodes)
if not can_evacuate:
    logging.error('Cannot evacuate: %s. Skipping evacuation.', error_msg)
    _cleanup_filtered_hosts(service, marked_hostnames, set(), current_time)
    return
```

**Execution order**:
1. Threshold validation
2. Critical services check
3. Kdump detection
4. Evacuation execution

If check returns `False`, evacuation is skipped and processing tracking is cleaned up.

---

## Configuration System

### Configuration Files

**1. Main Configuration** (`/var/lib/instanceha/config.yaml`):
```yaml
config:
  EVACUABLE_TAG: 'evacuable'
  DELTA: 30           # Service staleness seconds
  POLL: 45            # Poll interval seconds
  THRESHOLD: 50       # Failure threshold percentage
  WORKERS: 4          # Thread pool size
  SMART_EVACUATION: true
  RESERVED_HOSTS: false
  FORCE_RESERVED_HOST_EVACUATION: false
  TAGGED_IMAGES: true
  TAGGED_FLAVORS: true
  TAGGED_AGGREGATES: true
  LEAVE_DISABLED: false
  FORCE_ENABLE: false
  CHECK_KDUMP: false
  KDUMP_TIMEOUT: 30
  DISABLED: false
  SSL_VERIFY: true
  FENCING_TIMEOUT: 30
  HASH_INTERVAL: 60
  LOGLEVEL: 'INFO'
```

**2. Cloud Configuration** (`clouds.yaml`):
```yaml
clouds:
  overcloud:
    auth:
      username: admin
      project_name: admin
      auth_url: http://keystone:5000/v3
      user_domain_name: Default
      project_domain_name: Default
    region_name: regionOne
```

**3. Secure Configuration** (`secure.yaml`):
```yaml
clouds:
  overcloud:
    auth:
      password: secret_password
```

**4. Fencing Configuration** (`fencing.yaml`):
```yaml
FencingConfig:
  compute-01.example.com:
    agent: ipmi
    ipaddr: 192.168.1.10
    ipport: '623'
    login: admin
    passwd: ipmi_password

  compute-02.example.com:
    agent: redfish
    ipaddr: 192.168.1.11
    ipport: '443'
    login: root
    passwd: redfish_password
    tls: 'true'
    uuid: System.Embedded.1

  compute-03.example.com:
    agent: bmh
    host: metal3-0
    namespace: openshift-machine-api
    token: eyJhbGciOi...
```

### Configuration Validation

**Type Checking**:
```python
def get_int(self, key: str, default: int = 0,
            min_val: Optional[int] = None,
            max_val: Optional[int] = None) -> int:
    value = self.config.get(key, default)

    try:
        int_value = int(value)
    except (ValueError, TypeError):
        logging.warning("Configuration %s should be integer, got %s, using default: %s", key, type(value).__name__, default)
        return default

    # Clamp to min/max bounds
    if min_val is not None:
        int_value = max(min_val, int_value)
    if max_val is not None:
        int_value = min(max_val, int_value)

    return int_value
```

**Validation Rules**:
- **LOGLEVEL**: Must be in ['DEBUG', 'INFO', 'WARNING', 'ERROR', 'CRITICAL'], converted to uppercase
- **SSL Paths**: Check file existence using `os.path.exists()` before returning
- **Integer Values**: Clamped to min/max bounds defined in `_config_map`
- **Critical Config**: Auto-validated on initialization via `_validate_config()`

**Kdump Timing**:
- Timeout is evaluated per-host based on when first detected as down
- Multiple poll cycles can occur during the waiting period

---

## Evacuation Mechanisms

### 1. Traditional Evacuation

**Pattern**: Fire-and-forget approach.

**Flow**:
```python
for server in evacuables:
    response = _server_evacuate(connection, server.id)
    if response.accepted:
        logging.debug("Evacuated %s", server.id)
    else:
        logging.warning("Failed to evacuate %s", server.id)
```

**Behavior**: Does not verify completion or detect errors after evacuation request is submitted.

---

### 2. Smart Evacuation

**Configuration**: `SMART_EVACUATION: true`

**Flow**:
```python
def _server_evacuate_future(connection, server, target_host=None) -> bool:
    # 0. Lock server to prevent concurrent operations
    connection.servers.lock(server.id, reason=LOCK_REASON_EVACUATION)

    try:
        # 1. Reset stuck task_state if present
        if getattr(server, 'OS-EXT-STS:task_state', None) is not None:
            connection.servers.reset_state(server.id, 'error')

        # 2. Initiate evacuation
        response = _server_evacuate(connection, server.id, target_host=target_host)

        # 2a. Retry without target host on 409/500
        if not response.accepted and response.status_code in (409, 500) and target_host:
            response = _server_evacuate(connection, server.id)

        if not response.accepted:
            return False

        # 3. Wait before first poll
        time.sleep(INITIAL_EVACUATION_WAIT_SECONDS)

        # 4. Poll migration status until completion or timeout
        result = _monitor_evacuation(connection, server.id, response.uuid, start_time)

        # 5. Restore original server state (skip if Nova already preserved it)
        if result and original_status in ('SHUTOFF', 'ERROR'):
            current = connection.servers.get(server.id)
            if current.status != original_status:
                if original_status == 'SHUTOFF':
                    connection.servers.stop(server.id)
                else:
                    connection.servers.reset_state(server.id, 'error')

    finally:
        # 6. Always unlock
        connection.servers.unlock(server.id)
```

**Monitoring** (`_monitor_evacuation`):
- Separate counters for migration errors (`EVACUATION_RETRIES`, default 5, configurable 1–20) and API errors (`MAX_API_RETRIES=10`)
- API error counter resets on successful API call
- On migration failure: resets server state to `error` and re-issues `_server_evacuate` (scheduler picks new target)
- Hard timeout: `MAX_EVACUATION_TIMEOUT_SECONDS` (300s) via `time.monotonic()`
- Per-instance K8s events emitted: `InstanceEvacuationStarted`, `InstanceEvacuationSucceeded`, `InstanceEvacuationFailed`

**Behavior**:
- Locks the server before evacuation (`LOCK_REASON_EVACUATION`) to prevent concurrent operations
- Resets stuck `task_state` before submitting the evacuation request
- Tracks migration status via Nova API
- Target host retry: if evacuation to a specific host fails with 409/500, retries without target (scheduler picks)
- Migration failures trigger automatic re-issue (reset state + new evacuate call)
- API errors retried independently (budget of 10, not shared with migration errors)
- Times out after 300 seconds
- Skips redundant state restore when Nova already preserved the original state after evacuation
- Restores original VM state after successful evacuation only when needed (`SHUTOFF` → stop, `ERROR` → reset_state)
- Unlocks the server in `finally` block (even on failure)

---

### 3. Orchestrated Evacuation

**Configuration**: `ORCHESTRATED_RESTART: true`

**Server Metadata** (set via `openstack server set --property`):
- `instanceha:restart_priority` (int, 1-1000, default 500): Higher values = evacuated first
- `instanceha:restart_group` (str, optional): Servers with same group evacuate concurrently

**Flow**:
```python
# 1. Read orchestration metadata from all evacuable servers
# 2. Group servers by restart_group (ungrouped → individual phases)
# 3. Sort groups by highest member priority (descending)
# 4. Evacuate each phase sequentially
# 5. Within each phase, use ThreadPoolExecutor (concurrent, like smart evacuation)

phases = _build_evacuation_groups(evacuables)  # group + sort
for phase in phases:
    # Evacuate phase concurrently, wait for completion
    with ThreadPoolExecutor(max_workers=WORKERS) as executor:
        futures = {executor.submit(_server_evacuate_future, conn, s, target): s
                   for s in phase}
        # Wait for all futures, continue-on-failure
```

**Example**:
```
Server metadata:
  db-1:  priority=900, group=database
  db-2:  priority=800, group=database
  app-1: priority=500, group=app
  web-1: priority=100 (no group)

Evacuation order:
  Phase 1: [db-1, db-2]  (group=database, priority=900) -- concurrent
  Phase 2: [app-1]       (group=app, priority=500)
  Phase 3: [web-1]       (ungrouped, priority=100)
```

**Activation**:
- Set `ORCHESTRATED_RESTART: true` in config
- Server metadata is only read when this config option is enabled

**Backwards Compatibility**: When `ORCHESTRATED_RESTART` is not enabled, behavior is identical to smart/traditional evacuation regardless of server metadata.

---

### 4. Server Evacuability Logic

**Tagging System** (OR semantics):
1. **Flavor-based** (`TAGGED_FLAVORS: true`)
2. **Image-based** (`TAGGED_IMAGES: true`)
3. **Aggregate-based** (`TAGGED_AGGREGATES: true`)

**Evaluation Logic**:
```python
def is_server_evacuable(self, server, evac_flavors=None, evac_images=None):
    images_enabled = self.config.get_config_value('TAGGED_IMAGES')
    flavors_enabled = self.config.get_config_value('TAGGED_FLAVORS')

    # When tagging is disabled, evacuate all servers (default behavior)
    if not (images_enabled or flavors_enabled):
        return True

    # No tagged resources: evacuate all
    if not ((images_enabled and evac_images) or (flavors_enabled and evac_flavors)):
        return True

    # Check matches (OR logic)
    matches = [
        self._check_image_match(server, evac_images) if images_enabled else False,
        self._check_flavor_match(server, evac_flavors) if flavors_enabled else False
    ]

    return any(matches)
```

---

## Fencing Agents

### Fencing Agent Dispatch Pattern

**Pattern**: Dispatch table for fencing agent selection with dedicated handler functions.

**Implementation**:
```python
FENCING_AGENTS = {
    'noop': _fence_noop,
    'ipmi': _fence_ipmi,
    'redfish': _fence_redfish,
    'bmh': _fence_bmh,
}

def _execute_fence_operation(host, action, fencing_data, service):
    agent = fencing_data.get("agent", "").lower()
    agent_func = FENCING_AGENTS.get(agent)
    if agent_func:
        return agent_func(host, action, fencing_data, service)
    logging.error("Unknown fencing agent: %s", agent)
    return False
```

### 1. IPMI (Intelligent Platform Management Interface)

**Agent**: `ipmi`

**Configuration**:
```yaml
compute-01:
  agent: ipmi
  ipaddr: 192.168.1.10
  ipport: '623'
  login: admin
  passwd: ipmi_password
```

**Security**:
- Password via environment variable (not command-line)
- Validation: IP, port, username
- Safe logging (credentials sanitized)

---

### 2. Redfish

**Agent**: `redfish`

**Configuration**:
```yaml
compute-02:
  agent: redfish
  ipaddr: 192.168.1.11
  ipport: '443'
  login: root
  passwd: redfish_password
  tls: 'true'
  uuid: System.Embedded.1
```

**Implementation**:
- Uses SSL/TLS connections when configured
- Retries operations (max 3 attempts)
- Verifies power state
- Validates URLs to prevent SSRF attacks

---

### 3. BMH (BareMetal Host - Metal3)

**Agent**: `bmh`

**Configuration**:
```yaml
compute-03:
  agent: bmh
  host: metal3-0
  namespace: openshift-machine-api
  token: eyJhbGciOi...
```

**Implementation**:
- Integrates with Kubernetes API
- Uses bearer token authentication
- Waits for power-off confirmation
- Validates input parameters

---

## Kubernetes Events and Prometheus Metrics

InstanceHA emits Kubernetes Events on the InstanceHa CR to provide observability into the evacuation lifecycle. Events are created via the K8s API using the pod's ServiceAccount token.

In addition, each event emission site also increments a corresponding Prometheus counter, exposed at `http://<pod-ip>:8080/metrics`. See the [Prometheus Metrics](#prometheus-metrics) section below for the full metric catalog.

### Prerequisites

- ServiceAccount token mounted at `/var/run/secrets/kubernetes.io/serviceaccount/token`
- `POD_NAMESPACE` environment variable set
- `INSTANCEHA_CR_NAME` environment variable set to the InstanceHa CR name
- `POD_NAME` environment variable set (used as `reportingInstance`)
- RBAC: the pod's namespaced Role must have `create` and `patch` permissions on `events`

### Event Catalog

| Reason | Type | Host | When |
|--------|------|------|------|
| `HostDown` | Warning | compute host | Compute service detected as down or stale |
| `FencingStarted` | Normal | compute host | Power-off fencing operation begins |
| `FencingSucceeded` | Normal | compute host | Fencing completed successfully |
| `FencingFailed` | Warning | compute host | Fencing operation failed |
| `EvacuationStarted` | Normal | compute host | VM evacuation begins (host-level) |
| `EvacuationSucceeded` | Normal | compute host | All VMs evacuated successfully (host-level) |
| `EvacuationFailed` | Warning | compute host | VM evacuation failed (host-level) |
| `InstanceEvacuationStarted` | Normal | compute host | Individual VM evacuation begins (per-instance, smart/orchestrated mode) |
| `InstanceEvacuationSucceeded` | Normal | compute host | Individual VM evacuated successfully |
| `InstanceEvacuationFailed` | Warning | compute host | Individual VM evacuation failed |
| `RecoveryCompleted` | Normal | compute host | Post-evacuation recovery workflow completed |
| `ProcessingFailed` | Warning | compute host | Unhandled exception during service processing |
| `ThresholdExceeded` | Warning | `cluster` | Failed host percentage exceeds THRESHOLD, evacuation skipped |
| `AggregateThresholdExceeded` | Warning | aggregate name | Failed hosts in aggregate exceed `instanceha:max_failures` metadata limit |
| `HostReachable` | Warning | compute host | Host reported down by Nova but still reachable via heartbeat (CHECK_HEARTBEAT) |
| `HostReenabled` | Normal | compute host | Host re-enabled after successful evacuation |
| `OrphanedHostRecovered` | Warning | compute host | Startup reconciliation recovered a fenced host left without evacuation marker |

### Event Structure

All events are created on the `InstanceHa` CR as the involved object:

```yaml
apiVersion: v1
kind: Event
metadata:
  name: instanceha.a1b2c3d4e5f67890
  namespace: openstack
involvedObject:
  apiVersion: instanceha.openstack.org/v1beta1
  kind: InstanceHa
  name: instanceha
  namespace: openstack
reason: FencingStarted
message: "[compute-0] Fencing host (power off)"
type: Normal
firstTimestamp: "2026-04-23T10:15:30Z"
lastTimestamp: "2026-04-23T10:15:30Z"
source:
  component: instanceha
reportingComponent: instanceha
reportingInstance: instanceha-0
```

### Event Lifecycle Example

A typical compute host failure produces this sequence of events:

```
1. HostDown        (Warning)  [compute-0] Compute host detected as down
2. FencingStarted  (Normal)   [compute-0] Fencing host (power off)
3. FencingSucceeded(Normal)   [compute-0] Host fenced successfully
4. EvacuationStarted(Normal)  [compute-0] Starting VM evacuation
5. EvacuationSucceeded(Normal)[compute-0] VM evacuation completed successfully
6. RecoveryCompleted(Normal)  [compute-0] Host recovery workflow completed
7. HostReenabled   (Normal)   [compute-0] Host re-enabled after successful evacuation
```

A fencing failure stops the sequence early:

```
1. HostDown        (Warning)  [compute-0] Compute host detected as down
2. FencingStarted  (Normal)   [compute-0] Fencing host (power off)
3. FencingFailed   (Warning)  [compute-0] Fencing failed
```

A threshold breach prevents any evacuation:

```
1. HostDown          (Warning) [compute-0] Compute host detected as down
2. HostDown          (Warning) [compute-1] Compute host detected as down
3. ThresholdExceeded (Warning) [cluster] Impacted computes (66.7%) exceed threshold (50%), evacuation skipped
```

### Querying Events

```bash
# All InstanceHA events
kubectl get events -n openstack --field-selector involvedObject.kind=InstanceHa

# Warning events only
kubectl get events -n openstack --field-selector involvedObject.kind=InstanceHa,type=Warning

# Events for a specific reason
kubectl get events -n openstack --field-selector involvedObject.kind=InstanceHa,reason=FencingFailed
```

### Prometheus Metrics

The agent exposes Prometheus-format metrics at `:8080/metrics` on the same HTTP server used for health checks. Metrics are served using the `prometheus_client` Python library.

**Counters** (monotonically increasing, reset on pod restart):

| Metric | Labels | Description |
|--------|--------|-------------|
| `instanceha_fencing_total` | `host`, `result` | Fencing operations (`started`/`succeeded`/`failed`) |
| `instanceha_evacuation_total` | `host`, `result` | Host-level evacuations |
| `instanceha_instance_evacuation_total` | `host`, `result` | Per-instance evacuations (smart/orchestrated) |
| `instanceha_host_down_total` | `host` | Host-down detections |
| `instanceha_host_reachable_total` | `host` | Hosts still reachable via heartbeat despite Nova reporting down |
| `instanceha_host_reenabled_total` | `host` | Hosts re-enabled after evacuation |
| `instanceha_threshold_exceeded_total` | | Evacuations blocked by global threshold |
| `instanceha_aggregate_threshold_exceeded_total` | `aggregate` | Evacuations blocked by per-aggregate threshold |
| `instanceha_recovery_completed_total` | `host` | Full recovery workflows completed |
| `instanceha_processing_failed_total` | `host` | Service processing failures |
| `instanceha_orphaned_host_recovered_total` | | Orphaned hosts recovered at startup |
| `instanceha_poll_cycles_total` | `result` | Poll cycles (`success`/`error`) |

**Gauges** (current value):

| Metric | Description |
|--------|-------------|
| `instanceha_poll_consecutive_failures` | Current consecutive Nova API failures |
| `instanceha_hosts_processing` | Hosts currently being processed |

**Histograms**:

| Metric | Labels | Buckets (s) | Description |
|--------|--------|-------------|-------------|
| `instanceha_instance_evacuation_duration_seconds` | `host` | 10, 30, 60, 120, 180, 300, 600 | Per-instance evacuation duration |

Each metric increment is co-located with the corresponding `_emit_k8s_event()` call, so the event catalog and metric catalog map 1:1.

---

## Advanced Features

### 1. Kdump Detection

**Purpose**: Detect when hosts are kdumping and evacuate them after fencing.

**Architecture**:
- Background UDP listener thread (port 7410)
- Magic number validation (0x1B302A40)
- Reverse DNS lookup (IP → hostname)
- Timestamp tracking with cleanup

**Behavior**:
1. **First poll**: When host is detected as down, start waiting for `KDUMP_TIMEOUT` seconds
2. **Kdump message received**: Host is fenced → evacuate immediately (with `resume=True`)
   - Host marked with `disabled_reason = "instanceha evacuation (kdump): {timestamp}"`
   - Power-off fencing is skipped (host already down/rebooting)
   - `_host_disable()` is still called (host not yet disabled from Nova's perspective)
3. **Timeout expired**: No kdump detected → proceed with normal evacuation
4. **Evacuation complete**: Update `disabled_reason` to preserve kdump marker
   - `disabled_reason = "instanceha evacuation complete (kdump): {timestamp}"`
   - Excludes service from resume category (has "evacuation complete")
   - Triggers re-enable delay (has "kdump" marker)
5. **Power-on optimization**: Skip power-on for kdump-fenced hosts during recovery
   - Kdump `final_action` in `/etc/kdump.conf` determines host behavior (poweroff/reboot/halt)
   - Skipping power-on avoids interfering with user-configured kdump recovery process
   - If the pod crashes and restarts during a kdump evacuation, the in-memory `kdump_fenced_hosts`
     set is lost. The host is then resumed as a normal orphaned evacuation and powered on after
     evacuation completes. This is the correct behavior: without kdump state, the agent cannot
     know whether the dump is still in progress, and leaving the host powered off indefinitely
     is worse than a potentially interrupted dump.
6. **Re-enablement delay**: After evacuation, wait 60s after last kdump message before unsetting force-down
   - Delays re-enablement while host is dumping memory and rebooting
   - Logs: `INFO {host} waiting for kdump to complete ({X}s since last message, waiting for 60s)`
   - Once 60s passed: `INFO {host} kdump messages stopped ({X}s since last message), proceeding with re-enable`
   - Only then attempts to unset force-down (requires migrations in `completed` state)
   - Kdump marker cleaned up from `kdump_fenced_hosts` set once force-down successfully unset

---

### 2. Heartbeat Detection

**Purpose**: Dual-channel failure detection — distinguish host-level failures from nova-compute crashes by listening for HMAC-authenticated heartbeat packets from compute nodes.

**Architecture**:
- Background UDP listener thread (port 7411, configurable)
- HBV2 protocol: magic number validation (`0x48425632`), HMAC-SHA256 verification, timestamp freshness check
- UTF-8 hostname extraction with regex validation
- Timestamp tracking per host

**Configuration**: `CHECK_HEARTBEAT: true`, `HEARTBEAT_TIMEOUT: 120`

**Behavior**:
1. Compute nodes send HMAC-authenticated UDP heartbeat packets every 30s (via systemd timer + `instanceha-heartbeat.py`)
2. The listener verifies each packet's HMAC-SHA256 signature and timestamp freshness (rejects packets older than 120s)
3. InstanceHA records timestamps in `heartbeat_hosts_timestamp`
4. When Nova reports a host as down, `_filter_reachable_hosts` checks:
   - If heartbeat received within `HEARTBEAT_TIMEOUT` seconds → host OS is alive → skip fencing (only nova-compute crashed)
   - If no heartbeat → host is genuinely down → proceed with fencing and evacuation
5. **Grace period**: During the first `HEARTBEAT_TIMEOUT` seconds after listener startup, all hosts bypass heartbeat filtering (no heartbeat history exists yet)

**Packet Format (HBV2)**:
```
Bytes 0-3:   Magic number 0x48425632 (uint32, network byte order)
Bytes 4-11:  Timestamp (uint64, network byte order, Unix epoch seconds)
Bytes 12-N:  Hostname (UTF-8, short hostname without domain)
Last 32B:    HMAC-SHA256 digest (over bytes 0 through N-32)
```

Minimum packet size: 45 bytes (4 magic + 8 timestamp + 1 hostname + 32 HMAC).

**Compute-Side Deployment** (edpm-ansible `edpm_instanceha_monitoring` role):
- `instanceha-heartbeat.py` — Python script using stdlib only (socket, struct, hmac)
- `instanceha-heartbeat.timer` — systemd timer (OnBootSec=10s, OnUnitActiveSec=30s)
- Runs as `nobody:nobody` (no root required for UDP send)
- HMAC key mounted from the `<instance-name>-heartbeat-hmac` secret via EDPM dataSource

**Security**:
- HMAC-SHA256 authentication prevents heartbeat spoofing
- Timestamp-based replay protection (120s window)
- Hostname validated against `VALIDATION_PATTERNS['hostname']` regex
- Maximum hostname length enforced
- Key rotation supported via current + previous key slots

---

### 3. Reserved Hosts

**Purpose**: Maintain spare capacity by auto-enabling reserved hosts and optionally forcing evacuation to them.

**Return Type**: Uses `ReservedHostResult` dataclass for consistent return values.

**Matching Strategies**:
1. **Aggregate-Based** (when `TAGGED_AGGREGATES: true`)
   - Matches reserved hosts in the same evacuable aggregate as the failed host
   - Verifies aggregate has `evacuable: true` metadata
2. **Zone-Based** (when `TAGGED_AGGREGATES: false`)
   - Matches reserved hosts in the same availability zone as the failed host

**Configuration Options**:
- `RESERVED_HOSTS`: Enable/disable reserved host management (default: false)
- `FORCE_RESERVED_HOST_EVACUATION`: Force evacuations to enabled reserved host (default: false)

**Forced Evacuation Workflow**:
1. `_enable_matching_reserved_host()` returns `ReservedHostResult` dataclass
2. `_manage_reserved_hosts()` returns `ReservedHostResult` dataclass
3. If `FORCE_RESERVED_HOST_EVACUATION=true` and result.hostname returned:
   - Pass `target_host` to evacuation functions
   - Nova API called with `servers.evacuate(server=id, host=target_host)`
4. If `FORCE_RESERVED_HOST_EVACUATION=false` or no host enabled:
   - Normal evacuation (Nova scheduler chooses destination)
   - `servers.evacuate(server=id)` without host parameter
5. If evacuation fails and a reserved host was activated:
   - Check if any VMs landed on the reserved host (`servers.list(host=target)`)
   - If VMs present: keep the reserved host active (it has workload, cannot be a spare)
   - If no VMs: `_return_reserved_host()` disables the host and returns it to the pool
   - If the VM check API call fails: err on the safe side and keep the reserved host

**Example Flow**:
```
Failed Host: compute-01 (in aggregate-A)
Reserved Hosts: reserved-01 (aggregate-A), reserved-02 (aggregate-B)

1. Match by aggregate → Enable reserved-01
2. If FORCE_RESERVED_HOST_EVACUATION=true:
   → Evacuate all VMs from compute-01 to reserved-01
3. If FORCE_RESERVED_HOST_EVACUATION=false:
   → Evacuate VMs (scheduler chooses from all available hosts)
```

---

### 4. Caching System

**Purpose**: Cache evacuable resources to reduce Nova API calls.

**Image Retrieval**: Uses strategy pattern with multiple fallback methods for Glance access.

**Cached Data**:
- Evacuable flavors (300s TTL)
- Evacuable images (300s TTL)
- Host servers mapping

**Thread-Safe Access**:
- Check with lock (fast read)
- API call outside lock (no blocking)
- Update with lock (fast write)

---

### 5. Threshold Protection

**Purpose**: Prevent mass evacuations during datacenter-level failures.

**Implementation**:
```python
# Filter to active services only — disabled and force-downed hosts excluded from denominator
active_services = [s for s in services if 'disabled' not in s.status and not s.forced_down]
threshold_percent = (len(compute_nodes) / len(active_services)) * 100 if active_services else 0

if threshold_percent > service.config.get_config_value('THRESHOLD'):
    logging.error('Impacted (%.1f%%) exceeds threshold', threshold_percent)
    return  # Do not evacuate
```

Both the standard path and the `TAGGED_AGGREGATES` path (`_count_evacuable_hosts`) use the same active-only filtering.

### Per-Aggregate Threshold

In addition to the global percentage-based threshold, operators can set an absolute failure limit per aggregate via the `instanceha:max_failures` metadata key on Nova aggregates. When `TAGGED_AGGREGATES` is enabled, the agent checks each evacuable aggregate's metadata after the global threshold passes. If the number of failed hosts in an aggregate exceeds its `instanceha:max_failures` value, evacuation is blocked for all failed hosts in that aggregate.

- **Activation**: Automatic when the metadata key is present (no config option needed)
- **Multi-aggregate semantics**: Most-restrictive-wins — if a host belongs to multiple aggregates and any one exceeds its limit, the host is blocked
- **K8s event**: `AggregateThresholdExceeded` (Warning) emitted per blocked aggregate
- **Metric**: `instanceha_aggregate_threshold_exceeded_total{aggregate}` counter

```
# Example: set on a Nova aggregate via OpenStack CLI
openstack aggregate set --property instanceha:max_failures=2 my-aggregate
```

---

## Region Handling

### Region Isolation

InstanceHA operates within a single OpenStack region. The region is specified in the `clouds.yaml` configuration file and determines which compute services the InstanceHA instance manages.

**Configuration**:
```yaml
clouds:
  overcloud:
    auth:
      username: admin
      project_name: admin
      auth_url: http://keystone:5000/v3
      user_domain_name: Default
      project_domain_name: Default
    region_name: regionOne  # Required field
```

**Behavior**:
- Nova client initialized with `region_name` parameter from configuration
- `services.list()` API call returns only services from the configured region
- Evacuation operations affect only compute services in the configured region
- Services from other regions are not visible to the InstanceHA instance

**Implementation**:
```python
def create_connection(self) -> Optional[OpenStackClient]:
    cloud_name = self.config.get_cloud_name()
    auth = self.config.clouds[cloud_name]["auth"]
    password = self.config.secure[cloud_name]["auth"]["password"]
    region_name = self.config.clouds[cloud_name]["region_name"]

    credentials = NovaLoginCredentials(
        username=auth["username"],
        password=password,
        project_name=auth["project_name"],
        auth_url=auth["auth_url"],
        user_domain_name=auth["user_domain_name"],
        project_domain_name=auth["project_domain_name"],
        region_name=region_name
    )
    return nova_login(credentials)
```

**Nova API Scoping**:
```python
def nova_login(credentials: NovaLoginCredentials) -> Optional[OpenStackClient]:
    auth = loader.load_from_options(
        auth_url=credentials.auth_url,
        username=credentials.username,
        password=credentials.password,
        project_name=credentials.project_name,
        user_domain_name=credentials.user_domain_name,
        project_domain_name=credentials.project_domain_name,
    )
    session = ksc_session.Session(auth=auth)
    # Nova client scoped to region_name
    nova = client.Client("2.73", session=session, region_name=credentials.region_name)
    return nova
```

### Multi-Region Deployments

For deployments spanning multiple OpenStack regions, deploy one InstanceHA instance per region. Each instance operates independently with its own configuration.

**Deployment Pattern**:
```
Region1:
  - InstanceHA instance configured with region_name: region1
  - Manages only compute services in region1

Region2:
  - InstanceHA instance configured with region_name: region2
  - Manages only compute services in region2

Region3:
  - InstanceHA instance configured with region_name: region3
  - Manages only compute services in region3
```

**Independence**:
- Each InstanceHA instance has separate configuration files
- Each instance connects to Nova API with different region_name
- No cross-region communication or coordination
- Failures in one region do not affect other regions
- Evacuation operations are isolated to each region

**Example Multi-Region Configuration**:

*Region1 Instance*:
```yaml
# clouds.yaml
clouds:
  overcloud:
    auth:
      username: admin
      project_name: admin
      auth_url: http://keystone:5000/v3
      user_domain_name: Default
      project_domain_name: Default
    region_name: region1
```

*Region2 Instance*:
```yaml
# clouds.yaml
clouds:
  overcloud:
    auth:
      username: admin
      project_name: admin
      auth_url: http://keystone:5000/v3
      user_domain_name: Default
      project_domain_name: Default
    region_name: region2
```

**Testing**:
- Region isolation tests verify Nova client receives correct region_name
- Tests confirm services.list() returns only same-region services
- Multi-region independence tests verify separate instances operate independently
- Configuration validation tests ensure region_name is required

---

## Authentication

InstanceHA supports two authentication methods for connecting to the Nova API:

### 1. Password Authentication (Default)

Uses `clouds.yaml` and `secure.yaml` for standard Keystone password authentication:

```python
def create_connection(self) -> Optional[OpenStackClient]:
    cloud_name = self.config.get_cloud_name()
    auth = self.config.clouds[cloud_name]["auth"]
    password = self.config.secure[cloud_name]["auth"]["password"]
    region_name = self.config.clouds[cloud_name]["region_name"]
    credentials = NovaLoginCredentials(...)
    return nova_login(credentials, ca_bundle=self.config.ssl_ca_bundle)
```

### 2. Application Credential Authentication

Uses Keystone Application Credentials for token-scoped authentication without storing user passwords.

**Activation**:
- Set `auth.applicationCredentialSecret` in the InstanceHa CR spec
- The controller mounts the referenced Secret and sets `AC_ENABLED=True` environment variable
- The Python service detects `AC_ENABLED` and loads credentials from the mounted path

**Credential Loading**:
```python
AC_CREDENTIALS_PATH = "/secrets/ac-credentials"

def _load_ac_credentials(auth_url: str, region_name: str) -> Optional[ACLoginCredentials]:
    """Load Application Credential from the mounted secret directory."""
    # Reads AC_ID and AC_SECRET files from the mounted secret
    return ACLoginCredentials(
        auth_url=auth_url,
        application_credential_id=ac_id,
        application_credential_secret=ac_secret,
        region_name=region_name,
    )
```

**Nova Login**:
```python
def nova_login_ac(credentials: ACLoginCredentials, ca_bundle=None) -> OpenStackClient:
    loader = loading.get_plugin_loader("v3applicationcredential")
    auth = loader.load_from_options(
        auth_url=credentials.auth_url,
        application_credential_id=credentials.application_credential_id,
        application_credential_secret=credentials.application_credential_secret,
    )
    session = ksc_session.Session(auth=auth, verify=verify, timeout=NOVA_API_TIMEOUT_SECONDS)
    nova = client.Client("2.73", session=session, region_name=credentials.region_name)
    nova.versions.get_current()  # Verify connectivity
    return nova
```

**Fallback**: If `AC_ENABLED` is set but credentials cannot be loaded, falls back to password authentication with a warning.

**Secret Format** (Kubernetes Secret with keys `AC_ID` and `AC_SECRET`):
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: instanceha-ac-credentials
type: Opaque
stringData:
  AC_ID: "application-credential-id"
  AC_SECRET: "application-credential-secret"
```

**Kubernetes Operator Integration**:
```yaml
apiVersion: instanceha.openstack.org/v1beta1
kind: InstanceHa
spec:
  auth:
    applicationCredentialSecret: instanceha-ac-credentials
```

---

## Startup Reconciliation

**Function**: `_reconcile_orphaned_hosts(conn)`

**Purpose**: Recover hosts left in an inconsistent state after a pod crash. Called once at startup before entering the main poll loop.

### Host Recovery

If the pod crashed after fencing a host (`forced_down=True`) but before setting the `disabled_reason` evacuation marker, the host would be incorrectly routed to the re-enable path instead of the evacuation path.

**Detection**: Finds services where `forced_down=True`, `state='down'`, but status is NOT `disabled` (missing the evacuation marker).

**Action**: Sets `disabled_reason` to `"instanceha evacuation (recovered): {timestamp}"` so the normal resume logic picks the host up for evacuation.

```python
for svc in services:
    if not (svc.forced_down and svc.state == 'down'):
        continue
    if 'disabled' in svc.status:
        continue
    # Orphaned — fenced but not marked for evacuation
    conn.services.disable_log_reason(svc.id, disable_reason)
    _emit_k8s_event(svc.host, 'OrphanedHostRecovered', ...)
```

### VM Unlock

If the pod crashed while evacuating VMs, some may be left locked with `locked_reason=instanceha-evacuation`. The reconciliation loop unlocks these VMs:

```python
for svc in services:
    if not (svc.forced_down and svc.state == 'down'):
        continue
    servers = conn.servers.list(search_opts={'host': svc.host, 'all_tenants': 1})
    for s in servers:
        if getattr(s, 'locked_reason', None) == LOCK_REASON_EVACUATION:
            conn.servers.unlock(s.id)
```

### Resume Behavior

Hosts recovered by startup reconciliation (or already marked with `"instanceha evacuation:"`) are processed with `resume=True`:
- **Power-off fencing is skipped** (host was already fenced before the crash)
- **Host disable is skipped** if the host is already `forced_down` and `disabled`
- **Evacuation runs** (idempotent — already-evacuated VMs are no-ops)
- **Power-on is performed** after evacuation completes, ensuring the host is brought back online

This ensures orphaned hosts are not left powered off indefinitely after a pod crash.

### K8s Event

Emits `OrphanedHostRecovered` (Warning) for each recovered host.

### Known Limitations

**Lock reconciliation scope**: The VM unlock sweep at startup only checks servers on hosts that are `forced_down AND state='down'`. This covers the realistic crash scenario (pod dies mid-evacuation while the host is still fenced). However, if the pod crashes after evacuation succeeds and post-recovery clears `forced_down`, but before the `finally` block unlocks the VMs, the locked VMs on their new (healthy) hosts would not be found by the reconciliation. This window is extremely narrow — the `finally` block in `_server_evacuate_future` runs per-server immediately after each evacuation completes, so the crash would need to occur between the evacuation completing and the `finally` executing. In practice, a SIGKILL (OOM, node crash) during concurrent evacuation would leave VMs on hosts that are still `forced_down`, which the current reconciliation handles correctly.

A broader scan (checking all servers with `locked_reason == LOCK_REASON_EVACUATION` regardless of host state) would close this theoretical gap but requires listing all servers in the cloud at startup, which may be expensive on large deployments.

---

## Graceful Shutdown

InstanceHA supports graceful shutdown via SIGTERM signal handling.

**Implementation**:
```python
def _sigterm_handler(signum, frame):
    logging.info("SIGTERM received, finishing in-flight work before shutdown")
    service.shutdown_event.set()

signal.signal(signal.SIGTERM, _sigterm_handler)
```

**Behavior**:
- `shutdown_event` is a `threading.Event` checked in the main loop condition (`while not service.shutdown_event.is_set()`)
- All `time.sleep()` calls replaced with `service.shutdown_event.wait(seconds)` — these return immediately when the event is set
- In-flight evacuations complete before shutdown (no abrupt termination)
- Logs `"Graceful shutdown complete"` on exit

**Kubernetes Integration**:
- The Deployment's `terminationGracePeriodSeconds` is set to 30 seconds
- The Deployment strategy is `Recreate` (not `RollingUpdate`) to avoid two instances running simultaneously

---

## Security

### 1. Input Validation

**SSRF Prevention**:
- URL validation using `ipaddress` module to block loopback and link-local addresses
- IP address validation (IPv4/IPv6) via `ipaddress.ip_address()`
- Port range validation (1-65535)

**Injection Prevention**:
- Kubernetes resource name validation
- Power action whitelisting
- Username validation

**Exception Handling**:
- URL parsing: catches `ValueError`, `AttributeError`, `TypeError`
- Flavor validation: catches `AttributeError`, `KeyError`, `TypeError`
- Kdump cleanup: catches `AttributeError`, `KeyError`
- All exception handlers use specific types instead of bare `Exception`

---

### 2. Server Locking

During smart/orchestrated evacuation, servers are locked before evacuation and unlocked after completion to prevent concurrent operations (e.g., user-initiated live migration) from interfering with the evacuation workflow.

```python
LOCK_REASON_EVACUATION = "instanceha-evacuation"

# In _server_evacuate_future:
try:
    connection.servers.lock(server.id, reason=LOCK_REASON_EVACUATION)
except Exception:
    logging.debug("Could not lock server %s (may already be locked)", server.id)

try:
    # ... evacuation logic ...
finally:
    try:
        connection.servers.unlock(server.id)
    except Exception:
        logging.debug("Could not unlock server %s (may have been deleted)", server.id)
```

- Lock uses a specific reason string to distinguish from user locks
- Startup reconciliation unlocks VMs left locked by a crashed instance (see [Startup Reconciliation](#startup-reconciliation))

---

### 3. Credential Security

**Password Handling**:
```python
# IPMI: Minimal environment with only PATH and password
env = {'PATH': os.environ.get('PATH', '/usr/bin:/usr/sbin'), 'IPMITOOL_PASSWORD': passwd}
cmd = ["ipmitool", "-U", login, "-E", ...]  # -E uses env var
```

**Safe Exception Logging**:
```python
_SECRET_PATTERNS = {
    'password': re.compile(r'\bpassword=[^\s)\'\"]+', re.IGNORECASE),
    'token': re.compile(r'\btoken=[^\s)\'\"]+', re.IGNORECASE),
    'secret': re.compile(r'\bsecret=[^\s)\'\"]+', re.IGNORECASE),
    'credential': re.compile(r'\bcredential=[^\s)\'\"]+', re.IGNORECASE),
    'application_credential_secret': re.compile(r'\bapplication_credential_secret=[^\s)\'\"]+', re.IGNORECASE),
    'application_credential_id': re.compile(r'\bapplication_credential_id=[^\s)\'\"]+', re.IGNORECASE),
    'auth': re.compile(r'\bauth=[^\s)\'\"]+', re.IGNORECASE),
    'json_password': re.compile(r'("(?:password|passwd|token|secret|credential|...)")\s*:\s*"[^"]*"', re.IGNORECASE),
}

def _safe_log_exception(msg: str, e: Exception, include_traceback: bool = False) -> None:
    safe_msg = str(e)
    for secret_word, pattern in _SECRET_PATTERNS.items():
        if secret_word == 'json_password':
            safe_msg = pattern.sub(r'\1: "***"', safe_msg)
        else:
            safe_msg = pattern.sub(f'{secret_word}=***', safe_msg)
    logging.error("%s: %s", msg, safe_msg)
```

Credential sanitization covers both `key=value` and JSON `"key": "value"` formats.

---

### 4. Heartbeat HMAC Authentication

Heartbeat packets are authenticated with HMAC-SHA256 (HBV2 protocol) to prevent UDP spoofing. The operator auto-generates a 32-byte key in the `<instance-name>-heartbeat-hmac` Kubernetes Secret during reconciliation.

**Packet format**: `[4B magic 0x48425632][8B timestamp BE][hostname UTF-8][32B HMAC-SHA256]`

- The HMAC covers everything before the signature (magic + timestamp + hostname)
- Timestamp provides replay protection (packets older than 120s are rejected)
- The receiver tries both `hmac-key` (current) and `hmac-key-previous` (rotation) keys
- Only HMAC-authenticated packets are accepted; unsigned packets are silently dropped

Key rotation is triggered via annotation (`instanceha.openstack.org/rotate-hmac-key`). The controller copies current→previous, generates a new key, and removes the annotation.

---

### 5. SSL/TLS Configuration

**Requests SSL Config**:
```python
def get_requests_ssl_config(self) -> Union[bool, str, tuple]:
    if not self.get_config_value('SSL_VERIFY'):
        return False  # Insecure

    if self.ssl_cert_path and self.ssl_key_path:
        return (self.ssl_cert_path, self.ssl_key_path)  # Client cert
    if self.ssl_ca_bundle:
        return self.ssl_ca_bundle  # CA bundle
    return True  # Default system CA

# SSL requests use dependency injection (no global state)
def _make_ssl_request(method: str, url: str, auth: tuple, timeout: int,
                      config_mgr: ConfigManager, **kwargs):
    ssl_config = config_mgr.get_requests_ssl_config()
    request_kwargs = {'auth': auth, 'timeout': timeout, **kwargs}

    if isinstance(ssl_config, tuple):
        request_kwargs['cert'] = ssl_config
    else:
        request_kwargs['verify'] = ssl_config

    return getattr(requests, method)(url, **request_kwargs)
```

**Metrics Endpoint TLS**:

The metrics/health HTTP server supports configurable TLS hardening via the CR spec:

```yaml
spec:
  metricsTLS:
    secretName: my-cert
    minTLSVersion: "1.3"
    cipherSuites: "HIGH:!aNULL:!MD5:!RC4:!3DES:!kRSA"
```

These values are passed as `METRICS_TLS_MIN_VERSION` and `METRICS_TLS_CIPHERS` environment variables to the Python process, which applies them to the `ssl.SSLContext`.

**Implementation**:
- Configuration passed explicitly as parameters
- No global mutable state

---

## Performance

### 1. Caching Performance

**Benchmark** (100 flavors, 100 images):
- First call: ~50ms (with API call)
- Cached call: ~0.1ms (without API call)

---

### 2. Concurrent Processing

**ThreadPoolExecutor** for smart evacuation:
```python
with concurrent.futures.ThreadPoolExecutor(max_workers=WORKERS) as executor:
    future_to_server = {
        executor.submit(_server_evacuate_future, conn, srv): srv
        for srv in evacuables
    }
```

**Concurrency**: WORKERS controls the number of hosts processed concurrently and, for smart/orchestrated mode, the per-host server concurrency (capped at `MAX_TOTAL_EVACUATION_THREADS=32`).

---

### 3. Memory Management

**Cleanup Strategies**:
- Kdump timestamp cleanup (>2000 entries)
- Host processing expiration
- Generic cleanup helper

---

## Testing

### Test Categories

**1. Core Unit Tests** (`test_unit_core.py`):
Core unit tests covering:
- Configuration management and validation
- Service initialization and caching
- Main function initialization and error handling
- Evacuation logic and tag checking
- Smart evacuation with success, failure, and exception path testing
- Thread safety and memory management
- Security and secret sanitization
- Reserved host management (aggregate and zone matching)
- FORCE_ENABLE configuration behavior

**2. Fencing Agents Tests** (`test_fencing_agents.py`):
- IPMI fencing operations (power on/off, status, timeouts)
- Redfish fencing operations (SSL, retries, power state verification)
- BMH/Metal3 fencing (Kubernetes API, bearer token auth, power-off wait)
- Noop fencing agent
- Fencing agent dispatch and validation

**3. Kdump Detection Tests** (`test_kdump_detection.py`):
- UDP message processing and magic number validation
- Kdump host timestamp tracking and cleanup
- Reverse DNS lookup integration
- Kdump timeout and delay behavior

**4. Security Validation Tests** (`test_security_validation.py`):
- SSRF prevention: URL validation, loopback/link-local blocking via `ipaddress` module
- Injection prevention: Port validation, power action whitelisting, username validation, Kubernetes resource validation
- Fencing validation: Power action validation, parameter validation

**5. Critical Error Path Tests** (`test_critical_error_paths.py`):
- Configuration errors: YAML parsing, missing files, permissions, type validation
- Nova API exceptions: NotFound, Forbidden, Unauthorized, connection failures
- Evacuation timeouts and fencing failures

**6. Evacuation Workflow Tests** (`test_evacuation_workflow.py`):
- Kdump resume disable logic and post-evacuation recovery error paths
- Process service step failures: Fencing, disable, reserved hosts, evacuation, recovery
- Reserved host return on partial evacuation failure: VMs present, empty host, API failure

**7. Configuration Feature Tests** (`test_config_features.py`):
- DISABLED, FORCE_ENABLE, LEAVE_DISABLED, TAGGED_AGGREGATES, DELAY, HASH_INTERVAL
- Critical services check: Scheduler validation

**8. Helper Functions Tests** (`test_helper_functions.py`):
- `_cleanup_filtered_hosts`, `_filter_processing_hosts`, `_prepare_evacuation_resources`, `_count_evacuable_hosts`

**9. Functional Tests** (`functional_test.py`):
- End-to-end evacuation workflows and large-scale scenarios
- Tagging logic combinations (flavors, images, aggregates)
- Reserved host management and kdump integration workflows

**10. Integration Tests** (`integration_test.py`):
- Service initialization and Nova connection
- Full evacuation and re-enabling workflows
- Performance, scaling, and error recovery

**11. Region Isolation Tests** (`test_region_isolation.py`):
- Nova client region scoping and multi-region independence
- Configuration requirement validation

**12. Coverage Gaps Tests** (`test_coverage_gaps.py`):
- Validation helpers: `_try_validate`, `_validate_fencing_params`, `_validate_fencing_inputs`
- Fencing agent dispatchers: `_fence_noop`, `_fence_ipmi`, `_fence_redfish`, `_fence_bmh`
- IPMI execution with retry logic: `_execute_ipmi_fence`
- Service resume eligibility: `_is_service_resume_candidate`
- Aggregate filtering: `_aggregate_ids`
- Traditional evacuation logic: `_traditional_evacuate`
- Step execution wrapper: `_execute_step`
- Redfish URL construction edge cases: `_build_redfish_url`
- Datetime parsing protection: `_is_service_stale` (None, malformed, missing attribute)
- Evacuation monitoring: `_monitor_evacuation` (completion, retry, timeout)
- Main loop backoff: exponential backoff on Unauthorized/DiscoveryFailure/generic errors
- Process stale services safety: empty nodes, threshold exceeded, disabled mode

**13. K8s Events Tests** (`test_k8s_events.py`):
- K8s credential reading and event emission
- Event lifecycle: fencing, evacuation, recovery, re-enable
- Error handling: missing credentials, failed API calls, missing CR name

**14. Thread Safety Tests** (`test_thread_safety.py`):
- Concurrent cache access and refresh
- Processing lock contention under parallel poll cycles
- Reserved host list thread-safe removal

**15. Heartbeat Detection Tests** (`test_heartbeat_detection.py`):
- UDP heartbeat message processing and magic number validation
- HMAC-SHA256 authentication and timestamp freshness verification
- Hostname extraction from HBV2 packet payload
- Heartbeat timestamp tracking and expiry
- Grace period behavior during listener startup
- Reachable host filtering based on heartbeat freshness
- Heartbeat listener thread lifecycle

**16. Heartbeat Scale Tests** (`test_heartbeat_scale.py`):
- Large-scale HMAC-authenticated heartbeat processing at 1000-node scale
- Concurrent heartbeat writes under load
- Cleanup threshold behavior with many hosts

**17. Orchestrated Evacuation Tests** (`test_orchestrated_evacuation.py`):
- Metadata extraction: `_get_evacuation_metadata` (priority parsing, clamping, defaults)
- Missing metadata warning: logs when enabled but no servers have metadata
- Group building: `_build_evacuation_groups` (grouping, priority sorting, mixed inputs)
- Phase execution: `_orchestrated_evacuate` (multi-phase success/failure, target_host passthrough)
- Branching logic: `_host_evacuate` orchestrated/smart/traditional routing
- Configuration: `ORCHESTRATED_RESTART` config key validation

**18. Aggregate Threshold Tests** (`test_aggregate_threshold.py`):
- Per-aggregate failure limit enforcement via `instanceha:max_failures` metadata
- Multi-aggregate host membership and most-restrictive-limit selection
- Interaction with global THRESHOLD percentage check

**19. IPv6 UDP Tests** (`test_ipv6_udp.py`):
- UDPSocketManager binding to IPv6 loopback, wildcard, and IPv4 addresses
- Heartbeat reception over IPv6 (single, multiple, native byte order)
- Kdump reception over IPv6 (single, multiple, invalid magic rejection)
- Dual-stack (`::`) listener accepting both IPv4 and IPv6 packets

### Coverage by Component

| Component | Coverage |
|-----------|----------|
| ConfigManager | 95% |
| Evacuation Logic | 80% |
| Smart Evacuation | 75% |
| Fencing | 70% |
| Kdump | 65% |
| Main Loop | 60% |
| Reserved Hosts | 55% |

### Test Patterns

**Exception Mocking**:
```python
class NotFound(Exception):
    pass
novaclient_exceptions = MagicMock()
novaclient_exceptions.NotFound = NotFound
sys.modules['novaclient.exceptions'] = novaclient_exceptions
```

**Performance Optimization**:
```python
with patch('instanceha.time.sleep'):
    with patch('instanceha.EVACUATION_POLL_INTERVAL_SECONDS', 0):
        result = _server_evacuate_future(conn, server)
```

---

## Deployment

### Kubernetes Deployment

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: instanceha          # matches the InstanceHa CR .metadata.name
spec:
  replicas: 1
  strategy:
    type: Recreate
  template:
    spec:
      serviceAccountName: instanceha-instanceha
      securityContext:
        fsGroup: 42401
      terminationGracePeriodSeconds: 30
      containers:
      - name: instanceha
        image: <resolved from infra-instanceha-config ConfigMap or RELATED_IMAGE env var>
        command: ["/usr/bin/python3", "-u", "/var/lib/instanceha/instanceha.py"]
        securityContext:
          runAsUser: 42401
          runAsGroup: 42401
          runAsNonRoot: true
          allowPrivilegeEscalation: false
          capabilities:
            drop: ["ALL"]
        env:
        - name: OS_CLOUD
          value: default                # from spec.openStackCloud
        - name: CONFIG_HASH
          value: <hash of all input configmaps/secrets>
        - name: INSTANCEHA_DISABLED
          value: "False"                # from spec.disabled
        - name: POD_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        - name: POD_NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
        - name: INSTANCEHA_CR_NAME
          value: instanceha             # the CR name
        - name: HEARTBEAT_PORT          # only set when > 0
          value: "7411"
        ports:
        - containerPort: 8080
          protocol: TCP
          name: metrics
        - containerPort: 7410
          protocol: UDP
          name: kdump
        - containerPort: 7411
          protocol: UDP
          name: heartbeat
        volumeMounts:
        - name: openstack-config
          mountPath: /home/cloud-admin/.config/openstack/clouds.yaml
          subPath: clouds.yaml
        - name: openstack-config-secret
          mountPath: /home/cloud-admin/.config/openstack/secure.yaml
          subPath: secure.yaml
        - name: fencing-secret
          mountPath: /secrets/fencing.yaml
          subPath: fencing.yaml
        - name: instanceha-script
          mountPath: /var/lib/instanceha/instanceha.py
          subPath: instanceha.py
          readOnly: true
        - name: instanceha-config
          mountPath: /var/lib/instanceha/config.yaml
          subPath: config.yaml
          readOnly: true
        livenessProbe:
          httpGet:
            path: /
            port: 8080
          initialDelaySeconds: 10
          periodSeconds: 30
          timeoutSeconds: 30
        readinessProbe:
          httpGet:
            path: /healthz
            port: 8080
          initialDelaySeconds: 5
          periodSeconds: 10
          timeoutSeconds: 10
      volumes:
      - name: openstack-config
        configMap:
          name: openstack-config          # from spec.openStackConfigMap
      - name: openstack-config-secret
        secret:
          secretName: openstack-config-secret  # from spec.openStackConfigSecret
          defaultMode: 0440
      - name: fencing-secret
        secret:
          secretName: fencing-secret       # from spec.fencingSecret
          defaultMode: 0440
      - name: instanceha-script
        configMap:
          name: instanceha-sh              # auto-generated: <cr-name>-sh
          defaultMode: 0644
      - name: instanceha-config
        configMap:
          name: instanceha-config          # from spec.instanceHaConfigMap
```

---

## Configuration Options Reference

This section describes each configuration option in the `_config_map`, including its purpose, validation rules, usage in the codebase, and testing coverage.

### Core Service Options

#### EVACUABLE_TAG
**Type**: String
**Default**: `'evacuable'`
**Range**: N/A

**Description**: Tag name used to identify resources (flavors, images, aggregates) that should be evacuated during host failures.

**Usage**:
- Used by tagging system to filter evacuable resources
- Checked against flavor extra specs, image properties, and aggregate metadata
- All three resource types use the same tag name for consistency

**Testing**:
- Unit tests verify tag loading from configuration
- Functional tests use custom tag values (`'test_tag'`, `'ha-enabled'`)
- Integration tests verify tag matching across flavors, images, and aggregates
- Test files: `test_instanceha.py:162,203`, functional tests

**Example**:
```yaml
config:
  EVACUABLE_TAG: 'ha-enabled'  # Custom tag for HA-enabled resources
```

---

#### DELTA
**Type**: Integer
**Default**: `30`
**Range**: `10-300` seconds

**Description**: Service staleness threshold. A compute service is considered stale (and eligible for evacuation) if its `updated_at` timestamp is older than `current_time - DELTA` seconds.

**Usage**:
- Used in main poll loop to calculate `target_date = datetime.utcnow() - timedelta(seconds=DELTA)`
- Services with `updated_at < target_date` are categorized as stale compute nodes
- Works in conjunction with Nova service heartbeat mechanism

**Testing**:
- Unit tests verify range validation (values <10 or >300 are clamped)
- Functional tests use custom values (60, 120) to test different staleness windows
- Integration tests verify stale service detection logic
- Test files: `test_instanceha.py:163,175,191`

**Example**:
```yaml
config:
  DELTA: 60  # Consider services stale after 60 seconds without heartbeat
```

**Notes**: Lower values provide faster failure detection but may cause false positives. Higher values reduce false positives but delay evacuation.

---

#### POLL
**Type**: Integer
**Default**: `45`
**Range**: `15-600` seconds

**Description**: Main poll loop interval. Controls how frequently InstanceHA queries Nova for service status and processes evacuations.

**Usage**:
- Sleep duration at the end of each main loop iteration
- Affects evacuation response time and API load

**Testing**:
- Unit tests verify range validation and default values
- Functional tests use various intervals to test timing behavior
- Test files: `test_instanceha.py:30,45`

**Example**:
```yaml
config:
  POLL: 30  # Check for failures every 30 seconds
```

**Notes**: Shorter intervals (15-30s) increase API load. Longer intervals (60-600s) delay evacuation.

---

#### THRESHOLD
**Type**: Integer
**Default**: `50`
**Range**: `0-100` (percentage)

**Description**: Maximum percentage of failed compute services before evacuation is blocked.

**Usage**:
- Denominator always excludes disabled and force-downed hosts (active services only)
- Calculation depends on `TAGGED_AGGREGATES` setting:
  - When `TAGGED_AGGREGATES=false`: `(failed_hosts / active_hosts) * 100`
  - When `TAGGED_AGGREGATES=true`: `(failed_hosts / active_evacuable_hosts) * 100`
- If threshold exceeded, evacuation is skipped and error logged

**Behavior**:
- **Without aggregate filtering** (`TAGGED_AGGREGATES=false`):
  - Calculates percentage against active compute services (excludes disabled/force-downed)
  - Example: 3 failed / 6 active = 50% (not 3/10 total = 30%)

- **With aggregate filtering** (`TAGGED_AGGREGATES=true`):
  - Calculates percentage against active hosts in evacuable aggregates
  - Example: 3 failed / 5 active evacuable = 60%

**Testing**:
- Unit tests verify percentage validation and clamping
- Functional tests verify threshold blocking behavior
- Integration tests simulate datacenter failures exceeding threshold
- Tests verify aggregate-aware threshold calculation
- Test files: `test_instanceha.py`, functional tests with 75% and 60% thresholds

**Example**:
```yaml
config:
  THRESHOLD: 75  # Block evacuation if >75% of (evacuable) hosts fail
  TAGGED_AGGREGATES: true  # Calculate against evacuable hosts only
```

**Notes**:
- Value of `0` blocks all evacuations (any failure percentage exceeds 0%)
- Value of `100` disables threshold checking (nothing can exceed 100%)
- Aggregate-aware calculation uses only evacuable hosts as denominator

---

#### WORKERS
**Type**: Integer
**Default**: `4`
**Range**: `1-50`

**Description**: Thread pool size for concurrent processing. Controls parallelism at two levels: host-level processing and per-host server evacuation.

**Usage**:
- **Host-level**: `_process_stale_services` uses `_run_concurrent` with `WORKERS` threads to process multiple failed hosts in parallel via `process_service`. This applies to all evacuation modes (traditional, smart, and orchestrated).
- **Per-host (smart/orchestrated only)**: Within each host's evacuation, `_smart_evacuate` and `_orchestrated_evacuate` use `_run_concurrent` to evacuate multiple VMs concurrently. The per-host thread count is calculated as `MAX_TOTAL_EVACUATION_THREADS (32) // WORKERS` to cap the total system-wide thread count.

**Testing**:
- Unit tests verify range validation (1-50)
- Functional tests use custom values (6, 8) to test concurrent evacuations
- Performance tests measure evacuation throughput with different worker counts
- Test files: `test_instanceha.py:191`, functional tests

**Example**:
```yaml
config:
  WORKERS: 8  # Process 8 hosts concurrently; each host gets up to 4 per-host threads (32/8)
```

**Notes**: Applies to all evacuation modes at the host level. For smart/orchestrated mode, also controls per-host server concurrency (capped by `MAX_TOTAL_EVACUATION_THREADS=32`).

---

#### DELAY
**Type**: Integer
**Default**: `0`
**Range**: `0-300` seconds

**Description**: Delay inserted before evacuating servers from a failed host.

**Usage**:
- Sleep duration after fencing and disabling, before evacuation starts
- Inserted in `_host_evacuate()` function before calling evacuation methods

**Behavior**:
- Applied per host before evacuating its VMs
- Executed after host is fenced and disabled
- Time window for storage systems to release file locks
- Applied for both smart and traditional evacuation modes

**Testing**:
- Minimal explicit testing (verified as configuration option)
- Tested indirectly through evacuation workflow tests

**Example**:
```yaml
config:
  DELAY: 15  # Wait 15 seconds after fencing before evacuation
```

**Notes**: Set to 0 for local storage or when locks aren't an issue.

---

#### LOGLEVEL
**Type**: String
**Default**: `'INFO'`
**Range**: `['DEBUG', 'INFO', 'WARNING', 'ERROR', 'CRITICAL']`

**Description**: Logging verbosity level. Controls the amount of diagnostic information logged by the service.

**Usage**:
- Validated against allowed log levels
- Applied to Python logging configuration
- Invalid values fall back to 'INFO' with warning

**Testing**:
- Unit tests verify log level validation
- Configuration tests check for proper level setting
- Test files: `test_instanceha.py:176`, config validation tests

**Example**:
```yaml
config:
  LOGLEVEL: 'DEBUG'  # Verbose logging for troubleshooting
```

**Notes**:
- `DEBUG`: Verbose, includes all decisions and API calls
- `INFO`: Standard operational logging
- `WARNING`: Only warnings and errors
- `ERROR`: Only errors

---

### Evacuation Behavior Options

#### SMART_EVACUATION
**Type**: Boolean
**Default**: `false`
**Range**: N/A

**Description**: Enable migration tracking and completion verification. When enabled, InstanceHA waits for evacuations to complete and verifies success, rather than fire-and-forget.

**Usage**:
- Controls evacuation mode selection in `_host_evacuate()`
- When `true`: Uses `_server_evacuate_future()` with polling and timeout
- When `false`: Uses `_server_evacuate()` fire-and-forget

**Behavior**:
- **Smart mode**:
  - Tracks migration status via Nova API
  - Polls every 5 seconds until completion or timeout (300s)
  - Separate error budgets: migration errors (max 5) and API errors (max 10)
  - Migration failures trigger automatic re-issue (reset state + new evacuate call)
  - Restores original VM state after success (`SHUTOFF` → stop, `ERROR` → reset_state)
  - Updates service `disabled_reason` on success/failure
  - Uses thread pool (`WORKERS`) for concurrent tracking

- **Traditional mode**:
  - Submits evacuation request and returns immediately
  - No completion verification
  - No error detection after submission
  - Lower resource usage

**Testing**:
- Unit tests verify both evacuation modes
- Smart evacuation tests cover: success, timeout, retry exhaustion, error states
- Traditional evacuation tests verify fire-and-forget behavior
- Test files: `test_instanceha.py:163,195`, evacuation workflow tests

**Example**:
```yaml
config:
  SMART_EVACUATION: true  # Track evacuations to completion
  WORKERS: 8              # Process 8 evacuations concurrently
```

**Notes**:
- Smart mode increases API load (polling) and memory usage (thread pool)
- Traditional mode does not track completion or verify success after submission

---

#### ORCHESTRATED_RESTART
**Type**: Boolean
**Default**: `false`
**Range**: N/A

**Description**: Enable priority-based evacuation ordering using server metadata. When enabled, servers are evacuated in priority-ordered phases rather than all at once.

**Usage**:
- Controls evacuation strategy selection in `_host_evacuate()`
- When `true`: Uses `_orchestrated_evacuate()` with priority-ordered phases
- Must be explicitly enabled; server metadata alone does not activate orchestrated mode
- A warning is logged if enabled but no servers have orchestration metadata

**Behavior**:
- Servers grouped by `instanceha:restart_group` metadata
- Groups sorted by highest `instanceha:restart_priority` (descending)
- Each group evacuated concurrently (using WORKERS thread pool)
- Groups processed sequentially (wait for completion before next group)
- Continue-on-failure: a failed group does not prevent subsequent groups from being processed

**Server Metadata Keys**:
- `instanceha:restart_priority`: Integer 1-1000 (default 500). Higher = evacuated first.
- `instanceha:restart_group`: String (optional). Servers with same group evacuate together.

**Testing**:
- Metadata extraction and validation tests
- Group building and priority sorting tests
- Orchestrated evacuation with phase sequencing tests
- Host evacuate branching logic tests
- Config-only activation: metadata ignored when ORCHESTRATED_RESTART=false

**Example**:
```yaml
config:
  ORCHESTRATED_RESTART: true
```

Server metadata:
```bash
openstack server set --property instanceha:restart_priority=900 --property instanceha:restart_group=database db-server
openstack server set --property instanceha:restart_priority=500 --property instanceha:restart_group=app app-server
openstack server set --property instanceha:restart_priority=100 web-server
```

---

#### SKIP_SERVERS_WITH_NAME
**Type**: List (of strings)
**Default**: `""` (empty string — no servers skipped)
**Range**: N/A

**Description**: List of glob patterns to exclude from evacuation. Any server whose name matches one of these patterns (using `fnmatch` glob syntax) is skipped during evacuation.

**Usage**:
- Evaluated in `_get_evacuable_servers()` before evacuability filtering
- Uses glob matching (`fnmatch.fnmatch(server.name, pattern)`) — supports `*` (any chars), `?` (single char), `[seq]` (character set)
- Skipped servers are logged with their IDs for auditability

**Behavior**:
- When empty (default): all servers in matching states are eligible for evacuation
- When populated: servers with names matching any listed glob pattern are excluded
- Filtering applied after state filtering (ACTIVE, ERROR, SHUTOFF) but before tag-based filtering

**Example** (comma-separated string, as used in ConfigMap):
```yaml
config:
  SKIP_SERVERS_WITH_NAME: "test-*, dev-instance-*"
```

This would skip servers named `test-vm-1`, `dev-instance-02`, etc.

**Use Cases**:
- Exclude test or development VMs from evacuation
- Protect specific workloads that should not be evacuated (e.g., VMs with local-only state)
- Temporary exclusion during maintenance of specific applications

---

#### EVACUATION_RETRIES
**Type**: Integer
**Default**: `5`
**Range**: 1–20

**Description**: Maximum number of times to retry a failed per-instance evacuation before giving up.

**Usage**:
- Used in `_monitor_evacuation()` to cap migration-error retries
- On each failure the server state is reset to `error` and evacuation is re-issued
- Does not affect API-error retries (`MAX_API_RETRIES`) or the overall evacuation timeout (`MAX_EVACUATION_TIMEOUT_SECONDS`)

**Example**:
```yaml
config:
  EVACUATION_RETRIES: 10
```

---

#### RESERVED_HOSTS
**Type**: Boolean
**Default**: `false`
**Range**: N/A

**Description**: Enable automatic management of reserved compute hosts.

**Usage**:
- Controls whether `_manage_reserved_hosts()` is called during evacuation
- Works with `FORCE_RESERVED_HOST_EVACUATION` to determine evacuation target

**Behavior**:
- Identifies reserved hosts by searching for disabled hosts with `disabled_reason` containing "reserved"
- Matches reserved hosts using:
  - **Aggregate-based** (when `TAGGED_AGGREGATES=true`): Match by shared evacuable aggregate
  - **Zone-based** (when `TAGGED_AGGREGATES=false`): Match by availability zone
- One matching reserved host is enabled per failed host
- Failed host capacity replaced by reserved host capacity

**Testing**:
- Functional tests verify aggregate-based and zone-based matching
- Integration tests verify reserved host enablement workflow
- Tests cover scenarios with multiple reserved hosts and aggregate combinations

**Example**:
```yaml
config:
  RESERVED_HOSTS: true
  TAGGED_AGGREGATES: true  # Use aggregate-based matching
```

**Workflow**:
```
1. compute-01 fails (in aggregate-A)
2. Find reserved hosts in aggregate-A → reserved-01
3. Enable reserved-01
4. Evacuate VMs from compute-01
   - If FORCE_RESERVED_HOST_EVACUATION=true → evacuate to reserved-01
   - If FORCE_RESERVED_HOST_EVACUATION=false → scheduler chooses destination
```

---

#### FORCE_RESERVED_HOST_EVACUATION
**Type**: Boolean
**Default**: `false`
**Range**: N/A

**Description**: Force evacuations to the enabled reserved host instead of letting Nova scheduler choose the destination. Only effective when `RESERVED_HOSTS=true`.

**Usage**:
- When `true` and a reserved host is enabled, pass `host=<reserved_host>` to `server.evacuate()`
- When `false`, call `server.evacuate()` without host parameter (scheduler chooses)

**Behavior**:
- VMs from failed host are evacuated to the specified reserved host
- Bypasses Nova scheduler destination selection
- Evacuations may fail if reserved host does not meet Nova placement requirements

**Testing**:
- Functional tests verify forced evacuation to specific reserved host
- Tests ensure evacuations fail gracefully if reserved host is incompatible

**Example**:
```yaml
config:
  RESERVED_HOSTS: true
  FORCE_RESERVED_HOST_EVACUATION: true  # Evacuate directly to reserved host
```

**Notes**: Nova placement validation still applies when target host is specified.

---

#### TAGGED_IMAGES
**Type**: Boolean
**Default**: `true`
**Range**: N/A

**Description**: Enable image-based evacuability filtering. When enabled, only servers using images with the evacuable tag are evacuated.

**Usage**:
- Controls whether image tags are checked in `is_server_evacuable()`
- Uses OR logic with `TAGGED_FLAVORS` and `TAGGED_AGGREGATES`

**Behavior**:
- When `true`: Check if server's image has `{EVACUABLE_TAG}: true` in properties
- When `false`: Ignore image tags (all images considered evacuable)
- If all tagging disabled and no tagged resources exist: evacuate all servers

**Testing**:
- Unit tests verify image tag checking logic
- Functional tests verify OR semantics with flavors and aggregates
- Test files: `test_instanceha.py:4575`, tagging logic tests

**Example**:
```yaml
config:
  TAGGED_IMAGES: true
  EVACUABLE_TAG: 'ha-enabled'
```

Image configuration:
```bash
openstack image set --property ha-enabled=true my-image
```


---

#### TAGGED_FLAVORS
**Type**: Boolean
**Default**: `true`
**Range**: N/A

**Description**: Enable flavor-based evacuability filtering. When enabled, only servers using flavors with the evacuable tag are evacuated.

**Usage**:
- Controls whether flavor tags are checked in `is_server_evacuable()`
- Uses OR logic with `TAGGED_IMAGES` and `TAGGED_AGGREGATES`

**Behavior**:
- When `true`: Check if server's flavor has `{EVACUABLE_TAG}: true` in extra specs
- When `false`: Ignore flavor tags (all flavors considered evacuable)
- If all tagging disabled and no tagged resources exist: evacuate all servers

**Testing**:
- Unit tests verify flavor tag checking logic
- Functional tests verify OR semantics with images and aggregates
- Test files: `test_instanceha.py:4576`, tagging logic tests

**Example**:
```yaml
config:
  TAGGED_FLAVORS: true
  EVACUABLE_TAG: 'ha-enabled'
```

Flavor configuration:
```bash
openstack flavor set --property ha-enabled=true m1.small
```


---

#### TAGGED_AGGREGATES
**Type**: Boolean
**Default**: `true`
**Range**: N/A

**Description**: Enable aggregate-based evacuability filtering. When enabled, only servers on hosts in aggregates with the evacuable tag are evacuated.

**Usage**:
- Controls whether aggregate tags are checked in `is_aggregate_evacuable()`
- Also controls reserved host matching strategy (aggregate vs zone)

**Behavior**:
- When `true`:
  - Only evacuate servers if host is in an aggregate with `{EVACUABLE_TAG}: true` metadata
  - Reserved hosts matched by shared aggregate
  - `THRESHOLD` percentage calculated against active evacuable hosts only
- When `false`:
  - Ignore aggregate tags (all aggregates considered evacuable)
  - Reserved hosts matched by availability zone
  - `THRESHOLD` percentage calculated against active compute services (excludes disabled/force-downed)

**Testing**:
- Functional tests verify aggregate-based filtering
- Integration tests verify reserved host matching changes based on this setting
- Tests verify threshold calculation uses evacuable host count
- Test files: `test_config_features.py`, aggregate tagging tests

**Example**:
```yaml
config:
  TAGGED_AGGREGATES: true
  EVACUABLE_TAG: 'ha-enabled'
  THRESHOLD: 50  # 50% of evacuable hosts (not all hosts)
```

Aggregate configuration:
```bash
openstack aggregate set --property ha-enabled=true production-hosts
```

**Notes**:
- Affects reserved host matching behavior
- Affects threshold calculation (percentage of active evacuable hosts vs active hosts)

---

#### LEAVE_DISABLED
**Type**: Boolean
**Default**: `false`
**Range**: N/A

**Description**: Keep compute services disabled after evacuation completion.

**Usage**:
- Controls whether services are included in re-enable processing
- When `true`: Services remain disabled indefinitely after evacuation
- When `false`: Services are automatically re-enabled when they come back up

**Behavior**:
- Affects re-enable filtering in `_process_reenabling()`
- When `true`: Services with "evacuation complete" marker are skipped during re-enable processing
- When `false`: Services progress through re-enable workflow (unset force-down → wait for up → enable)

**Testing**:
- Configuration feature tests verify re-enable filtering
- Integration tests verify services remain disabled when enabled
- Test files: `test_config_features.py`, re-enable workflow tests

**Example**:
```yaml
config:
  LEAVE_DISABLED: true  # Require manual intervention before re-enabling hosts
```

**Re-enable**: Requires manual `openstack compute service set --enable` command.

---

#### FORCE_ENABLE
**Type**: Boolean
**Default**: `false`
**Range**: N/A

**Description**: Skip migration completion checks before re-enabling services.

**Usage**:
- Controls whether migration completion is verified before unsetting `forced_down`
- When `true`: Unset `forced_down` immediately without checking migration status
- When `false`: Wait for all migrations to reach "completed" state before unsetting `forced_down`

**Behavior**:
- Normal workflow: Check migrations → wait for completion → unset force-down → wait for up → enable
- With `FORCE_ENABLE=true`: Skip migration check → unset force-down → wait for up → enable
- Kdump delay still respected (60s wait after last kdump message)

**Testing**:
- Configuration feature tests verify migration check bypass
- Tests verify kdump delay is still respected even with `FORCE_ENABLE=true`
- Test files: `test_config_features.py`, force-enable behavior tests

**Example**:
```yaml
config:
  FORCE_ENABLE: true  # Re-enable hosts immediately, don't wait for migrations
```

**Notes**: Kdump-fenced hosts still wait 60s after last kdump message before re-enable attempt.

---

### Kdump Detection Options

#### CHECK_KDUMP
**Type**: Boolean
**Default**: `false`
**Range**: N/A

**Description**: Enable kdump detection via UDP listener.

**Usage**:
- Controls whether UDP listener thread is started
- Affects evacuation timing and fencing behavior
- Triggers validation warning if `POLL=30` and `KDUMP_TIMEOUT=30`

**Behavior**:
- **Enabled**:
  - Start UDP listener on port 7410 (or `UDP_PORT` env var)
  - When host detected as down, wait up to `KDUMP_TIMEOUT` seconds for kdump message
  - If message received: fence host → evacuate with `resume=True` (skip power-off)
  - If timeout: proceed with normal evacuation (power off → evacuate)

- **Disabled**:
  - No UDP listener thread
  - Evacuation starts when host detected as down
  - Fencing includes power-off operation

**Kdump Message Protocol**:
- Magic number: `0x1B302A40` (4 bytes)
- UDP port: 7410
- Reverse DNS lookup to identify host
- Timestamp tracking with cleanup (>2000 entries)

**Testing**:
- Extensive kdump workflow tests
- UDP message processing tests
- Timeout and delay tests
- Test files: `test_instanceha.py:1022-1034,1339-1341`, kdump workflow tests

**Example**:
```yaml
config:
  CHECK_KDUMP: true
  KDUMP_TIMEOUT: 300  # Wait up to 5 minutes for kdump
```

Compute node `/etc/kdump.conf`:
```
fence_kdump_nodes instanceha-service-ip
fence_kdump_args -p 7410
```

**Notes**:
- Requires `fence_kdump` configuration on compute nodes
- Kdump messages are received immediately by background UDP listener thread (independent of `POLL` interval)

---

#### KDUMP_TIMEOUT
**Type**: Integer
**Default**: `30`
**Range**: `5-300` seconds

**Description**: Maximum time to wait for kdump messages after detecting a down host. Only used when `CHECK_KDUMP=true`.

**Usage**:
- Per-host timeout tracked from first detection of down state
- Timeout evaluated across multiple poll cycles
- After timeout, proceed with normal evacuation

**Behavior**:
- Host detected as down → record timestamp
- Each poll cycle: check if `current_time - detection_time > KDUMP_TIMEOUT`
- If kdump message received before timeout: immediate evacuation with kdump marker
- If timeout expires: normal evacuation workflow
- After evacuation complete, wait 60s after last kdump message before re-enable

**Testing**:
- Unit tests verify timeout calculation
- Functional tests verify timeout expiration behavior
- Integration tests verify multi-poll-cycle timeout tracking
- Test files: `test_instanceha.py:994,1001,1018-1019`, kdump timeout tests

**Example**:
```yaml
config:
  CHECK_KDUMP: true
  KDUMP_TIMEOUT: 300  # Wait 5 minutes for kdump
  POLL: 45            # Check every 45 seconds (7 poll cycles for 300s timeout)
```

**Timeout Calculation Example**:
```
POLL=45, KDUMP_TIMEOUT=300
- Cycle 1 (t=0s):   Host down, start waiting
- Cycle 2 (t=45s):  Still waiting (45 < 300)
- Cycle 3 (t=90s):  Still waiting (90 < 300)
- ...
- Cycle 7 (t=270s): Still waiting (270 < 300)
- Cycle 8 (t=315s): Timeout expired (315 > 300) → evacuate
```

**Notes**: Multiple poll cycles can occur during timeout period.

---

### Heartbeat Detection Options

#### CHECK_HEARTBEAT
**Type**: Boolean
**Default**: `false`
**Range**: N/A

**Description**: Enable heartbeat-based dual-channel failure detection. When enabled, compute nodes send periodic UDP heartbeat packets. If Nova reports a host as down but heartbeats are still arriving, the host OS is alive and only nova-compute has crashed — fencing is skipped.

**Usage**:
- Controls whether heartbeat UDP listener thread is started (port 7411)
- Affects host filtering in `_filter_reachable_hosts()` before evacuation

**Behavior**:
- **Enabled**:
  - Start UDP listener on port 7411 (or `HEARTBEAT_PORT` env var)
  - Record heartbeat timestamps per hostname
  - When host detected as down, check if heartbeat received within `HEARTBEAT_TIMEOUT`
  - If heartbeat recent: skip fencing (nova-compute crash, not host failure)
  - If no heartbeat: proceed with normal evacuation
  - Grace period: first `HEARTBEAT_TIMEOUT` seconds after listener startup, all hosts bypass heartbeat filtering

- **Disabled**:
  - No heartbeat listener thread
  - All down hosts processed through normal fencing/evacuation

**Compute-Side Requirements**:
- Deploy `edpm_instanceha_monitoring` role via edpm-ansible
- Installs `instanceha-heartbeat.py` script + systemd timer
- Sends HMAC-authenticated HBV2 packets every 30s (magic `0x48425632` + timestamp + hostname + HMAC-SHA256)
- HMAC key distributed automatically via the `<instance-name>-heartbeat-hmac` secret dataSource

**Example**:
```yaml
config:
  CHECK_HEARTBEAT: true
  HEARTBEAT_TIMEOUT: 120
```

---

#### HEARTBEAT_TIMEOUT
**Type**: Integer
**Default**: `120`
**Range**: `30-600` seconds

**Description**: Maximum time since last heartbeat before a host is considered unreachable. Also used as the grace period duration after listener startup.

**Usage**:
- Hosts with heartbeat received within `HEARTBEAT_TIMEOUT` seconds are skipped during evacuation
- Grace period: listener bypasses heartbeat filtering for the first `HEARTBEAT_TIMEOUT` seconds (no history exists yet)

**Example**:
```yaml
config:
  CHECK_HEARTBEAT: true
  HEARTBEAT_TIMEOUT: 180  # 3-minute window for heartbeat freshness
```

**Notes**: Should be at least 2x the heartbeat send interval (default 30s) to tolerate missed packets.

---

### Service Control Options

#### DISABLED
**Type**: Boolean
**Default**: `false`
**Range**: N/A

**Description**: Disable evacuation processing.

**Usage**:
- Checked at the beginning of evacuation processing
- When `true`: Log "Service disabled" and skip all evacuation logic
- When `false`: Normal operation

**Behavior**:
- Service runs and polls Nova API
- Service categorization occurs
- No fencing, disabling, or evacuation actions executed
- Health checks continue to update

**Testing**:
- Configuration feature tests verify evacuation skipping
- Test files: `test_config_features.py`, disabled mode tests

**Example**:
```yaml
config:
  DISABLED: true  # Temporarily disable evacuations for maintenance
```

**Use Cases**:
- Planned maintenance windows
- Debugging and testing
- Gradual rollout (enable service without evacuation first)
- Emergency brake (quickly disable evacuations without stopping service)


---

### Security Options

#### SSL_VERIFY
**Type**: Boolean
**Default**: `true`
**Range**: N/A

**Description**: Enable SSL/TLS certificate verification for HTTPS requests.

**Usage**:
- Controls `verify` parameter in `requests` library calls
- Affects Redfish fencing and Kubernetes API (BMH) connections
**Behavior**:
- **Enabled** (`true`):
  - Use `get_requests_ssl_config()` to determine SSL configuration
  - Options: system CA, custom CA bundle, client certificates
  - Certificate validation performed

- **Disabled** (`false`):
  - Certificate validation skipped
  - Warning logged on startup

**SSL Configuration Resolution**:
1. If `SSL_VERIFY=false`: return `False` (no verification)
2. If client cert+key available: return `(cert_path, key_path)` tuple
3. If CA bundle available: return CA bundle path
4. Otherwise: return `True` (system CA bundle)

**Testing**:
- Configuration tests verify SSL behavior
- Security tests verify proper SSL configuration
- Test files: `test_instanceha.py:196`, SSL configuration tests

**Example**:
```yaml
config:
  SSL_VERIFY: true  # Verify certificates (recommended)
```

**Environment Variables**:
```bash
export SSL_CA_BUNDLE=/path/to/ca-bundle.pem
export SSL_CERT_PATH=/path/to/client-cert.pem
export SSL_KEY_PATH=/path/to/client-key.pem
```

**Notes**: Warning message: "SSL verification is DISABLED - this is insecure for production use"

---

#### FENCING_TIMEOUT
**Type**: Integer
**Default**: `30`
**Range**: `5-120` seconds

**Description**: Timeout for fencing operations (IPMI, Redfish, BMH). Controls how long to wait for power operations before considering them failed.

**Usage**:
- Used in all three fencing agent implementations:
  - **IPMI**: Timeout for `ipmitool` commands
  - **Redfish**: HTTP request timeout and retry budget
  - **BMH**: Kubernetes API timeout and power-off wait time

**Behavior**:
- **IPMI**: Direct timeout for command execution
- **Redfish**:
  - Request timeout: `FENCING_TIMEOUT` (clamped to 5–300 seconds per request)
  - Retries on timeout or transient errors (max 3 attempts)
  - Total operation time: up to `3 * FENCING_TIMEOUT`
- **BMH**:
  - Kubernetes PATCH timeout: `FENCING_TIMEOUT`
  - Power-off verification timeout: `FENCING_TIMEOUT`
  - Total: up to `2 * FENCING_TIMEOUT`

**Testing**:
- Extensive fencing timeout tests
- Retry logic tests for Redfish
- Timeout expiration tests for all agents
- Test files: `test_instanceha.py:473,3004-3014`, fencing tests

**Example**:
```yaml
config:
  FENCING_TIMEOUT: 60  # 60 second fencing timeout
```

**Timeout Examples**:

*IPMI*:
```bash
timeout 60 ipmitool -I lanplus -H 192.168.1.10 ... power off
```

*Redfish*:
```python
# 3 retries × 20 second timeout = 60 seconds max
requests.post(url, timeout=20)  # Retry up to 3 times
```

*BMH*:
```python
# PATCH request: 60s timeout
# Wait for power-off: 60s timeout
# Total: up to 120s
```


---

#### HASH_INTERVAL
**Type**: Integer
**Default**: `60`
**Range**: `30-300` seconds

**Description**: Interval for updating the health check hash. Controls how frequently the service updates its health status indicator.

**Usage**:
- Hash is SHA256 of current timestamp
- Updated only if `current_time - last_update >= HASH_INTERVAL`
- Used by health check endpoint to verify service is running

**Behavior**:
- Health hash updated approximately every `HASH_INTERVAL` seconds
- Hash exposed via HTTP health check server on port 8080:
  - `GET /` — **Liveness probe**: returns 200 with current hash if `hash_update_successful`, 500 otherwise
  - `GET /healthz` — **Readiness probe**: returns 200 if `ready` flag is set (after first successful poll), 503 otherwise
  - `GET /metrics` — **Prometheus metrics**: returns all registered metrics in Prometheus text format
- `hash_update_successful` flag indicates service liveness
- `ready` flag set to `True` after first successful poll cycle completes
- Hash updated at most once per `HASH_INTERVAL` seconds

**Testing**:
- Unit tests verify hash update timing
- Integration tests verify health check behavior
- Test files: `test_instanceha.py:478`, health monitoring tests

**Example**:
```yaml
config:
  HASH_INTERVAL: 120  # Update health hash every 2 minutes
```

**Use Cases**:
- Kubernetes liveness/readiness probes
- External monitoring systems
- Service health verification


---

## References

- **Code**: `instanceha.py`
- **Tests**:
  - `test_unit_core.py` (core unit tests)
  - `test_fencing_agents.py` (fencing agent tests)
  - `test_kdump_detection.py` (kdump detection tests)
  - `test_security_validation.py` (security tests)
  - `test_critical_error_paths.py` (critical error tests)
  - `test_evacuation_workflow.py` (workflow tests)
  - `test_config_features.py` (config tests)
  - `test_helper_functions.py` (helper tests)
  - `functional_test.py` (functional tests)
  - `integration_test.py` (integration tests)
  - `test_region_isolation.py` (region tests)
  - `test_coverage_gaps.py` (coverage gap tests)
  - `test_k8s_events.py` (K8s events tests)
  - `test_thread_safety.py` (thread safety tests)
  - `test_orchestrated_evacuation.py` (orchestrated evacuation tests)
  - `test_heartbeat_detection.py` (heartbeat detection tests)
  - `test_heartbeat_scale.py` (heartbeat scale tests)
  - `test_aggregate_threshold.py` (per-aggregate failure threshold tests)
  - `test_ipv6_udp.py` (IPv6 UDP listener tests)
- **Documentation**:
  - This file (instanceha_architecture.md)
  - [instanceha_guide.md](instanceha_guide.md) — Operator deployment and configuration guide
  - [instanceha_prometheus.md](instanceha_prometheus.md) — Prometheus monitoring, alerting, and dashboards
- **OpenStack API**: https://docs.openstack.org/api-ref/compute/
- **Redfish**: https://www.dmtf.org/standards/redfish
- **Metal3**: https://metal3.io/
- **Python Type Hints**: https://docs.python.org/3/library/typing.html
- **Dataclasses**: https://docs.python.org/3/library/dataclasses.html

# InstanceHA Architecture Documentation

## Overview

InstanceHA is a high-availability service for OpenStack that automatically detects and evacuates instances from failed compute nodes.

**Version**: 2.1
**Code Size**: 2,872 lines
**Code Coverage**: 70%
**Test Suite**: 401 tests, ~26 seconds execution time

## Table of Contents

1. [Core Components](#core-components)
2. [Architecture Patterns](#architecture-patterns)
3. [Service Workflow](#service-workflow)
4. [Critical Services Validation](#critical-services-validation)
5. [Configuration System](#configuration-system)
6. [Evacuation Mechanisms](#evacuation-mechanisms)
7. [Fencing Agents](#fencing-agents)
8. [Advanced Features](#advanced-features)
9. [Region Handling](#region-handling)
10. [Security](#security)
11. [Performance](#performance)
12. [Testing](#testing)
13. [Configuration Options Reference](#configuration-options-reference)

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

@dataclass
class EvacuationStatus:
    """Status of an ongoing server evacuation."""
    completed: bool
    error: bool

@dataclass
class FencingCredentials:
    """Credentials and connection info for fencing operations."""
    agent: str
    ipaddr: str
    login: str
    passwd: str
    ipport: str = "443"
    timeout: int = 30
    uuid: str = "System.Embedded.1"
    tls: str = "false"
    token: Optional[str] = None
    namespace: Optional[str] = None
    host: Optional[str] = None

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
- **Environment Overrides**: OS_CLOUD, UDP_PORT, SSL paths
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
    'DISABLED': ConfigItem('bool', False),
    'SSL_VERIFY': ConfigItem('bool', True),
    'FENCING_TIMEOUT': ConfigItem('int', 30, 5, 120),
    'HASH_INTERVAL': ConfigItem('int', 60, 30, 300),
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

    # Initialization split into domain-specific methods
    self._initialize_health_state()
    self._initialize_cache()
    self._initialize_threading()
    self._initialize_kdump_state()
    self._initialize_processing_state()

    logging.info("InstanceHA service initialized successfully")
```

**State Management** (organized by domain):

*Health Monitoring* (`_initialize_health_state()`):
```python
self.current_hash = ""                          # Health check hash
self.hash_update_successful = True              # Health status
self._last_hash_time = 0                        # Hash timestamp
self._previous_hash = ""                        # Previous hash for validation
```

*Caching* (`_initialize_cache()`):
```python
self._host_servers_cache = {}                   # Host -> servers mapping
self._evacuable_flavors_cache = None            # Cached evacuable flavors
self._evacuable_images_cache = None             # Cached evacuable images
self._cache_timestamp = 0                       # Cache age
self._cache_lock = threading.Lock()             # Thread-safe access
self.evacuable_tag = config.get_evacuable_tag() # Cached config value
```

*Kdump Detection* (`_initialize_kdump_state()`):
```python
self.kdump_hosts_timestamp = defaultdict(float) # Host -> last kdump time
self.kdump_hosts_checking = defaultdict(float)  # Host -> check start time
self.kdump_listener_stop_event = threading.Event()  # Stop signal
self.kdump_fenced_hosts = set()                 # Kdump-fenced host tracking
```

*Processing Tracking* (`_initialize_processing_state()`):
```python
self.hosts_processing = defaultdict(float)      # Host -> processing start
self.processing_lock = threading.Lock()         # Thread-safe tracking
```

*Threading* (`_initialize_threading()`):
```python
self.health_check_thread = None                 # Health check thread handle
self.UDP_IP = ''                                # UDP listener IP
```

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

### 3. CloudConnectionProvider (Protocol)

**Purpose**: Abstract interface for cloud connection management.

**Pattern**: Protocol-based dependency injection for testability.

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
            logging.debug(f'Cleaned up processing tracking for {hostname}')
```

**UDP Socket Management**:
```python
class UDPSocketManager:
    """Context manager for UDP socket with proper resource cleanup."""
    def __enter__(self):
        self.socket = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.socket.bind((self.udp_ip, self.udp_port))
        return self.socket

    def __exit__(self, exc_type, exc_val, exc_tb):
        if self.socket:
            self.socket.close()
```

---

### 3. Thread Safety

**Cache Access**:
```python
# Pattern: read check → API call outside lock → write update
# Lock held only for dictionary operations, not during API calls

# 1. Check cache with lock (fast read)
with self._cache_lock:
    if self._evacuable_flavors_cache is not None:
        return self._evacuable_flavors_cache

# 2. Expensive API call outside lock (no blocking)
flavors = connection.flavors.list()
cache_data = [f.id for f in flavors if self._is_flavor_evacuable(f)]

# 3. Update cache with lock (fast write)
with self._cache_lock:
    self._evacuable_flavors_cache = cache_data
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
            logging.error(f"{error_msg} for {context}: {e}")
        return False

VALIDATION_PATTERNS = {
    'k8s_namespace': (r'^[a-z0-9]([-a-z0-9]*[a-z0-9])?$', 63),
    'k8s_resource': (r'^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]...)*$', 253),
    'power_action': (['on', 'off', 'status', 'ForceOff', ...], None),
    'ip_address': ('ip', None),
    'port': ('port', None),
}

def validate_input(value: str, validation_type: str, context: str) -> bool:
    # Special validation for URLs (block localhost, link-local)
    if validation_type == 'url':
        def _validate_url():
            p = urlparse(value)
            if p.scheme not in ['http', 'https'] or not p.netloc:
                return False
            h = p.hostname
            if h and (h.lower() in ['localhost', '127.0.0.1', '::1', '0.0.0.0'] or h.startswith('169.254.')):
                logging.error(f"Blocked localhost/link-local access in {context}")
                return False
            return True
        return _try_validate(_validate_url, "Invalid URL", context, log_error=False)
    # ... other validations using _try_validate helper
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
    │    - Check threshold                             │
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
    │ 6. Sleep (POLL seconds)                          │
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
    │   └─ _manage_reserved_hosts(conn, failed_service, reserved_hosts)
    │       ├─ Match by aggregate or zone
    │       └─ Enable matching reserved host
    │
    ├─ 4. Evacuate Servers
    │   └─ _host_evacuate(connection, failed_service, service)
    │       ├─ Get evacuable images/flavors
    │       ├─ List servers on host
    │       ├─ Filter evacuable servers
    │       └─ Execute evacuation
    │           ├─ Smart: _server_evacuate_future (track to completion)
    │           └─ Traditional: fire-and-forget
    │
    └─ 5. Post-Evacuation Recovery
        └─ _post_evacuation_recovery(conn, failed_service, service, resume)
            ├─ Power on host (_host_fence(host, 'on'))
            │   ├─ Skip if resume=True
            │   └─ Skip if kdump-fenced
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

def _categorize_services(services: List[Any], target_date: datetime) -> tuple:
    # Compute nodes: not disabled/forced-down, and (down OR stale)
    compute_nodes = (svc for svc in services
                     if not ('disabled' in svc.status or svc.forced_down)
                     and (svc.state == 'down' or
                          datetime.fromisoformat(svc.updated_at) < target_date))

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
    logging.error(f'Cannot evacuate: {error_msg}. Skipping evacuation.')
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
    agent: fence_ipmilan
    ipaddr: 192.168.1.10
    ipport: '623'
    login: admin
    passwd: ipmi_password

  compute-02.example.com:
    agent: fence_redfish
    ipaddr: 192.168.1.11
    ipport: '443'
    login: root
    passwd: redfish_password
    tls: 'true'
    uuid: System.Embedded.1

  compute-03.example.com:
    agent: fence_metal3
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
        logging.warning(f"Configuration {key} should be integer, got {type(value).__name__}, using default: {default}")
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
def _server_evacuate_future(connection, server) -> bool:
    # 1. Initiate evacuation
    response = _server_evacuate(connection, server.id)
    if not response.accepted:
        return False

    # 2. Wait before first poll
    time.sleep(INITIAL_EVACUATION_WAIT_SECONDS)

    # 3. Poll migration status until completion or timeout
    start_time = time.time()
    while True:
        if time.time() - start_time > MAX_EVACUATION_TIMEOUT_SECONDS:
            return False

        status = _server_evacuation_status(connection, server.id)

        if status.completed:
            return True
        if status.error:
            error_count += 1
            if error_count >= MAX_EVACUATION_RETRIES:
                return False
            time.sleep(EVACUATION_RETRY_WAIT_SECONDS)
            continue

        time.sleep(EVACUATION_POLL_INTERVAL_SECONDS)
```

**Behavior**:
- Tracks migration status via Nova API
- Retries on transient errors (max 5 retries)
- Times out after 300 seconds
- Detects and reports errors

---

### 3. Server Evacuability Logic

**Tagging System** (OR semantics):
1. **Flavor-based** (`TAGGED_FLAVORS: true`)
2. **Image-based** (`TAGGED_IMAGES: true`)
3. **Aggregate-based** (`TAGGED_AGGREGATES: true`)

**Evaluation Logic**:
```python
def is_server_evacuable(self, server, evac_flavors=None, evac_images=None):
    images_enabled = self.config.is_tagged_images_enabled()
    flavors_enabled = self.config.is_tagged_flavors_enabled()

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
    for agent_key, agent_func in FENCING_AGENTS.items():
        if agent_key in agent:
            return agent_func(host, action, fencing_data, service)
    logging.error("Unknown fencing agent: %s", agent)
    return False
```

### 1. IPMI (Intelligent Platform Management Interface)

**Agent**: `fence_ipmilan`

**Configuration**:
```yaml
compute-01:
  agent: fence_ipmilan
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

**Agent**: `fence_redfish`

**Configuration**:
```yaml
compute-02:
  agent: fence_redfish
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

**Agent**: `fence_metal3` (BMH)

**Configuration**:
```yaml
compute-03:
  agent: fence_metal3
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
6. **Re-enablement delay**: After evacuation, wait 60s after last kdump message before unsetting force-down
   - Delays re-enablement while host is dumping memory and rebooting
   - Logs: `INFO {host} waiting for kdump to complete ({X}s since last message, waiting for 60s)`
   - Once 60s passed: `INFO {host} kdump messages stopped ({X}s since last message), proceeding with re-enable`
   - Only then attempts to unset force-down (requires migrations in `completed` state)
   - Kdump marker cleaned up from `kdump_fenced_hosts` set once force-down successfully unset

---

### 2. Reserved Hosts

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

### 3. Caching System

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

### 4. Threshold Protection

**Purpose**: Prevent mass evacuations during datacenter-level failures.

**Implementation**:
```python
threshold_percent = (len(compute_nodes) / len(services)) * 100
if threshold_percent > service.config.get_threshold():
    logging.error(f'Impacted ({threshold_percent:.1f}%) exceeds threshold')
    return  # Do not evacuate
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
    nova = client.Client("2.59", session=session, region_name=credentials.region_name)
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

## Security

### 1. Input Validation

**SSRF Prevention**:
- URL validation (block localhost, link-local) with specific exception handling
- IP address validation (IPv4/IPv6)
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

### 2. Credential Security

**Password Handling**:
```python
# IPMI: Use environment variable
env = os.environ.copy()
env['IPMITOOL_PASSWORD'] = passwd
cmd = ["ipmitool", "-U", login, "-E", ...]  # -E uses env var
```

**Safe Exception Logging**:
```python
_SECRET_PATTERNS = {
    'password': re.compile(r'\bpassword=[^\s)\'\"]+', re.IGNORECASE),
    'token': re.compile(r'\btoken=[^\s)\'\"]+', re.IGNORECASE),
    'secret': re.compile(r'\bsecret=[^\s)\'\"]+', re.IGNORECASE),
    'credential': re.compile(r'\bcredential=[^\s)\'\"]+', re.IGNORECASE),
    'auth': re.compile(r'\bauth=[^\s)\'\"]+', re.IGNORECASE),
}

def _safe_log_exception(msg: str, e: Exception):
    safe_msg = str(e)
    for secret_word, pattern in _SECRET_PATTERNS.items():
        safe_msg = pattern.sub(f'{secret_word}=***', safe_msg)
    logging.error("%s: %s", msg, safe_msg)
```

---

### 3. SSL/TLS Configuration

**Requests SSL Config**:
```python
def get_requests_ssl_config(self) -> Union[bool, str, tuple]:
    if not self.is_ssl_verification_enabled():
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

**Concurrency**: WORKERS configuration controls number of concurrent evacuations.

---

### 3. Memory Management

**Cleanup Strategies**:
- Kdump timestamp cleanup (>100 entries)
- Host processing expiration
- Generic cleanup helper

---

## Testing

### Test Statistics

- **Total Tests**: 401 (203 unit + 16 security + 29 critical error + 12 workflow + 32 config + 18 helper + 68 functional + 22 integration + 7 region)
- **Code Coverage**: 71% (unit tests alone), 83% (with all tests)
- **Execution Time**: ~13 seconds (unit tests), ~26 seconds (all tests)

### Test Categories

**1. Unit Tests** (203 tests):
The unit test suite in `test_instanceha.py` includes tests for:
- Configuration management and validation
- Service initialization and caching
- Main function initialization and error handling
- Evacuation logic and tag checking
- Smart evacuation with success, failure, and exception path testing
- Kdump detection and UDP message processing
- Redfish, IPMI, and BMH fencing operations
- Thread safety and memory management
- Security and secret sanitization
- Input validation and SSRF prevention
- Reserved host management (aggregate and zone matching)
- FORCE_ENABLE configuration behavior

**2. Security Validation Tests** (16 tests):
The security validation test suite in `test_security_validation.py` covers:
- SSRF prevention (6 tests): URL validation, localhost/link-local blocking
- Injection prevention (7 tests): Port validation, power action whitelisting, username validation, Kubernetes resource validation
- Fencing validation (3 tests): Power action validation, parameter validation

**3. Critical Error Path Tests** (29 tests):
The critical error path test suite in `test_critical_error_paths.py` covers:
- Configuration errors (7 tests): YAML parsing, missing files, permissions, type validation
- Nova API exceptions (8 tests): NotFound, Forbidden, Unauthorized, connection failures
- Service disable validation (4 tests): Missing connection/service, attribute validation
- Evacuation timeouts (3 tests): Smart evacuation timeout, retry exhaustion
- Fencing failures (7 tests): Missing configuration, invalid parameters

**4. Evacuation Workflow Tests** (12 tests):
The evacuation workflow test suite in `test_evacuation_workflow.py` covers:
- Kdump resume disable logic (3 tests): Already disabled service handling, kdump-fenced host handling, new evacuation handling
- Post-evacuation recovery error paths (3 tests): Power-on failures, disable reason update failures, unexpected exceptions
- Process service step failures (6 tests): Fencing, disable, reserved hosts, evacuation, recovery step failures

**5. Configuration Feature Tests** (32 tests):
The configuration feature test suite in `test_config_features.py` covers:
- DISABLED configuration (2 tests): Skip evacuations when enabled
- Critical services check (8 tests): Scheduler and conductor validation
- FORCE_ENABLE configuration (8 tests): Migration completion bypass, kdump delay respect, forced_down two-stage process
- LEAVE_DISABLED configuration (2 tests): Service filtering for re-enable
- TAGGED_AGGREGATES configuration (2 tests): Aggregate-based filtering
- DELAY configuration (4 tests): Pre-evacuation delay validation
- HASH_INTERVAL configuration (6 tests): Health hash update interval

**6. Helper Functions Tests** (18 tests):
The helper functions test suite in `test_helper_functions.py` covers:
- _cleanup_filtered_hosts (4 tests): Host cleanup logic
- _filter_processing_hosts (5 tests): Processing state filtering
- _prepare_evacuation_resources (5 tests): Resource preparation
- _count_evacuable_hosts (4 tests): Evacuable host counting

**7. Functional Tests** (68 tests):
The functional test suite in `functional_test.py` covers:
- End-to-end evacuation workflows
- Large-scale scenarios (100+ hosts)
- Host state classification and filtering
- Tagging logic combinations (flavors, images, aggregates)
- Reserved host management (aggregate/zone matching and forced evacuation)
- Performance testing
- Kdump integration workflows

**8. Integration Tests** (22 tests):
The integration test suite in `integration_test.py` covers:
- Service initialization workflows
- Nova connection establishment
- Service categorization and filtering pipelines
- Full evacuation workflows with all components
- Re-enabling workflows with migration checks
- Performance and scaling under load
- Error handling and recovery scenarios

**9. Region Isolation Tests** (7 tests):
The region isolation test suite in `test_region_isolation.py` covers:
- Nova client region scoping verification
- Service list filtering by region
- Evacuation operation region boundaries
- Multi-region deployment independence
- Region name authentication chain validation
- Configuration requirement validation

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
  name: instanceha
spec:
  replicas: 1
  template:
    spec:
      containers:
      - name: instanceha
        image: quay.io/openstack-k8s-operators/instanceha:latest
        env:
        - name: OS_CLOUD
          value: overcloud
        volumeMounts:
        - name: config
          mountPath: /var/lib/instanceha
        - name: clouds
          mountPath: /home/cloud-admin/.config/openstack
        - name: fencing
          mountPath: /secrets
        livenessProbe:
          httpGet:
            path: /
            port: 8080
        readinessProbe:
          httpGet:
            path: /
            port: 8080
      volumes:
      - name: config
        configMap:
          name: instanceha-config
      - name: clouds
        secret:
          secretName: clouds-yaml
      - name: fencing
        secret:
          secretName: fencing-credentials
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
- Calculation depends on `TAGGED_AGGREGATES` setting:
  - When `TAGGED_AGGREGATES=false`: `(failed_hosts / total_hosts) * 100`
  - When `TAGGED_AGGREGATES=true`: `(failed_hosts / total_evacuable_hosts) * 100`
- If threshold exceeded, evacuation is skipped and error logged

**Behavior**:
- **Without aggregate filtering** (`TAGGED_AGGREGATES=false`):
  - Calculates percentage against all compute services
  - Example: 5 failed / 10 total = 50%

- **With aggregate filtering** (`TAGGED_AGGREGATES=true`):
  - Calculates percentage against only hosts in evacuable aggregates
  - Example: 5 failed / 8 evacuable = 62.5% (vs 5/10=50% without filtering)

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
- Value of `0` disables threshold checking
- Value of `100` blocks all evacuations
- Aggregate-aware calculation uses only evacuable hosts as denominator

---

#### WORKERS
**Type**: Integer
**Default**: `4`
**Range**: `1-50`

**Description**: Thread pool size for concurrent smart evacuations. Controls how many server evacuations can be processed simultaneously.

**Usage**:
- Used with `ThreadPoolExecutor` for parallel evacuation tracking
- Only applies when `SMART_EVACUATION=true`
- Higher values increase concurrency but consume more resources

**Testing**:
- Unit tests verify range validation (1-50)
- Functional tests use custom values (6, 8) to test concurrent evacuations
- Performance tests measure evacuation throughput with different worker counts
- Test files: `test_instanceha.py:191`, functional tests

**Example**:
```yaml
config:
  WORKERS: 8  # Process 8 evacuations concurrently
```

**Notes**: Only relevant when `SMART_EVACUATION=true` (traditional mode doesn't use threads).

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
  - Polls every 10 seconds until completion or timeout (300s)
  - Retries on transient errors (max 5 retries)
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
  - `THRESHOLD` percentage calculated against evacuable hosts only
- When `false`:
  - Ignore aggregate tags (all aggregates considered evacuable)
  - Reserved hosts matched by availability zone
  - `THRESHOLD` percentage calculated against all compute services

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
- Affects threshold calculation (percentage of evacuable hosts vs all hosts)

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
- Affects service categorization in `_categorize_services()`
- When `true`: Services with "evacuation complete" marker are excluded from `reenable` list
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
- Timestamp tracking with cleanup (>100 entries)

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
  - Request timeout: `FENCING_TIMEOUT / 3` (allows 3 retries)
  - Total operation time: up to `FENCING_TIMEOUT`
  - Retries on timeout or transient errors (max 3 attempts)
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
- Hash exposed via HTTP endpoint for monitoring
- `hash_update_successful` flag indicates service health
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

- **Code**: `instanceha.py` (2,872 lines)
- **Tests**: 401 tests across 9 test files
  - `test_instanceha.py` (203 unit tests)
  - `test_security_validation.py` (16 security tests)
  - `test_critical_error_paths.py` (29 critical error tests)
  - `test_evacuation_workflow.py` (12 workflow tests)
  - `test_config_features.py` (32 config tests)
  - `test_helper_functions.py` (18 helper tests)
  - `functional_test.py` (68 functional tests)
  - `integration_test.py` (22 integration tests)
  - `test_region_isolation.py` (7 region tests)
- **Documentation**: This file (INSTANCEHA_ARCHITECTURE.md)
- **OpenStack API**: https://docs.openstack.org/api-ref/compute/
- **Redfish**: https://www.dmtf.org/standards/redfish
- **Metal3**: https://metal3.io/
- **Python Type Hints**: https://docs.python.org/3/library/typing.html
- **Dataclasses**: https://docs.python.org/3/library/dataclasses.html

# InstanceHA Test Suite

Test suite for the InstanceHA service.

## Test Statistics

- **Total Tests**: 401 (203 unit + 16 security + 29 critical error + 12 workflow + 26 config + 18 helper + 68 functional + 22 integration + 7 region)
- **Code Coverage**: 71% (unit tests alone), 83% (with all tests)
- **Execution Time**: ~13 seconds (unit tests), ~26 seconds (all tests)
- **Code Size**: 2,871 lines
- **Status**: All tests passing ✅

## Test Structure

The test suite is organized into nine main categories:

### 1. Unit Tests (`test_instanceha.py`)
- **203 tests** individual components and isolated functionality
- Configuration management and validation
- Service initialization and caching
- Main function initialization and error handling
- Evacuation logic and tag checking
- Smart evacuation with success, failure, and exception path testing
- WORKERS configuration and concurrent evacuation limiting
- Kdump detection and UDP message processing
- Redfish, IPMI, and BMH fencing operations
- Thread safety and memory management
- Security and secret sanitization
- Input validation and SSRF prevention

### 2. Security Validation Tests (`test_security_validation.py`)
- **16 tests** security-critical input validation and SSRF prevention
- **SSRF Prevention** (6 tests):
  - URL validation blocks localhost addresses
  - URL validation blocks loopback IPs (127.0.0.1, ::1, 0.0.0.0)
  - URL validation blocks link-local addresses (169.254.x.x for AWS metadata)
  - URL validation allows legitimate URLs
  - URL validation rejects malformed URLs
  - Redfish URL building rejects invalid configurations
- **Injection Prevention** (7 tests):
  - Port validation rejects out-of-range ports
  - Port validation rejects non-numeric values
  - Power action validation uses strict whitelisting
  - Username validation rejects shell metacharacters
  - Kubernetes resource name validation
  - Kubernetes namespace validation
- **Fencing Validation** (3 tests):
  - Host fence rejects invalid power actions
  - Host fence validates actions before lookup
  - Host fence requires host parameter

### 3. Critical Error Path Tests (`test_critical_error_paths.py`)
- **29 tests** critical error handling paths
- **Configuration Errors** (7 tests):
  - YAML parsing errors (corrupted files)
  - Missing configuration files
  - Permission denied on config files
  - Invalid configuration format (not dictionary)
  - Type validation failures (non-integer, non-boolean)
  - Invalid log level validation
- **Nova API Exceptions** (8 tests):
  - Server evacuation with NotFound exception
  - Server evacuation with Forbidden exception
  - Server evacuation with Unauthorized exception
  - Evacuation status check with no connection
  - Evacuation status check with API failure
  - Nova login with DiscoveryFailure
  - Nova login with Unauthorized
- **Service Disable Validation** (4 tests):
  - Host disable with missing connection
  - Host disable with missing service
  - Host disable with service missing ID attribute
  - Host disable with service missing host attribute
- **Evacuation Timeouts** (3 tests):
  - Smart evacuation timeout handling
  - Smart evacuation retry exhaustion
  - Smart evacuation with invalid server object
- **Fencing Failures** (7 tests):
  - Host fence with no configuration
  - Host fence with invalid fencing data
  - Host fence with missing agent
  - Redfish reset with missing parameters
  - Redfish reset with invalid URL
  - BMH fence with missing parameters
  - BMH fence with invalid action

### 4. Evacuation Workflow Tests (`test_evacuation_workflow.py`)
- **12 tests** evacuation workflow paths
- **Kdump Resume Disable Logic** (3 tests):
  - Resume with already disabled service skips disable step
  - Kdump-fenced hosts with resume=True still call disable
  - New evacuation (resume=False) always calls disable
- **Post-Evacuation Recovery Error Paths** (3 tests):
  - Recovery failure when power-on fails
  - Recovery continues when disable reason update fails (non-critical)
  - Recovery handles unexpected exceptions during power-on
- **Process Service Step Failures** (6 tests):
  - Fencing step failure stops processing immediately
  - Disable step failure stops processing immediately
  - Reserved hosts step failure stops processing immediately
  - Evacuation step failure stops processing immediately
  - Recovery step failure stops processing immediately
  - All steps succeeding results in success

### 5. Configuration Feature Tests (`test_config_features.py`)
- **26 tests** configuration parameter behaviors
- **DISABLED Configuration** (2 tests):
  - DISABLED=True skips all evacuations
  - DISABLED=False processes evacuations normally
- **Critical Services Check** (2 tests):
  - All schedulers down blocks evacuation
  - At least one scheduler up allows evacuation
- **FORCE_ENABLE Configuration** (8 tests):
  - FORCE_ENABLE=True bypasses migration completion check
  - FORCE_ENABLE=False waits for migration completion
  - FORCE_ENABLE=True still respects kdump re-enable delay
  - FORCE_ENABLE works correctly with completed migrations
  - FORCE_ENABLE handles forced_down two-stage process correctly
  - FORCE_ENABLE respects service state (down vs up)
  - Error state migrations are properly ignored
  - Migration query parameters validated
- **LEAVE_DISABLED Configuration** (2 tests):
  - LEAVE_DISABLED=True filters instanceha-evacuated services from re-enable
  - LEAVE_DISABLED=False enables all services including instanceha-evacuated
- **TAGGED_AGGREGATES Configuration** (2 tests):
  - TAGGED_AGGREGATES=True filters compute nodes by aggregate metadata
  - TAGGED_AGGREGATES=False does not filter by aggregate
- **DELAY Configuration** (4 tests):
  - DELAY=0 does not pause before evacuation
  - DELAY>0 pauses correctly before evacuation
  - DELAY works with SMART_EVACUATION enabled
  - DELAY boundary value testing (min=0, max=300)
- **HASH_INTERVAL Configuration** (6 tests):
  - HASH_INTERVAL default value verification (60 seconds)
  - HASH_INTERVAL prevents frequent hash updates
  - HASH_INTERVAL allows updates after interval elapsed
  - HASH_INTERVAL custom override capability
  - HASH_INTERVAL boundary value testing (min=30, max=300)
  - Hash update failure detection (duplicate hash)

### 6. Helper Functions Tests (`test_helper_functions.py`)
- **18 tests** internal helper function logic
- **_cleanup_filtered_hosts** (4 tests):
  - Basic cleanup of hosts filtered from processing
  - Handling empty host sets
  - No-op when all hosts are finalized
  - Thread-safe operation verification
- **_filter_processing_hosts** (5 tests):
  - Basic filtering of hosts already being processed
  - Cleanup of expired processing entries
  - Marking new hosts for processing
  - Handling empty input lists
  - Resume list filtering
- **_prepare_evacuation_resources** (5 tests):
  - Basic resource preparation (cache refresh, filters)
  - Handling empty compute node lists
  - Reserved hosts inclusion when enabled
  - Tagged images/flavors retrieval
  - Filtering hosts without servers
- **_count_evacuable_hosts** (4 tests):
  - Basic host counting in evacuable aggregates
  - Handling scenarios with no aggregates
  - Exception handling and fallback behavior
  - Overlapping aggregates (unique host counting)

### 7. Functional Tests (`functional_test.py`)
- **68 tests** end-to-end scenarios
- Evacuation workflows
- Large-scale evacuation scenarios
- Host state classification and filtering
- Tagging logic combinations (images, flavors, aggregates)
- Reserved hosts management and forced evacuation
- Performance testing
- Kdump integration workflows
- Error handling and edge cases

### 8. Integration Tests (`integration_test.py`)
- **22 tests** cross-component interactions
- Service initialization workflows
- Nova connection establishment
- Service categorization and filtering pipelines
- Evacuation workflows with all components
- Re-enabling workflows with migration checks
  - Two-stage re-enabling: unset force_down first, then enable when up
  - force_down unset verification
  - Disabled services NOT enabled until state='up'
  - Disabled services enabled when back up with migrations complete
  - FORCE_ENABLE + LEAVE_DISABLED interaction testing
- Performance and scaling
- Error handling and recovery scenarios

### 9. Region Isolation Tests (`test_region_isolation.py`)
- **7 tests** region scoping and multi-region safety
- **Nova Client Region Scoping** (1 test):
  - Verify Nova client created with correct region_name parameter
- **Region Isolation** (2 tests):
  - Services from different regions never mixed in service list
  - Evacuation operations only affect same-region services
- **Multi-Region Deployments** (2 tests):
  - Multiple InstanceHA instances in different regions operate independently
  - Region name correctly passed through authentication chain
- **Configuration Requirements** (2 tests):
  - region_name required in clouds.yaml configuration
  - Empty region_name handling

## Running Tests

### Run All Tests
```bash
./run_tests.sh
```

### Run Individual Test Suites
```bash
# Unit and integration tests
python3 test_instanceha.py

# Security validation tests only
python3 test_security_validation.py

# Critical error path tests only
python3 test_critical_error_paths.py

# Evacuation workflow tests only
python3 test_evacuation_workflow.py

# Configuration feature tests only
python3 test_config_features.py

# Helper functions tests only
python3 test_helper_functions.py

# Functional tests only
python3 functional_test.py

# Integration tests only
python3 integration_test.py

# Region isolation tests only
python3 test_region_isolation.py
```

### Run Coverage Analysis
```bash
# Quick coverage (unit tests only)
PYTHONPATH="../../templates/instanceha/bin:$PYTHONPATH" \
  python3 -m coverage run --source=../../templates/instanceha/bin test_instanceha.py
python3 -m coverage report
```

## Test Coverage

### Overall Coverage
- **Unit tests alone**: 71% coverage (203 tests)
- **Core test suites**: 77% coverage (286 tests)
- **All tests**: 83% coverage (401 tests)

### Uncovered Code (17%)
The remaining uncovered lines primarily include:
- Main event loop and initialization
- Exception handlers
- Error message paths
- Debug logging branches
- Configuration validation edge cases
- Timeout and retry paths

### Core Functionality
- Main function initialization with config loading and error handling
- Service initialization and configuration loading
- Nova API connection establishment
- Service state categorization (stale, resume, re-enable)
  - `disabled_reason` markers prevent resume loops:
    - "instanceha evacuation: {timestamp}" → service needs resume/completion
    - "instanceha evacuation complete: {timestamp}" → service ready for re-enable
- Host and server filtering logic
- Evacuation workflow execution
- Service re-enabling after evacuation
  - Two-stage re-enabling process:
    1. Unset force_down first (allows nova-compute to report status)
    2. Wait for service to report state='up'
    3. Then enable disabled services
  - Migration completion verification before starting re-enable process
  - Resume bug fix: hosts not powered on repeatedly during resume cycles
  - Resume loop fix: disabled_reason updated to prevent re-processing
- Migration status tracking
- Cache lifecycle management

### Advanced Features
- **Tagging and Filtering**:
  - Flavor-based tagging and filtering
  - Image-based tagging and filtering
  - Aggregate-based filtering
  - Combined tag logic (OR semantics)
- **Reserved Host Management**:
  - Aggregate-based matching (with evacuable verification)
  - Zone-based matching
  - No match scenarios (different aggregates, non-evacuable aggregates)
  - Pool exhaustion handling
  - Forced evacuation to reserved hosts
  - Configuration loading (`FORCE_RESERVED_HOST_EVACUATION`)
  - End-to-end integration through `process_service()`
  - Smart evacuation mode compatibility
  - Multiple matching hosts (first selection)
- **Smart Evacuation**:
  - Successful evacuation scenarios
  - Migration tracking with status polling
  - Timeout handling
  - Retry logic on transient errors
  - Partial failure scenarios with host marking
  - Exception handling during evacuation with proper host marking
- **WORKERS Configuration**:
  - ThreadPoolExecutor initialization with correct max_workers value
  - Concurrent evacuation limiting (10 servers with WORKERS=4)
  - Thread-safe concurrent execution tracking
  - Different WORKERS values (1, 4, 8, 50)
- **Kdump Detection**:
  - UDP listener thread operation
  - Message parsing and validation (magic number 0x1B302A40)
  - Waiting mechanism for KDUMP_TIMEOUT before evacuation
  - Immediate evacuation when kdump messages received (kdump-fenced)
  - Timeout-based evacuation when no kdump detected
  - Power-on skip optimization for kdump-fenced hosts
  - Cleanup of old entries and tracking state
  - **Disable Logic** (new tests):
    - Kdump-fenced hosts call `_host_disable` despite `resume=True`
    - Resume evacuations skip `_host_disable` (already disabled)
    - New evacuations call both fence and disable
    - Edge case handling for partial disable states
  - **Disabled Reason Management** (new tests):
    - Preservation of kdump marker in "evacuation complete" message
    - Stale service object handling (pre-disable status)
  - **Re-enable Delay**:
    - 60-second wait after kdump messages stop
    - Logging for waiting and completion states
- **Threshold Protection**:
  - Mass failure prevention
  - Percentage-based limits

### Performance and Scaling
- Large-scale evacuations (100+ hosts)
- Caching mechanisms for flavors, images, and servers
- Cache refresh and invalidation
- Concurrent evacuation processing with ThreadPoolExecutor
- Memory management and cleanup
- Performance benchmarks (categorization < 1s for 100 services)
- Optimized test execution (time.sleep() mocking)

### Error Handling
- Nova API failures and timeouts
- Configuration errors and validation
- Partial evacuation failures and recovery with proper host marking
- Exception handling in smart evacuation (ensures failed hosts are marked appropriately)
- Network errors and permission issues
- Malformed data handling
- Missing resource handling
- Main loop error recovery and continuation

### Security and Reliability
- **Input Validation**:
  - SSRF prevention (URL, IP, port validation)
  - Injection attack prevention
  - Kubernetes resource name validation
  - Power action whitelisting
- **Password Security**:
  - Environment variable usage for IPMI passwords
  - Credential sanitization in logs
  - Safe exception logging
- **Thread Safety**:
  - Lock-protected cache operations
  - Concurrent evacuation handling
  - Processing state tracking
- **Resource Management**:
  - Cleanup and leak prevention
  - Context managers for automatic cleanup
  - Background thread lifecycle management

## Test Implementation Patterns

## Reserved Hosts Test Coverage

Reserved host management and forced evacuation test coverage:

### Unit Tests (8 tests in `test_instanceha.py`)
1. **test_reserved_hosts_aggregate_matching** - Verifies hosts in same evacuable aggregate are matched
2. **test_reserved_hosts_aggregate_no_match** - Tests different aggregates (no overlap)
3. **test_reserved_hosts_aggregate_not_evacuable** - Tests non-evacuable aggregate rejection
4. **test_reserved_hosts_aggregate_failed_host_no_aggregate** - Tests failed host with no aggregate membership
5. **test_reserved_hosts_aggregate_multiple_matches** - Verifies first match selection and list modification
6. **test_reserved_hosts_zone_matching** - Tests zone-based matching
7. **test_reserved_hosts_zone_no_match** - Tests different zones (no match)
8. **test_reserved_hosts_pool_exhaustion** - Tests empty reserved host pool handling

### Functional Tests (7 tests in `functional_test.py`)
1. **test_configuration_loading** - Verifies `FORCE_RESERVED_HOST_EVACUATION` config loads correctly (defaults to false)
2. **test_reserved_host_aggregate_matching** - Mocked aggregate matching flow verification
3. **test_reserved_host_aggregate_matching_end_to_end** - Full aggregate matching without mocks
4. **test_reserved_host_aggregate_no_match_end_to_end** - No match scenario without mocks
5. **test_reserved_host_zone_matching** - Zone-based matching with MatchType verification
6. **test_forced_evacuation_to_reserved_host** - Forced evacuation enabled, verifies target_host parameter
7. **test_forced_evacuation_disabled** - Forced evacuation disabled, verifies no target_host
8. **test_forced_evacuation_no_reserved_host_available** - Forced enabled but no host available
9. **test_forced_evacuation_end_to_end_integration** - Full `process_service()` flow with forced evacuation
10. **test_forced_evacuation_with_smart_evacuation** - Forced evacuation + smart evacuation mode compatibility

### Exception Class Mocking
Tests create real Exception subclasses instead of MagicMock objects to avoid "catching classes that do not inherit from BaseException" errors:

```python
class NotFound(Exception):
    pass
class Conflict(Exception):
    pass
class Forbidden(Exception):
    pass
class Unauthorized(Exception):
    pass

novaclient_exceptions = MagicMock()
novaclient_exceptions.NotFound = NotFound
novaclient_exceptions.Conflict = Conflict
novaclient_exceptions.Forbidden = Forbidden
novaclient_exceptions.Unauthorized = Unauthorized
sys.modules['novaclient.exceptions'] = novaclient_exceptions
```

### Glance API Mocking
Mocking `glance.list()` to return an empty list instead of Mock object to avoid "TypeError: 'Mock' object is not iterable":

```python
mock_nova.glance = Mock()
mock_nova.glance.list = Mock(return_value=[])  # Return list, not Mock
```

### Performance Optimization
Tests mock `time.sleep()` and timing constants for fast execution:

```python
with patch('instanceha.time.sleep'):  # Mock all sleep calls
    with patch('instanceha.INITIAL_EVACUATION_WAIT_SECONDS', 0):
        with patch('instanceha.EVACUATION_POLL_INTERVAL_SECONDS', 0):
            result = instanceha._server_evacuate_future(conn, server)
```

### Stateful Nova Client Mock
Tests use stateful mock that simulates Nova API behavior:

```python
def _create_mock_nova_stateful():
    state = {
        'services': [],
        'servers': defaultdict(list),
        'migrations': []
    }

    def evacuate_server(server=None, *args, **kwargs):
        migration = Mock(status='completed', instance_uuid=server)
        state['migrations'].append(migration)
        return (Mock(status_code=200, reason='OK'), {})

    mock_nova.servers.evacuate.side_effect = evacuate_server
    return mock_nova, state
```

## Coverage Notes

- **Unit test coverage**: 71% (test_instanceha.py only)
- **Core test coverage**: 77% (unit + security + critical errors + workflows + config + helpers)
- **Combined coverage**: 83% (all 394 tests including functional + integration)

### How to Run Coverage Analysis

Run all tests with coverage:
```bash
cd tests/instanceha
PYTHONPATH="../../templates/instanceha/bin:$PYTHONPATH" \
  python3 -m coverage run --source=../../templates/instanceha/bin -a test_instanceha.py && \
  python3 -m coverage run --source=../../templates/instanceha/bin -a test_security_validation.py && \
  python3 -m coverage run --source=../../templates/instanceha/bin -a test_critical_error_paths.py && \
  python3 -m coverage run --source=../../templates/instanceha/bin -a test_evacuation_workflow.py && \
  python3 -m coverage run --source=../../templates/instanceha/bin -a test_config_features.py && \
  python3 -m coverage run --source=../../templates/instanceha/bin -a test_helper_functions.py && \
  python3 -m coverage run --source=../../templates/instanceha/bin -a functional_test.py && \
  python3 -m coverage run --source=../../templates/instanceha/bin -a integration_test.py && \
  python3 -m coverage run --source=../../templates/instanceha/bin -a test_region_isolation.py && \
  python3 -m coverage report
```

Generate detailed HTML coverage report:
```bash
python3 -m coverage html
# Open htmlcov/index.html in browser
```

Uncovered code (17%):
- Main event loop and initialization
- Exception handlers
- Debug logging branches
- Alternative error message paths

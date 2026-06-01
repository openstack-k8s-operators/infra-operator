# InstanceHA Test Suite

## Test Structure

| File | Scope |
|------|-------|
| `test_unit_core.py` | Core components: config, caching, evacuation logic, tag checking |
| `test_security_validation.py` | SSRF prevention, injection prevention, input validation |
| `test_critical_error_paths.py` | Config errors, Nova API exceptions, fencing failures, timeouts |
| `test_evacuation_workflow.py` | Kdump resume logic, post-evacuation recovery, step failures |
| `test_config_features.py` | DISABLED, FORCE_ENABLE, LEAVE_DISABLED, TAGGED_AGGREGATES, DELAY, HASH_INTERVAL |
| `test_helper_functions.py` | `_cleanup_filtered_hosts`, `_filter_processing_hosts`, `_prepare_evacuation_resources`, `_count_evacuable_hosts` |
| `test_fencing_agents.py` | IPMI, Redfish, BMH/Metal3, noop agents |
| `test_kdump_detection.py` | UDP listener, magic number validation, timeout behavior |
| `test_heartbeat_detection.py` | HBV2 protocol, HMAC-SHA256, timestamp freshness, grace period, reachable host filtering, cliff detection |
| `test_heartbeat_scale.py` | 1000-node scale HMAC processing, concurrent writes, cleanup thresholds |
| `test_orchestrated_evacuation.py` | Metadata extraction, group building, phase execution, routing logic |
| `test_aggregate_threshold.py` | Per-aggregate `instanceha:max_failures` enforcement |
| `test_k8s_events.py` | K8s event emission, credential handling, lifecycle sequences |
| `test_k8s_partition.py` | K8s API reachability, consecutive failure threshold, fencing block |
| `test_thread_safety.py` | Concurrent cache access, processing lock contention |
| `test_ipv6_udp.py` | IPv6/dual-stack UDP binding, heartbeat and kdump over IPv6 |
| `test_coverage_gaps.py` | Supplemental edge cases and error paths |
| `test_region_isolation.py` | Nova client region scoping, multi-region independence |
| `functional_test.py` | End-to-end evacuation workflows, tagging combinations, reserved hosts |
| `integration_test.py` | Cross-component interactions, re-enabling workflows, error recovery |

## Running Tests

Run all tests:
```bash
./run_tests.sh
```

Run a single test file:
```bash
PYTHONPATH="../../templates/instanceha/bin:$PYTHONPATH" python3 <test_file>.py
```

## Coverage

Quick coverage (unit tests only):
```bash
PYTHONPATH="../../templates/instanceha/bin:$PYTHONPATH" \
  python3 -m coverage run --source=../../templates/instanceha/bin test_unit_core.py
python3 -m coverage report
```

Full coverage (all test files):
```bash
PYTHONPATH="../../templates/instanceha/bin:$PYTHONPATH" \
  python3 -m coverage run --source=../../templates/instanceha/bin -a test_unit_core.py && \
  python3 -m coverage run --source=../../templates/instanceha/bin -a test_security_validation.py && \
  python3 -m coverage run --source=../../templates/instanceha/bin -a test_critical_error_paths.py && \
  python3 -m coverage run --source=../../templates/instanceha/bin -a test_evacuation_workflow.py && \
  python3 -m coverage run --source=../../templates/instanceha/bin -a test_config_features.py && \
  python3 -m coverage run --source=../../templates/instanceha/bin -a test_helper_functions.py && \
  python3 -m coverage run --source=../../templates/instanceha/bin -a test_fencing_agents.py && \
  python3 -m coverage run --source=../../templates/instanceha/bin -a test_kdump_detection.py && \
  python3 -m coverage run --source=../../templates/instanceha/bin -a test_heartbeat_detection.py && \
  python3 -m coverage run --source=../../templates/instanceha/bin -a test_heartbeat_scale.py && \
  python3 -m coverage run --source=../../templates/instanceha/bin -a test_orchestrated_evacuation.py && \
  python3 -m coverage run --source=../../templates/instanceha/bin -a test_aggregate_threshold.py && \
  python3 -m coverage run --source=../../templates/instanceha/bin -a test_k8s_events.py && \
  python3 -m coverage run --source=../../templates/instanceha/bin -a test_k8s_partition.py && \
  python3 -m coverage run --source=../../templates/instanceha/bin -a test_thread_safety.py && \
  python3 -m coverage run --source=../../templates/instanceha/bin -a test_ipv6_udp.py && \
  python3 -m coverage run --source=../../templates/instanceha/bin -a test_coverage_gaps.py && \
  python3 -m coverage run --source=../../templates/instanceha/bin -a test_region_isolation.py && \
  python3 -m coverage run --source=../../templates/instanceha/bin -a functional_test.py && \
  python3 -m coverage run --source=../../templates/instanceha/bin -a integration_test.py && \
  python3 -m coverage report
```

Generate HTML coverage report:
```bash
python3 -m coverage html
# Open htmlcov/index.html in browser
```

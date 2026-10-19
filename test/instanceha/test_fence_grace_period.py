"""
Tests for the fencing grace period (FENCE_AFTER_DOWN_CYCLES).

A host may optionally be allowed to stay down for several consecutive poll
cycles before it is fenced, giving it time to recover on its own (e.g.
nova-compute reconnecting to RabbitMQ) before its instances are evacuated.

Counter management (record_down_cycles) runs in the main poll loop on the
complete detected-down set; the eligibility check (within_fence_grace_period)
is consulted inside _admit_stale_services after the safety gates, so those
gates still evaluate the full detected-down set.
"""

import logging
import unittest
import threading
from collections import defaultdict
from concurrent.futures import Future
from unittest.mock import Mock, patch

logging.getLogger().setLevel(logging.CRITICAL)

from conftest import make_mock_config  # noqa: E402
import instanceha  # noqa: E402


def _make_compute_svc(hostname):
    svc = Mock()
    svc.host = f'{hostname}.example.com'
    svc.status = 'enabled'
    svc.forced_down = False
    svc.state = 'up'
    return svc


def _make_admission_service(required, threshold, host_cycles, tagged_aggregates=False):
    config = make_mock_config(
        FENCE_AFTER_DOWN_CYCLES=required,
        THRESHOLD=threshold,
        CHECK_HEARTBEAT=False,
        TAGGED_IMAGES=False,
        TAGGED_FLAVORS=False,
        TAGGED_AGGREGATES=tagged_aggregates,
        MAX_HOSTS_PER_CYCLE=100,
    )
    service = Mock()
    service.config = config
    service.host_down_cycles = defaultdict(int, host_cycles)
    service.within_fence_grace_period = lambda host: (
        required > 1
        and service.host_down_cycles.get(instanceha._extract_hostname(host), 0) < required
    )
    service.processing_lock = threading.Lock()
    service.hosts_processing = defaultdict(float)
    service.refresh_evacuable_cache = Mock()
    service.get_hosts_with_servers_cached = Mock(return_value={})
    service.filter_hosts_with_servers = Mock(side_effect=lambda nodes, _cache: list(nodes))
    future = Future()
    future.set_result(True)
    service.processing_executor = Mock()
    service.processing_executor.submit.return_value = future
    return service


class TestFenceGracePeriod(unittest.TestCase):
    """Test record_down_cycles / within_fence_grace_period."""

    def _service(self, cycles):
        return instanceha.InstanceHAService(
            make_mock_config(FENCE_AFTER_DOWN_CYCLES=cycles))

    def test_default_never_defers(self):
        """With the default of 1, no counters are tracked and nothing is deferred."""
        service = self._service(1)
        svc = _make_compute_svc('compute-01')

        service.record_down_cycles([svc])
        self.assertEqual(dict(service.host_down_cycles), {})
        self.assertFalse(service.within_fence_grace_period(svc.host))

    def test_defers_until_threshold_reached(self):
        """A host stays within the grace period until it has been down N cycles."""
        service = self._service(3)
        svc = _make_compute_svc('compute-01')

        # Cycle 1 and 2: still within grace.
        service.record_down_cycles([svc])
        self.assertTrue(service.within_fence_grace_period(svc.host))
        service.record_down_cycles([svc])
        self.assertTrue(service.within_fence_grace_period(svc.host))
        self.assertEqual(service.host_down_cycles['compute-01'], 2)

        # Cycle 3: threshold reached, host is now eligible to fence.
        service.record_down_cycles([svc])
        self.assertFalse(service.within_fence_grace_period(svc.host))
        self.assertEqual(service.host_down_cycles['compute-01'], 3)

    def test_recovery_resets_counter(self):
        """A host that recovers before the threshold has its counter cleared."""
        service = self._service(3)
        svc = _make_compute_svc('compute-01')

        service.record_down_cycles([svc])
        self.assertEqual(service.host_down_cycles['compute-01'], 1)

        # Host no longer reported down -- counter is reset (removed from the map).
        service.record_down_cycles([])
        self.assertNotIn('compute-01', service.host_down_cycles)

        # A later failure starts counting from scratch again.
        service.record_down_cycles([svc])
        self.assertEqual(service.host_down_cycles['compute-01'], 1)
        self.assertTrue(service.within_fence_grace_period(svc.host))

    def test_independent_per_host_counters(self):
        """Each host accumulates its own down-cycle count."""
        service = self._service(2)
        a = _make_compute_svc('compute-a')
        b = _make_compute_svc('compute-b')

        # Only 'a' is down the first cycle.
        service.record_down_cycles([a])

        # Both down the second cycle: 'a' reaches the threshold, 'b' does not.
        service.record_down_cycles([a, b])
        self.assertFalse(service.within_fence_grace_period(a.host))
        self.assertTrue(service.within_fence_grace_period(b.host))
        self.assertEqual(service.host_down_cycles['compute-b'], 1)

    def test_counter_keeps_climbing_past_threshold(self):
        """Once eligible, a still-down host stays eligible on subsequent cycles."""
        service = self._service(2)
        svc = _make_compute_svc('compute-01')

        service.record_down_cycles([svc])
        service.record_down_cycles([svc])
        self.assertFalse(service.within_fence_grace_period(svc.host))
        # Still down next cycle (e.g. fencing in progress / failed): remains eligible.
        service.record_down_cycles([svc])
        self.assertFalse(service.within_fence_grace_period(svc.host))
        self.assertEqual(service.host_down_cycles['compute-01'], 3)


class TestFenceGraceAdmission(unittest.TestCase):
    """Test grace filtering inside _admit_stale_services and gate ordering."""

    @patch('instanceha._emit_k8s_event')
    @patch('instanceha._check_critical_services', return_value=(True, ''))
    def test_only_mature_hosts_are_submitted_and_grace_event_is_aggregated(
            self, _critical_services, emit_event):
        hosts = [_make_compute_svc(f'compute-{i:02d}') for i in range(4)]
        healthy = [_make_compute_svc(f'healthy-{i:02d}') for i in range(4)]
        host_cycles = {
            'compute-00': 3,
            'compute-01': 3,
            'compute-02': 1,
            'compute-03': 1,
        }
        service = _make_admission_service(3, 100, host_cycles)
        deferred_before = {
            host.host: instanceha.FENCING_DEFERRED_TOTAL.labels(host=host.host)._value.get()
            for host in hosts[2:]
        }

        instanceha._admit_stale_services(Mock(), service, hosts + healthy, hosts, [])

        submitted_hosts = [call.args[1].host
                           for call in service.processing_executor.submit.call_args_list]
        self.assertEqual(submitted_hosts, [hosts[0].host, hosts[1].host])
        grace_events = [call for call in emit_event.call_args_list
                        if len(call.args) >= 2 and call.args[1] == 'HostDownGrace']
        self.assertEqual(len(grace_events), 1)
        self.assertEqual(grace_events[0].args[0], 'cluster')
        host_down_events = [call.args[0] for call in emit_event.call_args_list
                            if len(call.args) >= 2 and call.args[1] == 'HostDown']
        self.assertEqual(host_down_events, [hosts[0].host, hosts[1].host])
        for host in hosts[2:]:
            after = instanceha.FENCING_DEFERRED_TOTAL.labels(host=host.host)._value.get()
            self.assertEqual(after - deferred_before[host.host], 1)

    @patch('instanceha._emit_k8s_event')
    def test_threshold_gate_uses_full_down_set_before_grace_filter(self, emit_event):
        hosts = [_make_compute_svc(f'compute-{i:02d}') for i in range(4)]
        healthy = [_make_compute_svc(f'healthy-{i:02d}') for i in range(6)]
        host_cycles = {
            'compute-00': 3,
            'compute-01': 3,
            'compute-02': 1,
            'compute-03': 1,
        }
        service = _make_admission_service(3, 30, host_cycles)

        instanceha._admit_stale_services(Mock(), service, hosts + healthy, hosts, [])

        reasons = [call.args[1] for call in emit_event.call_args_list
                   if len(call.args) >= 2]
        self.assertIn('ThresholdExceeded', reasons)
        self.assertNotIn('HostDownGrace', reasons)
        service.processing_executor.submit.assert_not_called()

    @patch('instanceha._emit_k8s_event')
    def test_all_services_stale_gate_uses_full_down_set_before_grace_filter(self, emit_event):
        hosts = [_make_compute_svc(f'compute-{i:02d}') for i in range(4)]
        host_cycles = {
            'compute-00': 3,
            'compute-01': 3,
            'compute-02': 1,
            'compute-03': 1,
        }
        service = _make_admission_service(3, 100, host_cycles)

        instanceha._admit_stale_services(Mock(), service, hosts, hosts, [])

        reasons = [call.args[1] for call in emit_event.call_args_list
                   if len(call.args) >= 2]
        self.assertIn('AllServicesStale', reasons)
        self.assertNotIn('HostDownGrace', reasons)
        service.processing_executor.submit.assert_not_called()

    @patch('instanceha._emit_k8s_event')
    @patch('instanceha._check_critical_services', return_value=(True, ''))
    def test_aggregate_threshold_receives_full_down_set_before_grace_filter(
            self, _critical_services, _emit_event):
        hosts = [_make_compute_svc(f'compute-{i:02d}') for i in range(4)]
        healthy = [_make_compute_svc(f'healthy-{i:02d}') for i in range(4)]
        host_cycles = {
            'compute-00': 3,
            'compute-01': 3,
            'compute-02': 1,
            'compute-03': 1,
        }
        service = _make_admission_service(3, 100, host_cycles, tagged_aggregates=True)
        conn = Mock()
        conn.aggregates.list.return_value = []
        captured_hosts = []

        def allow_aggregate_candidates(nodes, _aggregates, _service):
            captured_hosts.extend(svc.host for svc in nodes)
            return list(nodes), []

        all_hosts = {svc.host for svc in hosts + healthy}
        with patch('instanceha._get_evacuable_hosts_from_aggregates', return_value=all_hosts), \
                patch('instanceha._filter_by_aggregate_threshold', side_effect=allow_aggregate_candidates):
            instanceha._admit_stale_services(conn, service, hosts + healthy, hosts, [])

        self.assertEqual(captured_hosts, [svc.host for svc in hosts])
        submitted_hosts = [call.args[1].host
                           for call in service.processing_executor.submit.call_args_list]
        self.assertEqual(submitted_hosts, [hosts[0].host, hosts[1].host])

    @patch('instanceha._emit_k8s_event')
    def test_aggregated_grace_event_limits_host_details(self, emit_event):
        hosts = [_make_compute_svc(f'compute-{i:02d}') for i in range(12)]
        healthy = [_make_compute_svc(f'healthy-{i:02d}') for i in range(12)]
        host_cycles = {f'compute-{i:02d}': 1 for i in range(12)}
        service = _make_admission_service(3, 100, host_cycles)

        instanceha._admit_stale_services(Mock(), service, hosts + healthy, hosts, [])

        grace_events = [call for call in emit_event.call_args_list
                        if len(call.args) >= 2 and call.args[1] == 'HostDownGrace']
        self.assertEqual(len(grace_events), 1)
        self.assertEqual(grace_events[0].args[0], 'cluster')
        message = grace_events[0].args[2]
        self.assertIn('12 host(s)', message)
        self.assertIn('and 2 more', message)
        self.assertNotIn('compute-11.example.com', message)
        self.assertFalse(any(call.args[1] == 'HostDown' for call in emit_event.call_args_list
                             if len(call.args) >= 2))
        service.processing_executor.submit.assert_not_called()

    @patch('instanceha._emit_k8s_event')
    def test_all_deferred_skips_expensive_resource_discovery(self, _emit_event):
        """When every down host is still within grace, skip Nova resource discovery."""
        hosts = [_make_compute_svc(f'compute-{i:02d}') for i in range(3)]
        healthy = [_make_compute_svc(f'healthy-{i:02d}') for i in range(3)]
        host_cycles = {f'compute-{i:02d}': 1 for i in range(3)}
        service = _make_admission_service(3, 100, host_cycles)

        instanceha._admit_stale_services(Mock(), service, hosts + healthy, hosts, [])

        # The forced evacuable-cache refresh and per-host server queries must be
        # skipped entirely for the all-deferred case (the whole point of the
        # grace-period short-circuit).
        service.refresh_evacuable_cache.assert_not_called()
        service.get_hosts_with_servers_cached.assert_not_called()
        service.processing_executor.submit.assert_not_called()

    @patch('instanceha._emit_k8s_event')
    @patch('instanceha._check_critical_services', return_value=(True, ''))
    def test_mixed_still_runs_resource_discovery(self, _critical_services, _emit_event):
        """When at least one host is eligible, discovery still runs (gates see full set)."""
        hosts = [_make_compute_svc(f'compute-{i:02d}') for i in range(2)]
        host_cycles = {'compute-00': 3, 'compute-01': 1}
        service = _make_admission_service(3, 100, host_cycles)

        instanceha._admit_stale_services(Mock(), service, hosts, hosts, [])

        service.get_hosts_with_servers_cached.assert_called()
        submitted_hosts = [call.args[1].host
                           for call in service.processing_executor.submit.call_args_list]
        self.assertEqual(submitted_hosts, [hosts[0].host])


if __name__ == '__main__':
    unittest.main()

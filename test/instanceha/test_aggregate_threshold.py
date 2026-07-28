"""
Unit tests for per-aggregate failure threshold filtering.

Tests for _filter_by_aggregate_threshold which blocks evacuation of hosts
in aggregates that have exceeded their instanceha:max_failures metadata limit.
"""

import unittest
from unittest.mock import Mock, patch, MagicMock

import conftest  # noqa: F401
import instanceha


def _make_aggregate(name, hosts, metadata=None):
    agg = Mock()
    agg.name = name
    agg.hosts = hosts
    agg.metadata = metadata or {}
    return agg


def _make_service_obj(host):
    svc = Mock()
    svc.host = host
    return svc


def _make_instanceha_service(evacuable_tag='evacuable'):
    service = Mock()
    service.evacuable_tag = evacuable_tag
    service.hosts_processing = {}
    service._is_resource_evacuable = instanceha.InstanceHAService._is_resource_evacuable.__get__(service)
    service._check_evacuable_tag = instanceha.InstanceHAService._check_evacuable_tag.__get__(service)
    return service


class TestFilterByAggregateThreshold(unittest.TestCase):

    @patch('instanceha._emit_k8s_event')
    def test_no_metadata_key_all_pass(self, mock_event):
        """Aggregates without instanceha:max_failures pass all hosts through."""
        nodes = [_make_service_obj('host-1'), _make_service_obj('host-2')]
        aggs = [_make_aggregate('agg1', ['host-1', 'host-2'],
                                {'evacuable': 'true'})]
        service = _make_instanceha_service()

        allowed, blocked = instanceha._filter_by_aggregate_threshold(nodes, aggs, service)

        self.assertEqual(len(allowed), 2)
        self.assertEqual(len(blocked), 0)
        mock_event.assert_not_called()

    @patch('instanceha._emit_k8s_event')
    def test_failures_below_limit(self, mock_event):
        """Hosts pass through when failures are within the limit."""
        nodes = [_make_service_obj('host-1')]
        aggs = [_make_aggregate('agg1', ['host-1', 'host-2', 'host-3'],
                                {'evacuable': 'true',
                                 'instanceha:max_failures': '2'})]
        service = _make_instanceha_service()

        allowed, blocked = instanceha._filter_by_aggregate_threshold(nodes, aggs, service)

        self.assertEqual(len(allowed), 1)
        self.assertEqual(len(blocked), 0)

    @patch('instanceha._emit_k8s_event')
    def test_failures_exceed_limit(self, mock_event):
        """Hosts in aggregate are blocked when failures exceed the limit."""
        nodes = [_make_service_obj('host-1'), _make_service_obj('host-2'),
                 _make_service_obj('host-3')]
        aggs = [_make_aggregate('agg1', ['host-1', 'host-2', 'host-3'],
                                {'evacuable': 'true',
                                 'instanceha:max_failures': '2'})]
        service = _make_instanceha_service()

        allowed, blocked = instanceha._filter_by_aggregate_threshold(nodes, aggs, service)

        self.assertEqual(len(allowed), 0)
        self.assertEqual(len(blocked), 3)
        mock_event.assert_called_once()
        self.assertIn('AggregateThresholdExceeded', mock_event.call_args[0])

    @patch('instanceha._emit_k8s_event')
    def test_multi_aggregate_most_restrictive(self, mock_event):
        """Host in two aggregates is blocked if ANY aggregate exceeds its limit."""
        nodes = [_make_service_obj('host-1'), _make_service_obj('host-2')]
        aggs = [
            _make_aggregate('agg-ok', ['host-1', 'host-2', 'host-3'],
                            {'evacuable': 'true',
                             'instanceha:max_failures': '5'}),
            _make_aggregate('agg-exceeded', ['host-1'],
                            {'evacuable': 'true',
                             'instanceha:max_failures': '0'}),
        ]
        service = _make_instanceha_service()

        allowed, blocked = instanceha._filter_by_aggregate_threshold(nodes, aggs, service)

        self.assertEqual([s.host for s in blocked], ['host-1'])
        self.assertEqual([s.host for s in allowed], ['host-2'])

    @patch('instanceha._emit_k8s_event')
    def test_multi_aggregate_neither_exceeded(self, mock_event):
        """Host in two aggregates passes when neither exceeds its limit."""
        nodes = [_make_service_obj('host-1')]
        aggs = [
            _make_aggregate('agg1', ['host-1', 'host-2'],
                            {'evacuable': 'true',
                             'instanceha:max_failures': '3'}),
            _make_aggregate('agg2', ['host-1', 'host-3'],
                            {'evacuable': 'true',
                             'instanceha:max_failures': '3'}),
        ]
        service = _make_instanceha_service()

        allowed, blocked = instanceha._filter_by_aggregate_threshold(nodes, aggs, service)

        self.assertEqual(len(allowed), 1)
        self.assertEqual(len(blocked), 0)

    @patch('instanceha._emit_k8s_event')
    def test_invalid_metadata_value(self, mock_event):
        """Invalid (non-integer) metadata value logs warning and passes hosts through."""
        nodes = [_make_service_obj('host-1')]
        aggs = [_make_aggregate('agg1', ['host-1'],
                                {'evacuable': 'true',
                                 'instanceha:max_failures': 'abc'})]
        service = _make_instanceha_service()

        with self.assertLogs(level='WARNING') as cm:
            allowed, blocked = instanceha._filter_by_aggregate_threshold(nodes, aggs, service)

        self.assertEqual(len(allowed), 1)
        self.assertEqual(len(blocked), 0)
        self.assertTrue(any('invalid' in msg for msg in cm.output))

    @patch('instanceha._emit_k8s_event')
    def test_negative_metadata_value(self, mock_event):
        """Negative metadata value logs warning and is ignored."""
        nodes = [_make_service_obj('host-1')]
        aggs = [_make_aggregate('agg1', ['host-1'],
                                {'evacuable': 'true',
                                 'instanceha:max_failures': '-1'})]
        service = _make_instanceha_service()

        with self.assertLogs(level='WARNING') as cm:
            allowed, blocked = instanceha._filter_by_aggregate_threshold(nodes, aggs, service)

        self.assertEqual(len(allowed), 1)
        self.assertEqual(len(blocked), 0)
        self.assertTrue(any('negative' in msg for msg in cm.output))

    @patch('instanceha._emit_k8s_event')
    def test_max_failures_zero(self, mock_event):
        """max_failures=0 blocks any failure in the aggregate."""
        nodes = [_make_service_obj('host-1')]
        aggs = [_make_aggregate('agg1', ['host-1', 'host-2'],
                                {'evacuable': 'true',
                                 'instanceha:max_failures': '0'})]
        service = _make_instanceha_service()

        allowed, blocked = instanceha._filter_by_aggregate_threshold(nodes, aggs, service)

        self.assertEqual(len(allowed), 0)
        self.assertEqual(len(blocked), 1)

    @patch('instanceha._emit_k8s_event')
    def test_mixed_hosts_partial_block(self, mock_event):
        """Only hosts in exceeded aggregates are blocked; others pass through."""
        nodes = [_make_service_obj('host-1'), _make_service_obj('host-2'),
                 _make_service_obj('host-3')]
        aggs = [
            _make_aggregate('agg-limited', ['host-1', 'host-2'],
                            {'evacuable': 'true',
                             'instanceha:max_failures': '1'}),
            _make_aggregate('agg-unlimited', ['host-3'],
                            {'evacuable': 'true'}),
        ]
        service = _make_instanceha_service()

        allowed, blocked = instanceha._filter_by_aggregate_threshold(nodes, aggs, service)

        self.assertEqual(sorted([s.host for s in allowed]), ['host-3'])
        self.assertEqual(sorted([s.host for s in blocked]), ['host-1', 'host-2'])

    @patch('instanceha._emit_k8s_event')
    def test_non_evacuable_aggregate_skipped(self, mock_event):
        """Aggregates not tagged as evacuable are ignored entirely."""
        nodes = [_make_service_obj('host-1'), _make_service_obj('host-2')]
        aggs = [_make_aggregate('agg-not-evacuable', ['host-1', 'host-2'],
                                {'instanceha:max_failures': '0'})]
        service = _make_instanceha_service()

        allowed, blocked = instanceha._filter_by_aggregate_threshold(nodes, aggs, service)

        self.assertEqual(len(allowed), 2)
        self.assertEqual(len(blocked), 0)


    @patch('instanceha._emit_k8s_event')
    def test_in_flight_hosts_not_double_counted(self, mock_event):
        """Hosts already in hosts_processing must not be double-counted as both
        'new' and 'in-flight', which would inflate the impacted count and
        incorrectly block aggregates within the threshold."""
        nodes = [_make_service_obj('host-10'), _make_service_obj('host-11')]
        aggs = [_make_aggregate('mix-B', [f'host-{i}' for i in range(10, 20)],
                                {'evacuable': 'true',
                                 'instanceha:max_failures': '3'})]
        service = _make_instanceha_service()
        # Simulate _filter_processing_hosts having marked these hosts before
        # the aggregate threshold check runs
        service.hosts_processing = {'host-10': 1000.0, 'host-11': 1000.0}

        allowed, blocked = instanceha._filter_by_aggregate_threshold(nodes, aggs, service)

        # 2 failed hosts with max_failures=3 should be allowed
        self.assertEqual(len(allowed), 2,
                         "2 failures in a max_failures=3 aggregate should not be blocked")
        self.assertEqual(len(blocked), 0)
        mock_event.assert_not_called()

    @patch('instanceha._emit_k8s_event')
    def test_in_flight_plus_new_exceeds_threshold(self, mock_event):
        """Genuine in-flight hosts (from a prior cycle) plus new failures should
        be counted together to enforce the threshold correctly."""
        nodes = [_make_service_obj('host-3'), _make_service_obj('host-4')]
        aggs = [_make_aggregate('agg1', [f'host-{i}' for i in range(10)],
                                {'evacuable': 'true',
                                 'instanceha:max_failures': '3'})]
        service = _make_instanceha_service()
        # 2 hosts already in-flight from a prior cycle (different from current failures)
        service.hosts_processing = {'host-1': 900.0, 'host-2': 900.0}

        allowed, blocked = instanceha._filter_by_aggregate_threshold(nodes, aggs, service)

        # 2 in-flight + 2 new = 4 > max_failures=3 → should be blocked
        self.assertEqual(len(allowed), 0)
        self.assertEqual(len(blocked), 2)
        mock_event.assert_called_once()

    @patch('instanceha._emit_k8s_event')
    def test_mixed_aggregates_with_in_flight_double_counting(self, mock_event):
        """Reproduces test 27 failure: two aggregates where in-flight
        double-counting blocks an aggregate that should be within its limit.

        mix-A: 4 failures, max_failures=3 → should be blocked
        mix-B: 2 failures, max_failures=3 → should be allowed
        """
        mix_a_failed = [_make_service_obj(f'fake-compute-vm0-{i}') for i in range(4)]
        mix_b_failed = [_make_service_obj(f'fake-compute-vm1-{i}') for i in range(2)]
        nodes = mix_a_failed + mix_b_failed
        aggs = [
            _make_aggregate('mix-A',
                            [f'fake-compute-vm0-{i}' for i in range(10)],
                            {'evacuable': 'true',
                             'instanceha:max_failures': '3'}),
            _make_aggregate('mix-B',
                            [f'fake-compute-vm1-{i}' for i in range(10)],
                            {'evacuable': 'true',
                             'instanceha:max_failures': '3'}),
        ]
        service = _make_instanceha_service()
        # Simulate _filter_processing_hosts marking all 6 hosts before
        # the aggregate threshold check
        service.hosts_processing = {
            f'fake-compute-vm0-{i}': 1000.0 for i in range(4)
        }
        service.hosts_processing.update({
            f'fake-compute-vm1-{i}': 1000.0 for i in range(2)
        })

        allowed, blocked = instanceha._filter_by_aggregate_threshold(nodes, aggs, service)

        allowed_hosts = sorted([s.host for s in allowed])
        blocked_hosts = sorted([s.host for s in blocked])

        # mix-A has 4 failures > max_failures=3 → blocked
        self.assertIn('fake-compute-vm0-0', blocked_hosts)
        # mix-B has 2 failures <= max_failures=3 → allowed
        self.assertIn('fake-compute-vm1-0', allowed_hosts)
        self.assertIn('fake-compute-vm1-1', allowed_hosts)
        self.assertEqual(len(allowed), 2)
        self.assertEqual(len(blocked), 4)


if __name__ == '__main__':
    unittest.main()

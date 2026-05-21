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


if __name__ == '__main__':
    unittest.main()

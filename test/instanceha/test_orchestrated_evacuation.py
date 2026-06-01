"""
Tests for orchestrated evacuation (priority-based restart ordering).

Covers:
- _get_evacuation_metadata: extracting priority and group from server metadata
- _build_evacuation_groups: grouping and sorting servers by priority
- _orchestrated_evacuate: phased evacuation with ThreadPoolExecutor
- _host_evacuate: branching logic for orchestrated/smart/traditional
- ORCHESTRATED_RESTART config option (explicit enable only)
"""

import unittest
import logging
from unittest.mock import Mock, patch, MagicMock, call

import conftest  # noqa: F401

logging.getLogger().setLevel(logging.CRITICAL)
import instanceha
logging.getLogger().setLevel(logging.CRITICAL)


def _make_server(server_id, status='ACTIVE', metadata=None):
    """Create a mock server with optional metadata."""
    server = Mock()
    server.id = server_id
    server.status = status
    server.metadata = metadata if metadata is not None else {}
    return server


# ============================================================================
# _get_evacuation_metadata tests
# ============================================================================

class TestGetEvacuationMetadata(unittest.TestCase):

    def test_default_values_no_metadata_attr(self):
        server = Mock(spec=[])
        priority, group = instanceha._get_evacuation_metadata(server)
        self.assertEqual(priority, 500)
        self.assertIsNone(group)

    def test_default_values_empty_metadata(self):
        server = _make_server('srv-1', metadata={})
        priority, group = instanceha._get_evacuation_metadata(server)
        self.assertEqual(priority, 500)
        self.assertIsNone(group)

    def test_priority_only(self):
        server = _make_server('srv-1', metadata={'instanceha:restart_priority': '100'})
        priority, group = instanceha._get_evacuation_metadata(server)
        self.assertEqual(priority, 100)
        self.assertIsNone(group)

    def test_group_only(self):
        server = _make_server('srv-1', metadata={'instanceha:restart_group': 'database'})
        priority, group = instanceha._get_evacuation_metadata(server)
        self.assertEqual(priority, 500)
        self.assertEqual(group, 'database')

    def test_both_values(self):
        server = _make_server('srv-1', metadata={
            'instanceha:restart_priority': '900',
            'instanceha:restart_group': 'database'
        })
        priority, group = instanceha._get_evacuation_metadata(server)
        self.assertEqual(priority, 900)
        self.assertEqual(group, 'database')

    def test_invalid_priority_uses_default(self):
        server = _make_server('srv-1', metadata={'instanceha:restart_priority': 'high'})
        priority, group = instanceha._get_evacuation_metadata(server)
        self.assertEqual(priority, 500)

    def test_priority_clamped_low(self):
        server = _make_server('srv-1', metadata={'instanceha:restart_priority': '0'})
        priority, _ = instanceha._get_evacuation_metadata(server)
        self.assertEqual(priority, 1)

    def test_priority_clamped_high(self):
        server = _make_server('srv-1', metadata={'instanceha:restart_priority': '9999'})
        priority, _ = instanceha._get_evacuation_metadata(server)
        self.assertEqual(priority, 1000)

    def test_metadata_none(self):
        server = _make_server('srv-1', metadata=None)
        server.metadata = None
        priority, group = instanceha._get_evacuation_metadata(server)
        self.assertEqual(priority, 500)
        self.assertIsNone(group)

    def test_empty_group_treated_as_none(self):
        server = _make_server('srv-1', metadata={'instanceha:restart_group': ''})
        _, group = instanceha._get_evacuation_metadata(server)
        self.assertIsNone(group)


# ============================================================================
# _build_evacuation_groups tests
# ============================================================================

class TestBuildEvacuationGroups(unittest.TestCase):

    def test_no_metadata_single_phase_per_server(self):
        servers = [_make_server('s1'), _make_server('s2'), _make_server('s3')]
        groups = instanceha._build_evacuation_groups(servers)
        self.assertEqual(len(groups), 3)
        for g in groups:
            self.assertEqual(len(g), 1)

    def test_single_group(self):
        servers = [
            _make_server('db-1', metadata={'instanceha:restart_group': 'database', 'instanceha:restart_priority': '900'}),
            _make_server('db-2', metadata={'instanceha:restart_group': 'database', 'instanceha:restart_priority': '800'}),
        ]
        groups = instanceha._build_evacuation_groups(servers)
        self.assertEqual(len(groups), 1)
        self.assertEqual(len(groups[0]), 2)
        server_ids = {s.id for s in groups[0]}
        self.assertEqual(server_ids, {'db-1', 'db-2'})

    def test_multiple_groups_priority_order(self):
        servers = [
            _make_server('web-1', metadata={'instanceha:restart_group': 'web', 'instanceha:restart_priority': '100'}),
            _make_server('db-1', metadata={'instanceha:restart_group': 'db', 'instanceha:restart_priority': '900'}),
            _make_server('app-1', metadata={'instanceha:restart_group': 'app', 'instanceha:restart_priority': '500'}),
        ]
        groups = instanceha._build_evacuation_groups(servers)
        self.assertEqual(len(groups), 3)
        self.assertEqual(groups[0][0].id, 'db-1')
        self.assertEqual(groups[1][0].id, 'app-1')
        self.assertEqual(groups[2][0].id, 'web-1')

    def test_group_priority_is_max_member(self):
        servers = [
            _make_server('db-1', metadata={'instanceha:restart_group': 'database', 'instanceha:restart_priority': '800'}),
            _make_server('db-2', metadata={'instanceha:restart_group': 'database', 'instanceha:restart_priority': '900'}),
            _make_server('web-1', metadata={'instanceha:restart_group': 'web', 'instanceha:restart_priority': '850'}),
        ]
        groups = instanceha._build_evacuation_groups(servers)
        # database group priority=900 > web group priority=850
        db_ids = {s.id for s in groups[0]}
        self.assertEqual(db_ids, {'db-1', 'db-2'})
        self.assertEqual(groups[1][0].id, 'web-1')

    def test_ungrouped_servers_sorted_by_priority(self):
        servers = [
            _make_server('low', metadata={'instanceha:restart_priority': '100'}),
            _make_server('high', metadata={'instanceha:restart_priority': '900'}),
            _make_server('mid', metadata={'instanceha:restart_priority': '500'}),
        ]
        groups = instanceha._build_evacuation_groups(servers)
        self.assertEqual(len(groups), 3)
        self.assertEqual(groups[0][0].id, 'high')
        self.assertEqual(groups[1][0].id, 'mid')
        self.assertEqual(groups[2][0].id, 'low')

    def test_mixed_grouped_and_ungrouped(self):
        servers = [
            _make_server('db-1', metadata={'instanceha:restart_group': 'database', 'instanceha:restart_priority': '900'}),
            _make_server('solo-1', metadata={'instanceha:restart_priority': '700'}),
            _make_server('web-1', metadata={'instanceha:restart_group': 'web', 'instanceha:restart_priority': '100'}),
        ]
        groups = instanceha._build_evacuation_groups(servers)
        self.assertEqual(len(groups), 3)
        # database group (900) first, solo-1 (700) second, web group (100) third
        self.assertEqual(groups[0][0].id, 'db-1')
        self.assertEqual(groups[1][0].id, 'solo-1')
        self.assertEqual(groups[2][0].id, 'web-1')

    def test_empty_list(self):
        groups = instanceha._build_evacuation_groups([])
        self.assertEqual(groups, [])

    def test_all_default_priority(self):
        servers = [_make_server('s1'), _make_server('s2')]
        groups = instanceha._build_evacuation_groups(servers)
        # Both have priority 500, each is its own group
        self.assertEqual(len(groups), 2)

    def test_warns_when_no_servers_have_metadata(self):
        servers = [_make_server('s1'), _make_server('s2')]
        with patch('instanceha.logging') as mock_log:
            instanceha._build_evacuation_groups(servers)
            mock_log.warning.assert_any_call(
                "ORCHESTRATED_RESTART is enabled but no servers have "
                "instanceha:restart_priority or instanceha:restart_group metadata. "
                "All servers will be evacuated with default priority 500.")

    def test_no_warning_when_servers_have_metadata(self):
        servers = [_make_server('s1', metadata={'instanceha:restart_priority': '900'})]
        with patch('instanceha.logging') as mock_log:
            instanceha._build_evacuation_groups(servers)
            for call in mock_log.warning.call_args_list:
                self.assertNotIn('ORCHESTRATED_RESTART is enabled but', str(call))


# ============================================================================
# _orchestrated_evacuate tests
# ============================================================================

class TestOrchestratedEvacuate(unittest.TestCase):

    def _make_config(self, workers=4, evacuation_retries=5):
        config = MagicMock()
        config.get_config_value.side_effect = lambda key: {
            'WORKERS': workers,
            'EVACUATION_RETRIES': evacuation_retries,
        }.get(key, None)
        return config

    def _make_service(self, workers=4):
        service = MagicMock()
        service.config = self._make_config(workers)
        return service

    @patch('instanceha._update_service_disable_reason')
    @patch('instanceha._server_evacuate_future')
    @patch('instanceha._build_evacuation_groups')
    def test_all_phases_succeed(self, mock_groups, mock_future, mock_update):
        phase1 = [_make_server('db-1')]
        phase2 = [_make_server('app-1')]
        mock_groups.return_value = [phase1, phase2]
        mock_future.return_value = True

        conn = MagicMock()
        service = self._make_service()

        result = instanceha._orchestrated_evacuate(conn, phase1 + phase2, service, 'host1', 'svc-1')
        self.assertTrue(result)
        self.assertEqual(mock_future.call_count, 2)
        mock_update.assert_not_called()

    @patch('instanceha._update_service_disable_reason')
    @patch('instanceha._server_evacuate_future')
    @patch('instanceha._build_evacuation_groups')
    def test_first_phase_failure_continues(self, mock_groups, mock_future, mock_update):
        phase1 = [_make_server('db-1')]
        phase2 = [_make_server('app-1')]
        mock_groups.return_value = [phase1, phase2]
        mock_future.return_value = False

        conn = MagicMock()
        service = self._make_service()

        result = instanceha._orchestrated_evacuate(conn, phase1 + phase2, service, 'host1', 'svc-1')
        self.assertFalse(result)
        # Both phases should be attempted despite phase 1 failure
        self.assertEqual(mock_future.call_count, 2)
        mock_update.assert_called_once()

    @patch('instanceha._update_service_disable_reason')
    @patch('instanceha._server_evacuate_future')
    @patch('instanceha._build_evacuation_groups')
    def test_second_phase_failure(self, mock_groups, mock_future, mock_update):
        phase1 = [_make_server('db-1')]
        phase2 = [_make_server('app-1')]
        mock_groups.return_value = [phase1, phase2]
        mock_future.side_effect = [True, False]

        conn = MagicMock()
        service = self._make_service()

        result = instanceha._orchestrated_evacuate(conn, phase1 + phase2, service, 'host1', 'svc-1')
        self.assertFalse(result)
        self.assertEqual(mock_future.call_count, 2)
        mock_update.assert_called_once()

    @patch('instanceha._update_service_disable_reason')
    @patch('instanceha._server_evacuate_future')
    @patch('instanceha._build_evacuation_groups')
    def test_exception_in_phase_returns_false(self, mock_groups, mock_future, mock_update):
        phase1 = [_make_server('db-1')]
        mock_groups.return_value = [phase1]
        mock_future.side_effect = Exception("connection lost")

        conn = MagicMock()
        service = self._make_service()

        result = instanceha._orchestrated_evacuate(conn, phase1, service, 'host1', 'svc-1')
        self.assertFalse(result)
        mock_update.assert_called_once()

    @patch('instanceha._update_service_disable_reason')
    @patch('instanceha._server_evacuate_future')
    @patch('instanceha._build_evacuation_groups')
    def test_partial_phase_failure_continues(self, mock_groups, mock_future, mock_update):
        servers = [_make_server('s-1'), _make_server('s-2'), _make_server('s-3')]
        mock_groups.return_value = [servers]
        mock_future.side_effect = [True, False, True]

        conn = MagicMock()
        service = self._make_service()

        result = instanceha._orchestrated_evacuate(conn, servers, service, 'host1', 'svc-1')
        self.assertFalse(result)
        self.assertEqual(mock_future.call_count, 3)
        mock_update.assert_called_once()

    @patch('instanceha._update_service_disable_reason')
    @patch('instanceha._server_evacuate_future')
    @patch('instanceha._build_evacuation_groups')
    def test_single_phase_all_concurrent(self, mock_groups, mock_future, mock_update):
        servers = [_make_server(f's-{i}') for i in range(5)]
        mock_groups.return_value = [servers]
        mock_future.return_value = True

        conn = MagicMock()
        service = self._make_service()

        result = instanceha._orchestrated_evacuate(conn, servers, service, 'host1', 'svc-1')
        self.assertTrue(result)
        self.assertEqual(mock_future.call_count, 5)

    @patch('instanceha._update_service_disable_reason')
    @patch('instanceha._server_evacuate_future')
    @patch('instanceha._build_evacuation_groups')
    def test_target_host_passed_through(self, mock_groups, mock_future, mock_update):
        server = _make_server('s-1')
        mock_groups.return_value = [[server]]
        mock_future.return_value = True

        conn = MagicMock()
        service = self._make_service()

        instanceha._orchestrated_evacuate(conn, [server], service, 'host1', 'svc-1',
                                          target_host='reserved-01')
        mock_future.assert_called_once_with(conn, server, 'reserved-01',
                                                   max_retries=5,
                                                   shutdown_event=service.shutdown_event)

    @patch('instanceha._update_service_disable_reason')
    @patch('instanceha._server_evacuate_future')
    @patch('instanceha._build_evacuation_groups')
    def test_empty_phases(self, mock_groups, mock_future, mock_update):
        mock_groups.return_value = []

        conn = MagicMock()
        service = self._make_service()

        result = instanceha._orchestrated_evacuate(conn, [], service, 'host1', 'svc-1')
        self.assertTrue(result)
        mock_future.assert_not_called()


# ============================================================================
# _host_evacuate orchestration branching tests
# ============================================================================

class TestHostEvacuateOrchestration(unittest.TestCase):

    def _make_service(self, orchestrated=False, smart=False):
        service = MagicMock()
        service.config.get_config_value.side_effect = lambda key: {
            'ORCHESTRATED_RESTART': orchestrated,
            'SMART_EVACUATION': smart,
            'DELAY': 0,
        }.get(key, None)
        return service

    @patch('instanceha._orchestrated_evacuate', return_value=True)
    @patch('instanceha._smart_evacuate', return_value=True)
    @patch('instanceha._traditional_evacuate', return_value=True)
    @patch('instanceha._get_evacuable_servers')
    @patch('instanceha.time.sleep')
    def test_orchestrated_restart_config_enabled(self, mock_sleep, mock_get, mock_trad,
                                                  mock_smart, mock_orch):
        mock_get.return_value = [_make_server('s1')]
        service = self._make_service(orchestrated=True)
        failed = Mock(host='host1', id='svc-1')

        instanceha._host_evacuate(MagicMock(), failed, service)
        mock_orch.assert_called_once()
        mock_smart.assert_not_called()
        mock_trad.assert_not_called()

    @patch('instanceha._orchestrated_evacuate', return_value=True)
    @patch('instanceha._smart_evacuate', return_value=True)
    @patch('instanceha._traditional_evacuate', return_value=True)
    @patch('instanceha._get_evacuable_servers')
    @patch('instanceha.time.sleep')
    def test_metadata_ignored_when_config_disabled(self, mock_sleep, mock_get, mock_trad, mock_smart, mock_orch):
        mock_get.return_value = [
            _make_server('s1', metadata={'instanceha:restart_priority': '900'}),
        ]
        service = self._make_service(orchestrated=False, smart=True)
        failed = Mock(host='host1', id='svc-1')

        instanceha._host_evacuate(MagicMock(), failed, service)
        mock_orch.assert_not_called()
        mock_smart.assert_called_once()

    @patch('instanceha._orchestrated_evacuate', return_value=True)
    @patch('instanceha._smart_evacuate', return_value=True)
    @patch('instanceha._traditional_evacuate', return_value=True)
    @patch('instanceha._get_evacuable_servers')
    @patch('instanceha.time.sleep')
    def test_no_metadata_smart_evacuation(self, mock_sleep, mock_get, mock_trad,
                                          mock_smart, mock_orch):
        mock_get.return_value = [_make_server('s1')]
        service = self._make_service(orchestrated=False, smart=True)
        failed = Mock(host='host1', id='svc-1')

        instanceha._host_evacuate(MagicMock(), failed, service)
        mock_smart.assert_called_once()
        mock_orch.assert_not_called()
        mock_trad.assert_not_called()

    @patch('instanceha._orchestrated_evacuate', return_value=True)
    @patch('instanceha._smart_evacuate', return_value=True)
    @patch('instanceha._traditional_evacuate', return_value=True)
    @patch('instanceha._get_evacuable_servers')
    @patch('instanceha.time.sleep')
    def test_no_metadata_traditional(self, mock_sleep, mock_get, mock_trad,
                                      mock_smart, mock_orch):
        mock_get.return_value = [_make_server('s1')]
        service = self._make_service(orchestrated=False, smart=False)
        failed = Mock(host='host1', id='svc-1')

        instanceha._host_evacuate(MagicMock(), failed, service)
        mock_trad.assert_called_once()
        mock_orch.assert_not_called()
        mock_smart.assert_not_called()

    @patch('instanceha._orchestrated_evacuate', return_value=True)
    @patch('instanceha._smart_evacuate', return_value=True)
    @patch('instanceha._get_evacuable_servers')
    @patch('instanceha.time.sleep')
    def test_orchestrated_takes_precedence_over_smart(self, mock_sleep, mock_get,
                                                       mock_smart, mock_orch):
        mock_get.return_value = [_make_server('s1')]
        service = self._make_service(orchestrated=True, smart=True)
        failed = Mock(host='host1', id='svc-1')

        instanceha._host_evacuate(MagicMock(), failed, service)
        mock_orch.assert_called_once()
        mock_smart.assert_not_called()

    @patch('instanceha._orchestrated_evacuate')
    @patch('instanceha._smart_evacuate')
    @patch('instanceha._traditional_evacuate')
    @patch('instanceha._get_evacuable_servers')
    def test_empty_evacuables_returns_true(self, mock_get, mock_trad, mock_smart, mock_orch):
        mock_get.return_value = []
        service = self._make_service()
        failed = Mock(host='host1', id='svc-1')

        result = instanceha._host_evacuate(MagicMock(), failed, service)
        self.assertTrue(result)
        mock_orch.assert_not_called()
        mock_smart.assert_not_called()
        mock_trad.assert_not_called()


# ============================================================================
# ORCHESTRATED_RESTART config tests
# ============================================================================

class TestOrchestratedRestartConfig(unittest.TestCase):

    def test_config_in_config_map(self):
        self.assertIn('ORCHESTRATED_RESTART', instanceha.ConfigManager._config_map)

    def test_config_type_is_bool(self):
        item = instanceha.ConfigManager._config_map['ORCHESTRATED_RESTART']
        self.assertEqual(item.type, 'bool')

    def test_config_default_is_false(self):
        item = instanceha.ConfigManager._config_map['ORCHESTRATED_RESTART']
        self.assertFalse(item.default)


if __name__ == '__main__':
    unittest.main()

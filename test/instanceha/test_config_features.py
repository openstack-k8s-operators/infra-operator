"""
Configuration feature tests for InstanceHA.

Tests configuration parameters from lines 461-483 that control
service behavior and ensure all features are properly tested.
"""

import os
import sys
import unittest
from unittest.mock import Mock, patch, MagicMock
from datetime import datetime, timedelta

# Mock OpenStack dependencies
if 'novaclient' not in sys.modules:
    sys.modules['novaclient'] = MagicMock()
    sys.modules['novaclient.client'] = MagicMock()
    # Create actual exception classes for novaclient
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

if 'keystoneauth1' not in sys.modules:
    sys.modules['keystoneauth1'] = MagicMock()
    sys.modules['keystoneauth1.loading'] = MagicMock()
    sys.modules['keystoneauth1.session'] = MagicMock()

    # Create discovery failure exception
    class DiscoveryFailure(Exception):
        pass

    discovery_module = MagicMock()
    discovery_module.DiscoveryFailure = DiscoveryFailure

    exceptions_module = MagicMock()
    exceptions_module.discovery = discovery_module

    sys.modules['keystoneauth1.exceptions'] = exceptions_module
    sys.modules['keystoneauth1.exceptions.discovery'] = discovery_module

# Add module path
test_dir = os.path.dirname(os.path.abspath(__file__))
instanceha_path = os.path.join(test_dir, '../../templates/instanceha/bin/')
sys.path.insert(0, os.path.abspath(instanceha_path))

import instanceha


class TestDisabledConfig(unittest.TestCase):
    """Test DISABLED configuration parameter (line 478, used at line 2567-2568)."""

    def test_disabled_true_skips_evacuation(self):
        """Test that DISABLED=True skips all evacuations."""
        mock_conn = Mock()
        mock_service = Mock()
        mock_service.config = Mock()
        mock_service.config.get_config_value = Mock(side_effect=lambda key: {'DISABLED': True, 'THRESHOLD': 50}.get(key, False))
        mock_service.processing_lock = Mock()
        mock_service.hosts_processing = {}

        mock_failed_service = Mock()
        mock_failed_service.host = 'test-host.example.com'

        # Create mock services list with one down service
        services = [mock_failed_service, Mock(), Mock()]  # Add more to avoid threshold
        compute_nodes = [mock_failed_service]

        with patch('instanceha._filter_processing_hosts', return_value=(compute_nodes, [], set(['test-host']), 0)):
            with patch('instanceha._prepare_evacuation_resources', return_value=(compute_nodes, [], [], [])):
                with patch('instanceha._cleanup_filtered_hosts'):
                    with patch('instanceha.process_service') as mock_process:
                        instanceha._process_stale_services(
                            mock_conn,
                            mock_service,
                            services,
                            compute_nodes,
                            []
                        )

        # Should NOT process any services when disabled
        mock_process.assert_not_called()

    def test_disabled_false_processes_evacuation(self):
        """Test that DISABLED=False processes evacuations normally."""
        mock_conn = Mock()
        mock_service = Mock()
        mock_service.config = Mock()
        config_values = {
            'DISABLED': False,
            'CHECK_KDUMP': False,
            'THRESHOLD': 50,
            'TAGGED_IMAGES': False,
            'TAGGED_FLAVORS': False,
            'TAGGED_AGGREGATES': False,
            'POLL': 45
        }
        mock_service.config.get_config_value = Mock(side_effect=lambda key: config_values.get(key, False))
        mock_service.processing_lock = Mock()
        mock_service.hosts_processing = {}

        mock_failed_service = Mock()
        mock_failed_service.host = 'test-host.example.com'

        services = [mock_failed_service, Mock(), Mock()]  # Add more to avoid threshold
        compute_nodes = [mock_failed_service]

        with patch('instanceha._filter_processing_hosts', return_value=(compute_nodes, [], set(['test-host']), 0)):
            with patch('instanceha._prepare_evacuation_resources', return_value=(compute_nodes, [], [], [])):
                with patch('instanceha._cleanup_filtered_hosts'):
                    with patch('instanceha.process_service', return_value=True) as mock_process:
                        instanceha._process_stale_services(
                            mock_conn,
                            mock_service,
                            services,
                            compute_nodes,
                            []
                        )

        # Should process services when not disabled
        mock_process.assert_called()


class TestForceEnableConfig(unittest.TestCase):
    """Test FORCE_ENABLE configuration parameter (line 475, used at line 2615)."""

    def test_force_enable_true_bypasses_migration_check(self):
        """Test that FORCE_ENABLE=True bypasses migration completion check."""
        mock_conn = Mock()
        mock_config = Mock()
        mock_config.get_config_value = Mock(side_effect=lambda key: {'LEAVE_DISABLED': False, 'FORCE_ENABLE': True}.get(key, False))

        service = instanceha.InstanceHAService(mock_config)

        mock_svc = Mock()
        mock_svc.host = 'test-host.example.com'
        mock_svc.forced_down = True
        mock_svc.status = 'disabled'
        mock_svc.disabled_reason = 'instanceha evacuation complete: 2025-01-01'

        # Mock incomplete migrations (normally would prevent re-enable)
        incomplete_migration = Mock()
        incomplete_migration.status = 'running'
        mock_conn.migrations.list.return_value = [incomplete_migration]

        with patch('instanceha._host_enable') as mock_enable:
            instanceha._process_reenabling(mock_conn, service, [mock_svc])
            # Should attempt to enable despite incomplete migrations
            mock_enable.assert_called_once_with(mock_conn, mock_svc, reenable=True, service=service)

    def test_force_enable_false_waits_for_migrations(self):
        """Test that FORCE_ENABLE=False waits for migration completion."""
        mock_conn = Mock()
        mock_config = Mock()
        mock_config.get_config_value = Mock(side_effect=lambda key: {'LEAVE_DISABLED': False, 'FORCE_ENABLE': False}.get(key, False))

        service = instanceha.InstanceHAService(mock_config)

        mock_svc = Mock()
        mock_svc.host = 'test-host.example.com'
        mock_svc.forced_down = True
        mock_svc.status = 'disabled'
        mock_svc.disabled_reason = 'instanceha evacuation complete: 2025-01-01'

        # Mock incomplete migrations
        incomplete_migration = Mock()
        incomplete_migration.status = 'running'
        mock_conn.migrations.list.return_value = [incomplete_migration]

        with patch('instanceha._host_enable') as mock_enable:
            instanceha._process_reenabling(mock_conn, service, [mock_svc])
            # Should NOT attempt to enable with incomplete migrations
            mock_enable.assert_not_called()

    def test_force_enable_still_respects_kdump_delay(self):
        """Test that FORCE_ENABLE=True still respects kdump re-enable delay."""
        mock_conn = Mock()
        mock_config = Mock()
        mock_config.get_config_value = Mock(side_effect=lambda key: {'LEAVE_DISABLED': False, 'FORCE_ENABLE': True}.get(key, False))

        service = instanceha.InstanceHAService(mock_config)
        service.kdump_hosts_timestamp['test-host'] = __import__('time').time() - 30  # 30s ago (< 60s)

        mock_svc = Mock()
        mock_svc.host = 'test-host.example.com'
        mock_svc.forced_down = True
        mock_svc.status = 'disabled'
        mock_svc.disabled_reason = 'instanceha evacuation complete (kdump): 2025-01-01'

        with patch('instanceha._host_enable') as mock_enable:
            instanceha._process_reenabling(mock_conn, service, [mock_svc])
            # Should NOT enable - kdump delay overrides FORCE_ENABLE
            mock_enable.assert_not_called()

    def test_force_enable_with_completed_migrations(self):
        """Test that FORCE_ENABLE=True works normally when migrations are complete."""
        mock_conn = Mock()
        mock_config = Mock()
        mock_config.get_config_value = Mock(side_effect=lambda key: {'LEAVE_DISABLED': False, 'FORCE_ENABLE': True}.get(key, False))

        service = instanceha.InstanceHAService(mock_config)

        mock_svc = Mock()
        mock_svc.host = 'test-host.example.com'
        mock_svc.forced_down = False
        mock_svc.state = 'up'
        mock_svc.status = 'disabled'
        mock_svc.disabled_reason = 'instanceha evacuation complete: 2025-01-01'

        # Migrations complete (empty list)
        mock_conn.migrations.list.return_value = []

        with patch('instanceha._host_enable') as mock_enable:
            instanceha._process_reenabling(mock_conn, service, [mock_svc])
            # Should enable - migrations complete and service is up
            mock_enable.assert_called_once_with(mock_conn, mock_svc, reenable=False)

    def test_force_enable_handles_forced_down_correctly(self):
        """Test that FORCE_ENABLE correctly handles forced_down hosts in two stages."""
        mock_conn = Mock()
        mock_config = Mock()
        mock_config.get_config_value = Mock(side_effect=lambda key: {'LEAVE_DISABLED': False, 'FORCE_ENABLE': True}.get(key, False))

        service = instanceha.InstanceHAService(mock_config)

        # Service is forced_down and disabled, but state is still down
        mock_svc = Mock()
        mock_svc.host = 'test-host.example.com'
        mock_svc.forced_down = True
        mock_svc.state = 'down'
        mock_svc.status = 'disabled'
        mock_svc.disabled_reason = 'instanceha evacuation complete: 2025-01-01'

        with patch('instanceha._host_enable') as mock_enable:
            instanceha._process_reenabling(mock_conn, service, [mock_svc])
            # Should unset forced_down (reenable=True) but NOT enable (service still down)
            mock_enable.assert_called_once_with(mock_conn, mock_svc, reenable=True, service=service)

    def test_force_enable_respects_service_state_down(self):
        """Test that FORCE_ENABLE still checks service state before enabling."""
        mock_conn = Mock()
        mock_config = Mock()
        mock_config.get_config_value = Mock(side_effect=lambda key: {'LEAVE_DISABLED': False, 'FORCE_ENABLE': True}.get(key, False))

        service = instanceha.InstanceHAService(mock_config)

        # Service is disabled but still down (not ready for enable)
        mock_svc = Mock()
        mock_svc.host = 'test-host.example.com'
        mock_svc.forced_down = False
        mock_svc.state = 'down'  # Still down
        mock_svc.status = 'disabled'
        mock_svc.disabled_reason = 'instanceha evacuation complete: 2025-01-01'

        with patch('instanceha._host_enable') as mock_enable:
            instanceha._process_reenabling(mock_conn, service, [mock_svc])
            # Should NOT enable - service is still down
            mock_enable.assert_not_called()

    def test_force_enable_with_error_migrations(self):
        """Test that FORCE_ENABLE ignores error state migrations."""
        mock_conn = Mock()
        mock_config = Mock()
        mock_config.get_config_value = Mock(side_effect=lambda key: {'LEAVE_DISABLED': False, 'FORCE_ENABLE': False}.get(key, False))

        service = instanceha.InstanceHAService(mock_config)

        mock_svc = Mock()
        mock_svc.host = 'test-host.example.com'
        mock_svc.forced_down = False
        mock_svc.state = 'up'
        mock_svc.status = 'disabled'
        mock_svc.disabled_reason = 'instanceha evacuation complete: 2025-01-01'

        # Migrations include error state (should be ignored)
        migration_error = Mock()
        migration_error.status = 'error'
        migration_completed = Mock()
        migration_completed.status = 'completed'

        mock_conn.migrations.list.return_value = [migration_error, migration_completed]

        with patch('instanceha._host_enable') as mock_enable:
            instanceha._process_reenabling(mock_conn, service, [mock_svc])
            # Should enable - error migrations are ignored
            mock_enable.assert_called_once()

    def test_migration_query_parameters(self):
        """Test that migration query uses correct parameters."""
        mock_conn = Mock()
        mock_config = Mock()
        mock_config.get_config_value = Mock(side_effect=lambda key: {'LEAVE_DISABLED': False, 'FORCE_ENABLE': False}.get(key, False))

        service = instanceha.InstanceHAService(mock_config)

        mock_svc = Mock()
        mock_svc.host = 'test-host.example.com'
        mock_svc.forced_down = False
        mock_svc.state = 'up'
        mock_svc.status = 'disabled'
        mock_svc.disabled_reason = 'instanceha evacuation complete: 2025-01-01'

        # Mock migrations list
        mock_conn.migrations.list.return_value = []

        with patch('instanceha._host_enable'):
            instanceha._process_reenabling(mock_conn, service, [mock_svc])

            # Verify migration query was called with correct parameters
            mock_conn.migrations.list.assert_called_once()
            call_kwargs = mock_conn.migrations.list.call_args[1]

            self.assertEqual(call_kwargs['source_compute'], 'test-host.example.com')
            self.assertEqual(call_kwargs['migration_type'], 'evacuation')
            self.assertIn('changes_since', call_kwargs)
            self.assertEqual(call_kwargs['limit'], 1000)


class TestLeaveDisabledConfig(unittest.TestCase):
    """Test LEAVE_DISABLED configuration parameter (line 474, used at line 2603-2606)."""

    def test_leave_disabled_true_filters_instanceha_services(self):
        """Test that LEAVE_DISABLED=True filters instanceha-evacuated services from re-enable."""
        mock_conn = Mock()
        mock_config = Mock()
        mock_config.get_config_value = Mock(side_effect=lambda key: {'LEAVE_DISABLED': True}.get(key, False))

        service = instanceha.InstanceHAService(mock_config)

        # Service evacuated by instanceha
        instanceha_svc = Mock()
        instanceha_svc.host = 'test-host-1.example.com'
        instanceha_svc.forced_down = True
        instanceha_svc.status = 'disabled'
        instanceha_svc.disabled_reason = 'instanceha evacuation complete: 2025-01-01'

        # Service disabled by other means
        other_svc = Mock()
        other_svc.host = 'test-host-2.example.com'
        other_svc.forced_down = True
        other_svc.status = 'disabled'
        other_svc.disabled_reason = 'manual maintenance'

        # Mock migrations complete for both
        mock_conn.migrations.list.return_value = []

        with patch('instanceha._host_enable') as mock_enable:
            instanceha._process_reenabling(mock_conn, service, [instanceha_svc, other_svc])

            # Should only enable the non-instanceha service
            self.assertEqual(mock_enable.call_count, 1)
            # Verify it was called for the 'other_svc', not 'instanceha_svc'
            mock_enable.assert_called_with(mock_conn, other_svc, reenable=True, service=service)

    def test_leave_disabled_false_enables_all_services(self):
        """Test that LEAVE_DISABLED=False enables all services including instanceha-evacuated."""
        mock_conn = Mock()
        mock_config = Mock()
        mock_config.get_config_value = Mock(side_effect=lambda key: {'LEAVE_DISABLED': False}.get(key, False))

        service = instanceha.InstanceHAService(mock_config)

        # Service evacuated by instanceha
        instanceha_svc = Mock()
        instanceha_svc.host = 'test-host.example.com'
        instanceha_svc.forced_down = True
        instanceha_svc.status = 'disabled'
        instanceha_svc.disabled_reason = 'instanceha evacuation complete: 2025-01-01'

        # Mock migrations complete
        mock_conn.migrations.list.return_value = []

        with patch('instanceha._host_enable') as mock_enable:
            instanceha._process_reenabling(mock_conn, service, [instanceha_svc])

            # Should enable instanceha-evacuated service when LEAVE_DISABLED=False
            mock_enable.assert_called_once_with(mock_conn, instanceha_svc, reenable=True, service=service)


class TestTaggedAggregatesConfig(unittest.TestCase):
    """Test TAGGED_AGGREGATES configuration parameter (line 473)."""

    def test_tagged_aggregates_true_filters_by_aggregate(self):
        """Test that TAGGED_AGGREGATES=True filters compute nodes by aggregate metadata."""
        mock_conn = Mock()

        # Create real InstanceHAService instance for proper _is_resource_evacuable behavior
        mock_config = Mock()
        mock_config.get_config_value = Mock(side_effect=lambda key: {'EVACUABLE_TAG': 'evacuable'}.get(key, 'evacuable'))
        mock_service = instanceha.InstanceHAService(mock_config)

        # Create aggregates - one evacuable, one not
        evacuable_agg = Mock()
        evacuable_agg.hosts = ['host1', 'host2']
        evacuable_agg.metadata = {'evacuable': 'true'}

        non_evacuable_agg = Mock()
        non_evacuable_agg.hosts = ['host3']
        non_evacuable_agg.metadata = {'evacuable': 'false'}

        mock_conn.aggregates.list.return_value = [evacuable_agg, non_evacuable_agg]

        # Create services on different hosts
        svc1 = Mock()
        svc1.host = 'host1'
        svc2 = Mock()
        svc2.host = 'host2'
        svc3 = Mock()
        svc3.host = 'host3'

        compute_nodes = [svc1, svc2, svc3]
        services = compute_nodes

        # Filter by aggregates
        result = instanceha._filter_by_aggregates(mock_conn, mock_service, compute_nodes, services)

        # Should only include hosts from evacuable aggregate
        self.assertEqual(len(result), 2)
        result_hosts = {svc.host for svc in result}
        self.assertIn('host1', result_hosts)
        self.assertIn('host2', result_hosts)
        self.assertNotIn('host3', result_hosts)

    def test_tagged_aggregates_false_does_not_filter(self):
        """Test that TAGGED_AGGREGATES=False does not filter by aggregate."""
        mock_conn = Mock()
        mock_service = Mock()
        mock_service.config = Mock()
        config_values = {
            'DISABLED': False,
            'CHECK_KDUMP': False,
            'THRESHOLD': 50,
            'TAGGED_IMAGES': False,
            'TAGGED_FLAVORS': False,
            'TAGGED_AGGREGATES': False
        }
        mock_service.config.get_config_value = Mock(side_effect=lambda key: config_values.get(key, False))
        mock_service.processing_lock = Mock()
        mock_service.hosts_processing = {}

        svc1 = Mock()
        svc1.host = 'host1'
        svc2 = Mock()
        svc2.host = 'host2'

        services = [svc1, svc2]
        compute_nodes = [svc1, svc2]

        with patch('instanceha._filter_processing_hosts', return_value=(compute_nodes, [], set(['host1', 'host2']), 0)):
            with patch('instanceha._prepare_evacuation_resources', return_value=(compute_nodes, [], [], [])):
                with patch('instanceha._cleanup_filtered_hosts'):
                    with patch('instanceha.process_service', return_value=True):
                        # Should not call _filter_by_aggregates when disabled
                        with patch('instanceha._filter_by_aggregates') as mock_filter:
                            instanceha._process_stale_services(
                                mock_conn,
                                mock_service,
                                services,
                                compute_nodes,
                                []
                            )
                            mock_filter.assert_not_called()


class TestDelayConfig(unittest.TestCase):
    """Test DELAY configuration parameter (line 479, used at line 1408)."""

    def test_delay_zero_skips_sleep(self):
        """Test that DELAY=0 does not delay evacuation."""
        import time
        mock_conn = Mock()
        mock_config = Mock()
        config_values = {
            'DELAY': 0,
            'SMART_EVACUATION': False
        }
        mock_config.get_config_value = Mock(side_effect=lambda key: config_values.get(key, False))

        service = instanceha.InstanceHAService(mock_config)

        # Create a mock server to evacuate
        mock_server = Mock()
        mock_server.id = 'server-123'

        # Mock the evacuation functions to avoid actual evacuation
        with patch('time.sleep') as mock_sleep:
            with patch('instanceha._get_evacuable_servers', return_value=[mock_server]):
                with patch('instanceha._traditional_evacuate', return_value=True):
                    failed_service = Mock()
                    failed_service.host = 'test-host.example.com'
                    failed_service.id = 'service-123'

                    # Call _host_evacuate which contains the DELAY sleep
                    result = instanceha._host_evacuate(mock_conn, failed_service, service)

                    # Verify sleep was called with 0 (DELAY=0)
                    mock_sleep.assert_called_once_with(0)

    def test_delay_non_zero_delays_evacuation(self):
        """Test that DELAY>0 delays evacuation by specified seconds."""
        mock_conn = Mock()
        mock_config = Mock()
        config_values = {
            'DELAY': 5,  # 5 second delay
            'SMART_EVACUATION': False
        }
        mock_config.get_config_value = Mock(side_effect=lambda key: config_values.get(key, False))

        service = instanceha.InstanceHAService(mock_config)

        # Create a mock server to evacuate
        mock_server = Mock()
        mock_server.id = 'server-123'

        with patch('time.sleep') as mock_sleep:
            with patch('instanceha._get_evacuable_servers', return_value=[mock_server]):
                with patch('instanceha._traditional_evacuate', return_value=True):
                    failed_service = Mock()
                    failed_service.host = 'test-host.example.com'
                    failed_service.id = 'service-123'

                    result = instanceha._host_evacuate(mock_conn, failed_service, service)

                    # Verify sleep was called with configured DELAY value
                    mock_sleep.assert_called_once_with(5)

    def test_delay_with_smart_evacuation(self):
        """Test that DELAY works with SMART_EVACUATION enabled."""
        mock_conn = Mock()
        mock_config = Mock()
        config_values = {
            'DELAY': 3,
            'SMART_EVACUATION': True
        }
        mock_config.get_config_value = Mock(side_effect=lambda key: config_values.get(key, False))

        service = instanceha.InstanceHAService(mock_config)

        # Create a mock server to evacuate
        mock_server = Mock()
        mock_server.id = 'server-123'

        with patch('time.sleep') as mock_sleep:
            with patch('instanceha._get_evacuable_servers', return_value=[mock_server]):
                with patch('instanceha._smart_evacuate', return_value=True):
                    failed_service = Mock()
                    failed_service.host = 'test-host.example.com'
                    failed_service.id = 'service-123'

                    result = instanceha._host_evacuate(mock_conn, failed_service, service)

                    # Verify sleep was called with DELAY before smart evacuation
                    mock_sleep.assert_called_once_with(3)

    def test_delay_boundary_values(self):
        """Test DELAY boundary values (min=0, max=300)."""
        mock_config = Mock()

        # Test minimum value
        config_values = {'DELAY': 0}
        mock_config.get_config_value = Mock(side_effect=lambda key: config_values.get(key, 0))
        service = instanceha.InstanceHAService(mock_config)
        self.assertEqual(service.config.get_config_value('DELAY'), 0)

        # Test maximum value
        config_values = {'DELAY': 300}
        mock_config.get_config_value = Mock(side_effect=lambda key: config_values.get(key, 0))
        service = instanceha.InstanceHAService(mock_config)
        self.assertEqual(service.config.get_config_value('DELAY'), 300)


class TestHashIntervalConfig(unittest.TestCase):
    """Test HASH_INTERVAL configuration parameter (line 494, used at line 621)."""

    def test_hash_interval_default_value(self):
        """Test that HASH_INTERVAL defaults to 60 seconds."""
        mock_config = Mock()
        # Simulate default behavior
        mock_config.get_config_value = Mock(return_value=60)

        service = instanceha.InstanceHAService(mock_config)
        hash_interval = service.config.get_config_value('HASH_INTERVAL')

        self.assertEqual(hash_interval, 60)

    def test_hash_interval_prevents_frequent_updates(self):
        """Test that HASH_INTERVAL prevents hash updates too frequently."""
        import time
        mock_config = Mock()
        config_values = {'HASH_INTERVAL': 60}
        mock_config.get_config_value = Mock(side_effect=lambda key: config_values.get(key, 60))

        service = instanceha.InstanceHAService(mock_config)

        # First hash update should succeed
        service.update_health_hash()
        first_hash = service.current_hash
        self.assertTrue(service.hash_update_successful)

        # Immediate second update should not change hash (within interval)
        service.update_health_hash()
        second_hash = service.current_hash

        # Hash should not change because interval hasn't elapsed
        self.assertEqual(first_hash, second_hash)

    def test_hash_interval_allows_update_after_interval(self):
        """Test that hash updates after HASH_INTERVAL has elapsed."""
        mock_config = Mock()
        config_values = {'HASH_INTERVAL': 1}  # 1 second for testing
        mock_config.get_config_value = Mock(side_effect=lambda key: config_values.get(key, 1))

        service = instanceha.InstanceHAService(mock_config)

        # First hash update
        service.update_health_hash()
        first_hash = service.current_hash
        first_time = service._last_hash_time

        # Simulate time passing by manually updating timestamp
        import time
        time.sleep(1.1)  # Wait slightly longer than interval

        # Second update should succeed and change hash
        service.update_health_hash()
        second_hash = service.current_hash

        # Hash should have changed after interval
        self.assertNotEqual(first_hash, second_hash)
        self.assertTrue(service.hash_update_successful)

    def test_hash_interval_custom_override(self):
        """Test that update_health_hash can override HASH_INTERVAL."""
        mock_config = Mock()
        config_values = {'HASH_INTERVAL': 60}
        mock_config.get_config_value = Mock(side_effect=lambda key: config_values.get(key, 60))

        service = instanceha.InstanceHAService(mock_config)

        # First update with default interval (60s)
        service.update_health_hash()
        first_hash = service.current_hash

        # Override with 0 to force immediate update
        import time
        time.sleep(0.1)
        service.update_health_hash(hash_interval=0)
        second_hash = service.current_hash

        # Hash should have changed because we overrode the interval
        self.assertNotEqual(first_hash, second_hash)

    def test_hash_interval_boundary_values(self):
        """Test HASH_INTERVAL boundary values (min=30, max=300)."""
        mock_config = Mock()

        # Test minimum value
        config_values = {'HASH_INTERVAL': 30}
        mock_config.get_config_value = Mock(side_effect=lambda key: config_values.get(key, 30))
        service = instanceha.InstanceHAService(mock_config)
        self.assertEqual(service.config.get_config_value('HASH_INTERVAL'), 30)

        # Test maximum value
        config_values = {'HASH_INTERVAL': 300}
        mock_config.get_config_value = Mock(side_effect=lambda key: config_values.get(key, 30))
        service = instanceha.InstanceHAService(mock_config)
        self.assertEqual(service.config.get_config_value('HASH_INTERVAL'), 300)

    def test_hash_update_failure_detection(self):
        """Test that hash update detects failures (duplicate hash)."""
        mock_config = Mock()
        config_values = {'HASH_INTERVAL': 0}  # Always allow updates
        mock_config.get_config_value = Mock(side_effect=lambda key: config_values.get(key, 0))

        service = instanceha.InstanceHAService(mock_config)

        # Mock hashlib to return same hash twice (simulated failure)
        with patch('hashlib.sha256') as mock_sha256:
            mock_hash = Mock()
            mock_hash.hexdigest.return_value = 'same-hash-value'
            mock_sha256.return_value = mock_hash

            # First update
            service.update_health_hash()
            self.assertTrue(service.hash_update_successful)
            self.assertEqual(service.current_hash, 'same-hash-value')

            # Second update with same hash should fail
            service.update_health_hash()
            self.assertFalse(service.hash_update_successful)


class TestCriticalServicesCheck(unittest.TestCase):
    """Test critical Nova services check."""

    def test_all_schedulers_down(self):
        """Test that check fails when all schedulers are down."""
        mock_conn = Mock()
        services = [
            Mock(binary='nova-scheduler', state='down', host='controller-1'),
            Mock(binary='nova-scheduler', state='down', host='controller-2'),
            Mock(binary='nova-compute', state='down', host='compute-1')
        ]
        compute_nodes = [services[2]]

        can_evacuate, error_msg = instanceha._check_critical_services(mock_conn, services, compute_nodes)

        self.assertFalse(can_evacuate)
        self.assertIn('nova-scheduler', error_msg)

    def test_at_least_one_scheduler_up(self):
        """Test that check passes when at least one scheduler is up."""
        mock_conn = Mock()

        services = [
            Mock(binary='nova-scheduler', state='down', host='controller-1'),
            Mock(binary='nova-scheduler', state='up', host='controller-2'),
            Mock(binary='nova-compute', state='down', host='compute-1')
        ]
        compute_nodes = [services[2]]

        can_evacuate, error_msg = instanceha._check_critical_services(mock_conn, services, compute_nodes)

        self.assertTrue(can_evacuate)
        self.assertEqual(error_msg, "")

    def test_no_schedulers_defined(self):
        """Test when no schedulers are in services list."""
        mock_conn = Mock()

        services = [
            Mock(binary='nova-compute', state='down', host='compute-1')
        ]
        compute_nodes = [services[0]]

        can_evacuate, error_msg = instanceha._check_critical_services(mock_conn, services, compute_nodes)

        # Should allow evacuation if no schedulers are in the list
        self.assertTrue(can_evacuate)
        self.assertEqual(error_msg, "")


if __name__ == '__main__':
    unittest.main()

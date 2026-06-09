"""
High-priority evacuation workflow tests for InstanceHA.

Tests critical evacuation flow paths including:
- Kdump resume disable logic
- Post-evacuation recovery error paths
- Process service step failure handling
"""

import threading
import unittest
from unittest.mock import Mock, patch, MagicMock
from datetime import datetime

import conftest  # noqa: F401
from conftest import patch_pipeline
import instanceha


class TestKdumpResumeDisableLogic(unittest.TestCase):
    """Test kdump resume disable logic in process_service."""

    def setUp(self):
        """Set up test fixtures."""
        self.mock_conn = Mock()
        self.mock_service = Mock()
        self.mock_service.config = Mock()
        self.mock_service.config.get_config_value = Mock(side_effect=lambda key: {'CHECK_KDUMP': True}.get(key, False))
        self.mock_service.kdump_fenced_hosts = set()
        self.mock_service.kdump_hosts_checking = {}
        self.mock_service.processing_lock = Mock()
        self.mock_service.hosts_processing = {}

        self.mock_failed_service = Mock()
        self.mock_failed_service.host = 'test-host.example.com'
        self.mock_failed_service.id = 'svc-123'
        self.mock_failed_service.binary = 'nova-compute'

    def test_resume_with_already_disabled_service_skips_disable(self):
        """Test that resume=True with already disabled service skips _host_disable."""
        self.mock_failed_service.forced_down = True
        self.mock_failed_service.status = 'disabled'

        with patch_pipeline(conn=self.mock_conn) as mocks:
            result = instanceha.process_service(
                self.mock_failed_service, [], resume=True, service=self.mock_service)

        self.assertTrue(result)
        mocks['disable'].assert_not_called()

    def test_resume_with_kdump_fenced_calls_disable(self):
        """Test that kdump-fenced hosts with resume=True still call _host_disable."""
        self.mock_failed_service.forced_down = False
        self.mock_failed_service.status = 'enabled'
        self.mock_service.kdump_fenced_hosts.add('test-host')

        with patch_pipeline(conn=self.mock_conn) as mocks:
            result = instanceha.process_service(
                self.mock_failed_service, [], resume=True, service=self.mock_service)

        self.assertTrue(result)
        mocks['disable'].assert_called_once()

    def test_new_evacuation_calls_disable(self):
        """Test that new evacuation (resume=False) always calls _host_disable."""
        self.mock_failed_service.forced_down = False
        self.mock_failed_service.status = 'enabled'

        with patch_pipeline(conn=self.mock_conn) as mocks:
            result = instanceha.process_service(
                self.mock_failed_service, [], resume=False, service=self.mock_service)

        self.assertTrue(result)
        mocks['disable'].assert_called_once()


class TestPostEvacuationRecoveryErrors(unittest.TestCase):
    """Test post-evacuation recovery error paths."""

    def setUp(self):
        """Set up test fixtures."""
        self.mock_conn = Mock()
        self.mock_service = Mock()
        self.mock_service.config = Mock()
        self.mock_service.config.get_config_value.return_value = False
        self.mock_service.kdump_fenced_hosts = set()
        self.mock_service.kdump_hosts_checking = {}
        self.mock_service.kdump_lock = threading.Lock()

        self.mock_failed_service = Mock()
        self.mock_failed_service.host = 'test-host.example.com'
        self.mock_failed_service.id = 'svc-123'

    def test_recovery_power_on_failure(self):
        """Test recovery when power-on fails."""
        with patch('instanceha._host_fence', return_value=False) as mock_fence:
            result = instanceha._post_evacuation_recovery(
                self.mock_conn,
                self.mock_failed_service,
                self.mock_service,
                resume=False
            )

        self.assertFalse(result)
        mock_fence.assert_called_once_with(self.mock_failed_service.host, 'on', self.mock_service)

    def test_recovery_disable_reason_update_failure(self):
        """Test recovery continues when disable reason update fails."""
        self.mock_conn.services.disable_log_reason.side_effect = Exception('Update failed')

        with patch('instanceha._host_fence', return_value=True):
            result = instanceha._post_evacuation_recovery(
                self.mock_conn,
                self.mock_failed_service,
                self.mock_service,
                resume=False
            )

        self.assertTrue(result)

    def test_recovery_unexpected_exception(self):
        """Test recovery handles unexpected exceptions."""
        with patch('instanceha._host_fence', side_effect=RuntimeError('Unexpected error')):
            result = instanceha._post_evacuation_recovery(
                self.mock_conn,
                self.mock_failed_service,
                self.mock_service,
                resume=False
            )

        self.assertFalse(result)


class TestProcessServiceStepFailures(unittest.TestCase):
    """Test process_service step failure handling."""

    def setUp(self):
        """Set up test fixtures."""
        self.mock_conn = Mock()
        self.mock_service = Mock()
        self.mock_service.config = Mock()
        self.mock_service.processing_lock = Mock()
        self.mock_service.hosts_processing = {}

        self.mock_failed_service = Mock()
        self.mock_failed_service.host = 'test-host.example.com'
        self.mock_failed_service.id = 'svc-123'
        self.mock_failed_service.forced_down = False
        self.mock_failed_service.status = 'enabled'

    def test_fencing_step_failure_stops_processing(self):
        """Test that fencing failure stops processing immediately."""
        with patch_pipeline(conn=self.mock_conn, fence=False) as mocks:
            result = instanceha.process_service(
                self.mock_failed_service, [], resume=False, service=self.mock_service)

        self.assertFalse(result)
        mocks['fence'].assert_called_once()
        mocks['disable'].assert_not_called()

    def test_disable_step_failure_stops_processing(self):
        """Test that disable failure stops processing immediately."""
        with patch_pipeline(conn=self.mock_conn, disable=False) as mocks:
            result = instanceha.process_service(
                self.mock_failed_service, [], resume=False, service=self.mock_service)

        self.assertFalse(result)
        mocks['disable'].assert_called_once()
        mocks['reserved'].assert_not_called()

    def test_reserved_hosts_step_failure_stops_processing(self):
        """Test that reserved hosts failure stops processing immediately."""
        with patch_pipeline(conn=self.mock_conn,
                            reserved=instanceha.ReservedHostResult(success=False, hostname=None)) as mocks:
            result = instanceha.process_service(
                self.mock_failed_service, [], resume=False, service=self.mock_service)

        self.assertFalse(result)
        mocks['reserved'].assert_called_once()
        mocks['evacuate'].assert_not_called()

    def test_evacuation_step_failure_stops_processing(self):
        """Test that evacuation failure stops processing immediately."""
        with patch_pipeline(conn=self.mock_conn, evacuate=False) as mocks:
            result = instanceha.process_service(
                self.mock_failed_service, [], resume=False, service=self.mock_service)

        self.assertFalse(result)
        mocks['evacuate'].assert_called_once()
        mocks['recovery'].assert_not_called()

    def test_recovery_step_failure_stops_processing(self):
        """Test that recovery failure stops processing immediately."""
        with patch_pipeline(conn=self.mock_conn, recovery=False) as mocks:
            result = instanceha.process_service(
                self.mock_failed_service, [], resume=False, service=self.mock_service)

        self.assertFalse(result)

    def test_all_steps_succeed(self):
        """Test that all steps succeeding results in success."""
        with patch_pipeline(conn=self.mock_conn) as mocks:
            result = instanceha.process_service(
                self.mock_failed_service, [], resume=False, service=self.mock_service)

        self.assertTrue(result)


class TestReservedHostReturnOnPartialFailure(unittest.TestCase):
    """Test reserved host return behavior when evacuation partially fails."""

    def setUp(self):
        """Set up test fixtures."""
        self.mock_conn = Mock()
        self.mock_service = Mock()
        self.mock_service.config = Mock()
        self.mock_service.config.get_config_value = Mock(
            side_effect=lambda key: {
                'FORCE_RESERVED_HOST_EVACUATION': True,
                'RESERVED_HOSTS': True,
            }.get(key, False))
        self.mock_service.processing_lock = Mock()
        self.mock_service.hosts_processing = {}

        self.mock_failed_service = Mock()
        self.mock_failed_service.host = 'compute-a.example.com'
        self.mock_failed_service.id = 'svc-123'
        self.mock_failed_service.forced_down = False
        self.mock_failed_service.status = 'enabled'

    def test_partial_failure_keeps_reserved_host_with_vms(self):
        """Reserved host with VMs on it should NOT be returned to pool."""
        vms_on_target = [Mock(id='vm-1'), Mock(id='vm-2')]
        self.mock_conn.servers.list.return_value = vms_on_target

        with patch_pipeline(conn=self.mock_conn, evacuate=False,
                            reserved=instanceha.ReservedHostResult(success=True, hostname='compute-b')) as mocks, \
             patch('instanceha._return_reserved_host') as mock_return:
            result = instanceha.process_service(
                self.mock_failed_service, [], resume=False, service=self.mock_service)

        self.assertFalse(result)
        mock_return.assert_not_called()
        self.mock_conn.servers.list.assert_called_once_with(
            search_opts={'host': 'compute-b', 'all_tenants': 1})

    def test_total_failure_returns_empty_reserved_host(self):
        """Reserved host with no VMs should be returned to pool."""
        self.mock_conn.servers.list.return_value = []

        with patch_pipeline(conn=self.mock_conn, evacuate=False,
                            reserved=instanceha.ReservedHostResult(success=True, hostname='compute-b')) as mocks, \
             patch('instanceha._return_reserved_host') as mock_return:
            result = instanceha.process_service(
                self.mock_failed_service, [], resume=False, service=self.mock_service)

        self.assertFalse(result)
        mock_return.assert_called_once_with(self.mock_conn, 'compute-b', [], self.mock_service)

    def test_api_failure_checking_vms_keeps_reserved_host(self):
        """If VM check fails, err on safe side and keep reserved host."""
        self.mock_conn.servers.list.side_effect = Exception("Nova API error")

        with patch_pipeline(conn=self.mock_conn, evacuate=False,
                            reserved=instanceha.ReservedHostResult(success=True, hostname='compute-b')) as mocks, \
             patch('instanceha._return_reserved_host') as mock_return:
            result = instanceha.process_service(
                self.mock_failed_service, [], resume=False, service=self.mock_service)

        self.assertFalse(result)
        mock_return.assert_not_called()


if __name__ == '__main__':
    unittest.main()

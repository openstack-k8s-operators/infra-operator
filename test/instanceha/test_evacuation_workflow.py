"""
High-priority evacuation workflow tests for InstanceHA.

Tests critical evacuation flow paths including:
- Kdump resume disable logic
- Post-evacuation recovery error paths
- Process service step failure handling
"""

import os
import sys
import unittest
from unittest.mock import Mock, patch, MagicMock
from datetime import datetime

# Mock OpenStack dependencies
if 'novaclient' not in sys.modules:
    sys.modules['novaclient'] = MagicMock()
    sys.modules['novaclient.client'] = MagicMock()
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
    sys.modules['keystoneauth1.exceptions'] = MagicMock()
    sys.modules['keystoneauth1.exceptions.discovery'] = MagicMock()

# Add module path
test_dir = os.path.dirname(os.path.abspath(__file__))
instanceha_path = os.path.join(test_dir, '../../templates/instanceha/bin/')
sys.path.insert(0, os.path.abspath(instanceha_path))

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
        # Set up service as already disabled and forced down (previous evacuation attempt)
        self.mock_failed_service.forced_down = True
        self.mock_failed_service.status = 'disabled'

        with patch('instanceha._get_nova_connection', return_value=self.mock_conn):
            with patch('instanceha._host_fence', return_value=True):
                with patch('instanceha._host_disable') as mock_disable:
                    with patch('instanceha._manage_reserved_hosts', return_value=instanceha.ReservedHostResult(success=True, hostname=None)):
                        with patch('instanceha._host_evacuate', return_value=True):
                            with patch('instanceha._post_evacuation_recovery', return_value=True):
                                with patch('instanceha.track_host_processing'):
                                    result = instanceha.process_service(
                                        self.mock_failed_service,
                                        [],
                                        resume=True,
                                        service=self.mock_service
                                    )

        # Should succeed without calling _host_disable
        self.assertTrue(result)
        mock_disable.assert_not_called()

    def test_resume_with_kdump_fenced_calls_disable(self):
        """Test that kdump-fenced hosts with resume=True still call _host_disable."""
        # Kdump-fenced hosts use resume=True but are NOT yet disabled
        self.mock_failed_service.forced_down = False
        self.mock_failed_service.status = 'enabled'
        self.mock_service.kdump_fenced_hosts.add('test-host')

        with patch('instanceha._get_nova_connection', return_value=self.mock_conn):
            with patch('instanceha._host_fence', return_value=True):
                with patch('instanceha._host_disable', return_value=True) as mock_disable:
                    with patch('instanceha._manage_reserved_hosts', return_value=instanceha.ReservedHostResult(success=True, hostname=None)):
                        with patch('instanceha._host_evacuate', return_value=True):
                            with patch('instanceha._post_evacuation_recovery', return_value=True):
                                with patch('instanceha.track_host_processing'):
                                    result = instanceha.process_service(
                                        self.mock_failed_service,
                                        [],
                                        resume=True,
                                        service=self.mock_service
                                    )

        # Should succeed and call _host_disable
        self.assertTrue(result)
        mock_disable.assert_called_once()

    def test_new_evacuation_calls_disable(self):
        """Test that new evacuation (resume=False) always calls _host_disable."""
        self.mock_failed_service.forced_down = False
        self.mock_failed_service.status = 'enabled'

        with patch('instanceha._get_nova_connection', return_value=self.mock_conn):
            with patch('instanceha._host_fence', return_value=True):
                with patch('instanceha._host_disable', return_value=True) as mock_disable:
                    with patch('instanceha._manage_reserved_hosts', return_value=instanceha.ReservedHostResult(success=True, hostname=None)):
                        with patch('instanceha._host_evacuate', return_value=True):
                            with patch('instanceha._post_evacuation_recovery', return_value=True):
                                with patch('instanceha.track_host_processing'):
                                    result = instanceha.process_service(
                                        self.mock_failed_service,
                                        [],
                                        resume=False,
                                        service=self.mock_service
                                    )

        # Should succeed and call _host_disable
        self.assertTrue(result)
        mock_disable.assert_called_once()


class TestPostEvacuationRecoveryErrors(unittest.TestCase):
    """Test post-evacuation recovery error paths."""

    def setUp(self):
        """Set up test fixtures."""
        self.mock_conn = Mock()
        self.mock_service = Mock()
        self.mock_service.config = Mock()
        self.mock_service.config.is_leave_disabled_enabled = Mock(return_value=False)
        self.mock_service.kdump_fenced_hosts = set()
        self.mock_service.kdump_hosts_checking = {}

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

        # Should fail when power-on fails
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

        # Should succeed even if disable reason update fails (non-critical)
        self.assertTrue(result)

    def test_recovery_unexpected_exception(self):
        """Test recovery handles unexpected exceptions."""
        # Cause exception during power-on by making fence raise unexpected error
        with patch('instanceha._host_fence', side_effect=RuntimeError('Unexpected error')):
            result = instanceha._post_evacuation_recovery(
                self.mock_conn,
                self.mock_failed_service,
                self.mock_service,
                resume=False
            )

        # Should fail on unexpected exception
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
        with patch('instanceha._get_nova_connection', return_value=self.mock_conn):
            with patch('instanceha._host_fence', return_value=False) as mock_fence:
                with patch('instanceha._host_disable') as mock_disable:
                    with patch('instanceha.track_host_processing'):
                        result = instanceha.process_service(
                            self.mock_failed_service,
                            [],
                            resume=False,
                            service=self.mock_service
                        )

        # Should fail and not proceed to disable step
        self.assertFalse(result)
        mock_fence.assert_called_once()
        mock_disable.assert_not_called()

    def test_disable_step_failure_stops_processing(self):
        """Test that disable failure stops processing immediately."""
        with patch('instanceha._get_nova_connection', return_value=self.mock_conn):
            with patch('instanceha._host_fence', return_value=True):
                with patch('instanceha._host_disable', return_value=False) as mock_disable:
                    with patch('instanceha._manage_reserved_hosts') as mock_reserved:
                        with patch('instanceha.track_host_processing'):
                            result = instanceha.process_service(
                                self.mock_failed_service,
                                [],
                                resume=False,
                                service=self.mock_service
                            )

        # Should fail and not proceed to reserved hosts step
        self.assertFalse(result)
        mock_disable.assert_called_once()
        mock_reserved.assert_not_called()

    def test_reserved_hosts_step_failure_stops_processing(self):
        """Test that reserved hosts failure stops processing immediately."""
        with patch('instanceha._get_nova_connection', return_value=self.mock_conn):
            with patch('instanceha._host_fence', return_value=True):
                with patch('instanceha._host_disable', return_value=True):
                    with patch('instanceha._manage_reserved_hosts', return_value=(False, None)) as mock_reserved:
                        with patch('instanceha._host_evacuate') as mock_evacuate:
                            with patch('instanceha.track_host_processing'):
                                result = instanceha.process_service(
                                    self.mock_failed_service,
                                    [],
                                    resume=False,
                                    service=self.mock_service
                                )

        # Should fail and not proceed to evacuation step
        self.assertFalse(result)
        mock_reserved.assert_called_once()
        mock_evacuate.assert_not_called()

    def test_evacuation_step_failure_stops_processing(self):
        """Test that evacuation failure stops processing immediately."""
        with patch('instanceha._get_nova_connection', return_value=self.mock_conn):
            with patch('instanceha._host_fence', return_value=True):
                with patch('instanceha._host_disable', return_value=True):
                    with patch('instanceha._manage_reserved_hosts', return_value=instanceha.ReservedHostResult(success=True, hostname=None)):
                        with patch('instanceha._host_evacuate', return_value=False) as mock_evacuate:
                            with patch('instanceha._post_evacuation_recovery') as mock_recovery:
                                with patch('instanceha.track_host_processing'):
                                    result = instanceha.process_service(
                                        self.mock_failed_service,
                                        [],
                                        resume=False,
                                        service=self.mock_service
                                    )

        # Should fail and not proceed to recovery step
        self.assertFalse(result)
        mock_evacuate.assert_called_once()
        mock_recovery.assert_not_called()

    def test_recovery_step_failure_stops_processing(self):
        """Test that recovery failure stops processing immediately."""
        with patch('instanceha._get_nova_connection', return_value=self.mock_conn):
            with patch('instanceha._host_fence', return_value=True):
                with patch('instanceha._host_disable', return_value=True):
                    with patch('instanceha._manage_reserved_hosts', return_value=instanceha.ReservedHostResult(success=True, hostname=None)):
                        with patch('instanceha._host_evacuate', return_value=True):
                            with patch('instanceha._post_evacuation_recovery', return_value=False):
                                with patch('instanceha.track_host_processing'):
                                    result = instanceha.process_service(
                                        self.mock_failed_service,
                                        [],
                                        resume=False,
                                        service=self.mock_service
                                    )

        # Should fail when recovery fails
        self.assertFalse(result)

    def test_all_steps_succeed(self):
        """Test that all steps succeeding results in success."""
        with patch('instanceha._get_nova_connection', return_value=self.mock_conn):
            with patch('instanceha._host_fence', return_value=True):
                with patch('instanceha._host_disable', return_value=True):
                    with patch('instanceha._manage_reserved_hosts', return_value=instanceha.ReservedHostResult(success=True, hostname=None)):
                        with patch('instanceha._host_evacuate', return_value=True):
                            with patch('instanceha._post_evacuation_recovery', return_value=True):
                                with patch('instanceha.track_host_processing'):
                                    result = instanceha.process_service(
                                        self.mock_failed_service,
                                        [],
                                        resume=False,
                                        service=self.mock_service
                                    )

        # Should succeed when all steps succeed
        self.assertTrue(result)


if __name__ == '__main__':
    unittest.main()

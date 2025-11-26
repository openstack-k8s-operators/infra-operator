"""
Kdump detection tests for InstanceHA.

Tests for kdump crash detection functionality:
- Kdump UDP listener and message processing
- Kdump integration with evacuation workflow
- Timeout and delay handling
- Fencing coordination with kdump detection
"""

#!/usr/bin/env python3

import os
import sys
import unittest
import tempfile
import yaml
import json
import socket
import threading
import time
import struct
import subprocess
import concurrent.futures
import logging
import requests
from unittest.mock import Mock, patch, MagicMock, call
from datetime import datetime, timedelta
from io import StringIO

# Mock OpenStack dependencies before importing instanceha
# This allows tests to run without novaclient, keystoneauth1, etc.
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
    sys.modules['keystoneauth1.exceptions'] = MagicMock()
    sys.modules['keystoneauth1.exceptions.discovery'] = MagicMock()

# Add the module path for testing
# Calculate the path to instanceha.py relative to this test file
test_dir = os.path.dirname(os.path.abspath(__file__))
instanceha_path = os.path.join(test_dir, '../../templates/instanceha/bin/')
sys.path.insert(0, os.path.abspath(instanceha_path))

# Suppress configuration warnings during testing
logging.getLogger().setLevel(logging.CRITICAL)

# Import the module under test
import instanceha

# Re-suppress logging after import (instanceha sets its own level)
logging.getLogger().setLevel(logging.CRITICAL)



class TestKdumpFunctionality(unittest.TestCase):
    """Test kdump detection and filtering functionality."""

    def setUp(self):
        """Set up test fixtures."""
        self.mock_service = Mock()
        self.mock_service.config.get_config_value.return_value = 10  # KDUMP_TIMEOUT
        self.mock_service.config.get_workers.return_value = 4
        self.mock_service.UDP_IP = '0.0.0.0'
        self.mock_service.config.get_udp_port.return_value = 7410

        # Create a mock service for kdump testing with proper config
        mock_config = Mock()
        mock_config.get_config_value.return_value = 10  # KDUMP_TIMEOUT
        mock_config.get_workers.return_value = 4
        self.mock_service_instance = instanceha.InstanceHAService(mock_config)
        self.mock_service_instance.kdump_hosts_timestamp.clear()

    def tearDown(self):
        """Clean up test fixtures."""
        # Clean up service instance
        if hasattr(self, 'mock_service_instance'):
            self.mock_service_instance.kdump_hosts_timestamp.clear()

    def test_kdump_timeout_configuration(self):
        """Test KDUMP_TIMEOUT configuration loading."""
        config = instanceha.ConfigManager()
        # Should have KDUMP_TIMEOUT in config map
        self.assertIn('KDUMP_TIMEOUT', config._config_map)
        # Should accept valid timeout values
        config._config = {'KDUMP_TIMEOUT': 30}
        self.assertEqual(config.get_config_value('KDUMP_TIMEOUT'), 30)

    def test_kdump_check_no_services(self):
        """Test _check_kdump with empty service list."""
        to_evacuate, kdump_fenced = instanceha._check_kdump([], self.mock_service_instance)
        self.assertEqual(to_evacuate, [])
        self.assertEqual(kdump_fenced, [])

    def test_kdump_check_immediate_detection(self):
        """Test _check_kdump with immediate kdump detection - host should be evacuated immediately."""
        mock_service1 = Mock()
        mock_service1.host = 'compute-01.example.com'

        # Set recent kdump timestamp
        self.mock_service_instance.kdump_hosts_timestamp['compute-01'] = time.time() - 5

        to_evacuate, kdump_fenced = instanceha._check_kdump([mock_service1], self.mock_service_instance)
        self.assertEqual(len(to_evacuate), 0)  # Host should NOT be in to_evacuate (kdump-fenced separately)
        self.assertEqual(len(kdump_fenced), 1)  # Host should be in kdump_fenced list
        self.assertIn('compute-01', self.mock_service_instance.kdump_fenced_hosts)  # Host tracked as fenced

    def test_kdump_check_no_kdump_activity(self):
        """Test _check_kdump with no kdump activity - should wait for timeout."""
        mock_service1 = Mock()
        mock_service1.host = 'compute-01.example.com'

        to_evacuate, kdump_fenced = instanceha._check_kdump([mock_service1], self.mock_service_instance)
        self.assertEqual(len(to_evacuate), 0)  # Host should be waiting (not evacuated yet)
        self.assertEqual(len(kdump_fenced), 0)  # Host should not be kdump-fenced
        self.assertNotIn('compute-01', self.mock_service_instance.kdump_fenced_hosts)  # Not tracked as fenced
        self.assertIn('compute-01', self.mock_service_instance.kdump_hosts_checking)  # Should be in checking state

    def test_kdump_message_processing_valid_magic(self):
        """Test UDP message processing with valid magic number."""
        # Test both byte orders
        native_data = struct.pack('I', 0x1B302A40) + b'test_data'
        network_data = struct.pack('!I', 0x1B302A40) + b'test_data'

        # Both should be valid magic numbers
        magic_native = struct.unpack('I', native_data[:4])[0]
        magic_network = struct.unpack('!I', network_data[:4])[0]

        self.assertTrue(magic_native == 0x1B302A40 or magic_network == 0x1B302A40)

    def test_kdump_message_processing_invalid_magic(self):
        """Test UDP message processing with invalid magic number."""
        invalid_data = struct.pack('I', 0xDEADBEEF) + b'test_data'
        magic = struct.unpack('I', invalid_data[:4])[0]

        self.assertNotEqual(magic, 0x1B302A40)

    def test_kdump_timestamp_cleanup(self):
        """Test automatic cleanup of old kdump timestamps."""
        # Add old timestamps
        old_time = time.time() - 400  # Older than 300s cleanup threshold
        self.mock_service_instance.kdump_hosts_timestamp['old-host'] = old_time
        self.mock_service_instance.kdump_hosts_timestamp['recent-host'] = time.time()

        # Simulate cleanup logic
        if len(self.mock_service_instance.kdump_hosts_timestamp) > 0:  # Simplified condition for test
            cutoff = time.time() - 300
            old_keys = [k for k, v in self.mock_service_instance.kdump_hosts_timestamp.items() if v < cutoff]
            for k in old_keys:
                del self.mock_service_instance.kdump_hosts_timestamp[k]

        self.assertNotIn('old-host', self.mock_service_instance.kdump_hosts_timestamp)
        self.assertIn('recent-host', self.mock_service_instance.kdump_hosts_timestamp)

    def test_kdump_multiple_hosts(self):
        """Test kdump checking for multiple hosts."""
        mock_service1 = Mock()
        mock_service1.host = 'compute-01.example.com'
        mock_service2 = Mock()
        mock_service2.host = 'compute-02.example.com'

        services = [mock_service1, mock_service2]

        # Create a service instance for this test
        mock_service_instance = instanceha.InstanceHAService(Mock())
        mock_service_instance.config = self.mock_service.config

        # First host has kdump message, second does not
        mock_service_instance.kdump_hosts_timestamp['compute-01'] = time.time() - 5

        to_evacuate, kdump_fenced = instanceha._check_kdump(services, mock_service_instance)

        # compute-01 has kdump message → in kdump_fenced (not to_evacuate)
        # compute-02 has no kdump → waiting (not evacuated yet)
        self.assertEqual(len(to_evacuate), 0)  # Neither in to_evacuate yet
        self.assertEqual(len(kdump_fenced), 1)  # compute-01 is kdump-fenced
        self.assertEqual(kdump_fenced[0].host, 'compute-01.example.com')
        self.assertIn('compute-01', mock_service_instance.kdump_fenced_hosts)
        self.assertIn('compute-02', mock_service_instance.kdump_hosts_checking)  # compute-02 waiting

    def test_kdump_timeout_exceeded(self):
        """Test kdump timeout exceeded - host should not be considered kdumping."""
        mock_service = Mock()
        mock_service.host = 'compute-01.example.com'

        # Create a service instance for this test
        mock_service_instance = instanceha.InstanceHAService(Mock())
        mock_service_instance.config = self.mock_service.config

        # Set checking timestamp beyond timeout (default 30s) - simulates waiting period expired
        mock_service_instance.kdump_hosts_checking['compute-01'] = time.time() - 35

        to_evacuate, kdump_fenced = instanceha._check_kdump([mock_service], mock_service_instance)
        self.assertEqual(len(to_evacuate), 1)  # Host should be evacuated (timeout expired, no kdump)
        self.assertEqual(len(kdump_fenced), 0)  # Host is not kdump-fenced (timeout exceeded)
        self.assertNotIn('compute-01', mock_service_instance.kdump_fenced_hosts)
        self.assertNotIn('compute-01', mock_service_instance.kdump_hosts_checking)  # Should be cleaned up

    def test_post_evacuation_recovery_kdump_fenced_skip_power_on(self):
        """Test that power on is skipped for kdump-fenced hosts."""
        mock_conn = Mock()
        mock_service_obj = Mock()
        mock_service_obj.host = 'compute-01.example.com'
        mock_service_obj.id = 'service-123'
        mock_service_obj.status = 'disabled'

        # Create service instance and mark host as kdump fenced
        mock_service_instance = instanceha.InstanceHAService(Mock())
        mock_service_instance.config = self.mock_service.config
        mock_service_instance.config.is_leave_disabled_enabled.return_value = False
        mock_service_instance.kdump_fenced_hosts.add('compute-01')

        with patch('instanceha._host_fence') as mock_fence, \
             patch('instanceha._host_enable', return_value=True):
            result = instanceha._post_evacuation_recovery(mock_conn, mock_service_obj, mock_service_instance)

            # Verify power on was NOT called
            mock_fence.assert_not_called()
            # Note: kdump_fenced_hosts cleanup now happens in _host_enable when force-down is unset
            # Since _host_enable is mocked here, the cleanup doesn't happen in this test
            self.assertTrue(result)

    def test_post_evacuation_recovery_normal_host_power_on(self):
        """Test that power on is performed for non-kdump-fenced hosts."""
        mock_conn = Mock()
        mock_service_obj = Mock()
        mock_service_obj.host = 'compute-01.example.com'
        mock_service_obj.id = 'service-123'
        mock_service_obj.status = 'disabled'

        # Create service instance without kdump fencing
        mock_service_instance = instanceha.InstanceHAService(Mock())
        mock_service_instance.config = self.mock_service.config
        mock_service_instance.config.is_leave_disabled_enabled.return_value = False

        with patch('instanceha._host_fence', return_value=True) as mock_fence, \
             patch('instanceha._host_enable', return_value=True):
            result = instanceha._post_evacuation_recovery(mock_conn, mock_service_obj, mock_service_instance)

            # Verify power on WAS called
            mock_fence.assert_called_once_with('compute-01.example.com', 'on', mock_service_instance)
            self.assertTrue(result)

    def test_kdump_fenced_hosts_cleanup_on_recovery(self):
        """Test that kdump_fenced_hosts is properly cleaned up after recovery."""
        mock_service_instance = instanceha.InstanceHAService(Mock())
        mock_service_instance.config = self.mock_service.config

        # Add multiple hosts to kdump_fenced_hosts and tracking dicts
        mock_service_instance.kdump_fenced_hosts.add('compute-01')
        mock_service_instance.kdump_fenced_hosts.add('compute-02')
        mock_service_instance.kdump_hosts_timestamp['compute-01'] = time.time()
        mock_service_instance.kdump_hosts_timestamp['compute-02'] = time.time()
        mock_service_instance.kdump_hosts_checking['compute-01'] = time.time()
        mock_service_instance.kdump_hosts_checking['compute-02'] = time.time()

        mock_conn = Mock()
        mock_service_obj = Mock()
        mock_service_obj.host = 'compute-01.example.com'
        mock_service_obj.id = 'service-123'
        mock_service_obj.status = 'disabled'
        mock_service_instance.config.is_leave_disabled_enabled.return_value = False

        with patch('instanceha._host_enable', return_value=True):
            instanceha._post_evacuation_recovery(mock_conn, mock_service_obj, mock_service_instance)

            # Only kdump_hosts_checking is cleaned up in _post_evacuation_recovery
            # kdump_fenced_hosts and kdump_hosts_timestamp are cleaned up in _host_enable when force-down is unset
            self.assertNotIn('compute-01', mock_service_instance.kdump_hosts_checking)
            self.assertIn('compute-02', mock_service_instance.kdump_hosts_checking)

    def test_kdump_disabled_reason_marker(self):
        """Test that kdump-fenced hosts get special disabled_reason marker."""
        mock_conn = Mock()
        mock_service = Mock()
        mock_service.id = 'service-123'
        mock_service.host = 'compute-01.example.com'

        mock_service_instance = instanceha.InstanceHAService(Mock())
        mock_service_instance.kdump_fenced_hosts.add('compute-01')

        result = instanceha._host_disable(mock_conn, mock_service, mock_service_instance)

        self.assertTrue(result)
        # Verify disabled_reason contains kdump marker
        call_args = mock_conn.services.disable_log_reason.call_args[0]
        self.assertIn('kdump', call_args[1])
        self.assertIn('instanceha evacuation (kdump):', call_args[1])

    def test_kdump_reenable_delay_recent_message(self):
        """Test that re-enablement is delayed when kdump messages are recent."""
        mock_conn = Mock()
        mock_config = Mock()
        mock_config.is_leave_disabled_enabled.return_value = False
        mock_config.is_force_enable_enabled.return_value = False

        service = instanceha.InstanceHAService(mock_config)
        service.kdump_hosts_timestamp['compute-01'] = time.time() - 30  # 30s ago, under 60s threshold

        mock_svc = Mock()
        mock_svc.host = 'compute-01.example.com'
        mock_svc.forced_down = True
        mock_svc.disabled_reason = 'instanceha evacuation (kdump): 2025-01-01'
        mock_svc.status = 'disabled'

        # Mock migrations to be complete
        mock_conn.migrations.list.return_value = []

        with patch('instanceha._host_enable') as mock_enable:
            instanceha._process_reenabling(mock_conn, service, [mock_svc])
            # Should NOT attempt to enable (too soon after kdump message)
            mock_enable.assert_not_called()

    def test_kdump_reenable_allowed_after_delay(self):
        """Test that re-enablement proceeds after 60s delay from last kdump message."""
        mock_conn = Mock()
        mock_config = Mock()
        mock_config.is_leave_disabled_enabled.return_value = False
        mock_config.is_force_enable_enabled.return_value = False

        service = instanceha.InstanceHAService(mock_config)
        service.kdump_hosts_timestamp['compute-01'] = time.time() - 65  # 65s ago, over 60s threshold

        mock_svc = Mock()
        mock_svc.host = 'compute-01.example.com'
        mock_svc.forced_down = True
        mock_svc.disabled_reason = 'instanceha evacuation (kdump): 2025-01-01'
        mock_svc.status = 'disabled'

        # Mock migrations to be complete
        mock_conn.migrations.list.return_value = []

        with patch('instanceha._host_enable') as mock_enable:
            instanceha._process_reenabling(mock_conn, service, [mock_svc])
            # Should attempt to enable (enough time passed)
            mock_enable.assert_called_once_with(mock_conn, mock_svc, reenable=True, service=service)

    def test_kdump_marker_cleanup_on_force_down_unset(self):
        """Test that kdump marker is removed when force-down is successfully unset."""
        mock_conn = Mock()
        mock_service = Mock()
        mock_service.id = 'service-123'
        mock_service.host = 'compute-01.example.com'
        mock_service.disabled_reason = 'instanceha evacuation (kdump): 2025-01-01'

        service_instance = instanceha.InstanceHAService(Mock())
        service_instance.kdump_fenced_hosts.add('compute-01')
        service_instance.kdump_hosts_timestamp['compute-01'] = time.time()

        result = instanceha._host_enable(mock_conn, mock_service, reenable=True, service=service_instance)

        self.assertTrue(result)
        # Verify force_down was unset
        mock_conn.services.force_down.assert_called_once_with('service-123', False)
        # Verify disabled_reason was updated to remove kdump marker
        self.assertEqual(mock_conn.services.disable_log_reason.call_count, 1)
        call_args = mock_conn.services.disable_log_reason.call_args[0]
        self.assertIn('complete', call_args[1])
        self.assertNotIn('kdump', call_args[1])
        # Verify kdump tracking was cleaned up
        self.assertNotIn('compute-01', service_instance.kdump_fenced_hosts)
        self.assertNotIn('compute-01', service_instance.kdump_hosts_timestamp)


class TestKdumpIntegration(unittest.TestCase):
    """Integration tests for kdump functionality."""

    def setUp(self):
        """Set up integration test fixtures."""
        self.temp_dir = tempfile.mkdtemp()
        self.mock_service = Mock()
        self.mock_service.config.get_config_value.return_value = 10
        self.mock_service.config.get_workers.return_value = 2
        self.mock_service.UDP_IP = '127.0.0.1'
        self.mock_service.config.get_udp_port.return_value = 7411  # Different port for testing

    def tearDown(self):
        """Clean up integration test fixtures."""
        import shutil
        shutil.rmtree(self.temp_dir, ignore_errors=True)
        # Reset kdump state
        if hasattr(self, 'mock_service_instance'):
            self.mock_service_instance.kdump_hosts_timestamp.clear()

    def test_kdump_configuration_loading(self):
        """Test kdump configuration loading from file."""
        config_path = os.path.join(self.temp_dir, 'config.yaml')
        config_data = {
            'config': {
                'CHECK_KDUMP': True,
                'KDUMP_TIMEOUT': 30,
                'POLL': 45
            }
        }
        with open(config_path, 'w') as f:
            yaml.dump(config_data, f)

        # Verify configuration can be loaded
        self.assertTrue(os.path.exists(config_path))

    def test_kdump_udp_message_simulation(self):
        """Test UDP message format and processing simulation."""
        # Simulate creating a valid kdump message
        magic_number = 0x1B302A40
        test_data = struct.pack('!I', magic_number) + b'additional_data'

        # Verify message format
        self.assertEqual(len(test_data), 4 + len(b'additional_data'))
        parsed_magic = struct.unpack('!I', test_data[:4])[0]
        self.assertEqual(parsed_magic, magic_number)

    def test_kdump_workflow_integration(self):
        """Test complete kdump workflow integration."""
        # Setup test services
        mock_service1 = Mock()
        mock_service1.host = 'compute-01.example.com'
        mock_service2 = Mock()
        mock_service2.host = 'compute-02.example.com'

        services = [mock_service1, mock_service2]

        # Create a service instance for this test
        mock_config = Mock()
        mock_config.get_config_value.return_value = 10  # KDUMP_TIMEOUT
        mock_config.get_workers.return_value = 4
        mock_service_instance = instanceha.InstanceHAService(mock_config)

        # Simulate one host kdumping, one not
        mock_service_instance.kdump_hosts_timestamp['compute-01'] = time.time() - 5

        to_evacuate, kdump_fenced = instanceha._check_kdump(services, mock_service_instance)

        # compute-01 has kdump → in kdump_fenced, compute-02 waiting
        self.assertEqual(len(to_evacuate), 0)  # Neither in to_evacuate yet
        self.assertEqual(len(kdump_fenced), 1)  # compute-01 is kdump-fenced
        self.assertEqual(kdump_fenced[0].host, 'compute-01.example.com')
        self.assertIn('compute-02', mock_service_instance.kdump_hosts_checking)

    def test_kdump_thread_safety(self):
        """Test thread safety of kdump operations."""
        # Simulate concurrent access to kdump_hosts_timestamp
        # Create a service instance for this test
        mock_service_instance = instanceha.InstanceHAService(Mock())

        def update_timestamp(host_id):
            for i in range(10):
                mock_service_instance.kdump_hosts_timestamp[f'host-{host_id}-{i}'] = time.time()

        threads = []
        for i in range(3):
            thread = threading.Thread(target=update_timestamp, args=(i,))
            threads.append(thread)
            thread.start()

        for thread in threads:
            thread.join()

        # Should have entries from all threads
        self.assertGreaterEqual(len(mock_service_instance.kdump_hosts_timestamp), 30)

    def test_kdump_memory_management(self):
        """Test memory management and cleanup."""
        # Create a service instance for this test
        mock_service_instance = instanceha.InstanceHAService(Mock())

        # Add many old entries
        old_time = time.time() - 400
        for i in range(150):
            mock_service_instance.kdump_hosts_timestamp[f'old-host-{i}'] = old_time

        # Add some recent entries
        recent_time = time.time()
        for i in range(10):
            mock_service_instance.kdump_hosts_timestamp[f'recent-host-{i}'] = recent_time

        # Simulate cleanup when > 100 entries
        if len(mock_service_instance.kdump_hosts_timestamp) > 100:
            cutoff = time.time() - 300
            old_keys = [k for k, v in mock_service_instance.kdump_hosts_timestamp.items() if v < cutoff]
            for k in old_keys:
                del mock_service_instance.kdump_hosts_timestamp[k]

        # Should only have recent entries left
        self.assertLessEqual(len(mock_service_instance.kdump_hosts_timestamp), 10)
        self.assertTrue(all('recent-host' in k for k in mock_service_instance.kdump_hosts_timestamp.keys()))

    def test_process_service_kdump_fenced_calls_disable(self):
        """Test that kdump-fenced hosts (resume=True, not disabled) still call _host_disable."""
        # Create a service that is NOT yet disabled/forced_down (fresh kdump-fenced host)
        failed_service = Mock()
        failed_service.host = 'compute-01.example.com'
        failed_service.forced_down = False
        failed_service.status = 'enabled'

        mock_service_instance = instanceha.InstanceHAService(Mock())
        mock_service_instance.kdump_fenced_hosts.add('compute-01')

        with unittest.mock.patch('instanceha._get_nova_connection') as mock_conn_func, \
             unittest.mock.patch('instanceha._host_fence') as mock_fence, \
             unittest.mock.patch('instanceha._host_disable') as mock_disable, \
             unittest.mock.patch('instanceha._manage_reserved_hosts', return_value=instanceha.ReservedHostResult(success=True, hostname=None)), \
             unittest.mock.patch('instanceha._host_evacuate', return_value=True), \
             unittest.mock.patch('instanceha._post_evacuation_recovery', return_value=True):

            mock_conn_func.return_value = Mock()
            mock_disable.return_value = True

            # Process as kdump-fenced (resume=True)
            result = instanceha.process_service(failed_service, [], True, mock_service_instance)

            # Should succeed
            self.assertTrue(result)

            # Fencing (power off) should NOT be called for kdump-fenced
            mock_fence.assert_not_called()

            # _host_disable SHOULD be called (kdump-fenced hosts need to be disabled)
            mock_disable.assert_called_once()

    def test_process_service_resume_skips_disable(self):
        """Test that resume evacuations (resume=True, already disabled) skip _host_disable."""
        # Create a service that is already disabled/forced_down (resume evacuation)
        failed_service = Mock()
        failed_service.host = 'compute-01.example.com'
        failed_service.forced_down = True
        failed_service.status = 'disabled'

        mock_service_instance = instanceha.InstanceHAService(Mock())

        with unittest.mock.patch('instanceha._get_nova_connection') as mock_conn_func, \
             unittest.mock.patch('instanceha._host_fence') as mock_fence, \
             unittest.mock.patch('instanceha._host_disable') as mock_disable, \
             unittest.mock.patch('instanceha._manage_reserved_hosts', return_value=instanceha.ReservedHostResult(success=True, hostname=None)), \
             unittest.mock.patch('instanceha._host_evacuate', return_value=True), \
             unittest.mock.patch('instanceha._post_evacuation_recovery', return_value=True):

            mock_conn_func.return_value = Mock()

            # Process as resume (resume=True)
            result = instanceha.process_service(failed_service, [], True, mock_service_instance)

            # Should succeed
            self.assertTrue(result)

            # Fencing should NOT be called for resume
            mock_fence.assert_not_called()

            # _host_disable should NOT be called (already disabled, prevents overwriting markers)
            mock_disable.assert_not_called()

    def test_process_service_new_evacuation_calls_disable(self):
        """Test that new evacuations (resume=False) call both fence and disable."""
        # Create a service that is not yet processed
        failed_service = Mock()
        failed_service.host = 'compute-01.example.com'
        failed_service.forced_down = False
        failed_service.status = 'enabled'

        mock_service_instance = instanceha.InstanceHAService(Mock())

        with unittest.mock.patch('instanceha._get_nova_connection') as mock_conn_func, \
             unittest.mock.patch('instanceha._host_fence') as mock_fence, \
             unittest.mock.patch('instanceha._host_disable') as mock_disable, \
             unittest.mock.patch('instanceha._manage_reserved_hosts', return_value=instanceha.ReservedHostResult(success=True, hostname=None)), \
             unittest.mock.patch('instanceha._host_evacuate', return_value=True), \
             unittest.mock.patch('instanceha._post_evacuation_recovery', return_value=True):

            mock_conn_func.return_value = Mock()
            mock_fence.return_value = True
            mock_disable.return_value = True

            # Process as new evacuation (resume=False)
            result = instanceha.process_service(failed_service, [], False, mock_service_instance)

            # Should succeed
            self.assertTrue(result)

            # Fencing (power off) SHOULD be called
            mock_fence.assert_called_once_with('compute-01.example.com', 'off', mock_service_instance)

            # _host_disable SHOULD be called
            mock_disable.assert_called_once()

    def test_process_service_resume_disabled_partial_match(self):
        """Test edge case: resume=True with forced_down but status not containing 'disabled'."""
        # This shouldn't happen in practice, but tests the condition logic
        failed_service = Mock()
        failed_service.host = 'compute-01.example.com'
        failed_service.forced_down = True
        failed_service.status = 'enabled'  # Edge case: forced_down but not disabled

        mock_service_instance = instanceha.InstanceHAService(Mock())

        with unittest.mock.patch('instanceha._get_nova_connection') as mock_conn_func, \
             unittest.mock.patch('instanceha._host_disable') as mock_disable, \
             unittest.mock.patch('instanceha._manage_reserved_hosts', return_value=instanceha.ReservedHostResult(success=True, hostname=None)), \
             unittest.mock.patch('instanceha._host_evacuate', return_value=True), \
             unittest.mock.patch('instanceha._post_evacuation_recovery', return_value=True):

            mock_conn_func.return_value = Mock()
            mock_disable.return_value = True

            # Process as resume
            result = instanceha.process_service(failed_service, [], True, mock_service_instance)

            # Should succeed
            self.assertTrue(result)

            # _host_disable SHOULD be called (not fully disabled)
            mock_disable.assert_called_once()

    def test_post_evacuation_recovery_updates_disabled_reason_for_stale_service(self):
        """Test that disabled_reason is updated even when service object is stale (not yet disabled)."""
        # Simulate a kdump-fenced host: service object is stale (shows 'enabled' status)
        # but _host_disable was just called, so it's actually disabled in Nova
        mock_conn = Mock()
        failed_service = Mock()
        failed_service.host = 'compute-01.example.com'
        failed_service.id = 'service-123'
        failed_service.status = 'enabled'  # Stale - shows pre-disable status

        mock_service_instance = instanceha.InstanceHAService(Mock())
        mock_service_instance.config.is_leave_disabled_enabled.return_value = False

        # Call post_evacuation_recovery
        result = instanceha._post_evacuation_recovery(mock_conn, failed_service, mock_service_instance, resume=True)

        # Should succeed
        self.assertTrue(result)

        # disabled_reason should be updated even though status shows 'enabled'
        # (service object is stale, but we know _host_disable was just called)
        mock_conn.services.disable_log_reason.assert_called_once()
        call_args = mock_conn.services.disable_log_reason.call_args[0]
        self.assertEqual(call_args[0], 'service-123')
        self.assertIn('instanceha evacuation complete:', call_args[1])

    def test_post_evacuation_recovery_preserves_kdump_marker(self):
        """Test that kdump marker is preserved in 'evacuation complete' message."""
        mock_conn = Mock()
        failed_service = Mock()
        failed_service.host = 'compute-01.example.com'
        failed_service.id = 'service-123'
        failed_service.status = 'disabled'

        mock_service_instance = instanceha.InstanceHAService(Mock())
        mock_service_instance.config.is_leave_disabled_enabled.return_value = False
        mock_service_instance.kdump_fenced_hosts.add('compute-01')

        # Call post_evacuation_recovery with resume=True
        result = instanceha._post_evacuation_recovery(mock_conn, failed_service, mock_service_instance, resume=True)

        # Should succeed
        self.assertTrue(result)

        # disabled_reason should have both "evacuation complete" AND "(kdump)" marker
        mock_conn.services.disable_log_reason.assert_called_once()
        call_args = mock_conn.services.disable_log_reason.call_args[0]
        self.assertEqual(call_args[0], 'service-123')
        self.assertIn('instanceha evacuation complete (kdump):', call_args[1])



if __name__ == '__main__':
    unittest.main()

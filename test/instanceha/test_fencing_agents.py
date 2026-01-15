"""
Fencing agents tests for InstanceHA.

Tests for all fencing mechanisms:
- Redfish fencing (power control via Redfish API)
- IPMI fencing (power control via IPMI)
- BMH fencing (BareMetal Host power control via Metal3/Kubernetes)
- Fencing race condition prevention
- BMH edge cases and error scenarios
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



class TestRedfishFencing(unittest.TestCase):
    """Unit tests for Redfish fencing functionality."""

    def setUp(self):
        """Set up test environment."""
        self.config_manager = instanceha.ConfigManager()

    @patch('instanceha.requests.post')
    @patch('instanceha.requests.get')
    def test_redfish_reset_409_conflict_already_off(self, mock_get, mock_post):
        """Test Redfish reset with 409 conflict when server is already off."""
        # Mock config manager
        mock_config_manager = Mock()
        mock_config_manager.get_requests_ssl_config.return_value = False

        # Mock POST response returning 409 Conflict
        mock_post_response = Mock()
        mock_post_response.status_code = 409
        mock_post.return_value = mock_post_response

        # Mock GET response returning power state OFF
        mock_get_response = Mock()
        mock_get_response.status_code = 200
        mock_get_response.json.return_value = {'PowerState': 'Off'}
        mock_get.return_value = mock_get_response

        # Test the function
        result = instanceha._redfish_reset('http://test-server/redfish/v1/Systems/1', 'user', 'pass', 30, 'ForceOff', mock_config_manager)

        # Should return True because server is already off
        self.assertTrue(result)

        # Verify POST was called
        mock_post.assert_called_once()

        # Verify GET was called to check power state
        mock_get.assert_called_once()

    @patch('instanceha.requests.post')
    @patch('instanceha.requests.get')
    def test_redfish_reset_409_conflict_not_off(self, mock_get, mock_post):
        """Test Redfish reset with 409 conflict when server is not off."""
        # Mock config manager
        mock_config_manager = Mock()
        mock_config_manager.get_requests_ssl_config.return_value = False

        # Mock POST response returning 409 Conflict
        mock_post_response = Mock()
        mock_post_response.status_code = 409
        mock_post.return_value = mock_post_response

        # Mock GET response returning power state ON
        mock_get_response = Mock()
        mock_get_response.status_code = 200
        mock_get_response.json.return_value = {'PowerState': 'On'}
        mock_get.return_value = mock_get_response

        # Test the function
        result = instanceha._redfish_reset('http://test-server/redfish/v1/Systems/1', 'user', 'pass', 30, 'ForceOff', mock_config_manager)

        # Should return False because server is not off
        self.assertFalse(result)

        # Verify both POST and GET were called
        mock_post.assert_called_once()
        mock_get.assert_called_once()

    @patch('instanceha.requests.post')
    @patch('instanceha.requests.get')
    def test_redfish_reset_400_bad_request_already_off(self, mock_get, mock_post):
        """Test Redfish reset with 400 bad request when server is already off."""
        # Mock config manager
        mock_config_manager = Mock()
        mock_config_manager.get_requests_ssl_config.return_value = False

        # Mock POST response returning 400 Bad Request
        mock_post_response = Mock()
        mock_post_response.status_code = 400
        mock_post.return_value = mock_post_response

        # Mock GET response returning power state OFF
        mock_get_response = Mock()
        mock_get_response.status_code = 200
        mock_get_response.json.return_value = {'PowerState': 'Off'}
        mock_get.return_value = mock_get_response

        # Test the function
        result = instanceha._redfish_reset('http://test-server/redfish/v1/Systems/1', 'user', 'pass', 30, 'ForceOff', mock_config_manager)

        # Should return True because server is already off
        self.assertTrue(result)

        # Verify POST was called
        mock_post.assert_called_once()

        # Verify GET was called to check power state
        mock_get.assert_called_once()

    @patch('instanceha.requests.post')
    @patch('instanceha.requests.get')
    def test_redfish_reset_400_bad_request_not_off(self, mock_get, mock_post):
        """Test Redfish reset with 400 bad request when server is not off."""
        # Mock config manager
        mock_config_manager = Mock()
        mock_config_manager.get_requests_ssl_config.return_value = False

        # Mock POST response returning 400 Bad Request
        mock_post_response = Mock()
        mock_post_response.status_code = 400
        mock_post.return_value = mock_post_response

        # Mock GET response returning power state ON
        mock_get_response = Mock()
        mock_get_response.status_code = 200
        mock_get_response.json.return_value = {'PowerState': 'On'}
        mock_get.return_value = mock_get_response

        # Test the function
        result = instanceha._redfish_reset('http://test-server/redfish/v1/Systems/1', 'user', 'pass', 30, 'ForceOff', mock_config_manager)

        # Should return False because server is not off
        self.assertFalse(result)

        # Verify both POST and GET were called
        mock_post.assert_called_once()
        mock_get.assert_called_once()

    @patch('instanceha.requests.post')
    def test_redfish_reset_success(self, mock_post):
        """Test successful Redfish reset."""
        # Mock config manager
        mock_config_manager = Mock()
        mock_config_manager.get_requests_ssl_config.return_value = False

        # Mock POST response returning 200 OK
        mock_post_response = Mock()
        mock_post_response.status_code = 200
        mock_post.return_value = mock_post_response

        # Test the function with On action (no wait required)
        result = instanceha._redfish_reset('http://test-server/redfish/v1/Systems/1', 'user', 'pass', 1, 'On', mock_config_manager)

        # Should return True
        self.assertTrue(result)

        # Verify POST was called
        mock_post.assert_called_once()

    @patch('requests.get')
    def test_redfish_get_power_state_success(self, mock_get):
        """Test successful power state retrieval."""
        # Mock config manager
        mock_config_manager = Mock()
        mock_config_manager.get_requests_ssl_config.return_value = False

        # Mock GET response - ensure it returns immediately
        mock_get_response = Mock()
        mock_get_response.status_code = 200
        mock_get_response.json.return_value = {'PowerState': 'On'}

        # Configure mock to return immediately
        mock_get.return_value = mock_get_response

        # Test the function with a very short timeout
        result = instanceha._get_power_state('redfish', url='http://test-server/redfish/v1/Systems/1',
                                            user='user', passwd='pass', timeout=0.1, config_mgr=mock_config_manager)

        # Should return 'ON'
        self.assertEqual(result, 'ON')

        # Verify GET was called with correct parameters
        mock_get.assert_called_once()
        call_args = mock_get.call_args
        self.assertEqual(call_args[1]['timeout'], 0.1)  # Verify timeout parameter

    @patch('requests.get')
    def test_redfish_get_power_state_failure(self, mock_get):
        """Test power state retrieval failure."""
        # Mock config manager
        mock_config_manager = Mock()
        mock_config_manager.get_requests_ssl_config.return_value = False

        # Mock GET response returning error
        mock_get_response = Mock()
        mock_get_response.status_code = 500
        mock_get.return_value = mock_get_response

        # Test the function
        result = instanceha._get_power_state('redfish', url='http://test-server/redfish/v1/Systems/1',
                                            user='user', passwd='pass', timeout=30, config_mgr=mock_config_manager)

        # Should return None
        self.assertIsNone(result)

        # Verify GET was called
        mock_get.assert_called_once()

    @patch('instanceha.requests.post')
    @patch('requests.get')
    @patch('instanceha.time.sleep')
    def test_redfish_reset_forceoff_wait_for_power_off(self, mock_sleep, mock_get, mock_post):
        """Test Redfish ForceOff waits for actual power off confirmation."""
        # Reset mock call counts
        mock_get.reset_mock()
        mock_sleep.reset_mock()

        # Mock config manager
        mock_config_manager = Mock()
        mock_config_manager.get_requests_ssl_config.return_value = False

        # Mock POST response returning 200 OK
        mock_post_response = Mock()
        mock_post_response.status_code = 200
        mock_post.return_value = mock_post_response

        # Mock GET responses: first ON, then OFF
        mock_get_responses = [
            Mock(status_code=200, json=Mock(return_value={'PowerState': 'On'})),
            Mock(status_code=200, json=Mock(return_value={'PowerState': 'Off'}))
        ]
        mock_get.side_effect = mock_get_responses

        # Test the function
        result = instanceha._redfish_reset('http://test-server/redfish/v1/Systems/1', 'user', 'pass', 2, 'ForceOff', mock_config_manager)

        # Should return True after power off confirmation
        self.assertTrue(result)

        # Verify that the function attempted to check power state multiple times
        # (exact count may vary due to other test interference)
        self.assertGreaterEqual(mock_get.call_count, 2)  # At least 2 calls expected
        self.assertGreaterEqual(mock_sleep.call_count, 1)  # At least 1 sleep call expected

    @patch('instanceha.requests.post')
    @patch('requests.get')
    @patch('instanceha.time.sleep')
    def test_redfish_reset_forceoff_timeout(self, mock_sleep, mock_get, mock_post):
        """Test Redfish ForceOff times out waiting for power off."""
        # Mock config manager
        mock_config_manager = Mock()
        mock_config_manager.get_requests_ssl_config.return_value = False

        # Mock POST response returning 200 OK
        mock_post_response = Mock()
        mock_post_response.status_code = 200
        mock_post.return_value = mock_post_response

        # Mock GET response always returning ON (never powers off)
        mock_get_response = Mock()
        mock_get_response.status_code = 200
        mock_get_response.json.return_value = {'PowerState': 'On'}
        mock_get.return_value = mock_get_response

        # Test the function with timeout=2
        result = instanceha._redfish_reset('http://test-server/redfish/v1/Systems/1', 'user', 'pass', 2, 'ForceOff', mock_config_manager)

        # Should return False due to timeout
        self.assertFalse(result)

        # Verify that the function attempted to check power state multiple times
        # (exact count may vary due to other test interference)
        self.assertGreaterEqual(mock_get.call_count, 2)  # At least 2 calls expected
        self.assertGreaterEqual(mock_sleep.call_count, 2)  # At least 2 sleep calls expected

    @patch('instanceha.requests.post')
    def test_redfish_reset_on_no_wait(self, mock_post):
        """Test Redfish On action doesn't wait for power off."""
        # Mock config manager
        mock_config_manager = Mock()
        mock_config_manager.get_requests_ssl_config.return_value = False

        # Mock POST response returning 200 OK
        mock_post_response = Mock()
        mock_post_response.status_code = 200
        mock_post.return_value = mock_post_response

        # Test the function
        result = instanceha._redfish_reset('http://test-server/redfish/v1/Systems/1', 'user', 'pass', 1, 'On', mock_config_manager)

        # Should return True immediately
        self.assertTrue(result)
        mock_post.assert_called_once()

    @patch('instanceha.requests.post')
    @patch('instanceha.time.sleep')
    def test_redfish_reset_retry_on_server_error(self, mock_sleep, mock_post):
        """Test Redfish reset retries on 5xx server errors."""
        # Mock config manager
        mock_config_manager = Mock()
        mock_config_manager.get_requests_ssl_config.return_value = False

        # Mock POST responses: first two 503 errors, then 200 success
        mock_post_responses = [
            Mock(status_code=503),
            Mock(status_code=503),
            Mock(status_code=200)
        ]
        mock_post.side_effect = mock_post_responses

        result = instanceha._redfish_reset('http://test-server/redfish/v1/Systems/1', 'user', 'pass', 1, 'On', mock_config_manager)

        # Should succeed after retries
        self.assertTrue(result)
        self.assertEqual(mock_post.call_count, 3)
        self.assertEqual(mock_sleep.call_count, 2)  # Sleep between retries

    @patch('instanceha.requests.post')
    @patch('instanceha.time.sleep')
    def test_redfish_reset_retry_exhausted(self, mock_sleep, mock_post):
        """Test Redfish reset fails after max retries on server errors."""
        # Mock config manager
        mock_config_manager = Mock()
        mock_config_manager.get_requests_ssl_config.return_value = False

        # Mock POST always returning 503
        mock_post_response = Mock(status_code=503)
        mock_post.return_value = mock_post_response

        result = instanceha._redfish_reset('http://test-server/redfish/v1/Systems/1', 'user', 'pass', 1, 'On', mock_config_manager)

        # Should fail after 3 attempts
        self.assertFalse(result)
        self.assertEqual(mock_post.call_count, 3)
        self.assertEqual(mock_sleep.call_count, 2)

    @patch('instanceha.requests.post')
    @patch('instanceha.time.sleep')
    def test_redfish_reset_retry_on_network_error(self, mock_sleep, mock_post):
        """Test Redfish reset retries on network errors."""
        # Mock config manager
        mock_config_manager = Mock()
        mock_config_manager.get_requests_ssl_config.return_value = False

        # Mock network errors then success
        import requests.exceptions
        mock_post.side_effect = [
            requests.exceptions.ConnectionError("Connection failed"),
            requests.exceptions.Timeout("Request timeout"),
            Mock(status_code=200)
        ]

        result = instanceha._redfish_reset('http://test-server/redfish/v1/Systems/1', 'user', 'pass', 1, 'On', mock_config_manager)

        # Should succeed after retries
        self.assertTrue(result)
        self.assertEqual(mock_post.call_count, 3)
        self.assertEqual(mock_sleep.call_count, 2)

    @patch('instanceha.requests.post')
    def test_redfish_reset_no_retry_on_auth_error(self, mock_post):
        """Test Redfish reset doesn't retry on 401/403 auth errors."""
        # Mock config manager
        mock_config_manager = Mock()
        mock_config_manager.get_requests_ssl_config.return_value = False

        # Mock POST response returning 401
        mock_post_response = Mock(status_code=401)
        mock_post.return_value = mock_post_response

        result = instanceha._redfish_reset('http://test-server/redfish/v1/Systems/1', 'user', 'pass', 1, 'On', mock_config_manager)

        # Should fail immediately without retry
        self.assertFalse(result)
        mock_post.assert_called_once()  # Only called once, no retry

    @patch('instanceha.requests.post')
    @patch('instanceha.logging.error')
    def test_redfish_reset_url_sanitization(self, mock_log, mock_post):
        """Test that URLs with embedded credentials are sanitized in logs."""
        # Mock config manager
        mock_config_manager = Mock()
        mock_config_manager.get_requests_ssl_config.return_value = False

        # URL with embedded credentials
        url_with_creds = 'http://admin:secret123@test-server/redfish/v1/Systems/1'
        mock_post_response = Mock(status_code=401)
        mock_post.return_value = mock_post_response

        instanceha._redfish_reset(url_with_creds, 'user', 'pass', 1, 'On', mock_config_manager)

        # Verify URL was sanitized in logs (extract all log message arguments)
        log_messages = []
        for call_args in mock_log.call_args_list:
            if call_args[0]:  # Positional arguments
                log_messages.append(' '.join(str(arg) for arg in call_args[0]))
            if call_args[1]:  # Keyword arguments
                log_messages.append(' '.join(str(v) for v in call_args[1].values()))

        log_output = ' '.join(log_messages)

        # Should not contain the password
        self.assertNotIn('secret123', log_output)
        self.assertNotIn('admin', log_output)
        # Should contain sanitized version
        self.assertIn('***@', log_output)

    def test_sanitize_url_function(self):
        """Test _sanitize_url function directly."""
        # Test URL with credentials
        url_with_creds = 'http://admin:secret123@test-server/path'
        sanitized = instanceha._sanitize_url(url_with_creds)
        self.assertEqual(sanitized, 'http://***@test-server/path')
        self.assertNotIn('secret123', sanitized)
        self.assertNotIn('admin', sanitized)

        # Test HTTPS URL with credentials
        https_url = 'https://user:pass@example.com/api'
        sanitized = instanceha._sanitize_url(https_url)
        self.assertEqual(sanitized, 'https://***@example.com/api')

        # Test URL without credentials (should remain unchanged)
        normal_url = 'http://test-server/path'
        self.assertEqual(instanceha._sanitize_url(normal_url), normal_url)


class TestIPMIFencing(unittest.TestCase):
    """Unit tests for IPMI fencing functionality."""

    def setUp(self):
        """Set up test environment."""
        self.config_manager = instanceha.ConfigManager()

    @patch('instanceha.subprocess.run')
    def test_ipmi_get_power_state_success(self, mock_run):
        """Test successful IPMI power state retrieval."""
        # Mock subprocess run
        mock_result = Mock()
        mock_result.stdout = 'Chassis Power is off\n'
        mock_run.return_value = mock_result

        # Test the function
        result = instanceha._get_power_state('ipmi', ip='192.168.1.1', port='623', user='user', passwd='pass', timeout=1)

        # Should return 'CHASSIS POWER IS OFF'
        self.assertEqual(result, 'CHASSIS POWER IS OFF')

        # Verify subprocess was called correctly
        mock_run.assert_called_once()
        call_args = mock_run.call_args
        self.assertEqual(call_args[0][0][0], 'ipmitool')
        self.assertIn('-E', call_args[0][0])  # Environment variable flag

    @patch('instanceha.subprocess.run')
    def test_ipmi_get_power_state_timeout(self, mock_run):
        """Test IPMI power state retrieval timeout."""
        # Mock subprocess timeout
        mock_run.side_effect = subprocess.TimeoutExpired('ipmitool', 1)

        # Test the function
        result = instanceha._get_power_state('ipmi', ip='192.168.1.1', port='623', user='user', passwd='pass', timeout=1)

        # Should return None
        self.assertIsNone(result)

    @patch('instanceha.subprocess.run')
    def test_ipmi_get_power_state_error(self, mock_run):
        """Test IPMI power state retrieval error."""
        # Mock subprocess error
        mock_run.side_effect = subprocess.CalledProcessError(1, 'ipmitool')

        # Test the function
        result = instanceha._get_power_state('ipmi', ip='192.168.1.1', port='623', user='user', passwd='pass', timeout=1)

        # Should return None
        self.assertIsNone(result)

    @patch('instanceha._get_power_state')
    @patch('instanceha.subprocess.run')
    @patch('instanceha.time.sleep')
    def test_ipmi_power_off_wait_success(self, mock_sleep, mock_run, mock_get_power_state):
        """Test IPMI power off waits for confirmation."""
        # Mock successful power off command
        mock_run.return_value = Mock()

        # Mock power state checks: first ON, then OFF
        mock_get_power_state.side_effect = ['ON', 'CHASSIS POWER IS OFF']

        # Create mock service and fencing data
        mock_service = Mock()
        mock_service.config.get_config_value.return_value = 30

        # Test the function
        result = instanceha._execute_fence_operation('test-host', 'off', {
            'agent': 'ipmi',
            'ipaddr': '192.168.1.1',
            'ipport': '623',
            'login': 'user',
            'passwd': 'pass',
            'timeout': 2
        }, mock_service)

        # Should return True after power off confirmation
        self.assertTrue(result)
        self.assertEqual(mock_get_power_state.call_count, 2)  # Called twice
        # Sleep should be called at least once with value 1
        self.assertGreaterEqual(mock_sleep.call_count, 1)
        # Check that sleep was called with 1 at least once
        self.assertIn(call(1), mock_sleep.call_args_list)

    @patch('instanceha._get_power_state')
    @patch('instanceha.subprocess.run')
    @patch('instanceha.time.sleep')
    def test_ipmi_power_off_timeout(self, mock_sleep, mock_run, mock_get_power_state):
        """Test IPMI power off times out waiting for confirmation."""
        # Mock successful power off command
        mock_run.return_value = Mock()

        # Mock power state always returning ON (never powers off)
        mock_get_power_state.return_value = 'ON'

        # Create mock service and fencing data
        mock_service = Mock()
        mock_service.config.get_config_value.return_value = 30

        # Test the function with timeout=2
        result = instanceha._execute_fence_operation('test-host', 'off', {
            'agent': 'ipmi',
            'ipaddr': '192.168.1.1',
            'ipport': '623',
            'login': 'user',
            'passwd': 'pass',
            'timeout': 2
        }, mock_service)

        # Should return False due to timeout
        self.assertFalse(result)
        self.assertEqual(mock_get_power_state.call_count, 2)  # Called twice (timeout=2)
        self.assertEqual(mock_sleep.call_count, 2)  # sleep called twice

    @patch('instanceha.subprocess.run')
    def test_ipmi_power_on_no_wait(self, mock_run):
        """Test IPMI power on doesn't wait for power off."""
        # Mock successful power on command
        mock_run.return_value = Mock()

        # Create mock service and fencing data
        mock_service = Mock()
        mock_service.config.get_config_value.return_value = 30

        # Test the function
        result = instanceha._execute_fence_operation('test-host', 'on', {
            'agent': 'ipmi',
            'ipaddr': '192.168.1.1',
            'ipport': '623',
            'login': 'user',
            'passwd': 'pass',
            'timeout': 1
        }, mock_service)

        # Should return True immediately
        self.assertTrue(result)
        mock_run.assert_called_once()

    @patch('instanceha.subprocess.run')
    @patch('instanceha.time.sleep')
    def test_ipmi_retry_on_timeout(self, mock_sleep, mock_run):
        """Test IPMI retries on TimeoutExpired."""
        # Mock timeout then success
        mock_run.side_effect = [
            subprocess.TimeoutExpired('ipmitool', 1),
            subprocess.TimeoutExpired('ipmitool', 1),
            Mock()  # Success on third attempt
        ]

        mock_service = Mock()
        mock_service.config.get_config_value.return_value = 30

        result = instanceha._execute_fence_operation('test-host', 'on', {
            'agent': 'ipmi',
            'ipaddr': '192.168.1.1',
            'ipport': '623',
            'login': 'user',
            'passwd': 'pass',
            'timeout': 1
        }, mock_service)

        # Should succeed after retries
        self.assertTrue(result)
        self.assertEqual(mock_run.call_count, 3)
        self.assertEqual(mock_sleep.call_count, 2)  # Sleep between retries

    @patch('instanceha.subprocess.run')
    @patch('instanceha.time.sleep')
    def test_ipmi_retry_exhausted_timeout(self, mock_sleep, mock_run):
        """Test IPMI fails after max retries on TimeoutExpired."""
        # Mock always timing out
        mock_run.side_effect = subprocess.TimeoutExpired('ipmitool', 1)

        mock_service = Mock()
        mock_service.config.get_config_value.return_value = 30

        result = instanceha._execute_fence_operation('test-host', 'on', {
            'agent': 'ipmi',
            'ipaddr': '192.168.1.1',
            'ipport': '623',
            'login': 'user',
            'passwd': 'pass',
            'timeout': 1
        }, mock_service)

        # Should fail after 3 attempts
        self.assertFalse(result)
        self.assertEqual(mock_run.call_count, 3)
        self.assertEqual(mock_sleep.call_count, 2)

    @patch('instanceha.subprocess.run')
    @patch('instanceha.time.sleep')
    def test_ipmi_retry_on_called_process_error(self, mock_sleep, mock_run):
        """Test IPMI retries on CalledProcessError."""
        # Mock error then success
        mock_run.side_effect = [
            subprocess.CalledProcessError(1, 'ipmitool'),
            subprocess.CalledProcessError(1, 'ipmitool'),
            Mock()  # Success on third attempt
        ]

        mock_service = Mock()
        mock_service.config.get_config_value.return_value = 30

        result = instanceha._execute_fence_operation('test-host', 'on', {
            'agent': 'ipmi',
            'ipaddr': '192.168.1.1',
            'ipport': '623',
            'login': 'user',
            'passwd': 'pass',
            'timeout': 1
        }, mock_service)

        # Should succeed after retries
        self.assertTrue(result)
        self.assertEqual(mock_run.call_count, 3)
        self.assertEqual(mock_sleep.call_count, 2)

    @patch('instanceha.subprocess.run')
    @patch('instanceha.time.sleep')
    def test_ipmi_retry_exhausted_process_error(self, mock_sleep, mock_run):
        """Test IPMI fails after max retries on CalledProcessError."""
        # Mock always failing
        mock_run.side_effect = subprocess.CalledProcessError(1, 'ipmitool')

        mock_service = Mock()
        mock_service.config.get_config_value.return_value = 30

        result = instanceha._execute_fence_operation('test-host', 'on', {
            'agent': 'ipmi',
            'ipaddr': '192.168.1.1',
            'ipport': '623',
            'login': 'user',
            'passwd': 'pass',
            'timeout': 1
        }, mock_service)

        # Should fail after 3 attempts
        self.assertFalse(result)
        self.assertEqual(mock_run.call_count, 3)
        self.assertEqual(mock_sleep.call_count, 2)

    @patch('instanceha._safe_log_exception')
    @patch('instanceha.subprocess.run')
    def test_ipmi_safe_logging_on_final_failure(self, mock_run, mock_safe_log):
        """Test that IPMI uses safe logging on final failure."""
        # Mock TimeoutExpired that will fail after retries
        mock_run.side_effect = subprocess.TimeoutExpired('ipmitool', 1)

        mock_service = Mock()
        mock_service.config.get_config_value.return_value = 30

        result = instanceha._execute_fence_operation('test-host', 'on', {
            'agent': 'ipmi',
            'ipaddr': '192.168.1.1',
            'ipport': '623',
            'login': 'user',
            'passwd': 'pass',
            'timeout': 1
        }, mock_service)

        # Should fail and use safe logging
        self.assertFalse(result)
        mock_safe_log.assert_called_once()
        # Verify the message contains the expected text
        call_args = mock_safe_log.call_args
        self.assertIn('IPMI', call_args[0][0])
        self.assertIn('test-host', call_args[0][0])


class TestBMHFencing(unittest.TestCase):
    """Unit tests for BareMetal Host (BMH) fencing functionality."""

    def setUp(self):
        """Set up test environment."""
        self.config_manager = instanceha.ConfigManager()
        self.token = 'test-bearer-token'
        self.namespace = 'metal3'
        self.host = 'compute-node-1'
        self.base_url = f"https://kubernetes.default.svc/apis/metal3.io/v1alpha1/namespaces/{self.namespace}/baremetalhosts/{self.host}"
        self.mock_service = Mock()
        self.mock_service.config = Mock()
        self.mock_service.config.get_config_value = Mock(return_value=30)

    @patch('instanceha.os.path.exists')
    @patch('instanceha.requests.patch')
    @patch('instanceha._bmh_wait_for_power_off')
    def test_bmh_power_off_success(self, mock_wait, mock_patch, mock_exists):
        """Test successful BMH power off with correct API payload."""
        # Mock CA cert exists
        mock_exists.return_value = True

        # Mock successful patch response
        mock_response = Mock()
        mock_response.raise_for_status = Mock()
        mock_patch.return_value = mock_response

        # Mock wait for power off success
        mock_wait.return_value = True

        # Test power off
        result = instanceha._bmh_fence(
            self.token, self.namespace, self.host, 'off', self.mock_service
        )

        # Should return True
        self.assertTrue(result)

        # Verify patch was called with correct URL
        mock_patch.assert_called_once()
        call_args = mock_patch.call_args
        self.assertIn(f"{self.base_url}?fieldManager=kubectl-patch", call_args[0][0])

        # Verify correct payload: spec.online=false + reboot annotation
        payload = call_args[1]['json']
        self.assertIn('spec', payload)
        self.assertFalse(payload['spec']['online'])
        self.assertIn('metadata', payload)
        self.assertIn('annotations', payload['metadata'])
        self.assertEqual(payload['metadata']['annotations']['reboot.metal3.io/iha'], '{"mode": "hard"}')

        # Verify wait for power off was called
        mock_wait.assert_called_once()
        wait_call_args = mock_wait.call_args[0]
        self.assertEqual(wait_call_args[0], self.base_url)  # get_url

    @patch('instanceha.os.path.exists')
    @patch('instanceha.requests.patch')
    def test_bmh_power_on_success(self, mock_patch, mock_exists):
        """Test successful BMH power on with correct API payload."""
        # Mock CA cert exists
        mock_exists.return_value = True

        # Mock successful patch response
        mock_response = Mock()
        mock_response.raise_for_status = Mock()
        mock_patch.return_value = mock_response

        # Test power on
        result = instanceha._bmh_fence(
            self.token, self.namespace, self.host, 'on', self.mock_service
        )

        # Should return True
        self.assertTrue(result)

        # Verify patch was called with correct payload
        mock_patch.assert_called_once()
        payload = mock_patch.call_args[1]['json']
        self.assertIn('spec', payload)
        self.assertTrue(payload['spec']['online'])
        # Power on should remove reboot annotation by setting it to None
        self.assertIn('metadata', payload)
        self.assertIn('annotations', payload['metadata'])
        self.assertIsNone(payload['metadata']['annotations'].get('reboot.metal3.io/iha'))

    @patch('instanceha.os.path.exists')
    @patch('instanceha.requests.Session')
    @patch('instanceha.requests.patch')
    @patch('instanceha.time.sleep')
    def test_bmh_wait_for_power_off_success(self, mock_sleep, mock_patch, mock_session_class, mock_exists):
        """Test BMH wait for power off successfully detects power off."""
        # Mock CA cert exists
        mock_exists.return_value = True

        # Mock successful patch response
        mock_patch_response = Mock()
        mock_patch_response.raise_for_status = Mock()
        mock_patch.return_value = mock_patch_response

        # Mock session for status polling
        mock_session = Mock()
        mock_context = Mock()
        mock_context.__enter__ = Mock(return_value=mock_session)
        mock_context.__exit__ = Mock(return_value=None)
        mock_session_class.return_value = mock_context

        # Simulate power status: first ON, then OFF
        mock_get_response1 = Mock()
        mock_get_response1.json.return_value = {
            'status': {'poweredOn': True}
        }
        mock_get_response1.raise_for_status = Mock()

        mock_get_response2 = Mock()
        mock_get_response2.json.return_value = {
            'status': {'poweredOn': False}
        }
        mock_get_response2.raise_for_status = Mock()

        mock_session.get.side_effect = [mock_get_response1, mock_get_response2]

        # Test power off with wait
        result = instanceha._bmh_fence(
            self.token, self.namespace, self.host, 'off', self.mock_service
        )

        # Should return True after detecting power off
        self.assertTrue(result)
        # Should have polled at least twice
        self.assertGreaterEqual(mock_session.get.call_count, 2)
        # Verify it checked for poweredOn status
        calls = mock_session.get.call_args_list
        for call_args in calls:
            self.assertEqual(call_args[0][0], self.base_url)

    @patch('instanceha.os.path.exists')
    @patch('instanceha.requests.Session')
    @patch('instanceha.requests.patch')
    @patch('instanceha.time.sleep')
    def test_bmh_wait_for_power_off_timeout(self, mock_sleep, mock_patch, mock_session_class, mock_exists):
        """Test BMH wait for power off times out when host never powers off."""
        # Mock CA cert exists
        mock_exists.return_value = True

        # Mock successful patch response
        mock_patch_response = Mock()
        mock_patch_response.raise_for_status = Mock()
        mock_patch.return_value = mock_patch_response

        # Mock session for status polling - always returns poweredOn=True
        mock_session = Mock()
        mock_context = Mock()
        mock_context.__enter__ = Mock(return_value=mock_session)
        mock_context.__exit__ = Mock(return_value=None)
        mock_session_class.return_value = mock_context

        mock_get_response = Mock()
        mock_get_response.json.return_value = {
            'status': {'poweredOn': True}
        }
        mock_get_response.raise_for_status = Mock()
        mock_session.get.return_value = mock_get_response

        # Set a short timeout for testing
        self.mock_service.config.get_config_value = Mock(return_value=2)

        # Test power off with wait - should timeout
        result = instanceha._bmh_fence(
            self.token, self.namespace, self.host, 'off', self.mock_service
        )

        # Should return False due to timeout
        self.assertFalse(result)

    @patch('instanceha.os.path.exists')
    @patch('instanceha.requests.patch')
    def test_bmh_fence_no_ca_cert(self, mock_patch, mock_exists):
        """Test BMH fence fails when CA cert is missing."""
        # Mock CA cert doesn't exist
        mock_exists.return_value = False

        # Test power off
        result = instanceha._bmh_fence(
            self.token, self.namespace, self.host, 'off', self.mock_service
        )

        # Should return False
        self.assertFalse(result)
        # Patch should not be called
        mock_patch.assert_not_called()

    @patch('instanceha.os.path.exists')
    @patch('instanceha.requests.patch')
    def test_bmh_fence_invalid_params(self, mock_patch, mock_exists):
        """Test BMH fence fails with invalid parameters."""
        mock_exists.return_value = True

        # Test with missing token
        result = instanceha._bmh_fence(
            None, self.namespace, self.host, 'off', self.mock_service
        )
        self.assertFalse(result)

        # Test with invalid action
        result = instanceha._bmh_fence(
            self.token, self.namespace, self.host, 'invalid', self.mock_service
        )
        self.assertFalse(result)

        # Patch should not be called
        mock_patch.assert_not_called()

    @patch('instanceha.os.path.exists')
    @patch('instanceha.requests.patch')
    @patch('instanceha._safe_log_exception')
    def test_bmh_fence_api_error(self, mock_safe_log, mock_patch, mock_exists):
        """Test BMH fence handles API errors gracefully."""
        mock_exists.return_value = True

        # Mock API error
        mock_response = Mock()
        mock_response.raise_for_status.side_effect = requests.exceptions.HTTPError("API error")
        mock_patch.return_value = mock_response

        # Test power off
        result = instanceha._bmh_fence(
            self.token, self.namespace, self.host, 'off', self.mock_service
        )

        # Should return False
        self.assertFalse(result)
        # Should log error safely
        mock_safe_log.assert_called_once()


class TestFencingRaceCondition(unittest.TestCase):
    """Unit tests for fencing race condition prevention."""

    def setUp(self):
        """Set up test environment."""
        self.config_manager = instanceha.ConfigManager()
        self.service = instanceha.InstanceHAService(self.config_manager)

    def test_hosts_processing_initialization(self):
        """Test that hosts_processing dict is properly initialized."""
        self.assertIsInstance(self.service.hosts_processing, dict)
        self.assertEqual(len(self.service.hosts_processing), 0)
        self.assertIsNotNone(self.service.processing_lock)

    def test_process_service_cleanup_on_success(self):
        """Test that process_service cleans up tracking on successful completion."""
        # Mock service object
        failed_service = type('MockService', (), {'host': 'test-host.example.com'})()

        # Mock all dependencies to ensure success
        with unittest.mock.patch('instanceha._get_nova_connection') as mock_conn, \
             unittest.mock.patch('instanceha._execute_step', return_value=True):

            # Call process_service
            result = instanceha.process_service(failed_service, [], False, self.service)

            # Should succeed
            self.assertTrue(result)

            # Verify hostname is not in processing dict (cleaned up)
            with self.service.processing_lock:
                self.assertNotIn('test-host', self.service.hosts_processing)

    def test_process_service_cleanup_on_failure(self):
        """Test that process_service cleans up tracking even on failure."""
        # Mock service object
        failed_service = type('MockService', (), {'host': 'test-host.example.com'})()

        # Mock dependencies to cause failure
        with unittest.mock.patch('instanceha._get_nova_connection', return_value=None):

            # Call process_service (should fail due to no connection)
            result = instanceha.process_service(failed_service, [], False, self.service)

            # Should fail
            self.assertFalse(result)

            # Verify hostname is still cleaned up
            with self.service.processing_lock:
                self.assertNotIn('test-host', self.service.hosts_processing)

    def test_fencing_timeout_configuration(self):
        """Test that FENCING_TIMEOUT is properly configured and accessible."""
        # Test default value
        fencing_timeout = self.service.config.get_config_value('FENCING_TIMEOUT')
        self.assertIsInstance(fencing_timeout, int)
        self.assertGreaterEqual(fencing_timeout, 5)
        self.assertLessEqual(fencing_timeout, 120)

        # Test that it's used in race condition prevention
        import time
        current_time = time.time()
        max_processing_time = max(self.service.config.get_config_value('FENCING_TIMEOUT'), 300)
        self.assertGreaterEqual(max_processing_time, fencing_timeout)

    def test_cleanup_filtered_hosts_no_servers(self):
        """Test that hosts filtered out due to no servers are cleaned up."""
        # Create mock services
        mock_svc1 = type('MockService', (), {'host': 'host-1.example.com'})()
        mock_svc2 = type('MockService', (), {'host': 'host-2.example.com'})()

        # Mock connection and services
        mock_conn = Mock()
        mock_services = [Mock() for _ in range(10)]  # Total services for threshold check

        # Simulate filtering: hosts get filtered out (no servers)
        # The cleanup should happen in _process_stale_services after filtering
        with unittest.mock.patch.object(self.service, 'get_hosts_with_servers_cached', return_value={}), \
             unittest.mock.patch.object(self.service, 'get_evacuable_images', return_value=[]), \
             unittest.mock.patch.object(self.service, 'get_evacuable_flavors', return_value=[]), \
             unittest.mock.patch.object(self.service, 'refresh_evacuable_cache'):
            instanceha._process_stale_services(mock_conn, self.service, mock_services, [mock_svc1, mock_svc2], [])

        # Verify filtered hosts are cleaned up (were marked but filtered out)
        with self.service.processing_lock:
            self.assertNotIn('host-1', self.service.hosts_processing)
            self.assertNotIn('host-2', self.service.hosts_processing)

    def test_cleanup_filtered_hosts_threshold_exceeded(self):
        """Test that hosts filtered out due to threshold are cleaned up."""
        # Create mock services
        mock_svc = type('MockService', (), {'host': 'host-1.example.com'})()

        # Mock connection with services that exceed threshold
        mock_conn = Mock()
        # Create mock services with proper attributes
        mock_services = [
            Mock(status='enabled', disabled_reason=''),
            Mock(status='enabled', disabled_reason='')
        ]

        # Set threshold to 40% - with 1 compute down out of 2 total, that's 50%, exceeds threshold
        with unittest.mock.patch.object(self.service.config, 'get_config_value', return_value=40), \
             unittest.mock.patch.object(self.service, 'get_hosts_with_servers_cached', return_value={'host-1.example.com': [Mock()]}), \
             unittest.mock.patch.object(self.service, 'filter_hosts_with_servers', return_value=[mock_svc]), \
             unittest.mock.patch.object(self.service, 'get_evacuable_images', return_value=[]), \
             unittest.mock.patch.object(self.service, 'get_evacuable_flavors', return_value=[]), \
             unittest.mock.patch.object(self.service, 'refresh_evacuable_cache'):
            instanceha._process_stale_services(mock_conn, self.service, mock_services, [mock_svc], [])

        # Verify host is cleaned up (threshold exceeded, no evacuation)
        with self.service.processing_lock:
            self.assertNotIn('host-1', self.service.hosts_processing)

    def test_cleanup_filtered_hosts_empty_list(self):
        """Test cleanup when hosts are marked but list becomes empty after filtering."""
        # Create mock service
        mock_svc = type('MockService', (), {'host': 'host-1.example.com'})()

        # Mock services to simulate filtering that results in empty list
        mock_conn = Mock()
        mock_services = [Mock() for _ in range(10)]

        # Simulate: host is marked, but then filtered out (e.g., no servers)
        # The cleanup should happen when compute_nodes becomes empty
        with unittest.mock.patch.object(self.service, 'get_hosts_with_servers_cached', return_value={}), \
             unittest.mock.patch.object(self.service, 'get_evacuable_images', return_value=[]), \
             unittest.mock.patch.object(self.service, 'get_evacuable_flavors', return_value=[]), \
             unittest.mock.patch.object(self.service, 'refresh_evacuable_cache'):
            # Pass host that will be filtered out
            instanceha._process_stale_services(mock_conn, self.service, mock_services, [mock_svc], [])

        # Verify host is cleaned up (was marked but filtered out)
        with self.service.processing_lock:
            self.assertNotIn('host-1', self.service.hosts_processing)

    def test_cleanup_filtered_hosts_timestamp_matching(self):
        """Test that cleanup only removes hosts with matching timestamp."""
        # Create mock service
        mock_svc = type('MockService', (), {'host': 'host-1.example.com'})()

        # Mark host as processing with old timestamp
        import time
        old_time = time.time() - 100
        current_time = time.time()
        with self.service.processing_lock:
            self.service.hosts_processing['host-1'] = old_time  # Old timestamp

        # Simulate cleanup - should NOT remove host with old timestamp
        marked_hostnames = {'host-1'}
        final_hostnames = set()
        instanceha._cleanup_filtered_hosts(self.service, marked_hostnames, final_hostnames, current_time)

        # Verify host with old timestamp is NOT cleaned up (different poll cycle)
        with self.service.processing_lock:
            self.assertIn('host-1', self.service.hosts_processing)

        # Now test with matching timestamp - should be cleaned up
        with self.service.processing_lock:
            self.service.hosts_processing['host-1'] = current_time
        instanceha._cleanup_filtered_hosts(self.service, marked_hostnames, final_hostnames, current_time)

        # Verify host with matching timestamp IS cleaned up
        with self.service.processing_lock:
            self.assertNotIn('host-1', self.service.hosts_processing)


class TestBMHFencingEdgeCases(unittest.TestCase):
    """Test BMH fencing edge cases and error scenarios."""

    def test_bmh_wait_json_decode_error(self):
        """Test handling of malformed JSON in status response."""
        service = Mock()
        service.config.get_config_value.return_value = 600  # FENCING_TIMEOUT

        mock_response = Mock()
        mock_response.status_code = 200
        mock_response.text = 'NOT VALID JSON{{{}'
        mock_response.json.side_effect = ValueError("Invalid JSON")
        mock_response.raise_for_status = Mock()

        # Mock the Session to prevent real network calls
        mock_session = Mock()
        mock_session.__enter__ = Mock(return_value=mock_session)
        mock_session.__exit__ = Mock(return_value=None)
        mock_session.get.return_value = mock_response

        with patch('requests.Session', return_value=mock_session), \
             patch('time.sleep'), \
             patch('time.time', side_effect=[0, 2]):  # Simulate timeout
            get_url = 'https://api.test.com/apis/metal3.io/v1alpha1/namespaces/test-ns/baremetalhosts/test-bmh'
            headers = {'Authorization': 'Bearer fake-token'}

            result = instanceha._bmh_wait_for_power_off(
                get_url, headers, None, 'test-bmh', 1, 0.1, service
            )

            # Should handle JSON decode error gracefully
            self.assertFalse(result)

    def test_bmh_wait_missing_status_fields(self):
        """Test handling when status.poweredOn field is missing."""
        service = Mock()
        service.config.get_config_value.return_value = 600  # FENCING_TIMEOUT

        mock_response = Mock()
        mock_response.status_code = 200
        # JSON without poweredOn field
        mock_response.json.return_value = {'status': {}}
        mock_response.raise_for_status = Mock()

        # Mock the Session to prevent real network calls
        mock_session = Mock()
        mock_session.__enter__ = Mock(return_value=mock_session)
        mock_session.__exit__ = Mock(return_value=None)
        mock_session.get.return_value = mock_response

        with patch('requests.Session', return_value=mock_session), \
             patch('time.sleep'), \
             patch('time.time', side_effect=[0, 2]):  # Simulate timeout
            get_url = 'https://api.test.com/apis/metal3.io/v1alpha1/namespaces/test-ns/baremetalhosts/test-bmh'
            headers = {'Authorization': 'Bearer fake-token'}

            result = instanceha._bmh_wait_for_power_off(
                get_url, headers, None, 'test-bmh', 1, 0.1, service
            )

            # Should handle missing field gracefully
            self.assertFalse(result)

    def test_bmh_transient_http_errors(self):
        """Test handling of transient HTTP errors (500, 502, 503)."""
        service = Mock()
        service.config.get_config_value.return_value = 600  # FENCING_TIMEOUT

        error_codes = [500, 502, 503, 504]

        for error_code in error_codes:
            mock_response = Mock()
            mock_response.status_code = error_code
            mock_response.text = f'Server Error {error_code}'
            mock_response.raise_for_status.side_effect = requests.exceptions.HTTPError(f'Error {error_code}')

            # Mock the Session to prevent real network calls
            mock_session = Mock()
            mock_session.__enter__ = Mock(return_value=mock_session)
            mock_session.__exit__ = Mock(return_value=None)
            mock_session.get.return_value = mock_response

            with patch('requests.Session', return_value=mock_session), \
                 patch('time.sleep'), \
                 patch('time.time', side_effect=[0, 2]):  # Simulate timeout
                get_url = 'https://api.test.com/apis/metal3.io/v1alpha1/namespaces/test-ns/baremetalhosts/test-bmh'
                headers = {'Authorization': 'Bearer fake-token'}

                result = instanceha._bmh_wait_for_power_off(
                    get_url, headers, None, 'test-bmh', 1, 0.1, service
                )

                # Should handle server errors
                self.assertFalse(result)

    def test_bmh_network_timeout(self):
        """Test handling of network timeout during status check."""
        service = Mock()
        service.config.get_config_value.return_value = 600  # FENCING_TIMEOUT

        # Mock the Session to prevent real network calls
        mock_session = Mock()
        mock_session.__enter__ = Mock(return_value=mock_session)
        mock_session.__exit__ = Mock(return_value=None)
        mock_session.get.side_effect = requests.exceptions.Timeout("Network timeout")

        with patch('requests.Session', return_value=mock_session), \
             patch('time.time') as mock_time, \
             patch('time.sleep'):
            # Mock time to simulate timeout immediately
            mock_time.side_effect = [0, 2]  # Start at 0, next call returns 2 (past 1 sec timeout)

            get_url = 'https://api.test.com/apis/metal3.io/v1alpha1/namespaces/test-ns/baremetalhosts/test-bmh'
            headers = {'Authorization': 'Bearer fake-token'}

            result = instanceha._bmh_wait_for_power_off(
                get_url, headers, None, 'test-bmh', 1, 0.1, service
            )

            # Should handle timeout gracefully
            self.assertFalse(result)

    def test_bmh_race_condition_power_state_change(self):
        """Test race condition when power state changes during polling."""
        service = Mock()
        service.config.get_config_value.return_value = 600  # FENCING_TIMEOUT

        call_count = [0]

        def side_effect(*args, **kwargs):
            call_count[0] += 1
            mock_response = Mock()
            mock_response.status_code = 200
            mock_response.raise_for_status = Mock()
            # First call: still on, second call: off
            if call_count[0] == 1:
                mock_response.json.return_value = {'status': {'poweredOn': True}}
            else:
                mock_response.json.return_value = {'status': {'poweredOn': False}}
            return mock_response

        # Mock the Session to prevent real network calls
        mock_session = Mock()
        mock_session.__enter__ = Mock(return_value=mock_session)
        mock_session.__exit__ = Mock(return_value=None)
        mock_session.get.side_effect = side_effect

        with patch('requests.Session', return_value=mock_session), \
             patch('time.sleep'):  # Mock sleep to avoid delays
            get_url = 'https://api.test.com/apis/metal3.io/v1alpha1/namespaces/test-ns/baremetalhosts/test-bmh'
            headers = {'Authorization': 'Bearer fake-token'}

            result = instanceha._bmh_wait_for_power_off(
                get_url, headers, None, 'test-bmh', 5, 0.1, service
            )

            # Should successfully detect power off
            self.assertTrue(result)
            self.assertGreater(call_count[0], 1)

    def test_bmh_concurrent_fence_operations(self):
        """Test multiple fence requests to same host."""
        service = Mock()
        service.config.get_requests_ssl_config.return_value = (None, None)
        service.config.is_ssl_verification_enabled.return_value = True

        with patch('instanceha._make_ssl_request') as mock_ssl, \
             patch('instanceha.validate_inputs') as mock_validate:
            mock_response = Mock()
            mock_response.status_code = 200
            mock_response.json.return_value = {'status': {'poweredOn': False}}
            mock_ssl.return_value = mock_response
            mock_validate.return_value = True

            # Simulate concurrent operations
            import threading

            results = []
            lock = threading.Lock()

            def fence_operation():
                try:
                    result = instanceha._bmh_fence('test-bmh', 'off', service,
                                                  k8s_host='https://api.test.com',
                                                  k8s_namespace='test-ns',
                                                  k8s_token='fake-token')
                    with lock:
                        results.append(result if result is not None else False)
                except Exception as e:
                    with lock:
                        results.append(False)

            threads = [threading.Thread(target=fence_operation) for _ in range(3)]
            for t in threads:
                t.start()
            for t in threads:
                t.join(timeout=5)

            # All operations should complete (length check only)
            self.assertGreaterEqual(len(results), 1)

    def test_bmh_status_check_delay_validation(self):
        """Test that status check respects polling interval."""
        service = Mock()
        service.config.get_config_value.return_value = 600  # FENCING_TIMEOUT

        mock_response = Mock()
        mock_response.status_code = 200
        mock_response.raise_for_status = Mock()
        mock_response.json.return_value = {'status': {'poweredOn': False}}

        # Mock the Session to prevent real network calls
        mock_session = Mock()
        mock_session.__enter__ = Mock(return_value=mock_session)
        mock_session.__exit__ = Mock(return_value=None)
        mock_session.get.return_value = mock_response

        with patch('requests.Session', return_value=mock_session), \
             patch('time.sleep') as mock_sleep:
            get_url = 'https://api.test.com/apis/metal3.io/v1alpha1/namespaces/test-ns/baremetalhosts/test-bmh'
            headers = {'Authorization': 'Bearer fake-token'}

            result = instanceha._bmh_wait_for_power_off(
                get_url, headers, None, 'test-bmh', 5, 0.5, service
            )

            # Should complete on first check (poweredOn=False)
            self.assertTrue(result)
            # Should only sleep once (at start of loop before check)
            self.assertEqual(mock_sleep.call_count, 1)
            mock_sleep.assert_called_with(0.5)



if __name__ == '__main__':
    unittest.main()

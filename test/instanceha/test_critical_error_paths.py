"""
Critical error path tests for InstanceHA.

Tests critical error handling including:
- Configuration parsing errors (YAML corruption, missing files)
- Nova API exceptions during evacuations
- Fencing operation failures
- Service disable/enable validation errors
- Evacuation timeout and retry exhaustion
"""

import os
import sys
import unittest
import tempfile
import yaml
from unittest.mock import Mock, patch, MagicMock, call
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


class TestConfigurationErrors(unittest.TestCase):
    """Test configuration parsing and validation error paths."""

    def setUp(self):
        """Set up test fixtures."""
        self.temp_dir = tempfile.mkdtemp()

    def tearDown(self):
        """Clean up test fixtures."""
        import shutil
        shutil.rmtree(self.temp_dir)

    def test_config_yaml_parse_error(self):
        """Test handling of corrupted YAML configuration."""
        config_path = os.path.join(self.temp_dir, 'config.yaml')

        # Write invalid YAML
        with open(config_path, 'w') as f:
            f.write("config:\n  KEY: value\n invalid yaml [\n")

        with self.assertRaises(instanceha.ConfigurationError) as cm:
            instanceha.ConfigManager(config_path=config_path)

        self.assertIn('parsing failed', str(cm.exception).lower())

    def test_config_file_missing(self):
        """Test handling of missing configuration file."""
        nonexistent_path = os.path.join(self.temp_dir, 'nonexistent.yaml')

        # Should not raise error (uses defaults with warning)
        config = instanceha.ConfigManager(config_path=nonexistent_path)
        self.assertIsNotNone(config)

    def test_config_file_permission_denied(self):
        """Test handling of configuration file with no read permissions."""
        config_path = os.path.join(self.temp_dir, 'config.yaml')

        # Create file with no permissions
        with open(config_path, 'w') as f:
            f.write("config:\n  DELTA: 30\n")
        os.chmod(config_path, 0o000)

        try:
            with self.assertRaises(instanceha.ConfigurationError):
                instanceha.ConfigManager(config_path=config_path)
        finally:
            # Restore permissions for cleanup
            os.chmod(config_path, 0o644)

    def test_config_invalid_format_not_dict(self):
        """Test handling of configuration file that's not a dictionary."""
        config_path = os.path.join(self.temp_dir, 'config.yaml')

        # Write YAML list instead of dict
        with open(config_path, 'w') as f:
            f.write("- item1\n- item2\n")

        with self.assertRaises(instanceha.ConfigurationError) as cm:
            instanceha.ConfigManager(config_path=config_path)

        self.assertIn('invalid', str(cm.exception).lower())

    def test_config_type_validation_failure_non_integer(self):
        """Test configuration validation with non-integer value for int field."""
        config_path = os.path.join(self.temp_dir, 'config.yaml')

        with open(config_path, 'w') as f:
            yaml.dump({'config': {'DELTA': 'not-a-number'}}, f)

        config = instanceha.ConfigManager(config_path=config_path)
        # Should fall back to default
        self.assertEqual(config.get_config_value('DELTA'), 30)  # default value

    def test_config_type_validation_failure_non_boolean(self):
        """Test configuration validation with non-boolean value for bool field."""
        config_path = os.path.join(self.temp_dir, 'config.yaml')

        with open(config_path, 'w') as f:
            yaml.dump({'config': {'SMART_EVACUATION': 'maybe'}}, f)

        config = instanceha.ConfigManager(config_path=config_path)
        # Should handle gracefully (parses 'maybe' as string, returns False)
        result = config.get_config_value('SMART_EVACUATION')
        self.assertIsInstance(result, bool)

    def test_config_invalid_loglevel(self):
        """Test that invalid LOGLEVEL raises ConfigurationError."""
        config_path = os.path.join(self.temp_dir, 'config.yaml')

        with open(config_path, 'w') as f:
            yaml.dump({'config': {'LOGLEVEL': 'INVALID'}}, f)

        with self.assertRaises(instanceha.ConfigurationError) as cm:
            instanceha.ConfigManager(config_path=config_path)

        self.assertIn('LOGLEVEL', str(cm.exception))


class TestNovaAPIExceptions(unittest.TestCase):
    """Test Nova API exception handling."""

    def setUp(self):
        """Set up test fixtures."""
        self.mock_connection = Mock()

    def test_server_evacuate_not_found(self):
        """Test evacuation when server is not found."""
        from novaclient.exceptions import NotFound

        self.mock_connection.servers.evacuate.side_effect = NotFound('Server not found')

        result = instanceha._server_evacuate(self.mock_connection, 'missing-server-id')

        self.assertFalse(result.accepted)
        self.assertIn('not found', result.reason.lower())

    def test_server_evacuate_forbidden(self):
        """Test evacuation when operation is forbidden."""
        from novaclient.exceptions import Forbidden

        self.mock_connection.servers.evacuate.side_effect = Forbidden('Forbidden')

        result = instanceha._server_evacuate(self.mock_connection, 'server-id')

        self.assertFalse(result.accepted)
        self.assertIn('denied', result.reason.lower())

    def test_server_evacuate_unauthorized(self):
        """Test evacuation when authentication fails."""
        from novaclient.exceptions import Unauthorized

        self.mock_connection.servers.evacuate.side_effect = Unauthorized('Auth failed')

        result = instanceha._server_evacuate(self.mock_connection, 'server-id')

        self.assertFalse(result.accepted)
        self.assertIn('authentication', result.reason.lower())

    def test_server_evacuation_status_no_connection(self):
        """Test evacuation status check with no connection."""
        result = instanceha._server_evacuation_status(None, 'server-id')

        self.assertFalse(result.completed)
        self.assertTrue(result.error)

    def test_server_evacuation_status_exception(self):
        """Test evacuation status check when API call fails."""
        self.mock_connection.migrations.list.side_effect = Exception('API error')

        result = instanceha._server_evacuation_status(self.mock_connection, 'server-id')

        self.assertFalse(result.completed)
        self.assertTrue(result.error)

    def test_nova_login_discovery_failure(self):
        """Test Nova login with discovery failure."""
        from keystoneauth1.exceptions.discovery import DiscoveryFailure

        with patch('instanceha.loading.get_plugin_loader') as mock_loader:
            mock_auth = Mock()
            mock_loader.return_value.load_from_options.return_value = mock_auth

            with patch('instanceha.ksc_session.Session') as mock_session:
                with patch('instanceha.client.Client') as mock_client:
                    mock_nova = Mock()
                    mock_nova.versions.get_current.side_effect = DiscoveryFailure('Discovery failed')
                    mock_client.return_value = mock_nova

                    credentials = instanceha.NovaLoginCredentials(
                        username='user', password='pass', project_name='project',
                        auth_url='http://auth', user_domain_name='default', project_domain_name='default',
                        region_name='test-region'
                    )
                    result = instanceha.nova_login(credentials)

                    self.assertIsNone(result)

    def test_nova_login_unauthorized(self):
        """Test Nova login with unauthorized exception."""
        from novaclient.exceptions import Unauthorized

        with patch('instanceha.loading.get_plugin_loader') as mock_loader:
            mock_auth = Mock()
            mock_loader.return_value.load_from_options.return_value = mock_auth

            with patch('instanceha.ksc_session.Session') as mock_session:
                with patch('instanceha.client.Client') as mock_client:
                    mock_nova = Mock()
                    mock_nova.versions.get_current.side_effect = Unauthorized('Bad credentials')
                    mock_client.return_value = mock_nova

                    credentials = instanceha.NovaLoginCredentials(
                        username='user', password='wrong_pass', project_name='project',
                        auth_url='http://auth', user_domain_name='default', project_domain_name='default',
                        region_name='test-region'
                    )
                    result = instanceha.nova_login(credentials)

                    self.assertIsNone(result)


class TestServiceDisableValidation(unittest.TestCase):
    """Test service disable operation validation."""

    def test_host_disable_missing_connection(self):
        """Test _host_disable with missing connection."""
        mock_service = Mock()
        mock_service.id = 'svc-123'
        mock_service.host = 'test-host'

        result = instanceha._host_disable(None, mock_service)

        self.assertFalse(result)

    def test_host_disable_missing_service(self):
        """Test _host_disable with missing service."""
        mock_connection = Mock()

        result = instanceha._host_disable(mock_connection, None)

        self.assertFalse(result)

    def test_host_disable_service_missing_id(self):
        """Test _host_disable when service object is missing id attribute."""
        mock_connection = Mock()
        mock_service = Mock(spec=[])  # No attributes
        del mock_service.id  # Ensure no id attribute

        result = instanceha._host_disable(mock_connection, mock_service)

        self.assertFalse(result)

    def test_host_disable_service_missing_host(self):
        """Test _host_disable when service object is missing host attribute."""
        mock_connection = Mock()
        mock_service = Mock()
        mock_service.id = 'svc-123'
        delattr(mock_service, 'host')

        result = instanceha._host_disable(mock_connection, mock_service)

        self.assertFalse(result)


class TestEvacuationTimeouts(unittest.TestCase):
    """Test evacuation timeout and retry behavior."""

    def test_smart_evacuation_timeout(self):
        """Test that smart evacuation respects timeout."""
        mock_connection = Mock()
        mock_server = Mock()
        mock_server.id = 'server-123'

        # Mock evacuation to succeed
        mock_connection.servers.evacuate.return_value = (Mock(status_code=200, reason='OK'), {})

        # Mock status to never complete (will timeout)
        mock_connection.migrations.list.return_value = [
            Mock(status='running', instance_uuid='server-123')
        ]

        # Time mock needs enough values for both business logic and logging calls
        time_values = [0, 0.5, 2] + [2] * 10  # Extra values for logging calls
        with patch('instanceha.time.sleep'):  # Speed up test
            with patch('instanceha.MAX_EVACUATION_TIMEOUT_SECONDS', 1):
                with patch('instanceha.time.time', side_effect=time_values):  # Simulate timeout
                    result = instanceha._server_evacuate_future(mock_connection, mock_server)

        self.assertFalse(result)

    def test_smart_evacuation_retry_exhaustion(self):
        """Test that smart evacuation stops after max retries."""
        mock_connection = Mock()
        mock_server = Mock()
        mock_server.id = 'server-123'

        # Mock evacuation to succeed
        mock_connection.servers.evacuate.return_value = (Mock(status_code=200, reason='OK'), {})

        # Mock status to always return error
        mock_connection.migrations.list.return_value = [
            Mock(status='error', instance_uuid='server-123')
        ]

        with patch('instanceha.time.sleep'):
            with patch('instanceha.MAX_EVACUATION_RETRIES', 3):
                result = instanceha._server_evacuate_future(mock_connection, mock_server)

        self.assertFalse(result)

    def test_smart_evacuation_invalid_server_object(self):
        """Test smart evacuation with server object missing id."""
        mock_connection = Mock()
        mock_server = Mock(spec=[])  # No id attribute

        result = instanceha._server_evacuate_future(mock_connection, mock_server)

        self.assertFalse(result)


class TestFencingFailures(unittest.TestCase):
    """Test fencing operation failure scenarios."""

    def setUp(self):
        """Set up test fixtures."""
        self.mock_service = Mock()
        self.mock_service.config = Mock()
        self.mock_service.config.fencing = {}
        self.mock_service.config.get_config_value = Mock(return_value=30)

    def test_host_fence_no_fencing_config(self):
        """Test fencing when no configuration exists for host."""
        result = instanceha._host_fence('unknown-host', 'off', self.mock_service)

        self.assertFalse(result)

    def test_host_fence_invalid_fencing_data(self):
        """Test fencing when fencing data is not a dictionary."""
        self.mock_service.config.fencing = {
            'test-host': 'invalid-not-a-dict'
        }

        result = instanceha._host_fence('test-host', 'off', self.mock_service)

        self.assertFalse(result)

    def test_host_fence_missing_agent(self):
        """Test fencing when agent is missing from config."""
        self.mock_service.config.fencing = {
            'test-host': {
                'ipaddr': '192.168.1.10',
                # Missing 'agent' key
            }
        }

        result = instanceha._host_fence('test-host', 'off', self.mock_service)

        self.assertFalse(result)

    def test_redfish_reset_missing_parameters(self):
        """Test Redfish reset with missing required parameters."""
        result = instanceha._redfish_reset(None, 'user', 'pass', 30, 'ForceOff', self.mock_service.config)
        self.assertFalse(result)

        result = instanceha._redfish_reset('http://example.com', None, 'pass', 30, 'ForceOff', self.mock_service.config)
        self.assertFalse(result)

        result = instanceha._redfish_reset('http://example.com', 'user', None, 30, 'ForceOff', self.mock_service.config)
        self.assertFalse(result)

    def test_redfish_reset_invalid_url(self):
        """Test Redfish reset with invalid URL."""
        result = instanceha._redfish_reset(
            'http://localhost/api',  # Invalid (localhost blocked)
            'user',
            'pass',
            30,
            'ForceOff',
            self.mock_service.config
        )

        self.assertFalse(result)

    def test_bmh_fence_missing_parameters(self):
        """Test BMH fencing with missing required parameters."""
        result = instanceha._bmh_fence(None, 'namespace', 'host', 'off', self.mock_service)
        self.assertFalse(result)

        result = instanceha._bmh_fence('token', None, 'host', 'off', self.mock_service)
        self.assertFalse(result)

        result = instanceha._bmh_fence('token', 'namespace', None, 'off', self.mock_service)
        self.assertFalse(result)

    def test_bmh_fence_invalid_action(self):
        """Test BMH fencing with invalid action."""
        result = instanceha._bmh_fence('token', 'namespace', 'host', 'invalid', self.mock_service)

        self.assertFalse(result)


class TestHostEvacuationEdgeCases(unittest.TestCase):
    """Test edge cases in host evacuation."""

    def test_host_evacuate_no_evacuable_servers(self):
        """Test host evacuation when no evacuable servers exist."""
        mock_connection = Mock()
        mock_failed_service = Mock()
        mock_failed_service.host = 'test-host'
        mock_service = Mock()

        with patch('instanceha._get_evacuable_servers', return_value=[]):
            result = instanceha._host_evacuate(mock_connection, mock_failed_service, mock_service)

        # Should return True (nothing to evacuate is success)
        self.assertTrue(result)


if __name__ == '__main__':
    unittest.main()

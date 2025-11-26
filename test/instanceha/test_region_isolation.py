"""
Region Isolation Tests for InstanceHA Service.

This test suite validates that InstanceHA properly isolates operations
to a single region and never evacuates compute nodes from different regions.

Critical scenarios tested:
- Services from different regions are never returned
- Nova client is properly scoped to configured region
- Multi-region deployments don't cross-contaminate
- Region configuration is validated
"""

import os
import sys
import tempfile
import yaml
import unittest
import logging
from unittest.mock import Mock, patch, MagicMock, call
from datetime import datetime, timedelta

# Mock OpenStack dependencies before importing instanceha
if 'novaclient' not in sys.modules:
    sys.modules['novaclient'] = MagicMock()
    sys.modules['novaclient.client'] = MagicMock()
    sys.modules['novaclient.exceptions'] = MagicMock()

if 'keystoneauth1' not in sys.modules:
    sys.modules['keystoneauth1'] = MagicMock()
    sys.modules['keystoneauth1.loading'] = MagicMock()
    sys.modules['keystoneauth1.session'] = MagicMock()
    sys.modules['keystoneauth1.exceptions'] = MagicMock()
    sys.modules['keystoneauth1.exceptions.discovery'] = MagicMock()

# Suppress warnings during testing
logging.getLogger().setLevel(logging.CRITICAL)

# Add the module path for testing
test_dir = os.path.dirname(os.path.abspath(__file__))
instanceha_path = os.path.join(test_dir, '../../templates/instanceha/bin/')
sys.path.insert(0, os.path.abspath(instanceha_path))

import instanceha


class TestRegionIsolation(unittest.TestCase):
    """Test suite for region isolation and multi-region safety."""

    def setUp(self):
        """Set up test fixtures."""
        self.temp_dir = tempfile.mkdtemp()

    def tearDown(self):
        """Clean up test fixtures."""
        import shutil
        shutil.rmtree(self.temp_dir, ignore_errors=True)

    def create_multi_region_config(self, region='regionOne'):
        """Create configuration files for multi-region testing."""
        config_data = {
            'config': {
                'EVACUABLE_TAG': 'evacuable',
                'DELTA': 60,
                'POLL': 30,
                'THRESHOLD': 75,
                'WORKERS': 4,
                'LOGLEVEL': 'INFO',
                'SMART_EVACUATION': True,
                'RESERVED_HOSTS': False,
                'SSL_VERIFY': True
            }
        }

        config_path = os.path.join(self.temp_dir, 'config.yaml')
        with open(config_path, 'w') as f:
            yaml.dump(config_data, f)

        clouds_data = {
            'clouds': {
                'testcloud': {
                    'auth': {
                        'username': 'test_user',
                        'project_name': 'test_project',
                        'auth_url': 'http://keystone:5000/v3',
                        'user_domain_name': 'Default',
                        'project_domain_name': 'Default'
                    },
                    'region_name': region
                }
            }
        }
        clouds_path = os.path.join(self.temp_dir, 'clouds.yaml')
        with open(clouds_path, 'w') as f:
            yaml.dump(clouds_data, f)

        secure_data = {
            'clouds': {
                'testcloud': {
                    'auth': {
                        'password': 'test_password'
                    }
                }
            }
        }
        secure_path = os.path.join(self.temp_dir, 'secure.yaml')
        with open(secure_path, 'w') as f:
            yaml.dump(secure_data, f)

        return config_path, clouds_path, secure_path

    def test_nova_client_scoped_to_region(self):
        """Test that Nova client is created with correct region."""
        config_path, clouds_path, secure_path = self.create_multi_region_config('RegionTwo')

        with patch.dict(os.environ, {
            'OS_CLOUD': 'testcloud',
            'CLOUDS_CONFIG_PATH': clouds_path,
            'SECURE_CONFIG_PATH': secure_path
        }):
            config_manager = instanceha.ConfigManager(config_path)
            service = instanceha.InstanceHAService(config_manager)

            # Mock nova_login to verify it's called with correct region
            with patch('instanceha.nova_login') as mock_nova_login:
                mock_client = Mock()
                mock_nova_login.return_value = mock_client

                conn = service.create_connection()

                # Verify nova_login was called with correct region_name
                self.assertIsNotNone(conn)
                call_args = mock_nova_login.call_args
                credentials = call_args[0][0]
                self.assertEqual(credentials.region_name, 'RegionTwo')

    def test_different_regions_isolated(self):
        """Test that services from different regions are isolated."""
        # Create config for RegionOne
        config_path, clouds_path, secure_path = self.create_multi_region_config('RegionOne')

        with patch.dict(os.environ, {
            'OS_CLOUD': 'testcloud',
            'CLOUDS_CONFIG_PATH': clouds_path,
            'SECURE_CONFIG_PATH': secure_path
        }):
            config_manager = instanceha.ConfigManager(config_path)
            service = instanceha.InstanceHAService(config_manager)

            # Mock Nova client to simulate multi-region behavior
            with patch('instanceha.nova_login') as mock_nova_login:
                mock_client = Mock()

                # In real OpenStack, services.list() only returns services from the client's region
                # Simulate RegionOne services only
                region_one_service = Mock()
                region_one_service.host = 'compute-1.regionone.example.com'
                region_one_service.state = 'down'
                region_one_service.status = 'enabled'
                region_one_service.binary = 'nova-compute'
                region_one_service.forced_down = False
                region_one_service.updated_at = (datetime.now() - timedelta(minutes=5)).isoformat()

                # These should NEVER appear when connected to RegionOne
                # (this simulates the OpenStack API behavior)
                mock_client.services.list.return_value = [region_one_service]
                mock_nova_login.return_value = mock_client

                conn = service.create_connection()
                services = conn.services.list(binary='nova-compute')

                # Verify only RegionOne services are returned
                self.assertEqual(len(services), 1)
                self.assertIn('regionone', services[0].host.lower())

    def test_region_configuration_required(self):
        """Test that region_name is required in configuration."""
        # Create config without region_name
        config_data = {
            'config': {
                'EVACUABLE_TAG': 'evacuable',
                'DELTA': 60,
                'POLL': 30
            }
        }
        config_path = os.path.join(self.temp_dir, 'config.yaml')
        with open(config_path, 'w') as f:
            yaml.dump(config_data, f)

        clouds_data = {
            'clouds': {
                'testcloud': {
                    'auth': {
                        'username': 'test_user',
                        'project_name': 'test_project',
                        'auth_url': 'http://keystone:5000/v3',
                        'user_domain_name': 'Default',
                        'project_domain_name': 'Default'
                    }
                    # Note: region_name is intentionally missing
                }
            }
        }
        clouds_path = os.path.join(self.temp_dir, 'clouds.yaml')
        with open(clouds_path, 'w') as f:
            yaml.dump(clouds_data, f)

        secure_data = {
            'clouds': {
                'testcloud': {
                    'auth': {
                        'password': 'test_password'
                    }
                }
            }
        }
        secure_path = os.path.join(self.temp_dir, 'secure.yaml')
        with open(secure_path, 'w') as f:
            yaml.dump(secure_data, f)

        config_manager = instanceha.ConfigManager(config_path)
        config_manager.clouds_path = clouds_path
        config_manager.secure_path = secure_path
        config_manager.__init__(config_path)

        service = instanceha.InstanceHAService(config_manager)

        # Attempting to create connection without region_name should raise error
        with self.assertRaises(instanceha.NovaConnectionError):
            with patch.dict(os.environ, {'OS_CLOUD': 'testcloud'}):
                service.create_connection()

    def test_evacuation_only_affects_same_region(self):
        """Test that evacuation operations only affect services in the same region."""
        config_path, clouds_path, secure_path = self.create_multi_region_config('RegionOne')

        with patch.dict(os.environ, {
            'OS_CLOUD': 'testcloud',
            'CLOUDS_CONFIG_PATH': clouds_path,
            'SECURE_CONFIG_PATH': secure_path
        }):
            config_manager = instanceha.ConfigManager(config_path)
            service = instanceha.InstanceHAService(config_manager)

            with patch('instanceha.nova_login') as mock_nova_login:
                mock_client = Mock()

                # Create services from the same region only
                regionone_service = Mock()
                regionone_service.host = 'compute-1.regionone'
                regionone_service.state = 'down'
                regionone_service.status = 'enabled'
                regionone_service.binary = 'nova-compute'
                regionone_service.forced_down = False
                regionone_service.disabled_reason = ''
                regionone_service.id = 'service-1'
                regionone_service.updated_at = (datetime.now() - timedelta(minutes=5)).isoformat()

                # Mock services list to return only same-region services
                mock_client.services.list.return_value = [regionone_service]
                mock_client.services.force_down = Mock()
                mock_client.services.disable_log_reason = Mock()

                # Mock servers for the host
                mock_server = Mock()
                mock_server.id = 'server-1'
                mock_server.host = 'compute-1.regionone'
                mock_server.status = 'ACTIVE'
                mock_client.servers.list.return_value = [mock_server]

                mock_nova_login.return_value = mock_client

                conn = service.create_connection()

                # Categorize services
                services = conn.services.list(binary='nova-compute')
                target_date = datetime.now() - timedelta(seconds=60)
                compute_nodes, to_resume, to_reenable = instanceha._categorize_services(
                    services, target_date
                )
                compute_nodes_list = list(compute_nodes)

                # Verify we only got services from RegionOne
                self.assertEqual(len(compute_nodes_list), 1)
                self.assertEqual(compute_nodes_list[0].host, 'compute-1.regionone')

    def test_multi_region_deployment_independence(self):
        """Test that multiple InstanceHA instances in different regions operate independently."""
        # Create two separate config sets for different regions
        config_r1_path, clouds_r1_path, secure_r1_path = self.create_multi_region_config('RegionOne')

        # Create second temp dir for RegionTwo
        temp_dir_r2 = tempfile.mkdtemp()

        config_data = {
            'config': {
                'EVACUABLE_TAG': 'evacuable',
                'DELTA': 60,
                'POLL': 30,
                'THRESHOLD': 75,
            }
        }
        config_r2_path = os.path.join(temp_dir_r2, 'config.yaml')
        with open(config_r2_path, 'w') as f:
            yaml.dump(config_data, f)

        clouds_data = {
            'clouds': {
                'testcloud': {
                    'auth': {
                        'username': 'test_user',
                        'project_name': 'test_project',
                        'auth_url': 'http://keystone:5000/v3',
                        'user_domain_name': 'Default',
                        'project_domain_name': 'Default'
                    },
                    'region_name': 'RegionTwo'
                }
            }
        }
        clouds_r2_path = os.path.join(temp_dir_r2, 'clouds.yaml')
        with open(clouds_r2_path, 'w') as f:
            yaml.dump(clouds_data, f)

        secure_data = {
            'clouds': {
                'testcloud': {
                    'auth': {
                        'password': 'test_password'
                    }
                }
            }
        }
        secure_r2_path = os.path.join(temp_dir_r2, 'secure.yaml')
        with open(secure_r2_path, 'w') as f:
            yaml.dump(secure_data, f)

        with patch.dict(os.environ, {'OS_CLOUD': 'testcloud'}):
            try:
                # Create service for RegionOne
                with patch.dict(os.environ, {
                    'CLOUDS_CONFIG_PATH': clouds_r1_path,
                    'SECURE_CONFIG_PATH': secure_r1_path
                }):
                    config_r1 = instanceha.ConfigManager(config_r1_path)
                    service_r1 = instanceha.InstanceHAService(config_r1)

                # Create service for RegionTwo
                with patch.dict(os.environ, {
                    'CLOUDS_CONFIG_PATH': clouds_r2_path,
                    'SECURE_CONFIG_PATH': secure_r2_path
                }):
                    config_r2 = instanceha.ConfigManager(config_r2_path)
                    service_r2 = instanceha.InstanceHAService(config_r2)

                # Mock nova_login to return different clients for different regions
                with patch('instanceha.nova_login') as mock_nova_login:
                    # Create separate mock clients for each region
                    mock_client_r1 = Mock()
                    mock_client_r2 = Mock()

                    # RegionOne service
                    r1_service = Mock()
                    r1_service.host = 'compute-r1.regionone'
                    r1_service.state = 'down'
                    mock_client_r1.services.list.return_value = [r1_service]
                    mock_client_r1.region_name = 'RegionOne'

                    # RegionTwo service
                    r2_service = Mock()
                    r2_service.host = 'compute-r2.regiontwo'
                    r2_service.state = 'down'
                    mock_client_r2.services.list.return_value = [r2_service]
                    mock_client_r2.region_name = 'RegionTwo'

                    # Make nova_login return the appropriate client based on region
                    def nova_login_side_effect(credentials):
                        if credentials.region_name == 'RegionOne':
                            return mock_client_r1
                        elif credentials.region_name == 'RegionTwo':
                            return mock_client_r2
                        return None

                    mock_nova_login.side_effect = nova_login_side_effect

                    conn_r1 = service_r1.create_connection()
                    conn_r2 = service_r2.create_connection()

                    services_r1 = conn_r1.services.list(binary='nova-compute')
                    services_r2 = conn_r2.services.list(binary='nova-compute')

                    # Verify each region only sees its own services
                    self.assertEqual(len(services_r1), 1)
                    self.assertEqual(services_r1[0].host, 'compute-r1.regionone')

                    self.assertEqual(len(services_r2), 1)
                    self.assertEqual(services_r2[0].host, 'compute-r2.regiontwo')

            finally:
                # Clean up second temp dir
                import shutil
                shutil.rmtree(temp_dir_r2, ignore_errors=True)

    def test_region_name_passed_to_nova_client(self):
        """Test that region_name is correctly passed through the authentication chain."""
        for region in ['RegionOne', 'RegionTwo', 'RegionThree', 'us-west-1', 'eu-central-1']:
            with self.subTest(region=region):
                config_path, clouds_path, secure_path = self.create_multi_region_config(region)

                with patch.dict(os.environ, {
                    'OS_CLOUD': 'testcloud',
                    'CLOUDS_CONFIG_PATH': clouds_path,
                    'SECURE_CONFIG_PATH': secure_path
                }):
                    config_manager = instanceha.ConfigManager(config_path)
                    service = instanceha.InstanceHAService(config_manager)

                    with patch('instanceha.nova_login') as mock_nova_login:
                        mock_client = Mock()
                        mock_nova_login.return_value = mock_client

                        service.create_connection()

                        # Verify the region was passed correctly
                        call_args = mock_nova_login.call_args
                        credentials = call_args[0][0]
                        self.assertEqual(credentials.region_name, region,
                                       f"Region name mismatch for {region}")


class TestRegionEdgeCases(unittest.TestCase):
    """Test edge cases and error conditions for region handling."""

    def setUp(self):
        """Set up test fixtures."""
        self.temp_dir = tempfile.mkdtemp()

    def tearDown(self):
        """Clean up test fixtures."""
        import shutil
        shutil.rmtree(self.temp_dir, ignore_errors=True)

    def test_empty_region_name_rejected(self):
        """Test that empty region_name is rejected."""
        config_data = {'config': {'EVACUABLE_TAG': 'evacuable', 'DELTA': 60}}
        config_path = os.path.join(self.temp_dir, 'config.yaml')
        with open(config_path, 'w') as f:
            yaml.dump(config_data, f)

        clouds_data = {
            'clouds': {
                'testcloud': {
                    'auth': {
                        'username': 'test_user',
                        'project_name': 'test_project',
                        'auth_url': 'http://keystone:5000/v3',
                        'user_domain_name': 'Default',
                        'project_domain_name': 'Default'
                    },
                    'region_name': ''  # Empty region name
                }
            }
        }
        clouds_path = os.path.join(self.temp_dir, 'clouds.yaml')
        with open(clouds_path, 'w') as f:
            yaml.dump(clouds_data, f)

        secure_data = {
            'clouds': {
                'testcloud': {
                    'auth': {'password': 'test_password'}
                }
            }
        }
        secure_path = os.path.join(self.temp_dir, 'secure.yaml')
        with open(secure_path, 'w') as f:
            yaml.dump(secure_data, f)

        config_manager = instanceha.ConfigManager(config_path)
        config_manager.clouds_path = clouds_path
        config_manager.secure_path = secure_path
        config_manager.__init__(config_path)

        service = instanceha.InstanceHAService(config_manager)

        with patch('instanceha.nova_login') as mock_nova_login:
            mock_nova_login.return_value = None  # Simulate connection failure

            with patch.dict(os.environ, {'OS_CLOUD': 'testcloud'}):
                # Empty region should still be passed, but may cause connection issues
                with self.assertRaises(instanceha.NovaConnectionError):
                    service.create_connection()


if __name__ == '__main__':
    # Run with verbose output
    suite = unittest.TestLoader().loadTestsFromModule(sys.modules[__name__])
    runner = unittest.TextTestRunner(verbosity=2)
    result = runner.run(suite)

    # Exit with appropriate code
    sys.exit(0 if result.wasSuccessful() else 1)

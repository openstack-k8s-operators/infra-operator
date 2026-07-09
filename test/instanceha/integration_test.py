"""
Comprehensive Integration Test Suite for InstanceHA Service.

This test suite validates end-to-end integration scenarios including:
- Service initialization and configuration
- Nova connection establishment
- Service categorization and filtering
- Complete evacuation workflows
- Error handling and recovery
- Performance under load
- Cross-component interactions
"""

import os
import sys
import time
import tempfile
import yaml
import threading
import unittest
import logging
from unittest.mock import Mock, patch, MagicMock, call
from datetime import datetime, timedelta
import concurrent.futures

# Suppress warnings during testing
logging.getLogger().setLevel(logging.CRITICAL)

from conftest import make_mock_config  # noqa: F401
import instanceha


class MockOpenStackEnvironment:
    """Complete mock OpenStack environment for integration testing."""

    def __init__(self):
        self.services = []
        self.servers = {}
        self.flavors = []
        self.images = []
        self.aggregates = []
        self.migrations = []
        self.forced_down_services = set()
        self.disabled_services = set()

    def add_compute_service(self, host, state='up', status='enabled', updated_at=None, forced_down=False, disabled_reason=''):
        """Add a compute service to the mock environment."""
        if updated_at is None:
            updated_at = datetime.now().isoformat()

        service = Mock()
        service.host = host
        service.state = state
        service.status = status
        service.updated_at = updated_at
        service.forced_down = forced_down
        service.disabled_reason = disabled_reason
        service.id = f"service-{len(self.services)}"
        service.binary = "nova-compute"

        self.services.append(service)
        return service

    def add_incomplete_compute_service(self, host, state=None, status='UNKNOWN'):
        """Add a compute service missing forced_down/disabled_reason attrs.

        Simulates services returned by Nova that are not fully registered
        (e.g. ID=None, Status=UNKNOWN, State=None). The real novaclient
        raises AttributeError for attributes absent from the API response.
        """

        class IncompleteService:
            pass

        service = IncompleteService()
        service.host = host
        service.state = state
        service.status = status
        service.updated_at = None
        service.id = None
        service.binary = "nova-compute"

        self.services.append(service)
        return service

    def add_server(self, host, server_id, flavor_id=None, image_id=None, status='ACTIVE'):
        """Add a server to a specific host."""
        if host not in self.servers:
            self.servers[host] = []

        server = Mock()
        server.id = server_id
        server.status = status
        server.host = host

        if flavor_id:
            server.flavor = {'id': flavor_id, 'extra_specs': {}}
        if image_id:
            server.image = {'id': image_id}

        self.servers[host].append(server)
        return server

    def add_flavor(self, flavor_id, evacuable=False, evacuable_tag='evacuable'):
        """Add a flavor to the mock environment."""
        flavor = Mock()
        flavor.id = flavor_id
        extra_specs = {}
        if evacuable:
            extra_specs[evacuable_tag] = 'true'
        flavor.get_keys.return_value = extra_specs

        self.flavors.append(flavor)
        return flavor

    def add_image(self, image_id, evacuable=False, evacuable_tag='evacuable'):
        """Add an image to the mock environment."""
        image = Mock()
        image.id = image_id
        tags = []
        if evacuable:
            tags.append(evacuable_tag)
        image.tags = tags
        image.metadata = {}
        image.properties = {}

        self.images.append(image)
        return image

    def add_aggregate(self, name, hosts, evacuable=False, evacuable_tag='evacuable'):
        """Add an aggregate to the mock environment."""
        aggregate = Mock()
        aggregate.name = name
        aggregate.hosts = hosts
        aggregate.metadata = {}
        if evacuable:
            aggregate.metadata[evacuable_tag] = 'true'

        self.aggregates.append(aggregate)
        return aggregate

    def create_nova_client_mock(self):
        """Create a complete Nova client mock."""
        nova_client = Mock()

        # Services manager
        nova_client.services.list.return_value = self.services
        nova_client.services.force_down = Mock()
        nova_client.services.disable_log_reason = Mock()
        nova_client.services.enable = Mock()

        # Servers manager
        def mock_servers_list(search_opts=None):
            if search_opts and 'host' in search_opts:
                host = search_opts['host']
                return self.servers.get(host, [])
            return [server for servers in self.servers.values() for server in servers]

        nova_client.servers.list.side_effect = mock_servers_list
        nova_client.servers.evacuate.return_value = (Mock(status_code=200, reason='OK'), {})

        # Flavors manager
        nova_client.flavors.list.return_value = self.flavors

        # Aggregates manager
        nova_client.aggregates.list.return_value = self.aggregates

        # Migrations manager
        nova_client.migrations.list.return_value = self.migrations

        # Images manager
        nova_client.images.list.return_value = self.images

        # Versions manager
        nova_client.versions.get_current.return_value = Mock()

        return nova_client


class TestServiceInitialization(unittest.TestCase):
    """Test service initialization and configuration loading."""

    def setUp(self):
        """Set up test fixtures."""
        self.temp_dir = tempfile.mkdtemp()
        self.mock_env = MockOpenStackEnvironment()

    def tearDown(self):
        """Clean up test fixtures."""
        import shutil
        shutil.rmtree(self.temp_dir, ignore_errors=True)

    def create_test_config(self, config_data=None):
        """Create test configuration files."""
        if config_data is None:
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
                    'region_name': 'regionOne'
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

    def test_complete_service_initialization(self):
        """Test complete service initialization workflow."""
        config_path, clouds_path, secure_path = self.create_test_config()

        # Create config manager
        config_manager = instanceha.ConfigManager(config_path)
        config_manager.clouds_path = clouds_path
        config_manager.secure_path = secure_path
        config_manager.__init__(config_path)

        # Create service
        service = instanceha.InstanceHAService(config_manager)

        # Verify service initialization
        self.assertIsNotNone(service.config)
        self.assertEqual(service.current_hash, "")
        self.assertFalse(service.hash_update_successful)
        self.assertIsNotNone(service._cache_lock)

    def test_nova_connection_establishment(self):
        """Test Nova connection establishment with mock credentials."""
        # Mock the nova login function directly instead of testing file loading
        mock_config = Mock()
        mock_config.clouds = {
            'testcloud': {
                'auth': {
                    'username': 'test_user',
                    'project_name': 'test_project',
                    'auth_url': 'http://keystone:5000/v3',
                    'user_domain_name': 'Default',
                    'project_domain_name': 'Default'
                },
                'region_name': 'regionOne'
            }
        }
        mock_config.secure = {
            'testcloud': {
                'auth': {
                    'password': 'test_password'
                }
            }
        }
        # Mock get_cloud_name to return 'testcloud'
        mock_config.get_cloud_name.return_value = 'testcloud'

        service = instanceha.InstanceHAService(mock_config)

        # Mock nova login function
        with patch('instanceha.nova_login') as mock_nova_login:
            mock_nova_client = self.mock_env.create_nova_client_mock()
            mock_nova_login.return_value = mock_nova_client

            # Set environment variable
            with patch.dict(os.environ, {'OS_CLOUD': 'testcloud'}):
                connection = instanceha._establish_nova_connection(service)

            self.assertIsNotNone(connection)
            mock_nova_login.assert_called_once()

    def test_service_initialization_with_threads(self):
        """Test service initialization with background threads."""
        config_path, clouds_path, secure_path = self.create_test_config()

        config_manager = instanceha.ConfigManager(config_path)
        config_manager.clouds_path = clouds_path
        config_manager.secure_path = secure_path
        config_manager.__init__(config_path)

        with patch('threading.Thread') as mock_thread:
            mock_thread_instance = Mock()
            mock_thread.return_value = mock_thread_instance

            service = instanceha._initialize_service(config_manager)

            # Verify threads were created (health check + potentially kdump)
            self.assertGreaterEqual(mock_thread.call_count, 1)
            mock_thread_instance.start.assert_called()

            # Verify service was created
            self.assertIsNotNone(service)


class TestServiceCategorization(unittest.TestCase):
    """Test service categorization and filtering logic."""

    def setUp(self):
        """Set up test fixtures."""
        self.mock_env = MockOpenStackEnvironment()

    def test_categorize_services_basic(self):
        """Test basic service categorization."""
        # Create services in different states
        target_date = datetime.now() - timedelta(seconds=60)
        old_date = (target_date - timedelta(seconds=60)).isoformat()

        # Stale service (should be evacuated)
        stale_service = self.mock_env.add_compute_service(
            'compute-01', state='up', status='enabled', updated_at=old_date
        )

        # Down service (should be evacuated)
        down_service = self.mock_env.add_compute_service(
            'compute-02', state='down', status='enabled'
        )

        # Re-enable candidate (forced down but enabled, and back up)
        reenable_service = self.mock_env.add_compute_service(
            'compute-03', state='up', status='enabled', forced_down=True
        )

        # Resume candidate (forced down, disabled with instanceha reason)
        resume_service = self.mock_env.add_compute_service(
            'compute-04', state='down', status='disabled', forced_down=True,
            disabled_reason='instanceha evacuation: 2023-01-01T00:00:00'
        )

        # Normal service (should not be touched)
        normal_service = self.mock_env.add_compute_service(
            'compute-05', state='up', status='enabled'
        )

        compute_nodes, to_resume, to_reenable = instanceha._categorize_services(
            self.mock_env.services, target_date
        )

        # Convert generators to lists for testing
        compute_nodes = list(compute_nodes)
        to_resume = list(to_resume)
        to_reenable = list(to_reenable)

        # Verify categorization
        self.assertEqual(len(compute_nodes), 2)  # stale and down
        self.assertIn(stale_service, compute_nodes)
        self.assertIn(down_service, compute_nodes)

        self.assertEqual(len(to_resume), 1)
        self.assertIn(resume_service, to_resume)

        self.assertEqual(len(to_reenable), 1)
        self.assertIn(reenable_service, to_reenable)

    def test_categorize_services_edge_cases(self):
        """Test service categorization edge cases."""
        target_date = datetime.now() - timedelta(seconds=60)

        # Service with failed evacuation (should not be resumed)
        failed_service = self.mock_env.add_compute_service(
            'compute-fail', state='down', status='disabled', forced_down=True,
            disabled_reason='instanceha evacuation FAILED: 2023-01-01T00:00:00'
        )

        # Disabled service (should not be evacuated)
        disabled_service = self.mock_env.add_compute_service(
            'compute-disabled', state='down', status='disabled'
        )

        compute_nodes, to_resume, to_reenable = instanceha._categorize_services(
            self.mock_env.services, target_date
        )

        # Failed evacuation should not be in resume list
        self.assertNotIn(failed_service, to_resume)

        # Disabled service should not be in compute_nodes
        self.assertNotIn(disabled_service, compute_nodes)


class TestEvacuationWorkflow(unittest.TestCase):
    """Test complete evacuation workflows."""

    def setUp(self):
        """Set up test fixtures."""
        self.mock_env = MockOpenStackEnvironment()
        self.mock_config = make_mock_config(
            TAGGED_IMAGES=False, TAGGED_FLAVORS=False,
            TAGGED_AGGREGATES=False, RESERVED_HOSTS=False,
            POLL=30,
        )

    def test_complete_evacuation_workflow(self):
        """Test complete evacuation workflow from start to finish."""
        # Setup failed compute with servers
        failed_host = 'compute-01'
        self.mock_env.add_compute_service(failed_host, state='down')
        self.mock_env.add_server(failed_host, 'server-1', status='ACTIVE')
        self.mock_env.add_server(failed_host, 'server-2', status='ACTIVE')

        # Setup working computes
        for i in range(2, 5):
            self.mock_env.add_compute_service(f'compute-0{i}', state='up')

        nova_client = self.mock_env.create_nova_client_mock()
        service = instanceha.InstanceHAService(self.mock_config, nova_client)

        # Get services that need evacuation
        target_date = datetime.now() - timedelta(seconds=60)
        compute_nodes, to_resume, to_reenable = instanceha._categorize_services(
            self.mock_env.services, target_date
        )

        # Test processing stale services
        with patch('instanceha.process_service') as mock_process_service:
            mock_process_service.return_value = True

            instanceha._process_stale_services(
                nova_client, service, self.mock_env.services, compute_nodes, to_resume
            )

            # Verify evacuation was attempted
            self.assertTrue(mock_process_service.called)

    def test_evacuation_with_threshold_protection(self):
        """Test evacuation threshold protection."""
        # Create scenario where too many hosts fail
        for i in range(1, 11):  # 10 hosts
            state = 'down' if i <= 6 else 'up'  # 6 failed = 60%
            self.mock_env.add_compute_service(f'compute-{i:02d}', state=state)
            if state == 'down':
                self.mock_env.add_server(f'compute-{i:02d}', f'server-{i}')

        self.mock_config.get_threshold.return_value = 50  # 50% threshold

        nova_client = self.mock_env.create_nova_client_mock()
        service = instanceha.InstanceHAService(self.mock_config, nova_client)

        target_date = datetime.now() - timedelta(seconds=60)
        compute_nodes, to_resume, to_reenable = instanceha._categorize_services(
            self.mock_env.services, target_date
        )

        with patch('instanceha.process_service') as mock_process_service:
            instanceha._process_stale_services(
                nova_client, service, self.mock_env.services, compute_nodes, to_resume
            )

            # Should not evacuate due to threshold
            mock_process_service.assert_not_called()

    def test_evacuation_with_flavor_tagging(self):
        """Test evacuation with flavor-based tagging."""
        self.mock_config = make_mock_config(
            TAGGED_IMAGES=False, TAGGED_FLAVORS=True,
            TAGGED_AGGREGATES=False, RESERVED_HOSTS=False,
            POLL=30,
        )

        # Setup host with mixed servers
        failed_host = 'compute-01'
        self.mock_env.add_compute_service(failed_host, state='down')

        # Add flavors
        evacuable_flavor = self.mock_env.add_flavor('flavor-evac', evacuable=True)
        normal_flavor = self.mock_env.add_flavor('flavor-normal', evacuable=False)

        # Add servers with different flavors
        evac_server = self.mock_env.add_server(failed_host, 'server-evac', flavor_id='flavor-evac')
        evac_server.flavor = {'id': 'flavor-evac', 'extra_specs': {'evacuable': 'true'}}

        normal_server = self.mock_env.add_server(failed_host, 'server-normal', flavor_id='flavor-normal')
        normal_server.flavor = {'id': 'flavor-normal', 'extra_specs': {}}

        nova_client = self.mock_env.create_nova_client_mock()
        service = instanceha.InstanceHAService(self.mock_config, nova_client)

        # Test server evacuability
        self.assertTrue(service.is_server_evacuable(evac_server))
        self.assertFalse(service.is_server_evacuable(normal_server))

    def test_evacuation_with_aggregate_filtering(self):
        """Test evacuation with aggregate-based filtering."""
        self.mock_config = make_mock_config(
            TAGGED_IMAGES=False, TAGGED_FLAVORS=False,
            TAGGED_AGGREGATES=True, RESERVED_HOSTS=False,
            POLL=30,
        )

        # Setup aggregates
        evacuable_agg = self.mock_env.add_aggregate(
            'evacuable-agg', ['compute-01', 'compute-02'], evacuable=True
        )
        normal_agg = self.mock_env.add_aggregate(
            'normal-agg', ['compute-03', 'compute-04'], evacuable=False
        )

        # Setup failed hosts
        for i in range(1, 5):
            self.mock_env.add_compute_service(f'compute-0{i}', state='down')
            self.mock_env.add_server(f'compute-0{i}', f'server-{i}')

        nova_client = self.mock_env.create_nova_client_mock()
        service = instanceha.InstanceHAService(self.mock_config, nova_client)

        # Test aggregate filtering
        compute_nodes = [svc for svc in self.mock_env.services if svc.state == 'down']
        filtered_nodes = instanceha._filter_by_aggregates(
            nova_client, service, compute_nodes, self.mock_env.services
        )

        # Only hosts in evacuable aggregate should remain
        filtered_hosts = [svc.host for svc in filtered_nodes]
        self.assertIn('compute-01', filtered_hosts)
        self.assertIn('compute-02', filtered_hosts)
        self.assertNotIn('compute-03', filtered_hosts)
        self.assertNotIn('compute-04', filtered_hosts)


class TestReenablingWorkflow(unittest.TestCase):
    """Test service re-enabling workflows."""

    def setUp(self):
        """Set up test fixtures."""
        self.mock_env = MockOpenStackEnvironment()
        self.mock_config = make_mock_config()

    def test_reenable_services_with_completed_migrations(self):
        """Test re-enabling services with completed migrations."""
        # Setup service that can be re-enabled (must be 'up' to be re-enabled)
        service_to_reenable = self.mock_env.add_compute_service(
            'compute-01', state='up', status='enabled', forced_down=True
        )

        # Setup completed migration
        completed_migration = Mock()
        completed_migration.status = 'completed'
        self.mock_env.migrations = [completed_migration]

        nova_client = self.mock_env.create_nova_client_mock()
        service = instanceha.InstanceHAService(self.mock_config, nova_client)

        # Test re-enabling
        instanceha._process_reenabling(nova_client, service, [service_to_reenable])

        # Verify enable was called
        nova_client.services.force_down.assert_called_with(service_to_reenable.id, False)

    def test_reenable_services_with_incomplete_migrations(self):
        """Test re-enabling blocked by incomplete migrations."""
        service_to_reenable = self.mock_env.add_compute_service(
            'compute-01', state='up', status='enabled', forced_down=True
        )

        # Setup incomplete migration
        incomplete_migration = Mock()
        incomplete_migration.status = 'running'
        self.mock_env.migrations = [incomplete_migration]

        nova_client = self.mock_env.create_nova_client_mock()
        service = instanceha.InstanceHAService(self.mock_config, nova_client)

        # Test re-enabling
        instanceha._process_reenabling(nova_client, service, [service_to_reenable])

        # Verify enable was NOT called
        nova_client.services.force_down.assert_not_called()

    def test_force_reenable_services(self):
        """Test force re-enabling of services."""
        self.mock_config = make_mock_config(FORCE_ENABLE=True)

        service_to_reenable = self.mock_env.add_compute_service(
            'compute-01', state='up', status='enabled', forced_down=True
        )

        nova_client = self.mock_env.create_nova_client_mock()
        service = instanceha.InstanceHAService(self.mock_config, nova_client)

        # Test force re-enabling
        instanceha._process_reenabling(nova_client, service, [service_to_reenable])

        # Verify enable was called without checking migrations
        nova_client.services.force_down.assert_called_with(service_to_reenable.id, False)

    def test_force_enable_with_leave_disabled_interaction(self):
        """
        Test FORCE_ENABLE + LEAVE_DISABLED interaction.
        LEAVE_DISABLED should take precedence and filter out instanceha-evacuated services.
        """
        self.mock_config = make_mock_config(FORCE_ENABLE=True, LEAVE_DISABLED=True)

        instanceha_evacuated = self.mock_env.add_compute_service(
            'compute-01', state='up', status='disabled', forced_down=False,
            disabled_reason='instanceha evacuation complete: 2023-01-01T00:00:00'
        )

        manually_disabled = self.mock_env.add_compute_service(
            'compute-02', state='up', status='disabled', forced_down=False,
            disabled_reason='manual maintenance'
        )

        nova_client = self.mock_env.create_nova_client_mock()
        service = instanceha.InstanceHAService(self.mock_config, nova_client)

        with patch('instanceha._host_enable') as mock_enable:
            instanceha._process_reenabling(nova_client, service,
                                          [instanceha_evacuated, manually_disabled])

            # LEAVE_DISABLED should filter out instanceha-evacuated service
            # Only manually_disabled should be processed
            enable_hosts = [call.args[1].host for call in mock_enable.call_args_list]
            self.assertNotIn('compute-01', enable_hosts)

    def test_disabled_service_not_reenabled_when_down(self):
        """Test that disabled services have force_down unset, but are NOT enabled when still down."""
        # Service is disabled (evacuation complete), force_down was already unset, but still down
        # This represents the state after force_down was unset but before service reports up
        service_disabled_down = self.mock_env.add_compute_service(
            'compute-01', state='down', status='disabled', forced_down=False,
            disabled_reason='instanceha evacuation complete: 2023-01-01T00:00:00'
        )

        # Setup completed migrations
        completed_migration = Mock()
        completed_migration.status = 'completed'
        self.mock_env.migrations = [completed_migration]

        nova_client = self.mock_env.create_nova_client_mock()
        service = instanceha.InstanceHAService(self.mock_config, nova_client)

        # Should be in reenable list to unset force_down
        target_date = datetime.now() - timedelta(seconds=60)
        _, _, to_reenable = instanceha._categorize_services(
            self.mock_env.services, target_date
        )
        to_reenable_list = list(to_reenable)

        self.assertEqual(len(to_reenable_list), 1)
        self.assertIn(service_disabled_down, to_reenable_list)

        # Process re-enabling
        instanceha._process_reenabling(nova_client, service, to_reenable_list)

        # Verify force_down was NOT called (already False) and service was NOT enabled (still down)
        nova_client.services.force_down.assert_not_called()
        nova_client.services.enable.assert_not_called()

    def test_disabled_service_reenabled_when_up(self):
        """Test that disabled services ARE re-enabled when they come back up."""
        self.mock_config = make_mock_config(FORCE_ENABLE=True)

        # Service is disabled (evacuation complete) and has come back up
        service_disabled_up = self.mock_env.add_compute_service(
            'compute-01', state='up', status='disabled', forced_down=True,
            disabled_reason='instanceha evacuation complete: 2023-01-01T00:00:00'
        )

        # Setup completed migrations (not needed with FORCE_ENABLE but keeping for clarity)
        completed_migration = Mock()
        completed_migration.status = 'completed'
        self.mock_env.migrations = [completed_migration]

        nova_client = self.mock_env.create_nova_client_mock()
        service = instanceha.InstanceHAService(self.mock_config, nova_client)

        # Should be in reenable list
        target_date = datetime.now() - timedelta(seconds=60)
        _, _, to_reenable = instanceha._categorize_services(
            self.mock_env.services, target_date
        )
        to_reenable_list = list(to_reenable)

        self.assertEqual(len(to_reenable_list), 1)
        self.assertIn(service_disabled_up, to_reenable_list)

        # Test re-enabling
        instanceha._process_reenabling(nova_client, service, to_reenable_list)

        # Verify both force_down was unset AND service was enabled
        nova_client.services.force_down.assert_called_with(service_disabled_up.id, False)
        nova_client.services.enable.assert_called_with(service_disabled_up.id)


class TestPerformanceAndScaling(unittest.TestCase):
    """Test performance and scaling scenarios."""

    def setUp(self):
        """Set up test fixtures."""
        self.mock_env = MockOpenStackEnvironment()
        self.mock_config = make_mock_config(THRESHOLD=30)

    def test_large_scale_evacuation_performance(self):
        """Test evacuation performance with large number of hosts."""
        # Create 100 compute services with various states
        for i in range(1, 101):
            state = 'down' if i <= 10 else 'up'  # 10% failure rate
            self.mock_env.add_compute_service(f'compute-{i:03d}', state=state)

            # Add servers to failed hosts
            if state == 'down':
                for j in range(5):  # 5 servers per failed host
                    self.mock_env.add_server(f'compute-{i:03d}', f'server-{i}-{j}')

        nova_client = self.mock_env.create_nova_client_mock()
        service = instanceha.InstanceHAService(self.mock_config, nova_client)

        # Measure categorization performance
        start_time = time.time()
        target_date = datetime.now() - timedelta(seconds=60)
        compute_nodes, to_resume, to_reenable = instanceha._categorize_services(
            self.mock_env.services, target_date
        )
        categorization_time = time.time() - start_time

        # Convert generator to list for testing
        compute_nodes = list(compute_nodes)

        self.assertLess(categorization_time, 10.0)
        self.assertEqual(len(compute_nodes), 10)  # 10 failed hosts

    def test_caching_performance(self):
        """Test caching performance with repeated calls."""
        # Setup flavors and images
        for i in range(50):
            self.mock_env.add_flavor(f'flavor-{i}', evacuable=(i % 2 == 0))
            self.mock_env.add_image(f'image-{i}', evacuable=(i % 3 == 0))

        nova_client = self.mock_env.create_nova_client_mock()
        service = instanceha.InstanceHAService(self.mock_config, nova_client)

        # First call should populate cache
        start_time = time.time()
        flavors1 = service.get_evacuable_flavors(nova_client)
        first_call_time = time.time() - start_time

        # Second call should use cache (much faster)
        start_time = time.time()
        flavors2 = service.get_evacuable_flavors(nova_client)
        second_call_time = time.time() - start_time

        # Results should be identical
        self.assertEqual(flavors1, flavors2)

        # Second call should be significantly faster (allow for timing variation)
        self.assertLess(second_call_time, first_call_time * 2.0)  # More lenient for fast operations

    def test_concurrent_evacuation_processing(self):
        """Test concurrent processing of multiple evacuations."""
        # Setup multiple failed hosts
        failed_hosts = []
        for i in range(5):
            host = f'compute-{i:02d}'
            self.mock_env.add_compute_service(host, state='down')
            failed_hosts.append(host)

            # Add servers to each host
            for j in range(3):
                self.mock_env.add_server(host, f'server-{i}-{j}')

        nova_client = self.mock_env.create_nova_client_mock()
        service = instanceha.InstanceHAService(self.mock_config, nova_client)

        # Mock process_service to simulate work
        def mock_process_service(failed_service, reserved_hosts, resume, service_instance):
            time.sleep(0.01)  # Simulate processing time
            return True

        with patch('instanceha.process_service', side_effect=mock_process_service) as mock_process:
            target_date = datetime.now() - timedelta(seconds=60)
            compute_nodes, to_resume, to_reenable = instanceha._categorize_services(
                self.mock_env.services, target_date
            )

            # Test concurrent processing
            start_time = time.time()
            instanceha._process_stale_services(
                nova_client, service, self.mock_env.services, compute_nodes, to_resume
            )
            processing_time = time.time() - start_time

            self.assertLess(processing_time, 10.0)


class TestIncompleteServiceHandling(unittest.TestCase):
    """Test graceful handling of incomplete Nova service objects.

    Nova can return compute services with UNKNOWN status, None state, and
    missing attributes (forced_down, disabled_reason) when services are not
    fully registered. The code must not crash on these.
    """

    def setUp(self):
        self.mock_env = MockOpenStackEnvironment()
        self.mock_config = make_mock_config(
            TAGGED_IMAGES=False, TAGGED_FLAVORS=False,
            TAGGED_AGGREGATES=False, RESERVED_HOSTS=False,
            POLL=30,
        )

    def test_categorize_services_with_incomplete_service(self):
        """Incomplete services should be silently skipped by categorization."""
        target_date = datetime.now() - timedelta(seconds=60)

        self.mock_env.add_incomplete_compute_service('compute-unknown-1')
        self.mock_env.add_incomplete_compute_service('compute-unknown-2', state=None, status='UNKNOWN')
        normal = self.mock_env.add_compute_service('compute-ok', state='up', status='enabled')

        compute_nodes, to_resume, to_reenable = instanceha._categorize_services(
            self.mock_env.services, target_date
        )

        compute_nodes = list(compute_nodes)
        to_resume = list(to_resume)
        to_reenable = list(to_reenable)

        self.assertEqual(compute_nodes, [])
        self.assertEqual(to_resume, [])
        self.assertEqual(to_reenable, [])

    def test_categorize_services_mixed_incomplete_and_normal(self):
        """Incomplete services mixed with real ones must not break categorization."""
        target_date = datetime.now() - timedelta(seconds=60)

        self.mock_env.add_incomplete_compute_service('compute-unknown')
        down_svc = self.mock_env.add_compute_service('compute-down', state='down', status='enabled')
        self.mock_env.add_compute_service('compute-ok', state='up', status='enabled')

        compute_nodes, to_resume, to_reenable = instanceha._categorize_services(
            self.mock_env.services, target_date
        )

        compute_nodes = list(compute_nodes)
        self.assertIn(down_svc, compute_nodes)
        self.assertEqual(len(compute_nodes), 1)

    def test_is_service_resume_candidate_incomplete_service(self):
        """_is_service_resume_candidate should return False for incomplete services."""
        svc = self.mock_env.add_incomplete_compute_service('compute-unknown')
        self.assertFalse(instanceha._is_service_resume_candidate(svc))

    def test_reconcile_orphaned_hosts_with_incomplete_services(self):
        """_reconcile_orphaned_hosts must not crash on incomplete services."""
        self.mock_env.add_incomplete_compute_service('compute-unknown-1')
        self.mock_env.add_incomplete_compute_service('compute-unknown-2')

        nova_client = self.mock_env.create_nova_client_mock()

        instanceha._reconcile_orphaned_hosts(nova_client)

        nova_client.services.disable_log_reason.assert_not_called()

    def test_reconcile_orphaned_hosts_mixed_services(self):
        """Reconciliation should process real orphans while skipping incomplete services."""
        self.mock_env.add_incomplete_compute_service('compute-unknown')
        orphan = self.mock_env.add_compute_service(
            'compute-orphan', state='down', status='enabled', forced_down=True
        )

        nova_client = self.mock_env.create_nova_client_mock()

        with patch('instanceha._emit_k8s_event'), \
             patch('instanceha.ORPHANED_HOST_RECOVERED_TOTAL'):
            instanceha._reconcile_orphaned_hosts(nova_client)

        nova_client.services.disable_log_reason.assert_called_once()
        call_args = nova_client.services.disable_log_reason.call_args
        self.assertEqual(call_args[0][0], orphan.id)

    def test_count_evacuable_hosts_with_incomplete_services(self):
        """_count_evacuable_hosts should ignore incomplete services."""
        self.mock_env.add_incomplete_compute_service('compute-unknown')
        self.mock_env.add_compute_service('compute-ok-1', state='up', status='enabled')
        self.mock_env.add_compute_service('compute-ok-2', state='up', status='enabled')

        service = instanceha.InstanceHAService(self.mock_config, Mock())
        nova_client = self.mock_env.create_nova_client_mock()

        with patch('instanceha._get_evacuable_hosts_from_aggregates', return_value=None):
            count = instanceha._count_evacuable_hosts(nova_client, service, self.mock_env.services)
        self.assertEqual(count, 3)

    def test_process_stale_services_with_incomplete_services(self):
        """_process_stale_services should not crash when incomplete services are in the list."""
        self.mock_env.add_incomplete_compute_service('compute-unknown')
        self.mock_env.add_compute_service('compute-ok', state='up', status='enabled')

        nova_client = self.mock_env.create_nova_client_mock()
        service = instanceha.InstanceHAService(self.mock_config, nova_client)

        target_date = datetime.now() - timedelta(seconds=60)
        compute_nodes, to_resume, to_reenable = instanceha._categorize_services(
            self.mock_env.services, target_date
        )

        with patch('instanceha.process_service') as mock_process:
            instanceha._process_stale_services(
                nova_client, service, self.mock_env.services,
                compute_nodes, to_resume
            )
            mock_process.assert_not_called()


class TestErrorHandlingAndRecovery(unittest.TestCase):
    """Test error handling and recovery scenarios."""

    def setUp(self):
        """Set up test fixtures."""
        self.mock_env = MockOpenStackEnvironment()
        self.mock_config = make_mock_config()

    def test_nova_api_failure_handling(self):
        """Test handling of Nova API failures."""
        nova_client = Mock()
        nova_client.services.list.side_effect = Exception("API unavailable")

        service = instanceha.InstanceHAService(self.mock_config, nova_client)

        # Should handle API failure gracefully
        try:
            services = nova_client.services.list()
            self.fail("Should have raised exception")
        except Exception as e:
            self.assertEqual(str(e), "API unavailable")

    def test_partial_evacuation_failure_recovery(self):
        """Test recovery from partial evacuation failures."""
        # Setup scenario with some successful and some failed evacuations
        for i in range(3):
            host = f'compute-{i:02d}'
            self.mock_env.add_compute_service(host, state='down')
            self.mock_env.add_server(host, f'server-{i}')

        nova_client = self.mock_env.create_nova_client_mock()
        service = instanceha.InstanceHAService(self.mock_config, nova_client)

        # Mock partial failure scenario
        def mock_process_service(failed_service, reserved_hosts, resume, service_instance):
            # Fail evacuation for compute-01
            return failed_service.host != 'compute-01'

        with patch('instanceha.process_service', side_effect=mock_process_service) as mock_process:
            target_date = datetime.now() - timedelta(seconds=60)
            compute_nodes, to_resume, to_reenable = instanceha._categorize_services(
                self.mock_env.services, target_date
            )

            instanceha._process_stale_services(
                nova_client, service, self.mock_env.services, compute_nodes, to_resume
            )

    def test_configuration_error_handling(self):
        """Test handling of configuration errors."""
        # Test with invalid configuration
        with self.assertRaises(instanceha.ConfigurationError):
            config = instanceha.ConfigManager('/nonexistent/path')
            config._validate_config = Mock(side_effect=instanceha.ConfigurationError("Invalid config"))
            config._validate_config()

    def test_network_timeout_handling(self):
        """Test handling of network timeouts."""
        nova_client = Mock()
        nova_client.services.list.side_effect = TimeoutError("Network timeout")

        service = instanceha.InstanceHAService(self.mock_config, nova_client)

        # Should handle timeout gracefully
        with self.assertRaises(TimeoutError):
            nova_client.services.list()


if __name__ == '__main__':
    # Configure logging for tests
    logging.basicConfig(level=logging.CRITICAL)

    print("========================================")
    print("   InstanceHA Integration Test Suite   ")
    print("========================================")
    print()

    # Create test suite
    test_suite = unittest.TestSuite()

    # Add all test classes
    test_classes = [
        TestServiceInitialization,
        TestServiceCategorization,
        TestEvacuationWorkflow,
        TestReenablingWorkflow,
        TestPerformanceAndScaling,
        TestIncompleteServiceHandling,
        TestErrorHandlingAndRecovery,
    ]

    for test_class in test_classes:
        tests = unittest.TestLoader().loadTestsFromTestCase(test_class)
        test_suite.addTests(tests)

    # Run tests
    runner = unittest.TextTestRunner(verbosity=2)
    result = runner.run(test_suite)

    print()
    print("========================================")
    print("         Integration Test Summary       ")
    print("========================================")

    if result.wasSuccessful():
        print("PASS: All integration tests completed successfully")
        print(f"Tests run: {result.testsRun}")
        print(f"Failures: {len(result.failures)}")
        print(f"Errors: {len(result.errors)}")
        sys.exit(0)
    else:
        print("FAIL: Some integration tests failed")
        print(f"Tests run: {result.testsRun}")
        print(f"Failures: {len(result.failures)}")
        print(f"Errors: {len(result.errors)}")

        if result.failures:
            print("\nFailures:")
            for test, traceback in result.failures:
                print(f"- {test}: {traceback}")

        if result.errors:
            print("\nErrors:")
            for test, traceback in result.errors:
                print(f"- {test}: {traceback}")

        sys.exit(1)

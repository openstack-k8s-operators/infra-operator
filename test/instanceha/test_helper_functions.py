"""
Unit tests for helper functions in InstanceHA.

Tests for helper functions that coordinate evacuation logic:
- _cleanup_filtered_hosts
- _filter_processing_hosts
- _prepare_evacuation_resources
- _count_evacuable_hosts
"""

import os
import sys
import time
import unittest
from unittest.mock import Mock, patch, MagicMock
from datetime import datetime
from collections import defaultdict

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


class TestCleanupFilteredHosts(unittest.TestCase):
    """Test _cleanup_filtered_hosts function (line 2515)."""

    def test_cleanup_filtered_hosts_basic(self):
        """Test basic cleanup of filtered hosts."""
        mock_config = Mock()
        service = instanceha.InstanceHAService(mock_config)

        current_time = time.time()

        # Mark 3 hosts as being processed
        service.hosts_processing['host1'] = current_time
        service.hosts_processing['host2'] = current_time
        service.hosts_processing['host3'] = current_time - 100  # Different time

        marked_hostnames = {'host1', 'host2', 'host3'}
        final_hostnames = {'host1'}  # Only host1 should remain

        # Cleanup should remove host2 (marked but not final, matching time)
        instanceha._cleanup_filtered_hosts(service, marked_hostnames, final_hostnames, current_time)

        # host1 should still be tracked (it's in final_hostnames)
        self.assertIn('host1', service.hosts_processing)

        # host2 should be removed (marked but not final, matching time)
        self.assertNotIn('host2', service.hosts_processing)

        # host3 should remain (different time, not matching current_time)
        self.assertIn('host3', service.hosts_processing)

    def test_cleanup_filtered_hosts_empty_sets(self):
        """Test cleanup with empty sets."""
        mock_config = Mock()
        service = instanceha.InstanceHAService(mock_config)

        current_time = time.time()
        marked_hostnames = set()
        final_hostnames = set()

        # Should not raise exception with empty sets
        instanceha._cleanup_filtered_hosts(service, marked_hostnames, final_hostnames, current_time)

        self.assertEqual(len(service.hosts_processing), 0)

    def test_cleanup_filtered_hosts_no_cleanup_needed(self):
        """Test cleanup when all marked hosts are finalized."""
        mock_config = Mock()
        service = instanceha.InstanceHAService(mock_config)

        current_time = time.time()
        service.hosts_processing['host1'] = current_time
        service.hosts_processing['host2'] = current_time

        marked_hostnames = {'host1', 'host2'}
        final_hostnames = {'host1', 'host2'}  # All marked hosts are final

        instanceha._cleanup_filtered_hosts(service, marked_hostnames, final_hostnames, current_time)

        # Both hosts should still be tracked
        self.assertIn('host1', service.hosts_processing)
        self.assertIn('host2', service.hosts_processing)

    def test_cleanup_filtered_hosts_thread_safety(self):
        """Test that cleanup works correctly with threading lock."""
        mock_config = Mock()
        service = instanceha.InstanceHAService(mock_config)

        current_time = time.time()
        service.hosts_processing['host1'] = current_time
        service.hosts_processing['host2'] = current_time

        marked_hostnames = {'host1', 'host2'}
        final_hostnames = {'host1'}  # Keep host1

        # Cleanup should work correctly even with real lock
        instanceha._cleanup_filtered_hosts(service, marked_hostnames, final_hostnames, current_time)

        # host1 should remain
        self.assertIn('host1', service.hosts_processing)
        # host2 should be removed
        self.assertNotIn('host2', service.hosts_processing)


class TestFilterProcessingHosts(unittest.TestCase):
    """Test _filter_processing_hosts function (line 2527)."""

    def test_filter_processing_hosts_basic(self):
        """Test basic filtering of hosts already being processed."""
        mock_config = Mock()
        mock_config.get_config_value = Mock(return_value=30)  # FENCING_TIMEOUT
        service = instanceha.InstanceHAService(mock_config)

        # Create mock services
        svc1 = Mock()
        svc1.host = 'host1.example.com'
        svc2 = Mock()
        svc2.host = 'host2.example.com'
        svc3 = Mock()
        svc3.host = 'host3.example.com'

        compute_nodes = [svc1, svc2, svc3]
        to_resume = []

        # Mark host1 as already being processed
        service.hosts_processing['host1'] = time.time()

        result = instanceha._filter_processing_hosts(service, compute_nodes, to_resume)
        compute_filtered, resume_filtered, marked, current_time = result

        # host1 should be filtered out
        self.assertEqual(len(compute_filtered), 2)
        hosts = [svc.host for svc in compute_filtered]
        self.assertNotIn('host1.example.com', hosts)
        self.assertIn('host2.example.com', hosts)
        self.assertIn('host3.example.com', hosts)

        # New hosts should be marked
        self.assertIn('host2', marked)
        self.assertIn('host3', marked)

    def test_filter_processing_hosts_expired_entries(self):
        """Test that expired processing entries are cleaned up."""
        mock_config = Mock()
        mock_config.get_config_value = Mock(return_value=30)  # FENCING_TIMEOUT
        service = instanceha.InstanceHAService(mock_config)

        # Add an expired entry (more than max timeout + padding ago)
        old_time = time.time() - 500  # Way in the past
        service.hosts_processing['old-host'] = old_time

        svc1 = Mock()
        svc1.host = 'host1.example.com'
        compute_nodes = [svc1]
        to_resume = []

        instanceha._filter_processing_hosts(service, compute_nodes, to_resume)

        # Old entry should be cleaned up
        self.assertNotIn('old-host', service.hosts_processing)

    def test_filter_processing_hosts_marks_new_hosts(self):
        """Test that new hosts are marked as being processed."""
        mock_config = Mock()
        mock_config.get_config_value = Mock(return_value=30)
        service = instanceha.InstanceHAService(mock_config)

        svc1 = Mock()
        svc1.host = 'host1.example.com'
        svc2 = Mock()
        svc2.host = 'host2.example.com'

        compute_nodes = [svc1]
        to_resume = [svc2]

        result = instanceha._filter_processing_hosts(service, compute_nodes, to_resume)
        compute_filtered, resume_filtered, marked, current_time = result

        # Both hosts should be marked
        self.assertIn('host1', marked)
        self.assertIn('host2', marked)

        # Both hosts should be in processing dict
        self.assertIn('host1', service.hosts_processing)
        self.assertIn('host2', service.hosts_processing)

    def test_filter_processing_hosts_empty_lists(self):
        """Test filtering with empty lists."""
        mock_config = Mock()
        mock_config.get_config_value = Mock(return_value=30)
        service = instanceha.InstanceHAService(mock_config)

        result = instanceha._filter_processing_hosts(service, [], [])
        compute_filtered, resume_filtered, marked, current_time = result

        self.assertEqual(len(compute_filtered), 0)
        self.assertEqual(len(resume_filtered), 0)
        self.assertEqual(len(marked), 0)

    def test_filter_processing_hosts_resume_list(self):
        """Test filtering works correctly for resume list."""
        mock_config = Mock()
        mock_config.get_config_value = Mock(return_value=30)
        service = instanceha.InstanceHAService(mock_config)

        # Mark a host as being processed
        service.hosts_processing['resume-host'] = time.time()

        svc_resume = Mock()
        svc_resume.host = 'resume-host.example.com'

        to_resume = [svc_resume]

        result = instanceha._filter_processing_hosts(service, [], to_resume)
        compute_filtered, resume_filtered, marked, current_time = result

        # resume-host should be filtered out
        self.assertEqual(len(resume_filtered), 0)


class TestPrepareEvacuationResources(unittest.TestCase):
    """Test _prepare_evacuation_resources function (line 2568)."""

    def test_prepare_evacuation_resources_basic(self):
        """Test basic resource preparation for evacuation."""
        mock_conn = Mock()
        mock_config = Mock()
        config_values = {
            'RESERVED_HOSTS': False,
            'TAGGED_IMAGES': False,
            'TAGGED_FLAVORS': False,
            'TAGGED_AGGREGATES': False
        }
        mock_config.get_config_value = Mock(side_effect=lambda key: config_values.get(key, False))

        service = instanceha.InstanceHAService(mock_config)

        # Create mock services
        svc1 = Mock()
        svc1.host = 'host1'
        svc2 = Mock()
        svc2.host = 'host2'

        compute_nodes = [svc1, svc2]
        services = [svc1, svc2]

        # Mock cache methods
        service.get_hosts_with_servers_cached = Mock(return_value={'host1': True, 'host2': True})
        service.filter_hosts_with_servers = Mock(return_value=compute_nodes)
        service.refresh_evacuable_cache = Mock()

        result = instanceha._prepare_evacuation_resources(mock_conn, service, services, compute_nodes)
        nodes, reserved, images, flavors = result

        # Cache should be refreshed
        service.refresh_evacuable_cache.assert_called_once_with(mock_conn, force=True)

        # Results should be returned
        self.assertEqual(len(nodes), 2)
        self.assertEqual(len(reserved), 0)
        self.assertEqual(len(images), 0)
        self.assertEqual(len(flavors), 0)

    def test_prepare_evacuation_resources_empty_compute_nodes(self):
        """Test preparation with no compute nodes."""
        mock_conn = Mock()
        mock_config = Mock()
        service = instanceha.InstanceHAService(mock_config)

        result = instanceha._prepare_evacuation_resources(mock_conn, service, [], [])
        nodes, reserved, images, flavors = result

        self.assertEqual(len(nodes), 0)
        self.assertEqual(len(reserved), 0)
        self.assertEqual(len(images), 0)
        self.assertEqual(len(flavors), 0)

    def test_prepare_evacuation_resources_with_reserved_hosts(self):
        """Test resource preparation with RESERVED_HOSTS enabled."""
        mock_conn = Mock()
        mock_config = Mock()
        config_values = {
            'RESERVED_HOSTS': True,
            'TAGGED_IMAGES': False,
            'TAGGED_FLAVORS': False,
            'TAGGED_AGGREGATES': False
        }
        mock_config.get_config_value = Mock(side_effect=lambda key: config_values.get(key, False))

        service = instanceha.InstanceHAService(mock_config)

        # Create services including reserved host
        svc1 = Mock()
        svc1.host = 'compute-host'
        svc1.status = 'enabled'

        reserved_svc = Mock()
        reserved_svc.host = 'reserved-host'
        reserved_svc.status = 'disabled'
        reserved_svc.disabled_reason = 'reserved for evacuation'

        compute_nodes = [svc1]
        services = [svc1, reserved_svc]

        # Mock cache methods
        service.get_hosts_with_servers_cached = Mock(return_value={'compute-host': True})
        service.filter_hosts_with_servers = Mock(return_value=compute_nodes)
        service.refresh_evacuable_cache = Mock()

        result = instanceha._prepare_evacuation_resources(mock_conn, service, services, compute_nodes)
        nodes, reserved, images, flavors = result

        # Should find reserved host
        self.assertEqual(len(reserved), 1)
        self.assertEqual(reserved[0].host, 'reserved-host')

    def test_prepare_evacuation_resources_with_tagged_resources(self):
        """Test resource preparation with tagged images and flavors."""
        mock_conn = Mock()
        mock_config = Mock()
        config_values = {
            'RESERVED_HOSTS': False,
            'TAGGED_IMAGES': True,
            'TAGGED_FLAVORS': True,
            'TAGGED_AGGREGATES': False
        }
        mock_config.get_config_value = Mock(side_effect=lambda key: config_values.get(key, False))

        service = instanceha.InstanceHAService(mock_config)

        # Create mock compute node
        svc1 = Mock()
        svc1.host = 'host1'
        compute_nodes = [svc1]
        services = [svc1]

        # Mock evacuable resources
        mock_images = ['image1', 'image2']
        mock_flavors = ['flavor1', 'flavor2']

        service.get_evacuable_images = Mock(return_value=mock_images)
        service.get_evacuable_flavors = Mock(return_value=mock_flavors)
        service.get_hosts_with_servers_cached = Mock(return_value={'host1': True})
        service.filter_hosts_with_servers = Mock(return_value=compute_nodes)
        service.filter_hosts_with_evacuable_servers = Mock(return_value=compute_nodes)
        service.refresh_evacuable_cache = Mock()

        result = instanceha._prepare_evacuation_resources(mock_conn, service, services, compute_nodes)
        nodes, reserved, images, flavors = result

        # Should retrieve evacuable resources
        self.assertEqual(images, mock_images)
        self.assertEqual(flavors, mock_flavors)
        service.get_evacuable_images.assert_called_once_with(mock_conn)
        service.get_evacuable_flavors.assert_called_once_with(mock_conn)

    def test_prepare_evacuation_resources_filters_no_servers(self):
        """Test that hosts without servers are filtered out."""
        mock_conn = Mock()
        mock_config = Mock()
        config_values = {
            'RESERVED_HOSTS': False,
            'TAGGED_IMAGES': False,
            'TAGGED_FLAVORS': False,
            'TAGGED_AGGREGATES': False
        }
        mock_config.get_config_value = Mock(side_effect=lambda key: config_values.get(key, False))

        service = instanceha.InstanceHAService(mock_config)

        svc1 = Mock()
        svc1.host = 'host-with-servers'
        svc2 = Mock()
        svc2.host = 'host-no-servers'

        compute_nodes = [svc1, svc2]
        services = [svc1, svc2]

        # Only host1 has servers
        service.get_hosts_with_servers_cached = Mock(return_value={'host-with-servers': True})
        service.filter_hosts_with_servers = Mock(return_value=[svc1])  # Only host1
        service.refresh_evacuable_cache = Mock()

        result = instanceha._prepare_evacuation_resources(mock_conn, service, services, compute_nodes)
        nodes, reserved, images, flavors = result

        # Should only include host with servers
        self.assertEqual(len(nodes), 1)
        self.assertEqual(nodes[0].host, 'host-with-servers')


class TestCountEvacuableHosts(unittest.TestCase):
    """Test _count_evacuable_hosts function (line 2679)."""

    def test_count_evacuable_hosts_basic(self):
        """Test basic counting of evacuable hosts."""
        mock_conn = Mock()
        mock_config = Mock()
        mock_config.get_config_value = Mock(return_value='evacuable')

        service = instanceha.InstanceHAService(mock_config)
        service.evacuable_tag = 'evacuable'

        # Create mock aggregates
        agg1 = Mock()
        agg1.hosts = ['host1', 'host2']
        agg1.metadata = {'evacuable': 'true'}

        agg2 = Mock()
        agg2.hosts = ['host3']
        agg2.metadata = {}  # Not evacuable

        mock_conn.aggregates.list.return_value = [agg1, agg2]

        # Create mock services
        svc1 = Mock()
        svc1.host = 'host1'
        svc2 = Mock()
        svc2.host = 'host2'
        svc3 = Mock()
        svc3.host = 'host3'
        svc4 = Mock()
        svc4.host = 'host4'

        services = [svc1, svc2, svc3, svc4]

        count = instanceha._count_evacuable_hosts(mock_conn, service, services)

        # Should count only host1 and host2 (in evacuable aggregate)
        self.assertEqual(count, 2)

    def test_count_evacuable_hosts_no_aggregates(self):
        """Test counting when there are no aggregates."""
        mock_conn = Mock()
        mock_config = Mock()
        mock_config.get_config_value = Mock(return_value='evacuable')

        service = instanceha.InstanceHAService(mock_config)
        service.evacuable_tag = 'evacuable'

        mock_conn.aggregates.list.return_value = []

        svc1 = Mock()
        svc1.host = 'host1'
        services = [svc1]

        count = instanceha._count_evacuable_hosts(mock_conn, service, services)

        # No evacuable aggregates means no evacuable hosts
        self.assertEqual(count, 0)

    def test_count_evacuable_hosts_exception_handling(self):
        """Test that exceptions are handled gracefully."""
        mock_conn = Mock()
        mock_config = Mock()
        service = instanceha.InstanceHAService(mock_config)

        # Simulate exception when listing aggregates
        mock_conn.aggregates.list.side_effect = Exception("API Error")

        svc1 = Mock()
        svc2 = Mock()
        services = [svc1, svc2]

        count = instanceha._count_evacuable_hosts(mock_conn, service, services)

        # Should fallback to total number of services
        self.assertEqual(count, 2)

    def test_count_evacuable_hosts_multiple_aggregates(self):
        """Test counting with hosts in multiple aggregates."""
        mock_conn = Mock()
        mock_config = Mock()
        mock_config.get_config_value = Mock(return_value='evacuable')

        service = instanceha.InstanceHAService(mock_config)
        service.evacuable_tag = 'evacuable'

        # Create overlapping aggregates
        agg1 = Mock()
        agg1.hosts = ['host1', 'host2']
        agg1.metadata = {'evacuable': 'true'}

        agg2 = Mock()
        agg2.hosts = ['host2', 'host3']  # host2 overlaps
        agg2.metadata = {'evacuable': 'true'}

        mock_conn.aggregates.list.return_value = [agg1, agg2]

        svc1 = Mock()
        svc1.host = 'host1'
        svc2 = Mock()
        svc2.host = 'host2'
        svc3 = Mock()
        svc3.host = 'host3'

        services = [svc1, svc2, svc3]

        count = instanceha._count_evacuable_hosts(mock_conn, service, services)

        # Should count each unique host once (host1, host2, host3)
        self.assertEqual(count, 3)


if __name__ == '__main__':
    unittest.main()

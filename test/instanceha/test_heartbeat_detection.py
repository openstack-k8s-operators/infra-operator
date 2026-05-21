"""
Heartbeat detection tests for InstanceHA.

Tests for compute node heartbeat functionality:
- Packet parsing and hostname extraction (_resolve_hostname_packet)
- Heartbeat filtering logic (_filter_reachable_hosts)
- Heartbeat port configuration (get_heartbeat_port)
- Heartbeat lock concurrency
"""

import os
import unittest
import time
import struct
import threading
import logging
from unittest.mock import Mock, patch
from collections import defaultdict

logging.getLogger().setLevel(logging.CRITICAL)

import conftest  # noqa: F401
import instanceha

logging.getLogger().setLevel(logging.CRITICAL)


class TestResolveHostnamePacket(unittest.TestCase):
    """Test _resolve_hostname_packet hostname extraction from UDP packets."""

    def _make_packet(self, hostname_bytes):
        magic = struct.pack('I', instanceha.HEARTBEAT_MAGIC_NUMBER)
        return magic + hostname_bytes

    def test_valid_hostname(self):
        data = self._make_packet(b'compute-01')
        result = instanceha._resolve_hostname_packet(data, ('10.0.0.1', 12345), 'Heartbeat')
        self.assertEqual(result, 'compute-01')

    def test_valid_fqdn_extracts_short_name(self):
        data = self._make_packet(b'compute-01.example.com')
        result = instanceha._resolve_hostname_packet(data, ('10.0.0.1', 12345), 'Heartbeat')
        self.assertEqual(result, 'compute-01')

    def test_invalid_utf8(self):
        data = self._make_packet(b'\xff\xfe\xfd\xfc')
        result = instanceha._resolve_hostname_packet(data, ('10.0.0.1', 12345), 'Heartbeat')
        self.assertIsNone(result)

    def test_empty_hostname(self):
        data = self._make_packet(b'')
        result = instanceha._resolve_hostname_packet(data, ('10.0.0.1', 12345), 'Heartbeat')
        self.assertIsNone(result)

    def test_hostname_too_long(self):
        long_name = b'a' * (instanceha.USERNAME_MAX_LENGTH + 1)
        data = self._make_packet(long_name)
        result = instanceha._resolve_hostname_packet(data, ('10.0.0.1', 12345), 'Heartbeat')
        self.assertIsNone(result)

    def test_hostname_max_length_accepted(self):
        name = b'a' * instanceha.USERNAME_MAX_LENGTH
        data = self._make_packet(name)
        result = instanceha._resolve_hostname_packet(data, ('10.0.0.1', 12345), 'Heartbeat')
        self.assertIsNotNone(result)

    def test_invalid_hostname_characters(self):
        data = self._make_packet(b'host; rm -rf /')
        result = instanceha._resolve_hostname_packet(data, ('10.0.0.1', 12345), 'Heartbeat')
        self.assertIsNone(result)

    def test_null_padded_hostname(self):
        data = self._make_packet(b'compute-01\x00\x00\x00')
        result = instanceha._resolve_hostname_packet(data, ('10.0.0.1', 12345), 'Heartbeat')
        self.assertEqual(result, 'compute-01')

    def test_hostname_with_underscores(self):
        data = self._make_packet(b'compute_node_01')
        result = instanceha._resolve_hostname_packet(data, ('10.0.0.1', 12345), 'Heartbeat')
        self.assertEqual(result, 'compute_node_01')

    def test_magic_number_native_byte_order(self):
        magic_native = struct.pack('I', instanceha.HEARTBEAT_MAGIC_NUMBER)
        magic_network = struct.pack('!I', instanceha.HEARTBEAT_MAGIC_NUMBER)

        native_val = struct.unpack('I', magic_native)[0]
        network_val = struct.unpack('!I', magic_network)[0]

        self.assertTrue(
            native_val == instanceha.HEARTBEAT_MAGIC_NUMBER or
            network_val == instanceha.HEARTBEAT_MAGIC_NUMBER
        )


class TestFilterReachableHosts(unittest.TestCase):
    """Test _filter_reachable_hosts heartbeat filtering logic."""

    def setUp(self):
        mock_config = Mock()
        mock_config.get_config_value.return_value = 120  # HEARTBEAT_TIMEOUT
        self.service = instanceha.InstanceHAService(mock_config)
        self.service.heartbeat_hosts_timestamp.clear()

    def _make_compute_svc(self, hostname):
        svc = Mock()
        svc.host = f'{hostname}.example.com'
        return svc

    def test_empty_compute_nodes(self):
        unreachable, skipped = instanceha._filter_reachable_hosts(self.service, [])
        self.assertEqual(unreachable, [])
        self.assertEqual(skipped, [])

    def test_grace_period_listener_not_started(self):
        """All hosts bypass filtering when listener hasn't started yet."""
        self.service.heartbeat_listener_start_time = 0.0
        svc = self._make_compute_svc('compute-01')

        unreachable, skipped = instanceha._filter_reachable_hosts(self.service, [svc])
        self.assertEqual(len(unreachable), 1)
        self.assertEqual(len(skipped), 0)

    def test_grace_period_listener_too_young(self):
        """All hosts bypass filtering when listener started less than HEARTBEAT_TIMEOUT ago."""
        self.service.heartbeat_listener_start_time = time.time() - 10  # 10s ago, timeout is 120s

        svc = self._make_compute_svc('compute-01')
        self.service.heartbeat_hosts_timestamp['compute-01'] = time.time() - 5

        unreachable, skipped = instanceha._filter_reachable_hosts(self.service, [svc])
        # During grace period, all hosts pass through unfiltered
        self.assertEqual(len(unreachable), 1)
        self.assertEqual(len(skipped), 0)

    def test_recent_heartbeat_skips_fencing(self):
        """Host with recent heartbeat is skipped (not fenced)."""
        self.service.heartbeat_listener_start_time = time.time() - 300  # Well past grace period

        svc = self._make_compute_svc('compute-01')
        self.service.heartbeat_hosts_timestamp['compute-01'] = time.time() - 30  # 30s ago, within 120s timeout

        unreachable, skipped = instanceha._filter_reachable_hosts(self.service, [svc])
        self.assertEqual(len(unreachable), 0)
        self.assertEqual(len(skipped), 1)
        self.assertEqual(skipped[0].host, 'compute-01.example.com')

    def test_stale_heartbeat_allows_fencing(self):
        """Host with stale heartbeat (older than timeout) is fenced."""
        self.service.heartbeat_listener_start_time = time.time() - 300

        svc = self._make_compute_svc('compute-01')
        self.service.heartbeat_hosts_timestamp['compute-01'] = time.time() - 200  # 200s ago, beyond 120s timeout

        unreachable, skipped = instanceha._filter_reachable_hosts(self.service, [svc])
        self.assertEqual(len(unreachable), 1)
        self.assertEqual(len(skipped), 0)

    def test_no_heartbeat_entry_allows_fencing(self):
        """Host with no heartbeat entry at all is fenced."""
        self.service.heartbeat_listener_start_time = time.time() - 300

        svc = self._make_compute_svc('compute-01')
        # No entry in heartbeat_hosts_timestamp

        unreachable, skipped = instanceha._filter_reachable_hosts(self.service, [svc])
        self.assertEqual(len(unreachable), 1)
        self.assertEqual(len(skipped), 0)

    def test_mixed_hosts(self):
        """Test with a mix of reachable and unreachable hosts."""
        self.service.heartbeat_listener_start_time = time.time() - 300

        svcs = [self._make_compute_svc(f'compute-{i:02d}') for i in range(5)]

        # compute-00 and compute-02 have recent heartbeats
        self.service.heartbeat_hosts_timestamp['compute-00'] = time.time() - 10
        self.service.heartbeat_hosts_timestamp['compute-02'] = time.time() - 50
        # compute-01 has stale heartbeat
        self.service.heartbeat_hosts_timestamp['compute-01'] = time.time() - 200
        # compute-03 and compute-04 have no heartbeat entries

        unreachable, skipped = instanceha._filter_reachable_hosts(self.service, svcs)
        self.assertEqual(len(unreachable), 3)  # compute-01, compute-03, compute-04
        self.assertEqual(len(skipped), 2)  # compute-00, compute-02

        skipped_hostnames = {s.host for s in skipped}
        self.assertIn('compute-00.example.com', skipped_hostnames)
        self.assertIn('compute-02.example.com', skipped_hostnames)


class TestHeartbeatPort(unittest.TestCase):
    """Test get_heartbeat_port configuration."""

    def test_default_port(self):
        config = instanceha.ConfigManager()
        with patch.dict(os.environ, {}, clear=True):
            os.environ.pop('HEARTBEAT_PORT', None)
            port = config.get_heartbeat_port()
            self.assertEqual(port, instanceha.DEFAULT_HEARTBEAT_PORT)

    def test_env_var_override(self):
        config = instanceha.ConfigManager()
        with patch.dict(os.environ, {'HEARTBEAT_PORT': '9999'}):
            port = config.get_heartbeat_port()
            self.assertEqual(port, 9999)

    def test_invalid_env_var(self):
        config = instanceha.ConfigManager()
        with patch.dict(os.environ, {'HEARTBEAT_PORT': 'not_a_number'}):
            port = config.get_heartbeat_port()
            self.assertEqual(port, instanceha.DEFAULT_HEARTBEAT_PORT)

    def test_out_of_range_port(self):
        config = instanceha.ConfigManager()
        with patch.dict(os.environ, {'HEARTBEAT_PORT': '99999'}):
            port = config.get_heartbeat_port()
            self.assertEqual(port, instanceha.DEFAULT_HEARTBEAT_PORT)

    def test_zero_port(self):
        config = instanceha.ConfigManager()
        with patch.dict(os.environ, {'HEARTBEAT_PORT': '0'}):
            port = config.get_heartbeat_port()
            self.assertEqual(port, instanceha.DEFAULT_HEARTBEAT_PORT)


class TestHeartbeatLockConcurrency(unittest.TestCase):
    """Test heartbeat_lock protects shared state between threads."""

    def setUp(self):
        config = instanceha.ConfigManager()
        self.service = instanceha.InstanceHAService(config)

    def test_concurrent_timestamp_writes(self):
        errors = []

        def writer(thread_id):
            try:
                for i in range(50):
                    hostname = f'host-{thread_id}-{i}'
                    with self.service.heartbeat_lock:
                        self.service.heartbeat_hosts_timestamp[hostname] = time.time()
            except Exception as e:
                errors.append(str(e))

        threads = [threading.Thread(target=writer, args=(t,)) for t in range(10)]
        for t in threads:
            t.start()
        for t in threads:
            t.join(timeout=5)

        self.assertEqual(errors, [])
        self.assertEqual(len(self.service.heartbeat_hosts_timestamp), 500)

    def test_concurrent_read_write(self):
        """Test concurrent reads and writes don't raise exceptions."""
        errors = []

        def writer():
            try:
                for i in range(100):
                    with self.service.heartbeat_lock:
                        self.service.heartbeat_hosts_timestamp[f'host-{i}'] = time.time()
            except Exception as e:
                errors.append(f'writer: {e}')

        def reader():
            try:
                for _ in range(100):
                    with self.service.heartbeat_lock:
                        for k, v in list(self.service.heartbeat_hosts_timestamp.items()):
                            _ = v  # Read the value
            except Exception as e:
                errors.append(f'reader: {e}')

        threads = []
        for _ in range(5):
            threads.append(threading.Thread(target=writer))
            threads.append(threading.Thread(target=reader))

        for t in threads:
            t.start()
        for t in threads:
            t.join(timeout=10)

        self.assertEqual(errors, [])


class TestHeartbeatConfig(unittest.TestCase):
    """Test heartbeat configuration items."""

    def test_check_heartbeat_in_config_map(self):
        config = instanceha.ConfigManager()
        self.assertIn('CHECK_HEARTBEAT', config._config_map)

    def test_heartbeat_timeout_in_config_map(self):
        config = instanceha.ConfigManager()
        self.assertIn('HEARTBEAT_TIMEOUT', config._config_map)

    def test_heartbeat_timeout_default(self):
        config = instanceha.ConfigManager()
        self.assertEqual(config._config_map['HEARTBEAT_TIMEOUT'].default, 120)

    def test_heartbeat_timeout_range(self):
        config = instanceha.ConfigManager()
        item = config._config_map['HEARTBEAT_TIMEOUT']
        self.assertEqual(item.min_val, 30)
        self.assertEqual(item.max_val, 600)

    def test_check_heartbeat_default_false(self):
        config = instanceha.ConfigManager()
        self.assertEqual(config._config_map['CHECK_HEARTBEAT'].default, False)


if __name__ == '__main__':
    unittest.main()

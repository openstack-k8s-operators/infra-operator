"""
Heartbeat scale tests for InstanceHA.

Validates heartbeat mechanism behavior at 1000-node scale:
- UDP throughput with 1000 concurrent senders
- Filter performance with large timestamp maps
- Burst packet handling (thundering herd)
- Cleanup behavior at scale
- Concurrent read/write under load
"""

import os
import unittest
import time
import socket
import struct
import threading
import logging
from unittest.mock import Mock
from collections import defaultdict

logging.getLogger().setLevel(logging.CRITICAL)

import conftest  # noqa: F401
import instanceha

logging.getLogger().setLevel(logging.CRITICAL)

NODE_COUNT = 1000


class TestHeartbeatUDPThroughput(unittest.TestCase):
    """Test UDP listener can handle 1000 nodes sending heartbeats."""

    def setUp(self):
        mock_config = Mock()
        mock_config.get_config_value.return_value = 120
        self.service = instanceha.InstanceHAService(mock_config)
        self.service.heartbeat_hosts_timestamp.clear()
        self.service.udp_ip = '127.0.0.1'

    def _find_free_port(self):
        with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as s:
            s.bind(('127.0.0.1', 0))
            return s.getsockname()[1]

    def _send_heartbeat(self, port, hostname):
        magic = struct.pack('I', instanceha.HEARTBEAT_MAGIC_NUMBER)
        payload = magic + hostname.encode('utf-8')
        sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        try:
            sock.sendto(payload, ('127.0.0.1', port))
        finally:
            sock.close()

    def test_1000_node_udp_throughput(self):
        """Start heartbeat listener, send 1000 packets from threads, verify all recorded."""
        port = self._find_free_port()
        self.service.config.get_heartbeat_port = Mock(return_value=port)

        listener_started = threading.Event()

        original_on_start = None

        def patched_listener():
            def on_start():
                with self.service.heartbeat_lock:
                    self.service.heartbeat_listener_start_time = time.time()
                listener_started.set()

            instanceha._udp_listener(
                self.service,
                port=port,
                label='Heartbeat',
                magic_number=instanceha.HEARTBEAT_MAGIC_NUMBER,
                min_packet_size=5,
                lock=self.service.heartbeat_lock,
                timestamps=self.service.heartbeat_hosts_timestamp,
                stop_event=self.service.heartbeat_listener_stop_event,
                cleanup_threshold=instanceha.HEARTBEAT_CLEANUP_THRESHOLD,
                cleanup_age_seconds=instanceha.HEARTBEAT_CLEANUP_AGE_SECONDS,
                resolve_hostname=instanceha._resolve_hostname_packet,
                log_level=logging.DEBUG,
                on_start=on_start,
            )

        listener_thread = threading.Thread(target=patched_listener, daemon=True)
        listener_thread.start()
        self.assertTrue(listener_started.wait(timeout=5), "Listener failed to start")

        send_errors = []

        def sender(hostname):
            try:
                self._send_heartbeat(port, hostname)
            except Exception as e:
                send_errors.append(str(e))

        threads = []
        for i in range(NODE_COUNT):
            t = threading.Thread(target=sender, args=(f'compute-{i:04d}',))
            threads.append(t)

        for t in threads:
            t.start()
        for t in threads:
            t.join(timeout=10)

        # Wait for listener to process all packets
        deadline = time.time() + 10
        while time.time() < deadline:
            with self.service.heartbeat_lock:
                count = len(self.service.heartbeat_hosts_timestamp)
            if count >= NODE_COUNT:
                break
            time.sleep(0.1)

        self.service.heartbeat_listener_stop_event.set()
        listener_thread.join(timeout=5)

        self.assertEqual(send_errors, [])

        with self.service.heartbeat_lock:
            recorded = len(self.service.heartbeat_hosts_timestamp)

        self.assertEqual(recorded, NODE_COUNT,
                         f"Expected {NODE_COUNT} hostnames recorded, got {recorded}")

    def test_burst_packet_handling(self):
        """Send 1000 packets as fast as possible from a single thread."""
        port = self._find_free_port()
        self.service.config.get_heartbeat_port = Mock(return_value=port)

        listener_started = threading.Event()

        def patched_listener():
            def on_start():
                with self.service.heartbeat_lock:
                    self.service.heartbeat_listener_start_time = time.time()
                listener_started.set()

            instanceha._udp_listener(
                self.service,
                port=port,
                label='Heartbeat',
                magic_number=instanceha.HEARTBEAT_MAGIC_NUMBER,
                min_packet_size=5,
                lock=self.service.heartbeat_lock,
                timestamps=self.service.heartbeat_hosts_timestamp,
                stop_event=self.service.heartbeat_listener_stop_event,
                cleanup_threshold=instanceha.HEARTBEAT_CLEANUP_THRESHOLD,
                cleanup_age_seconds=instanceha.HEARTBEAT_CLEANUP_AGE_SECONDS,
                resolve_hostname=instanceha._resolve_hostname_packet,
                log_level=logging.DEBUG,
                on_start=on_start,
            )

        listener_thread = threading.Thread(target=patched_listener, daemon=True)
        listener_thread.start()
        self.assertTrue(listener_started.wait(timeout=5), "Listener failed to start")

        magic = struct.pack('I', instanceha.HEARTBEAT_MAGIC_NUMBER)
        sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        try:
            for i in range(NODE_COUNT):
                payload = magic + f'burst-{i:04d}'.encode('utf-8')
                sock.sendto(payload, ('127.0.0.1', port))
        finally:
            sock.close()

        deadline = time.time() + 10
        while time.time() < deadline:
            with self.service.heartbeat_lock:
                count = len(self.service.heartbeat_hosts_timestamp)
            if count >= NODE_COUNT:
                break
            time.sleep(0.1)

        self.service.heartbeat_listener_stop_event.set()
        listener_thread.join(timeout=5)

        with self.service.heartbeat_lock:
            recorded = len(self.service.heartbeat_hosts_timestamp)

        self.assertEqual(recorded, NODE_COUNT,
                         f"Burst test: expected {NODE_COUNT} hostnames, got {recorded}")


class TestHeartbeatFilterPerformance(unittest.TestCase):
    """Test _filter_reachable_hosts performance at scale."""

    def setUp(self):
        mock_config = Mock()
        mock_config.get_config_value.return_value = 120
        self.service = instanceha.InstanceHAService(mock_config)
        self.service.heartbeat_hosts_timestamp.clear()
        self.service.heartbeat_listener_start_time = time.time() - 300

    def _make_compute_svc(self, hostname):
        svc = Mock()
        svc.host = f'{hostname}.example.com'
        return svc

    def test_1000_node_filter_performance(self):
        """Filter 10 down hosts against 1000-entry timestamp map in < 100ms."""
        for i in range(NODE_COUNT):
            self.service.heartbeat_hosts_timestamp[f'compute-{i:04d}'] = time.time() - 10

        down_hosts = [self._make_compute_svc(f'compute-{i:04d}') for i in range(10)]

        start = time.time()
        unreachable, skipped = instanceha._filter_reachable_hosts(self.service, down_hosts)
        elapsed_ms = (time.time() - start) * 1000

        self.assertEqual(len(skipped), 10)
        self.assertEqual(len(unreachable), 0)
        self.assertLess(elapsed_ms, 100,
                        f"Filter took {elapsed_ms:.1f}ms, expected < 100ms")

    def test_filter_all_1000_down(self):
        """Worst case: all 1000 hosts are down with no heartbeats."""
        down_hosts = [self._make_compute_svc(f'compute-{i:04d}') for i in range(NODE_COUNT)]

        start = time.time()
        unreachable, skipped = instanceha._filter_reachable_hosts(self.service, down_hosts)
        elapsed_ms = (time.time() - start) * 1000

        self.assertEqual(len(unreachable), NODE_COUNT)
        self.assertEqual(len(skipped), 0)
        self.assertLess(elapsed_ms, 500,
                        f"Filter took {elapsed_ms:.1f}ms, expected < 500ms")

    def test_filter_mixed_1000_hosts(self):
        """500 hosts with recent heartbeats, 500 without."""
        for i in range(500):
            self.service.heartbeat_hosts_timestamp[f'compute-{i:04d}'] = time.time() - 10

        for i in range(500, NODE_COUNT):
            self.service.heartbeat_hosts_timestamp[f'compute-{i:04d}'] = time.time() - 200

        down_hosts = [self._make_compute_svc(f'compute-{i:04d}') for i in range(NODE_COUNT)]

        unreachable, skipped = instanceha._filter_reachable_hosts(self.service, down_hosts)
        self.assertEqual(len(skipped), 500)
        self.assertEqual(len(unreachable), 500)


class TestHeartbeatCleanupAtScale(unittest.TestCase):
    """Test timestamp cleanup behavior at scale."""

    def test_cleanup_above_threshold(self):
        """Populate entries exceeding threshold (half stale), verify cleanup works correctly."""
        mock_config = Mock()
        mock_config.get_config_value.return_value = 120
        service = instanceha.InstanceHAService(mock_config)
        service.heartbeat_hosts_timestamp.clear()

        now = time.time()
        half = instanceha.HEARTBEAT_CLEANUP_THRESHOLD

        for i in range(half + 1):
            service.heartbeat_hosts_timestamp[f'stale-{i:04d}'] = now - 700  # Older than 600s cleanup age

        for i in range(half + 1):
            service.heartbeat_hosts_timestamp[f'fresh-{i:04d}'] = now - 10

        total = len(service.heartbeat_hosts_timestamp)
        self.assertGreater(total, instanceha.HEARTBEAT_CLEANUP_THRESHOLD)

        # Simulate the cleanup logic from _udp_listener
        with service.heartbeat_lock:
            if len(service.heartbeat_hosts_timestamp) > instanceha.HEARTBEAT_CLEANUP_THRESHOLD:
                cutoff = time.time() - instanceha.HEARTBEAT_CLEANUP_AGE_SECONDS
                to_remove = [k for k, v in service.heartbeat_hosts_timestamp.items() if v < cutoff]
                for k in to_remove:
                    del service.heartbeat_hosts_timestamp[k]

        self.assertEqual(len(service.heartbeat_hosts_timestamp), half + 1)
        self.assertIn('fresh-0000', service.heartbeat_hosts_timestamp)
        self.assertNotIn('stale-0000', service.heartbeat_hosts_timestamp)

    def test_no_cleanup_below_threshold(self):
        """Verify no cleanup happens when below the threshold."""
        mock_config = Mock()
        mock_config.get_config_value.return_value = 120
        service = instanceha.InstanceHAService(mock_config)
        service.heartbeat_hosts_timestamp.clear()

        now = time.time()
        count = instanceha.HEARTBEAT_CLEANUP_THRESHOLD - 1
        for i in range(count):
            service.heartbeat_hosts_timestamp[f'stale-{i:04d}'] = now - 700

        self.assertTrue(len(service.heartbeat_hosts_timestamp) <= instanceha.HEARTBEAT_CLEANUP_THRESHOLD)

        # Cleanup should NOT trigger
        with service.heartbeat_lock:
            if len(service.heartbeat_hosts_timestamp) > instanceha.HEARTBEAT_CLEANUP_THRESHOLD:
                cutoff = time.time() - instanceha.HEARTBEAT_CLEANUP_AGE_SECONDS
                to_remove = [k for k, v in service.heartbeat_hosts_timestamp.items() if v < cutoff]
                for k in to_remove:
                    del service.heartbeat_hosts_timestamp[k]

        # All stale entries should remain because we're below threshold
        self.assertEqual(len(service.heartbeat_hosts_timestamp), count)


class TestConcurrentReadWriteAtScale(unittest.TestCase):
    """Test concurrent access to heartbeat state at 1000-node scale."""

    def test_concurrent_writers_and_filter_readers(self):
        """1000 writer threads + 10 reader threads calling _filter_reachable_hosts."""
        mock_config = Mock()
        mock_config.get_config_value.return_value = 120
        service = instanceha.InstanceHAService(mock_config)
        service.heartbeat_hosts_timestamp.clear()
        service.heartbeat_listener_start_time = time.time() - 300

        errors = []

        def writer(thread_id):
            try:
                hostname = f'compute-{thread_id:04d}'
                with service.heartbeat_lock:
                    service.heartbeat_hosts_timestamp[hostname] = time.time()
            except Exception as e:
                errors.append(f'writer-{thread_id}: {e}')

        def reader():
            try:
                svcs = []
                for i in range(10):
                    svc = Mock()
                    svc.host = f'compute-{i:04d}.example.com'
                    svcs.append(svc)
                instanceha._filter_reachable_hosts(service, svcs)
            except Exception as e:
                errors.append(f'reader: {e}')

        threads = []
        for i in range(NODE_COUNT):
            threads.append(threading.Thread(target=writer, args=(i,)))
        for _ in range(10):
            threads.append(threading.Thread(target=reader))

        for t in threads:
            t.start()
        for t in threads:
            t.join(timeout=30)

        self.assertEqual(errors, [], f"Errors during concurrent access: {errors}")
        self.assertEqual(len(service.heartbeat_hosts_timestamp), NODE_COUNT)

    def test_sustained_write_throughput(self):
        """Simulate sustained heartbeat rate: 1000 nodes * 10 heartbeats each."""
        mock_config = Mock()
        mock_config.get_config_value.return_value = 120
        service = instanceha.InstanceHAService(mock_config)
        service.heartbeat_hosts_timestamp.clear()

        errors = []

        def writer(thread_id):
            try:
                for iteration in range(10):
                    hostname = f'compute-{thread_id:04d}'
                    with service.heartbeat_lock:
                        service.heartbeat_hosts_timestamp[hostname] = time.time()
            except Exception as e:
                errors.append(f'writer-{thread_id}: {e}')

        start = time.time()
        threads = [threading.Thread(target=writer, args=(i,)) for i in range(NODE_COUNT)]
        for t in threads:
            t.start()
        for t in threads:
            t.join(timeout=30)
        elapsed = time.time() - start

        self.assertEqual(errors, [])
        self.assertEqual(len(service.heartbeat_hosts_timestamp), NODE_COUNT)
        self.assertLess(elapsed, 10.0,
                        f"10000 lock-protected writes took {elapsed:.1f}s, expected < 10s")


if __name__ == '__main__':
    unittest.main()

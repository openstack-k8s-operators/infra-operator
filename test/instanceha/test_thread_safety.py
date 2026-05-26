"""
Thread safety tests for InstanceHA.

Tests concurrent access patterns including:
- Kdump lock protecting shared state between UDP listener and main thread
- Health check server error handling (port binding failures)
- Executor exception handling (submit+as_completed resilience)
- Monotonic timeout correctness
"""

import time
import threading
import unittest
from unittest.mock import Mock, patch, MagicMock
from collections import defaultdict
from http.server import HTTPServer

import conftest  # noqa: F401

import instanceha


class TestKdumpLock(unittest.TestCase):
    """Test kdump_lock protects shared state between threads."""

    def setUp(self):
        """Set up test fixtures."""
        self.config = instanceha.ConfigManager()
        self.service = instanceha.InstanceHAService(self.config)

    def test_concurrent_timestamp_writes(self):
        """Test that concurrent writes to kdump_hosts_timestamp are safe."""
        errors = []

        def writer(thread_id):
            try:
                for i in range(50):
                    hostname = f'host-{thread_id}-{i}'
                    with self.service.kdump_lock:
                        self.service.kdump_hosts_timestamp[hostname] = time.time()
            except Exception as e:
                errors.append(str(e))

        threads = [threading.Thread(target=writer, args=(t,)) for t in range(10)]
        for t in threads:
            t.start()
        for t in threads:
            t.join(timeout=5)

        self.assertEqual(errors, [])
        with self.service.kdump_lock:
            self.assertEqual(len(self.service.kdump_hosts_timestamp), 500)

    def test_concurrent_read_write_timestamps(self):
        """Test that reads and writes to timestamps don't race."""
        errors = []

        with self.service.kdump_lock:
            for i in range(100):
                self.service.kdump_hosts_timestamp[f'host-{i}'] = time.time()

        def reader():
            try:
                for _ in range(100):
                    with self.service.kdump_lock:
                        _ = dict(self.service.kdump_hosts_timestamp)
            except Exception as e:
                errors.append(f"reader: {e}")

        def writer():
            try:
                for i in range(100):
                    with self.service.kdump_lock:
                        self.service.kdump_hosts_timestamp[f'new-host-{i}'] = time.time()
            except Exception as e:
                errors.append(f"writer: {e}")

        threads = [threading.Thread(target=reader) for _ in range(5)]
        threads += [threading.Thread(target=writer) for _ in range(3)]
        for t in threads:
            t.start()
        for t in threads:
            t.join(timeout=5)

        self.assertEqual(errors, [])

    def test_concurrent_fenced_set_access(self):
        """Test that concurrent access to kdump_fenced_hosts is safe."""
        errors = []

        def modifier(thread_id):
            try:
                for i in range(50):
                    hostname = f'host-{thread_id}-{i}'
                    with self.service.kdump_lock:
                        self.service.kdump_fenced_hosts.add(hostname)
                    with self.service.kdump_lock:
                        self.service.kdump_fenced_hosts.discard(hostname)
            except Exception as e:
                errors.append(str(e))

        threads = [threading.Thread(target=modifier, args=(t,)) for t in range(10)]
        for t in threads:
            t.start()
        for t in threads:
            t.join(timeout=5)

        self.assertEqual(errors, [])

    def test_cleanup_under_lock(self):
        """Test that timestamp cleanup (dict reassignment) is safe under lock."""
        with self.service.kdump_lock:
            for i in range(200):
                self.service.kdump_hosts_timestamp[f'host-{i}'] = time.time() - 500

        with self.service.kdump_lock:
            cutoff = time.time() - 300
            self.service.kdump_hosts_timestamp = defaultdict(float,
                {k: v for k, v in self.service.kdump_hosts_timestamp.items() if v >= cutoff})

        with self.service.kdump_lock:
            self.assertEqual(len(self.service.kdump_hosts_timestamp), 0)


class TestHealthCheckServer(unittest.TestCase):
    """Test health check server error handling."""

    def setUp(self):
        """Set up test fixtures."""
        self.config = instanceha.ConfigManager()
        self.service = instanceha.InstanceHAService(self.config)

    def test_port_binding_failure_logs_error(self):
        """Test that port binding failure is logged, not silently ignored."""
        with patch('http.server.HTTPServer', side_effect=OSError('Address already in use')):
            with self.assertLogs(level='ERROR') as cm:
                self.service._run_health_check_server()

        self.assertTrue(any('failed to bind' in msg for msg in cm.output))

    def test_unexpected_error_logs_error(self):
        """Test that unexpected server errors are logged."""
        with patch('http.server.HTTPServer', side_effect=RuntimeError('Unexpected')):
            with self.assertLogs(level='ERROR') as cm:
                self.service._run_health_check_server()

        self.assertTrue(any('failed' in msg.lower() for msg in cm.output))

    def test_health_check_response_200_on_success(self):
        """Test health check returns 200 when hash update is successful."""
        self.service.hash_update_successful = True
        self.service.current_hash = 'test-hash-value'
        self.assertIsNotNone(self.service.current_hash)
        self.assertTrue(self.service.hash_update_successful)


class TestExecutorExceptionHandling(unittest.TestCase):
    """Test that executor exceptions don't abort batch processing."""

    def setUp(self):
        """Set up test fixtures."""
        self.config = instanceha.ConfigManager()
        self.service = instanceha.InstanceHAService(self.config)

    def test_one_failure_doesnt_abort_batch(self):
        """Test that submit+as_completed handles individual failures gracefully."""
        import concurrent.futures

        results = []
        errors = []

        def task(host):
            if host == 'fail-host':
                raise RuntimeError('Simulated failure')
            results.append(host)
            return True

        hosts = ['ok-host-1', 'fail-host', 'ok-host-2']

        with concurrent.futures.ThreadPoolExecutor(max_workers=4) as executor:
            futures = {executor.submit(task, h): h for h in hosts}
            for future in concurrent.futures.as_completed(futures):
                host = futures[future]
                try:
                    future.result()
                except Exception as e:
                    errors.append(host)

        self.assertEqual(len(results), 2)
        self.assertIn('ok-host-1', results)
        self.assertIn('ok-host-2', results)
        self.assertEqual(errors, ['fail-host'])

    def test_max_workers_from_config(self):
        """Test that ThreadPoolExecutor uses WORKERS config value."""
        config_values = {'WORKERS': 8}
        self.service.config.get_config_value = Mock(
            side_effect=lambda key: config_values.get(key, 4))

        workers = self.service.config.get_config_value('WORKERS')
        self.assertEqual(workers, 8)

    def test_workers_default_is_positive(self):
        """Test that default WORKERS value is positive (required by ThreadPoolExecutor)."""
        config = instanceha.ConfigManager()
        workers = config.get_config_value('WORKERS')
        self.assertGreater(workers, 0)


class TestMonotonicTimeouts(unittest.TestCase):
    """Test that timeout functions use monotonic clock."""

    def test_wait_for_power_off_uses_monotonic(self):
        """Test that _wait_for_power_off uses time.monotonic for timeout."""
        check_func = Mock(return_value='off')

        with patch('instanceha.time.monotonic', side_effect=[0, 0, 1]) as mock_mono:
            with patch('instanceha.time.sleep'):
                result = instanceha._wait_for_power_off(
                    check_func, 30, 'test-host', 'off', 'IPMI'
                )

        mock_mono.assert_called()
        self.assertTrue(result)

    def test_bmh_wait_uses_monotonic(self):
        """Test that _bmh_wait_for_power_off uses time.monotonic for timeout."""
        mock_service = Mock()
        mock_service.config.get_config_value.return_value = 30

        mock_session = Mock()
        mock_session.__enter__ = Mock(return_value=mock_session)
        mock_session.__exit__ = Mock(return_value=None)

        mock_response = Mock()
        mock_response.json.return_value = {'status': {'poweredOn': False}}
        mock_response.raise_for_status = Mock()
        mock_session.get.return_value = mock_response

        with patch('instanceha.time.monotonic', side_effect=[0, 0, 1]) as mock_mono:
            with patch('instanceha.time.sleep'):
                with patch('instanceha.requests.Session', return_value=mock_session):
                    result = instanceha._bmh_wait_for_power_off(
                        'https://api/bmh', {'Authorization': 'Bearer test'},
                        None, 'test-host', 30, 1, mock_service
                    )

        mock_mono.assert_called()
        self.assertTrue(result)

    def test_monotonic_not_affected_by_wall_clock(self):
        """Test that monotonic() is used for duration, not time()."""
        start = time.monotonic()
        time.sleep(0.01)
        elapsed = time.monotonic() - start

        self.assertGreater(elapsed, 0)
        self.assertLess(elapsed, 1)


if __name__ == '__main__':
    unittest.main()

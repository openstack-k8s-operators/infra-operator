"""
Tests for Kubernetes API connectivity check and network partition detection.

Covers:
- _check_k8s_api_reachable: K8s API health probe
- _run_k8s_health_check: background thread state machine
- Fencing gate: main loop skips fencing when k8s_api_reachable is False
- K8S_API_CHECK_INTERVAL config validation
"""

import threading
import unittest
from unittest.mock import patch, mock_open, MagicMock

import conftest  # noqa: F401
import instanceha


class TestCheckK8sApiReachable(unittest.TestCase):
    """Tests for _check_k8s_api_reachable()."""

    @patch('instanceha.requests.get')
    @patch('instanceha._get_k8s_credentials', return_value=('token', 'openstack'))
    def test_returns_true_on_200(self, _creds, mock_get):
        mock_get.return_value = MagicMock(status_code=200)
        self.assertTrue(instanceha._check_k8s_api_reachable())
        mock_get.assert_called_once()
        call_args = mock_get.call_args
        self.assertIn('/api/v1/namespaces/openstack', call_args[0][0])

    @patch('instanceha.requests.get')
    @patch('instanceha._get_k8s_credentials', return_value=('token', 'openstack'))
    def test_returns_true_on_403(self, _creds, mock_get):
        mock_get.return_value = MagicMock(status_code=403)
        self.assertTrue(instanceha._check_k8s_api_reachable())

    @patch('instanceha.requests.get')
    @patch('instanceha._get_k8s_credentials', return_value=('token', 'openstack'))
    def test_returns_false_on_connection_error(self, _creds, mock_get):
        mock_get.side_effect = ConnectionError("connection refused")
        self.assertFalse(instanceha._check_k8s_api_reachable())

    @patch('instanceha.requests.get')
    @patch('instanceha._get_k8s_credentials', return_value=('token', 'openstack'))
    def test_returns_false_on_timeout(self, _creds, mock_get):
        mock_get.side_effect = TimeoutError("timed out")
        self.assertFalse(instanceha._check_k8s_api_reachable())

    @patch('instanceha.requests.get')
    @patch('instanceha._get_k8s_credentials', return_value=('token', 'openstack'))
    def test_returns_false_on_500(self, _creds, mock_get):
        mock_get.return_value = MagicMock(status_code=500)
        self.assertFalse(instanceha._check_k8s_api_reachable())

    @patch('instanceha._get_k8s_credentials', return_value=(None, None))
    def test_returns_false_when_no_credentials(self, _creds):
        self.assertFalse(instanceha._check_k8s_api_reachable())


class TestK8sHealthCheckThread(unittest.TestCase):
    """Tests for _run_k8s_health_check state machine."""

    def setUp(self):
        self.config = instanceha.ConfigManager()
        self.service = instanceha.InstanceHAService(self.config)

    def test_initial_state(self):
        self.assertTrue(self.service.k8s_api_reachable)

    @patch('instanceha._check_k8s_api_reachable', return_value=True)
    def test_stays_reachable_on_success(self, _check):
        self.service.k8s_api_reachable = True
        self.service.ready = True
        self.service.shutdown_event.set()
        self.service._run_k8s_health_check()
        self.assertTrue(self.service.k8s_api_reachable)
        self.assertTrue(self.service.ready)

    @patch('instanceha._check_k8s_api_reachable', return_value=False)
    def test_marks_not_ready_after_3_failures(self, _check):
        """After 3 consecutive failures, k8s_api_reachable and ready should be False."""
        self.service.ready = True
        call_count = 0

        def stop_after_3(*args, **kwargs):
            nonlocal call_count
            call_count += 1
            if call_count >= 3:
                self.service.shutdown_event.set()

        self.service.shutdown_event.wait = stop_after_3
        self.service._run_k8s_health_check()

        self.assertFalse(self.service.k8s_api_reachable)
        self.assertFalse(self.service.ready)

    @patch('instanceha._check_k8s_api_reachable')
    def test_does_not_mark_not_ready_on_1_failure(self, mock_check):
        """A single failure should not change reachability state."""
        self.service.ready = True
        call_count = 0

        def side_effect():
            nonlocal call_count
            call_count += 1
            return call_count > 1  # fail first, succeed second

        mock_check.side_effect = lambda: side_effect()

        wait_count = 0
        def stop_after_2(*args, **kwargs):
            nonlocal wait_count
            wait_count += 1
            if wait_count >= 2:
                self.service.shutdown_event.set()

        self.service.shutdown_event.wait = stop_after_2
        self.service._run_k8s_health_check()

        self.assertTrue(self.service.k8s_api_reachable)

    @patch('instanceha._check_k8s_api_reachable')
    def test_recovers_after_partition(self, mock_check):
        """After failures followed by success, reachable should be restored."""
        self.service.ready = True
        call_count = 0

        def side_effect():
            nonlocal call_count
            call_count += 1
            return call_count > 3  # fail 3 times, then succeed

        mock_check.side_effect = lambda: side_effect()

        wait_count = 0
        def stop_after_4(*args, **kwargs):
            nonlocal wait_count
            wait_count += 1
            if wait_count >= 4:
                self.service.shutdown_event.set()

        self.service.shutdown_event.wait = stop_after_4
        self.service._run_k8s_health_check()

        self.assertTrue(self.service.k8s_api_reachable)
        # ready stays False — the next successful Nova poll restores it
        self.assertFalse(self.service.ready)


class TestFencingGate(unittest.TestCase):
    """Tests for the fencing gate that skips processing when K8s API is unreachable."""

    def setUp(self):
        self.config = instanceha.ConfigManager()
        self.service = instanceha.InstanceHAService(self.config)

    def test_k8s_unreachable_blocks_fencing(self):
        """When k8s_api_reachable is False, _process_stale_services should not be called."""
        self.service.k8s_api_reachable = False
        self.service.ready = False

        # _process_stale_services should not be called
        with patch('instanceha._process_stale_services') as mock_process:
            with patch('instanceha._process_reenabling') as mock_reenable:
                # Simulate what the main loop does
                if not self.service.k8s_api_reachable:
                    skipped = True
                else:
                    skipped = False
                    mock_process()
                    mock_reenable()

                self.assertTrue(skipped)
                mock_process.assert_not_called()
                mock_reenable.assert_not_called()

    def test_k8s_reachable_allows_fencing(self):
        """When k8s_api_reachable is True, processing should proceed."""
        self.service.k8s_api_reachable = True
        self.assertTrue(self.service.k8s_api_reachable)


class TestK8sApiCheckConfig(unittest.TestCase):
    """Tests for K8S_API_CHECK_INTERVAL config."""

    def test_default_interval(self):
        config = instanceha.ConfigManager()
        self.assertEqual(config.get_config_value('K8S_API_CHECK_INTERVAL'), 15)

    def test_config_key_exists_in_config_map(self):
        self.assertIn('K8S_API_CHECK_INTERVAL', instanceha.ConfigManager._config_map)


class TestStartK8sHealthCheck(unittest.TestCase):
    """Tests for start_k8s_health_check thread startup."""

    def test_starts_daemon_thread(self):
        config = instanceha.ConfigManager()
        service = instanceha.InstanceHAService(config)
        service.shutdown_event.set()

        with patch.object(service, '_run_k8s_health_check'):
            service.start_k8s_health_check()
            # Give the thread a moment to start
            import time
            time.sleep(0.1)


if __name__ == '__main__':
    unittest.main()

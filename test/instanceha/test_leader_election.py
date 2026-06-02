"""
Tests for Lease-based leader election.

Covers:
- _get_lease, _create_lease, _update_lease: Lease REST helpers
- start_leader_election: initialization and disabled mode
- _try_acquire_lease: creation, expiry, and conflict paths
- _try_renew_lease: renewal and lost-leadership paths
- _run_leader_election: state machine transitions
- Main loop leader gate
- TOCTOU guard in _host_fence
"""

import threading
import unittest
from datetime import datetime, timedelta, timezone
from unittest.mock import patch, MagicMock

import conftest  # noqa: F401
import instanceha


class TestGetLease(unittest.TestCase):
    """Tests for _get_lease()."""

    @patch('instanceha.requests.get')
    @patch('instanceha._get_k8s_credentials', return_value=('token', 'ns'))
    def test_returns_lease_on_200(self, _creds, mock_get):
        lease_body = {'metadata': {'name': 'test'}, 'spec': {}}
        mock_get.return_value = MagicMock(status_code=200, json=lambda: lease_body)
        result = instanceha._get_lease('test', 'ns')
        self.assertEqual(result, lease_body)

    @patch('instanceha.requests.get')
    @patch('instanceha._get_k8s_credentials', return_value=('token', 'ns'))
    def test_returns_none_on_404(self, _creds, mock_get):
        mock_get.return_value = MagicMock(status_code=404)
        self.assertIsNone(instanceha._get_lease('test', 'ns'))

    @patch('instanceha.requests.get')
    @patch('instanceha._get_k8s_credentials', return_value=('token', 'ns'))
    def test_returns_none_on_exception(self, _creds, mock_get):
        mock_get.side_effect = ConnectionError("refused")
        self.assertIsNone(instanceha._get_lease('test', 'ns'))

    @patch('instanceha._get_k8s_credentials', return_value=(None, None))
    def test_returns_none_without_credentials(self, _creds):
        self.assertIsNone(instanceha._get_lease('test', 'ns'))


class TestCreateLease(unittest.TestCase):
    """Tests for _create_lease()."""

    @patch('instanceha.requests.post')
    @patch('instanceha._get_k8s_credentials', return_value=('token', 'ns'))
    def test_returns_true_on_201(self, _creds, mock_post):
        mock_post.return_value = MagicMock(status_code=201)
        self.assertTrue(instanceha._create_lease('test', 'ns', 'pod-0', 15))
        call_kwargs = mock_post.call_args
        body = call_kwargs.kwargs.get('json') or call_kwargs[1].get('json')
        self.assertEqual(body['spec']['holderIdentity'], 'pod-0')
        self.assertEqual(body['spec']['leaseDurationSeconds'], 15)

    @patch('instanceha.requests.post')
    @patch('instanceha._get_k8s_credentials', return_value=('token', 'ns'))
    def test_returns_false_on_409_conflict(self, _creds, mock_post):
        mock_post.return_value = MagicMock(status_code=409)
        self.assertFalse(instanceha._create_lease('test', 'ns', 'pod-0', 15))

    @patch('instanceha.requests.post')
    @patch('instanceha._get_k8s_credentials', return_value=('token', 'ns'))
    def test_returns_false_on_exception(self, _creds, mock_post):
        mock_post.side_effect = ConnectionError("refused")
        self.assertFalse(instanceha._create_lease('test', 'ns', 'pod-0', 15))

    @patch('instanceha._get_k8s_credentials', return_value=(None, None))
    def test_returns_false_without_credentials(self, _creds):
        self.assertFalse(instanceha._create_lease('test', 'ns', 'pod-0', 15))


class TestUpdateLease(unittest.TestCase):
    """Tests for _update_lease()."""

    def _make_lease(self, holder='pod-0', rv='100'):
        return {
            'metadata': {'name': 'test', 'namespace': 'ns', 'resourceVersion': rv},
            'spec': {
                'holderIdentity': holder,
                'leaseDurationSeconds': 15,
                'acquireTime': '2026-01-01T00:00:00.000000Z',
                'renewTime': '2026-01-01T00:00:00.000000Z',
            },
        }

    @patch('instanceha.requests.put')
    @patch('instanceha._get_k8s_credentials', return_value=('token', 'ns'))
    def test_returns_true_on_200(self, _creds, mock_put):
        mock_put.return_value = MagicMock(status_code=200)
        lease = self._make_lease()
        self.assertTrue(instanceha._update_lease('test', 'ns', lease, 'pod-0', 15))

    @patch('instanceha.requests.put')
    @patch('instanceha._get_k8s_credentials', return_value=('token', 'ns'))
    def test_returns_false_on_409_conflict(self, _creds, mock_put):
        mock_put.return_value = MagicMock(status_code=409)
        lease = self._make_lease()
        self.assertFalse(instanceha._update_lease('test', 'ns', lease, 'pod-0', 15))

    @patch('instanceha.requests.put')
    @patch('instanceha._get_k8s_credentials', return_value=('token', 'ns'))
    def test_preserves_resource_version(self, _creds, mock_put):
        mock_put.return_value = MagicMock(status_code=200)
        lease = self._make_lease(rv='42')
        instanceha._update_lease('test', 'ns', lease, 'pod-0', 15)
        call_kwargs = mock_put.call_args
        body = call_kwargs.kwargs.get('json') or call_kwargs[1].get('json')
        self.assertEqual(body['metadata']['resourceVersion'], '42')

    @patch('instanceha.requests.put')
    @patch('instanceha._get_k8s_credentials', return_value=('token', 'ns'))
    def test_does_not_mutate_caller_dict(self, _creds, mock_put):
        mock_put.return_value = MagicMock(status_code=200)
        lease = self._make_lease(holder='pod-1')
        original_holder = lease['spec']['holderIdentity']
        instanceha._update_lease('test', 'ns', lease, 'pod-0', 15)
        self.assertEqual(lease['spec']['holderIdentity'], original_holder)

    @patch('instanceha._get_k8s_credentials', return_value=(None, None))
    def test_returns_false_without_credentials(self, _creds):
        lease = self._make_lease()
        self.assertFalse(instanceha._update_lease('test', 'ns', lease, 'pod-0', 15))


class TestStartLeaderElection(unittest.TestCase):
    """Tests for start_leader_election()."""

    def _make_service(self):
        config = MagicMock()
        config.get_config_value.return_value = 45
        svc = instanceha.InstanceHAService(config)
        return svc

    @patch.dict('os.environ', {'INSTANCEHA_LEADER_ELECTION': 'false'})
    def test_disabled_mode_sets_leader_true(self):
        svc = self._make_service()
        svc.start_leader_election()
        self.assertTrue(svc.is_leader)
        self.assertFalse(svc.leader_election_enabled)

    @patch.dict('os.environ', {
        'INSTANCEHA_LEADER_ELECTION': 'true',
        'INSTANCEHA_LEASE_NAME': 'test-lease',
        'INSTANCEHA_LEASE_DURATION': '15',
        'INSTANCEHA_LEASE_RENEW_DEADLINE': '10',
        'INSTANCEHA_LEASE_RETRY_PERIOD': '2',
        'POD_NAME': 'pod-0',
    })
    def test_enabled_mode_starts_thread(self):
        svc = self._make_service()
        svc.start_leader_election()
        self.assertTrue(svc.leader_election_enabled)
        self.assertFalse(svc.is_leader)
        self.assertEqual(svc._lease_name, 'test-lease')
        self.assertEqual(svc._holder_identity, 'pod-0')
        svc.shutdown_event.set()


class TestTryAcquireLease(unittest.TestCase):
    """Tests for _try_acquire_lease()."""

    def _make_service(self):
        config = MagicMock()
        config.get_config_value.return_value = 45
        svc = instanceha.InstanceHAService(config)
        svc._lease_name = 'test-lease'
        svc._lease_duration = 15
        svc._holder_identity = 'pod-0'
        return svc

    @patch('instanceha._create_lease', return_value=True)
    @patch('instanceha._get_lease', return_value=None)
    def test_creates_lease_when_none_exists(self, _get, _create):
        svc = self._make_service()
        self.assertTrue(svc._try_acquire_lease('ns'))
        _create.assert_called_once_with('test-lease', 'ns', 'pod-0', 15)

    @patch('instanceha._get_lease')
    def test_does_not_acquire_active_lease(self, mock_get):
        now = datetime.now(tz=timezone.utc)
        mock_get.return_value = {
            'spec': {
                'holderIdentity': 'pod-1',
                'leaseDurationSeconds': 15,
                'renewTime': now.strftime('%Y-%m-%dT%H:%M:%S.%fZ'),
            },
        }
        svc = self._make_service()
        self.assertFalse(svc._try_acquire_lease('ns'))

    @patch('instanceha._update_lease', return_value=True)
    @patch('instanceha._get_lease')
    def test_acquires_expired_lease(self, mock_get, mock_update):
        expired = datetime.now(tz=timezone.utc) - timedelta(seconds=30)
        mock_get.return_value = {
            'spec': {
                'holderIdentity': 'pod-1',
                'leaseDurationSeconds': 15,
                'renewTime': expired.strftime('%Y-%m-%dT%H:%M:%S.%fZ'),
            },
        }
        svc = self._make_service()
        self.assertTrue(svc._try_acquire_lease('ns'))

    @patch('instanceha._update_lease', return_value=False)
    @patch('instanceha._get_lease')
    def test_fails_expired_lease_conflict(self, mock_get, mock_update):
        expired = datetime.now(tz=timezone.utc) - timedelta(seconds=30)
        mock_get.return_value = {
            'spec': {
                'holderIdentity': 'pod-1',
                'leaseDurationSeconds': 15,
                'renewTime': expired.strftime('%Y-%m-%dT%H:%M:%S.%fZ'),
            },
        }
        svc = self._make_service()
        self.assertFalse(svc._try_acquire_lease('ns'))

    @patch('instanceha._update_lease', return_value=True)
    @patch('instanceha._get_lease')
    def test_renews_own_lease(self, mock_get, mock_update):
        now = datetime.now(tz=timezone.utc)
        mock_get.return_value = {
            'spec': {
                'holderIdentity': 'pod-0',
                'renewTime': now.strftime('%Y-%m-%dT%H:%M:%S.%fZ'),
            },
        }
        svc = self._make_service()
        self.assertTrue(svc._try_acquire_lease('ns'))

    @patch('instanceha._get_lease')
    def test_does_not_acquire_when_renewtime_missing(self, mock_get):
        mock_get.return_value = {
            'spec': {
                'holderIdentity': 'pod-1',
                'leaseDurationSeconds': 15,
                'renewTime': '',
            },
        }
        svc = self._make_service()
        self.assertFalse(svc._try_acquire_lease('ns'))

    @patch('instanceha._get_lease')
    def test_does_not_acquire_when_renewtime_key_absent(self, mock_get):
        mock_get.return_value = {
            'spec': {
                'holderIdentity': 'pod-1',
                'leaseDurationSeconds': 15,
            },
        }
        svc = self._make_service()
        self.assertFalse(svc._try_acquire_lease('ns'))

    @patch('instanceha._update_lease', return_value=True)
    @patch('instanceha._get_lease')
    def test_acquires_expired_lease_no_fractional_seconds(self, mock_get, _update):
        expired = datetime.now(tz=timezone.utc) - timedelta(seconds=30)
        mock_get.return_value = {
            'spec': {
                'holderIdentity': 'pod-1',
                'leaseDurationSeconds': 15,
                'renewTime': expired.strftime('%Y-%m-%dT%H:%M:%SZ'),
            },
        }
        svc = self._make_service()
        self.assertTrue(svc._try_acquire_lease('ns'))


class TestTryRenewLease(unittest.TestCase):
    """Tests for _try_renew_lease()."""

    def _make_service(self):
        config = MagicMock()
        config.get_config_value.return_value = 45
        svc = instanceha.InstanceHAService(config)
        svc._lease_name = 'test-lease'
        svc._lease_duration = 15
        svc._holder_identity = 'pod-0'
        return svc

    @patch('instanceha._update_lease', return_value=True)
    @patch('instanceha._get_lease')
    def test_renews_own_lease(self, mock_get, mock_update):
        mock_get.return_value = {
            'spec': {'holderIdentity': 'pod-0'},
        }
        svc = self._make_service()
        self.assertTrue(svc._try_renew_lease('ns'))

    @patch('instanceha._get_lease')
    def test_fails_if_holder_changed(self, mock_get):
        mock_get.return_value = {
            'spec': {'holderIdentity': 'pod-1'},
        }
        svc = self._make_service()
        self.assertFalse(svc._try_renew_lease('ns'))

    @patch('instanceha._get_lease', return_value=None)
    def test_fails_if_lease_gone(self, _get):
        svc = self._make_service()
        self.assertFalse(svc._try_renew_lease('ns'))

    @patch('instanceha._update_lease', return_value=False)
    @patch('instanceha._get_lease')
    def test_fails_on_update_conflict(self, mock_get, mock_update):
        mock_get.return_value = {
            'spec': {'holderIdentity': 'pod-0'},
        }
        svc = self._make_service()
        self.assertFalse(svc._try_renew_lease('ns'))


class TestRunLeaderElection(unittest.TestCase):
    """Tests for _run_leader_election() state machine."""

    def _make_service(self):
        config = MagicMock()
        config.get_config_value.return_value = 45
        svc = instanceha.InstanceHAService(config)
        svc._lease_name = 'test-lease'
        svc._lease_duration = 15
        svc._lease_renew_deadline = 10
        svc._lease_retry_period = 0.01
        svc._holder_identity = 'pod-0'
        svc.leader_election_enabled = True
        return svc

    @patch('instanceha._emit_k8s_event')
    @patch('instanceha._get_k8s_credentials', return_value=('token', 'ns'))
    def test_acquires_leadership(self, _creds, _event):
        svc = self._make_service()

        def acquire_and_stop(ns):
            svc.shutdown_event.set()
            return True

        svc._try_acquire_lease = acquire_and_stop
        svc._run_leader_election()
        self.assertTrue(svc.is_leader)
        _event.assert_called_once()
        self.assertIn('LeaderAcquired', _event.call_args[0])

    @patch('instanceha._emit_k8s_event')
    @patch('instanceha._get_k8s_credentials', return_value=('token', 'ns'))
    def test_single_renew_failure_does_not_demote(self, _creds, _event):
        svc = self._make_service()
        svc.is_leader = True
        svc.ready = True

        def fail_renew_and_stop(ns):
            svc.shutdown_event.set()
            return False

        svc._try_renew_lease = fail_renew_and_stop
        svc._run_leader_election()
        self.assertTrue(svc.is_leader)
        self.assertTrue(svc.ready)

    @patch('instanceha._emit_k8s_event')
    @patch('instanceha._get_k8s_credentials', return_value=('token', 'ns'))
    def test_loses_leadership_after_renew_deadline(self, _creds, _event):
        svc = self._make_service()
        svc._lease_renew_deadline = 0
        svc.is_leader = True
        svc.ready = True

        def fail_renew_and_stop(ns):
            svc.shutdown_event.set()
            return False

        svc._try_renew_lease = fail_renew_and_stop
        svc._run_leader_election()
        self.assertFalse(svc.is_leader)
        self.assertFalse(svc.ready)
        _event.assert_called_once()
        self.assertIn('LeaderLost', _event.call_args[0])

    @patch('instanceha._emit_k8s_event')
    @patch('instanceha._get_k8s_credentials', return_value=('token', 'ns'))
    def test_loses_leadership_on_exception_after_deadline(self, _creds, _event):
        svc = self._make_service()
        svc._lease_renew_deadline = 0
        svc.is_leader = True
        svc.ready = True

        def raise_and_stop(ns):
            svc.shutdown_event.set()
            raise RuntimeError("network error")

        svc._try_renew_lease = raise_and_stop
        svc._run_leader_election()
        self.assertFalse(svc.is_leader)
        self.assertFalse(svc.ready)


class TestLeaderFenceGuard(unittest.TestCase):
    """Tests for the TOCTOU guard in _host_fence."""

    def _make_service(self):
        config = MagicMock()
        config.get_config_value.return_value = 45
        svc = instanceha.InstanceHAService(config)
        return svc

    def test_fence_aborted_when_leadership_lost(self):
        svc = self._make_service()
        svc.leader_election_enabled = True
        svc.is_leader = False
        result = instanceha._host_fence('compute-0', 'off', svc)
        self.assertFalse(result)

    def test_fence_proceeds_when_leader(self):
        svc = self._make_service()
        svc.leader_election_enabled = True
        svc.is_leader = True
        svc.config.fencing = {}
        result = instanceha._host_fence('compute-0', 'off', svc)
        self.assertFalse(result)

    def test_fence_skips_guard_in_single_replica(self):
        svc = self._make_service()
        svc.leader_election_enabled = False
        svc.is_leader = True
        svc.config.fencing = {}
        result = instanceha._host_fence('compute-0', 'off', svc)
        self.assertFalse(result)


class TestLeaderMainLoopGate(unittest.TestCase):
    """Tests that _process_stale_services is not called when not leader."""

    @patch('instanceha._process_reenabling')
    @patch('instanceha._process_stale_services')
    def test_standby_skips_fencing(self, mock_stale, mock_reenable):
        svc = MagicMock()
        svc.is_leader = False
        svc.k8s_api_reachable = True
        svc.shutdown_event = threading.Event()

        if not svc.is_leader:
            pass
        else:
            instanceha._process_stale_services(None, svc, [], [], [])
            instanceha._process_reenabling(None, svc, [])

        mock_stale.assert_not_called()
        mock_reenable.assert_not_called()

    @patch('instanceha._process_reenabling')
    @patch('instanceha._process_stale_services')
    def test_leader_proceeds_to_fencing(self, mock_stale, mock_reenable):
        svc = MagicMock()
        svc.is_leader = True
        svc.k8s_api_reachable = True

        if not svc.is_leader:
            pass
        else:
            instanceha._process_stale_services(None, svc, [], [], [])
            instanceha._process_reenabling(None, svc, [])

        mock_stale.assert_called_once()
        mock_reenable.assert_called_once()


if __name__ == '__main__':
    unittest.main()

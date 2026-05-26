"""
Tests for Kubernetes Event emission from the InstanceHA Python script.

Covers:
- _get_k8s_credentials: reading ServiceAccount token and namespace
- _emit_k8s_event: creating K8s Event objects via the API
- Event integration in process_service lifecycle
"""

import os
import unittest
import logging
from unittest.mock import Mock, patch, MagicMock, mock_open

import conftest  # noqa: F401
import instanceha


class TestGetK8sCredentials(unittest.TestCase):
    """Tests for _get_k8s_credentials()."""

    @patch.dict(os.environ, {'POD_NAMESPACE': 'openstack'})
    @patch('builtins.open', mock_open(read_data='my-token-value'))
    def test_returns_token_and_namespace(self):
        token, namespace = instanceha._get_k8s_credentials()
        self.assertEqual(token, 'my-token-value')
        self.assertEqual(namespace, 'openstack')

    @patch.dict(os.environ, {'POD_NAMESPACE': 'openstack'})
    @patch('builtins.open', side_effect=IOError("no such file"))
    def test_returns_none_on_missing_token_file(self, _):
        token, namespace = instanceha._get_k8s_credentials()
        self.assertIsNone(token)
        self.assertIsNone(namespace)

    @patch.dict(os.environ, {}, clear=True)
    @patch('builtins.open', mock_open(read_data='my-token'))
    def test_returns_none_when_namespace_not_set(self):
        # POD_NAMESPACE not in env → empty string → returns None
        token, namespace = instanceha._get_k8s_credentials()
        self.assertIsNone(token)
        self.assertIsNone(namespace)

    @patch.dict(os.environ, {'POD_NAMESPACE': 'openstack'})
    @patch('builtins.open', mock_open(read_data='  \n'))
    def test_returns_none_on_empty_token(self):
        token, namespace = instanceha._get_k8s_credentials()
        self.assertIsNone(token)
        self.assertIsNone(namespace)

    @patch.dict(os.environ, {'POD_NAMESPACE': 'openstack'})
    @patch('builtins.open', side_effect=OSError("permission denied"))
    def test_returns_none_on_os_error(self, _):
        token, namespace = instanceha._get_k8s_credentials()
        self.assertIsNone(token)
        self.assertIsNone(namespace)


class TestEmitK8sEvent(unittest.TestCase):
    """Tests for _emit_k8s_event()."""

    @patch('instanceha.requests.post')
    @patch('instanceha._get_k8s_credentials', return_value=('token123', 'openstack'))
    @patch.dict(os.environ, {'INSTANCEHA_CR_NAME': 'instanceha', 'POD_NAME': 'instanceha-0'})
    def test_emits_normal_event(self, mock_creds, mock_post):
        mock_response = Mock()
        mock_response.status_code = 201
        mock_post.return_value = mock_response

        instanceha._emit_k8s_event('compute-0', 'FencingStarted', 'Fencing host')

        mock_post.assert_called_once()
        args, kwargs = mock_post.call_args
        url = args[0]
        self.assertIn('/api/v1/namespaces/openstack/events', url)

        event = kwargs['json']
        self.assertEqual(event['reason'], 'FencingStarted')
        self.assertIn('compute-0', event['message'])
        self.assertEqual(event['type'], 'Normal')
        self.assertEqual(event['involvedObject']['kind'], 'InstanceHa')
        self.assertEqual(event['involvedObject']['name'], 'instanceha')
        self.assertEqual(event['reportingInstance'], 'instanceha-0')

    @patch('instanceha.requests.post')
    @patch('instanceha._get_k8s_credentials', return_value=('token123', 'openstack'))
    @patch.dict(os.environ, {'INSTANCEHA_CR_NAME': 'instanceha', 'POD_NAME': 'instanceha-0'})
    def test_emits_warning_event(self, mock_creds, mock_post):
        mock_response = Mock()
        mock_response.status_code = 201
        mock_post.return_value = mock_response

        instanceha._emit_k8s_event('compute-0', 'FencingFailed', 'Fencing failed',
                                   event_type='Warning')

        event = mock_post.call_args[1]['json']
        self.assertEqual(event['type'], 'Warning')
        self.assertEqual(event['reason'], 'FencingFailed')

    @patch('instanceha.requests.post')
    @patch('instanceha._get_k8s_credentials', return_value=(None, None))
    def test_skips_when_no_credentials(self, mock_creds, mock_post):
        instanceha._emit_k8s_event('compute-0', 'FencingStarted', 'test')
        mock_post.assert_not_called()

    @patch('instanceha.requests.post')
    @patch('instanceha._get_k8s_credentials', return_value=('token123', 'openstack'))
    @patch.dict(os.environ, {}, clear=True)
    def test_skips_when_no_cr_name(self, mock_creds, mock_post):
        instanceha._emit_k8s_event('compute-0', 'FencingStarted', 'test')
        mock_post.assert_not_called()

    @patch('instanceha.requests.post')
    @patch('instanceha._get_k8s_credentials', return_value=('token123', 'openstack'))
    @patch.dict(os.environ, {'INSTANCEHA_CR_NAME': 'instanceha', 'POD_NAME': 'instanceha-0'})
    def test_handles_api_failure_gracefully(self, mock_creds, mock_post):
        mock_response = Mock()
        mock_response.status_code = 403
        mock_response.text = 'Forbidden'
        mock_post.return_value = mock_response

        # Should not raise
        instanceha._emit_k8s_event('compute-0', 'FencingStarted', 'test')

    @patch('instanceha.requests.post', side_effect=Exception("connection refused"))
    @patch('instanceha._get_k8s_credentials', return_value=('token123', 'openstack'))
    @patch.dict(os.environ, {'INSTANCEHA_CR_NAME': 'instanceha', 'POD_NAME': 'instanceha-0'})
    def test_handles_connection_error_gracefully(self, mock_creds, mock_post):
        # Should not raise
        instanceha._emit_k8s_event('compute-0', 'FencingStarted', 'test')

    @patch('instanceha.requests.post')
    @patch('instanceha._get_k8s_credentials', return_value=('token123', 'openstack'))
    @patch.dict(os.environ, {'INSTANCEHA_CR_NAME': 'instanceha', 'POD_NAME': 'instanceha-0'})
    def test_event_has_correct_involved_object(self, mock_creds, mock_post):
        mock_response = Mock()
        mock_response.status_code = 201
        mock_post.return_value = mock_response

        instanceha._emit_k8s_event('compute-0', 'Test', 'test msg')

        event = mock_post.call_args[1]['json']
        obj = event['involvedObject']
        self.assertEqual(obj['apiVersion'], 'instanceha.openstack.org/v1beta1')
        self.assertEqual(obj['kind'], 'InstanceHa')
        self.assertEqual(obj['namespace'], 'openstack')

    @patch('instanceha.requests.post')
    @patch('instanceha._get_k8s_credentials', return_value=('token123', 'openstack'))
    @patch.dict(os.environ, {'INSTANCEHA_CR_NAME': 'instanceha', 'POD_NAME': 'instanceha-0'})
    def test_event_name_uses_cr_name_prefix(self, mock_creds, mock_post):
        mock_response = Mock()
        mock_response.status_code = 201
        mock_post.return_value = mock_response

        instanceha._emit_k8s_event('compute-0', 'Test', 'test msg')

        event = mock_post.call_args[1]['json']
        self.assertTrue(event['metadata']['name'].startswith('instanceha.'))

    @patch('instanceha.requests.post')
    @patch('instanceha._get_k8s_credentials', return_value=('token123', 'openstack'))
    @patch.dict(os.environ, {'INSTANCEHA_CR_NAME': 'instanceha', 'POD_NAME': 'instanceha-0'})
    def test_uses_correct_auth_header(self, mock_creds, mock_post):
        mock_response = Mock()
        mock_response.status_code = 201
        mock_post.return_value = mock_response

        instanceha._emit_k8s_event('compute-0', 'Test', 'msg')

        headers = mock_post.call_args[1]['headers']
        self.assertEqual(headers['Authorization'], 'Bearer token123')

    @patch('instanceha.requests.post')
    @patch('instanceha._get_k8s_credentials', return_value=('token123', 'openstack'))
    @patch.dict(os.environ, {'INSTANCEHA_CR_NAME': 'instanceha', 'POD_NAME': 'instanceha-0'})
    def test_message_includes_host(self, mock_creds, mock_post):
        mock_response = Mock()
        mock_response.status_code = 201
        mock_post.return_value = mock_response

        instanceha._emit_k8s_event('compute-node-7', 'EvacuationStarted', 'Starting evacuation')

        event = mock_post.call_args[1]['json']
        self.assertEqual(event['message'], '[compute-node-7] Starting evacuation')

    @patch('instanceha.requests.post')
    @patch('instanceha._get_k8s_credentials', return_value=('token123', 'openstack'))
    @patch.dict(os.environ, {'INSTANCEHA_CR_NAME': 'instanceha', 'POD_NAME': 'instanceha-0'})
    def test_uses_ca_cert_for_tls(self, mock_creds, mock_post):
        mock_response = Mock()
        mock_response.status_code = 201
        mock_post.return_value = mock_response

        instanceha._emit_k8s_event('compute-0', 'Test', 'msg')

        self.assertEqual(mock_post.call_args[1]['verify'], instanceha._K8S_CA_PATH)

    @patch('instanceha.requests.post')
    @patch('instanceha._get_k8s_credentials', return_value=('token123', 'openstack'))
    @patch.dict(os.environ, {'INSTANCEHA_CR_NAME': 'instanceha', 'POD_NAME': 'instanceha-0'})
    def test_uses_5_second_timeout(self, mock_creds, mock_post):
        mock_response = Mock()
        mock_response.status_code = 201
        mock_post.return_value = mock_response

        instanceha._emit_k8s_event('compute-0', 'Test', 'msg')

        self.assertEqual(mock_post.call_args[1]['timeout'], 5)


class TestProcessServiceEvents(unittest.TestCase):
    """Tests that process_service emits events at key lifecycle points."""

    def _make_service(self):
        service = Mock()
        service.config = Mock()
        service.config.get_config_value = Mock(side_effect=lambda key: {
            'DELAY': 0,
            'SMART_EVACUATION': True,
            'ORCHESTRATED_RESTART': False,
            'WORKERS': 4,
            'FORCE_RESERVED_HOST_EVACUATION': False,
        }.get(key, Mock()))
        service.hosts_processing = {}
        service.processing_lock = MagicMock()
        return service

    def _make_failed_service(self, host='compute-0'):
        svc = Mock()
        svc.host = host
        svc.id = 'svc-123'
        svc.forced_down = False
        svc.status = 'enabled'
        return svc

    @patch('instanceha._emit_k8s_event')
    @patch('instanceha._post_evacuation_recovery', return_value=True)
    @patch('instanceha._host_evacuate', return_value=True)
    @patch('instanceha._manage_reserved_hosts')
    @patch('instanceha._host_disable', return_value=True)
    @patch('instanceha._host_fence', return_value=True)
    @patch('instanceha._get_nova_connection')
    def test_emits_events_on_successful_workflow(self, mock_conn, mock_fence,
                                                  mock_disable, mock_reserved,
                                                  mock_evacuate, mock_recovery,
                                                  mock_event):
        mock_conn.return_value = Mock()
        mock_reserved.return_value = Mock(success=True, hostname=None)

        service = self._make_service()
        failed_svc = self._make_failed_service()

        result = instanceha.process_service(failed_svc, [], False, service)

        self.assertTrue(result)
        event_reasons = [c.args[1] for c in mock_event.call_args_list]
        self.assertIn('FencingStarted', event_reasons)
        self.assertIn('FencingSucceeded', event_reasons)
        self.assertIn('EvacuationStarted', event_reasons)
        self.assertIn('EvacuationSucceeded', event_reasons)
        self.assertIn('RecoveryCompleted', event_reasons)

    @patch('instanceha._emit_k8s_event')
    @patch('instanceha._host_fence', return_value=False)
    @patch('instanceha._get_nova_connection')
    def test_emits_fencing_failed_event(self, mock_conn, mock_fence, mock_event):
        mock_conn.return_value = Mock()
        service = self._make_service()
        failed_svc = self._make_failed_service()

        result = instanceha.process_service(failed_svc, [], False, service)

        self.assertFalse(result)
        event_reasons = [c.args[1] for c in mock_event.call_args_list]
        self.assertIn('FencingStarted', event_reasons)
        self.assertIn('FencingFailed', event_reasons)
        self.assertNotIn('EvacuationStarted', event_reasons)

    @patch('instanceha._emit_k8s_event')
    @patch('instanceha._host_evacuate', return_value=False)
    @patch('instanceha._manage_reserved_hosts')
    @patch('instanceha._host_disable', return_value=True)
    @patch('instanceha._host_fence', return_value=True)
    @patch('instanceha._get_nova_connection')
    def test_emits_evacuation_failed_event(self, mock_conn, mock_fence,
                                           mock_disable, mock_reserved,
                                           mock_evacuate, mock_event):
        mock_conn.return_value = Mock()
        mock_reserved.return_value = Mock(success=True, hostname=None)
        service = self._make_service()
        failed_svc = self._make_failed_service()

        result = instanceha.process_service(failed_svc, [], False, service)

        self.assertFalse(result)
        event_reasons = [c.args[1] for c in mock_event.call_args_list]
        self.assertIn('EvacuationStarted', event_reasons)
        self.assertIn('EvacuationFailed', event_reasons)
        self.assertNotIn('EvacuationSucceeded', event_reasons)

    @patch('instanceha._emit_k8s_event')
    @patch('instanceha._post_evacuation_recovery', return_value=True)
    @patch('instanceha._host_evacuate', return_value=True)
    @patch('instanceha._manage_reserved_hosts')
    @patch('instanceha._host_disable', return_value=True)
    @patch('instanceha._get_nova_connection')
    def test_skips_fencing_events_on_resume(self, mock_conn, mock_disable,
                                            mock_reserved, mock_evacuate,
                                            mock_recovery, mock_event):
        mock_conn.return_value = Mock()
        mock_reserved.return_value = Mock(success=True, hostname=None)
        service = self._make_service()
        failed_svc = self._make_failed_service()

        result = instanceha.process_service(failed_svc, [], True, service)

        self.assertTrue(result)
        event_reasons = [c.args[1] for c in mock_event.call_args_list]
        self.assertNotIn('FencingStarted', event_reasons)
        self.assertNotIn('FencingSucceeded', event_reasons)
        self.assertIn('EvacuationStarted', event_reasons)

    @patch('instanceha._emit_k8s_event')
    @patch('instanceha._get_nova_connection', side_effect=Exception("boom"))
    def test_emits_processing_failed_on_exception(self, mock_conn, mock_event):
        service = self._make_service()
        failed_svc = self._make_failed_service()

        result = instanceha.process_service(failed_svc, [], False, service)

        self.assertFalse(result)
        event_reasons = [c.args[1] for c in mock_event.call_args_list]
        self.assertIn('ProcessingFailed', event_reasons)
        # Check it was emitted as Warning
        for call_obj in mock_event.call_args_list:
            if call_obj.args[1] == 'ProcessingFailed':
                self.assertEqual(call_obj.kwargs.get('event_type', call_obj.args[3] if len(call_obj.args) > 3 else 'Normal'), 'Warning')


class TestPerInstanceEvacuationEvents(unittest.TestCase):
    """Tests for per-instance K8s events in _server_evacuate_future."""

    def _make_server(self, status='ACTIVE'):
        server = Mock()
        server.id = 'vm-123'
        server.status = status
        setattr(server, 'OS-EXT-STS:task_state', None)
        setattr(server, 'OS-EXT-SRV-ATTR:host', 'compute-0')
        return server

    @patch('instanceha._emit_k8s_event')
    @patch('instanceha._monitor_evacuation', return_value=True)
    @patch('instanceha.time')
    def test_per_instance_evacuation_started_event(self, mock_time, mock_monitor, mock_event):
        mock_time.monotonic.return_value = 0
        conn = MagicMock()
        resp = Mock(status_code=200, reason='OK')
        conn.servers.evacuate.return_value = (resp, None)
        server = self._make_server()

        instanceha._server_evacuate_future(conn, server)

        reasons = [c.args[1] for c in mock_event.call_args_list]
        self.assertIn('InstanceEvacuationStarted', reasons)
        started_call = [c for c in mock_event.call_args_list
                        if c.args[1] == 'InstanceEvacuationStarted'][0]
        self.assertIn('vm-123', started_call.args[2])

    @patch('instanceha._emit_k8s_event')
    @patch('instanceha._monitor_evacuation', return_value=True)
    @patch('instanceha.time')
    def test_per_instance_evacuation_succeeded_event(self, mock_time, mock_monitor, mock_event):
        mock_time.monotonic.return_value = 0
        conn = MagicMock()
        resp = Mock(status_code=200, reason='OK')
        conn.servers.evacuate.return_value = (resp, None)
        server = self._make_server()

        result = instanceha._server_evacuate_future(conn, server)

        self.assertTrue(result)
        reasons = [c.args[1] for c in mock_event.call_args_list]
        self.assertIn('InstanceEvacuationSucceeded', reasons)

    @patch('instanceha._emit_k8s_event')
    @patch('instanceha.time')
    def test_per_instance_evacuation_failed_event(self, mock_time, mock_event):
        mock_time.monotonic.return_value = 0
        conn = MagicMock()
        resp = Mock(status_code=500, reason='Internal Server Error')
        conn.servers.evacuate.return_value = (resp, None)
        server = self._make_server()

        result = instanceha._server_evacuate_future(conn, server)

        self.assertFalse(result)
        reasons = [c.args[1] for c in mock_event.call_args_list]
        self.assertIn('InstanceEvacuationFailed', reasons)
        failed_call = [c for c in mock_event.call_args_list
                       if c.args[1] == 'InstanceEvacuationFailed'][0]
        self.assertEqual(failed_call.kwargs.get('event_type',
                         failed_call.args[3] if len(failed_call.args) > 3 else 'Normal'),
                         'Warning')


if __name__ == '__main__':
    logging.basicConfig(level=logging.WARNING)
    unittest.main(verbosity=2)

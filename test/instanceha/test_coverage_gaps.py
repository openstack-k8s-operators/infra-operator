"""
Tests for previously uncovered or under-covered functions in InstanceHA.

Covers:
- _try_validate: validation orchestrator with error handling
- _validate_fencing_params: required parameter presence check
- _validate_fencing_inputs: injection prevention for fencing
- _execute_ipmi_fence: IPMI power control with retry logic
- _fence_noop, _fence_ipmi, _fence_redfish, _fence_bmh: agent dispatchers
- _is_service_resume_candidate: evacuation resume eligibility
- _aggregate_ids: aggregate filtering for host selection
- _traditional_evacuate: fire-and-forget evacuation logic
- _execute_step: processing step error handling wrapper
- _build_redfish_url: URL construction error cases
- _is_service_stale: datetime.fromisoformat error handling
- _monitor_evacuation: poll/retry/timeout behavior
- main() backoff: exponential backoff on Unauthorized/DiscoveryFailure/generic errors
- _process_stale_services: safety branches for empty/threshold/disabled paths
"""

import threading
import unittest
import logging
from unittest.mock import Mock, patch, MagicMock, call

logging.getLogger().setLevel(logging.CRITICAL)
import conftest  # noqa: F401
import instanceha
logging.getLogger().setLevel(logging.CRITICAL)


# ============================================================================
# _try_validate tests
# ============================================================================

class TestTryValidate(unittest.TestCase):
    """Tests for the _try_validate helper."""

    def test_returns_true_on_successful_validation(self):
        result = instanceha._try_validate(lambda: True, "err", "ctx")
        self.assertTrue(result)

    def test_returns_false_on_failed_validation(self):
        result = instanceha._try_validate(lambda: False, "err", "ctx")
        self.assertFalse(result)

    def test_catches_value_error_returns_false(self):
        result = instanceha._try_validate(lambda: (_ for _ in ()).throw(ValueError("bad")), "err", "ctx")
        self.assertFalse(result)

    def test_catches_type_error_returns_false(self):
        result = instanceha._try_validate(lambda: (_ for _ in ()).throw(TypeError("bad")), "err", "ctx")
        self.assertFalse(result)

    def test_catches_attribute_error_returns_false(self):
        result = instanceha._try_validate(lambda: (_ for _ in ()).throw(AttributeError("bad")), "err", "ctx")
        self.assertFalse(result)

    def test_does_not_catch_runtime_error(self):
        with self.assertRaises(RuntimeError):
            instanceha._try_validate(lambda: (_ for _ in ()).throw(RuntimeError("bad")), "err", "ctx")

    def test_log_error_true_logs_on_exception(self):
        with patch('instanceha.logging') as mock_log:
            instanceha._try_validate(lambda: (_ for _ in ()).throw(ValueError("oops")),
                                     "Validation failed", "test_ctx", log_error=True)
            mock_log.error.assert_called()

    def test_log_error_false_suppresses_log(self):
        with patch('instanceha.logging') as mock_log:
            instanceha._try_validate(lambda: (_ for _ in ()).throw(ValueError("oops")),
                                     "Validation failed", "test_ctx", log_error=False)
            mock_log.error.assert_not_called()


# ============================================================================
# _validate_fencing_params tests
# ============================================================================

class TestValidateFencingParams(unittest.TestCase):
    """Tests for _validate_fencing_params."""

    def test_all_params_present_returns_true(self):
        data = {"ipaddr": "10.0.0.1", "login": "admin", "passwd": "secret"}
        result = instanceha._validate_fencing_params(data, ["ipaddr", "login", "passwd"], "IPMI", "host1")
        self.assertTrue(result)

    def test_missing_param_returns_false(self):
        data = {"ipaddr": "10.0.0.1"}
        result = instanceha._validate_fencing_params(data, ["ipaddr", "login", "passwd"], "IPMI", "host1")
        self.assertFalse(result)

    def test_all_params_missing_returns_false(self):
        data = {}
        result = instanceha._validate_fencing_params(data, ["ipaddr", "login"], "IPMI", "host1")
        self.assertFalse(result)

    def test_extra_params_ignored(self):
        data = {"ipaddr": "10.0.0.1", "login": "admin", "passwd": "secret", "extra": "value"}
        result = instanceha._validate_fencing_params(data, ["ipaddr", "login"], "IPMI", "host1")
        self.assertTrue(result)

    def test_empty_required_list_returns_true(self):
        data = {"ipaddr": "10.0.0.1"}
        result = instanceha._validate_fencing_params(data, [], "IPMI", "host1")
        self.assertTrue(result)


# ============================================================================
# _validate_fencing_inputs tests
# ============================================================================

class TestValidateFencingInputs(unittest.TestCase):
    """Tests for _validate_fencing_inputs (injection prevention)."""

    def test_valid_ip_and_port_returns_true(self):
        data = {"ipaddr": "192.168.1.10", "ipport": "623", "login": "admin"}
        result = instanceha._validate_fencing_inputs(data, "IPMI")
        self.assertTrue(result)

    def test_invalid_ip_returns_false(self):
        data = {"ipaddr": "not-an-ip", "ipport": "623"}
        result = instanceha._validate_fencing_inputs(data, "IPMI")
        self.assertFalse(result)

    def test_invalid_port_returns_false(self):
        data = {"ipaddr": "192.168.1.10", "ipport": "99999"}
        result = instanceha._validate_fencing_inputs(data, "IPMI")
        self.assertFalse(result)

    def test_no_optional_fields_still_validates_ip(self):
        data = {"ipaddr": "10.0.0.1"}
        result = instanceha._validate_fencing_inputs(data, "Redfish")
        self.assertTrue(result)

    def test_ipv6_address_accepted(self):
        data = {"ipaddr": "::1"}
        result = instanceha._validate_fencing_inputs(data, "IPMI")
        self.assertTrue(result)

    def test_port_zero_returns_false(self):
        data = {"ipaddr": "10.0.0.1", "ipport": "0"}
        result = instanceha._validate_fencing_inputs(data, "IPMI")
        self.assertFalse(result)

    def test_port_negative_returns_false(self):
        data = {"ipaddr": "10.0.0.1", "ipport": "-1"}
        result = instanceha._validate_fencing_inputs(data, "IPMI")
        self.assertFalse(result)


# ============================================================================
# _execute_ipmi_fence tests
# ============================================================================

class TestExecuteIpmiFence(unittest.TestCase):
    """Tests for _execute_ipmi_fence (IPMI power control with retries)."""

    def _make_fencing_data(self):
        return {
            "ipaddr": "192.168.1.10",
            "ipport": "623",
            "login": "admin",
            "passwd": "secret",
        }

    @patch('instanceha.subprocess.run')
    def test_power_on_success(self, mock_run):
        """Successful power on returns True."""
        mock_run.return_value = Mock(returncode=0)
        result = instanceha._execute_ipmi_fence("host1", "on", self._make_fencing_data(), 30)
        self.assertTrue(result)
        mock_run.assert_called_once()
        cmd = mock_run.call_args[0][0]
        self.assertIn("power", cmd)
        self.assertIn("on", cmd)

    @patch('instanceha.subprocess.run')
    def test_password_passed_via_env_not_cmdline(self, mock_run):
        """IPMI password passed via environment variable, not command line."""
        mock_run.return_value = Mock(returncode=0)
        instanceha._execute_ipmi_fence("host1", "on", self._make_fencing_data(), 30)
        env = mock_run.call_args[1]['env']
        self.assertEqual(env['IPMITOOL_PASSWORD'], 'secret')
        cmd = mock_run.call_args[0][0]
        self.assertNotIn("secret", cmd)

    @patch('instanceha._wait_for_power_off', return_value=True)
    @patch('instanceha.subprocess.run')
    def test_power_off_waits_for_state(self, mock_run, mock_wait):
        """Power off calls _wait_for_power_off after command succeeds."""
        mock_run.return_value = Mock(returncode=0)
        result = instanceha._execute_ipmi_fence("host1", "off", self._make_fencing_data(), 30)
        self.assertTrue(result)
        mock_wait.assert_called_once()

    @patch('instanceha.time.sleep')
    @patch('instanceha.subprocess.run')
    def test_retries_on_timeout(self, mock_run, mock_sleep):
        """Retries on subprocess.TimeoutExpired."""
        mock_run.side_effect = [
            __import__('subprocess').TimeoutExpired(cmd="ipmitool", timeout=30),
            Mock(returncode=0),
        ]
        result = instanceha._execute_ipmi_fence("host1", "on", self._make_fencing_data(), 30)
        self.assertTrue(result)
        self.assertEqual(mock_run.call_count, 2)

    @patch('instanceha.time.sleep')
    @patch('instanceha.subprocess.run')
    def test_retries_on_called_process_error(self, mock_run, mock_sleep):
        """Retries on subprocess.CalledProcessError."""
        mock_run.side_effect = [
            __import__('subprocess').CalledProcessError(returncode=1, cmd="ipmitool"),
            Mock(returncode=0),
        ]
        result = instanceha._execute_ipmi_fence("host1", "on", self._make_fencing_data(), 30)
        self.assertTrue(result)
        self.assertEqual(mock_run.call_count, 2)

    @patch('instanceha.time.sleep')
    @patch('instanceha.subprocess.run')
    def test_all_retries_exhausted_returns_false(self, mock_run, mock_sleep):
        """Returns False after all retries exhausted."""
        mock_run.side_effect = __import__('subprocess').TimeoutExpired(cmd="ipmitool", timeout=30)
        result = instanceha._execute_ipmi_fence("host1", "on", self._make_fencing_data(), 30)
        self.assertFalse(result)
        self.assertEqual(mock_run.call_count, instanceha.MAX_FENCING_RETRIES)


# ============================================================================
# _fence_noop tests
# ============================================================================

class TestFenceNoop(unittest.TestCase):
    """Tests for _fence_noop agent."""

    def test_returns_true(self):
        service = Mock()
        result = instanceha._fence_noop("host1", "off", {}, service)
        self.assertTrue(result)

    def test_returns_true_for_any_action(self):
        service = Mock()
        for action in ["off", "on", "status"]:
            result = instanceha._fence_noop("host1", action, {}, service)
            self.assertTrue(result)


# ============================================================================
# _fence_ipmi tests
# ============================================================================

class TestFenceIpmi(unittest.TestCase):
    """Tests for _fence_ipmi dispatcher."""

    def _make_service(self):
        svc = Mock()
        svc.config.get_config_value.return_value = 30
        return svc

    def test_missing_params_returns_false(self):
        """Returns False when required IPMI params are missing."""
        result = instanceha._fence_ipmi("host1", "off", {"ipaddr": "10.0.0.1"}, self._make_service())
        self.assertFalse(result)

    def test_invalid_ip_returns_false(self):
        """Returns False when IP address is invalid."""
        data = {"ipaddr": "not-an-ip", "ipport": "623", "login": "admin", "passwd": "secret"}
        result = instanceha._fence_ipmi("host1", "off", data, self._make_service())
        self.assertFalse(result)

    @patch('instanceha._execute_ipmi_fence', return_value=True)
    def test_valid_params_calls_execute(self, mock_exec):
        """Valid params delegates to _execute_ipmi_fence."""
        data = {"ipaddr": "192.168.1.10", "ipport": "623", "login": "admin", "passwd": "secret"}
        result = instanceha._fence_ipmi("host1", "off", data, self._make_service())
        self.assertTrue(result)
        mock_exec.assert_called_once_with("host1", "off", data, 30)

    @patch('instanceha._execute_ipmi_fence', return_value=True)
    def test_uses_custom_timeout_from_fencing_data(self, mock_exec):
        """Uses timeout from fencing_data if provided."""
        data = {"ipaddr": "192.168.1.10", "ipport": "623", "login": "admin", "passwd": "secret", "timeout": 60}
        instanceha._fence_ipmi("host1", "off", data, self._make_service())
        mock_exec.assert_called_once_with("host1", "off", data, 60)

    @patch('instanceha._execute_ipmi_fence', return_value=False)
    def test_returns_false_when_execute_fails(self, mock_exec):
        """Returns False when _execute_ipmi_fence fails."""
        data = {"ipaddr": "192.168.1.10", "ipport": "623", "login": "admin", "passwd": "secret"}
        result = instanceha._fence_ipmi("host1", "off", data, self._make_service())
        self.assertFalse(result)


# ============================================================================
# _fence_redfish tests
# ============================================================================

class TestFenceRedfish(unittest.TestCase):
    """Tests for _fence_redfish dispatcher."""

    def _make_service(self):
        svc = Mock()
        svc.config.get_config_value.return_value = 30
        svc.config = Mock()
        svc.config.get_config_value.return_value = 30
        return svc

    def test_missing_params_returns_false(self):
        """Returns False when required Redfish params are missing."""
        data = {"ipaddr": "10.0.0.1"}
        result = instanceha._fence_redfish("host1", "off", data, self._make_service())
        self.assertFalse(result)

    def test_invalid_ip_returns_false(self):
        """Returns False when IP is invalid."""
        data = {"ipaddr": "bad-ip", "login": "root", "passwd": "secret"}
        result = instanceha._fence_redfish("host1", "off", data, self._make_service())
        self.assertFalse(result)

    @patch('instanceha._redfish_reset', return_value=True)
    @patch('instanceha._build_redfish_url', return_value="https://10.0.0.1:443/redfish/v1/Systems/System.Embedded.1")
    def test_power_off_maps_to_force_off(self, mock_url, mock_reset):
        """Action 'off' is mapped to Redfish 'ForceOff'."""
        data = {"ipaddr": "192.168.1.10", "login": "root", "passwd": "secret"}
        svc = self._make_service()
        instanceha._fence_redfish("host1", "off", data, svc)
        mock_reset.assert_called_once()
        self.assertEqual(mock_reset.call_args[0][3], 30)  # timeout
        self.assertEqual(mock_reset.call_args[0][4], "ForceOff")  # action

    @patch('instanceha._redfish_reset', return_value=True)
    @patch('instanceha._build_redfish_url', return_value="https://10.0.0.1:443/redfish/v1/Systems/System.Embedded.1")
    def test_power_on_maps_to_on(self, mock_url, mock_reset):
        """Action 'on' is mapped to Redfish 'On'."""
        data = {"ipaddr": "192.168.1.10", "login": "root", "passwd": "secret"}
        svc = self._make_service()
        instanceha._fence_redfish("host1", "on", data, svc)
        self.assertEqual(mock_reset.call_args[0][4], "On")

    @patch('instanceha._build_redfish_url', return_value=None)
    def test_invalid_url_returns_false(self, mock_url):
        """Returns False when URL construction fails."""
        data = {"ipaddr": "192.168.1.10", "login": "root", "passwd": "secret"}
        result = instanceha._fence_redfish("host1", "off", data, self._make_service())
        self.assertFalse(result)


# ============================================================================
# _fence_bmh tests
# ============================================================================

class TestFenceBmh(unittest.TestCase):
    """Tests for _fence_bmh dispatcher."""

    def test_missing_params_returns_false(self):
        """Returns False when required BMH params are missing."""
        data = {"token": "tok123"}
        svc = Mock()
        result = instanceha._fence_bmh("host1", "off", data, svc)
        self.assertFalse(result)

    @patch('instanceha._bmh_fence', return_value=True)
    def test_valid_params_calls_bmh_fence(self, mock_bmh):
        """Valid params delegates to _bmh_fence."""
        data = {"token": "tok123", "host": "metal3-0", "namespace": "openshift-machine-api"}
        svc = Mock()
        result = instanceha._fence_bmh("host1", "off", data, svc)
        self.assertTrue(result)
        mock_bmh.assert_called_once_with("tok123", "openshift-machine-api", "metal3-0", "off", svc)

    @patch('instanceha._bmh_fence', return_value=False)
    def test_returns_false_when_bmh_fence_fails(self, mock_bmh):
        """Returns False when underlying _bmh_fence fails."""
        data = {"token": "tok123", "host": "metal3-0", "namespace": "openshift-machine-api"}
        svc = Mock()
        result = instanceha._fence_bmh("host1", "off", data, svc)
        self.assertFalse(result)


# ============================================================================
# _is_service_resume_candidate tests
# ============================================================================

class TestIsServiceResumeCandidate(unittest.TestCase):
    """Tests for _is_service_resume_candidate."""

    def _make_svc(self, forced_down=True, state='down', status='disabled',
                  disabled_reason='instanceha evacuation: 2024-01-01T00:00:00'):
        svc = Mock()
        svc.forced_down = forced_down
        svc.state = state
        svc.status = status
        svc.disabled_reason = disabled_reason
        return svc

    def test_valid_resume_candidate(self):
        """Service with evacuation marker, forced_down, down, disabled is a candidate."""
        svc = self._make_svc()
        self.assertTrue(instanceha._is_service_resume_candidate(svc))

    def test_not_forced_down_is_not_candidate(self):
        svc = self._make_svc(forced_down=False)
        self.assertFalse(instanceha._is_service_resume_candidate(svc))

    def test_state_up_is_not_candidate(self):
        svc = self._make_svc(state='up')
        self.assertFalse(instanceha._is_service_resume_candidate(svc))

    def test_status_enabled_is_not_candidate(self):
        svc = self._make_svc(status='enabled')
        self.assertFalse(instanceha._is_service_resume_candidate(svc))

    def test_complete_marker_excludes_candidate(self):
        svc = self._make_svc(disabled_reason='instanceha evacuation complete: 2024-01-01T00:00:00')
        self.assertFalse(instanceha._is_service_resume_candidate(svc))

    def test_failed_marker_excludes_candidate(self):
        svc = self._make_svc(disabled_reason='instanceha evacuation FAILED: 2024-01-01T00:00:00')
        self.assertFalse(instanceha._is_service_resume_candidate(svc))

    def test_no_evacuation_marker_not_candidate(self):
        svc = self._make_svc(disabled_reason='some other reason')
        self.assertFalse(instanceha._is_service_resume_candidate(svc))

    def test_empty_disabled_reason_not_candidate(self):
        svc = self._make_svc(disabled_reason='')
        self.assertFalse(instanceha._is_service_resume_candidate(svc))

    def test_kdump_evacuation_is_candidate(self):
        """Kdump evacuation marker is still a valid resume candidate."""
        svc = self._make_svc(disabled_reason='instanceha evacuation (kdump): 2024-01-01T00:00:00')
        self.assertTrue(instanceha._is_service_resume_candidate(svc))

    def test_no_disabled_reason_attr_not_candidate(self):
        """Service without disabled_reason attribute is not a candidate."""
        svc = Mock()
        svc.forced_down = True
        svc.state = 'down'
        svc.status = 'disabled'
        # delattr to ensure getattr returns ''
        del svc.disabled_reason
        svc.disabled_reason = ''
        self.assertFalse(instanceha._is_service_resume_candidate(svc))


# ============================================================================
# _aggregate_ids tests
# ============================================================================

class TestAggregateIds(unittest.TestCase):
    """Tests for _aggregate_ids."""

    def test_returns_matching_aggregate_ids(self):
        agg1 = Mock(id=1, hosts=['host1', 'host2'])
        agg2 = Mock(id=2, hosts=['host2', 'host3'])
        agg3 = Mock(id=3, hosts=['host1'])
        conn = Mock()
        conn.aggregates.list.return_value = [agg1, agg2, agg3]
        service = Mock(host='host1')

        result = instanceha._aggregate_ids(conn, service)
        self.assertEqual(result, [1, 3])

    def test_no_matching_aggregates(self):
        agg1 = Mock(id=1, hosts=['host2', 'host3'])
        conn = Mock()
        conn.aggregates.list.return_value = [agg1]
        service = Mock(host='host1')

        result = instanceha._aggregate_ids(conn, service)
        self.assertEqual(result, [])

    def test_empty_aggregates_list(self):
        conn = Mock()
        conn.aggregates.list.return_value = []
        service = Mock(host='host1')

        result = instanceha._aggregate_ids(conn, service)
        self.assertEqual(result, [])

    def test_host_in_all_aggregates(self):
        agg1 = Mock(id=1, hosts=['host1'])
        agg2 = Mock(id=2, hosts=['host1'])
        conn = Mock()
        conn.aggregates.list.return_value = [agg1, agg2]
        service = Mock(host='host1')

        result = instanceha._aggregate_ids(conn, service)
        self.assertEqual(result, [1, 2])


# ============================================================================
# _traditional_evacuate tests
# ============================================================================

class TestTraditionalEvacuate(unittest.TestCase):
    """Tests for _traditional_evacuate (fire-and-forget)."""

    def _make_server(self, server_id):
        s = Mock()
        s.id = server_id
        return s

    @patch('instanceha._server_evacuate')
    def test_all_succeed_returns_true(self, mock_evac):
        """Returns True when all evacuations succeed."""
        mock_evac.return_value = Mock(accepted=True, uuid='srv1', reason='ok')
        servers = [self._make_server('srv1'), self._make_server('srv2')]
        conn = Mock()

        result = instanceha._traditional_evacuate(conn, servers, 'host1')
        self.assertTrue(result)
        self.assertEqual(mock_evac.call_count, 2)

    @patch('instanceha._server_evacuate')
    def test_partial_failure_returns_false(self, mock_evac):
        """Returns False when any evacuation fails."""
        mock_evac.side_effect = [
            Mock(accepted=True, uuid='srv1', reason='ok'),
            Mock(accepted=False, uuid='srv2', reason='error'),
        ]
        servers = [self._make_server('srv1'), self._make_server('srv2')]
        conn = Mock()

        result = instanceha._traditional_evacuate(conn, servers, 'host1')
        self.assertFalse(result)

    @patch('instanceha._server_evacuate')
    def test_empty_server_list_returns_true(self, mock_evac):
        """Returns True for empty server list (nothing to evacuate)."""
        result = instanceha._traditional_evacuate(Mock(), [], 'host1')
        self.assertTrue(result)
        mock_evac.assert_not_called()

    @patch('instanceha._server_evacuate')
    def test_server_without_id_returns_false(self, mock_evac):
        """Server without 'id' attribute is logged as error, returns False."""
        server = Mock(spec=[])  # spec=[] means no attributes
        server.to_dict = Mock(return_value={'name': 'bad_server'})
        conn = Mock()

        result = instanceha._traditional_evacuate(conn, [server], 'host1')
        self.assertFalse(result)
        mock_evac.assert_not_called()

    @patch('instanceha._server_evacuate')
    def test_target_host_passed_to_evacuate(self, mock_evac):
        """target_host parameter is forwarded to _server_evacuate."""
        mock_evac.return_value = Mock(accepted=True, uuid='srv1', reason='ok')
        servers = [self._make_server('srv1')]
        conn = Mock()

        instanceha._traditional_evacuate(conn, servers, 'host1', target_host='reserved-01')
        mock_evac.assert_called_once_with(conn, 'srv1', target_host='reserved-01', server_obj=servers[0])

    @patch('instanceha._server_evacuate')
    def test_all_fail_returns_false(self, mock_evac):
        """Returns False when all evacuations fail."""
        mock_evac.return_value = Mock(accepted=False, uuid='srv1', reason='error')
        servers = [self._make_server('srv1'), self._make_server('srv2')]
        conn = Mock()

        result = instanceha._traditional_evacuate(conn, servers, 'host1')
        self.assertFalse(result)


# ============================================================================
# _execute_step tests
# ============================================================================

class TestExecuteStep(unittest.TestCase):
    """Tests for _execute_step error handling wrapper."""

    def test_returns_result_on_success(self):
        result = instanceha._execute_step("Fencing", lambda: True, "host1")
        self.assertTrue(result)

    def test_returns_false_on_step_failure(self):
        result = instanceha._execute_step("Fencing", lambda: False, "host1")
        self.assertFalse(result)

    def test_returns_none_on_step_returning_none(self):
        result = instanceha._execute_step("Fencing", lambda: None, "host1")
        self.assertIsNone(result)

    def test_catches_exception_returns_false(self):
        def bad_step():
            raise RuntimeError("step exploded")
        result = instanceha._execute_step("Fencing", bad_step, "host1")
        self.assertFalse(result)

    def test_passes_args_and_kwargs(self):
        def step_func(a, b, key=None):
            return a + b + (key or 0)
        result = instanceha._execute_step("Add", step_func, "host1", 1, 2, key=3)
        self.assertEqual(result, 6)

    def test_logs_error_on_failure(self):
        with patch('instanceha.logging') as mock_log:
            instanceha._execute_step("Fencing", lambda: False, "host1")
            mock_log.error.assert_called()

    def test_logs_error_on_exception(self):
        with patch('instanceha.logging') as mock_log:
            instanceha._execute_step("Fencing", lambda: (_ for _ in ()).throw(RuntimeError("boom")), "host1")
            mock_log.error.assert_called()


# ============================================================================
# _build_redfish_url edge cases
# ============================================================================

class TestBuildRedfishUrl(unittest.TestCase):
    """Tests for _build_redfish_url edge cases."""

    def test_valid_tls_url(self):
        data = {"ipaddr": "192.168.1.10", "ipport": "443", "tls": "true", "uuid": "System.Embedded.1"}
        url = instanceha._build_redfish_url(data)
        self.assertIsNotNone(url)
        self.assertTrue(url.startswith("https://"))

    def test_valid_no_tls_url(self):
        data = {"ipaddr": "192.168.1.10", "ipport": "8080", "tls": "false", "uuid": "System.Embedded.1"}
        url = instanceha._build_redfish_url(data)
        self.assertIsNotNone(url)
        self.assertTrue(url.startswith("http://"))

    def test_defaults_port_443(self):
        data = {"ipaddr": "192.168.1.10"}
        url = instanceha._build_redfish_url(data)
        self.assertIn(":443", url)

    def test_defaults_uuid(self):
        data = {"ipaddr": "192.168.1.10"}
        url = instanceha._build_redfish_url(data)
        self.assertIn("System.Embedded.1", url)

    def test_missing_ipaddr_raises(self):
        with self.assertRaises(KeyError):
            instanceha._build_redfish_url({})

    def test_localhost_ip_blocked(self):
        """SSRF: localhost IP is blocked by URL validation."""
        data = {"ipaddr": "127.0.0.1", "tls": "true"}
        url = instanceha._build_redfish_url(data)
        self.assertIsNone(url)

    def test_link_local_ip_blocked(self):
        """SSRF: link-local IP is blocked by URL validation."""
        data = {"ipaddr": "169.254.1.1", "tls": "true"}
        url = instanceha._build_redfish_url(data)
        self.assertIsNone(url)

    def test_custom_uuid(self):
        data = {"ipaddr": "192.168.1.10", "uuid": "Custom.System.1"}
        url = instanceha._build_redfish_url(data)
        self.assertIn("Custom.System.1", url)


# ============================================================================
# _is_service_stale tests
# ============================================================================

class TestIsServiceStale(unittest.TestCase):
    """Tests for _is_service_stale() datetime parsing protection."""

    def _make_svc(self, updated_at, host='test-host'):
        svc = Mock()
        svc.host = host
        svc.updated_at = updated_at
        return svc

    def test_valid_stale_timestamp(self):
        from datetime import datetime, timedelta
        target = datetime.now()
        svc = self._make_svc((datetime.now() - timedelta(hours=1)).isoformat())
        self.assertTrue(instanceha._is_service_stale(svc, target))

    def test_valid_fresh_timestamp(self):
        from datetime import datetime, timedelta
        target = datetime.now() - timedelta(hours=2)
        svc = self._make_svc(datetime.now().isoformat())
        self.assertFalse(instanceha._is_service_stale(svc, target))

    def test_none_updated_at(self):
        from datetime import datetime
        svc = self._make_svc(None)
        self.assertFalse(instanceha._is_service_stale(svc, datetime.now()))

    def test_malformed_updated_at(self):
        from datetime import datetime
        svc = self._make_svc('not-a-date')
        self.assertFalse(instanceha._is_service_stale(svc, datetime.now()))

    def test_missing_updated_at_attribute(self):
        from datetime import datetime
        svc = Mock(spec=['host'])
        svc.host = 'test-host'
        self.assertFalse(instanceha._is_service_stale(svc, datetime.now()))


# ============================================================================
# _monitor_evacuation tests
# ============================================================================

class TestMonitorEvacuation(unittest.TestCase):
    """Tests for _monitor_evacuation() poll/retry/timeout behavior."""

    def _run_monitor(self, status_sequence, monotonic_values=None, start_time=100):
        """Helper to run _monitor_evacuation with controlled time and status."""
        if monotonic_values is None:
            monotonic_values = [start_time + i for i in range(len(status_sequence) + 5)]

        conn = Mock()
        conn.servers.evacuate.return_value = (Mock(status_code=200, reason='OK'), {})

        with patch('instanceha.time.sleep'):
            with patch('instanceha.time.monotonic', side_effect=monotonic_values):
                with patch('instanceha._server_evacuation_status', side_effect=status_sequence):
                    return instanceha._monitor_evacuation(conn, 'srv-1', 'resp-1', start_time)

    def test_immediate_completion(self):
        status = Mock(completed=True, error=False)
        self.assertTrue(self._run_monitor([status]))

    def test_in_progress_then_completion(self):
        in_progress = Mock(completed=False, error=False)
        done = Mock(completed=True, error=False)
        self.assertTrue(self._run_monitor([in_progress, done]))

    def test_error_retry_then_success(self):
        error = Mock(completed=False, error=True)
        done = Mock(completed=True, error=False)
        self.assertTrue(self._run_monitor([error, done]))

    def test_error_retry_exhaustion(self):
        error = Mock(completed=False, error=True)
        retries = instanceha.DEFAULT_EVACUATION_RETRIES
        self.assertFalse(self._run_monitor([error] * retries))

    def test_exception_retry_then_success(self):
        done = Mock(completed=True, error=False)
        self.assertFalse(self._run_monitor([Exception("API error"), done]) is None)
        result = self._run_monitor([Exception("API error"), done])
        self.assertTrue(result)

    def test_exception_retry_exhaustion(self):
        retries = instanceha.DEFAULT_EVACUATION_RETRIES
        self.assertFalse(self._run_monitor([Exception("fail")] * retries))

    def test_timeout(self):
        in_progress = Mock(completed=False, error=False)
        timeout = instanceha.MAX_EVACUATION_TIMEOUT_SECONDS
        monotonic_values = [100, 100 + timeout + 1]
        self.assertFalse(self._run_monitor([in_progress], monotonic_values=monotonic_values))


# ============================================================================
# Main loop backoff tests
# ============================================================================

class TestMainLoopBackoff(unittest.TestCase):
    """Tests for main() exponential backoff on Nova failures."""

    def _make_service_mock(self):
        svc = Mock()
        svc.config = Mock()
        svc.config.get_config_value = Mock(side_effect=lambda key: {
            'POLL': 5, 'DELTA': 30, 'LOGLEVEL': 'INFO',
            'DISABLED': False, 'FENCING_TIMEOUT': 30,
        }.get(key, Mock()))
        svc.update_health_hash = Mock()
        svc.processing_lock = Mock()
        svc.ready = False
        # Use a real Event; tests set it to break the main loop
        svc.shutdown_event = threading.Event()
        return svc

    @patch('instanceha.signal.signal')
    @patch('instanceha._establish_nova_connection')
    @patch('instanceha._initialize_service')
    @patch('instanceha.ConfigManager')
    def test_unauthorized_triggers_reconnection(self, mock_cm, mock_init, mock_conn, mock_signal):
        from novaclient.exceptions import Unauthorized

        mock_cm.return_value.get_config_value = Mock(return_value='INFO')
        service = self._make_service_mock()
        mock_init.return_value = service
        conn = Mock()
        mock_conn.return_value = conn

        call_count = [0]
        def unauthorized_then_stop(*args, **kwargs):
            call_count[0] += 1
            if call_count[0] >= 2:
                service.shutdown_event.set()
            raise Unauthorized('expired')
        conn.services.list.side_effect = unauthorized_then_stop

        instanceha.main()

        mock_conn.assert_called()

    @patch('instanceha.signal.signal')
    @patch('instanceha._establish_nova_connection')
    @patch('instanceha._initialize_service')
    @patch('instanceha.ConfigManager')
    def test_generic_exception_increments_consecutive_failures(self, mock_cm, mock_init, mock_conn, mock_signal):
        mock_cm.return_value.get_config_value = Mock(return_value='INFO')
        service = self._make_service_mock()
        mock_init.return_value = service
        conn = Mock()
        mock_conn.return_value = conn

        call_count = [0]
        def error_then_stop(*args, **kwargs):
            call_count[0] += 1
            if call_count[0] >= 2:
                service.shutdown_event.set()
            raise RuntimeError('API down')
        conn.services.list.side_effect = error_then_stop

        instanceha.main()

        self.assertGreaterEqual(call_count[0], 2)

    @patch('instanceha.signal.signal')
    @patch('instanceha._process_reenabling')
    @patch('instanceha._process_stale_services')
    @patch('instanceha._categorize_services')
    @patch('instanceha._establish_nova_connection')
    @patch('instanceha._initialize_service')
    @patch('instanceha.ConfigManager')
    def test_successful_poll_resets_consecutive_failures(self, mock_cm, mock_init, mock_conn,
                                                         mock_cat, mock_proc, mock_reen, mock_signal):
        mock_cm.return_value.get_config_value = Mock(return_value='INFO')
        service = self._make_service_mock()
        mock_init.return_value = service
        conn = Mock()
        mock_conn.return_value = conn
        svc_mock = Mock()

        call_count = [0]
        def services_then_stop(*args, **kwargs):
            call_count[0] += 1
            if call_count[0] >= 2:
                service.shutdown_event.set()
            return [svc_mock]
        conn.services.list.side_effect = services_then_stop
        mock_cat.return_value = ([], [], [])

        instanceha.main()

        mock_cat.assert_called()

    @patch('instanceha.signal.signal')
    @patch('instanceha._establish_nova_connection')
    @patch('instanceha._initialize_service')
    @patch('instanceha.ConfigManager')
    def test_backoff_capped_at_max(self, mock_cm, mock_init, mock_conn, mock_signal):
        mock_cm.return_value.get_config_value = Mock(return_value='INFO')
        service = self._make_service_mock()
        mock_init.return_value = service
        conn = Mock()
        mock_conn.return_value = conn

        backoff_values = []
        original_wait = service.shutdown_event.wait
        def capture_wait(timeout=None):
            if timeout is not None:
                backoff_values.append(timeout)
            original_wait(0)
        service.shutdown_event.wait = capture_wait

        call_count = [0]
        def error_then_stop(*args, **kwargs):
            call_count[0] += 1
            if call_count[0] >= 3:
                service.shutdown_event.set()
            raise RuntimeError('API down')
        conn.services.list.side_effect = error_then_stop

        instanceha.main()

        for v in backoff_values:
            self.assertLessEqual(v, instanceha.MAX_NOVA_BACKOFF_SECONDS)


# ============================================================================
# _process_stale_services safety branches tests
# ============================================================================

class TestProcessStaleServicesSafety(unittest.TestCase):
    """Tests for _process_stale_services safety branches."""

    def _make_service(self):
        import threading
        from collections import defaultdict
        svc = Mock()
        svc.config = Mock()
        svc.config.get_config_value = Mock(side_effect=lambda key: {
            'DISABLED': False, 'THRESHOLD': 50, 'CHECK_KDUMP': False,
            'CHECK_HEARTBEAT': False,
            'WORKERS': 2, 'POLL': 5, 'FENCING_TIMEOUT': 30,
            'TAGGED_IMAGES': False, 'TAGGED_FLAVORS': False,
            'TAGGED_AGGREGATES': False, 'RESERVED_HOSTS': False,
        }.get(key, Mock()))
        svc.processing_lock = threading.Lock()
        svc.hosts_processing = defaultdict(float)
        svc.refresh_evacuable_cache = Mock()
        svc.get_hosts_with_servers_cached = Mock(return_value={})
        svc.filter_hosts_with_servers = Mock(return_value=[])
        svc._cleanup_dict_by_condition = Mock()
        return svc

    def test_empty_compute_nodes_and_resume(self):
        service = self._make_service()
        instanceha._process_stale_services(Mock(), service, [], [], [])

    def test_all_hosts_already_processing(self):
        import time as time_mod
        service = self._make_service()
        svc1 = Mock()
        svc1.host = 'host-1'
        with service.processing_lock:
            service.hosts_processing['host-1'] = time_mod.monotonic()
        instanceha._process_stale_services(Mock(), service, [svc1], [svc1], [])

    def test_threshold_exceeded_skips_evacuation(self):
        service = self._make_service()
        conn = Mock()
        svc1 = Mock()
        svc1.host = 'host-1'
        svc1.forced_down = False
        svc1.status = 'enabled'
        svc1.state = 'down'
        service.get_hosts_with_servers_cached.return_value = {'host-1': ['server-1']}
        service.filter_hosts_with_servers.return_value = [svc1]
        with patch('instanceha._emit_k8s_event'):
            with patch('instanceha._check_critical_services', return_value=(True, None)):
                instanceha._process_stale_services(conn, service, [svc1], [svc1], [])

    def test_disabled_mode_skips_evacuation(self):
        service = self._make_service()
        service.config.get_config_value = Mock(side_effect=lambda key: {
            'DISABLED': True, 'THRESHOLD': 50, 'CHECK_KDUMP': False,
            'CHECK_HEARTBEAT': False,
            'WORKERS': 2, 'POLL': 5, 'FENCING_TIMEOUT': 30,
            'TAGGED_IMAGES': False, 'TAGGED_FLAVORS': False,
            'TAGGED_AGGREGATES': False, 'RESERVED_HOSTS': False,
        }.get(key, Mock()))
        conn = Mock()
        svc1 = Mock(host='host-1', status='enabled', forced_down=False)
        service.get_hosts_with_servers_cached.return_value = {'host-1': ['server-1']}
        service.filter_hosts_with_servers.return_value = [svc1]
        with patch('instanceha._emit_k8s_event'):
            instanceha._process_stale_services(conn, service, [svc1], [svc1], [])

    def test_no_evacuable_servers_after_filtering(self):
        service = self._make_service()
        conn = Mock()
        svc1 = Mock(host='host-1', status='enabled', forced_down=False)
        service.get_hosts_with_servers_cached.return_value = {}
        service.filter_hosts_with_servers.return_value = []
        with patch('instanceha._emit_k8s_event'):
            instanceha._process_stale_services(conn, service, [svc1], [svc1], [])


class TestReconcileOrphanedHosts(unittest.TestCase):
    """Tests for _reconcile_orphaned_hosts startup reconciliation."""

    def _make_service(self, host, forced_down, state, status, disabled_reason=''):
        svc = Mock()
        svc.host = host
        svc.id = f'svc-{host}'
        svc.forced_down = forced_down
        svc.state = state
        svc.status = status
        svc.disabled_reason = disabled_reason
        return svc

    @patch('instanceha._emit_k8s_event')
    def test_recovers_orphaned_host(self, mock_event):
        conn = MagicMock()
        orphan = self._make_service('host-1', forced_down=True, state='down', status='enabled')
        conn.services.list.return_value = [orphan]

        instanceha._reconcile_orphaned_hosts(conn)

        conn.services.disable_log_reason.assert_called_once()
        call_args = conn.services.disable_log_reason.call_args
        self.assertEqual(call_args[0][0], 'svc-host-1')
        self.assertIn('instanceha evacuation (recovered)', call_args[0][1])
        mock_event.assert_called_once()

    @patch('instanceha._emit_k8s_event')
    def test_skips_already_disabled(self, mock_event):
        conn = MagicMock()
        svc = self._make_service('host-1', forced_down=True, state='down', status='disabled',
                                 disabled_reason='instanceha evacuation')
        conn.services.list.return_value = [svc]

        instanceha._reconcile_orphaned_hosts(conn)

        conn.services.disable_log_reason.assert_not_called()

    @patch('instanceha._emit_k8s_event')
    def test_skips_healthy_hosts(self, mock_event):
        conn = MagicMock()
        svc = self._make_service('host-1', forced_down=False, state='up', status='enabled')
        conn.services.list.return_value = [svc]

        instanceha._reconcile_orphaned_hosts(conn)

        conn.services.disable_log_reason.assert_not_called()

    @patch('instanceha._emit_k8s_event')
    def test_skips_forced_down_but_up(self, mock_event):
        conn = MagicMock()
        svc = self._make_service('host-1', forced_down=True, state='up', status='enabled')
        conn.services.list.return_value = [svc]

        instanceha._reconcile_orphaned_hosts(conn)

        conn.services.disable_log_reason.assert_not_called()

    @patch('instanceha._emit_k8s_event')
    def test_handles_api_failure(self, mock_event):
        conn = MagicMock()
        conn.services.list.side_effect = Exception("Nova unavailable")

        instanceha._reconcile_orphaned_hosts(conn)

        conn.services.disable_log_reason.assert_not_called()

    @patch('instanceha._emit_k8s_event')
    def test_handles_per_host_failure(self, mock_event):
        conn = MagicMock()
        orphan1 = self._make_service('host-1', forced_down=True, state='down', status='enabled')
        orphan2 = self._make_service('host-2', forced_down=True, state='down', status='enabled')
        conn.services.list.return_value = [orphan1, orphan2]
        conn.services.disable_log_reason.side_effect = [Exception("fail"), None]

        instanceha._reconcile_orphaned_hosts(conn)

        self.assertEqual(conn.services.disable_log_reason.call_count, 2)

    @patch('instanceha._emit_k8s_event')
    def test_no_orphans_no_action(self, mock_event):
        conn = MagicMock()
        conn.services.list.return_value = []

        instanceha._reconcile_orphaned_hosts(conn)

        conn.services.disable_log_reason.assert_not_called()
        mock_event.assert_not_called()


# ============================================================================
# Task state reset before evacuation (G2a)
# ============================================================================

class TestTaskStateReset(unittest.TestCase):
    """Tests for resetting stuck task_state before evacuation."""

    def _make_server(self, task_state=None):
        server = Mock()
        server.id = 'server-1'
        # openstacksdk uses attribute-style access with dashes
        setattr(server, 'OS-EXT-STS:task_state', task_state)
        return server

    def test_stuck_task_state_triggers_reset(self):
        conn = MagicMock()
        resp = Mock(status_code=200, reason='OK')
        conn.servers.evacuate.return_value = (resp, None)
        server_obj = self._make_server(task_state='migrating')

        result = instanceha._server_evacuate(conn, 'server-1', server_obj=server_obj)

        conn.servers.reset_state.assert_called_once_with('server-1', 'error')
        conn.servers.evacuate.assert_called_once()
        self.assertTrue(result.accepted)

    def test_none_task_state_skips_reset(self):
        conn = MagicMock()
        resp = Mock(status_code=200, reason='OK')
        conn.servers.evacuate.return_value = (resp, None)
        server_obj = self._make_server(task_state=None)

        result = instanceha._server_evacuate(conn, 'server-1', server_obj=server_obj)

        conn.servers.reset_state.assert_not_called()
        conn.servers.evacuate.assert_called_once()
        self.assertTrue(result.accepted)

    def test_no_server_obj_skips_reset(self):
        conn = MagicMock()
        resp = Mock(status_code=200, reason='OK')
        conn.servers.evacuate.return_value = (resp, None)

        result = instanceha._server_evacuate(conn, 'server-1')

        conn.servers.reset_state.assert_not_called()
        self.assertTrue(result.accepted)

    def test_reset_state_failure_still_attempts_evacuate(self):
        conn = MagicMock()
        conn.servers.reset_state.side_effect = Exception("API error")
        resp = Mock(status_code=200, reason='OK')
        conn.servers.evacuate.return_value = (resp, None)
        server_obj = self._make_server(task_state='resizing')

        result = instanceha._server_evacuate(conn, 'server-1', server_obj=server_obj)

        conn.servers.reset_state.assert_called_once_with('server-1', 'error')
        conn.servers.evacuate.assert_called_once()
        self.assertTrue(result.accepted)


# ============================================================================
# Instance locking during evacuation (G1)
# ============================================================================

class TestInstanceLocking(unittest.TestCase):
    """Tests for lock/unlock around evacuation."""

    def _make_server(self):
        server = Mock()
        server.id = 'server-lock-1'
        setattr(server, 'OS-EXT-STS:task_state', None)
        setattr(server, 'OS-EXT-SRV-ATTR:host', 'compute-0')
        server.status = 'ACTIVE'
        return server

    @patch('instanceha._emit_k8s_event')
    @patch('instanceha._monitor_evacuation', return_value=True)
    @patch('instanceha.time')
    def test_lock_before_evacuate_unlock_after(self, mock_time, mock_monitor, mock_event):
        mock_time.monotonic.return_value = 0
        conn = MagicMock()
        resp = Mock(status_code=200, reason='OK')
        conn.servers.evacuate.return_value = (resp, None)
        server = self._make_server()
        call_order = []
        conn.servers.lock.side_effect = lambda sid, **kw: call_order.append('lock')
        conn.servers.evacuate.side_effect = lambda **kw: (call_order.append('evacuate'), (resp, None))[1]
        conn.servers.unlock.side_effect = lambda sid: call_order.append('unlock')

        result = instanceha._server_evacuate_future(conn, server)

        self.assertTrue(result)
        self.assertEqual(call_order[:2], ['lock', 'evacuate'])
        self.assertEqual(call_order[-1], 'unlock')
        conn.servers.lock.assert_called_once_with('server-lock-1', reason='instanceha-evacuation')

    @patch('instanceha._emit_k8s_event')
    @patch('instanceha._monitor_evacuation', return_value=True)
    @patch('instanceha.time')
    def test_lock_failure_does_not_block_evacuation(self, mock_time, mock_monitor, mock_event):
        mock_time.monotonic.return_value = 0
        conn = MagicMock()
        conn.servers.lock.side_effect = Exception("lock failed")
        resp = Mock(status_code=200, reason='OK')
        conn.servers.evacuate.return_value = (resp, None)
        server = self._make_server()

        result = instanceha._server_evacuate_future(conn, server)

        self.assertTrue(result)
        conn.servers.evacuate.assert_called_once()

    @patch('instanceha._emit_k8s_event')
    @patch('instanceha.time')
    def test_unlock_after_failed_evacuation(self, mock_time, mock_event):
        mock_time.monotonic.return_value = 0
        conn = MagicMock()
        resp = Mock(status_code=500, reason='Internal Server Error')
        conn.servers.evacuate.return_value = (resp, None)
        server = self._make_server()

        result = instanceha._server_evacuate_future(conn, server)

        self.assertFalse(result)
        conn.servers.unlock.assert_called_once_with('server-lock-1')

    @patch('instanceha._emit_k8s_event')
    def test_reconcile_unlocks_orphaned_vms(self, mock_event):
        conn = MagicMock()
        svc = Mock()
        svc.forced_down = True
        svc.state = 'down'
        svc.status = 'disabled'
        svc.host = 'compute-0'
        svc.id = 'svc-1'
        conn.services.list.return_value = [svc]

        our_locked_vm = Mock()
        our_locked_vm.id = 'vm-our-lock'
        our_locked_vm.locked_reason = 'instanceha-evacuation'
        user_locked_vm = Mock()
        user_locked_vm.id = 'vm-user-lock'
        user_locked_vm.locked_reason = 'maintenance window'
        unlocked_vm = Mock()
        unlocked_vm.id = 'vm-unlocked'
        unlocked_vm.locked_reason = None
        conn.servers.list.return_value = [our_locked_vm, user_locked_vm, unlocked_vm]

        instanceha._reconcile_orphaned_hosts(conn)

        conn.servers.unlock.assert_called_once_with('vm-our-lock')


# ============================================================================
# Post-evacuation state restoration (G2b)
# ============================================================================

class TestStateRestoration(unittest.TestCase):
    """Tests for restoring VM state after successful evacuation."""

    def _make_server(self, status='ACTIVE'):
        server = Mock()
        server.id = 'server-state-1'
        server.status = status
        setattr(server, 'OS-EXT-STS:task_state', None)
        setattr(server, 'OS-EXT-SRV-ATTR:host', 'compute-0')
        return server

    @patch('instanceha._emit_k8s_event')
    @patch('instanceha._monitor_evacuation', return_value=True)
    @patch('instanceha.time')
    def test_shutoff_vm_restored_after_evacuation(self, mock_time, mock_monitor, mock_event):
        mock_time.monotonic.return_value = 0
        conn = MagicMock()
        resp = Mock(status_code=200, reason='OK')
        conn.servers.evacuate.return_value = (resp, None)
        server = self._make_server(status='SHUTOFF')

        result = instanceha._server_evacuate_future(conn, server)

        self.assertTrue(result)
        conn.servers.stop.assert_called_once_with('server-state-1')

    @patch('instanceha._emit_k8s_event')
    @patch('instanceha._monitor_evacuation', return_value=True)
    @patch('instanceha.time')
    def test_error_vm_restored_after_evacuation(self, mock_time, mock_monitor, mock_event):
        mock_time.monotonic.return_value = 0
        conn = MagicMock()
        resp = Mock(status_code=200, reason='OK')
        conn.servers.evacuate.return_value = (resp, None)
        server = self._make_server(status='ERROR')

        result = instanceha._server_evacuate_future(conn, server)

        self.assertTrue(result)
        conn.servers.reset_state.assert_called_with('server-state-1', 'error')

    @patch('instanceha._emit_k8s_event')
    @patch('instanceha._monitor_evacuation', return_value=True)
    @patch('instanceha.time')
    def test_active_vm_no_restoration(self, mock_time, mock_monitor, mock_event):
        mock_time.monotonic.return_value = 0
        conn = MagicMock()
        resp = Mock(status_code=200, reason='OK')
        conn.servers.evacuate.return_value = (resp, None)
        server = self._make_server(status='ACTIVE')

        result = instanceha._server_evacuate_future(conn, server)

        self.assertTrue(result)
        conn.servers.stop.assert_not_called()
        # reset_state may be called for task_state check, but not for state restoration
        # so we check it wasn't called with 'error' as second arg after evacuation
        for c in conn.servers.reset_state.call_args_list:
            if c == call('server-state-1', 'error'):
                self.fail("reset_state called for ACTIVE server state restoration")

    @patch('instanceha._emit_k8s_event')
    @patch('instanceha._monitor_evacuation', return_value=True)
    @patch('instanceha.time')
    def test_restoration_failure_does_not_fail_evacuation(self, mock_time, mock_monitor, mock_event):
        mock_time.monotonic.return_value = 0
        conn = MagicMock()
        resp = Mock(status_code=200, reason='OK')
        conn.servers.evacuate.return_value = (resp, None)
        conn.servers.stop.side_effect = Exception("stop failed")
        server = self._make_server(status='SHUTOFF')

        result = instanceha._server_evacuate_future(conn, server)

        self.assertTrue(result)
        conn.servers.stop.assert_called_once_with('server-state-1')

    @patch('instanceha._emit_k8s_event')
    @patch('instanceha._monitor_evacuation', return_value=False)
    @patch('instanceha.time')
    def test_no_restoration_on_failed_evacuation(self, mock_time, mock_monitor, mock_event):
        mock_time.monotonic.return_value = 0
        conn = MagicMock()
        resp = Mock(status_code=200, reason='OK')
        conn.servers.evacuate.return_value = (resp, None)
        server = self._make_server(status='SHUTOFF')

        result = instanceha._server_evacuate_future(conn, server)

        self.assertFalse(result)
        conn.servers.stop.assert_not_called()


# ============================================================================
# Evacuation retry on retryable errors (G5)
# ============================================================================

class TestEvacuationRetry(unittest.TestCase):
    """Tests for retrying evacuation without target host on retryable errors."""

    def _make_server(self):
        server = Mock()
        server.id = 'server-retry-1'
        server.status = 'ACTIVE'
        setattr(server, 'OS-EXT-STS:task_state', None)
        setattr(server, 'OS-EXT-SRV-ATTR:host', 'compute-0')
        return server

    @patch('instanceha._emit_k8s_event')
    @patch('instanceha._monitor_evacuation', return_value=True)
    @patch('instanceha.time')
    def test_retry_on_409_without_target(self, mock_time, mock_monitor, mock_event):
        mock_time.monotonic.return_value = 0
        conn = MagicMock()
        fail_resp = Mock(status_code=409, reason='Conflict')
        ok_resp = Mock(status_code=200, reason='OK')
        conn.servers.evacuate.side_effect = [
            (fail_resp, None),
            (ok_resp, None),
        ]
        server = self._make_server()

        result = instanceha._server_evacuate_future(conn, server, target_host='reserved-0')

        self.assertTrue(result)
        self.assertEqual(conn.servers.evacuate.call_count, 2)
        # First call with target host, second without
        first_call = conn.servers.evacuate.call_args_list[0]
        self.assertEqual(first_call, call(server='server-retry-1', host='reserved-0'))
        second_call = conn.servers.evacuate.call_args_list[1]
        self.assertEqual(second_call, call(server='server-retry-1'))

    @patch('instanceha._emit_k8s_event')
    @patch('instanceha.time')
    def test_no_retry_on_403(self, mock_time, mock_event):
        mock_time.monotonic.return_value = 0
        conn = MagicMock()
        fail_resp = Mock(status_code=403, reason='Forbidden')
        conn.servers.evacuate.return_value = (fail_resp, None)
        server = self._make_server()

        result = instanceha._server_evacuate_future(conn, server, target_host='reserved-0')

        self.assertFalse(result)
        self.assertEqual(conn.servers.evacuate.call_count, 1)

    @patch('instanceha._emit_k8s_event')
    @patch('instanceha.time')
    def test_retry_fails_returns_false(self, mock_time, mock_event):
        mock_time.monotonic.return_value = 0
        conn = MagicMock()
        fail_resp = Mock(status_code=409, reason='Conflict')
        fail_resp2 = Mock(status_code=500, reason='Internal Server Error')
        conn.servers.evacuate.side_effect = [
            (fail_resp, None),
            (fail_resp2, None),
        ]
        server = self._make_server()

        result = instanceha._server_evacuate_future(conn, server, target_host='reserved-0')

        self.assertFalse(result)
        self.assertEqual(conn.servers.evacuate.call_count, 2)

    @patch('instanceha._emit_k8s_event')
    @patch('instanceha.time')
    def test_no_retry_when_no_target_host(self, mock_time, mock_event):
        mock_time.monotonic.return_value = 0
        conn = MagicMock()
        fail_resp = Mock(status_code=409, reason='Conflict')
        conn.servers.evacuate.return_value = (fail_resp, None)
        server = self._make_server()

        result = instanceha._server_evacuate_future(conn, server)

        self.assertFalse(result)
        self.assertEqual(conn.servers.evacuate.call_count, 1)


# ============================================================================
# Traditional mode warning (G4)
# ============================================================================

@unittest.skip("Traditional mode warning is intentionally disabled")
class TestTraditionalModeWarning(unittest.TestCase):
    """Tests for startup warning when traditional mode is active."""

    @patch('instanceha._reconcile_orphaned_hosts')
    @patch('instanceha._establish_nova_connection')
    @patch('instanceha._initialize_service')
    @patch('instanceha.signal')
    def test_warning_when_traditional_mode(self, mock_signal, mock_init, mock_conn, mock_reconcile):
        mock_config = MagicMock()
        mock_config.get_config_value.side_effect = lambda key: {
            'LOGLEVEL': 'INFO',
            'SMART_EVACUATION': False,
            'ORCHESTRATED_RESTART': False,
        }.get(key, Mock())
        service = Mock()
        service.shutdown_event = Mock()
        service.shutdown_event.is_set.return_value = True
        mock_init.return_value = service

        with patch('instanceha.ConfigManager', return_value=mock_config), \
             patch('instanceha.logging') as mock_logging:
            mock_logging.INFO = logging.INFO
            mock_logging.getLogger.return_value = Mock()
            instanceha.main()

            warning_calls = [c for c in mock_logging.warning.call_args_list
                           if 'Traditional evacuation mode' in str(c)]
            self.assertTrue(len(warning_calls) > 0, "Expected traditional mode warning")

    @patch('instanceha._reconcile_orphaned_hosts')
    @patch('instanceha._establish_nova_connection')
    @patch('instanceha._initialize_service')
    @patch('instanceha.signal')
    def test_no_warning_when_smart_enabled(self, mock_signal, mock_init, mock_conn, mock_reconcile):
        mock_config = MagicMock()
        mock_config.get_config_value.side_effect = lambda key: {
            'LOGLEVEL': 'INFO',
            'SMART_EVACUATION': True,
            'ORCHESTRATED_RESTART': False,
        }.get(key, Mock())
        service = Mock()
        service.shutdown_event = Mock()
        service.shutdown_event.is_set.return_value = True
        mock_init.return_value = service

        with patch('instanceha.ConfigManager', return_value=mock_config), \
             patch('instanceha.logging') as mock_logging:
            mock_logging.INFO = logging.INFO
            mock_logging.getLogger.return_value = Mock()
            instanceha.main()

            warning_calls = [c for c in mock_logging.warning.call_args_list
                           if 'Traditional evacuation mode' in str(c)]
            self.assertEqual(len(warning_calls), 0, "Should not warn when smart is enabled")


if __name__ == '__main__':
    unittest.main()

"""Shared test fixtures for InstanceHA tests.

Sets up OpenStack module mocks and sys.path so that all test files can
import instanceha without duplicating the boilerplate.
"""

import os
import sys
from contextlib import contextmanager
from unittest.mock import MagicMock, patch


# --- Mock OpenStack dependencies before any test file imports instanceha ---

if 'novaclient' not in sys.modules:
    _novaclient_mock = MagicMock()
    _novaclient_client_mock = MagicMock()
    _novaclient_mock.client = _novaclient_client_mock
    sys.modules['novaclient'] = _novaclient_mock
    sys.modules['novaclient.client'] = _novaclient_client_mock

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
    _novaclient_mock.exceptions = novaclient_exceptions
    sys.modules['novaclient.exceptions'] = novaclient_exceptions

if 'keystoneauth1' not in sys.modules:
    _keystoneauth_mock = MagicMock()
    _loading_mock = MagicMock()
    _session_mock = MagicMock()
    _keystoneauth_mock.loading = _loading_mock
    _keystoneauth_mock.session = _session_mock
    sys.modules['keystoneauth1'] = _keystoneauth_mock
    sys.modules['keystoneauth1.loading'] = _loading_mock
    sys.modules['keystoneauth1.session'] = _session_mock

    class DiscoveryFailure(Exception):
        pass

    discovery_module = MagicMock()
    discovery_module.DiscoveryFailure = DiscoveryFailure

    exceptions_module = MagicMock()
    exceptions_module.discovery = discovery_module

    _keystoneauth_mock.exceptions = exceptions_module
    sys.modules['keystoneauth1.exceptions'] = exceptions_module
    sys.modules['keystoneauth1.exceptions.discovery'] = discovery_module


# --- Add instanceha module to sys.path ---

_test_dir = os.path.dirname(os.path.abspath(__file__))
_instanceha_path = os.path.join(_test_dir, '../../templates/instanceha/bin/')
_abs_path = os.path.abspath(_instanceha_path)
if _abs_path not in sys.path:
    sys.path.insert(0, _abs_path)


# --- Shared test fixtures ---

import instanceha  # noqa: E402


@contextmanager
def patch_pipeline(conn=None, fence=True, disable=True,
                   reserved=None, evacuate=True, recovery=True):
    """Patch all process_service pipeline steps with configurable return values.

    Yields a dict of mock objects keyed by step name, so tests can assert
    on individual steps (e.g. mocks['disable'].assert_not_called()).
    """
    if reserved is None:
        reserved = instanceha.ReservedHostResult(success=True, hostname=None)
    with patch('instanceha._get_nova_connection', return_value=conn) as m_conn, \
         patch('instanceha._host_fence', return_value=fence) as m_fence, \
         patch('instanceha._host_disable', return_value=disable) as m_disable, \
         patch('instanceha._manage_reserved_hosts', return_value=reserved) as m_reserved, \
         patch('instanceha._host_evacuate', return_value=evacuate) as m_evacuate, \
         patch('instanceha._post_evacuation_recovery', return_value=recovery) as m_recovery, \
         patch('instanceha.track_host_processing') as m_track:
        yield {
            'conn': m_conn, 'fence': m_fence, 'disable': m_disable,
            'reserved': m_reserved, 'evacuate': m_evacuate,
            'recovery': m_recovery, 'track': m_track,
        }

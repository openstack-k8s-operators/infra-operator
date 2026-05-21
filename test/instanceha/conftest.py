"""Shared test fixtures for InstanceHA tests.

Sets up OpenStack module mocks and sys.path so that all test files can
import instanceha without duplicating the boilerplate.
"""

import os
import sys
from unittest.mock import MagicMock


# --- Mock OpenStack dependencies before any test file imports instanceha ---

if 'novaclient' not in sys.modules:
    sys.modules['novaclient'] = MagicMock()
    sys.modules['novaclient.client'] = MagicMock()

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

    class DiscoveryFailure(Exception):
        pass

    discovery_module = MagicMock()
    discovery_module.DiscoveryFailure = DiscoveryFailure

    exceptions_module = MagicMock()
    exceptions_module.discovery = discovery_module

    sys.modules['keystoneauth1.exceptions'] = exceptions_module
    sys.modules['keystoneauth1.exceptions.discovery'] = discovery_module


# --- Add instanceha module to sys.path ---

_test_dir = os.path.dirname(os.path.abspath(__file__))
_instanceha_path = os.path.join(_test_dir, '../../templates/instanceha/bin/')
_abs_path = os.path.abspath(_instanceha_path)
if _abs_path not in sys.path:
    sys.path.insert(0, _abs_path)

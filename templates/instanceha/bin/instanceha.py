#!/usr/libexec/platform-python -tt

import fnmatch
import os
import sys
import time
import logging
import re
from datetime import datetime, timedelta, timezone
from contextlib import contextmanager
from dataclasses import dataclass
from enum import Enum
import concurrent.futures
import requests
import requests.exceptions
import yaml
import subprocess
from yaml.loader import SafeLoader
import socket
import struct
import signal
import threading
import hashlib
import hmac as hmac_mod
import json
import uuid
from typing import Dict, Any, Optional, Union, List, Protocol, Tuple, Callable
from collections import defaultdict
from abc import ABC, abstractmethod

from prometheus_client import Counter, Gauge, Histogram, generate_latest, CONTENT_TYPE_LATEST


class ConfigurationError(Exception):
    """Raised when configuration loading or validation fails."""
    pass


class NovaConnectionError(Exception):
    """Raised when Nova API connection fails."""
    pass


# Constants
CACHE_TIMEOUT_SECONDS = 300
KDUMP_CLEANUP_THRESHOLD = 2000
KDUMP_CLEANUP_AGE_SECONDS = 300
MAX_EVACUATION_TIMEOUT_SECONDS = 300
FUTURE_RESULT_TIMEOUT_SECONDS = MAX_EVACUATION_TIMEOUT_SECONDS + 60
INITIAL_EVACUATION_WAIT_SECONDS = 10
EVACUATION_POLL_INTERVAL_SECONDS = 5
EVACUATION_RETRY_WAIT_SECONDS = 5
DEFAULT_EVACUATION_RETRIES = 5
MAX_API_RETRIES = 10
MAX_ENABLE_RETRIES = 3
MAX_FENCING_RETRIES = 3
HEALTH_CHECK_PORT = 8080
DEFAULT_UDP_PORT = 7410
KDUMP_MAGIC_NUMBER = 0x1B302A40
MAX_PROCESSING_TIME_PADDING_SECONDS = 30
MIGRATION_QUERY_MINUTES = 5
MIGRATION_QUERY_LIMIT = 1000
USERNAME_MAX_LENGTH = 64
FENCING_RETRY_DELAY_SECONDS = 1
KDUMP_REENABLE_DELAY_SECONDS = 60
MAX_NOVA_BACKOFF_SECONDS = 300
MAX_CONSECUTIVE_FAILURES_READY = 3
NOVA_API_TIMEOUT_SECONDS = 30
MAX_TOTAL_EVACUATION_THREADS = 32
HEARTBEAT_MAGIC_NUMBER = 0x48425632
HEARTBEAT_HMAC_DIGEST_SIZE = 32
HEARTBEAT_MIN_PACKET_SIZE = 4 + 8 + 1 + 32  # magic + timestamp + min hostname + HMAC
DEFAULT_HEARTBEAT_PORT = 7411
HEARTBEAT_CLEANUP_THRESHOLD = 2000
HEARTBEAT_CLEANUP_AGE_SECONDS = 600

# Disabled reason markers
LOCK_REASON_EVACUATION = "instanceha-evacuation"
DISABLED_REASON_EVACUATION = "instanceha evacuation"
DISABLED_REASON_EVACUATION_COMPLETE = "instanceha evacuation complete"
DISABLED_REASON_EVACUATION_FAILED = "instanceha evacuation FAILED"
DISABLED_REASON_KDUMP_MARKER = "(kdump)"

# Per-aggregate failure threshold metadata key
AGGREGATE_MAX_FAILURES_KEY = 'instanceha:max_failures'

# Prometheus metrics
FENCING_TOTAL = Counter(
    'instanceha_fencing_total',
    'Total fencing operations',
    ['host', 'result'],
)
EVACUATION_TOTAL = Counter(
    'instanceha_evacuation_total',
    'Total host-level evacuation operations',
    ['host', 'result'],
)
INSTANCE_EVACUATION_TOTAL = Counter(
    'instanceha_instance_evacuation_total',
    'Total per-instance evacuation operations',
    ['host', 'result'],
)
INSTANCE_EVACUATION_DURATION = Histogram(
    'instanceha_instance_evacuation_duration_seconds',
    'Duration of individual instance evacuations',
    ['host'],
    buckets=[10, 30, 60, 120, 180, 300, 600],
)
HOST_DOWN_TOTAL = Counter(
    'instanceha_host_down_total',
    'Total host-down detections',
    ['host'],
)
HOST_REACHABLE_TOTAL = Counter(
    'instanceha_host_reachable_total',
    'Hosts reported down by Nova but still reachable via heartbeat',
    ['host'],
)
HOST_REENABLED_TOTAL = Counter(
    'instanceha_host_reenabled_total',
    'Hosts re-enabled after evacuation',
    ['host'],
)
THRESHOLD_EXCEEDED_TOTAL = Counter(
    'instanceha_threshold_exceeded_total',
    'Times evacuation was skipped due to threshold',
)
AGGREGATE_THRESHOLD_EXCEEDED_TOTAL = Counter(
    'instanceha_aggregate_threshold_exceeded_total',
    'Times evacuation was blocked for an aggregate due to per-aggregate threshold',
    ['aggregate'],
)
RECOVERY_COMPLETED_TOTAL = Counter(
    'instanceha_recovery_completed_total',
    'Full recovery workflows completed',
    ['host'],
)
PROCESSING_FAILED_TOTAL = Counter(
    'instanceha_processing_failed_total',
    'Service processing failures',
    ['host'],
)
ORPHANED_HOST_RECOVERED_TOTAL = Counter(
    'instanceha_orphaned_host_recovered_total',
    'Orphaned fenced hosts recovered at startup',
)
POLL_CONSECUTIVE_FAILURES = Gauge(
    'instanceha_poll_consecutive_failures',
    'Current consecutive Nova API poll failures',
)
HOSTS_PROCESSING = Gauge(
    'instanceha_hosts_processing',
    'Number of hosts currently being processed',
)
POLL_CYCLE_TOTAL = Counter(
    'instanceha_poll_cycles_total',
    'Total poll cycles executed',
    ['result'],
)
HEARTBEAT_REJECTED_TOTAL = Counter(
    'instanceha_heartbeat_rejected_total',
    'Heartbeat packets rejected',
    ['reason'],
)
K8S_API_REACHABLE = Gauge(
    'instanceha_k8s_api_reachable',
    'Whether the Kubernetes API is reachable (1=yes, 0=no)',
)
HEARTBEAT_CLIFF_TOTAL = Counter(
    'instanceha_heartbeat_cliff_total',
    'Times fencing was skipped due to sudden heartbeat loss',
)


# Enums
class MatchType(Enum):
    """Type of matching to use for reserved hosts."""
    AGGREGATE = "aggregate"
    ZONE = "zone"


class MigrationStatus(Enum):
    """Migration status values."""
    COMPLETED = "completed"
    DONE = "done"
    ERROR = "error"
    FAILED = "failed"
    CANCELLED = "cancelled"


# Migration status constants for checking migration state
MIGRATION_STATUS_COMPLETED = [MigrationStatus.COMPLETED.value, MigrationStatus.DONE.value]
MIGRATION_STATUS_ERROR = [MigrationStatus.ERROR.value, MigrationStatus.FAILED.value, MigrationStatus.CANCELLED.value]


# Dataclasses
@dataclass
class ConfigItem:
    """Configuration item with type and validation constraints."""
    type: str
    default: Any
    min_val: Optional[int] = None
    max_val: Optional[int] = None


@dataclass
class EvacuationResult:
    """Result of a server evacuation request."""
    uuid: str
    accepted: bool
    reason: str
    status_code: Optional[int] = None


@dataclass
class EvacuationStatus:
    """Status of an ongoing server evacuation."""
    completed: bool
    error: bool


@dataclass
class NovaLoginCredentials:
    """Credentials for Nova API login."""
    username: str
    password: str
    project_name: str
    auth_url: str
    user_domain_name: str
    project_domain_name: str
    region_name: str


@dataclass
class ACLoginCredentials:
    """Credentials for Application Credential based login."""
    auth_url: str
    application_credential_id: str
    application_credential_secret: str
    region_name: str


@dataclass
class ReservedHostResult:
    """Result of reserved host management operation."""
    success: bool
    hostname: Optional[str] = None


# Pre-compiled regex patterns for credential sanitization
_SECRET_PATTERNS = {
    'password': re.compile(r'\bpassword=[^\s)\'\"]+', re.IGNORECASE),
    'token': re.compile(r'\btoken=[^\s)\'\"]+', re.IGNORECASE),
    'secret': re.compile(r'\bsecret=[^\s)\'\"]+', re.IGNORECASE),
    'credential': re.compile(r'\bcredential=[^\s)\'\"]+', re.IGNORECASE),
    'application_credential_secret': re.compile(r'\bapplication_credential_secret=[^\s)\'\"]+', re.IGNORECASE),
    'application_credential_id': re.compile(r'\bapplication_credential_id=[^\s)\'\"]+', re.IGNORECASE),
    'auth': re.compile(r'\bauth=[^\s)\'\"]+', re.IGNORECASE),
    'json_password': re.compile(r'("(?:password|passwd|token|secret|credential|application_credential_secret|application_credential_id)")\s*:\s*"(?:[^"\\]|\\.)*"', re.IGNORECASE),
    'authorization': re.compile(r'\bAuthorization:\s*\S+', re.IGNORECASE),
}


def _sanitize_message(msg: str) -> str:
    """Strip credentials from a message string."""
    for secret_word, pattern in _SECRET_PATTERNS.items():
        if secret_word == 'json_password':
            msg = pattern.sub(r'\1: "***"', msg)
        else:
            msg = pattern.sub(f'{secret_word}=***', msg)
    return msg


def _make_disable_reason(prefix, suffix=""):
    return f"{prefix}{suffix}: {datetime.now(timezone.utc).isoformat()}"


def _safe_log_exception(msg: str, e: Exception, include_traceback: bool = False) -> None:
    """Log exception without exposing secrets in messages or tracebacks."""
    safe_msg = _sanitize_message(str(e))
    logging.error("%s: %s", msg, safe_msg)
    if include_traceback:
        logging.debug("Exception traceback:", exc_info=True)


def _interruptible_sleep(shutdown_event, seconds):
    """Sleep that can be interrupted by a shutdown event."""
    if shutdown_event:
        shutdown_event.wait(seconds)
    else:
        time.sleep(seconds)


def _retry_with_backoff(func, max_attempts, label, delay=FENCING_RETRY_DELAY_SECONDS):
    """Retry func with linear backoff. Returns func result on success, raises on final failure."""
    for attempt in range(max_attempts):
        try:
            return func()
        except Exception as e:
            if attempt < max_attempts - 1:
                logging.warning('%s failed (attempt %d/%d): %s', label, attempt + 1, max_attempts, _sanitize_message(str(e)))
                time.sleep(delay * (attempt + 1))
            else:
                logging.error('%s failed after %d attempts: %s', label, max_attempts, _sanitize_message(str(e)))
                raise


# Security validation patterns
VALIDATION_PATTERNS = {
    'k8s_namespace': (r'^[a-z0-9]([-a-z0-9]*[a-z0-9])?$', 63),
    'k8s_resource': (r'^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$', 253),
    'power_action': (['on', 'off', 'status', 'reset', 'cycle', 'soft', 'On', 'ForceOff',
                      'GracefulShutdown', 'GracefulRestart', 'ForceRestart', 'Nmi',
                      'ForceOn', 'PushPowerButton'], None),
    'ip_address': ('ip', None),  # Special marker for IP address validation
    'port': ('port', None),  # Special marker for port validation
    'username': (r'^[a-zA-Z0-9_-]{1,64}$', USERNAME_MAX_LENGTH),
    'hostname': (r'^[a-zA-Z0-9]([a-zA-Z0-9._-]*[a-zA-Z0-9])?$', USERNAME_MAX_LENGTH)
}

def _try_validate(validator_func: Callable[[], bool], error_msg: str, context: str, log_error: bool = True) -> bool:
    """Helper to execute validation with consistent error handling."""
    try:
        return validator_func()
    except (ValueError, AttributeError, TypeError) as e:
        if log_error:
            logging.error("%s for %s: %s", error_msg, context, e)
        return False


def validate_input(value: str, validation_type: str, context: str) -> bool:
    """Unified validation to prevent SSRF and injection attacks."""
    from urllib.parse import urlparse
    import ipaddress

    if not value:
        return False

    # Special validation types
    if validation_type == 'url':
        def _validate_url():
            p = urlparse(value)
            if p.scheme not in ['http', 'https'] or not p.netloc:
                return False
            # Block localhost, loopback, and link-local addresses
            h = p.hostname
            if not h:
                return False
            h_lower = h.lower()
            if h_lower in ['localhost', '0.0.0.0']:
                logging.error("Blocked localhost access in %s", context)
                return False
            # Check IP literals against blocked ranges
            try:
                addr = ipaddress.ip_address(h_lower.strip('[]'))
                if addr.is_loopback or addr.is_link_local:
                    logging.error("Blocked loopback/link-local access in %s", context)
                    return False
            except ValueError:
                pass  # Not an IP literal (hostname) - allow through
            return True
        return _try_validate(_validate_url, "Invalid URL", context, log_error=False)

    if validation_type == 'ip_address':
        return _try_validate(lambda: ipaddress.ip_address(value) and True,
                            "Invalid IP address", context)

    if validation_type == 'port':
        def _validate_port():
            port_int = int(value)
            if 1 <= port_int <= 65535:
                return True
            logging.error("Port out of range for %s: %s", context, value)
            return False
        return _try_validate(_validate_port, "Invalid port format", context)

    # Pattern-based validation
    pattern_data = VALIDATION_PATTERNS.get(validation_type)
    if not pattern_data:
        logging.error("Unknown validation type '%s' for %s - rejecting input", validation_type, context)
        return False

    pattern_or_list, max_len = pattern_data

    # List-based validation (for actions)
    if isinstance(pattern_or_list, list):
        return value in pattern_or_list

    # Regex-based validation (for k8s resources, usernames)
    if max_len and len(value) > max_len:
        return False
    return bool(re.match(pattern_or_list, value))


def validate_inputs(validations: dict, context: str) -> bool:
    """Validate multiple inputs at once. Returns True if all valid."""
    return all(validate_input(val, vtype, context) for vtype, val in validations.items())


def _make_ssl_request(method: str, url: str, auth: tuple, timeout: int,
                      config_mgr: 'ConfigManager', session: requests.Session = None, **kwargs):
    """Make HTTP request with SSL configuration from config manager."""
    _ALLOWED_HTTP_METHODS = frozenset({'get', 'post', 'patch', 'put', 'delete'})
    if method not in _ALLOWED_HTTP_METHODS:
        raise ValueError(f"Unsupported HTTP method: {method}")

    ssl_config = config_mgr.get_requests_ssl_config()
    request_kwargs = {'auth': auth, 'timeout': timeout, **kwargs}

    if isinstance(ssl_config, tuple):
        request_kwargs['cert'] = ssl_config
    else:
        request_kwargs['verify'] = ssl_config

    requester = session or requests
    return getattr(requester, method)(url, **request_kwargs)


def _wait_for_power_off(check_func: Callable[[], Optional[str]], timeout: int,
                       host_identifier: str, expected_state: str, agent_type: str) -> bool:
    """Wait for host to power off by polling power state."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        time.sleep(1)
        power_state = check_func()
        if power_state == expected_state:
            logging.info("%s power off successful for %s", agent_type, host_identifier)
            return True
    logging.error('Power off of %s timed out', host_identifier)
    return False


def _build_redfish_url(fencing_data: dict) -> Optional[str]:
    """Build and validate Redfish URL from fencing data."""
    ip = fencing_data["ipaddr"]
    port = fencing_data.get("ipport", "443")
    uuid = fencing_data.get("uuid", "System.Embedded.1")
    tls = fencing_data.get("tls", "false").lower() == "true"
    protocol = "https" if tls else "http"
    url = f'{protocol}://{ip}:{port}/redfish/v1/Systems/{uuid}'

    if not validate_input(url, 'url', "Redfish fencing"):
        logging.error("Redfish fencing failed: invalid URL")
        return None
    return url


@contextmanager
def track_host_processing(service: 'InstanceHAService', hostname: str):
    """Context manager for tracking host processing state with automatic cleanup."""
    HOSTS_PROCESSING.inc()
    try:
        yield
    finally:
        HOSTS_PROCESSING.dec()
        with service.processing_lock:
            service.hosts_processing.pop(hostname, None)
            logging.debug('Cleaned up processing tracking for %s', hostname)


def _extract_hostname(host: str) -> str:
    """Extract short hostname from FQDN or hostname."""
    return host.split('.', 1)[0]


def _sanitize_url(url: str) -> str:
    """Remove credentials from URL if present (e.g., http://user:pass@host -> http://***@host)."""
    # Remove user:password from URLs to prevent credential leaks in logs
    return re.sub(r'://([^/:]+):([^@]+)@', r'://***@', url)


class OpenStackClient(Protocol):
    """Protocol defining the interface for OpenStack clients."""

    def services(self): ...
    def flavors(self): ...
    def aggregates(self): ...
    def servers(self): ...


class CloudConnectionProvider(ABC):
    """Abstract interface for cloud connection management."""

    @abstractmethod
    def get_connection(self) -> Optional[OpenStackClient]:
        """Get a connection to the cloud provider."""
        pass

    @abstractmethod
    def create_connection(self) -> Optional[OpenStackClient]:
        """Create a new connection to the cloud provider."""
        pass


AC_CREDENTIALS_PATH = "/secrets/ac-credentials"


def _load_ac_credentials(auth_url: str, region_name: str) -> Optional[ACLoginCredentials]:
    """Load Application Credential from the mounted secret directory."""
    ac_id_path = os.path.join(AC_CREDENTIALS_PATH, "AC_ID")
    ac_secret_path = os.path.join(AC_CREDENTIALS_PATH, "AC_SECRET")
    try:
        with open(ac_id_path) as f:
            ac_id = f.read().strip()
        with open(ac_secret_path) as f:
            ac_secret = f.read().strip()
        if not ac_id or not ac_secret:
            logging.error("AC credentials files are empty")
            return None
        return ACLoginCredentials(
            auth_url=auth_url,
            application_credential_id=ac_id,
            application_credential_secret=ac_secret,
            region_name=region_name,
        )
    except FileNotFoundError:
        logging.error("AC credentials not found at %s", AC_CREDENTIALS_PATH)
        return None


class ConfigManager:
    """Secure configuration manager for InstanceHA with type checking and validation."""

    def __init__(self, config_path: Optional[str] = None):
        self.config_path = config_path or os.getenv('INSTANCEHA_CONFIG_PATH', '/var/lib/instanceha/config.yaml')
        self.clouds_path = os.getenv('CLOUDS_CONFIG_PATH', '/home/cloud-admin/.config/openstack/clouds.yaml')
        self.secure_path = os.getenv('SECURE_CONFIG_PATH', '/home/cloud-admin/.config/openstack/secure.yaml')
        self.fencing_path = os.getenv('FENCING_CONFIG_PATH', '/secrets/fencing.yaml')

        # Load configurations
        self.config = self._load_config()
        self.clouds = self._load_clouds_config()
        self.secure = self._load_secure_config()
        self.fencing = self._load_fencing_config()

        # Validate critical configuration
        self._validate_config()

    def _load_yaml_file(self, path: str, name: str) -> Dict[str, Any]:
        """Load and parse a YAML file with error handling."""
        try:
            if not os.path.exists(path):
                logging.warning("%s file not found at %s, using defaults", name, path)
                return {}

            with open(path, 'r') as stream:
                data = yaml.load(stream, Loader=SafeLoader)
                if not isinstance(data, dict):
                    raise ConfigurationError(f"Invalid {name} format in {path}")
                return data

        except yaml.YAMLError as e:
            logging.error("Failed to parse %s file %s: %s", name, path, e)
            raise ConfigurationError(f"{name} parsing failed: {e}")
        except (IOError, OSError) as e:
            logging.error("Failed to read %s file %s: %s", name, path, e)
            raise ConfigurationError(f"{name} file access failed: {e}")

    def _load_config(self) -> Dict[str, Any]:
        data = self._load_yaml_file(self.config_path, "configuration")
        config = data.get("config", {})
        # Allow the INSTANCEHA_DISABLED env var (set from the CR spec) to override
        # the config file value. The env var is always set (either "True" or
        # "False"), so we only override when it is explicitly "True".
        if os.getenv('INSTANCEHA_DISABLED') == 'True':
            config['DISABLED'] = True
        return config

    def _load_keyed_yaml(self, path: str, name: str, key: str) -> Dict[str, Any]:
        """Load a YAML file and return the value under a required top-level key."""
        data = self._load_yaml_file(path, name)
        if not data:
            return {}
        if key not in data:
            raise ConfigurationError(f"Missing '{key}' key in {name} at {path}")
        return data[key]

    def _load_clouds_config(self) -> Dict[str, Any]:
        return self._load_keyed_yaml(self.clouds_path, "clouds configuration", "clouds")

    def _load_secure_config(self) -> Dict[str, Any]:
        return self._load_keyed_yaml(self.secure_path, "secure configuration", "clouds")

    def _load_fencing_config(self) -> Dict[str, Any]:
        return self._load_keyed_yaml(self.fencing_path, "fencing configuration", "FencingConfig")

    def _validate_config(self) -> None:
        """Validate critical configuration values using the configuration map."""
        # Auto-validate all config values with constraints
        validation_keys = ['DELTA', 'POLL', 'THRESHOLD', 'WORKERS', 'LOGLEVEL']
        for key in validation_keys:
            try:
                self.get_config_value(key)  # This will validate against _config_map constraints
            except ValueError as e:
                raise ConfigurationError(str(e))

    def get_str(self, key: str, default: str = '') -> str:
        """Get a string configuration value with validation."""
        value = self.config.get(key, default)
        if not isinstance(value, str):
            logging.warning("Configuration key %s should be string, got %s, using default: %s",
                          key, type(value).__name__, default)
            return default
        return value

    def get_int(self, key: str, default: int = 0, min_val: Optional[int] = None, max_val: Optional[int] = None) -> int:
        """Get an integer configuration value with validation."""
        value = self.config.get(key, default)

        try:
            int_value = int(value)
        except (ValueError, TypeError):
            logging.warning("Configuration %s should be integer, got %s, using default: %s", key, type(value).__name__, default)
            return default

        # Clamp value to min/max bounds
        original = int_value
        if min_val is not None:
            int_value = max(min_val, int_value)
        if max_val is not None:
            int_value = min(max_val, int_value)
        if int_value != original:
            logging.warning("Configuration %s value %d out of range [%s, %s], clamped to %d",
                            key, original, min_val, max_val, int_value)
        return int_value

    def get_bool(self, key: str, default: bool = False) -> bool:
        """Get a boolean configuration value with validation."""
        value = self.config.get(key, default)

        if isinstance(value, bool):
            return value
        if isinstance(value, str):
            return value.lower() in ('true', '1', 'yes', 'on', 'enabled')

        logging.warning("Configuration %s should be boolean, got %s, using default: %s", key, type(value).__name__, default)
        return default

    def get_list(self, key: str, default: Optional[List] = None) -> List:
        """Get a list configuration value with validation."""
        if default is None:
            default = []
        value = self.config.get(key, default)
        if isinstance(value, list):
            return value
        if isinstance(value, str):
            return [v.strip() for v in value.split(',') if v.strip()]
        logging.warning("Configuration key %s should be list, got %s, using default: %s",
                       key, type(value).__name__, default)
        return default

    # Configuration mapping with defaults and validation
    _config_map: Dict[str, ConfigItem] = {
        'EVACUABLE_TAG': ConfigItem('str', 'evacuable'),
        'DELTA': ConfigItem('int', 30, 10, 300),
        'POLL': ConfigItem('int', 45, 15, 600),
        'THRESHOLD': ConfigItem('int', 50, 0, 100),
        'WORKERS': ConfigItem('int', 4, 1, 50),
        'DELAY': ConfigItem('int', 0, 0, 300),
        'LOGLEVEL': ConfigItem('str', 'INFO'),
        'SMART_EVACUATION': ConfigItem('bool', False),
        'RESERVED_HOSTS': ConfigItem('bool', False),
        'FORCE_RESERVED_HOST_EVACUATION': ConfigItem('bool', False),
        'TAGGED_IMAGES': ConfigItem('bool', True),
        'TAGGED_FLAVORS': ConfigItem('bool', True),
        'TAGGED_AGGREGATES': ConfigItem('bool', True),
        'LEAVE_DISABLED': ConfigItem('bool', False),
        'FORCE_ENABLE': ConfigItem('bool', False),
        'CHECK_KDUMP': ConfigItem('bool', False),
        'KDUMP_TIMEOUT': ConfigItem('int', 30, 5, 300),
        'CHECK_HEARTBEAT': ConfigItem('bool', False),
        'HEARTBEAT_TIMEOUT': ConfigItem('int', 120, 30, 600),
        'DISABLED': ConfigItem('bool', False),
        'SSL_VERIFY': ConfigItem('bool', True),
        'FENCING_TIMEOUT': ConfigItem('int', 30, 5, 120),
        'HASH_INTERVAL': ConfigItem('int', 60, 30, 300),
        'ORCHESTRATED_RESTART': ConfigItem('bool', False),
        'SKIP_SERVERS_WITH_NAME': ConfigItem('list', []),
        'EVACUATION_RETRIES': ConfigItem('int', DEFAULT_EVACUATION_RETRIES, 1, 20),
        'HEARTBEAT_CLIFF_THRESHOLD': ConfigItem('int', 50, 10, 100),
        'K8S_API_CHECK_INTERVAL': ConfigItem('int', 15, 5, 120),
    }

    def get_config_value(self, key: str) -> Union[str, int, bool, List]:
        """Get configuration value with automatic type handling and validation."""
        if key not in self._config_map:
            raise ValueError(f"Unknown configuration key: {key}")

        config_item = self._config_map[key]

        if config_item.type == 'str':
            result = self.get_str(key, config_item.default)
            if key == 'LOGLEVEL':
                result = result.upper()
                valid_levels = ['DEBUG', 'INFO', 'WARNING', 'ERROR', 'CRITICAL']
                if result not in valid_levels:
                    raise ValueError(f"LOGLEVEL must be one of {valid_levels}, got {result}")
            return result
        elif config_item.type == 'int':
            return self.get_int(key, config_item.default, config_item.min_val, config_item.max_val)
        elif config_item.type == 'bool':
            return self.get_bool(key, config_item.default)
        elif config_item.type == 'list':
            return self.get_list(key, config_item.default)

        return config_item.default

    def get_cloud_name(self) -> str:
        return os.getenv('OS_CLOUD', 'overcloud')

    def _get_env_port(self, env_var: str, default: int) -> int:
        """Read a port number from an environment variable with validation."""
        try:
            port = int(os.getenv(env_var, default))
        except (ValueError, TypeError):
            logging.warning("Invalid %s, using default %d", env_var, default)
            return default
        if not 1 <= port <= 65535:
            logging.warning("%s %d out of range, using default %d", env_var, port, default)
            return default
        return port

    def get_udp_port(self) -> int:
        return self._get_env_port('UDP_PORT', DEFAULT_UDP_PORT)

    def get_heartbeat_port(self) -> int:
        return self._get_env_port('HEARTBEAT_PORT', DEFAULT_HEARTBEAT_PORT)

    @property
    def ssl_ca_bundle(self) -> Optional[str]:
        return self._get_ssl_path('SSL_CA_BUNDLE')

    @property
    def ssl_cert_path(self) -> Optional[str]:
        return self._get_ssl_path('SSL_CERT_PATH')

    @property
    def ssl_key_path(self) -> Optional[str]:
        return self._get_ssl_path('SSL_KEY_PATH')

    def _get_ssl_path(self, key: str) -> Optional[str]:
        """Get SSL file path if it exists."""
        path = self.get_str(key, '')
        return path if path and os.path.exists(path) else None

    def get_requests_ssl_config(self) -> Union[bool, str, tuple]:
        """Get SSL configuration for requests library."""
        if not self.get_config_value('SSL_VERIFY'):
            logging.warning("SSL verification is DISABLED - this is insecure for production use")
            return False

        if self.ssl_cert_path and self.ssl_key_path:
            return (self.ssl_cert_path, self.ssl_key_path)
        if self.ssl_ca_bundle:
            return self.ssl_ca_bundle
        return True



class InstanceHAService(CloudConnectionProvider):
    """Main service class that encapsulates all InstanceHA functionality."""

    def __init__(self, config_manager: ConfigManager, cloud_client: Optional[OpenStackClient] = None):
        self.config = config_manager
        self.cloud_client = cloud_client

        # Health monitoring
        self.current_hash = ""
        self.hash_update_successful = False
        self._last_hash_time = 0
        self._previous_hash = ""
        self.ready = False
        self.k8s_api_reachable = False

        # Caching
        self._evacuable_flavors_cache = None
        self._evacuable_images_cache = None
        self._cache_timestamp = 0
        self._cache_lock = threading.Lock()
        self.evacuable_tag = self.config.get_config_value('EVACUABLE_TAG')

        # Threading
        self.health_check_thread = None
        self.udp_ip = ''
        self.shutdown_event = threading.Event()

        # Kdump state (protected by kdump_lock — UDP listener writes concurrently)
        self.kdump_lock = threading.Lock()
        self.kdump_hosts_timestamp = defaultdict(float)
        self.kdump_hosts_checking = defaultdict(float)
        self.kdump_listener_stop_event = threading.Event()
        self.kdump_fenced_hosts = set()

        # Heartbeat state (protected by heartbeat_lock — UDP listener writes concurrently)
        self.heartbeat_lock = threading.Lock()
        self.heartbeat_hosts_timestamp = defaultdict(float)
        self.heartbeat_listener_stop_event = threading.Event()
        self.heartbeat_listener_start_time = 0.0
        self.heartbeat_previous_active_count = 0
        self.heartbeat_hmac_keys = self._load_heartbeat_hmac_keys()
        if self.heartbeat_hmac_keys:
            logging.info("Heartbeat HMAC authentication enabled with %d key(s)", len(self.heartbeat_hmac_keys))

        # Host processing tracking
        self.hosts_processing = defaultdict(float)
        self.processing_lock = threading.Lock()
        self.reserved_hosts_lock = threading.Lock()

        logging.info("InstanceHA service initialized successfully")

    @staticmethod
    def _load_heartbeat_hmac_keys():
        """Load HMAC keys for heartbeat authentication from mounted secrets."""
        keys = []
        for env_var in ('HEARTBEAT_HMAC_KEY_PATH', 'HEARTBEAT_HMAC_KEY_PREVIOUS_PATH'):
            path = os.getenv(env_var, '')
            if not path:
                continue
            try:
                with open(path, 'r') as f:
                    hex_key = f.read().strip()
                if hex_key:
                    keys.append(bytes.fromhex(hex_key))
            except (IOError, OSError) as e:
                logging.debug("Could not read HMAC key from %s: %s", path, e)
            except ValueError as e:
                logging.warning("Invalid hex in HMAC key file %s: %s", path, e)
        return keys


    def update_health_hash(self, hash_interval: Optional[int] = None) -> None:
        """Update health monitoring hash for service status tracking."""
        if hash_interval is None:
            hash_interval = self.config.get_config_value('HASH_INTERVAL')

        current_timestamp = time.time()

        if current_timestamp - self._last_hash_time > hash_interval:
            self.current_hash = hashlib.sha256(str(current_timestamp).encode()).hexdigest()
            self.hash_update_successful = True
            self._previous_hash = self.current_hash
            self._last_hash_time = current_timestamp

    def get_connection(self) -> Optional[OpenStackClient]:
        """Get cloud client connection, using injected client or creating a new one."""
        if self.cloud_client:
            return self.cloud_client

        return self.create_connection()

    def create_connection(self) -> Optional[OpenStackClient]:
        """Create a new Nova connection using configuration."""
        try:
            cloud_name = self.config.get_cloud_name()
            auth = self.config.clouds[cloud_name]["auth"]
            region_name = self.config.clouds[cloud_name]["region_name"]

            if os.environ.get("AC_ENABLED") == "True":
                ac_creds = _load_ac_credentials(auth["auth_url"], region_name)
                if ac_creds:
                    return nova_login_ac(ac_creds, ca_bundle=self.config.ssl_ca_bundle)
                logging.warning("AC_ENABLED but credentials not found, falling back to password auth")

            password = self.config.secure[cloud_name]["auth"]["password"]
            credentials = NovaLoginCredentials(
                username=auth["username"],
                password=password,
                project_name=auth["project_name"],
                auth_url=auth["auth_url"],
                user_domain_name=auth["user_domain_name"],
                project_domain_name=auth["project_domain_name"],
                region_name=region_name
            )
            return nova_login(credentials, ca_bundle=self.config.ssl_ca_bundle)
        except Exception as e:
            _safe_log_exception("Failed to create Nova connection", e, include_traceback=True)
            raise NovaConnectionError("Nova connection failed")

    def _check_image_match(self, server, evac_images) -> bool:
        """Check if server image matches evacuable criteria."""
        server_image_id = self._get_server_image_id(server)
        return (evac_images and server_image_id and server_image_id in evac_images) or \
               self.is_server_image_evacuable(server)

    def _check_flavor_match(self, server, evac_flavors) -> bool:
        """Check if server flavor matches evacuable criteria."""
        if not evac_flavors:
            return False

        try:
            flavor_extra_specs = server.flavor.get('extra_specs', {})

            # Check for exact key match or substring match in composite keys
            matching_key = next((k for k in flavor_extra_specs
                               if k == self.evacuable_tag or self.evacuable_tag in k), None)

            return matching_key and str(flavor_extra_specs[matching_key]).lower() == 'true'
        except (AttributeError, KeyError, TypeError):
            logging.debug("Could not check flavor extra specs for server %s", server.id)
            return False

    def is_server_evacuable(self, server, evac_flavors=None, evac_images=None):
        """Check if a server is evacuable based on flavor and image tags."""
        evac_flavors = evac_flavors if evac_flavors is not None else self.get_evacuable_flavors()
        evac_images = evac_images if evac_images is not None else self.get_evacuable_images()

        images_enabled = self.config.get_config_value('TAGGED_IMAGES')
        flavors_enabled = self.config.get_config_value('TAGGED_FLAVORS')

        # When tagging is disabled, evacuate all servers (default behavior)
        if not (images_enabled or flavors_enabled):
            return True

        # Early return if no tagged resources
        if not ((images_enabled and evac_images) or (flavors_enabled and evac_flavors)):
            logging.info("No tagged resources found - evacuating all servers")
            return True

        # Check matches
        matches = [
            self._check_image_match(server, evac_images) if images_enabled else False,
            self._check_flavor_match(server, evac_flavors) if flavors_enabled else False
        ]

        if any(matches):
            return True

        # Provide feedback on why server is not evacuable
        criteria = [c for c, enabled in [
            ("evacuable images", images_enabled and evac_images),
            ("evacuable flavors", flavors_enabled and evac_flavors)
        ] if enabled]
        if criteria:
            logging.warning("Instance %s is not evacuable: not using any of the defined %s tagged with '%s'",
                           server.id, " or ".join(criteria), self.evacuable_tag)
        return False

    def _get_cached_evacuable(self, cache_attr: str, config_key: str,
                              fetch_func: Callable, connection=None) -> List:
        """Generic cache-aside pattern: check config → check cache → fetch → store."""
        if not self.config.get_config_value(config_key):
            return []
        if connection is None:
            connection = self.get_connection()
        if connection is None:
            logging.error("No Nova connection available for %s caching", config_key)
            return []
        # Read check under lock, API call outside lock, write under lock
        with self._cache_lock:
            cached = getattr(self, cache_attr)
            if cached is not None:
                return cached
        try:
            cache_data = fetch_func(connection)
        except Exception as e:
            logging.error("Failed to get evacuable resources (%s): %s", config_key, e)
            logging.debug('Exception traceback:', exc_info=True)
            cache_data = []
        with self._cache_lock:
            setattr(self, cache_attr, cache_data)
        return cache_data

    def _fetch_evacuable_flavors(self, connection) -> List:
        """Fetch evacuable flavor IDs from Nova."""
        flavors = connection.flavors.list(is_public=None)
        cache_data = []
        for flavor in flavors:
            try:
                if self._is_flavor_evacuable(flavor, self.evacuable_tag):
                    cache_data.append(flavor.id)
                    logging.debug("Added flavor %s to evacuable cache", flavor.id)
            except Exception as e:
                logging.debug("Could not check keys for flavor %s: %s", flavor.id, e)
        logging.debug("Cached %d evacuable flavors from %d total", len(cache_data), len(flavors))
        return cache_data

    def _fetch_evacuable_images(self, connection) -> List:
        """Fetch evacuable image IDs, trying multiple retrieval strategies."""
        retrieval_strategies = [
            ("Nova glance client", lambda: connection.glance.list() if hasattr(connection, 'glance') else None),
            ("Nova images interface", lambda: connection.images.list() if hasattr(connection, 'images') else None),
        ]
        # Try separate Glance client as last resort
        def _try_separate_glance():
            from glanceclient import client as glance_client
            if hasattr(connection, 'api') and hasattr(connection.api, 'session'):
                return list(glance_client.Client('2', session=connection.api.session,
                            region_name=connection.region_name).images.list())
            return None
        retrieval_strategies.append(("Separate Glance client", _try_separate_glance))

        images = []
        for strategy_name, strategy_func in retrieval_strategies:
            try:
                result = strategy_func()
                if result:
                    logging.debug("Retrieved %d images via %s", len(result), strategy_name)
                    images = result
                    break
            except ImportError:
                logging.debug("%s not available for import", strategy_name)
            except Exception as e:
                logging.debug("%s access failed: %s", strategy_name, e)

        if not images:
            logging.warning("Could not retrieve images for caching. Image evacuation will use per-server checking.")
            return []

        cache_data = [img.id for img in images
                      if self._is_resource_evacuable(img, self.evacuable_tag, ['tags', 'metadata', 'properties'])]
        logging.debug("Cached %d evacuable images from %d total", len(cache_data), len(images))
        return cache_data

    def get_evacuable_flavors(self, connection: Optional[OpenStackClient] = None):
        """Get list of evacuable flavor IDs with caching."""
        return self._get_cached_evacuable('_evacuable_flavors_cache', 'TAGGED_FLAVORS',
                                          self._fetch_evacuable_flavors, connection)

    def get_evacuable_images(self, connection: Optional[OpenStackClient] = None):
        """Get list of evacuable image IDs with caching."""
        return self._get_cached_evacuable('_evacuable_images_cache', 'TAGGED_IMAGES',
                                          self._fetch_evacuable_images, connection)

    def _get_server_image_id(self, server):
        """Extract image ID from server object, handling different data formats."""
        try:
            image = getattr(server, 'image', None)
            if not image:
                return None
            if isinstance(image, dict):
                return image.get('id')
            if isinstance(image, str):
                return image
            return getattr(image, 'id', None)
        except (AttributeError, TypeError):
            return None

    def _check_evacuable_tag(self, data, evacuable_tag):
        """Generic method to check for evacuable tag in different data structures."""
        try:
            if isinstance(data, dict):
                # Dictionary - check for exact key match or composite keys
                if evacuable_tag in data:
                    return str(data[evacuable_tag]).lower() == 'true'
                return any(evacuable_tag in key and str(data[key]).lower() == 'true' for key in data)
            elif hasattr(data, '__iter__') and not isinstance(data, str):
                # List/tags - check for exact match or substring match
                return evacuable_tag in data or any(
                    isinstance(item, str) and evacuable_tag in item for item in data)
            return False
        except (AttributeError, TypeError):
            return False

    def _is_resource_evacuable(self, resource, evacuable_tag, attribute_checks):
        """Check if a resource (image, flavor, aggregate) is evacuable by checking attribute_checks for the tag."""
        for attr_name in attribute_checks:
            if hasattr(resource, attr_name):
                attr_value = getattr(resource, attr_name, {})
                if self._check_evacuable_tag(attr_value, evacuable_tag):
                    return True
        return False

    def _is_flavor_evacuable(self, flavor, evacuable_tag):
        """Check if a flavor is evacuable based on its extra specs."""
        try:
            return self._check_evacuable_tag(flavor.get_keys(), evacuable_tag)
        except (AttributeError, KeyError, TypeError):
            return False

    def is_server_image_evacuable(self, server, connection: Optional[OpenStackClient] = None):
        """Check if a server's image is tagged as evacuable (fallback when pre-caching fails)."""
        if not self.config.get_config_value('TAGGED_IMAGES'):
            return False

        if connection is None:
            connection = self.get_connection()

        if connection is None:
            logging.debug("No Nova connection available for image evacuability check")
            return False

        try:
            server_image_id = self._get_server_image_id(server)
            if not server_image_id:
                logging.debug("Server %s has no image ID", server.id)
                return False

            # Try multiple methods to get image details and check evacuability
            for get_method, check_attrs in [
                (lambda: connection.glance.get(server_image_id), ['tags']),
                (lambda: connection.images.get(server_image_id), ['metadata', 'properties'])
            ]:
                try:
                    image = get_method()
                    if self._is_resource_evacuable(image, self.evacuable_tag, check_attrs):
                        return True
                except (AttributeError, TypeError, KeyError) as e:
                    logging.debug("Error checking image attributes: %s", e)
                    continue

            logging.debug("Could not determine evacuability of image %s for server %s", server_image_id, server.id)
            return False

        except Exception as e:
            logging.debug("Error checking image evacuability for server %s: %s", server.id, e)
            return False

    def is_aggregate_evacuable(self, connection: OpenStackClient, host: str, aggregates=None) -> bool:
        """Check if a host is part of an aggregate tagged as evacuable."""
        try:
            if aggregates is None:
                aggregates = connection.aggregates.list()

            # Use unified resource checking logic
            return any(host in agg.hosts for agg in aggregates
                      if self._is_resource_evacuable(agg, self.evacuable_tag, ['metadata']))
        except Exception as e:
            logging.error("Failed to check aggregate evacuability for %s: %s", host, e)
            logging.debug('Exception traceback:', exc_info=True)
            return False

    def get_hosts_with_servers_cached(self, connection, services):
        """Cache server lists for all hosts to avoid repeated API calls."""
        host_servers = {}

        for service in services:
            if service.host not in host_servers:
                try:
                    servers = connection.servers.list(search_opts={
                        'host': service.host,
                        'all_tenants': 1
                    })
                    host_servers[service.host] = servers
                    if servers:
                        server_info = [(s.id, s.name, s.status) for s in servers]
                        logging.debug("Found %d servers on host %s: %s", len(servers), service.host, server_info)
                    else:
                        logging.info("No servers found on host %s", service.host)
                except Exception as e:
                    logging.warning("Failed to get servers for host %s: %s", service.host, e)
                    logging.debug('Exception traceback:', exc_info=True)
                    # Don't cache empty — treat query failure as "might have servers"
                    # so the host is not incorrectly skipped during evacuation
                    host_servers[service.host] = None

        return host_servers

    def filter_hosts_with_servers(self, services, host_servers_cache):
        """Filter out hosts that have no running servers."""
        return [
            service for service in services
            if host_servers_cache.get(service.host) is None
            or host_servers_cache.get(service.host)
        ]

    def filter_hosts_with_evacuable_servers(self, services, host_servers_cache, flavors=None, images=None):
        """Filter hosts that have evacuable servers."""
        if flavors is None:
            flavors = self.get_evacuable_flavors()
        if images is None:
            images = self.get_evacuable_images()

        services_with_evacuable = []

        for service in services:
            servers = host_servers_cache.get(service.host)

            if servers is None:
                # Query failed — assume host has evacuable servers (fail safe)
                services_with_evacuable.append(service)
                logging.warning("Server query failed for %s, assuming evacuable", service.host)
                continue

            # Check if any server on this host is evacuable
            has_evacuable = any(
                self.is_server_evacuable(server, flavors, images)
                for server in servers
            )

            if has_evacuable:
                services_with_evacuable.append(service)
                logging.debug("Host %s has evacuable servers", service.host)

        return services_with_evacuable

    def start_health_check_server(self):
        """Start the health check server in a separate thread."""
        if self.health_check_thread is None or not self.health_check_thread.is_alive():
            self.health_check_thread = threading.Thread(target=self._run_health_check_server)
            self.health_check_thread.daemon = True
            self.health_check_thread.start()
            logging.info("Health check server started")

    def start_k8s_health_check(self):
        """Start a background thread that monitors K8s API connectivity."""
        thread = threading.Thread(target=self._run_k8s_health_check)
        thread.daemon = True
        thread.start()
        logging.info("K8s API health check thread started (interval=%ds)",
                     self.config.get_config_value('K8S_API_CHECK_INTERVAL'))

    def _run_k8s_health_check(self):
        """Periodically check K8s API reachability and update readiness."""
        consecutive_failures = 0
        max_failures = 3
        interval = self.config.get_config_value('K8S_API_CHECK_INTERVAL')
        while not self.shutdown_event.is_set():
            try:
                interval = self.config.get_config_value('K8S_API_CHECK_INTERVAL')
                if _check_k8s_api_reachable():
                    if consecutive_failures > 0:
                        logging.info("K8s API connectivity restored after %d failures",
                                     consecutive_failures)
                    consecutive_failures = 0
                    self.k8s_api_reachable = True
                    K8S_API_REACHABLE.set(1)
                else:
                    consecutive_failures += 1
                    if consecutive_failures >= max_failures:
                        if self.k8s_api_reachable:
                            logging.warning(
                                "K8s API unreachable for %d consecutive checks — "
                                "possible network partition, marking not ready",
                                consecutive_failures)
                            self.k8s_api_reachable = False
                            self.ready = False
                            K8S_API_REACHABLE.set(0)
            except Exception as e:
                logging.error("K8s health check error: %s", e, exc_info=True)
                consecutive_failures += 1
            self.shutdown_event.wait(interval)

    def _run_health_check_server(self):
        """Simple health check server implementation."""
        from http.server import HTTPServer, BaseHTTPRequestHandler

        # Capture self reference for use in handler
        service_instance = self

        class HealthHandler(BaseHTTPRequestHandler):
            timeout = 5

            def log_message(self, format, *args):
                logging.debug("Health check: %s", format % args)

            def _respond(self, code, content_type, body):
                self.send_response(code)
                self.send_header("Content-type", content_type)
                self.end_headers()
                self.wfile.write(body if isinstance(body, bytes) else body.encode('utf-8'))

            def do_GET(self):
                if self.path == '/healthz':
                    if service_instance.ready:
                        self._respond(200, "text/plain", b"ok")
                    else:
                        self._respond(503, "text/plain", b"not ready")
                    return

                if self.path == '/metrics':
                    self._respond(200, CONTENT_TYPE_LATEST, generate_latest())
                    return

                if service_instance.hash_update_successful:
                    self._respond(200, "text/plain", service_instance.current_hash)
                else:
                    self._respond(500, "text/plain", b"Error: Hash not updated properly.")

        try:
            server = HTTPServer(('', HEALTH_CHECK_PORT), HealthHandler)
            tls_cert = os.getenv('METRICS_TLS_CERT')
            tls_key = os.getenv('METRICS_TLS_KEY')
            if tls_cert and tls_key:
                import ssl
                ssl_ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
                tls_versions = {'1.2': ssl.TLSVersion.TLSv1_2, '1.3': ssl.TLSVersion.TLSv1_3}
                min_ver = os.getenv('METRICS_TLS_MIN_VERSION', '1.2')
                ssl_ctx.minimum_version = tls_versions.get(min_ver, ssl.TLSVersion.TLSv1_2)
                # FIPS: on RHEL FIPS mode, OpenSSL's FIPS provider filters this
                # string to only FIPS-approved ciphers (AES-GCM/CCM + ECDHE/DHE).
                ciphers = os.getenv('METRICS_TLS_CIPHERS', 'HIGH:!aNULL:!MD5:!RC4:!3DES:!kRSA')
                ssl_ctx.set_ciphers(ciphers)
                ssl_ctx.load_cert_chain(tls_cert, tls_key)
                server.socket = ssl_ctx.wrap_socket(server.socket, server_side=True)
                logging.info("Metrics endpoint serving over HTTPS on port %d", HEALTH_CHECK_PORT)
            server.serve_forever()
        except OSError as e:
            logging.error('Health check server failed to bind to port %d: %s',
                         HEALTH_CHECK_PORT, e)
        except Exception as e:
            logging.error('Health check server failed: %s', e)

    def _cleanup_dict_by_condition(self, dictionary: dict, condition_func: Callable[[Any, Any], bool],
                                   log_message: Optional[str] = None) -> int:
        """Remove dictionary entries matching condition function."""
        to_remove = [k for k, v in dictionary.items() if condition_func(k, v)]
        for k in to_remove:
            del dictionary[k]
            if log_message:
                logging.debug(log_message.format(k))
        return len(to_remove)

    def refresh_evacuable_cache(self, connection=None, force=False, cache_timeout=CACHE_TIMEOUT_SECONDS):
        """Refresh evacuable flavors and images cache with intelligent timing."""
        current_time = time.time()

        with self._cache_lock:
            cache_age = current_time - self._cache_timestamp

            # Refresh cache when cache_timeout exceeded or when forced
            if force or cache_age > cache_timeout or not self._evacuable_flavors_cache:
                logging.debug("Refreshing evacuable cache (age: %.1f seconds)", cache_age)
                self._evacuable_flavors_cache = None
                self._evacuable_images_cache = None
                self._cache_timestamp = current_time
            else:
                # Cache is still fresh, no need to refresh
                logging.debug("Cache is fresh (age: %.1f seconds), skipping refresh", cache_age)
                return False

        # Force re-cache (done outside lock to avoid blocking)
        evac_flavors = self.get_evacuable_flavors(connection)
        evac_images = self.get_evacuable_images(connection)

        logging.debug("Cache refreshed: %d flavors, %d images", len(evac_flavors), len(evac_images))
        return True


class UDPSocketManager:
    """Context manager for UDP socket handling with proper resource cleanup."""

    def __init__(self, udp_ip, udp_port, label='UDP'):
        self.udp_ip = udp_ip
        self.udp_port = udp_port
        self.label = label
        self.socket = None

    def __enter__(self):
        info = socket.getaddrinfo(self.udp_ip or '::', self.udp_port,
                                  type=socket.SOCK_DGRAM)[0]
        self.socket = socket.socket(info[0], info[1])
        if info[0] == socket.AF_INET6:
            self.socket.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 0)
        self.socket.settimeout(1.0)
        self.socket.bind(info[4])
        host = '[%s]' % info[4][0] if info[0] == socket.AF_INET6 else info[4][0]
        logging.info('%s listener started on %s:%s', self.label, host, info[4][1])
        return self.socket

    def __exit__(self, exc_type, exc_val, exc_tb):
        if self.socket:
            try:
                self.socket.close()
            except (OSError, AttributeError):
                pass
        logging.info('%s listener stopped', self.label)

def _resolve_hostname_dns(data, address, label):
    """Resolve hostname via reverse DNS lookup (kdump listener)."""
    try:
        return _extract_hostname(socket.gethostbyaddr(address[0])[0])
    except socket.herror:
        logging.warning(
            '%s packet from %s but reverse DNS lookup failed. '
            'Ensure PTR records exist for compute node management IPs. '
            'This host will fall back to timeout-based detection.',
            label, address[0])
        return None


def _validate_raw_hostname(raw_hostname, address, label):
    """Validate a raw hostname string against length and pattern rules."""
    if not raw_hostname or len(raw_hostname) > USERNAME_MAX_LENGTH:
        logging.warning('%s from %s has invalid hostname length', label, address[0])
        return None
    pattern, _ = VALIDATION_PATTERNS['hostname']
    if not re.match(pattern, raw_hostname):
        logging.warning('%s from %s has invalid hostname characters', label, address[0])
        return None
    return _extract_hostname(raw_hostname)


def _resolve_hostname_packet(data, address, label, hmac_keys=None):
    """Extract hostname from HMAC-authenticated heartbeat packet.

    FIPS: uses HMAC-SHA256 (FIPS 198-1 / 180-4), safe on RHEL FIPS mode.
    """
    if len(data) < HEARTBEAT_MIN_PACKET_SIZE:
        logging.warning('%s from %s too short (%d bytes)', label, address[0], len(data))
        return None

    magic_network = struct.unpack('!I', data[:4])[0]
    if magic_network != HEARTBEAT_MAGIC_NUMBER:
        return None

    if not hmac_keys:
        logging.warning('%s from %s but no HMAC keys loaded', label, address[0])
        return None

    timestamp = struct.unpack('!Q', data[4:12])[0]
    hostname_bytes = data[12:-HEARTBEAT_HMAC_DIGEST_SIZE]
    received_hmac = data[-HEARTBEAT_HMAC_DIGEST_SIZE:]
    signed_data = data[:-HEARTBEAT_HMAC_DIGEST_SIZE]

    verified = False
    for key in hmac_keys:
        expected = hmac_mod.new(key, signed_data, 'sha256').digest()
        if hmac_mod.compare_digest(received_hmac, expected):
            verified = True
            break
    if not verified:
        logging.warning('%s from %s HMAC verification failed', label, address[0])
        HEARTBEAT_REJECTED_TOTAL.labels(reason='hmac_failed').inc()
        return None

    delta = time.time() - timestamp
    if delta < -60 or delta > 120:
        logging.warning('%s from %s rejected: timestamp out of range (delta=%.0fs)', label, address[0], delta)
        HEARTBEAT_REJECTED_TOTAL.labels(reason='timestamp_invalid').inc()
        return None

    try:
        raw_hostname = hostname_bytes.decode('utf-8').strip('\x00').strip()
    except UnicodeDecodeError:
        logging.warning('%s from %s has invalid hostname encoding', label, address[0])
        return None
    return _validate_raw_hostname(raw_hostname, address, label)


def _udp_listener(service, port, label, magic_numbers, min_packet_size,
                  lock, timestamps, stop_event,
                  cleanup_threshold, cleanup_age_seconds,
                  resolve_hostname, log_level=logging.INFO, on_start=None):
    """Generic UDP listener for magic-number-based host detection messages."""
    if isinstance(magic_numbers, int):
        magic_numbers = {magic_numbers}
    else:
        magic_numbers = set(magic_numbers)
    udp_ip = service.udp_ip if service.udp_ip else '::'

    try:
        with UDPSocketManager(udp_ip, port, label=f'{label} UDP') as sock:
            if on_start:
                on_start()

            while not stop_event.is_set():
                try:
                    data, address = sock.recvfrom(4096)
                    # Normalize IPv4-mapped IPv6 addresses (::ffff:1.2.3.4 -> 1.2.3.4)
                    if address[0].startswith('::ffff:'):
                        address = (address[0][7:],) + address[1:]

                    if len(data) < min_packet_size:
                        continue

                    magic_native = struct.unpack('I', data[:4])[0]
                    magic_network = struct.unpack('!I', data[:4])[0]

                    if magic_native not in magic_numbers and magic_network not in magic_numbers:
                        continue

                    hostname = resolve_hostname(data, address, label)
                    if not hostname:
                        continue

                    try:
                        with lock:
                            timestamps[hostname] = time.time()
                            if len(timestamps) > cleanup_threshold:
                                cutoff = time.time() - cleanup_age_seconds
                                to_remove = [k for k, v in timestamps.items() if v < cutoff]
                                for k in to_remove:
                                    del timestamps[k]
                        logging.log(log_level, '%s message received from host: %s', label, hostname)
                    except Exception as e:
                        logging.warning('Failed to process %s message from %s: %s', label.lower(), address[0], e)

                except socket.timeout:
                    continue
                except OSError as e:
                    if not stop_event.is_set():
                        logging.debug('%s listener socket error: %s', label, e)
                    continue
    except Exception as e:
        logging.error('%s listener failed to start: %s', label, e)


def _kdump_udp_listener(service: 'InstanceHAService') -> None:
    """Background UDP listener for kdump messages."""
    _udp_listener(
        service,
        port=service.config.get_udp_port(),
        label='Kdump',
        magic_numbers=KDUMP_MAGIC_NUMBER,
        min_packet_size=8,
        lock=service.kdump_lock,
        timestamps=service.kdump_hosts_timestamp,
        stop_event=service.kdump_listener_stop_event,
        cleanup_threshold=KDUMP_CLEANUP_THRESHOLD,
        cleanup_age_seconds=KDUMP_CLEANUP_AGE_SECONDS,
        resolve_hostname=_resolve_hostname_dns,
    )


def _heartbeat_udp_listener(service: 'InstanceHAService') -> None:
    """Background UDP listener for host heartbeat messages."""
    def on_start():
        with service.heartbeat_lock:
            service.heartbeat_listener_start_time = time.time()

    def resolve_with_hmac(data, address, label):
        return _resolve_hostname_packet(data, address, label, hmac_keys=service.heartbeat_hmac_keys)

    _udp_listener(
        service,
        port=service.config.get_heartbeat_port(),
        label='Heartbeat',
        magic_numbers=HEARTBEAT_MAGIC_NUMBER,
        min_packet_size=HEARTBEAT_MIN_PACKET_SIZE,
        lock=service.heartbeat_lock,
        timestamps=service.heartbeat_hosts_timestamp,
        stop_event=service.heartbeat_listener_stop_event,
        cleanup_threshold=HEARTBEAT_CLEANUP_THRESHOLD,
        cleanup_age_seconds=HEARTBEAT_CLEANUP_AGE_SECONDS,
        resolve_hostname=resolve_with_hmac,
        log_level=logging.DEBUG,
        on_start=on_start,
    )


def _aggregate_ids(conn, service, aggregates=None) -> List[int]:
    """Get aggregate IDs for a service's host."""
    if aggregates is None:
        aggregates = conn.aggregates.list()
    return [ag.id for ag in aggregates if service.host in ag.hosts]


def _update_service_disable_reason(connection, host, service_id=None) -> bool:
    """Helper to update service disable reason for evacuation failures."""
    try:
        if service_id is None:
            # Fetch fresh service object
            services = connection.services.list(host=host, binary='nova-compute')
            service_obj = next((s for s in services if s.host == host and s.binary == 'nova-compute'), None)
            if not service_obj:
                logging.warning('Could not find service object for host %s', host)
                return False
            service_id = service_obj.id

        connection.services.disable_log_reason(service_id, _make_disable_reason(DISABLED_REASON_EVACUATION_FAILED))
        logging.debug('Updated disabled reason for host %s after evacuation failure', host)
        return True
    except Exception as e:
        logging.error('Failed to update disable_reason for host %s. Error: %s', host, e)
        logging.debug('Exception traceback:', exc_info=True)
        return False


def _should_skip_server(server, skip_names) -> bool:
    """Check if a server should be skipped based on glob pattern matching (fnmatch)."""
    if not skip_names or not hasattr(server, 'name') or not server.name:
        return False
    return any(fnmatch.fnmatch(server.name, pattern) for pattern in skip_names)


def _get_evacuable_servers(connection, host, service) -> List:
    """Get list of evacuable servers from a host."""
    images = service.get_evacuable_images(connection)
    flavors = service.get_evacuable_flavors(connection)

    servers = connection.servers.list(search_opts={'host': host, 'all_tenants': 1})
    servers = [s for s in servers if s.status in {'ACTIVE', 'ERROR', 'SHUTOFF'}]

    skip_names = service.config.get_config_value('SKIP_SERVERS_WITH_NAME')
    if skip_names:
        skipped = [s for s in servers if _should_skip_server(s, skip_names)]
        if skipped:
            logging.info("Skipping %d server(s) matching name filter %s: %s",
                        len(skipped), skip_names,
                        ', '.join(s.id for s in skipped))
        servers = [s for s in servers if not _should_skip_server(s, skip_names)]

    if flavors or images:
        logging.debug("Filtering images and flavors: %s %s", repr(flavors), repr(images))
        evacuables = [s for s in servers if service.is_server_evacuable(s, flavors, images)]
        logging.debug("Evacuating %s", repr(evacuables))
    else:
        logging.debug("Evacuating all images and flavors")
        evacuables = servers

    return evacuables


def _get_evacuation_metadata(server) -> Tuple[int, Optional[str]]:
    """Extract (priority, group) from server metadata. Priority 1-1000, default 500."""
    raw = getattr(server, 'metadata', None)
    metadata = raw if isinstance(raw, dict) else {}
    group = metadata.get('instanceha:restart_group') or None

    try:
        priority = int(metadata.get('instanceha:restart_priority', 500))
        priority = max(1, min(1000, priority))
    except (ValueError, TypeError):
        priority = 500

    return priority, group



def _build_evacuation_groups(evacuables) -> List[List]:
    """Build priority-ordered evacuation groups from server metadata."""
    grouped = defaultdict(list)
    ungrouped = []
    group_priorities = defaultdict(int)

    has_metadata = False
    for server in evacuables:
        priority, group = _get_evacuation_metadata(server)
        if group is not None:
            grouped[group].append(server)
            group_priorities[group] = max(group_priorities[group], priority)
            has_metadata = True
        else:
            ungrouped.append((priority, server))
            if 'instanceha:restart_priority' in (getattr(server, 'metadata', None) or {}):
                has_metadata = True

    if not has_metadata and evacuables:
        logging.warning("ORCHESTRATED_RESTART is enabled but no servers have "
                       "instanceha:restart_priority or instanceha:restart_group metadata. "
                       "All servers will be evacuated with default priority 500.")

    phases = []
    for group_name, servers in grouped.items():
        phases.append((group_priorities[group_name], group_name, servers))

    for priority, server in ungrouped:
        phases.append((priority, None, [server]))

    phases.sort(key=lambda x: x[0], reverse=True)

    for priority, group_name, servers in phases:
        server_ids = [s.id for s in servers]
        if group_name:
            logging.info("Evacuation phase: group=%s priority=%d servers=%s",
                        group_name, priority, server_ids)
        else:
            logging.info("Evacuation phase: ungrouped priority=%d servers=%s",
                        priority, server_ids)

    return [servers for _, _, servers in phases]


def _run_concurrent(func, items, max_workers, item_id_func, log_prefix=""):
    """Run func for each item concurrently, return True if all succeeded."""
    all_succeeded = True
    with concurrent.futures.ThreadPoolExecutor(max_workers=max_workers) as executor:
        future_to_item = {executor.submit(func, item): item for item in items}
        for future in concurrent.futures.as_completed(future_to_item):
            item = future_to_item[future]
            item_id = item_id_func(item)
            try:
                if not future.result(timeout=FUTURE_RESULT_TIMEOUT_SECONDS):
                    logging.error('%s%s failed', log_prefix, item_id)
                    all_succeeded = False
                else:
                    logging.info('%s%s succeeded', log_prefix, item_id)
            except concurrent.futures.TimeoutError:
                logging.error('%s%s timed out', log_prefix, item_id)
                all_succeeded = False
            except Exception as exc:
                logging.error('%s%s raised exception: %s', log_prefix, item_id, exc)
                logging.debug('Exception traceback:', exc_info=True)
                all_succeeded = False
    return all_succeeded


def _concurrent_evacuate(connection, evacuables, service, host, service_id, target_host=None) -> bool:
    """Evacuate concurrently, optionally with priority-ordered phases."""
    orchestrated = service.config.get_config_value('ORCHESTRATED_RESTART') is True
    phases = _build_evacuation_groups(evacuables) if orchestrated else [evacuables]
    total_phases = len(phases)
    max_retries = service.config.get_config_value('EVACUATION_RETRIES')
    inner_workers = max(1, MAX_TOTAL_EVACUATION_THREADS // service.config.get_config_value('WORKERS'))

    logging.info("Concurrent evacuation: %d phase(s), %d total servers from %s (orchestrated=%s)",
                total_phases, sum(len(p) for p in phases), host, orchestrated)

    all_succeeded = True
    for phase_num, phase_servers in enumerate(phases, 1):
        if total_phases > 1:
            logging.info("Starting evacuation phase %d/%d (%d servers)",
                        phase_num, total_phases, len(phase_servers))

        phase_ok = _run_concurrent(
            lambda s: _server_evacuate_future(connection, s, target_host,
                                              max_retries=max_retries,
                                              shutdown_event=service.shutdown_event),
            phase_servers,
            inner_workers,
            lambda s: s.id,
            log_prefix=f"Phase {phase_num}: " if total_phases > 1 else "",
        )
        if not phase_ok:
            all_succeeded = False
        if total_phases > 1:
            logging.log(logging.INFO if phase_ok else logging.WARNING,
                        "Evacuation phase %d/%d completed %s", phase_num, total_phases,
                        "successfully" if phase_ok else "with failures")

    if not all_succeeded:
        _update_service_disable_reason(connection, host, service_id)

    return all_succeeded


def _traditional_evacuate(connection, evacuables, host, target_host=None) -> bool:
    """Fire-and-forget evacuation without migration tracking."""
    logging.debug("Using traditional evacuation approach%s",
                 f", targeting host {target_host}" if target_host else "")
    all_succeeded = True

    for server in evacuables:
        logging.debug("Processing %s", server)
        if hasattr(server, 'id'):
            response = _server_evacuate(connection, server.id, target_host=target_host, server_obj=server)
            if response.accepted:
                logging.debug("Evacuated %s from %s: %s", response.uuid, host, response.reason)
            else:
                logging.warning("Evacuation of %s on %s failed: %s", response.uuid, host, response.reason)
                all_succeeded = False
        else:
            logging.error("Could not evacuate instance: %s", server.to_dict())
            all_succeeded = False

    return all_succeeded

def _host_evacuate(connection, failed_service, service, target_host=None) -> bool:
    """Evacuate all instances from a failed host."""
    host = failed_service.host

    evacuables = _get_evacuable_servers(connection, host, service)
    if not evacuables:
        logging.info("Nothing to evacuate")
        return True

    time.sleep(service.config.get_config_value('DELAY'))

    if service.config.get_config_value('ORCHESTRATED_RESTART') is True or service.config.get_config_value('SMART_EVACUATION'):
        return _concurrent_evacuate(connection, evacuables, service, host, failed_service.id, target_host=target_host)
    else:
        return _traditional_evacuate(connection, evacuables, host, target_host=target_host)


def _server_evacuate(connection, server, target_host=None, server_obj=None) -> EvacuationResult:
    """Evacuate a single server instance, returning EvacuationResult."""
    from novaclient.exceptions import Conflict, NotFound, Forbidden, Unauthorized
    success = False
    error_message = ""
    resp_status_code = None

    if server_obj is not None:
        task_state = getattr(server_obj, 'OS-EXT-STS:task_state', None)
        if task_state is not None:
            logging.warning("Server %s has stuck task_state=%s, resetting to error",
                            server, task_state)
            try:
                connection.servers.reset_state(server, 'error')
            except Exception as e:
                logging.warning("Failed to reset task_state for %s: %s", server, e)

    try:
        if target_host:
            logging.info("Evacuating instance %s to target host %s", server, target_host)
            response, _ = connection.servers.evacuate(server=server, host=target_host)
        else:
            logging.info("Evacuating instance %s", server)
            response, _ = connection.servers.evacuate(server=server)

        if response is None:
            error_message = "No response received while evacuating instance"
        else:
            resp_status_code = response.status_code
            if response.status_code == 200:
                success = True
                if target_host:
                    error_message = response.reason or f"Evacuation to {target_host} initiated successfully"
                else:
                    error_message = response.reason or "Evacuation initiated successfully"
            else:
                error_message = response.reason or f"Evacuation failed with status {response.status_code}"
    except NotFound:
        resp_status_code = 404
        error_message = f"Instance {server} not found"
    except Conflict as e:
        resp_status_code = 409
        error_message = f"Conflict while evacuating instance {server}: {e}"
    except Forbidden:
        resp_status_code = 403
        error_message = f"Access denied while evacuating instance {server}"
    except Unauthorized:
        resp_status_code = 401
        error_message = f"Authentication failed while evacuating instance {server}"
    except Exception as e:
        resp_status_code = getattr(e, 'http_status', None)
        error_message = f"Error while evacuating instance {server}: {e}"

    return EvacuationResult(
        uuid=server,
        accepted=success,
        reason=error_message,
        status_code=resp_status_code,
    )


def _server_evacuation_status(connection, server) -> EvacuationStatus:
    """Check the status of a server evacuation by querying recent migrations."""
    if not connection or not server:
        return EvacuationStatus(completed=False, error=True)

    try:
        query_time = (datetime.now(timezone.utc) - timedelta(minutes=MIGRATION_QUERY_MINUTES)).isoformat()
        migrations = connection.migrations.list(
            instance_uuid=str(server),
            migration_type='evacuation',
            changes_since=query_time,
            limit=str(MIGRATION_QUERY_LIMIT)
        )

        if not migrations:
            return EvacuationStatus(completed=False, error=True)

        # Get status from most recent migration (sort by created_at descending)
        def _migration_sort_key(m):
            created = getattr(m, 'created_at', None)
            return created if isinstance(created, str) else ''
        migrations = sorted(migrations, key=_migration_sort_key, reverse=True)
        migration = migrations[0]
        status = getattr(migration, 'status', None) or getattr(migration, '_info', {}).get('status')

        return EvacuationStatus(
            completed=status in MIGRATION_STATUS_COMPLETED if status else False,
            error=status in MIGRATION_STATUS_ERROR if status else True
        )

    except Exception as e:
        logging.error("Failed to check evacuation status for %s: %s", server, e)
        return EvacuationStatus(completed=False, error=True)


def _monitor_evacuation(connection, server_id, response_uuid, start_time,
                        max_retries=DEFAULT_EVACUATION_RETRIES,
                        shutdown_event=None) -> bool:
    """Poll Nova migrations API until evacuation completes, errors out, or times out."""
    migration_error_count = 0
    api_error_count = 0

    while True:
        if shutdown_event and shutdown_event.is_set():
            logging.warning("Shutdown requested, aborting evacuation monitoring for %s", response_uuid)
            return False

        if time.monotonic() - start_time > MAX_EVACUATION_TIMEOUT_SECONDS:
            logging.error("Evacuation of %s timed out after %d seconds. Giving up.",
                         response_uuid, MAX_EVACUATION_TIMEOUT_SECONDS)
            return False

        try:
            status = _server_evacuation_status(connection, server_id)
            api_error_count = 0

            if status.completed:
                logging.info("Evacuation of %s completed successfully", response_uuid)
                return True

            if status.error:
                migration_error_count += 1
                if migration_error_count >= max_retries:
                    logging.error("Failed evacuating %s %d times. Giving up.",
                                 response_uuid, max_retries)
                    return False
                logging.warning("Evacuation of instance %s failed %d times. Re-issuing evacuation...",
                               response_uuid, migration_error_count)
                try:
                    connection.servers.reset_state(server_id, 'error')
                    retry_result = _server_evacuate(connection, server_id)
                    if retry_result.accepted:
                        response_uuid = retry_result.uuid
                        logging.info("Re-issued evacuation for %s as %s", server_id, response_uuid)
                    else:
                        logging.warning("Re-issue of evacuation for %s failed: %s", server_id, retry_result.reason)
                except Exception as retry_err:
                    logging.warning("Failed to re-issue evacuation for %s: %s", server_id, retry_err)
                _interruptible_sleep(shutdown_event, EVACUATION_RETRY_WAIT_SECONDS)
                continue

            logging.debug("Evacuation of %s still in progress", response_uuid)
            _interruptible_sleep(shutdown_event, EVACUATION_POLL_INTERVAL_SECONDS)

        except Exception as e:
            api_error_count += 1
            logging.error("Error checking evacuation status for %s: %s",
                         response_uuid, _sanitize_message(str(e)))
            logging.debug('Exception traceback:', exc_info=True)
            if api_error_count >= MAX_API_RETRIES:
                logging.error("Too many API errors checking evacuation status for %s. Giving up.",
                             response_uuid)
                return False
            logging.warning("Retrying evacuation status check for %s in %d seconds (API attempt %d/%d)...",
                           response_uuid, EVACUATION_RETRY_WAIT_SECONDS, api_error_count, MAX_API_RETRIES)
            _interruptible_sleep(shutdown_event, EVACUATION_RETRY_WAIT_SECONDS)


def _server_evacuate_future(connection, server, target_host=None,
                            max_retries=DEFAULT_EVACUATION_RETRIES,
                            shutdown_event=None) -> bool:
    """Evacuate a server and monitor until completion."""
    if not hasattr(server, 'id'):
        logging.warning("Could not evacuate instance - missing server ID: %s",
                       getattr(server, 'to_dict', lambda: str(server))())
        return False

    start_time = time.monotonic()

    logging.info("Processing evacuation for server %s%s", server.id,
                 f" to target host {target_host}" if target_host else "")

    original_status = getattr(server, 'status', None)
    source_host = getattr(server, 'OS-EXT-SRV-ATTR:host', 'unknown')

    try:
        connection.servers.lock(server.id, reason=LOCK_REASON_EVACUATION)
    except Exception:
        logging.debug("Could not lock server %s (may already be locked)", server.id)

    try:
        _emit_k8s_event(source_host, 'InstanceEvacuationStarted',
                        f'Evacuating instance {server.id}')
        INSTANCE_EVACUATION_TOTAL.labels(host=source_host, result='started').inc()

        response = _server_evacuate(connection, server.id, target_host=target_host, server_obj=server)

        if not response.accepted and response.status_code in (409, 500) and target_host:
            logging.warning("Evacuation of %s to %s failed with %s, retrying without target host",
                            server.id, target_host, response.status_code)
            response = _server_evacuate(connection, server.id, server_obj=server)

        if not response.accepted:
            logging.warning("Evacuation of %s on %s failed: %s",
                           response.uuid, server.id, _sanitize_message(str(response.reason)))
            _emit_k8s_event(source_host, 'InstanceEvacuationFailed',
                            f'Instance {server.id} evacuation failed: {_sanitize_message(str(response.reason))}',
                            event_type='Warning')
            INSTANCE_EVACUATION_TOTAL.labels(host=source_host, result='failed').inc()
            return False

        logging.debug("Starting evacuation of %s", response.uuid)
        time.sleep(INITIAL_EVACUATION_WAIT_SECONDS)

        result = _monitor_evacuation(connection, server.id, response.uuid, start_time,
                                     max_retries=max_retries,
                                     shutdown_event=shutdown_event)

        if result:
            _emit_k8s_event(source_host, 'InstanceEvacuationSucceeded',
                            f'Instance {server.id} evacuated successfully')
            INSTANCE_EVACUATION_TOTAL.labels(host=source_host, result='succeeded').inc()
            INSTANCE_EVACUATION_DURATION.labels(host=source_host).observe(time.monotonic() - start_time)

            if original_status in ('SHUTOFF', 'ERROR'):
                current = connection.servers.get(server.id)
                current_status = getattr(current, 'status', None)
                if current_status == original_status:
                    logging.info("Server %s already in %s state after evacuation", server.id, original_status)
                elif original_status == 'SHUTOFF':
                    try:
                        connection.servers.stop(server.id)
                        logging.info("Restored server %s to SHUTOFF state", server.id)
                    except Exception as e:
                        logging.warning("Failed to restore SHUTOFF state for %s: %s", server.id, e)
                else:
                    try:
                        connection.servers.reset_state(server.id, 'error')
                        logging.info("Restored server %s to ERROR state", server.id)
                    except Exception as e:
                        logging.warning("Failed to restore ERROR state for %s: %s", server.id, e)
        else:
            _emit_k8s_event(source_host, 'InstanceEvacuationFailed',
                            f'Instance {server.id} evacuation failed',
                            event_type='Warning')
            INSTANCE_EVACUATION_TOTAL.labels(host=source_host, result='failed').inc()

        return result

    except Exception as e:
        logging.error("Unexpected error during evacuation of server %s: %s",
                     server.id, _sanitize_message(str(e)))
        logging.debug('Exception traceback:', exc_info=True)
        _emit_k8s_event(source_host, 'InstanceEvacuationFailed',
                        f'Instance {server.id} evacuation error: {_sanitize_message(str(e))}',
                        event_type='Warning')
        INSTANCE_EVACUATION_TOTAL.labels(host=source_host, result='failed').inc()
        return False
    finally:
        try:
            connection.servers.unlock(server.id)
        except Exception:
            logging.debug("Could not unlock server %s (may have been deleted)", server.id)


def _nova_login(plugin_name: str, auth_kwargs: dict, region_name: str,
                ca_bundle: Optional[str] = None) -> OpenStackClient:
    """Create and return Nova client connection. Raises NovaConnectionError on failure."""
    from keystoneauth1 import loading
    from keystoneauth1 import session as ksc_session
    from keystoneauth1.exceptions.discovery import DiscoveryFailure
    from novaclient import client
    from novaclient.exceptions import Unauthorized
    try:
        loader = loading.get_plugin_loader(plugin_name)
        auth = loader.load_from_options(**auth_kwargs)
        verify = ca_bundle if ca_bundle else True
        session = ksc_session.Session(auth=auth, verify=verify, timeout=NOVA_API_TIMEOUT_SECONDS)
        nova = client.Client("2.73", session=session, region_name=region_name)
        nova.versions.get_current()
        logging.info("Nova login successful (%s)", plugin_name)
        return nova
    except (DiscoveryFailure, Unauthorized) as e:
        _safe_log_exception(f"Nova login failed: {type(e).__name__}", e)
        raise NovaConnectionError(f"Nova login failed: {type(e).__name__}") from e
    except Exception as e:
        _safe_log_exception("Nova login failed", e, include_traceback=True)
        raise NovaConnectionError("Nova login failed") from e


def nova_login(credentials: NovaLoginCredentials, ca_bundle: Optional[str] = None) -> OpenStackClient:
    """Create Nova client with password auth."""
    return _nova_login("password", {
        "auth_url": credentials.auth_url,
        "username": credentials.username,
        "password": credentials.password,
        "project_name": credentials.project_name,
        "user_domain_name": credentials.user_domain_name,
        "project_domain_name": credentials.project_domain_name,
    }, credentials.region_name, ca_bundle)


def nova_login_ac(credentials: ACLoginCredentials, ca_bundle: Optional[str] = None) -> OpenStackClient:
    """Create Nova client with Application Credential auth."""
    return _nova_login("v3applicationcredential", {
        "auth_url": credentials.auth_url,
        "application_credential_id": credentials.application_credential_id,
        "application_credential_secret": credentials.application_credential_secret,
    }, credentials.region_name, ca_bundle)


def _handle_nova_exception(operation: str, service_info: str, e: Exception, is_critical: bool = True) -> bool:
    """Handle Nova API exceptions with consistent logging."""
    from novaclient.exceptions import NotFound, Conflict
    nova_exception_messages = {
        NotFound: "Resource not found",
        Conflict: "Conflicting operation",
    }
    error_msg = nova_exception_messages.get(type(e))

    if error_msg:
        logging.error("Failed to %s for %s. %s: %s", operation, service_info, error_msg, type(e).__name__)
    else:
        _safe_log_exception(f"Failed to {operation} for {service_info}. Unexpected error", e, include_traceback=True)

    if not is_critical:
        logging.warning("Service %s operation partially failed. Manual cleanup may be needed.", service_info)
    return False


def _host_disable(connection, service, instanceha_service=None):
    """Force-down a compute service and log the disable reason."""
    service_info = f"service {getattr(service, 'binary', 'unknown')} on host {service.host}"

    # Step 1: Force the service down
    logging.info("Forcing %s down before evacuation", service.host)
    try:
        connection.services.force_down(service.id, True)
        logging.debug("Successfully forced down %s", service_info)
    except Exception as e:
        return _handle_nova_exception("force-down", service_info, e, is_critical=True)

    # Step 2: Log the reason for disabling (only if force_down succeeded)
    try:
        if instanceha_service:
            with instanceha_service.kdump_lock:
                is_kdump = _extract_hostname(service.host) in instanceha_service.kdump_fenced_hosts
        else:
            is_kdump = False
        suffix = f" {DISABLED_REASON_KDUMP_MARKER}" if is_kdump else ""
        disable_reason = _make_disable_reason(DISABLED_REASON_EVACUATION, suffix)
        connection.services.disable_log_reason(service.id, disable_reason)
        logging.info("Successfully disabled %s with reason: %s", service_info, disable_reason)
        return True
    except Exception as e:
        return _handle_nova_exception("log disable reason", service_info, e, is_critical=False)


def _check_kdump(stale_services: List[Any], service: InstanceHAService) -> Tuple[List[Any], List[str]]:
    """Check for kdump messages. Wait KDUMP_TIMEOUT for messages; evacuate immediately once received."""
    if not stale_services:
        return [], []

    logging.info("Checking %d hosts for kdump activity", len(stale_services))
    kdump_fenced = []
    waiting = []
    current_time = time.time()
    kdump_timeout = service.config.get_config_value('KDUMP_TIMEOUT')

    # Kdump state machine per host (under lock — UDP listener writes timestamps concurrently):
    # State 1: Fresh down host (check_start==0) → start waiting, set check_start=now
    # State 2: Waiting (check_start > 0, time < timeout) → continue waiting
    # State 3a: Kdump received (last_msg within timeout) → mark fenced, evacuate immediately
    # State 3b: Timeout expired (time >= timeout) → no kdump received, evacuate normally
    with service.kdump_lock:
        for svc in stale_services:
            hostname = _extract_hostname(svc.host)
            last_msg = service.kdump_hosts_timestamp.get(hostname, 0)
            check_start = service.kdump_hosts_checking.get(hostname, 0)

            if last_msg > 0 and (current_time - last_msg) <= kdump_timeout:
                kdump_fenced.append(svc.host)
                service.kdump_fenced_hosts.add(hostname)
                service.kdump_hosts_checking.pop(hostname, None)
                logging.info('Host %s fenced via kdump - evacuating', svc.host)
            elif check_start == 0:
                service.kdump_hosts_checking[hostname] = current_time
                waiting.append(svc.host)
                logging.info('Host %s down, waiting %ds for kdump', svc.host, kdump_timeout)
            elif (current_time - check_start) < kdump_timeout:
                waiting.append(svc.host)
                logging.info('Host %s waiting (%.1fs/%ds)', svc.host, current_time - check_start, kdump_timeout)
            else:
                service.kdump_hosts_checking.pop(hostname, None)
                logging.info('Host %s timeout expired, no kdump - evacuating', svc.host)

    kdump_fenced_services = [s for s in stale_services if s.host in kdump_fenced]
    to_evacuate = [s for s in stale_services if s.host not in waiting and s.host not in kdump_fenced]
    if kdump_fenced:
        logging.info('Total kdump-fenced: %d', len(kdump_fenced))
    return to_evacuate, kdump_fenced_services


def _host_enable(connection, nova_service, reenable: bool = False, service=None) -> bool:
    """Enable a host service, optionally unsetting force-down."""
    if reenable:
        try:
            logging.debug('Unsetting force-down on host %s after evacuation', nova_service.host)
            connection.services.force_down(nova_service.id, False)
            logging.info('Unset force-down for %s', nova_service.host)
        except Exception as e:
            logging.warning('Could not unset force-down for %s as not all the migrations are complete yet. Will try again the next poll cycle.', nova_service.host)
            logging.debug('Full error details for %s: %s', nova_service.host, e)
            return False

        if 'kdump' in (getattr(nova_service, 'disabled_reason', '') or ''):
            try:
                connection.services.disable_log_reason(nova_service.id, _make_disable_reason(DISABLED_REASON_EVACUATION_COMPLETE))
                if service:
                    hostname = _extract_hostname(nova_service.host)
                    with service.kdump_lock:
                        service.kdump_fenced_hosts.discard(hostname)
                        service.kdump_hosts_timestamp.pop(hostname, None)
            except (AttributeError, KeyError) as e:
                logging.warning('Failed to clean up kdump marker for %s: %s', nova_service.host, e)
        return True

    try:
        _retry_with_backoff(
            lambda: connection.services.enable(nova_service.id),
            MAX_ENABLE_RETRIES,
            f'Enabling {nova_service.host}',
        )
        logging.info('Host %s is now enabled', nova_service.host)
        return True
    except Exception:
        return False


def _get_power_state(agent_type: str, session: requests.Session = None, **kwargs) -> Optional[str]:
    """Query power state via redfish or ipmi. Returns uppercase string or None."""
    if agent_type == 'redfish':
        url, user, passwd, timeout, config_mgr = (
            kwargs['url'], kwargs['user'], kwargs['passwd'],
            kwargs['timeout'], kwargs['config_mgr']
        )

        if not validate_input(url, 'url', "Redfish power state check"):
            logging.error("Redfish power state check failed: invalid URL")
            return None

        try:
            response = _make_ssl_request('get', url, (user, passwd), timeout, config_mgr, session=session)
            if response.status_code == 200:
                data = response.json()
                return data.get('PowerState', '').upper()
        except (requests.exceptions.Timeout, requests.exceptions.ConnectionError) as e:
            logging.debug('Power state check network error: %s', type(e).__name__)
        except Exception as e:
            _safe_log_exception('Failed to get power state', e)

    elif agent_type == 'ipmi':
        ip, port, user, passwd, timeout = (
            kwargs['ip'], kwargs['port'], kwargs['user'],
            kwargs['passwd'], kwargs['timeout']
        )

        try:
            env = {'PATH': os.environ.get('PATH', '/usr/bin:/usr/sbin'), 'IPMITOOL_PASSWORD': passwd}
            cmd = ['ipmitool', '-I', 'lanplus', '-H', ip, '-U', user, '-E', '-p', port, 'power', 'status']
            cmd_output = subprocess.run(cmd, timeout=timeout, capture_output=True, text=True, check=True, env=env)
            return cmd_output.stdout.strip().upper()
        except subprocess.TimeoutExpired:
            logging.error('Failed to get IPMI power state: timeout expired')
        except subprocess.CalledProcessError as e:
            logging.error('Failed to get IPMI power state: command failed with return code %d' % e.returncode)

    return None

def _redfish_reset(url, user, passwd, timeout, action, config_mgr):
    """Perform a Redfish reset operation on a computer system."""
    if not all([url, user, passwd, action]):
        logging.error("Redfish reset failed: missing required parameters")
        return False

    # Validate inputs to prevent SSRF and injection attacks
    if not validate_inputs({'url': url, 'power_action': action}, "Redfish reset"):
        logging.error("Redfish reset failed: invalid inputs")
        return False

    timeout = max(5, min(300, int(timeout or 30)))  # Clamp between 5-300 seconds
    url = url.rstrip('/')
    reset_url = f"{url}/Actions/ComputerSystem.Reset"
    safe_url = _sanitize_url(url)  # Sanitize for logging

    payload = {"ResetType": action}
    headers = {'Content-Type': 'application/json', 'Accept': 'application/json'}

    # Retry logic for transient failures
    for attempt in range(MAX_FENCING_RETRIES):
        try:
            response = _make_ssl_request('post', reset_url, (user, passwd), timeout,
                                        config_mgr, json=payload, headers=headers)

            if response.status_code in [200, 202, 204]:
                if action == "ForceOff":
                    poll_session = requests.Session()
                    try:
                        return _wait_for_power_off(
                            lambda: _get_power_state('redfish', session=poll_session, url=url, user=user, passwd=passwd, timeout=timeout, config_mgr=config_mgr),
                            timeout, safe_url, 'OFF', 'Redfish')
                    finally:
                        poll_session.close()
                else:
                    logging.info("Redfish reset successful: %s on %s", action, safe_url)
                    return True
            elif response.status_code in [400, 409]:
                # Check if server is already powered off
                power_state = _get_power_state('redfish', url=url, user=user, passwd=passwd, timeout=timeout, config_mgr=config_mgr)
                if power_state == 'OFF':
                    if action in ['On', 'ForceOn']:
                        # Server is OFF but not yet ready to accept power-on
                        # (e.g. PRIMEQUEST 4400E needs time after ForceOff before
                        # accepting On). Retry with backoff.
                        if attempt < MAX_FENCING_RETRIES - 1:
                            logging.warning(
                                "Redfish reset: %s rejected with %d while server is OFF "
                                "(attempt %d/%d), server may not be ready yet, retrying...",
                                action, response.status_code, attempt + 1, MAX_FENCING_RETRIES)
                            time.sleep(FENCING_RETRY_DELAY_SECONDS * (attempt + 1))
                            continue
                        else:
                            logging.error(
                                "Redfish reset failed: %s rejected with %d while server is OFF "
                                "after %d attempts for %s",
                                action, response.status_code, MAX_FENCING_RETRIES, safe_url)
                            return False
                    else:
                        logging.info("Redfish reset successful: %s on %s (already off)", action, safe_url)
                        return True
                else:
                    logging.error("Redfish reset failed: %s conflict but not OFF (status: %s) for %s", response.status_code, power_state, safe_url)
                    return False
            elif response.status_code in [401, 403]:
                # Authentication/authorization errors - don't retry
                logging.error("Redfish reset failed: authentication error (status %d) for %s", response.status_code, safe_url)
                return False
            elif response.status_code in [500, 502, 503, 504] and attempt < MAX_FENCING_RETRIES - 1:
                # Transient server errors - retry
                logging.warning("Redfish reset failed: server error %d (attempt %d/%d), retrying...",
                              response.status_code, attempt + 1, MAX_FENCING_RETRIES)
                time.sleep(FENCING_RETRY_DELAY_SECONDS * (attempt + 1))
                continue
            else:
                logging.error("Redfish reset failed: status %d for %s", response.status_code, safe_url)
                return False

        except (requests.exceptions.Timeout, requests.exceptions.ConnectionError) as e:
            # Network errors - retry
            if attempt < MAX_FENCING_RETRIES - 1:
                logging.warning("Redfish reset network error (attempt %d/%d), retrying...", attempt + 1, MAX_FENCING_RETRIES)
                time.sleep(FENCING_RETRY_DELAY_SECONDS * (attempt + 1))
                continue
            else:
                _safe_log_exception(f"Redfish reset failed for {safe_url}", e)
                return False
        except Exception as e:
            _safe_log_exception(f"Redfish reset failed for {safe_url}", e)
            return False

    return False

def _bmh_fence(token, namespace, host, action, service):
    """Fence a BareMetal Host using Kubernetes API."""
    if not all([token, namespace, host]) or action not in ['on', 'off']:
        logging.error("BMH fencing failed: invalid parameters")
        return False

    # Validate inputs to prevent SSRF attacks
    if not validate_inputs({'k8s_namespace': namespace, 'k8s_resource': host, 'power_action': action}, "BMH fencing"):
        logging.error("BMH fencing failed: invalid inputs")
        return False

    base_url = f"https://kubernetes.default.svc/apis/metal3.io/v1alpha1/namespaces/{namespace}/baremetalhosts/{host}"
    headers = {'Authorization': f'Bearer {token}', 'Content-Type': 'application/merge-patch+json'}
    cacert = '/var/run/secrets/kubernetes.io/serviceaccount/ca.crt'

    if not os.path.exists(cacert):
        logging.error("BMH fencing failed: CA certificate not found")
        return False

    try:
        if action == 'off':
            # Use reboot annotation with hard mode for immediate hard power-off
            # Combined with spec.online=false to ensure it stays off
            data = {
                "spec": {"online": False},
                "metadata": {"annotations": {"reboot.metal3.io/iha": '{"mode": "hard"}'}}
            }
        else:
            # Power on: set online=True and remove reboot annotation if present
            data = {
                "spec": {"online": True},
                "metadata": {"annotations": {"reboot.metal3.io/iha": None}}
            }

        fencing_timeout = service.config.get_config_value('FENCING_TIMEOUT')
        response = requests.patch(f"{base_url}?fieldManager=kubectl-patch",
                                headers=headers, json=data, verify=cacert, timeout=fencing_timeout)
        response.raise_for_status()

        if action == 'off':
            return _bmh_wait_for_power_off(base_url, headers, cacert, host, fencing_timeout, 3, service)
        else:
            logging.info("BMH power on successful for %s", host)
            return True

    except Exception as e:
        _safe_log_exception(f"BMH fencing failed for {host}", e)
        return False


def _bmh_wait_for_power_off(get_url, headers, cacert, host, timeout, poll_interval, service):
    """Wait for BareMetal Host to power off by polling its status."""
    request_timeout = service.config.get_config_value('FENCING_TIMEOUT')
    logging.debug("Waiting up to %d seconds for %s to power off", timeout, host)
    timeout_end = time.monotonic() + timeout

    with requests.Session() as session:
        while time.monotonic() < timeout_end:
            time.sleep(poll_interval)

            try:
                response = session.get(get_url, headers=headers, verify=cacert, timeout=request_timeout)
                response.raise_for_status()
            except (requests.exceptions.Timeout, requests.exceptions.HTTPError,
                    requests.exceptions.RequestException) as e:
                logging.warning("Error checking power status for %s: %s, continuing to wait",
                               host, type(e).__name__)
                continue

            try:
                status_data = response.json()
            except (json.JSONDecodeError, ValueError) as e:
                logging.warning("Invalid JSON response checking power status for %s: %s, continuing to wait", host, e)
                continue

            try:
                power_status = status_data['status']['poweredOn']
                logging.debug("Power status for %s: %s", host, power_status)
                if not power_status:
                    logging.info("BMH power off confirmed for %s", host)
                    return True
            except (KeyError, Exception) as e:
                logging.warning("Error parsing power status for %s: %s, continuing to wait", host, e)
                continue

    logging.error("BMH power off timeout: %s did not power off within %d seconds", host, timeout)
    return False


def _validate_fencing_params(fencing_data, required_params, agent_name, host) -> bool:
    """Validate required fencing parameters are present."""
    missing = [p for p in required_params if p not in fencing_data]
    if missing:
        logging.error("Missing %s params for %s: %s", agent_name, host, missing)
        return False
    return True

def _validate_fencing_inputs(fencing_data, agent_name) -> bool:
    """Validate fencing input parameters to prevent injection attacks."""
    validations = {'ip_address': fencing_data["ipaddr"]}
    if "ipport" in fencing_data:
        validations['port'] = fencing_data["ipport"]
    if "login" in fencing_data:
        validations['username'] = fencing_data["login"]
    return validate_inputs(validations, agent_name)

def _execute_ipmi_fence(host, action, fencing_data, timeout) -> bool:
    """Execute IPMI fencing operation with retry logic."""
    timeout = max(5, min(300, int(timeout or 30)))
    ipaddr, ipport, login, passwd = fencing_data["ipaddr"], fencing_data["ipport"], \
                                     fencing_data["login"], fencing_data["passwd"]

    env = {'PATH': os.environ.get('PATH', '/usr/bin:/usr/sbin'), 'IPMITOOL_PASSWORD': passwd}
    cmd = ["ipmitool", "-I", "lanplus", "-H", ipaddr, "-U", login, "-E", "-p", ipport, "power", action]

    try:
        _retry_with_backoff(
            lambda: subprocess.run(cmd, timeout=timeout, env=env, capture_output=True, text=True, check=True),
            MAX_FENCING_RETRIES,
            f'IPMI {action} for {host}',
        )
    except Exception as e:
        _safe_log_exception(f"IPMI {action} failed for {host}", e)
        return False

    if action == 'off':
        return _wait_for_power_off(
            lambda: _get_power_state('ipmi', ip=ipaddr, port=ipport, user=login, passwd=passwd, timeout=timeout),
            timeout, host, 'CHASSIS POWER IS OFF', 'IPMI')

    logging.info("IPMI %s successful for %s", action, host)
    return True

def _fence_noop(host, action, fencing_data, service):
    """Noop fencing agent (no actual fencing)."""
    logging.warning("Using noop fencing agent for %s %s. VMs may get corrupted.", host, action)
    return True


def _fence_ipmi(host, action, fencing_data, service):
    """IPMI fencing agent."""
    if not _validate_fencing_params(fencing_data, ["ipaddr", "ipport", "login", "passwd"], "IPMI", host):
        return False
    if not _validate_fencing_inputs(fencing_data, "IPMI"):
        return False
    timeout = fencing_data.get("timeout", service.config.get_config_value('FENCING_TIMEOUT'))
    return _execute_ipmi_fence(host, action, fencing_data, timeout)


def _fence_redfish(host, action, fencing_data, service):
    """Redfish fencing agent."""
    if not _validate_fencing_params(fencing_data, ["ipaddr", "login", "passwd"], "Redfish", host):
        return False
    if not _validate_fencing_inputs(fencing_data, "Redfish"):
        return False
    url = _build_redfish_url(fencing_data)
    if not url:
        return False
    redfish_action = "ForceOff" if action == "off" else "On"
    timeout = fencing_data.get("timeout", service.config.get_config_value('FENCING_TIMEOUT'))
    return _redfish_reset(url, fencing_data["login"], fencing_data["passwd"],
                        timeout, redfish_action, service.config)


def _fence_bmh(host, action, fencing_data, service):
    """BareMetal Host fencing agent."""
    if not _validate_fencing_params(fencing_data, ["token", "host", "namespace"], "BMH", host):
        return False
    return _bmh_fence(fencing_data["token"], fencing_data["namespace"],
                     fencing_data["host"], action, service)


# Fencing agent dispatch table
FENCING_AGENTS = {
    'noop': _fence_noop,
    'ipmi': _fence_ipmi,
    'redfish': _fence_redfish,
    'bmh': _fence_bmh,
}


def _execute_fence_operation(host, action, fencing_data, service):
    """Unified fencing execution with agent-specific dispatch and validation."""
    agent = fencing_data.get("agent", "").lower()

    try:
        # Find matching fencing agent (exact match)
        agent_func = FENCING_AGENTS.get(agent)
        if agent_func:
            return agent_func(host, action, fencing_data, service)

        logging.error("Unknown fencing agent: %s", agent)
        return False

    except Exception as e:
        logging.error("%s fencing failed for %s: %s", agent.upper(), host, e)
        return False


def _host_fence(host, action, service):
    """Fence a host using the configured fencing agent."""
    if not host:
        logging.error("Invalid fence parameters: missing host")
        return False

    # Validate action parameter at entry point
    if not validate_input(action, 'power_action', "host fencing"):
        logging.error("Invalid fence action: %s", action)
        return False

    # Additional check for fence-specific actions
    if action not in ['on', 'off']:
        logging.error("Unsupported fence action: %s (only 'on' and 'off' supported)", action)
        return False

    logging.info("Fencing host %s %s", host, action)

    try:
        # Look up fencing configuration by comparing short hostnames
        short_hostname = _extract_hostname(host)
        matching_configs = [v for k, v in service.config.fencing.items()
                           if _extract_hostname(k) == short_hostname]

        if not matching_configs:
            logging.error("No fencing data found for %s", host)
            return False

        fencing_data = matching_configs[0]

        if not isinstance(fencing_data, dict) or "agent" not in fencing_data:
            logging.error("Invalid fencing data for %s", host)
            return False

        logging.debug("Using fencing agent '%s' for %s", fencing_data["agent"], host)
        return _execute_fence_operation(host, action, fencing_data, service)

    except Exception as e:
        logging.error("Fencing failed for %s: %s", host, e)
        return False


def _get_nova_connection(service):
    """Establish a connection to Nova using service configuration."""
    try:
        return service.create_connection()
    except NovaConnectionError:
        logging.error("Nova connection failed")
        return None
    except Exception as e:
        _safe_log_exception("Failed to establish Nova connection", e)
        return None


def _enable_matching_reserved_host(conn, failed_service, reserved_hosts, service,
                                   match_type: MatchType = MatchType.AGGREGATE) -> ReservedHostResult:
    """Enable a reserved host matching the failed service by aggregate or zone."""
    try:
        matching_hosts = []

        with service.reserved_hosts_lock:
            reserved_snapshot = list(reserved_hosts)

        if match_type == MatchType.AGGREGATE:
            # Match by aggregate
            compute_aggregate_ids = _aggregate_ids(conn, failed_service)
            if not compute_aggregate_ids:
                return ReservedHostResult(success=False)

            for host in reserved_snapshot:
                if (service.is_aggregate_evacuable(conn, host.host) and
                    set(_aggregate_ids(conn, host)).intersection(compute_aggregate_ids)):
                    matching_hosts.append(host)
        else:
            # Match by availability zone
            matching_hosts = [host for host in reserved_snapshot
                            if host.zone == failed_service.zone]

        if not matching_hosts:
            logging.warning("No reserved compute found in the same %s as %s", match_type.value, failed_service.host)
            return ReservedHostResult(success=False)

        # Enable the first matching host (thread-safe removal)
        selected_host = matching_hosts[0]
        with service.reserved_hosts_lock:
            if selected_host not in reserved_hosts:
                logging.warning("Reserved host %s already claimed by another thread", selected_host.host)
                return ReservedHostResult(success=False)
            reserved_hosts.remove(selected_host)

        if not _host_enable(conn, selected_host, reenable=False):
            logging.error("Failed to enable reserved host %s", selected_host.host)
            with service.reserved_hosts_lock:
                reserved_hosts.append(selected_host)
            return ReservedHostResult(success=False)

        logging.info("Enabled host %s from the reserved pool (same %s as %s)", selected_host.host, match_type.value, failed_service.host)
        return ReservedHostResult(success=True, hostname=selected_host.host)

    except Exception as e:
        logging.error("Error enabling reserved host by %s: %s", match_type.value, e)
        return ReservedHostResult(success=False)


def _return_reserved_host(conn, hostname, reserved_hosts, service):
    """Disable a reserved host and return it to the pool after evacuation failure."""
    try:
        svc_list = list(conn.services.list(binary='nova-compute', host=hostname))
        if not svc_list:
            logging.warning("Cannot return reserved host %s: service not found", hostname)
            return
        svc = svc_list[0]
        conn.services.disable_log_reason(svc.id, _make_disable_reason('instanceha-reserved'))
        with service.reserved_hosts_lock:
            reserved_hosts.append(svc)
        logging.info("Returned reserved host %s to pool after evacuation failure", hostname)
    except Exception as e:
        logging.error("Failed to return reserved host %s to pool: %s", hostname, e)


def _manage_reserved_hosts(conn, failed_service, reserved_hosts, service) -> ReservedHostResult:
    """Enable a replacement reserved host if available."""
    if not service.config.get_config_value('RESERVED_HOSTS'):
        logging.debug("Reserved hosts feature is disabled")
        return ReservedHostResult(success=True)

    if not reserved_hosts:
        logging.warning("Not enough hosts available from the reserved pool")
        return ReservedHostResult(success=True)  # This is not a failure condition

    try:
        # Try to enable a matching host based on configuration
        match_type = MatchType.AGGREGATE if service.config.get_config_value('TAGGED_AGGREGATES') else MatchType.ZONE
        result = _enable_matching_reserved_host(conn, failed_service, reserved_hosts, service, match_type)
        if result.success:
            return result

        # If no specific match found, it's still not a failure
        logging.debug("No matching reserved host found for %s, continuing with evacuation", failed_service.host)
        return ReservedHostResult(success=True)

    except Exception as e:
        logging.error("Error managing reserved hosts for %s: %s", failed_service.host, e)
        return ReservedHostResult(success=False)


def _post_evacuation_recovery(conn, failed_service, service, resume=False):
    """Power on the host after evacuation and update disable reason."""
    hostname = _extract_hostname(failed_service.host)
    with service.kdump_lock:
        kdump_fenced = hostname in service.kdump_fenced_hosts
    leave_disabled = service.config.get_config_value('LEAVE_DISABLED')

    logging.info("Evacuation successful. Starting recovery for %s", failed_service.host)

    try:
        # Step 1: Power on the host (skip if kdump fenced)
        if kdump_fenced:
            logging.info("Skipping power on for %s (kdump fenced)", failed_service.host)
            with service.kdump_lock:
                service.kdump_hosts_checking.pop(hostname, None)
        else:
            logging.debug("Powering on host %s", failed_service.host)
            power_on_result = _host_fence(failed_service.host, 'on', service)
            if not power_on_result:
                logging.error("Failed to power on %s during recovery", failed_service.host)
                return False

        # Update disabled reason to indicate evacuation complete (prevents resume loops)
        # Note: Service object may be stale (pre-disable status), so we always try to update
        # Preserve kdump marker if present to ensure proper re-enable delay
        try:
            suffix = f" {DISABLED_REASON_KDUMP_MARKER}" if kdump_fenced else ""
            new_reason = _make_disable_reason(DISABLED_REASON_EVACUATION_COMPLETE, suffix)
            conn.services.disable_log_reason(failed_service.id, new_reason)
            logging.debug("Updated disable reason for %s to indicate evacuation complete", failed_service.host)
        except Exception as e:
            logging.warning("Failed to update disable reason for %s: %s", failed_service.host, e)

        # Service re-enabling behavior depends on LEAVE_DISABLED setting
        if leave_disabled:
            logging.info("Recovery completed successfully for %s (will remain disabled due to LEAVE_DISABLED)",
                        failed_service.host)
        else:
            logging.info("Recovery completed successfully for %s (will re-enable when host is up)",
                        failed_service.host)
        return True

    except Exception as e:
        logging.error("Error during post-evacuation recovery for %s: %s", failed_service.host, e)
        logging.debug("Exception traceback:", exc_info=True)
        return False


_K8S_TOKEN_PATH = '/var/run/secrets/kubernetes.io/serviceaccount/token'
_K8S_NAMESPACE_PATH = '/var/run/secrets/kubernetes.io/serviceaccount/namespace'
_K8S_CA_PATH = '/var/run/secrets/kubernetes.io/serviceaccount/ca.crt'
_K8S_API_BASE = 'https://kubernetes.default.svc'


_k8s_credentials_warned = False


def _get_k8s_credentials():
    """Read ServiceAccount token and namespace from the pod filesystem."""
    global _k8s_credentials_warned
    try:
        with open(_K8S_TOKEN_PATH, 'r') as f:
            token = f.read().strip()
        namespace = os.environ.get('POD_NAMESPACE', '')
        if not namespace:
            try:
                with open(_K8S_NAMESPACE_PATH, 'r') as f:
                    namespace = f.read().strip()
            except (IOError, OSError):
                pass
        if not token or not namespace:
            if not _k8s_credentials_warned:
                logging.warning("K8s credentials unavailable: token=%s, namespace=%s",
                                'present' if token else 'missing',
                                'present' if namespace else 'missing')
                _k8s_credentials_warned = True
            return None, None
        _k8s_credentials_warned = False
        return token, namespace
    except (IOError, OSError):
        return None, None


def _check_k8s_api_reachable():
    """Check if the Kubernetes API server is reachable."""
    token, namespace = _get_k8s_credentials()
    if not token:
        return False
    headers = {'Authorization': f'Bearer {token}'}
    try:
        response = requests.get(
            f"{_K8S_API_BASE}/api/v1/namespaces/{namespace}",
            headers=headers,
            verify=_K8S_CA_PATH,
            timeout=5,
        )
        return response.status_code < 500
    except Exception:
        return False


def _emit_k8s_event(host, reason, message, event_type='Normal'):
    """Emit a Kubernetes Event on the InstanceHA CR."""
    token, namespace = _get_k8s_credentials()
    if not token:
        logging.debug("K8s credentials not available, skipping event emission")
        return

    cr_name = os.environ.get('INSTANCEHA_CR_NAME', '')
    if not cr_name:
        logging.debug("INSTANCEHA_CR_NAME not set, skipping event emission")
        return

    now = datetime.now(tz=timezone.utc).strftime('%Y-%m-%dT%H:%M:%SZ')
    event_name = f"{cr_name}.{uuid.uuid4().hex[:16]}"

    event = {
        'apiVersion': 'v1',
        'kind': 'Event',
        'metadata': {
            'name': event_name,
            'namespace': namespace,
        },
        'involvedObject': {
            'apiVersion': 'instanceha.openstack.org/v1beta1',
            'kind': 'InstanceHa',
            'name': cr_name,
            'namespace': namespace,
        },
        'reason': reason,
        'message': f"[{host}] {message}",
        'type': event_type,
        'firstTimestamp': now,
        'lastTimestamp': now,
        'source': {
            'component': 'instanceha',
        },
        'reportingComponent': 'instanceha',
        'reportingInstance': os.environ.get('POD_NAME', 'instanceha'),
    }

    url = f"{_K8S_API_BASE}/api/v1/namespaces/{namespace}/events"
    headers = {
        'Authorization': f'Bearer {token}',
        'Content-Type': 'application/json',
    }

    try:
        response = requests.post(url, headers=headers, json=event,
                                 verify=_K8S_CA_PATH, timeout=5)
        if response.status_code in (200, 201):
            logging.debug("Emitted K8s event: %s for host %s", reason, host)
        else:
            logging.warning("Failed to emit K8s event (HTTP %d): %s",
                            response.status_code, response.text[:200])
    except Exception as e:
        logging.warning("Failed to emit K8s event: %s", e)


def _execute_step(step_name, step_func, host_name, *args, **kwargs):
    """Execute a processing step with unified error handling."""
    try:
        result = step_func(*args, **kwargs)
        if not result:
            logging.error("%s failed for %s", step_name, host_name)
        return result
    except Exception as e:
        logging.error("%s failed for %s: %s", step_name, host_name, e)
        return False


def process_service(failed_service, reserved_hosts, resume, service) -> bool:
    """Process a failed compute service through the complete recovery workflow."""
    if not failed_service or not hasattr(failed_service, 'host'):
        logging.error("Invalid service object provided")
        return False

    host_name = failed_service.host
    hostname = _extract_hostname(host_name)
    reserved_hosts = reserved_hosts or []

    logging.info("Processing service %s (resume=%s)", host_name, resume)

    with track_host_processing(service, hostname):
        try:
            conn = _get_nova_connection(service)
            if not conn:
                logging.error("Nova connection failed for %s", host_name)
                return False

            # Execute processing steps
            if not resume:
                # Fence (power off) the host before evacuation
                _emit_k8s_event(host_name, 'FencingStarted',
                                'Fencing host (power off)')
                FENCING_TOTAL.labels(host=host_name, result='started').inc()
                if not _execute_step("Fencing", _host_fence, host_name, host_name, 'off', service):
                    _emit_k8s_event(host_name, 'FencingFailed',
                                    'Fencing failed', event_type='Warning')
                    FENCING_TOTAL.labels(host=host_name, result='failed').inc()
                    return False
                _emit_k8s_event(host_name, 'FencingSucceeded',
                                'Host fenced successfully')
                FENCING_TOTAL.labels(host=host_name, result='succeeded').inc()

            # Disable the host (skip if already disabled from previous evacuation attempt)
            # Note: kdump-fenced hosts use resume=True but are NOT yet disabled, so we still disable them
            if not (resume and failed_service.forced_down and 'disabled' in failed_service.status):
                if not _execute_step("Host disable", _host_disable, host_name, conn, failed_service, service):
                    return False

            # Manage reserved hosts and capture the enabled host (if any)
            try:
                reserved_result = _manage_reserved_hosts(conn, failed_service, reserved_hosts, service)
                if not reserved_result.success:
                    logging.error("Reserved host management failed for %s", host_name)
                    return False
                target_host = reserved_result.hostname
            except Exception as e:
                logging.error("Reserved host management failed for %s: %s", host_name, e)
                return False

            # Evacuate instances, optionally targeting the enabled reserved host
            _emit_k8s_event(host_name, 'EvacuationStarted',
                            'Starting VM evacuation')
            EVACUATION_TOTAL.labels(host=host_name, result='started').inc()
            evac_target = target_host if target_host and service.config.get_config_value('FORCE_RESERVED_HOST_EVACUATION') else None
            if evac_target:
                logging.info("Forcing evacuation to reserved host: %s", evac_target)
            if not _execute_step("Evacuation", _host_evacuate, host_name,
                                conn, failed_service, service, evac_target):
                _emit_k8s_event(host_name, 'EvacuationFailed',
                                'VM evacuation failed', event_type='Warning')
                EVACUATION_TOTAL.labels(host=host_name, result='failed').inc()
                if target_host:
                    try:
                        servers_on_target = conn.servers.list(
                            search_opts={'host': target_host, 'all_tenants': 1})
                        if servers_on_target:
                            logging.warning(
                                "Not returning reserved host %s to pool: "
                                "%d VM(s) evacuated to it",
                                target_host, len(servers_on_target))
                        else:
                            _return_reserved_host(conn, target_host, reserved_hosts, service)
                    except Exception as e:
                        logging.warning(
                            "Could not check VMs on reserved host %s, "
                            "not returning to pool: %s", target_host, e)
                return False
            _emit_k8s_event(host_name, 'EvacuationSucceeded',
                            'VM evacuation completed successfully')
            EVACUATION_TOTAL.labels(host=host_name, result='succeeded').inc()

            if not _execute_step("Recovery", _post_evacuation_recovery, host_name,
                                conn, failed_service, service, resume):
                return False

            _emit_k8s_event(host_name, 'RecoveryCompleted',
                            'Host recovery workflow completed')
            RECOVERY_COMPLETED_TOTAL.labels(host=host_name).inc()
            return True

        except Exception as e:
            logging.error("Service processing failed for %s: %s", host_name, e)
            _emit_k8s_event(host_name, 'ProcessingFailed',
                            f'Service processing failed: {_sanitize_message(str(e))}',
                            event_type='Warning')
            PROCESSING_FAILED_TOTAL.labels(host=host_name).inc()
            return False


def _initialize_service(config_mgr):
    """Initialize InstanceHA service and supporting threads."""
    try:
        service = InstanceHAService(config_mgr)
    except Exception as e:
        logging.error("Failed to initialize InstanceHA service: %s", e)
        sys.exit(1)

    # Start health check server
    health_check_thread = threading.Thread(target=service.start_health_check_server)
    health_check_thread.daemon = True
    health_check_thread.start()

    # Start K8s API connectivity monitor
    service.start_k8s_health_check()

    # Start kdump listener if enabled
    if service.config.get_config_value('CHECK_KDUMP'):
        kdump_thread = threading.Thread(target=_kdump_udp_listener, args=(service,))
        kdump_thread.daemon = True
        kdump_thread.start()

    # Start heartbeat listener if enabled
    if service.config.get_config_value('CHECK_HEARTBEAT'):
        heartbeat_thread = threading.Thread(target=_heartbeat_udp_listener, args=(service,))
        heartbeat_thread.daemon = True
        heartbeat_thread.start()

    return service

def _establish_nova_connection(service, fatal=True):
    """Establish Nova connection using service configuration."""
    try:
        return service.create_connection()
    except NovaConnectionError:
        logging.error("Failed: Unable to connect to Nova")
        if fatal:
            sys.exit(1)
        return None
    except Exception as e:
        _safe_log_exception("Failed: Unable to connect to Nova", e)
        if fatal:
            sys.exit(1)
        return None

def _is_service_resume_candidate(svc) -> bool:
    """Check if a service is a candidate for resuming evacuation."""
    if not (svc.forced_down and svc.state == 'down' and 'disabled' in svc.status):
        return False

    reason = getattr(svc, 'disabled_reason', '') or ''
    return (DISABLED_REASON_EVACUATION in reason and
            DISABLED_REASON_EVACUATION_FAILED not in reason and
            DISABLED_REASON_EVACUATION_COMPLETE not in reason)


def _is_service_stale(svc, target_date: datetime) -> bool:
    """Check whether a service's updated_at timestamp is older than target_date."""
    try:
        updated = svc.updated_at
        updated_naive = datetime.fromisoformat(str(updated).replace('Z', '+00:00')).replace(tzinfo=None)
        return updated_naive < target_date.replace(tzinfo=None)
    except (ValueError, TypeError, AttributeError):
        logging.warning("Service %s has invalid updated_at: %r, skipping (not treating as stale)",
                       getattr(svc, 'host', 'unknown'), getattr(svc, 'updated_at', None))
        return False


def _categorize_services(services: List[Any], target_date: datetime) -> tuple:
    """Categorize services into compute nodes, resume candidates, and re-enable candidates."""
    # Compute nodes needing evacuation (not disabled/forced-down, and either down or stale)
    compute_nodes = (svc for svc in services
                     if not ('disabled' in svc.status or svc.forced_down)
                     and (svc.state == 'down' or _is_service_stale(svc, target_date)))

    # Resume candidates (forced down, disabled with instanceha marker, not failed, not complete)
    resume = (svc for svc in services if _is_service_resume_candidate(svc))

    # Re-enable candidates (forced down OR disabled with instanceha complete marker, but NOT resume candidates)
    # Note: We unset force_down first to allow service to report up, then enable once up
    reenable = (svc for svc in services
                if (('enabled' in svc.status and svc.forced_down)
                   or ('disabled' in svc.status and DISABLED_REASON_EVACUATION_COMPLETE in (svc.disabled_reason or '')))
                and not _is_service_resume_candidate(svc))

    return compute_nodes, resume, reenable

def _check_critical_services(conn, services, compute_nodes):
    """Check if critical Nova services are operational for evacuation."""
    # Check if at least one scheduler is up
    schedulers = [s for s in services if s.binary == 'nova-scheduler']
    if schedulers and not any(s.state == 'up' for s in schedulers):
        return False, "All nova-scheduler services are down"

    return True, ""

def _cleanup_filtered_hosts(service, marked_hostnames, final_hostnames, current_time):
    """Clean up hosts from processing tracking that were filtered out."""
    with service.processing_lock:
        # Use set operations to find hosts to cleanup (marked but not finalized)
        candidate_hosts = marked_hostnames - final_hostnames
        # Remove hosts that match the current processing time
        to_cleanup = [h for h in candidate_hosts if service.hosts_processing.get(h) == current_time]
        for hostname in to_cleanup:
            service.hosts_processing.pop(hostname, None)
        if to_cleanup:
            logging.debug('Cleaned up %d filtered hosts from processing tracking', len(to_cleanup))

def _filter_processing_hosts(service, compute_nodes, to_resume):
    """Filter out hosts already being processed and mark new ones."""
    current_time = time.monotonic()
    max_processing_time = max(service.config.get_config_value('FENCING_TIMEOUT'), MAX_EVACUATION_TIMEOUT_SECONDS)
    marked_hostnames = set()

    with service.processing_lock:
        # Clean up expired processing entries
        service._cleanup_dict_by_condition(
            service.hosts_processing,
            lambda h, t: current_time - t > max_processing_time + MAX_PROCESSING_TIME_PADDING_SECONDS,
            'Cleaned up expired processing entry for {}')

        # Filter out hosts currently being processed
        original_count = len(compute_nodes)
        compute_nodes_filtered = []
        to_resume_filtered = []

        for svc in compute_nodes:
            hostname = _extract_hostname(svc.host)
            if hostname not in service.hosts_processing:
                compute_nodes_filtered.append(svc)

        for svc in to_resume:
            hostname = _extract_hostname(svc.host)
            if hostname not in service.hosts_processing:
                to_resume_filtered.append(svc)

        if original_count > len(compute_nodes_filtered):
            skipped_hosts = original_count - len(compute_nodes_filtered)
            logging.info('Skipped %d hosts already being processed by another poll cycle', skipped_hosts)

        # Mark hosts as being processed
        for svc in compute_nodes_filtered + to_resume_filtered:
            hostname = _extract_hostname(svc.host)
            service.hosts_processing[hostname] = current_time
            marked_hostnames.add(hostname)

    return compute_nodes_filtered, to_resume_filtered, marked_hostnames, current_time


def _prepare_evacuation_resources(conn, service, services, compute_nodes, aggregates=None):
    """Prepare and filter resources for evacuation."""
    if not compute_nodes:
        return compute_nodes, [], [], []

    # Force refresh cache when compute nodes go down
    service.refresh_evacuable_cache(conn, force=True)

    # Filter compute nodes with servers
    host_servers_cache = service.get_hosts_with_servers_cached(conn, compute_nodes)
    original_count = len(compute_nodes)
    compute_nodes = service.filter_hosts_with_servers(compute_nodes, host_servers_cache)
    filtered_count = len(compute_nodes)
    logging.debug("Filtered compute nodes: %d -> %d (removed %d hosts with no servers)",
                original_count, filtered_count, original_count - filtered_count)

    if not compute_nodes:
        logging.debug("No compute nodes with servers to evacuate - all filtered out")
        return [], [], [], []

    # Get reserved hosts if enabled
    reserved_hosts = []
    if service.config.get_config_value('RESERVED_HOSTS'):
        reserved_hosts = [svc for svc in services
                          if 'disabled' in svc.status and 'reserved' in (svc.disabled_reason or '')]

    # Get evacuable resources
    images_enabled = service.config.get_config_value('TAGGED_IMAGES')
    flavors_enabled = service.config.get_config_value('TAGGED_FLAVORS')
    images = service.get_evacuable_images(conn) if images_enabled else []
    flavors = service.get_evacuable_flavors(conn) if flavors_enabled else []

    # Filter evacuable servers if tagging is enabled
    if (images_enabled or flavors_enabled) and host_servers_cache:
        compute_nodes = service.filter_hosts_with_evacuable_servers(compute_nodes, host_servers_cache, flavors, images)

    # Apply aggregate filtering if enabled
    if service.config.get_config_value('TAGGED_AGGREGATES'):
        compute_nodes = _filter_by_aggregates(conn, service, compute_nodes, services, aggregates=aggregates)

    return compute_nodes, reserved_hosts, images, flavors


def _filter_reachable_hosts(service, compute_nodes):
    """Filter out hosts still sending heartbeats (host OS alive, only nova-compute down).

    Returns (unreachable, skipped, cliff_detected):
    - unreachable: hosts with no recent heartbeat (should be fenced)
    - skipped: hosts with recent heartbeat OR all hosts if cliff detected
    - cliff_detected: True if a sudden mass heartbeat loss triggered a blanket skip
    """
    if not compute_nodes:
        return compute_nodes, [], False

    heartbeat_timeout = service.config.get_config_value('HEARTBEAT_TIMEOUT')
    current_time = time.time()

    with service.heartbeat_lock:
        listener_start_time = service.heartbeat_listener_start_time

        # Grace period: can't trust absence of heartbeats until listener has run
        # for at least HEARTBEAT_TIMEOUT seconds
        if listener_start_time == 0.0 or (current_time - listener_start_time) < heartbeat_timeout:
            return compute_nodes, [], False

        timestamps_snapshot = dict(service.heartbeat_hosts_timestamp)

    current_active = sum(
        1 for ts in timestamps_snapshot.values()
        if ts > 0 and (current_time - ts) <= heartbeat_timeout
    )
    previous_active = service.heartbeat_previous_active_count

    cliff_detected = False
    if previous_active >= 3:
        threshold = service.config.get_config_value('HEARTBEAT_CLIFF_THRESHOLD')
        drop_percent = (previous_active - current_active) / previous_active * 100
        if drop_percent >= threshold:
            cliff_detected = True
            logging.error(
                'Heartbeat cliff detected: %d→%d active hosts (%.1f%% drop, '
                'threshold %d%%) — possible network partition, skipping fencing this cycle',
                previous_active, current_active, drop_percent, threshold)
            _emit_k8s_event('cluster', 'HeartbeatCliff',
                            f'Sudden heartbeat loss: {previous_active}→{current_active} active hosts '
                            f'({drop_percent:.1f}% drop) — skipping fencing',
                            event_type='Warning')
            HEARTBEAT_CLIFF_TOTAL.inc()
            return [], list(compute_nodes), True

    if not cliff_detected:
        service.heartbeat_previous_active_count = current_active

    unreachable = []
    skipped = []

    for svc in compute_nodes:
        hostname = _extract_hostname(svc.host)
        last_heartbeat = timestamps_snapshot.get(hostname, 0)

        if last_heartbeat > 0 and (current_time - last_heartbeat) <= heartbeat_timeout:
            skipped.append(svc)
            logging.warning(
                'Host %s reported down by Nova but heartbeat received %.1fs ago — '
                'skipping fencing (likely nova-compute crash)',
                svc.host, current_time - last_heartbeat)
        else:
            unreachable.append(svc)

    return unreachable, skipped, False


def _process_stale_services(conn, service, services, compute_nodes, to_resume):
    """Process stale compute services for evacuation."""
    # Convert generators to lists - needed for multiple iterations and length checks
    compute_nodes = list(compute_nodes)
    to_resume = list(to_resume)

    if not (compute_nodes or to_resume):
        return

    # Filter out hosts already being processed
    compute_nodes, to_resume, marked_hostnames, current_time = _filter_processing_hosts(service, compute_nodes, to_resume)

    if not (compute_nodes or to_resume):
        _cleanup_filtered_hosts(service, marked_hostnames, set(), current_time)
        return

    # Filter out hosts still reachable via heartbeat
    if service.config.get_config_value('CHECK_HEARTBEAT'):
        compute_nodes, heartbeat_skipped, cliff_detected = _filter_reachable_hosts(service, compute_nodes)
        if not cliff_detected:
            for svc in heartbeat_skipped:
                _emit_k8s_event(svc.host, 'HostReachable',
                                'Host reported down by Nova but still sending heartbeats — '
                                'skipping fencing (likely nova-compute crash)',
                                event_type='Warning')
                HOST_REACHABLE_TOTAL.labels(host=svc.host).inc()

        if not (compute_nodes or to_resume):
            _cleanup_filtered_hosts(service, marked_hostnames, set(), current_time)
            return

    if compute_nodes:
        logging.warning('The following computes are down: %s', [svc.host for svc in compute_nodes])
        for svc in compute_nodes:
            _emit_k8s_event(svc.host, 'HostDown',
                            'Compute host detected as down', event_type='Warning')
            HOST_DOWN_TOTAL.labels(host=svc.host).inc()

    # Fetch aggregates once per poll cycle to avoid redundant API calls
    aggregates = None
    if service.config.get_config_value('TAGGED_AGGREGATES'):
        try:
            aggregates = conn.aggregates.list()
        except Exception as e:
            logging.warning("Failed to fetch aggregates: %s", e)

    # Prepare resources for evacuation
    compute_nodes, reserved_hosts, images, flavors = _prepare_evacuation_resources(
        conn, service, services, compute_nodes, aggregates=aggregates)

    # Check evacuation threshold
    if services and compute_nodes:
        # When TAGGED_AGGREGATES is enabled, calculate threshold against evacuable hosts only
        if service.config.get_config_value('TAGGED_AGGREGATES'):
            total_evacuable = _count_evacuable_hosts(conn, service, services, aggregates=aggregates)
            threshold_percent = (len(compute_nodes) / total_evacuable * 100) if total_evacuable > 0 else 0
        else:
            active_services = [s for s in services if 'disabled' not in s.status and not s.forced_down]
            threshold_percent = (len(compute_nodes) / len(active_services)) * 100 if active_services else 0

        threshold = service.config.get_config_value('THRESHOLD')
        if threshold_percent > threshold:
            logging.error('Number of impacted computes (%.1f%%) exceeds threshold (%d%%). Not evacuating.', threshold_percent, threshold)
            _emit_k8s_event('cluster', 'ThresholdExceeded',
                            f'Impacted computes ({threshold_percent:.1f}%) exceed threshold ({threshold}%%), evacuation skipped',
                            event_type='Warning')
            THRESHOLD_EXCEEDED_TOTAL.inc()
            _cleanup_filtered_hosts(service, marked_hostnames, set(), current_time)
            return

    # Per-aggregate failure threshold check
    if compute_nodes and isinstance(aggregates, list) and service.config.get_config_value('TAGGED_AGGREGATES'):
        compute_nodes, agg_blocked = _filter_by_aggregate_threshold(
            compute_nodes, aggregates, service)
        if agg_blocked:
            logging.warning('Blocked %d host(s) due to per-aggregate threshold: %s',
                           len(agg_blocked), [svc.host for svc in agg_blocked])
        if not compute_nodes and not to_resume:
            logging.warning('All compute nodes blocked by per-aggregate thresholds')
            _cleanup_filtered_hosts(service, marked_hostnames, set(), current_time)
            return

    # Process evacuations
    if not service.config.get_config_value('DISABLED'):
        # Check if critical services are operational
        can_evacuate, error_msg = _check_critical_services(conn, services, compute_nodes)
        if not can_evacuate:
            logging.error('Cannot evacuate: %s. Skipping evacuation.', error_msg)
            _cleanup_filtered_hosts(service, marked_hostnames, set(), current_time)
            return

        if service.config.get_config_value('CHECK_KDUMP'):
            to_evacuate, kdump_fenced = _check_kdump(compute_nodes, service)
        else:
            to_evacuate = compute_nodes
            kdump_fenced = []

        workers = service.config.get_config_value('WORKERS')
        poll_interval = service.config.get_config_value('POLL')
        for batch_name, batch, resume in [
            ('new evacuations', to_evacuate, False),
            ('kdump-fenced', kdump_fenced, True),
            ('resumed', to_resume, True),
        ]:
            if not batch:
                continue
            _run_concurrent(
                lambda svc, _resume=resume: process_service(svc, reserved_hosts, _resume, service),
                batch,
                workers,
                lambda svc: f"{svc.host} ({batch_name})",
            )

        # Clean up any hosts that were marked but filtered out before processing
        # (process_service cleans up hosts it processes, so we only need to clean up filtered ones)
        final_hostnames = {_extract_hostname(svc.host) for svc in to_evacuate + kdump_fenced + to_resume}
        _cleanup_filtered_hosts(service, marked_hostnames, final_hostnames, current_time)
    else:
        logging.info('InstanceHA DISABLED is true, not evacuating')
        _cleanup_filtered_hosts(service, marked_hostnames, set(), current_time)

def _get_evacuable_hosts_from_aggregates(conn, service, aggregates=None):
    """Build set of hosts in evacuable aggregates."""
    try:
        if aggregates is None:
            aggregates = conn.aggregates.list()
        evacuable_hosts = set()
        for agg in aggregates:
            if service._is_resource_evacuable(agg, service.evacuable_tag, ['metadata']):
                evacuable_hosts.update(agg.hosts)
        return evacuable_hosts
    except Exception as e:
        logging.warning("Failed to get evacuable hosts from aggregates: %s", e)
        return None


def _count_evacuable_hosts(conn, service, services, aggregates=None):
    """Count total number of compute services in evacuable aggregates."""
    evacuable_hosts = _get_evacuable_hosts_from_aggregates(conn, service, aggregates)
    active_services = [s for s in services if 'disabled' not in s.status and not s.forced_down]
    if evacuable_hosts is None:
        return len(active_services)
    return sum(1 for svc in active_services if svc.host in evacuable_hosts)


def _filter_by_aggregates(conn, service, compute_nodes, services, aggregates=None):
    """Filter compute nodes by aggregate evacuability."""
    evacuable_hosts = _get_evacuable_hosts_from_aggregates(conn, service, aggregates)
    if evacuable_hosts is not None:
        compute_nodes_down = list(compute_nodes)
        compute_nodes = [svc for svc in compute_nodes if svc.host in evacuable_hosts]

        down_not_tagged = [svc.host for svc in compute_nodes_down if svc not in compute_nodes]
        if down_not_tagged:
            logging.warning('Computes not part of evacuable aggregate: %s', down_not_tagged)

    return compute_nodes


def _filter_by_aggregate_threshold(compute_nodes, aggregates, service):
    """Filter out compute nodes whose aggregates exceeded their per-aggregate failure threshold."""
    failed_hosts = {svc.host for svc in compute_nodes}
    blocked_hosts = set()

    for agg in aggregates:
        if not service._is_resource_evacuable(agg, service.evacuable_tag, ['metadata']):
            continue

        metadata = getattr(agg, 'metadata', {}) or {}
        max_failures_str = metadata.get(AGGREGATE_MAX_FAILURES_KEY)
        if max_failures_str is None:
            continue

        try:
            max_failures = int(max_failures_str)
            if max_failures < 0:
                logging.warning("Aggregate '%s' has negative %s=%s, ignoring",
                               agg.name, AGGREGATE_MAX_FAILURES_KEY, max_failures_str)
                continue
        except (ValueError, TypeError):
            logging.warning("Aggregate '%s' has invalid %s='%s', ignoring",
                           agg.name, AGGREGATE_MAX_FAILURES_KEY, max_failures_str)
            continue

        agg_failed = failed_hosts.intersection(agg.hosts)
        if len(agg_failed) > max_failures:
            logging.error(
                "Aggregate '%s' has %d failed hosts (limit: %d). "
                "Blocking evacuation for hosts in this aggregate: %s",
                agg.name, len(agg_failed), max_failures, sorted(agg_failed))
            _emit_k8s_event(
                agg.name, 'AggregateThresholdExceeded',
                f'Aggregate has {len(agg_failed)} failed hosts '
                f'(limit: {max_failures}), evacuation blocked',
                event_type='Warning')
            AGGREGATE_THRESHOLD_EXCEEDED_TOTAL.labels(aggregate=agg.name).inc()
            blocked_hosts.update(agg_failed)

    allowed = [svc for svc in compute_nodes if svc.host not in blocked_hosts]
    blocked = [svc for svc in compute_nodes if svc.host in blocked_hosts]
    return allowed, blocked


def _process_reenabling(conn, service, to_reenable) -> None:
    """Process services that can be re-enabled."""
    # Convert generator to list for processing
    to_reenable = list(to_reenable)

    if not to_reenable:
        return

    # Filter out instanceha-evacuated services if LEAVE_DISABLED is enabled
    if service.config.get_config_value('LEAVE_DISABLED'):
        to_reenable = [svc for svc in to_reenable
                      if not ('disabled' in svc.status and 'instanceha evacuation complete' in (svc.disabled_reason or ''))]
        if not to_reenable:
            return

    logging.debug('Checking %d computes for re-enabling', len(to_reenable))
    force_enable = service.config.get_config_value('FORCE_ENABLE')

    for svc in to_reenable:
        try:
            # Check if migrations are complete (or force enable)
            if force_enable:
                migrations_complete = True
            else:
                query_time = (datetime.now(timezone.utc) - timedelta(minutes=MIGRATION_QUERY_MINUTES)).isoformat()
                migrations = conn.migrations.list(source_compute=svc.host, migration_type='evacuation',
                                                 changes_since=query_time, limit=str(MIGRATION_QUERY_LIMIT))
                incomplete = [m for m in migrations if m.status not in MIGRATION_STATUS_COMPLETED and m.status not in MIGRATION_STATUS_ERROR]
                migrations_complete = len(incomplete) == 0

            if not migrations_complete:
                logging.debug('%d migration(s) incomplete for %s, not re-enabling', len(incomplete), svc.host)
                continue

            # For kdump hosts, wait until kdump messages stop (60s timeout)
            if 'kdump' in (getattr(svc, 'disabled_reason', '') or ''):
                hostname = _extract_hostname(svc.host)
                with service.kdump_lock:
                    last_kdump = service.kdump_hosts_timestamp.get(hostname, 0)
                time_since_kdump = time.time() - last_kdump if last_kdump > 0 else float('inf')
                if time_since_kdump < KDUMP_REENABLE_DELAY_SECONDS:
                    logging.info('%s waiting for kdump to complete (%.0fs since last message, waiting for %ds)', svc.host, time_since_kdump, KDUMP_REENABLE_DELAY_SECONDS)
                    continue
                else:
                    logging.info('%s kdump messages stopped (%.0fs since last message), proceeding with re-enable', svc.host, time_since_kdump)

            # Unset force_down if needed (allows service to report up)
            if svc.forced_down:
                _host_enable(conn, svc, reenable=True, service=service)

            # Enable disabled services only if they're now up
            if 'disabled' in svc.status:
                if svc.state == 'up':
                    _host_enable(conn, svc, reenable=False)
                    logging.info('Enabled %s (migrations complete, service is up)', svc.host)
                    _emit_k8s_event(svc.host, 'HostReenabled',
                                    'Host re-enabled after successful evacuation')
                    HOST_REENABLED_TOTAL.labels(host=svc.host).inc()
                else:
                    logging.debug('%s still down, will enable once up', svc.host)
        except Exception as e:
            logging.error('Failed to enable %s: %s', svc.host, e)

def _reconcile_orphaned_hosts(conn):
    """Reconcile hosts left in an inconsistent state after a crash.

    Finds hosts that were fenced (forced_down=True) but not yet marked with
    the evacuation disabled_reason, and sets the marker so the normal resume
    logic picks them up. Also unlocks VMs left locked by a crashed instanceha.
    """
    try:
        services = conn.services.list(binary="nova-compute")
    except Exception as e:
        logging.warning("Startup reconciliation skipped: failed to list services: %s", e)
        return

    recovered = 0
    unlocked = 0
    for svc in services:
        if not (svc.forced_down and svc.state == 'down'):
            continue

        # Mark orphaned fenced hosts for evacuation resume
        if 'disabled' not in svc.status:
            try:
                disable_reason = _make_disable_reason(DISABLED_REASON_EVACUATION, " (recovered)")
                conn.services.disable_log_reason(svc.id, disable_reason)
                logging.warning("Recovered orphaned fenced host %s — marked for evacuation resume", svc.host)
                _emit_k8s_event(svc.host, 'OrphanedHostRecovered',
                                'Recovered orphaned fenced host, marked for evacuation resume',
                                event_type='Warning')
                ORPHANED_HOST_RECOVERED_TOTAL.inc()
                recovered += 1
            except Exception as e:
                logging.error("Failed to reconcile orphaned host %s: %s", svc.host, e)

        # Unlock VMs left locked by a crashed instanceha (only our own locks)
        try:
            servers = conn.servers.list(search_opts={'host': svc.host, 'all_tenants': 1})
            for s in servers:
                if getattr(s, 'locked_reason', None) == LOCK_REASON_EVACUATION:
                    conn.servers.unlock(s.id)
                    logging.warning("Unlocked orphaned locked VM %s on host %s", s.id, svc.host)
                    unlocked += 1
        except Exception as e:
            logging.warning("Failed to check/unlock VMs on host %s: %s", svc.host, e)

    if recovered:
        logging.info("Startup reconciliation: recovered %d orphaned fenced host(s)", recovered)
    if unlocked:
        logging.info("Startup reconciliation: unlocked %d orphaned locked VM(s)", unlocked)


def _handle_poll_failure(service, consecutive_failures, poll_interval, msg, e, include_traceback=False):
    """Handle a poll cycle failure with backoff and readiness updates. Returns new consecutive_failures."""
    consecutive_failures += 1
    POLL_CONSECUTIVE_FAILURES.set(consecutive_failures)
    POLL_CYCLE_TOTAL.labels(result='error').inc()
    if consecutive_failures >= MAX_CONSECUTIVE_FAILURES_READY and service.ready:
        service.ready = False
        logging.warning("Readiness probe deactivated after %d consecutive failures", consecutive_failures)
    backoff = min(poll_interval * (2 ** consecutive_failures), MAX_NOVA_BACKOFF_SECONDS)
    logging.warning("%s (attempt %d): %s. Retrying in %ds.", msg, consecutive_failures, e, backoff)
    if include_traceback:
        logging.debug('Exception traceback:', exc_info=True)
    service.shutdown_event.wait(backoff)
    return consecutive_failures


def main():
    # Set up logging early so config loading errors are properly formatted
    logging.basicConfig(format='%(asctime)s %(levelname)s %(message)s', level=logging.INFO)

    # Initialize configuration
    try:
        config_manager = ConfigManager()
        # Reconfigure log level from config
        logging.getLogger().setLevel(config_manager.get_config_value('LOGLEVEL'))
        logging.info("Configuration loaded successfully")
    except ConfigurationError as e:
        logging.error("Configuration failed: %s", e)
        sys.exit(1)

    # Initialize service and establish connections
    service = _initialize_service(config_manager)
    conn = _establish_nova_connection(service)

    _reconcile_orphaned_hosts(conn)

    def _sigterm_handler(signum, frame):
        logging.info("SIGTERM received, finishing in-flight work before shutdown")
        service.ready = False
        service.shutdown_event.set()
        service.kdump_listener_stop_event.set()
        service.heartbeat_listener_stop_event.set()

    signal.signal(signal.SIGTERM, _sigterm_handler)

    consecutive_failures = 0

    from novaclient.exceptions import Unauthorized
    from keystoneauth1.exceptions.discovery import DiscoveryFailure

    while not service.shutdown_event.is_set():
        service.update_health_hash()
        poll_interval = service.config.get_config_value('POLL')

        try:
            if not service.k8s_api_reachable:
                logging.warning("Skipping fencing — K8s API unreachable (possible network partition)")
                service.shutdown_event.wait(poll_interval)
                continue

            services = conn.services.list(binary="nova-compute")
            if not services:
                service.shutdown_event.wait(poll_interval)
                continue

            target_date = datetime.now(timezone.utc) - timedelta(seconds=service.config.get_config_value('DELTA'))
            compute_nodes, to_resume, to_reenable = _categorize_services(services, target_date)

            compute_nodes_list = list(compute_nodes)

            _process_stale_services(conn, service, services, compute_nodes_list, to_resume)
            _process_reenabling(conn, service, to_reenable)

            if not service.ready:
                service.ready = True
                logging.info("Readiness probe active — first poll cycle completed")
            consecutive_failures = 0
            POLL_CONSECUTIVE_FAILURES.set(0)
            POLL_CYCLE_TOTAL.labels(result='success').inc()

        except (Unauthorized, DiscoveryFailure) as e:
            consecutive_failures = _handle_poll_failure(
                service, consecutive_failures, poll_interval,
                "Nova session expired or discovery failed", type(e).__name__)
            new_conn = _establish_nova_connection(service, fatal=False)
            if new_conn:
                conn = new_conn
            continue

        except Exception as e:
            consecutive_failures = _handle_poll_failure(
                service, consecutive_failures, poll_interval,
                "Failed to query Nova API", e, include_traceback=True)
            continue

        service.shutdown_event.wait(poll_interval)

    logging.info("Graceful shutdown complete")


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        logging.info("Shutting down due to keyboard interrupt")
    except Exception as e:
        logging.error('Error: %s', e)
        raise

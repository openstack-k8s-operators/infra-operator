#!/usr/libexec/platform-python -tt

import os
import sys
import time
import logging
import re
from datetime import datetime, timedelta
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
import threading
import hashlib
import json
from typing import Dict, Any, Optional, Union, List, Protocol, Tuple, Callable
from collections import defaultdict
from abc import ABC, abstractmethod

from novaclient import client
from novaclient.exceptions import Conflict, NotFound, Forbidden, Unauthorized

from keystoneauth1 import loading
from keystoneauth1 import session as ksc_session
from keystoneauth1.exceptions.discovery import DiscoveryFailure


class ConfigurationError(Exception):
    """Raised when configuration loading or validation fails."""
    pass


class NovaConnectionError(Exception):
    """Raised when Nova API connection fails."""
    pass


# Constants
CACHE_TIMEOUT_SECONDS = 300
KDUMP_CLEANUP_THRESHOLD = 100
KDUMP_CLEANUP_AGE_SECONDS = 300
MAX_EVACUATION_TIMEOUT_SECONDS = 300
INITIAL_EVACUATION_WAIT_SECONDS = 10
EVACUATION_POLL_INTERVAL_SECONDS = 5
EVACUATION_RETRY_WAIT_SECONDS = 5
MAX_EVACUATION_RETRIES = 5
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

# Disabled reason markers
DISABLED_REASON_EVACUATION = "instanceha evacuation"
DISABLED_REASON_EVACUATION_COMPLETE = "instanceha evacuation complete"
DISABLED_REASON_EVACUATION_FAILED = "evacuation FAILED"
DISABLED_REASON_KDUMP_MARKER = "(kdump)"


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


@dataclass
class EvacuationStatus:
    """Status of an ongoing server evacuation."""
    completed: bool
    error: bool


@dataclass
class FencingCredentials:
    """Credentials and connection info for fencing operations."""
    agent: str
    ipaddr: str
    login: str
    passwd: str
    ipport: str = "443"
    timeout: int = 30
    uuid: str = "System.Embedded.1"
    tls: str = "false"
    # BMH-specific fields
    token: Optional[str] = None
    namespace: Optional[str] = None
    host: Optional[str] = None


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
    'auth': re.compile(r'\bauth=[^\s)\'\"]+', re.IGNORECASE),
}


def _safe_log_exception(msg: str, e: Exception, include_traceback: bool = False) -> None:
    """Log exception without exposing secrets in messages or tracebacks."""
    safe_msg = str(e)
    for secret_word, pattern in _SECRET_PATTERNS.items():
        safe_msg = pattern.sub(f'{secret_word}=***', safe_msg)
    logging.error("%s: %s", msg, safe_msg)
    if include_traceback:
        logging.debug("Exception traceback (sanitized): %s", type(e).__name__)


# Security validation patterns
VALIDATION_PATTERNS = {
    'k8s_namespace': (r'^[a-z0-9]([-a-z0-9]*[a-z0-9])?$', 63),
    'k8s_resource': (r'^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$', 253),
    'power_action': (['on', 'off', 'status', 'reset', 'cycle', 'soft', 'On', 'ForceOff',
                      'GracefulShutdown', 'GracefulRestart', 'ForceRestart', 'Nmi',
                      'ForceOn', 'PushPowerButton'], None),
    'ip_address': ('ip', None),  # Special marker for IP address validation
    'port': ('port', None),  # Special marker for port validation
    'username': (r'^[a-zA-Z0-9_-]{1,64}$', USERNAME_MAX_LENGTH)
}

def _try_validate(validator_func: Callable[[], bool], error_msg: str, context: str, log_error: bool = True) -> bool:
    """Helper to execute validation with consistent error handling."""
    try:
        return validator_func()
    except (ValueError, AttributeError, TypeError) as e:
        if log_error:
            logging.error(f"{error_msg} for {context}: {e}")
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
            # Block localhost and link-local
            h = p.hostname
            if h and (h.lower() in ['localhost', '127.0.0.1', '::1', '0.0.0.0'] or h.startswith('169.254.')):
                logging.error(f"Blocked localhost/link-local access in {context}")
                return False
            return True
        return _try_validate(_validate_url, "Invalid URL", context, log_error=False)

    if validation_type == 'ip_address':
        return _try_validate(lambda: ipaddress.ip_address(value) and True,
                            f"Invalid IP address", context)

    if validation_type == 'port':
        def _validate_port():
            port_int = int(value)
            if 1 <= port_int <= 65535:
                return True
            logging.error(f"Port out of range for {context}: {value}")
            return False
        return _try_validate(_validate_port, "Invalid port format", context)

    # Pattern-based validation
    pattern_data = VALIDATION_PATTERNS.get(validation_type)
    if not pattern_data:
        return True  # Unknown type, skip validation

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
                      config_mgr: 'ConfigManager', **kwargs):
    """Make HTTP request with SSL configuration from config manager."""
    ssl_config = config_mgr.get_requests_ssl_config()
    request_kwargs = {'auth': auth, 'timeout': timeout, **kwargs}

    if isinstance(ssl_config, tuple):
        request_kwargs['cert'] = ssl_config
    else:
        request_kwargs['verify'] = ssl_config

    return getattr(requests, method)(url, **request_kwargs)


def _wait_for_power_off(check_func: Callable[[], Optional[str]], timeout: int,
                       host_identifier: str, expected_state: str, agent_type: str) -> bool:
    """Wait for host to power off by polling power state."""
    for _ in range(timeout):
        time.sleep(1)
        power_state = check_func()
        if power_state == expected_state:
            logging.info("%s power off successful for %s", agent_type, host_identifier)
            return True
    logging.error('Power off of %s timed out', host_identifier)
    return False


def _build_redfish_url(fencing_data: dict) -> Optional[str]:
    """Build and validate Redfish URL from fencing data.

    Args:
        fencing_data: Dictionary containing Redfish connection parameters

    Returns:
        Validated URL string or None if validation fails
    """
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
    try:
        yield
    finally:
        with service.processing_lock:
            service.hosts_processing.pop(hostname, None)
            logging.debug(f'Cleaned up processing tracking for {hostname}')


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


class ConfigManager:
    """
    Secure configuration manager for InstanceHA application.

    This class handles loading, validation, and secure access to configuration
    values with proper type checking and default values.
    """

    def __init__(self, config_path: Optional[str] = None):
        """
        Initialize the configuration manager.

        Args:
            config_path: Path to configuration file, defaults to environment variable or standard path
        """
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
        return data.get("config", {})

    def _load_clouds_config(self) -> Dict[str, Any]:
        data = self._load_yaml_file(self.clouds_path, "clouds configuration")
        if not data:
            return {}
        if "clouds" not in data:
            raise ConfigurationError(f"Missing 'clouds' key in clouds configuration at {self.clouds_path}")
        return data["clouds"]

    def _load_secure_config(self) -> Dict[str, Any]:
        data = self._load_yaml_file(self.secure_path, "secure configuration")
        if not data:
            return {}
        if "clouds" not in data:
            raise ConfigurationError(f"Missing 'clouds' key in secure configuration at {self.secure_path}")
        return data["clouds"]

    def _load_fencing_config(self) -> Dict[str, Any]:
        data = self._load_yaml_file(self.fencing_path, "fencing configuration")
        if not data:
            return {}
        if "FencingConfig" not in data:
            raise ConfigurationError(f"Missing 'FencingConfig' key in fencing configuration at {self.fencing_path}")
        return data["FencingConfig"]

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
            logging.warning(f"Configuration {key} should be integer, got {type(value).__name__}, using default: {default}")
            return default

        # Clamp value to min/max bounds
        if min_val is not None:
            int_value = max(min_val, int_value)
        if max_val is not None:
            int_value = min(max_val, int_value)
        return int_value

    def get_bool(self, key: str, default: bool = False) -> bool:
        """Get a boolean configuration value with validation."""
        value = self.config.get(key, default)

        if isinstance(value, bool):
            return value
        if isinstance(value, str):
            return value.lower() in ('true', '1', 'yes', 'on', 'enabled')

        logging.warning(f"Configuration {key} should be boolean, got {type(value).__name__}, using default: {default}")
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
        'DISABLED': ConfigItem('bool', False),
        'SSL_VERIFY': ConfigItem('bool', True),
        'FENCING_TIMEOUT': ConfigItem('int', 30, 5, 120),
        'HASH_INTERVAL': ConfigItem('int', 60, 30, 300),
    }

    def get_config_value(self, key: str) -> Union[str, int, bool]:
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

        return config_item.default

    def get_cloud_name(self) -> str:
        return os.getenv('OS_CLOUD', 'overcloud')

    def get_udp_port(self) -> int:
        return int(os.getenv('UDP_PORT', DEFAULT_UDP_PORT))

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
    """
    Main service class that encapsulates all InstanceHA functionality.

    This class eliminates global variables and provides dependency injection
    for better testability, maintainability, and architectural cleanliness.
    """

    def __init__(self, config_manager: ConfigManager, cloud_client: Optional[OpenStackClient] = None):
        """
        Initialize the InstanceHA service.

        Args:
            config_manager: Configuration manager instance
            cloud_client: Optional OpenStack client for testing (None for production)
        """
        self.config = config_manager
        self.cloud_client = cloud_client

        self._initialize_health_state()
        self._initialize_cache()
        self._initialize_threading()
        self._initialize_kdump_state()
        self._initialize_processing_state()

        logging.info("InstanceHA service initialized successfully")

    def _initialize_health_state(self) -> None:
        """Initialize health monitoring state."""
        self.current_hash = ""
        self.hash_update_successful = True
        self._last_hash_time = 0
        self._previous_hash = ""

    def _initialize_cache(self) -> None:
        """Initialize caching infrastructure and cached configuration values."""
        self._host_servers_cache = {}
        self._evacuable_flavors_cache = None
        self._evacuable_images_cache = None
        self._cache_timestamp = 0
        self._cache_lock = threading.Lock()

        # Cache frequently accessed config values
        self.evacuable_tag = self.config.get_config_value('EVACUABLE_TAG')

    def _initialize_threading(self) -> None:
        """Initialize threading infrastructure."""
        self.health_check_thread = None
        self.UDP_IP = ''  # UDP listener IP

    def _initialize_kdump_state(self) -> None:
        """Initialize kdump detection and management state."""
        self.kdump_hosts_timestamp = defaultdict(float)
        self.kdump_hosts_checking = defaultdict(float)
        self.kdump_listener_stop_event = threading.Event()
        self.kdump_fenced_hosts = set()

    def _initialize_processing_state(self) -> None:
        """Initialize host processing tracking state."""
        self.hosts_processing = defaultdict(float)
        self.processing_lock = threading.Lock()

    def update_health_hash(self, hash_interval: Optional[int] = None) -> None:
        """Update health monitoring hash for service status tracking."""
        if hash_interval is None:
            hash_interval = self.config.get_config_value('HASH_INTERVAL')

        current_timestamp = time.time()

        if current_timestamp - self._last_hash_time > hash_interval:
            new_hash = hashlib.sha256(str(current_timestamp).encode()).hexdigest()
            if new_hash == self._previous_hash:
                logging.error("Hash has not changed. Something went wrong.")
                self.hash_update_successful = False
            else:
                self.current_hash = new_hash
                self.hash_update_successful = True
                self._previous_hash = self.current_hash
                self._last_hash_time = current_timestamp

    def get_connection(self) -> Optional[OpenStackClient]:
        """
        Get cloud client connection with dependency injection support.

        Returns:
            OpenStack client connection or None if failed
        """
        if self.cloud_client:
            return self.cloud_client

        return self.create_connection()

    def create_connection(self) -> Optional[OpenStackClient]:
        """Create a new Nova connection using configuration."""
        try:
            cloud_name = self.config.get_cloud_name()
            auth = self.config.clouds[cloud_name]["auth"]
            password = self.config.secure[cloud_name]["auth"]["password"]
            region_name = self.config.clouds[cloud_name]["region_name"]

            credentials = NovaLoginCredentials(
                username=auth["username"],
                password=password,
                project_name=auth["project_name"],
                auth_url=auth["auth_url"],
                user_domain_name=auth["user_domain_name"],
                project_domain_name=auth["project_domain_name"],
                region_name=region_name
            )
            return nova_login(credentials)
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
        """
        Check if a server is evacuable based on flavor and image tags.

        Args:
            server: Server instance to check
            evac_flavors: Optional list of evacuable flavor IDs
            evac_images: Optional list of evacuable image IDs

        Returns:
            bool: True if server is evacuable, False otherwise
        """
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

    def get_evacuable_flavors(self, connection: Optional[OpenStackClient] = None):
        """
        Get list of evacuable flavor IDs with caching.

        Args:
            connection: Optional Nova connection

        Returns:
            List of evacuable flavor IDs
        """
        if not self.config.get_config_value('TAGGED_FLAVORS'):
            return []

        if connection is None:
            connection = self.get_connection()

        if connection is None:
            logging.error("No Nova connection available for flavor caching")
            return []

        # Lock granularity pattern: read check → API call outside lock → write update
        # This minimizes lock hold time and prevents blocking during slow API calls
        with self._cache_lock:
            if self._evacuable_flavors_cache is not None:
                return self._evacuable_flavors_cache

        # Perform expensive API call outside lock (no blocking)
        try:
            flavors = connection.flavors.list(is_public=None)

            cache_data = []
            for flavor in flavors:
                try:
                    if self._is_flavor_evacuable(flavor, self.evacuable_tag):
                        cache_data.append(flavor.id)
                        logging.debug("Added flavor %s to evacuable cache", flavor.id)
                except Exception as e:
                    logging.debug("Could not check keys for flavor %s: %s", flavor.id, e)

            logging.debug("Cached %d evacuable flavors from %d total flavors",
                         len(cache_data), len(flavors))
        except Exception as e:
            logging.error("Failed to get evacuable flavors: %s", e)
            logging.debug('Exception traceback:', exc_info=True)
            cache_data = []

        # Update cache with lock
        with self._cache_lock:
            self._evacuable_flavors_cache = cache_data

        return self._evacuable_flavors_cache

    def get_evacuable_images(self, connection: Optional[OpenStackClient] = None):
        """
        Get list of evacuable image IDs with caching.

        Attempts multiple methods to access image information and cache evacuable image IDs.

        Args:
            connection: Optional Nova connection

        Returns:
            List of evacuable image IDs
        """
        if not self.config.get_config_value('TAGGED_IMAGES'):
            return []

        if connection is None:
            connection = self.get_connection()

        if connection is None:
            logging.error("No Nova connection available for image caching")
            return []

        # Check cache with lock
        with self._cache_lock:
            if self._evacuable_images_cache is not None:
                return self._evacuable_images_cache

        # Image retrieval strategies (in order of preference)
        def _try_nova_glance():
            if hasattr(connection, 'glance'):
                images = connection.glance.list()
                logging.debug("Retrieved %d images via Nova glance client", len(images))
                return images
            return None

        def _try_nova_images():
            if hasattr(connection, 'images'):
                images = connection.images.list()
                logging.debug("Retrieved %d images via Nova images interface", len(images))
                return images
            return None

        def _try_separate_glance():
            from glanceclient import client as glance_client
            if hasattr(connection, 'api') and hasattr(connection.api, 'session'):
                session = connection.api.session
                region_name = connection.region_name
                glance = glance_client.Client('2', session=session, region_name=region_name)
                images = list(glance.images.list())
                logging.debug("Retrieved %d images via separate Glance client", len(images))
                return images
            return None

        retrieval_strategies = [
            ("Nova glance client", _try_nova_glance),
            ("Nova images interface", _try_nova_images),
            ("Separate Glance client", _try_separate_glance),
        ]

        # Try each strategy until one succeeds
        images = []
        for strategy_name, strategy_func in retrieval_strategies:
            try:
                result = strategy_func()
                if result:
                    images = result
                    break
            except ImportError:
                logging.debug("%s not available for import", strategy_name)
            except Exception as e:
                logging.debug("%s access failed: %s", strategy_name, e)

        # Perform expensive API call outside lock
        try:
            # Process images to find evacuable ones
            cache_data = []
            if images:
                for image in images:
                    # Check image for evacuable tags using unified method
                    if self._is_resource_evacuable(image, self.evacuable_tag, ['tags', 'metadata', 'properties']):
                        cache_data.append(image.id)

                logging.debug("Cached %d evacuable images from %d total images",
                             len(cache_data), len(images))
            else:
                # No images retrieved, fall back to per-server checking
                logging.warning("Could not retrieve images for caching. Image evacuation will use per-server checking.")

        except Exception as e:
            logging.error("Failed to get evacuable images: %s", e)
            logging.debug('Exception traceback:', exc_info=True)
            cache_data = []

        # Update cache with lock
        with self._cache_lock:
            self._evacuable_images_cache = cache_data

        return self._evacuable_images_cache

    def _get_server_image_id(self, server):
        """
        Extract image ID from server object, handling different data formats.

        Args:
            server: Server instance

        Returns:
            str: Image ID or None if not found
        """
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
                return evacuable_tag in data or any(evacuable_tag in item for item in data)
            return False
        except (AttributeError, TypeError):
            return False

    def _is_resource_evacuable(self, resource, evacuable_tag, attribute_checks):
        """
        Unified method to check if a resource (image, flavor, aggregate) is evacuable.

        Args:
            resource: The resource object to check
            evacuable_tag: The tag to look for
            attribute_checks: List of attribute names to check on the resource

        Returns:
            bool: True if resource is evacuable, False otherwise
        """
        for attr_name in attribute_checks:
            if hasattr(resource, attr_name):
                attr_value = getattr(resource, attr_name, {})
                if self._check_evacuable_tag(attr_value, evacuable_tag):
                    return True
        return False

    def _is_flavor_evacuable(self, flavor, evacuable_tag):
        """
        Check if a flavor is evacuable based on its extra specs.

        Args:
            flavor: The flavor object to check
            evacuable_tag: The tag to look for

        Returns:
            bool: True if flavor is evacuable, False otherwise
        """
        try:
            flavor_keys = flavor.get_keys()

            # Check if evacuable_tag is an exact key match or part of a key (e.g., trait:CUSTOM_HA)
            matching_key = None
            if evacuable_tag in flavor_keys:
                matching_key = evacuable_tag
            else:
                # Look for the tag in composite keys like "trait:CUSTOM_HA"
                for key in flavor_keys:
                    if evacuable_tag in key:
                        matching_key = key
                        break

            if matching_key:
                value = flavor_keys[matching_key]
                return str(value).lower() == 'true'
            return False
        except (AttributeError, KeyError, TypeError):
            return False

    def is_server_image_evacuable(self, server, connection: Optional[OpenStackClient] = None):
        """
        Check if a server's image is tagged as evacuable (fallback method).

        This method is used when pre-caching fails. It attempts to check
        individual server images for evacuable tags.

        Args:
            server: Server instance to check
            connection: Optional Nova connection

        Returns:
            bool: True if server's image is evacuable, False otherwise
        """
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

    def is_aggregate_evacuable(self, connection: OpenStackClient, host: str) -> bool:
        """
        Check if a host is part of an aggregate that has been tagged as evacuable.

        Args:
            connection: Nova connection
            host: Host name to check

        Returns:
            bool: True if host is in evacuable aggregate, False otherwise
        """
        try:
            aggregates = connection.aggregates.list()

            # Use unified resource checking logic
            return any(host in agg.hosts for agg in aggregates
                      if self._is_resource_evacuable(agg, self.evacuable_tag, ['metadata']))
        except Exception as e:
            logging.error("Failed to check aggregate evacuability for %s: %s", host, e)
            logging.debug('Exception traceback:', exc_info=True)
            return False

    def get_hosts_with_servers_cached(self, connection, services):
        """
        Efficiently cache server lists for all hosts to avoid repeated API calls.

        Args:
            connection: Nova client connection
            services: List of compute services

        Returns:
            Dict mapping host names to their server lists
        """
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
                        logging.info("Found %d servers on host %s: %s", len(servers), service.host, server_info)
                    else:
                        logging.info("No servers found on host %s", service.host)
                except Exception as e:
                    logging.warning("Failed to get servers for host %s: %s", service.host, e)
                    logging.debug('Exception traceback:', exc_info=True)
                    host_servers[service.host] = []

        return host_servers

    def filter_hosts_with_servers(self, services, host_servers_cache):
        """
        Efficiently filter out hosts that have no running servers.

        Args:
            services: List of compute services to filter
            host_servers_cache: Pre-cached mapping of host -> servers

        Returns:
            List of services that have servers running
        """
        return [
            service for service in services
            if host_servers_cache.get(service.host, [])
        ]

    def filter_hosts_with_evacuable_servers(self, services, host_servers_cache, flavors=None, images=None):
        """
        Efficiently filter hosts that have evacuable servers.

        Args:
            services: List of compute services to filter
            host_servers_cache: Pre-cached mapping of host -> servers
            flavors: Optional list of evacuable flavor IDs
            images: Optional list of evacuable image IDs

        Returns:
            List of services that have evacuable servers
        """
        if flavors is None:
            flavors = self.get_evacuable_flavors()
        if images is None:
            images = self.get_evacuable_images()

        services_with_evacuable = []

        for service in services:
            servers = host_servers_cache.get(service.host, [])

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

    def _run_health_check_server(self):
        """Simple health check server implementation."""
        from http.server import HTTPServer, BaseHTTPRequestHandler

        # Capture self reference for use in handler
        service_instance = self

        class HealthHandler(BaseHTTPRequestHandler):
            def do_GET(self):
                if service_instance.hash_update_successful:
                    self.send_response(200)
                    self.send_header("Content-type", "text/plain")
                    self.end_headers()
                    self.wfile.write(service_instance.current_hash.encode('utf-8'))
                else:
                    self.send_response(500)
                    self.send_header("Content-type", "text/plain")
                    self.end_headers()
                    self.wfile.write(b"Error: Hash not updated properly.")

        HTTPServer(('', HEALTH_CHECK_PORT), HealthHandler).serve_forever()

    def _cleanup_dict_by_condition(self, dictionary: dict, condition_func: Callable[[Any, Any], bool],
                                   log_message: Optional[str] = None) -> int:
        """Remove dictionary entries matching condition function.

        Args:
            dictionary: Dictionary to clean up
            condition_func: Function that takes (key, value) and returns True if should be removed
            log_message: Optional format string for logging removals (should contain {})

        Returns:
            Number of entries removed
        """
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

    def __init__(self, udp_ip, udp_port):
        self.udp_ip = udp_ip
        self.udp_port = udp_port
        self.socket = None

    def __enter__(self):
        self.socket = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.socket.settimeout(1.0)
        self.socket.bind((self.udp_ip, self.udp_port))
        logging.info(f'Kdump UDP listener started on {self.udp_ip}:{self.udp_port}')
        return self.socket

    def __exit__(self, exc_type, exc_val, exc_tb):
        if self.socket:
            try:
                self.socket.close()
            except (OSError, AttributeError):
                pass
        logging.info('Kdump UDP listener stopped')

def _kdump_udp_listener(service: 'InstanceHAService') -> None:
    """Background UDP listener for kdump messages."""
    udp_ip = service.UDP_IP if service.UDP_IP else '0.0.0.0'
    udp_port = service.config.get_udp_port()

    try:
        with UDPSocketManager(udp_ip, udp_port) as sock:
            while not service.kdump_listener_stop_event.is_set():
                try:
                    # recvmsg returns: (data, ancdata, msg_flags, address)
                    # ancdata and msg_flags unused, only data and address needed
                    data, _, _, address = sock.recvmsg(65535, 1024, 0)
                    logging.debug(f'Received UDP message from {address[0]}, length: {len(data)}')

                    if len(data) >= 8:
                        # Try both byte orders for magic number (fence_kdump compatibility)
                        magic_native = struct.unpack('I', data[:4])[0]
                        magic_network = struct.unpack('!I', data[:4])[0]

                        if magic_native == KDUMP_MAGIC_NUMBER or magic_network == KDUMP_MAGIC_NUMBER:
                            try:
                                hostname = _extract_hostname(socket.gethostbyaddr(address[0])[0])
                                service.kdump_hosts_timestamp[hostname] = time.time()
                                logging.debug(f'Kdump message received from host: {hostname}')

                                # Cleanup old entries periodically
                                if len(service.kdump_hosts_timestamp) > KDUMP_CLEANUP_THRESHOLD:
                                    cutoff = time.time() - KDUMP_CLEANUP_AGE_SECONDS
                                    service.kdump_hosts_timestamp = defaultdict(float,
                                        {k: v for k, v in service.kdump_hosts_timestamp.items() if v >= cutoff})
                            except Exception as e:
                                logging.debug(f'Failed to process kdump message from {address[0]}: {e}')
                        else:
                            logging.debug(f'Invalid magic number from {address[0]}: 0x{magic_native:x} / 0x{magic_network:x}')
                except socket.timeout:
                    continue
                except OSError as e:
                    if not service.kdump_listener_stop_event.is_set():
                        logging.debug(f'UDP listener socket error: {e}')
                    continue
    except Exception as e:
        logging.error(f'Kdump listener failed to start: {e}')

def _aggregate_ids(conn, service) -> List[int]:
    """Get aggregate IDs for a service's host."""
    return [ag.id for ag in conn.aggregates.list() if service.host in ag.hosts]


def _update_service_disable_reason(connection, host, service_id=None) -> None:
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

        disable_reason = f"evacuation FAILED: {datetime.now().isoformat()}"
        connection.services.disable_log_reason(service_id, disable_reason)
        logging.debug('Updated disabled reason for host %s after evacuation failure', host)
        return True
    except Exception as e:
        logging.error('Failed to update disable_reason for host %s. Error: %s', host, e)
        logging.debug('Exception traceback:', exc_info=True)
        return False


def _get_evacuable_servers(connection, host, service) -> List:
    """Get list of evacuable servers from a host."""
    images = service.get_evacuable_images(connection)
    flavors = service.get_evacuable_flavors(connection)

    servers = connection.servers.list(search_opts={'host': host, 'all_tenants': 1})
    servers = [s for s in servers if s.status in {'ACTIVE', 'ERROR', 'STOPPED'}]

    if flavors or images:
        logging.debug("Filtering images and flavors: %s %s", repr(flavors), repr(images))
        evacuables = [s for s in servers if service.is_server_evacuable(s, flavors, images)]
        logging.debug("Evacuating %s", repr(evacuables))
    else:
        logging.debug("Evacuating all images and flavors")
        evacuables = servers

    return evacuables

def _smart_evacuate(connection, evacuables, service, host, service_id, target_host=None) -> bool:
    """Execute smart evacuation with migration tracking.

    Args:
        connection: Nova client connection
        evacuables: List of servers to evacuate
        service: Service instance
        host: Source host name
        service_id: Service ID
        target_host: Optional target host for evacuation

    Returns:
        bool: True if all evacuations succeeded
    """
    if target_host:
        logging.debug("Using smart evacuation with %d workers, targeting host %s",
                     service.config.get_config_value('WORKERS'), target_host)
    else:
        logging.debug("Using smart evacuation with %d workers", service.config.get_config_value('WORKERS'))

    with concurrent.futures.ThreadPoolExecutor(max_workers=service.config.get_config_value('WORKERS')) as executor:
        future_to_server = {executor.submit(_server_evacuate_future, connection, s, target_host): s for s in evacuables}

        for future in concurrent.futures.as_completed(future_to_server):
            server = future_to_server[future]
            try:
                if not future.result():
                    logging.debug('Evacuation of %s failed', server.id)
                    _update_service_disable_reason(connection, host, service_id)
                    return False
                logging.info('%r evacuated successfully', server.id)
            except Exception as exc:
                logging.error('Evacuation generated an exception: %s', exc)
                logging.debug('Exception traceback:', exc_info=True)
                _update_service_disable_reason(connection, host, service_id)
                return False

    return True

def _traditional_evacuate(connection, evacuables, host, target_host=None) -> bool:
    """Execute traditional fire-and-forget evacuation.

    Args:
        connection: Nova client connection
        evacuables: List of servers to evacuate
        host: Source host name
        target_host: Optional target host for evacuation

    Returns:
        bool: True if all evacuations succeeded
    """
    if target_host:
        logging.debug("Using traditional evacuation approach, targeting host %s", target_host)
    else:
        logging.debug("Using traditional evacuation approach")
    all_succeeded = True

    for server in evacuables:
        logging.debug("Processing %s", server)
        if hasattr(server, 'id'):
            response = _server_evacuate(connection, server.id, target_host=target_host)
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
    """Evacuate all instances from a failed host.

    Args:
        connection: Nova client connection
        failed_service: Failed service object
        service: Service instance
        target_host: Optional target host for evacuation

    Returns:
        bool: True if evacuation succeeded
    """
    host = failed_service.host

    evacuables = _get_evacuable_servers(connection, host, service)
    if not evacuables:
        logging.info("Nothing to evacuate")
        return True

    time.sleep(service.config.get_config_value('DELAY'))

    if service.config.get_config_value('SMART_EVACUATION'):
        return _smart_evacuate(connection, evacuables, service, host, failed_service.id, target_host=target_host)
    else:
        return _traditional_evacuate(connection, evacuables, host, target_host=target_host)


def _server_evacuate(connection, server, target_host=None) -> EvacuationResult:
    """Evacuate a server instance.

    Args:
        connection: Nova client connection
        server: Server ID to evacuate
        target_host: Optional target host to evacuate to. If None, Nova scheduler chooses.

    Returns:
        EvacuationResult with evacuation status
    """
    success = False
    error_message = ""

    try:
        if target_host:
            logging.info("Evacuating instance %s to target host %s", server, target_host)
            response, _ = connection.servers.evacuate(server=server, host=target_host)
        else:
            logging.info("Evacuating instance %s", server)
            response, _ = connection.servers.evacuate(server=server)

        if response is None:
            error_message = "No response received while evacuating instance"
        elif response.status_code == 200:
            success = True
            if target_host:
                error_message = response.reason or f"Evacuation to {target_host} initiated successfully"
            else:
                error_message = response.reason or "Evacuation initiated successfully"
        else:
            error_message = response.reason or f"Evacuation failed with status {response.status_code}"
    except NotFound:
        error_message = f"Instance {server} not found"
    except Forbidden:
        error_message = f"Access denied while evacuating instance {server}"
    except Unauthorized:
        error_message = f"Authentication failed while evacuating instance {server}"
    except Exception as e:
        error_message = f"Error while evacuating instance {server}: {e}"

    return EvacuationResult(
        uuid=server,
        accepted=success,
        reason=error_message,
    )


def _server_evacuation_status(connection, server) -> EvacuationStatus:
    """Check the status of a server evacuation by querying recent migrations."""
    if not connection or not server:
        return EvacuationStatus(completed=False, error=True)

    try:
        query_time = (datetime.now() - timedelta(minutes=MIGRATION_QUERY_MINUTES)).isoformat()
        migrations = connection.migrations.list(
            instance_uuid=str(server),
            migration_type='evacuation',
            changes_since=query_time,
            limit=str(MIGRATION_QUERY_LIMIT)
        )

        if not migrations:
            return EvacuationStatus(completed=False, error=True)

        # Get status from most recent migration
        migration = migrations[0]
        status = getattr(migration, 'status', None) or getattr(migration, '_info', {}).get('status')

        return EvacuationStatus(
            completed=status in MIGRATION_STATUS_COMPLETED if status else False,
            error=status in MIGRATION_STATUS_ERROR if status else True
        )

    except Exception as e:
        logging.error("Failed to check evacuation status for %s: %s", server, e)
        return EvacuationStatus(completed=False, error=True)


def _server_evacuate_future(connection, server, target_host=None) -> bool:
    """
    Evacuate a server and monitor the evacuation process until completion.

    Args:
        connection: Nova client connection
        server: Server object to evacuate
        target_host: Optional target host to evacuate to

    Returns:
        bool: True if evacuation completed successfully, False otherwise
    """

    # Initialize variables
    error_count = 0
    start_time = time.time()

    # Validate server object
    if not hasattr(server, 'id'):
        logging.warning("Could not evacuate instance - missing server ID: %s",
                       getattr(server, 'to_dict', lambda: str(server))())
        return False

    if target_host:
        logging.info("Processing evacuation for server %s to target host %s", server.id, target_host)
    else:
        logging.info("Processing evacuation for server %s", server.id)

    try:
        # Initiate evacuation
        response = _server_evacuate(connection, server.id, target_host=target_host)

        if not response.accepted:
            logging.warning("Evacuation of %s on %s failed: %s",
                           response.uuid, server.id, response.reason)
            return False

        logging.debug("Starting evacuation of %s", response.uuid)

        # Initial wait before polling
        time.sleep(INITIAL_EVACUATION_WAIT_SECONDS)

        # Monitor evacuation progress
        while True:
            # Check for overall timeout
            if time.time() - start_time > MAX_EVACUATION_TIMEOUT_SECONDS:
                logging.error("Evacuation of %s timed out after %d seconds. Giving up.",
                             response.uuid, MAX_EVACUATION_TIMEOUT_SECONDS)
                return False

            try:
                status = _server_evacuation_status(connection, server.id)

                if status.completed:
                    logging.info("Evacuation of %s completed successfully", response.uuid)
                    return True

                if status.error:
                    error_count += 1
                    if error_count >= MAX_EVACUATION_RETRIES:
                        logging.error("Failed evacuating %s %d times. Giving up.",
                                     response.uuid, MAX_EVACUATION_RETRIES)
                        return False

                    logging.warning("Evacuation of instance %s failed %d times. Retrying...",
                                   response.uuid, error_count)
                    time.sleep(EVACUATION_RETRY_WAIT_SECONDS)
                    continue

                # Evacuation still in progress
                logging.debug("Evacuation of %s still in progress", response.uuid)
                time.sleep(EVACUATION_POLL_INTERVAL_SECONDS)

            except Exception as e:
                error_count += 1
                logging.error("Error checking evacuation status for %s: %s",
                             response.uuid, str(e))
                logging.debug('Exception traceback:', exc_info=True)

                if error_count >= MAX_EVACUATION_RETRIES:
                    logging.error("Too many errors checking evacuation status for %s. Giving up.",
                                 response.uuid)
                    return False

                logging.warning("Retrying evacuation status check for %s in %d seconds (attempt %d/%d)...",
                               response.uuid, EVACUATION_RETRY_WAIT_SECONDS, error_count, MAX_EVACUATION_RETRIES)
                time.sleep(EVACUATION_RETRY_WAIT_SECONDS)
                continue

    except Exception as e:
        logging.error("Unexpected error during evacuation of server %s: %s",
                     server.id, str(e))
        logging.debug('Exception traceback:', exc_info=True)
        return False

    return False


def nova_login(credentials: NovaLoginCredentials) -> Optional[OpenStackClient]:
    """Create and return Nova client connection."""
    try:
        loader = loading.get_plugin_loader("password")
        auth = loader.load_from_options(
            auth_url=credentials.auth_url,
            username=credentials.username,
            password=credentials.password,
            project_name=credentials.project_name,
            user_domain_name=credentials.user_domain_name,
            project_domain_name=credentials.project_domain_name,
        )

        session = ksc_session.Session(auth=auth)
        nova = client.Client("2.59", session=session, region_name=credentials.region_name)
        nova.versions.get_current()
        logging.info("Nova login successful")
        return nova
    except (DiscoveryFailure, Unauthorized) as e:
        _safe_log_exception(f"Nova login failed: {type(e).__name__}", e)
    except Exception as e:
        _safe_log_exception("Nova login failed", e, include_traceback=True)
    return None


NOVA_EXCEPTION_MESSAGES = {
    NotFound: "Resource not found",
    Conflict: "Conflicting operation",
}


def _handle_nova_exception(operation: str, service_info: str, e: Exception, is_critical: bool = True) -> bool:
    """Handle Nova API exceptions with consistent logging."""
    error_msg = NOVA_EXCEPTION_MESSAGES.get(type(e))

    if error_msg:
        logging.error("Failed to %s for %s. %s: %s", operation, service_info, error_msg, type(e).__name__)
    else:
        _safe_log_exception("Failed to %s for %s. Unexpected error" % (operation, service_info), e, include_traceback=True)

    if not is_critical:
        logging.warning("Service %s operation partially failed. Manual cleanup may be needed.", service_info)
    return False


def _host_disable(connection, service, instanceha_service=None):
    """
    Disable a compute service by forcing it down and logging the reason.

    This function performs two operations:
    1. Forces the compute service down (required for evacuation)
    2. Logs the reason for the disable operation

    Args:
        connection: Nova client connection
        service: Service object to disable
        instanceha_service: Optional InstanceHA service to check kdump state

    Returns:
        bool: True if both operations succeeded, False otherwise
    """
    # Input validation
    if not connection or not service:
        logging.error("Cannot disable service - missing connection or service object")
        return False

    if not hasattr(service, 'id') or not hasattr(service, 'host'):
        missing = 'id' if not hasattr(service, 'id') else 'host'
        logging.error("Cannot disable service - service object missing %s: %s",
                     missing, getattr(service, 'to_dict', lambda: str(service))())
        return False

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
        is_kdump = instanceha_service and _extract_hostname(service.host) in instanceha_service.kdump_fenced_hosts
        suffix = f" {DISABLED_REASON_KDUMP_MARKER}" if is_kdump else ""
        disable_reason = f"{DISABLED_REASON_EVACUATION}{suffix}: {datetime.now().isoformat()}"
        connection.services.disable_log_reason(service.id, disable_reason)
        logging.info("Successfully disabled %s with reason: %s", service_info, disable_reason)
        return True
    except Exception as e:
        return _handle_nova_exception("log disable reason", service_info, e, is_critical=False)


def _check_kdump(stale_services: List[Any], service: InstanceHAService) -> Tuple[List[Any], List[str]]:
    """Check for kdump messages. Wait KDUMP_TIMEOUT for messages; evacuate immediately once received."""
    if not stale_services:
        return [], []

    logging.info(f"Checking {len(stale_services)} hosts for kdump activity")
    kdump_fenced = []
    waiting = []
    current_time = time.time()
    kdump_timeout = service.config.get_config_value('KDUMP_TIMEOUT')

    # Kdump timeout state machine per host:
    # State 1: Fresh down host (check_start==0) → start waiting, set check_start=now
    # State 2: Waiting (check_start > 0, time < timeout) → continue waiting
    # State 3a: Kdump received (last_msg within timeout) → fenced, evacuate immediately
    # State 3b: Timeout expired (time >= timeout) → no kdump, evacuate normally
    for svc in stale_services:
        hostname = _extract_hostname(svc.host)
        last_msg = service.kdump_hosts_timestamp.get(hostname, 0)
        check_start = service.kdump_hosts_checking.get(hostname, 0)

        # State 3a: Kdump message received - host is fenced, evacuate immediately
        if last_msg > 0 and (current_time - last_msg) <= kdump_timeout:
            kdump_fenced.append(svc.host)
            service.kdump_fenced_hosts.add(hostname)
            service.kdump_hosts_checking.pop(hostname, None)
            logging.info(f'Host {svc.host} fenced via kdump - evacuating')
        # State 1: First time seeing host down - start waiting
        elif check_start == 0:
            service.kdump_hosts_checking[hostname] = current_time
            waiting.append(svc.host)
            logging.info(f'Host {svc.host} down, waiting {kdump_timeout}s for kdump')
        # State 2: Still waiting for timeout
        elif (current_time - check_start) < kdump_timeout:
            waiting.append(svc.host)
            logging.info(f'Host {svc.host} waiting ({current_time - check_start:.1f}s/{kdump_timeout}s)')
        # State 3b: Timeout expired - proceed with evacuation
        else:
            service.kdump_hosts_checking.pop(hostname, None)
            logging.info(f'Host {svc.host} timeout expired, no kdump - evacuating')

    kdump_fenced_services = [s for s in stale_services if s.host in kdump_fenced]
    to_evacuate = [s for s in stale_services if s.host not in waiting and s.host not in kdump_fenced]
    if kdump_fenced:
        logging.info(f'Total kdump-fenced: {len(kdump_fenced)}')
    return to_evacuate, kdump_fenced_services


def _host_enable(connection, nova_service, reenable: bool = False, service=None) -> bool:
    """Enable a host service, optionally unsetting force-down."""
    if reenable:
        try:
            logging.debug(f'Unsetting force-down on host {nova_service.host} after evacuation')
            connection.services.force_down(nova_service.id, False)
            logging.info(f'Unset force-down for {nova_service.host}')
            # Clean up kdump marker and tracking if present
            if 'kdump' in getattr(nova_service, 'disabled_reason', ''):
                try:
                    connection.services.disable_log_reason(nova_service.id, f"{DISABLED_REASON_EVACUATION_COMPLETE}: {datetime.now().isoformat()}")
                    if service:
                        hostname = _extract_hostname(nova_service.host)
                        service.kdump_fenced_hosts.discard(hostname)
                        service.kdump_hosts_timestamp.pop(hostname, None)
                except (AttributeError, KeyError):
                    pass
            return True
        except Exception as e:
            logging.warning(f'Could not unset force-down for {nova_service.host} as not all the migrations are complete yet. Will try again the next poll cycle.')
            logging.debug(f'Full error details for {nova_service.host}: {e}')
            return False

    # Retry logic for enabling service
    for attempt in range(MAX_ENABLE_RETRIES):
        try:
            logging.info(f'Trying to enable {nova_service.host} (attempt {attempt + 1}/{MAX_ENABLE_RETRIES})')
            connection.services.enable(nova_service.id)
            logging.info(f'Host {nova_service.host} is now enabled')
            return True
        except Exception as e:
            if attempt < MAX_ENABLE_RETRIES - 1:
                logging.warning(f'Failed to enable {nova_service.host}, retrying: {e}')
            else:
                logging.error(f'Failed to enable {nova_service.host} after {MAX_ENABLE_RETRIES} attempts. Last error: {e}')
            continue

    return False


def _get_power_state(agent_type: str, **kwargs) -> Optional[str]:
    """Unified power state retrieval interface.

    Args:
        agent_type: Type of agent ('redfish' or 'ipmi')
        **kwargs: Agent-specific parameters

    Returns:
        Power state string (uppercase) or None if failed
    """
    if agent_type == 'redfish':
        url, user, passwd, timeout, config_mgr = (
            kwargs['url'], kwargs['user'], kwargs['passwd'],
            kwargs['timeout'], kwargs['config_mgr']
        )

        if not validate_input(url, 'url', "Redfish power state check"):
            logging.error("Redfish power state check failed: invalid URL")
            return None

        try:
            response = _make_ssl_request('get', url, (user, passwd), timeout, config_mgr)
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
            env = os.environ.copy()
            env['IPMITOOL_PASSWORD'] = passwd
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
                    # Wait for server to actually power off
                    return _wait_for_power_off(
                        lambda: _get_power_state('redfish', url=url, user=user, passwd=passwd, timeout=timeout, config_mgr=config_mgr),
                        timeout, safe_url, 'OFF', 'Redfish')
                else:
                    logging.debug("Redfish reset successful: %s on %s", action, safe_url)
                    logging.info("Redfish reset successful: %s", action)
                    return True
            elif response.status_code in [400, 409]:
                # Check if server is already powered off
                power_state = _get_power_state('redfish', url=url, user=user, passwd=passwd, timeout=timeout, config_mgr=config_mgr)
                if power_state == 'OFF':
                    logging.debug("Redfish reset successful: %s on %s (already off)", action, safe_url)
                    logging.info("Redfish reset successful: %s (already off)", action)
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
                _safe_log_exception("Redfish reset failed for %s" % safe_url, e)
                return False
        except Exception as e:
            _safe_log_exception("Redfish reset failed for %s" % safe_url, e)
            return False

    return False

def _bmh_fence(token, namespace, host, action, service):
    """Fence a BareMetal Host using Kubernetes API."""
    if not all([token, namespace, host]) or action not in ['on', 'off']:
        logging.error("BMH fencing failed: invalid parameters")
        return False

    # Validate inputs to prevent SSRF attacks
    if not validate_inputs({'k8s_namespace': namespace, 'k8s_resource': host, 'power_action': action}, "BMH fencing"):
        logging.error(f"BMH fencing failed: invalid inputs")
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
            fencing_timeout = service.config.get_config_value('FENCING_TIMEOUT')
            return _bmh_wait_for_power_off(base_url, headers, cacert, host, fencing_timeout, 3, service)
        else:
            logging.info("BMH power on successful for %s", host)
            return True

    except Exception as e:
        _safe_log_exception("BMH fencing failed for %s" % host, e)
        return False


def _bmh_wait_for_power_off(get_url, headers, cacert, host, timeout, poll_interval, service):
    """
    Wait for BareMetal Host to power off by polling its status.

    Args:
        get_url: URL to get BMH status
        headers: HTTP headers for authentication
        cacert: Path to CA certificate file
        host: Host name for logging
        timeout: Maximum time to wait in seconds
        poll_interval: Time between status checks in seconds

    Returns:
        bool: True if host powered off within timeout, False otherwise
    """
    fencing_timeout = service.config.get_config_value('FENCING_TIMEOUT')

    logging.debug("Waiting up to %d seconds for %s to power off", timeout, host)

    timeout_end = time.time() + timeout

    with requests.Session() as session:
        while time.time() < timeout_end:
            time.sleep(poll_interval)

            try:
                response = session.get(get_url, headers=headers, verify=cacert, timeout=fencing_timeout)
                response.raise_for_status()

            except requests.exceptions.Timeout:
                logging.warning("Timeout checking power status for %s, continuing to wait", host)
                continue
            except requests.exceptions.HTTPError as e:
                logging.warning("HTTP error checking power status for %s, continuing to wait", host)
                continue
            except requests.exceptions.RequestException as e:
                logging.warning("Request error checking power status for %s, continuing to wait", host)
                continue

            # Parse JSON response
            try:
                status_data = response.json()
            except (json.JSONDecodeError, ValueError) as e:
                logging.warning("Invalid JSON response checking power status for %s: %s, continuing to wait", host, e)
                continue

            # Extract power status
            try:
                power_status = status_data['status']['poweredOn']
                logging.debug("Power status for %s: %s", host, power_status)

                if not power_status:
                    logging.info("BMH power off confirmed for %s", host)
                    return True

            except KeyError as e:
                logging.warning("Missing power status field for %s: %s, continuing to wait", host, e)
                continue
            except Exception as e:
                logging.warning("Error parsing power status for %s: %s, continuing to wait", host, e)
                continue

    # Timeout reached
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
    ipaddr, ipport, login, passwd = fencing_data["ipaddr"], fencing_data["ipport"], \
                                     fencing_data["login"], fencing_data["passwd"]

    env = os.environ.copy()
    env['IPMITOOL_PASSWORD'] = passwd
    cmd = ["ipmitool", "-I", "lanplus", "-H", ipaddr, "-U", login, "-E", "-p", ipport, "power", action]

    for attempt in range(MAX_FENCING_RETRIES):
        try:
            subprocess.run(cmd, timeout=timeout, env=env, capture_output=True, text=True, check=True)
            break
        except subprocess.TimeoutExpired as e:
            if attempt < MAX_FENCING_RETRIES - 1:
                logging.warning("IPMI %s timeout for %s (attempt %d/%d), retrying...",
                              action, host, attempt + 1, MAX_FENCING_RETRIES)
                time.sleep(FENCING_RETRY_DELAY_SECONDS * (attempt + 1))
            else:
                _safe_log_exception("IPMI %s failed for %s: timeout after %d attempts" %
                                  (action, host, MAX_FENCING_RETRIES), e)
                return False
        except subprocess.CalledProcessError as e:
            if attempt < MAX_FENCING_RETRIES - 1:
                logging.warning("IPMI %s failed for %s with return code %d (attempt %d/%d), retrying...",
                              action, host, e.returncode, attempt + 1, MAX_FENCING_RETRIES)
                time.sleep(FENCING_RETRY_DELAY_SECONDS * (attempt + 1))
            else:
                _safe_log_exception("IPMI %s failed for %s: command failed with return code %d after %d attempts" %
                                  (action, host, e.returncode, MAX_FENCING_RETRIES), e)
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
        if not validate_input(action, 'power_action', "host fencing"):
            logging.error(f"Invalid fence action: {action}")
            return False

        # Find matching fencing agent
        for agent_key, agent_func in FENCING_AGENTS.items():
            if agent_key in agent:
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
        logging.error(f"Invalid fence action: {action}")
        return False

    # Additional check for fence-specific actions
    if action not in ['on', 'off']:
        logging.error(f"Unsupported fence action: {action} (only 'on' and 'off' supported)")
        return False

    logging.info(f"Fencing host {host} {action}")

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
    """Establish a connection to Nova using environment configuration."""
    try:
        cloud_name = os.getenv('OS_CLOUD', 'overcloud')

        if cloud_name not in service.config.clouds or cloud_name not in service.config.secure:
            logging.error("Configuration not found for cloud: %s", cloud_name)
            return None

        auth = service.config.clouds[cloud_name]["auth"]
        secure = service.config.secure[cloud_name]["auth"]
        region_name = service.config.clouds[cloud_name]["region_name"]

        credentials = NovaLoginCredentials(
            username=auth["username"],
            password=secure["password"],
            project_name=auth["project_name"],
            auth_url=auth["auth_url"],
            user_domain_name=auth["user_domain_name"],
            project_domain_name=auth["project_domain_name"],
            region_name=region_name
        )
        conn = nova_login(credentials)

        if not conn:
            logging.error("Nova connection failed")
        return conn

    except Exception as e:
        _safe_log_exception("Failed to establish Nova connection", e)
        return None


def _enable_matching_reserved_host(conn, failed_service, reserved_hosts, service, match_type: MatchType = MatchType.AGGREGATE) -> ReservedHostResult:
    """
    Enable a reserved host that matches the failed service criteria.

    Args:
        conn: Nova client connection
        failed_service: Failed service to match
        reserved_hosts: List of available reserved hosts
        service: Service instance for configuration access
        match_type: Type of matching to use (AGGREGATE or ZONE)

    Returns:
        ReservedHostResult: Result with success flag and optional hostname
    """
    try:
        matching_hosts = []

        if match_type == MatchType.AGGREGATE:
            # Match by aggregate
            compute_aggregate_ids = _aggregate_ids(conn, failed_service)
            if not compute_aggregate_ids:
                return ReservedHostResult(success=False)

            for host in reserved_hosts:
                if (service.is_aggregate_evacuable(conn, host.host) and
                    set(_aggregate_ids(conn, host)).intersection(compute_aggregate_ids)):
                    matching_hosts.append(host)
        else:
            # Match by availability zone
            matching_hosts = [host for host in reserved_hosts
                            if host.zone == failed_service.zone]

        if not matching_hosts:
            logging.warning(f"No reserved compute found in the same {match_type.value} as {failed_service.host}")
            return ReservedHostResult(success=False)

        # Enable the first matching host
        selected_host = matching_hosts[0]
        reserved_hosts.remove(selected_host)

        if not _host_enable(conn, selected_host, reenable=False):
            logging.error(f"Failed to enable reserved host {selected_host.host}")
            return ReservedHostResult(success=False)

        logging.info(f"Enabled host {selected_host.host} from the reserved pool (same {match_type.value} as {failed_service.host})")
        return ReservedHostResult(success=True, hostname=selected_host.host)

    except Exception as e:
        logging.error(f"Error enabling reserved host by {match_type.value}: {e}")
        return ReservedHostResult(success=False)


def _manage_reserved_hosts(conn, failed_service, reserved_hosts, service) -> ReservedHostResult:
    """
    Manage reserved hosts by enabling a replacement host if available.

    Args:
        conn: Nova client connection
        failed_service: Failed service that needs replacement capacity
        reserved_hosts: List of available reserved hosts
        service: Service instance for configuration access

    Returns:
        ReservedHostResult: Result with success flag and optional hostname
    """
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
        logging.debug(f"No matching reserved host found for {failed_service.host}, continuing with evacuation")
        return ReservedHostResult(success=True)

    except Exception as e:
        logging.error(f"Error managing reserved hosts for {failed_service.host}: {e}")
        return ReservedHostResult(success=False)


def _post_evacuation_recovery(conn, failed_service, service, resume=False):
    """
    Perform post-evacuation recovery by powering on the host.

    Args:
        conn: Nova client connection
        failed_service: Service to recover
        service: InstanceHA service instance
        resume: If True, skip power-on (host was already powered on previously)

    Returns:
        bool: True if recovery completed successfully, False otherwise
    """
    hostname = _extract_hostname(failed_service.host)
    kdump_fenced = hostname in service.kdump_fenced_hosts
    leave_disabled = service.config.get_config_value('LEAVE_DISABLED')

    logging.info("Evacuation successful. Starting recovery for %s", failed_service.host)

    try:
        # Step 1: Power on the host (skip if kdump fenced or resuming)
        if resume:
            logging.debug("Skipping power on for %s (resume)", failed_service.host)
        elif kdump_fenced:
            logging.info("Skipping power on for %s (kdump fenced)", failed_service.host)
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
            new_reason = f"{DISABLED_REASON_EVACUATION_COMPLETE}{suffix}: {datetime.now().isoformat()}"
            conn.services.disable_log_reason(failed_service.id, new_reason)
            logging.debug(f"Updated disable reason for {failed_service.host} to indicate evacuation complete")
        except Exception as e:
            logging.warning(f"Failed to update disable reason for {failed_service.host}: {e}")

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

    logging.info(f"Processing service {host_name} (resume={resume})")

    with track_host_processing(service, hostname):
        try:
            # Get Nova connection first
            conn = _get_nova_connection(service)
            if not conn:
                logging.error(f"Nova connection failed for {host_name}")
                return False

            # Execute processing steps
            if not resume:
                # Fence (power off) the host before evacuation
                if not _execute_step("Fencing", _host_fence, host_name, host_name, 'off', service):
                    return False

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
            if target_host and service.config.get_config_value('FORCE_RESERVED_HOST_EVACUATION'):
                logging.info(f"Forcing evacuation to reserved host: {target_host}")
                if not _execute_step("Evacuation", _host_evacuate, host_name,
                                    conn, failed_service, service, target_host):
                    return False
            else:
                if not _execute_step("Evacuation", _host_evacuate, host_name,
                                    conn, failed_service, service):
                    return False

            if not _execute_step("Recovery", _post_evacuation_recovery, host_name,
                                conn, failed_service, service, resume):
                return False

            logging.debug(f"Service processing completed successfully for {host_name}")
            return True

        except Exception as e:
            logging.error(f"Service processing failed for {host_name}: {e}")
            return False


def _initialize_service():
    """Initialize InstanceHA service and supporting threads."""
    try:
        service = InstanceHAService(config_manager)
        logging.info("InstanceHA service initialized successfully")
    except Exception as e:
        logging.error("Failed to initialize InstanceHA service: %s", e)
        sys.exit(1)

    # Start health check server
    health_check_thread = threading.Thread(target=service.start_health_check_server)
    health_check_thread.daemon = True
    health_check_thread.start()

    # Start kdump listener if enabled
    if service.config.get_config_value('CHECK_KDUMP'):
        kdump_thread = threading.Thread(target=_kdump_udp_listener, args=(service,))
        kdump_thread.daemon = True
        kdump_thread.start()

    return service

def _establish_nova_connection(service):
    """Establish Nova connection using service configuration."""
    try:
        conn = service.create_connection()
        if conn is None:
            logging.error("Failed: Unable to connect to Nova - connection is None")
            sys.exit(1)
        return conn
    except NovaConnectionError as e:
        logging.error("Failed: Unable to connect to Nova")
        sys.exit(1)
    except Exception as e:
        _safe_log_exception("Failed: Unable to connect to Nova", e)
        sys.exit(1)

def _is_service_resume_candidate(svc) -> bool:
    """Check if a service is a candidate for resuming evacuation.

    A service qualifies if it:
    - Is forced down and down state
    - Is disabled with instanceha evacuation marker
    - Does not have failed or complete markers
    """
    if not (svc.forced_down and svc.state == 'down' and 'disabled' in svc.status):
        return False

    reason = getattr(svc, 'disabled_reason', '')
    return (DISABLED_REASON_EVACUATION in reason and
            DISABLED_REASON_EVACUATION_FAILED not in reason and
            DISABLED_REASON_EVACUATION_COMPLETE not in reason)


def _categorize_services(services: List[Any], target_date: datetime) -> tuple:
    """Categorize services into compute nodes, resume candidates, and re-enable candidates."""
    # Compute nodes needing evacuation (not disabled/forced-down, and either down or stale)
    compute_nodes = (svc for svc in services
                     if not ('disabled' in svc.status or svc.forced_down)
                     and (svc.state == 'down' or datetime.fromisoformat(svc.updated_at) < target_date))

    # Resume candidates (forced down, disabled with instanceha marker, not failed, not complete)
    resume = (svc for svc in services if _is_service_resume_candidate(svc))

    # Re-enable candidates (forced down OR disabled with instanceha complete marker, but NOT resume candidates)
    # Note: We unset force_down first to allow service to report up, then enable once up
    reenable = (svc for svc in services
                if (('enabled' in svc.status and svc.forced_down)
                   or ('disabled' in svc.status and DISABLED_REASON_EVACUATION_COMPLETE in svc.disabled_reason))
                and not _is_service_resume_candidate(svc))

    return compute_nodes, resume, reenable

def _check_critical_services(conn, services, compute_nodes):
    """
    Check if critical Nova services are operational for evacuation.

    Returns (bool, str): (can_evacuate, error_message)
    """
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
            logging.debug(f'Cleaned up {len(to_cleanup)} filtered hosts from processing tracking')

def _filter_processing_hosts(service, compute_nodes, to_resume):
    """Filter out hosts already being processed and mark new ones."""
    current_time = time.time()
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
            logging.info(f'Skipped {skipped_hosts} hosts already being processed by another poll cycle')

        # Mark hosts as being processed
        for svc in compute_nodes_filtered + to_resume_filtered:
            hostname = _extract_hostname(svc.host)
            service.hosts_processing[hostname] = current_time
            marked_hostnames.add(hostname)

    return compute_nodes_filtered, to_resume_filtered, marked_hostnames, current_time


def _prepare_evacuation_resources(conn, service, services, compute_nodes):
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
                          if 'disabled' in svc.status and 'reserved' in svc.disabled_reason]

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
        compute_nodes = _filter_by_aggregates(conn, service, compute_nodes, services)

    return compute_nodes, reserved_hosts, images, flavors


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

    if compute_nodes:
        logging.warning(f'The following computes are down: {[svc.host for svc in compute_nodes]}')

    # Prepare resources for evacuation
    compute_nodes, reserved_hosts, images, flavors = _prepare_evacuation_resources(conn, service, services, compute_nodes)

    # Check evacuation threshold
    if services and compute_nodes:
        # When TAGGED_AGGREGATES is enabled, calculate threshold against evacuable hosts only
        if service.config.get_config_value('TAGGED_AGGREGATES'):
            total_evacuable = _count_evacuable_hosts(conn, service, services)
            threshold_percent = (len(compute_nodes) / total_evacuable * 100) if total_evacuable > 0 else 0
        else:
            threshold_percent = (len(compute_nodes) / len(services)) * 100

        threshold = service.config.get_config_value('THRESHOLD')
        if threshold_percent > threshold:
            logging.error(f'Number of impacted computes ({threshold_percent:.1f}%) exceeds threshold ({threshold}%). Not evacuating.')
            _cleanup_filtered_hosts(service, marked_hostnames, set(), current_time)
            return

    # Process evacuations
    if not service.config.get_config_value('DISABLED'):
        # Check if critical services are operational
        can_evacuate, error_msg = _check_critical_services(conn, services, compute_nodes)
        if not can_evacuate:
            logging.error(f'Cannot evacuate: {error_msg}. Skipping evacuation.')
            _cleanup_filtered_hosts(service, marked_hostnames, set(), current_time)
            return

        if service.config.get_config_value('CHECK_KDUMP'):
            to_evacuate, kdump_fenced = _check_kdump(compute_nodes, service)
        else:
            to_evacuate = compute_nodes
            kdump_fenced = []

        with concurrent.futures.ThreadPoolExecutor() as executor:
            poll_interval = service.config.get_config_value('POLL')
            # Process new evacuations
            results = list(executor.map(lambda svc: process_service(svc, reserved_hosts, False, service), to_evacuate))
            if not all(results):
                logging.warning(f'Some services failed to evacuate. Retrying in {poll_interval} seconds.')
            # Process kdump-fenced hosts (skip fencing step)
            results = list(executor.map(lambda svc: process_service(svc, reserved_hosts, True, service), kdump_fenced))
            if not all(results):
                logging.warning(f'Some kdump-fenced services failed to evacuate. Retrying in {poll_interval} seconds.')
            # Process resumed evacuations
            results = list(executor.map(lambda svc: process_service(svc, reserved_hosts, True, service), to_resume))
            if not all(results):
                logging.warning(f'Some services failed to evacuate. Retrying in {poll_interval} seconds.')

        # Clean up any hosts that were marked but filtered out before processing
        # (process_service cleans up hosts it processes, so we only need to clean up filtered ones)
        final_hostnames = {_extract_hostname(svc.host) for svc in to_evacuate + kdump_fenced + to_resume}
        _cleanup_filtered_hosts(service, marked_hostnames, final_hostnames, current_time)
    else:
        logging.info('InstanceHA DISABLED is true, not evacuating')
        _cleanup_filtered_hosts(service, marked_hostnames, set(), current_time)

def _count_evacuable_hosts(conn, service, services):
    """Count total number of compute services in evacuable aggregates."""
    try:
        aggregates = conn.aggregates.list()
        evacuable_hosts = set()

        for agg in aggregates:
            if service._is_resource_evacuable(agg, service.evacuable_tag, ['metadata']):
                evacuable_hosts.update(agg.hosts)

        # Count services that are in evacuable aggregates
        return sum(1 for svc in services if svc.host in evacuable_hosts)

    except Exception as e:
        logging.warning(f"Failed to count evacuable hosts: {e}")
        return len(services)  # Fallback to all services on error


def _filter_by_aggregates(conn, service, compute_nodes, services):
    """Filter compute nodes by aggregate evacuability."""
    try:
        aggregates = conn.aggregates.list()
        evacuable_hosts = set()

        for agg in aggregates:
            if service._is_resource_evacuable(agg, service.evacuable_tag, ['metadata']):
                evacuable_hosts.update(agg.hosts)

        compute_nodes_down = list(compute_nodes)
        compute_nodes = [svc for svc in compute_nodes if svc.host in evacuable_hosts]

        down_not_tagged = [svc.host for svc in compute_nodes_down if svc not in compute_nodes]
        if down_not_tagged:
            logging.warning(f'Computes not part of evacuable aggregate: {down_not_tagged}')

    except Exception as e:
        logging.warning(f"Failed to check aggregate evacuability: {e}")

    return compute_nodes

def _process_reenabling(conn, service, to_reenable) -> None:
    """Process services that can be re-enabled."""
    # Convert generator to list for processing
    to_reenable = list(to_reenable)

    if not to_reenable:
        return

    # Filter out instanceha-evacuated services if LEAVE_DISABLED is enabled
    if service.config.get_config_value('LEAVE_DISABLED'):
        to_reenable = [svc for svc in to_reenable
                      if not ('disabled' in svc.status and 'instanceha evacuation complete' in svc.disabled_reason)]
        if not to_reenable:
            return

    logging.debug(f'Checking {len(to_reenable)} computes for re-enabling')
    force_enable = service.config.get_config_value('FORCE_ENABLE')

    for svc in to_reenable:
        try:
            # Check if migrations are complete (or force enable)
            if force_enable:
                migrations_complete = True
            else:
                query_time = (datetime.now() - timedelta(minutes=MIGRATION_QUERY_MINUTES)).isoformat()
                migrations = conn.migrations.list(source_compute=svc.host, migration_type='evacuation',
                                                 changes_since=query_time, limit=MIGRATION_QUERY_LIMIT)
                incomplete = [m for m in migrations if m.status not in MIGRATION_STATUS_COMPLETED and m.status not in MIGRATION_STATUS_ERROR]
                migrations_complete = len(incomplete) == 0

            if not migrations_complete:
                logging.debug(f'{len(incomplete)}/{len(migrations)} migration(s) incomplete for {svc.host}, not re-enabling')
                continue

            # For kdump hosts, wait until kdump messages stop (60s timeout)
            if 'kdump' in getattr(svc, 'disabled_reason', ''):
                hostname = _extract_hostname(svc.host)
                last_kdump = service.kdump_hosts_timestamp.get(hostname, 0)
                time_since_kdump = time.time() - last_kdump if last_kdump > 0 else float('inf')
                if time_since_kdump < KDUMP_REENABLE_DELAY_SECONDS:
                    logging.info(f'{svc.host} waiting for kdump to complete ({time_since_kdump:.0f}s since last message, waiting for {KDUMP_REENABLE_DELAY_SECONDS}s)')
                    continue
                else:
                    logging.info(f'{svc.host} kdump messages stopped ({time_since_kdump:.0f}s since last message), proceeding with re-enable')

            # Unset force_down if needed (allows service to report up)
            if svc.forced_down:
                _host_enable(conn, svc, reenable=True, service=service)

            # Enable disabled services only if they're now up
            if 'disabled' in svc.status:
                if svc.state == 'up':
                    _host_enable(conn, svc, reenable=False)
                    logging.info(f'Enabled {svc.host} (migrations complete, service is up)')
                else:
                    logging.debug(f'{svc.host} still down, will enable once up')
        except Exception as e:
            logging.error(f'Failed to enable {svc.host}: {e}')

def main():
    # Initialize configuration
    global config_manager
    try:
        config_manager = ConfigManager()
        logging.basicConfig(format='%(asctime)s %(levelname)s %(message)s', level=config_manager.get_config_value('LOGLEVEL'))
        logging.info("Configuration loaded successfully")
    except ConfigurationError as e:
        logging.error("Configuration failed: %s", e)
        sys.exit(1)

    # Initialize service and establish connections
    service = _initialize_service()
    conn = _establish_nova_connection(service)

    while True:
        # Update health monitoring hash
        service.update_health_hash()

        try:
            services = conn.services.list(binary="nova-compute")
            if not services:
                time.sleep(service.config.get_config_value('POLL'))
                continue

            target_date = datetime.now() - timedelta(seconds=service.config.get_config_value('DELTA'))
            compute_nodes, to_resume, to_reenable = _categorize_services(services, target_date)

            # Convert generator to list for processing
            compute_nodes_list = list(compute_nodes)

            # Process stale services for evacuation
            _process_stale_services(conn, service, services, compute_nodes_list, to_resume)

            # Process services that can be re-enabled
            _process_reenabling(conn, service, to_reenable)

        except Exception as e:
            logging.warning(f"Failed to query compute status from Nova API: {e}. Please check the Nova API availability.")
            logging.debug('Exception traceback:', exc_info=True)

        time.sleep(service.config.get_config_value('POLL'))


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        logging.info("Shutting down due to keyboard interrupt")
    except Exception as e:
        logging.error(f'Error: {e}')
        raise

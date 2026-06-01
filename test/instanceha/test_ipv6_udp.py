"""
IPv6 UDP listener tests for InstanceHA.

Tests that kdump and heartbeat UDP listeners work over IPv6:
- UDPSocketManager binds to IPv6 addresses
- Heartbeat packets received over IPv6
- Kdump packets received over IPv6
- Dual-stack (::) accepts both IPv4 and IPv6
"""

import hmac as hmac_mod
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

_TEST_HMAC_KEY = b'\x01' * 32


def _ipv6_available():
    """Check if IPv6 loopback is usable on this host."""
    try:
        s = socket.socket(socket.AF_INET6, socket.SOCK_DGRAM)
        s.bind(('::1', 0))
        s.close()
        return True
    except OSError:
        return False


def _find_free_port_v6():
    s = socket.socket(socket.AF_INET6, socket.SOCK_DGRAM)
    s.bind(('::1', 0))
    port = s.getsockname()[1]
    s.close()
    return port


def _find_free_port_v4():
    s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    s.bind(('127.0.0.1', 0))
    port = s.getsockname()[1]
    s.close()
    return port


@unittest.skipUnless(_ipv6_available(), "IPv6 loopback not available")
class TestUDPSocketManagerIPv6(unittest.TestCase):
    """Test UDPSocketManager binds and receives on IPv6."""

    def test_bind_ipv6_loopback(self):
        port = _find_free_port_v6()
        mgr = instanceha.UDPSocketManager('::1', port, label='Test')
        with mgr as sock:
            self.assertIsNotNone(sock)
            self.assertEqual(sock.family, socket.AF_INET6)

    def test_bind_ipv6_wildcard(self):
        port = _find_free_port_v6()
        mgr = instanceha.UDPSocketManager('::', port, label='Test')
        with mgr as sock:
            self.assertIsNotNone(sock)
            self.assertEqual(sock.family, socket.AF_INET6)

    def test_bind_ipv4_still_works(self):
        port = _find_free_port_v4()
        mgr = instanceha.UDPSocketManager('127.0.0.1', port, label='Test')
        with mgr as sock:
            self.assertIsNotNone(sock)
            self.assertEqual(sock.family, socket.AF_INET)


@unittest.skipUnless(_ipv6_available(), "IPv6 loopback not available")
class TestHeartbeatIPv6(unittest.TestCase):
    """Test heartbeat UDP listener receives packets over IPv6."""

    def setUp(self):
        mock_config = Mock()
        mock_config.get_config_value.return_value = 120
        self.service = instanceha.InstanceHAService(mock_config)
        self.service.heartbeat_hosts_timestamp.clear()
        self.service.heartbeat_listener_stop_event = threading.Event()
        self.service.udp_ip = '::1'
        self.service.heartbeat_hmac_keys = [_TEST_HMAC_KEY]

    def tearDown(self):
        self.service.heartbeat_listener_stop_event.set()

    def _send_heartbeat_v6(self, port, hostname):
        payload = struct.pack('!I', instanceha.HEARTBEAT_MAGIC_NUMBER)
        payload += struct.pack('!Q', int(time.time()))
        payload += hostname.encode('utf-8')
        payload += hmac_mod.new(_TEST_HMAC_KEY, payload, 'sha256').digest()
        sock = socket.socket(socket.AF_INET6, socket.SOCK_DGRAM)
        try:
            sock.sendto(payload, ('::1', port))
        finally:
            sock.close()

    def test_single_heartbeat_ipv6(self):
        """Receive a single heartbeat over IPv6 and verify hostname recorded."""
        port = _find_free_port_v6()
        listener_started = threading.Event()

        def run_listener():
            instanceha._udp_listener(
                self.service,
                port=port,
                label='Heartbeat',
                magic_numbers=instanceha.HEARTBEAT_MAGIC_NUMBER,
                min_packet_size=instanceha.HEARTBEAT_MIN_PACKET_SIZE,
                lock=self.service.heartbeat_lock,
                timestamps=self.service.heartbeat_hosts_timestamp,
                stop_event=self.service.heartbeat_listener_stop_event,
                cleanup_threshold=instanceha.HEARTBEAT_CLEANUP_THRESHOLD,
                cleanup_age_seconds=instanceha.HEARTBEAT_CLEANUP_AGE_SECONDS,
                resolve_hostname=lambda data, addr, label: instanceha._resolve_hostname_packet(data, addr, label, hmac_keys=self.service.heartbeat_hmac_keys),
                log_level=logging.DEBUG,
                on_start=listener_started.set,
            )

        t = threading.Thread(target=run_listener, daemon=True)
        t.start()
        self.assertTrue(listener_started.wait(timeout=5), "Listener failed to start")

        self._send_heartbeat_v6(port, 'compute-v6-01')

        deadline = time.time() + 5
        while time.time() < deadline:
            with self.service.heartbeat_lock:
                if 'compute-v6-01' in self.service.heartbeat_hosts_timestamp:
                    break
            time.sleep(0.05)

        self.service.heartbeat_listener_stop_event.set()
        t.join(timeout=5)

        with self.service.heartbeat_lock:
            self.assertIn('compute-v6-01', self.service.heartbeat_hosts_timestamp)

    def test_multiple_heartbeats_ipv6(self):
        """Receive heartbeats from multiple hosts over IPv6."""
        port = _find_free_port_v6()
        listener_started = threading.Event()
        hostnames = [f'compute-v6-{i:02d}' for i in range(10)]

        def run_listener():
            instanceha._udp_listener(
                self.service,
                port=port,
                label='Heartbeat',
                magic_numbers=instanceha.HEARTBEAT_MAGIC_NUMBER,
                min_packet_size=instanceha.HEARTBEAT_MIN_PACKET_SIZE,
                lock=self.service.heartbeat_lock,
                timestamps=self.service.heartbeat_hosts_timestamp,
                stop_event=self.service.heartbeat_listener_stop_event,
                cleanup_threshold=instanceha.HEARTBEAT_CLEANUP_THRESHOLD,
                cleanup_age_seconds=instanceha.HEARTBEAT_CLEANUP_AGE_SECONDS,
                resolve_hostname=lambda data, addr, label: instanceha._resolve_hostname_packet(data, addr, label, hmac_keys=self.service.heartbeat_hmac_keys),
                log_level=logging.DEBUG,
                on_start=listener_started.set,
            )

        t = threading.Thread(target=run_listener, daemon=True)
        t.start()
        self.assertTrue(listener_started.wait(timeout=5), "Listener failed to start")

        for name in hostnames:
            self._send_heartbeat_v6(port, name)

        deadline = time.time() + 5
        while time.time() < deadline:
            with self.service.heartbeat_lock:
                if len(self.service.heartbeat_hosts_timestamp) >= len(hostnames):
                    break
            time.sleep(0.05)

        self.service.heartbeat_listener_stop_event.set()
        t.join(timeout=5)

        with self.service.heartbeat_lock:
            for name in hostnames:
                self.assertIn(name, self.service.heartbeat_hosts_timestamp)

    def test_heartbeat_hbv2_ipv6(self):
        """Verify HBV2 HMAC-authenticated packets work over IPv6."""
        port = _find_free_port_v6()
        listener_started = threading.Event()

        def run_listener():
            instanceha._udp_listener(
                self.service,
                port=port,
                label='Heartbeat',
                magic_numbers=instanceha.HEARTBEAT_MAGIC_NUMBER,
                min_packet_size=instanceha.HEARTBEAT_MIN_PACKET_SIZE,
                lock=self.service.heartbeat_lock,
                timestamps=self.service.heartbeat_hosts_timestamp,
                stop_event=self.service.heartbeat_listener_stop_event,
                cleanup_threshold=instanceha.HEARTBEAT_CLEANUP_THRESHOLD,
                cleanup_age_seconds=instanceha.HEARTBEAT_CLEANUP_AGE_SECONDS,
                resolve_hostname=lambda data, addr, label: instanceha._resolve_hostname_packet(data, addr, label, hmac_keys=self.service.heartbeat_hmac_keys),
                log_level=logging.DEBUG,
                on_start=listener_started.set,
            )

        t = threading.Thread(target=run_listener, daemon=True)
        t.start()
        self.assertTrue(listener_started.wait(timeout=5), "Listener failed to start")

        self._send_heartbeat_v6(port, 'compute-hbv2')

        deadline = time.time() + 5
        while time.time() < deadline:
            with self.service.heartbeat_lock:
                if 'compute-hbv2' in self.service.heartbeat_hosts_timestamp:
                    break
            time.sleep(0.05)

        self.service.heartbeat_listener_stop_event.set()
        t.join(timeout=5)

        with self.service.heartbeat_lock:
            self.assertIn('compute-hbv2', self.service.heartbeat_hosts_timestamp)


@unittest.skipUnless(_ipv6_available(), "IPv6 loopback not available")
class TestKdumpIPv6(unittest.TestCase):
    """Test kdump UDP listener receives packets over IPv6."""

    def setUp(self):
        mock_config = Mock()
        mock_config.get_config_value.return_value = 30
        self.service = instanceha.InstanceHAService(mock_config)
        self.service.kdump_hosts_timestamp.clear()
        self.service.kdump_listener_stop_event = threading.Event()
        self.service.udp_ip = '::1'

    def tearDown(self):
        self.service.kdump_listener_stop_event.set()

    def _send_kdump_v6(self, port):
        """Send a kdump magic packet over IPv6."""
        magic = struct.pack('!I', instanceha.KDUMP_MAGIC_NUMBER)
        payload = magic + b'\x00' * 8
        sock = socket.socket(socket.AF_INET6, socket.SOCK_DGRAM)
        try:
            sock.sendto(payload, ('::1', port))
        finally:
            sock.close()

    def _mock_resolve(self, data, address, label):
        """Mock hostname resolver that returns a fixed name from the IPv6 address."""
        return 'compute-kdump-v6'

    def test_kdump_packet_ipv6(self):
        """Receive a kdump packet over IPv6 and verify timestamp recorded."""
        port = _find_free_port_v6()
        listener_started = threading.Event()

        def run_listener():
            instanceha._udp_listener(
                self.service,
                port=port,
                label='Kdump',
                magic_numbers=instanceha.KDUMP_MAGIC_NUMBER,
                min_packet_size=8,
                lock=self.service.kdump_lock,
                timestamps=self.service.kdump_hosts_timestamp,
                stop_event=self.service.kdump_listener_stop_event,
                cleanup_threshold=instanceha.KDUMP_CLEANUP_THRESHOLD,
                cleanup_age_seconds=instanceha.KDUMP_CLEANUP_AGE_SECONDS,
                resolve_hostname=self._mock_resolve,
                on_start=listener_started.set,
            )

        t = threading.Thread(target=run_listener, daemon=True)
        t.start()
        self.assertTrue(listener_started.wait(timeout=5), "Listener failed to start")

        self._send_kdump_v6(port)

        deadline = time.time() + 5
        while time.time() < deadline:
            with self.service.kdump_lock:
                if 'compute-kdump-v6' in self.service.kdump_hosts_timestamp:
                    break
            time.sleep(0.05)

        self.service.kdump_listener_stop_event.set()
        t.join(timeout=5)

        with self.service.kdump_lock:
            self.assertIn('compute-kdump-v6', self.service.kdump_hosts_timestamp)

    def test_kdump_multiple_packets_ipv6(self):
        """Multiple kdump packets over IPv6 update timestamps."""
        port = _find_free_port_v6()
        listener_started = threading.Event()
        call_count = [0]

        def counting_resolve(data, address, label):
            call_count[0] += 1
            return f'compute-kdump-{call_count[0]:02d}'

        def run_listener():
            instanceha._udp_listener(
                self.service,
                port=port,
                label='Kdump',
                magic_numbers=instanceha.KDUMP_MAGIC_NUMBER,
                min_packet_size=8,
                lock=self.service.kdump_lock,
                timestamps=self.service.kdump_hosts_timestamp,
                stop_event=self.service.kdump_listener_stop_event,
                cleanup_threshold=instanceha.KDUMP_CLEANUP_THRESHOLD,
                cleanup_age_seconds=instanceha.KDUMP_CLEANUP_AGE_SECONDS,
                resolve_hostname=counting_resolve,
                on_start=listener_started.set,
            )

        t = threading.Thread(target=run_listener, daemon=True)
        t.start()
        self.assertTrue(listener_started.wait(timeout=5), "Listener failed to start")

        for _ in range(5):
            self._send_kdump_v6(port)

        deadline = time.time() + 5
        while time.time() < deadline:
            with self.service.kdump_lock:
                if len(self.service.kdump_hosts_timestamp) >= 5:
                    break
            time.sleep(0.05)

        self.service.kdump_listener_stop_event.set()
        t.join(timeout=5)

        with self.service.kdump_lock:
            self.assertEqual(len(self.service.kdump_hosts_timestamp), 5)

    def test_kdump_invalid_magic_ignored_ipv6(self):
        """Invalid magic number packets are silently dropped over IPv6."""
        port = _find_free_port_v6()
        listener_started = threading.Event()

        def run_listener():
            instanceha._udp_listener(
                self.service,
                port=port,
                label='Kdump',
                magic_numbers=instanceha.KDUMP_MAGIC_NUMBER,
                min_packet_size=8,
                lock=self.service.kdump_lock,
                timestamps=self.service.kdump_hosts_timestamp,
                stop_event=self.service.kdump_listener_stop_event,
                cleanup_threshold=instanceha.KDUMP_CLEANUP_THRESHOLD,
                cleanup_age_seconds=instanceha.KDUMP_CLEANUP_AGE_SECONDS,
                resolve_hostname=self._mock_resolve,
                on_start=listener_started.set,
            )

        t = threading.Thread(target=run_listener, daemon=True)
        t.start()
        self.assertTrue(listener_started.wait(timeout=5), "Listener failed to start")

        bad_magic = struct.pack('!I', 0xDEADBEEF) + b'\x00' * 8
        sock = socket.socket(socket.AF_INET6, socket.SOCK_DGRAM)
        try:
            sock.sendto(bad_magic, ('::1', port))
        finally:
            sock.close()

        time.sleep(0.5)

        self.service.kdump_listener_stop_event.set()
        t.join(timeout=5)

        with self.service.kdump_lock:
            self.assertEqual(len(self.service.kdump_hosts_timestamp), 0)


@unittest.skipUnless(_ipv6_available(), "IPv6 loopback not available")
class TestDualStackListener(unittest.TestCase):
    """Test that :: wildcard accepts both IPv4 and IPv6 packets."""

    def setUp(self):
        mock_config = Mock()
        mock_config.get_config_value.return_value = 120
        self.service = instanceha.InstanceHAService(mock_config)
        self.service.heartbeat_hosts_timestamp.clear()
        self.service.heartbeat_listener_stop_event = threading.Event()
        self.service.udp_ip = ''
        self.service.heartbeat_hmac_keys = [_TEST_HMAC_KEY]

    def tearDown(self):
        self.service.heartbeat_listener_stop_event.set()

    def _build_hbv2_packet(self, hostname):
        payload = struct.pack('!I', instanceha.HEARTBEAT_MAGIC_NUMBER)
        payload += struct.pack('!Q', int(time.time()))
        payload += hostname.encode('utf-8')
        payload += hmac_mod.new(_TEST_HMAC_KEY, payload, 'sha256').digest()
        return payload

    def test_dual_stack_receives_ipv6(self):
        """Listener on :: receives IPv6 HBV2 packets."""
        port = _find_free_port_v6()
        listener_started = threading.Event()

        def run_listener():
            instanceha._udp_listener(
                self.service,
                port=port,
                label='Heartbeat',
                magic_numbers=instanceha.HEARTBEAT_MAGIC_NUMBER,
                min_packet_size=instanceha.HEARTBEAT_MIN_PACKET_SIZE,
                lock=self.service.heartbeat_lock,
                timestamps=self.service.heartbeat_hosts_timestamp,
                stop_event=self.service.heartbeat_listener_stop_event,
                cleanup_threshold=instanceha.HEARTBEAT_CLEANUP_THRESHOLD,
                cleanup_age_seconds=instanceha.HEARTBEAT_CLEANUP_AGE_SECONDS,
                resolve_hostname=lambda data, addr, label: instanceha._resolve_hostname_packet(data, addr, label, hmac_keys=self.service.heartbeat_hmac_keys),
                log_level=logging.DEBUG,
                on_start=listener_started.set,
            )

        t = threading.Thread(target=run_listener, daemon=True)
        t.start()
        self.assertTrue(listener_started.wait(timeout=5), "Listener failed to start")

        payload = self._build_hbv2_packet('dual-stack-v6')
        sock = socket.socket(socket.AF_INET6, socket.SOCK_DGRAM)
        try:
            sock.sendto(payload, ('::1', port))
        finally:
            sock.close()

        deadline = time.time() + 5
        while time.time() < deadline:
            with self.service.heartbeat_lock:
                if 'dual-stack-v6' in self.service.heartbeat_hosts_timestamp:
                    break
            time.sleep(0.05)

        self.service.heartbeat_listener_stop_event.set()
        t.join(timeout=5)

        with self.service.heartbeat_lock:
            self.assertIn('dual-stack-v6', self.service.heartbeat_hosts_timestamp)

    def test_dual_stack_receives_ipv4(self):
        """Listener on :: receives IPv4 HBV2 packets (mapped to IPv6)."""
        port = _find_free_port_v6()
        listener_started = threading.Event()

        def run_listener():
            instanceha._udp_listener(
                self.service,
                port=port,
                label='Heartbeat',
                magic_numbers=instanceha.HEARTBEAT_MAGIC_NUMBER,
                min_packet_size=instanceha.HEARTBEAT_MIN_PACKET_SIZE,
                lock=self.service.heartbeat_lock,
                timestamps=self.service.heartbeat_hosts_timestamp,
                stop_event=self.service.heartbeat_listener_stop_event,
                cleanup_threshold=instanceha.HEARTBEAT_CLEANUP_THRESHOLD,
                cleanup_age_seconds=instanceha.HEARTBEAT_CLEANUP_AGE_SECONDS,
                resolve_hostname=lambda data, addr, label: instanceha._resolve_hostname_packet(data, addr, label, hmac_keys=self.service.heartbeat_hmac_keys),
                log_level=logging.DEBUG,
                on_start=listener_started.set,
            )

        t = threading.Thread(target=run_listener, daemon=True)
        t.start()
        self.assertTrue(listener_started.wait(timeout=5), "Listener failed to start")

        payload = self._build_hbv2_packet('dual-stack-v4')
        sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        try:
            sock.sendto(payload, ('127.0.0.1', port))
        finally:
            sock.close()

        deadline = time.time() + 5
        while time.time() < deadline:
            with self.service.heartbeat_lock:
                if 'dual-stack-v4' in self.service.heartbeat_hosts_timestamp:
                    break
            time.sleep(0.05)

        self.service.heartbeat_listener_stop_event.set()
        t.join(timeout=5)

        with self.service.heartbeat_lock:
            self.assertIn('dual-stack-v4', self.service.heartbeat_hosts_timestamp)


if __name__ == '__main__':
    unittest.main()

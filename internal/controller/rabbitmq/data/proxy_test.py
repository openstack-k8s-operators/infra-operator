#!/usr/bin/env python3
"""
Integration test for AMQP durability proxy.

Tests the scenario where OpenStack services with:
  [oslo_messaging_rabbit]
  amqp_durable_queues=false
  amqp_auto_delete=false

Connect to RabbitMQ with default_queue_type=quorum through the proxy.

This simulates the exact upgrade scenario where non-durable clients
need to work with quorum queues without reconfiguration.
"""

import sys
import time
import socket
import argparse
from typing import Tuple, Optional

try:
    import pika
except ImportError:
    print("ERROR: pika library not found. Install with: pip install pika")
    sys.exit(1)


class OsloMessagingClient:
    """
    Simulates an OpenStack Oslo messaging client with amqp_durable_queues=false.

    This client declares queues and exchanges with durable=False, which would
    normally fail with quorum queues (PRECONDITION_FAILED).

    The proxy should transparently rewrite these to durable=True.
    """

    def __init__(self, host: str, port: int, use_tls: bool = False):
        self.host = host
        self.port = port
        self.use_tls = use_tls
        self.connection: Optional[pika.BlockingConnection] = None
        self.channel = None

    def connect(self) -> None:
        """Connect to RabbitMQ (through proxy)."""
        params = pika.ConnectionParameters(
            host=self.host,
            port=self.port,
            heartbeat=600,
            blocked_connection_timeout=300,
        )

        if self.use_tls:
            import ssl
            ssl_context = ssl.create_default_context()
            ssl_context.check_hostname = False
            ssl_context.verify_mode = ssl.CERT_NONE
            params.ssl_options = pika.SSLOptions(ssl_context)

        self.connection = pika.BlockingConnection(params)
        self.channel = self.connection.channel()
        print(f"✓ Connected to {self.host}:{self.port}")

    def declare_exchange(self, name: str) -> None:
        """
        Declare exchange with durable=False (Oslo messaging default).

        With quorum queues, this would fail without the proxy.
        Proxy should rewrite to durable=True.
        """
        self.channel.exchange_declare(
            exchange=name,
            exchange_type='topic',
            durable=False,  # Oslo messaging default when amqp_durable_queues=false
            auto_delete=False,
        )
        print(f"✓ Declared exchange '{name}' with durable=False")

    def declare_queue(self, name: str) -> None:
        """
        Declare queue with durable=False (Oslo messaging default).

        With quorum queues, this would fail: PRECONDITION_FAILED.
        Proxy should rewrite to durable=True + x-queue-type=quorum.
        """
        result = self.channel.queue_declare(
            queue=name,
            durable=False,  # Oslo messaging default when amqp_durable_queues=false
            exclusive=False,
            auto_delete=False,
        )
        print(f"✓ Declared queue '{name}' with durable=False")
        return result

    def bind_queue(self, queue: str, exchange: str, routing_key: str) -> None:
        """Bind queue to exchange."""
        self.channel.queue_bind(
            queue=queue,
            exchange=exchange,
            routing_key=routing_key,
        )
        print(f"✓ Bound queue '{queue}' to exchange '{exchange}' with routing key '{routing_key}'")

    def publish_message(self, exchange: str, routing_key: str, message: str) -> None:
        """Publish a message."""
        self.channel.basic_publish(
            exchange=exchange,
            routing_key=routing_key,
            body=message,
            properties=pika.BasicProperties(
                delivery_mode=2,  # Persistent
            )
        )
        print(f"✓ Published message to '{exchange}' with routing key '{routing_key}'")

    def consume_message(self, queue: str) -> Optional[str]:
        """Consume a single message from queue."""
        method, properties, body = self.channel.basic_get(queue=queue, auto_ack=True)
        if method:
            print(f"✓ Consumed message from '{queue}': {body.decode()}")
            return body.decode()
        return None

    def close(self) -> None:
        """Close connection."""
        if self.connection:
            self.connection.close()
            print("✓ Connection closed")


def wait_for_proxy(host: str, port: int, timeout: int = 30) -> bool:
    """Wait for proxy to be ready."""
    print(f"Waiting for proxy at {host}:{port}...")
    start = time.time()
    while time.time() - start < timeout:
        try:
            sock = socket.create_connection((host, port), timeout=1)
            sock.close()
            print(f"✓ Proxy is ready at {host}:{port}")
            return True
        except (socket.timeout, ConnectionRefusedError, OSError):
            time.sleep(1)
    return False


def test_oslo_messaging_scenario(host: str, port: int, use_tls: bool = False) -> Tuple[bool, str]:
    """
    Test the complete Oslo messaging scenario:
    1. Client declares non-durable exchange
    2. Client declares non-durable queue
    3. Client binds queue to exchange
    4. Client publishes message
    5. Client consumes message

    All operations should succeed despite using durable=False with quorum queues.
    """
    print("\n" + "="*60)
    print("Testing Oslo Messaging with amqp_durable_queues=false")
    print("="*60 + "\n")

    client = OsloMessagingClient(host, port, use_tls)

    try:
        # Test 1: Connect
        print("\n[Test 1] Connecting to proxy...")
        client.connect()

        # Test 2: Declare exchange (non-durable)
        print("\n[Test 2] Declaring non-durable exchange...")
        exchange_name = "test_exchange_oslo"
        client.declare_exchange(exchange_name)

        # Test 3: Declare queue (non-durable)
        print("\n[Test 3] Declaring non-durable queue...")
        queue_name = "test_queue_oslo"
        client.declare_queue(queue_name)

        # Test 4: Bind queue
        print("\n[Test 4] Binding queue to exchange...")
        client.bind_queue(queue_name, exchange_name, "test.routing.key")

        # Test 5: Publish message
        print("\n[Test 5] Publishing message...")
        client.publish_message(exchange_name, "test.routing.key", "Hello from Oslo!")

        # Test 6: Consume message
        print("\n[Test 6] Consuming message...")
        time.sleep(0.5)  # Give message time to route
        message = client.consume_message(queue_name)
        if message != "Hello from Oslo!":
            return False, f"Message mismatch: expected 'Hello from Oslo!', got '{message}'"

        # Test 7: Clean up
        print("\n[Test 7] Cleaning up...")
        client.channel.queue_delete(queue_name)
        client.channel.exchange_delete(exchange_name)
        print("✓ Cleaned up test resources")

        client.close()

        print("\n" + "="*60)
        print("✅ ALL TESTS PASSED")
        print("="*60)
        print("\nThe proxy successfully rewrote durable=False to durable=True,")
        print("allowing Oslo messaging client to work with quorum queues!")
        print()

        return True, "All tests passed"

    except pika.exceptions.ChannelClosedByBroker as e:
        error_msg = f"Channel closed by broker: {e}\nThis likely means the proxy is NOT rewriting frames correctly."
        if "PRECONDITION_FAILED" in str(e):
            error_msg += "\n\nPRECONDITION_FAILED error indicates the queue/exchange durability mismatch."
            error_msg += "\nThe proxy should have rewritten durable=False to durable=True."
        return False, error_msg

    except Exception as e:
        return False, f"Test failed with error: {e}"

    finally:
        if client.connection and client.connection.is_open:
            client.connection.close()


def main():
    parser = argparse.ArgumentParser(
        description="Test AMQP proxy with Oslo messaging scenario",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog="""
Examples:
  # Test proxy locally
  python proxy_test.py --host localhost --port 5672

  # Test with TLS
  python proxy_test.py --host localhost --port 5672 --tls

  # Test in Kubernetes
  kubectl run -it --rm proxy-test --image=python:3.11 -- bash
  pip install pika
  python proxy_test.py --host rabbitmq.openstack.svc --port 5672
        """
    )
    parser.add_argument('--host', default='localhost', help='Proxy host (default: localhost)')
    parser.add_argument('--port', type=int, default=5672, help='Proxy port (default: 5672)')
    parser.add_argument('--tls', action='store_true', help='Use TLS connection')
    parser.add_argument('--no-wait', action='store_true', help='Do not wait for proxy to be ready')

    args = parser.parse_args()

    # Wait for proxy unless --no-wait
    if not args.no_wait:
        if not wait_for_proxy(args.host, args.port):
            print(f"ERROR: Proxy not available at {args.host}:{args.port}")
            sys.exit(1)

    # Run test
    success, message = test_oslo_messaging_scenario(args.host, args.port, args.tls)

    if success:
        print(f"\n✅ SUCCESS: {message}")
        sys.exit(0)
    else:
        print(f"\n❌ FAILURE: {message}")
        sys.exit(1)


if __name__ == '__main__':
    main()

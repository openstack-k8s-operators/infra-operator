#!/usr/bin/env python3
"""
RabbitMQ Durability Proxy

Transparent proxy that rewrites AMQP queue.declare and exchange.declare
frames to force durable=True, allowing non-durable clients to work with
durable quorum queues.

This solves the problem of upgrading to RabbitMQ 4.2 with quorum queues
without reconfiguring OpenStack dataplane clients.

Usage:
    python proxy.py --backend rabbitmq-green:5672 --listen 0.0.0.0:5672

Author: OpenStack Infrastructure Operator Team
License: Apache 2.0
"""

import asyncio
import argparse
import logging
import struct
import sys
import ssl
from typing import Optional, Tuple
from dataclasses import dataclass
from pathlib import Path

logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s - %(name)s - %(levelname)s - %(message)s'
)
logger = logging.getLogger(__name__)


@dataclass
class AMQPFrame:
    """Represents an AMQP frame"""
    frame_type: int
    channel: int
    payload: bytes

    @classmethod
    def parse(cls, data: bytes) -> Optional['AMQPFrame']:
        """Parse AMQP frame from bytes"""
        if len(data) < 8:
            return None

        frame_type = data[0]
        channel = struct.unpack('!H', data[1:3])[0]
        size = struct.unpack('!I', data[3:7])[0]

        if len(data) < 8 + size:
            return None

        payload = data[7:7+size]
        frame_end = data[7+size]

        if frame_end != 0xCE:
            logger.error(f"Invalid frame end marker: {frame_end:#x}")
            return None

        return cls(frame_type, channel, payload)

    def to_bytes(self) -> bytes:
        """Serialize frame to bytes"""
        header = struct.pack('!BHI',
            self.frame_type,
            self.channel,
            len(self.payload)
        )
        return header + self.payload + b'\xce'

    @property
    def size(self) -> int:
        """Total frame size in bytes"""
        return 8 + len(self.payload)


class AMQPMethodParser:
    """Parser for AMQP method frames"""

    @staticmethod
    def parse_shortstr(data: bytes, offset: int) -> Tuple[str, int]:
        """Parse AMQP short string (length-prefixed)"""
        if offset >= len(data):
            return "", offset
        length = data[offset]
        offset += 1
        if offset + length > len(data):
            return "", offset
        string = data[offset:offset+length].decode('utf-8', errors='ignore')
        offset += length
        return string, offset

    @staticmethod
    def parse_table(data: bytes, offset: int) -> Tuple[dict, int]:
        """Parse AMQP field table"""
        if offset + 4 > len(data):
            return {}, offset

        table_size = struct.unpack('!I', data[offset:offset+4])[0]
        offset += 4

        table_end = offset + table_size
        table = {}

        while offset < table_end and offset < len(data):
            # Parse key (short string)
            key, offset = AMQPMethodParser.parse_shortstr(data, offset)
            if not key or offset >= len(data):
                break

            # Parse value type
            value_type = chr(data[offset])
            offset += 1

            # Parse value based on type
            if value_type == 't':  # Boolean
                value = bool(data[offset])
                offset += 1
            elif value_type == 'b':  # Signed 8-bit
                value = struct.unpack('!b', data[offset:offset+1])[0]
                offset += 1
            elif value_type == 's':  # Signed 16-bit
                value = struct.unpack('!h', data[offset:offset+2])[0]
                offset += 2
            elif value_type == 'I':  # Signed 32-bit
                value = struct.unpack('!i', data[offset:offset+4])[0]
                offset += 4
            elif value_type == 'l':  # Signed 64-bit
                value = struct.unpack('!q', data[offset:offset+8])[0]
                offset += 8
            elif value_type == 'S':  # Long string
                str_len = struct.unpack('!I', data[offset:offset+4])[0]
                offset += 4
                value = data[offset:offset+str_len].decode('utf-8', errors='ignore')
                offset += str_len
            elif value_type == 'x':  # Byte array
                arr_len = struct.unpack('!I', data[offset:offset+4])[0]
                offset += 4
                value = data[offset:offset+arr_len]
                offset += arr_len
            else:
                # Unknown type, skip
                logger.warning(f"Unknown table value type: {value_type}")
                break

            table[key] = value

        return table, offset

    @staticmethod
    def encode_shortstr(s: str) -> bytes:
        """Encode AMQP short string"""
        encoded = s.encode('utf-8')
        if len(encoded) > 255:
            encoded = encoded[:255]
        return bytes([len(encoded)]) + encoded

    @staticmethod
    def encode_table(table: dict) -> bytes:
        """Encode AMQP field table"""
        if not table:
            return struct.pack('!I', 0)

        table_bytes = b''

        for key, value in table.items():
            # Encode key
            table_bytes += AMQPMethodParser.encode_shortstr(key)

            # Encode value with type indicator
            if isinstance(value, bool):
                table_bytes += b't' + bytes([1 if value else 0])
            elif isinstance(value, int):
                if -128 <= value <= 127:
                    table_bytes += b'b' + struct.pack('!b', value)
                elif -32768 <= value <= 32767:
                    table_bytes += b's' + struct.pack('!h', value)
                elif -2147483648 <= value <= 2147483647:
                    table_bytes += b'I' + struct.pack('!i', value)
                else:
                    table_bytes += b'l' + struct.pack('!q', value)
            elif isinstance(value, str):
                encoded = value.encode('utf-8')
                table_bytes += b'S' + struct.pack('!I', len(encoded)) + encoded
            elif isinstance(value, bytes):
                table_bytes += b'x' + struct.pack('!I', len(value)) + value
            else:
                logger.warning(f"Unknown table value type for {key}: {type(value)}")
                continue

        return struct.pack('!I', len(table_bytes)) + table_bytes


class QueueDeclareRewriter:
    """Rewrites queue.declare frames to force durable=True"""

    @staticmethod
    def should_force_durable(queue_name: str) -> bool:
        """Determine if queue should be forced to durable"""
        # Don't modify reply queues (temporary, exclusive)
        if queue_name.startswith('reply_'):
            return False

        # Don't modify auto-generated queues
        if queue_name.startswith('amq.gen-'):
            return False

        # Don't modify system queues
        if queue_name.startswith('amq.'):
            return False

        # Force durable for all server queues
        return True

    @staticmethod
    def rewrite(payload: bytes) -> Optional[bytes]:
        """
        Rewrite queue.declare payload to force durable=True

        Queue.Declare frame structure:
        - class_id (2 bytes): 50
        - method_id (2 bytes): 10
        - reserved (2 bytes)
        - queue (shortstr)
        - flags (1 byte): bit 0=passive, bit 1=durable, bit 2=exclusive, bit 3=auto-delete, bit 4=no-wait
        - arguments (table)
        """
        if len(payload) < 4:
            return None

        offset = 0

        # Parse class_id and method_id
        class_id = struct.unpack('!H', payload[offset:offset+2])[0]
        offset += 2
        method_id = struct.unpack('!H', payload[offset:offset+2])[0]
        offset += 2

        if class_id != 50 or method_id != 10:
            return None

        # Parse reserved
        reserved = struct.unpack('!H', payload[offset:offset+2])[0]
        offset += 2

        # Parse queue name
        queue_name, offset = AMQPMethodParser.parse_shortstr(payload, offset)

        if not QueueDeclareRewriter.should_force_durable(queue_name):
            logger.debug(f"Not forcing durable for queue: {queue_name}")
            return None

        # Parse flags
        if offset >= len(payload):
            return None
        flags = payload[offset]
        flags_offset = offset
        offset += 1

        passive = bool(flags & 0x01)
        durable = bool(flags & 0x02)
        exclusive = bool(flags & 0x04)
        auto_delete = bool(flags & 0x08)
        no_wait = bool(flags & 0x10)

        # If already durable, no need to rewrite
        if durable:
            logger.debug(f"Queue {queue_name} already durable")
            return None

        # Parse arguments
        arguments, offset = AMQPMethodParser.parse_table(payload, offset)

        logger.info(f"Rewriting queue.declare: {queue_name} durable=False → durable=True, "
                   f"exclusive={exclusive}, auto_delete={auto_delete}")

        # Rebuild payload with durable=True
        new_flags = flags | 0x02  # Set durable bit

        # If forcing durable, also ensure it's not exclusive
        # (quorum queues can't be exclusive)
        if exclusive:
            logger.info(f"Removing exclusive flag from {queue_name} (incompatible with quorum)")
            new_flags = new_flags & ~0x04  # Clear exclusive bit

        # Build new payload
        new_payload = bytearray(payload)
        new_payload[flags_offset] = new_flags

        # Add x-queue-type: quorum if not present
        if 'x-queue-type' not in arguments:
            arguments['x-queue-type'] = 'quorum'
            logger.info(f"Adding x-queue-type=quorum to {queue_name}")

            # Need to rebuild the entire payload to include new arguments
            new_payload = (
                struct.pack('!HH', class_id, method_id) +
                struct.pack('!H', reserved) +
                AMQPMethodParser.encode_shortstr(queue_name) +
                bytes([new_flags]) +
                AMQPMethodParser.encode_table(arguments)
            )

        return bytes(new_payload)


class ExchangeDeclareRewriter:
    """Rewrites exchange.declare frames to force durable=True"""

    @staticmethod
    def should_force_durable(exchange_name: str) -> bool:
        """Determine if exchange should be forced to durable"""
        # Default exchange (empty name)
        if exchange_name == '':
            return False

        # System exchanges
        if exchange_name.startswith('amq.'):
            return False

        # Force durable for all user exchanges
        return True

    @staticmethod
    def rewrite(payload: bytes) -> Optional[bytes]:
        """
        Rewrite exchange.declare payload to force durable=True

        Exchange.Declare frame structure:
        - class_id (2 bytes): 40
        - method_id (2 bytes): 10
        - reserved (2 bytes)
        - exchange (shortstr)
        - type (shortstr)
        - flags (1 byte): bit 0=passive, bit 1=durable, bit 2=auto-delete, bit 3=internal, bit 4=no-wait
        - arguments (table)
        """
        if len(payload) < 4:
            return None

        offset = 0

        # Parse class_id and method_id
        class_id = struct.unpack('!H', payload[offset:offset+2])[0]
        offset += 2
        method_id = struct.unpack('!H', payload[offset:offset+2])[0]
        offset += 2

        if class_id != 40 or method_id != 10:
            return None

        # Parse reserved
        reserved = struct.unpack('!H', payload[offset:offset+2])[0]
        offset += 2

        # Parse exchange name
        exchange_name, offset = AMQPMethodParser.parse_shortstr(payload, offset)

        if not ExchangeDeclareRewriter.should_force_durable(exchange_name):
            logger.debug(f"Not forcing durable for exchange: {exchange_name}")
            return None

        # Parse exchange type
        exchange_type, offset = AMQPMethodParser.parse_shortstr(payload, offset)

        # Parse flags
        if offset >= len(payload):
            return None
        flags = payload[offset]
        flags_offset = offset
        offset += 1

        passive = bool(flags & 0x01)
        durable = bool(flags & 0x02)
        auto_delete = bool(flags & 0x04)
        internal = bool(flags & 0x08)
        no_wait = bool(flags & 0x10)

        # If already durable, no need to rewrite
        if durable:
            logger.debug(f"Exchange {exchange_name} already durable")
            return None

        logger.info(f"Rewriting exchange.declare: {exchange_name} ({exchange_type}) "
                   f"durable=False → durable=True")

        # Rebuild payload with durable=True
        new_flags = flags | 0x02  # Set durable bit

        # Build new payload
        new_payload = bytearray(payload)
        new_payload[flags_offset] = new_flags

        return bytes(new_payload)


class DurabilityProxy:
    """Main proxy class that handles connections"""

    def __init__(self, backend_host: str, backend_port: int,
                 tls_cert: Optional[str] = None,
                 tls_key: Optional[str] = None,
                 tls_ca: Optional[str] = None,
                 backend_tls: bool = False):
        self.backend_host = backend_host
        self.backend_port = backend_port
        self.tls_cert = tls_cert
        self.tls_key = tls_key
        self.tls_ca = tls_ca
        self.backend_tls = backend_tls
        self.stats = {
            'connections': 0,
            'queue_rewrites': 0,
            'exchange_rewrites': 0,
            'bytes_forwarded': 0
        }

        # Create SSL context if TLS is enabled
        self.ssl_context = None
        if tls_cert and tls_key:
            self.ssl_context = self._create_ssl_context()

    def _create_ssl_context(self) -> ssl.SSLContext:
        """Create SSL context for TLS support"""
        ctx = ssl.create_default_context(ssl.Purpose.CLIENT_AUTH)
        ctx.load_cert_chain(certfile=self.tls_cert, keyfile=self.tls_key)

        if self.tls_ca:
            ctx.load_verify_locations(cafile=self.tls_ca)
            ctx.verify_mode = ssl.CERT_REQUIRED
        else:
            ctx.check_hostname = False
            ctx.verify_mode = ssl.CERT_NONE

        logger.info(f"TLS enabled with cert={self.tls_cert}, key={self.tls_key}, ca={self.tls_ca}")
        return ctx

    async def handle_client(self, client_reader: asyncio.StreamReader,
                           client_writer: asyncio.StreamWriter):
        """Handle a client connection"""
        client_addr = client_writer.get_extra_info('peername')
        self.stats['connections'] += 1

        # Check if connection is over TLS
        ssl_obj = client_writer.get_extra_info('ssl_object')
        tls_status = "TLS" if ssl_obj else "plain"
        logger.info(f"Client connected from {client_addr} ({tls_status}) (total: {self.stats['connections']})")

        try:
            # Connect to backend RabbitMQ
            # If backend_tls is True, use TLS; otherwise plain connection (for localhost)
            backend_ssl = None
            if self.backend_tls:
                backend_ssl = ssl.create_default_context()
                backend_ssl.check_hostname = False
                backend_ssl.verify_mode = ssl.CERT_NONE

            backend_reader, backend_writer = await asyncio.open_connection(
                self.backend_host, self.backend_port, ssl=backend_ssl
            )
            backend_status = "TLS" if self.backend_tls else "plain"
            logger.info(f"Connected to backend {self.backend_host}:{self.backend_port} ({backend_status})")

            # Bidirectional forwarding
            await asyncio.gather(
                self.forward_client_to_server(client_reader, backend_writer),
                self.forward_server_to_client(backend_reader, client_writer),
                return_exceptions=True
            )

        except Exception as e:
            logger.error(f"Error handling client {client_addr}: {e}")
        finally:
            client_writer.close()
            await client_writer.wait_closed()
            logger.info(f"Client disconnected from {client_addr}")

    async def forward_client_to_server(self, client_reader: asyncio.StreamReader,
                                      server_writer: asyncio.StreamWriter):
        """Forward client → server with frame rewriting"""
        buffer = b''

        try:
            while True:
                # Read data from client
                data = await client_reader.read(8192)
                if not data:
                    break

                buffer += data
                self.stats['bytes_forwarded'] += len(data)

                # Process complete frames
                while len(buffer) >= 8:
                    # Check for AMQP protocol header
                    if buffer[:4] == b'AMQP':
                        # Protocol header, forward as-is
                        server_writer.write(buffer[:8])
                        await server_writer.drain()
                        buffer = buffer[8:]
                        continue

                    # Try to parse AMQP frame
                    frame = AMQPFrame.parse(buffer)
                    if frame is None:
                        # Incomplete frame, wait for more data
                        break

                    # Rewrite if needed
                    modified_payload = self.rewrite_client_frame(frame)

                    if modified_payload is not None:
                        # Send modified frame
                        modified_frame = AMQPFrame(
                            frame.frame_type,
                            frame.channel,
                            modified_payload
                        )
                        server_writer.write(modified_frame.to_bytes())
                    else:
                        # Send original frame
                        server_writer.write(frame.to_bytes())

                    await server_writer.drain()

                    # Remove processed frame from buffer
                    buffer = buffer[frame.size:]

        except asyncio.CancelledError:
            raise
        except Exception as e:
            logger.error(f"Error in client→server forwarding: {e}")
        finally:
            server_writer.close()
            await server_writer.wait_closed()

    async def forward_server_to_client(self, server_reader: asyncio.StreamReader,
                                      client_writer: asyncio.StreamWriter):
        """Forward server → client (no rewriting needed)"""
        try:
            while True:
                data = await server_reader.read(8192)
                if not data:
                    break

                client_writer.write(data)
                await client_writer.drain()
                self.stats['bytes_forwarded'] += len(data)

        except asyncio.CancelledError:
            raise
        except Exception as e:
            logger.error(f"Error in server→client forwarding: {e}")
        finally:
            client_writer.close()
            await client_writer.wait_closed()

    def rewrite_client_frame(self, frame: AMQPFrame) -> Optional[bytes]:
        """Rewrite client frame if needed"""

        # Only rewrite method frames
        if frame.frame_type != 1:  # 1 = METHOD frame
            return None

        if len(frame.payload) < 4:
            return None

        # Parse class_id and method_id
        class_id = struct.unpack('!H', frame.payload[0:2])[0]
        method_id = struct.unpack('!H', frame.payload[2:4])[0]

        # Queue.Declare (class=50, method=10)
        if class_id == 50 and method_id == 10:
            modified = QueueDeclareRewriter.rewrite(frame.payload)
            if modified:
                self.stats['queue_rewrites'] += 1
            return modified

        # Exchange.Declare (class=40, method=10)
        if class_id == 40 and method_id == 10:
            modified = ExchangeDeclareRewriter.rewrite(frame.payload)
            if modified:
                self.stats['exchange_rewrites'] += 1
            return modified

        return None

    def print_stats(self):
        """Print proxy statistics"""
        logger.info("=== Proxy Statistics ===")
        logger.info(f"Total connections: {self.stats['connections']}")
        logger.info(f"Queue rewrites: {self.stats['queue_rewrites']}")
        logger.info(f"Exchange rewrites: {self.stats['exchange_rewrites']}")
        logger.info(f"Bytes forwarded: {self.stats['bytes_forwarded']:,}")


async def periodic_stats(proxy: DurabilityProxy, interval: int = 60):
    """Print stats periodically"""
    while True:
        await asyncio.sleep(interval)
        proxy.print_stats()


async def main():
    parser = argparse.ArgumentParser(
        description='RabbitMQ Durability Proxy - Forces durable=True for quorum queues'
    )
    parser.add_argument(
        '--backend',
        default='rabbitmq-service:5672',
        help='Backend RabbitMQ address (host:port)'
    )
    parser.add_argument(
        '--listen',
        default='0.0.0.0:5672',
        help='Listen address (host:port)'
    )
    parser.add_argument(
        '--tls-cert',
        help='Path to TLS certificate file (enables TLS on listen port)'
    )
    parser.add_argument(
        '--tls-key',
        help='Path to TLS private key file'
    )
    parser.add_argument(
        '--tls-ca',
        help='Path to TLS CA certificate file (for client verification)'
    )
    parser.add_argument(
        '--backend-tls',
        action='store_true',
        help='Use TLS when connecting to backend'
    )
    parser.add_argument(
        '--stats-interval',
        type=int,
        default=60,
        help='Statistics print interval in seconds'
    )
    parser.add_argument(
        '--log-level',
        default='INFO',
        choices=['DEBUG', 'INFO', 'WARNING', 'ERROR'],
        help='Log level'
    )

    args = parser.parse_args()

    # Set log level
    logging.getLogger().setLevel(getattr(logging, args.log_level))

    # Parse backend address
    backend_parts = args.backend.split(':')
    backend_host = backend_parts[0]
    backend_port = int(backend_parts[1]) if len(backend_parts) > 1 else 5672

    # Parse listen address
    listen_parts = args.listen.split(':')
    listen_host = listen_parts[0]
    listen_port = int(listen_parts[1]) if len(listen_parts) > 1 else 5672

    # Validate TLS arguments
    if (args.tls_cert and not args.tls_key) or (args.tls_key and not args.tls_cert):
        logger.error("Both --tls-cert and --tls-key must be provided together")
        sys.exit(1)

    # Create proxy
    proxy = DurabilityProxy(
        backend_host,
        backend_port,
        tls_cert=args.tls_cert,
        tls_key=args.tls_key,
        tls_ca=args.tls_ca,
        backend_tls=args.backend_tls
    )

    # Start server
    server = await asyncio.start_server(
        proxy.handle_client,
        listen_host,
        listen_port,
        ssl=proxy.ssl_context
    )

    addrs = ', '.join(str(sock.getsockname()) for sock in server.sockets)
    logger.info(f"Proxy listening on {addrs}")
    logger.info(f"Backend: {backend_host}:{backend_port}")
    logger.info("Ready to accept connections")

    # Start periodic stats
    stats_task = asyncio.create_task(periodic_stats(proxy, args.stats_interval))

    try:
        async with server:
            await server.serve_forever()
    except KeyboardInterrupt:
        logger.info("Shutting down...")
    finally:
        stats_task.cancel()
        proxy.print_stats()


if __name__ == '__main__':
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        logger.info("Interrupted by user")
        sys.exit(0)

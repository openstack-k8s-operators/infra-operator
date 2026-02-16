# RabbitMQ Controller Data Files

This directory contains embedded data files used by the RabbitMQ controller.

## Files

### proxy.py

The AMQP durability proxy script that is deployed as a sidecar container during RabbitMQ upgrades and queue type migrations.

**Purpose**: Transparently rewrites AMQP frames to force `durable=True`, enabling non-durable clients (OpenStack services with `amqp_durable_queues=false`) to work with durable quorum queues.

**Deployment**: Embedded at build time using Go's `embed` directive in `proxy.go` and deployed as a ConfigMap to RabbitMQ pods.

**Usage**: See [PROXY_INTEGRATION.md](../../../PROXY_INTEGRATION.md) for complete documentation.

### proxy_test.py

Integration test that simulates an Oslo messaging client (with `amqp_durable_queues=false`) connecting through the proxy to RabbitMQ with quorum queues.

**Usage**: See [TESTING.md](TESTING.md) for how to run the tests.

## Modifying the Proxy

When updating the proxy script:

1. Edit `internal/controller/rabbitmq/data/proxy.py`
2. Rebuild the operator: `make`
3. Run tests: `make test`
4. Run integration test: `python3 proxy_test.py` (see [TESTING.md](TESTING.md))

The proxy is embedded at compile time, so changes require a rebuild.

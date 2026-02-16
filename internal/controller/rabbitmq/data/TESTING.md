# Testing the AMQP Proxy

## Overview

Two types of tests verify the proxy works correctly with Oslo messaging clients:

1. **Go Unit Tests** (`test/functional/rabbitmq_proxy_test.go`) - Verify proxy sidecar integration
2. **Python Integration Test** (`proxy_test.py`) - Verify actual AMQP protocol rewriting

## Go Unit Tests

These tests verify that the operator correctly:
- Adds proxy sidecar during upgrades
- Configures TLS properly
- Removes proxy after client reconfiguration
- Respects enable-proxy annotation

### Running Go Tests

```bash
# Run all tests
make test

# Run only proxy tests
cd test/functional
ginkgo -focus="RabbitMQ Proxy"
```

### What They Test

- ✅ Proxy sidecar is added during upgrade phase
- ✅ Proxy container has correct image and command
- ✅ TLS certificates are mounted properly
- ✅ Backend port is configured (localhost:5673)
- ✅ ConfigMap is created with proxy script
- ✅ Proxy is removed when `clients-reconfigured` annotation is set
- ✅ Manual activation via `enable-proxy` annotation

## Python Integration Test

This test simulates the actual Oslo messaging scenario:

```python
# Client configuration (OpenStack default)
amqp_durable_queues=false
amqp_auto_delete=false
```

Connecting to RabbitMQ with `default_queue_type=quorum` through the proxy.

### Prerequisites

```bash
# Install pika library
pip install pika

# Or use a container
podman run -it --rm python:3.11 bash
pip install pika
```

### Running the Integration Test

#### Local Testing (with local RabbitMQ)

```bash
# Terminal 1: Start RabbitMQ with quorum queues
podman run -d --name rabbitmq \
  -p 5673:5672 \
  -p 15672:15672 \
  -e RABBITMQ_SERVER_ADDITIONAL_ERL_ARGS="-rabbit default_queue_type quorum" \
  rabbitmq:4.0-management

# Terminal 2: Start the proxy
cd internal/controller/rabbitmq/data
python3 proxy.py --backend localhost:5673 --listen 0.0.0.0:5672 --log-level DEBUG

# Terminal 3: Run the test
python3 proxy_test.py --host localhost --port 5672
```

Expected output:
```
============================================================
Testing Oslo Messaging with amqp_durable_queues=false
============================================================

[Test 1] Connecting to proxy...
✓ Connected to localhost:5672

[Test 2] Declaring non-durable exchange...
✓ Declared exchange 'test_exchange_oslo' with durable=False

[Test 3] Declaring non-durable queue...
✓ Declared queue 'test_queue_oslo' with durable=False

[Test 4] Binding queue to exchange...
✓ Bound queue 'test_queue_oslo' to exchange 'test_exchange_oslo'

[Test 5] Publishing message...
✓ Published message to 'test_exchange_oslo'

[Test 6] Consuming message...
✓ Consumed message from 'test_queue_oslo': Hello from Oslo!

[Test 7] Cleaning up...
✓ Cleaned up test resources

============================================================
✅ ALL TESTS PASSED
============================================================

The proxy successfully rewrote durable=False to durable=True,
allowing Oslo messaging client to work with quorum queues!
```

#### Kubernetes Testing

```bash
# Port-forward to RabbitMQ with proxy
kubectl port-forward -n openstack rabbitmq-server-0 5672:5672

# Run test
python3 proxy_test.py --host localhost --port 5672 --tls
```

Or run directly in cluster:

```bash
# Copy test to a pod
kubectl cp proxy_test.py openstack/rabbitmq-server-0:/tmp/

# Exec into pod and run
kubectl exec -it -n openstack rabbitmq-server-0 -c amqp-proxy -- \
  python3 /tmp/proxy_test.py --host localhost --port 5672 --no-wait
```

### Test Scenarios

The integration test covers:

1. **Non-durable Exchange Declaration**
   - Client declares with `durable=False`
   - Proxy rewrites to `durable=True`
   - RabbitMQ accepts it

2. **Non-durable Queue Declaration**
   - Client declares with `durable=False`
   - Proxy rewrites to `durable=True` + `x-queue-type=quorum`
   - RabbitMQ creates quorum queue

3. **Queue Binding**
   - Verifies proxy doesn't break bindings

4. **Message Publish/Consume**
   - Verifies end-to-end message flow works

5. **Cleanup**
   - Verifies delete operations work

## Expected Behavior Without Proxy

If you run the test directly against RabbitMQ with quorum queues (bypassing the proxy):

```bash
python3 proxy_test.py --host localhost --port 5673 --no-wait
```

Expected **FAILURE**:
```
❌ FAILURE: Channel closed by broker: (406, 'PRECONDITION_FAILED')

PRECONDITION_FAILED error indicates the queue/exchange durability mismatch.
The proxy should have rewritten durable=False to durable=True.
```

This confirms that:
- ❌ Non-durable clients **cannot** use quorum queues
- ✅ Proxy is **required** for the migration

## Continuous Integration

The Go unit tests run automatically in CI:

```yaml
# .github/workflows/test.yml
- name: Run tests
  run: make test
```

The Python integration test requires a running RabbitMQ instance, so it's meant for manual testing or dedicated integration test environments.

## Troubleshooting

### Test fails with "connection refused"

```bash
# Check if proxy is running
netstat -tlnp | grep 5672

# Check proxy logs
kubectl logs -n openstack rabbitmq-server-0 -c amqp-proxy
```

### Test fails with PRECONDITION_FAILED

This means the proxy is **not rewriting** frames correctly. Check:

```bash
# Enable DEBUG logging
python3 proxy.py --log-level DEBUG ...

# Should see:
# DEBUG - Rewriting queue.declare: test_queue_oslo (durable=False → True, quorum)
```

### Test hangs

Check if RabbitMQ backend is reachable:

```bash
# From proxy container
telnet localhost 5673
```

## Performance Testing

For load testing the proxy:

```bash
# Install performance tools
pip install pika locust

# Run load test (TBD: create load test script)
```

## References

- Go tests: `test/functional/rabbitmq_proxy_test.go`
- Python test: `internal/controller/rabbitmq/data/proxy_test.py`
- Proxy implementation: `internal/controller/rabbitmq/data/proxy.py`
- Integration docs: [PROXY_INTEGRATION.md](../../../PROXY_INTEGRATION.md)

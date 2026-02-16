# AMQP Proxy Sidecar Integration

## Overview

The RabbitMQ controller now automatically deploys an AMQP durability proxy as a sidecar container during upgrades and queue type migrations. This proxy enables seamless migration from classic/mirrored queues to quorum queues without requiring immediate OpenStack dataplane reconfiguration.

## Problem Solved

When migrating from RabbitMQ 3.9 (mirrored queues) to 4.2 (quorum queues), external OpenStack services have `amqp_durable_queues=false` configured. However, quorum queues require `durable=True`, causing `PRECONDITION_FAILED` errors. The proxy transparently rewrites AMQP frames to force durability, allowing non-durable clients to work with durable quorum queues.

## Architecture

```
┌─────────────────────────────────────────┐
│ RabbitMQ Pod                            │
│                                         │
│  ┌───────────────────┐                  │
│  │ amqp-proxy        │  Port: 5672      │
│  │ (with TLS)        │  (external)      │
│  └─────────┬─────────┘                  │
│            │ localhost:5673             │
│            ↓ (no TLS)                   │
│  ┌───────────────────┐                  │
│  │ rabbitmq          │  Port: 5673      │
│  │                   │  (localhost)     │
│  └───────────────────┘                  │
└─────────────────────────────────────────┘
         ↑ Port 5672 (TLS)
         │
   External clients
```

## How It Works

1. **Automatic Activation**: Proxy sidecar is automatically added when:
   - Upgrade is in progress (`Status.UpgradePhase != ""`)
   - Migrating to quorum queues before dataplane is reconfigured
   - Annotation `rabbitmq.openstack.org/enable-proxy=true` is set

2. **TLS Handling**:
   - Proxy terminates TLS on port 5672 (using certs from `Spec.TLS.SecretName`)
   - RabbitMQ listens on localhost:5673 without TLS
   - External clients connect to proxy with TLS

3. **Frame Rewriting**: Proxy intercepts AMQP frames and:
   - Rewrites `queue.declare(durable=False)` → `durable=True` + `x-queue-type=quorum`
   - Rewrites `exchange.declare(durable=False)` → `durable=True`
   - Skips reply queues and system queues

4. **Automatic Removal**: Proxy is removed after:
   - Dataplane is reconfigured with `amqp_durable_queues=true`
   - User sets annotation `rabbitmq.openstack.org/clients-reconfigured=true`

## Files Created/Modified

### New Files
- `internal/controller/rabbitmq/proxy.go` - Proxy sidecar integration logic
- `internal/controller/rabbitmq/data/proxy.py` - AMQP proxy script (embedded)
- `internal/controller/rabbitmq/data/README.md` - Documentation for data files
- `PROXY_INTEGRATION.md` - Complete integration documentation

### Modified Files
- `internal/controller/rabbitmq/rabbitmq_controller.go` (lines 805-835):
  - Added proxy sidecar logic after `ConfigureCluster`
  - Calls `ensureProxyConfigMap`, `addProxySidecar`, `configureRabbitMQBackendPort`
  - Removes proxy when `shouldEnableProxy()` returns false

## Key Functions

### proxy.go

- `ensureProxyConfigMap(ctx, instance, helper)` - Creates ConfigMap with embedded proxy script
- `addProxySidecar(instance, cluster)` - Adds proxy container to StatefulSet
- `buildProxySidecarContainer(instance)` - Builds container spec with TLS support
- `removeProxySidecar(cluster)` - Removes proxy sidecar
- `shouldEnableProxy(instance)` - Determines when proxy should be enabled
- `configureRabbitMQBackendPort(instance, cluster)` - Configures RabbitMQ to listen on localhost:5673

## Container Spec

- **Image**: `quay.io/openstack-k8s-operators/openstack-operator-client:latest`
- **Command**: `python3 /scripts/proxy.py`
- **Args**:
  - `--backend localhost:5673`
  - `--listen 0.0.0.0:5672`
  - `--tls-cert /etc/rabbitmq-tls/tls.crt` (if TLS enabled)
  - `--tls-key /etc/rabbitmq-tls/tls.key` (if TLS enabled)
  - `--tls-ca /etc/rabbitmq-tls-ca/ca.crt` (if CA specified)
- **Resources**:
  - Requests: 128Mi memory, 100m CPU
  - Limits: 256Mi memory, 500m CPU
- **Probes**: TCP liveness and readiness on port 5672

## Usage

### Automatic (during upgrade)

The proxy is automatically enabled when upgrading RabbitMQ:

```yaml
apiVersion: rabbitmq.openstack.org/v1beta1
kind: RabbitMq
metadata:
  name: rabbitmq
  annotations:
    rabbitmq.openstack.org/target-version: "4.2.0"
spec:
  queueType: Quorum
  tls:
    secretName: rabbitmq-tls
```

### Manual activation

Force proxy activation with annotation:

```bash
kubectl annotate rabbitmq rabbitmq \
  rabbitmq.openstack.org/enable-proxy=true
```

### Manual deactivation

After dataplane reconfiguration:

```bash
kubectl annotate rabbitmq rabbitmq \
  rabbitmq.openstack.org/clients-reconfigured=true
```

## Monitoring

Check proxy logs:
```bash
kubectl logs rabbitmq-server-0 -c amqp-proxy
```

Proxy prints statistics every 5 minutes:
```
=== Proxy Statistics ===
Total connections: 25
Queue rewrites: 120
Exchange rewrites: 15
Bytes forwarded: 5,432,100
```

## Upgrade Workflow

1. **Start upgrade**: Set target-version annotation → Proxy enabled
2. **Cluster recreated**: RabbitMQ 4.2 with quorum queues + proxy sidecar
3. **External services**: Continue using `amqp_durable_queues=false`
4. **Proxy rewrites**: Frames rewritten to use durable queues
5. **Dataplane reconfig**: Update services to `amqp_durable_queues=true`
6. **Set annotation**: `clients-reconfigured=true` → Proxy removed
7. **Direct connection**: Services connect directly to RabbitMQ

## Important Notes

### Proxy Script Location

The proxy script is located at:
- `internal/controller/rabbitmq/data/proxy.py`

When updating the proxy:
1. Edit `internal/controller/rabbitmq/data/proxy.py`
2. Rebuild: `make`
3. Test: `make test`

### Performance

- Proxy adds ~0.05ms latency (localhost forwarding)
- Memory overhead: ~64Mi per RabbitMQ pod
- CPU overhead: ~20m per RabbitMQ pod

### Security

- Runs as non-root
- No privilege escalation
- Drops all capabilities
- TLS certificates mounted read-only

## Testing

### Go Unit Tests

Proxy sidecar integration tests verify:
- ✅ Proxy is added during upgrades
- ✅ TLS configuration is correct
- ✅ Proxy is removed after client reconfiguration
- ✅ Manual activation via annotations

Run tests:
```bash
make test
# Test Suite Passed
# composite coverage: 72.7% of statements

# Run only proxy tests
cd test/functional
ginkgo -focus="RabbitMQ Proxy"
```

### Python Integration Test

Simulates Oslo messaging client with `amqp_durable_queues=false` connecting through proxy to RabbitMQ with quorum queues.

Quick test:
```bash
cd internal/controller/rabbitmq/data
python3 proxy_test.py --host localhost --port 5672
```

For complete testing documentation, see [TESTING.md](internal/controller/rabbitmq/data/TESTING.md).

## References

- Proxy implementation: `internal/controller/rabbitmq/data/proxy.py`
- Proxy integration: `internal/controller/rabbitmq/proxy.go`
- AMQP 0-9-1 spec: https://www.rabbitmq.com/resources/specs/amqp0-9-1.pdf

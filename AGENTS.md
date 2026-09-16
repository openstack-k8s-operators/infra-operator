# AGENTS.md - infra-operator

## Project overview

infra-operator is a Kubernetes operator that manages shared OpenStack
infrastructure services (IP address management, DNS, caching, messaging, and
high availability) on OpenShift/Kubernetes. It is part of the
[openstack-k8s-operators](https://github.com/openstack-k8s-operators) project.

Key domain concepts: **IPAM** (IP address management and reservation),
**DNS** (dnsmasq-based name resolution), **memcached** (distributed caching),
**Redis** (key-value store), **RabbitMQ transport URLs** (AMQP messaging for
OpenStack services), **BGP** (border gateway protocol routing),
**instance HA** (compute instance fencing and evacuation), **topology**
(placement topology awareness).

## Tech stack

| Layer | Technology |
|-------|------------|
| Language | Go (modules, multi-module workspace via `go.work`) |
| Scaffolding | [Kubebuilder v4](https://book.kubebuilder.io/) + [Operator SDK](https://sdk.operatorframework.io/) |
| CRD generation | controller-gen (DeepCopy, CRDs, RBAC, webhooks) |
| Config management | Kustomize |
| Packaging | OLM bundle |
| Testing | Ginkgo/Gomega + envtest (functional), KUTTL (integration), Python (instanceha) |
| Linting | golangci-lint (`.golangci.yaml`) |
| CI | Zuul (`zuul.d/`), Prow (`.ci-operator.yaml`), GitHub Actions |

## Custom Resources

### Network group (`network.openstack.org`)

| Kind | Purpose |
|------|---------|
| `DNSMasq` | Manages dnsmasq DNS/DHCP server instances. |
| `DNSData` | Defines DNS records to be served by DNSMasq. |
| `IPSet` | Requests and manages IP address allocations. |
| `NetConfig` | Defines network configuration (subnets, ranges). |
| `Reservation` | Manages IP address reservations. |
| `BGPConfiguration` | Manages BGP routing configuration. |

### Memcached group (`memcached.openstack.org`)

| Kind | Purpose |
|------|---------|
| `Memcached` | Manages Memcached cache server deployments. |

### Redis group (`redis.openstack.org`)

| Kind | Purpose |
|------|---------|
| `Redis` | Manages Redis server deployments. |

### RabbitMQ group (`rabbitmq.openstack.org`)

| Kind | Purpose |
|------|---------|
| `TransportURL` | Creates RabbitMQ transport URL secrets for OpenStack services. |

### InstanceHA group (`instanceha.openstack.org`)

| Kind | Purpose |
|------|---------|
| `InstanceHA` | Manages compute instance HA (fencing/evacuation). |

## Directory structure

**Important:** This operator uses `apis/` (not the typical `api/`) for its
type definitions, organized by API group subdirectories.

**Maintenance rule:** when directories are added, removed, or renamed, or when
their purpose changes, update this table to match.

| Directory | Contents |
|-----------|----------|
| `apis/network/v1beta1/` | Network CRD types (`dnsmasq_types.go`, `dnsdata_types.go`, `ipset_types.go`, `netconfig_types.go`, `reservation_types.go`, `bgpconfiguration_types.go`), conditions, webhooks |
| `apis/memcached/v1beta1/` | Memcached CRD types (`memcached_types.go`), conditions, webhook |
| `apis/redis/v1beta1/` | Redis CRD types (`redis_types.go`), webhook |
| `apis/rabbitmq/v1beta1/` | RabbitMQ CRD types (`transporturl_types.go`), conditions, webhooks |
| `apis/instanceha/v1beta1/` | InstanceHA CRD types (`instanceha_types.go`), conditions, webhook |
| `apis/topology/v1beta1/` | Topology CRD types (`topology_types.go`) |
| `apis/bases/` | Generated CRD YAML base manifests |
| `cmd/` | `main.go` entry point |
| `internal/controller/` | Reconcilers organized by API group subdirectories |
| `internal/bgp/` | BGP helper functions |
| `internal/dnsmasq/` | DNSMasq resource builders |
| `internal/ipam/` | IPAM helper functions (IP allocation logic) |
| `internal/memcached/` | Memcached resource builders (statefulset, service, volumes) |
| `internal/redis/` | Redis resource builders (statefulset, service, volumes) |
| `internal/rabbitmq/` | RabbitMQ resource builders |
| `internal/instanceha/` | InstanceHA resource builders |
| `internal/tlsprofile/` | Reads the cluster-wide TLS security profile ConfigMap published by openstack-operator and renders it into each service's native config syntax |
| `internal/webhook/` | Webhook implementations organized by API group |
| `templates/` | Config files and scripts mounted into pods via `OPERATOR_TEMPLATES` env var. Subdirs: `instanceha/`, `memcached/`, `redis/` |
| `config/crd,rbac,manager,webhook/` | Generated Kubernetes manifests (CRDs, RBAC, deployment, webhooks) |
| `config/samples/` | Example CRs for all API groups |
| `pkg/` | Shared utility packages |
| `test/functional/` | envtest-based Ginkgo/Gomega tests |
| `test/instanceha/` | Python-based InstanceHA tests |
| `test/kuttl/` | KUTTL integration tests |
| `test/scripts/` | Helper test scripts |
| `hack/` | Helper scripts (CRD schema checker, local webhook runner) |
| `docs/` | Architecture documentation |

## Build commands

After modifying Go code, always run: `make generate manifests fmt vet`.

## Code style guidelines

- Follow standard openstack-k8s-operators conventions and lib-common patterns.
- Use `lib-common` modules for conditions, endpoints, TLS, storage, and other
  cross-cutting concerns rather than re-implementing them.
- CRD types are organized under `apis/<group>/v1beta1/` (note: `apis/` not
  `api/`). Each API group has its own subdirectory.
- Controller logic goes in `internal/controller/` with group subdirectories.
  Resource-building helpers go in `internal/<service>/` packages matching the
  service they support.
- Config templates are plain files in `templates/` -- they are mounted at
  runtime via the `OPERATOR_TEMPLATES` environment variable.
- Webhook logic is split between the kubebuilder markers in
  `apis/<group>/v1beta1/` and the implementation in `internal/webhook/`.

## Testing

- Functional tests use the envtest framework with Ginkgo/Gomega and live in
  `test/functional/`.
- InstanceHA has its own Python-based test suite in `test/instanceha/`.
- KUTTL integration tests live in `test/kuttl/`.
- Run all functional tests: `make test`.
- When adding a new field or feature, add corresponding test cases in
  `test/functional/` with fixture data.

## Key dependencies

- [lib-common](https://github.com/openstack-k8s-operators/lib-common): shared modules for conditions, endpoints, database, TLS, secrets, etc.

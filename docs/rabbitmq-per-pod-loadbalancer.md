# RabbitMQ per-pod LoadBalancer services

`RabbitMq.spec.podOverride` creates one Service per RabbitMQ pod
(`<name>-server-0`, `<name>-server-1`, ...) instead of a single client Service.
Each entry in `podOverride.services` maps **positionally to one pod**, so the
number of entries must equal `spec.replicas`. The resulting per-pod hostnames
(and ports) are recorded in `RabbitMq.status.serviceHostnames` and used to build
the `TransportURL` connection string, so clients connect to every node directly.

## Overriding the port (shared LoadBalancer IP)

By default each per-pod Service exposes `amqp` on 5672 and `amqps` on 5671. You
can override the Service port per pod via `spec.ports`. Ports are merged into the
base ports **by name**: an override that matches an existing port by `name`
replaces only the fields it sets, while unset fields (e.g. `targetPort`) fall
back to the base value. This lets you change the external `port` without
restating `targetPort`, so traffic on the overridden port still reaches the
RabbitMQ container on 5672/5671.

This makes it possible to place several per-pod Services on a **single**
LoadBalancer IP using metallb's `allow-shared-ip`, distinguished by port.
metallb only allows Services to share an IP when their exposed ports do **not**
overlap. Each per-pod Service exposes both named ports, so assign a distinct
`amqp` and `amqps` port to every Service:

```yaml
apiVersion: rabbitmq.openstack.org/v1beta1
kind: RabbitMq
metadata:
  name: rabbitmq
spec:
  replicas: 3
  podOverride:
    services:
    - metadata:
        annotations:
          metallb.universe.tf/address-pool: internalapi
          metallb.universe.tf/allow-shared-ip: internalapi
          metallb.universe.tf/loadBalancerIPs: 172.17.0.87
      spec:
        type: LoadBalancer
        ports:
        - name: amqp
          port: 5672
        - name: amqps
          port: 5671
    - metadata:
        annotations:
          metallb.universe.tf/address-pool: internalapi
          metallb.universe.tf/allow-shared-ip: internalapi
          metallb.universe.tf/loadBalancerIPs: 172.17.0.87
      spec:
        type: LoadBalancer
        ports:
        - name: amqp
          port: 5673
        - name: amqps
          port: 5675
    - metadata:
        annotations:
          metallb.universe.tf/address-pool: internalapi
          metallb.universe.tf/allow-shared-ip: internalapi
          metallb.universe.tf/loadBalancerIPs: 172.17.0.87
      spec:
        type: LoadBalancer
        ports:
        - name: amqp
          port: 5674
        - name: amqps
          port: 5676
```

The TransportURL uses the `amqp` ports when TLS is disabled and the `amqps`
ports when TLS is enabled. Both sets remain unique across Services sharing the
IP. The generated TransportURL carries the selected per-pod ports. Without TLS,
the example produces:

```
rabbit://user:pass@172.17.0.87:5672,user:pass@172.17.0.87:5673,user:pass@172.17.0.87:5674/?ssl=0
```

With TLS enabled, it uses the AMQPS ports:

```
rabbit://user:pass@172.17.0.87:5671,user:pass@172.17.0.87:5675,user:pass@172.17.0.87:5676/?ssl=1
```

## VIP placement and failure-domain considerations

Exposing each RabbitMQ pod through its own LoadBalancer Service is meant to give
each node an independently reachable address, so that losing one node only
affects that node's address. Whether that guarantee actually holds depends on
two separate layers that have to be reasoned about together: where MetalLB
announces each Service's external IP, and where the scheduler actually places
the pods.

### `externalTrafficPolicy` determines which node can announce a VIP

Per-pod Services default to `externalTrafficPolicy: Cluster`, which the generic
[Kubernetes external-load-balancer
docs](https://kubernetes.io/docs/tasks/access-application-cluster/create-external-load-balancer/)
describe mainly in terms of source-IP preservation. For MetalLB specifically,
the effect is broader than that: in [Layer2
mode](https://metallb.universe.tf/concepts/layer2/), every speaker computes the
set of nodes eligible to announce a given VIP by hashing `node+VIP`, and
`externalTrafficPolicy` is one of the direct inputs to that computation
alongside active endpoints. Under `Cluster`, every node is an eligible
candidate regardless of whether it runs the pod behind the Service, so a
per-pod VIP can end up announced from a node that has nothing to do with that
pod. Under `Local`, kube-proxy's `healthCheckNodePort` fails on any node
without a local, ready endpoint, which excludes those nodes from the
announcer computation — so for a per-pod Service (selector pinned to exactly
one pod via `statefulset.kubernetes.io/pod-name`), the VIP can only ever be
announced by the node currently running that pod:

```yaml
podOverride:
  services:
  - spec:
      type: LoadBalancer
      externalTrafficPolicy: Local
      ports:
      - name: amqp
        port: 5672
```

Without this, nothing prevents two or three per-pod VIPs from independently
hashing onto the *same* node even when their pods are correctly spread across
different nodes. If that node fails, all of those VIPs become unreachable at
once (until MetalLB re-elects an announcer for each), even though only one of
the underlying RabbitMQ pods actually went down — a wider, correlated outage
than the per-pod design is meant to produce.

**This does not combine with the shared-IP pattern above.** MetalLB requires
every Service sharing one IP via `allow-shared-ip` to use the same
`externalTrafficPolicy`. Setting `Local` on a set of per-pod Services that
share one IP requires a single node with a local, ready endpoint for *all* of
them simultaneously — which cannot exist once their pods are spread across
different nodes, leaving the shared VIP with no eligible announcer. `Local`
for VIP-to-pod colocation only works with one dedicated IP per pod (no
`allow-shared-ip`); the shared-IP/distinct-port pattern must stay on
`Cluster` and accept the colocation risk above.

### `externalTrafficPolicy: Local` only ties the VIP to wherever the pod is — it does not spread the pods

`Local` makes VIP placement track pod placement faithfully; it does not make
pod placement more resilient. The default Pod anti-affinity and topology
spread applied by this operator (`internal/rabbitmq/statefulset.go`, used
whenever `spec.affinity`/`spec.topology` is not set) are both soft:

```go
PodAntiAffinity: PreferredDuringSchedulingIgnoredDuringExecution  // node-level, weight 100
TopologySpreadConstraints: ..., WhenUnsatisfiable: ScheduleAnyway  // zone-level
```

Neither constraint can block scheduling, so the scheduler is free to place two
or even all three RabbitMQ pods on the same worker node when it has to (a
drained/cordoned node, a small cluster, resource pressure). This is
intentional — it mirrors the legacy rabbitmq-cluster-operator's default and
avoids pods going `Pending` during routine maintenance — but it means
`externalTrafficPolicy: Local` plus dedicated per-pod VIPs is not sufficient
on its own to guarantee that a single node failure only costs one broker: if
two pods already share a node, their VIPs correctly (and unavoidably)
co-locate there too, and losing that node takes out both pods and both VIPs
together.

Getting an actual one-node-one-broker failure-domain guarantee requires
**both** of the following, not just the `externalTrafficPolicy` change:

- Dedicated per-pod LoadBalancer IPs with `externalTrafficPolicy: Local`
  (ties each VIP to its pod's node).
- A `spec.affinity` override using
  `requiredDuringSchedulingIgnoredDuringExecution` pod anti-affinity (forces
  pods onto distinct nodes).

Hard anti-affinity has an operational cost: it requires at least `replicas`
schedulable nodes to be available at all times, including during node
drains/maintenance, or the evicted pod stays `Pending` instead of
rescheduling. Whether that tradeoff is acceptable depends on cluster size and
spare capacity, and is a deployment-specific decision rather than something
this operator defaults to.

## Notes

- `spec.ports` (per-pod port override) requires lib-common's service override to
  support `ports`; older lib-common releases silently ignore it.
- Only override `port`; leaving `targetPort` unset keeps the RabbitMQ container
  port so routing to the pod is preserved.
- `externalTrafficPolicy` can be overridden per pod the same way as `ports`
  (`spec.podOverride.services[i].spec.externalTrafficPolicy`); see
  [VIP placement and failure-domain considerations](#vip-placement-and-failure-domain-considerations)
  above before combining it with the shared-IP pattern.

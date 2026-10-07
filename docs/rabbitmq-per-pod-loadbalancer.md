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
metallb only allows Services to share an IP when their ports do **not** overlap,
so each pod must use a distinct port:

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
```

With TLS enabled, override `name: amqps` instead (clients connect on the amqps
port). The generated transport URL then carries the per-pod ports, for example:

```
rabbit://user:pass@172.17.0.87:5672,user:pass@172.17.0.87:5673,user:pass@172.17.0.87:5674/?ssl=0
```

## Notes

- `spec.ports` (per-pod port override) requires lib-common's service override to
  support `ports`; older lib-common releases silently ignore it.
- Only override `port`; leaving `targetPort` unset keeps the RabbitMQ container
  port so routing to the pod is preserved.

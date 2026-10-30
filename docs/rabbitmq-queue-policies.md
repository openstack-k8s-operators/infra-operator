# Managed RabbitMQ queue policies

The RabbitMq CR can manage a small set of opt-in broker policies for you, under
`spec.queuePolicies`. The controller reconciles these into managed
`RabbitMQPolicy` resources (labeled `rabbitmq.openstack.org/queue-policy=<cluster>`)
that apply to queues at priority 0. Clearing a sub-policy removes the managed
policy it contributed to.

**One policy per pattern.** RabbitMQ applies at most one regular policy to a
given queue — the highest priority, and ties are nondeterministic — so two
policies that match the same queue cannot both take effect. Because of this the
controller does **not** emit one `RabbitMQPolicy` per sub-policy. Instead it
groups the active sub-policies by their resolved pattern and emits **exactly one
`RabbitMQPolicy` per unique pattern**, merging the definition keys. Sub-policies
that share a pattern (e.g. `messageTTL` and `queueExpiry` both on `^clear-.*`)
end up in a single policy whose definition carries both `message-ttl` and
`expires`. Each managed policy is named `<cluster>-qp-<hash>` where `<hash>` is
derived from the pattern (the raw regex is not a valid object name).

```yaml
apiVersion: rabbitmq.openstack.org/v1beta1
kind: RabbitMq
metadata:
  name: rabbitmq
spec:
  queueType: Quorum
  queuePolicies:
    transientQueueExpiry:
      expiry: 3600000
    messageTTL:
      ttl: 60000
      pattern: "^clear-.*"
    queueExpiry:
      expiry: 1800000
      pattern: "^clear-.*"
```

The patterns are admin-trusted and are ultimately evaluated by RabbitMQ's
(backtracking) regex engine against queue names, so keep them simple and
anchored. The webhook validates only that each pattern is a well-formed regular
expression.

| Field | RabbitMQ argument | Queue types |
|-------|-------------------|-------------|
| `transientQueueExpiry.expiry` | `expires` | Quorum only |
| `messageTTL.ttl` | `message-ttl` | any |
| `queueExpiry.expiry` | `expires` | any |

> **Note on Mirrored clusters.** On a multi-replica Mirrored cluster the operator
> also manages the `ha-all` mirroring policy (empty pattern, `applyTo: all`,
> priority 0), which matches every queue. Because only one regular policy applies
> per queue, a managed queue policy that matches the same queues as `ha-all` can
> win that equal-priority selection and leave those queues without the `ha-mode`
> definition. Prefer Quorum clusters, or choose queue-policy patterns that do not
> overlap the queues you need mirrored.

## Reaping orphaned transient quorum queues (`transientQueueExpiry`)

### Problem

When a RabbitMQ cluster uses **quorum** queues (`spec.queueType: Quorum`), the
OpenStack services are configured with `rabbit_transient_quorum_queue=true`. This
makes the per-process *transient* queues that `oslo.messaging` and the services
create — fanout queues, reply queues, and the RPC server-target queues — quorum
queues.

Unlike classic transient queues, **quorum queues are durable and are never
auto-deleted when their consumer disconnects**. So every time a service process
restarts or reconnects, it creates a fresh set of these queues and leaves the
previous ones behind. Nothing removes them.

This is most visible with **heat**: `heat-engine` uses a freshly generated
`engine_id` (a UUID) as the RPC server identity for each worker process, so its
queues are named `heat-engine-listener.<engine_id>` and
`engine_worker.<engine_id>`. During an event that causes repeated engine
restarts (for example a RabbitMQ outage with the engine crash-looping), these
accumulate without bound — tens of thousands of orphaned quorum queues have been
observed, which can eventually exhaust broker resources (including Erlang atoms).

`oslo.messaging`'s own `rabbit_transient_queues_ttl` only sets `x-expires` on
*reply* and *fanout* queues, not on the RPC server-target queues, so it does not
address the queues that dominate the accumulation.

### Fix

The reliable, naming-agnostic remedy is a broker-side **queue expiry policy**:
RabbitMQ auto-deletes any matching queue that has had **no consumer** for a
configured time (`expires`, in milliseconds). Active service queues keep a
consumer and are therefore untouched while in use.

When `queuePolicies.transientQueueExpiry` is set on a `Quorum` cluster, the
operator manages a `RabbitMQPolicy` that applies `expires` to the matching
queues:

```yaml
spec:
  queueType: Quorum
  queuePolicies:
    transientQueueExpiry:
      # Auto-delete matching transient queues after 1 hour without a consumer.
      expiry: 3600000
```

`expiry` must be at least `60000` (60s): it has to stay well above normal
consumer reconnect windows so a queue briefly left without a consumer is not
reaped during routine reconnects (for reference, oslo.messaging's own
`rabbit_transient_queues_ttl` defaults to 1800000 ms).

This sub-policy is only meaningful on Quorum clusters. On a non-Quorum cluster it
is ignored and any previously managed policy is removed.

#### Which queues are affected

By default the policy targets only transient queues and leaves durable ones
alone. The default pattern is:

```
^(reply_.*|.*_fanout_.*|.*\.[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$
```

This matches:

- `reply_*` and `*_fanout_*` — the oslo.messaging reply/fanout queues.
- `*.<uuid>` — per-process RPC server-target queues keyed by a UUID (e.g. heat's
  `heat-engine-listener.<engine_id>` and `engine_worker.<engine_id>`).

It deliberately does **not** match:

- **Notification queues** (e.g. `versioned_notifications.info`,
  `barbican_notifications.info`) — these are durable and may legitimately sit
  without a consumer while holding buffered messages, so they must never be
  reaped.
- Service queues with stable, host-based server names (e.g.
  `scheduler.nova-scheduler-1`, `cinder-scheduler`) — these are bounded and
  reused across restarts.

Override the pattern only if you need to widen or narrow coverage:

```yaml
spec:
  queueType: Quorum
  queuePolicies:
    transientQueueExpiry:
      expiry: 3600000
      pattern: "^(reply_.*|.*_fanout_.*)$"
```

### Notes

- `expires` set as a queue **argument** by a client (as oslo does for reply and
  fanout queues via `rabbit_transient_queues_ttl`) wins over a policy, so the
  policy mainly takes effect on the RPC server-target queues that are not
  otherwise expired.
- This is the broker-side half of the mitigation. On the service side, enabling
  `use_queue_manager=true` (where supported) reduces fanout/reply churn by giving
  those queues deterministic names, but it does not rename the UUID-keyed
  server-target queues, which is why the expiry policy is needed.

## Expiring old messages (`messageTTL`)

`queuePolicies.messageTTL` contributes a `message-ttl` (in milliseconds) to the
managed policy for the queues matching `pattern`. Messages older than the TTL are
dropped (or dead-lettered, if a dead-letter exchange is configured). Both `ttl`
and `pattern` are required. This applies to any queue type.

```yaml
spec:
  queuePolicies:
    messageTTL:
      ttl: 60000
      pattern: "^clear-.*"
```

## Auto-deleting unused queues (`queueExpiry`)

`queuePolicies.queueExpiry` contributes an `expires` (in milliseconds) to the
managed policy for the queues matching `pattern`, so a queue is auto-deleted once
it has been unused for that long. Both `expiry` and `pattern` are required. This
applies to any queue type.

Unlike `transientQueueExpiry`, this sub-policy makes no assumptions about
OpenStack queue naming — you supply the pattern — and is not gated on the cluster
queue type.

```yaml
spec:
  queuePolicies:
    queueExpiry:
      expiry: 1800000
      pattern: "^clear-.*"
```

## Disabling

Remove a sub-policy (or the whole `queuePolicies` block) and the operator deletes
the corresponding managed `RabbitMQPolicy`. Switching a cluster away from
`Quorum` additionally removes the transient-queue expiry policy.

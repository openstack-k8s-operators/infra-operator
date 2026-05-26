# InstanceHA Heartbeat Performance at 1000-Node Scale

## Overview

This document presents performance profiling results for the InstanceHA
heartbeat mechanism at 1000-node scale, tested with both `THRESHOLD=5%`
(max 50 down hosts) and `THRESHOLD=10%` (max 100 down hosts). All
measurements were taken using the benchmark script at
`test/instanceha/benchmark_heartbeat.py`.

### Configuration Under Test

| Parameter             | Value           |
|-----------------------|-----------------|
| Compute nodes         | 1000            |
| HEARTBEAT_INTERVAL    | 30s             |
| HEARTBEAT_TIMEOUT     | 120s            |
| THRESHOLD             | 5% and 10%      |
| POLL                  | 45s             |
| HEARTBEAT_CLEANUP_THRESHOLD | 2000      |

---

## Memory

The heartbeat mechanism stores a `{hostname: timestamp}` dictionary
protected by a `threading.Lock`. Memory scales linearly with node count
and amortizes Python object overhead at higher counts. Memory usage is
independent of the THRESHOLD setting.

| Nodes | Total Memory | Per-node Cost |
|------:|-------------:|--------------:|
|   100 |     30.9 KiB |         316 B |
|   500 |     67.7 KiB |         138 B |
| 1,000 |    117.1 KiB |         119 B |
| 2,000 |    218.0 KiB |         111 B |

**At 1000 nodes the heartbeat map adds ~117 KiB of RAM.** This is
negligible compared to the overall instanceha process memory (Nova API
client, keystoneauth session, caches, etc.) which typically runs in the
tens of MiB range.

---

## UDP Listener Throughput

The listener is a single-threaded blocking `recvmsg` loop. It processes
incoming heartbeat packets serially: validate magic number, decode
hostname from the payload, and write the timestamp under the lock.
Throughput is independent of THRESHOLD since the listener processes all
packets regardless of how many hosts are down.

| Scenario                        | Packets | Recorded | Wall Time | Per-Packet |  Loss |
|---------------------------------|--------:|---------:|----------:|-----------:|------:|
| 1000 sequential (burst)        |   1,000 |    1,000 |   20 ms   |    20 us   | 0.0%  |
| 1000 threaded (1 thread/node)  |   1,000 |    1,000 |  234 ms   |   234 us   | 0.0%  |
| 5000 sustained (5 rounds)      |   5,000 |    1,000 |  100 ms   |    20 us   | 0.0%  |

### Steady-State Rate

At 1000 nodes with a 30-second heartbeat interval, the listener
processes **~33 packets per second**. Each packet takes approximately
20 us to process, yielding an estimated CPU usage of **< 0.07% of one
core** in steady state.

The single-threaded listener has capacity for roughly 50,000 packets per
second before saturation, providing **~1500x headroom** over the
1000-node steady-state rate.

### Packet Loss

Zero packet loss was observed across all test scenarios, including the
burst test where all 1000 packets were sent as fast as possible from a
single thread. The Linux kernel's default UDP receive buffer (~212 KiB)
can hold approximately 3,000 heartbeat packets (~70 bytes each),
providing sufficient buffering for any realistic burst pattern.

The `RandomizedDelaySec` added to the systemd timer on compute nodes
further prevents thundering herd behavior at boot time.

---

## Heartbeat Filter Performance

`_filter_reachable_hosts()` runs during each polling cycle to separate
hosts that are genuinely down (no heartbeat) from those where only
nova-compute crashed (heartbeat still arriving). It acquires the
`heartbeat_lock`, iterates the down-host list, and performs dict lookups
against the timestamp map.

### Latency by Down-Host Count

The THRESHOLD determines the maximum number of simultaneously down hosts
that will be processed before evacuation is blocked. Filter latency
scales linearly with the number of down hosts being checked.

#### THRESHOLD=5% (max 50 down hosts)

| Down Hosts | Avg CPU  | Avg Wall | Within Threshold? |
|-----------:|---------:|---------:|:-----------------:|
|          1 |    17 us |    18 us | Yes               |
|          5 |    13 us |    13 us | Yes               |
|         10 |    13 us |    13 us | Yes               |
|         25 |    22 us |    22 us | Yes               |
|     **50** | **41 us**| **41 us**| **Yes (max)**     |
|        100 |    78 us |    78 us | No (blocked)      |
|        500 |   374 us |   375 us | No (blocked)      |
|      1,000 |   744 us |   746 us | No (blocked)      |

#### THRESHOLD=10% (max 100 down hosts)

| Down Hosts | Avg CPU  | Avg Wall | Within Threshold? |
|-----------:|---------:|---------:|:-----------------:|
|          1 |    10 us |    10 us | Yes               |
|          5 |    10 us |    10 us | Yes               |
|         10 |    12 us |    12 us | Yes               |
|         25 |    21 us |    21 us | Yes               |
|        50  |    41 us |    41 us | Yes               |
|    **100** | **73 us**| **73 us**| **Yes (max)**     |
|        500 |   346 us |   347 us | No (blocked)      |
|      1,000 |   751 us |   754 us | No (blocked)      |

#### Comparison at Threshold Limit

| THRESHOLD | Max Down Hosts | Filter Latency |
|----------:|---------------:|---------------:|
|        5% |             50 |          41 us |
|       10% |            100 |          73 us |

Both are orders of magnitude below the POLL interval (45 seconds) and
the Nova API call latency (typically 100ms+). Even at THRESHOLD=10%,
the filter completes in under 100 microseconds.

### Mixed Scenario

In practice, some down hosts will still be sending heartbeats
(nova-compute crash) while others are genuinely unreachable (host
failure). The filter correctly separates these:

#### THRESHOLD=5%

| Scenario                           | Latency | Fenced | Skipped |
|------------------------------------|--------:|-------:|--------:|
| 50 down (25 heartbeat + 25 dead)  |   30 us |     25 |      25 |
| 100 down (50 heartbeat + 50 dead) |   54 us |     50 |      50 |

#### THRESHOLD=10%

| Scenario                            | Latency | Fenced | Skipped |
|-------------------------------------|--------:|-------:|--------:|
| 100 down (50 heartbeat + 50 dead)  |   49 us |     50 |      50 |

### cProfile Breakdown

The function-level CPU breakdown is consistent across both threshold
values. Shown here for 10,000 iterations at the respective threshold
limits:

#### THRESHOLD=5% (50 down hosts, 10,000 iterations — 1.42s total)

| Function                  | % Time | Notes                          |
|---------------------------|-------:|--------------------------------|
| `_filter_reachable_hosts` |  32.5% | Main loop + lock acquire       |
| `logging.warning`         |  31.5% | Log per skipped host           |
| `_extract_hostname`       |  12.9% | `str.split('.')` per host      |
| `dict.get`                |   4.8% | Timestamp lookup per host      |
| `list.append`             |   4.2% | Building result lists          |

#### THRESHOLD=10% (100 down hosts, 10,000 iterations — 2.61s total)

| Function                  | % Time | Notes                          |
|---------------------------|-------:|--------------------------------|
| `_filter_reachable_hosts` |  34.5% | Main loop + lock acquire       |
| `logging.warning`         |  33.9% | Log per skipped host           |
| `_extract_hostname`       |  13.9% | `str.split('.')` per host      |
| `dict.get`                |   5.3% | Timestamp lookup per host      |
| `list.append`             |   4.3% | Building result lists          |

Logging dominates the filter cost at both thresholds. Since heartbeat
filtering logs at WARNING level (one message per skipped host), and
these messages are important for operational visibility, this is
acceptable overhead.

---

## Realistic Polling Cycle Simulation

Simulates 10 consecutive polling cycles with concurrent heartbeat
packet arrival and increasing down-host counts.

### THRESHOLD=5% (down hosts: 10 → 50)

| Cycle | Down Hosts | Fenced | Skipped | Filter Time |
|------:|-----------:|-------:|--------:|------------:|
|     1 |         10 |     10 |       0 |       34 us |
|     2 |         15 |     15 |       0 |       23 us |
|     3 |         20 |     20 |       0 |       27 us |
|     4 |         25 |     25 |       0 |       20 us |
|     5 |         30 |     30 |       0 |       23 us |
|     6 |         35 |     35 |       0 |       26 us |
|     7 |         40 |     40 |       0 |       25 us |
|     8 |         45 |     45 |       0 |       36 us |
|     9 |         50 |     50 |       0 |       31 us |
|    10 |         50 |     50 |       0 |       35 us |

- **Average filter time: 28 us**
- **Maximum filter time: 36 us**

### THRESHOLD=10% (down hosts: 10 → 55)

| Cycle | Down Hosts | Fenced | Skipped | Filter Time |
|------:|-----------:|-------:|--------:|------------:|
|     1 |         10 |     10 |       0 |       56 us |
|     2 |         15 |     15 |       0 |       32 us |
|     3 |         20 |     20 |       0 |       44 us |
|     4 |         25 |     25 |       0 |       25 us |
|     5 |         30 |     30 |       0 |       24 us |
|     6 |         35 |     35 |       0 |       30 us |
|     7 |         40 |     40 |       0 |       32 us |
|     8 |         45 |     45 |       0 |       29 us |
|     9 |         50 |     50 |       0 |       27 us |
|    10 |         55 |     55 |       0 |       27 us |

- **Average filter time: 33 us**
- **Maximum filter time: 56 us**

The filter adds negligible latency to the polling cycle at both
threshold values. The dominant cost in each cycle is the Nova API call
to list compute services (~100ms or more).

---

## Lock Contention

The `heartbeat_lock` is shared between the listener thread (writes on
every incoming packet) and the polling loop (reads during
`_filter_reachable_hosts`). Measured under heavy load (20,000 concurrent
packets being processed while running 100 filter calls):

| Metric | THRESHOLD=5% | THRESHOLD=10% |
|--------|-------------:|--------------:|
| Avg    |        31 us |         22 us |
| P50    |        26 us |         22 us |
| P95    |        42 us |         36 us |
| P99    |       299 us |         73 us |
| Max    |       299 us |         73 us |

Lock contention is minimal at both threshold values. The P99 values
represent artificial stress (20x the steady-state packet rate). In
production, the listener processes ~33 packets/second and the filter
runs once every 45 seconds, making contention effectively zero.

---

## Resource Sizing Recommendations

### CPU

The heartbeat listener consumes **< 0.1% of one CPU core** at 1000-node
steady state. The filter adds < 100 us per polling cycle regardless of
threshold setting. No CPU limit increase is needed.

For comparison, a single Nova API call to list compute services
typically takes 100-500ms, which is 1,000-7,000x more CPU-intensive
than the heartbeat filter at THRESHOLD=10%.

### Memory

The heartbeat timestamp map adds **~117 KiB** at 1000 nodes. The
overall instanceha process memory is dominated by the Python runtime,
Nova client, and keystone session objects (typically 50-100 MiB). The
heartbeat overhead is < 0.2% of total process memory.

### Network

Each heartbeat packet is ~70 bytes (4-byte magic + hostname). At 33
packets/second, the bandwidth is **~2.3 KB/s** — negligible on any
network.

### Container Resource Limits

No changes to container resource requests or limits are needed for
heartbeat support at 1000-node scale. The existing limits set for
Nova API operations and evacuation processing provide more than
sufficient headroom.

| Resource | Heartbeat Overhead | Typical Process Total |
|----------|-------------------:|----------------------:|
| CPU      |        < 0.07%     |          Variable     |
| Memory   |        117 KiB     |         50-100 MiB    |
| Network  |        2.3 KB/s    |          Variable     |

---

## Scalability Limits

Based on the profiling data, the heartbeat mechanism can scale well
beyond 1000 nodes:

- **UDP listener saturation**: ~50,000 pkt/s capacity vs 33 pkt/s at
  1000 nodes. Could theoretically support ~45,000 nodes before the
  listener becomes a bottleneck.
- **Memory**: Linear scaling at ~119 B/node. Even at 10,000 nodes the
  map would consume only ~1.2 MiB.
- **Filter latency**: Linear in the number of down hosts, not total
  hosts. The max down hosts is bounded by THRESHOLD, and even at
  THRESHOLD=10% with 1000 nodes (100 down hosts), the filter completes
  in 73 us.
- **Cleanup threshold**: Set to 2000 entries. For deployments beyond
  2000 nodes, this constant should be raised to avoid per-packet
  cleanup iterations.

---

## How to Reproduce

```bash
cd /path/to/infra-operator

# Default (THRESHOLD=5%)
PYTHONPATH=templates/instanceha/bin:test/instanceha \
    python3 test/instanceha/benchmark_heartbeat.py

# With THRESHOLD=10%
THRESHOLD_PERCENT=10 \
PYTHONPATH=templates/instanceha/bin:test/instanceha \
    python3 test/instanceha/benchmark_heartbeat.py
```

The benchmark takes approximately 10-15 seconds to complete and produces
all measurements shown in this document.

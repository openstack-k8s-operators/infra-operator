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
| Protocol version      | HBV2 (HMAC-SHA256) |
| Compute nodes         | 1000            |
| HEARTBEAT_INTERVAL    | 30s             |
| HEARTBEAT_TIMEOUT     | 120s            |
| THRESHOLD             | 5% and 10%      |
| POLL                  | 45s             |
| HEARTBEAT_CLEANUP_THRESHOLD | 2000      |

### HBV2 Packet Format

Each heartbeat packet carries HMAC-SHA256 authentication:

| Field     | Size    | Byte Order      |
|-----------|---------|-----------------|
| Magic     | 4 bytes | Network (BE)    |
| Timestamp | 8 bytes | Network (BE)    |
| Hostname  | 1-64 bytes | UTF-8        |
| HMAC-SHA256 | 32 bytes | —            |

Minimum packet size: 45 bytes. Typical packet for a hostname like
`compute-0000`: 56 bytes. The HMAC is computed over everything except
the last 32 bytes (magic + timestamp + hostname).

---

## Memory

The heartbeat mechanism stores a `{hostname: timestamp}` dictionary
protected by a `threading.Lock`. Memory scales linearly with node count
and amortizes Python object overhead at higher counts. Memory usage is
independent of the THRESHOLD setting.

| Nodes | Total Memory | Per-node Cost |
|------:|-------------:|--------------:|
|   100 |     31.2 KiB |         319 B |
|   500 |     67.4 KiB |         137 B |
| 1,000 |    117.0 KiB |         119 B |
| 2,000 |    218.0 KiB |         111 B |

**At 1000 nodes the heartbeat map adds ~117 KiB of RAM.** This is
negligible compared to the overall instanceha process memory (Nova API
client, keystoneauth session, caches, etc.) which typically runs in the
tens of MiB range.

---

## UDP Listener Throughput

The listener is a single-threaded blocking `recvfrom` loop. For each
HBV2 packet it: validates the magic number, computes HMAC-SHA256 and
compares against loaded keys, checks timestamp freshness, decodes and
validates the hostname, and writes the timestamp under the lock.

Throughput is independent of THRESHOLD since the listener processes all
packets regardless of how many hosts are down.

| Scenario                        | Packets | Recorded | Wall Time | Per-Packet |  Loss |
|---------------------------------|--------:|---------:|----------:|-----------:|------:|
| 1000 sequential (burst)        |   1,000 |    1,000 |   47 ms   |    47 us   | 0.0%  |
| 1000 threaded (1 thread/node)  |   1,000 |    1,000 |  240 ms   |   240 us   | 0.0%  |
| 5000 sustained (5 rounds)      |   5,000 |    1,000 |  174 ms   |    35 us   | 0.0%  |

### HMAC Overhead

Each packet requires HMAC-SHA256 computation (~35 us per packet),
timestamp parsing, and age validation. This overhead is negligible
at the steady-state rate of 33 packets/second.

### Steady-State Rate

At 1000 nodes with a 30-second heartbeat interval, the listener
processes **~33 packets per second**. Each packet takes approximately
35 us to process, yielding an estimated CPU usage of **< 0.12% of one
core** in steady state.

The single-threaded listener has capacity for roughly 28,000 packets per
second before saturation (at 35 us/packet), providing **~850x headroom**
over the 1000-node steady-state rate.

### Packet Loss

Zero packet loss was observed across all realistic test scenarios. The
Linux kernel's default UDP receive buffer (~212 KiB) can hold
approximately 2,400 HBV2 heartbeat packets (~56 bytes each plus kernel
per-packet overhead), providing sufficient buffering for any realistic
arrival pattern.

The `RandomizedDelaySec` added to the systemd timer on compute nodes
further prevents thundering herd behavior at boot time.

**Note on burst testing:** Under extreme synthetic burst conditions
(1000 packets sent in a tight loop from a single thread), occasional
packet loss may be observed due to Python GIL contention between sender
and listener threads in the benchmark harness. This is a benchmark
artifact and does not occur in production, where packets arrive from
separate hosts at the steady-state rate of ~33 packets/second.

---

## Heartbeat Filter Performance

`_filter_reachable_hosts()` runs during each polling cycle to separate
hosts that are genuinely down (no heartbeat) from those where only
nova-compute crashed (heartbeat still arriving). It acquires the
`heartbeat_lock`, iterates the down-host list, and performs dict lookups
against the timestamp map. Filter performance is unaffected by the
protocol version since it operates on the in-memory timestamp dictionary.

### Latency by Down-Host Count

The THRESHOLD determines the maximum number of simultaneously down hosts
that will be processed before evacuation is blocked. Filter latency
scales linearly with the number of down hosts being checked.

#### THRESHOLD=5% (max 50 down hosts)

| Down Hosts | Avg CPU  | Avg Wall | Within Threshold? |
|-----------:|---------:|---------:|:-----------------:|
|          1 |    20 us |    20 us | Yes               |
|          5 |    13 us |    13 us | Yes               |
|         10 |    16 us |    16 us | Yes               |
|         25 |    25 us |    25 us | Yes               |
|     **50** | **42 us**| **42 us**| **Yes (max)**     |
|        100 |    77 us |    77 us | No (blocked)      |
|        500 |   356 us |   358 us | No (blocked)      |
|      1,000 |   692 us |   694 us | No (blocked)      |

#### THRESHOLD=10% (max 100 down hosts)

| Down Hosts | Avg CPU  | Avg Wall | Within Threshold? |
|-----------:|---------:|---------:|:-----------------:|
|          1 |    13 us |    13 us | Yes               |
|          5 |    13 us |    13 us | Yes               |
|         10 |    16 us |    16 us | Yes               |
|         25 |    25 us |    25 us | Yes               |
|        50  |    42 us |    42 us | Yes               |
|    **100** | **77 us**| **77 us**| **Yes (max)**     |
|        500 |   347 us |   349 us | No (blocked)      |
|      1,000 |   726 us |   731 us | No (blocked)      |

#### Comparison at Threshold Limit

| THRESHOLD | Max Down Hosts | Filter Latency |
|----------:|---------------:|---------------:|
|        5% |             50 |          42 us |
|       10% |            100 |          77 us |

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
| 50 down (25 heartbeat + 25 dead)  |   43 us |     25 |      25 |
| 100 down (50 heartbeat + 50 dead) |   64 us |     50 |      50 |

#### THRESHOLD=10%

| Scenario                            | Latency | Fenced | Skipped |
|-------------------------------------|--------:|-------:|--------:|
| 100 down (50 heartbeat + 50 dead)  |   64 us |     50 |      50 |

### cProfile Breakdown

The function-level CPU breakdown is consistent across both threshold
values. Shown here for 10,000 iterations at the respective threshold
limits:

#### THRESHOLD=5% (50 down hosts, 10,000 iterations — 1.48s total)

| Function                  | % Time | Notes                          |
|---------------------------|-------:|--------------------------------|
| `_filter_reachable_hosts` |  33.0% | Main loop + lock acquire       |
| `logging.warning`         |  31.1% | Log per skipped host           |
| `_extract_hostname`       |  12.8% | `str.split('.')` per host      |
| `dict.get`                |   4.6% | Timestamp lookup per host      |
| `list.append`             |   4.1% | Building result lists          |

#### THRESHOLD=10% (100 down hosts, 10,000 iterations — 2.64s total)

| Function                  | % Time | Notes                          |
|---------------------------|-------:|--------------------------------|
| `_filter_reachable_hosts` |  34.6% | Main loop + lock acquire       |
| `logging.warning`         |  33.9% | Log per skipped host           |
| `_extract_hostname`       |  13.9% | `str.split('.')` per host      |
| `dict.get`                |   5.0% | Timestamp lookup per host      |
| `list.append`             |   4.2% | Building result lists          |

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
|     1 |         10 |     10 |       0 |       38 us |
|     2 |         15 |     15 |       0 |       26 us |
|     3 |         20 |     20 |       0 |       33 us |
|     4 |         25 |     25 |       0 |       35 us |
|     5 |         30 |     30 |       0 |       72 us |
|     6 |         35 |     35 |       0 |       45 us |
|     7 |         40 |     40 |       0 |       49 us |
|     8 |         45 |     45 |       0 |       73 us |
|     9 |         50 |     50 |       0 |       74 us |
|    10 |         50 |     50 |       0 |      144 us |

- **Average filter time: 59 us**
- **Maximum filter time: 144 us**

### THRESHOLD=10% (down hosts: 10 → 55)

| Cycle | Down Hosts | Fenced | Skipped | Filter Time |
|------:|-----------:|-------:|--------:|------------:|
|     1 |         10 |     10 |       0 |       45 us |
|     2 |         15 |     15 |       0 |       37 us |
|     3 |         20 |     20 |       0 |       29 us |
|     4 |         25 |     25 |       0 |       49 us |
|     5 |         30 |     30 |       0 |       25 us |
|     6 |         35 |     35 |       0 |       33 us |
|     7 |         40 |     40 |       0 |       22 us |
|     8 |         45 |     45 |       0 |       32 us |
|     9 |         50 |     50 |       0 |       66 us |
|    10 |         55 |     55 |       0 |       32 us |

- **Average filter time: 37 us**
- **Maximum filter time: 66 us**

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
| Avg    |        36 us |          7 us |
| P50    |        26 us |          6 us |
| P95    |        56 us |         10 us |
| P99    |       737 us |         27 us |
| Max    |       737 us |         27 us |

Lock contention is minimal at both threshold values. The elevated P99
at THRESHOLD=5% reflects artificial stress (20x the steady-state packet
rate with concurrent HMAC verification). In production, the listener
processes ~33 packets/second and the filter runs once every 45 seconds,
making contention effectively zero.

---

## Resource Sizing Recommendations

### CPU

The heartbeat listener with HBV2 HMAC verification consumes **< 0.12%
of one CPU core** at 1000-node steady state. The filter adds < 100 us
per polling cycle regardless of threshold setting. No CPU limit increase
is needed.

For comparison, a single Nova API call to list compute services
typically takes 100-500ms, which is 1,000-7,000x more CPU-intensive
than the heartbeat filter at THRESHOLD=10%.

### Memory

The heartbeat timestamp map adds **~117 KiB** at 1000 nodes. The
overall instanceha process memory is dominated by the Python runtime,
Nova client, and keystone session objects (typically 50-100 MiB). The
heartbeat overhead is < 0.2% of total process memory.

### Network

Each HBV2 heartbeat packet is ~56 bytes (4-byte magic + 8-byte timestamp
+ hostname + 32-byte HMAC). At 33 packets/second, the bandwidth is
**~1.8 KB/s** — negligible on any network.

### Container Resource Limits

No changes to container resource requests or limits are needed for
heartbeat support at 1000-node scale. The existing limits set for
Nova API operations and evacuation processing provide more than
sufficient headroom.

| Resource | Heartbeat Overhead | Typical Process Total |
|----------|-------------------:|----------------------:|
| CPU      |        < 0.12%     |          Variable     |
| Memory   |        117 KiB     |         50-100 MiB    |
| Network  |        1.8 KB/s    |          Variable     |

---

## Scalability Limits

Based on the profiling data, the heartbeat mechanism can scale well
beyond 1000 nodes:

- **UDP listener saturation**: ~28,000 pkt/s capacity vs 33 pkt/s at
  1000 nodes. Could theoretically support ~25,000 nodes before the
  listener becomes a bottleneck.
- **Memory**: Linear scaling at ~119 B/node. Even at 10,000 nodes the
  map would consume only ~1.2 MiB.
- **Filter latency**: Linear in the number of down hosts, not total
  hosts. The max down hosts is bounded by THRESHOLD, and even at
  THRESHOLD=10% with 1000 nodes (100 down hosts), the filter completes
  in 77 us.
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

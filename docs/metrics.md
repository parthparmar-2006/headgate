# Metrics and measured overhead

Each gateway serves Prometheus metrics on a separate port, so they're never routed through the gateway or exposed with it:

```yaml
metrics_listen: ":9100"   # GET /metrics; omit to disable
```

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `headgate_requests_total` | counter | route, outcome | `allowed`, `denied`, `fail_open`, `fail_closed`, `no_route`, `no_key` |
| `headgate_denied_total` | counter | route, scope | which limit (`key`, `tenant`, `route`) denied |
| `headgate_limiter_seconds` | histogram | route | time to reach a rate-limit decision, including timeouts |
| `headgate_upstream_seconds` | histogram | route | upstream round trip until response headers |
| `headgate_overhead_seconds` | histogram | route, outcome | time the gateway adds: handler time minus upstream round trip |
| `headgate_deny_cache_hits_total` | counter | route | denials answered locally |
| `headgate_deny_cache_entries` | gauge | route | keys held in the deny cache |
| `headgate_redis_pool_connections` | gauge | | open Redis connections |
| `headgate_redis_pool_misses_total` | counter | | connections opened because none was idle |
| `headgate_redis_pool_timeouts_total` | counter | | waits for a pool connection that timed out |

Latency histograms use exponential buckets from 50 µs to about 0.9 s, each 25% wider than the previous one.

## What "overhead" means

`headgate_overhead_seconds` is measured **inside** the handler: from the moment the handler starts until the proxy returns, minus the upstream round trip, which is measured by wrapping the proxy's connection layer to the upstream. Fail-open requests carry `outcome="fail_open"`, so the time they spend waiting out the limiter budget doesn't mix with normal requests.

It **doesn't** include what any proxy adds outside its handler: the extra network hop, connection handling and HTTP parsing. To measure that too, a load test ran two scenarios at the same rate in the same run, one through the gateways and one directly to the backend, and compared their latency percentiles.

## Measured

Three gateway replicas, 1,000 req/s through the gateways plus 1,000 req/s direct, all allowed (no denials). Fixed window. Runs alternated between backends. One Windows laptop under memory pressure, so medians are reliable and tails aren't.

| (ms) | In-memory limiter | Redis limiter |
|---|---|---|
| Rate-limit decision, p50 | 0.025 | **0.31** |
| In-handler overhead, p50 | 0.07 to 0.09 | **0.40** |
| In-handler overhead, p90 | 0.12 to 0.21 | 0.90 to 0.96 |
| Client-visible added latency, p50 | 0.37 to 0.53 | **0.81** |
| Client-visible minus in-handler (the proxy hop itself) | 0.29 to 0.44 | 0.41 |

- **The Redis round trip is about three quarters of in-handler overhead at p50**, and nearly all of it in the tail (decision p99 7.5 to 9 ms of an overhead p99 of 8 to 10 ms on this machine).
- **The proxy hop costs about 0.3 to 0.4 ms** whichever limiter is used. That's the price of having a gateway in the path at all.
- Client-visible overhead at p50: **0.81 ms** with Redis, of which about 0.4 ms is the Redis call and about 0.4 ms is the proxy hop.

Tail latencies on this machine are dominated by memory-pressure stalls (the host had 0.9 GB free, with OS memory compression active). Tail claims need a dedicated Linux environment.

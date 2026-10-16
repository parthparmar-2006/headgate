# Rate-limiting algorithms: measured comparison

Headgate implements three algorithms behind one `Limiter` interface. Each runs as a single Redis Lua script, so the check and the update are atomic across all gateway replicas. Every number below was measured, not taken from the literature. The method is at the end.

## Summary

| | Fixed window | Sliding window log | Sliding window counter |
|---|---|---|---|
| What Redis stores per key | count + window start (hash) | one sorted-set entry per allowed request | current count + previous count + window start (hash) |
| Memory per key, limit 100 | 160 B | 3.7 KB | 168 B |
| Memory per key, limit 10,000 | 160 B | 1.1 MB | 176 B |
| Grows with the limit? | no | **yes, linearly** | no |
| Worst 1 s span, burst before and after an edge | **200** (2×) | 100 | 110 |
| Worst 1 s span, burst at the end of a window, then steady traffic | **200** (2×) | 100 | **195** (1.95×) |
| Worst 1 s span, steady overload starting mid-window, 3 replicas | **200** | 100 | **157** |
| Redis time per call (noisy VM; relative only) | ~40 µs | ~36 µs | ~33 µs |

Limit 100 requests per 1 s window throughout.

## How each one works

**Fixed window.** Time is cut into aligned windows (12:00:00.000 to 12:00:00.999, and so on). Each key has a counter that resets at every boundary. It's cheap and simple, but the limit is only enforced *within* a calendar window. A client can use the whole limit at the end of one window and again at the start of the next, which gives twice the limit in a few milliseconds.

**Sliding window log.** Every allowed request is stored as a timestamp in a sorted set. On each request, timestamps older than one window are removed and the rest are counted. This is exact for any rolling window, but memory is proportional to the limit: the log holds up to `limit` entries per key.

**Sliding window counter.** This keeps the fixed-window counts for the current and previous windows, and estimates the rolling count as `previous × (time left in the current window ÷ window) + current`. That assumes the previous window's requests were spread evenly. Memory is constant, as with fixed window.

## Burst behaviour

### Experiment 1: the full limit just before and just after a window edge
At 900 ms into a window, 100 concurrent requests are sent. At 100 ms into the next window, another 100. The two bursts are about 200 ms apart.

| | Allowed before | Allowed after | Total in ~200 ms |
|---|---|---|---|
| Fixed window | 100 | 100 | **200** |
| Sliding log | 100 | 0 | **100** |
| Sliding counter | 100 | 10 to 11 | **110 to 111** |

The counter allows 10, which is exactly what the formula predicts: 100 ms into the new window, the previous window is weighted at 0.9, giving 90 + 0. Read on its own, this experiment makes the counter look about 10% leaky. It isn't the worst case.

### Experiment 2: a burst at the end of a window, then steady traffic
At 950 ms, 100 concurrent requests are sent. Then traffic runs at 200 req/s (twice the limit) for one second. The worst count in any 1-second span is computed from the exact decision timestamps.

| | Worst 1 s span |
|---|---|
| Fixed window | **200** |
| Sliding log | **100** |
| Sliding counter | **194 to 195** |

The counter assumes the burst was spread across the previous window, so it "forgets" it at a steady rate, while all 100 requests are still inside the real rolling window. As the new window progresses, the counter lets in about `100 × elapsed` more requests. Just before the burst finally leaves the rolling window, that's nearly 100 extra. **The counter's worst case is close to 2×, the same as fixed window.** Its advantage only holds for traffic that really is spread evenly.

### Experiment 3: realistic steady overload across 3 gateway replicas
Two keys each send 600 req/s, spread round-robin over 3 replicas, for 30 s. No deliberate bursts.

| | Worst calendar second | Worst 1 s span |
|---|---|---|
| Fixed window | 100 | **200** |
| Sliding log | 100 | **100** |
| Sliding counter | 99 | **150 to 157** |

The overshoot comes from **the moment load begins**. The client starts partway through a window, so its first allowed batch arrives late in that window. That's the same shape as experiment 2, without trying. Fixed window doubles. The counter overshoots by roughly the fraction of the window that had already passed when the client started (k6 started about 0.6 s in, giving a predicted ~160 and a measured 150 to 157). After the first seconds, all three settle to the limit.

## Memory per key

`MEMORY USAGE` after filling one key to its limit (1-hour window, so nothing expires):

| Limit | Fixed window | Sliding log | Sliding counter |
|---|---|---|---|
| 10 | 160 B | 440 B (listpack) | 168 B |
| 100 | 160 B | 3,704 B (listpack) | 168 B |
| 1,000 | 160 B | 102,760 B (skiplist) | 168 B |
| 10,000 | 160 B | 1,138,248 B (skiplist) | 176 B |

The sliding log costs about **37 B per entry** while Redis stores it compactly (a listpack, up to 128 entries by default), and about **103 to 114 B per entry** once it switches to a skiplist. Scaled up, **one million active keys at limit 100 means about 3.7 GB for the log, against about 160 MB for either counter-based algorithm**.

## Redis CPU per call

Medians of three interleaved rounds of 20,000 calls (limit 1,000 per 1 s, 50 concurrent clients):

| Fixed window | Sliding log | Sliding counter |
|---|---|---|
| 40 µs | 36 µs | 33 µs |

On this machine the differences are inside the noise. The absolute values are inflated by a congested Docker VM, where Redis loses CPU time. The sliding log isn't measurably more expensive here, because removing expired entries is amortized: each call removes only the few that expired since the last call. **Its cost is memory, not CPU.**

## Which one to use

- **Sliding log** when the limit must hold for *any* 1-second span (billing, abuse protection, contractual limits) and limits are modest, or the number of active keys is bounded. It's the only exact one.
- **Sliding counter** for high-cardinality keys with large limits and traffic that's roughly uniform, where an occasional overshoot of up to about 2× at load onset is acceptable.
- **Fixed window** when "N per calendar window" is the actual contract (for example "1,000 per minute, reset on the minute") and simplicity matters. Its overshoot is predictable: up to 2× around a boundary.

Headgate defaults to the sliding log. A per-route choice is planned.

## Method
- All three algorithms are Lua scripts that read Redis's own clock (`TIME`) inside the script. Window boundaries are therefore the same on every replica, and each decision carries an exact timestamp.
- Accuracy is measured from those decision timestamps. The gateway forwards each allowed request's decision time to the test backend, which records it. "Worst 1 s span" is the maximum count over every half-open interval `(t - 1s, t]`.
- Experiments 1 and 2 drive the limiters directly against Redis, timed against Redis's clock (`internal/limiter/experiment_test.go`, run with `HEADGATE_EXPERIMENTS=1`). Experiment 3 runs the full gateway: 3 replicas, k6 load, Docker Compose (`bench/k6/overshoot.js`).
- Environment: one Windows laptop (8 cores, 8 GB), Docker Desktop, Redis 7.4. Accuracy results were identical across repeated trials. Latency and CPU figures are only valid relative to each other.

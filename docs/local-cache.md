# Local deny cache

Every gateway replica keeps a small in-process cache of keys that Redis recently denied. When Redis answers "denied, retry in X", the replica denies that key locally until X has passed, without asking Redis again. Allowed decisions and errors are never cached.

```yaml
deny_cache:
  enabled: true
  max_keys: 100000   # per route; when full, expired entries are swept at most once per second
```

## Why it can't over-allow

The cache only ever turns a Redis call into a **deny**. That's safe as long as Redis's `retry after` is never later than the earliest moment the key could really be allowed again:

| Algorithm | Retry-after returned | Earliest real allow |
|---|---|---|
| Fixed window | end of the current window | exactly then |
| Sliding log | when the oldest entry leaves the window | exactly then; entries only expire or get added |
| Sliding counter | solved from the weighted formula (or window end if the current window alone is full) | then or later; the window-end case is a lower bound |

A denied request never changes Redis's state, so the sequence of allowed decisions is the same with or without the cache. The only accuracy effect is that a cached deny expires a fraction of a millisecond late, because the replica starts the countdown after the reply arrives. That's slightly conservative.

## Measured

**Same traffic shape, cache off vs on** (a burst of 100 at 950 ms into a window, then 500 req/s for 2 s; limit 100/s; real Redis):

| | Allowed | Worst 1 s span | Redis calls |
|---|---|---|---|
| Fixed window | 300 → 300 | 200 → 200 | 1,095 → **303** |
| Sliding log | 204 → 202 | 100 → 100 | 1,040 → **204** |
| Sliding counter | 294 → 294 | 194 → 194 | 1,100 → **490** |

Identical in two repeated runs. The sliding log's 2 fewer allows are the late-expiry effect: 1%, in the conservative direction.

**Full gateway, 3 replicas, 2 keys each sending 600 req/s against a limit of 100/s (6× overload):**

| | Redis calls/s, off → on | Reduction | Allowed/s, off → on |
|---|---|---|---|
| Fixed window | 1,200 → 216 | **82%** | 100 → 100 |
| Sliding log | 1,199 → 235 | **80%** | 100 → 100 |
| Sliding counter | 1,200 → 766 | **36%** | 99 → 99 |

**Keys under their limit (90 req/s against 100):** 2,701 → 2,701 calls. **No saving**, as expected: there are no denies to cache.

## Why the algorithms differ

- **Fixed window**: a deny lasts until the window ends, so one Redis call covers hundreds of milliseconds of denies per replica.
- **Sliding log**: under sustained overload, each freed slot is taken immediately by a waiting request, so allowed requests stay bunched together and the log frees capacity in bursts about one window apart. Retry times are long and cache well. *I expected the opposite and was wrong.*
- **Sliding counter**: the previous window's weight shrinks continuously, so capacity frees bit by bit and retry times are short. Each cached deny expires almost immediately.

## What it doesn't do

It doesn't reduce Redis traffic for **allowed** requests. A lease design (each replica reserving a batch of permits) would, at the cost of wasted permits and awkwardness for the sliding log. At the target load, Redis capacity isn't the constraint: about 12k script calls/s is a fraction of one Redis core. The remaining cost is the network round trip on each allowed request, which is a latency question, settled by the performance measurements.

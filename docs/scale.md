# Scaling to 8 replicas on one machine

```sh
docker compose -f deploy/docker-compose.yml --profile scale up --build -d   # 8 gateways
```

The `scale` profile adds gateways 4 to 8. All gateways share one definition (`x-gateway` in the compose file). `GOMAXPROCS` is configurable per run.

**Environment:** one Windows laptop, 8 cores, 8 GB RAM, Docker Desktop (WSL2 VM with about 3.7 GB). Load generator, gateways, Redis and backend all on the same machine, with the host under memory pressure (under 1 GB free). The accuracy results don't depend on this; the throughput results do.

## Accuracy across 8 replicas

Four keys each sending 500 req/s, spread round-robin over 8 gateways. Key limit 100/s; keys 0 and 1 share tenant `acme` (limit 150/s). The worst count in any rolling 1-second span comes from the limiter's own decision timestamps.

| | Each key, worst 1 s span | Tenant acme, worst 1 s span | Seconds over limit |
|---|---|---|---|
| **Sliding log** | **100** | **150** | **0** |
| Fixed window | 136 to 164 (edge effect); max 100 per calendar second | 278 | 0 per calendar second |

The sliding log is exact across 8 replicas: zero overshoot. Fixed window is exact per calendar window and shows its known edge burst on rolling spans (see [algorithms.md](algorithms.md)).

## Throughput: where this machine breaks

Constant arrival rate through 8 gateways (default `GOMAXPROCS=8`), 20 s per step, all requests within their limits. "Served" comes from the gateways' own counters:

| Offered | Served | Fail-closed 503s | Decision p50 / p99 | CPU: gateways / load generator / Redis |
|---|---|---|---|---|
| 2,000/s | 1,960/s | 4.2% | 0.5 / 78 ms | 206% / 91% / 29% |
| 4,000/s | 3,846/s | 3.6% | 1.6 / 62 ms | 292% / 171% / 42% |
| 6,000/s | 4,797/s | 7.3% | 4.8 / 72 ms | 328% / 217% / 50% |
| 8,000 to 12,000/s | **4,650 to 4,985/s (ceiling)** | 7 to 11.5% | 7.7 / 70 to 77 ms | 300 to 343% / 130 to 216% / 42 to 49% |

- **Ceiling ~4.5k to 5k req/s on this machine.** Redis stays under half a core, so it isn't the limit.
- **A CPU profile of a gateway under load** shows about 500 µs of CPU per request, mostly system calls (`Syscall6` 30%, write flushes 20%, `futex` 12%). Each request makes three network round trips (client, Redis, upstream), and system calls are expensive inside this memory-starved VM. The gateway's own code barely appears in the profile.
- **The 503s under overload** come from the rate-limit decision exceeding its 50 ms budget on a fail-closed route. That's load shedding, but as a side effect of the timeout rather than by design.

## 8 replicas on one host are slower than 3

At 4,000 req/s offered:

| | Served | 503s | Gateway CPU | Client p50 |
|---|---|---|---|---|
| 3 replicas | 3,895/s | 0.4% | 246% | 2.4 ms |
| 8 replicas, `GOMAXPROCS=8` | 3,631/s | 6.1% | 316% | 8.4 ms |
| 8 replicas, `GOMAXPROCS=1` | 3,837/s | 2.7% | 232% | 3.2 ms |

Eight Go processes, each running 8 scheduler threads on 8 shared cores, spend extra CPU switching and waking threads. Size each replica's threads to its share of the machine: either set `GOMAXPROCS`, or give each container a CPU limit, which Go 1.25+ reads automatically. Replicas exist to spread load across machines, not to multiply processes on one.

## What this doesn't show

Throughput and tail latency at 12k req/s need dedicated Linux machines, with the load generator separate from the gateways. On this laptop, the ceiling is the virtualized host.

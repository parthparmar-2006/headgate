# What happens when Redis fails

Each route chooses what to do when the rate limiter can't reach Redis:

```yaml
redis:
  addr: "redis:6379"
  timeout: 50ms          # budget for one limiter decision
  min_idle_conns: 16     # warm connections kept per gateway

routes:
  - name: payments
    path_prefix: /payments
    algorithm: sliding_log
    on_redis_error: fail_closed   # reject with 503 + Retry-After: 1
    limits:
      - per: key
        limit: 100
        window: 1s
  - name: catalog
    path_prefix: /catalog
    algorithm: sliding_counter
    on_redis_error: fail_open     # forward without limiting
    limits:
      - per: key
        limit: 1000
        window: 1s
```

Degraded responses carry `X-Headgate-Limiter: fail-open` or `fail-closed`, so they can be counted.

## Measured behaviour

Three gateway replicas, 1,200 req/s split across a fail-open route and a fail-closed route (each with limit 100/s). Each route has one "hot" key sending 200/s (twice its limit) and 20 normal keys. Redis was paused, then stopped, mid-run.

| Phase | Fail-open route | Fail-closed route | p99 latency |
|---|---|---|---|
| Normal | hot key held to **100/s** | hot key held to **100/s** | 4 to 80 ms* |
| Redis **paused** (accepts connections, never replies) | all 200; hot key gets **200/s, unlimited** | **all 503** | 60 to 350 ms*; each request waits the 50 ms budget |
| Redis **stopped** (connection refused) | all 200 | all 503 | **1 to 40 ms***; refused connections fail immediately |
| Redis back | limiting resumes within **1 to 2 s** | same | normal |

\*On a congested single-laptop Docker VM. Compare rows with each other, not with production numbers.

## Design decisions behind these numbers

**A hard per-decision budget, enforced through the request context.** go-redis ignores context deadlines unless `ContextTimeoutEnabled` is set. Measured against a server that accepts connections and never replies, the default client held a request for **5.0 s** under a 50 ms budget. With the option set, it returns in 50 ms. Without it, fail-open would still make every request wait 5 seconds.

**No client retries (`MaxRetries: -1`).** A limiter script isn't idempotent. A retry after a timeout can count one request twice, and retries multiply the worst-case latency. One attempt within the budget is predictable.

**A warm connection pool (`min_idle_conns`).** With an empty pool, bursts force new connections to be opened mid-request, and those miss a 50 ms budget. Interleaved A/B runs: **704 to 825** spuriously degraded requests per 8 s run with no idle connections, against **72 to 89** with 16 per gateway.

**No circuit breaker (yet).** During a silent outage, each request costs at most the budget (50 ms). Recovery takes 1 to 2 s without any help, because the pool reconnects by itself. A breaker would remove the 50 ms during outages, at the cost of delaying recovery until a probe succeeds. That's worth it if the budget has to be much larger, for example with Redis in another region.

**Startup doesn't require Redis.** If Redis is unreachable when a gateway starts, it logs a warning and serves traffic under each route's failure policy instead of refusing to start.

## The budget trade-off

A tighter budget bounds outage latency, but in a noisy environment it turns ordinary slow calls into spurious degradations. On a fail-open route, each spurious degradation is a request the limiter didn't count. In one run, a hot key got 118/s through against a limit of 100 while Redis was healthy, purely because some calls exceeded 50 ms during VM stalls. Pick the budget from the Redis call's own p99.9 in production, not from a round number.

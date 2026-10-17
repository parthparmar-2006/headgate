# Per-key, per-tenant and per-route limits

A route can enforce several limits on the same request. The request is allowed only if **every** limit has room, and only then are all of them counted.

```yaml
tenants:
  acme: [key-0, key-1]
  globex: [key-2]

routes:
  - name: default
    path_prefix: /
    algorithm: sliding_log
    on_redis_error: fail_closed
    limits:
      - per: key        # each API key
        limit: 100
        window: 1s
      - per: tenant     # all keys of the same tenant combined
        limit: 150
        window: 1s
      - per: route      # every caller of this route combined
        limit: 1000
        window: 1s
```

- **Tenants** come from a static key-to-tenant map. Keys not in the map share one tenant, `_unassigned`, so rotating through random keys doesn't escape a tenant-level limit. The gateway forwards the resolved tenant as `X-Headgate-Tenant` and overwrites any value the client sent. A production deployment would get this mapping from its auth service.
- Responses carry `X-RateLimit-Scope` (`key`, `tenant` or `route`). On a 429 it names the limit that denied; on a 200 it names the limit with the least room left. `X-RateLimit-Limit` and `X-RateLimit-Remaining` refer to that same limit.

## All-or-nothing, in one atomic script

All of a request's limits are checked and updated in **one** Redis Lua script:

1. **Check phase:** read every limit and note any that would be exceeded.
2. If any would be exceeded, deny. Nothing is counted. The reply names the first limit that denied and the longest retry-after among the denying limits.
3. **Update phase** (only if all passed): count the request against every limit.

That's one network round trip per request however many limits apply. A denied request never uses up any limit.

### Why not check the limits one at a time?

Checking one limit after another counts a request against the earlier limits even when a later one rejects it. Measured against real Redis: tenant limit 150/s and key limit 100/s; key A is abusive (500/s), key B is polite (50/s, within every limit); 5 s per strategy, sliding log:

| Strategy | Abusive A | Polite B (wants 50/s) | Tenant total |
|---|---|---|---|
| **All-or-nothing** | 100/s | **49.2/s** | 149/s |
| Tenant first, then key | 100/s | **12 to 13/s** | 113/s |
| Key first, then tenant | 99/s | 49.6/s | 149/s |

With the tenant checked first, each of A's 500 requests per second uses up tenant quota before A's own key limit rejects 400 of them. The tenant runs out on requests that were going to be rejected anyway, B loses three quarters of its traffic, and the tenant only uses 113 of its 150. Checking the key first happened to be safe here, but correctness then depends on which limit runs out first, which changes at runtime. Both orderings also cost two round trips. Doing everything in one atomic step removes the ordering problem.

## Measured across 3 gateway replicas

Four keys each sending 400 req/s: two in `acme` (tenant limit 150/s), one in `globex`, one unassigned; key limit 100/s.

| | acme, worst 1 s span | acme keys, per second | globex / unassigned |
|---|---|---|---|
| Sliding log | **150** | 78 + 69 | 100 / 100 |
| Fixed window | 300 at load start; **75 + 75 = 150** per calendar second | 75 + 75 | 100 / 100 per calendar second |
| Sliding counter | 180 at load start | 98 + 51 | 99 / 99 |

Each algorithm's tenant limit behaves exactly like its key limit, including the edge effects described in [algorithms.md](algorithms.md).

## Limitations

- **Sharing a tenant limit is first-come, not fair.** With a tight tenant limit (120/s), all-or-nothing gave abusive A its full key limit (100/s) and polite B only 20/s, simply because A arrives ten times as often. Fair sharing inside a tenant would need weighted or per-key-share limits.
- **Redis Cluster.** The script touches several keys (`{route:key:…}`, `{route:tenant:…}`, `{route:route}`) that hash to different slots, so it needs a single Redis node or a primary-replica setup. In Cluster mode it would fail with CROSSSLOT. Supporting Cluster would mean grouping limits by slot, giving up atomicity across groups.
- All limits on a route use the route's algorithm.

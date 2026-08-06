# GoBeyond cache profiles

GoBeyond caching is opt-in. A call without `Revalidate` or a named profile is
uncached.

```go
value, err := cache.Load(ctx, cache.Options{
    Name: "catalog.products",
    Args: []any{locale},
    Profile: cache.ProfileUntilInvalidated,
    Tags: []string{"catalog"},
}, cache.JSONCodec[Products](), loadProducts)
```

Named profiles provide a shared vocabulary for application data:

- `ProfileShortLived`: one minute.
- `ProfilePublicRoute`: fifteen minutes.
- `ProfileStaleWhileRevalidate`: five minutes.
- `ProfileUntilInvalidated`: thirty-one days, with tag invalidation as the
  normal freshness boundary and the finite TTL as a recovery guard.
- `ProfileImmutable`: thirty-one days for deployment-scoped values.

Private requests never read or write shared cache entries. Public route
responses carry the active cache generation in `X-GoBeyond-Cache-Generation`
and soft-navigation payloads. A generation change causes the browser router
cache to discard entries when it observes the next fresh response.

Applications should call `cache.RevalidateTag` or `cache.RevalidatePath` from
their own mutation/webhook boundary. The platform invalidation workflow uses
an idempotency key and publishes the resulting generation to the active edge
route. Edge purge is an optimization; generation changes are the correctness
boundary.

For hosted applications, `cache.InvalidateRemote` submits that event to the
platform invalidation endpoint. The application still owns webhook signature
verification; GoBeyond owns idempotency, generation publication, and retries.

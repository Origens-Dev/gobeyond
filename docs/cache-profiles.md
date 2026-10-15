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

## Hosted invalidation

`cache.InvalidateHosted` is available only in a compatible hosted runtime that
provides the capability. Applications must authenticate and validate incoming
webhooks before calling it.

```go
result, err := cache.InvalidateHosted(ctx, cache.HostedInvalidationOptions{
    IdempotencyKey: deliveryID,
})
```

The request contains only `idempotency_key`; its trimmed value must be nonempty
and at most 200 bytes. Reuse the same key when retrying the same event, and use
a different key for a new event. The runtime determines the invalidation scope;
applications cannot select another environment or supply host lists.

A successful call reports durable acceptance, not completed cache propagation.
The result status may be `accepted`, `started`, `already_started`, or `existing`.
Consumers must not treat any of these as proof that fresh content is visible.

The call returns an error for invalid input, an unavailable capability, request
failure or timeout, a non-202 response, or an invalid acceptance response. A
request uses a ten-second timeout and also respects context cancellation. An
error after submission may leave acceptance uncertain; retry with the same key.
There is no automatic fallback to a credential-based API.

The transport uses the existing `GOBEYOND_HOST_REPORT_SOCKET` configuration.
Local environments without that capability return an error. The older
`cache.InvalidateRemote` API is separate and requires its own configuration.

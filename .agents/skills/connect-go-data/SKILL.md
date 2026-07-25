---
name: connect-go-data
description: >
  Connect an existing GoBeyond React page to request-time Go data. Use when
  adding app/<route>/page.go, page.schema.ts, PageContext, metadata,
  redirects, not-found results, auth, or dynamic SEO pages.
user-invocable: false
---

# Connect Go data to a page

Use this skill when the website already exists and needs request-time data,
authorization, a request-specific status, or SEO metadata.

1. Keep the React view in `app/<route>/page.tsx`; add or refine its
   `page.schema.ts` contract first.
2. Add the sibling `app/<route>/page.go`. `page.tsx` alone is static; this
   file opts the route into request-time Go behavior.
3. Run generation before compiling. The generated-safe runtime package lives
   at `internal/gobeyondgen/routes/<route-ID>/`; runtime code imports that
   package, never an `app/` source directory.
4. Return the generated props type, not maps or untyped JSON. Derive data from
   `ctx.Params`, `ctx.Request`, and context values.
5. Return explicit `OK`, `NotFound`, or `Redirect` results. Do not use a 200
   error page for missing SEO content.
6. Resolve complete metadata before returning. Canonicals use the configured
   public origin, never the request Host header.
7. Treat cookies or authorization as private/no-store. Public body content may
   not vary by authentication cookie. `cache.Load` and props ISR skip reads on
   private requests; never store secrets or viewer-specific data in cached
   values.
8. For shared loader work, use `cache.Load` with a deploy-unique `Name`,
   positive `Revalidate`, and invalidation `Tags`. Use `cache.Memo` for
   per-request deduplication inside one loader.
9. To reuse a route's loaded props across requests, set
   `definePage({ revalidate, tags })` in `page.schema.ts` beside a sibling
   `page.go`. Unknown keys are rejected. Set `gb.CachePolicy` on the loader
   result separately for HTTP caching; keep both windows aligned deliberately.
10. Keep indexable loader data in portable initial markup. A reported
   `use client` downgrade or explicit `ClientOnly` region may have an optional
   fallback, but it cannot be the only copy of critical route content.

```go
func Page(ctx *gb.PageContext) (gb.PageResult[Props], error) {
  product, found := lookup(ctx.Params["slug"])
  if !found { return gb.NotFound[Props](notFoundMetadata()), nil }
  return gb.OK(product, metadataFor(product)), nil
}

// Shared loader data (optional):
// product, err := cache.Load(ctx.Context, cache.Options{
//   Name: "catalog.product", Args: []any{ctx.Params["slug"]},
//   Revalidate: 60 * time.Second, Tags: []string{"products"},
// }, cache.JSONCodec[Product](), fetchProduct)
```

```ts
export const page = definePage({
  props: schema.object({ title: schema.string() }),
  revalidate: 60,
  tags: ["products"],
})
```

```bash
pnpm generate
pnpm generate:check
pnpm routes
pnpm test
pnpm build && pnpm preview
```

Verify the no-JavaScript response contains the title, canonical URL, body
content, links, and JSON-LD. See `docs/guides/connect-go-data.md`,
`docs/architecture.md` (request-time caching), and `$debug-contracts` for type
or hydration failures.

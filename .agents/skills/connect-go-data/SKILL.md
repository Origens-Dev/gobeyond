---
name: connect-go-data
description: >
  Connect an existing GoBeyond React page to request-time Go data. Use when
  adding server/pages/<route>/page.go, page.schema.ts, PageContext, metadata,
  redirects, not-found results, auth, or dynamic SEO pages.
user-invocable: false
---

# Connect Go data to a page

Use this skill when the website already exists and needs request-time data,
authorization, a request-specific status, or SEO metadata.

1. Keep the React view in `app/<route>/page.tsx`; add or refine its
   `page.schema.ts` contract first.
2. Add `server/pages/<go-safe-route-key>/page.go`. The key mirrors the route:
   `products/[slug]` becomes `products_slug`.
3. Return the generated props type, not maps or untyped JSON. Derive data from
   `ctx.Params`, `ctx.Request`, and context values.
4. Return explicit `OK`, `NotFound`, or `Redirect` results. Do not use a 200
   error page for missing SEO content.
5. Resolve complete metadata before returning. Canonicals use the configured
   public origin, never the request Host header.
6. Treat cookies or authorization as private/no-store. Public body content may
   not vary by authentication cookie.

```go
func Page(ctx *gb.PageContext) (gb.PageResult[Props], error) {
  product, found := lookup(ctx.Params["slug"])
  if !found { return gb.NotFound[Props](notFoundMetadata()), nil }
  return gb.OK(product, metadataFor(product)), nil
}
```

```bash
pnpm generate
pnpm generate:check
pnpm routes
pnpm test
pnpm build && pnpm preview
```

Verify the no-JavaScript response contains the title, canonical URL, body
content, links, and JSON-LD. See `docs/guides/connect-go-data.md` and
`$debug-contracts` for type or hydration failures.

# Connect Go data

Keep the view in `app/`; add Go only at its request-time boundary.

```text
app/products/[slug]/page.tsx
app/products/[slug]/page.schema.ts
app/products/[slug]/page.go
```

The schema describes what React needs. Code generation makes the matching Go
props type and route registry. The build projects the route-owned Go source
into a generated safe package; the loader reads `PageContext`, loads data, and
returns a typed result plus complete metadata. Do not import Go source from
`app/` directly. Put reusable data access or policy in `internal/` packages.

```go
func Page(ctx *gb.PageContext) (gb.PageResult[Props], error) {
  product, ok := findProduct(ctx.Params["slug"])
  if !ok { return gb.NotFound[Props](notFoundMetadata()), nil }
  return gb.OK(product, metadataFor(product)), nil
}
```

For public SEO pages, resolve `lang`, title, description, canonical URL,
Open Graph fields, alternates, JSON-LD, and body props before headers commit.
Missing content must return a real `404`; redirects must preserve their
temporary/permanent status. Use the configured public origin for canonicals,
not request headers.

For request-time Open Graph images, derive an absolute HTTPS URL from
`ctx.PublicOrigin` and provide known dimensions. Add generated favicon and
Apple touch paths through `gb.Metadata.Icons`. See
[Icons and social sharing](icons-and-social.md) and
[Configure the public origin](public-origin.md).

Cookies, authorization, or personalization make a response private and
`no-store`. Do not vary an indexable body by authentication state. Cache reads
skip private requests; never store secrets or viewer-specific payloads in
`cache.Load` or props ISR entries.

For shared loader work, use `cache.Load` with a deploy-unique `Name`, positive
`Revalidate`, and invalidation `Tags`. Use `cache.Memo` for per-request
deduplication inside one loader.

To reuse loaded props across requests, declare origin ISR in the schema beside
`page.go`:

```ts
export const page = definePage({
  props: schema.object({ title: schema.string() }),
  revalidate: 60,
  tags: ["products"],
})
```

Unknown `definePage` keys are rejected. Schema `revalidate` controls origin
props reuse; set `gb.CachePolicy` on the loader for HTTP caching and keep both
windows aligned deliberately (for example `gb.PublicRevalidate(60*time.Second, …)`
beside `revalidate: 60`).

Run `pnpm generate --check`, `pnpm test`, then inspect the generated document with
JavaScript disabled before shipping.

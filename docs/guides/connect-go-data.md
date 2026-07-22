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

Cookies, authorization, or personalization make a response private and
`no-store`. Do not vary an indexable body by authentication state. Run
`pnpm generate --check`, `pnpm test`, then inspect the generated document with
JavaScript disabled before shipping.

# GoBeyond SEO acceptance site

This website-first fixture defines the crawler-visible contract for the MVP.
The pages intentionally stay within GoBeyond's portable React profile: typed
props, intrinsic markup, project-owned components, conditions, and keyed lists.

The `expected/` documents are acceptance fixtures, not hand-maintained server
templates. Once the compiler and Go runtime are integrated, the conformance
harness must replace their input with actual HTTP responses and compare those
responses against the same assertions in `test/no-js-seo.test.mjs`.

Run the dependency-free no-JavaScript contract checks with:

```bash
pnpm --filter @go-beyond/example-seo-site test
```

The fixture demonstrates articles, products, crawlable pagination, locations,
localized URLs, an authenticated noindex page, a typed action declaration,
robots.txt, and sitemap.xml.

Its route source is co-located: a static route has `page.tsx` only, while each
request-time route adds a sibling `page.go`; the product mutation lives beside
its declaration in `app/products/[slug]/actions.go`. Shared Go helpers live in
`internal/site`; optional request middleware is website-root `middleware.go`.
`robots.txt` / `sitemap.xml` come from `app/robots.ts` and `app/sitemap.ts` (build-time Metadata files). The runtime imports
generated-safe projections of route files, never the `app/` source directories
directly.

It also imports a real stylesheet and ships product/Open Graph image fixtures.
`gobeyond build` links the content-hashed CSS in static and dynamic documents,
copies the images to `dist/static`, and records their public URLs in
`dist/deploy/route-trie.json` for CloudFront/S3 routing.

For local Temporal durables, see `examples/durables-site`.

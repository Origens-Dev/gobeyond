---
name: add-page
description: >
  Create a website-first GoBeyond route in app/. Use when adding page.tsx,
  layout.tsx, page.build.ts, route groups, or dynamic route segments such as
  [slug]. Covers static React pages, portable initial markup, and verification.
user-invocable: false
---

# Add a GoBeyond page

Use this skill when the request is primarily a website page. Start in `app/`;
do not create a Go package unless the page needs request-time behavior.

1. Pick the route directory under `app/`: `app/products/[slug]/page.tsx` maps
   to `/products/[slug]`; `(marketing)` affects organization but not the URL.
2. Add `page.schema.ts` when the page receives props. Keep props serializable.
3. Write `page.tsx` with semantic, crawler-visible HTML. Put title, headings,
   links, image `src`/`alt`, and primary copy in the initial markup.
4. Add `page.build.ts` only for build-time data or `generateStaticParams`.
   Build-only code may use secrets, but its returned props are public.
5. Use project-owned components and the portable React profile. The compiler
   still attempts portable compilation below `use client`; unsupported render
   code may downgrade only at the nearest marked boundary and is reported in
   the build output. Explicit `<ClientOnly>` remains available, and its
   fallback is optional.
6. Run generation and verify static output before considering Go.
7. Route caching (`definePage({ revalidate, tags })`) requires a sibling
   `page.go`; see `$connect-go-data` for origin ISR and data-cache patterns.

`gobeyond add page <route>` creates a portable `page.tsx` and empty
`page.schema.ts` scaffold; `page.tsx` alone is a static route. `gobeyond add
dynamic <route>` additionally creates a typed sibling
`app/<route>/page.go`. Run `gobeyond generate` to emit its contract and a safe
runtime projection under `generated/routes/<route-ID>/`. The runtime
imports that projection by generated route ID, never the source directory below
`app/`.

```tsx
// app/articles/[slug]/page.tsx
export default function Article({ title, body }: { title: string; body: string }) {
  return <article><h1>{title}</h1><p>{body}</p></article>
}
```

```bash
pnpm generate
pnpm routes
pnpm test
pnpm build
```

Write indexable content portably so it remains in the Go response. Optional
client-only fallbacks are progressive enhancement, not the only copy of
critical content. See `docs/guides/add-page.md` and `$connect-go-data` when
build-time data is insufficient.

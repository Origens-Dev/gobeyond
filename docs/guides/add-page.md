# Add a page

Start a feature in `app/`. A GoBeyond page is a React component first; Go is
not a second templating language.

Start with the scaffold rather than an empty directory:

```bash
gobeyond add page articles/[slug]
```

It creates `page.tsx` and its authored static-route `page.schema.ts`. The
command is idempotent for an unchanged scaffold and refuses to overwrite either
file after you edit it.
`page.tsx` alone is a static route. Use `gobeyond add dynamic articles/[slug]`
when the page also needs request-time Go data; it adds a sibling
`app/articles/[slug]/page.go`. Define its `Props` type using your app's normal
Go domain packages, then run `gobeyond generate`. Generation writes the
ignored `page.schema.ts` consumed by the React page, so do not hand-edit that
file. The build projects route source into a generated Go package that the
runtime registers by route ID; never import an `app/` directory directly.

```text
app/articles/[slug]/page.tsx        React content and composition
app/articles/[slug]/page.schema.ts  generated React component props (dynamic)
app/articles/[slug]/page.build.ts   optional build-time data
app/articles/[slug]/page.go         optional request-time data and metadata
```

Add `page.build.ts` when a page needs build-time props or
`generateStaticParams`. Its output is public because it is
serialized into generated HTML and route data.

Keep initial markup portable: use intrinsic tags and project components, props,
conditions, and keyed lists. The title, headings, links, image fallbacks, and
main copy belong in the initial HTML. The compiler attempts portable
compilation inside `use client` modules and reports any exact call site it must
downgrade. Explicit `ClientOnly` is also available with an optional fallback;
an empty client boundary cannot contain the only copy of an SEO page.

```tsx
export default function Article({ title, summary }: Props) {
  return <article><h1>{title}</h1><p>{summary}</p></article>
}
```

Run `pnpm generate`, `pnpm routes`, `pnpm test`, and `pnpm build`. When the
page needs a session, current inventory, or request-specific metadata, follow
[Connect Go data](connect-go-data.md).

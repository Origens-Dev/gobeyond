# Add a page

Start a feature in `app/`. A GoBeyond page is a React component first; Go is
not a second templating language.

Start with the scaffold rather than an empty directory:

```bash
gobeyond add page articles/[slug]
```

It creates both `page.tsx` and `page.schema.ts`. The command is idempotent for
an unchanged scaffold and refuses to overwrite either file after you edit it.
Use `gobeyond add dynamic articles/[slug]` when the page also needs
request-time Go data. In addition to those two files, it creates a typed
`server/pages/articles_slug/page.go` that imports the deterministic generated
route contract. Run `gobeyond generate` before compiling the Go server, then
register the loader in `gbruntime.Config.Pages` with the generated route ID.

```text
app/articles/[slug]/page.tsx        React content and composition
app/articles/[slug]/page.schema.ts  serializable component props
app/articles/[slug]/page.build.ts   optional build-time data
```

`page.tsx` alone creates a static route. Add `page.build.ts` when a page needs
build-time props or `generateStaticParams`. Its output is public because it is
serialized into generated HTML and route data.

Keep initial markup portable: use intrinsic tags and project components, props,
conditions, and keyed lists. The title, headings, links, image fallbacks, and
main copy belong in the initial HTML. A browser-only map or chart must use
`ClientOnly` with a useful fallback; it cannot contain the only copy of an SEO
page.

```tsx
export default function Article({ title, summary }: Props) {
  return <article><h1>{title}</h1><p>{summary}</p></article>
}
```

Run `pnpm generate`, `pnpm routes`, `pnpm test`, and `pnpm build`. When the
page needs a session, current inventory, or request-specific metadata, follow
[Connect Go data](connect-go-data.md).

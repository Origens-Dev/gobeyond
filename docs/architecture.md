# GoBeyond MVP architecture

## Core invariant

For a supported component, the Go renderer, React's server renderer used by
conformance tests, and React's first hydration render must produce the same
browser-normalized DOM.

The compiler rejects render-time JavaScript outside the portable profile. It
does not silently fall back to client rendering.

## Source boundaries

- `app/`: website routes and React components.
- `server/`: Go loaders, actions, APIs, layouts, and middleware.
- `server/internal/gobeyondgen/`: committed generated contracts and registries.
- `render-plans/`: versioned language-neutral render artifacts packaged with the server.

The same URL is represented by a React route directory and, only when needed,
a Go-safe server key:

```text
app/products/[slug]/page.tsx       /products/[slug]
server/pages/products_slug/page.go request-time loader
```

Generated stable IDs join those trees; filesystem paths and Go package names
are never inferred from one another at runtime.

## Render-plan contract

The canonical JSON Schema is `contracts/render-plan.schema.json`. The MVP uses
JSON for inspection and conformance. A compact binary encoding can be added
later without changing the semantic plan version.

## Build and runtime

At build time, TypeScript and TSX are parsed, validated, and compiled into a
render plan and a browser bundle. Static build data is schema-validated,
rendered through the Go renderer, and packaged under `runtime-data/` for the
same Go binary to serve during soft navigation. The build ID fingerprints the
source tree and finalized plans, contracts, route modules, static props, and
pinned React version.

At request time, Go resolves middleware and a typed loader, renders metadata
and body HTML, embeds the exact hydration props, and references immutable
browser assets. No JavaScript executes on the server.

In development, one stable public listener proxies to a generated Go server.
The watcher records content digests for build inputs and classifies their
impact. An edit to an existing Go file below the website's `server/` directory
reuses the current render plans, contracts, static documents, browser assets,
and compatibility build ID while compiling a replacement Go executable.
Additions, deletions, frontend files, schemas, route structure, framework
runtime files, and ambiguous changes take the complete staged-build path.
Complete development rebuilds reuse the prepared portable compiler unless its
own source tree changed.
After either candidate passes `readyz`, the proxy atomically changes targets,
emits a browser reload event, and gracefully shuts down the prior process. A
failed candidate never replaces the last working server.

## Production request flow

```text
CloudFront document request
  -> static HTML in private S3, when a route has build props only
  -> Go runtime, when a route has page.go or request-time middleware

Go runtime
  -> strip reserved headers and validate host
  -> apply ordered middleware and bounded rewrites
  -> resolve route, status, cache policy, metadata, and typed props
  -> interpret the packaged rendering plan
  -> emit one complete non-streamed HTML document
```

The renderer loads plans and packaged static props once at startup. It does
not fetch plans or page data from S3 per request. Browser assets and manifests
are immutable and keyed by build ID.
Vite-emitted stylesheets are discovered after bundling and recorded in the
runtime/browser manifests; both static documents and dynamic page
registrations use those exact URLs. Copied `public/` files are listed in the
deployment route trie so the edge can select the static origin explicitly.

## Browser protocol

Public URLs always return documents. Same-origin links whose route patterns
are in the generated browser registry use reserved build-aware data endpoints;
external, modified, download, error, and redirect navigation remains a normal
document load. Successful navigation updates the React tree, metadata,
history, scroll, focus, and an assistive-technology announcement. A mismatched
build receives `409 build_mismatch` before a loader/action executes; the
browser performs at most one guarded full reload and never retries a mutation.

Go's output is a compatibility implementation of the pinned initial React
render, not a general HTML approximation. Changes to escaping, attributes,
form state, SVG, tables, whitespace, or scalar serialization require a React
reference case and a hydration case.

## SEO boundary

Indexable results must resolve their status, language, title, description,
canonical URL, robots policy, Open Graph/Twitter fields, absolute social
images, alternates, JSON-LD, and body props before response headers are
committed. Canonicals use configured public origin, never request Host. Private
or authenticated output is `noindex` and `private, no-store`; cookie-bearing
responses cannot be stored in a public cache.

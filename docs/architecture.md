# GoBeyond MVP architecture

## Core invariant

For a supported component, the Go renderer, React's server renderer used by
conformance tests, and React's first hydration render must produce the same
browser-normalized DOM.

The compiler always attempts the portable profile, including in `use client`
modules. Unsupported render behavior can downgrade only at the nearest marked
client boundary. The compiler emits a deterministic source-location record for
each downgrade, and Vite transforms only those recorded call sites. Unmarked
unsupported code and all parse, type, module, contract, and internal failures
remain fatal.

## Source boundaries

- `app/`: website route source. `page.tsx` is a static route; a sibling
  `page.go` opts that route into request-time Go. Route-owned `actions.go` and
  `app/api/**/route.go` stay beside the route they serve.
- `internal/`: reusable Go services, policy, and integrations that are not
  owned by one route.
- `internal/gobeyondgen/`: committed generated contracts and registries,
  plus ignored safe Go projection packages. The runtime imports projections,
  never source directories below `app/`.
- `render-plans/`: versioned language-neutral render artifacts packaged with the server.

The same URL is represented by a React route directory and, only when needed,
a Go-safe server key:

```text
app/products/[slug]/page.tsx       static /products/[slug]
app/products/[slug]/page.go        request-time loader for /products/[slug]
```

Generated stable IDs join those trees; filesystem paths and Go package names
are never inferred from one another at runtime.

Go import paths cannot contain route brackets. Generation therefore writes an
ignored, marker-protected `go.mod` beside every route as an editor-only package
boundary, then projects authored Go into a safe package under
`internal/gobeyondgen/`. This lets `gopls` diagnose the authored `page.go`
without making `app/products/[slug]` a production import path. Generated module
files are never shipped and user-owned `go.mod` files are never overwritten.

## Render-plan contract

The canonical JSON Schema is `contracts/render-plan.schema.json`. The MVP uses
JSON for inspection and conformance. A compact binary encoding can be added
later without changing the semantic plan version.

## Build and runtime

At build time, TypeScript and TSX are parsed, validated, and compiled into a
render plan, a client-boundary manifest, and a browser bundle. Static build data is schema-validated,
rendered through the Go renderer, and packaged under `runtime-data/` for the
same Go binary to serve during soft navigation. A client-only plan node may
carry portable fallback markup or no fallback. In the empty case Go emits no
markup and the browser wrapper also returns `null` on its first render; an
effect mounts the original component after hydration. The build ID fingerprints the
source tree and finalized plans, contracts, route modules, static props, and
pinned React version.

At request time, Go resolves middleware and a typed loader, renders metadata
and body HTML, embeds the exact hydration props, and references immutable
browser assets. No JavaScript executes on the server.

In development, one stable public listener proxies to a generated Go server.
The watcher records content digests for build inputs and classifies their
impact. An edit to shared Go code under `internal/` can reuse the current
render plans, contracts, static documents, browser assets, and compatibility
build ID while compiling a replacement Go executable. Route-owned Go changes
are projected into generated packages before the candidate is built.
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

Social previews cross the indexing boundary: whenever metadata includes a
social image, its URL must be absolute HTTPS even if the result is `noindex`.
Open Graph can include a structured primary image with dimensions, alt text,
and media type. Browser icon metadata links generated favicon and Apple touch
assets from the static origin. `robots.txt` determines whether a crawler may
fetch the document and image; meta robots directives determine whether the
document should be indexed. These controls are related but not interchangeable.

The build derives 16- and 32-pixel favicons plus a 180-pixel Apple touch icon
from `app/icon.png`. Social images remain authored files under `public/` and
are copied without transformation. Runtime image optimization (`/_gobeyond/image`,
portable `imageSrc()`) is outside this SEO boundary; social previews must not
use it. See [Runtime images](guides/images.md).

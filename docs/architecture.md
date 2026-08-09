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
- `agents/`: compiler-visible agent definitions. An `agents/<id>/agent.go`
  package exports `var Agent = agents.Define(...)` for a typed handler or
  `agents.DefineAI(...)` plus `instructions.md` for a framework-owned model/tool
  loop; direct execution is the default and durable agents opt in explicitly.
- `workflows/`: compiler-visible Temporal workflow and activity definitions,
  grouped by definition folder and resolved logical task queue.
- `generated/`: gobeyond-owned contracts, registries, process mains, and
  ignored safe Go projection packages. The runtime imports projections, never
  source directories below `app/`. Authors write `app/`, `agents/`, `workflows/`, and
  `internal/` only.
- `render-plans/`: versioned language-neutral render artifacts packaged with the server.

The same URL is represented by a React route directory and, only when needed,
a Go-safe server key:

```text
app/products/[slug]/page.tsx       static /products/[slug]
app/products/[slug]/page.go        request-time loader for /products/[slug]
```

Generated stable IDs join those trees; filesystem paths and Go package names
are never inferred from one another at runtime.

For request-time routes, the serializable boundary is authored in Go:
`type Props struct { ... }` plus an optional `gb.PageConfig`. Generation derives
the ignored sibling `page.schema.ts` that React imports for `InferPageProps`.
This keeps domain types (CMS, database DTOs, and application policy) in the
application's Go packages instead of under a framework namespace.

Go import paths cannot contain route brackets. Generation therefore writes an
ignored, marker-protected `go.mod` beside every route as an editor-only package
boundary, then projects authored Go into a safe package under `generated/`.
This lets `gopls` diagnose the authored `page.go` without making
`app/products/[slug]` a production import path. Generated module files are
never shipped and user-owned `go.mod` files are never overwritten.

## Render-plan contract

The canonical JSON Schema is `contracts/render-plan.schema.json`. Inspection and
conformance may use JSON. Runtime packaging uses immutable pack containers
(`.gbp` / `.gbs`) with per-record `json+zstd` payloads; a compact binary record
codec can be added later via `recordCodec` without changing the semantic plan
version. See `docs/adr/004-lazy-route-residency.md`.

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

The renderer opens local plan/static packs at startup and decodes records on
demand into a bounded residency cache. It does not fetch plans or page data
from S3 per request. Browser assets and manifests are immutable and keyed by
build ID. Process shutdown remains the definitive full memory reclaim.
Vite-emitted stylesheets are discovered after bundling and recorded in the
runtime/browser manifests; both static documents and dynamic page
registrations use those exact URLs. Copied `public/` files are listed in the
deployment route trie so the edge can select the static origin explicitly.

Optional request middleware is a website-root `middleware.go` exporting
`Middleware() []gbmiddleware.Rule`. Omit the file when unused. Cache wiring and
`ctx.PublicOrigin` are owned by the generated registry/runtime; `internal/` is
for ordinary app code. Next-compatible Metadata files (`robots`, `sitemap`, `manifest`, icons, Open Graph / Twitter images) live under `app/` and are materialized into `dist/static` at build time. `public/` remains a generic static escape hatch.
Process mains are generated under `generated/cmd/{site,workflows}`. Durable
authors define workflow and activity packages under `workflows/`; the durable
runtime artifacts intentionally remain worker-shaped (`dist/workers/` and
`dist/deploy/workers.json`) because they describe Temporal poller processes,
not the authored source tree.

When the Go process serves origin static files itself (local preview, or an
origin without CloudFront in front), wrap the server with
`runtime.StaticFiles(directory, handler)` (or `StaticFilesFromEnv`). It serves
`buildpaths.IsStaticArtifact` paths and existing public files from
`GOBEYOND_STATIC_DIR`, sets `Cache-Control: public, max-age=31536000, immutable`
only for content-addressed `/_gobeyond/builds/...` artifacts, and gzip-compresses
JS/CSS/SVG/JSON/HTML/source maps when the client accepts gzip—matching document
and API compression. Non-hashed `public/` files are not marked immutable.

## Request-time caching

GoBeyond ships Next.js-style origin caching without HTML-body persistence.
Each document request still re-renders HTML so every response gets its own CSP
nonce and hydration `renderNow`; only loader props, metadata, status, and
result kind may be shared across visitors.

The runtime attaches a `cache.RequestScope` to every document, runtime-data,
API, and action request. `cache.Memo`, `cache.Load`, `cache.LoadRoute`, and
`cache.RevalidateTag` / `cache.RevalidatePath` require that scope on the
context; they do not operate on a bare `context.Context`. The scope holds the
request's privacy flag (computed once from inbound headers), a per-request memo
bag, and a refresh recorder actions use to accumulate invalidation targets.

| Surface | Role |
| --- | --- |
| `cache.Memo(ctx, key, fn)` | Request-scoped deduplication (React `cache()` analogue). |
| `cache.Load(ctx, Options{…}, codec, fn)` | Shared data cache keyed by deploy prefix, build ID, name, and args. |
| `cache.LoadRoute(…)` | Props ISR keyed by route ID, path, raw query, and public origin. |
| `cache.RevalidateTag` / `cache.RevalidatePath` | Bump tag versions, drop matching L1 entries, and record paths/tags on the scope. |
| `gb.PageConfig` | Origin props ISR window and invalidation tags for a Go-owned page payload; generation writes them into `page.schema.ts`. |
| `gb.CachePolicy` on loader results | HTTP `Cache-Control` only; not inferred from `PageConfig.Revalidate`. |

`PageConfig.Revalidate` and loader `gb.CachePolicy` are separate knobs. When both
apply, keep them aligned deliberately—for example
`gb.PublicRevalidate(revalidate, k*revalidate, staleIfError)` beside
`gb.PageConfig{Revalidate: 60, …}`. The runtime cannot detect an accidental
mismatch from an omitted policy versus an explicit `private, no-store`.

Privacy is fail-closed and shared across HTTP headers and every cache layer.
`cache.IsPrivateRequest` gates reads on Cookie, Authorization,
and `X-Gobeyond-Auth-Context`. `X-Origens-Oidc-Token` is platform workload
identity, not viewer identity, so it does not make an otherwise-public response
private. Hosting layers must strip inbound copies before injecting a trusted
workload token. `cache.IsPrivateResponse` also inspects `Set-Cookie` on the
loaded result. Private requests skip cache reads; non-OK results and
cookie-minting responses are never written. Actions may still call
`RevalidateTag` / `RevalidatePath` from authenticated requests.

Successful actions return a frozen envelope:
`{ apiVersion, buildId, data, refresh?: { paths, tags } }`. The client helpers
`postAction` / `runAction` parse it and, when `refresh.paths` is present,
re-fetch the current route's runtime JSON through `refreshNavigation` (no
history change), which only re-renders the mounted route if it matches one of
those paths. Either way, `refresh` invalidates the client Router Cache
(`packages/react/src/router-cache.ts`): matching entries when `paths` is
given, the whole cache otherwise. That cache is in-memory, keyed by
path+search, and only stores `mode: "public"` soft-nav payloads, for a TTL
taken from the response's `CachePolicy` (`maxAge`/`sharedMaxAge`) and capped
at 30s; prefetch (hover/focus) warms it ahead of navigation. Soft navigation
still replaces props/metadata only; it does not refresh hydration `renderNow`.

Store tiers are an in-process L1 (`cache/memstore`) and an optional shared L2
(`cache/redisstore`, ElastiCache Serverless Valkey in the AWS reference). Keys
include `{deployPrefix}/{buildID}/…`; the deploy prefix is the tenant boundary
when Redis is shared. There is no application-level encrypt/decrypt—do not put
secrets, session tokens, or viewer-specific payloads in cached values. L2 is
opt-in via `GOBEYOND_CACHE_*` environment variables. Apps should call
`cache/openfromenv.OpenFromEnv` once at startup: it builds bounded L1, attaches
Redis when an endpoint is present, starts the tag-bump watcher, and returns a
`Close` for shutdown.

At the edge, CloudFront's dynamic cache key includes cookies, query strings,
and `Authorization`, but not the middleware auth-context header. For those
viewer-authenticated requests, the origin's `Cache-Control: private, no-store`
downgrade is the sole edge-cache isolator. Workload identity does not identify
the viewer or vary rendered content. See `infra/opentofu/README.md` for
optional Valkey wiring and the edge boundary.

## Browser protocol

Public URLs always return documents. Same-origin links whose route patterns
are in the generated browser registry use reserved build-aware data endpoints;
external, modified, download, error, and redirect navigation remains a normal
document load. Successful navigation updates the React tree, metadata,
history, scroll, focus, and an assistive-technology announcement. Nested
`app/layout.tsx` components are composed outermost→innermost around the page
on both SSR and hydration; soft navigation keeps shared layout module
identities mounted and swaps only diverging layout segments plus the page.
Apps can subscribe to soft-navigation lifecycle events (`start` / `success` /
`error`) with `subscribeNavigation()` from `@go-beyond/react/browser` (usable
from a layout island even when the generated client entry discards bootstrap's
return value). A mismatched
build receives `409 build_mismatch` before a loader/action executes; the
browser performs at most one guarded full reload and never retries a mutation.

Go's output is a compatibility implementation of the pinned initial React
render, not a general HTML approximation. Changes to escaping, attributes,
form state, SVG, tables, whitespace, or scalar serialization require a React
reference case and a hydration case.

## Protected React APIs

Some idiomatic React APIs are recognized from `react` imports so first paint can
stay in the Go plan. The compiler registry lives in
`packages/compiler/src/protected-apis.ts`; keep this table, the compiler README,
and the root README in sync when shipping a new API. Every new API needs a
compiler success case, a failure/diagnostic case, and a Vite/hydration case when
the strategy is `rewrite`.

| Technique | Examples | Browser rewrite? |
| --- | --- | --- |
| Bake into plan | `useState` / lazy init, `useRef` (`.current`), `useMemo` / `useCallback`, `useReducer` init, `useContext`+Provider, keyed `Fragment`, static `Children.*`, static `createElement` / limited `cloneElement`, `defaultProps`, presentational class `render()` + baked `this.state`, `usePathname` / `useRoute` | Usually no |
| Call-site identity | `useId()` | Yes (skip for nested map inlines whose plan already holds the parametric id) |
| Transparent wrapper | `<Suspense>` children only; `<Columns>` → styled `div` | No (not streaming) |
| Render snapshot | `new Date().get*()` / `getUTC*()` | Yes → `renderSnapshotDate()` + hydration `renderNow` |
| Reject with guidance | `React.lazy`; unsupported hooks (`GB1086`), array methods (`GB1087`), arbitrary calls (`GB1088`) | Use `ClientOnly` or `use client` |

`useId` ids are **span-stable** (`gb-<spanHash>-<n>`), not route-scoped, so a
shared module hydrates the same string on every route. Multiple inlines of one
span use a sequence factory. Inside a keyed `.map` the id is parametric
(`prefix + String(key)`). Nested components rendered from `.map` bake the same
parametric plan expression and mark the Vite site `skipViteRewrite` (the parent
key is out of scope in the child module). Conditional / loop hook calls emit
`GB1085` (parametric map `useId` is the intentional exception).

Portable control flow includes statement-level `if` / early `return` desugared
into nested `conditional` plan nodes. Dynamic indexing (`items[i]`) uses plan
expression kind `index` (missing/OOB → null). `.filter(pred).map` and
`.slice(a,b).map` lower into `each` with an optional `when` predicate.

Presentational `React.Component` / `PureComponent` classes compile when
`render()` is portable (`this.props` / baked `this.state`). Diagnostics name the
unsupported construct (`window`, `setState`, lifecycle-in-render), not “is a
class.” Third-party layout widgets that depend on viewport or browser state stay
client-only. `<Columns>` is an independent portable multi-column layout
primitive that emits real content without JavaScript; see the investigated case
study in [ADR 003](adr/003-masonry-first-paint-spike.md).

Downgrades record `triggerCode` / `triggerConstruct` and suggest wrapping just
that subtree in `ClientOnly`. Rank fixes with `gobeyond report portability` (or
`gobeyond-compile report-portability`) against compiler-project output.

Date getters use one render clock embedded as hydration `renderNow`. Prefer UTC
getters for cross-timezone safety; local getters match when browser and server
zones agree (same class of SSR caveat as Next.js). Format locale-sensitive
`Intl` strings in Go and pass them as props.

Portable forms should use `defaultValue` / `defaultChecked` for first paint;
controlled `value={…}` is fine only when the expression is portable and matches
Go’s markup. Same-module `const` bindings with portable initializers are baked
into the plan (no need to inline string literals for SVG gradient ids and similar);
`let`/`var` and dynamic module initializers remain `GB1068` when referenced.
`useSyncExternalStore` is not registered yet—there is no platform
store in `@go-beyond/react` to map through `getServerSnapshot`.

Streaming Suspense, Error Boundaries, and `useLayoutEffect` stay behind
`ClientOnly` or Go HTTP / route error documents.

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

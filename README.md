# GoBeyond

GoBeyond is an experimental, MIT-licensed framework for React websites with a
Go production server. It compiles a documented portable subset of TSX into a
language-neutral rendering plan. Go combines that plan with build-time or
request-time props to return meaningful HTML, and React 19.2.8 hydrates the
same component tree in the browser.

> GoBeyond generates meaningful, crawler-visible dynamic HTML from its
> documented portable React profile, then hydrates it with a pinned React
> version—without a Node production runtime.

It is not a TypeScript-to-Go translator, a general JavaScript SSR engine, or an
exact Next.js replacement. Node is required for development and builds. The
production server artifact is a Go executable plus rendering plans and
manifests; browser JavaScript, CSS, images, and fonts belong on a CDN.

## Website-first model

Start with the page. Add Go only when the page crosses a request-time boundary.

```text
app/products/[slug]/page.tsx        React content, composition, interaction
app/products/[slug]/page.schema.ts  serializable props contract
app/products/[slug]/actions.ts      browser-visible action contract
app/products/[slug]/page.go         request-time data, status, metadata, cache
app/products/[slug]/actions.go       authorization and mutations
app/api/products/route.go            Go HTTP API
internal/                            shared Go services and policy
```

`page.tsx` is the single source of truth for initial markup. The build produces
both its browser bundle and its Go rendering plan. Developers do not maintain a
second Go template. GoBeyond also creates ignored, managed `go.mod` sidecars in
route folders so `gopls` can type-check names such as `[slug]`; production code
imports only the generated packages under `internal/gobeyondgen/`.

## What the MVP proves

- Cross-file project React components compile to versioned rendering plans.
- Intrinsic HTML/SVG, fragments, props, conditions, keyed lists, forms,
  deterministic initial state, events, opaque effects, and `ClientOnly` have a
  strict portable profile.
- Portable compilation is attempted below `use client`. Unsupported render
  behavior may downgrade only at the nearest marked boundary, is reported in a
  deterministic manifest, and is transformed at that exact browser call site.
  Unmarked unsupported code and non-portability failures remain fatal.
- TypeScript page/action schemas generate deterministic, committed Go types.
- Go produces full metadata, canonical URLs, JSON-LD, semantic body HTML,
  hydration data, real redirects, and real `404` responses.
- Same-origin links use build-aware soft navigation, reconcile SEO metadata,
  restore history/scroll/focus, and fall back to full documents for redirects
  and errors. Stale actions are rejected before user code and never replayed.
- Static build props and metadata are packaged with the Go server and loaded
  once at startup, so middleware-promoted static pages and soft navigation do
  not execute Node or fetch rendering data from object storage.
- The production artifact audit rejects Node/npm executables and dependency
  trees in `dist/server`.

The conformance gate renders the same portable fixture with Go, hydrates it in
a browser-like DOM using pinned React, asserts zero recoverable hydration
errors, and verifies post-hydration interaction.

## Try the repository

Requirements: Go 1.24+, Node 22+, and pnpm 10.33.0.

For full-stack development with automatic Go rebuilds and browser reloads:

```bash
go run ./cmd/gobeyond dev
go run ./cmd/gobeyond dev --port 4000
```

The public address defaults to `http://localhost:3000`. Each change builds a
replacement server on a fresh internal port. GoBeyond switches the stable
proxy only after the replacement passes readiness, then gracefully drains the
old process. Failed builds leave the last working server online and appear in
the browser development overlay. Independent build stages overlap: compiler
preparation runs with the website type-check, and the browser bundle runs with
the Go server build after generated contracts are ready. Editing an existing
Go file under `app/`, `server/`, or `internal/` takes a dependency-aware fast
path: GoBeyond reuses the
unchanged render plans, hydration contract, static documents, and browser
assets, then compiles and swaps only the Go server. Route-owned Go source is
projected into generated packages before its candidate is built; shared Go
code under `internal/` uses the same Go-only path. Structural or frontend
changes automatically fall back to a complete staged build. Development also
reuses the already-prepared portable compiler until the compiler's own source
changes.

```bash
pnpm install
go run ./cmd/gobeyond doctor
go run ./cmd/gobeyond generate
go run ./cmd/gobeyond generate --check
go test ./...
go test ./... -C imageopt/s3
pnpm -r test
go run ./cmd/gobeyond build
./scripts/verify-node-free-server.sh
```

`gobeyond doctor` checks Go/Node/pnpm, verifies each `@go-beyond/{react,schema,compiler,vite}`
package's compiled exports entrypoints exist, and fails on linked version skew
(instead of a later raw `ERR_MODULE_NOT_FOUND`). The nested `imageopt/s3`
module is tested separately so the AWS SDK stays out of the root module graph.

The build emits:

```text
dist/
  static/   # CDN documents and browser assets
  server/   # Go executable, render plans, runtime manifest
  deploy/   # contracts and artifact manifest
```

CSS imported by the browser entry graph is emitted with a content-hashed name.
The build records that exact URL in both static documents and the runtime
manifest, so dynamic Go documents link the same stylesheet. Files under
`public/` are copied unchanged and listed as `staticAssetPaths` in the deploy
route trie for CDN origin routing.

If `app/icon.png` is present, the build also generates 16- and 32-pixel
favicons plus a 180-pixel Apple touch icon in `dist/static` and lists them for
static-origin routing. Social images remain authored files under `public/`.

## Environment variables and CSS tooling

`gobeyond dev` loads `.env`, `.env.development`, `.env.local`, then
`.env.development.local`. `gobeyond build` uses the same order with
`production` in place of `development`. A variable already supplied by the
process always wins; dotenv loading never mutates the CLI process. The resolved
environment is passed to the compiler, Go build, Vite build, and the dev Go
runtime.

Vite receives the resolved environment but exposes only `VITE_*` values to
browser modules. Keep Contentful delivery/preview tokens and all other secrets
unprefixed; static props and generated route data remain public as well.

Vite owns CSS processing through each project's `vite.config.*` and optional
`postcss.config.*`. Tailwind v4 is an opt-in project capability: install
`tailwindcss` and `@tailwindcss/postcss`, add a project-owned PostCSS config,
and import Tailwind from your CSS. `create-gobeyond --tailwind my-site` creates
that setup. GoBeyond has no Tailwind runtime dependency or framework config.

Preview the complete built site (static assets plus dynamic Go pages):

```bash
go run ./cmd/gobeyond preview
```

## Portable React boundary

SEO-critical initial markup may use project-owned components, schema-backed
props, deterministic expressions, typed conditions, and stable keyed maps.
Event handlers and effect bodies stay browser JavaScript and are not executed
by Go. `ClientOnly` is available for genuinely browser-only third-party UI and
its fallback is optional. Keep content required without JavaScript outside an
empty client boundary, or provide a portable fallback explicitly.

Rich HTML is explicit: validate/sanitize it into the schema package's branded
`SafeHTML` value, then render `<SafeHTML as="div" value={body} />`. Plain
strings cannot cross that trust boundary, and the wrapper is identical in the
Go document and the hydrated React tree.

The alpha intentionally defers arbitrary render helpers, streaming, generalized
third-party render adapters, HTML-body caching, WebP image output, production
S3-backed image loading with an applied CloudFront image-cache policy, and
exact arbitrary React SSR compatibility. Props-only origin ISR, the data
cache, request memoization, action refresh, and an in-memory client Router
Cache (public payloads only, TTL capped at 30s) are
available—see [Architecture and runtime boundary](docs/architecture.md#request-time-caching). Nested component default props, scalar ternaries, statically
known JSX spreads, `useMemo` / `useCallback`, lazy `useState`, `useReducer`,
provider-backed `useContext`, transparent `Suspense` children, keyed `Fragment`,
static `Children` helpers, limited `createElement` / `cloneElement`, and React
`useId()` (rewritten to stable call-site ids via the compiler + Vite plugin;
parametric under `.map`, including nested inlines) are portable. Zero-arg
`new Date().get*()` / `getUTC*()` use the render-snapshot clock (`renderNow` +
Vite `renderSnapshotDate()`). Prefer form `defaultValue` / `defaultChecked` for
first paint. Same-module portable `const` bindings are baked into the plan.
Local and preview servers can use
`imageSrc()` with `GOBEYOND_STATIC_DIR`; see
[Runtime images](docs/guides/images.md).

## Documentation

- [Architecture and runtime boundary](docs/architecture.md)
- [Add a React page](docs/guides/add-page.md)
- [Connect request-time Go data](docs/guides/connect-go-data.md)
- [Configure fixed or request-resolved public origins](docs/guides/public-origin.md)
- [Configure icons and social sharing](docs/guides/icons-and-social.md)
- [Optimize runtime images](docs/guides/images.md)
- [Add a typed action](docs/guides/add-action.md)
- [Add a Go API](docs/guides/add-api.md)
- [Debug contracts and hydration](docs/guides/debug-contracts.md)
- [AWS CloudFront, S3, ALB, and ECS reference](docs/guides/aws-reference.md)
- [SEO acceptance site](examples/seo-site/README.md)
- [Implementation evidence ledger](docs/plans/implementation-ledger.md)

## Status

This is an MVP implementation and compatibility experiment, not a stable
release. React compatibility is deliberately pinned to 19.2.8. Expanding the
portable profile requires new compiler, Go-renderer, browser-normalization, and
hydration conformance cases—not an undocumented compatibility promise.

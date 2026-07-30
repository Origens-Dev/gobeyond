# Changelog

GoBeyond follows semantic versioning. The portable React, render-plan, value
contract, browser payload, and deployment manifests are versioned compatibility
surfaces; alpha releases may revise them with explicit changelog entries.

## 0.1.0-alpha.15 - 2026-07-29

- Skip Metadata materialization when the site has no `app/` Metadata files, so
  builds do not require the alpha.14 `materialize-metadata` compiler subcommand
  unless those files are present.

## 0.1.0-alpha.14 - 2026-07-29

- Drop required `internal/site` hook surface. `internal/` is for app code.
- Optional website-root `middleware.go` exporting `Middleware()`; omit when unused.
- Generated registry opens cache via `openfromenv` itself; loaders use `ctx.PublicOrigin`.
- Mirror Next.js Metadata file conventions under `app/` (`robots`, `sitemap`,
  `manifest`, `favicon`, `icon`, `apple-icon`, `opengraph-image`, `twitter-image`),
  materialized at build time by `@go-beyond/compiler` into `dist/static`.
- `public/` remains a generic static escape hatch; the same URL from `app/` and
  `public/` is a hard error.
- Add `@go-beyond/react/og` (`ImageResponse`) for build-time metadata images.

## 0.1.0-alpha.13 - 2026-07-29

- Move gobeyond-owned projections from `.generated/` to `generated/`.
  Go ignores directories whose names start with `.`, so import paths like
  `module/.generated/routes` break normal package discovery and site builds.

## 0.1.0-alpha.12 - 2026-07-29

- Publish `@go-beyond/workflows` via OIDC trusted publishing (package now exists on npm).
- Document Worlds as `@origens-dev/temporal`.

## 0.1.0-alpha.11 - 2026-07-29

- Add `workers/**/durables.go` authoring, `{workerId}__{environment}` task queues,
  `adapters/temporal` worker process lifecycle, and `gobeyond.builds/v3` worker
  binary emit (`dist/workers/<id>/gobeyond-worker`).
- Generate site/worker process mains and route projections under `.generated/`;
  authors write `app/`, `workers/`, and optional website-root `middleware.go` (no
  hand-written `server/` tree).
- Add `@go-beyond/workflows` portable client (requires a configured World).
- Point create-gobeyond at the generated site entry and site hooks.

## 0.1.0-alpha.4 - 2026-07-25

- Persist nested layouts across soft navigation: generated browser route modules
  export `page` + outermost→innermost `layouts` instead of a precomposed Route
  wrapper, and the React runtime composes that tree so shared layout module
  identities stay mounted while only diverging segments and the page swap.
- Add soft-navigation lifecycle events (`start` / `success` / `error`) via
  `subscribeNavigation()`, `SoftNavigationOptions.onNavigationStart` /
  `onNavigationSettled`, and the bootstrap controller `subscribe(listener)`.

## 0.1.0-alpha.3 - 2026-07-25

- Expand protected React APIs for portable SSR: bake `useId`, `useMemo`,
  `useState`/`useReducer` initial state, `useContext`/`createContext`,
  `useCallback`, and transparent `Suspense` passthrough into the render plan.
- Bake same-module portable `const` bindings into component environments
  (reject non-portable module bindings with GB1068 when referenced).
- Parametric `useId` under `.map`, span-stable ids across routes, and Vite
  rewrite sequencing for linked packages (realpath-aware matching).
- Decode JSX text/attribute HTML entities in the compiler so Go SSR matches
  Vite/React client semantics (`&hellip;` → `…`).
- Add render-snapshot Date/Intl contract (`renderNow` / `renderLocale` in
  hydration) and Vite rewrites for Date intrinsic call sites.
- Wire `@go-beyond/vite` into create-gobeyond scaffolds by default.
- Add optional `gobeyond.builds/v2` middleware artifact
  (`dist/middleware/gobeyond-middleware`) for listen-mode reverse proxies.

## 0.1.0-alpha.0 - Unreleased

- Move the Go module path to `github.com/Origens-Dev/gobeyond`.
- Rename npm packages to the `@go-beyond/*` scope.
- Add `adapters/lambda` Function URL helper (`Serve` / `Dispatch`) for
  `provided.al2023` site Lambdas that keep `dist/static` on object storage.
- Establish the `gobeyond.render/v1alpha1` portable TSX compiler and Go
  renderer contract.
- Pin React and React DOM to 19.2.8 and add cross-language hydration
  conformance tests.
- Add deterministic route discovery, generated Go page/action contracts,
  dynamic Go documents, middleware, APIs, actions, and build mismatch safety.
- Co-locate route-owned Go with React under `app/`, project it into import-safe
  generated packages, and add managed route modules for bracket-path IDE support.
- Add the SEO acceptance website, Node-free artifact audit, starter generator,
  AWS OpenTofu reference, and website-first contributor documentation.

The alpha does not claim arbitrary React SSR or Next.js compatibility. See the
portable profile and deferred list in the main README before adopting it.

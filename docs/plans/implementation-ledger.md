# GoBeyond implementation ledger

## Current milestone

MVP vertical slice implemented; independent launch-readiness review is in
progress. The compatibility surface remains alpha and frozen at
`v1alpha1`.

## Frozen invariants

- React/React DOM compatibility target: exactly 19.2.8.
- Render-plan semantic version: `gobeyond.render/v1alpha1`.
- The `app/` tree contains no Go packages.
- Generated Go contracts are committed and checked for staleness.
- Rendering plans are packaged beside the Go executable.
- Unsupported render-time JavaScript fails compilation.
- Initial dynamic rendering is non-streaming.

## Milestone 1 evidence

- Go renderer tests cover intrinsic HTML/SVG, conditions, keyed lists, forms,
  nested components, URL/style serialization, contextual escaping, and typed
  safe HTML.
- The cross-language conformance test compiles TSX, renders through Go,
  hydrates with React 19.2.8, reports zero recoverable errors, and exercises a
  click interaction.
- The live compiled Go executable returns an indexable product document with
  canonical metadata and JSON-LD, a real missing-product `404`, and a typed
  `409 build_mismatch` for stale navigation.
- The acceptance site posts a generated action input through the production
  HTTP path and verifies its generated output type, build ID, and origin
  protection; lower-level tests also prove mismatched builds never execute a
  mutation.
- `scripts/verify-node-free-server.sh` passes against `dist/server`; the local
  stripped arm64 server executable is approximately 6.0 MiB.

## Completed vertical slice

- Deterministic route discovery and Go-safe route IDs.
- Graph-aware portable TSX compiler with project component imports, direct
  layouts/children composition, portable diagnostics, and direct schema
  forwarding.
- Versioned render-plan and value-contract JSON Schemas.
- Deterministic generated Go props and action types with stale-output checks.
- Node-free Go page runtime, metadata/document renderer, middleware, rewrites,
  actions, APIs, deadlines, panic recovery, health/readiness, and build-ID
  mismatch handling.
- Pinned React hydration runtime, `ClientOnly`, same-origin soft navigation,
  metadata/history/scroll/focus reconciliation, and guarded reload behavior.
- Content-derived build IDs plus startup-loaded static props and metadata for
  middleware-promoted static documents and browser data navigation.
- Functional, idempotent `gobeyond add page|dynamic|action|api` scaffolding
  with safe refusal rather than overwriting engineer-owned files.
- Eight-route SEO site for articles, products, pagination, locations,
  localization, private account semantics, redirects, robots, and sitemap.
- `create-gobeyond` starter, contributor agent skills, AWS OpenTofu reference,
  CI, generation audit, and production artifact audit. Its clean-room test
  generates contracts, type-checks, builds the Go binary, starts it, and fetches
  meaningful dynamic HTML without a Node production process.

## Repeatable verification

```bash
go run ./cmd/gobeyond generate --check
go test ./...
go test -race ./...
go vet ./...
pnpm -r build
pnpm -r typecheck
pnpm -r test
go run ./cmd/gobeyond build
./scripts/verify-node-free-server.sh
```

OpenTofu validation is separate and requires the `tofu` binary:

```bash
./scripts/verify-opentofu.sh
```

## Known alpha limits

- No Suspense, streaming, ISR, runtime image optimization, parallel routes, or
  intercepted routes.
- No general third-party server-render metadata; browser-only packages require
  `ClientOnly` and a useful fallback.
- No seamless routing of old browser sessions to old Go deployments; stale
  clients use guarded reload and actions are never replayed.
- Conformance currently freezes a useful MVP corpus rather than claiming
  arbitrary React SSR compatibility.

## Pre-launch gates not yet claimed

- The scaffold includes `loading.tsx`, `error.tsx`, and `not-found.tsx`, but
  route-specific boundary-plan selection is not wired yet; HTTP result kinds,
  real redirects, and real `404` semantics are implemented.
- Structured logs, IDs, timings, health/readiness, and graceful shutdown are
  implemented; a public OpenTelemetry adapter and browser hydration-reporting
  endpoint remain launch work.
- The conformance and navigation suites cover hydration and accessibility
  behavior, but the target-hardware p50/p95/p99 report and a real-browser
  axe/LCP matrix have not been published.
- Registry/domain/trademark clearance, signed releases, alpha/beta feedback,
  and production deployment evidence are external launch steps.

## Ownership

- Root orchestrator: protocol, integration, dependency and release surfaces.
- Compiler track: `packages/compiler` only.
- Renderer track: `renderplan`, `renderer` only.
- Conformance track: `packages/react`, `examples/seo-site`, browser fixtures only.

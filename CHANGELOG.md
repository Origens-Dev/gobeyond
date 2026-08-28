# Changelog

## Unreleased

- Stamp `activity.scheduled` / `child.scheduled` SoR events from the workflow
  outbound interceptor when `ActivityOptions` / child options target a sibling
  task queue, so gbhost can `InitWorker` the cold poller before the activity
  sits pending with no WAIT# (ADR 010 cold-sibling schedule wake).
- Resolve AI catalog ids (`openai/gpt-4o-mini`, `google/gemini-2.5-flash`,
  `x-ai/grok-4.6`) through the host-report UDS `POST /v1/ai-proxy` with an
  explicit dummy key so ambient `OPENROUTER_API_KEY` cannot leak onto the
  gateway path.
- Allow `AIConfig.Inference` (`openrouter|vertex|anthropic|bedrock` only) as a
  process-local unmetered BYOK bypass; do not copy it into the agents manifest
  or Temporal input.

## 0.1.0-alpha.49 - 2026-08-19

- Treat explicit `noindex` and `none` robots directives as authoritative
  non-indexable metadata during document validation, including hosted static
  routes whose conservative generated indexability flag is true.

## 0.1.0-alpha.47 - 2026-08-18

- Move authored request middleware into the Go application process and slot;
  add the edge-safe `gobeyond.json` redirect/rewrite policy artifact with
  origin fallback evaluation.
- Replace the former standalone middleware/edge build path in the current V5
  artifact layout while retaining bounded legacy readers for older releases.

## 0.1.0-alpha.46 - 2026-08-14

- expose the HTML document's W3C `traceparent` and `tracestate` to JavaScript
  so same-origin fetches can continue the page request instead of starting a
  new root.

## 0.1.0-alpha.45 - 2026-08-13

- observe Link marker attributes when client-only content upgrades a server anchor;
- keep viewport prefetch working across hydration and dynamically updated links.

## 0.1.0-alpha.44 - 2026-08-13

- add the progressive `Link` component with viewport, hover, and focus code prefetch;
- keep route data and priority image warming opt-in and private to the current tab;
- coalesce Link prefetches with navigation and observe links added after hydration.

## 0.1.0-alpha.43 - 2026-08-12

- Make navigation prefetch code-only by default, with explicit private data
  warming, request coalescing, and priority image warming.
- Project page prefetch and image hints through Go and TypeScript route
  generation.
- Run verification and npm publishing on Blacksmith runners.

GoBeyond follows semantic versioning. The portable React, render-plan, value
contract, browser payload, and deployment manifests are versioned compatibility
surfaces; alpha releases may revise them with explicit changelog entries.

## 0.1.0-alpha.42 - 2026-08-11

- Resolve hosted document and action origins from the platform-admitted request
  host so one long-lived customer runtime can safely serve assigned domains.

## 0.1.0-alpha.41 - 2026-08-09

- Decode percent-escaped claim object-key segments before reconstructing AEAD
  identity, allowing hosted agent workflow IDs to remain opaque even when they
  contain path separators.

## 0.1.0-alpha.40 - 2026-08-09

- Route hosted durable agent execution, approval signals, and cancellation
  through the site-bound GoBeyond host socket. Hosted web sandboxes no longer
  require direct Temporal credentials; local development retains the lazy
  direct Temporal client.

## 0.1.0-alpha.39 - 2026-08-09

- Fixed API-only generated registries so projects with static pages and API
  routes import the GoBeyond handler contract and compile under the v4 output
  layout.

## 0.1.0-alpha.38 - 2026-08-09

- Added per-agent customer-owned durable update stores for durable AI agents.
  Hosted workers compose each exact compiled revision with the slot-private
  host review publisher; local workers continue using only the durable
  store.
- Pinned `go-temporal-ai-sdk` alpha.8 so review events preserve agent,
  conversation, execution, and compiled-revision identity through terminal
  completion and enforce the broker's 64 KiB envelope.

## 0.1.0-alpha.37 - 2026-08-09

- Publish the JavaScript workspace packages at the same release revision as
  the alpha.36 Go conversation-identity contract.

## 0.1.0-alpha.36 - 2026-08-09

- Keep durable agent conversation/session identity separate from each
  execution stream in hosted review events, so live and retained review views
  can group multiple runs without weakening per-run authorization.

## 0.1.0-alpha.35 - 2026-08-09

- Resolve durable agent, tool, and subagent task queues as one validated graph:
  tools inherit their owner agent queue, agents inherit their root queue, and
  authored queue overrides produce explicit poller artifacts.
- Keep realtime agents durable while assigning a unique queue per agent and
  selecting local model/tool activities where allowed. Long-running remote
  boundaries heartbeat through the Temporal AI SDK.
- Emit the cumulative v1alpha2 agent manifest and prompt-free hosted bundle so
  internal builders can discover queues only after compilation.
- Add optional Temporal Worker Deployment name/build identity with a pinned
  default versioning behavior; local development remains unversioned when both
  values are absent.
- Preserve application-owned server composition and middleware by compiling a
  custom `server/cmd/app` target when present.
- Publish hosted review events through the existing slot-private host-report
  socket, carrying the compiled agent identity so the host can validate it;
  workers need no second mount or platform storage credentials.

## 0.1.0-alpha.34 - 2026-08-09

- Replace authored Go middleware with exactly one optional root
  `middleware.ts` or `middleware.js` default export. Build emits a standalone
  `dist/edge-middleware/worker.mjs` module, while `dev` and `preview` execute
  that same bundle before the Go site server.
- Add filesystem agents under `agents/<id>/`, including typed handlers and
  framework-owned Go AI SDK model/tool loops with direct or per-agent Temporal
  durability, native session/SSE APIs, and browser-safe TypeScript transport.
- Replace the authored `workers/` layout with compiler-discovered
  `workflows/<id>/` definitions for workflows, owned activities/subworkflows,
  and standalone activities with deterministic logical task-queue inheritance.
- Generate one Temporal poller binary per resolved task queue and supervise
  those pollers during `gobeyond dev` and `gobeyond preview`, with distinct
  `__local` and `__preview` physical queues.
- Fence durable AI runtimes by finalized build revision, route model and tool
  execution through granular Temporal activities, acknowledge cancellation
  only after dispatch succeeds, and reject approval-gated tools until shared
  interaction delivery is available.
- Reframe the public README and documentation around web, workflow, and agent
  primitives, mark the framework's heavily evolving alpha status, use a
  provider-neutral CDN boundary in the architecture, and retire internal
  ADR/spike/implementation-ledger documents after folding their lasting runtime
  contracts into the architecture guide.
- Add allowlisted remote-image optimization, negotiated WebP delivery, explicit
  cache profiles and invalidation metadata, and a deployment-owned image policy
  that hosted runtimes can consume without project-specific configuration.
- Allow hosted runtimes to serve platform-assigned domains while preserving the
  framework's public-origin and preview robots-policy boundaries.

## 0.1.0-alpha.26 - 2026-08-01

- `@go-beyond/compiler` forwards typed all-required props into portable local
  components, recognizes keyed JSX guarded inside array maps, and lowers
  optional property access through null-safe render-plan lookup.
- Optional or indexed prop spreads remain rejected so server rendering cannot
  silently change default-prop behavior during hydration.

## 0.1.0-alpha.25 - 2026-07-31

- `adapters/temporal`: Dynamo-first wake stamps — RetryPolicy header on
  `ExecuteActivity` → `activity.failed` payload; `ExecuteChildWorkflow` emits
  `child.started`; workflow terminals stamp parent linkage and skip
  ContinueAsNew (ADR 010 interceptor-driven wake).
- `examples/durables-site`: Soft Sleep / long-retry / parent-child dogfood
  workflows for staging canaries.

## 0.1.0-alpha.24 - 2026-07-30

- `adapters/temporal` registers local-activity `ReportSorEvent` and workflow /
  activity interceptors that POST SoR timeline/activity events (including
  terminal `workflow.completed` / `failed` / `canceled`) to
  `/internal/workflows/sor/ingest` via host-report UDS or sealed
  `GOBEYOND_API_URL` (ADR 010 close-loop). Failures are swallowed so SoR never
  fails the workflow task.

## 0.1.0-alpha.23 - 2026-07-30


- `adapters/temporal.Serve` wires a claim-check `PayloadCodec` when
  `GOBEYOND_CLAIM_DEK` is sealed (hosted/preview). Decode expands claim-ref
  Temporal payloads (inline encrypted body) back to author plaintext args so
  workflows/activities see their declared types again (ADR 010).

## 0.1.0-alpha.22 - 2026-07-30

- `adapters/temporal.Serve` starts the worker with `Start()` before any
  fail-closed `Stop()` on namespace probe failure, avoiding the Temporal SDK
  panic `attempted to start a worker that has been stopped before` when the
  probe lost the race with `Run`'s internal start (α.21 overlap path).

## 0.1.0-alpha.21 - 2026-07-30

- `adapters/temporal.Serve` overlaps namespace `ListOpenWorkflow` with worker
  poller start after Dial; unixgram readiness still waits for both (fail-closed
  on probe/worker failure — Dial alone never signals ready).
- Log child-side timings for dial, namespace probe, and worker run-start.

## 0.1.0-alpha.20 - 2026-07-30

- `adapters/temporal.Serve` probes the namespace with `ListOpenWorkflow`
  before readiness (CheckHealth alone is not namespace-scoped on Temporal Cloud).
- Require `GOBEYOND_READINESS_SIGNAL` whenever `GOBEYOND_READINESS_NONCE` is set
  so an empty signal target cannot no-op and fake hosted readiness.

## 0.1.0-alpha.19 - 2026-07-30

- `adapters/temporal.Serve` publishes the hosted unixgram readiness nonce
  (`GOBEYOND_READINESS_SIGNAL` / `GOBEYOND_READINESS_NONCE`) after a successful
  Temporal health check and worker start, and again on `SIGCONT` — matching
  `adapters/listen` so RoleWorker cold-start can complete on gbhost.
- Fail closed on `CheckHealth` before readiness so mTLS "Request unauthorized"
  does not leave a running process that never becomes ready.
- Report worker saturation to gbhost via `GOBEYOND_HOST_REPORT_SOCKET`
  (`/v1/worker-health`) for schedule heartbeats.

## 0.1.0-alpha.18 - 2026-07-30

- **Breaking:** reject root `workers/durables.go`. Authored durables must live
  under `workers/<id>/durables.go` (migrate with
  `mkdir workers/default && mv workers/durables.go workers/default/`). Keep
  `package durables` for the default worker folder — `default` is a Go keyword.
- `adapters/temporal` supports mTLS via `GOBEYOND_TEMPORAL_TLS_CERT` +
  `GOBEYOND_TEMPORAL_TLS_KEY` (`tls.X509KeyPair`, TLS 1.2+). Reject half-set
  PEMs and API key + mTLS together; plaintext / API-key dial unchanged when
  neither TLS pair is set.
- Stamp `"adapter": "temporal"` in `dist/deploy/workers.json` (v1 default).

## 0.1.0-alpha.17 - 2026-07-29

- Rename `@go-beyond/workflows` surface from World jargon to `WorkflowClient`.
  Configure with `workflows.use(createClient())` from `@origens-dev/temporal`.

## 0.1.0-alpha.16 - 2026-07-29

- Keep `create-gobeyond` scaffold dependency pins aligned with the release version.

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

# GoBeyond

GoBeyond is an experimental, MIT-licensed application framework for building
web experiences, durable workflows, and AI agents in one Go application.
React owns interactive UI, Go owns the site and durable runtimes, and Temporal
is an optional durability layer for the definitions that need it.

> [!WARNING]
> GoBeyond is under heavy active development. APIs, filesystem conventions,
> generated artifacts, and hosting contracts can and likely will change before
> a stable release. Pin exact alpha versions and review the changelog when
> upgrading.

> Build the page, the long-running work behind it, and the agent that helps the
> user—without shipping a Node production runtime.

Node is used for development and builds. The site and workflow runtimes are Go
executables; an optional root `middleware.go` runs in the same Go process and
slot as the application. Simple redirects and same-origin rewrites live in
`gobeyond.json`, which can be evaluated by the platform edge and by the Go
origin when the edge is bypassed. Browser JavaScript, CSS, images, and fonts
can also be served from a CDN.

## One project, three primitives

| Primitive | Author in | Use it for | Runtime |
| --- | --- | --- | --- |
| Web | `app/`, optional `middleware.go`, `gobeyond.json` | React pages, typed actions, HTTP APIs, middleware, redirects, rewrites, and request-time data | Go site server, optional platform policy evaluation, and browser assets |
| Workflows | `workflows/` | Durable orchestration and reusable standalone activities | Temporal queue workers |
| Agents | `agents/` | Typed handlers or AI agents with tools and streaming | Direct in the site process, or durable through Temporal |

All three surfaces share ordinary application code under `internal/`:

```text
app/                         web routes, actions, and APIs
middleware.go                optional authored Go middleware in the app slot
gobeyond.json                optional edge/origin redirects and rewrites
agents/<id>/                 one typed or AI agent definition
workflows/<id>/              one workflow or standalone activity definition
internal/                    shared services, integrations, and policy
generated/                   GoBeyond-owned projections and registries
```

The compiler discovers definitions from the filesystem, generates safe Go
registries, and groups durable work by logical task queue. Application code
does not maintain worker mains, provider plumbing, session routes, or Temporal
registration by hand.

### Web

`page.tsx` is the source of truth for initial markup and browser interaction. A
sibling `page.go` opts a route into request-time Go data; `actions.go` adds
typed mutations and `app/api/**/route.go` adds Go HTTP endpoints.

An optional root `middleware.go` defines exactly one `Middleware(next
gb.Handler) gb.Handler` hook. It runs in the same Go process and slot as the
application handlers. `gobeyond.json` carries the smaller redirect/rewrite
policy that the platform edge may evaluate before cache/origin routing and the
Go runtime evaluates again when the edge is bypassed.

```text
app/products/[slug]/page.tsx        React content and interaction
app/products/[slug]/page.go         request-time props, status, and metadata
app/products/[slug]/actions.go      authorization and mutations
app/api/products/route.go           Go HTTP API
```

GoBeyond compiles the documented portable React profile into a
language-neutral rendering plan. Go returns meaningful HTML and React hydrates
the same component tree. It is not a TypeScript-to-Go translator, a general
JavaScript SSR engine, or an exact Next.js replacement.

### Workflows

Each immediate child of `workflows/` defines one workflow or standalone
activity. Workflow-owned activities and child workflows stay inside that
definition folder. Empty task queues inherit from their owner and ultimately
resolve to the logical `default` queue.

```text
workflows/orders/workflow.go
workflows/orders/activities/charge/activity.go
workflows/send-receipt/activity.go
```

Builds emit one Go poller binary per resolved queue. Local and preview modes
supervise those binaries when Temporal definitions are present; GoBeyond does
not start or manage Temporal itself.

### Agents

Each immediate child of `agents/` defines one agent. Typed handlers run
directly by default for low latency. `DefineAI` hides model streaming, tools,
provider binding, and the model/tool loop behind a compiler-visible definition
and an `instructions.md` prompt.

Set `Durable: true` per agent to run model and tool steps as Temporal
activities. Direct and durable agents share the generated session API and the
browser-safe `@go-beyond/agents` client, so durability is an execution choice
rather than a different application contract.

## Try the repository

Requirements: Go 1.24+, Node 22+, and pnpm 10.33.0.

```bash
pnpm install
go run ./cmd/gobeyond doctor
go run ./cmd/gobeyond generate
go run ./cmd/gobeyond dev
```

The public address defaults to `http://localhost:3000`. Development builds a
replacement Go server on a fresh internal port, switches the stable proxy only
after readiness passes, and keeps the last working server online after failed
builds. When middleware exists, development also bundles and runs it in front
of each candidate Go server. Direct agents run with the site process.

When the project contains workflows or durable agents, `dev` also builds and
supervises the required queue workers. They retry while user-managed Temporal
is unavailable. Pass `--no-workflows` to run the site and direct agents without
Temporal pollers.

The durable example includes local Temporal setup and web, workflow, and agent
entry points:

```bash
docker compose -f examples/durables-site/docker-compose.temporal.yml up -d
export GOBEYOND_WEBSITE=examples/durables-site
go run ./cmd/gobeyond dev
```

## Build and verify

```bash
go run ./cmd/gobeyond generate --check
go test ./...
go test ./... -C imageopt/s3
pnpm -r test
go run ./cmd/gobeyond build
./scripts/verify-node-free-server.sh
```

The nested `imageopt/s3` module is tested separately so the AWS SDK stays out
of the root module graph. A build emits:

```text
dist/
  static/    CDN documents and browser assets
  server/    Go site executable, rendering plans, and runtime manifest
  workers/   Go Temporal poller binaries grouped by logical task queue
  deploy/    route, worker, policy, and artifact manifests
```

Preview serves the complete built application and supervises its built queue
workers:

```bash
go run ./cmd/gobeyond preview
go run ./cmd/gobeyond preview --no-workflows
```

## What you can build today

- **Web:** portable cross-file React components render meaningful HTML from Go,
  hydrate without a second template, and support typed Go data, actions, APIs,
  metadata, caching, redirects, `404` responses, and soft navigation.
- **Workflows:** filesystem definitions compile into deterministic workflow and
  activity registrations, inherit logical queues, and run in supervised local,
  preview, and production worker binaries.
- **Agents:** typed and AI definitions expose one session/streaming contract;
  direct execution favors latency while durable execution uses granular model
  and tool activities with exact build-revision fencing.
- **Production:** site and worker runtimes are Go binaries; authored request
  middleware is compiled into the site process, while the small policy artifact
  may also be evaluated by the platform edge. The server artifact audit rejects
  Node/npm executables and dependency trees under `dist/server`.

The web conformance gate renders the same portable fixture with Go, hydrates it
in a browser-like DOM using pinned React, asserts zero recoverable hydration
errors, and verifies post-hydration interaction.

## Web rendering boundary

SEO-critical initial markup may use project-owned components, schema-backed
props, deterministic expressions, typed conditions, and stable keyed maps.
Event handlers and effect bodies stay browser JavaScript. Unsupported render
behavior may downgrade only at an explicit client boundary; unmarked
unsupported behavior and contract failures remain fatal.

Rich HTML crosses an explicit `SafeHTML` boundary. Static props and generated
route data are public and must never contain secrets. Vite exposes only
`VITE_*` environment values to browser modules; unprefixed provider, database,
and CMS credentials remain server-side.

See the architecture and web guides for the complete portability, caching,
metadata, image, and deployment contracts.

## Documentation

Start with the [documentation map](docs/README.md), or go directly to a
primitive:

- [Build agents](docs/guides/agents.md)
- [Build workflows and activities](docs/guides/workflows.md)
- [Add a React page](docs/guides/add-page.md)
- [Connect request-time Go data](docs/guides/connect-go-data.md)
- [Add a typed action](docs/guides/add-action.md)
- [Add a Go API](docs/guides/add-api.md)
- [Add request middleware](docs/guides/middleware.md)
- [Architecture and runtime boundaries](docs/architecture.md)
- [AWS deployment reference](docs/guides/aws-reference.md)

## Status

GoBeyond is alpha software, as the warning above describes. Web compatibility
is deliberately pinned to React 19.2.8. Workflow and agent APIs are evolving,
and hosted persistence and revision retention depend on the selected deployment
adapter.

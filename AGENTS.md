# GoBeyond Development Guide

GoBeyond is application-first, with three equal authored surfaces: React and Go
web routes under `app/`, durable definitions under `workflows/`, and direct or
durable agents under `agents/`. Optional request middleware is one root
`middleware.go` Go handler that runs in the same application process and slot.
Route rewrites and redirects may be declared in `gobeyond.json`; the build
emits them as a validated policy artifact for both edge and origin evaluation.
Ordinary Go services and policy under `internal/` can be shared by all three
without mixing framework-generated plumbing into application logic.

## Always-on guardrails

- Keep route-owned source together in `app/`: `page.tsx` is static by itself;
  add a sibling `page.go` only when the route needs request-time behavior.
- Keep route-specific mutations in a sibling `actions.go` and HTTP endpoints in
  `app/api/**/route.go`. Put reusable Go services and policy in ordinary
  `internal/` packages, never in a second route tree.
- Keep at most one root `middleware.go`. It must use `package middleware` and
  export exactly `func Middleware(next gb.Handler) gb.Handler`. The hook is
  compiled into the application server; it is not a separate middleware slot,
  process, socket, or deployment artifact.
- Keep edge-safe rewrites, redirects, and access conditions in `gobeyond.json`.
  They are platform policy, not a second authored middleware runtime, and the
  same artifact is applied again by the origin when the edge is bypassed.
- Keep durable definitions in `workflows/<id>/workflow.go` or a standalone
  `workflows/<id>/activity.go`. Workflow-owned activities live under
  `activities/<id>/activity.go`; owned subworkflows live under
  `subworkflows/<id>/workflow.go`. Root `workflows/*.go` is rejected. Define
  exported `var Workflow = workflows.Define(...)` or
  `var Activity = workflows.DefineActivity(...)`; generated workers register
  them. Do not import one authored definition package from another; share code
  via `internal/`. TaskQueue is logical (`orders`), while runtime workers use
  `{taskQueueId}__{environment}` (local environment is `local`).
- Keep agent definitions in `agents/<id>/agent.go` with an exported
  `var Agent = agents.Define(...)`. Agent configs and tool, skill, subagent,
  schedule, and channel slots must remain compiler-visible literals. Direct is
  the zero-value execution mode; durable agents opt in with `Durable: true`.
- Start or signal workflows from Go server code with
  `adapters/temporal.NewClientFromEnv`; never dial Temporal from browser
  bundles. See [workflow-triggers-go.md](docs/guides/workflow-triggers-go.md).
- Authors write `app/`, `agents/`, `workflows/`, `internal/`, and optional root
  Go middleware only. Generated projections, contracts, registries, and process
  mains live under `generated/`.
- Do not move React component composition into Go handlers.
- Initial Go-rendered markup must stay inside the documented portable profile.
- Always attempt portable compilation, including inside `use client` modules.
- Unsupported render code may downgrade only at its nearest `use client`
  boundary, and every downgrade must be emitted in the client-boundary
  manifest. Unsupported code without that boundary remains a compile error.
- Parse, type, module, contract, and internal compiler errors are always fatal;
  they must never be converted to client rendering.
- All TypeScript-to-Go values cross a schema-generated contract.
- Static props and generated route data are public; never put secrets in them.
- Never add Node, npm, or source TypeScript execution to the production server.
- Regenerate and run hydration conformance tests after contract or renderer changes.
- The root orchestrator owns render-plan versions, route IDs, generated registries,
  generated Go projection packages, dependency lockfiles, and release versions.

## Task skills

- Use `$add-page` for the web route workflow.
- Use `$connect-go-data` when converting a static route to request-time Go data.
- Use `$add-action` for typed React-to-Go mutations.
- Use `$add-api` for Go HTTP endpoints.
- Use `$debug-contracts` for code generation or hydration mismatches.
- Use `$aws-reference` for the OpenTofu deployment example.

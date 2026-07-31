# `@go-beyond/edge-middleware` (Stream 4 stub)

Public TypeScript contract for hosted **edge middleware** as a Workers for
Platforms User Worker. Consumed by the Origens control plane
(`gobeyond-internal`) via a **tagged pin** — not via `file:` in release/CI.

**Status:** Stub only. `private: true` / version `0.0.0-stream4-stub`. Do not
publish until the contract ADR ships and Stream 5 CLI emit is ready.

Cross-repo requirements:
https://github.com/Origens-Dev/gobeyond-internal/blob/main/docs/cloudflare-edge-middleware-bundle.md
(or the matching path on branch `feat/cloudflare-edge-swap`).

## Hard rules

1. No origin credentials in this package or customer bundles built from it.
2. No direct origin fetch — use relative/`fetch(request)` so platform
   **outbound** intercepts.
3. Do **not** set `x-gobeyond-auth-context` yet (Stream 6).
4. Document WfP CPU/body/subrequest limits as customer-facing product limits.

## Local development

Workspace package under `packages/edge-middleware`. Sibling
`gobeyond-internal` documents the control-plane flag
`GOBEYOND_CLOUDFLARE_EDGE_MIDDLEWARE_ENABLED` (default off).

```bash
pnpm --filter @go-beyond/edge-middleware build
pnpm --filter @go-beyond/edge-middleware test
```

# GoBeyond Development Guide

GoBeyond is website-first. React owns content and component composition; Go
begins at request-time data, actions, middleware, and API boundaries.

## Always-on guardrails

- Keep route-owned source together in `app/`: `page.tsx` is static by itself;
  add a sibling `page.go` only when the route needs request-time behavior.
- Keep route-specific mutations in a sibling `actions.go` and HTTP endpoints in
  `app/api/**/route.go`. Put reusable Go services and policy in ordinary
  `internal/` packages, never in a second route tree.
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

- Use `$add-page` for the website-first route workflow.
- Use `$connect-go-data` when converting a static route to request-time Go data.
- Use `$add-action` for typed React-to-Go mutations.
- Use `$add-api` for Go HTTP endpoints.
- Use `$debug-contracts` for code generation or hydration mismatches.
- Use `$aws-reference` for the OpenTofu deployment example.

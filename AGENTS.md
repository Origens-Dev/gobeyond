# GoBeyond Development Guide

GoBeyond is website-first. React owns content and component composition; Go
begins at request-time data, actions, middleware, and API boundaries.

## Always-on guardrails

- Keep the `app/` tree React-only and Go implementations under `server/`.
- Do not move React component composition into Go handlers.
- Initial Go-rendered markup must stay inside the documented portable profile.
- Unsupported initial-render JavaScript is a compile error, never a silent CSR fallback.
- All TypeScript-to-Go values cross a schema-generated contract.
- Static props and generated route data are public; never put secrets in them.
- Never add Node, npm, or source TypeScript execution to the production server.
- Regenerate and run hydration conformance tests after contract or renderer changes.
- The root orchestrator owns render-plan versions, route IDs, generated registries,
  dependency lockfiles, and release versions.

## Task skills

- Use `$add-page` for the website-first route workflow.
- Use `$connect-go-data` when converting a static route to request-time Go data.
- Use `$add-action` for typed React-to-Go mutations.
- Use `$add-api` for Go HTTP endpoints.
- Use `$debug-contracts` for code generation or hydration mismatches.
- Use `$aws-reference` for the OpenTofu deployment example.

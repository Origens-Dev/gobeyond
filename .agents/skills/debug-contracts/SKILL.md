---
name: debug-contracts
description: >
  Diagnose GoBeyond generated-contract, render-plan, and React hydration
  failures. Use when gobeyond generate --check, page.schema.ts, generated Go,
  render-plan diagnostics, build mismatches, or hydrateRoot warnings fail.
user-invocable: false
---

# Debug cross-language contracts

Use this skill when React, generated code, Go, or hydration disagree.

1. Reproduce with the smallest route and save the exact compiler/Go/browser
   error.
2. Run `pnpm generate --check`. If it is stale, run `pnpm generate`, inspect
   only generated diffs, and commit them with the schema change.
3. Compare `page.schema.ts`, the generated Go props type, and the loader
   signature. The schema is the boundary source of truth.
4. For a render-plan error, move unsupported initial computations to Go props
   or use a portable helper. If browser rendering is intentional, mark the
   nearest component module `use client` and verify the compiler emitted one
   exact client-boundary record; explicit `ClientOnly` remains available with
   an optional fallback.
5. For hydration, compare no-JS Go HTML with the first React render. Check
   values, condition order, keys, whitespace, IDs, URLs, and client-only
   fallbacks. An empty client boundary must render empty in Go and on React's
   first pass, then mount after an effect.
6. For a build mismatch, do not replay an action. Allow the guarded reload and
   investigate build IDs and deployment ordering.

```bash
pnpm generate --check
pnpm routes
pnpm test
pnpm build && pnpm preview
```

Do not silence a hydration warning or move indexable content behind a client
boundary. Parse, type, module, contract, and internal errors must remain fatal.
See `docs/guides/debug-contracts.md`.

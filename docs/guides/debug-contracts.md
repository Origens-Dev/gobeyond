# Debug contracts and hydration

GoBeyond’s schema is the boundary between a React view and Go loader/action.
When a contract fails, repair the boundary rather than working around it with
untyped JSON or client-only SEO content.

1. Run `pnpm generate --check`. If output is stale, run `pnpm generate` and
   review the generated diff with the schema change.
2. Compare `page.schema.ts` or `actions.ts` to the generated Go type and the
   sibling `page.go` or `actions.go` implementation signature. The runtime
   consumes their generated-safe projection, never the source `app/` folder.
3. For a render-plan failure, compute the initial value in Go or use a portable
   helper. A genuinely browser-only widget may use the nearest `use client`
   module boundary; confirm the compiler reports its exact downgrade. Explicit
   `ClientOnly` remains available with an optional fallback.
4. For hydration warnings, compare Go’s no-JS document with React’s first
   render. Check props, condition ordering, list keys, URLs, whitespace, IDs,
   and client-only fallbacks. An empty boundary must be empty in Go and on the
   first React pass, then mount after an effect.
5. For a build mismatch, let the guarded full reload happen and investigate
   build IDs and deployment order. Never retry an action automatically.

The useful local sequence is:

```bash
pnpm generate --check
pnpm routes
pnpm test
pnpm build && pnpm preview
```

Hydration conformance is part of the renderer contract. Do not suppress its
warnings or move indexable content behind client-only rendering. Parse, type,
module, contract, and internal errors must remain fatal.

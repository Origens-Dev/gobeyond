# Debug contracts and hydration

GoBeyond’s schema is the boundary between a React view and Go loader/action.
When a contract fails, repair the boundary rather than working around it with
untyped JSON or client-only SEO content.

1. Run `pnpm generate --check`. If output is stale, run `pnpm generate` and
   review the generated diff with the schema change.
2. Compare `page.schema.ts` or `actions.ts` to the generated Go type and
   implementation signature.
3. For a render-plan failure, compute the initial value in Go, use a portable
   helper, or isolate a genuinely browser-only widget with `ClientOnly`.
4. For hydration warnings, compare Go’s no-JS document with React’s first
   render. Check props, condition ordering, list keys, URLs, whitespace, IDs,
   and client-only fallbacks. Effects cannot change initial markup.
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
warnings or convert indexable pages to silent CSR fallbacks.

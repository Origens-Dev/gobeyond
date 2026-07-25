---
name: add-action
description: >
  Add a typed GoBeyond mutation from React to Go. Use when editing actions.ts,
  app/<route>/actions.go, defineAction, ActionContext, generated registration,
  CSRF-protected forms, or client refresh behavior.
user-invocable: false
---

# Add a Go action

Use actions for a page-owned mutation. Use `$add-api` for a public or
non-page HTTP contract.

1. Declare input and output schemas in `app/<route>/actions.ts`.
2. Generate bindings, then implement the matching Go function in the sibling
   `app/<route>/actions.go`.
3. The runtime imports the generated-safe route projection under
   `internal/gobeyondgen/routes/<route-ID>/`, never the source `app/`
   directory.
4. Validate business rules in Go even though the browser client validates
   schema shape.
5. Return the declared output on success or an error on failure. Field-error,
   redirect, and refresh result variants are not part of the MVP action API.
6. When a mutation changes cached data or a route's props, call
   `cache.RevalidateTag` and/or `cache.RevalidatePath` on `ctx.Context`.
   Invalidation runs even from authenticated requests; it also records paths
   and tags on the request scope for the action envelope.
7. On the client, use `postAction` or `runAction` from `@go-beyond/react/browser`.
   They parse the frozen envelope (`{ apiVersion, buildId, data, refresh? }`),
   re-fetch the current route when `refresh.paths` is present, and invalidate
   the matching entries in the client Router Cache (or the whole cache when
   `refresh.paths` is omitted).
8. Never put credentials, authorization decisions, or trust in browser input.

`gobeyond add action <route> <name>` creates the declaration and a typed Go
handler in the sibling `actions.go` that imports the deterministic generated
action contract. It only appends to an `actions.ts` scaffold carrying its
insertion marker; it refuses to touch any other existing file. Run generation
and register the generated-safe route handler through the generated action
contract in `gbruntime.Config.Actions`.

```tsx
export const rename = defineAction({
  input: schema.object({ name: schema.string() }),
  output: schema.object({ saved: schema.boolean() }),
})
```

```go
func Rename(ctx *gb.ActionContext, input contract.Input) (contract.Output, error) {
  if input.Name == "" { return contract.Output{}, errors.New("name is required") }
  _ = cache.RevalidateTag(ctx.Context, "account")
  _ = cache.RevalidatePath(ctx.Context, "/account")
  return contract.Output{Saved: true}, nil
}

// In the server registry, using imports for the generated contract and route:
Actions: []gbruntime.Action{actioncontract.Register(routeprojection.Rename)}
```

```tsx
await postAction(
  `/_gobeyond/builds/${buildId}/actions/${encodeURIComponent(actionId)}`,
  { name: "Ada" },
  { buildId },
);
```

```bash
pnpm generate
pnpm generate:check
pnpm test
```

Test a successful request, malformed input, handler error, CSRF rejection,
cancellation, and build mismatch. See
`docs/guides/add-action.md`.

---
name: add-action
description: >
  Add a typed GoBeyond mutation from React to Go. Use when editing actions.ts,
  server/actions/<route>/actions.go, defineAction, ActionContext, field errors,
  redirects, CSRF-protected forms, or route refresh behavior.
user-invocable: false
---

# Add a Go action

Use actions for a page-owned mutation. Use `$add-api` for a public or
non-page HTTP contract.

1. Declare input and output schemas in `app/<route>/actions.ts`.
2. Generate bindings, then implement the matching Go function under
   `server/actions/<go-safe-route-key>/actions.go`.
3. Validate business rules in Go even though the browser client validates
   schema shape.
4. Return field errors for correctable input and a redirect or refresh route
   only after the mutation commits.
5. Never put credentials, authorization decisions, or trust in browser input.

`gobeyond add action <route> <name>` creates the declaration and a typed Go
handler that imports the deterministic generated action contract. It only
appends to an `actions.ts` scaffold carrying its insertion marker; it refuses
to touch any other existing file. Run generation and register
`contract.ActionID` in `gbruntime.Config.Actions`.

```tsx
export const rename = defineAction({
  input: schema.object({ name: schema.string() }),
  output: schema.object({ saved: schema.boolean() }),
})
```

```go
func Rename(ctx *gb.ActionContext, input RenameInput) (gb.ActionResult[RenameOutput], error) {
  if input.Name == "" { return gb.ActionResult[RenameOutput]{FieldErrors: map[string]string{"name": "Required"}}, nil }
  return gb.ActionResult[RenameOutput]{Data: RenameOutput{Saved: true}}, nil
}
```

```bash
pnpm generate
pnpm generate:check
pnpm test
```

Test a successful request, malformed input, field error, CSRF rejection,
redirect/refresh, cancellation, and build mismatch. See
`docs/guides/add-action.md`.

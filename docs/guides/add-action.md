# Add an action

Actions are typed page mutations. The browser owns the form and pending UI;
Go owns validation, authorization, mutation, and the final result.

For a page that already exists, begin with:

```bash
gobeyond add action products/[slug] save
```

The command creates or appends the `save` declaration in `actions.ts` and a
typed sibling `actions.go`. That handler imports the deterministic generated
action contract. Run `gobeyond generate` before compiling it; the build
projects the route source into the generated safe package that the runtime
registers in `gbruntime.Config.Actions`.
An existing `actions.ts` is only updated when it contains the insertion marker
created by this command; otherwise GoBeyond refuses rather than risk
overwriting hand-authored TypeScript. Add the export manually in that case.

```text
app/products/[slug]/actions.ts
app/products/[slug]/actions.go
```

Declare the input/output contract in TypeScript, generate bindings, and then
implement the matching Go function. Browser validation improves feedback but is
never a trust boundary.

```tsx
export const save = defineAction({
  input: schema.object({ name: schema.string() }),
  output: schema.object({ saved: schema.boolean() }),
})
```

```go
func Save(ctx *gb.ActionContext, input contract.Input) (contract.Output, error) {
  if input.Name == "" { return contract.Output{}, errors.New("name is required") }
  return contract.Output{Saved: true}, nil
}

// In the server registry, using the generated contract and route packages:
Actions: []gbruntime.Action{actioncontract.Register(routeprojection.Save)}
```

Actions are POST-only, schema-validated server side, and protected by the
framework’s origin/CSRF policy for cookie-backed sessions. The generated
registration rejects missing or unknown fields, invalid nested values, and
trailing JSON before `Save` runs. It also validates the typed output before a
success response can be delivered. Test success, validation errors,
unauthorized requests, cancellation, and build mismatch behavior. A mismatch
reloads safely; it never replays a mutation.

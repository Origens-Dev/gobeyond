---
name: add-api
description: >
  Add a GoBeyond Go HTTP API route. Use when creating server/api/**/route.go,
  GET/POST handlers, RequestContext, Response, content types, authentication,
  cache headers, or external webhook/API interfaces.
user-invocable: false
---

# Add a Go API route

Use an API route for a stable HTTP interface, webhook, or non-page consumer.
Keep React page mutations in `$add-action` instead.

1. Create `server/api/<go-safe-route-key>/route.go`; its route is `/api/...`.
2. Implement only the required uppercase HTTP-method functions (`GET`, `POST`,
   and so on) using `*gobeyond.RequestContext` and `gobeyond.Response`.
3. Set status, `Content-Type`, and cache headers explicitly. JSON responses
   must use `application/json`.
4. Authenticate and validate input in Go. Apply body limits before decoding.
5. Keep public responses cacheable only when they do not vary by cookies,
   authorization, or private request state.

`gobeyond add api <route>` creates a compiling JSON `GET` handler at
`server/api/<safe-key>/route.go` and never overwrites an existing handler.
Register the route with the runtime before exposing it publicly.

```go
func GET(_ *gb.RequestContext) (gb.Response, error) {
  return gb.Response{Status: http.StatusOK, Headers: http.Header{
    "Content-Type": {"application/json"},
  }, Body: []byte(`{"ok":true}`)}, nil
}
```

```bash
pnpm generate
pnpm routes
pnpm test
```

Test method handling, validation, unauthenticated access, cache headers,
malformed JSON, and error redaction. See `docs/guides/add-api.md`.

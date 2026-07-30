---
name: add-api
description: >
  Add a GoBeyond Go HTTP API route. Use when creating app/api/**/route.go,
  GET/POST handlers, RequestContext, Response, content types, authentication,
  cache headers, or external webhook/API interfaces.
user-invocable: false
---

# Add a Go API route

Use an API route for a stable HTTP interface, webhook, or non-page consumer.
Keep React page mutations in `$add-action` instead.

1. Create `app/api/<route>/route.go`; its route is `/api/...`.
2. Run generation before compiling. The runtime imports the generated-safe API
   projection under `generated/api/<route-ID>/`, never the source
   directory below `app/`.
3. Implement only the required uppercase HTTP-method functions (`GET`, `POST`,
   and so on) using `*gobeyond.RequestContext` and `gobeyond.Response`.
4. Set status, `Content-Type`, and cache headers explicitly. JSON responses
   must use `application/json`.
5. Authenticate and validate input in Go. Apply body limits before decoding.
6. Keep public responses cacheable only when they do not vary by cookies,
   authorization, or private request state.

`gobeyond add api <route>` creates a compiling JSON `GET` handler at
`app/api/<route>/route.go` and never overwrites an existing handler. Register
the generated-safe API projection with the runtime before exposing it publicly.

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

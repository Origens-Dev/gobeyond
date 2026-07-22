# Add an API route

Use an API route for a public HTTP interface, a webhook, or a consumer outside
the current page. Use an action for a page-owned mutation.

Create a JSON GET starting point with:

```bash
gobeyond add api products
```

This writes `server/api/products/route.go` with a compiling `GET` handler and
will refuse to overwrite an existing route implementation. Wire the route into
your runtime's API registry, then replace the placeholder response with the
real validation, authorization, and cache behavior.

```text
server/api/products/route.go  →  /api/products
```

Implement uppercase HTTP-method functions and return an explicit
`gobeyond.Response`.

```go
func GET(_ *gb.RequestContext) (gb.Response, error) {
  return gb.Response{
    Status: http.StatusOK,
    Headers: http.Header{"Content-Type": {"application/json"}},
    Body: []byte(`{"ok":true}`),
  }, nil
}
```

Validate and authorize in Go. Set `Content-Type`, status, and cache policy
intentionally. Public caching is valid only for content independent of cookies,
authorization, and other private request state. Bound request bodies before
decoding, redact internal errors, and test method, malformed-body,
authentication, and cache behavior.

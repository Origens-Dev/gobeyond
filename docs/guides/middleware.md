# Add request middleware

> [!WARNING]
> Middleware is an alpha surface. Its API and deployment contract can change
> before GoBeyond reaches a stable release.

Create exactly one file at the application root: `middleware.ts` or
`middleware.js`. Do not add both.

```ts
export default function middleware(
  request: Request,
): Response | Promise<Response> {
  const url = new URL(request.url)

  if (url.pathname === '/docs/old') {
    return Response.redirect(new URL('/docs', request.url), 308)
  }

  return fetch(request)
}
```

The default export receives a standard Fetch `Request`. Return a `Response`
directly to redirect, reject, or answer the request. Return `fetch(request)` to
continue through the platform-controlled path to the application.

Middleware currently runs for every request. Branch on `request.method`, the
URL, or headers when behavior applies only to part of the application. There
is no separate matcher configuration in the current alpha.

## Build and local execution

`gobeyond build` bundles the entry and its imports into one module-worker
artifact:

```text
dist/edge-middleware/worker.mjs
```

The artifact uses `export default { fetch }`, which deployment adapters can
install in a compatible CDN or edge runtime. `dist/deploy/artifacts.json`
publishes its path.

`gobeyond dev` and `gobeyond preview` execute that same built module in front
of the Go site server. A middleware edit takes the complete replacement-build
path, and development switches traffic only after both the middleware and Go
server pass readiness. Node is used for this local Fetch runtime; it is not
added to `dist/server` or the production Go server image.

## Hosting boundary

Middleware is allowed to decide how a request proceeds, but it is not an
origin credential holder. Do not embed private-origin URLs, mTLS material,
workload tokens, signing keys, or trusted viewer claims in middleware source.

In a hosted deployment, same-origin `fetch(request)` must be intercepted by the
platform so tenant admission, cache/static selection, and origin authentication
remain outside customer code. Emitting `worker.mjs` does not imply that every
CDN provides this isolation; the selected deployment adapter must implement
the boundary.

The framework rejects the former `middleware.go`, `server/cmd/middleware`,
`edge-middleware.ts`, and `edge-middleware/` authoring layouts with migration
guidance. This keeps one middleware contract across generate, build, dev,
preview, and hosting.

# Add Go request middleware

> [!WARNING]
> Middleware is an alpha surface. Its API and deployment contract can change
> before GoBeyond reaches a stable release.

Create exactly one file at the application root: `middleware.go`.

```go
package middleware

import gb "github.com/Origens-Dev/gobeyond"

func Middleware(next gb.Handler) gb.Handler {
	return func(ctx *gb.RequestContext) (gb.Response, error) {
		// Inspect ctx.Request, set ctx.Values, or return a response.
		return next(ctx)
	}
}
```

The handler runs in the same Go process and execution slot as the rest of the
application. It is applied to documents, APIs, actions, and runtime payloads;
it is not a separate worker, relay, socket, or edge deployment.

## Edge/origin policy

Put small, edge-safe redirects and same-origin rewrites in `gobeyond.json`:

```json
{
  "redirects": [
    { "source": "/docs/old", "destination": "/docs", "status": 308 }
  ],
  "rewrites": [
    { "source": "/legacy/[slug]", "destination": "/docs/[slug]" }
  ]
}
```

The build emits `dist/deploy/proxy-policy.json` with the finalized build ID
and digest. The platform can evaluate this policy before cache/origin routing;
the Go origin evaluates the same artifact again for direct-origin/bypass
parity. Platform access/firewall rules remain platform-owned configuration,
not authored middleware.

## Build and local execution

`gobeyond build` compiles `middleware.go` into the application server and
publishes the policy artifact path in `dist/deploy/artifacts.json`.

`gobeyond dev` and `gobeyond preview` run one Go site server with the root hook
inside it. A middleware edit takes the normal replacement-build path and
readiness is checked once for the Go server. Node is not added to `dist/server`
or the production Go server image.

## Hosting boundary

Middleware is application code, but it is not an origin credential holder. Do
not embed private-origin URLs, mTLS material, workload tokens, signing keys, or
trusted viewer claims in the source.

The framework rejects the former TypeScript/JavaScript edge middleware and
paired middleware-process layouts. This keeps one Go middleware contract across
generate, build, dev, preview, and hosting.

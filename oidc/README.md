# github.com/Origens-Dev/gobeyond/oidc

GoBeyond workload-identity access for Go applications and workers.

```go
source := &oidc.TokenSource{}
token, err := source.Token(ctx, oidc.TokenOptions{Audience: oidc.AWSTSAudience})
```

Resolution order is request header, context, environment token, then the
per-slot Unix-socket broker. Audience exchange is strict and uses the
issuer’s `POST /~token` endpoint.

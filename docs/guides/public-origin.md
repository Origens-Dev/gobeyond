# Configure the public origin

`runtime.Config` needs one canonical public-origin strategy. The normal
single-site setup pins a value at startup:

```go
server, err := gbruntime.New(gbruntime.Config{
  BuildID:      routes.BuildID,
  PublicOrigin: "https://www.example.com",
  // ...
})
```

Use `PublicOrigin` for ordinary deployments. It is the source for document
metadata, action same-origin checks, and CSRF validation; do not derive those
values from an untrusted `Host` header.

For a trusted reverse proxy, preview system, or custom-domain front door, use
`ResolvePublicOrigin` instead. It must return an absolute origin only for
hosts the application recognizes:

```go
server, err := gbruntime.New(gbruntime.Config{
  BuildID: routes.BuildID,
  ResolvePublicOrigin: func(request *http.Request) (string, error) {
    switch request.Host {
    case "preview.example.com":
      return "https://preview.example.com", nil
    case "www.example.com":
      return "https://www.example.com", nil
    default:
      return "", fmt.Errorf("unknown public host %q", request.Host)
    }
  },
  // ...
})
```

Choose exactly one strategy. The runtime rejects missing or ambiguous
configuration and rejects a request when the resolver declines its host. The
resolved value is available as `ctx.PublicOrigin` to page, action, API, and
middleware code; request-time metadata should use that value when constructing
absolute canonical URLs or social-image URLs.

Social-image URLs must be absolute HTTPS even for non-indexable routes. Include
the image's known width and height in structured Open Graph metadata so sharing
crawlers do not need to infer its layout. Icon metadata may use root-relative
paths because browsers resolve `<link rel="icon">` against the current public
origin; the build generates those files from `app/icon.png`. See
[Icons and social sharing](icons-and-social.md) for the complete metadata and
deployment checklist.

This is an origin-selection extension point, not a tenant registry or an
application cache. A hosted multi-site control plane belongs outside the
public framework.

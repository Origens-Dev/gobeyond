# Lambda Function URL adapter

`adapters/lambda` wraps an `http.Handler` for AWS Lambda Function URLs
(payload format 2.0).

## Usage

```go
import (
  lambdaurl "github.com/Origens-Dev/gobeyond/adapters/lambda"
  gbruntime "github.com/Origens-Dev/gobeyond/runtime"
)

func main() {
  server, err := gbruntime.New(/* ... */)
  if err != nil {
    log.Fatal(err)
  }
  // Do not wrap with local disk static middleware in production:
  // CloudFront (or equivalent CDN) should serve SSG HTML and /_gobeyond/*
  // from object storage.
  lambdaurl.Serve(server)
}
```

## Packaging

| Item | Include in zip? |
| --- | --- |
| `bootstrap` (linux/arm64 binary of this entrypoint) | yes |
| `dist/server/render-plans.gbp` (render-plan pack) | yes |
| `dist/server/runtime-data/static-build.gbs` (static-entry pack) | yes |
| `dist/server/runtime-data/contracts.json` | yes |
| `dist/server/runtime-manifest.json` | yes |
| `dist/server/render-plans/**` (inspection-only JSON dumps) | no |
| `dist/static/**` (assets, SSG HTML, robots) | **no** — sync to object storage / CDN |

Runtime: `provided.al2023`, handler `bootstrap`, architecture `arm64`.

Set `GOBEYOND_PLAN_PACK` (default `dist/server/render-plans.gbp`) and
`GOBEYOND_STATIC_PACK` (default `dist/server/runtime-data/static-build.gbs`)
so the binary opens the packs when the working directory is the unzipped
deployment root (place the packs next to `bootstrap` or adjust env). The
runtime never loads the per-route JSON plan dumps; they exist for human
inspection and conformance tooling only.

## Edge split

The CDN should send hashed assets, `robots.txt`, public files, and prebuilt
route HTML to object storage. Soft-nav (`/_gobeyond/builds/*/runtime/*`), actions,
APIs, and dynamic documents should reach this Function URL (`AWS_IAM`) through
your authenticated origin path (for example a reverse proxy that SigV4-signs
requests).

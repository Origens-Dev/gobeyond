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
| `dist/server/render-plans/**` | yes |
| `dist/server/runtime-data/**` | yes |
| `dist/server/runtime-manifest.json` | yes |
| `dist/static/**` (assets, SSG HTML, robots) | **no** — sync to object storage / CDN |

Runtime: `provided.al2023`, handler `bootstrap`, architecture `arm64`.

Set `GOBEYOND_PLAN_DIR` (default `dist/server/render-plans`) so the binary
finds packaged plans when the working directory is the unzipped deployment
root (place plans next to `bootstrap` or adjust env).

## Edge split

The CDN should send hashed assets, `robots.txt`, public files, and prebuilt
route HTML to object storage. Soft-nav (`/_gobeyond/builds/*/runtime/*`), actions,
APIs, and dynamic documents should reach this Function URL (`AWS_IAM`) through
your authenticated origin path (for example a reverse proxy that SigV4-signs
requests).

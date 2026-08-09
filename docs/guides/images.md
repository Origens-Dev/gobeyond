# Runtime images

GoBeyond provides a portable `imageSrc()` helper and a Node-free Go image
endpoint for local development and standalone deployments. Hosted GoBeyond
deployments route this endpoint through the shared edge image service; the
customer runtime is not used as an image-processing fallback. The runtime
loads sources from local disk when
`GOBEYOND_STATIC_DIR` is set, from S3 when
`GOBEYOND_IMAGE_SOURCE_BUCKET` and `GOBEYOND_IMAGE_SOURCE_PREFIX` are set,
and from explicitly allowlisted public HTTPS domains when
`GOBEYOND_IMAGE_REMOTE_DOMAINS` is set. Disk takes precedence for local
development; a standalone deployment may combine its local/S3 source with
remote images.

The core `imageopt` package is AWS-free: it contains `Loader`, `DiskLoader`,
`RemoteLoader`, `RouterLoader`, `Handler`, and the optimize path. S3 support lives in the nested module
`github.com/Origens-Dev/gobeyond/imageopt/s3`, so apps that do not serve images
from S3 never pull the AWS SDK into their module graph.

## Build an image URL

```tsx
import { imageSrc } from "@go-beyond/react";

export default function Brand() {
  return (
    <img
      src={imageSrc("/brand/logo.png", { w: 256 })}
      alt="Example brand"
      width={256}
      height={256}
    />
  );
}
```

The helper is portable, so the compiler emits the same URL during Go rendering
and React hydration:

```text
/_gobeyond/image?url=%2Fbrand%2Flogo.png&w=256&q=75
```

Its signature is `imageSrc(source, { w, q?, f? })`:

```tsx
imageSrc("/photos/hero.jpg", { w: 1200, q: 82, f: "jpeg" });
```

Quality defaults to `75`; format may be `"jpeg"` or `"png"`. When format is
omitted, the source JPEG/PNG format is retained.

## Endpoint parameters

- `url` is required and must be a same-site absolute path beginning with one
  `/`, such as `/photos/hero.jpg`, or an HTTPS URL whose host is allowlisted by
  the deployment. Protocol-relative URLs, ports, query strings, fragments,
  backslashes, and path traversal are rejected. Remote requests do not forward
  viewer cookies, authorization headers, or other credentials.
- `w` is required and must be one of `16, 32, 48, 64, 96, 128, 256, 384, 640,
  750, 828, 1080, 1200, 1920, 2048, 3840`.
- `q` is optional, defaults to `75`, and is clamped to the range `1` through
  `100`. It controls JPEG encoding; PNG encoding is lossless.
- `f` is optional. Supported values are `jpeg`, `jpg`, and `png`. WebP is
  deferred.

JPEG and PNG sources are supported. The requested width preserves the source
aspect ratio.

## Local disk setup

Set `GOBEYOND_STATIC_DIR` to the built static directory. The runtime resolves
`url` beneath that directory and prevents traversal, including symlink escapes.

```bash
GOBEYOND_STATIC_DIR="$PWD/dist/static" ./dist/server/gobeyond-server
curl -o /tmp/logo.png \
  "http://localhost:8080/_gobeyond/image?url=%2Fbrand%2Flogo.png&w=256&q=75"
```

Successful responses set `Cache-Control: public, max-age=3600`. The customer
deployment's CDN caches each host/source/variant combination; the Go runtime is
invoked on a cache miss and performs the resize before returning the response.
Optimized images are public and shareable by default, matching the semantics of
Next's default image optimizer. Authentication belongs to the page or
middleware boundary; private images require a separate, auth-aware design.

## Production S3 setup (requires infra apply)

Production Lambdas have no disk-resident `public/` copy. The hosting platform
configures:

```text
GOBEYOND_IMAGE_SOURCE_BUCKET=gobeyond-{env}-site-static
GOBEYOND_IMAGE_SOURCE_PREFIX=landing  # or app
```

Add the nested module only when you use S3:

```bash
go get github.com/Origens-Dev/gobeyond/imageopt/s3
```

```go
import (
    "github.com/Origens-Dev/gobeyond/imageopt"
    imageopts3 "github.com/Origens-Dev/gobeyond/imageopt/s3"
)

// Disk when GOBEYOND_STATIC_DIR is set, otherwise the configured S3 source.
loader, err := imageopts3.NewLoaderFromEnvironment(ctx, "")
// runtime.Config{ImageLoader: loader}
```

`imageopt.NewLoaderFromEnvironment` still resolves the disk source in AWS-free
builds; when the S3 variables are configured but the nested module is not
imported, it returns an error naming `imageopt/s3` rather than silently serving
nothing.

A request for `/brand/logo.png` therefore reads
`s3://gobeyond-{env}-site-static/landing/brand/logo.png` (or the corresponding
`app/` key). An external hosting platform may also add cross-account S3
`GetObject` plus a CDN cache policy and ordered behavior for
`/_gobeyond/image*` (query keys `url`, `w`, `q`, `f`, and trusted viewer host).
That infrastructure is outside the public single-site reference and has not
been applied by this repository.

The S3 loader (`imageopt/s3.Loader`) and Lambda environment wiring are present in code, but production
availability must not be claimed until the IAM, bucket policy, Lambda
configuration, and edge behavior have been applied.

## Remote image setup

Commit `.gobeyond/images.json` to the application repository. This is the
deployment-owned source of truth:

```json
{
  "remoteDomains": ["images.ctfassets.net"],
  "cacheSeconds": 2592000
}
```

`cacheSeconds` controls the public freshness lifetime of successful image
variants at the hosting edge. It defaults to 3600 seconds and accepts values
from 60 seconds through 31536000 seconds. Use a long lifetime when the source
URL is content-addressed or otherwise immutable; change the source URL or use
an edge purge when an existing variant must be replaced.

The GoBeyond build validates the domains and cache lifetime and emits the
deployment manifest. In hosted deployments, the platform publishes this
non-secret policy to an immutable, deployment-scoped edge record. It is not
injected into a customer runtime. Rollbacks switch the active deployment
pointer while retaining historical policy records for cache isolation.

The domains must be exact HTTPS domains or subdomain patterns. For example:

For standalone deployments only, the equivalent legacy environment variable is
`GOBEYOND_IMAGE_REMOTE_DOMAINS=images.example.com,*.ctfassets.net`.

Use the remote URL directly with `imageSrc()`:

```tsx
imageSrc("https://images.ctfassets.net/space/asset/photo.jpg", { w: 640 })
```

The helper still emits a same-origin `/_gobeyond/image` URL. Hosted GoBeyond
fetches the approved remote source at the edge, sends bounded bytes to the
shared libvips service, and caches successful JPEG/PNG variants for the
configured lifetime. The browser does not fetch the remote image directly.
The hosted path enforces HTTPS, rejects credentials, ports, queries,
fragments, traversal, and unapproved domains. Do not place provider
management tokens in this configuration.

Social preview images should remain direct, absolute HTTPS URLs such as
`https://example.com/social/og.png`; do not route Open Graph or Twitter cards
through the runtime optimizer. See [Icons and social sharing](icons-and-social.md).

## Portable multi-column galleries

The first-party `<Columns>` component flows real content across CSS columns.
It uses no JavaScript measurement, so Go can include the images and layout
styles in first-paint HTML:

```tsx
import { Columns, imageSrc } from "@go-beyond/react";

export function Gallery({ photos }: { photos: readonly string[] }) {
  return (
    <Columns columnCount={3} gap="1rem">
      {photos.map((src) => (
        <img key={src} src={imageSrc(src, { w: 640 })} alt="" width={640} height={480} />
      ))}
    </Columns>
  );
}
```

`<Columns>` uses CSS `column-count` / `column-gap` only — no JavaScript
measurement — so it is safe for portable compilation and hydration. Third-party
layout widgets that depend on viewport or browser state remain client-only.
When such a widget is necessary, isolate it behind `ClientOnly`; a portable
layout may be supplied as the fallback when showing the same content before
JavaScript is useful.

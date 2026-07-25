# Runtime images

GoBeyond provides a portable `imageSrc()` helper and a Node-free Go image
endpoint. The runtime loads sources from local disk when
`GOBEYOND_STATIC_DIR` is set, or from S3 when
`GOBEYOND_IMAGE_SOURCE_BUCKET` and `GOBEYOND_IMAGE_SOURCE_PREFIX` are set.
Disk takes precedence so local development never depends on S3.

The core `imageopt` package is AWS-free: it contains `Loader`, `DiskLoader`,
`Handler`, and the optimize path only. S3 support lives in the nested module
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
  `/`, such as `/photos/hero.jpg`. Remote URLs, protocol-relative URLs, query
  strings, fragments, backslashes, and path traversal are rejected.
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

Successful responses set `Cache-Control: public, max-age=3600`.

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
`GetObject` plus a CloudFront cache policy and ordered behavior for
`/_gobeyond/image*` (query keys `url`, `w`, `q`, `f`, and trusted viewer host).
That infrastructure is outside the public single-site reference and has not
been applied by this repository.

The S3 loader (`imageopt/s3.Loader`) and Lambda environment wiring are present in code, but production
availability must not be claimed until the IAM, bucket policy, Lambda
configuration, and edge behavior have been applied.

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

# Runtime images

GoBeyond provides a portable `imageSrc()` helper and a Node-free Go image
endpoint. The runtime loads sources from local disk when
`GOBEYOND_STATIC_DIR` is set, or from S3 when
`GOBEYOND_IMAGE_SOURCE_BUCKET` and `GOBEYOND_IMAGE_SOURCE_PREFIX` are set.
Disk takes precedence so local development never depends on S3.

## Build an image URL

```tsx
import { imageSrc } from "@go-beyond/react";

export default function Brand() {
  return (
    <img
      src={imageSrc("/brand/logo.png", { w: 256 })}
      alt="Origens"
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

A request for `/brand/logo.png` therefore reads
`s3://gobeyond-{env}-site-static/landing/brand/logo.png` (or the corresponding
`app/` key). The hosting platform also codes cross-account S3 `GetObject` plus a
CloudFront cache policy and ordered behavior for `/_gobeyond/image*` (query
keys `url`, `w`, `q`, `f`, and trusted viewer host). That OpenTofu is not yet
applied in AWS. Design details are locked in
[ADR 002](https://github.com/Origens-Dev/gobeyond-internal/blob/main/docs/adr/002-image-optimizer-design-lock.md).

The S3 loader and Lambda environment wiring are present in code, but production
availability must not be claimed until the IAM, bucket policy, Lambda
configuration, and edge behavior have been applied.

Social preview images should remain direct, absolute HTTPS URLs such as
`https://example.com/social/og.png`; do not route Open Graph or Twitter cards
through the runtime optimizer. See [Icons and social sharing](icons-and-social.md).

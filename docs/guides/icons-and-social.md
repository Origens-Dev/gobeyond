# Icons and social sharing

GoBeyond keeps browser icons and social-card images in the static asset
pipeline. It does not resize social images at request time.

## Generate browser icons

Add one non-empty square PNG at `app/icon.png`. During `gobeyond build`, the
framework writes these files to `dist/static`:

```text
favicon-16x16.png
favicon-32x32.png
apple-touch-icon.png
```

The generated paths are included in the deployment route trie. PWA icons at
192 and 512 pixels are not generated yet.

Reference the generated files from route metadata:

```ts
icons: {
  icon: "/favicon-32x32.png",
  appleTouch: "/apple-touch-icon.png",
}
```

Request-time Go metadata uses the equivalent values:

```go
Icons: gb.Icons{
  Icon:       "/favicon-32x32.png",
  AppleTouch: "/apple-touch-icon.png",
},
```

## Publish a social image

Create a PNG such as `public/social/og.png`. A 1200 by 630 image is a broadly
supported default. Files under `public/` are copied unchanged, so metadata must
use the absolute public HTTPS URL rather than a relative path or an image
optimization endpoint:

```ts
openGraph: {
  type: "website",
  title: "Example",
  description: "An example site",
  url: "https://www.example.com/",
  siteName: "Example",
  locale: "en_US",
  image: {
    url: "https://www.example.com/social/og.png",
    width: 1200,
    height: 630,
    alt: "Example",
    type: "image/png",
  },
},
twitter: {
  card: "summary_large_image",
  title: "Example",
  description: "An example site",
  site: "@example",
  imageAlt: "Example",
  images: ["https://www.example.com/social/og.png"],
},
```

The older Open Graph `images: string[]` field remains supported. Prefer the
structured `image` field for a primary image because it emits dimensions, alt
text, and media type.

Social images must use absolute HTTPS URLs even on `noindex` pages. A crawler
may fetch a sign-in or campaign URL for a preview even when search indexing is
disabled. `robots.txt` controls crawler access; a document's meta robots value
controls indexing. Do not block the document or image merely because the page
is `noindex`.

## Verify a deployment

1. Fetch the page HTML and check `og:image`, its width and height, and the
   Twitter tags.
2. Fetch the exact image URL without cookies and confirm `200` plus the expected
   `Content-Type`.
3. Fetch each generated icon path.
4. Refresh the page in the Facebook, X, and LinkedIn sharing debuggers.

CloudFront or another front door must route public social and icon paths to the
static origin. Local document rendering alone does not prove that a crawler can
reach the files.

Do not route social preview images through `/_gobeyond/image`; keep absolute
HTTPS URLs to static PNGs. See [Runtime images](images.md) for in-page logos
and other same-site resize use cases.

# Icons and social sharing

GoBeyond mirrors Next.js Metadata file conventions for icons and social images.
They are materialized into `dist/static` during `gobeyond build`. The Go server
does not evaluate these modules at request time.

## Browser icons

| File | Result |
| --- | --- |
| `app/favicon.ico` | `/favicon.ico` |
| `app/icon.(ico\|jpg\|jpeg\|png\|svg)` | `/icon...` |
| `app/apple-icon.(jpg\|jpeg\|png)` | `/apple-icon...` |
| `app/icon.tsx` (etc.) | evaluated at build; default-export a `Response` / binary / `ImageResponse` |

A square `app/icon.png` also still generates:

```text
favicon-16x16.png
favicon-32x32.png
apple-touch-icon.png
```

Reference generated or authored icon paths from route metadata:

```ts
icons: {
  icon: "/favicon-32x32.png",
  appleTouch: "/apple-touch-icon.png",
}
```

## Social images

| File | Result |
| --- | --- |
| `app/opengraph-image.(jpg\|jpeg\|png\|gif)` | `/opengraph-image...` |
| `app/twitter-image.(jpg\|jpeg\|png\|gif)` | `/twitter-image...` |
| `app/opengraph-image.tsx` / `twitter-image.tsx` | build-time image modules |

Code-generated images import `ImageResponse` from `@go-beyond/react/og` (build-time only):

```tsx
import { ImageResponse } from '@go-beyond/react/og'

export const size = { width: 1200, height: 630 }
export const contentType = 'image/png'

export default function Image() {
  return new ImageResponse(
    <div style={{ fontSize: 48, background: '#111', color: 'white', width: '100%', height: '100%', display: 'flex', alignItems: 'center', justifyContent: 'center' }}>
      Example
    </div>,
    { ...size },
  )
}
```

`public/social/` remains valid for hand-authored cards. Metadata must use absolute
HTTPS URLs for Open Graph / Twitter image fields.

## Robots, sitemap, and manifest

| File | Result |
| --- | --- |
| `app/robots.txt` \| `app/robots.ts` | `/robots.txt` |
| `app/sitemap.xml` \| `app/sitemap.ts` | `/sitemap.xml` |
| `app/manifest.json` \| `.webmanifest` \| `.ts` | `/manifest...` |

Use `defineRobots` / `defineSitemap` / `defineManifest` from `@go-beyond/schema`
for the TypeScript forms. Nested `app/**/sitemap.ts` and `robots.ts` are also
supported (same URL mapping as Next.js).

Do not author the same URL under both `app/` and `public/`.

## Verify a deployment

1. Fetch the page HTML and check `og:image`, its width and height, and the
   Twitter tags.
2. Fetch the exact image URL without cookies and confirm `200` plus the expected
   `Content-Type`.
3. Fetch each icon / crawler path (`/robots.txt`, `/sitemap.xml`, `/favicon.ico`).
4. Refresh the page in the Facebook, X, and LinkedIn sharing debuggers.

The CDN or another front door must route these public paths to the static
origin. Do not route social preview images through `/_gobeyond/image`.

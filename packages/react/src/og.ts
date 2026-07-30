/**
 * Build-time image response helper for app/icon.tsx, opengraph-image.tsx, and
 * twitter-image.tsx (mirrored from Next.js `next/og`).
 *
 * Evaluated only while `gobeyond build` materializes static metadata files —
 * the Go server never imports this module.
 */
export { ImageResponse } from '@vercel/og'

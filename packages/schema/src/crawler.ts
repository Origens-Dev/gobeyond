/** Next.js MetadataRoute.Robots-shaped document for app/robots.ts */
export type Robots = {
  rules:
    | {
        userAgent?: string | string[]
        allow?: string | string[]
        disallow?: string | string[]
        crawlDelay?: number
      }
    | Array<{
        userAgent?: string | string[]
        allow?: string | string[]
        disallow?: string | string[]
        crawlDelay?: number
      }>
  sitemap?: string | string[]
  host?: string
}

/** Next.js MetadataRoute.Sitemap entry shape for app/sitemap.ts */
export type SitemapFile = Array<{
  url: string
  lastModified?: string | Date
  changeFrequency?: 'always' | 'hourly' | 'daily' | 'weekly' | 'monthly' | 'yearly' | 'never'
  priority?: number
  alternates?: { languages?: Record<string, string> }
}>

/** Next.js MetadataRoute.Manifest-shaped document for app/manifest.ts */
export type Manifest = {
  name?: string
  short_name?: string
  description?: string
  start_url?: string
  display?: 'fullscreen' | 'standalone' | 'minimal-ui' | 'browser'
  background_color?: string
  theme_color?: string
  icons?: Array<{
    src: string
    sizes?: string
    type?: string
    purpose?: string
  }>
  [key: string]: unknown
}

export function defineRobots(robots: Robots): Robots {
  return robots
}

export function defineSitemap(entries: SitemapFile): SitemapFile {
  return entries
}

export function defineManifest(manifest: Manifest): Manifest {
  return manifest
}

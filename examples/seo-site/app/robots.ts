import { defineRobots } from '@go-beyond/schema'

export default function robots() {
  return defineRobots({
    rules: {
      userAgent: '*',
      allow: '/',
      disallow: '/account',
    },
    sitemap: 'https://example.gobeyond.dev/sitemap.xml',
  })
}

import { defineSitemap } from '@go-beyond/schema'

export default function sitemap() {
  return defineSitemap([
    { url: 'https://example.gobeyond.dev/articles/portable-react' },
    { url: 'https://example.gobeyond.dev/products/trail-pack' },
    { url: 'https://example.gobeyond.dev/category/1' },
    { url: 'https://example.gobeyond.dev/locations/seattle' },
    { url: 'https://example.gobeyond.dev/en/articles/portable-react' },
    { url: 'https://example.gobeyond.dev/fr/articles/react-portable' },
  ])
}

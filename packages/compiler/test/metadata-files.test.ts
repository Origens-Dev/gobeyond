import assert from 'node:assert/strict'
import { mkdtemp, mkdir, readFile, rm, writeFile } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'

import {
  materializeAppMetadata,
  serializeRobots,
  serializeSitemap,
} from '../src/metadata-files.js'

test('serializeRobots matches Next-style robots.txt', () => {
  const body = serializeRobots({
    rules: { userAgent: '*', allow: '/', disallow: '/account' },
    sitemap: 'https://example.com/sitemap.xml',
  })
  assert.match(body, /User-Agent: \*/)
  assert.match(body, /Allow: \//)
  assert.match(body, /Disallow: \/account/)
  assert.match(body, /Sitemap: https:\/\/example.com\/sitemap.xml/)
})

test('serializeSitemap emits loc entries', () => {
  const body = serializeSitemap([{ url: 'https://example.com/a' }])
  assert.match(body, /<loc>https:\/\/example.com\/a<\/loc>/)
})

test('materializeAppMetadata copies static Metadata files and rejects public conflicts', async () => {
  const root = await mkdtemp(join(tmpdir(), 'gb-meta-'))
  try {
    await mkdir(join(root, 'app'), { recursive: true })
    await mkdir(join(root, 'static'), { recursive: true })
    await writeFile(join(root, 'app', 'robots.txt'), 'User-agent: *\nAllow: /\n')
    await writeFile(
      join(root, 'app', 'opengraph-image.png'),
      Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    )
    const ok = await materializeAppMetadata({
      projectRoot: root,
      staticDir: join(root, 'static'),
    })
    assert.equal(ok.ok, true)
    if (!ok.ok) return
    assert.deepEqual(ok.paths, ['/opengraph-image.png', '/robots.txt'])
    assert.equal(
      await readFile(join(root, 'static', 'robots.txt'), 'utf8'),
      'User-agent: *\nAllow: /\n',
    )

    const conflictDir = join(root, 'static-conflict')
    await mkdir(conflictDir, { recursive: true })
    await writeFile(join(conflictDir, 'robots.txt'), 'from-public\n')
    const conflict = await materializeAppMetadata({
      projectRoot: root,
      staticDir: conflictDir,
    })
    assert.equal(conflict.ok, false)
  } finally {
    await rm(root, { recursive: true, force: true })
  }
})

#!/usr/bin/env node

import { writeFileSync } from 'node:fs'
import { pathToFileURL } from 'node:url'

type WorkerInput = {
  kind: 'robots' | 'sitemap' | 'manifest' | 'icon' | 'apple-icon' | 'opengraph-image' | 'twitter-image'
  moduleFile: string
}

const imageKinds = new Set(['icon', 'apple-icon', 'opengraph-image', 'twitter-image'])

try {
  const input = JSON.parse(await readStandardInput()) as WorkerInput
  const moduleNamespace = await importFresh(input.moduleFile)
  if (imageKinds.has(input.kind)) {
    const value = await resolveImageExport(moduleNamespace, input.kind)
    writeResponse({ ok: true, image: value })
  } else {
    const value = await resolveExport(moduleNamespace, input.kind)
    writeResponse({ ok: true, value })
  }
} catch (error) {
  writeResponse({
    ok: false,
    error: error instanceof Error ? error.message : String(error),
  })
}

async function resolveExport(
  moduleNamespace: Record<string, unknown>,
  kind: string,
): Promise<unknown> {
  const preferred = kind
  const candidate = moduleNamespace[preferred] ?? moduleNamespace.default
  if (candidate === undefined) {
    throw new Error(`app/${preferred}.ts must export ${preferred}() or default.`)
  }
  const value = typeof candidate === 'function' ? await candidate() : candidate
  assertSerializable(value, `app/${preferred}.ts`)
  if (kind === 'sitemap' && !Array.isArray(value)) {
    throw new Error('app/sitemap.ts must return an array of sitemap entries.')
  }
  if ((kind === 'robots' || kind === 'manifest') && !isPlainObject(value)) {
    throw new Error(`app/${preferred}.ts must return a plain object.`)
  }
  return value
}

async function resolveImageExport(
  moduleNamespace: Record<string, unknown>,
  kind: string,
): Promise<{ bytesBase64: string; contentType: string; extension: string }> {
  const candidate = moduleNamespace.default
  if (typeof candidate !== 'function') {
    throw new Error(`app/${kind}.tsx must default-export a function.`)
  }
  const exportedType =
    typeof moduleNamespace.contentType === 'string' && moduleNamespace.contentType.length > 0
      ? moduleNamespace.contentType
      : 'image/png'
  const value = await candidate()
  const { bytes, contentType } = await normalizeImageResult(value, exportedType)
  return {
    bytesBase64: Buffer.from(bytes).toString('base64'),
    contentType,
    extension: extensionForContentType(contentType),
  }
}

async function normalizeImageResult(
  value: unknown,
  fallbackType: string,
): Promise<{ bytes: Uint8Array; contentType: string }> {
  if (value instanceof Response) {
    const buffer = new Uint8Array(await value.arrayBuffer())
    const contentType = value.headers.get('content-type') || fallbackType
    return { bytes: buffer, contentType }
  }
  if (typeof Blob !== 'undefined' && value instanceof Blob) {
    const buffer = new Uint8Array(await value.arrayBuffer())
    return { bytes: buffer, contentType: value.type || fallbackType }
  }
  if (value instanceof ArrayBuffer) {
    return { bytes: new Uint8Array(value), contentType: fallbackType }
  }
  if (ArrayBuffer.isView(value)) {
    const view = value as ArrayBufferView
    return {
      bytes: new Uint8Array(view.buffer, view.byteOffset, view.byteLength),
      contentType: fallbackType,
    }
  }
  if (typeof ReadableStream !== 'undefined' && value instanceof ReadableStream) {
    const response = new Response(value)
    const buffer = new Uint8Array(await response.arrayBuffer())
    return { bytes: buffer, contentType: fallbackType }
  }
  // ImageResponse / Response-like objects from @vercel/og expose arrayBuffer().
  if (
    isPlainObject(value) &&
    typeof (value as { arrayBuffer?: unknown }).arrayBuffer === 'function'
  ) {
    const buffer = new Uint8Array(
      await (value as { arrayBuffer: () => Promise<ArrayBuffer> }).arrayBuffer(),
    )
    const headers = (value as { headers?: { get?: (name: string) => string | null } }).headers
    const contentType = headers?.get?.('content-type') || fallbackType
    return { bytes: buffer, contentType }
  }
  throw new Error(
    `app image module must return Blob | ArrayBuffer | TypedArray | ReadableStream | Response (got ${Object.prototype.toString.call(value)}).`,
  )
}

function extensionForContentType(contentType: string): string {
  const normalized = contentType.split(';')[0]!.trim().toLowerCase()
  switch (normalized) {
    case 'image/jpeg':
      return '.jpg'
    case 'image/gif':
      return '.gif'
    case 'image/svg+xml':
      return '.svg'
    case 'image/x-icon':
    case 'image/vnd.microsoft.icon':
      return '.ico'
    case 'image/webp':
      return '.webp'
    default:
      return '.png'
  }
}

function assertSerializable(value: unknown, label: string): void {
  try {
    JSON.stringify(value)
  } catch (error) {
    throw new Error(
      `${label} must return a JSON-serializable value: ${
        error instanceof Error ? error.message : String(error)
      }`,
    )
  }
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

async function importFresh(moduleFile: string): Promise<Record<string, unknown>> {
  const url = `${pathToFileURL(moduleFile).href}?gobeyond=${Date.now()}`
  return (await import(url)) as Record<string, unknown>
}

async function readStandardInput(): Promise<string> {
  const chunks: Buffer[] = []
  for await (const chunk of process.stdin) {
    chunks.push(Buffer.from(chunk))
  }
  return Buffer.concat(chunks).toString('utf8')
}

function writeResponse(value: unknown): void {
  writeFileSync(3, `${JSON.stringify(value)}\n`)
}

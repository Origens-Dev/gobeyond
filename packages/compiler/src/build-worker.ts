#!/usr/bin/env node

import { writeFileSync } from 'node:fs'
import { pathToFileURL } from 'node:url'

type ParamDefinition = {
  name: string
  catchAll: boolean
  optional: boolean
}

type WorkerInput = {
  buildModuleFile?: string
  metadataModuleFile?: string
  params: ParamDefinition[]
}

type Entry = {
  params: Record<string, string | string[]>
  props: unknown
  metadata?: Record<string, unknown>
}

try {
  const input = JSON.parse(await readStandardInput()) as WorkerInput
  const buildModule = input.buildModuleFile
    ? await importFresh(input.buildModuleFile)
    : {}
  const metadataModule = input.metadataModuleFile
    ? await importFresh(input.metadataModuleFile)
    : {}
  const generate = optionalFunction(buildModule, 'generateStaticParams')
  const load = optionalFunction(buildModule, 'loadStaticProps')
  const metadata = optionalFunction(metadataModule, 'metadata')
  if (input.metadataModuleFile && !metadata) {
    throw new Error('page.metadata.ts must export metadata(props, params).')
  }

  if (input.params.length > 0 && !generate) {
    throw new Error(
      `Parameterized static route requires page.build.ts to export generateStaticParams().`,
    )
  }

  const generated = generate ? await generate() : [{}]
  if (!Array.isArray(generated)) {
    throw new Error('generateStaticParams() must return an array of parameter objects.')
  }

  const entries: Entry[] = []
  const seen = new Set<string>()
  for (let index = 0; index < generated.length; index += 1) {
    const params = validateParams(generated[index], input.params, index)
    const key = JSON.stringify(params)
    if (seen.has(key)) {
      throw new Error(`generateStaticParams() returned duplicate params at index ${index}: ${key}.`)
    }
    seen.add(key)
    const props = load ? await load(params) : {}
    assertSerializable(props, `loadStaticProps(${key})`)
    const documentMetadata = metadata ? await metadata(props, params) : undefined
    if (documentMetadata !== undefined) {
      assertSerializable(documentMetadata, `metadata(props, ${key})`)
      assertPlainObject(documentMetadata, `metadata(props, ${key})`)
    }
    entries.push({
      params,
      props,
      ...(documentMetadata === undefined
        ? {}
        : { metadata: documentMetadata as Record<string, unknown> }),
    })
  }
  writeResponse({ ok: true, entries })
} catch (error) {
  writeResponse({
    ok: false,
    error: error instanceof Error ? error.message : String(error),
  })
}

function optionalFunction(
  moduleNamespace: Record<string, unknown>,
  name: string,
): ((...arguments_: unknown[]) => unknown | Promise<unknown>) | undefined {
  const value = moduleNamespace[name]
  if (value === undefined) return undefined
  if (typeof value !== 'function') {
    throw new Error(`${name} must be a function when exported from page.build.ts.`)
  }
  return value as (...arguments_: unknown[]) => unknown | Promise<unknown>
}

function validateParams(
  value: unknown,
  definitions: ParamDefinition[],
  index: number,
): Record<string, string | string[]> {
  assertPlainObject(value, `generateStaticParams()[${index}]`)
  const object = value as Record<string, unknown>
  const expectedNames = new Set(definitions.map((definition) => definition.name))
  for (const name of Object.keys(object)) {
    if (!expectedNames.has(name)) {
      throw new Error(`generateStaticParams()[${index}] contains unknown route param ${JSON.stringify(name)}.`)
    }
  }

  const output: Record<string, string | string[]> = {}
  for (const definition of definitions) {
    const candidate = object[definition.name]
    if (candidate === undefined && definition.optional) continue
    if (definition.catchAll) {
      if (
        !Array.isArray(candidate) ||
        (!definition.optional && candidate.length === 0) ||
        !candidate.every((part) => typeof part === 'string' && part.length > 0)
      ) {
        throw new Error(
          `generateStaticParams()[${index}].${definition.name} must be ${definition.optional ? 'an' : 'a non-empty'} array of non-empty strings.`,
        )
      }
      output[definition.name] = candidate
    } else {
      if (typeof candidate !== 'string' || candidate.length === 0) {
        throw new Error(
          `generateStaticParams()[${index}].${definition.name} must be a non-empty string.`,
        )
      }
      output[definition.name] = candidate
    }
  }
  return output
}

function assertSerializable(value: unknown, path: string, stack = new Set<object>()): void {
  if (
    value === null ||
    typeof value === 'string' ||
    typeof value === 'boolean'
  ) return
  if (typeof value === 'number') {
    if (!Number.isFinite(value)) throw new Error(`${path} contains a non-finite number.`)
    return
  }
  if (typeof value !== 'object') {
    throw new Error(`${path} contains non-serializable ${typeof value}.`)
  }
  if (stack.has(value)) throw new Error(`${path} contains a cyclic value.`)
  stack.add(value)
  try {
    if (Array.isArray(value)) {
      for (let index = 0; index < value.length; index += 1) {
        if (!(index in value)) throw new Error(`${path}[${index}] is a sparse array entry.`)
        assertSerializable(value[index], `${path}[${index}]`, stack)
      }
      return
    }
    assertPlainObject(value, path)
    for (const [name, child] of Object.entries(value)) {
      assertSerializable(child, `${path}.${name}`, stack)
    }
  } finally {
    stack.delete(value)
  }
}

function assertPlainObject(value: unknown, path: string): void {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new Error(`${path} must be a plain object.`)
  }
  const prototype = Object.getPrototypeOf(value)
  if (prototype !== Object.prototype && prototype !== null) {
    throw new Error(`${path} must be a plain object, not ${prototype?.constructor?.name ?? 'an exotic object'}.`)
  }
}

function writeResponse(value: unknown): void {
  writeFileSync(3, `${JSON.stringify(value)}\n`)
}

async function readStandardInput(): Promise<string> {
  const chunks: Buffer[] = []
  for await (const chunk of process.stdin) chunks.push(Buffer.from(chunk))
  return Buffer.concat(chunks).toString('utf8')
}

async function importFresh(fileName: string): Promise<Record<string, unknown>> {
  const moduleURL = `${pathToFileURL(fileName).href}?gobeyond=${Date.now()}`
  return await import(moduleURL) as Record<string, unknown>
}

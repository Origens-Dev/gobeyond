import { spawn } from 'node:child_process'
import type { Readable } from 'node:stream'
import {
  copyFile,
  mkdir,
  mkdtemp,
  readdir,
  rm,
  stat,
  writeFile,
} from 'node:fs/promises'
import { dirname, extname, join, relative, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import ts from 'typescript'

import { type Diagnostic } from './types.js'

export type MetadataKind =
  | 'robots'
  | 'sitemap'
  | 'manifest'
  | 'icon'
  | 'apple-icon'
  | 'opengraph-image'
  | 'twitter-image'

const imageKinds = new Set<MetadataKind>([
  'icon',
  'apple-icon',
  'opengraph-image',
  'twitter-image',
])

const staticImageName =
  /^(icon|apple-icon|opengraph-image|twitter-image)(\d*)\.(ico|jpg|jpeg|png|svg|gif)$/
const codeImageName =
  /^(icon|apple-icon|opengraph-image|twitter-image)(\d*)\.(ts|tsx|js|jsx)$/

type WorkerResponse =
  | { ok: true; value: unknown }
  | {
      ok: true
      image: { bytesBase64: string; contentType: string; extension: string }
    }
  | { ok: false; error: string }

export type MaterializeAppMetadataResult =
  | { ok: true; paths: string[] }
  | { ok: false; diagnostics: Diagnostic[] }

/**
 * Discover Next.js-compatible Metadata files under app/ and write them into
 * staticDir (typically dist/static). public/ should already be copied there;
 * colliding URLs are hard errors.
 */
export async function materializeAppMetadata(options: {
  projectRoot: string
  staticDir: string
  timeoutMs?: number
}): Promise<MaterializeAppMetadataResult> {
  const projectRoot = resolve(options.projectRoot)
  const staticDir = resolve(options.staticDir)
  const appDir = resolve(projectRoot, 'app')
  if (!(await isDirectory(appDir))) {
    return { ok: true, paths: [] }
  }

  const paths: string[] = []
  const claimed = new Map<string, string>()
  const diagnostics: Diagnostic[] = []

  const claim = async (outputRel: string, sourceLabel: string): Promise<boolean> => {
    const normalized = `/${outputRel.replace(/^\/+/, '')}`
    const previous = claimed.get(normalized)
    if (previous) {
      diagnostics.push(
        diagnostic(
          join(appDir, sourceLabel.replace(/^app\//, '')),
          'GB1253',
          `${sourceLabel} and ${previous} both materialize to ${normalized}; keep one.`,
        ),
      )
      return false
    }
    const destination = join(staticDir, ...normalized.slice(1).split('/'))
    if (await isFile(destination)) {
      diagnostics.push(
        diagnostic(
          join(appDir, sourceLabel.replace(/^app\//, '')),
          'GB1254',
          `${sourceLabel} conflicts with public${normalized}; keep one.`,
        ),
      )
      return false
    }
    claimed.set(normalized, sourceLabel)
    paths.push(normalized)
    return true
  }

  // Root crawler / manifest / favicon.
  for (const job of [
    {
      kind: 'robots' as const,
      staticNames: ['robots.txt'],
      moduleNames: ['robots.ts', 'robots.js'],
      outputName: 'robots.txt',
    },
    {
      kind: 'sitemap' as const,
      staticNames: ['sitemap.xml'],
      moduleNames: ['sitemap.ts', 'sitemap.js'],
      outputName: 'sitemap.xml',
    },
    {
      kind: 'manifest' as const,
      staticNames: ['manifest.webmanifest', 'manifest.json'],
      moduleNames: ['manifest.ts', 'manifest.js'],
      outputName: 'manifest.webmanifest',
    },
  ]) {
    const staticSource = await firstExisting(appDir, job.staticNames)
    const moduleSource = await firstExisting(appDir, job.moduleNames)
    if (!staticSource && !moduleSource) continue
    if (staticSource && moduleSource) {
      diagnostics.push(
        diagnostic(
          moduleSource,
          'GB1255',
          `app/${job.staticNames[0]} and app/${job.moduleNames[0]} both exist; keep one.`,
        ),
      )
      continue
    }
    let outputName = job.outputName
    if (job.kind === 'manifest' && staticSource?.endsWith('.json')) {
      outputName = 'manifest.json'
    }
    const label = `app/${staticSource ? basename(staticSource) : basename(moduleSource!)}`
    if (!(await claim(outputName, label))) continue
    const destination = join(staticDir, outputName)
    if (staticSource) {
      await mkdir(dirname(destination), { recursive: true })
      await copyFile(staticSource, destination)
    } else {
      const result = await materializeMetadataModule({
        projectRoot,
        moduleFile: moduleSource!,
        kind: job.kind,
        ...(options.timeoutMs === undefined ? {} : { timeoutMs: options.timeoutMs }),
      })
      if (!result.ok) {
        diagnostics.push(...result.diagnostics)
        continue
      }
      await mkdir(dirname(destination), { recursive: true })
      await writeFile(destination, result.body)
    }
  }

  const favicon = join(appDir, 'favicon.ico')
  if (await isFile(favicon)) {
    if (await claim('favicon.ico', 'app/favicon.ico')) {
      await mkdir(staticDir, { recursive: true })
      await copyFile(favicon, join(staticDir, 'favicon.ico'))
    }
  }

  // Walk app/ for icons, social images, and nested robots/sitemap.
  const files = await listFiles(appDir)
  for (const abs of files) {
    const rel = toPosix(relative(appDir, abs))
    const name = basename(abs)
    const dirRel = dirname(rel)
    const underRoot = dirRel === '.'

    if (staticImageName.test(name)) {
      const label = `app/${rel}`
      if (!(await claim(rel, label))) continue
      const destination = join(staticDir, ...rel.split('/'))
      await mkdir(dirname(destination), { recursive: true })
      await copyFile(abs, destination)
      continue
    }

    const codeMatch = name.match(codeImageName)
    if (codeMatch) {
      const kind = codeMatch[1] as MetadataKind
      const stem = name.slice(0, -extname(name).length)
      let outputRel = `${stem}.png`
      if (!underRoot) outputRel = `${dirRel}/${outputRel}`
      const label = `app/${rel}`
      // Reserve the default path; may rewrite extension after evaluation.
      if (!(await claim(outputRel, label))) continue
      const defaultDestination = join(staticDir, ...outputRel.split('/'))
      const result = await materializeMetadataModule({
        projectRoot,
        moduleFile: abs,
        kind,
        ...(options.timeoutMs === undefined ? {} : { timeoutMs: options.timeoutMs }),
      })
      if (!result.ok) {
        claimed.delete(`/${outputRel}`)
        paths.splice(paths.indexOf(`/${outputRel}`), 1)
        diagnostics.push(...result.diagnostics)
        continue
      }
      let destination = defaultDestination
      if (result.outExtension) {
        const rewritten = outputRel.replace(/\.png$/i, result.outExtension)
        if (rewritten !== outputRel) {
          claimed.delete(`/${outputRel}`)
          paths.splice(paths.indexOf(`/${outputRel}`), 1)
          if (!(await claim(rewritten, label))) continue
          destination = join(staticDir, ...rewritten.split('/'))
        }
      }
      await mkdir(dirname(destination), { recursive: true })
      await writeFile(destination, result.body)
      continue
    }

    if (!underRoot && (name === 'robots.txt' || name === 'sitemap.xml')) {
      const label = `app/${rel}`
      if (!(await claim(rel, label))) continue
      const destination = join(staticDir, ...rel.split('/'))
      await mkdir(dirname(destination), { recursive: true })
      await copyFile(abs, destination)
      continue
    }

    if (
      !underRoot &&
      (name === 'robots.ts' ||
        name === 'robots.js' ||
        name === 'sitemap.ts' ||
        name === 'sitemap.js')
    ) {
      const kind: MetadataKind = name.startsWith('robots.') ? 'robots' : 'sitemap'
      const outputName = kind === 'robots' ? 'robots.txt' : 'sitemap.xml'
      const outputRel = `${dirRel}/${outputName}`
      const label = `app/${rel}`
      if (!(await claim(outputRel, label))) continue
      const result = await materializeMetadataModule({
        projectRoot,
        moduleFile: abs,
        kind,
        ...(options.timeoutMs === undefined ? {} : { timeoutMs: options.timeoutMs }),
      })
      if (!result.ok) {
        claimed.delete(`/${outputRel}`)
        paths.splice(paths.indexOf(`/${outputRel}`), 1)
        diagnostics.push(...result.diagnostics)
        continue
      }
      const destination = join(staticDir, ...outputRel.split('/'))
      await mkdir(dirname(destination), { recursive: true })
      await writeFile(destination, result.body)
    }
  }

  if (diagnostics.length > 0) {
    return { ok: false, diagnostics }
  }
  paths.sort()
  return { ok: true, paths }
}

export type MaterializeMetadataResult =
  | { ok: true; body: string | Buffer; contentType: string; outExtension?: string }
  | { ok: false; diagnostics: Diagnostic[] }

/** Compile and evaluate one metadata module into a static body. */
export async function materializeMetadataModule(options: {
  projectRoot: string
  moduleFile: string
  kind: MetadataKind
  timeoutMs?: number
}): Promise<MaterializeMetadataResult> {
  const projectRoot = resolve(options.projectRoot)
  const moduleFile = resolve(options.moduleFile)
  const temporaryParent = resolve(projectRoot, '.gobeyond-build-exec')
  await mkdir(temporaryParent, { recursive: true })
  const temporaryDirectory = await mkdtemp(resolve(temporaryParent, 'metadata-'))
  try {
    const emitted = compileModules(projectRoot, [moduleFile], temporaryDirectory)
    if (!emitted.ok) return emitted
    const workerFile = fileURLToPath(new URL('./metadata-worker.js', import.meta.url))
    const response = await runWorker(
      workerFile,
      projectRoot,
      { kind: options.kind, moduleFile: emitted.moduleFiles.get(moduleFile)! },
      options.timeoutMs ?? 30_000,
    )
    if (!response.ok) {
      return failure(
        moduleFile,
        'GB1250',
        `Metadata module failed: ${response.error}`,
        `Export a valid ${options.kind} module from ${options.moduleFile}.`,
      )
    }
    try {
      if (imageKinds.has(options.kind)) {
        if (!('image' in response) || !response.image) {
          return failure(moduleFile, 'GB1251', 'Image metadata worker returned no image payload.')
        }
        return {
          ok: true,
          body: Buffer.from(response.image.bytesBase64, 'base64'),
          contentType: response.image.contentType,
          outExtension: response.image.extension,
        }
      }
      if (!('value' in response)) {
        return failure(moduleFile, 'GB1251', 'Metadata worker returned no value payload.')
      }
      if (options.kind === 'robots') {
        return { ok: true, body: serializeRobots(response.value), contentType: 'text/plain' }
      }
      if (options.kind === 'sitemap') {
        return {
          ok: true,
          body: serializeSitemap(response.value),
          contentType: 'application/xml',
        }
      }
      return {
        ok: true,
        body: `${JSON.stringify(response.value, null, 2)}\n`,
        contentType: 'application/manifest+json',
      }
    } catch (error) {
      return failure(
        moduleFile,
        'GB1251',
        error instanceof Error ? error.message : String(error),
      )
    }
  } finally {
    await rm(temporaryDirectory, { recursive: true, force: true })
    try {
      await rm(temporaryParent)
    } catch {
      /* concurrent owner */
    }
  }
}

export function serializeRobots(value: unknown): string {
  if (!isPlainObject(value)) throw new Error('robots() must return an object.')
  const rules = normalizeRules(value.rules)
  const lines: string[] = []
  for (const rule of rules) {
    for (const agent of asStringList(rule.userAgent, '*')) lines.push(`User-Agent: ${agent}`)
    for (const allow of asStringList(rule.allow)) lines.push(`Allow: ${allow}`)
    for (const disallow of asStringList(rule.disallow)) lines.push(`Disallow: ${disallow}`)
    if (typeof rule.crawlDelay === 'number' && Number.isFinite(rule.crawlDelay)) {
      lines.push(`Crawl-delay: ${rule.crawlDelay}`)
    }
    lines.push('')
  }
  for (const sitemap of asStringList(value.sitemap)) lines.push(`Sitemap: ${sitemap}`)
  if (typeof value.host === 'string' && value.host.length > 0) lines.push(`Host: ${value.host}`)
  return `${lines.join('\n').replace(/\n+$/, '')}\n`
}

export function serializeSitemap(value: unknown): string {
  if (!Array.isArray(value)) throw new Error('sitemap() must return an array.')
  const hasAlternates = value.some(
    (entry) =>
      isPlainObject(entry) &&
      isPlainObject(entry.alternates) &&
      isPlainObject(entry.alternates.languages),
  )
  const xmlns = hasAlternates
    ? ' xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:xhtml="http://www.w3.org/1999/xhtml"'
    : ' xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"'
  const urls = value.map((entry, index) => serializeSitemapEntry(entry, index)).join('')
  return `<?xml version="1.0" encoding="UTF-8"?>\n<urlset${xmlns}>${urls}\n</urlset>\n`
}

function serializeSitemapEntry(entry: unknown, index: number): string {
  if (!isPlainObject(entry) || typeof entry.url !== 'string' || entry.url.length === 0) {
    throw new Error(`sitemap()[${index}] must include a non-empty url string.`)
  }
  const parts = [`\n  <url>`, `\n    <loc>${escapeXML(entry.url)}</loc>`]
  const lastModified = normalizeLastModified(entry.lastModified)
  if (lastModified) parts.push(`\n    <lastmod>${escapeXML(lastModified)}</lastmod>`)
  if (typeof entry.changeFrequency === 'string') {
    parts.push(`\n    <changefreq>${escapeXML(entry.changeFrequency)}</changefreq>`)
  }
  if (typeof entry.priority === 'number' && Number.isFinite(entry.priority)) {
    parts.push(`\n    <priority>${entry.priority}</priority>`)
  }
  if (isPlainObject(entry.alternates) && isPlainObject(entry.alternates.languages)) {
    for (const [lang, href] of Object.entries(entry.alternates.languages)) {
      if (typeof href !== 'string') continue
      parts.push(
        `\n    <xhtml:link rel="alternate" hreflang="${escapeXML(lang)}" href="${escapeXML(href)}" />`,
      )
    }
  }
  parts.push('\n  </url>')
  return parts.join('')
}

function normalizeLastModified(value: unknown): string | undefined {
  if (value === undefined || value === null) return undefined
  if (typeof value === 'string') return value
  if (value instanceof Date) return value.toISOString()
  throw new Error('sitemap lastModified must be a string or Date.')
}

function normalizeRules(value: unknown): Array<Record<string, unknown>> {
  if (value === undefined) return [{ userAgent: '*' }]
  if (Array.isArray(value)) {
    return value.map((rule, index) => {
      if (!isPlainObject(rule)) throw new Error(`robots.rules[${index}] must be an object.`)
      return rule
    })
  }
  if (!isPlainObject(value)) throw new Error('robots.rules must be an object or array.')
  return [value]
}

function asStringList(value: unknown, fallback?: string): string[] {
  if (value === undefined || value === null) return fallback === undefined ? [] : [fallback]
  if (typeof value === 'string') return [value]
  if (Array.isArray(value) && value.every((item) => typeof item === 'string')) return value
  throw new Error('Expected a string or string array.')
}

function escapeXML(value: string): string {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&apos;')
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function compileModules(
  projectRoot: string,
  moduleFiles: string[],
  outDirectory: string,
):
  | { ok: true; moduleFiles: Map<string, string> }
  | { ok: false; diagnostics: Diagnostic[] } {
  let compilerOptions: ts.CompilerOptions = {
    target: ts.ScriptTarget.ES2022,
    module: ts.ModuleKind.NodeNext,
    moduleResolution: ts.ModuleResolutionKind.NodeNext,
    jsx: ts.JsxEmit.ReactJSX,
    strict: true,
    skipLibCheck: true,
  }
  const configFile = ts.findConfigFile(projectRoot, ts.sys.fileExists, 'tsconfig.json')
  if (configFile) {
    const read = ts.readConfigFile(configFile, ts.sys.readFile)
    if (!read.error) {
      const parsed = ts.parseJsonConfigFileContent(read.config, ts.sys, dirname(configFile))
      compilerOptions = { ...compilerOptions, ...parsed.options }
    }
  }
  compilerOptions = {
    ...compilerOptions,
    rootDir: projectRoot,
    outDir: outDirectory,
    noEmit: false,
    noEmitOnError: true,
    declaration: false,
    declarationMap: false,
    emitDeclarationOnly: false,
    sourceMap: false,
    inlineSourceMap: false,
    incremental: false,
    composite: false,
    jsx: compilerOptions.jsx ?? ts.JsxEmit.ReactJSX,
  }
  const program = ts.createProgram(moduleFiles, compilerOptions)
  const errors = ts
    .getPreEmitDiagnostics(program)
    .filter((candidate) => candidate.category === ts.DiagnosticCategory.Error)
  const emit = errors.length === 0 ? program.emit() : undefined
  const allErrors = [...errors, ...(emit?.diagnostics ?? [])]
  if (allErrors.length > 0 || emit?.emitSkipped) {
    return {
      ok: false,
      diagnostics: allErrors.map((candidate) => typescriptDiagnostic(candidate, moduleFiles[0]!)),
    }
  }
  const outputs = new Map<string, string>()
  for (const moduleFile of moduleFiles) {
    const relativeModuleFile = relative(projectRoot, moduleFile)
    const extension = extname(relativeModuleFile)
    const outputExtension = extension === '.mts' ? '.mjs' : extension === '.cts' ? '.cjs' : '.js'
    outputs.set(
      moduleFile,
      resolve(outDirectory, `${relativeModuleFile.slice(0, -extension.length)}${outputExtension}`),
    )
  }
  return { ok: true, moduleFiles: outputs }
}

function typescriptDiagnostic(candidate: ts.Diagnostic, fallbackFile: string): Diagnostic {
  const file = candidate.file
  const start = candidate.start ?? 0
  const location = file?.getLineAndCharacterOfPosition(start)
  return {
    code: `TS${candidate.code}`,
    message: ts.flattenDiagnosticMessageText(candidate.messageText, '\n'),
    suggestion: 'Fix the metadata TypeScript module before building.',
    fileName: file?.fileName ?? fallbackFile,
    start,
    length: candidate.length ?? 1,
    line: (location?.line ?? 0) + 1,
    column: (location?.character ?? 0) + 1,
  }
}

function failure(
  fileName: string,
  code: string,
  message: string,
  suggestion?: string,
): { ok: false; diagnostics: Diagnostic[] } {
  return { ok: false, diagnostics: [diagnostic(fileName, code, message, suggestion)] }
}

function diagnostic(
  fileName: string,
  code: string,
  message: string,
  suggestion?: string,
): Diagnostic {
  return {
    code,
    message,
    ...(suggestion === undefined ? {} : { suggestion }),
    fileName,
    start: 0,
    length: 1,
    line: 1,
    column: 1,
  }
}

async function runWorker(
  workerFile: string,
  projectRoot: string,
  input: unknown,
  timeoutMs: number,
): Promise<WorkerResponse> {
  return new Promise((resolvePromise) => {
    const child = spawn(process.execPath, [workerFile], {
      cwd: projectRoot,
      env: process.env,
      stdio: ['pipe', 'pipe', 'pipe', 'pipe'],
    })
    let protocol = ''
    let stderr = ''
    const protocolStream = child.stdio[3] as Readable | null
    protocolStream?.setEncoding('utf8')
    protocolStream?.on('data', (chunk: string) => {
      protocol += chunk
    })
    child.stderr.setEncoding('utf8')
    child.stderr.on('data', (chunk: string) => {
      stderr += chunk
    })
    child.stdout.resume()
    const timer = setTimeout(() => child.kill('SIGKILL'), timeoutMs)
    child.stdin.end(JSON.stringify(input))
    child.once('close', (code, signal) => {
      clearTimeout(timer)
      if (signal === 'SIGKILL') {
        resolvePromise({ ok: false, error: `execution exceeded ${timeoutMs}ms` })
        return
      }
      try {
        resolvePromise(JSON.parse(protocol) as WorkerResponse)
      } catch {
        resolvePromise({
          ok: false,
          error: `worker exited with code ${code ?? 'unknown'} without a valid result${
            stderr.trim() ? `: ${stderr.trim()}` : ''
          }`,
        })
      }
    })
  })
}

async function listFiles(root: string): Promise<string[]> {
  const out: string[] = []
  async function walk(directory: string): Promise<void> {
    for (const entry of await readdir(directory, { withFileTypes: true })) {
      if (entry.name === 'node_modules' || entry.name === 'generated' || entry.name.startsWith('.')) {
        continue
      }
      const path = join(directory, entry.name)
      if (entry.isDirectory()) {
        await walk(path)
      } else if (entry.isFile()) {
        out.push(path)
      }
    }
  }
  await walk(root)
  return out
}

async function firstExisting(dir: string, names: string[]): Promise<string | undefined> {
  for (const name of names) {
    const path = join(dir, name)
    if (await isFile(path)) return path
  }
  return undefined
}

async function isFile(path: string): Promise<boolean> {
  try {
    return (await stat(path)).isFile()
  } catch {
    return false
  }
}

async function isDirectory(path: string): Promise<boolean> {
  try {
    return (await stat(path)).isDirectory()
  } catch {
    return false
  }
}

function basename(path: string): string {
  const parts = toPosix(path).split('/')
  return parts[parts.length - 1] || path
}

function toPosix(path: string): string {
  return path.split(sep).join('/')
}

/** CLI: materialize all app metadata, or one module. */
export async function runMaterializeMetadataCLI(args: string[]): Promise<number> {
  if (args[0] === '--help' || args[0] === '-h') return printUsage()

  // Whole-app mode used by `gobeyond build`.
  if (args.includes('--static-dir')) {
    let projectRoot = process.cwd()
    let staticDir: string | undefined
    let outputPath: string | undefined
    for (let index = 0; index < args.length; index += 1) {
      const argument = args[index]!
      if (argument === '--project-root') {
        const value = args[++index]
        if (!value) return printUsage()
        projectRoot = resolve(value)
      } else if (argument === '--static-dir') {
        staticDir = args[++index]
      } else if (argument === '--out') {
        outputPath = args[++index]
      } else {
        return printUsage()
      }
    }
    if (!staticDir) return printUsage()
    const result = await materializeAppMetadata({
      projectRoot,
      staticDir: resolve(staticDir),
    })
    if (!result.ok) {
      process.stderr.write(`${formatDiagnostics(result.diagnostics)}\n`)
      return 1
    }
    const payload = `${JSON.stringify({ paths: result.paths }, null, 2)}\n`
    if (outputPath) {
      await mkdir(dirname(resolve(outputPath)), { recursive: true })
      await writeFile(resolve(outputPath), payload)
    } else {
      process.stdout.write(payload)
    }
    return 0
  }

  let projectRoot = process.cwd()
  let moduleFile: string | undefined
  let kind: MetadataKind | undefined
  let outputPath: string | undefined
  for (let index = 0; index < args.length; index += 1) {
    const argument = args[index]!
    if (argument === '--project-root') {
      const value = args[++index]
      if (!value) return printUsage()
      projectRoot = resolve(value)
    } else if (argument === '--module') {
      moduleFile = args[++index]
    } else if (argument === '--kind') {
      kind = args[++index] as MetadataKind
    } else if (argument === '--out') {
      outputPath = args[++index]
    } else {
      return printUsage()
    }
  }
  const allowed: MetadataKind[] = [
    'robots',
    'sitemap',
    'manifest',
    'icon',
    'apple-icon',
    'opengraph-image',
    'twitter-image',
  ]
  if (!moduleFile || !kind || !allowed.includes(kind)) return printUsage()
  const result = await materializeMetadataModule({ projectRoot, moduleFile, kind })
  if (!result.ok) {
    process.stderr.write(`${formatDiagnostics(result.diagnostics)}\n`)
    return 1
  }
  if (outputPath) {
    await mkdir(dirname(resolve(outputPath)), { recursive: true })
    await writeFile(resolve(outputPath), result.body)
  } else if (typeof result.body === 'string') {
    process.stdout.write(result.body)
  } else {
    process.stdout.write(result.body)
  }
  return 0
}

function printUsage(): number {
  process.stderr.write(`usage:
  gobeyond-compile materialize-metadata --project-root <dir> --static-dir <dir> [--out paths.json]
  gobeyond-compile materialize-metadata --kind <kind> --module <file> [--project-root <dir>] [--out <file>]
`)
  return 2
}

function formatDiagnostics(diagnostics: Diagnostic[]): string {
  return diagnostics
    .map(
      (item) =>
        `${item.code} ${item.fileName}:${item.line}:${item.column}: ${item.message}`,
    )
    .join('\n')
}

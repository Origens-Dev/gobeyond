import { spawn } from 'node:child_process'
import type { Readable } from 'node:stream'
import { mkdir, mkdtemp, rm, stat } from 'node:fs/promises'
import { dirname, extname, isAbsolute, relative, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'
import ts from 'typescript'

import {
  type CompileStaticRouteOptions,
  type CompileStaticRouteResult,
  type Diagnostic,
  type ProjectRoute,
  type RouteValueContract,
  type StaticBuildEntry,
  type ValueSchema,
} from './types.js'

type ParamDefinition = {
  name: string
  catchAll: boolean
  optional: boolean
}

type WorkerResponse =
  | { ok: true; entries: StaticBuildEntry[] }
  | { ok: false; error: string }

const layoutExtensions = ['.tsx', '.ts', '.jsx', '.js', '.mts', '.cts'] as const

export async function compileStaticRoute(
  options: CompileStaticRouteOptions,
): Promise<CompileStaticRouteResult> {
  const projectRoot = resolve(options.projectRoot)
  const entryFile = resolve(projectRoot, options.route.entryFile)
  const configuredBuildFile = options.route.buildFile
    ? resolve(projectRoot, options.route.buildFile)
    : resolve(dirname(entryFile), 'page.build.ts')
  const configuredMetadataFile = options.route.metadataFile
    ? resolve(projectRoot, options.route.metadataFile)
    : resolve(dirname(entryFile), 'page.metadata.ts')
  const layouts = await discoverRouteLayouts(
    projectRoot,
    entryFile,
    options.appDirectory,
  )
  const buildFileExists = await isFile(configuredBuildFile)
  const metadataFileExists = await isFile(configuredMetadataFile)
  if (options.route.buildFile && !buildFileExists) {
    return failure(
      configuredBuildFile,
      'GB1240',
      `Configured static build module does not exist: ${configuredBuildFile}.`,
    )
  }
  if (options.route.metadataFile && !metadataFileExists) {
    return failure(
      configuredMetadataFile,
      'GB1244',
      `Configured static metadata module does not exist: ${configuredMetadataFile}.`,
    )
  }

  const params = parseParamDefinitions(
    options.route.routePattern ?? deriveRoutePattern(projectRoot, entryFile, options.appDirectory),
  )
  let entries: StaticBuildEntry[]
  if (buildFileExists || metadataFileExists) {
    const execution = await executeBuildModule({
      projectRoot,
      ...(buildFileExists ? { buildFile: configuredBuildFile } : {}),
      ...(metadataFileExists ? { metadataFile: configuredMetadataFile } : {}),
      params,
      timeoutMs: options.timeoutMs ?? 30_000,
    })
    if (!execution.ok) return execution
    entries = execution.entries
  } else {
    if (params.length > 0) {
      return failure(
        configuredBuildFile,
        'GB1241',
        'Parameterized static route requires page.build.ts with generateStaticParams().',
      )
    }
    entries = [{ params: {}, props: {} }]
  }

  const diagnostics: Diagnostic[] = []
  for (let index = 0; index < entries.length; index += 1) {
    const issue = validateSchema(
      options.contract.props,
      entries[index]!.props,
      `staticBuild.routes[${options.route.routeId}].entries[${index}].props`,
    )
    if (issue) {
      diagnostics.push(diagnostic(
        buildFileExists ? configuredBuildFile : entryFile,
        'GB1242',
        `Static props do not satisfy the page schema: ${issue}`,
        'Return a JSON-serializable value matching page.schema.ts from loadStaticProps().',
      ))
    }
    if (entries[index]!.metadata !== undefined) {
      const metadataIssue = validateMetadata(entries[index]!.metadata!)
      if (metadataIssue) {
        diagnostics.push(diagnostic(
          metadataFileExists ? configuredMetadataFile : entryFile,
          'GB1245',
          `Static metadata is invalid: ${metadataIssue}`,
          'Return complete serializable document metadata from metadata(props, params).',
        ))
      }
    }
  }
  if (diagnostics.length > 0) return { ok: false, diagnostics }

  return {
    ok: true,
    artifact: {
      routeId: options.route.routeId,
      ...(buildFileExists
        ? { buildFile: toProjectPath(projectRoot, configuredBuildFile) }
        : {}),
      ...(metadataFileExists
        ? { metadataFile: toProjectPath(projectRoot, configuredMetadataFile) }
        : {}),
      layoutFiles: layouts.map((fileName) => toProjectPath(projectRoot, fileName)),
      entries,
    },
    diagnostics: [],
  }
}

export async function discoverRouteLayouts(
  projectRoot: string,
  entryFile: string,
  configuredAppDirectory?: string,
): Promise<string[]> {
  const appRoot = resolveAppRoot(projectRoot, entryFile, configuredAppDirectory)
  const routeDirectory = dirname(resolve(entryFile))
  if (!isWithin(appRoot, routeDirectory)) return []
  const directories: string[] = []
  let current = routeDirectory
  while (isWithin(appRoot, current)) {
    directories.push(current)
    if (current === appRoot) break
    current = dirname(current)
  }
  directories.reverse()

  const layouts: string[] = []
  for (const directory of directories) {
    for (const extension of layoutExtensions) {
      const candidate = resolve(directory, `layout${extension}`)
      if (await isFile(candidate)) {
        layouts.push(candidate)
        break
      }
    }
  }
  return layouts
}

async function executeBuildModule(options: {
  projectRoot: string
  buildFile?: string
  metadataFile?: string
  params: ParamDefinition[]
  timeoutMs: number
}): Promise<
  | { ok: true; entries: StaticBuildEntry[] }
  | { ok: false; diagnostics: Diagnostic[] }
> {
  const temporaryParent = resolve(options.projectRoot, '.gobeyond-build-exec')
  await mkdir(temporaryParent, { recursive: true })
  const temporaryDirectory = await mkdtemp(resolve(temporaryParent, 'route-'))
  try {
    const emitted = compileBuildModules(
      options.projectRoot,
      [options.buildFile, options.metadataFile].filter((file): file is string => !!file),
      temporaryDirectory,
    )
    if (!emitted.ok) return emitted

    const workerFile = fileURLToPath(new URL('./build-worker.js', import.meta.url))
    const response = await runWorker(
      workerFile,
      options.projectRoot,
      {
        ...(options.buildFile
          ? { buildModuleFile: emitted.moduleFiles.get(options.buildFile)! }
          : {}),
        ...(options.metadataFile
          ? { metadataModuleFile: emitted.moduleFiles.get(options.metadataFile)! }
          : {}),
        params: options.params,
      },
      options.timeoutMs,
    )
    if (!response.ok) {
      return failure(
        options.buildFile ?? options.metadataFile ?? options.projectRoot,
        'GB1243',
        `Static build module failed: ${response.error}`,
      )
    }
    return { ok: true, entries: response.entries }
  } finally {
    await rm(temporaryDirectory, { recursive: true, force: true })
    // Remove the shared parent only when no concurrent route still owns it.
    try { await rm(temporaryParent) } catch { /* another route may still own it */ }
  }
}

function compileBuildModules(
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
  }
  const program = ts.createProgram(moduleFiles, compilerOptions)
  const errors = ts.getPreEmitDiagnostics(program).filter(
    (candidate) => candidate.category === ts.DiagnosticCategory.Error,
  )
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
    outputs.set(moduleFile, resolve(
      outDirectory,
      `${relativeModuleFile.slice(0, -extension.length)}${outputExtension}`,
    ))
  }
  return { ok: true, moduleFiles: outputs }
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
    protocolStream?.on('data', (chunk: string) => { protocol += chunk })
    child.stderr.setEncoding('utf8')
    child.stderr.on('data', (chunk: string) => { stderr += chunk })
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
        const parsed = JSON.parse(protocol) as WorkerResponse
        resolvePromise(parsed)
      } catch {
        resolvePromise({
          ok: false,
          error: `worker exited with code ${code ?? 'unknown'} without a valid result${stderr.trim() ? `: ${stderr.trim()}` : ''}`,
        })
      }
    })
  })
}

function validateSchema(schema: ValueSchema, value: unknown, path: string): string | undefined {
  if (value === null) return schema.nullable ? undefined : `${path} must not be null.`
  if (value === undefined) return schema.optional ? undefined : `${path} is required.`
  switch (schema.kind) {
    case 'string':
    case 'safeHtml':
      return typeof value === 'string' ? undefined : `${path} must be a string.`
    case 'datetime':
      return typeof value === 'string' && isRFC3339(value)
        ? undefined
        : `${path} must be an RFC 3339 date-time string.`
    case 'bytes':
      return typeof value === 'string' && isBase64(value)
        ? undefined
        : `${path} must be a base64 string.`
    case 'number':
      return typeof value === 'number' && Number.isFinite(value)
        ? undefined
        : `${path} must be a finite number.`
    case 'integer':
      return typeof value === 'number' && Number.isSafeInteger(value)
        ? undefined
        : `${path} must be a safe integer.`
    case 'boolean':
      return typeof value === 'boolean' ? undefined : `${path} must be a boolean.`
    case 'literal':
      return Object.is(value, schema.value)
        ? undefined
        : `${path} must equal ${JSON.stringify(schema.value)}.`
    case 'enum':
      return typeof value === 'string' && schema.values?.includes(value)
        ? undefined
        : `${path} must be one of ${JSON.stringify(schema.values ?? [])}.`
    case 'array': {
      if (!Array.isArray(value)) return `${path} must be an array.`
      if (!schema.items) return `${path} has an invalid array schema.`
      for (let index = 0; index < value.length; index += 1) {
        const issue = validateSchema(schema.items, value[index], `${path}[${index}]`)
        if (issue) return issue
      }
      return undefined
    }
    case 'object': {
      if (!isPlainObject(value)) return `${path} must be an object.`
      const shape = schema.shape ?? {}
      for (const name of Object.keys(value)) {
        if (!(name in shape)) return `${path}.${name} is not declared in the page schema.`
      }
      for (const [name, propertySchema] of Object.entries(shape)) {
        const issue = validateSchema(propertySchema, value[name], `${path}.${name}`)
        if (issue) return issue
      }
      return undefined
    }
    case 'union': {
      const variants = schema.variants ?? []
      if (variants.some((variant) => validateSchema(variant, value, path) === undefined)) {
        return undefined
      }
      return `${path} does not match any union variant.`
    }
  }
}

function validateMetadata(metadata: Record<string, unknown>): string | undefined {
  const allowed = new Set([
    'lang', 'title', 'description', 'canonical', 'robots',
    'openGraph', 'twitter', 'alternates', 'jsonLd',
  ])
  for (const name of Object.keys(metadata)) {
    if (!allowed.has(name)) return `unknown field ${JSON.stringify(name)}.`
  }
  for (const name of ['lang', 'title', 'description', 'canonical', 'robots'] as const) {
    if (typeof metadata[name] !== 'string' || metadata[name].trim() === '') {
      return `${name} must be a non-empty string.`
    }
  }
  if (!isAbsoluteHTTPURL(metadata.canonical as string)) {
    return 'canonical must be an absolute HTTP(S) URL.'
  }
  const openGraphIssue = validateSocialMetadata(metadata.openGraph, 'openGraph', true)
  if (openGraphIssue) return openGraphIssue
  const twitterIssue = validateSocialMetadata(metadata.twitter, 'twitter', false)
  if (twitterIssue) return twitterIssue
  if (metadata.alternates !== undefined) {
    if (!Array.isArray(metadata.alternates)) return 'alternates must be an array.'
    for (let index = 0; index < metadata.alternates.length; index += 1) {
      const alternate = metadata.alternates[index]
      if (
        !isPlainObject(alternate) ||
        typeof alternate.language !== 'string' || alternate.language === '' ||
        typeof alternate.url !== 'string' || !isAbsoluteHTTPURL(alternate.url)
      ) return `alternates[${index}] must contain language and an absolute HTTP(S) url.`
    }
  }
  if (metadata.jsonLd !== undefined) {
    if (!Array.isArray(metadata.jsonLd)) return 'jsonLd must be an array.'
    for (let index = 0; index < metadata.jsonLd.length; index += 1) {
      if (!isPlainObject(metadata.jsonLd[index])) {
        return `jsonLd[${index}] must be an object.`
      }
    }
  }
  return undefined
}

function validateSocialMetadata(
  value: unknown,
  name: 'openGraph' | 'twitter',
  requireURL: boolean,
): string | undefined {
  if (!isPlainObject(value)) return `${name} must be an object.`
  const requiredStrings = requireURL
    ? ['type', 'title', 'description', 'url']
    : ['card', 'title', 'description']
  for (const field of requiredStrings) {
    if (typeof value[field] !== 'string' || value[field].trim() === '') {
      return `${name}.${field} must be a non-empty string.`
    }
  }
  if (requireURL && !isAbsoluteHTTPURL(value.url as string)) {
    return `${name}.url must be an absolute HTTP(S) URL.`
  }
  if (
    !Array.isArray(value.images) || value.images.length === 0 ||
    !value.images.every((image) => typeof image === 'string' && isAbsoluteHTTPURL(image))
  ) return `${name}.images must contain absolute HTTP(S) URLs.`
  return undefined
}

function isAbsoluteHTTPURL(value: string): boolean {
  try {
    const parsed = new URL(value)
    return (parsed.protocol === 'http:' || parsed.protocol === 'https:') && parsed.host !== ''
  } catch { return false }
}

function parseParamDefinitions(pattern: string): ParamDefinition[] {
  const definitions: ParamDefinition[] = []
  for (const segment of pattern.split('/')) {
    const optionalCatchAll = /^\[\[\.\.\.([^\]]+)\]\]$/.exec(segment)
    if (optionalCatchAll) {
      definitions.push({ name: optionalCatchAll[1]!, catchAll: true, optional: true })
      continue
    }
    const catchAll = /^\[\.\.\.([^\]]+)\]$/.exec(segment)
    if (catchAll) {
      definitions.push({ name: catchAll[1]!, catchAll: true, optional: false })
      continue
    }
    const dynamic = /^\[([^\]]+)\]$/.exec(segment)
    if (dynamic) definitions.push({ name: dynamic[1]!, catchAll: false, optional: false })
  }
  return definitions
}

function deriveRoutePattern(
  projectRoot: string,
  entryFile: string,
  configuredAppDirectory?: string,
): string {
  const appRoot = resolveAppRoot(projectRoot, entryFile, configuredAppDirectory)
  return relative(appRoot, dirname(entryFile)).split(sep).join('/')
}

function resolveAppRoot(
  projectRoot: string,
  entryFile: string,
  configuredAppDirectory?: string,
): string {
  if (configuredAppDirectory) {
    return isAbsolute(configuredAppDirectory)
      ? resolve(configuredAppDirectory)
      : resolve(projectRoot, configuredAppDirectory)
  }
  const relativeEntry = relative(projectRoot, entryFile).split(sep)
  const appIndex = relativeEntry.lastIndexOf('app')
  return appIndex === -1
    ? dirname(entryFile)
    : resolve(projectRoot, ...relativeEntry.slice(0, appIndex + 1))
}

function isRFC3339(value: string): boolean {
  return /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(value) &&
    Number.isFinite(Date.parse(value))
}

function isBase64(value: string): boolean {
  if (!/^(?:[A-Za-z0-9+/]{4})*(?:[A-Za-z0-9+/]{2}==|[A-Za-z0-9+/]{3}=)?$/.test(value)) {
    return false
  }
  return Buffer.from(value, 'base64').toString('base64') === value
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  return !!value && typeof value === 'object' && !Array.isArray(value) &&
    Object.getPrototypeOf(value) === Object.prototype
}

async function isFile(fileName: string): Promise<boolean> {
  try { return (await stat(fileName)).isFile() } catch { return false }
}

function isWithin(root: string, target: string): boolean {
  const path = relative(resolve(root), resolve(target))
  return path === '' || (!path.startsWith(`..${sep}`) && path !== '..' && !isAbsolute(path))
}

function toProjectPath(projectRoot: string, fileName: string): string {
  return relative(projectRoot, fileName).split(sep).join('/')
}

function typescriptDiagnostic(candidate: ts.Diagnostic, fallbackFile: string): Diagnostic {
  const file = candidate.file
  const start = candidate.start ?? 0
  const location = file?.getLineAndCharacterOfPosition(start)
  return {
    code: `TS${candidate.code}`,
    message: ts.flattenDiagnosticMessageText(candidate.messageText, '\n'),
    suggestion: 'Fix the build-only TypeScript module before generating static props.',
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

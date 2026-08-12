import { readFile, realpath, stat } from 'node:fs/promises'
import {
  dirname,
  extname,
  isAbsolute,
  relative,
  resolve,
} from 'node:path'
import ts from 'typescript'

import {
  attachSharedTypeProgram,
  SourceCompiler,
  type CompilationContext,
  type ComponentImport,
} from './compiler.js'
import {
  compileActionContractSource,
  compilePageContractSource,
} from './contracts.js'
import { createDiagnostic } from './diagnostics.js'
import {
  compileStaticRoute,
  discoverRouteLayouts,
} from './static-build.js'
import {
  CLIENT_BOUNDARY_API_VERSION,
  COMPILER_PROJECT_API_VERSION,
  STATIC_BUILD_API_VERSION,
  VALUE_CONTRACT_API_VERSION,
  type ActionValueContract,
  type CompileFileOptions,
  type CompileProjectOptions,
  type CompileProjectResult,
  type CompileResult,
  type Diagnostic,
  type PageContractCompileResult,
  type ProjectRoute,
  type ProjectRouteModules,
  type RouteValueContract,
  type SourceRoot,
} from './types.js'

type Resolution =
  | { kind: 'source'; fileName: string }
  | { kind: 'package'; specifier: string }
  | { kind: 'asset'; specifier: string }
  | { kind: 'unresolved'; specifier: string; reason: string }

type ResolvedSourceRoot = { prefix: string; directory: string }

const sourceExtensions = ['.tsx', '.ts', '.jsx', '.js', '.mts', '.cts'] as const
const replaceableExtensions = new Set(['.js', '.jsx', '.mjs', '.cjs'])

class SourceGraph {
  readonly context: CompilationContext
  readonly compilers = new Map<string, SourceCompiler>()
  readonly sourceFiles = new Map<string, ts.SourceFile>()
  readonly loading = new Map<string, Promise<SourceCompiler | undefined>>()
  readonly graphDiagnostics: Diagnostic[] = []
  readonly projectRoot: string
  readonly sourceRoots: ResolvedSourceRoot[]
  readonly appDirectory: string | undefined
  readonly packageRoots = new Set<string>()
  private typeProgramAttached = false

  constructor(
    projectRoot: string,
    sourceRoots: readonly SourceRoot[],
    appDirectory?: string,
  ) {
    this.projectRoot = resolve(projectRoot)
    this.appDirectory = appDirectory
    this.sourceRoots = sourceRoots.map((root) => ({
      prefix: root.prefix,
      directory: resolve(this.projectRoot, root.directory),
    }))
    this.context = {
      componentStack: [],
      diagnostics: [],
      clientBoundaries: [],
      useIdSites: [],
      dateIntrinsicSites: [],
      eachKeyStack: [],
      rejectUseIdFrames: [],
      conditionalHookDepth: 0,
      routeId: '',
      sourceName: (fileName) => toProjectPath(this.projectRoot, fileName),
    }
    for (const root of this.sourceRoots) {
      if (root.prefix.length === 0) {
        this.graphDiagnostics.push(this.globalDiagnostic(
          'GB1100',
          'A source-root import prefix cannot be empty.',
          this.projectRoot,
        ))
      }
    }
  }

  async compileRoute(route: ProjectRoute): Promise<CompileResult> {
    const boundaryStart = this.context.clientBoundaries.length
    const useIdStart = this.context.useIdSites.length
    const dateStart = this.context.dateIntrinsicSites.length
    const entryFile = resolve(this.projectRoot, route.entryFile)
    if (!this.isAllowedSourcePath(entryFile)) {
      return {
        ok: false,
        diagnostics: [this.globalDiagnostic(
          'GB1101',
          `Entry file ${JSON.stringify(entryFile)} is outside the configured project/source roots.`,
          entryFile,
        )],
      }
    }
    const compiler = await this.load(entryFile, true)
    if (!compiler) return { ok: false, diagnostics: this.allDiagnostics() }
    this.attachTypeProgram()
    let plan = compiler.compile(route.routeId, route.componentName)
    const layouts = await this.routeLayouts(entryFile)
    for (const layoutFile of [...layouts].reverse()) {
      if (!plan) break
      const layoutCompiler = await this.load(layoutFile, true)
      if (!layoutCompiler) break
      plan = layoutCompiler.compileAround(route.routeId, plan.root)
    }
    const diagnostics = this.allDiagnostics()
    if (!plan || diagnostics.length > 0) return { ok: false, diagnostics }
    return {
      ok: true,
      plan,
      clientBoundaries: this.context.clientBoundaries.slice(boundaryStart),
      useIdSites: this.context.useIdSites.slice(useIdStart),
      dateIntrinsicSites: this.context.dateIntrinsicSites.slice(dateStart),
      diagnostics: [],
    }
  }

  async prepareRoute(route: ProjectRoute): Promise<void> {
    const entryFile = resolve(this.projectRoot, route.entryFile)
    if (!this.isAllowedSourcePath(entryFile)) return
    await this.load(entryFile, true)
    const layouts = await this.routeLayouts(entryFile)
    for (const layoutFile of layouts) await this.load(layoutFile, true)
  }

  attachTypeProgram(): void {
    if (this.typeProgramAttached) return
    this.typeProgramAttached = true
    attachSharedTypeProgram([...this.compilers.values()])
  }

  async routeLayouts(entryFile: string): Promise<string[]> {
    return discoverRouteLayouts(
      this.projectRoot,
      resolve(entryFile),
      this.appDirectory,
    )
  }

  allDiagnostics(): Diagnostic[] {
    return [
      ...this.graphDiagnostics,
      ...this.context.diagnostics,
    ].sort(compareDiagnostics)
  }

  private async load(
    fileName: string,
    entry = false,
    requestedExports?: ReadonlySet<string>,
  ): Promise<SourceCompiler | undefined> {
    const absoluteFile = resolve(fileName)
    const loadKey = `${absoluteFile}\0${requestedExports
      ? [...requestedExports].sort().join(',')
      : '*'}`
    const existing = this.compilers.get(loadKey)
    if (existing) return existing
    const inFlight = this.loading.get(loadKey)
    if (inFlight) return inFlight

    const promise = this.loadUncached(absoluteFile, entry, requestedExports, loadKey)
    this.loading.set(loadKey, promise)
    return promise
  }

  private async loadUncached(
    fileName: string,
    entry: boolean,
    requestedExports: ReadonlySet<string> | undefined,
    loadKey: string,
  ): Promise<SourceCompiler | undefined> {
    let sourceText: string
    try {
      sourceText = await readFile(fileName, 'utf8')
    } catch (error) {
      this.graphDiagnostics.push(this.globalDiagnostic(
        entry ? 'GB1102' : 'GB1103',
        `Cannot read ${entry ? 'entry file' : 'source module'} ${JSON.stringify(fileName)}: ${errorMessage(error)}.`,
        fileName,
      ))
      return undefined
    }

    let sourceFile = this.sourceFiles.get(fileName)
    if (!sourceFile) {
      sourceFile = ts.createSourceFile(
        fileName,
        sourceText,
        ts.ScriptTarget.Latest,
        true,
        ts.ScriptKind.TSX,
      )
      this.sourceFiles.set(fileName, sourceFile)
    }
    const compiler = new SourceCompiler(sourceText, fileName, this.context, {
      sourceFile,
      deferTypeChecking: true,
    })
    // Register before traversing imports so an import cycle terminates. A render
    // cycle is diagnosed later using the shared component stack.
    this.compilers.set(loadKey, compiler)

    const componentReferences = referencedComponentNames(compiler.sourceFile)
    const forwardedImports = forwardedImportNames(compiler.sourceFile)

    for (const statement of compiler.sourceFile.statements) {
      if (!ts.isImportDeclaration(statement) || !ts.isStringLiteral(statement.moduleSpecifier)) {
        continue
      }
      const specifier = statement.moduleSpecifier.text
      const bindings = importBindings(statement).filter((binding) =>
        componentReferences.has(binding.localName) ||
        forwardedImports.has(binding.localName),
      )
      if (statement.importClause?.isTypeOnly || bindings.length === 0) continue
      const resolution = await this.resolveImport(fileName, specifier)

      if (resolution.kind === 'asset') continue
      if (resolution.kind === 'package') {
        for (const binding of bindings) {
          compiler.registerImport(binding.localName, {
            kind: 'package',
            specifier,
          })
        }
        continue
      }
      if (resolution.kind === 'unresolved') {
        compiler.reportImport(
          statement.moduleSpecifier,
          'GB1104',
          `Cannot resolve project import ${JSON.stringify(specifier)}: ${resolution.reason}`,
          'Fix the relative path or configure an explicit source-root alias.',
        )
        for (const binding of bindings) {
          compiler.registerImport(binding.localName, {
            kind: 'unresolved',
            specifier,
            reason: resolution.reason,
          })
        }
        continue
      }

      const importedCompiler = await this.load(
        resolution.fileName,
        false,
        new Set(bindings.map((binding) => binding.exportName)),
      )
      if (!importedCompiler) {
        for (const binding of bindings) {
          compiler.registerImport(binding.localName, {
            kind: 'unresolved',
            specifier,
            reason: `resolved module ${resolution.fileName} could not be read`,
          })
        }
        continue
      }
      for (const binding of bindings) {
        const reference: ComponentImport = {
          kind: 'local',
          compiler: importedCompiler,
          exportName: binding.exportName,
          specifier,
        }
        compiler.registerImport(binding.localName, reference)
      }
    }
    for (const statement of compiler.sourceFile.statements) {
      if (
        !ts.isExportDeclaration(statement) ||
        statement.isTypeOnly ||
        !statement.exportClause ||
        !ts.isNamedExports(statement.exportClause) ||
        statement.moduleSpecifier
      ) continue
      for (const element of statement.exportClause.elements) {
        if (requestedExports && !requestedExports.has(element.name.text)) continue
        if (element.isTypeOnly) continue
        const localName = element.propertyName?.text ?? element.name.text
        const reference = compiler.imports.get(localName)
        if (reference) compiler.registerReexport(element.name.text, reference)
      }
    }
    for (const statement of compiler.sourceFile.statements) {
      if (
        !ts.isExportDeclaration(statement) ||
        statement.isTypeOnly ||
        !statement.moduleSpecifier ||
        !ts.isStringLiteral(statement.moduleSpecifier)
      ) continue
      const specifier = statement.moduleSpecifier.text
      const selectedElements = statement.exportClause &&
        ts.isNamedExports(statement.exportClause)
        ? statement.exportClause.elements.filter((element) =>
            !element.isTypeOnly &&
            (!requestedExports || requestedExports.has(element.name.text)),
          )
        : undefined
      if (selectedElements && selectedElements.length === 0) continue
      const resolution = await this.resolveImport(fileName, specifier)
      if (resolution.kind === 'asset') continue
      if (resolution.kind === 'package') {
        if (selectedElements) {
          for (const element of selectedElements) {
            compiler.registerReexport(element.name.text, {
              kind: 'package',
              specifier,
            })
          }
        }
        continue
      }
      if (resolution.kind === 'unresolved') {
        compiler.reportImport(
          statement.moduleSpecifier,
          'GB1105',
          `Cannot resolve re-export ${JSON.stringify(specifier)}: ${resolution.reason}`,
          'Fix the package export, relative path, or source-root alias.',
        )
        continue
      }
      const targetExports = selectedElements
        ? new Set(selectedElements.map((element) =>
            element.propertyName?.text ?? element.name.text,
          ))
        : requestedExports
      const targetCompiler = await this.load(
        resolution.fileName,
        false,
        targetExports,
      )
      if (!targetCompiler) continue
      if (!statement.exportClause) {
        compiler.registerStarExport(targetCompiler)
        if (
          requestedExports &&
          [...requestedExports].every((name) => compiler.hasExport(name))
        ) break
        continue
      }
      if (selectedElements) {
        for (const element of selectedElements) {
          compiler.registerReexport(element.name.text, {
            kind: 'local',
            compiler: targetCompiler,
            exportName: element.propertyName?.text ?? element.name.text,
            specifier,
          })
        }
      }
    }
    return compiler
  }

  private async resolveImport(fromFile: string, specifier: string): Promise<Resolution> {
    if (isNonSourceAsset(specifier)) return { kind: 'asset', specifier }

    let unresolvedBase: string | undefined
    if (specifier.startsWith('.')) {
      unresolvedBase = resolve(dirname(fromFile), specifier)
    } else if (isAbsolute(specifier)) {
      unresolvedBase = resolve(specifier)
    } else {
      if (
        specifier === 'react' ||
        specifier.startsWith('react/') ||
        specifier === '@go-beyond/react' ||
        specifier.startsWith('@go-beyond/react/')
      ) return { kind: 'package', specifier }
      const sourceRoot = this.sourceRoots
        .filter((root) => specifier.startsWith(root.prefix))
        .sort((left, right) => right.prefix.length - left.prefix.length)[0]
      if (!sourceRoot) {
        const packageResolution = await this.resolvePackageImport(
          fromFile,
          specifier,
        )
        return packageResolution ?? { kind: 'package', specifier }
      }
      unresolvedBase = resolve(
        sourceRoot.directory,
        specifier.slice(sourceRoot.prefix.length),
      )
    }

    if (!this.isAllowedSourcePath(unresolvedBase)) {
      return {
        kind: 'unresolved',
        specifier,
        reason: 'resolved path escapes the configured project/source roots',
      }
    }

    for (const candidate of sourceCandidates(unresolvedBase)) {
      if (!this.isAllowedSourcePath(candidate)) continue
      if (await isFile(candidate)) return { kind: 'source', fileName: candidate }
    }
    return {
      kind: 'unresolved',
      specifier,
      reason: `no TypeScript/TSX source file exists at ${unresolvedBase}`,
    }
  }

  private isAllowedSourcePath(fileName: string): boolean {
    return [
      this.projectRoot,
      ...this.sourceRoots.map((root) => root.directory),
      ...this.packageRoots,
    ]
      .some((root) => isWithin(root, fileName))
  }

  private async resolvePackageImport(
    fromFile: string,
    specifier: string,
  ): Promise<Resolution | undefined> {
    const parsed = packageSpecifier(specifier)
    if (!parsed) return undefined
    const startDirectories = [dirname(fromFile)]
    try {
      const physicalDirectory = dirname(await realpath(fromFile))
      if (!startDirectories.includes(physicalDirectory)) {
        startDirectories.push(physicalDirectory)
      }
    } catch {
      // The caller reports unreadable source modules; resolution can still try
      // the logical path here.
    }
    const searched = new Set<string>()
    for (const directory of ancestorDirectories(startDirectories)) {
      const packageRoot = resolve(directory, 'node_modules', parsed.name)
      if (searched.has(packageRoot)) continue
      searched.add(packageRoot)
      const packageJSONFile = resolve(packageRoot, 'package.json')
      const packageJSONSource = await readOptionalFile(packageJSONFile)
      if (packageJSONSource !== undefined) {
        let packageJSON: PackageJSON
        try {
          packageJSON = JSON.parse(packageJSONSource) as PackageJSON
        } catch (error) {
          return {
            kind: 'unresolved',
            specifier,
            reason: `invalid ${packageJSONFile}: ${errorMessage(error)}`,
          }
        }
        const target = packageEntry(packageJSON, parsed.subpath)
        if (!target) {
          return {
            kind: 'unresolved',
            specifier,
            reason: `${packageJSONFile} does not export ${JSON.stringify(parsed.subpath || '.')}`,
          }
        }
        const unresolvedTarget = resolve(packageRoot, target)
        if (!isWithin(packageRoot, unresolvedTarget)) {
          return {
            kind: 'unresolved',
            specifier,
            reason: 'package export target escapes its package root',
          }
        }
        for (const candidate of sourceCandidates(unresolvedTarget)) {
          if (await isFile(candidate)) {
            this.packageRoots.add(packageRoot)
            return { kind: 'source', fileName: candidate }
          }
        }
        // Workspace packages may intentionally point exports at build output
        // that has not been emitted yet. Keep the import opaque here; if it is
        // rendered as a component, GB1053 remains a fatal module diagnostic.
        return { kind: 'package', specifier }
      }
    }
    return undefined
  }

  private globalDiagnostic(code: string, message: string, fileName: string): Diagnostic {
    return {
      code,
      message,
      fileName,
      start: 0,
      length: 1,
      line: 1,
      column: 1,
    }
  }
}

type PackageJSON = {
  exports?: unknown
  module?: string
  main?: string
}

function packageSpecifier(
  specifier: string,
): { name: string; subpath: string } | undefined {
  if (specifier.startsWith('#')) return undefined
  const parts = specifier.split('/')
  const name = specifier.startsWith('@')
    ? parts.slice(0, 2).join('/')
    : parts[0]
  if (!name) return undefined
  const consumed = name.split('/').length
  return { name, subpath: parts.slice(consumed).join('/') }
}

function ancestorDirectories(startDirectories: readonly string[]): string[] {
  const result: string[] = []
  for (const start of startDirectories) {
    let directory = start
    while (true) {
      result.push(directory)
      const parent = dirname(directory)
      if (parent === directory) break
      directory = parent
    }
  }
  return result
}

function referencedComponentNames(sourceFile: ts.SourceFile): Set<string> {
  const names = new Set<string>()
  const visit = (node: ts.Node): void => {
    if (
      (ts.isJsxOpeningElement(node) || ts.isJsxSelfClosingElement(node)) &&
      ts.isIdentifier(node.tagName) &&
      /^[A-Z]/.test(node.tagName.text)
    ) names.add(node.tagName.text)
    if (ts.isCallExpression(node)) {
      const callName = node.expression.getText(sourceFile)
      if (
        [
          'createElement',
          'React.createElement',
          'jsx',
          'jsxs',
          '_jsx',
          '_jsxs',
          'jsxDEV',
          '_jsxDEV',
        ].includes(callName)
      ) {
        const component = node.arguments[0]
        if (component && ts.isIdentifier(component) && /^[A-Z]/.test(component.text)) {
          names.add(component.text)
        }
      }
    }
    ts.forEachChild(node, visit)
  }
  visit(sourceFile)
  return names
}

function forwardedImportNames(sourceFile: ts.SourceFile): Set<string> {
  const names = new Set<string>()
  for (const statement of sourceFile.statements) {
    if (
      !ts.isExportDeclaration(statement) ||
      statement.moduleSpecifier ||
      !statement.exportClause ||
      !ts.isNamedExports(statement.exportClause)
    ) continue
    for (const element of statement.exportClause.elements) {
      if (!element.isTypeOnly) {
        names.add(element.propertyName?.text ?? element.name.text)
      }
    }
  }
  return names
}

function packageEntry(packageJSON: PackageJSON, subpath: string): string | undefined {
  const exportKey = subpath ? `./${subpath}` : '.'
  if (packageJSON.exports !== undefined) {
    const selected = selectPackageExport(packageJSON.exports, exportKey)
    return typeof selected === 'string' ? selected : undefined
  }
  if (subpath) return `./${subpath}`
  return packageJSON.module ?? packageJSON.main ?? './index.js'
}

function selectPackageExport(value: unknown, exportKey: string): string | undefined {
  if (typeof value === 'string') return exportKey === '.' ? value : undefined
  if (Array.isArray(value)) {
    for (const candidate of value) {
      const selected = selectPackageExport(candidate, exportKey)
      if (selected) return selected
    }
    return undefined
  }
  if (!value || typeof value !== 'object') return undefined
  const object = value as Record<string, unknown>
  const subpathKeys = Object.keys(object).filter((key) => key.startsWith('.'))
  if (subpathKeys.length > 0) {
    if (object[exportKey] !== undefined) {
      return selectPackageConditions(object[exportKey])
    }
    for (const key of subpathKeys.sort()) {
      const star = key.indexOf('*')
      if (star === -1) continue
      const prefix = key.slice(0, star)
      const suffix = key.slice(star + 1)
      if (!exportKey.startsWith(prefix) || !exportKey.endsWith(suffix)) continue
      const wildcard = exportKey.slice(prefix.length, exportKey.length - suffix.length)
      const selected = selectPackageConditions(object[key])
      if (selected) return selected.replaceAll('*', wildcard)
    }
    return undefined
  }
  return selectPackageConditions(object)
}

function selectPackageConditions(value: unknown): string | undefined {
  if (typeof value === 'string') return value
  if (Array.isArray(value)) {
    for (const candidate of value) {
      const selected = selectPackageConditions(candidate)
      if (selected) return selected
    }
    return undefined
  }
  if (!value || typeof value !== 'object') return undefined
  const object = value as Record<string, unknown>
  for (const condition of ['import', 'browser', 'module', 'default', 'require']) {
    if (object[condition] === undefined) continue
    const selected = selectPackageConditions(object[condition])
    if (selected) return selected
  }
  return undefined
}

export async function compileFile(options: CompileFileOptions): Promise<CompileResult> {
  const projectRoot = resolve(options.projectRoot ?? process.cwd())
  const graph = new SourceGraph(
    projectRoot,
    options.sourceRoots ?? [],
    options.appDirectory,
  )
  const route: ProjectRoute = {
    routeId: options.routeId,
    entryFile: isAbsolute(options.entryFile)
      ? relative(projectRoot, options.entryFile)
      : options.entryFile,
    ...(options.componentName === undefined ? {} : { componentName: options.componentName }),
  }
  await graph.prepareRoute(route)
  graph.attachTypeProgram()
  return graph.compileRoute(route)
}

export async function compileProject(
  options: CompileProjectOptions,
): Promise<CompileProjectResult> {
  const graph = new SourceGraph(
    options.projectRoot,
    options.sourceRoots ?? [],
    options.appDirectory,
  )
  const routeIds = new Set<string>()
  const plans = []
  const routeContracts: RouteValueContract[] = []
  const actionContracts: ActionValueContract[] = []
  const routeModules = []
  const clientBoundaries = []
  const useIdSites = []
  const dateIntrinsicSites = []
  const staticRoutes = []
  const projectDiagnostics: Diagnostic[] = []

  for (const route of options.routes) await graph.prepareRoute(route)
  graph.attachTypeProgram()

  for (const route of options.routes) {
    if (routeIds.has(route.routeId)) {
      projectDiagnostics.push({
        code: 'GB1110',
        message: `Duplicate project routeId ${JSON.stringify(route.routeId)}.`,
        fileName: resolve(options.projectRoot, route.entryFile),
        start: 0,
        length: 1,
        line: 1,
        column: 1,
      })
      continue
    }
    routeIds.add(route.routeId)
    const result = await graph.compileRoute(route)
    if (result.ok) {
      plans.push(result.plan)
      clientBoundaries.push(...result.clientBoundaries)
      useIdSites.push(...result.useIdSites)
      dateIntrinsicSites.push(...result.dateIntrinsicSites)
    }

    const absoluteEntry = resolve(options.projectRoot, route.entryFile)
    const layouts = await graph.routeLayouts(absoluteEntry)
    const routeModule: ProjectRouteModules = {
      routeId: route.routeId,
      entryFile: toProjectPath(resolve(options.projectRoot), absoluteEntry),
      layoutFiles: layouts.map((fileName) =>
        toProjectPath(resolve(options.projectRoot), fileName)),
    }
    routeModules.push(routeModule)
    const schemaFile = route.schemaFile
      ? resolve(options.projectRoot, route.schemaFile)
      : resolve(dirname(absoluteEntry), 'page.schema.ts')
    const contractResult = await compilePageContractFile(
      schemaFile,
      route.routeId,
      resolve(options.projectRoot),
    )
    if (contractResult === undefined) {
      projectDiagnostics.push({
        code: 'GB1230',
        message: `Route ${JSON.stringify(route.routeId)} requires a page value contract at ${schemaFile}.`,
        suggestion: 'Add page.schema.ts with one exported definePage declaration.',
        fileName: schemaFile,
        start: 0,
        length: 1,
        line: 1,
        column: 1,
      })
    } else if (contractResult.ok) {
      if (contractResult.contract.prefetch?.data) routeModule.prefetch = 'data'
      if (contractResult.contract.prefetch?.images?.length) {
        routeModule.prefetchImages = contractResult.contract.prefetch.images
      }
      const isrDiagnostic = await routeCacheRequiresLoader(
        contractResult.contract,
        absoluteEntry,
        schemaFile,
      )
      if (isrDiagnostic) projectDiagnostics.push(isrDiagnostic)
      routeContracts.push(contractResult.contract)
      if (route.kind === 'static') {
        const staticResult = await compileStaticRoute({
          projectRoot: options.projectRoot,
          route,
          contract: contractResult.contract,
          ...(options.appDirectory === undefined
            ? {}
            : { appDirectory: options.appDirectory }),
        })
        if (staticResult.ok) staticRoutes.push(staticResult.artifact)
        else projectDiagnostics.push(...staticResult.diagnostics)
      }
    }
    else projectDiagnostics.push(...contractResult.diagnostics)

    const actionsFile = route.actionsFile
      ? resolve(options.projectRoot, route.actionsFile)
      : resolve(dirname(absoluteEntry), 'actions.ts')
    const actionsSource = await readOptionalFile(actionsFile)
    if (actionsSource !== undefined) {
      const actionResult = compileActionContractSource({
        sourceText: actionsSource,
        fileName: actionsFile,
        routeId: route.routeId,
      })
      if (actionResult.ok) actionContracts.push(...actionResult.contracts)
      else projectDiagnostics.push(...actionResult.diagnostics)
    } else if (route.actionsFile) {
      projectDiagnostics.push({
        code: 'GB1231',
        message: `Configured actions contract file does not exist: ${actionsFile}.`,
        fileName: actionsFile,
        start: 0,
        length: 1,
        line: 1,
        column: 1,
      })
    }
  }

  const diagnostics = deduplicateDiagnostics([
    ...projectDiagnostics,
    ...graph.allDiagnostics(),
  ])
  if (diagnostics.length > 0 || plans.length !== options.routes.length) {
    return { ok: false, diagnostics }
  }
  return {
    ok: true,
    output: {
      apiVersion: COMPILER_PROJECT_API_VERSION,
      plans,
      contracts: {
        apiVersion: VALUE_CONTRACT_API_VERSION,
        routes: routeContracts,
        actions: actionContracts,
      },
      routeModules,
      clientBoundaries: {
        apiVersion: CLIENT_BOUNDARY_API_VERSION,
        boundaries: clientBoundaries.sort(compareClientBoundaries),
        useIdSites: useIdSites.sort(compareUseIdSites),
        dateIntrinsicSites: dateIntrinsicSites.sort(compareDateIntrinsicSites),
      },
      staticBuild: {
        apiVersion: STATIC_BUILD_API_VERSION,
        routes: staticRoutes,
      },
    },
    diagnostics: [],
  }
}

/**
 * Route caching is only meaningful for a route that computes something at
 * request time, so `definePage({ revalidate })` / `{ tags }` requires the
 * sibling `page.go` loader whose result would be cached. A purely static route
 * is already built once per deploy; accepting the keys there would advertise
 * an ISR window that nothing implements.
 */
async function routeCacheRequiresLoader(
  contract: RouteValueContract,
  entryFile: string,
  schemaFile: string,
): Promise<Diagnostic | undefined> {
  const declared: string[] = []
  if (contract.revalidate !== undefined) declared.push('revalidate')
  if (contract.tags !== undefined) declared.push('tags')
  if (declared.length === 0) return undefined
  const loaderFile = resolve(dirname(entryFile), 'page.go')
  if (await isFile(loaderFile)) return undefined
  return {
    code: 'GB1235',
    message: `Route ${JSON.stringify(contract.routeId)} declares definePage ${declared.join(
      ' and ',
    )}, which requires a request-time loader at ${loaderFile}.`,
    suggestion: 'Add a sibling page.go loader, or remove the route caching keys from page.schema.ts.',
    fileName: schemaFile,
    start: 0,
    length: 1,
    line: 1,
    column: 1,
  }
}

function compareClientBoundaries(
  left: { routeId: string; source: string; start: number; id: string },
  right: { routeId: string; source: string; start: number; id: string },
): number {
  return left.routeId.localeCompare(right.routeId) ||
    left.source.localeCompare(right.source) ||
    left.start - right.start ||
    left.id.localeCompare(right.id)
}

function compareUseIdSites(
  left: { routeId: string; source: string; start: number; id: string },
  right: { routeId: string; source: string; start: number; id: string },
): number {
  return left.routeId.localeCompare(right.routeId) ||
    left.source.localeCompare(right.source) ||
    left.start - right.start ||
    left.id.localeCompare(right.id)
}

function compareDateIntrinsicSites(
  left: { routeId: string; source: string; start: number; getter: string },
  right: { routeId: string; source: string; start: number; getter: string },
): number {
  return left.routeId.localeCompare(right.routeId) ||
    left.source.localeCompare(right.source) ||
    left.start - right.start ||
    left.getter.localeCompare(right.getter)
}

function toProjectPath(projectRoot: string, fileName: string): string {
  return relative(projectRoot, fileName).split(process.platform === 'win32' ? '\\' : '/').join('/')
}

function importBindings(
  declaration: ts.ImportDeclaration,
): Array<{ localName: string; exportName: string }> {
  const clause = declaration.importClause
  if (!clause) return []
  const bindings: Array<{ localName: string; exportName: string }> = []
  if (clause.name) bindings.push({ localName: clause.name.text, exportName: 'default' })
  if (clause.namedBindings && ts.isNamedImports(clause.namedBindings)) {
    for (const element of clause.namedBindings.elements) {
      if (element.isTypeOnly) continue
      bindings.push({
        localName: element.name.text,
        exportName: element.propertyName?.text ?? element.name.text,
      })
    }
  }
  if (clause.namedBindings && ts.isNamespaceImport(clause.namedBindings)) {
    bindings.push({ localName: clause.namedBindings.name.text, exportName: '*' })
  }
  return bindings
}

function sourceCandidates(base: string): string[] {
  const extension = extname(base)
  const candidates = [base]
  if (replaceableExtensions.has(extension)) {
    const withoutExtension = base.slice(0, -extension.length)
    candidates.push(...sourceExtensions.map((candidate) => `${withoutExtension}${candidate}`))
  } else if (extension === '') {
    candidates.push(...sourceExtensions.map((candidate) => `${base}${candidate}`))
    candidates.push(...sourceExtensions.map((candidate) => resolve(base, `index${candidate}`)))
  }
  return [...new Set(candidates.map((candidate) => resolve(candidate)))]
}

function isNonSourceAsset(specifier: string): boolean {
  const withoutQuery = specifier.split(/[?#]/, 1)[0]!
  const extension = extname(withoutQuery)
  return extension !== '' &&
    !sourceExtensions.includes(extension as (typeof sourceExtensions)[number]) &&
    !replaceableExtensions.has(extension)
}

async function isFile(fileName: string): Promise<boolean> {
  try {
    return (await stat(fileName)).isFile()
  } catch {
    return false
  }
}

async function readOptionalFile(fileName: string): Promise<string | undefined> {
  try {
    return await readFile(fileName, 'utf8')
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === 'ENOENT') return undefined
    throw error
  }
}

async function compilePageContractFile(
  fileName: string,
  routeId: string,
  projectRoot: string,
  stack: string[] = [],
): Promise<PageContractCompileResult | undefined> {
  const absoluteFile = resolve(fileName)
  const sourceText = await readOptionalFile(absoluteFile)
  if (sourceText === undefined) return undefined

  const direct = compilePageContractSource({
    sourceText,
    fileName: absoluteFile,
    routeId,
  })
  if (direct.ok || direct.diagnostics.some((diagnostic) => diagnostic.code.startsWith('TS'))) {
    return direct
  }

  const sourceFile = ts.createSourceFile(
    absoluteFile,
    sourceText,
    ts.ScriptTarget.Latest,
    true,
    ts.ScriptKind.TS,
  )
  const forwarding = findDirectPageForward(sourceFile)
  if (!forwarding) return direct

  const cycleStart = stack.indexOf(absoluteFile)
  if (cycleStart !== -1) {
    const cycle = [...stack.slice(cycleStart), absoluteFile]
    return {
      ok: false,
      diagnostics: [createDiagnostic(
        sourceFile,
        forwarding.moduleSpecifier,
        'GB1233',
        `Page schema forwarding cycle: ${cycle.join(' -> ')}.`,
        'Forward page directly to a concrete definePage declaration.',
      )],
    }
  }

  const specifier = forwarding.moduleSpecifier.text
  if (!specifier.startsWith('.')) {
    return {
      ok: false,
      diagnostics: [createDiagnostic(
        sourceFile,
        forwarding.moduleSpecifier,
        'GB1234',
        'Page schema forwarding must use a relative project source import.',
      )],
    }
  }
  const unresolvedTarget = resolve(dirname(absoluteFile), specifier)
  if (!isWithin(projectRoot, unresolvedTarget)) {
    return {
      ok: false,
      diagnostics: [createDiagnostic(
        sourceFile,
        forwarding.moduleSpecifier,
        'GB1234',
        'Page schema forwarding target escapes the configured project root.',
      )],
    }
  }

  let targetFile: string | undefined
  for (const candidate of sourceCandidates(unresolvedTarget)) {
    if (isWithin(projectRoot, candidate) && await isFile(candidate)) {
      targetFile = candidate
      break
    }
  }
  if (!targetFile) {
    return {
      ok: false,
      diagnostics: [createDiagnostic(
        sourceFile,
        forwarding.moduleSpecifier,
        'GB1232',
        `Cannot resolve forwarded page schema ${JSON.stringify(specifier)}.`,
        'Fix the relative path so it resolves to a TypeScript schema module.',
      )],
    }
  }

  const forwarded = await compilePageContractFile(
    targetFile,
    routeId,
    projectRoot,
    [...stack, absoluteFile],
  )
  if (forwarded === undefined) {
    return {
      ok: false,
      diagnostics: [createDiagnostic(
        sourceFile,
        forwarding.moduleSpecifier,
        'GB1232',
        `Cannot read forwarded page schema ${JSON.stringify(targetFile)}.`,
      )],
    }
  }
  return forwarded
}

function findDirectPageForward(sourceFile: ts.SourceFile): {
  moduleSpecifier: ts.StringLiteral
} | undefined {
  for (const statement of sourceFile.statements) {
    if (
      !ts.isExportDeclaration(statement) ||
      !statement.exportClause ||
      !ts.isNamedExports(statement.exportClause) ||
      !statement.moduleSpecifier ||
      !ts.isStringLiteral(statement.moduleSpecifier)
    ) continue
    const forwardsPage = statement.exportClause.elements.some((element) =>
      element.name.text === 'page' &&
      (element.propertyName?.text ?? element.name.text) === 'page',
    )
    if (forwardsPage) return { moduleSpecifier: statement.moduleSpecifier }
  }
  return undefined
}

function isWithin(root: string, target: string): boolean {
  const relativePath = relative(resolve(root), resolve(target))
  return relativePath === '' ||
    (!relativePath.startsWith(`..${process.platform === 'win32' ? '\\' : '/'}`) &&
      relativePath !== '..' &&
      !isAbsolute(relativePath))
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

function compareDiagnostics(left: Diagnostic, right: Diagnostic): number {
  return left.fileName.localeCompare(right.fileName) ||
    left.start - right.start ||
    left.code.localeCompare(right.code)
}

function deduplicateDiagnostics(diagnostics: Diagnostic[]): Diagnostic[] {
  const seen = new Set<string>()
  return diagnostics.filter((diagnostic) => {
    const key = `${diagnostic.fileName}:${diagnostic.start}:${diagnostic.code}:${diagnostic.message}`
    if (seen.has(key)) return false
    seen.add(key)
    return true
  }).sort(compareDiagnostics)
}

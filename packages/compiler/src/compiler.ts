import ts from 'typescript'
import { createHash } from 'node:crypto'

import { createDiagnostic, PortableCompileError } from './diagnostics.js'
import {
  dateIntrinsicName,
  isProtectedHookName,
  isProtectedReactModule,
  type ProtectedHookName,
} from './protected-apis.js'
import {
  RENDER_PLAN_API_VERSION,
  type Attribute,
  type CompileOptions,
  type CompileResult,
  type ClientBoundaryRecord,
  type DateIntrinsicSiteRecord,
  type Diagnostic,
  type PlanExpression,
  type PlanNode,
  type RenderPlan,
  type UseIdSiteRecord,
} from './types.js'

type ExpressionEnvironment = Map<string, PlanExpression>
type NodeEnvironment = Map<string, PlanNode>
type CompiledProperty = {
  name: string
  value?: PlanExpression
  node?: PlanNode
}
type Component =
  | ts.FunctionDeclaration
  | ts.ArrowFunction
  | ts.FunctionExpression

export type ComponentImport =
  | {
      kind: 'local'
      compiler: SourceCompiler
      exportName: string
      specifier: string
    }
  | { kind: 'package'; specifier: string }
  | { kind: 'unresolved'; specifier: string; reason: string }

export type CompilationContext = {
  componentStack: Array<{ key: string; display: string }>
  diagnostics: Diagnostic[]
  clientBoundaries: ClientBoundaryRecord[]
  useIdSites: UseIdSiteRecord[]
  dateIntrinsicSites: DateIntrinsicSiteRecord[]
  /** Active `.map` keys for parametric useId, across every compile unit. */
  eachKeyStack: Array<{ keyText: string; key: PlanExpression }>
  /** One frame per component; true when the component was entered from `.map`. */
  rejectUseIdFrames: boolean[]
  /**
   * Depth of conditional / short-circuit expression branches. Protected hooks
   * under depth > 0 emit GB1085 (except intentional map parametric useId).
   */
  conditionalHookDepth: number
  routeId: string
  sourceName: (fileName: string) => string
}

function standaloneContext(): CompilationContext {
  return {
    componentStack: [],
    diagnostics: [],
    clientBoundaries: [],
    useIdSites: [],
    dateIntrinsicSites: [],
    eachKeyStack: [],
    rejectUseIdFrames: [],
    conditionalHookDepth: 0,
    routeId: '',
    sourceName: (fileName) => fileName.replaceAll('\\', '/'),
  }
}

const helperNames = new Set(['string', 'lower', 'upper', 'join', 'url', 'imageSrc'])
type PortableIntrinsicDefinition = {
  name: string
  getter: string
  stability: 'pure' | 'render-snapshot'
}

const dateProjectionIntrinsics = new Map<string, PortableIntrinsicDefinition>(
  (
    [
      'getFullYear',
      'getUTCFullYear',
      'getMonth',
      'getUTCMonth',
      'getDate',
      'getUTCDate',
      'getHours',
      'getUTCHours',
      'getMinutes',
      'getUTCMinutes',
      'getSeconds',
      'getUTCSeconds',
    ] as const
  ).map((getter) => [
    getter,
    {
      name: dateIntrinsicName(getter)!,
      getter,
      stability: 'render-snapshot' as const,
    },
  ]),
)

const parserControlledTextElements = new Set([
  'iframe',
  'noembed',
  'noframes',
  'noscript',
  'plaintext',
  'script',
  'style',
  'textarea',
  'title',
  'xmp',
])
const booleanAttributes = new Set([
  'allowFullScreen',
  'async',
  'autoFocus',
  'autoPlay',
  'checked',
  'controls',
  'default',
  'defer',
  'disabled',
  'formNoValidate',
  'hidden',
  'loop',
  'multiple',
  'muted',
  'noModule',
  'noValidate',
  'open',
  'playsInline',
  'readOnly',
  'required',
  'reversed',
  'scoped',
  'seamless',
  'selected',
])
const urlAttributes = new Set([
  'action',
  'cite',
  'data',
  'formAction',
  'href',
  'manifest',
  'poster',
  'src',
  'xlinkHref',
])
const actionableTypeDiagnosticCodes = new Set([
  2322, // assignment incompatibility
  2339, // missing property
  2345, // argument incompatibility
  2739, // missing required properties
  2741, // missing required property
  2769, // no overload matches
])

export class SourceCompiler {
  readonly sourceFile: ts.SourceFile
  readonly checker: ts.TypeChecker | undefined
  readonly diagnostics: Diagnostic[]
  readonly isClientModule: boolean
  readonly components = new Map<string, Component>()
  readonly exportedComponents = new Set<string>()
  readonly knownExports = new Set<string>()
  readonly imports = new Map<string, ComponentImport>()
  readonly reexports = new Map<string, ComponentImport>()
  readonly starExports: SourceCompiler[] = []
  readonly nodeScopes: NodeEnvironment[] = []
  readonly namespaceScopes: Array<'html' | 'svg'> = ['html']
  readonly forwardedRefComponents = new Set<Component>()
  /** Local names bound to protected React exports. */
  private readonly reactHookLocals = new Map<string, ProtectedHookName>()
  /** Local names bound to the React module namespace/default. */
  private readonly reactNamespaceLocals = new Set<string>()
  /** Identifiers created via createContext(...) in this module. */
  private readonly contextLocals = new Set<string>()
  /**
   * Module-scope `const` bindings whose initializers compile to portable
   * expressions. Seeded into every component environment in this module.
   */
  private readonly moduleConstants = new Map<string, PlanExpression>()
  /**
   * Module-scope bindings that exist but are not portable constants
   * (`let`/`var`, or `const` with a non-portable initializer). Referencing
   * them in portable expressions emits GB1068.
   */
  private readonly nonPortableModuleBindings = new Map<
    string,
    'const' | 'let' | 'var'
  >()
  /** Function component → portable defaultProps literal fields. */
  private readonly defaultProps = new Map<Component, Map<string, PlanExpression>>()
  /** Per-component JSX locals eligible for limited cloneElement. */
  private readonly jsxElementScopes: Array<Map<string, ts.Expression>> = []
  /** Per-component object literals eligible for style={local}. */
  private readonly styleObjectScopes: Array<Map<string, ts.ObjectLiteralExpression>> = []
  /** Provider value stack: context local name → value expression. */
  private readonly contextValueStack: Array<{
    contextName: string
    value: PlanExpression
  }> = []
  /**
   * Active `.map` keys for parametric useId. Both stacks live on the shared
   * compilation context so a component imported from another module still
   * knows it is being inlined into a keyed array body.
   */
  private get eachKeyStack(): Array<{ keyText: string; key: PlanExpression }> {
    return this.context.eachKeyStack
  }
  /** True while compiling a nested component entered from inside `.map`. */
  private get rejectUseIdFrames(): boolean[] {
    return this.context.rejectUseIdFrames
  }

  constructor(
    readonly sourceText: string,
    readonly fileName: string,
    readonly context: CompilationContext = standaloneContext(),
  ) {
    this.diagnostics = context.diagnostics
    this.sourceFile = ts.createSourceFile(
      fileName,
      sourceText,
      ts.ScriptTarget.Latest,
      true,
      ts.ScriptKind.TSX,
    )
    this.collectProtectedReactHooks()
    const typeProgram = /\.(?:tsx?|mts|cts)$/i.test(this.fileName)
      ? this.createTypeChecker()
      : { checker: undefined, diagnostics: [] }
    this.checker = typeProgram.checker
    const parseDiagnostics = (
      this.sourceFile as ts.SourceFile & {
        parseDiagnostics?: readonly ts.DiagnosticWithLocation[]
      }
    ).parseDiagnostics
    for (const diagnostic of parseDiagnostics ?? []) {
      const start = diagnostic.start
      const location = this.sourceFile.getLineAndCharacterOfPosition(start)
      this.diagnostics.push({
        code: `TS${diagnostic.code}`,
        message: ts.flattenDiagnosticMessageText(diagnostic.messageText, '\n'),
        suggestion:
          'Fix the TypeScript/TSX syntax before compiling the portable render plan.',
        fileName: this.sourceFile.fileName,
        start,
        length: diagnostic.length,
        line: location.line + 1,
        column: location.character + 1,
      })
    }
    for (const diagnostic of typeProgram.diagnostics) {
      if (
        diagnostic.start === undefined ||
        !actionableTypeDiagnosticCodes.has(diagnostic.code)
      ) continue
      const location = this.sourceFile.getLineAndCharacterOfPosition(
        diagnostic.start,
      )
      this.diagnostics.push({
        code: `TS${diagnostic.code}`,
        message: ts.flattenDiagnosticMessageText(diagnostic.messageText, '\n'),
        suggestion: 'Fix the TypeScript type error before compiling the render plan.',
        fileName: this.sourceFile.fileName,
        start: diagnostic.start,
        length: diagnostic.length,
        line: location.line + 1,
        column: location.character + 1,
      })
    }
    this.isClientModule = hasClientDirective(this.sourceFile)
    this.collectComponents()
    this.collectDefaultProps()
    this.collectModuleContextLocals()
    this.collectModuleConstants()
  }

  private createTypeChecker(): {
    checker: ts.TypeChecker | undefined
    diagnostics: readonly ts.DiagnosticWithLocation[]
  } {
    const options: ts.CompilerOptions = {
      target: ts.ScriptTarget.ES2022,
      module: ts.ModuleKind.NodeNext,
      moduleResolution: ts.ModuleResolutionKind.NodeNext,
      jsx: ts.JsxEmit.ReactJSX,
      strict: true,
      noEmit: true,
      noResolve: true,
      skipLibCheck: true,
    }
    const host = ts.createCompilerHost(options)
    const getSourceFile = host.getSourceFile.bind(host)
    const fileExists = host.fileExists.bind(host)
    const readFile = host.readFile.bind(host)
    host.getSourceFile = (
      name,
      languageVersion,
      onError,
      shouldCreateNewSourceFile,
    ) =>
      name === this.fileName
        ? this.sourceFile
        : getSourceFile(
            name,
            languageVersion,
            onError,
            shouldCreateNewSourceFile,
          )
    host.fileExists = (name) => name === this.fileName || fileExists(name)
    host.readFile = (name) =>
      name === this.fileName ? this.sourceText : readFile(name)
    const program = ts.createProgram([this.fileName], options, host)
    return {
      checker: program.getTypeChecker(),
      diagnostics: /\.(?:tsx?|mts|cts)$/i.test(this.fileName)
        ? program
            .getSemanticDiagnostics(this.sourceFile)
            .filter((diagnostic): diagnostic is ts.DiagnosticWithLocation =>
              diagnostic.file === this.sourceFile && diagnostic.start !== undefined,
            )
        : [],
    }
  }

  registerImport(localName: string, reference: ComponentImport): void {
    this.imports.set(localName, reference)
  }

  registerReexport(exportName: string, reference: ComponentImport): void {
    this.reexports.set(exportName, reference)
    if (reference.kind === 'local') this.knownExports.add(exportName)
  }

  registerStarExport(compiler: SourceCompiler): void {
    this.starExports.push(compiler)
  }

  reportImport(
    node: ts.Node,
    code: string,
    message: string,
    suggestion?: string,
  ): void {
    this.report(node, code, message, suggestion)
  }

  resolveExport(
    exportName: string,
    visited: Set<string> = new Set(),
  ): {
    name: string
    component: Component
    compiler: SourceCompiler
    clientBoundary?: SourceCompiler
  } | undefined {
    const visitKey = `${this.fileName}#${exportName}`
    if (visited.has(visitKey)) return undefined
    visited.add(visitKey)
    if (exportName === 'default') {
      const component = this.findDefaultComponent()
      if (component) {
        return {
          name: this.componentDisplayName(component),
          component,
          compiler: this,
          ...(this.isClientModule ? { clientBoundary: this } : {}),
        }
      }
    } else if (this.exportedComponents.has(exportName)) {
      const component = this.components.get(exportName)
      if (component) {
        return {
          name: exportName,
          component,
          compiler: this,
          ...(this.isClientModule ? { clientBoundary: this } : {}),
        }
      }
    }
    const reexport = this.reexports.get(exportName)
    if (reexport?.kind === 'local') {
      const result = reexport.compiler.resolveExport(reexport.exportName, visited)
      return result && this.isClientModule
        ? { ...result, clientBoundary: this }
        : result
    }
    if (exportName !== 'default') {
      for (const compiler of this.starExports) {
        const component = compiler.resolveExport(exportName, visited)
        if (component) {
          return this.isClientModule
            ? { ...component, clientBoundary: this }
            : component
        }
      }
    }
    return undefined
  }

  hasExport(exportName: string, visited: Set<string> = new Set()): boolean {
    const visitKey = `${this.fileName}#${exportName}`
    if (visited.has(visitKey)) return false
    visited.add(visitKey)
    if (this.knownExports.has(exportName)) return true
    if (exportName !== 'default') {
      return this.starExports.some((compiler) =>
        compiler.hasExport(exportName, visited),
      )
    }
    return false
  }

  clientBoundaryForExport(
    exportName: string,
    visited: Set<string> = new Set(),
  ): SourceCompiler | undefined {
    const visitKey = `${this.fileName}#${exportName}`
    if (visited.has(visitKey)) return undefined
    visited.add(visitKey)
    if (this.isClientModule && this.hasExport(exportName)) return this
    const reexport = this.reexports.get(exportName)
    if (reexport?.kind === 'local') {
      return reexport.compiler.clientBoundaryForExport(
        reexport.exportName,
        visited,
      )
    }
    if (exportName !== 'default') {
      for (const compiler of this.starExports) {
        if (!compiler.hasExport(exportName)) continue
        const boundary = compiler.clientBoundaryForExport(exportName, visited)
        if (boundary) return boundary
      }
    }
    return undefined
  }

  compile(routeId: string, componentName?: string): RenderPlan | undefined {
    this.context.routeId = routeId
    if (routeId.trim() === '') {
      this.report(
        this.sourceFile,
        'GB1000',
        'routeId must be a non-empty stable route identifier.',
      )
      return undefined
    }

    const component = componentName
      ? this.components.get(componentName)
      : this.findDefaultComponent()
    if (!component) {
      this.report(
        this.sourceFile,
        'GB1001',
        componentName
          ? `Cannot find function component ${JSON.stringify(componentName)} in this compile unit.`
          : 'Cannot find a default-exported function component.',
        'Export a function component as default, or pass componentName for a local component.',
      )
      return undefined
    }

    const name = componentName ?? this.componentDisplayName(component)
    const root = this.isClientModule
      ? this.compileAtClientBoundary(
          name,
          component,
          this,
          'component',
          () => this.compileComponent(name, component, new Map(), true),
        )
      : this.compileComponent(name, component, new Map(), true)
    if (root) this.validateHydrationTree(root, component)
    if (!root || this.diagnostics.length > 0) return undefined
    return { apiVersion: RENDER_PLAN_API_VERSION, routeId, root }
  }

  /** Compile a layout component around an already compiled route subtree. */
  compileAround(
    routeId: string,
    child: PlanNode,
    componentName?: string,
  ): RenderPlan | undefined {
    this.context.routeId = routeId
    if (routeId.trim() === '') {
      this.report(
        this.sourceFile,
        'GB1000',
        'routeId must be a non-empty stable route identifier.',
      )
      return undefined
    }
    const component = componentName
      ? this.components.get(componentName)
      : this.findDefaultComponent()
    if (!component) {
      this.report(
        this.sourceFile,
        'GB1001',
        componentName
          ? `Cannot find layout component ${JSON.stringify(componentName)} in this compile unit.`
          : 'Cannot find a default-exported layout function component.',
      )
      return undefined
    }
    const name = componentName ?? this.componentDisplayName(component)
    const compile = () =>
      this.compileComponent(
        name,
        component,
        new Map(),
        true,
        new Map([['children', child]]),
      )
    const root = this.isClientModule
      ? this.compileAtClientBoundary(
          name,
          component,
          this,
          'component',
          compile,
        )
      : compile()
    if (root) this.validateHydrationTree(root, component)
    if (!root || this.diagnostics.length > 0) return undefined
    return { apiVersion: RENDER_PLAN_API_VERSION, routeId, root }
  }

  private collectModuleContextLocals(): void {
    for (const statement of this.sourceFile.statements) {
      if (!ts.isVariableStatement(statement)) continue
      for (const declaration of statement.declarationList.declarations) {
        if (
          !ts.isIdentifier(declaration.name) ||
          !declaration.initializer ||
          !ts.isCallExpression(declaration.initializer)
        ) {
          continue
        }
        if (this.isProtectedHookCall(declaration.initializer, 'createContext')) {
          this.contextLocals.add(declaration.name.text)
        }
      }
    }
  }

  /**
   * Bake same-module `const` initializers into portable plan expressions so
   * components can reference them without inlining literals. `let`/`var` and
   * dynamic initializers stay rejected when used in portable expressions.
   */
  private collectModuleConstants(): void {
    const environment: ExpressionEnvironment = new Map()
    for (const statement of this.sourceFile.statements) {
      if (!ts.isVariableStatement(statement)) continue
      const flags = statement.declarationList.flags
      const isConst = (flags & ts.NodeFlags.Const) !== 0
      const bindingKind: 'const' | 'let' | 'var' = isConst
        ? 'const'
        : (flags & ts.NodeFlags.Let) !== 0
          ? 'let'
          : 'var'
      for (const declaration of statement.declarationList.declarations) {
        if (!ts.isIdentifier(declaration.name) || !declaration.initializer) {
          continue
        }
        const name = declaration.name.text
        if (this.components.has(name) || this.contextLocals.has(name)) {
          continue
        }
        const initializer = this.unwrapExpression(declaration.initializer)
        if (
          ts.isJsxElement(initializer) ||
          ts.isJsxSelfClosingElement(initializer) ||
          ts.isJsxFragment(initializer) ||
          this.componentFromExpression(declaration.initializer)
        ) {
          continue
        }
        if (!isConst) {
          this.nonPortableModuleBindings.set(name, bindingKind)
          continue
        }
        const diagnosticStart = this.diagnostics.length
        const value = this.compileExpression(declaration.initializer, environment)
        if (value) {
          this.moduleConstants.set(name, value)
          environment.set(name, value)
          continue
        }
        // Unused non-portable module consts must not fail the module; only
        // referencing them in portable expressions is a hard error (GB1068).
        this.diagnostics.splice(diagnosticStart)
        this.nonPortableModuleBindings.set(name, 'const')
      }
    }
  }

  private collectComponents(): void {
    for (const statement of this.sourceFile.statements) {
      if (ts.isFunctionDeclaration(statement) && statement.name) {
        this.components.set(statement.name.text, statement)
        if (this.hasModifier(statement, ts.SyntaxKind.ExportKeyword)) {
          this.exportedComponents.add(statement.name.text)
          this.knownExports.add(
            this.hasModifier(statement, ts.SyntaxKind.DefaultKeyword)
              ? 'default'
              : statement.name.text,
          )
        }
        continue
      }
      if (ts.isClassDeclaration(statement)) {
        if (this.hasModifier(statement, ts.SyntaxKind.ExportKeyword)) {
          this.knownExports.add(
            this.hasModifier(statement, ts.SyntaxKind.DefaultKeyword)
              ? 'default'
              : statement.name?.text ?? 'default',
          )
        }
        continue
      }
      if (ts.isExportAssignment(statement) && !statement.isExportEquals) {
        this.knownExports.add('default')
        continue
      }
      if (!ts.isVariableStatement(statement)) continue
      const exported = this.hasModifier(statement, ts.SyntaxKind.ExportKeyword)
      for (const declaration of statement.declarationList.declarations) {
        if (ts.isIdentifier(declaration.name) && declaration.initializer) {
          const component = this.componentFromExpression(declaration.initializer)
          if (!component) continue
          this.components.set(declaration.name.text, component)
          if (exported) {
            this.exportedComponents.add(declaration.name.text)
            this.knownExports.add(declaration.name.text)
          }
        } else if (exported && ts.isIdentifier(declaration.name)) {
          this.knownExports.add(declaration.name.text)
        }
      }
    }
    for (const statement of this.sourceFile.statements) {
      if (
        !ts.isExportDeclaration(statement) ||
        statement.moduleSpecifier ||
        !statement.exportClause ||
        !ts.isNamedExports(statement.exportClause)
      ) continue
      for (const element of statement.exportClause.elements) {
        const localName = element.propertyName?.text ?? element.name.text
        if (this.components.has(localName)) {
          this.exportedComponents.add(element.name.text)
          this.knownExports.add(element.name.text)
          if (localName !== element.name.text) {
            const component = this.components.get(localName)
            if (component) this.components.set(element.name.text, component)
          }
        }
      }
    }
  }

  private collectDefaultProps(): void {
    for (const statement of this.sourceFile.statements) {
      if (
        !ts.isExpressionStatement(statement) ||
        !ts.isBinaryExpression(statement.expression) ||
        statement.expression.operatorToken.kind !== ts.SyntaxKind.EqualsToken
      ) {
        continue
      }
      const left = statement.expression.left
      if (
        !ts.isPropertyAccessExpression(left) ||
        left.name.text !== 'defaultProps' ||
        !ts.isIdentifier(left.expression)
      ) {
        continue
      }
      const component = this.components.get(left.expression.text)
      if (!component) continue
      const right = this.unwrapExpression(statement.expression.right)
      if (!ts.isObjectLiteralExpression(right)) {
        this.report(
          statement.expression.right,
          'GB1018',
          'defaultProps must be an object literal of portable values.',
        )
        continue
      }
      const defaults = new Map<string, PlanExpression>()
      let ok = true
      for (const property of right.properties) {
        if (!ts.isPropertyAssignment(property)) {
          this.report(
            property,
            'GB1018',
            'defaultProps only supports portable data properties.',
          )
          ok = false
          break
        }
        const name = this.propertyNameText(property.name)
        const value = this.compileExpression(property.initializer, new Map())
        if (name === undefined || !value) {
          ok = false
          break
        }
        defaults.set(name, value)
      }
      if (ok) this.defaultProps.set(component, defaults)
    }
  }

  private findDefaultComponent(): Component | undefined {
    for (const statement of this.sourceFile.statements) {
      if (
        ts.isFunctionDeclaration(statement) &&
        this.hasModifier(statement, ts.SyntaxKind.DefaultKeyword)
      ) {
        return statement
      }
      if (
        ts.isExportAssignment(statement) &&
        !statement.isExportEquals
      ) {
        if (ts.isIdentifier(statement.expression)) {
          return this.components.get(statement.expression.text)
        }
        return this.componentFromExpression(statement.expression)
      }
    }
    return this.components.get('default')
  }

  private componentFromExpression(expression: ts.Expression): Component | undefined {
    expression = this.unwrapExpression(expression)
    if (ts.isIdentifier(expression)) return this.components.get(expression.text)
    if (ts.isArrowFunction(expression) || ts.isFunctionExpression(expression)) {
      return expression
    }
    if (!ts.isCallExpression(expression)) return undefined
    const callName = expression.expression.getText(this.sourceFile)
    if (callName === 'Object.assign') {
      const target = expression.arguments[0]
      return target ? this.componentFromExpression(target) : undefined
    }
    if (!['forwardRef', 'React.forwardRef', 'memo', 'React.memo'].includes(callName)) {
      return undefined
    }
    const target = expression.arguments[0]
    if (!target) return undefined
    const component = this.componentFromExpression(target)
    if (component && callName.endsWith('forwardRef')) {
      this.forwardedRefComponents.add(component)
    }
    return component
  }

  private hasModifier(node: ts.Node, kind: ts.SyntaxKind): boolean {
    return !!ts
      .getModifiers(node as ts.HasModifiers)
      ?.some((modifier) => modifier.kind === kind)
  }

  private componentDisplayName(component: Component): string {
    if (component.name && ts.isIdentifier(component.name))
      return component.name.text
    for (const [name, candidate] of this.components) {
      if (candidate === component) return name
    }
    return 'default'
  }

  private compileComponent(
    name: string,
    component: Component,
    suppliedProps: ExpressionEnvironment,
    rootComponent = false,
    suppliedNodes: NodeEnvironment = new Map(),
  ): PlanNode | undefined {
    const stackKey = `${this.fileName}#${name}`
    const existing = this.context.componentStack.findIndex(
      (entry) => entry.key === stackKey,
    )
    if (existing !== -1) {
      const cycle = [
        ...this.context.componentStack
          .slice(existing)
          .map((entry) => entry.display),
        `${this.fileName}:${name}`,
      ]
      this.report(
        component,
        'GB1010',
        `Recursive component rendering is not portable (${cycle.join(' -> ')}).`,
        'Move recursive data traversal into a keyed array map or precompute the tree in Go.',
      )
      return undefined
    }
    this.context.componentStack.push({
      key: stackKey,
      display: `${this.fileName}:${name}`,
    })
    this.rejectUseIdFrames.push(this.eachKeyStack.length > 0)
    this.styleObjectScopes.push(new Map())
    this.jsxElementScopes.push(new Map())
    const nodeEnvironment = new Map<string, PlanNode>()
    const componentParameter = component.parameters[0]
    if (componentParameter) {
      this.bindComponentNodeParameter(
        componentParameter.name,
        suppliedNodes,
        nodeEnvironment,
      )
    }
    this.nodeScopes.push(nodeEnvironment)
    try {
      const environment = new Map<string, PlanExpression>(this.moduleConstants)
      const parameter = componentParameter
      const effectiveProps = new Map(suppliedProps)
      const defaults = this.defaultProps.get(component)
      if (defaults) {
        for (const [propName, value] of defaults) {
          if (!effectiveProps.has(propName)) effectiveProps.set(propName, value)
        }
      }
      if (parameter) {
        this.bindComponentParameter(
          parameter.name,
          effectiveProps,
          environment,
          rootComponent,
        )
      }
      if (
        component.parameters.length > 1 &&
        !this.forwardedRefComponents.has(component)
      ) {
        this.report(
          component.parameters[1]!,
          'GB1011',
          'Portable function components accept a single props parameter.',
        )
      }
      if (this.forwardedRefComponents.has(component)) {
        const ref = component.parameters[1]
        if (ref && ts.isIdentifier(ref.name)) {
          environment.set(ref.name.text, { kind: 'literal', value: null })
        }
      }

      const componentBody = component.body
      if (!componentBody) {
        this.report(
          component,
          'GB1012',
          'A portable component must have an implementation body.',
        )
        return undefined
      }
      if (!ts.isBlock(componentBody)) {
        return this.compileNodeExpression(componentBody, environment)
      }

      let returnExpression: ts.Expression | undefined
      for (const statement of componentBody.statements) {
        if (ts.isReturnStatement(statement)) {
          if (!statement.expression) {
            this.report(
              statement,
              'GB1012',
              'A portable component must return markup.',
            )
          } else {
            returnExpression = statement.expression
          }
          continue
        }
        if (ts.isVariableStatement(statement)) {
          this.compileVariableStatement(statement, environment)
          continue
        }
        if (
          ts.isExpressionStatement(statement) &&
          ts.isCallExpression(statement.expression) &&
          ts.isIdentifier(statement.expression.expression) &&
          statement.expression.expression.text === 'useEffect'
        ) {
          // Effects run after hydration and cannot contribute server markup.
          continue
        }
        this.report(
          statement,
          'GB1013',
          `Unsupported statement in portable component: ${ts.SyntaxKind[statement.kind]}.`,
          'Precompute render data in Go, use a portable const expression, or isolate browser behavior in an event/effect/ClientOnly boundary.',
        )
      }
      if (!returnExpression) {
        this.report(
          component,
          'GB1014',
          'Portable component has no markup return.',
        )
        return undefined
      }
      return this.compileNodeExpression(returnExpression, environment)
    } finally {
      this.nodeScopes.pop()
      this.styleObjectScopes.pop()
      this.jsxElementScopes.pop()
      this.context.componentStack.pop()
      this.rejectUseIdFrames.pop()
    }
  }

  private bindComponentParameter(
    binding: ts.BindingName,
    suppliedProps: ExpressionEnvironment,
    environment: ExpressionEnvironment,
    rootComponent: boolean,
  ): void {
    if (ts.isIdentifier(binding)) {
      if (rootComponent) {
        environment.set(binding.text, { kind: 'path', path: [] })
      } else {
        for (const [propName, expression] of suppliedProps) {
          environment.set(`${binding.text}.${propName}`, expression)
        }
      }
      return
    }
    if (ts.isArrayBindingPattern(binding)) {
      this.report(
        binding,
        'GB1015',
        'Array-destructured component props are not portable.',
        'Use an object props contract.',
      )
      return
    }
    for (const element of binding.elements) {
      if (element.dotDotDotToken) {
        if (!ts.isIdentifier(element.name) || rootComponent) {
          this.report(
            element,
            'GB1016',
            'Rest props require a nested component with statically supplied props.',
          )
          continue
        }
        const consumed = new Set(
          binding.elements
            .filter((candidate) => !candidate.dotDotDotToken)
            .map((candidate) =>
              candidate.propertyName
                ? this.propertyNameText(candidate.propertyName)
                : ts.isIdentifier(candidate.name)
                  ? candidate.name.text
                  : undefined,
            )
            .filter((name): name is string => name !== undefined),
        )
        for (const [propName, value] of suppliedProps) {
          if (!consumed.has(propName)) {
            environment.set(`${element.name.text}.${propName}`, value)
          }
        }
        continue
      }
      if (!ts.isIdentifier(element.name)) {
        this.report(
          element.name,
          'GB1017',
          'Nested props destructuring is not yet portable.',
        )
        continue
      }
      const propertyName = element.propertyName
        ? this.propertyNameText(element.propertyName)
        : element.name.text
      if (propertyName === undefined) continue
      const supplied = suppliedProps.get(propertyName)
      if (supplied) {
        environment.set(element.name.text, supplied)
        continue
      }
      if (element.initializer) {
        // Nested/default props: compile portable default expressions (literals,
        // templates, ?? / ternaries, etc.) into the plan instead of rejecting.
        const defaultValue = this.compileExpression(
          element.initializer,
          environment,
        )
        if (defaultValue) {
          environment.set(element.name.text, defaultValue)
          continue
        }
        this.report(
          element.initializer,
          'GB1018',
          'Default value for this nested component prop is not a portable expression.',
          'Use a literal/prop-derived default, pass the value explicitly from the parent, or calculate it in Go.',
        )
        continue
      }
      environment.set(
        element.name.text,
        rootComponent
          ? { kind: 'path', path: [propertyName] }
          : { kind: 'literal', value: null },
      )
    }
  }

  private bindComponentNodeParameter(
    binding: ts.BindingName,
    suppliedNodes: NodeEnvironment,
    environment: NodeEnvironment,
  ): void {
    if (ts.isIdentifier(binding)) {
      for (const [propName, node] of suppliedNodes) {
        environment.set(`${binding.text}.${propName}`, node)
      }
      return
    }
    if (!ts.isObjectBindingPattern(binding)) return
    for (const element of binding.elements) {
      if (!ts.isIdentifier(element.name)) continue
      const propertyName = element.propertyName
        ? this.propertyNameText(element.propertyName)
        : element.name.text
      if (propertyName === undefined) continue
      const node = suppliedNodes.get(propertyName)
      if (node) environment.set(element.name.text, node)
    }
  }

  private compileVariableStatement(
    statement: ts.VariableStatement,
    environment: ExpressionEnvironment,
  ): void {
    for (const declaration of statement.declarationList.declarations) {
      if (!declaration.initializer) {
        this.report(
          declaration,
          'GB1020',
          'Portable local bindings require an initializer.',
        )
        continue
      }
      if (
        ts.isArrayBindingPattern(declaration.name) &&
        ts.isCallExpression(declaration.initializer)
      ) {
        if (this.isProtectedHookCall(declaration.initializer, 'useState')) {
          if (!this.assertUnconditionalHook(declaration.initializer, 'useState')) {
            continue
          }
          this.compileUseState(declaration, environment)
          continue
        }
        if (this.isProtectedHookCall(declaration.initializer, 'useReducer')) {
          if (!this.assertUnconditionalHook(declaration.initializer, 'useReducer')) {
            continue
          }
          this.compileUseReducer(declaration, environment)
          continue
        }
      }
      if (
        ts.isCallExpression(declaration.initializer) &&
        this.isProtectedHookCall(declaration.initializer, 'createContext') &&
        ts.isIdentifier(declaration.name)
      ) {
        this.contextLocals.add(declaration.name.text)
        environment.set(declaration.name.text, {
          kind: 'literal',
          value: null,
        })
        continue
      }
      if (
        ts.isCallExpression(declaration.initializer) &&
        this.isProtectedHookCall(declaration.initializer, 'useCallback') &&
        ts.isIdentifier(declaration.name)
      ) {
        if (!this.assertUnconditionalHook(declaration.initializer, 'useCallback')) {
          continue
        }
        const baked = this.compileProtectedUseCallback(
          declaration.initializer,
          environment,
        )
        // Event-handler callbacks are stripped from markup; bind a placeholder
        // so an unused portable factory still succeeds.
        environment.set(
          declaration.name.text,
          baked ?? { kind: 'literal', value: null },
        )
        continue
      }
      if (!ts.isIdentifier(declaration.name)) {
        this.report(
          declaration.name,
          'GB1021',
          'Only identifier local bindings and useState/useReducer tuples are portable.',
        )
        continue
      }
      const initializer = this.unwrapExpression(declaration.initializer)
      if (ts.isObjectLiteralExpression(initializer)) {
        this.styleObjectScopes.at(-1)?.set(declaration.name.text, initializer)
        const diagnosticStart = this.diagnostics.length
        const value = this.compileExpression(declaration.initializer, environment)
        if (value) environment.set(declaration.name.text, value)
        else this.diagnostics.splice(diagnosticStart)
        continue
      }
      if (
        ts.isJsxElement(initializer) ||
        ts.isJsxSelfClosingElement(initializer) ||
        ts.isJsxFragment(initializer)
      ) {
        this.jsxElementScopes.at(-1)?.set(declaration.name.text, initializer)
        continue
      }
      const value = this.compileExpression(declaration.initializer, environment)
      if (value) environment.set(declaration.name.text, value)
    }
  }

  private compileUseState(
    declaration: ts.VariableDeclaration,
    environment: ExpressionEnvironment,
  ): void {
    const pattern = declaration.name as ts.ArrayBindingPattern
    const call = declaration.initializer as ts.CallExpression
    const state = pattern.elements[0]
    if (
      !state ||
      ts.isOmittedExpression(state) ||
      !ts.isIdentifier(state.name)
    ) {
      this.report(
        pattern,
        'GB1022',
        'useState must bind an identifier as its first tuple entry.',
      )
      return
    }
    const initializer = call.arguments[0]
    if (!initializer) {
      environment.set(state.name.text, { kind: 'literal', value: null })
      return
    }
    if (
      ts.isArrowFunction(initializer) ||
      ts.isFunctionExpression(initializer)
    ) {
      const body = this.portableFunctionBody(initializer)
      if (!body) {
        this.report(
          initializer,
          'GB1023',
          'Lazy useState initializers must be a portable expression body.',
          'Use a literal/prop-derived initial value, a portable () => expr, or calculate it in Go.',
        )
        return
      }
      const value = this.compileExpression(body, environment)
      if (value) environment.set(state.name.text, value)
      return
    }
    const value = this.compileExpression(initializer, environment)
    if (value) environment.set(state.name.text, value)
  }

  private compileUseReducer(
    declaration: ts.VariableDeclaration,
    environment: ExpressionEnvironment,
  ): void {
    const pattern = declaration.name as ts.ArrayBindingPattern
    const call = declaration.initializer as ts.CallExpression
    const state = pattern.elements[0]
    if (
      !state ||
      ts.isOmittedExpression(state) ||
      !ts.isIdentifier(state.name)
    ) {
      this.report(
        pattern,
        'GB1022',
        'useReducer must bind an identifier as its first tuple entry.',
      )
      return
    }
    // useReducer(reducer, initialState) or useReducer(reducer, initArg, init)
    if (call.arguments.length >= 3) {
      const init = call.arguments[2]
      if (
        init &&
        (ts.isArrowFunction(init) || ts.isFunctionExpression(init))
      ) {
        const body = this.portableFunctionBody(init)
        if (!body) {
          this.report(
            init,
            'GB1076',
            'useReducer lazy init must be a portable expression body.',
          )
          return
        }
        const initScope = new Map(environment)
        const parameter = init.parameters[0]
        if (parameter) {
          if (!ts.isIdentifier(parameter.name)) {
            this.report(
              parameter,
              'GB1076',
              'useReducer init must take a single identifier parameter.',
            )
            return
          }
          const initArgument = call.arguments[1]
          const compiledArgument = initArgument
            ? this.compileExpression(initArgument, environment)
            : { kind: 'literal' as const, value: null }
          if (!compiledArgument) return
          initScope.set(parameter.name.text, compiledArgument)
        }
        const value = this.compileExpression(body, initScope)
        if (value) environment.set(state.name.text, value)
        return
      }
    }
    const initial = call.arguments[1]
    if (!initial) {
      environment.set(state.name.text, { kind: 'literal', value: null })
      return
    }
    if (ts.isArrowFunction(initial) || ts.isFunctionExpression(initial)) {
      this.report(
        initial,
        'GB1076',
        'useReducer initial state cannot be a bare function; pass initArg and init, or a portable value.',
      )
      return
    }
    const value = this.compileExpression(initial, environment)
    if (value) environment.set(state.name.text, value)
  }

  private portableFunctionBody(
    fn: ts.ArrowFunction | ts.FunctionExpression,
  ): ts.Expression | undefined {
    if (!ts.isBlock(fn.body)) return fn.body
    const returns = fn.body.statements.filter(ts.isReturnStatement)
    if (fn.body.statements.length !== 1 || !returns[0]?.expression) {
      return undefined
    }
    return returns[0].expression
  }

  private compileNodeExpression(
    expression: ts.Expression,
    environment: ExpressionEnvironment,
  ): PlanNode | undefined {
    expression = this.unwrapExpression(expression)
    if (
      ts.isIdentifier(expression) ||
      ts.isPropertyAccessExpression(expression)
    ) {
      const node = this.nodeScopes
        .at(-1)
        ?.get(expression.getText(this.sourceFile))
      if (node) return node
    }
    if (ts.isJsxElement(expression))
      return this.compileJsxElement(expression, environment)
    if (ts.isJsxSelfClosingElement(expression)) {
      return this.compileJsxSelfClosing(expression, environment)
    }
    if (ts.isJsxFragment(expression)) {
      return {
        kind: 'fragment',
        children: this.compileJsxChildren(expression.children, environment),
      }
    }
    if (ts.isCallExpression(expression)) {
      const childrenHelper = this.tryCompileChildrenHelper(expression, environment)
      if (childrenHelper.handled) return childrenHelper.node
      const element = this.compileReactElementCall(expression, environment)
      if (element.handled) return element.node
    }
    if (ts.isArrayLiteralExpression(expression)) {
      return {
        kind: 'fragment',
        children: expression.elements.flatMap((element) => {
          if (ts.isSpreadElement(element)) {
            this.report(element, 'GB1080', 'Spread children are not portable.')
            return []
          }
          const node = this.compileNodeExpression(element, environment)
          return node ? [node] : []
        }),
      }
    }
    if (ts.isConditionalExpression(expression)) {
      const test = this.compileExpression(expression.condition, environment)
      const consequent = this.compileNodeExpression(
        expression.whenTrue,
        environment,
      )
      const alternate = this.compileNodeExpression(
        expression.whenFalse,
        environment,
      )
      if (!test || !consequent) return undefined
      return alternate
        ? { kind: 'conditional', test, consequent, alternate }
        : { kind: 'conditional', test, consequent }
    }
    if (
      ts.isBinaryExpression(expression) &&
      expression.operatorToken.kind === ts.SyntaxKind.AmpersandAmpersandToken
    ) {
      const test = this.compileExpression(expression.left, environment)
      const consequent = this.compileNodeExpression(
        expression.right,
        environment,
      )
      if (!test || !consequent) return undefined
      return { kind: 'conditional', test, consequent }
    }
    const each = this.tryCompileEach(expression, environment)
    if (each) return each
    if (this.isReactEmptyExpression(expression)) return undefined
    const value = this.compileExpression(expression, environment)
    return value ? { kind: 'text', value } : undefined
  }

  private compileJsxElement(
    element: ts.JsxElement,
    environment: ExpressionEnvironment,
  ): PlanNode | undefined {
    const tagName = element.openingElement.tagName.getText(this.sourceFile)
    if (tagName === 'ClientOnly') {
      return this.compileClientOnly(
        element.openingElement.attributes,
        element.children,
        environment,
      )
    }
    if (tagName === 'SafeHTML') {
      return this.compileSafeHTML(
        element.openingElement.attributes,
        environment,
      )
    }
    if (this.isSuspenseTag(tagName)) {
      return this.compileSuspensePassthrough(
        element.openingElement.attributes,
        element.children,
        environment,
      )
    }
    if (this.isFragmentTag(tagName)) {
      return this.compileFragmentElement(
        element.openingElement.attributes,
        element.children,
        environment,
      )
    }
    if (this.isContextProviderTag(tagName)) {
      return this.compileContextProvider(
        tagName,
        element.openingElement.attributes,
        element.children,
        environment,
      )
    }
    if (this.isIntrinsicTag(tagName)) {
      return this.compileIntrinsic(
        tagName,
        element.openingElement.attributes,
        element.children,
        environment,
      )
    }
    if (this.isLazyComponent(tagName)) {
      this.report(
        element.openingElement.tagName,
        'GB1098',
        `React.lazy component ${tagName} cannot participate in Go-rendered markup.`,
        'Wrap it in <ClientOnly> with a portable fallback, or place it under a use client boundary.',
      )
      return undefined
    }
    return this.compileLocalComponent(
      tagName,
      element.openingElement.attributes,
      element.children,
      environment,
      element,
    )
  }

  private compileJsxSelfClosing(
    element: ts.JsxSelfClosingElement,
    environment: ExpressionEnvironment,
  ): PlanNode | undefined {
    const tagName = element.tagName.getText(this.sourceFile)
    if (tagName === 'ClientOnly') {
      return this.compileClientOnly(element.attributes, [], environment)
    }
    if (tagName === 'SafeHTML')
      return this.compileSafeHTML(element.attributes, environment)
    if (this.isSuspenseTag(tagName)) {
      return this.compileSuspensePassthrough(element.attributes, [], environment)
    }
    if (this.isFragmentTag(tagName)) {
      return this.compileFragmentElement(element.attributes, [], environment)
    }
    if (this.isIntrinsicTag(tagName)) {
      return this.compileIntrinsic(tagName, element.attributes, [], environment)
    }
    if (this.isLazyComponent(tagName)) {
      this.report(
        element.tagName,
        'GB1098',
        `React.lazy component ${tagName} cannot participate in Go-rendered markup.`,
        'Wrap it in <ClientOnly> with a portable fallback, or place it under a use client boundary.',
      )
      return undefined
    }
    return this.compileLocalComponent(
      tagName,
      element.attributes,
      [],
      environment,
      element,
    )
  }

  private compileIntrinsic(
    tag: string,
    attributes: ts.JsxAttributes,
    children: readonly ts.JsxChild[],
    environment: ExpressionEnvironment,
  ): PlanNode {
    const compiledAttributes: Attribute[] = []
    for (const attribute of attributes.properties) {
      if (ts.isJsxSpreadAttribute(attribute)) {
        const entries = this.compileObjectProperties(
          attribute.expression,
          environment,
        )
        if (!entries) continue
        for (const entry of entries) {
          if (!entry.value) continue
          if (entry.name === 'key' || /^on[A-Z]/.test(entry.name)) continue
          if (entry.name === 'dangerouslySetInnerHTML') {
            this.report(
              attribute,
              'GB1031',
              'dangerouslySetInnerHTML is not portable.',
              'Use <SafeHTML value={schemaValidatedHTML} /> with the configured sanitizer.',
            )
            continue
          }
          this.mergeAttribute(compiledAttributes, {
            name: entry.name,
            value: entry.value,
            mode: booleanAttributes.has(entry.name)
              ? 'boolean'
              : urlAttributes.has(entry.name)
                ? 'url'
                : entry.name === 'style'
                  ? 'style'
                  : 'string',
          })
        }
        continue
      }
      const name = attribute.name.getText(this.sourceFile)
      if (name === 'key' || /^on[A-Z]/.test(name)) continue
      if (name === 'dangerouslySetInnerHTML') {
        this.report(
          attribute,
          'GB1031',
          'dangerouslySetInnerHTML is not portable.',
          'Use <SafeHTML value={schemaValidatedHTML} /> with the configured sanitizer.',
        )
        continue
      }
      const value = this.compileJsxAttributeValue(attribute, environment)
      if (!value) continue
      this.mergeAttribute(compiledAttributes, {
        name,
        value,
        mode: booleanAttributes.has(name)
          ? 'boolean'
          : urlAttributes.has(name)
            ? 'url'
            : name === 'style'
              ? 'style'
              : 'string',
      })
    }
    const inheritedNamespace =
      this.namespaceScopes[this.namespaceScopes.length - 1] ?? 'html'
    const namespace = tag === 'svg' ? 'svg' : inheritedNamespace
    const childNamespace =
      tag.toLowerCase() === 'foreignobject' ? 'html' : namespace
    if (namespace === 'html') this.validateRawTextChildren(tag, children)
    this.namespaceScopes.push(childNamespace)
    let compiledChildren: PlanNode[]
    try {
      compiledChildren = this.compileJsxChildren(children, environment)
    } finally {
      this.namespaceScopes.pop()
    }
    const node: PlanNode = {
      kind: 'element',
      tag,
      namespace,
      attributes: compiledAttributes,
      children: compiledChildren,
    }
    return node
  }

  private validateRawTextChildren(
    tag: string,
    children: readonly ts.JsxChild[],
  ): void {
    const lowerTag = tag.toLowerCase()
    if (!parserControlledTextElements.has(lowerTag)) return
    for (const child of children) {
      if (ts.isJsxExpression(child) && !child.expression) continue
      if (ts.isJsxText(child) && child.text === '') continue
      const suggestion =
        lowerTag === 'script'
          ? 'Use the typed metadata JSON-LD API for structured data, or create browser-only scripts in an effect.'
          : lowerTag === 'style'
            ? 'Put static CSS in a stylesheet, or apply browser-only style changes in an effect.'
            : lowerTag === 'textarea'
              ? 'Use a value or defaultValue attribute so GoBeyond can apply React-compatible textarea normalization.'
              : lowerTag === 'title'
                ? 'Set the document title through the typed metadata API.'
                : 'Move parser-controlled fallback content behind ClientOnly or render it as ordinary semantic HTML outside this element.'
      this.report(
        child,
        'GB1037',
        `Children inside HTML <${lowerTag}> are not portable initial markup.`,
        suggestion,
      )
    }
  }

  private compileJsxAttributeValue(
    attribute: ts.JsxAttribute,
    environment: ExpressionEnvironment,
  ): PlanExpression | undefined {
    if (!attribute.initializer) return { kind: 'literal', value: true }
    if (ts.isStringLiteral(attribute.initializer)) {
      // JSX attribute string literals decode HTML entities the same way JSX text does.
      return { kind: 'literal', value: decodeJsxEntities(attribute.initializer.text) }
    }
    if (
      !ts.isJsxExpression(attribute.initializer) ||
      !attribute.initializer.expression
    ) {
      this.report(attribute, 'GB1032', 'Unsupported JSX attribute initializer.')
      return undefined
    }
    if (attribute.name.getText(this.sourceFile) === 'style') {
      return this.compileStyleValue(
        attribute.initializer.expression,
        environment,
        attribute,
      )
    }
    return this.compileExpression(attribute.initializer.expression, environment)
  }

  private compileJsxChildren(
    children: readonly ts.JsxChild[],
    environment: ExpressionEnvironment,
  ): PlanNode[] {
    const result: PlanNode[] = []
    for (const child of children) {
      if (ts.isJsxText(child)) {
        const text = this.cleanJsxText(child.text)
        if (text !== '')
          result.push({
            kind: 'text',
            value: { kind: 'literal', value: text },
          })
        continue
      }
      if (ts.isJsxExpression(child)) {
        if (!child.expression) continue
        const node = this.compileNodeExpression(child.expression, environment)
        if (node) result.push(node)
        continue
      }
      const node = this.compileNodeExpression(child, environment)
      if (node) result.push(node)
    }
    return result
  }

  private cleanJsxText(value: string): string {
    const lines = value.replace(/\r\n?/g, '\n').split('\n')
    let lastNonEmptyLine = 0
    for (let index = 0; index < lines.length; index += 1) {
      if (/[^\t ]/.test(lines[index]!)) lastNonEmptyLine = index
    }
    let result = ''
    for (let index = 0; index < lines.length; index += 1) {
      let line = lines[index]!
      const isFirst = index === 0
      const isLast = index === lines.length - 1
      const isLastNonEmpty = index === lastNonEmptyLine
      line = line.replace(/\t/g, ' ')
      if (!isFirst) line = line.replace(/^ +/, '')
      if (!isLast) line = line.replace(/ +$/, '')
      if (line !== '') {
        if (!isLastNonEmpty) line += ' '
        result += line
      }
    }
    // Match JSX/browser semantics (Babel, esbuild, React): entities in JSX text
    // are decoded. Leaving `&hellip;` literal makes Go SSR disagree with the
    // Vite client bundle, which decodes to `…`.
    return decodeJsxEntities(result)
  }

  private compileClientOnly(
    attributes: ts.JsxAttributes,
    children: readonly ts.JsxChild[],
    environment: ExpressionEnvironment,
  ): PlanNode | undefined {
    const fallback = this.findAttribute(attributes, 'fallback')
    if (!fallback) {
      void children
      return { kind: 'clientOnly' }
    }
    if (!fallback.initializer || !ts.isJsxExpression(fallback.initializer)) {
      this.report(fallback, 'GB1040', 'ClientOnly fallback must be JSX.')
      return undefined
    }
    if (!fallback.initializer.expression) {
      void children
      return { kind: 'clientOnly' }
    }
    if (this.isReactEmptyExpression(fallback.initializer.expression)) {
      void children
      return { kind: 'clientOnly', fallback: null }
    }
    const fallbackNode = this.compileNodeExpression(
      fallback.initializer.expression,
      environment,
    )
    if (!fallbackNode) return undefined
    // Children are deliberately opaque browser code. Their syntax is parsed by TS,
    // but the portable compiler does not inspect or execute it.
    void children
    return { kind: 'clientOnly', fallback: fallbackNode }
  }

  private compileSafeHTML(
    attributes: ts.JsxAttributes,
    environment: ExpressionEnvironment,
  ): PlanNode | undefined {
    const asAttribute = this.findAttribute(attributes, 'as')
    if (
      !asAttribute?.initializer ||
      !ts.isStringLiteral(asAttribute.initializer) ||
      !['div', 'span'].includes(asAttribute.initializer.text)
    ) {
      this.report(
        asAttribute ?? attributes,
        'GB1043',
        'SafeHTML requires as="div" or as="span" so React and Go own the same hydration element.',
      )
      return undefined
    }
    for (const attribute of attributes.properties) {
      if (
        ts.isJsxSpreadAttribute(attribute) ||
        !['as', 'value'].includes(attribute.name.getText(this.sourceFile))
      ) {
        this.report(
          attribute,
          'GB1044',
          'SafeHTML only accepts as and value in the MVP.',
        )
      }
    }
    const valueAttribute = this.findAttribute(attributes, 'value')
    if (
      !valueAttribute?.initializer ||
      !ts.isJsxExpression(valueAttribute.initializer) ||
      !valueAttribute.initializer.expression
    ) {
      this.report(
        valueAttribute ?? attributes,
        'GB1042',
        'SafeHTML requires a schema-validated value expression.',
      )
      return undefined
    }
    const value = this.compileExpression(
      valueAttribute.initializer.expression,
      environment,
    )
    return value
      ? {
          kind: 'element',
          tag: asAttribute.initializer.text,
          namespace:
            this.namespaceScopes[this.namespaceScopes.length - 1] ?? 'html',
          attributes: [],
          children: [{ kind: 'rawHtml', value }],
        }
      : undefined
  }

  private compileLocalComponent(
    name: string,
    attributes: ts.JsxAttributes,
    children: readonly ts.JsxChild[],
    environment: ExpressionEnvironment,
    sourceNode: ts.Node,
  ): PlanNode | undefined {
    if (name.includes('.')) {
      this.report(
        attributes.parent,
        'GB1050',
        `Namespaced or member component ${name} is not portable.`,
        'Use a project-local function component in this compile unit or wrap the package component in ClientOnly.',
      )
      return undefined
    }
    const supplied = new Map<string, PlanExpression>()
    for (const attribute of attributes.properties) {
      if (ts.isJsxSpreadAttribute(attribute)) {
        const entries = this.compileObjectProperties(
          attribute.expression,
          environment,
        )
        if (!entries) continue
        for (const entry of entries) {
          if (entry.name === 'key' || !entry.value) continue
          supplied.set(entry.name, entry.value)
        }
        continue
      }
      const propName = attribute.name.getText(this.sourceFile)
      if (propName === 'key') continue
      const value = this.compileJsxAttributeValue(attribute, environment)
      if (value) supplied.set(propName, value)
    }
    const compiledChildren = this.compileJsxChildren(children, environment)
    const suppliedNodes: NodeEnvironment = new Map([
      [
        'children',
        compiledChildren.length === 1
          ? compiledChildren[0]!
          : { kind: 'fragment', children: compiledChildren },
      ],
    ])

    return this.compileComponentReference(
      name,
      supplied,
      suppliedNodes,
      sourceNode,
    )
  }

  private compileComponentReference(
    name: string,
    supplied: ExpressionEnvironment,
    suppliedNodes: NodeEnvironment,
    sourceNode: ts.Node,
  ): PlanNode | undefined {
    const localComponent = this.components.get(name)
    if (localComponent) {
      return this.compileComponent(
        name,
        localComponent,
        supplied,
        false,
        suppliedNodes,
      )
    }

    const imported = this.imports.get(name)
    if (!imported) {
      this.report(
        sourceNode,
        'GB1051',
        `Component ${name} is not a project-local function component.`,
        'Declare or import it from a configured project source root, or wrap browser-only markup in ClientOnly.',
      )
      return undefined
    }
    if (imported.kind === 'package') {
      this.report(
        sourceNode,
        'GB1053',
        `Package component ${name} from ${JSON.stringify(imported.specifier)} cannot participate in Go-rendered markup.`,
        'Wrap the package component in ClientOnly with a deterministic fallback.',
      )
      return undefined
    }
    if (imported.kind === 'unresolved') {
      this.report(
        sourceNode,
        'GB1054',
        `Cannot resolve component ${name} from ${JSON.stringify(imported.specifier)}: ${imported.reason}`,
        'Fix the import path or add an explicit source-root alias.',
      )
      return undefined
    }
    const target = imported.compiler.resolveExport(imported.exportName)
    if (!target) {
      const knownExport = imported.compiler.hasExport(imported.exportName)
      const reportUnsupportedShape = (): PlanNode | undefined => {
        this.report(
          sourceNode,
          knownExport ? 'GB1056' : 'GB1055',
          knownExport
            ? `${JSON.stringify(imported.specifier)} exports ${JSON.stringify(imported.exportName)}, but its component shape is not portable.`
            : `${JSON.stringify(imported.specifier)} does not export component ${JSON.stringify(imported.exportName)}.`,
          knownExport
            ? 'Keep it below a use client boundary or wrap it in ClientOnly.'
            : 'Fix the export name or package entry point.',
        )
        return undefined
      }
      const clientBoundary = knownExport
        ? imported.compiler.clientBoundaryForExport(imported.exportName)
        : undefined
      return clientBoundary
        ? this.compileAtClientBoundary(
            name,
            sourceNode,
            clientBoundary,
            'callSite',
            reportUnsupportedShape,
          )
        : reportUnsupportedShape()
    }
    const targetCompiler = target.compiler
    const inheritedNamespace =
      this.namespaceScopes[this.namespaceScopes.length - 1] ?? 'html'
    const compile = () => {
      targetCompiler.namespaceScopes.push(inheritedNamespace)
      try {
        return targetCompiler.compileComponent(
          target.name,
          target.component,
          supplied,
          false,
          suppliedNodes,
        )
      } finally {
        targetCompiler.namespaceScopes.pop()
      }
    }
    const clientBoundary = target.clientBoundary
    return clientBoundary
      ? this.compileAtClientBoundary(
          name,
          sourceNode,
          clientBoundary,
          'callSite',
          compile,
        )
      : compile()
  }

  private compileReactElementCall(
    call: ts.CallExpression,
    environment: ExpressionEnvironment,
  ): { handled: boolean; node?: PlanNode } {
    if (this.isProtectedHookCall(call, 'cloneElement')) {
      const node = this.compileCloneElement(call, environment)
      return node ? { handled: true, node } : { handled: true }
    }
    const callName = call.expression.getText(this.sourceFile)
    const isCreateElement =
      callName === 'createElement' ||
      callName === 'React.createElement' ||
      this.isProtectedHookCall(call, 'createElement')
    if (
      !isCreateElement &&
      ![
        'jsx',
        'jsxs',
        '_jsx',
        '_jsxs',
        'jsxDEV',
        '_jsxDEV',
      ].includes(callName)
    ) {
      return { handled: false }
    }
    const tagExpression = call.arguments[0]
    if (!tagExpression) {
      this.report(call, 'GB1091', `${callName} requires an element type.`)
      return { handled: true }
    }
    if (
      (ts.isIdentifier(tagExpression) && this.isFragmentTag(tagExpression.text)) ||
      (ts.isPropertyAccessExpression(tagExpression) &&
        ts.isIdentifier(tagExpression.expression) &&
        this.reactNamespaceLocals.has(tagExpression.expression.text) &&
        tagExpression.name.text === 'Fragment')
    ) {
      const propsExpression = call.arguments[1]
      const properties = propsExpression
        ? this.compileObjectProperties(propsExpression, environment)
        : []
      if (!properties) return { handled: true }
      for (const property of properties) {
        if (property.name === 'key' || property.name === 'children') continue
        this.report(
          propsExpression ?? call,
          'GB1095',
          `Portable Fragment only accepts key; found ${property.name}.`,
        )
        return { handled: true }
      }
      const childNodes: PlanNode[] = []
      const propertyChildren = properties.find((property) => property.name === 'children')
      if (propertyChildren?.node) childNodes.push(propertyChildren.node)
      if (isCreateElement) {
        for (const childExpression of call.arguments.slice(2)) {
          const child = this.compileNodeExpression(childExpression, environment)
          if (child) childNodes.push(child)
        }
      }
      return {
        handled: true,
        node: {
          kind: 'fragment',
          children: childNodes.flatMap((node) =>
            node.kind === 'fragment' ? node.children : [node],
          ),
        },
      }
    }
    const propsExpression = call.arguments[1]
    const properties = propsExpression
      ? this.compileObjectProperties(propsExpression, environment)
      : []
    if (!properties) return { handled: true }

    const childNodes: PlanNode[] = []
    const propertyChildren = properties.find((property) => property.name === 'children')
    if (propertyChildren?.node) childNodes.push(propertyChildren.node)
    if (propertyChildren?.value) {
      childNodes.push({ kind: 'text', value: propertyChildren.value })
    }
    if (isCreateElement) {
      for (const childExpression of call.arguments.slice(2)) {
        const child = this.compileNodeExpression(childExpression, environment)
        if (child) childNodes.push(child)
      }
    }
    const filteredProperties = properties.filter(
      (property) => property.name !== 'children' && property.name !== 'key',
    )

    if (ts.isStringLiteral(tagExpression)) {
      const tag = tagExpression.text
      const attributes: Attribute[] = []
      for (const property of filteredProperties) {
        if (property.name === 'ref' || /^on[A-Z]/.test(property.name)) continue
        if (property.name === 'dangerouslySetInnerHTML') {
          this.report(
            propsExpression ?? call,
            'GB1031',
            'dangerouslySetInnerHTML is not portable.',
            'Use SafeHTML with a schema-validated value.',
          )
          continue
        }
        if (!property.value) {
          this.report(
            propsExpression ?? call,
            'GB1092',
            `Element property ${property.name} must be a portable scalar.`,
          )
          continue
        }
        const mode = booleanAttributes.has(property.name)
          ? 'boolean'
          : urlAttributes.has(property.name)
            ? 'url'
            : property.name === 'style'
              ? 'style'
              : 'string'
        attributes.push({ name: property.name, value: property.value, mode })
      }
      const inheritedNamespace = this.namespaceScopes.at(-1) ?? 'html'
      const namespace = tag === 'svg' ? 'svg' : inheritedNamespace
      return {
        handled: true,
        node: {
          kind: 'element',
          tag,
          namespace,
          attributes,
          children: childNodes.flatMap((node) =>
            node.kind === 'fragment' ? node.children : [node],
          ),
        },
      }
    }

    if (!ts.isIdentifier(tagExpression)) {
      this.report(
        tagExpression,
        'GB1050',
        `Element type ${tagExpression.getText(this.sourceFile)} is not portable.`,
      )
      return { handled: true }
    }
    if (this.isLazyComponent(tagExpression.text)) {
      this.report(
        tagExpression,
        'GB1098',
        `React.lazy component ${tagExpression.text} cannot participate in Go-rendered markup.`,
        'Wrap it in <ClientOnly> with a portable fallback, or place it under a use client boundary.',
      )
      return { handled: true }
    }
    const supplied = new Map<string, PlanExpression>()
    const suppliedNodes = new Map<string, PlanNode>()
    for (const property of filteredProperties) {
      if (property.value) supplied.set(property.name, property.value)
      if (property.node) suppliedNodes.set(property.name, property.node)
    }
    if (childNodes.length > 0) {
      suppliedNodes.set(
        'children',
        childNodes.length === 1
          ? childNodes[0]!
          : { kind: 'fragment', children: childNodes },
      )
    }
    const node = this.compileComponentReference(
      tagExpression.text,
      supplied,
      suppliedNodes,
      call,
    )
    return node ? { handled: true, node } : { handled: true }
  }

  private compileCloneElement(
    call: ts.CallExpression,
    environment: ExpressionEnvironment,
  ): PlanNode | undefined {
    const elementArg = call.arguments[0]
    const propsArg = call.arguments[1]
    if (!elementArg) {
      this.report(call, 'GB1097', 'cloneElement requires an element argument.')
      return undefined
    }
    let elementExpression = this.unwrapExpression(elementArg)
    if (ts.isIdentifier(elementExpression)) {
      const bound = this.jsxElementScopes.at(-1)?.get(elementExpression.text)
      if (bound) elementExpression = bound
      else {
        this.report(
          elementArg,
          'GB1097',
          'cloneElement element must be a portable JSX expression local.',
          'Bind const el = <div /> then cloneElement(el, props), or write the element inline.',
        )
        return undefined
      }
    }
    const base = this.compileNodeExpression(elementExpression, environment)
    if (!base || base.kind !== 'element') {
      this.report(
        elementArg,
        'GB1097',
        'cloneElement only supports portable host elements.',
      )
      return undefined
    }
    if (!propsArg || this.isReactEmptyExpression(propsArg)) return base
    const properties = this.compileObjectProperties(propsArg, environment)
    if (!properties) return undefined
    const attributes = [...(base.attributes ?? [])]
    for (const property of properties) {
      if (property.name === 'key' || property.name === 'children' || /^on[A-Z]/.test(property.name)) {
        continue
      }
      if (!property.value) {
        this.report(
          propsArg,
          'GB1097',
          `cloneElement prop ${property.name} must be a portable scalar.`,
        )
        return undefined
      }
      this.mergeAttribute(attributes, {
        name: property.name,
        value: property.value,
        mode: booleanAttributes.has(property.name)
          ? 'boolean'
          : urlAttributes.has(property.name)
            ? 'url'
            : property.name === 'style'
              ? 'style'
              : 'string',
      })
    }
    const propertyChildren = properties.find((property) => property.name === 'children')
    let children = base.children ?? []
    if (propertyChildren?.node) {
      children = propertyChildren.node.kind === 'fragment'
        ? propertyChildren.node.children
        : [propertyChildren.node]
    } else if (propertyChildren?.value) {
      children = [{ kind: 'text', value: propertyChildren.value }]
    }
    return {
      kind: 'element',
      tag: base.tag,
      ...(base.namespace === undefined ? {} : { namespace: base.namespace }),
      attributes,
      children,
    }
  }

  private tryCompileChildrenHelper(
    expression: ts.CallExpression,
    environment: ExpressionEnvironment,
  ): { handled: boolean; node?: PlanNode } {
    if (!ts.isPropertyAccessExpression(expression.expression)) {
      return { handled: false }
    }
    const method = expression.expression.name.text
    if (method !== 'map' && method !== 'toArray') return { handled: false }
    const receiver = expression.expression.expression
    const isChildren =
      (ts.isIdentifier(receiver) && this.reactHookLocals.get(receiver.text) === 'Children') ||
      (ts.isPropertyAccessExpression(receiver) &&
        ts.isIdentifier(receiver.expression) &&
        this.reactNamespaceLocals.has(receiver.expression.text) &&
        receiver.name.text === 'Children')
    if (!isChildren) return { handled: false }

    const childrenArg = expression.arguments[0]
    if (!childrenArg) {
      this.report(expression, 'GB1096', `Children.${method} requires a children argument.`)
      return { handled: true }
    }
    const staticChildren = this.unwrapExpression(childrenArg)
    if (
      !ts.isJsxFragment(staticChildren) &&
      !ts.isArrayLiteralExpression(staticChildren) &&
      !ts.isJsxElement(staticChildren) &&
      !ts.isJsxSelfClosingElement(staticChildren)
    ) {
      this.report(
        childrenArg,
        'GB1096',
        `Children.${method} is portable only over static JSX children or arrays.`,
        'Dynamic props.children enumeration is not portable; precompute structure in Go or write explicit markup.',
      )
      return { handled: true }
    }
    if (method === 'toArray') {
      const node = this.compileNodeExpression(staticChildren, environment)
      return node ? { handled: true, node } : { handled: true }
    }
    // Children.map(children, callback) — lower like .map when children are static.
    const callback = expression.arguments[1]
    if (
      !callback ||
      (!ts.isArrowFunction(callback) && !ts.isFunctionExpression(callback))
    ) {
      this.report(
        expression,
        'GB1096',
        'Portable Children.map requires an inline callback.',
      )
      return { handled: true }
    }
    // Expand static children into a fragment of mapped results when the input
    // is a static array/fragment of elements (same structures as manual maps).
    const itemsExpression = ts.isArrayLiteralExpression(staticChildren)
      ? staticChildren
      : ts.isJsxFragment(staticChildren)
        ? undefined
        : undefined
    if (ts.isArrayLiteralExpression(staticChildren)) {
      const mapped: PlanNode[] = []
      const itemParameter = callback.parameters[0]
      if (!itemParameter || !ts.isIdentifier(itemParameter.name)) {
        this.report(callback, 'GB1096', 'Children.map callback requires an item identifier.')
        return { handled: true }
      }
      for (const [index, element] of staticChildren.elements.entries()) {
        if (ts.isSpreadElement(element)) {
          this.report(element, 'GB1096', 'Children.map does not support spreads.')
          return { handled: true }
        }
        const callbackEnvironment = new Map(environment)
        const compiledElement = this.compileNodeExpression(element, environment)
        if (compiledElement) {
          callbackEnvironment.set(itemParameter.name.text, {
            kind: 'literal',
            value: null,
          })
        }
        // Bind the element as a JSX local for the callback body when possible.
        if (
          ts.isJsxElement(element) ||
          ts.isJsxSelfClosingElement(element) ||
          ts.isJsxFragment(element)
        ) {
          this.jsxElementScopes.at(-1)?.set(itemParameter.name.text, element)
        }
        void index
        let bodyExpression: ts.Expression
        if (ts.isBlock(callback.body)) {
          const returns = callback.body.statements.filter(ts.isReturnStatement)
          if (!returns.at(-1)?.expression) {
            this.report(callback, 'GB1096', 'Children.map callback must return JSX.')
            return { handled: true }
          }
          bodyExpression = returns.at(-1)!.expression!
        } else {
          bodyExpression = callback.body
        }
        // Recompile with the element substituted via compileNodeExpression on a
        // rewritten approach: compile the static element through the callback by
        // treating the callback as identity when it returns the child, else
        // compile the body with the child available as a node scope binding.
        this.nodeScopes.push(
          new Map([[itemParameter.name.text, compiledElement!]]),
        )
        try {
          const body = this.compileNodeExpression(bodyExpression, callbackEnvironment)
          if (body) mapped.push(body)
        } finally {
          this.nodeScopes.pop()
          this.jsxElementScopes.at(-1)?.delete(itemParameter.name.text)
        }
      }
      return {
        handled: true,
        node: { kind: 'fragment', children: mapped },
      }
    }
    void itemsExpression
    // Fragment / single element: treat as toArray then identity map.
    const node = this.compileNodeExpression(staticChildren, environment)
    return node ? { handled: true, node } : { handled: true }
  }

  private compileObjectProperties(
    expression: ts.Expression,
    environment: ExpressionEnvironment,
  ): CompiledProperty[] | undefined {
    expression = this.unwrapExpression(expression)
    if (
      expression.kind === ts.SyntaxKind.NullKeyword ||
      (ts.isIdentifier(expression) && expression.text === 'undefined')
    ) return []
    if (
      ts.isCallExpression(expression) &&
      expression.expression.getText(this.sourceFile) === 'Object.assign'
    ) {
      const result: CompiledProperty[] = []
      for (const argument of expression.arguments) {
        const entries = this.compileObjectProperties(argument, environment)
        if (!entries) return undefined
        mergeProperties(result, entries)
      }
      return result
    }
    if (ts.isIdentifier(expression) || ts.isPropertyAccessExpression(expression)) {
      const prefix = expression.getText(this.sourceFile) + '.'
      const result: CompiledProperty[] = []
      for (const [name, value] of environment) {
        if (name.startsWith(prefix) && !name.slice(prefix.length).includes('.')) {
          result.push({ name: name.slice(prefix.length), value })
        }
      }
      if (result.length > 0) return result
      if (ts.isIdentifier(expression)) {
        const bound = environment.get(expression.text)
        if (
          bound?.kind === 'literal' &&
          bound.value !== null &&
          typeof bound.value === 'object' &&
          !Array.isArray(bound.value)
        ) {
          return Object.entries(bound.value as Record<string, unknown>).map(
            ([name, value]) => ({
              name,
              value: { kind: 'literal' as const, value },
            }),
          )
        }
        // Rest props bind only `name.prop` keys. When nothing remains, the
        // spread is a portable no-op (same as `{...{}}`).
        if (!bound) return []
      }
      this.report(
        expression,
        'GB1093',
        `Object spread ${expression.getText(this.sourceFile)} is not statically known.`,
        'Spread a statically known object literal, rest props from a nested component call, or write each attribute explicitly.',
      )
      return undefined
    }
    if (!ts.isObjectLiteralExpression(expression)) {
      this.report(
        expression,
        'GB1093',
        'Compiled JSX props must be an object literal or safe Object.assign call.',
      )
      return undefined
    }
    const result: CompiledProperty[] = []
    for (const property of expression.properties) {
      if (ts.isSpreadAssignment(property)) {
        const entries = this.compileObjectProperties(property.expression, environment)
        if (!entries) return undefined
        mergeProperties(result, entries)
        continue
      }
      if (ts.isShorthandPropertyAssignment(property)) {
        const value = this.compileExpression(property.name, environment)
        if (!value) return undefined
        mergeProperties(result, [{ name: property.name.text, value }])
        continue
      }
      if (!ts.isPropertyAssignment(property)) {
        this.report(property, 'GB1094', 'Compiled JSX props must be data properties.')
        return undefined
      }
      const name = this.propertyNameText(property.name)
      if (name === undefined) return undefined
      if (name === 'children') {
        const node = this.compileNodeExpression(property.initializer, environment)
        if (node) mergeProperties(result, [{ name, node }])
        else if (this.isReactEmptyExpression(property.initializer)) {
          mergeProperties(result, [{ name }])
        }
        continue
      }
      if (name === 'ref' || /^on[A-Z]/.test(name)) {
        mergeProperties(result, [{ name }])
        continue
      }
      const value = this.compileExpression(property.initializer, environment)
      if (!value) return undefined
      mergeProperties(result, [{ name, value }])
    }
    return result
  }

  private compileAtClientBoundary(
    component: string,
    sourceNode: ts.Node,
    boundaryCompiler: SourceCompiler,
    target: ClientBoundaryRecord['target'],
    compile: () => PlanNode | undefined,
  ): PlanNode | undefined {
    const diagnosticStart = this.context.diagnostics.length
    const boundaryStart = this.context.clientBoundaries.length
    const useIdStart = this.context.useIdSites.length
    const dateStart = this.context.dateIntrinsicSites.length
    const result = compile()
    const diagnostics = this.context.diagnostics.slice(diagnosticStart)
    if (result && diagnostics.length === 0) return result
    if (
      diagnostics.length === 0 ||
      diagnostics.some((diagnostic) => !isDowngradeableDiagnostic(diagnostic))
    ) {
      return result
    }

    this.context.diagnostics.splice(diagnosticStart)
    this.context.clientBoundaries.splice(boundaryStart)
    this.context.useIdSites.splice(useIdStart)
    this.context.dateIntrinsicSites.splice(dateStart)
    const source = this.context.sourceName(this.fileName)
    const boundary = this.context.sourceName(boundaryCompiler.fileName)
    const start = sourceNode.getStart(this.sourceFile)
    const end = sourceNode.getEnd()
    const location = this.sourceFile.getLineAndCharacterOfPosition(start)
    const identity = [
      this.context.routeId,
      source,
      start,
      end,
      component,
      boundary,
      target,
    ].join('\0')
    this.context.clientBoundaries.push({
      id: `gbc_${createHash('sha256').update(identity).digest('hex').slice(0, 20)}`,
      routeId: this.context.routeId,
      source,
      component,
      boundary,
      reason: diagnostics
        .map((diagnostic) => `${diagnostic.code}: ${diagnostic.message}`)
        .join(' | '),
      target,
      start,
      end,
      line: location.line + 1,
      column: location.character + 1,
    })
    return { kind: 'clientOnly' }
  }

  private tryCompileEach(
    expression: ts.Expression,
    environment: ExpressionEnvironment,
  ): PlanNode | undefined {
    if (
      !ts.isCallExpression(expression) ||
      !ts.isPropertyAccessExpression(expression.expression) ||
      expression.expression.name.text !== 'map'
    ) {
      return undefined
    }
    const callback = expression.arguments[0]
    if (
      !callback ||
      (!ts.isArrowFunction(callback) && !ts.isFunctionExpression(callback))
    ) {
      this.report(
        expression,
        'GB1060',
        'Portable .map rendering requires an inline function callback.',
      )
      return undefined
    }
    const itemParameter = callback.parameters[0]
    if (!itemParameter || !ts.isIdentifier(itemParameter.name)) {
      this.report(
        callback,
        'GB1061',
        'Portable .map callbacks require an item identifier.',
      )
      return undefined
    }
    const indexParameter = callback.parameters[1]
    if (indexParameter && !ts.isIdentifier(indexParameter.name)) {
      this.report(
        indexParameter,
        'GB1062',
        'Map index parameter must be an identifier.',
      )
      return undefined
    }
    const indexName =
      indexParameter && ts.isIdentifier(indexParameter.name)
        ? indexParameter.name.text
        : undefined
    const items = this.compileExpression(
      expression.expression.expression,
      environment,
    )
    if (!items) return undefined
    const callbackEnvironment = new Map(environment)
    callbackEnvironment.set(itemParameter.name.text, {
      kind: 'path',
      path: [itemParameter.name.text],
    })
    if (indexName) {
      callbackEnvironment.set(indexName, {
        kind: 'path',
        path: [indexName],
      })
    }
    let bodyExpression: ts.Expression | undefined
    if (ts.isBlock(callback.body)) {
      const returns = callback.body.statements.filter(ts.isReturnStatement)
      const returnExpression = returns.at(-1)?.expression
      if (!returnExpression) {
        this.report(
          callback.body,
          'GB1063',
          'Portable .map callbacks must return one JSX expression.',
        )
        return undefined
      }
      bodyExpression = returnExpression
    } else {
      bodyExpression = callback.body
    }
    const keyNode = this.unwrapExpression(bodyExpression)
    const keyExpression = this.getRootKeyExpression(keyNode)
    if (!keyExpression) {
      this.report(
        bodyExpression,
        'GB1064',
        'Portable .map output requires an explicit expression key on its root element.',
        'Use a stable data key such as key={item.id}; array indexes are discouraged for hydration identity.',
      )
      return undefined
    }
    const key = this.compileExpression(keyExpression, callbackEnvironment)
    if (!key) return undefined
    this.eachKeyStack.push({
      keyText: keyExpression.getText(this.sourceFile),
      key,
    })
    let body: PlanNode | undefined
    try {
      if (ts.isBlock(callback.body)) {
        for (const statement of callback.body.statements) {
          if (ts.isReturnStatement(statement)) continue
          if (ts.isVariableStatement(statement)) {
            this.compileVariableStatement(statement, callbackEnvironment)
            continue
          }
          this.report(
            statement,
            'GB1063',
            'Portable .map callbacks may only contain local bindings and a single JSX return.',
          )
          return undefined
        }
      }
      body = this.compileNodeExpression(bodyExpression, callbackEnvironment)
    } finally {
      this.eachKeyStack.pop()
    }
    if (!body) return undefined
    const node = {
      kind: 'each' as const,
      items,
      item: itemParameter.name.text,
      key,
      body,
    }
    return indexName ? { ...node, index: indexName } : node
  }

  private getRootKeyAttribute(
    expression: ts.Expression,
  ): ts.JsxAttribute | undefined {
    if (ts.isJsxFragment(expression)) {
      // Short fragments cannot carry keys; require Fragment/React.Fragment.
      return undefined
    }
    const attributes = ts.isJsxElement(expression)
      ? expression.openingElement.attributes
      : ts.isJsxSelfClosingElement(expression)
        ? expression.attributes
        : undefined
    return attributes ? this.findAttribute(attributes, 'key') : undefined
  }

  private getRootKeyExpression(
    expression: ts.Expression,
  ): ts.Expression | undefined {
    const jsxKey = this.getRootKeyAttribute(expression)
    if (
      jsxKey?.initializer &&
      ts.isJsxExpression(jsxKey.initializer) &&
      jsxKey.initializer.expression
    ) {
      return jsxKey.initializer.expression
    }
    if (!ts.isCallExpression(expression)) return undefined
    const callName = expression.expression.getText(this.sourceFile)
    const isCreateElement =
      callName === 'createElement' ||
      callName === 'React.createElement' ||
      this.isProtectedHookCall(expression, 'createElement')
    if (!isCreateElement) return undefined
    const props = expression.arguments[1]
    if (!props || !ts.isObjectLiteralExpression(props)) return undefined
    for (const property of props.properties) {
      if (
        ts.isPropertyAssignment(property) &&
        this.propertyNameText(property.name) === 'key'
      ) {
        return property.initializer
      }
    }
    return undefined
  }

  private compileExpression(
    expression: ts.Expression,
    environment: ExpressionEnvironment,
  ): PlanExpression | undefined {
    expression = this.unwrapExpression(expression)
    if (
      ts.isStringLiteral(expression) ||
      ts.isNoSubstitutionTemplateLiteral(expression)
    ) {
      return { kind: 'literal', value: expression.text }
    }
    if (ts.isTemplateExpression(expression)) {
      let value: PlanExpression = {
        kind: 'literal',
        value: expression.head.text,
      }
      for (const span of expression.templateSpans) {
        const interpolation = this.compileExpression(
          span.expression,
          environment,
        )
        if (!interpolation) return undefined
        value = {
          kind: 'binary',
          operator: '+',
          left: value,
          right: {
            kind: 'helper',
            name: 'string',
            arguments: [interpolation],
          },
        }
        if (span.literal.text !== '') {
          value = {
            kind: 'binary',
            operator: '+',
            left: value,
            right: { kind: 'literal', value: span.literal.text },
          }
        }
      }
      return value
    }
    if (ts.isNumericLiteral(expression)) {
      return { kind: 'literal', value: Number(expression.text) }
    }
    if (expression.kind === ts.SyntaxKind.TrueKeyword)
      return { kind: 'literal', value: true }
    if (expression.kind === ts.SyntaxKind.FalseKeyword)
      return { kind: 'literal', value: false }
    if (expression.kind === ts.SyntaxKind.NullKeyword)
      return { kind: 'literal', value: null }
    if (ts.isConditionalExpression(expression)) {
      this.context.conditionalHookDepth += 1
      try {
        const test = this.compileExpression(expression.condition, environment)
        const consequent = this.compileExpression(
          expression.whenTrue,
          environment,
        )
        const alternate = this.compileExpression(
          expression.whenFalse,
          environment,
        )
        if (!test || !consequent || !alternate) return undefined
        return { kind: 'ternary', test, consequent, alternate }
      } finally {
        this.context.conditionalHookDepth -= 1
      }
    }
    if (ts.isIdentifier(expression)) {
      if (expression.text === 'undefined')
        return { kind: 'literal', value: null }
      const value = environment.get(expression.text)
      if (value) {
        if (value.kind === 'path' && value.path.length === 0) {
          this.report(
            expression,
            'GB1069',
            'The complete props object cannot be rendered as a portable scalar.',
            'Read a schema-backed property from props.',
          )
          return undefined
        }
        return value
      }
      const nonPortable = this.nonPortableModuleBindings.get(expression.text)
      if (nonPortable) {
        this.report(
          expression,
          'GB1068',
          nonPortable === 'const'
            ? `Module-level const ${expression.text} is not a portable constant (initializer must be a portable expression).`
            : `Module-level ${nonPortable} ${expression.text} is not portable; only const bindings may be referenced in portable expressions.`,
          'Use a module-level const with a portable literal/expression initializer, inline the value, or calculate it in Go.',
        )
        return undefined
      }
      this.report(
        expression,
        'GB1070',
        `Identifier ${expression.text} is not a portable prop, item, state initializer, local constant, or module-level constant.`,
        'Calculate the value in Go and pass it as a typed prop, or use a supported portable helper.',
      )
      return undefined
    }
    if (ts.isPropertyAccessExpression(expression)) {
      const fullName = expression.getText(this.sourceFile)
      const direct = environment.get(fullName)
      if (direct) return direct
      const base = ts.isIdentifier(expression.expression)
        ? environment.get(expression.expression.text)
        : this.compileExpression(expression.expression, environment)
      if (base?.kind !== 'path') {
        this.report(
          expression,
          'GB1071',
          'Property access requires a portable path base.',
        )
        return undefined
      }
      return { kind: 'path', path: [...base.path, expression.name.text] }
    }
    if (ts.isElementAccessExpression(expression)) {
      const base = ts.isIdentifier(expression.expression)
        ? environment.get(expression.expression.text)
        : this.compileExpression(expression.expression, environment)
      const argument = expression.argumentExpression
      if (base?.kind !== 'path' || !argument) {
        this.report(
          expression,
          'GB1072',
          'Element access requires a portable path base and key.',
        )
        return undefined
      }
      if (ts.isStringLiteral(argument) || ts.isNumericLiteral(argument)) {
        const key = ts.isNumericLiteral(argument)
          ? Number(argument.text)
          : argument.text
        return { kind: 'path', path: [...base.path, key] }
      }
      this.report(
        argument,
        'GB1073',
        'Computed dynamic property access is not portable.',
        'Use a typed property, literal index, or precompute the value in Go.',
      )
      return undefined
    }
    if (ts.isPrefixUnaryExpression(expression)) {
      const operator =
        expression.operator === ts.SyntaxKind.ExclamationToken
          ? '!'
          : expression.operator === ts.SyntaxKind.MinusToken
            ? '-'
            : undefined
      if (!operator) {
        this.report(expression, 'GB1074', 'Only ! and unary - are portable.')
        return undefined
      }
      const operand = this.compileExpression(expression.operand, environment)
      return operand ? { kind: 'unary', operator, operand } : undefined
    }
    if (ts.isBinaryExpression(expression)) {
      if (
        expression.operatorToken.kind === ts.SyntaxKind.EqualsEqualsToken ||
        expression.operatorToken.kind === ts.SyntaxKind.ExclamationEqualsToken
      ) {
        this.report(
          expression.operatorToken,
          'GB1075',
          'Loose equality is not portable in initial markup.',
          'Use === or !== so Go and JavaScript compare the same typed values.',
        )
        return undefined
      }
      const operator = this.binaryOperator(expression.operatorToken.kind)
      if (!operator) {
        this.report(
          expression.operatorToken,
          'GB1075',
          `Binary operator ${expression.operatorToken.getText(this.sourceFile)} is not portable.`,
        )
        return undefined
      }
      const shortCircuit =
        operator === '&&' || operator === '||' || operator === '??'
      const left = this.compileExpression(expression.left, environment)
      let right: PlanExpression | undefined
      if (shortCircuit) {
        this.context.conditionalHookDepth += 1
        try {
          right = this.compileExpression(expression.right, environment)
        } finally {
          this.context.conditionalHookDepth -= 1
        }
      } else {
        right = this.compileExpression(expression.right, environment)
      }
      if (
        left &&
        right &&
        (operator === '==' || operator === '!=') &&
        (!this.isPortableScalarEqualityOperand(
          expression.left,
          left,
          right,
        ) ||
          !this.isPortableScalarEqualityOperand(
            expression.right,
            right,
            left,
          ))
      ) {
        this.report(
          expression.operatorToken,
          'GB1082',
          'Portable strict equality is limited to scalar values; JavaScript objects and arrays use reference identity.',
          'Compare a stable scalar property such as an ID, or calculate the boolean in Go and pass it as a typed prop.',
        )
        return undefined
      }
      return left && right
        ? { kind: 'binary', operator, left, right }
        : undefined
    }
    if (ts.isCallExpression(expression)) {
      const intrinsic = this.compilePortableIntrinsic(expression)
      if (intrinsic) return intrinsic
      if (
        ts.isIdentifier(expression.expression) &&
        helperNames.has(expression.expression.text)
      ) {
        const args = expression.arguments.map((argument) =>
          this.compileExpression(argument, environment),
        )
        if (args.some((argument) => !argument)) return undefined
        const helperName = expression.expression.text
        if (helperName === 'lower' || helperName === 'upper') {
          const argument = args[0]
          if (
            expression.arguments.length !== 1 ||
            argument?.kind !== 'literal' ||
            typeof argument.value !== 'string' ||
            !/^[\x00-\x7F]*$/.test(argument.value)
          ) {
            this.report(
              expression,
              'GB1083',
              `${helperName}() is portable only for a statically known ASCII string.`,
              'Calculate Unicode-aware casing in Go and pass the result as a typed prop; JavaScript and Go Unicode case mappings are not byte-for-byte compatible.',
            )
            return undefined
          }
        }
        return {
          kind: 'helper',
          name: helperName as 'string' | 'lower' | 'upper' | 'join' | 'url' | 'imageSrc',
          arguments: args as PlanExpression[],
        }
      }
      if (this.isProtectedHookCall(expression, 'useId')) {
        return this.compileProtectedUseId(expression)
      }
      if (this.isProtectedHookCall(expression, 'useMemo')) {
        if (!this.assertUnconditionalHook(expression, 'useMemo')) return undefined
        return this.compileProtectedUseMemo(expression, environment)
      }
      if (this.isProtectedHookCall(expression, 'useCallback')) {
        if (!this.assertUnconditionalHook(expression, 'useCallback')) {
          return undefined
        }
        return this.compileProtectedUseCallback(expression, environment)
      }
      if (this.isProtectedHookCall(expression, 'useContext')) {
        if (!this.assertUnconditionalHook(expression, 'useContext')) {
          return undefined
        }
        return this.compileProtectedUseContext(expression)
      }
      const callName = expression.expression.getText(this.sourceFile)
      const knownDeferred = new Set([
        'useLayoutEffect',
      ])
      this.report(
        expression,
        knownDeferred.has(callName) ? 'GB1076' : 'GB1077',
        knownDeferred.has(callName)
          ? `${callName} is explicitly deferred from the MVP portable render profile.`
          : `Function call ${callName} cannot execute in the Go rendering plan.`,
        'Calculate the initial value in Go, use a portable helper, or place browser-dependent markup behind ClientOnly.',
      )
      return undefined
    }
    if (ts.isObjectLiteralExpression(expression)) {
      const value: Record<string, unknown> = {}
      for (const property of expression.properties) {
        if (!ts.isPropertyAssignment(property)) {
          this.report(
            property,
            'GB1078',
            'Only literal object properties are portable.',
          )
          return undefined
        }
        const name = this.propertyNameText(property.name)
        const compiled = this.compileExpression(
          property.initializer,
          environment,
        )
        if (name === undefined || compiled?.kind !== 'literal') {
          this.report(
            property,
            'GB1079',
            'Object literals may contain only literal values in the MVP.',
          )
          return undefined
        }
        value[name] = compiled.value
      }
      return { kind: 'literal', value }
    }
    if (ts.isArrayLiteralExpression(expression)) {
      const values: unknown[] = []
      for (const element of expression.elements) {
        const compiled = this.compileExpression(element, environment)
        if (compiled?.kind !== 'literal') {
          this.report(
            element,
            'GB1080',
            'Array literals may contain only literal values.',
          )
          return undefined
        }
        values.push(compiled.value)
      }
      return { kind: 'literal', value: values }
    }
    this.report(
      expression,
      'GB1081',
      `Unsupported render expression: ${ts.SyntaxKind[expression.kind]}.`,
      'Calculate it in Go, rewrite it with portable operations, or put the region behind ClientOnly.',
    )
    return undefined
  }

  private isPortableScalarEqualityOperand(
    expression: ts.Expression,
    compiled: PlanExpression,
    other: PlanExpression,
  ): boolean {
    if (compiled.kind === 'literal') {
      return (
        compiled.value === null ||
        typeof compiled.value === 'string' ||
        typeof compiled.value === 'number' ||
        typeof compiled.value === 'boolean'
      )
    }
    if (compiled.kind === 'unary' || compiled.kind === 'ternary') return true
    if (compiled.kind === 'helper') return compiled.name !== 'style'
    if (compiled.kind === 'binary') {
      return !['&&', '||', '??'].includes(compiled.operator)
    }
    if (
      compiled.kind === 'path' &&
      other.kind === 'literal' &&
      (other.value === null ||
        typeof other.value === 'string' ||
        typeof other.value === 'number' ||
        typeof other.value === 'boolean')
    ) return true
    return this.checker
      ? this.isPortableScalarType(this.checker.getTypeAtLocation(expression))
      : false
  }

  private isPortableScalarType(type: ts.Type): boolean {
    if (type.isUnion())
      return type.types.every((member) => this.isPortableScalarType(member))
    if (type.isIntersection()) return false
    const scalarFlags =
      ts.TypeFlags.StringLike |
      ts.TypeFlags.NumberLike |
      ts.TypeFlags.BooleanLike |
      ts.TypeFlags.Null |
      ts.TypeFlags.Undefined
    return (
      (type.flags & scalarFlags) !== 0 &&
      (type.flags &
        (ts.TypeFlags.Any | ts.TypeFlags.Unknown | ts.TypeFlags.Object)) ===
        0
    )
  }

  private binaryOperator(
    kind: ts.SyntaxKind,
  ): Extract<PlanExpression, { kind: 'binary' }>['operator'] | undefined {
    switch (kind) {
      case ts.SyntaxKind.PlusToken:
        return '+'
      case ts.SyntaxKind.MinusToken:
        return '-'
      case ts.SyntaxKind.AsteriskToken:
        return '*'
      case ts.SyntaxKind.SlashToken:
        return '/'
      case ts.SyntaxKind.PercentToken:
        return '%'
      case ts.SyntaxKind.EqualsEqualsEqualsToken:
        return '=='
      case ts.SyntaxKind.ExclamationEqualsEqualsToken:
        return '!='
      case ts.SyntaxKind.LessThanToken:
        return '<'
      case ts.SyntaxKind.LessThanEqualsToken:
        return '<='
      case ts.SyntaxKind.GreaterThanToken:
        return '>'
      case ts.SyntaxKind.GreaterThanEqualsToken:
        return '>='
      case ts.SyntaxKind.AmpersandAmpersandToken:
        return '&&'
      case ts.SyntaxKind.BarBarToken:
        return '||'
      case ts.SyntaxKind.QuestionQuestionToken:
        return '??'
      default:
        return undefined
    }
  }

  private unwrapExpression(expression: ts.Expression): ts.Expression {
    while (
      ts.isParenthesizedExpression(expression) ||
      ts.isAsExpression(expression) ||
      ts.isTypeAssertionExpression(expression) ||
      ts.isNonNullExpression(expression) ||
      ts.isSatisfiesExpression(expression)
    ) {
      expression = expression.expression
    }
    return expression
  }

  private validateHydrationTree(root: PlanNode, location: ts.Node): void {
    const reported = new Set<string>()
    const report = (
      code: string,
      message: string,
      suggestion: string,
    ): void => {
      if (reported.has(code + message)) return
      reported.add(code + message)
      this.report(location, code, message, suggestion)
    }
    const blockInsideParagraph = new Set([
      'address',
      'article',
      'aside',
      'blockquote',
      'div',
      'dl',
      'fieldset',
      'footer',
      'form',
      'h1',
      'h2',
      'h3',
      'h4',
      'h5',
      'h6',
      'header',
      'hgroup',
      'hr',
      'main',
      'nav',
      'ol',
      'p',
      'pre',
      'section',
      'table',
      'ul',
    ])
    const interactiveInsideButton = new Set([
      'a',
      'button',
      'input',
      'select',
      'textarea',
    ])
    const headings = new Set(['h1', 'h2', 'h3', 'h4', 'h5', 'h6'])
    const visit = (node: PlanNode, ancestors: string[]): void => {
      if (node.kind === 'element') {
        const tag = node.tag.toLowerCase()
        if (ancestors.includes('p') && blockInsideParagraph.has(tag)) {
          report(
            'GB1036',
            `<${tag}> cannot be nested inside <p> without browser reparsing.`,
            'Use valid semantic HTML so the browser DOM remains identical during hydration.',
          )
        }
        if (
          (tag === 'a' && ancestors.includes('a')) ||
          (tag === 'form' && ancestors.includes('form')) ||
          (tag === 'li' && ancestors.includes('li'))
        ) {
          report(
            'GB1036',
            `Nested <${tag}> elements are not hydration-safe.`,
            'Close the outer element before rendering another one.',
          )
        }
        if (
          headings.has(tag) &&
          ancestors.some((ancestor) => headings.has(ancestor))
        ) {
          report(
            'GB1036',
            'Heading elements cannot be nested inside another heading.',
            'Use sibling headings with a valid outline.',
          )
        }
        if (ancestors.includes('button') && interactiveInsideButton.has(tag)) {
          report(
            'GB1036',
            `<${tag}> cannot be nested inside <button>.`,
            'Use one interactive control per activation target.',
          )
        }
        if (
          (tag === 'dt' || tag === 'dd') &&
          ancestors.some((ancestor) => ancestor === 'dt' || ancestor === 'dd')
        ) {
          report(
            'GB1036',
            `<${tag}> cannot be nested inside a description-list item.`,
            'Render dt and dd as siblings under dl.',
          )
        }
        if (
          tag === 'table' &&
          (node.children ?? []).some((child) =>
            this.planCanYieldTag(child, 'tr'),
          )
        ) {
          report(
            'GB1033',
            'A <table> child can render <tr> without an explicit row-group element.',
            'Wrap rows, mapped rows, and row components in <tbody>, <thead>, or <tfoot>.',
          )
        }
        for (const child of node.children ?? [])
          visit(child, [...ancestors, tag])
        return
      }
      if (node.kind === 'fragment')
        for (const child of node.children) visit(child, ancestors)
      else if (node.kind === 'conditional') {
        visit(node.consequent, ancestors)
        if (node.alternate) visit(node.alternate, ancestors)
      } else if (node.kind === 'each') visit(node.body, ancestors)
      else if (node.kind === 'clientOnly' && node.fallback)
        visit(node.fallback, ancestors)
    }
    visit(root, [])
  }

  private planCanYieldTag(node: PlanNode, tag: string): boolean {
    switch (node.kind) {
      case 'element':
        return node.tag.toLowerCase() === tag
      case 'fragment':
        return node.children.some((child) => this.planCanYieldTag(child, tag))
      case 'conditional':
        return (
          this.planCanYieldTag(node.consequent, tag) ||
          (node.alternate ? this.planCanYieldTag(node.alternate, tag) : false)
        )
      case 'each':
        return this.planCanYieldTag(node.body, tag)
      case 'clientOnly':
        return node.fallback
          ? this.planCanYieldTag(node.fallback, tag)
          : false
      default:
        return false
    }
  }

  private isReactEmptyExpression(expression: ts.Expression): boolean {
    expression = this.unwrapExpression(expression)
    return (
      expression.kind === ts.SyntaxKind.NullKeyword ||
      expression.kind === ts.SyntaxKind.TrueKeyword ||
      expression.kind === ts.SyntaxKind.FalseKeyword ||
      (ts.isIdentifier(expression) && expression.text === 'undefined')
    )
  }

  private isIntrinsicTag(name: string): boolean {
    return /^[a-z]/.test(name) || name.includes('-')
  }

  private findAttribute(
    attributes: ts.JsxAttributes,
    name: string,
  ): ts.JsxAttribute | undefined {
    return attributes.properties.find(
      (property): property is ts.JsxAttribute =>
        ts.isJsxAttribute(property) &&
        property.name.getText(this.sourceFile) === name,
    )
  }

  private propertyNameText(name: ts.PropertyName): string | undefined {
    if (
      ts.isIdentifier(name) ||
      ts.isStringLiteral(name) ||
      ts.isNumericLiteral(name)
    ) {
      return name.text
    }
    this.report(name, 'GB1090', 'Computed property names are not portable.')
    return undefined
  }

  private mergeAttribute(
    attributes: Attribute[],
    attribute: Attribute,
  ): void {
    const existing = attributes.findIndex((entry) => entry.name === attribute.name)
    if (existing >= 0) attributes.splice(existing, 1)
    attributes.push(attribute)
  }

  private isProtectedHookCall(
    expression: ts.CallExpression,
    hook: ProtectedHookName,
  ): boolean {
    const callee = expression.expression
    if (ts.isIdentifier(callee)) {
      return this.reactHookLocals.get(callee.text) === hook
    }
    if (
      ts.isPropertyAccessExpression(callee) &&
      callee.name.text === hook &&
      ts.isIdentifier(callee.expression)
    ) {
      return this.reactNamespaceLocals.has(callee.expression.text)
    }
    return false
  }

  private isSuspenseTag(tagName: string): boolean {
    if (this.reactHookLocals.get(tagName) === 'Suspense') return true
    const parts = tagName.split('.')
    return (
      parts.length === 2 &&
      this.reactNamespaceLocals.has(parts[0]!) &&
      parts[1] === 'Suspense'
    )
  }

  private isFragmentTag(tagName: string): boolean {
    if (this.reactHookLocals.get(tagName) === 'Fragment') return true
    const parts = tagName.split('.')
    return (
      parts.length === 2 &&
      this.reactNamespaceLocals.has(parts[0]!) &&
      parts[1] === 'Fragment'
    )
  }

  private isContextProviderTag(tagName: string): boolean {
    const parts = tagName.split('.')
    return parts.length === 2 && parts[1] === 'Provider' && this.contextLocals.has(parts[0]!)
  }

  private isLazyComponent(tagName: string): boolean {
    // Bound via const X = lazy(...) where lazy is a protected import.
    // Tracked when the binding's initializer is a lazy() call — see collectLazyLocals.
    return this.lazyLocals.has(tagName)
  }

  private compileFragmentElement(
    attributes: ts.JsxAttributes,
    children: readonly ts.JsxChild[],
    environment: ExpressionEnvironment,
  ): PlanNode | undefined {
    for (const attribute of attributes.properties) {
      if (ts.isJsxSpreadAttribute(attribute)) {
        this.report(
          attribute,
          'GB1095',
          'Portable Fragment does not support spreads.',
          'Pass only key={…} on Fragment / React.Fragment.',
        )
        return undefined
      }
      const name = attribute.name.getText(this.sourceFile)
      if (name === 'key') continue
      this.report(
        attribute,
        'GB1095',
        `Portable Fragment only accepts key; found ${name}.`,
        'Move non-key props onto a real host element.',
      )
      return undefined
    }
    const compiled = this.compileJsxChildren(children, environment)
    return { kind: 'fragment', children: compiled }
  }

  private resolveStyleObjectLiteral(
    expression: ts.Expression,
    environment: ExpressionEnvironment,
  ): ts.ObjectLiteralExpression | undefined {
    if (ts.isObjectLiteralExpression(expression)) return expression
    if (ts.isIdentifier(expression)) {
      const fromScope = this.styleObjectScopes.at(-1)?.get(expression.text)
      if (fromScope) return fromScope
      const bound = environment.get(expression.text)
      if (
        bound?.kind === 'literal' &&
        bound.value !== null &&
        typeof bound.value === 'object' &&
        !Array.isArray(bound.value)
      ) {
        const statement = this.findVariableInitializer(expression.text)
        if (statement && ts.isObjectLiteralExpression(statement)) return statement
      }
    }
    return undefined
  }

  private compileStyleValue(
    expression: ts.Expression,
    environment: ExpressionEnvironment,
    reportNode: ts.Node,
  ): PlanExpression | undefined {
    const styleObject = this.resolveStyleObjectLiteral(
      this.unwrapExpression(expression),
      environment,
    )
    if (!styleObject) {
      this.report(
        reportNode,
        'GB1034',
        'Portable style values must be inline object literals or static style object locals.',
        'Use className or write style={{ property: value }} / const style = { … } in source order.',
      )
      return undefined
    }
    const arguments_: PlanExpression[] = []
    for (const property of styleObject.properties) {
      if (!ts.isPropertyAssignment(property)) {
        this.report(
          property,
          'GB1035',
          'Portable styles do not support spreads or computed entries.',
        )
        return undefined
      }
      const name = this.propertyNameText(property.name)
      const value = this.compileExpression(property.initializer, environment)
      if (name === undefined || !value) return undefined
      arguments_.push({ kind: 'literal', value: name }, value)
    }
    return { kind: 'helper', name: 'style', arguments: arguments_ }
  }

  private findVariableInitializer(name: string): ts.Expression | undefined {
    for (const statement of this.sourceFile.statements) {
      if (!ts.isVariableStatement(statement)) continue
      for (const declaration of statement.declarationList.declarations) {
        if (
          ts.isIdentifier(declaration.name) &&
          declaration.name.text === name &&
          declaration.initializer
        ) {
          return this.unwrapExpression(declaration.initializer)
        }
      }
    }
    return undefined
  }

  private compileSuspensePassthrough(
    _attributes: ts.JsxAttributes,
    children: readonly ts.JsxChild[],
    environment: ExpressionEnvironment,
  ): PlanNode {
    // Transparent Suspense: emit children only (sync portable trees). Not streaming.
    const compiled = this.compileJsxChildren(children, environment)
    return compiled.length === 1
      ? compiled[0]!
      : { kind: 'fragment', children: compiled }
  }

  private compileContextProvider(
    tagName: string,
    attributes: ts.JsxAttributes,
    children: readonly ts.JsxChild[],
    environment: ExpressionEnvironment,
  ): PlanNode | undefined {
    const contextName = tagName.split('.')[0]!
    const valueAttr = this.findAttribute(attributes, 'value')
    let value: PlanExpression | undefined
    if (valueAttr?.initializer && ts.isStringLiteral(valueAttr.initializer)) {
      value = { kind: 'literal', value: valueAttr.initializer.text }
    } else if (
      valueAttr?.initializer &&
      ts.isJsxExpression(valueAttr.initializer) &&
      valueAttr.initializer.expression
    ) {
      value = this.compileExpression(
        valueAttr.initializer.expression,
        environment,
      )
    }
    if (!value) {
      this.report(
        attributes,
        'GB1076',
        'Portable context Providers require a portable value={...} prop.',
      )
      return undefined
    }
    this.contextValueStack.push({ contextName, value })
    try {
      const compiled = this.compileJsxChildren(children, environment)
      return compiled.length === 1
        ? compiled[0]!
        : { kind: 'fragment', children: compiled }
    } finally {
      this.contextValueStack.pop()
    }
  }

  private compilePortableIntrinsic(
    expression: ts.CallExpression,
  ): PlanExpression | undefined {
    if (
      !ts.isPropertyAccessExpression(expression.expression) ||
      expression.arguments.length !== 0
    ) {
      return undefined
    }
    const receiver = expression.expression.expression
    if (
      !ts.isNewExpression(receiver) ||
      !ts.isIdentifier(receiver.expression) ||
      receiver.expression.text !== 'Date' ||
      (receiver.arguments?.length ?? 0) !== 0
    ) {
      return undefined
    }
    const definition = dateProjectionIntrinsics.get(expression.expression.name.text)
    if (!definition) return undefined
    const source = this.context.sourceName(this.fileName)
    const start = expression.getStart(this.sourceFile)
    const end = expression.getEnd()
    const location = this.sourceFile.getLineAndCharacterOfPosition(start)
    this.context.dateIntrinsicSites.push({
      routeId: this.context.routeId,
      source,
      start,
      end,
      line: location.line + 1,
      column: location.character + 1,
      getter: definition.getter,
    })
    return { kind: 'intrinsic', name: definition.name, arguments: [] }
  }

  private assertUnconditionalHook(
    expression: ts.CallExpression,
    hook: string,
  ): boolean {
    if (this.context.conditionalHookDepth > 0) {
      this.report(
        expression,
        'GB1085',
        `Protected hook ${hook} must be unconditional; conditionals and loops are not portable.`,
        'Call hooks at the top level of the component body. Parametric useId inside keyed .map is the only intentional exception.',
      )
      return false
    }
    if (this.eachKeyStack.length > 0 && hook !== 'useId') {
      this.report(
        expression,
        'GB1085',
        `Protected hook ${hook} inside .map is not portable.`,
        'Keep hooks outside the map callback, or use parametric useId for list identity.',
      )
      return false
    }
    return true
  }

  private compileProtectedUseMemo(
    expression: ts.CallExpression,
    environment: ExpressionEnvironment,
  ): PlanExpression | undefined {
    const factory = expression.arguments[0]
    if (
      !factory ||
      (!ts.isArrowFunction(factory) && !ts.isFunctionExpression(factory))
    ) {
      this.report(
        expression,
        'GB1076',
        'Portable useMemo requires an inline () => expr factory.',
      )
      return undefined
    }
    const body = this.portableFunctionBody(factory)
    if (!body) {
      this.report(
        factory,
        'GB1076',
        'Portable useMemo factory must be a single portable expression.',
      )
      return undefined
    }
    return this.compileExpression(body, environment)
  }

  private compileProtectedUseCallback(
    expression: ts.CallExpression,
    environment: ExpressionEnvironment,
  ): PlanExpression | undefined {
    const factory = expression.arguments[0]
    if (
      !factory ||
      (!ts.isArrowFunction(factory) && !ts.isFunctionExpression(factory))
    ) {
      this.report(
        expression,
        'GB1076',
        'Portable useCallback requires an inline () => expr factory.',
      )
      return undefined
    }
    const body = this.portableFunctionBody(factory)
    if (!body) {
      // Impure / multi-statement callbacks are fine when only passed to on*.
      // Binders use a null placeholder; reject when the callback value itself
      // must appear in portable markup.
      return undefined
    }
    return this.compileExpression(body, environment)
  }

  private compileProtectedUseContext(
    expression: ts.CallExpression,
  ): PlanExpression | undefined {
    const argument = expression.arguments[0]
    if (!argument || !ts.isIdentifier(argument)) {
      this.report(
        expression,
        'GB1076',
        'Portable useContext requires a createContext identifier from this module.',
      )
      return undefined
    }
    if (!this.contextLocals.has(argument.text)) {
      this.report(
        argument,
        'GB1076',
        `useContext(${argument.text}) is not a portable createContext binding in this module.`,
      )
      return undefined
    }
    for (let index = this.contextValueStack.length - 1; index >= 0; index -= 1) {
      const entry = this.contextValueStack[index]!
      if (entry.contextName === argument.text) return entry.value
    }
    this.report(
      expression,
      'GB1076',
      `No portable Provider value found for useContext(${argument.text}).`,
      'Wrap the consumer in <Ctx.Provider value={portable}> in the same portable tree.',
    )
    return undefined
  }

  private compileProtectedUseId(
    expression: ts.CallExpression,
  ): PlanExpression | undefined {
    if (expression.arguments.length !== 0) {
      this.report(
        expression,
        'GB1077',
        'Portable useId() takes no arguments in source; the Vite transform injects the stable id for hydration.',
        'Call useId() with no arguments. GoBeyond assigns a stable call-site id during compile.',
      )
      return undefined
    }
    if (this.context.conditionalHookDepth > 0) {
      this.report(
        expression,
        'GB1085',
        'Protected hook useId must be unconditional; conditionals and loops are not portable.',
        'Call useId at the top level of the component body, or inside a keyed .map callback / nested map child.',
      )
      return undefined
    }
    const nestedUnderMap = this.rejectUseIdFrames.at(-1) === true
    if (nestedUnderMap && this.eachKeyStack.length === 0) {
      this.report(
        expression,
        'GB1084',
        'useId() inside a nested component rendered from .map requires an enclosing keyed map.',
        'Ensure the map root has key={…}, or call useId in the map callback.',
      )
      return undefined
    }
    const source = this.context.sourceName(this.fileName)
    const start = expression.getStart(this.sourceFile)
    const end = expression.getEnd()
    const location = this.sourceFile.getLineAndCharacterOfPosition(start)
    // Occurrence is per route tree + span, but the id token is span-stable (no
    // routeId) so a shared module hydrates the same string on every route.
    const occurrence = this.context.useIdSites.filter(
      (site) =>
        site.routeId === this.context.routeId &&
        site.source === source &&
        site.start === start &&
        site.end === end,
    ).length
    const spanId = stableUseIdSpanToken(source, start, end)
    if (this.eachKeyStack.length > 0) {
      const prefix = `gb-${spanId}-${occurrence}-`
      let value: PlanExpression = { kind: 'literal', value: prefix }
      for (const [index, frame] of this.eachKeyStack.entries()) {
        if (index > 0) {
          value = {
            kind: 'binary',
            operator: '+',
            left: value,
            right: { kind: 'literal', value: '-' },
          }
        }
        value = {
          kind: 'binary',
          operator: '+',
          left: value,
          right: { kind: 'helper', name: 'string', arguments: [frame.key] },
        }
      }
      this.context.useIdSites.push({
        id: prefix,
        routeId: this.context.routeId,
        source,
        start,
        end,
        line: location.line + 1,
        column: location.character + 1,
        keyExpression: this.eachKeyStack
          .map((frame) => frame.keyText)
          .join(' + "-" + '),
        // Nested map inlines bake the parametric id into the plan; Vite must
        // not rewrite this instance (parent keyExpression is out of scope in
        // the child module). Map-callback useId keeps a normal rewrite site.
        ...(nestedUnderMap ? { skipViteRewrite: true } : {}),
      })
      return value
    }
    const id = `gb-${spanId}-${occurrence}`
    this.context.useIdSites.push({
      id,
      routeId: this.context.routeId,
      source,
      start,
      end,
      line: location.line + 1,
      column: location.character + 1,
    })
    return { kind: 'literal', value: id }
  }

  private collectProtectedReactHooks(): void {
    for (const statement of this.sourceFile.statements) {
      if (!ts.isImportDeclaration(statement) || !statement.importClause) continue
      if (!ts.isStringLiteral(statement.moduleSpecifier)) continue
      const specifier = statement.moduleSpecifier.text
      if (!isProtectedReactModule(specifier)) continue
      const clause = statement.importClause
      if (clause.name) this.reactNamespaceLocals.add(clause.name.text)
      const bindings = clause.namedBindings
      if (bindings && ts.isNamespaceImport(bindings)) {
        this.reactNamespaceLocals.add(bindings.name.text)
      }
      if (bindings && ts.isNamedImports(bindings)) {
        for (const element of bindings.elements) {
          const imported = (element.propertyName ?? element.name).text
          if (isProtectedHookName(imported)) {
            this.reactHookLocals.set(element.name.text, imported)
          }
        }
      }
    }
    this.collectLazyLocals()
  }

  private readonly lazyLocals = new Set<string>()

  private collectLazyLocals(): void {
    for (const statement of this.sourceFile.statements) {
      if (!ts.isVariableStatement(statement)) continue
      for (const declaration of statement.declarationList.declarations) {
        if (
          !ts.isIdentifier(declaration.name) ||
          !declaration.initializer ||
          !ts.isCallExpression(declaration.initializer)
        ) {
          continue
        }
        if (this.isProtectedHookCall(declaration.initializer, 'lazy')) {
          this.lazyLocals.add(declaration.name.text)
        }
      }
    }
  }

  private report(
    node: ts.Node,
    code: string,
    message: string,
    suggestion?: string,
  ): void {
    this.diagnostics.push(
      createDiagnostic(this.sourceFile, node, code, message, suggestion),
    )
  }
}

function sanitizeUseIdRoute(routeId: string): string {
  const cleaned = routeId.replace(/[^a-zA-Z0-9_-]+/g, '-').replace(/^-+|-+$/g, '')
  return cleaned.length > 0 ? cleaned : 'route'
}

/** Span-stable token so shared modules emit the same id on every route. */
function stableUseIdSpanToken(
  source: string,
  start: number,
  end: number,
): string {
  return createHash('sha256')
    .update(`${source}\0${start}\0${end}`)
    .digest('hex')
    .slice(0, 8)
}

/**
 * Decode HTML/XML character references the way JSX transformers do for text
 * and quoted attribute values. TypeScript's `JsxText.text` keeps the source
 * form (`&hellip;`); Vite/esbuild emit the decoded character (`…`).
 */
function decodeJsxEntities(value: string): string {
  return value.replace(
    /&(?:#x([0-9a-fA-F]+)|#([0-9]+)|([a-zA-Z][a-zA-Z0-9]*));/g,
    (match, hex: string | undefined, decimal: string | undefined, name: string | undefined) => {
      if (hex !== undefined) {
        const codePoint = Number.parseInt(hex, 16)
        return isUnicodeCodePoint(codePoint)
          ? String.fromCodePoint(codePoint)
          : match
      }
      if (decimal !== undefined) {
        const codePoint = Number.parseInt(decimal, 10)
        return isUnicodeCodePoint(codePoint)
          ? String.fromCodePoint(codePoint)
          : match
      }
      if (name !== undefined) {
        const named = JSX_NAMED_ENTITIES[name]
        if (named !== undefined) return named
      }
      return match
    },
  )
}

/** Named entities commonly emitted in JSX source; extend as mismatches appear. */
const JSX_NAMED_ENTITIES: Readonly<Record<string, string>> = {
  amp: '&',
  apos: "'",
  gt: '>',
  lt: '<',
  quot: '"',
  nbsp: '\u00A0',
  ensp: '\u2002',
  emsp: '\u2003',
  thinsp: '\u2009',
  ndash: '\u2013',
  mdash: '\u2014',
  lsquo: '\u2018',
  rsquo: '\u2019',
  ldquo: '\u201C',
  rdquo: '\u201D',
  bull: '\u2022',
  hellip: '\u2026',
  copy: '\u00A9',
  reg: '\u00AE',
  trade: '\u2122',
  deg: '\u00B0',
  times: '\u00D7',
  divide: '\u00F7',
}

function isUnicodeCodePoint(value: number): boolean {
  return Number.isInteger(value) && value >= 0 && value <= 0x10ffff
}

export function compileSource(options: CompileOptions): CompileResult {
  const context = standaloneContext()
  const compiler = new SourceCompiler(
    options.sourceText,
    options.fileName ?? 'page.tsx',
    context,
  )
  const plan = compiler.compile(options.routeId, options.componentName)
  if (!plan || compiler.diagnostics.length > 0) {
    return { ok: false, diagnostics: compiler.diagnostics }
  }
  return {
    ok: true,
    plan,
    clientBoundaries: context.clientBoundaries,
    useIdSites: context.useIdSites,
    dateIntrinsicSites: context.dateIntrinsicSites,
    diagnostics: [],
  }
}

export function compileSourceOrThrow(options: CompileOptions): RenderPlan {
  const result = compileSource(options)
  if (!result.ok) throw new PortableCompileError(result.diagnostics)
  return result.plan
}

function isDowngradeableDiagnostic(diagnostic: Diagnostic): boolean {
  const match = /^GB(\d{4})$/.exec(diagnostic.code)
  if (!match) return false
  const number = Number(match[1])
  if (number < 1010 || number > 1099) return false
  // These codes describe broken module/export resolution, not unsupported
  // render behavior, and must remain fatal even below a client directive.
  return ![1051, 1053, 1054, 1055].includes(number)
}

function mergeProperties(
  target: CompiledProperty[],
  additions: readonly CompiledProperty[],
): void {
  for (const addition of additions) {
    const existing = target.findIndex((property) => property.name === addition.name)
    if (existing !== -1) target.splice(existing, 1)
    target.push(addition)
  }
}

function hasClientDirective(sourceFile: ts.SourceFile): boolean {
  for (const statement of sourceFile.statements) {
    if (
      !ts.isExpressionStatement(statement) ||
      !ts.isStringLiteral(statement.expression)
    ) return false
    if (statement.expression.text === 'use client') return true
  }
  return false
}

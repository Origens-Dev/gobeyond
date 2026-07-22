import ts from 'typescript'

import { createDiagnostic, PortableCompileError } from './diagnostics.js'
import {
  RENDER_PLAN_API_VERSION,
  type Attribute,
  type CompileOptions,
  type CompileResult,
  type Diagnostic,
  type PlanExpression,
  type PlanNode,
  type RenderPlan,
} from './types.js'

type ExpressionEnvironment = Map<string, PlanExpression>
type NodeEnvironment = Map<string, PlanNode>
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
}

const helperNames = new Set(['string', 'lower', 'upper', 'join', 'url'])
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

export class SourceCompiler {
  readonly sourceFile: ts.SourceFile
  readonly checker: ts.TypeChecker
  readonly diagnostics: Diagnostic[] = []
  readonly components = new Map<string, Component>()
  readonly exportedComponents = new Set<string>()
  readonly imports = new Map<string, ComponentImport>()
  readonly nodeScopes: NodeEnvironment[] = []
  readonly namespaceScopes: Array<'html' | 'svg'> = ['html']

  constructor(
    readonly sourceText: string,
    readonly fileName: string,
    readonly context: CompilationContext = { componentStack: [] },
  ) {
    this.sourceFile = ts.createSourceFile(
      fileName,
      sourceText,
      ts.ScriptTarget.Latest,
      true,
      ts.ScriptKind.TSX,
    )
    this.checker = this.createTypeChecker()
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
    this.collectComponents()
  }

  private createTypeChecker(): ts.TypeChecker {
    const options: ts.CompilerOptions = {
      target: ts.ScriptTarget.ES2022,
      module: ts.ModuleKind.NodeNext,
      moduleResolution: ts.ModuleResolutionKind.NodeNext,
      jsx: ts.JsxEmit.ReactJSX,
      strict: true,
      noEmit: true,
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
    return ts.createProgram([this.fileName], options, host).getTypeChecker()
  }

  registerImport(localName: string, reference: ComponentImport): void {
    this.imports.set(localName, reference)
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
  ): { name: string; component: Component } | undefined {
    if (exportName === 'default') {
      const component = this.findDefaultComponent()
      return component
        ? { name: this.componentDisplayName(component), component }
        : undefined
    }
    if (!this.exportedComponents.has(exportName)) return undefined
    const component = this.components.get(exportName)
    return component ? { name: exportName, component } : undefined
  }

  compile(routeId: string, componentName?: string): RenderPlan | undefined {
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
    const root = this.compileComponent(name, component, new Map(), true)
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
    const root = this.compileComponent(
      name,
      component,
      new Map(),
      true,
      new Map([['children', child]]),
    )
    if (root) this.validateHydrationTree(root, component)
    if (!root || this.diagnostics.length > 0) return undefined
    return { apiVersion: RENDER_PLAN_API_VERSION, routeId, root }
  }

  private collectComponents(): void {
    for (const statement of this.sourceFile.statements) {
      if (ts.isFunctionDeclaration(statement) && statement.name) {
        this.components.set(statement.name.text, statement)
        if (this.hasModifier(statement, ts.SyntaxKind.ExportKeyword)) {
          this.exportedComponents.add(statement.name.text)
        }
        continue
      }
      if (!ts.isVariableStatement(statement)) continue
      const exported = this.hasModifier(statement, ts.SyntaxKind.ExportKeyword)
      for (const declaration of statement.declarationList.declarations) {
        if (
          ts.isIdentifier(declaration.name) &&
          declaration.initializer &&
          (ts.isArrowFunction(declaration.initializer) ||
            ts.isFunctionExpression(declaration.initializer))
        ) {
          this.components.set(declaration.name.text, declaration.initializer)
          if (exported) this.exportedComponents.add(declaration.name.text)
        }
      }
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
        !statement.isExportEquals &&
        ts.isIdentifier(statement.expression)
      ) {
        return this.components.get(statement.expression.text)
      }
    }
    return undefined
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
      const environment = new Map<string, PlanExpression>()
      const parameter = componentParameter
      if (parameter) {
        this.bindComponentParameter(
          parameter.name,
          suppliedProps,
          environment,
          rootComponent,
        )
      }
      if (component.parameters.length > 1) {
        this.report(
          component.parameters[1]!,
          'GB1011',
          'Portable function components accept a single props parameter.',
        )
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
      this.context.componentStack.pop()
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
        this.report(element, 'GB1016', 'Rest props are not portable.')
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
      if (!supplied && !rootComponent && element.initializer) {
        this.report(
          element.initializer,
          'GB1018',
          'Default values on nested component props are not portable yet.',
          'Pass the value explicitly from the parent component or calculate it in Go.',
        )
      }
      environment.set(
        element.name.text,
        supplied ??
          (rootComponent
            ? { kind: 'path', path: [propertyName] }
            : { kind: 'literal', value: null }),
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
        ts.isCallExpression(declaration.initializer) &&
        ts.isIdentifier(declaration.initializer.expression) &&
        declaration.initializer.expression.text === 'useState'
      ) {
        this.compileUseState(declaration, environment)
        continue
      }
      if (!ts.isIdentifier(declaration.name)) {
        this.report(
          declaration.name,
          'GB1021',
          'Only identifier local bindings and useState tuples are portable.',
        )
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
      this.report(
        initializer,
        'GB1023',
        'Lazy useState initializers are not portable in the MVP.',
        'Use a literal/prop-derived initial value, or calculate it in Go and pass it as a prop.',
      )
      return
    }
    const value = this.compileExpression(initializer, environment)
    if (value) environment.set(state.name.text, value)
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
    if (this.isIntrinsicTag(tagName)) {
      return this.compileIntrinsic(
        tagName,
        element.openingElement.attributes,
        element.children,
        environment,
      )
    }
    return this.compileLocalComponent(
      tagName,
      element.openingElement.attributes,
      element.children,
      environment,
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
    if (this.isIntrinsicTag(tagName)) {
      return this.compileIntrinsic(tagName, element.attributes, [], environment)
    }
    return this.compileLocalComponent(
      tagName,
      element.attributes,
      [],
      environment,
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
        this.report(
          attribute,
          'GB1030',
          'JSX spread attributes are not portable.',
          'Write each server-rendered attribute explicitly.',
        )
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
      const mode = booleanAttributes.has(name)
        ? 'boolean'
        : urlAttributes.has(name)
          ? 'url'
          : name === 'style'
            ? 'style'
            : 'string'
      compiledAttributes.push({ name, value, mode })
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
      return { kind: 'literal', value: attribute.initializer.text }
    }
    if (
      !ts.isJsxExpression(attribute.initializer) ||
      !attribute.initializer.expression
    ) {
      this.report(attribute, 'GB1032', 'Unsupported JSX attribute initializer.')
      return undefined
    }
    if (attribute.name.getText(this.sourceFile) === 'style') {
      const expression = this.unwrapExpression(attribute.initializer.expression)
      if (!ts.isObjectLiteralExpression(expression)) {
        this.report(
          attribute,
          'GB1034',
          'Portable style values must be inline object literals.',
          'Use className or write style={{ property: value }} in source order.',
        )
        return undefined
      }
      const arguments_: PlanExpression[] = []
      for (const property of expression.properties) {
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
    return result
  }

  private compileClientOnly(
    attributes: ts.JsxAttributes,
    children: readonly ts.JsxChild[],
    environment: ExpressionEnvironment,
  ): PlanNode | undefined {
    const fallback = this.findAttribute(attributes, 'fallback')
    if (!fallback?.initializer || !ts.isJsxExpression(fallback.initializer)) {
      this.report(
        fallback ?? attributes,
        'GB1040',
        'ClientOnly requires a JSX fallback attribute.',
        'Provide deterministic, SEO-safe markup: <ClientOnly fallback={<Placeholder />}>…</ClientOnly>.',
      )
      return undefined
    }
    if (!fallback.initializer.expression) {
      this.report(fallback, 'GB1041', 'ClientOnly fallback cannot be empty.')
      return undefined
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
        this.report(
          attribute,
          'GB1052',
          'Spread props on local components are not portable.',
        )
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
        attributes.parent,
        'GB1051',
        `Component ${name} is not a project-local function component.`,
        'Declare or import it from a configured project source root, or wrap browser-only markup in ClientOnly.',
      )
      return undefined
    }
    if (imported.kind === 'package') {
      this.report(
        attributes.parent,
        'GB1053',
        `Package component ${name} from ${JSON.stringify(imported.specifier)} cannot participate in Go-rendered markup.`,
        'Wrap the package component in ClientOnly with a deterministic fallback.',
      )
      return undefined
    }
    if (imported.kind === 'unresolved') {
      this.report(
        attributes.parent,
        'GB1054',
        `Cannot resolve component ${name} from ${JSON.stringify(imported.specifier)}: ${imported.reason}`,
        'Fix the import path or add an explicit source-root alias.',
      )
      return undefined
    }
    const target = imported.compiler.resolveExport(imported.exportName)
    if (!target) {
      this.report(
        attributes.parent,
        'GB1055',
        `${JSON.stringify(imported.specifier)} does not export portable function component ${JSON.stringify(imported.exportName)}.`,
        'Export a project-owned function component or wrap the component in ClientOnly.',
      )
      return undefined
    }
    const inheritedNamespace =
      this.namespaceScopes[this.namespaceScopes.length - 1] ?? 'html'
    imported.compiler.namespaceScopes.push(inheritedNamespace)
    try {
      return imported.compiler.compileComponent(
        target.name,
        target.component,
        supplied,
        false,
        suppliedNodes,
      )
    } finally {
      imported.compiler.namespaceScopes.pop()
    }
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
      if (callback.body.statements.length !== 1 || !returns[0]?.expression) {
        this.report(
          callback.body,
          'GB1063',
          'Portable .map callbacks must directly return one JSX expression.',
        )
        return undefined
      }
      bodyExpression = returns[0].expression
    } else {
      bodyExpression = callback.body
    }
    const keyNode = this.unwrapExpression(bodyExpression)
    const keyAttribute = this.getRootKeyAttribute(keyNode)
    if (
      !keyAttribute?.initializer ||
      !ts.isJsxExpression(keyAttribute.initializer) ||
      !keyAttribute.initializer.expression
    ) {
      this.report(
        bodyExpression,
        'GB1064',
        'Portable .map output requires an explicit expression key on its root element.',
        'Use a stable data key such as key={item.id}; array indexes are discouraged for hydration identity.',
      )
      return undefined
    }
    const key = this.compileExpression(
      keyAttribute.initializer.expression,
      callbackEnvironment,
    )
    const body = this.compileNodeExpression(bodyExpression, callbackEnvironment)
    if (!key || !body) return undefined
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
    const attributes = ts.isJsxElement(expression)
      ? expression.openingElement.attributes
      : ts.isJsxSelfClosingElement(expression)
        ? expression.attributes
        : undefined
    return attributes ? this.findAttribute(attributes, 'key') : undefined
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
      this.report(
        expression,
        'GB1070',
        `Identifier ${expression.text} is not a portable prop, item, state initializer, or local constant.`,
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
      const left = this.compileExpression(expression.left, environment)
      const right = this.compileExpression(expression.right, environment)
      if (
        left &&
        right &&
        (operator === '==' || operator === '!=') &&
        (!this.isPortableScalarEqualityOperand(expression.left, left) ||
          !this.isPortableScalarEqualityOperand(expression.right, right))
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
          name: helperName as 'string' | 'lower' | 'upper' | 'join' | 'url',
          arguments: args as PlanExpression[],
        }
      }
      const callName = expression.expression.getText(this.sourceFile)
      const knownDeferred = new Set([
        'useContext',
        'useId',
        'useLayoutEffect',
        'useMemo',
        'useReducer',
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
  ): boolean {
    if (compiled.kind === 'literal') {
      return (
        compiled.value === null ||
        typeof compiled.value === 'string' ||
        typeof compiled.value === 'number' ||
        typeof compiled.value === 'boolean'
      )
    }
    if (compiled.kind === 'unary') return true
    if (compiled.kind === 'helper') return compiled.name !== 'style'
    if (compiled.kind === 'binary') {
      return !['&&', '||', '??'].includes(compiled.operator)
    }
    return this.isPortableScalarType(this.checker.getTypeAtLocation(expression))
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
      else if (node.kind === 'clientOnly') visit(node.fallback, ancestors)
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
        return this.planCanYieldTag(node.fallback, tag)
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

export function compileSource(options: CompileOptions): CompileResult {
  const compiler = new SourceCompiler(
    options.sourceText,
    options.fileName ?? 'page.tsx',
  )
  const plan = compiler.compile(options.routeId, options.componentName)
  if (!plan || compiler.diagnostics.length > 0) {
    return { ok: false, diagnostics: compiler.diagnostics }
  }
  return { ok: true, plan, diagnostics: [] }
}

export function compileSourceOrThrow(options: CompileOptions): RenderPlan {
  const result = compileSource(options)
  if (!result.ok) throw new PortableCompileError(result.diagnostics)
  return result.plan
}

import ts from 'typescript'

import { createDiagnostic } from './diagnostics.js'
import type {
  ActionContractCompileResult,
  ActionValueContract,
  ContractSourceOptions,
  Diagnostic,
  PageContractCompileResult,
  RouteValueContract,
  ValueSchema,
} from './types.js'

const primitiveKinds = new Map<string, ValueSchema['kind']>([
  ['string', 'string'],
  ['number', 'number'],
  ['integer', 'integer'],
  ['boolean', 'boolean'],
  ['datetime', 'datetime'],
  ['bytes', 'bytes'],
  ['safeHTML', 'safeHtml'],
] as const)

class ContractCompiler {
  readonly sourceFile: ts.SourceFile
  readonly diagnostics: Diagnostic[] = []
  readonly schemaAliases = new Set(['schema'])
  readonly definePageAliases = new Set(['definePage'])
  readonly defineActionAliases = new Set(['defineAction'])

  constructor(sourceText: string, fileName: string) {
    this.sourceFile = ts.createSourceFile(
      fileName,
      sourceText,
      ts.ScriptTarget.Latest,
      true,
      ts.ScriptKind.TS,
    )
    const parseDiagnostics = (
      this.sourceFile as ts.SourceFile & {
        parseDiagnostics?: readonly ts.DiagnosticWithLocation[]
      }
    ).parseDiagnostics
    for (const diagnostic of parseDiagnostics ?? []) {
      const location = this.sourceFile.getLineAndCharacterOfPosition(diagnostic.start)
      this.diagnostics.push({
        code: `TS${diagnostic.code}`,
        message: ts.flattenDiagnosticMessageText(diagnostic.messageText, '\n'),
        suggestion: 'Fix the schema TypeScript syntax before generating value contracts.',
        fileName,
        start: diagnostic.start,
        length: diagnostic.length,
        line: location.line + 1,
        column: location.character + 1,
      })
    }
    this.collectAliases()
  }

  compilePage(routeId: string): RouteValueContract | undefined {
    const definitions = this.exportedCalls(this.definePageAliases)
    if (definitions.length !== 1) {
      this.report(
        this.sourceFile,
        'GB1200',
        `Expected exactly one exported definePage declaration, found ${definitions.length}.`,
      )
      return undefined
    }
    const definition = this.requireObject(definitions[0]!.call.arguments[0], 'definePage')
    if (!definition) return undefined
    const props = this.objectProperty(definition, 'props')
    if (!props) {
      this.report(definition, 'GB1201', 'definePage requires a props schema.')
      return undefined
    }
    const schema = this.compileSchema(props)
    return schema ? { routeId, props: schema } : undefined
  }

  compileActions(routeId: string): ActionValueContract[] {
    const contracts: ActionValueContract[] = []
    for (const definition of this.exportedCalls(this.defineActionAliases)) {
      const object = this.requireObject(definition.call.arguments[0], 'defineAction')
      if (!object) continue
      const inputNode = this.objectProperty(object, 'input')
      const outputNode = this.objectProperty(object, 'output')
      if (!inputNode || !outputNode) {
        this.report(object, 'GB1202', 'defineAction requires input and output schemas.')
        continue
      }
      const input = this.compileSchema(inputNode)
      const output = this.compileSchema(outputNode)
      if (input && output) {
        contracts.push({
          actionId: `${routeId}:${definition.exportName}`,
          input,
          output,
        })
      }
    }
    return contracts
  }

  private collectAliases(): void {
    for (const statement of this.sourceFile.statements) {
      if (
        !ts.isImportDeclaration(statement) ||
        !ts.isStringLiteral(statement.moduleSpecifier) ||
        statement.moduleSpecifier.text !== '@gobeyond/schema' ||
        !statement.importClause?.namedBindings ||
        !ts.isNamedImports(statement.importClause.namedBindings)
      ) continue
      for (const element of statement.importClause.namedBindings.elements) {
        const importedName = element.propertyName?.text ?? element.name.text
        if (importedName === 'schema') this.schemaAliases.add(element.name.text)
        if (importedName === 'definePage') this.definePageAliases.add(element.name.text)
        if (importedName === 'defineAction') this.defineActionAliases.add(element.name.text)
      }
    }
  }

  private exportedCalls(allowedNames: Set<string>): Array<{
    exportName: string
    call: ts.CallExpression
  }> {
    const result: Array<{ exportName: string; call: ts.CallExpression }> = []
    for (const statement of this.sourceFile.statements) {
      if (!ts.isVariableStatement(statement) || !this.isExported(statement)) continue
      for (const declaration of statement.declarationList.declarations) {
        if (
          ts.isIdentifier(declaration.name) &&
          declaration.initializer &&
          ts.isCallExpression(declaration.initializer) &&
          ts.isIdentifier(declaration.initializer.expression) &&
          allowedNames.has(declaration.initializer.expression.text)
        ) {
          result.push({ exportName: declaration.name.text, call: declaration.initializer })
        }
      }
    }
    return result
  }

  private compileSchema(node: ts.Expression): ValueSchema | undefined {
    node = unwrap(node)
    if (
      !ts.isCallExpression(node) ||
      !ts.isPropertyAccessExpression(node.expression) ||
      !ts.isIdentifier(node.expression.expression) ||
      !this.schemaAliases.has(node.expression.expression.text)
    ) {
      this.report(
        node,
        'GB1210',
        'Value contracts must use direct @gobeyond/schema descriptor calls.',
        'Use schema.object, schema.string, and the other documented schema primitives without helper execution.',
      )
      return undefined
    }
    const method = node.expression.name.text
    const primitive = primitiveKinds.get(method)
    if (primitive) {
      if (node.arguments.length !== 0) {
        this.report(node, 'GB1211', `schema.${method} does not accept arguments.`)
        return undefined
      }
      return { kind: primitive }
    }
    if (method === 'literal') {
      const value = node.arguments[0] && this.literalValue(node.arguments[0])
      if (node.arguments.length !== 1 || value === undefined) {
        this.report(node, 'GB1212', 'schema.literal requires one string, number, boolean, or null literal.')
        return undefined
      }
      return { kind: 'literal', value }
    }
    if (method === 'enum') {
      const valuesNode = node.arguments[0]
      if (!valuesNode || !ts.isArrayLiteralExpression(valuesNode) || valuesNode.elements.length === 0) {
        this.report(node, 'GB1213', 'schema.enum requires a non-empty literal string array.')
        return undefined
      }
      const values: string[] = []
      for (const element of valuesNode.elements) {
        if (!ts.isStringLiteral(element)) {
          this.report(element, 'GB1214', 'Enum values must be string literals.')
          return undefined
        }
        values.push(element.text)
      }
      if (new Set(values).size !== values.length) {
        this.report(valuesNode, 'GB1215', 'Enum values must be unique.')
        return undefined
      }
      return { kind: 'enum', values }
    }
    if (method === 'array') {
      const itemNode = node.arguments[0]
      if (!itemNode || node.arguments.length !== 1) {
        this.report(node, 'GB1216', 'schema.array requires exactly one item schema.')
        return undefined
      }
      const items = this.compileSchema(itemNode)
      return items ? { kind: 'array', items } : undefined
    }
    if (method === 'object') {
      const shapeNode = this.requireObject(node.arguments[0], 'schema.object')
      if (!shapeNode || node.arguments.length !== 1) return undefined
      const shape: Record<string, ValueSchema> = {}
      for (const property of shapeNode.properties) {
        if (!ts.isPropertyAssignment(property)) {
          this.report(property, 'GB1217', 'Schema object shapes cannot use spreads or shorthand properties.')
          return undefined
        }
        const name = propertyName(property.name)
        if (name === undefined) {
          this.report(property.name, 'GB1218', 'Schema object keys must be identifiers or literals.')
          return undefined
        }
        const value = this.compileSchema(property.initializer)
        if (!value) return undefined
        shape[name] = value
      }
      return { kind: 'object', shape }
    }
    if (method === 'optional' || method === 'nullable') {
      const innerNode = node.arguments[0]
      if (!innerNode || node.arguments.length !== 1) {
        this.report(node, 'GB1219', `schema.${method} requires exactly one inner schema.`)
        return undefined
      }
      const inner = this.compileSchema(innerNode)
      if (!inner) return undefined
      return method === 'optional'
        ? { ...inner, optional: true }
        : { ...inner, nullable: true }
    }
    if (method === 'union') {
      const variantsNode = node.arguments[0]
      if (!variantsNode || !ts.isArrayLiteralExpression(variantsNode) || variantsNode.elements.length < 2) {
        this.report(node, 'GB1220', 'schema.union requires an array containing at least two schemas.')
        return undefined
      }
      const variants: ValueSchema[] = []
      for (const variantNode of variantsNode.elements) {
        const variant = this.compileSchema(variantNode)
        if (!variant) return undefined
        variants.push(variant)
      }
      return { kind: 'union', variants }
    }
    this.report(
      node.expression.name,
      'GB1221',
      `Unsupported schema descriptor schema.${method}.`,
    )
    return undefined
  }

  private literalValue(node: ts.Expression): string | number | boolean | null | undefined {
    node = unwrap(node)
    if (ts.isStringLiteral(node) || ts.isNoSubstitutionTemplateLiteral(node)) return node.text
    if (ts.isNumericLiteral(node)) return Number(node.text)
    if (node.kind === ts.SyntaxKind.TrueKeyword) return true
    if (node.kind === ts.SyntaxKind.FalseKeyword) return false
    if (node.kind === ts.SyntaxKind.NullKeyword) return null
    if (
      ts.isPrefixUnaryExpression(node) &&
      node.operator === ts.SyntaxKind.MinusToken &&
      ts.isNumericLiteral(node.operand)
    ) return -Number(node.operand.text)
    return undefined
  }

  private requireObject(
    node: ts.Expression | undefined,
    owner: string,
  ): ts.ObjectLiteralExpression | undefined {
    if (!node || !ts.isObjectLiteralExpression(node)) {
      this.report(node ?? this.sourceFile, 'GB1222', `${owner} requires an inline object literal.`)
      return undefined
    }
    return node
  }

  private objectProperty(
    object: ts.ObjectLiteralExpression,
    selectedName: string,
  ): ts.Expression | undefined {
    for (const property of object.properties) {
      if (ts.isPropertyAssignment(property) && propertyName(property.name) === selectedName) {
        return property.initializer
      }
    }
    return undefined
  }

  private isExported(node: ts.Node): boolean {
    return !!ts.getModifiers(node as ts.HasModifiers)?.some(
      (modifier) => modifier.kind === ts.SyntaxKind.ExportKeyword,
    )
  }

  private report(
    node: ts.Node,
    code: string,
    message: string,
    suggestion?: string,
  ): void {
    this.diagnostics.push(createDiagnostic(this.sourceFile, node, code, message, suggestion))
  }
}

export function compilePageContractSource(
  options: ContractSourceOptions,
): PageContractCompileResult {
  const compiler = new ContractCompiler(options.sourceText, options.fileName ?? 'page.schema.ts')
  const contract = compiler.compilePage(options.routeId)
  if (!contract || compiler.diagnostics.length > 0) {
    return { ok: false, diagnostics: compiler.diagnostics }
  }
  return { ok: true, contract, diagnostics: [] }
}

export function compileActionContractSource(
  options: ContractSourceOptions,
): ActionContractCompileResult {
  const compiler = new ContractCompiler(options.sourceText, options.fileName ?? 'actions.ts')
  const contracts = compiler.compileActions(options.routeId)
  if (compiler.diagnostics.length > 0) {
    return { ok: false, diagnostics: compiler.diagnostics }
  }
  return { ok: true, contracts, diagnostics: [] }
}

function unwrap(node: ts.Expression): ts.Expression {
  while (
    ts.isParenthesizedExpression(node) ||
    ts.isAsExpression(node) ||
    ts.isSatisfiesExpression(node)
  ) node = node.expression
  return node
}

function propertyName(name: ts.PropertyName): string | undefined {
  if (ts.isIdentifier(name) || ts.isStringLiteral(name) || ts.isNumericLiteral(name)) {
    return name.text
  }
  return undefined
}

import ts from 'typescript'

import type { Diagnostic } from './types.js'

export class PortableCompileError extends Error {
  readonly diagnostics: Diagnostic[]

  constructor(diagnostics: Diagnostic[]) {
    super(formatDiagnostics(diagnostics))
    this.name = 'PortableCompileError'
    this.diagnostics = diagnostics
  }
}

export function createDiagnostic(
  sourceFile: ts.SourceFile,
  node: ts.Node,
  code: string,
  message: string,
  suggestion?: string,
): Diagnostic {
  const start = node.getStart(sourceFile)
  const location = sourceFile.getLineAndCharacterOfPosition(start)
  const base = {
    code,
    message,
    fileName: sourceFile.fileName,
    start,
    length: Math.max(1, node.getEnd() - start),
    line: location.line + 1,
    column: location.character + 1,
  }
  return suggestion === undefined ? base : { ...base, suggestion }
}

export function formatDiagnostic(diagnostic: Diagnostic): string {
  const location = `${diagnostic.fileName}:${diagnostic.line}:${diagnostic.column}`
  const suggestion = diagnostic.suggestion
    ? `\n  suggestion: ${diagnostic.suggestion}`
    : ''
  return `${location} [${diagnostic.code}] ${diagnostic.message}${suggestion}`
}

export function formatDiagnostics(diagnostics: readonly Diagnostic[]): string {
  return diagnostics.map(formatDiagnostic).join('\n')
}

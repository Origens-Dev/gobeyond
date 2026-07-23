import { readFile } from 'node:fs/promises'
import { isAbsolute, resolve } from 'node:path'
import type {
  ClientBoundaryManifest,
  ClientBoundaryRecord,
} from '@gobeyond/compiler'
import type { Plugin, ResolvedConfig } from 'vite'

export type GoBeyondViteOptions = {
  /** A manifest object or JSON path. Defaults to GOBEYOND_CLIENT_BOUNDARIES. */
  clientBoundaries?: ClientBoundaryManifest | string
}

type ProjectCompilerOutput = {
  clientBoundaries?: ClientBoundaryManifest
}

export function goBeyond(options: GoBeyondViteOptions = {}): Plugin {
  let config: ResolvedConfig | undefined
  let manifest: ClientBoundaryManifest | undefined
  return {
    name: 'gobeyond-client-boundaries',
    enforce: 'pre',
    configResolved(resolvedConfig) {
      config = resolvedConfig
    },
    async buildStart() {
      if (!config) throw new Error('GoBeyond Vite plugin was not configured.')
      manifest = await loadClientBoundaryManifest(
        options.clientBoundaries ?? process.env.GOBEYOND_CLIENT_BOUNDARIES,
        config.root,
      )
    },
    transform(code, id) {
      if (!config || !manifest) return null
      return transformClientBoundaries(code, id, manifest.boundaries, config.root)
    },
  }
}

export default goBeyond

export async function loadClientBoundaryManifest(
  input: ClientBoundaryManifest | string | undefined,
  root: string,
): Promise<ClientBoundaryManifest> {
  if (!input) {
    return {
      apiVersion: 'gobeyond.client-boundaries/v1alpha1',
      boundaries: [],
    }
  }
  const value: unknown = typeof input === 'string'
    ? JSON.parse(await readFile(
        isAbsolute(input) ? input : resolve(root, input),
        'utf8',
      ))
    : input
  const candidate = value as ClientBoundaryManifest & ProjectCompilerOutput
  const manifest = candidate.clientBoundaries ?? candidate
  if (manifest.apiVersion !== 'gobeyond.client-boundaries/v1alpha1') {
    throw new Error(
      `Unsupported GoBeyond client-boundary manifest: ${String(manifest.apiVersion)}`,
    )
  }
  if (!Array.isArray(manifest.boundaries)) {
    throw new Error('GoBeyond client-boundary manifest requires a boundaries array.')
  }
  return manifest
}

export function transformClientBoundaries(
  code: string,
  id: string,
  boundaries: readonly ClientBoundaryRecord[],
  root: string,
): { code: string; map: null } | null {
  const fileName = cleanModuleID(id)
  const matching = deduplicateBoundaries(boundaries.filter(
    (boundary) => resolve(root, boundary.source) === fileName,
  ))
  if (matching.length === 0) return null

  const replacements: Array<{ start: number; end: number; text: string }> = []
  let needsClientOnly = false
  let needsCreateElement = false
  let needsDeferredRoot = false
  for (const boundary of matching) {
    if (boundary.target === 'callSite') {
      const selected = code.slice(boundary.start, boundary.end)
      if (!isExpectedCallSite(selected, boundary.component)) {
        throw staleBoundaryError(boundary)
      }
      const jsxCallSite = isJSXCallSite(selected)
      replacements.push({
        start: boundary.start,
        end: boundary.end,
        // A bare JavaScript call inserted between JSX tags is parsed as text.
        // Keep JSX call sites as JSX so this replacement is valid both as a
        // direct child and in an existing expression/return position.
        text: jsxCallSite
          ? `<__gbClientOnly>{${selected}}</__gbClientOnly>`
          : `__gbCreateElement(__gbClientOnly, null, ${selected})`,
      })
      needsClientOnly = true
      needsCreateElement ||= !jsxCallSite
      continue
    }
    const rootReplacement = deferredRootReplacement(code, boundary)
    if (!rootReplacement) throw staleBoundaryError(boundary)
    replacements.push(...rootReplacement)
    needsDeferredRoot = true
  }
  assertNonOverlapping(replacements, fileName)
  replacements.sort((left, right) => right.start - left.start)
  let transformed = code
  for (const replacement of replacements) {
    transformed =
      transformed.slice(0, replacement.start) +
      replacement.text +
      transformed.slice(replacement.end)
  }

  const imports = [
    needsCreateElement
      ? `import { createElement as __gbCreateElement } from 'react';`
      : '',
    needsClientOnly || needsDeferredRoot
      ? `import { ${needsClientOnly ? 'ClientOnly as __gbClientOnly' : ''}${needsClientOnly && needsDeferredRoot ? ', ' : ''}${needsDeferredRoot ? 'deferClientRender as __gbDeferClientRender' : ''} } from '@gobeyond/react';`
      : '',
  ].filter(Boolean).join('\n')
  const insertion = directivePrologueEnd(code)
  transformed =
    transformed.slice(0, insertion) +
    `${insertion === 0 ? '' : '\n'}${imports}\n` +
    transformed.slice(insertion)
  return { code: transformed, map: null }
}

function deferredRootReplacement(
  code: string,
  boundary: ClientBoundaryRecord,
): Array<{ start: number; end: number; text: string }> | undefined {
  const selected = code.slice(boundary.start, boundary.end)
  const declaration = new RegExp(
    `^export\\s+default\\s+(?:async\\s+)?function\\s+${escapeRegExp(boundary.component)}\\b`,
  ).exec(selected)
  if (declaration) {
    const exportPrefix = /^export\s+default\s+/.exec(selected)
    if (!exportPrefix) return undefined
    return [
      {
        start: boundary.start,
        end: boundary.start + exportPrefix[0].length,
        text: '',
      },
      {
        start: boundary.end,
        end: boundary.end,
        text: `\nexport default __gbDeferClientRender(${boundary.component});`,
      },
    ]
  }
  if (
    boundary.component === 'default' &&
    /^export\s+default\s+(?:async\s+)?function\b/.test(selected)
  ) {
    const prefix = /^export\s+default\s+((?:async\s+)?function\b)/.exec(selected)
    if (!prefix) return undefined
    return [
      {
        start: boundary.start,
        end: boundary.start + prefix[0].length,
        text: `const __gbDefaultComponent = ${prefix[1]}`,
      },
      {
        start: boundary.end,
        end: boundary.end,
        text: '\nexport default __gbDeferClientRender(__gbDefaultComponent);',
      },
    ]
  }
  const exportPattern = new RegExp(
    `\\bexport\\s+default\\s+${escapeRegExp(boundary.component)}\\s*;?`,
    'g',
  )
  const matches = [...code.matchAll(exportPattern)]
  if (matches.length !== 1 || matches[0]?.index === undefined) return undefined
  const match = matches[0]
  const expressionOffset = match[0].indexOf(boundary.component)
  const start = match.index + expressionOffset
  return [{
    start,
    end: start + boundary.component.length,
    text: `__gbDeferClientRender(${boundary.component})`,
  }]
}

function isExpectedCallSite(selected: string, component: string): boolean {
  const escaped = escapeRegExp(component)
  return new RegExp(`^<\\s*${escaped}(?:[\\s/>])`).test(selected) ||
    new RegExp(`(?:jsx|jsxs|jsxDEV|createElement)\\s*\\(\\s*${escaped}(?:\\s*[,)]|$)`).test(selected)
}

function isJSXCallSite(selected: string): boolean {
  return /^\s*</.test(selected)
}

function directivePrologueEnd(code: string): number {
  const match = /^(?:\s*["'][^"'\r\n]+["']\s*;?)+/.exec(code)
  return match?.[0].length ?? 0
}

function deduplicateBoundaries(
  boundaries: readonly ClientBoundaryRecord[],
): ClientBoundaryRecord[] {
  const seen = new Set<string>()
  return boundaries.filter((boundary) => {
    const key = `${boundary.source}:${boundary.start}:${boundary.end}:${boundary.target}`
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

function assertNonOverlapping(
  replacements: readonly { start: number; end: number }[],
  fileName: string,
): void {
  const sorted = [...replacements].sort((left, right) => left.start - right.start)
  for (let index = 1; index < sorted.length; index += 1) {
    const previous = sorted[index - 1]!
    const current = sorted[index]!
    if (current.start < previous.end) {
      throw new Error(`Overlapping GoBeyond client boundaries in ${fileName}.`)
    }
  }
}

function staleBoundaryError(boundary: ClientBoundaryRecord): Error {
  return new Error(
    `Stale GoBeyond client boundary ${boundary.id} at ${boundary.source}:${boundary.line}:${boundary.column}. Re-run the compiler.`,
  )
}

function cleanModuleID(id: string): string {
  return resolve(id.split(/[?#]/, 1)[0]!)
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

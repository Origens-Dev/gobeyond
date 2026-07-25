import { realpathSync } from 'node:fs'
import { readFile } from 'node:fs/promises'
import { isAbsolute, resolve } from 'node:path'
import type {
  ClientBoundaryManifest,
  ClientBoundaryRecord,
  DateIntrinsicSiteRecord,
  UseIdSiteRecord,
} from '@go-beyond/compiler'
import type { Plugin, ResolvedConfig } from 'vite'

export type GoBeyondViteOptions = {
  /** A manifest object or JSON path. Defaults to GOBEYOND_CLIENT_BOUNDARIES. */
  clientBoundaries?: ClientBoundaryManifest | string
}

type ProjectCompilerOutput = {
  clientBoundaries?: ClientBoundaryManifest
}

type TextReplacement = { start: number; end: number; text: string }

/**
 * Vite site kinds mirrored from the compiler protected-API registry:
 * - useId → __gbUseId / sequence factory
 * - dateIntrinsic → renderSnapshotDate().getter()
 */
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
      const useIdSites = (manifest.useIdSites ?? []).filter(
        (site) => !site.skipViteRewrite,
      )
      // useId first: its spans are compiler offsets into the original source.
      const useIds = transformUseIdSites(code, id, useIdSites, config.root)
      const afterUseId = useIds?.code ?? code
      const shiftedDates = useIds
        ? shiftDateSitesAfterUseId(
            code,
            manifest.dateIntrinsicSites ?? [],
            useIdSites,
            config.root,
            id,
          )
        : (manifest.dateIntrinsicSites ?? [])
      const dates = transformDateIntrinsicSites(
        afterUseId,
        id,
        shiftedDates,
        config.root,
      )
      const afterDate = dates?.code ?? afterUseId
      const boundaries = transformClientBoundaries(
        afterDate,
        id,
        useIds || dates
          ? shiftBoundariesAfterRewrites(
              code,
              manifest.boundaries,
              useIdSites,
              manifest.dateIntrinsicSites ?? [],
              config.root,
              id,
              Boolean(useIds),
              Boolean(dates),
            )
          : manifest.boundaries,
        config.root,
      )
      return boundaries ?? dates ?? useIds
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
      useIdSites: [],
      dateIntrinsicSites: [],
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
  return {
    ...manifest,
    useIdSites: Array.isArray(manifest.useIdSites) ? manifest.useIdSites : [],
    dateIntrinsicSites: Array.isArray(manifest.dateIntrinsicSites)
      ? manifest.dateIntrinsicSites
      : [],
  }
}

export function transformClientBoundaries(
  code: string,
  id: string,
  boundaries: readonly ClientBoundaryRecord[],
  root: string,
): { code: string; map: null } | null {
  const fileName = cleanModuleID(id)
  const matching = deduplicateBoundaries(boundaries.filter(
    (boundary) => matchesManifestSource(root, boundary.source, fileName),
  ))
  if (matching.length === 0) return null

  const replacements: TextReplacement[] = []
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
      ? `import { ${needsClientOnly ? 'ClientOnly as __gbClientOnly' : ''}${needsClientOnly && needsDeferredRoot ? ', ' : ''}${needsDeferredRoot ? 'deferClientRender as __gbDeferClientRender' : ''} } from '@go-beyond/react';`
      : '',
  ].filter(Boolean).join('\n')
  const insertion = directivePrologueEnd(code)
  transformed =
    transformed.slice(0, insertion) +
    `${insertion === 0 ? '' : '\n'}${imports}\n` +
    transformed.slice(insertion)
  return { code: transformed, map: null }
}

/**
 * Rewrite compiler-recorded `useId()` call sites so hydration matches the Go plan.
 * Same source span with multiple ids (shared component inlined N times) becomes a
 * sequence factory. Parametric sites (inside `.map`) append String(keyExpression).
 */
export function transformUseIdSites(
  code: string,
  id: string,
  sites: readonly UseIdSiteRecord[],
  root: string,
): { code: string; map: null } | null {
  const fileName = cleanModuleID(id)
  const matching = sites.filter(
    (site) => matchesManifestSource(root, site.source, fileName) && !site.skipViteRewrite,
  )
  if (matching.length === 0) return null
  const rewrite = planUseIdRewrites(code, matching, fileName)

  const replacements = [...rewrite.replacements].sort(
    (left, right) => right.start - left.start,
  )
  let transformed = code
  for (const replacement of replacements) {
    transformed =
      transformed.slice(0, replacement.start) +
      replacement.text +
      transformed.slice(replacement.end)
  }
  const insertion = directivePrologueEnd(code)
  transformed =
    transformed.slice(0, insertion) +
    useIdHeaderText(rewrite.header, insertion) +
    transformed.slice(insertion)
  return { code: transformed, map: null }
}

/**
 * Rewrite `new Date().get*()` / `getUTC*()` to `renderSnapshotDate().get*()` so
 * hydration matches Go's renderNow clock.
 */
export function transformDateIntrinsicSites(
  code: string,
  id: string,
  sites: readonly DateIntrinsicSiteRecord[],
  root: string,
): { code: string; map: null } | null {
  const fileName = cleanModuleID(id)
  // Route aggregation can record the same shared-module span once per route.
  const matching = normalizeDateIntrinsicSites(
    sites.filter((site) => matchesManifestSource(root, site.source, fileName)),
  )
  if (matching.length === 0) return null

  const replacements: TextReplacement[] = []
  for (const site of matching) {
    const selected = code.slice(site.start, site.end)
    if (!isExpectedDateIntrinsicCall(selected, site.getter)) {
      throw staleDateIntrinsicError(site)
    }
    replacements.push({
      start: site.start,
      end: site.end,
      text: `__gbRenderSnapshotDate().${site.getter}()`,
    })
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
  const insertion = directivePrologueEnd(code)
  const header =
    `import { renderSnapshotDate as __gbRenderSnapshotDate } from '@go-beyond/react';`
  transformed =
    transformed.slice(0, insertion) +
    `${insertion === 0 ? '' : '\n'}${header}\n` +
    transformed.slice(insertion)
  return { code: transformed, map: null }
}

type UseIdRewrite = {
  replacements: TextReplacement[]
  /** Imports and sequence bindings inserted after the directive prologue. */
  header: string
}

/**
 * Plan every rewrite for one module. The client-boundary pass replays this
 * plan to shift its own spans, so both passes always agree on the edit sizes.
 */
function planUseIdRewrites(
  code: string,
  matching: readonly UseIdSiteRecord[],
  fileName: string,
): UseIdRewrite {
  const groups = new Map<string, UseIdSiteRecord[]>()
  for (const site of matching) {
    const key = `${site.start}:${site.end}`
    const group = groups.get(key) ?? []
    group.push(site)
    groups.set(key, group)
  }

  const replacements: TextReplacement[] = []
  const prelude: string[] = []
  let needsUseId = false
  let needsSequence = false
  let sequenceIndex = 0

  for (const group of groups.values()) {
    const site = group[0]!
    const selected = code.slice(site.start, site.end)
    if (!isExpectedUseIdCall(selected)) {
      throw staleUseIdError(site)
    }
    if (site.keyExpression) {
      const prefixes = uniquePreserveOrder(group.map((entry) => entry.id))
      const keys = uniquePreserveOrder(
        group.map((entry) => entry.keyExpression ?? ''),
      )
      if (prefixes.length > 1 || keys.length > 1) {
        throw new Error(
          `GoBeyond cannot rewrite parametric useId at ${site.source}:${site.line} with multiple prefixes; lift useId out of the nested component.`,
        )
      }
      needsUseId = true
      replacements.push({
        start: site.start,
        end: site.end,
        text: `__gbUseId(${JSON.stringify(site.id)} + String(${site.keyExpression}))`,
      })
      continue
    }
    const ids = uniquePreserveOrder(group.map((entry) => entry.id))
    if (ids.length === 1) {
      needsUseId = true
      replacements.push({
        start: site.start,
        end: site.end,
        text: `__gbUseId(${JSON.stringify(ids[0])})`,
      })
      continue
    }
    needsSequence = true
    const binding = `__gbUseIdSeq${sequenceIndex}`
    sequenceIndex += 1
    prelude.push(
      `const ${binding} = __gbUseIdSeq(${JSON.stringify(ids)});`,
    )
    replacements.push({
      start: site.start,
      end: site.end,
      text: `${binding}()`,
    })
  }

  assertNonOverlapping(replacements, fileName)
  const imports: string[] = []
  if (needsUseId) {
    imports.push(`import { useId as __gbUseId } from '@go-beyond/react';`)
  }
  if (needsSequence) {
    imports.push(
      `import { createUseIdSequence as __gbUseIdSeq } from '@go-beyond/react';`,
    )
  }
  return { replacements, header: [...imports, ...prelude].join('\n') }
}

function useIdHeaderText(header: string, insertion: number): string {
  return `${insertion === 0 ? '' : '\n'}${header}\n`
}

function shiftDateSitesAfterUseId(
  original: string,
  sites: readonly DateIntrinsicSiteRecord[],
  useIdSites: readonly UseIdSiteRecord[],
  root: string,
  id: string,
): DateIntrinsicSiteRecord[] {
  const fileName = cleanModuleID(id)
  const normalized = normalizeDateIntrinsicSites(sites)
  const fileSites = useIdSites.filter(
    (site) => matchesManifestSource(root, site.source, fileName) && !site.skipViteRewrite,
  )
  if (fileSites.length === 0) return normalized

  const rewrite = planUseIdRewrites(original, fileSites, fileName)
  const insertion = directivePrologueEnd(original)
  const headerDelta = useIdHeaderText(rewrite.header, insertion).length

  return normalized.map((site) => {
    if (!matchesManifestSource(root, site.source, fileName)) return site
    let start = site.start
    let end = site.end
    if (start >= insertion) {
      start += headerDelta
      end += headerDelta
    }
    for (const replacement of rewrite.replacements) {
      if (replacement.end > site.start) continue
      const delta =
        replacement.text.length - (replacement.end - replacement.start)
      start += delta
      end += delta
    }
    return { ...site, start, end }
  })
}

function shiftBoundariesAfterRewrites(
  original: string,
  boundaries: readonly ClientBoundaryRecord[],
  useIdSites: readonly UseIdSiteRecord[],
  dateSites: readonly DateIntrinsicSiteRecord[],
  root: string,
  id: string,
  appliedUseId: boolean,
  appliedDate: boolean,
): ClientBoundaryRecord[] {
  const fileName = cleanModuleID(id)
  let shifted = [...boundaries]
  if (appliedUseId) {
    const fileSites = useIdSites.filter(
      (site) => matchesManifestSource(root, site.source, fileName) && !site.skipViteRewrite,
    )
    if (fileSites.length > 0) {
      const rewrite = planUseIdRewrites(original, fileSites, fileName)
      const insertion = directivePrologueEnd(original)
      const headerDelta = useIdHeaderText(rewrite.header, insertion).length
      shifted = shifted.map((boundary) => {
        if (!matchesManifestSource(root, boundary.source, fileName)) return boundary
        let start = boundary.start
        let end = boundary.end
        if (start >= insertion) {
          start += headerDelta
          end += headerDelta
        }
        for (const replacement of rewrite.replacements) {
          if (replacement.end > boundary.start) continue
          const delta =
            replacement.text.length - (replacement.end - replacement.start)
          start += delta
          end += delta
        }
        return { ...boundary, start, end }
      })
    }
  }
  if (appliedDate) {
    // Date sites were already shifted for useId when applied; recompute against
    // the post-useId coordinate space using shiftDateSitesAfterUseId.
    const shiftedDates = appliedUseId
      ? shiftDateSitesAfterUseId(original, dateSites, useIdSites, root, id)
      : normalizeDateIntrinsicSites(dateSites)
    const fileDates = shiftedDates.filter(
      (site) => matchesManifestSource(root, site.source, fileName),
    )
    if (fileDates.length > 0) {
      const dateHeader =
        `import { renderSnapshotDate as __gbRenderSnapshotDate } from '@go-beyond/react';`
      // After useId, the prologue insertion point is measured on the post-useId
      // code. Approximate using original prologue + useId header length.
      const originalInsertion = directivePrologueEnd(original)
      let useIdHeaderLen = 0
      if (appliedUseId) {
        const fileSites = useIdSites.filter(
          (site) =>
            matchesManifestSource(root, site.source, fileName) &&
            !site.skipViteRewrite,
        )
        if (fileSites.length > 0) {
          const rewrite = planUseIdRewrites(original, fileSites, fileName)
          useIdHeaderLen = useIdHeaderText(rewrite.header, originalInsertion)
            .length
        }
      }
      const dateInsertion = originalInsertion + useIdHeaderLen
      const dateHeaderText =
        `${dateInsertion === 0 && useIdHeaderLen === 0 ? '' : '\n'}${dateHeader}\n`
      // When useId already inserted after prologue, date inserts after that header.
      const headerDelta = dateHeaderText.length
      shifted = shifted.map((boundary) => {
        if (!matchesManifestSource(root, boundary.source, fileName)) return boundary
        let start = boundary.start
        let end = boundary.end
        if (start >= dateInsertion) {
          start += headerDelta
          end += headerDelta
        }
        for (const site of fileDates) {
          if (site.end > boundary.start) continue
          const selectedLen = site.end - site.start
          const textLen = `__gbRenderSnapshotDate().${site.getter}()`.length
          const delta = textLen - selectedLen
          start += delta
          end += delta
        }
        return { ...boundary, start, end }
      })
    }
  }
  return shifted
}

function deferredRootReplacement(
  code: string,
  boundary: ClientBoundaryRecord,
): TextReplacement[] | undefined {
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

function uniquePreserveOrder(values: readonly string[]): string[] {
  const seen = new Set<string>()
  const result: string[] = []
  for (const value of values) {
    if (seen.has(value)) continue
    seen.add(value)
    result.push(value)
  }
  return result
}

function isExpectedCallSite(selected: string, component: string): boolean {
  const escaped = escapeRegExp(component)
  return new RegExp(`^<\\s*${escaped}(?:[\\s/>])`).test(selected) ||
    new RegExp(`(?:jsx|jsxs|jsxDEV|createElement)\\s*\\(\\s*${escaped}(?:\\s*[,)]|$)`).test(selected)
}

function isExpectedUseIdCall(selected: string): boolean {
  return /^(?:[\w$]+\.)?useId\s*\(\s*\)$/.test(selected.trim())
}

function isExpectedDateIntrinsicCall(selected: string, getter: string): boolean {
  const escaped = escapeRegExp(getter)
  return new RegExp(
    `^new\\s+Date\\s*\\(\\s*\\)\\s*\\.\\s*${escaped}\\s*\\(\\s*\\)$`,
  ).test(selected.trim())
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

/**
 * Collapse identical Date sites from per-route manifest aggregation.
 * Key: source + start + end + getter. Same span with different getters is an error.
 */
function normalizeDateIntrinsicSites(
  sites: readonly DateIntrinsicSiteRecord[],
): DateIntrinsicSiteRecord[] {
  const byKey = new Map<string, DateIntrinsicSiteRecord>()
  const getterBySpan = new Map<string, string>()
  for (const site of sites) {
    const spanKey = `${site.source}:${site.start}:${site.end}`
    const priorGetter = getterBySpan.get(spanKey)
    if (priorGetter !== undefined && priorGetter !== site.getter) {
      throw new Error(
        `Conflicting GoBeyond Date intrinsic getters at ${site.source}:${site.line}:${site.column}: ${priorGetter} vs ${site.getter}.`,
      )
    }
    getterBySpan.set(spanKey, site.getter)
    const key = `${spanKey}:${site.getter}`
    if (!byKey.has(key)) byKey.set(key, site)
  }
  return [...byKey.values()]
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
      throw new Error(`Overlapping GoBeyond transforms in ${fileName}.`)
    }
  }
}

function staleBoundaryError(boundary: ClientBoundaryRecord): Error {
  return new Error(
    `Stale GoBeyond client boundary ${boundary.id} at ${boundary.source}:${boundary.line}:${boundary.column}. Re-run the compiler.`,
  )
}

function staleUseIdError(site: UseIdSiteRecord): Error {
  return new Error(
    `Stale GoBeyond useId site ${site.id} at ${site.source}:${site.line}:${site.column}. Re-run the compiler.`,
  )
}

function staleDateIntrinsicError(site: DateIntrinsicSiteRecord): Error {
  return new Error(
    `Stale GoBeyond Date intrinsic site at ${site.source}:${site.line}:${site.column}. Re-run the compiler.`,
  )
}

function cleanModuleID(id: string): string {
  return resolve(id.split(/[?#]/, 1)[0]!)
}

/**
 * Match manifest `source` paths to Vite module ids across symlinks.
 * Linked workspace packages often record `node_modules/@scope/pkg/...` while
 * Vite transforms the realpath under `packages/...`.
 */
function sameSourceFile(left: string, right: string): boolean {
  if (left === right) return true
  try {
    return realpathSync(left) === realpathSync(right)
  } catch {
    return false
  }
}

function matchesManifestSource(
  root: string,
  source: string,
  fileName: string,
): boolean {
  return sameSourceFile(resolve(root, source), fileName)
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

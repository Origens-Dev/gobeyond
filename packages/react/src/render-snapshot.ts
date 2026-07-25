/**
 * Render-snapshot clock for portable Date projections.
 *
 * Go evaluates `new Date().getFullYear()` (and related getters) against one
 * instant captured at render start, then embeds that instant in hydration JSON
 * as `renderNow`. The Vite plugin rewrites matching call sites to use this
 * helper so React's first paint matches Go.
 *
 * Contract (definitive):
 * - UTC getters (`getUTCFullYear`, …) are hydration-safe across timezones.
 * - Local getters (`getFullYear`, …) match when the browser timezone equals the
 *   server timezone — the same class of SSR caveat as Next.js.
 * - `Intl.DateTimeFormat` and other locale-sensitive strings are NOT portable
 *   Date intrinsics. Format them in the Go loader and pass as props.
 */
export function renderSnapshotDate(documentRef: Document = document): Date {
  const raw = readRenderNow(documentRef)
  if (raw) {
    const parsed = new Date(raw)
    if (!Number.isNaN(parsed.getTime())) return parsed
  }
  return new Date()
}

function readRenderNow(documentRef: Document): string | undefined {
  const node = documentRef.getElementById('__GOBEYOND_DATA__')
  const text = node?.textContent?.trim()
  if (!text) return undefined
  try {
    const payload = JSON.parse(text) as { renderNow?: unknown }
    return typeof payload.renderNow === 'string' ? payload.renderNow : undefined
  } catch {
    return undefined
  }
}

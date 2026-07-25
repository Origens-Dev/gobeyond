import { useId as reactUseId } from 'react'

/**
 * Hydration-safe `useId` for GoBeyond portable markup.
 *
 * The compiler emits a stable call-site id into the Go render plan. The Vite
 * plugin rewrites matching `useId()` call sites to `useId("gb-...")` or a
 * sequence factory so the browser returns the same string(s). Without a baked
 * id (client-only trees), this falls back to React's fiber-encoded `useId()`.
 */
export function useId(stableId?: string): string {
  if (typeof stableId === 'string' && stableId.length > 0) {
    return stableId
  }
  return reactUseId()
}

/**
 * Returns a hook-compatible function that hands each component instance its
 * own baked id. Used when the same source `useId()` span is inlined multiple
 * times (shared components) so Go's distinct plan literals still hydrate.
 *
 * Instances claim ids in mount order, which is the order the compiler walked
 * the tree. React's own `useId` identifies the instance, so re-renders keep
 * the id the instance claimed first and extra instances beyond the baked list
 * fall back to a unique React id instead of colliding.
 */
export function createUseIdSequence(ids: readonly string[]): () => string {
  const claimed = new Map<string, string>()
  let cursor = 0
  return function useIdFromSequence(stableId?: string): string {
    if (typeof stableId === 'string' && stableId.length > 0) {
      return stableId
    }
    const instance = reactUseId()
    const existing = claimed.get(instance)
    if (existing !== undefined) return existing
    const id = ids[cursor]
    if (typeof id !== 'string' || id.length === 0) return instance
    cursor += 1
    claimed.set(instance, id)
    return id
  }
}

import { normalizeComparablePath } from "./path-utils.js";

/**
 * Soft-navigation / request route snapshot read by `usePathname` and
 * `useRoute`. Bootstrap seeds this before hydrate; soft-nav success updates it
 * so React re-renders match Go's baked request pathname.
 */
export interface ActiveNavigationState {
  routeId: string;
  pathname: string;
  params: Record<string, string>;
}

let activeNavigation: ActiveNavigationState | undefined;
const listeners = new Set<() => void>();

function notify(): void {
  for (const listener of listeners) {
    listener();
  }
}

/** Current active route, or `undefined` before bootstrap / after destroy. */
export function getActiveNavigation(): ActiveNavigationState | undefined {
  return activeNavigation;
}

/**
 * Replace the active route snapshot. Pathnames are normalized with
 * `normalizeComparablePath` so `/products/x` and `/products/x/` compare equal.
 */
export function setActiveNavigation(
  next: ActiveNavigationState | undefined,
): void {
  if (next === undefined) {
    if (activeNavigation === undefined) return;
    activeNavigation = undefined;
    notify();
    return;
  }
  const pathname = normalizeComparablePath(next.pathname);
  const prev = activeNavigation;
  if (
    prev &&
    prev.routeId === next.routeId &&
    prev.pathname === pathname &&
    paramsEqual(prev.params, next.params)
  ) {
    return;
  }
  activeNavigation = {
    routeId: next.routeId,
    pathname,
    params: { ...next.params },
  };
  notify();
}

/** Subscribe to active-route changes (for `useSyncExternalStore`). */
export function subscribeActiveNavigation(listener: () => void): () => void {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

function paramsEqual(
  left: Record<string, string>,
  right: Record<string, string>,
): boolean {
  const leftKeys = Object.keys(left);
  const rightKeys = Object.keys(right);
  if (leftKeys.length !== rightKeys.length) return false;
  for (const key of leftKeys) {
    if (left[key] !== right[key]) return false;
  }
  return true;
}

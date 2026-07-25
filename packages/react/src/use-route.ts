import { useSyncExternalStore } from "react";
import {
  getActiveNavigation,
  subscribeActiveNavigation,
  type ActiveNavigationState,
} from "./active-navigation.js";
import { normalizeComparablePath } from "./path-utils.js";

export type { ActiveNavigationState };

export interface RouteSnapshot {
  routeId: string;
  pathname: string;
  params: Record<string, string>;
}

/**
 * Active request pathname for portable active-nav patterns.
 *
 * The compiler bakes the request pathname into the Go render plan. In the
 * browser this hook reads the soft-navigation active route (seeded at bootstrap
 * and updated on soft-nav success) so hydration matches Go. An optional baked
 * argument is accepted for tests and for call sites rewritten with a stable
 * first-paint value when no active route is installed yet.
 */
export function usePathname(baked?: string): string {
  const active = useSyncExternalStore(
    subscribeActiveNavigation,
    getActiveNavigation,
    getActiveNavigation,
  );
  if (active) return active.pathname;
  if (typeof baked === "string" && baked.length > 0) {
    return normalizeComparablePath(baked);
  }
  return "/";
}

/**
 * Active route id, pathname, and dynamic params.
 *
 * Same bake / soft-nav contract as {@link usePathname}: Go gets request data
 * from the compiler; the browser reads module-level active navigation state.
 */
export function useRoute(baked?: RouteSnapshot): RouteSnapshot {
  const active = useSyncExternalStore(
    subscribeActiveNavigation,
    getActiveNavigation,
    getActiveNavigation,
  );
  if (active) {
    return {
      routeId: active.routeId,
      pathname: active.pathname,
      params: active.params,
    };
  }
  if (baked) {
    return {
      routeId: baked.routeId,
      pathname: normalizeComparablePath(baked.pathname),
      params: { ...baked.params },
    };
  }
  return { routeId: "", pathname: "/", params: {} };
}

/**
 * Normalize a pathname for comparison: drop empty segments (trailing/leading
 * slashes, repeats) and collapse to `/` for the root. Shared by soft
 * navigation's action-refresh path matching and the Router Cache's
 * path-based invalidation so both treat `/products/widget`,
 * `/products/widget/`, and `products/widget` as the same route.
 */
export function normalizeComparablePath(path: string): string {
  const segments = path.split("/").filter((segment) => segment.length > 0);
  return segments.length === 0 ? "/" : `/${segments.join("/")}`;
}

/** Whether `pathname` matches one of `paths`, ignoring the normalization above. */
export function pathsIncludePathname(
  paths: readonly string[],
  pathname: string,
): boolean {
  const target = normalizeComparablePath(pathname);
  return paths.some((path) => normalizeComparablePath(path) === target);
}

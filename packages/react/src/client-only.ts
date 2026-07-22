import {
  createElement,
  useEffect,
  useState,
  type ComponentType,
  type ReactNode,
} from "react";

export interface ClientOnlyProps {
  /**
   * Deterministic markup emitted by Go and rendered during React's first
   * hydration pass. SEO-critical content must not exist only in children.
   */
  fallback?: ReactNode;
  children: ReactNode;
}

/**
 * Defers browser-dependent children until after hydration. The server output
 * and React's first browser render both use `fallback`, preventing a hydration
 * mismatch without requiring effect analysis in the Go renderer.
 */
export function ClientOnly({ fallback = null, children }: ClientOnlyProps) {
  const [mounted, setMounted] = useState(false);

  useEffect(() => {
    setMounted(true);
  }, []);

  return mounted ? children : fallback;
}

/** Wraps a route-root client boundary without changing its public props. */
export function deferClientRender<Props extends object>(
  Component: ComponentType<Props>,
): ComponentType<Props> {
  function DeferredClientBoundary(props: Props) {
    return createElement(
      ClientOnly,
      null,
      createElement(Component, props),
    );
  }
  DeferredClientBoundary.displayName = `GoBeyondClientBoundary(${Component.displayName ?? Component.name ?? "Component"})`;
  return DeferredClientBoundary;
}

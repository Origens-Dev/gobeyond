import {
  createContext,
  createElement,
  useContext,
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
 * Nested ClientOnly boundaries consult this gate so a child never activates
 * while an ancestor is still showing its fallback.
 *
 * That matters for the common layout pattern:
 *
 * ```tsx
 * <ClientOnly fallback={children}>
 *   <BrowserProvider>{children}</BrowserProvider>
 * </ClientOnly>
 * ```
 *
 * Without the gate, a nested ClientOnly inside `fallback={children}` can mount
 * its browser children before the ancestor swaps to the provider-wrapped tree,
 * so hooks that need the provider run outside it (or hang forever).
 *
 * Default `true` means a root ClientOnly has no ancestor to wait for.
 */
const ClientOnlyGate = createContext(true);

/**
 * Defers browser-dependent children until after hydration. The server output
 * and React's first browser render both use `fallback`, preventing a hydration
 * mismatch without requiring effect analysis in the Go renderer.
 *
 * Nested ClientOnly boundaries also wait until every ancestor ClientOnly is
 * active before mounting their own children.
 */
export function ClientOnly({ fallback = null, children }: ClientOnlyProps) {
  const ancestorReady = useContext(ClientOnlyGate);
  const [selfMounted, setSelfMounted] = useState(false);

  useEffect(() => {
    if (ancestorReady) {
      setSelfMounted(true);
    }
  }, [ancestorReady]);

  const ready = ancestorReady && selfMounted;

  return createElement(
    ClientOnlyGate.Provider,
    { value: ready },
    ready ? children : fallback,
  );
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

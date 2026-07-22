import { useEffect, useState, type ReactNode } from "react";

export interface ClientOnlyProps {
  /**
   * Deterministic markup emitted by Go and rendered during React's first
   * hydration pass. SEO-critical content must not exist only in children.
   */
  fallback: ReactNode;
  children: ReactNode;
}

/**
 * Defers browser-dependent children until after hydration. The server output
 * and React's first browser render both use `fallback`, preventing a hydration
 * mismatch without requiring effect analysis in the Go renderer.
 */
export function ClientOnly({ fallback, children }: ClientOnlyProps) {
  const [mounted, setMounted] = useState(false);

  useEffect(() => {
    setMounted(true);
  }, []);

  return mounted ? children : fallback;
}

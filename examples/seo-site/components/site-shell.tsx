import type { ReactNode } from "react";

export interface SiteShellProps {
  children: ReactNode;
}

export function SiteShell({ children }: SiteShellProps) {
  return (
    <>
      <a href="#main-content">Skip to content</a>
      <header>
        <a href="/" aria-label="GoBeyond Field Guide home">
          GoBeyond Field Guide
        </a>
        <nav aria-label="Primary navigation">
          <a href="/articles/portable-react">Articles</a>
          <a href="/products/trail-pack">Products</a>
          <a href="/locations/seattle">Locations</a>
        </nav>
      </header>
      <main id="main-content">{children}</main>
      <footer>
        <p>Built with crawler-visible React rendered by Go.</p>
      </footer>
    </>
  );
}

import {
  createElement,
  forwardRef,
  type AnchorHTMLAttributes,
  type ReactNode,
} from "react";

export type LinkPrefetch = "auto" | "code" | "data" | false;

export interface LinkProps
  extends Omit<AnchorHTMLAttributes<HTMLAnchorElement>, "href"> {
  href: string | URL;
  /**
   * `auto` follows the generated route contract: visible links warm code and
   * opted-in data/images. `code` and `data` force that level of warming;
   * `false` disables automatic warming while retaining a native anchor.
   */
  prefetch?: LinkPrefetch;
  children?: ReactNode;
}

/**
 * A progressive-enhancement anchor for GoBeyond client navigation.
 *
 * The component intentionally renders a normal `<a>` so server HTML, bots,
 * modified clicks, and no-JavaScript clients retain native link behavior.
 * The browser navigation controller discovers the data attributes after
 * hydration and adds soft navigation/prefetch behavior where it is safe.
 */
export const Link = forwardRef<HTMLAnchorElement, LinkProps>(function Link(
  { href, prefetch = "auto", children, ...props },
  ref,
) {
  return createElement(
    "a",
    {
      ...props,
      ref,
      href: typeof href === "string" ? href : href.toString(),
      "data-gobeyond-link": "",
      "data-gobeyond-prefetch": prefetch === false ? "off" : prefetch,
    },
    children,
  );
});

Link.displayName = "GoBeyondLink";

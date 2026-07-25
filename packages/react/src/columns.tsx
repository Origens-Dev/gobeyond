import {
  createElement,
  type CSSProperties,
  type ReactElement,
  type ReactNode,
} from "react";

export interface ColumnsProps {
  /** Number of CSS columns. Defaults to `2`. */
  columnCount?: number;
  /** Column gap as a CSS length string or pixel number. Defaults to `1rem`. */
  gap?: string | number;
  children?: ReactNode;
  className?: string;
  style?: CSSProperties;
}

/**
 * Portable presentational layout that flows real content across CSS columns.
 * It requires no JavaScript measurement or resize listeners, so Go can emit
 * the content and layout styles in first-paint HTML.
 *
 * @example Multi-column content or gallery layout
 * ```tsx
 * <Columns columnCount={3} gap="1rem">
 *   {items}
 * </Columns>
 * ```
 */
export function Columns({
  columnCount = 2,
  gap = "1rem",
  children,
  className,
  style,
}: ColumnsProps): ReactElement {
  const columnGap = typeof gap === "number" ? `${gap}px` : gap;
  return createElement(
    "div",
    {
      className,
      style: {
        columnCount,
        columnGap,
        ...style,
      },
    },
    children,
  );
}

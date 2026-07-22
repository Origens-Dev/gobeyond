import { createElement } from "react";

export interface SafeHTMLProps {
  as: "div" | "span";
  /** HTML sanitized before it crossed the generated SafeHTML contract. */
  value: string;
}

/**
 * Hydrates the explicit wrapper emitted by the Go renderer. The compiler only
 * accepts values carried by a schema.safeHTML contract; this browser component
 * never performs sanitization itself.
 */
export function SafeHTML({ as, value }: SafeHTMLProps) {
  return createElement(as, { dangerouslySetInnerHTML: { __html: value } });
}

import assert from "node:assert/strict";
import test from "node:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { SafeHTML } from "../dist/safe-html.js";

test("SafeHTML renders the explicit hydration wrapper", () => {
  const markup = renderToStaticMarkup(
    createElement(SafeHTML, {
      as: "div",
      value: "<p>Sanitized <strong>content</strong></p>",
    }),
  );
  assert.equal(markup, "<div><p>Sanitized <strong>content</strong></p></div>");
});

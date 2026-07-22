import assert from "node:assert/strict";
import test from "node:test";
import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { ClientOnly } from "../dist/client-only.js";

test("ClientOnly emits its deterministic fallback on the server", () => {
  const markup = renderToStaticMarkup(
    createElement(
      ClientOnly,
      { fallback: createElement("p", null, "Map unavailable without JavaScript") },
      createElement("canvas", { "aria-label": "Interactive map" }),
    ),
  );
  assert.equal(markup, "<p>Map unavailable without JavaScript</p>");
});

test("ClientOnly emits an empty first render when fallback is omitted", () => {
  const markup = renderToStaticMarkup(
    createElement(
      ClientOnly,
      null,
      createElement("canvas", { "aria-label": "Interactive map" }),
    ),
  );
  assert.equal(markup, "");
});

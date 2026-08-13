import assert from "node:assert/strict";
import test from "node:test";
import { createElement } from "react";
import { renderToString } from "react-dom/server";
import { Link } from "../dist/index.js";

test("Link renders a progressive anchor with its prefetch policy", () => {
  const html = renderToString(
    createElement(
      Link,
      { href: new URL("/reports", "https://example.gobeyond.dev"), prefetch: "data" },
      "Reports",
    ),
  );

  assert.match(html, /<a[^>]+href="https:\/\/example\.gobeyond\.dev\/reports"/);
  assert.match(html, /data-gobeyond-link=""/);
  assert.match(html, /data-gobeyond-prefetch="data"/);
  assert.match(html, />Reports<\/a>/);
});

test("Link false policy remains a native anchor and disables automatic warming", () => {
  const html = renderToString(
    createElement(Link, { href: "/settings", prefetch: false }, "Settings"),
  );

  assert.match(html, /href="\/settings"/);
  assert.match(html, /data-gobeyond-prefetch="off"/);
});

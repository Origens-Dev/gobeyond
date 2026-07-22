import assert from "node:assert/strict";
import test from "node:test";
import {
  BROWSER_PROTOCOL_VERSION,
  parseBootstrapPayload,
} from "../dist/browser.js";

test("parses a versioned bootstrap payload", () => {
  const payload = parseBootstrapPayload(
    JSON.stringify({
      apiVersion: BROWSER_PROTOCOL_VERSION,
      buildId: "build-1",
      routeId: "article_slug",
      props: { title: "Portable React" },
    }),
  );
  assert.equal(payload.routeId, "article_slug");
  assert.deepEqual(payload.props, { title: "Portable React" });
});

test("rejects unknown protocols and non-object props", () => {
  assert.throws(
    () =>
      parseBootstrapPayload(
        JSON.stringify({
          apiVersion: "gobeyond.browser/v2",
          buildId: "build-1",
          routeId: "article_slug",
          props: {},
        }),
      ),
    /Unsupported GoBeyond browser protocol/,
  );
  assert.throws(
    () =>
      parseBootstrapPayload(
        JSON.stringify({
          apiVersion: BROWSER_PROTOCOL_VERSION,
          buildId: "build-1",
          routeId: "article_slug",
          props: null,
        }),
      ),
    /props must be an object/,
  );
});

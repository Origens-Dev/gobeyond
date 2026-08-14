import assert from "node:assert/strict";
import test from "node:test";
import { JSDOM } from "jsdom";
import {
  documentTraceHeaders,
  getDocumentTraceContext,
} from "../dist/trace-context.js";

function documentWithTrace(html: string) {
  return new JSDOM(`<!doctype html><html><head>${html}</head><body></body></html>`, {
    url: "https://example.gobeyond.dev/projects/portal/applications",
  }).window.document;
}

test("getDocumentTraceContext reads valid document meta tags", () => {
  const doc = documentWithTrace(
    `<meta name="traceparent" content="00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01">` +
      `<meta name="tracestate" content="vendor=opaque">`,
  );
  assert.deepEqual(getDocumentTraceContext(doc), {
    traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
    tracestate: "vendor=opaque",
  });
  assert.deepEqual(documentTraceHeaders(doc), {
    traceparent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
    tracestate: "vendor=opaque",
  });
});

test("getDocumentTraceContext ignores invalid or missing parents", () => {
  assert.deepEqual(getDocumentTraceContext(documentWithTrace("")), {});
  assert.deepEqual(
    getDocumentTraceContext(
      documentWithTrace(
        `<meta name="traceparent" content="00-00000000000000000000000000000000-00f067aa0ba902b7-01">`,
      ),
    ),
    {},
  );
  assert.deepEqual(documentTraceHeaders(documentWithTrace("")), {});
});

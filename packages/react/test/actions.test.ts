import assert from "node:assert/strict";
import test from "node:test";
import {
  ACTION_API_VERSION,
  parseActionEnvelope,
  postAction,
  runAction,
} from "../dist/actions.js";

test("parseActionEnvelope parses the frozen envelope shape with refresh", () => {
  const envelope = parseActionEnvelope(
    JSON.stringify({
      apiVersion: ACTION_API_VERSION,
      buildId: "build-1",
      data: { saved: true },
      refresh: { paths: ["/products/widget"], tags: ["products"] },
    }),
  );
  assert.equal(envelope.apiVersion, ACTION_API_VERSION);
  assert.equal(envelope.buildId, "build-1");
  assert.deepEqual(envelope.data, { saved: true });
  assert.deepEqual(envelope.refresh, {
    paths: ["/products/widget"],
    tags: ["products"],
  });
});

test("parseActionEnvelope omits refresh when the field is absent", () => {
  const envelope = parseActionEnvelope(
    JSON.stringify({ apiVersion: ACTION_API_VERSION, buildId: "build-1", data: { saved: true } }),
  );
  assert.equal(envelope.refresh, undefined);
});

test("parseActionEnvelope accepts the pre-envelope {data, buildId} shape for back-compat", () => {
  const envelope = parseActionEnvelope(
    JSON.stringify({ data: { saved: true }, buildId: "build-1" }),
  );
  assert.equal(envelope.apiVersion, undefined);
  assert.equal(envelope.buildId, "build-1");
  assert.deepEqual(envelope.data, { saved: true });
  assert.equal(envelope.refresh, undefined);
});

test("parseActionEnvelope rejects an unsupported apiVersion", () => {
  assert.throws(
    () =>
      parseActionEnvelope(
        JSON.stringify({ apiVersion: "gobeyond.action/v2", buildId: "build-1", data: {} }),
      ),
    /Unsupported GoBeyond action protocol/,
  );
});

test("parseActionEnvelope requires buildId", () => {
  assert.throws(
    () => parseActionEnvelope(JSON.stringify({ data: {} })),
    /missing buildId/,
  );
});

test("parseActionEnvelope rejects a non-array refresh.paths", () => {
  assert.throws(
    () =>
      parseActionEnvelope(
        JSON.stringify({ buildId: "build-1", data: {}, refresh: { paths: "not-an-array" } }),
      ),
    /must be an array of strings/,
  );
});

test("runAction refreshes recorded paths and returns the parsed envelope", async () => {
  const refreshed: (readonly string[])[] = [];
  const request: typeof fetch = async () =>
    new Response(
      JSON.stringify({
        apiVersion: ACTION_API_VERSION,
        buildId: "build-1",
        data: { saved: true },
        refresh: { paths: ["/products/widget"] },
      }),
      { status: 200, headers: { "content-type": "application/json" } },
    );

  const envelope = await runAction(
    "https://example.gobeyond.dev/_gobeyond/builds/build-1/actions/publish",
    { method: "POST", body: "{}" },
    {
      buildId: "build-1",
      fetch: request,
      refresh: async (paths) => {
        refreshed.push(paths);
        return undefined;
      },
    },
  );

  assert.deepEqual(envelope.data, { saved: true });
  assert.deepEqual(refreshed, [["/products/widget"]]);
});

test("runAction does not call refresh when the action recorded nothing", async () => {
  let refreshCalls = 0;
  const request: typeof fetch = async () =>
    new Response(
      JSON.stringify({ apiVersion: ACTION_API_VERSION, buildId: "build-1", data: { saved: true } }),
      { status: 200, headers: { "content-type": "application/json" } },
    );

  await runAction(
    "https://example.gobeyond.dev/_gobeyond/builds/build-1/actions/save",
    { method: "POST", body: "{}" },
    {
      buildId: "build-1",
      fetch: request,
      refresh: async () => {
        refreshCalls += 1;
        return undefined;
      },
    },
  );

  assert.equal(refreshCalls, 0);
});

test("runAction throws on a non-OK response", async () => {
  const request: typeof fetch = async () => new Response("boom", { status: 500 });
  await assert.rejects(
    runAction(
      "https://example.gobeyond.dev/_gobeyond/builds/build-1/actions/save",
      { method: "POST", body: "{}" },
      { buildId: "build-1", fetch: request },
    ),
    /status 500/,
  );
});

test("postAction posts JSON and parses the envelope", async () => {
  let receivedBody: string | undefined;
  const request: typeof fetch = async (_input, init) => {
    receivedBody = String(init?.body);
    return new Response(
      JSON.stringify({ apiVersion: ACTION_API_VERSION, buildId: "build-1", data: { saved: true } }),
      { status: 200, headers: { "content-type": "application/json" } },
    );
  };

  const envelope = await postAction(
    "https://example.gobeyond.dev/_gobeyond/builds/build-1/actions/save",
    { productSlug: "trail-pack", quantity: 1 },
    { buildId: "build-1", fetch: request },
  );

  assert.equal(receivedBody, JSON.stringify({ productSlug: "trail-pack", quantity: 1 }));
  assert.deepEqual(envelope.data, { saved: true });
});

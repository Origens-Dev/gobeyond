import assert from "node:assert/strict";
import test from "node:test";
import {
  BUILD_ERROR_HEADER,
  BUILD_ID_HEADER,
  DEPLOYMENT_REVISION_HEADER,
  BuildMismatchError,
  fetchWithBuildGuard,
  handleAssetLoadFailure,
  handleBuildMismatch,
  markBuildHealthy,
  type BuildMismatchEnvironment,
} from "../dist/build-mismatch.js";

class MemoryStorage {
  readonly #values = new Map<string, string>();

  get length() {
    return this.#values.size;
  }

  getItem(key: string) {
    return this.#values.get(key) ?? null;
  }

  setItem(key: string, value: string) {
    this.#values.set(key, value);
  }

  removeItem(key: string) {
    this.#values.delete(key);
  }

  key(index: number) {
    return [...this.#values.keys()][index] ?? null;
  }
}

function environment() {
  let reloads = 0;
  const value: BuildMismatchEnvironment = {
    location: { reload: () => reloads++ },
    sessionStorage: new MemoryStorage(),
  };
  return { value, reloads: () => reloads };
}

test("a stale document build reloads once, then requests an update screen", () => {
  const env = environment();
  let updateRequired = 0;

  const first = handleBuildMismatch("old", "new", { environment: env.value });
  assert.equal(first.disposition, "reloading");
  assert.equal(env.reloads(), 1);

  const second = handleBuildMismatch("old", "newer", {
    environment: env.value,
    onUpdateRequired: () => updateRequired++,
  });
  assert.equal(second.disposition, "update-required");
  assert.equal(env.reloads(), 1);
  assert.equal(updateRequired, 1);
});

test("production keeps the reload but skips the terminal update UI by default", () => {
  const env = environment();
  handleBuildMismatch("old", "new", { environment: env.value });
  let updateRequired = 0;
  const second = handleBuildMismatch("old", "new", {
    environment: env.value,
    showUpdateRequiredUI: false,
    onUpdateRequired: () => updateRequired++,
  });
  assert.equal(second.disposition, "update-required");
  assert.equal(updateRequired, 1);

  const silent = environment();
  handleBuildMismatch("stale", "fresh", { environment: silent.value });
  // Exhausted mismatch with no callback and UI disabled must not throw.
  const exhausted = handleBuildMismatch("stale", "fresh", {
    environment: silent.value,
    showUpdateRequiredUI: false,
  });
  assert.equal(exhausted.disposition, "update-required");
  assert.equal(silent.reloads(), 1);
});

test("a stale reload keeps its guard until a different build hydrates", () => {
  const env = environment();
  handleBuildMismatch("old", "new", { environment: env.value });
  assert.equal(env.value.sessionStorage.length, 1);
  markBuildHealthy("old", env.value);
  assert.equal(env.value.sessionStorage.length, 1);

  const repeated = handleBuildMismatch("old", "new", {
    environment: env.value,
    onUpdateRequired() {},
  });
  assert.equal(repeated.disposition, "update-required");
  assert.equal(env.reloads(), 1);

  markBuildHealthy("new", env.value);
  assert.equal(env.value.sessionStorage.length, 0);
});

test("an initial route chunk reloads once and a successful hydrate clears its guard", () => {
  const env = environment();
  const first = handleAssetLoadFailure("build-current", { environment: env.value });
  assert.equal(first.disposition, "reloading");
  assert.equal(env.reloads(), 1);

  let updateRequired = 0;
  const repeated = handleAssetLoadFailure("build-current", {
    environment: env.value,
    onUpdateRequired: () => updateRequired++,
  });
  assert.equal(repeated.disposition, "update-required");
  assert.equal(updateRequired, 1);

  markBuildHealthy("build-current", env.value);
  const later = handleAssetLoadFailure("build-current", { environment: env.value });
  assert.equal(later.disposition, "reloading");
  assert.equal(env.reloads(), 2);
});

test("build-aware fetch sends the build and never replays a mismatched action", async () => {
  const env = environment();
  let requests = 0;
  let receivedHeaders: Headers | undefined;

  const request: typeof fetch = async (_input, init) => {
    requests++;
    receivedHeaders = new Headers(init?.headers);
    return new Response(
      JSON.stringify({
        error: "build_mismatch",
        buildId: "build-new",
        reload: true,
      }),
      { status: 409, headers: { "content-type": "application/json" } },
    );
  };

  await assert.rejects(
    fetchWithBuildGuard(
      "/_gobeyond/builds/build-old/actions/save",
      { method: "POST", body: "{}" },
      {
        buildId: "build-old",
        deploymentRevision: "revision-1",
        fetch: request,
        environment: env.value,
      },
    ),
    (error: unknown) =>
      error instanceof BuildMismatchError && error.disposition === "reloading",
  );
  assert.equal(receivedHeaders?.get(BUILD_ID_HEADER), "build-old");
  assert.equal(receivedHeaders?.get(DEPLOYMENT_REVISION_HEADER), "revision-1");
  assert.equal(receivedHeaders?.get("x-gobeyond-build"), "build-old");
  assert.equal(receivedHeaders?.get("x-gobeyond-deployment"), "revision-1");
  assert.equal(receivedHeaders?.get("x-gobeyond-error"), null);
  assert.equal(requests, 1);
  assert.equal(env.reloads(), 1);
});

test("build-aware fetch treats new mismatch headers as authoritative", async () => {
  const env = environment();
  const request: typeof fetch = async () =>
    new Response(null, {
      status: 409,
      headers: {
        [BUILD_ERROR_HEADER]: "build_mismatch",
        [BUILD_ID_HEADER]: "build-new",
      },
    });

  await assert.rejects(
    fetchWithBuildGuard(
      "/_gobeyond/builds/build-old/runtime/home",
      { method: "GET" },
      { buildId: "build-old", fetch: request, environment: env.value },
    ),
    (error: unknown) =>
      error instanceof BuildMismatchError &&
      error.disposition === "reloading" &&
      error.expectedBuildId === "build-new",
  );
  assert.equal(env.reloads(), 1);
});

test("build-aware fetch falls back to legacy mismatch headers for one release", async () => {
  const env = environment();
  const request: typeof fetch = async () =>
    new Response(null, {
      status: 409,
      headers: {
        "x-gobeyond-error": "build_mismatch",
        "x-gobeyond-build": "build-legacy",
      },
    });

  await assert.rejects(
    fetchWithBuildGuard(
      "/_gobeyond/builds/build-old/runtime/home",
      { method: "GET" },
      { buildId: "build-old", fetch: request, environment: env.value },
    ),
    (error: unknown) =>
      error instanceof BuildMismatchError &&
      error.disposition === "reloading" &&
      error.expectedBuildId === "build-legacy",
  );
  assert.equal(env.reloads(), 1);
});

test("build-aware fetch prefers new mismatch headers over legacy names", async () => {
  const env = environment();
  const request: typeof fetch = async () =>
    new Response(null, {
      status: 409,
      headers: {
        [BUILD_ERROR_HEADER]: "build_mismatch",
        [BUILD_ID_HEADER]: "build-new",
        "x-gobeyond-error": "build_mismatch",
        "x-gobeyond-build": "build-legacy",
      },
    });

  await assert.rejects(
    fetchWithBuildGuard(
      "/_gobeyond/builds/build-old/runtime/home",
      { method: "GET" },
      { buildId: "build-old", fetch: request, environment: env.value },
    ),
    (error: unknown) =>
      error instanceof BuildMismatchError && error.expectedBuildId === "build-new",
  );
});

test("build-aware fetch still reads 412 JSON bodies without mismatch headers", async () => {
  const env = environment();
  const request: typeof fetch = async () =>
    new Response(
      JSON.stringify({
        error: { code: "build_mismatch", expectedBuildId: "build-new" },
      }),
      { status: 412, headers: { "content-type": "application/json" } },
    );

  await assert.rejects(
    fetchWithBuildGuard(
      "/_gobeyond/builds/build-old/actions/save",
      { method: "POST", body: "{}" },
      { buildId: "build-old", fetch: request, environment: env.value },
    ),
    (error: unknown) =>
      error instanceof BuildMismatchError &&
      error.disposition === "reloading" &&
      error.expectedBuildId === "build-new",
  );
  assert.equal(env.reloads(), 1);
});

test("build-aware fetch treats new deployment mismatch headers as a reload", async () => {
  const env = environment();
  const request: typeof fetch = async () =>
    new Response(JSON.stringify({ error: "deployment_mismatch" }), {
      status: 409,
      headers: {
        [BUILD_ERROR_HEADER]: "deployment_mismatch",
        [DEPLOYMENT_REVISION_HEADER]: "revision-new",
      },
    });

  await assert.rejects(
    fetchWithBuildGuard(
      "/_gobeyond/builds/build-old/actions/save",
      { method: "POST", body: "{}" },
      {
        buildId: "build-old",
        deploymentRevision: "revision-old",
        fetch: request,
        environment: env.value,
      },
    ),
    (error: unknown) =>
      error instanceof BuildMismatchError &&
      error.currentBuildId === "deployment:revision-old" &&
      error.expectedBuildId === "deployment:revision-new",
  );
});

test("build-aware fetch falls back to legacy deployment mismatch headers", async () => {
  const env = environment();
  const request: typeof fetch = async () =>
    new Response(JSON.stringify({ error: "deployment_mismatch" }), {
      status: 409,
      headers: {
        "x-gobeyond-error": "deployment_mismatch",
        "x-gobeyond-deployment": "revision-legacy",
      },
    });

  await assert.rejects(
    fetchWithBuildGuard(
      "/_gobeyond/builds/build-old/actions/save",
      { method: "POST", body: "{}" },
      {
        buildId: "build-old",
        deploymentRevision: "revision-old",
        fetch: request,
        environment: env.value,
      },
    ),
    (error: unknown) =>
      error instanceof BuildMismatchError &&
      error.expectedBuildId === "deployment:revision-legacy",
  );
});

test("same compiled build hydration preserves stale deployment reload guard", () => {
 const env = environment();
 handleBuildMismatch("deployment:old", "deployment:new", { environment: env.value });
 markBuildHealthy("build-unchanged", env.value);
 markBuildHealthy("deployment:old", env.value);
 const result = handleBuildMismatch("deployment:old", "deployment:new", { environment: env.value });
 assert.equal(result.disposition, "update-required");
 assert.equal(env.reloads(), 1);
 markBuildHealthy("deployment:new", env.value);
 assert.equal(env.value.sessionStorage.length, 0);
});

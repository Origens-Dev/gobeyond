import assert from "node:assert/strict";
import test from "node:test";
import {
  BUILD_ID_HEADER,
  BuildMismatchError,
  fetchWithBuildGuard,
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

test("build-aware fetch sends the build and never replays a mismatched action", async () => {
  const env = environment();
  let requests = 0;
  let receivedBuild = "";

  const request: typeof fetch = async (_input, init) => {
    requests++;
    receivedBuild = new Headers(init?.headers).get(BUILD_ID_HEADER) ?? "";
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
      "/_gobeyond/actions/build-old/save",
      { method: "POST", body: "{}" },
      { buildId: "build-old", fetch: request, environment: env.value },
    ),
    (error: unknown) =>
      error instanceof BuildMismatchError && error.disposition === "reloading",
  );
  assert.equal(receivedBuild, "build-old");
  assert.equal(requests, 1);
  assert.equal(env.reloads(), 1);
});

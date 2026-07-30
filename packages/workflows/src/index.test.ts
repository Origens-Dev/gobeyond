import assert from "node:assert/strict";
import test from "node:test";
import { start, use, type WorkflowBackend } from "./index.js";

test("requires a configured backend", async () => {
  await assert.rejects(() => start({ workflowName: "demo" }), /no backend configured/);
});

test("delegates start to the backend", async () => {
  const backend: WorkflowBackend = {
    async start() {
      return { workflowId: "wf-1", runId: "run-1" };
    },
    async signal() {},
  };
  use(backend);
  const handle = await start({ workflowName: "demo" });
  assert.deepEqual(handle, { workflowId: "wf-1", runId: "run-1" });
});

import assert from "node:assert/strict";
import test from "node:test";
import { start, use, type WorkflowClient } from "./index.js";

test("requires a configured client", async () => {
  await assert.rejects(() => start({ workflowName: "demo" }), /no client configured/);
});

test("delegates start to the client", async () => {
  const client: WorkflowClient = {
    async start() {
      return { workflowId: "wf-1", runId: "run-1" };
    },
    async signal() {},
  };
  use(client);
  const handle = await start({ workflowName: "demo" });
  assert.deepEqual(handle, { workflowId: "wf-1", runId: "run-1" });
});

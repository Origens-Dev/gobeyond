import assert from "node:assert/strict";
import test from "node:test";
import { start, use, type WorkflowWorld } from "./index.js";

test("requires a configured World", async () => {
  await assert.rejects(() => start({ workflowName: "demo" }), /no World configured/);
});

test("delegates start to the World", async () => {
  const world: WorkflowWorld = {
    async start(options) {
      return { workflowId: options.workflowId ?? "w1" };
    },
    async signal() {},
  };
  use(world);
  const handle = await start({ workflowName: "demo", workflowId: "abc" });
  assert.equal(handle.workflowId, "abc");
});

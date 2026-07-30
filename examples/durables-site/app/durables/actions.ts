import { defineAction, schema } from "@go-beyond/schema";

export const startEchoOnce = defineAction({
  input: schema.object({}),
  output: schema.object({
    started: schema.boolean(),
    workflowId: schema.string(),
  }),
});

export const startDemo = defineAction({
  input: schema.object({}),
  output: schema.object({
    started: schema.boolean(),
    workflowId: schema.string(),
  }),
});

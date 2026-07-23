import {
  defineAction,
  schema,
  type InferActionInput,
  type InferActionOutput,
} from "@go-beyond/schema";

export const addToCart = defineAction({
  input: schema.object({
    productSlug: schema.string(),
    quantity: schema.integer(),
  }),
  output: schema.object({
    saved: schema.boolean(),
    cartItemCount: schema.integer(),
  }),
});

export type AddToCartInput = InferActionInput<typeof addToCart>;
export type AddToCartOutput = InferActionOutput<typeof addToCart>;

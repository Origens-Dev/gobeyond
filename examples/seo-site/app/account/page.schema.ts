import { definePage, schema } from "@gobeyond/schema";

export const page = definePage({
  props: schema.object({
    displayName: schema.string(),
  }),
});

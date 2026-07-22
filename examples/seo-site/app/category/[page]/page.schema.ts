import { definePage, schema } from "@gobeyond/schema";

export const page = definePage({
  props: schema.object({
    currentPage: schema.integer(),
    canonical: schema.string(),
    previousHref: schema.optional(schema.string()),
    nextHref: schema.optional(schema.string()),
    items: schema.array(
      schema.object({
        href: schema.string(),
        name: schema.string(),
        summary: schema.string(),
      }),
    ),
  }),
});

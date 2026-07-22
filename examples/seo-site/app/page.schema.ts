import { definePage, schema } from "@gobeyond/schema";

export const page = definePage({
  props: schema.object({
    featuredArticleHref: schema.string(),
    featuredProductHref: schema.string(),
  }),
});

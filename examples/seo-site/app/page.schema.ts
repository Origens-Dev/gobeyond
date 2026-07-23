import { definePage, schema } from "@go-beyond/schema";

export const page = definePage({
  props: schema.object({
    featuredArticleHref: schema.string(),
    featuredProductHref: schema.string(),
  }),
});

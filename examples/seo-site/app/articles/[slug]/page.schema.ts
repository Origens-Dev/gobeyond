import { definePage, schema } from "@go-beyond/schema";

export const page = definePage({
  props: schema.object({
    slug: schema.string(),
    title: schema.string(),
    description: schema.string(),
    authorName: schema.string(),
    publishedAt: schema.datetime(),
    publishedLabel: schema.string(),
    canonical: schema.string(),
    alternateEnglish: schema.string(),
    alternateFrench: schema.string(),
    socialImage: schema.string(),
    paragraphs: schema.array(schema.string()),
  }),
});

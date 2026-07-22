import { definePage, schema } from "@gobeyond/schema";

export const page = definePage({
  props: schema.object({
    name: schema.string(),
    description: schema.string(),
    canonical: schema.string(),
    streetAddress: schema.string(),
    locality: schema.string(),
    region: schema.string(),
    postalCode: schema.string(),
    phone: schema.string(),
    phoneHref: schema.string(),
    hours: schema.array(schema.string()),
    mapHref: schema.string(),
  }),
});

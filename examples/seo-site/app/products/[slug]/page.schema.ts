import { definePage, schema } from "@go-beyond/schema";

export const page = definePage({
  props: schema.object({
    slug: schema.string(),
    name: schema.string(),
    description: schema.string(),
    canonical: schema.string(),
    image: schema.string(),
    imageAlt: schema.string(),
    price: schema.number(),
    priceLabel: schema.string(),
    currency: schema.string(),
    availability: schema.enum(["InStock", "OutOfStock"]),
  }),
});

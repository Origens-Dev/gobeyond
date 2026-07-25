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
  // The Go origin may reuse a product's loaded props for a minute, and
  // publishing a product bumps the "products" tag to drop them sooner. The
  // loader's gb.CachePolicy (public, max-age=60) is the matching edge header;
  // the two are declared separately and kept in step deliberately.
  revalidate: 60,
  tags: ["products"],
});

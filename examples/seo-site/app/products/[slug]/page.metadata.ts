import type { DocumentMetadata } from "@go-beyond/react";
import type { InferPageProps } from "@go-beyond/schema";
import { page } from "./page.schema.js";

type PageProps = InferPageProps<typeof page>;

export function metadata(props: PageProps): DocumentMetadata {
  return {
    title: props.name,
    description: props.description,
    canonical: props.canonical,
    lang: "en",
    robots: "index, follow",
    openGraph: {
      type: "product",
      title: props.name,
      description: props.description,
      url: props.canonical,
      images: [props.image],
    },
    twitter: {
      card: "summary_large_image",
      title: props.name,
      description: props.description,
      images: [props.image],
    },
    jsonLd: [
      {
        "@context": "https://schema.org",
        "@type": "Product",
        name: props.name,
        description: props.description,
        image: props.image,
        offers: {
          "@type": "Offer",
          price: props.price,
          priceCurrency: props.currency,
          availability: `https://schema.org/${props.availability}`,
          url: props.canonical,
        },
      },
    ],
  };
}

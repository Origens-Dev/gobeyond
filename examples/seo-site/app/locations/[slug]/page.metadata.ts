import type { DocumentMetadata } from "@gobeyond/react";
import type { InferPageProps } from "@gobeyond/schema";
import { page } from "./page.schema.js";

type PageProps = InferPageProps<typeof page>;

export function metadata(props: PageProps): DocumentMetadata {
  return {
    title: props.name,
    description: props.description,
    canonical: props.canonical,
    lang: "en",
    robots: "index, follow",
    jsonLd: [
      {
        "@context": "https://schema.org",
        "@type": "LocalBusiness",
        name: props.name,
        description: props.description,
        url: props.canonical,
        telephone: props.phone,
        address: {
          "@type": "PostalAddress",
          streetAddress: props.streetAddress,
          addressLocality: props.locality,
          addressRegion: props.region,
          postalCode: props.postalCode,
        },
      },
    ],
  };
}

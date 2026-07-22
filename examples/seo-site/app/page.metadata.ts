import type { DocumentMetadata } from "@gobeyond/react";
import type { InferPageProps } from "@gobeyond/schema";

import { page } from "./page.schema.js";

type Props = InferPageProps<typeof page>;

export function metadata(_props: Props): DocumentMetadata {
  const origin = "https://example.com";
  const title = "GoBeyond Field Guide";
  const description =
    "Practical notes and equipment for building beyond the usual path.";
  const image = `${origin}/social/home.svg`;
  return {
    lang: "en",
    title,
    description,
    canonical: `${origin}/`,
    robots: "index, follow",
    openGraph: {
      type: "website",
      title,
      description,
      url: `${origin}/`,
      images: [image],
    },
    twitter: {
      card: "summary_large_image",
      title,
      description,
      images: [image],
    },
    jsonLd: [
      {
        "@context": "https://schema.org",
        "@type": "WebSite",
        name: title,
        url: `${origin}/`,
      },
    ],
  };
}

import type { DocumentMetadata } from "@gobeyond/react";
import type { InferPageProps } from "@gobeyond/schema";
import { page } from "./page.schema.js";

type PageProps = InferPageProps<typeof page>;

export function metadata(props: PageProps): DocumentMetadata {
  return {
    title: props.title,
    description: props.description,
    canonical: props.canonical,
    lang: "en",
    robots: "index, follow",
    alternates: [
      { language: "en", url: props.alternateEnglish },
      { language: "fr", url: props.alternateFrench },
    ],
    openGraph: {
      type: "article",
      title: props.title,
      description: props.description,
      url: props.canonical,
      images: [props.socialImage],
    },
    twitter: {
      card: "summary_large_image",
      title: props.title,
      description: props.description,
      images: [props.socialImage],
    },
    jsonLd: [
      {
        "@context": "https://schema.org",
        "@type": "Article",
        headline: props.title,
        description: props.description,
        author: { "@type": "Person", name: props.authorName },
        datePublished: props.publishedAt,
        mainEntityOfPage: props.canonical,
        image: props.socialImage,
      },
    ],
  };
}

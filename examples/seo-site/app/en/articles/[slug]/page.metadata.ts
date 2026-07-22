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
  };
}

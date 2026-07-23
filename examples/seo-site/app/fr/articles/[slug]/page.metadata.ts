import type { DocumentMetadata } from "@go-beyond/react";
import type { InferPageProps } from "@go-beyond/schema";
import { page } from "./page.schema.js";

type PageProps = InferPageProps<typeof page>;

export function metadata(props: PageProps): DocumentMetadata {
  return {
    title: props.title,
    description: props.description,
    canonical: props.canonical,
    lang: "fr",
    robots: "index, follow",
    alternates: [
      { language: "en", url: props.alternateEnglish },
      { language: "fr", url: props.alternateFrench },
    ],
  };
}

import type { DocumentMetadata } from "@go-beyond/react";
import type { InferPageProps } from "@go-beyond/schema";
import { page } from "./page.schema.js";

type PageProps = InferPageProps<typeof page>;

export function metadata(props: PageProps): DocumentMetadata {
  return {
    title: `Field notes · page ${props.currentPage}`,
    description: `Browse field notes on page ${props.currentPage}`,
    canonical: props.canonical,
    lang: "en",
    robots: "index, follow",
  };
}

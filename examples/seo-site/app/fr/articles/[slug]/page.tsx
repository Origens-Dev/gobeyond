import type { InferPageProps } from "@gobeyond/schema";
import { page } from "./page.schema.js";

export type PageProps = InferPageProps<typeof page>;

export default function FrenchArticlePage(props: PageProps) {
  return (
    <article>
      <h1>{props.title}</h1>
      <p>{props.description}</p>
      {props.paragraphs.map((paragraph) => (
        <p key={paragraph}>{paragraph}</p>
      ))}
      <nav aria-label="Langues">
        <a href={props.alternateEnglish} hrefLang="en">
          English
        </a>
      </nav>
    </article>
  );
}

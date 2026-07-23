import type { InferPageProps } from "@go-beyond/schema";
import { page } from "./page.schema.js";

export type PageProps = InferPageProps<typeof page>;

export default function EnglishArticlePage(props: PageProps) {
  return (
    <article>
      <h1>{props.title}</h1>
      <p>{props.description}</p>
      {props.paragraphs.map((paragraph) => (
        <p key={paragraph}>{paragraph}</p>
      ))}
      <nav aria-label="Languages">
        <a href={props.alternateFrench} hrefLang="fr">
          Français
        </a>
      </nav>
    </article>
  );
}

import type { InferPageProps } from "@gobeyond/schema";
import { page } from "./page.schema.js";

export type PageProps = InferPageProps<typeof page>;

export default function ArticlePage(props: PageProps) {
  return (
    <article>
      <header>
        <h1>{props.title}</h1>
        <p>{props.description}</p>
        <p>
          By {props.authorName} ·{" "}
          <time dateTime={props.publishedAt}>{props.publishedLabel}</time>
        </p>
      </header>
      {props.paragraphs.map((paragraph) => (
        <p key={paragraph}>{paragraph}</p>
      ))}
    </article>
  );
}

import type { InferPageProps } from "@gobeyond/schema";
import { page } from "./page.schema.js";

export type PageProps = InferPageProps<typeof page>;

export default function HomePage({
  featuredArticleHref,
  featuredProductHref,
}: PageProps) {
  return (
    <article>
      <h1>GoBeyond Field Guide</h1>
      <p>Practical notes and equipment for building beyond the usual path.</p>
      <ul>
        <li>
          <a href={featuredArticleHref}>Read the featured article</a>
        </li>
        <li>
          <a href={featuredProductHref}>See the featured product</a>
        </li>
      </ul>
    </article>
  );
}

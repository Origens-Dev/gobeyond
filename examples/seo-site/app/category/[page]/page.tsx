import type { InferPageProps } from "@go-beyond/schema";
import { Pagination } from "../../../components/pagination.js";
import { page } from "./page.schema.js";

export type PageProps = InferPageProps<typeof page>;

export default function CategoryPage(props: PageProps) {
  return (
    <section>
      <h1>Field notes · page {props.currentPage}</h1>
      <ol>
        {props.items.map((item) => (
          <li key={item.href}>
            <h2>
              <a href={item.href}>{item.name}</a>
            </h2>
            <p>{item.summary}</p>
          </li>
        ))}
      </ol>
      <Pagination
        currentPage={props.currentPage}
        previousHref={props.previousHref}
        nextHref={props.nextHref}
      />
    </section>
  );
}

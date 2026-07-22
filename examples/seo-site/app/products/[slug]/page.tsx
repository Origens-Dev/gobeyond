import type { InferPageProps } from "@gobeyond/schema";
import { AddToCart } from "../../../components/add-to-cart.js";
import { page } from "./page.schema.js";

export type PageProps = InferPageProps<typeof page>;

export default function ProductPage(props: PageProps) {
  return (
    <article>
      <h1>{props.name}</h1>
      <img src={props.image} alt={props.imageAlt} width={1200} height={800} />
      <p>{props.description}</p>
      <p>
        <data value={props.price}>{props.priceLabel}</data>
      </p>
      <p>{props.availability === "InStock" ? "In stock" : "Out of stock"}</p>
      <AddToCart productName={props.name} />
    </article>
  );
}

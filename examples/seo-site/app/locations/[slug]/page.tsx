import type { InferPageProps } from "@go-beyond/schema";
import { LocationMap } from "../../../components/location-map.js";
import { page } from "./page.schema.js";

export type PageProps = InferPageProps<typeof page>;

export default function LocationPage(props: PageProps) {
  return (
    <article>
      <h1>{props.name}</h1>
      <p>{props.description}</p>
      <address>
        {props.streetAddress}
        <br />
        {props.locality}, {props.region} {props.postalCode}
        <br />
        <a href={props.phoneHref}>{props.phone}</a>
      </address>
      <h2>Hours</h2>
      <ul>
        {props.hours.map((hours) => (
          <li key={hours}>{hours}</li>
        ))}
      </ul>
      <LocationMap name={props.name} mapHref={props.mapHref} />
    </article>
  );
}

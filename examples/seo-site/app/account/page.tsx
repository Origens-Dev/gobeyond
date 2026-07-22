import type { RouteMetadata } from "@gobeyond/react";
import type { InferPageProps } from "@gobeyond/schema";
import { page } from "./page.schema.js";

export type PageProps = InferPageProps<typeof page>;

export const metadata: RouteMetadata = {
  title: "Your account",
  description: "Private GoBeyond account",
  canonical: "https://example.gobeyond.dev/account",
  lang: "en",
  robots: "noindex, nofollow",
};

export default function AccountPage({ displayName }: PageProps) {
  return (
    <section>
      <h1>Your account</h1>
      <p>Signed in as {displayName}</p>
    </section>
  );
}

export interface OpenGraphMetadata {
  type?: "article" | "product" | "profile" | "website";
  title?: string;
  description?: string;
  url?: string;
  siteName?: string;
  locale?: string;
  images?: readonly string[];
}

export interface TwitterMetadata {
  card: "summary" | "summary_large_image";
  title?: string;
  description?: string;
  images?: readonly string[];
}

export interface AlternateLanguage {
  language: string;
  url: string;
}

export interface RouteMetadata {
  title: string;
  description: string;
  canonical: string;
  lang: string;
  robots?: "index, follow" | "noindex, nofollow" | "noindex, follow";
  alternates?: readonly AlternateLanguage[];
  openGraph?: OpenGraphMetadata;
  twitter?: TwitterMetadata;
}

type JsonPrimitive = string | number | boolean | null;
export type JsonValue =
  | JsonPrimitive
  | readonly JsonValue[]
  | { readonly [key: string]: JsonValue };

export interface DocumentMetadata extends RouteMetadata {
  /**
   * Typed Schema.org documents emitted and escaped by Go in the document
   * head. JSON-LD is deliberately outside the portable body render plan.
   */
  jsonLd?: readonly Readonly<Record<string, JsonValue>>[];
}

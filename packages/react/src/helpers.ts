export function string(value: string | number | boolean | null | undefined): string {
  return value == null ? "" : String(value);
}

export function lower(value: string): string {
  return value.toLowerCase();
}

export function upper(value: string): string {
  return value.toUpperCase();
}

export function join(values: readonly (string | number | boolean)[], separator: string): string {
  return values.join(separator);
}

export function url(value: string): string {
  return encodeURIComponent(value)
    .replace(/%20/g, "+")
    .replace(/[!'()*]/g, (character) =>
      `%${character.charCodeAt(0).toString(16).toUpperCase()}`,
    );
}

export type ImageFormat = "jpeg" | "png" | "webp" | "auto";

export interface ImageOptions {
  w: number;
  q?: number;
  f?: ImageFormat;
}

/**
 * Build a same-site GoBeyond runtime image URL.
 *
 * The source may be an absolute path on the current site, or an HTTPS URL
 * whose domain is allowlisted by the deployment, such as a public CMS asset.
 */
export function imageSrc(
  source: string,
  options: ImageOptions,
): string {
  const query = `url=${url(source)}&w=${url(String(options.w))}&q=${url(String(options.q ?? 75))}`;
  return `/_gobeyond/image?${query}${options.f ? `&f=${url(options.f)}` : ""}`;
}

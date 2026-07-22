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

const schemaBrand: unique symbol = Symbol.for('gobeyond.schema')
declare const safeHTMLBrand: unique symbol

/** A string returned by an application-selected HTML sanitizer. */
export type SafeHTML = string & { readonly [safeHTMLBrand]: true }

/** Bind the framework trust marker to the site's chosen sanitizer. */
export function createHTMLSanitizer(sanitize: (untrusted: string) => string) {
  return (untrusted: string): SafeHTML => sanitize(untrusted) as SafeHTML
}

export interface Schema<T> {
  readonly [schemaBrand]: T
  readonly kind: string
  readonly nullable?: boolean
  readonly optional?: boolean
  readonly [key: string]: unknown
}

export type Infer<S extends Schema<unknown>> = S[typeof schemaBrand]

export interface OptionalSchema<S extends Schema<unknown>> extends Schema<Infer<S> | undefined> {
  readonly optional: true
  readonly inner: S
}

type Shape = Readonly<Record<string, Schema<unknown>>>
type InferShape<S extends Shape> = {
  -readonly [K in keyof S]: Infer<S[K]>
}

type OptionalKeys<S extends Shape> = {
  [K in keyof S]: S[K] extends { readonly optional: true } ? K : never
}[keyof S]

type ObjectOutput<S extends Shape> = Omit<InferShape<S>, OptionalKeys<S>> &
  Partial<Pick<InferShape<S>, OptionalKeys<S>>>

function descriptor<T>(value: Omit<Schema<T>, typeof schemaBrand>): Schema<T> {
  return value as Schema<T>
}

export const schema = {
  string: () => descriptor<string>({ kind: 'string' }),
  number: () => descriptor<number>({ kind: 'number' }),
  integer: () => descriptor<number>({ kind: 'integer' }),
  boolean: () => descriptor<boolean>({ kind: 'boolean' }),
  datetime: () => descriptor<string>({ kind: 'datetime', format: 'date-time' }),
  bytes: () => descriptor<string>({ kind: 'bytes', encoding: 'base64' }),
	 safeHTML: () => descriptor<SafeHTML>({ kind: 'safeHtml' }),
  literal: <const T extends string | number | boolean | null>(value: T) =>
    descriptor<T>({ kind: 'literal', value }),
  enum: <const T extends readonly [string, ...string[]]>(values: T) =>
    descriptor<T[number]>({ kind: 'enum', values }),
  array: <S extends Schema<unknown>>(items: S) =>
    descriptor<Array<Infer<S>>>({ kind: 'array', items }),
  object: <const S extends Shape>(shape: S) =>
    descriptor<ObjectOutput<S>>({ kind: 'object', shape }),
  optional: <S extends Schema<unknown>>(inner: S): OptionalSchema<S> =>
    ({ ...inner, optional: true, inner }) as OptionalSchema<S>,
  nullable: <S extends Schema<unknown>>(inner: S) =>
    descriptor<Infer<S> | null>({ ...inner, nullable: true, inner }),
  union: <const S extends readonly [Schema<unknown>, ...Schema<unknown>[]]>(variants: S) =>
    descriptor<Infer<S[number]>>({ kind: 'union', variants }),
} as const

export interface PageDefinition<S extends Schema<unknown>> {
  readonly kind: 'page'
  readonly props: S
  /** Origin props-ISR window in seconds. Absent when the route is not cached. */
  readonly revalidate?: number
  /** Invalidation handles for this route's cached props. */
  readonly tags?: readonly string[]
}

/**
 * Declare a route's props contract and, optionally, how long the Go origin may
 * reuse computed props for it.
 *
 * `revalidate` is origin props ISR measured in seconds, not an HTTP directive:
 * the edge `Cache-Control` for a route stays the loader's explicit
 * `gb.CachePolicy`. A route that sets both should derive one from the other
 * (`gb.PublicRevalidate(revalidate, ...)`) because nothing at request time can
 * tell an omitted policy from a deliberately private one. Both `revalidate`
 * and `tags` require a sibling `page.go`; the compiler rejects them on a
 * purely static route, which has no request-time loader to cache.
 */
export function definePage<S extends Schema<unknown>>(definition: {
  readonly props: S
  readonly revalidate?: number
  readonly tags?: readonly string[]
}): PageDefinition<S> {
  return {
    kind: 'page',
    props: definition.props,
    ...(definition.revalidate === undefined ? {} : { revalidate: definition.revalidate }),
    ...(definition.tags === undefined ? {} : { tags: definition.tags }),
  }
}

export type InferPageProps<P extends PageDefinition<Schema<unknown>>> = Infer<P['props']>

export interface ActionDefinition<I extends Schema<unknown>, O extends Schema<unknown>> {
  readonly kind: 'action'
  readonly input: I
  readonly output: O
}

export function defineAction<I extends Schema<unknown>, O extends Schema<unknown>>(definition: {
  readonly input: I
  readonly output: O
}): ActionDefinition<I, O> {
  return { kind: 'action', input: definition.input, output: definition.output }
}

export type InferActionInput<A extends ActionDefinition<Schema<unknown>, Schema<unknown>>> = Infer<
  A['input']
>

export type InferActionOutput<A extends ActionDefinition<Schema<unknown>, Schema<unknown>>> = Infer<
  A['output']
>

export {
  defineManifest,
  defineRobots,
  defineSitemap,
  type Manifest,
  type Robots,
  type SitemapFile,
} from './crawler.js'

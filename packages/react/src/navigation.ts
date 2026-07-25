import { createElement, type ComponentType, type ReactElement } from "react";
import { flushSync } from "react-dom";
import type { Root } from "react-dom/client";
import {
  BUILD_ID_HEADER,
  BuildMismatchError,
  fetchWithBuildGuard,
  handleBuildMismatch,
  renderUpdateRequired,
  shouldShowUpdateRequiredUI,
  type BuildMismatchEnvironment,
} from "./build-mismatch.js";
import { normalizeComparablePath, pathsIncludePathname } from "./path-utils.js";
import { createRouterCache, type RouterCache, type RouterCacheOptions } from "./router-cache.js";
import type {
  AlternateLanguage,
  IconMetadata,
  JsonValue,
  OpenGraphImageMetadata,
  OpenGraphMetadata,
  TwitterMetadata,
} from "./seo.js";

export const NAVIGATION_ANNOUNCER_ID = "__gobeyond_route_announcer__";

/** Eager or resolved browser route: page plus outermost→innermost layouts. */
export interface ResolvedBrowserRoute {
  page: ComponentType<any>;
  /**
   * Layout components ordered outermost to innermost. Shared module identity
   * across routes lets React keep layout instances mounted during soft nav.
   */
  layouts: readonly ComponentType<any>[];
}

export interface BrowserRoute {
  /** @deprecated Prefer `page`; kept for existing eager registrations. */
  component?: ComponentType<any>;
  page?: ComponentType<any>;
  layouts?: readonly ComponentType<any>[];
  pattern: string;
}

export type BrowserRouteModule =
  | ComponentType<any>
  | Readonly<{
      default?: ComponentType<any>;
      page?: ComponentType<any>;
      layouts?: readonly ComponentType<any>[];
    }>;

export interface LazyBrowserRoute {
  load: () => Promise<BrowserRouteModule>;
  pattern: string;
}

export type BrowserRouteRegistration =
  | ComponentType<any>
  | BrowserRoute
  | LazyBrowserRoute;

// The generated registry erases individual prop types only at the route
// boundary. Each imported page and layout remains strongly typed beforehand.
export type RouteRegistry = Readonly<
  Record<string, BrowserRouteRegistration>
>;

export type NavigationLifecycleEvent =
  | {
      type: "start";
      url: string;
      routeId: string;
    }
  | {
      type: "success";
      url: string;
      routeId: string;
      payload?: RuntimeNavigationPayload;
    }
  | {
      type: "error";
      url: string;
      routeId?: string;
      error: unknown;
    };

export type NavigationLifecycleListener = (
  event: NavigationLifecycleEvent,
) => void;

const navigationLifecycleListeners = new Set<NavigationLifecycleListener>();

/**
 * Subscribe to soft-navigation lifecycle events from any module (for example a
 * layout-mounted progress bar). Works even when the generated client entry
 * discards the bootstrap return value.
 */
export function subscribeNavigation(
  listener: NavigationLifecycleListener,
): () => void {
  navigationLifecycleListeners.add(listener);
  return () => {
    navigationLifecycleListeners.delete(listener);
  };
}

function emitNavigationLifecycle(event: NavigationLifecycleEvent): void {
  for (const listener of navigationLifecycleListeners) {
    listener(event);
  }
}

export interface RuntimeMetadata {
  lang: string;
  title: string;
  description?: string;
  canonical?: string;
  robots?: string;
  alternates?: readonly AlternateLanguage[];
  openGraph?: OpenGraphMetadata;
  twitter?: Omit<TwitterMetadata, "card"> & {
    card?: TwitterMetadata["card"];
  };
  icons?: IconMetadata;
  jsonLd?: readonly Readonly<Record<string, JsonValue>>[];
}

export type RuntimeResultKind =
  | "ok"
  | "redirect"
  | "not_found"
  | "public_error"
  | "internal_error";

/** Matches gb.CacheMode (gobeyond.go). */
export type CacheMode = "private_no_store" | "public";

/**
 * Matches gb.CachePolicy (gobeyond.go) - the edge/browser HTTP cache header
 * a page loader chose, carried through the soft-nav runtime JSON so the
 * client Router Cache (see router-cache.ts) can apply the same freshness
 * rule the origin already committed to instead of guessing. `gb.OK()`
 * defaults to `private_no_store`; only `mode: "public"` is ever cached
 * client-side.
 */
export interface CachePolicy {
  mode: CacheMode;
  maxAge?: number;
  sharedMaxAge?: number;
  staleWhileRevalidate?: number;
  staleIfError?: number;
}

export interface RuntimeNavigationResult {
  kind: RuntimeResultKind;
  props: Record<string, unknown>;
  metadata?: RuntimeMetadata;
  status?: number;
  redirectTo?: string;
  errorCode?: string;
  message?: string;
  /** Present on every runtime response; absent only in hand-built test fixtures. */
  cache?: CachePolicy;
}

export interface RuntimeNavigationPayload {
  apiVersion: "gobeyond.render/v1alpha1";
  buildId: string;
  routeId: string;
  result: RuntimeNavigationResult;
}

export interface MatchedBrowserRoute {
  routeId: string;
  pattern: string;
  component?: ComponentType<any>;
  load?: LazyBrowserRoute["load"];
}

export type NavigationHistoryMode = "push" | "replace" | "pop";

export interface NavigateOptions {
  history?: NavigationHistoryMode;
  scroll?: { x: number; y: number };
  /**
   * Skip the accessible route-change announcement and focus move. Used by
   * `SoftNavigationController.refresh`, which updates the current route's
   * data in place rather than navigating anywhere a user would notice.
   */
  silent?: boolean;
}

export interface SoftNavigationController {
  navigate(
    target: string | URL,
    options?: NavigateOptions,
  ): Promise<RuntimeNavigationPayload | undefined>;
  prefetch(target: string | URL): Promise<void>;
  /**
   * Re-fetch the currently mounted route's runtime JSON in place (no history
   * entry, no scroll change) and re-render with the fresh props. This is the
   * client half of the action envelope's "refresh" field (Locked decision
   * 9): a server action that calls `cache.RevalidatePath` records the path,
   * and the action response's `refresh.paths` tells the client which paths
   * changed.
   *
   * When `paths` is provided, the refresh only runs if the current route's
   * path is one of them; when omitted, the current route always refreshes.
   * Either way this also invalidates the client Router Cache: with `paths`,
   * only matching entries are dropped (they may belong to routes other than
   * the one currently mounted); without `paths`, the entire Router Cache is
   * cleared, since there is no narrower signal for "something changed."
   */
  refresh(paths?: readonly string[]): Promise<RuntimeNavigationPayload | undefined>;
  /**
   * Drop entries from the client Router Cache (see router-cache.ts).
   * `refresh` already calls this for action-triggered revalidation and
   * `destroy` clears it on teardown; call this directly for transitions
   * this package has no built-in hook for - most importantly auth-ish
   * transitions (login, logout, account switch) - so a subsequent
   * back/forward never replays another session's cached page. Pass specific
   * paths to invalidate only those (matched the same way `refresh`'s
   * `paths` is), or omit to clear everything.
   */
  clearRouterCache(paths?: readonly string[]): void;
  /** Subscribe to soft-navigation lifecycle events (start / success / error). */
  subscribe(listener: NavigationLifecycleListener): () => void;
  destroy(): void;
}

function disabledSoftNavigationController(): SoftNavigationController {
  return {
    async navigate() {
      return undefined;
    },
    async prefetch() {},
    async refresh() {
      return undefined;
    },
    clearRouterCache() {},
    subscribe() {
      return () => {};
    },
    destroy() {},
  };
}

let activeSoftNavigation: SoftNavigationController | undefined;

/**
 * Refresh the currently mounted route's data through whichever
 * `SoftNavigationController` `bootstrap`/`bootstrapAsync` most recently
 * created (and has not `destroy()`ed). Lets action helpers such as
 * `runAction` (see actions.ts) trigger a refresh without every caller
 * threading the `bootstrap` return value through their component tree, the
 * same decoupling `subscribeNavigation` gives lifecycle listeners. A no-op
 * when no soft navigation is currently installed.
 */
export function refreshNavigation(
  paths?: readonly string[],
): Promise<RuntimeNavigationPayload | undefined> {
  if (!activeSoftNavigation) return Promise.resolve(undefined);
  return activeSoftNavigation.refresh(paths);
}

export interface SoftNavigationOptions {
  buildId: string;
  routes: RouteRegistry;
  root: Root;
  rootElement: HTMLElement;
  document: Document;
  render?: (
    route: ResolvedBrowserRoute,
    props: Record<string, unknown>,
  ) => ReactElement;
  fetch?: typeof globalThis.fetch;
  mismatchEnvironment?: BuildMismatchEnvironment;
  onUpdateRequired?: (error: BuildMismatchError) => void;
  showUpdateRequiredUI?: boolean;
  onNavigationStart?: (
    event: Extract<NavigationLifecycleEvent, { type: "start" }>,
  ) => void;
  onNavigationSettled?: (
    event: Extract<NavigationLifecycleEvent, { type: "success" | "error" }>,
  ) => void;
  onNavigationError?: (error: unknown) => void;
  hardNavigate?: (url: string) => void;
  scrollTo?: (x: number, y: number) => void;
  /**
   * Client Router Cache for soft-nav payloads (see router-cache.ts). Pass an
   * instance to share one cache across controllers/tests, or tune its TTL
   * cap via `routerCacheOptions`; otherwise a private cache is created and
   * torn down with this controller.
   */
  routerCache?: RouterCache;
  routerCacheOptions?: RouterCacheOptions;
}

interface NavigationHistoryState {
  buildId: string;
  path: string;
  scrollX: number;
  scrollY: number;
}

const HISTORY_STATE_KEY = "__gobeyondNavigation";
const RUNTIME_API_VERSION = "gobeyond.render/v1alpha1";

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function requiredString(
  record: Record<string, unknown>,
  name: string,
  context: string,
): string {
  const value = record[name];
  if (typeof value !== "string" || value.length === 0) {
    throw new Error(`${context} is missing ${name}.`);
  }
  return value;
}

function optionalString(
  record: Record<string, unknown>,
  name: string,
  context: string,
): string | undefined {
  const value = record[name];
  if (value === undefined) return undefined;
  if (typeof value !== "string") {
    throw new Error(`${context}.${name} must be a string.`);
  }
  return value;
}

function optionalNonNegativeInt(
  record: Record<string, unknown>,
  name: string,
  context: string,
): number | undefined {
  const value = record[name];
  if (value === undefined) return undefined;
  if (typeof value !== "number" || !Number.isInteger(value) || value < 0) {
    throw new Error(`${context}.${name} must be a non-negative integer.`);
  }
  return value;
}

function parseCachePolicy(value: unknown): CachePolicy | undefined {
  if (value === undefined) return undefined;
  if (!isRecord(value)) {
    throw new Error("GoBeyond runtime result.cache must be an object.");
  }
  const mode = requiredString(value, "mode", "GoBeyond runtime result.cache");
  if (mode !== "private_no_store" && mode !== "public") {
    throw new Error(`GoBeyond runtime result.cache.mode is unsupported: ${mode}`);
  }
  return {
    mode,
    maxAge: optionalNonNegativeInt(value, "maxAge", "result.cache"),
    sharedMaxAge: optionalNonNegativeInt(value, "sharedMaxAge", "result.cache"),
    staleWhileRevalidate: optionalNonNegativeInt(value, "staleWhileRevalidate", "result.cache"),
    staleIfError: optionalNonNegativeInt(value, "staleIfError", "result.cache"),
  };
}

function optionalStrings(
  record: Record<string, unknown>,
  name: string,
  context: string,
): readonly string[] | undefined {
  const value = record[name];
  if (value === undefined) return undefined;
  if (!Array.isArray(value) || value.some((item) => typeof item !== "string")) {
    throw new Error(`${context}.${name} must be an array of strings.`);
  }
  return value as string[];
}

function parseOpenGraphImage(value: unknown): OpenGraphImageMetadata | undefined {
  if (value === undefined) return undefined;
  if (!isRecord(value)) {
    throw new Error("metadata.openGraph.image must be an object.");
  }
  const url = requiredString(value, "url", "metadata.openGraph.image");
  const width = value.width;
  const height = value.height;
  if (width !== undefined && (typeof width !== "number" || width < 0)) {
    throw new Error("metadata.openGraph.image.width must be a non-negative number.");
  }
  if (height !== undefined && (typeof height !== "number" || height < 0)) {
    throw new Error("metadata.openGraph.image.height must be a non-negative number.");
  }
  return {
    url,
    width,
    height,
    alt: optionalString(value, "alt", "metadata.openGraph.image"),
    type: optionalString(value, "type", "metadata.openGraph.image"),
  };
}

function parseOpenGraph(value: unknown): OpenGraphMetadata | undefined {
  if (value === undefined) return undefined;
  if (!isRecord(value)) {
    throw new Error("GoBeyond runtime metadata.openGraph must be an object.");
  }
  const type = optionalString(value, "type", "metadata.openGraph");
  if (
    type !== undefined &&
    type !== "article" &&
    type !== "product" &&
    type !== "profile" &&
    type !== "website"
  ) {
    throw new Error("metadata.openGraph.type is unsupported.");
  }
  const result: OpenGraphMetadata = {
    type,
    title: optionalString(value, "title", "metadata.openGraph"),
    description: optionalString(value, "description", "metadata.openGraph"),
    url: optionalString(value, "url", "metadata.openGraph"),
    siteName: optionalString(value, "siteName", "metadata.openGraph"),
    locale: optionalString(value, "locale", "metadata.openGraph"),
    image: parseOpenGraphImage(value.image),
    images: optionalStrings(value, "images", "metadata.openGraph"),
  };
  return Object.values(result).every((item) => item === undefined)
    ? undefined
    : result;
}

function parseTwitter(value: unknown): RuntimeMetadata["twitter"] {
  if (value === undefined) return undefined;
  if (!isRecord(value)) {
    throw new Error("GoBeyond runtime metadata.twitter must be an object.");
  }
  const card = optionalString(value, "card", "metadata.twitter");
  if (card !== undefined && card !== "summary" && card !== "summary_large_image") {
    throw new Error("metadata.twitter.card is unsupported.");
  }
  const result: NonNullable<RuntimeMetadata["twitter"]> = {
    card,
    title: optionalString(value, "title", "metadata.twitter"),
    description: optionalString(value, "description", "metadata.twitter"),
    site: optionalString(value, "site", "metadata.twitter"),
    imageAlt: optionalString(value, "imageAlt", "metadata.twitter"),
    images: optionalStrings(value, "images", "metadata.twitter"),
  };
  return Object.values(result).every((item) => item === undefined)
    ? undefined
    : result;
}

function parseMetadata(value: unknown): RuntimeMetadata | undefined {
  if (value === undefined) return undefined;
  if (!isRecord(value)) {
    throw new Error("GoBeyond runtime metadata must be an object.");
  }
  const alternatesValue = value.alternates;
  let alternates: AlternateLanguage[] | undefined;
  if (alternatesValue !== undefined) {
    if (!Array.isArray(alternatesValue)) {
      throw new Error("metadata.alternates must be an array.");
    }
    alternates = alternatesValue.map((alternate) => {
      if (!isRecord(alternate)) {
        throw new Error("metadata.alternates entries must be objects.");
      }
      return {
        language: requiredString(alternate, "language", "metadata alternate"),
        url: requiredString(alternate, "url", "metadata alternate"),
      };
    });
  }

  const jsonLdValue = value.jsonLd;
  let jsonLd: Readonly<Record<string, JsonValue>>[] | undefined;
  if (jsonLdValue !== undefined) {
    if (!Array.isArray(jsonLdValue) || jsonLdValue.some((item) => !isRecord(item))) {
      throw new Error("metadata.jsonLd must be an array of objects.");
    }
    jsonLd = jsonLdValue as Readonly<Record<string, JsonValue>>[];
  }

  let icons: IconMetadata | undefined;
  if (value.icons !== undefined) {
    if (!isRecord(value.icons)) {
      throw new Error("metadata.icons must be an object.");
    }
    icons = {
      icon: optionalString(value.icons, "icon", "metadata.icons"),
      appleTouch: optionalString(value.icons, "appleTouch", "metadata.icons"),
    };
  }

  return {
    lang: requiredString(value, "lang", "GoBeyond runtime metadata"),
    title: requiredString(value, "title", "GoBeyond runtime metadata"),
    description: optionalString(value, "description", "metadata"),
    canonical: optionalString(value, "canonical", "metadata"),
    robots: optionalString(value, "robots", "metadata"),
    alternates,
    openGraph: parseOpenGraph(value.openGraph),
    twitter: parseTwitter(value.twitter),
    icons,
    jsonLd,
  };
}

/** Parse the explicit lower-camel runtime DTO used by soft navigation. */
export function parseRuntimeNavigationPayload(
  text: string,
): RuntimeNavigationPayload {
  const value: unknown = JSON.parse(text);
  if (!isRecord(value)) {
    throw new Error("GoBeyond runtime data must be an object.");
  }
  if (value.apiVersion !== RUNTIME_API_VERSION) {
    throw new Error(`Unsupported GoBeyond runtime protocol: ${String(value.apiVersion)}`);
  }
  const buildId = requiredString(value, "buildId", "GoBeyond runtime data");
  const routeId = requiredString(value, "routeId", "GoBeyond runtime data");
  if (!isRecord(value.result)) {
    throw new Error("GoBeyond runtime result must be an object.");
  }

  const kind = requiredString(value.result, "kind", "GoBeyond runtime result");
  if (
    kind !== "ok" &&
    kind !== "redirect" &&
    kind !== "not_found" &&
    kind !== "public_error" &&
    kind !== "internal_error"
  ) {
    throw new Error(`Unsupported GoBeyond runtime result: ${kind}`);
  }
  const propsValue = value.result.props;
  if (propsValue !== undefined && !isRecord(propsValue)) {
    throw new Error("GoBeyond runtime result.props must be an object.");
  }
  const statusValue = value.result.status;
  if (
    statusValue !== undefined &&
    (typeof statusValue !== "number" || !Number.isInteger(statusValue))
  ) {
    throw new Error("GoBeyond runtime result.status must be an integer.");
  }

  return {
    apiVersion: RUNTIME_API_VERSION,
    buildId,
    routeId,
    result: {
      kind,
      props: propsValue ?? {},
      metadata: parseMetadata(value.result.metadata),
      status: statusValue,
      redirectTo: optionalString(value.result, "redirectTo", "runtime result"),
      errorCode: optionalString(value.result, "errorCode", "runtime result"),
      message: optionalString(value.result, "message", "runtime result"),
      cache: parseCachePolicy(value.result.cache),
    },
  };
}

function routeDefinition(
  routeId: string,
  registration: BrowserRouteRegistration,
): MatchedBrowserRoute | undefined {
  if (
    isRecord(registration) &&
    ("component" in registration || "page" in registration) &&
    typeof registration.pattern === "string"
  ) {
    const component = (registration.page ?? registration.component) as
      | ComponentType<any>
      | undefined;
    if (typeof component !== "function") return undefined;
    return {
      routeId,
      component,
      pattern: registration.pattern,
    };
  }
  if (
    isRecord(registration) &&
    "load" in registration &&
    typeof registration.load === "function" &&
    typeof registration.pattern === "string"
  ) {
    return {
      routeId,
      load: registration.load as LazyBrowserRoute["load"],
      pattern: registration.pattern,
    };
  }
  return undefined;
}

function asLayouts(
  value: unknown,
): readonly ComponentType<any>[] {
  if (!Array.isArray(value)) return [];
  if (value.some((item) => typeof item !== "function")) {
    throw new Error("GoBeyond browser route layouts must be components.");
  }
  return value as ComponentType<any>[];
}

/** Normalize an eager registry entry or loaded module into page + layouts. */
export function browserRouteFromModule(
  module: BrowserRouteModule,
): ResolvedBrowserRoute {
  if (typeof module === "function") {
    return { page: module, layouts: [] };
  }
  if (!isRecord(module)) {
    throw new Error("GoBeyond browser route module is invalid.");
  }
  const page = module.page ?? module.default;
  if (typeof page !== "function") {
    throw new Error("GoBeyond browser route module has no page component.");
  }
  return { page, layouts: asLayouts(module.layouts) };
}

export function routeParts(
  registration: BrowserRouteRegistration | undefined,
): ResolvedBrowserRoute | undefined {
  if (typeof registration === "function") {
    return { page: registration, layouts: [] };
  }
  if (
    isRecord(registration) &&
    ("page" in registration || "component" in registration)
  ) {
    const page = (registration.page ?? registration.component) as
      | ComponentType<any>
      | undefined;
    if (typeof page !== "function") return undefined;
    return { page, layouts: asLayouts(registration.layouts) };
  }
  return undefined;
}

export function routeComponent(
  registration: BrowserRouteRegistration | undefined,
): ComponentType<any> | undefined {
  return routeParts(registration)?.page;
}

/**
 * Compose outermost→innermost layouts around the page. React preserves layout
 * instances across soft navigation when shared layout module identities match.
 */
export function composeRouteElement(
  route: ResolvedBrowserRoute,
  props: Record<string, unknown>,
): ReactElement {
  let element: ReactElement = createElement(route.page, props);
  for (let index = route.layouts.length - 1; index >= 0; index -= 1) {
    element = createElement(route.layouts[index], props, element);
  }
  return element;
}

/** Longest shared layout prefix length (by component identity). */
export function commonLayoutPrefixLength(
  current: readonly ComponentType<any>[],
  next: readonly ComponentType<any>[],
): number {
  const limit = Math.min(current.length, next.length);
  let index = 0;
  while (index < limit && current[index] === next[index]) {
    index += 1;
  }
  return index;
}

const pendingRouteModules = new WeakMap<object, Promise<ResolvedBrowserRoute>>();

/** Resolve an eager or lazy route, sharing only in-flight/successful imports. */
export async function resolveBrowserRoute(
  registration: BrowserRouteRegistration | undefined,
): Promise<ResolvedBrowserRoute | undefined> {
  const eager = routeParts(registration);
  if (eager) return eager;
  if (!isRecord(registration) || typeof registration.load !== "function") {
    return undefined;
  }
  const key = registration as object;
  const existing = pendingRouteModules.get(key);
  if (existing) return existing;
  const load = registration.load as LazyBrowserRoute["load"];
  const promise = Promise.resolve()
    .then(() => load())
    .then((loaded) => browserRouteFromModule(loaded))
    .catch((error: unknown) => {
      pendingRouteModules.delete(key);
      throw error;
    });
  pendingRouteModules.set(key, promise);
  return promise;
}

/** Resolve the page component from an eager or lazy route registration. */
export async function resolveRouteComponent(
  registration: BrowserRouteRegistration | undefined,
): Promise<ComponentType<any> | undefined> {
  const route = await resolveBrowserRoute(registration);
  return route?.page;
}

function pathSegments(path: string): string[] {
  const normalized = path.length > 1 ? path.replace(/\/+$/, "") : path;
  return normalized === "/" ? [] : normalized.replace(/^\//, "").split("/");
}

function patternScore(pattern: string, pathname: string): number | undefined {
  const patterns = pathSegments(pattern);
  const paths = pathSegments(pathname);
  let score = 0;
  let pathIndex = 0;
  for (let index = 0; index < patterns.length; index += 1) {
    const segment = patterns[index];
    const optionalCatchAll = /^\[\[\.\.\.[^\]]+\]\]$/.test(segment);
    const catchAll = /^\[\.\.\.[^\]]+\]$/.test(segment);
    const dynamic = /^\[[^\]]+\]$/.test(segment);
    if (optionalCatchAll || catchAll) {
      if (index !== patterns.length - 1) return undefined;
      if (catchAll && pathIndex >= paths.length) return undefined;
      return score + (optionalCatchAll ? 1 : 2);
    }
    if (pathIndex >= paths.length) return undefined;
    if (dynamic) {
      score += 10;
    } else if (segment === paths[pathIndex]) {
      score += 100;
    } else {
      return undefined;
    }
    pathIndex += 1;
  }
  return pathIndex === paths.length ? score + 1_000 : undefined;
}

/** Match a public pathname with the same bracket patterns as the route manifest. */
export function matchBrowserRoute(
  pathname: string,
  routes: RouteRegistry,
): MatchedBrowserRoute | undefined {
  let best: { route: MatchedBrowserRoute; score: number } | undefined;
  for (const [routeId, registration] of Object.entries(routes)) {
    const route = routeDefinition(routeId, registration);
    if (!route) continue;
    const score = patternScore(route.pattern, pathname);
    if (score === undefined || (best && best.score >= score)) continue;
    best = { route, score };
  }
  return best?.route;
}

function replaceSingleton(
  targetDocument: Document,
  selector: string,
  create: () => HTMLElement,
  value: string | undefined,
  assign: (element: HTMLElement, value: string) => void,
): void {
  const existing = [...targetDocument.head.querySelectorAll<HTMLElement>(selector)];
  if (value === undefined || value.length === 0) {
    existing.forEach((element) => element.remove());
    return;
  }
  const element = existing.shift() ?? create();
  assign(element, value);
  existing.forEach((extra) => extra.remove());
  if (!element.isConnected) targetDocument.head.append(element);
}

function replaceMetaGroup(
  targetDocument: Document,
  attribute: "name" | "property",
  key: string,
  values: readonly string[],
): void {
  const selector = `meta[${attribute}="${key}"]`;
  targetDocument.head.querySelectorAll(selector).forEach((element) => element.remove());
  for (const value of values) {
    if (value.length === 0) continue;
    const meta = targetDocument.createElement("meta");
    meta.setAttribute(attribute, key);
    meta.content = value;
    targetDocument.head.append(meta);
  }
}

function scriptSafeJSON(value: Readonly<Record<string, JsonValue>>): string {
  return JSON.stringify(value)
    .replaceAll("<", "\\u003c")
    .replaceAll(">", "\\u003e")
    .replaceAll("&", "\\u0026")
    .replaceAll("\u2028", "\\u2028")
    .replaceAll("\u2029", "\\u2029");
}

/** Reconcile all route-owned head metadata without injecting markup strings. */
export function applyDocumentMetadata(
  metadata: RuntimeMetadata,
  targetDocument: Document = document,
): void {
  targetDocument.title = metadata.title;
  targetDocument.documentElement.lang = metadata.lang;

  replaceSingleton(
    targetDocument,
    'meta[name="description"]',
    () => targetDocument.createElement("meta"),
    metadata.description,
    (element, value) => {
      element.setAttribute("name", "description");
      element.setAttribute("content", value);
    },
  );
  replaceSingleton(
    targetDocument,
    'meta[name="robots"]',
    () => targetDocument.createElement("meta"),
    metadata.robots,
    (element, value) => {
      element.setAttribute("name", "robots");
      element.setAttribute("content", value);
    },
  );
  replaceSingleton(
    targetDocument,
    'link[rel="canonical"]',
    () => targetDocument.createElement("link"),
    metadata.canonical,
    (element, value) => {
      element.setAttribute("rel", "canonical");
      element.setAttribute("href", value);
    },
  );
  replaceSingleton(
    targetDocument,
    'link[rel="icon"]',
    () => targetDocument.createElement("link"),
    metadata.icons?.icon,
    (element, value) => {
      element.setAttribute("rel", "icon");
      element.setAttribute("href", value);
    },
  );
  replaceSingleton(
    targetDocument,
    'link[rel="apple-touch-icon"]',
    () => targetDocument.createElement("link"),
    metadata.icons?.appleTouch,
    (element, value) => {
      element.setAttribute("rel", "apple-touch-icon");
      element.setAttribute("href", value);
    },
  );

  const openGraph = metadata.openGraph;
  replaceMetaGroup(targetDocument, "property", "og:type", openGraph?.type ? [openGraph.type] : []);
  replaceMetaGroup(
    targetDocument,
    "property",
    "og:title",
    openGraph?.title ? [openGraph.title] : [],
  );
  replaceMetaGroup(
    targetDocument,
    "property",
    "og:description",
    openGraph?.description ? [openGraph.description] : [],
  );
  replaceMetaGroup(targetDocument, "property", "og:url", openGraph?.url ? [openGraph.url] : []);
  replaceMetaGroup(
    targetDocument,
    "property",
    "og:site_name",
    openGraph?.siteName ? [openGraph.siteName] : [],
  );
  replaceMetaGroup(
    targetDocument,
    "property",
    "og:locale",
    openGraph?.locale ? [openGraph.locale] : [],
  );
  replaceMetaGroup(
    targetDocument,
    "property",
    "og:image",
    [...(openGraph?.image ? [openGraph.image.url] : []), ...(openGraph?.images ?? [])],
  );
  replaceMetaGroup(
    targetDocument,
    "property",
    "og:image:width",
    openGraph?.image?.width ? [String(openGraph.image.width)] : [],
  );
  replaceMetaGroup(
    targetDocument,
    "property",
    "og:image:height",
    openGraph?.image?.height ? [String(openGraph.image.height)] : [],
  );
  replaceMetaGroup(
    targetDocument,
    "property",
    "og:image:alt",
    openGraph?.image?.alt ? [openGraph.image.alt] : [],
  );
  replaceMetaGroup(
    targetDocument,
    "property",
    "og:image:type",
    openGraph?.image?.type ? [openGraph.image.type] : [],
  );

  const twitter = metadata.twitter;
  replaceMetaGroup(targetDocument, "name", "twitter:card", twitter?.card ? [twitter.card] : []);
  replaceMetaGroup(targetDocument, "name", "twitter:title", twitter?.title ? [twitter.title] : []);
  replaceMetaGroup(
    targetDocument,
    "name",
    "twitter:description",
    twitter?.description ? [twitter.description] : [],
  );
  replaceMetaGroup(targetDocument, "name", "twitter:site", twitter?.site ? [twitter.site] : []);
  replaceMetaGroup(targetDocument, "name", "twitter:image", twitter?.images ?? []);
  replaceMetaGroup(
    targetDocument,
    "name",
    "twitter:image:alt",
    twitter?.imageAlt ? [twitter.imageAlt] : [],
  );

  targetDocument.head
    .querySelectorAll('link[rel="alternate"][hreflang]')
    .forEach((element) => element.remove());
  for (const alternate of metadata.alternates ?? []) {
    const link = targetDocument.createElement("link");
    link.rel = "alternate";
    link.hreflang = alternate.language;
    link.href = alternate.url;
    targetDocument.head.append(link);
  }

  const nonce =
    targetDocument.getElementById("__GOBEYOND_DATA__")?.getAttribute("nonce") ??
    undefined;
  targetDocument.head
    .querySelectorAll('script[type="application/ld+json"]')
    .forEach((element) => element.remove());
  for (const document of metadata.jsonLd ?? []) {
    const script = targetDocument.createElement("script");
    script.type = "application/ld+json";
    if (nonce) script.setAttribute("nonce", nonce);
    script.textContent = scriptSafeJSON(document);
    targetDocument.head.append(script);
  }
}

function targetAnchor(
  event: Event,
  targetWindow: Window & typeof globalThis,
): HTMLAnchorElement | undefined {
  const target = event.target;
  if (!(target instanceof targetWindow.Element)) return undefined;
  const anchor = target.closest<HTMLAnchorElement>("a[href]");
  if (
    !anchor ||
    anchor.hasAttribute("download") ||
    (anchor.target !== "" && anchor.target.toLowerCase() !== "_self")
  ) {
    return undefined;
  }
  return anchor;
}

function eventAnchor(
  event: MouseEvent,
  targetWindow: Window & typeof globalThis,
): HTMLAnchorElement | undefined {
  if (
    event.defaultPrevented ||
    event.button !== 0 ||
    event.metaKey ||
    event.ctrlKey ||
    event.shiftKey ||
    event.altKey
  ) {
    return undefined;
  }
  return targetAnchor(event, targetWindow);
}

function historyMarker(
  state: unknown,
): NavigationHistoryState | undefined {
  if (!isRecord(state) || !isRecord(state[HISTORY_STATE_KEY])) return undefined;
  const marker = state[HISTORY_STATE_KEY];
  if (
    typeof marker.buildId !== "string" ||
    typeof marker.path !== "string" ||
    typeof marker.scrollX !== "number" ||
    typeof marker.scrollY !== "number"
  ) {
    return undefined;
  }
  return marker as unknown as NavigationHistoryState;
}

function withHistoryMarker(
  state: unknown,
  marker: NavigationHistoryState,
): Record<string, unknown> {
  return {
    ...(isRecord(state) ? state : {}),
    [HISTORY_STATE_KEY]: marker,
  };
}

function currentPath(targetWindow: Window): string {
  return (
    targetWindow.location.pathname +
    targetWindow.location.search +
    targetWindow.location.hash
  );
}

function safeDocumentURL(value: string, fallback: URL): string {
  try {
    const target = new URL(value, fallback);
    if (target.protocol === "http:" || target.protocol === "https:") {
      return target.href;
    }
  } catch {
    // Fall back to the requested public route for malformed locations.
  }
  return fallback.href;
}

function ensureAnnouncer(targetDocument: Document): HTMLElement {
  const existing = targetDocument.getElementById(NAVIGATION_ANNOUNCER_ID);
  if (existing) return existing;
  const announcer = targetDocument.createElement("div");
  announcer.id = NAVIGATION_ANNOUNCER_ID;
  announcer.setAttribute("role", "status");
  announcer.setAttribute("aria-live", "polite");
  announcer.setAttribute("aria-atomic", "true");
  Object.assign(announcer.style, {
    position: "absolute",
    width: "1px",
    height: "1px",
    padding: "0",
    margin: "-1px",
    overflow: "hidden",
    clip: "rect(0, 0, 0, 0)",
    whiteSpace: "nowrap",
    border: "0",
  });
  targetDocument.body.append(announcer);
  return announcer;
}

function focusRouteContent(rootElement: HTMLElement): void {
  const target =
    rootElement.querySelector<HTMLElement>("main, [role=main], h1") ?? rootElement;
  const temporaryTabIndex = !target.hasAttribute("tabindex");
  if (temporaryTabIndex) target.tabIndex = -1;
  target.focus({ preventScroll: true });
  if (temporaryTabIndex) {
    target.addEventListener("blur", () => target.removeAttribute("tabindex"), {
      once: true,
    });
  }
}

function isAbort(error: unknown): boolean {
  return isRecord(error) && error.name === "AbortError";
}

type ComponentResolution =
  | { resolved: ResolvedBrowserRoute | undefined }
  | { error: unknown };

export function createSoftNavigation(
  options: SoftNavigationOptions,
): SoftNavigationController {
  const defaultView = options.document.defaultView;
  const hasRoutePatterns = Object.entries(options.routes).some(
    ([routeId, registration]) =>
      routeDefinition(routeId, registration) !== undefined,
  );
  if (!defaultView || !hasRoutePatterns) {
    return disabledSoftNavigationController();
  }
  const targetWindow: Window & typeof globalThis = defaultView;

  const render =
    options.render ??
    ((route, props) => composeRouteElement(route, props));
  const hardNavigate =
    options.hardNavigate ?? ((url: string) => targetWindow.location.assign(url));
  const scrollTo =
    options.scrollTo ?? ((x: number, y: number) => targetWindow.scrollTo(x, y));
  const mismatchEnvironment = options.mismatchEnvironment ?? {
    location: targetWindow.location,
    sessionStorage: targetWindow.sessionStorage,
  };
  const onUpdateRequired =
    options.onUpdateRequired ??
    ((error: BuildMismatchError) => {
      if (!shouldShowUpdateRequiredUI(options)) return;
      renderUpdateRequired(error, options.document);
    });
  const previousScrollRestoration = targetWindow.history.scrollRestoration;
  targetWindow.history.scrollRestoration = "manual";
  let active: AbortController | undefined;
  let destroyed = false;
  const listeners = new Set<NavigationLifecycleListener>();
  const routerCache = options.routerCache ?? createRouterCache(options.routerCacheOptions);
  const pendingWarms = new Map<string, Promise<void>>();

  function emit(event: NavigationLifecycleEvent): void {
    if (event.type === "start") {
      options.onNavigationStart?.(event);
    } else {
      options.onNavigationSettled?.(event);
    }
    emitNavigationLifecycle(event);
    for (const listener of listeners) {
      listener(event);
    }
  }

  function marker(x = targetWindow.scrollX, y = targetWindow.scrollY): NavigationHistoryState {
    return {
      buildId: options.buildId,
      path: currentPath(targetWindow),
      scrollX: x,
      scrollY: y,
    };
  }

  function saveScroll(): void {
    targetWindow.history.replaceState(
      withHistoryMarker(targetWindow.history.state, marker()),
      "",
    );
  }

  const initialMarker = historyMarker(targetWindow.history.state);
  if (!initialMarker || initialMarker.buildId !== options.buildId) saveScroll();

  async function navigate(
    target: string | URL,
    navigationOptions: NavigateOptions = {},
  ): Promise<RuntimeNavigationPayload | undefined> {
    if (destroyed) return undefined;
    const url = new URL(target, targetWindow.location.href);
    const route = matchBrowserRoute(url.pathname, options.routes);
    if (!route) {
      hardNavigate(url.href);
      return undefined;
    }

    active?.abort();
    const controller = new AbortController();
    active = controller;
    const href = url.href;
    emit({ type: "start", url: href, routeId: route.routeId });

    try {
      const cached = routerCache.get(routerCache.keyFor(url));
      if (cached && cached.routeId === route.routeId) {
        return await renderNavigationResult(
          url,
          href,
          route,
          controller,
          navigationOptions,
          cached,
          startComponentResolve(route),
        );
      }
      return await performNavigation(
        url,
        href,
        route,
        controller,
        navigationOptions,
      );
    } catch (error) {
      if (!isAbort(error) && !destroyed) {
        emit({
          type: "error",
          url: href,
          routeId: route.routeId,
          error,
        });
      }
      if (error instanceof BuildMismatchError) routerCache.clear();
      throw error;
    }
  }

  function startComponentResolve(
    route: MatchedBrowserRoute,
  ): Promise<ComponentResolution> {
    return resolveBrowserRoute(options.routes[route.routeId]).then(
      (resolved) => ({ resolved } as const),
      (error: unknown) => ({ error } as const),
    );
  }

  async function performNavigation(
    url: URL,
    href: string,
    route: MatchedBrowserRoute,
    controller: AbortController,
    navigationOptions: NavigateOptions,
  ): Promise<RuntimeNavigationPayload | undefined> {
    const runtimePath =
      `/_gobeyond/builds/${encodeURIComponent(options.buildId)}/runtime/` +
      encodeURIComponent(route.routeId);
    const runtimeURL = new URL(runtimePath, targetWindow.location.origin);
    runtimeURL.searchParams.set("path", url.pathname + url.search);

    const componentResult = startComponentResolve(route);

    const settleHard = (): void => {
      if (active !== controller || destroyed) return;
      emit({ type: "success", url: href, routeId: route.routeId });
    };

    let response: Response;
    try {
      response = await fetchWithBuildGuard(
        runtimeURL,
        {
          method: "GET",
          headers: { accept: "application/json" },
          redirect: "manual",
          signal: controller.signal,
        },
        {
          buildId: options.buildId,
          fetch: options.fetch,
          environment: mismatchEnvironment,
          onUpdateRequired,
        },
      );
    } catch (error) {
      controller.abort();
      throw error;
    }
    if (response.type === "opaqueredirect") {
      settleHard();
      hardNavigate(url.href);
      return undefined;
    }
    const location = response.headers.get("location");
    if (response.redirected) {
      settleHard();
      hardNavigate(safeDocumentURL(response.url || location || url.href, url));
      return undefined;
    }
    if (location) {
      settleHard();
      hardNavigate(safeDocumentURL(location, url));
      return undefined;
    }
    const isRedirectStatus = response.status >= 300 && response.status < 400;
    const isJSON = response.headers
      .get("content-type")
      ?.toLowerCase()
      .startsWith("application/json");
    if ((isRedirectStatus && !isJSON) || (!response.ok && !isRedirectStatus)) {
      settleHard();
      hardNavigate(url.href);
      return undefined;
    }
    const payload = parseRuntimeNavigationPayload(await response.text());
    if (payload.buildId !== options.buildId) {
      throw handleBuildMismatch(options.buildId, payload.buildId, {
        environment: mismatchEnvironment,
        onUpdateRequired,
      });
    }
    if (payload.routeId !== route.routeId) {
      throw new Error(
        `GoBeyond runtime returned route ${payload.routeId} for ${route.routeId}.`,
      );
    }
    if (payload.result.kind === "redirect") {
      if (!payload.result.redirectTo) {
        throw new Error("GoBeyond runtime redirect is missing redirectTo.");
      }
      settleHard();
      hardNavigate(new URL(payload.result.redirectTo, url).href);
      return payload;
    }
    if (payload.result.kind !== "ok") {
      settleHard();
      hardNavigate(url.href);
      return payload;
    }
    if (!payload.result.metadata) {
      throw new Error("GoBeyond runtime result is missing metadata.");
    }
    // Cache before rendering: a payload that reaches here is validated (kind
    // "ok", metadata present, matching build/route), which is exactly the
    // shape `navigate`'s cache-hit fast path expects to replay later. `set`
    // itself no-ops for private/no-store CachePolicy results.
    routerCache.set(routerCache.keyFor(url), payload);
    return renderNavigationResult(
      url,
      href,
      route,
      controller,
      navigationOptions,
      payload,
      componentResult,
    );
  }

  /**
   * Render an already-fetched (or cache-hit) "ok" payload: resolve the route
   * component, commit the DOM/history/scroll/focus side effects, and emit
   * the success lifecycle event. Shared by `performNavigation` (fresh fetch)
   * and `navigate`'s Router Cache fast path (see `routerCache.get` above),
   * so a cache hit behaves identically to a network hit from here on.
   */
  async function renderNavigationResult(
    url: URL,
    href: string,
    route: MatchedBrowserRoute,
    controller: AbortController,
    navigationOptions: NavigateOptions,
    payload: RuntimeNavigationPayload,
    componentResult: Promise<ComponentResolution>,
  ): Promise<RuntimeNavigationPayload | undefined> {
    const metadata = payload.result.metadata;
    if (!metadata) {
      throw new Error("GoBeyond runtime result is missing metadata.");
    }
    if (active !== controller || destroyed) return undefined;
    const resolvedResult = await componentResult;
    if ("error" in resolvedResult) {
      controller.abort();
      throw resolvedResult.error;
    }
    const resolved = resolvedResult.resolved;
    if (!resolved) {
      controller.abort();
      throw new Error(`No browser route registered for ${route.routeId}.`);
    }
    if (active !== controller || destroyed) return undefined;

    const mode = navigationOptions.history ?? "push";
    if (mode === "push") saveScroll();
    flushSync(() => {
      options.root.render(render(resolved, payload.result.props));
    });
    options.rootElement.dataset.gobeyondRoute = route.routeId;
    applyDocumentMetadata(metadata, options.document);

    const nextMarker: NavigationHistoryState = {
      buildId: options.buildId,
      path: url.pathname + url.search + url.hash,
      scrollX: navigationOptions.scroll?.x ?? 0,
      scrollY: navigationOptions.scroll?.y ?? 0,
    };
    if (mode === "push") {
      targetWindow.history.pushState(
        withHistoryMarker({}, nextMarker),
        "",
        url.href,
      );
    } else if (mode === "replace") {
      targetWindow.history.replaceState(
        withHistoryMarker(targetWindow.history.state, nextMarker),
        "",
        url.href,
      );
    }

    if (!navigationOptions.silent) {
      const announcer = ensureAnnouncer(options.document);
      announcer.textContent = `Navigated to ${metadata.title}`;
      focusRouteContent(options.rootElement);
    }
    const restored = navigationOptions.scroll;
    if (restored) {
      scrollTo(restored.x, restored.y);
    } else if (url.hash) {
      let id = url.hash.slice(1);
      try {
        id = decodeURIComponent(id);
      } catch {
        // An invalid escape is still a valid opaque fragment identifier.
      }
      const hashTarget = options.document.getElementById(id);
      if (hashTarget && typeof hashTarget.scrollIntoView === "function") {
        hashTarget.scrollIntoView();
      } else {
        scrollTo(0, 0);
      }
    } else {
      scrollTo(0, 0);
    }
    emit({
      type: "success",
      url: href,
      routeId: route.routeId,
      payload,
    });
    return payload;
  }

  async function refresh(
    paths?: readonly string[],
  ): Promise<RuntimeNavigationPayload | undefined> {
    if (destroyed) return undefined;
    const url = new URL(targetWindow.location.href);
    // Drop the Router Cache before deciding whether to re-render: `paths`
    // may name routes other than the one currently mounted, and an omitted
    // `paths` (no narrower signal for "something changed") clears
    // everything rather than risk replaying stale data.
    routerCache.invalidatePaths(paths ?? []);
    if (paths && paths.length > 0 && !pathsIncludePathname(paths, url.pathname)) {
      return undefined;
    }
    const route = matchBrowserRoute(url.pathname, options.routes);
    if (!route) return undefined;

    active?.abort();
    const controller = new AbortController();
    active = controller;
    const href = url.href;
    emit({ type: "start", url: href, routeId: route.routeId });

    try {
      return await performNavigation(url, href, route, controller, {
        history: "replace",
        scroll: { x: targetWindow.scrollX, y: targetWindow.scrollY },
        silent: true,
      });
    } catch (error) {
      if (!isAbort(error) && !destroyed) {
        emit({ type: "error", url: href, routeId: route.routeId, error });
      }
      if (error instanceof BuildMismatchError) routerCache.clear();
      throw error;
    }
  }

  function clearRouterCache(paths?: readonly string[]): void {
    routerCache.invalidatePaths(paths ?? []);
  }

  /**
   * Best-effort fetch of a route's runtime JSON into the Router Cache,
   * deduped per key so rapid repeated hover/focus prefetches of the same
   * link only issue one request. Silently no-ops on any failure (bad
   * response, build/route mismatch, non-"ok" kind) or when the policy is
   * private/no-store - `routerCache.set` already enforces the latter, this
   * just avoids doing the fetch's error handling twice.
   */
  function warmRouterCache(url: URL, route: MatchedBrowserRoute): Promise<void> {
    const key = routerCache.keyFor(url);
    if (routerCache.get(key)) return Promise.resolve();
    const pending = pendingWarms.get(key);
    if (pending) return pending;

    const runtimePath =
      `/_gobeyond/builds/${encodeURIComponent(options.buildId)}/runtime/` +
      encodeURIComponent(route.routeId);
    const runtimeURL = new URL(runtimePath, targetWindow.location.origin);
    runtimeURL.searchParams.set("path", url.pathname + url.search);
    const request = options.fetch ?? targetWindow.fetch.bind(targetWindow);

    const warm = (async () => {
      const response = await request(runtimeURL, {
        method: "GET",
        headers: { accept: "application/json", [BUILD_ID_HEADER]: options.buildId },
        redirect: "manual",
      });
      if (!response.ok) return;
      if (!response.headers.get("content-type")?.toLowerCase().startsWith("application/json")) {
        return;
      }
      const payload = parseRuntimeNavigationPayload(await response.text());
      if (
        payload.buildId !== options.buildId ||
        payload.routeId !== route.routeId ||
        payload.result.kind !== "ok"
      ) {
        return;
      }
      routerCache.set(key, payload);
    })().catch(() => {
      // Prefetch data-warming is opportunistic; a real navigation retries.
    });
    pendingWarms.set(key, warm);
    void warm.finally(() => {
      if (pendingWarms.get(key) === warm) pendingWarms.delete(key);
    });
    return warm;
  }

  async function prefetch(target: string | URL): Promise<void> {
    if (destroyed) return;
    const url = new URL(target, targetWindow.location.href);
    if (url.origin !== targetWindow.location.origin) return;
    const route = matchBrowserRoute(url.pathname, options.routes);
    if (!route) return;
    await Promise.all([
      resolveBrowserRoute(options.routes[route.routeId]),
      warmRouterCache(url, route),
    ]);
  }

  function onClick(event: MouseEvent): void {
    const anchor = eventAnchor(event, targetWindow);
    if (!anchor) return;
    const url = new URL(anchor.href, targetWindow.location.href);
    if (
      url.origin !== targetWindow.location.origin ||
      (url.pathname === targetWindow.location.pathname &&
        url.search === targetWindow.location.search &&
        url.hash.length > 0)
    ) {
      return;
    }
    if (!matchBrowserRoute(url.pathname, options.routes)) return;
    event.preventDefault();
    void navigate(url).catch((error: unknown) => {
      if (isAbort(error)) return;
      options.onNavigationError?.(error);
      if (!(error instanceof BuildMismatchError)) hardNavigate(url.href);
    });
  }

  function onPopState(event: PopStateEvent): void {
    const saved = historyMarker(event.state);
    const scroll =
      saved?.buildId === options.buildId
        ? { x: saved.scrollX, y: saved.scrollY }
        : undefined;
    const url = targetWindow.location.href;
    void navigate(url, { history: "pop", scroll }).catch((error: unknown) => {
      if (isAbort(error)) return;
      options.onNavigationError?.(error);
      if (!(error instanceof BuildMismatchError)) hardNavigate(url);
    });
  }

  function onPrefetch(event: Event): void {
    const anchor = targetAnchor(event, targetWindow);
    if (!anchor) return;
    void prefetch(anchor.href).catch(() => {
      // Prefetch is opportunistic. A later navigation retries failed imports.
    });
  }

  options.document.addEventListener("click", onClick);
  options.document.addEventListener("pointerover", onPrefetch);
  options.document.addEventListener("focusin", onPrefetch);
  targetWindow.addEventListener("popstate", onPopState);
  targetWindow.addEventListener("scroll", saveScroll, { passive: true });
  targetWindow.addEventListener("pagehide", saveScroll);

  const publicController: SoftNavigationController = {
    navigate,
    prefetch,
    refresh,
    clearRouterCache,
    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    destroy() {
      if (destroyed) return;
      destroyed = true;
      active?.abort();
      listeners.clear();
      routerCache.clear();
      pendingWarms.clear();
      options.document.removeEventListener("click", onClick);
      options.document.removeEventListener("pointerover", onPrefetch);
      options.document.removeEventListener("focusin", onPrefetch);
      targetWindow.removeEventListener("popstate", onPopState);
      targetWindow.removeEventListener("scroll", saveScroll);
      targetWindow.removeEventListener("pagehide", saveScroll);
      targetWindow.history.scrollRestoration = previousScrollRestoration;
      if (activeSoftNavigation === publicController) {
        activeSoftNavigation = undefined;
      }
    },
  };
  activeSoftNavigation = publicController;
  return publicController;
}

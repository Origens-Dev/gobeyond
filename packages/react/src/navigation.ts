import { createElement, type ComponentType, type ReactElement } from "react";
import { flushSync } from "react-dom";
import type { Root } from "react-dom/client";
import {
  BuildMismatchError,
  fetchWithBuildGuard,
  handleBuildMismatch,
  renderUpdateRequired,
  type BuildMismatchEnvironment,
} from "./build-mismatch.js";
import type {
  AlternateLanguage,
  JsonValue,
  OpenGraphMetadata,
  TwitterMetadata,
} from "./seo.js";

export const NAVIGATION_ANNOUNCER_ID = "__gobeyond_route_announcer__";

export interface BrowserRoute {
  component: ComponentType<any>;
  pattern: string;
}

export type BrowserRouteRegistration = ComponentType<any> | BrowserRoute;

// The generated registry erases individual prop types only at the route
// boundary. Each imported page and layout remains strongly typed beforehand.
export type RouteRegistry = Readonly<
  Record<string, BrowserRouteRegistration>
>;

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
  jsonLd?: readonly Readonly<Record<string, JsonValue>>[];
}

export type RuntimeResultKind =
  | "ok"
  | "redirect"
  | "not_found"
  | "public_error"
  | "internal_error";

export interface RuntimeNavigationResult {
  kind: RuntimeResultKind;
  props: Record<string, unknown>;
  metadata?: RuntimeMetadata;
  status?: number;
  redirectTo?: string;
  errorCode?: string;
  message?: string;
}

export interface RuntimeNavigationPayload {
  apiVersion: "gobeyond.render/v1alpha1";
  buildId: string;
  routeId: string;
  result: RuntimeNavigationResult;
}

export interface MatchedBrowserRoute extends BrowserRoute {
  routeId: string;
}

export type NavigationHistoryMode = "push" | "replace" | "pop";

export interface NavigateOptions {
  history?: NavigationHistoryMode;
  scroll?: { x: number; y: number };
}

export interface SoftNavigationController {
  navigate(
    target: string | URL,
    options?: NavigateOptions,
  ): Promise<RuntimeNavigationPayload | undefined>;
  destroy(): void;
}

export interface SoftNavigationOptions {
  buildId: string;
  routes: RouteRegistry;
  root: Root;
  rootElement: HTMLElement;
  document: Document;
  render?: (
    page: ComponentType<Record<string, unknown>>,
    props: Record<string, unknown>,
  ) => ReactElement;
  fetch?: typeof globalThis.fetch;
  mismatchEnvironment?: BuildMismatchEnvironment;
  onUpdateRequired?: (error: BuildMismatchError) => void;
  onNavigationError?: (error: unknown) => void;
  hardNavigate?: (url: string) => void;
  scrollTo?: (x: number, y: number) => void;
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

  return {
    lang: requiredString(value, "lang", "GoBeyond runtime metadata"),
    title: requiredString(value, "title", "GoBeyond runtime metadata"),
    description: optionalString(value, "description", "metadata"),
    canonical: optionalString(value, "canonical", "metadata"),
    robots: optionalString(value, "robots", "metadata"),
    alternates,
    openGraph: parseOpenGraph(value.openGraph),
    twitter: parseTwitter(value.twitter),
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
    },
  };
}

function routeDefinition(
  routeId: string,
  registration: BrowserRouteRegistration,
): MatchedBrowserRoute | undefined {
  if (
    isRecord(registration) &&
    "component" in registration &&
    typeof registration.pattern === "string"
  ) {
    return {
      routeId,
      component: registration.component as ComponentType<any>,
      pattern: registration.pattern,
    };
  }
  return undefined;
}

export function routeComponent(
  registration: BrowserRouteRegistration | undefined,
): ComponentType<any> | undefined {
  if (typeof registration === "function") return registration;
  if (isRecord(registration) && "component" in registration) {
    return registration.component as ComponentType<any>;
  }
  return undefined;
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
  replaceMetaGroup(targetDocument, "property", "og:image", openGraph?.images ?? []);

  const twitter = metadata.twitter;
  replaceMetaGroup(targetDocument, "name", "twitter:card", twitter?.card ? [twitter.card] : []);
  replaceMetaGroup(targetDocument, "name", "twitter:title", twitter?.title ? [twitter.title] : []);
  replaceMetaGroup(
    targetDocument,
    "name",
    "twitter:description",
    twitter?.description ? [twitter.description] : [],
  );
  replaceMetaGroup(targetDocument, "name", "twitter:image", twitter?.images ?? []);

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

export function createSoftNavigation(
  options: SoftNavigationOptions,
): SoftNavigationController {
  const defaultView = options.document.defaultView;
  const hasRoutePatterns = Object.entries(options.routes).some(
    ([routeId, registration]) =>
      routeDefinition(routeId, registration) !== undefined,
  );
  if (!defaultView || !hasRoutePatterns) {
    return {
      async navigate() {
        return undefined;
      },
      destroy() {},
    };
  }
  const targetWindow: Window & typeof globalThis = defaultView;

  const render = options.render ?? ((Page, props) => createElement(Page, props));
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
    ((error: BuildMismatchError) => renderUpdateRequired(error, options.document));
  const previousScrollRestoration = targetWindow.history.scrollRestoration;
  targetWindow.history.scrollRestoration = "manual";
  let active: AbortController | undefined;
  let destroyed = false;

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
    const runtimePath =
      `/_gobeyond/runtime/${encodeURIComponent(options.buildId)}/` +
      encodeURIComponent(route.routeId);
    const runtimeURL = new URL(runtimePath, targetWindow.location.origin);
    runtimeURL.searchParams.set("path", url.pathname + url.search);

    const response = await fetchWithBuildGuard(
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
    if (response.type === "opaqueredirect") {
      hardNavigate(url.href);
      return undefined;
    }
    const location = response.headers.get("location");
    if (response.redirected) {
      hardNavigate(safeDocumentURL(response.url || location || url.href, url));
      return undefined;
    }
    if (location) {
      hardNavigate(safeDocumentURL(location, url));
      return undefined;
    }
    const isRedirectStatus = response.status >= 300 && response.status < 400;
    const isJSON = response.headers
      .get("content-type")
      ?.toLowerCase()
      .startsWith("application/json");
    if ((isRedirectStatus && !isJSON) || (!response.ok && !isRedirectStatus)) {
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
      hardNavigate(new URL(payload.result.redirectTo, url).href);
      return payload;
    }
    if (payload.result.kind !== "ok") {
      hardNavigate(url.href);
      return payload;
    }
    if (!payload.result.metadata) {
      throw new Error("GoBeyond runtime result is missing metadata.");
    }
    if (active !== controller || destroyed) return undefined;

    const mode = navigationOptions.history ?? "push";
    if (mode === "push") saveScroll();
    flushSync(() => {
      options.root.render(
        render(
          route.component as ComponentType<Record<string, unknown>>,
          payload.result.props,
        ),
      );
    });
    options.rootElement.dataset.gobeyondRoute = route.routeId;
    applyDocumentMetadata(payload.result.metadata, options.document);

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

    const announcer = ensureAnnouncer(options.document);
    announcer.textContent = `Navigated to ${payload.result.metadata.title}`;
    focusRouteContent(options.rootElement);
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
    return payload;
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

  options.document.addEventListener("click", onClick);
  targetWindow.addEventListener("popstate", onPopState);
  targetWindow.addEventListener("scroll", saveScroll, { passive: true });
  targetWindow.addEventListener("pagehide", saveScroll);

  return {
    navigate,
    destroy() {
      if (destroyed) return;
      destroyed = true;
      active?.abort();
      options.document.removeEventListener("click", onClick);
      targetWindow.removeEventListener("popstate", onPopState);
      targetWindow.removeEventListener("scroll", saveScroll);
      targetWindow.removeEventListener("pagehide", saveScroll);
      targetWindow.history.scrollRestoration = previousScrollRestoration;
    },
  };
}

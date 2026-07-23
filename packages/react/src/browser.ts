import {
  createElement,
  type ComponentType,
  type ReactElement,
} from "react";
import { hydrateRoot, type RootOptions } from "react-dom/client";
import {
  handleAssetLoadFailure,
  markBuildHealthy,
} from "./build-mismatch.js";
import {
  createSoftNavigation,
  resolveRouteComponent,
  routeComponent,
  type RouteRegistry,
  type SoftNavigationOptions,
} from "./navigation.js";
import { assertPinnedReactVersions } from "./version.js";

export {
  BUILD_ERROR_HEADER,
  BUILD_ID_HEADER,
  BUILD_MISMATCH_CODE,
  BuildMismatchError,
  UPDATE_REQUIRED_ELEMENT_ID,
  fetchWithBuildGuard,
  handleAssetLoadFailure,
  handleBuildMismatch,
  markBuildHealthy,
  renderUpdateRequired,
  type BuildAwareFetchOptions,
  type BuildMismatchBody,
  type BuildMismatchEnvironment,
  type BuildMismatchOptions,
  type ReloadLocation,
  type StringStorage,
} from "./build-mismatch.js";
export {
  NAVIGATION_ANNOUNCER_ID,
  applyDocumentMetadata,
  createSoftNavigation,
  matchBrowserRoute,
  parseRuntimeNavigationPayload,
  resolveRouteComponent,
  routeComponent,
  type BrowserRoute,
  type BrowserRouteRegistration,
  type BrowserRouteModule,
  type LazyBrowserRoute,
  type MatchedBrowserRoute,
  type NavigateOptions,
  type NavigationHistoryMode,
  type RouteRegistry,
  type RuntimeMetadata,
  type RuntimeNavigationPayload,
  type RuntimeNavigationResult,
  type RuntimeResultKind,
  type SoftNavigationController,
  type SoftNavigationOptions,
} from "./navigation.js";
export { PINNED_REACT_VERSION, assertPinnedReactVersions } from "./version.js";

export const BROWSER_PROTOCOL_VERSION = "gobeyond.render/v1alpha1" as const;
export const DEFAULT_DATA_ELEMENT_ID = "__GOBEYOND_DATA__";
export const DEFAULT_ROOT_SELECTOR = "#__gobeyond";

export interface BootstrapPayload<Props = unknown> {
  apiVersion: typeof BROWSER_PROTOCOL_VERSION;
  buildId: string;
  routeId: string;
  props: Props;
}

export interface BootstrapOptions {
  routes: RouteRegistry;
  document?: Document;
  dataElementId?: string;
  rootSelector?: string;
  onRecoverableError?: RootOptions["onRecoverableError"];
  render?: (
    page: ComponentType<Record<string, unknown>>,
    props: Record<string, unknown>,
  ) => ReactElement;
  navigation?: boolean;
  fetch?: SoftNavigationOptions["fetch"];
  mismatchEnvironment?: SoftNavigationOptions["mismatchEnvironment"];
  onUpdateRequired?: SoftNavigationOptions["onUpdateRequired"];
  onNavigationError?: SoftNavigationOptions["onNavigationError"];
  hardNavigate?: SoftNavigationOptions["hardNavigate"];
  scrollTo?: SoftNavigationOptions["scrollTo"];
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function parseBootstrapPayload(text: string): BootstrapPayload {
  const value: unknown = JSON.parse(text);
  if (!isRecord(value)) throw new Error("GoBeyond bootstrap data must be an object.");
  if (value.apiVersion !== BROWSER_PROTOCOL_VERSION) {
    throw new Error(`Unsupported GoBeyond browser protocol: ${String(value.apiVersion)}`);
  }
  if (typeof value.buildId !== "string" || value.buildId.length === 0) {
    throw new Error("GoBeyond bootstrap data is missing buildId.");
  }
  if (typeof value.routeId !== "string" || value.routeId.length === 0) {
    throw new Error("GoBeyond bootstrap data is missing routeId.");
  }
  if (!isRecord(value.props)) {
    throw new Error("GoBeyond bootstrap props must be an object.");
  }
  return value as unknown as BootstrapPayload;
}

export function bootstrap(options: BootstrapOptions) {
  assertPinnedReactVersions();

  const targetDocument = options.document ?? document;
  const payload = readBootstrapPayload(options, targetDocument);
  const page = routeComponent(options.routes[payload.routeId]);
  if (!page) {
    throw new Error(
      `No eager browser route registered for ${payload.routeId}; use bootstrapAsync for lazy routes.`,
    );
  }
  return hydrate(options, targetDocument, payload, page);
}

/** Load the initial route chunk before hydrating server-rendered markup. */
export async function bootstrapAsync(options: BootstrapOptions) {
  assertPinnedReactVersions();
  const targetDocument = options.document ?? document;
  const payload = readBootstrapPayload(options, targetDocument);
  let page: ComponentType<any> | undefined;
  try {
    page = await resolveRouteComponent(options.routes[payload.routeId]);
  } catch (error) {
    options.onNavigationError?.(error);
    const targetWindow = targetDocument.defaultView;
    handleAssetLoadFailure(payload.buildId, {
      environment:
        options.mismatchEnvironment ??
        (targetWindow
          ? {
              location: targetWindow.location,
              sessionStorage: targetWindow.sessionStorage,
            }
          : undefined),
      onUpdateRequired: options.onUpdateRequired,
    });
    return undefined;
  }
  if (!page) {
    throw new Error(`No browser route registered for ${payload.routeId}.`);
  }
  return hydrate(options, targetDocument, payload, page);
}

function readBootstrapPayload(
  options: BootstrapOptions,
  targetDocument: Document,
): BootstrapPayload {
  const dataElement = targetDocument.getElementById(
    options.dataElementId ?? DEFAULT_DATA_ELEMENT_ID,
  );
  if (!dataElement?.textContent) {
    throw new Error("GoBeyond bootstrap data element is missing or empty.");
  }

  return parseBootstrapPayload(dataElement.textContent);
}

function hydrate(
  options: BootstrapOptions,
  targetDocument: Document,
  payload: BootstrapPayload,
  page: ComponentType<any>,
) {
  const rootElement = targetDocument.querySelector<HTMLElement>(
    options.rootSelector ?? DEFAULT_ROOT_SELECTOR,
  );
  if (!rootElement) {
    throw new Error("GoBeyond hydration root was not found.");
  }

  const render = options.render ?? ((Page, props) => createElement(Page, props));
  const root = hydrateRoot(
    rootElement,
    render(page, payload.props as Record<string, unknown>),
    { onRecoverableError: options.onRecoverableError },
  );

  rootElement.dataset.gobeyondBuild = payload.buildId;
  rootElement.dataset.gobeyondRoute = payload.routeId;
  const targetWindow = targetDocument.defaultView;
  if (targetWindow) {
    markBuildHealthy(payload.buildId, {
      sessionStorage: targetWindow.sessionStorage,
    });
  }

  const navigation =
    options.navigation === false
      ? {
          async navigate() {
            return undefined;
          },
          async prefetch() {},
          destroy() {},
        }
      : createSoftNavigation({
          buildId: payload.buildId,
          routes: options.routes,
          root,
          rootElement,
          document: targetDocument,
          render,
          fetch: options.fetch,
          mismatchEnvironment: options.mismatchEnvironment,
          onUpdateRequired: options.onUpdateRequired,
          onNavigationError: options.onNavigationError,
          hardNavigate: options.hardNavigate,
          scrollTo: options.scrollTo,
        });

  return { root, payload, ...navigation };
}

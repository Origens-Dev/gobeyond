import {
  fetchWithBuildGuard,
  type BuildAwareFetchOptions,
} from "./build-mismatch.js";
import { refreshNavigation, type RuntimeNavigationPayload } from "./navigation.js";

/** Matches cache.ActionAPIVersion (cache/envelope.go). */
export const ACTION_API_VERSION = "gobeyond.action/v1alpha1" as const;

export interface ActionRefresh {
  paths?: readonly string[];
  tags?: readonly string[];
}

/** Parsed form of cache.ActionEnvelope (cache/envelope.go). */
export interface ActionEnvelope<Data = unknown> {
  /**
   * Undefined for a pre-envelope `{data, buildId}` response (older server or
   * test fixture) - back-compat per Locked decision 9's rollout.
   */
  apiVersion?: typeof ACTION_API_VERSION;
  buildId: string;
  data: Data;
  refresh?: ActionRefresh;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function optionalStringArray(
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

/**
 * Parse a GoBeyond action response body. Accepts both the frozen envelope
 * shape (`{apiVersion, buildId, data, refresh}`) and the older two-field
 * `{data, buildId}` shape emitted before this phase, which back-compat keeps
 * accepting so older servers and existing test fixtures keep working.
 */
export function parseActionEnvelope<Data = unknown>(text: string): ActionEnvelope<Data> {
  const value: unknown = JSON.parse(text);
  if (!isRecord(value)) {
    throw new Error("GoBeyond action response must be an object.");
  }
  if (typeof value.buildId !== "string" || value.buildId.length === 0) {
    throw new Error("GoBeyond action response is missing buildId.");
  }
  if (value.apiVersion !== undefined && value.apiVersion !== ACTION_API_VERSION) {
    throw new Error(`Unsupported GoBeyond action protocol: ${String(value.apiVersion)}`);
  }

  let refresh: ActionRefresh | undefined;
  if (value.refresh !== undefined) {
    if (!isRecord(value.refresh)) {
      throw new Error("GoBeyond action response refresh must be an object.");
    }
    refresh = {
      paths: optionalStringArray(value.refresh, "paths", "action response refresh"),
      tags: optionalStringArray(value.refresh, "tags", "action response refresh"),
    };
  }

  return {
    apiVersion: value.apiVersion === ACTION_API_VERSION ? ACTION_API_VERSION : undefined,
    buildId: value.buildId,
    data: value.data as Data,
    refresh,
  };
}

export interface RunActionOptions extends BuildAwareFetchOptions {
  /**
   * Applies `refresh.paths` from the parsed envelope. Defaults to
   * `refreshNavigation`, which refreshes through whichever
   * `SoftNavigationController` `bootstrap`/`bootstrapAsync` last installed.
   * Override for tests or when driving soft navigation manually.
   */
  refresh?: (paths: readonly string[]) => Promise<RuntimeNavigationPayload | undefined>;
}

/**
 * Submit a GoBeyond action request, parse its envelope, and - when the
 * action recorded `cache.RevalidatePath` calls - refresh the affected
 * client-side route data. This is the client half of the frozen action
 * envelope (Locked decision 9): server emission and this consumer ship in
 * the same slice so a `RevalidatePath` call is never invalidation-only.
 *
 * Throws on a non-OK HTTP response (after the build-mismatch guard in
 * `fetchWithBuildGuard` has already run) or a malformed envelope body.
 */
export async function runAction<Data = unknown>(
  input: RequestInfo | URL,
  init: RequestInit | undefined,
  options: RunActionOptions,
): Promise<ActionEnvelope<Data>> {
  const response = await fetchWithBuildGuard(input, init, options);
  if (!response.ok) {
    throw new Error(`GoBeyond action request failed with status ${response.status}`);
  }
  const envelope = parseActionEnvelope<Data>(await response.text());
  const paths = envelope.refresh?.paths;
  if (paths && paths.length > 0) {
    const refresh = options.refresh ?? refreshNavigation;
    await refresh(paths);
  }
  return envelope;
}

/** Convenience wrapper around `runAction` for the common JSON-body POST case. */
export async function postAction<Data = unknown, Body = unknown>(
  url: string | URL,
  body: Body,
  options: RunActionOptions,
): Promise<ActionEnvelope<Data>> {
  return runAction<Data>(
    url,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    },
    options,
  );
}

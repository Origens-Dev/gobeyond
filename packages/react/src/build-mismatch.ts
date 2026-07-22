export const BUILD_ID_HEADER = "x-gobeyond-build";
export const BUILD_ERROR_HEADER = "x-gobeyond-error";
export const BUILD_MISMATCH_CODE = "build_mismatch";

const GUARD_PREFIX = "gobeyond:build-mismatch:";
export const UPDATE_REQUIRED_ELEMENT_ID = "__gobeyond_update_required__";

interface MismatchAttempt {
  currentBuildId: string;
  expectedBuildId?: string;
}

export interface BuildMismatchBody {
  error:
    | typeof BUILD_MISMATCH_CODE
    | {
        code: typeof BUILD_MISMATCH_CODE;
        expectedBuildId?: string;
        receivedBuildId?: string;
      };
  buildId?: string;
}

export class BuildMismatchError extends Error {
  readonly code = BUILD_MISMATCH_CODE;
  readonly currentBuildId: string;
  readonly expectedBuildId: string | undefined;
  readonly disposition: "reloading" | "update-required";

  constructor(
    currentBuildId: string,
    expectedBuildId: string | undefined,
    disposition: "reloading" | "update-required",
  ) {
    super(
      disposition === "reloading"
        ? "A new GoBeyond build is available; reloading the document."
        : "The document and server builds are incompatible. Reload or try again later.",
    );
    this.name = "BuildMismatchError";
    this.currentBuildId = currentBuildId;
    this.expectedBuildId = expectedBuildId;
    this.disposition = disposition;
  }
}

export interface ReloadLocation {
  reload(): void;
}

export interface StringStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
  readonly length: number;
  key(index: number): string | null;
}

export interface BuildMismatchEnvironment {
  location: ReloadLocation;
  sessionStorage: StringStorage;
}

export interface BuildMismatchOptions {
  environment?: BuildMismatchEnvironment;
  onUpdateRequired?: (error: BuildMismatchError) => void;
}

/** Render an accessible terminal state after the guarded reload was exhausted. */
export function renderUpdateRequired(
  error: BuildMismatchError,
  targetDocument: Document = document,
): HTMLElement {
  const existing = targetDocument.getElementById(UPDATE_REQUIRED_ELEMENT_ID);
  if (existing) return existing;

  const region = targetDocument.createElement("section");
  region.id = UPDATE_REQUIRED_ELEMENT_ID;
  region.setAttribute("role", "alert");
  region.setAttribute("aria-labelledby", `${UPDATE_REQUIRED_ELEMENT_ID}_title`);

  const title = targetDocument.createElement("h1");
  title.id = `${UPDATE_REQUIRED_ELEMENT_ID}_title`;
  title.textContent = "This site was updated";

  const message = targetDocument.createElement("p");
  message.textContent =
    "Your open page is from an older version. Reload it to continue safely.";

  const reload = targetDocument.createElement("button");
  reload.type = "button";
  reload.textContent = "Reload page";
  reload.addEventListener("click", () => targetDocument.defaultView?.location.reload());

  const detail = targetDocument.createElement("p");
  detail.textContent =
    `Current build: ${error.currentBuildId}; expected build: ` +
    `${error.expectedBuildId ?? "unknown"}.`;

  region.append(title, message, reload, detail);
  targetDocument.body.prepend(region);
  reload.focus();
  return region;
}

function browserEnvironment(): BuildMismatchEnvironment {
  return {
    location: window.location,
    sessionStorage: window.sessionStorage,
  };
}

function mismatchKey(currentBuildId: string): string {
  return `${GUARD_PREFIX}${currentBuildId}`;
}

/** Reload at most once for a particular stale document build. */
export function handleBuildMismatch(
  currentBuildId: string,
  expectedBuildId?: string,
  options: BuildMismatchOptions = {},
): BuildMismatchError {
  const environment = options.environment ?? browserEnvironment();
  const key = mismatchKey(currentBuildId);

  if (environment.sessionStorage.getItem(key) === null) {
    environment.sessionStorage.setItem(
      key,
      JSON.stringify({ currentBuildId, expectedBuildId } satisfies MismatchAttempt),
    );
    const error = new BuildMismatchError(
      currentBuildId,
      expectedBuildId,
      "reloading",
    );
    environment.location.reload();
    return error;
  }

  const error = new BuildMismatchError(
    currentBuildId,
    expectedBuildId,
    "update-required",
  );
  if (options.onUpdateRequired) {
    options.onUpdateRequired(error);
  } else if (typeof document !== "undefined") {
    renderUpdateRequired(error);
  }
  return error;
}

/**
 * Clear mismatch guards only after hydration reaches a different build.
 *
 * A reload can be served the same stale document again. Retaining that
 * document's guard is what turns the next mismatch into an update-required
 * state instead of an automatic reload loop.
 */
export function markBuildHealthy(
  currentBuildId: string,
  environment: Pick<BuildMismatchEnvironment, "sessionStorage"> = {
    sessionStorage: window.sessionStorage,
  },
): void {
  const keys: string[] = [];
  for (let index = 0; index < environment.sessionStorage.length; index += 1) {
    const key = environment.sessionStorage.key(index);
    if (!key?.startsWith(GUARD_PREFIX)) continue;
    const value = environment.sessionStorage.getItem(key);
    if (value === null) continue;
    try {
      const attempt = JSON.parse(value) as Partial<MismatchAttempt>;
      if (
        typeof attempt.currentBuildId === "string" &&
        attempt.currentBuildId !== currentBuildId
      ) {
        keys.push(key);
      }
    } catch {
      // Preserve unknown/legacy guards: clearing them could re-enable a loop.
    }
  }
  for (const key of keys) environment.sessionStorage.removeItem(key);
}

async function readMismatchBody(
  response: Response,
): Promise<BuildMismatchBody | undefined> {
  if (
    response.headers.get(BUILD_ERROR_HEADER)?.toLowerCase() ===
    BUILD_MISMATCH_CODE
  ) {
    const expectedBuildId = response.headers.get(BUILD_ID_HEADER) ?? undefined;
    return {
      error: { code: BUILD_MISMATCH_CODE, expectedBuildId },
      buildId: expectedBuildId,
    };
  }

  if (response.status !== 409 && response.status !== 412) return undefined;

  try {
    const body = (await response.clone().json()) as Partial<BuildMismatchBody>;
    if (body.error === BUILD_MISMATCH_CODE) return body as BuildMismatchBody;
    return body.error?.code === BUILD_MISMATCH_CODE ? (body as BuildMismatchBody) : undefined;
  } catch {
    return undefined;
  }
}

export interface BuildAwareFetchOptions extends BuildMismatchOptions {
  buildId: string;
  fetch?: typeof globalThis.fetch;
}

/**
 * Fetch route data or submit an action exactly once. A mismatch triggers a
 * guarded document reload and is always surfaced as an error; callers must not
 * replay a mutation automatically.
 */
export async function fetchWithBuildGuard(
  input: RequestInfo | URL,
  init: RequestInit | undefined,
  options: BuildAwareFetchOptions,
): Promise<Response> {
  const headers = new Headers(init?.headers);
  headers.set(BUILD_ID_HEADER, options.buildId);

  const request = options.fetch ?? globalThis.fetch;
  const response = await request(input, { ...init, headers });
  const mismatch = await readMismatchBody(response);
  if (!mismatch) return response;

  throw handleBuildMismatch(
    options.buildId,
    typeof mismatch.error === "string"
      ? mismatch.buildId
      : mismatch.error.expectedBuildId,
    options,
  );
}

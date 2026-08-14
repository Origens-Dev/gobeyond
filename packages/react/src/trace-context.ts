export const TRACEPARENT_META_NAME = "traceparent";
export const TRACESTATE_META_NAME = "tracestate";

const TRACEPARENT_PATTERN =
  /^([0-9a-f]{2})-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})$/;
const ALL_ZERO_TRACE_ID = "0".repeat(32);
const ALL_ZERO_SPAN_ID = "0".repeat(16);

export type DocumentTraceContext = {
  traceparent?: string;
  tracestate?: string;
};

function normalizeTraceParent(value: string | null | undefined): string {
  const normalized = value?.trim().toLowerCase() ?? "";
  const match = TRACEPARENT_PATTERN.exec(normalized);
  if (
    !match ||
    match[1] === "ff" ||
    match[2] === ALL_ZERO_TRACE_ID ||
    match[3] === ALL_ZERO_SPAN_ID
  ) {
    return "";
  }
  return normalized;
}

function normalizeTraceState(value: string | null | undefined): string {
  const normalized = value?.trim() ?? "";
  if (!normalized || normalized.length > 512) return "";
  for (let i = 0; i < normalized.length; i += 1) {
    const code = normalized.charCodeAt(i);
    if (code < 0x20 || code > 0x7e || code === 0x22 || code === 0x3c || code === 0x3e) {
      return "";
    }
  }
  return normalized;
}

function metaContent(target: Document, name: string): string | null {
  return target.querySelector(`meta[name="${name}"]`)?.getAttribute("content") ?? null;
}

/** Read the HTML document's W3C trace context so fetches can nest under the page request. */
export function getDocumentTraceContext(
  doc?: Document,
): DocumentTraceContext {
  const target = doc ?? (typeof document === "undefined" ? undefined : document);
  if (!target) return {};
  const traceparent = normalizeTraceParent(metaContent(target, TRACEPARENT_META_NAME));
  if (!traceparent) return {};
  const tracestate = normalizeTraceState(metaContent(target, TRACESTATE_META_NAME));
  return tracestate ? { traceparent, tracestate } : { traceparent };
}

/** Headers that continue the document request's W3C parent on a same-origin fetch. */
export function documentTraceHeaders(doc?: Document): Record<string, string> {
  const context = getDocumentTraceContext(doc);
  const headers: Record<string, string> = {};
  if (context.traceparent) headers.traceparent = context.traceparent;
  if (context.tracestate) headers.tracestate = context.tracestate;
  return headers;
}

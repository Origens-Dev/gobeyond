package document

import (
	"regexp"
	"strings"
	"unicode"
)

// W3C Trace Context header names. Hosts such as gbhost inject these on the
// document request; the HTML document then exposes them to JavaScript so
// same-origin fetches can continue the page request instead of starting a
// new root.
const (
	TraceParentHeader = "traceparent"
	TraceStateHeader  = "tracestate"
	TraceParentMeta   = "traceparent"
	TraceStateMeta    = "tracestate"
)

var (
	traceParentPattern = regexp.MustCompile(`^([0-9a-f]{2})-([0-9a-f]{32})-([0-9a-f]{16})-([0-9a-f]{2})$`)
	allZeroTraceID     = strings.Repeat("0", 32)
	allZeroSpanID      = strings.Repeat("0", 16)
)

// NormalizeTraceParent returns a lowercase W3C traceparent, or empty when the
// value is missing or invalid. Invalid values are dropped so they cannot be
// reflected into HTML or reused as a parent.
func NormalizeTraceParent(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	match := traceParentPattern.FindStringSubmatch(normalized)
	if match == nil || match[1] == "ff" || match[2] == allZeroTraceID || match[3] == allZeroSpanID {
		return ""
	}
	return normalized
}

// NormalizeTraceState returns a W3C tracestate value, or empty when it is
// missing or not safe to copy into HTML.
func NormalizeTraceState(value string) string {
	normalized := strings.TrimSpace(value)
	if normalized == "" || len(normalized) > 512 {
		return ""
	}
	for _, r := range normalized {
		if r < 0x20 || r > 0x7e || r == '"' || r == '<' || r == '>' {
			return ""
		}
		if unicode.IsControl(r) {
			return ""
		}
	}
	return normalized
}

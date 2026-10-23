package voice

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func providerEventFixture() ProviderEvent {
	zero := int64(0)
	return ProviderEvent{Kind: ProviderEventUsage, Provider: "openai", Model: "gpt-live", ProviderSessionID: "session-provider", ProviderResponseID: "response-provider", SourceEventType: "response.completed", PayloadDigest: "sha256:" + strings.Repeat("a", 64), InputTokens: &zero, Final: true}
}
func TestProviderEventPreservesAbsentAndMeasuredZero(t *testing.T) {
	event := providerEventFixture()
	if e := event.Validate(); e != nil {
		t.Fatal(e)
	}
	raw, e := json.Marshal(event)
	if e != nil {
		t.Fatal(e)
	}
	var out ProviderEvent
	if e = json.Unmarshal(raw, &out); e != nil {
		t.Fatal(e)
	}
	if out.InputTokens == nil || *out.InputTokens != 0 || out.OutputTokens != nil || out.TotalTokens != nil || out.DurationSeconds != nil {
		t.Fatalf("field presence changed: %+v", out)
	}
	if !strings.Contains(string(raw), `"input_tokens":0`) || strings.Contains(string(raw), `"total_tokens"`) {
		t.Fatalf("omissions synthesized: %s", raw)
	}
}
func TestProviderEventClosureIsSeparateFromMetricFinality(t *testing.T) {
	usage := providerEventFixture()
	usage.Final = true
	if e := usage.Validate(); e != nil {
		t.Fatal(e)
	}
	closed := usage
	closed.Kind = ProviderEventSessionClosed
	closed.SourceEventType = "session.closed"
	closed.InputTokens = nil
	closed.Final = false
	if e := closed.Validate(); e != nil {
		t.Fatal(e)
	}
	closed.Final = true
	if e := closed.Validate(); e == nil {
		t.Fatal("closure asserted metric finality")
	}
	closed.Final = false
	zero := float64(0)
	closed.DurationSeconds = &zero
	if e := closed.Validate(); e == nil {
		t.Fatal("closure carried implicit metric authority")
	}
	usage.InputTokens = nil
	if e := usage.Validate(); e == nil {
		t.Fatal("empty usage accepted")
	}
}
func TestProviderEventRejectsMalformedEvidence(t *testing.T) {
	for _, name := range []string{"kind", "provider", "model", "session", "response", "source", "digest", "uppercase-digest", "oversize-model", "negative-input", "negative-output", "negative-total", "negative-duration", "nan", "infinity", "overflow-duration", "oversize-digest", "unicode-control"} {
		t.Run(name, func(t *testing.T) {
			e := providerEventFixture()
			negative := int64(-1)
			duration := float64(-1)
			switch name {
			case "kind":
				e.Kind = "closed"
			case "provider":
				e.Provider = ""
			case "model":
				e.Model = " model"
			case "session":
				e.ProviderSessionID = "bad\x00session"
			case "response":
				e.ProviderResponseID = "bad\nresponse"
			case "source":
				e.SourceEventType = "bad\tframe"
			case "digest":
				e.PayloadDigest = "sha256:abc"
			case "uppercase-digest":
				e.PayloadDigest = "sha256:" + strings.Repeat("A", 64)
			case "oversize-model":
				e.Model = strings.Repeat("x", 513)
			case "oversize-digest":
				e.PayloadDigest = "sha256:" + strings.Repeat("a", 1024)
			case "unicode-control":
				e.SourceEventType = "session.\u0085closed"
			case "negative-input":
				e.InputTokens = &negative
			case "negative-output":
				e.OutputTokens = &negative
			case "negative-total":
				e.TotalTokens = &negative
			case "negative-duration":
				e.DurationSeconds = &duration
			case "nan":
				duration = math.NaN()
				e.DurationSeconds = &duration
			case "infinity":
				duration = math.Inf(1)
				e.DurationSeconds = &duration
			case "overflow-duration":
				duration = math.Exp2(63) / 1000
				e.DurationSeconds = &duration
			}
			if e.Validate() == nil {
				t.Fatalf("accepted malformed %s", name)
			}
		})
	}
}
func TestProviderEventCloneDetachesMetricsAndPreservesUnknowns(t *testing.T) {
	event := providerEventFixture()
	duration := float64(0)
	output, total := int64(2), int64(3)
	event.DurationSeconds = &duration
	event.OutputTokens = &output
	event.TotalTokens = &total
	clone := event.Clone()
	if !reflect.DeepEqual(event, clone) {
		t.Fatal("clone changed evidence")
	}
	*clone.DurationSeconds = 10
	*clone.InputTokens = 10
	*clone.OutputTokens = 10
	*clone.TotalTokens = 10
	if *event.DurationSeconds != 0 || *event.InputTokens != 0 || *event.OutputTokens != 2 || *event.TotalTokens != 3 {
		t.Fatal("clone mutated original evidence")
	}
	absent := ProviderEvent{}.Clone()
	if absent.DurationSeconds != nil || absent.InputTokens != nil || absent.OutputTokens != nil || absent.TotalTokens != nil {
		t.Fatal("clone synthesized measurements")
	}
}
func TestProviderEventDurationConversionBoundary(t *testing.T) {
	event := providerEventFixture()
	duration := math.Nextafter(math.Exp2(63)/1000, 0)
	event.DurationSeconds = &duration
	if e := event.Validate(); e != nil {
		t.Fatal("safe finite duration rejected", e)
	}
	duration = math.Exp2(63) / 1000
	if e := event.Validate(); e == nil {
		t.Fatal("unsafe signed-millisecond conversion accepted")
	}
	zero := float64(0)
	event.DurationSeconds = &zero
	if e := event.Validate(); e != nil {
		t.Fatal("measured zero rejected", e)
	}
	max := int64(math.MaxInt64)
	event.InputTokens = &max
	if e := event.Validate(); e != nil {
		t.Fatal("bounded int64 token measurement rejected", e)
	}
}

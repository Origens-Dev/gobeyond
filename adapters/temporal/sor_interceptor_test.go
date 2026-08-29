package temporal

import (
	"encoding/json"
	"testing"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/temporal"
)

func TestShouldReportTimer(t *testing.T) {
	if shouldReportTimer(0) || shouldReportTimer(-time.Second) {
		t.Fatal("non-positive duration must skip SoR")
	}
	if !shouldReportTimer(time.Millisecond) {
		t.Fatal("positive duration must report")
	}
}

func TestFireAtFromDuration(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	d := 5 * time.Minute
	got := now.Add(d).UTC().Format(time.RFC3339)
	want := "2026-07-31T12:05:00Z"
	if got != want {
		t.Fatalf("fire_at=%q want %q", got, want)
	}
}

func shouldReportTimer(d time.Duration) bool {
	return d > 0
}

func TestStampFromRetryPolicyDefaults(t *testing.T) {
	got := stampFromRetryPolicy(nil)
	if got.InitialIntervalMS != 1000 || got.BackoffCoefficient != 2.0 || got.MaximumAttempts != 0 {
		t.Fatalf("defaults=%+v", got)
	}
	got = stampFromRetryPolicy(&temporal.RetryPolicy{
		InitialInterval:    2 * time.Minute,
		BackoffCoefficient: 1.5,
		MaximumInterval:    10 * time.Minute,
		MaximumAttempts:    5,
	})
	if got.InitialIntervalMS != 120000 || got.MaximumAttempts != 5 {
		t.Fatalf("custom=%+v", got)
	}
}

func TestIsNonRetryableActivityErr(t *testing.T) {
	if isNonRetryableActivityErr(temporal.NewApplicationError("x", "t")) {
		t.Fatal("retryable ApplicationError must not mark non_retryable")
	}
	if !isNonRetryableActivityErr(temporal.NewNonRetryableApplicationError("x", "t", nil)) {
		t.Fatal("NonRetryableApplicationError must mark non_retryable")
	}
}

func TestSiblingScheduleTarget(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		workflowTQ  string
		scheduledTQ string
		wantTarget  string
		wantOK      bool
	}{
		{name: "empty inherits", workflowTQ: "workflows__production", scheduledTQ: "", wantOK: false},
		{name: "whitespace inherits", workflowTQ: "workflows__production", scheduledTQ: "  ", wantOK: false},
		{name: "same queue", workflowTQ: "workflows__production", scheduledTQ: "workflows__production", wantOK: false},
		{
			name: "sibling restricted", workflowTQ: "control-plane-workflows__production",
			scheduledTQ: "control-plane-restricted__production",
			wantTarget:  "control-plane-restricted__production", wantOK: true,
		},
		{
			name: "sibling general", workflowTQ: "portal-workflows__production",
			scheduledTQ: "portal-general__production",
			wantTarget:  "portal-general__production", wantOK: true,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := siblingScheduleTarget(tc.workflowTQ, tc.scheduledTQ)
			if ok != tc.wantOK || got != tc.wantTarget {
				t.Fatalf("siblingScheduleTarget(%q, %q)=(%q, %v) want (%q, %v)",
					tc.workflowTQ, tc.scheduledTQ, got, ok, tc.wantTarget, tc.wantOK)
			}
		})
	}
}

func TestReadWorkflowTaskQueueFromActivityHeader(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal("control-plane-workflows__production")
	if err != nil {
		t.Fatal(err)
	}
	header := map[string]*commonpb.Payload{
		workflowTaskQueueHeaderKey: {
			Metadata: map[string][]byte{converter.MetadataEncoding: []byte(converter.MetadataEncodingJSON)},
			Data:     raw,
		},
	}
	if got := readWorkflowTaskQueueFromActivityHeader(header); got != "control-plane-workflows__production" {
		t.Fatalf("got %q", got)
	}
	if got := readWorkflowTaskQueueFromActivityHeader(nil); got != "" {
		t.Fatalf("nil header got %q", got)
	}
}

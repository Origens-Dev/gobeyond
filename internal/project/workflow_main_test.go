package project

import (
	"bytes"
	"testing"
)

func TestGeneratedWorkflowMainLogsBeforeServe(t *testing.T) {
	src, err := generatedWorkflowMain("example.com/app", WorkflowQueue{ID: "realtime-portal-support"})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte(`log.Printf("gobeyond worker: before temporal serve task_queue=%s namespace=%s"`)
	if !bytes.Contains(src, want) {
		t.Fatalf("generated worker main missing startup marker:\n%s", src)
	}
	for _, forbidden := range []string{"GOBEYOND_TEMPORAL_API_KEY", "GOBEYOND_TEMPORAL_TLS_CERT", "GOBEYOND_TEMPORAL_TLS_KEY", "GOBEYOND_CLAIM_DEK"} {
		if bytes.Contains(src, []byte(forbidden)) {
			t.Fatalf("generated worker main referenced secret env %s", forbidden)
		}
	}
}

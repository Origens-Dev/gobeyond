package voice

import "testing"

func TestSessionTerminationCloneDoesNotAlias(t *testing.T) {
	original := SessionTermination{OutstandingResponseIDs: []string{"response"}}
	copy := original.Clone()
	copy.OutstandingResponseIDs[0] = "changed"
	if original.OutstandingResponseIDs[0] != "response" {
		t.Fatal("termination clone aliases stored inventory")
	}
}

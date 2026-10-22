package temporalruntime

import (
	"github.com/Origens-Dev/gobeyond/agents/voicecontract"
	"testing"
)

func TestResourceAgentWriteBindingUsesOnlyVerifiedHomeScope(t *testing.T) {
	scope := voicecontract.Scope{Kind: "resource_agent", ResourceType: "home", ResourceID: "cedar", ConfigRevision: 3}
	bound := resourceBindingFromGrantScope(scope)
	if bound.Kind != "resource_agent" || bound.ResourceID != "cedar" || bound.AlternateID != "" {
		t.Fatalf("resource authority lost: %+v", bound)
	}
	scope.ResourceID = ""
	scope.LineID = "forged-line"
	if got := resourceBindingFromGrantScope(scope); got.Present() {
		t.Fatal("invalid scope retained authority")
	}
}

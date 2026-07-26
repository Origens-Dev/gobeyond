package pack

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/Origens-Dev/gobeyond/renderplan"
)

func parsePlan(t *testing.T, encoded []byte) *renderplan.Plan {
	t.Helper()
	plan, err := renderplan.Parse(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestPlanWeightsFormula(t *testing.T) {
	encoded := planJSON("home")
	plan := parsePlan(t, encoded)
	decoded, peak := PlanWeights(plan, len(encoded))
	if decoded == 0 {
		t.Fatal("decoded weight must be positive")
	}
	if want := decoded + uint64(len(encoded))*PlanPeakEncodedFactor; peak != want {
		t.Fatalf("peak = %d, want decoded + encoded*%d = %d", peak, PlanPeakEncodedFactor, want)
	}
}

func TestPlanWeightsAreDeterministicAndGrow(t *testing.T) {
	small := planJSON("home")
	first, _ := PlanWeights(parsePlan(t, small), len(small))
	second, _ := PlanWeights(parsePlan(t, small), len(small))
	if first != second {
		t.Fatalf("weights differ between identical plans: %d != %d", first, second)
	}
	large := planJSON("home_with_a_much_longer_route_identifier")
	bigger, _ := PlanWeights(parsePlan(t, large), len(large))
	if bigger <= first {
		t.Fatalf("larger plan must weigh more: %d <= %d", bigger, first)
	}
}

func TestStaticWeightsFormula(t *testing.T) {
	encoded := []byte(`{"title":"Home","count":3,"tags":["a","b"],"nested":{"ok":true}}`)
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var props any
	if err := decoder.Decode(&props); err != nil {
		t.Fatal(err)
	}
	decoded, peak := StaticWeights(props, nil, len(encoded))
	if decoded == 0 {
		t.Fatal("decoded weight must be positive")
	}
	if want := decoded + uint64(len(encoded))*StaticPeakEncodedFactor; peak != want {
		t.Fatalf("peak = %d, want decoded + encoded*%d = %d", peak, StaticPeakEncodedFactor, want)
	}
	withMetadata, _ := StaticWeights(props, map[string]any{"lang": "en"}, len(encoded))
	if withMetadata <= decoded {
		t.Fatalf("metadata must add weight: %d <= %d", withMetadata, decoded)
	}
}

func TestFallbackWeights(t *testing.T) {
	decoded, peak := FallbackWeights(ContentPlans, 10)
	if peak != 10*PlanFallbackPeakFactor {
		t.Fatalf("plan fallback peak = %d, want encoded*%d", peak, PlanFallbackPeakFactor)
	}
	if decoded != peak-10*PlanPeakEncodedFactor {
		t.Fatalf("plan fallback decoded = %d does not satisfy the peak formula", decoded)
	}
	decoded, peak = FallbackWeights(ContentStatic, 10)
	if peak != 10*StaticFallbackPeakFactor {
		t.Fatalf("static fallback peak = %d, want encoded*%d", peak, StaticFallbackPeakFactor)
	}
	if decoded != peak-10*StaticPeakEncodedFactor {
		t.Fatalf("static fallback decoded = %d does not satisfy the peak formula", decoded)
	}
}

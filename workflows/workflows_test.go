package workflows

import "testing"

func TestPhysicalSiblingQueue(t *testing.T) {
	for _, test := range []struct {
		current string
		logical string
		want    string
	}{
		{"orders__local", "fulfillment", "fulfillment__local"},
		{"orders__preview", "default", "default__preview"},
		{"orders", "fulfillment", "fulfillment"},
		{"orders__local", "", "orders__local"},
	} {
		if got := physicalSiblingQueue(test.current, test.logical); got != test.want {
			t.Errorf("physicalSiblingQueue(%q, %q) = %q, want %q", test.current, test.logical, got, test.want)
		}
	}
}

func TestValidateTaskQueueRejectsPhysicalQueue(t *testing.T) {
	if _, err := ValidateTaskQueue("orders__local"); err == nil {
		t.Fatal("expected physical task queue to be rejected")
	}
	if queue, err := ValidateTaskQueue("Orders"); err != nil || queue != "orders" {
		t.Fatalf("logical queue = %q, %v", queue, err)
	}
}

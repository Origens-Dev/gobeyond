package gobeyond

import "testing"

func TestTaskQueueName(t *testing.T) {
	queue, err := TaskQueueName("invoices", "preview")
	if err != nil {
		t.Fatal(err)
	}
	if queue != "invoices__preview" {
		t.Fatalf("queue = %q", queue)
	}
	queue, err = TaskQueueName("", LocalEnvironment)
	if err != nil {
		t.Fatal(err)
	}
	if queue != "default__local" {
		t.Fatalf("default queue = %q", queue)
	}
}

func TestNormalizeTaskQueueID(t *testing.T) {
	got, err := NormalizeTaskQueueID("Invoices")
	if err != nil || got != "invoices" {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := NormalizeTaskQueueID("has_underscore"); err == nil {
		t.Fatal("expected underscore rejection")
	}
	long := make([]byte, MaxTaskQueueIDBytes+1)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := NormalizeTaskQueueID(string(long)); err == nil {
		t.Fatal("expected length rejection")
	}
}

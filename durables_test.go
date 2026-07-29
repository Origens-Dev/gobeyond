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

func TestNormalizeWorkerID(t *testing.T) {
	got, err := NormalizeWorkerID("Invoices")
	if err != nil || got != "invoices" {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := NormalizeWorkerID("has_underscore"); err == nil {
		t.Fatal("expected underscore rejection")
	}
	long := make([]byte, MaxWorkerIDBytes+1)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := NormalizeWorkerID(string(long)); err == nil {
		t.Fatal("expected length rejection")
	}
}

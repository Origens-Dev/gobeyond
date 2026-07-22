package jsvalue

import (
	"testing"
)

func TestRejectsUnsafeIntegersAndNilCollections(t *testing.T) {
	for _, value := range []any{int64(9007199254740992), map[string]any{"items": []string(nil)}} {
		if err := Validate(value); err == nil {
			t.Fatalf("expected %T to fail", value)
		}
	}
	if err := Validate(map[string]any{"items": []string{}, "count": int64(2)}); err != nil {
		t.Fatal(err)
	}
	if err := Validate(map[string]any{"bytes": []byte("AB")}); err != nil {
		t.Fatal(err)
	}
}

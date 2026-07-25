package redisstore

import "testing"

func TestCASAllows(t *testing.T) {
	tests := []struct {
		name     string
		current  map[string]string
		expected map[string]int64
		want     bool
	}{
		{"no tags expected", map[string]string{"a": "5"}, nil, true},
		{"matching version", map[string]string{"a": "1"}, map[string]int64{"a": 1}, true},
		{"bumped version rejects", map[string]string{"a": "2"}, map[string]int64{"a": 1}, false},
		{"missing counter vs expected zero", map[string]string{}, map[string]int64{"a": 0}, true},
		{"missing counter vs expected nonzero", map[string]string{}, map[string]int64{"a": 1}, false},
		{"extra current tags irrelevant", map[string]string{"a": "1", "b": "99"}, map[string]int64{"a": 1}, true},
		{"multiple tags all match", map[string]string{"a": "1", "b": "2"}, map[string]int64{"a": 1, "b": 2}, true},
		{"multiple tags one mismatched", map[string]string{"a": "1", "b": "3"}, map[string]int64{"a": 1, "b": 2}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := casAllows(test.current, test.expected); got != test.want {
				t.Fatalf("casAllows(%v, %v) = %v, want %v", test.current, test.expected, got, test.want)
			}
		})
	}
}

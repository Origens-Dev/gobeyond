package redisstore

import (
	"bytes"
	"testing"
	"time"

	"github.com/Origens-Dev/gobeyond/cache"
)

func TestRecordRoundTrip(t *testing.T) {
	fresh := time.Unix(1700000000, 123456789)
	tests := map[string]cache.Record{
		"typical":           {Value: []byte("hello world"), TagVersions: map[string]int64{"a": 1, "b": 2}, FreshUntil: fresh},
		"binary value":      {Value: []byte{0x00, 0x01, '\n', 0xff, 0x00}, TagVersions: map[string]int64{"a": 1}},
		"empty value":       {Value: []byte{}, TagVersions: map[string]int64{"a": 1}},
		"nil value":         {TagVersions: map[string]int64{"a": 1}},
		"no tags":           {Value: []byte("x")},
		"no fresh until":    {Value: []byte("x"), TagVersions: map[string]int64{"a": 1}},
		"empty and no tags": {},
	}
	for name, record := range tests {
		t.Run(name, func(t *testing.T) {
			encoded, err := encodeRecord(record)
			if err != nil {
				t.Fatalf("encodeRecord: %v", err)
			}
			decoded, err := decodeRecord(encoded)
			if err != nil {
				t.Fatalf("decodeRecord: %v", err)
			}
			if !bytes.Equal(decoded.Value, record.Value) {
				t.Fatalf("Value = %v, want %v", decoded.Value, record.Value)
			}
			if len(decoded.TagVersions) != len(record.TagVersions) {
				t.Fatalf("TagVersions = %v, want %v", decoded.TagVersions, record.TagVersions)
			}
			for tag, version := range record.TagVersions {
				if decoded.TagVersions[tag] != version {
					t.Fatalf("TagVersions[%q] = %d, want %d", tag, decoded.TagVersions[tag], version)
				}
			}
			switch {
			case record.FreshUntil.IsZero():
				if !decoded.FreshUntil.IsZero() {
					t.Fatalf("FreshUntil = %v, want zero", decoded.FreshUntil)
				}
			case !decoded.FreshUntil.Equal(record.FreshUntil):
				t.Fatalf("FreshUntil = %v, want %v", decoded.FreshUntil, record.FreshUntil)
			}
		})
	}
}

func TestDecodeRecordRejectsPayloadWithoutDelimiter(t *testing.T) {
	if _, err := decodeRecord([]byte("no newline here")); err == nil {
		t.Fatal("expected an error for a payload with no metadata delimiter")
	}
}

package redisstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"

	"github.com/Origens-Dev/gobeyond/cache"
)

// recordMeta is the JSON metadata line written ahead of a Record's raw value
// bytes. Field names are kept short because this line is written on every
// cache entry. FreshUnixNano is a pointer so a zero-value FreshUntil (never
// set) can be told apart from a Record whose fresh boundary genuinely lands
// on the Unix epoch.
type recordMeta struct {
	TagVersions   map[string]int64 `json:"tv,omitempty"`
	FreshUnixNano *int64           `json:"fu,omitempty"`
}

// encodeRecord serializes a Record as one metadata JSON line, a newline, and
// the raw value bytes, so a single Redis string holds everything Get needs
// except ExpiresAt (which the store derives from the key's own TTL instead of
// duplicating it in the payload). encoding/json never emits a raw newline
// inside an object, which is what makes decodeRecord's split-on-first-newline
// unambiguous.
func encodeRecord(record cache.Record) ([]byte, error) {
	meta := recordMeta{TagVersions: record.TagVersions}
	if !record.FreshUntil.IsZero() {
		nanos := record.FreshUntil.UnixNano()
		meta.FreshUnixNano = &nanos
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	encoded := make([]byte, 0, len(metaJSON)+1+len(record.Value))
	encoded = append(encoded, metaJSON...)
	encoded = append(encoded, '\n')
	encoded = append(encoded, record.Value...)
	return encoded, nil
}

// decodeRecord reverses encodeRecord. ExpiresAt is left zero: callers that
// have the key's remaining TTL (Get, via a GET+PTTL pipeline) fill it in
// themselves.
func decodeRecord(payload []byte) (cache.Record, error) {
	idx := bytes.IndexByte(payload, '\n')
	if idx < 0 {
		return cache.Record{}, errors.New("redisstore: malformed entry payload: no metadata delimiter")
	}
	var meta recordMeta
	if err := json.Unmarshal(payload[:idx], &meta); err != nil {
		return cache.Record{}, err
	}
	record := cache.Record{
		Value:       append([]byte(nil), payload[idx+1:]...),
		TagVersions: meta.TagVersions,
	}
	if meta.FreshUnixNano != nil {
		record.FreshUntil = time.Unix(0, *meta.FreshUnixNano)
	}
	return record, nil
}

package pack

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/Origens-Dev/gobeyond/renderplan"
	"github.com/klauspost/compress/zstd"
)

// Builder assembles one pack at build time. Every added record is fully
// parsed during the CLI validation stage, weighed over the decoded
// value, and zstd-compressed; WriteTo then emits the sorted container.
type Builder struct {
	content ContentType
	buildID string
	records []builderRecord
	keys    map[string]struct{}
}

type builderRecord struct {
	key           string
	stored        []byte
	digest        [sha256.Size]byte
	encodedLen    uint64
	decodedWeight uint64
	peakWeight    uint64
}

func NewBuilder(content ContentType, buildID string) (*Builder, error) {
	if !content.valid() {
		return nil, fmt.Errorf("pack: invalid content type %d", content)
	}
	if buildID == "" || len(buildID) > MaxBuildIDLen {
		return nil, fmt.Errorf("pack: build ID must be 1..%d bytes, got %d", MaxBuildIDLen, len(buildID))
	}
	return &Builder{content: content, buildID: buildID, keys: make(map[string]struct{})}, nil
}

// AddPlan validates one render plan and records it under its route ID. The
// encoded JSON must parse as a valid plan whose routeId matches routeID.
func (b *Builder) AddPlan(routeID string, encoded []byte) error {
	if b.content != ContentPlans {
		return fmt.Errorf("pack: cannot add a plan to a %s pack", b.content)
	}
	plan, err := renderplan.Parse(encoded)
	if err != nil {
		return fmt.Errorf("pack: plan %q: %w", routeID, err)
	}
	if plan.RouteID != routeID {
		return fmt.Errorf("pack: plan key %q does not match plan routeId %q", routeID, plan.RouteID)
	}
	decodedWeight, peakWeight := PlanWeights(plan, len(encoded))
	return b.add(routeID, encoded, decodedWeight, peakWeight)
}

// AddStatic validates one static entry's JSON and records it under key,
// normally a StaticEntryKey value. The decoded value (props plus metadata) is
// weighed generically.
func (b *Builder) AddStatic(key string, encoded []byte) error {
	if b.content != ContentStatic {
		return fmt.Errorf("pack: cannot add a static entry to a %s pack", b.content)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("pack: static entry %q: %w", key, err)
	}
	if decoder.More() {
		return fmt.Errorf("pack: static entry %q: trailing JSON value", key)
	}
	decodedWeight, peakWeight := StaticWeights(value, nil, len(encoded))
	return b.add(key, encoded, decodedWeight, peakWeight)
}

func (b *Builder) add(key string, encoded []byte, decodedWeight, peakWeight uint64) error {
	if key == "" || len(key) > MaxKeyLen {
		return fmt.Errorf("pack: key must be 1..%d bytes, got %d", MaxKeyLen, len(key))
	}
	if _, dup := b.keys[key]; dup {
		return fmt.Errorf("pack: duplicate key %q", key)
	}
	if len(b.records) >= MaxRecords {
		return fmt.Errorf("pack: more than %d records", MaxRecords)
	}
	if len(encoded) > MaxRecordEncodedLen {
		return fmt.Errorf("pack: record %q: encoded length %d exceeds %d", key, len(encoded), MaxRecordEncodedLen)
	}
	encoder, err := sharedEncoder()
	if err != nil {
		return err
	}
	stored := encoder.EncodeAll(encoded, nil)
	b.keys[key] = struct{}{}
	b.records = append(b.records, builderRecord{
		key:           key,
		stored:        stored,
		digest:        sha256.Sum256(stored),
		encodedLen:    uint64(len(encoded)),
		decodedWeight: decodedWeight,
		peakWeight:    peakWeight,
	})
	return nil
}

func (b *Builder) Len() int { return len(b.records) }

// WriteTo emits the container: header, index sorted ascending by key, then
// record bytes in the same order.
func (b *Builder) WriteTo(w io.Writer) (int64, error) {
	records := slices.Clone(b.records)
	slices.SortFunc(records, func(a, b builderRecord) int { return strings.Compare(a.key, b.key) })

	indexLen := 0
	for _, rec := range records {
		indexLen += indexEntryLen(rec.key)
	}
	head := appendHeader(nil, b.content.magic(), FormatVersion, WeigherVersion, b.buildID, RecordCodecJSONZstd, uint32(len(records)), uint64(indexLen))

	offset := uint64(len(head) + indexLen)
	index := make([]byte, 0, indexLen)
	for _, rec := range records {
		index = appendIndexEntry(index, Record{
			Key:           rec.key,
			Offset:        offset,
			Length:        uint64(len(rec.stored)),
			Digest:        rec.digest,
			EncodedLen:    rec.encodedLen,
			DecodedWeight: rec.decodedWeight,
			PeakWeight:    rec.peakWeight,
		})
		offset += uint64(len(rec.stored))
	}

	var written int64
	for _, chunk := range [][]byte{head, index} {
		n, err := w.Write(chunk)
		written += int64(n)
		if err != nil {
			return written, err
		}
	}
	for _, rec := range records {
		n, err := w.Write(rec.stored)
		written += int64(n)
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

// WriteFile writes the pack to path via a temporary file and rename, so an
// interrupted build never leaves a partial pack behind.
func (b *Builder) WriteFile(path string) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := b.WriteTo(tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// WritePlans writes the render-plan pack (.gbp) for one build from the
// route-ID-keyed plan JSON produced by the compiler.
func WritePlans(path, buildID string, plans map[string][]byte) error {
	builder, err := NewBuilder(ContentPlans, buildID)
	if err != nil {
		return err
	}
	for _, routeID := range slices.Sorted(maps.Keys(plans)) {
		if err := builder.AddPlan(routeID, plans[routeID]); err != nil {
			return err
		}
	}
	return builder.WriteFile(path)
}

// WriteStatic writes the packaged static-entry pack (.gbs) for one build from
// entry JSON keyed by StaticEntryKey.
func WriteStatic(path, buildID string, entries map[string][]byte) error {
	builder, err := NewBuilder(ContentStatic, buildID)
	if err != nil {
		return err
	}
	for _, key := range slices.Sorted(maps.Keys(entries)) {
		if err := builder.AddStatic(key, entries[key]); err != nil {
			return err
		}
	}
	return builder.WriteFile(path)
}

// StaticEntryKey derives the canonical pack key for one static entry:
// routeID + "?" + sorted queryEscape(name)=queryEscape(value) pairs. Empty
// params yield routeID + "?". An optional catch-all matched with zero
// segments must be passed as a present, empty value.
func StaticEntryKey(routeID string, params map[string]string) string {
	var key strings.Builder
	key.WriteString(routeID)
	key.WriteByte('?')
	for i, name := range slices.Sorted(maps.Keys(params)) {
		if i > 0 {
			key.WriteByte('&')
		}
		key.WriteString(url.QueryEscape(name))
		key.WriteByte('=')
		key.WriteString(url.QueryEscape(params[name]))
	}
	return key.String()
}

// One shared encoder/decoder pair: EncodeAll and DecodeAll are safe for
// concurrent use and hold no per-pack state.
var (
	sharedEncoder = sync.OnceValues(func() (*zstd.Encoder, error) {
		return zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBetterCompression), zstd.WithEncoderConcurrency(1))
	})
	sharedDecoder = sync.OnceValues(func() (*zstd.Decoder, error) {
		return zstd.NewReader(nil, zstd.WithDecoderMaxMemory(MaxRecordEncodedLen))
	})
)

package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Origens-Dev/gobeyond/codegen"
	"github.com/Origens-Dev/gobeyond/pack"
	"github.com/Origens-Dev/gobeyond/residency"
)

// packedStaticEntry is the JSON shape of one record in a static-entry pack:
// the packaged props and metadata for one (routeID, params) pair, keyed by
// pack.StaticEntryKey. Params live in the key, not the record.
type packedStaticEntry struct {
	Props    json.RawMessage `json:"props"`
	Metadata json.RawMessage `json:"metadata"`
}

// PackStaticStore is the pack-backed StaticEntries: an open static-entry
// pack (.gbs) plus the build's value contracts, behind a bounded residency
// cache. Entries are decoded on first use - props re-marked as SafeHTML
// through the contracts exactly like the eager LoadStaticStore path - and
// stay resident subject to the cache bounds. The application owns Close.
type PackStaticStore struct {
	reader    *pack.Reader
	contracts codegen.Document
	routes    map[string]struct{}
	cache     *residency.Cache[LoadedPage]
}

// OpenStaticStore opens the static-entry pack at path together with the
// value-contract document at contractsPath. Without options the residency
// cache uses the static defaults: 128 entries, 32 MiB estimated
// decoded bytes, 10 minute idle expiry.
func OpenStaticStore(path, contractsPath string, opts ...StoreOption) (*PackStaticStore, error) {
	contracts, err := LoadContracts(contractsPath)
	if err != nil {
		return nil, err
	}
	reader, err := pack.Open(path, pack.ContentStatic)
	if err != nil {
		return nil, err
	}
	routes := make(map[string]struct{})
	for _, record := range reader.Records() {
		routeID := record.Key
		if separator := strings.IndexByte(routeID, '?'); separator >= 0 {
			routeID = routeID[:separator]
		}
		routes[routeID] = struct{}{}
	}
	options := residency.Options{MaxEntries: DefaultStaticMaxEntries}
	for _, opt := range opts {
		opt(&options)
	}
	return &PackStaticStore{
		reader:    reader,
		contracts: *contracts,
		routes:    routes,
		cache:     residency.New[LoadedPage](options),
	}, nil
}

func (s *PackStaticStore) BuildID() string { return s.reader.BuildID() }

// Has reports whether the pack carries at least one entry for routeID.
func (s *PackStaticStore) Has(routeID string) bool {
	_, ok := s.routes[routeID]
	return ok
}

// Contracts returns the value-contract document the entries were packaged
// against, so New can default Config.Contracts from the store.
func (s *PackStaticStore) Contracts() *codegen.Document { return &s.contracts }

// Entry returns the packaged page for (routeID, params), loading it through
// the residency cache on a miss. A key absent from the pack index is a plain
// miss - (LoadedPage{}, false, nil) - so the caller can render its packaged
// not-found shape; pack read and decode failures are errors.
func (s *PackStaticStore) Entry(ctx context.Context, routeID string, params map[string]string) (LoadedPage, bool, error) {
	key := pack.StaticEntryKey(routeID, params)
	record, ok := s.reader.Record(key)
	if !ok {
		return LoadedPage{}, false, nil
	}
	page, err := s.cache.Get(ctx, key, packWeight(record.DecodedWeight), packWeight(record.PeakWeight),
		func(context.Context) (LoadedPage, int64, int64, error) {
			page, err := s.decodeEntry(key, routeID)
			return page, 0, 0, err
		})
	if err != nil {
		return LoadedPage{}, false, err
	}
	return page, true, nil
}

// decodeEntry is the cold-load path: read and digest-verify the stored
// bytes, decompress, and rebuild the LoadedPage with SafeHTML restored via
// the contracts. Failures over the immutable bytes are marked immutable for
// the negative cache; transient I/O errors are not.
func (s *PackStaticStore) decodeEntry(key, routeID string) (LoadedPage, error) {
	encoded, err := s.reader.DecodeJSONRecord(key)
	if err != nil {
		if errors.Is(err, pack.ErrDigestMismatch) || errors.Is(err, pack.ErrNotFound) {
			return LoadedPage{}, residency.ImmutableError(err)
		}
		return LoadedPage{}, err
	}
	var entry packedStaticEntry
	if err := json.Unmarshal(encoded, &entry); err != nil {
		return LoadedPage{}, residency.ImmutableError(fmt.Errorf("static entry %q: %w", key, err))
	}
	page, err := decodeStaticEntry(s.contracts, routeID, entry.Props, entry.Metadata)
	if err != nil {
		return LoadedPage{}, residency.ImmutableError(fmt.Errorf("static entry %q: %w", key, err))
	}
	return page, nil
}

// Stats snapshots the residency cache behind the store.
func (s *PackStaticStore) Stats() residency.Stats { return s.cache.Stats() }

// Trim evicts resident entries until estimated bytes are at or below
// targetBytes. Entries already handed to in-flight requests remain valid.
func (s *PackStaticStore) Trim(targetBytes int64) { s.cache.Trim(targetBytes) }

// Close releases the residency cache and the underlying pack file.
func (s *PackStaticStore) Close() error {
	_ = s.cache.Close()
	return s.reader.Close()
}

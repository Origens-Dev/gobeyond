package runtime

import (
	"context"
	"errors"
	"fmt"

	"github.com/Origens-Dev/gobeyond/pack"
	"github.com/Origens-Dev/gobeyond/renderplan"
	"github.com/Origens-Dev/gobeyond/residency"
)

// PackPlanStore is the pack-backed PlanStore: an open render-plan pack
// (.gbp) behind a bounded residency cache. Opening validates the container
// header and index only; a route's plan is read, digest-verified, and parsed
// the first time a request needs it, then stays resident subject to the
// cache's entry, byte, and idle bounds. The application owns Close.
type PackPlanStore struct {
	reader *pack.Reader
	cache  *residency.Cache[*renderplan.Plan]
}

// OpenPlanStore opens the render-plan pack at path. Without options the
// residency cache uses the ADR 004 plan defaults: 64 entries, 32 MiB
// estimated decoded bytes, 10 minute idle expiry.
func OpenPlanStore(path string, opts ...StoreOption) (*PackPlanStore, error) {
	reader, err := pack.Open(path, pack.ContentPlans)
	if err != nil {
		return nil, err
	}
	var options residency.Options
	for _, opt := range opts {
		opt(&options)
	}
	return &PackPlanStore{reader: reader, cache: residency.New[*renderplan.Plan](options)}, nil
}

func (s *PackPlanStore) BuildID() string { return s.reader.BuildID() }

func (s *PackPlanStore) Has(routeID string) bool { return s.reader.Has(routeID) }

// Plan returns the decoded render plan for routeID, loading it through the
// residency cache on a miss. Concurrent requests for the same route share
// one decode.
func (s *PackPlanStore) Plan(ctx context.Context, routeID string) (*renderplan.Plan, error) {
	record, ok := s.reader.Record(routeID)
	if !ok {
		return nil, fmt.Errorf("%w: %q", pack.ErrNotFound, routeID)
	}
	return s.cache.Get(ctx, routeID, packWeight(record.DecodedWeight), packWeight(record.PeakWeight),
		func(context.Context) (*renderplan.Plan, int64, int64, error) {
			plan, err := s.decodePlan(routeID)
			return plan, 0, 0, err
		})
}

// decodePlan is the cold-load path: read and digest-verify the stored bytes,
// decompress, parse, and confirm the plan's identity. Failures over the
// immutable bytes - digest mismatch, parse errors, a plan carrying another
// route's ID - are marked immutable so the negative cache stops re-running a
// decode that cannot succeed this build; transient I/O errors are not.
func (s *PackPlanStore) decodePlan(routeID string) (*renderplan.Plan, error) {
	encoded, err := s.reader.DecodeJSONRecord(routeID)
	if err != nil {
		if errors.Is(err, pack.ErrDigestMismatch) || errors.Is(err, pack.ErrNotFound) {
			return nil, residency.ImmutableError(err)
		}
		return nil, err
	}
	plan, err := renderplan.Parse(encoded)
	if err != nil {
		return nil, residency.ImmutableError(fmt.Errorf("plan record %q: %w", routeID, err))
	}
	if plan.RouteID != routeID {
		return nil, residency.ImmutableError(fmt.Errorf("plan record %q carries routeId %q", routeID, plan.RouteID))
	}
	return plan, nil
}

// Stats snapshots the residency cache behind the store.
func (s *PackPlanStore) Stats() residency.Stats { return s.cache.Stats() }

// Trim evicts resident plans until estimated bytes are at or below
// targetBytes. Plans already handed to in-flight requests remain valid.
func (s *PackPlanStore) Trim(targetBytes int64) { s.cache.Trim(targetBytes) }

// Close releases the residency cache and the underlying pack file.
func (s *PackPlanStore) Close() error {
	_ = s.cache.Close()
	return s.reader.Close()
}

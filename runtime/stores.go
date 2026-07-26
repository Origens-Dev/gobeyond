package runtime

import (
	"context"
	"math"

	"github.com/Origens-Dev/gobeyond/codegen"
	"github.com/Origens-Dev/gobeyond/renderplan"
	"github.com/Origens-Dev/gobeyond/residency"
)

// PlanStore supplies render plans on demand for pages whose PageRoute.Plan is
// nil (ADR 004). New checks Has for every such page and rejects a store whose
// BuildID differs from Config.BuildID, so a request that reaches Plan can
// trust the answer belongs to this build. Plan is called only when a document
// must actually render - after loaders, redirects, and error short-circuits -
// never at New.
type PlanStore interface {
	BuildID() string
	Has(routeID string) bool
	Plan(ctx context.Context, routeID string) (*renderplan.Plan, error)
}

// StaticEntries supplies packaged static page data on demand for pages that
// ship neither inline Static data nor a loader. BuildID may be empty for
// build-agnostic adapters (the eager JSON StaticStore); a non-empty BuildID
// must equal Config.BuildID. Entry returns ok=false without error when the
// route is known but no entry was packaged for the given params; pack-level
// failures surface as errors. Contracts returns the value-contract document
// the entries were packaged against, which New adopts as Config.Contracts
// when the caller supplied none.
type StaticEntries interface {
	BuildID() string
	Has(routeID string) bool
	Entry(ctx context.Context, routeID string, params map[string]string) (LoadedPage, bool, error)
	Contracts() *codegen.Document
}

// DefaultStaticMaxEntries is the ADR 004 residency entry bound for static
// entry stores. Plan stores use residency.DefaultMaxEntries; both share the
// package's byte and idle defaults.
const DefaultStaticMaxEntries = 128

// StoreOption adjusts the residency cache behind a pack-backed store opened
// by OpenPlanStore or OpenStaticStore.
type StoreOption func(*residency.Options)

// WithResidencyOptions replaces the store's residency cache options. Zero
// fields keep the residency package defaults.
func WithResidencyOptions(options residency.Options) StoreOption {
	return func(target *residency.Options) { *target = options }
}

// packWeight narrows a pack index weight to the residency cache's int64
// domain. Weights near the limit only occur in forged packs; saturating keeps
// the cache's accounting sane (the entry reads as oversized and is served
// without being retained).
func packWeight(weight uint64) int64 {
	if weight > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(weight)
}

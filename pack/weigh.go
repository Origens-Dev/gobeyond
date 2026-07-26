package pack

import (
	"reflect"

	"github.com/Origens-Dev/gobeyond/renderplan"
)

// WeigherVersion identifies the deterministic pack-time weight model. Readers
// that see a different version must not trust stored weights and fall back to
// FallbackWeights.
const WeigherVersion = 1

// Weight formulas locked by ADR 004. "Encoded" is the JSON record length in
// bytes before compression (Record.EncodedLen).
const (
	// PlanPeakEncodedFactor: plan peak = decoded + encoded*3.
	PlanPeakEncodedFactor = 3
	// StaticPeakEncodedFactor: static peak = decoded + encoded*2.
	StaticPeakEncodedFactor = 2
	// PlanFallbackPeakFactor: unknown weigher, plan peak = encoded*8.
	PlanFallbackPeakFactor = 8
	// StaticFallbackPeakFactor: unknown weigher, static peak = encoded*5.
	StaticFallbackPeakFactor = 5
)

// PlanWeights estimates the resident weight of a decoded render plan and its
// peak weight while decoding, for a plan whose JSON encoding is encodedLen
// bytes.
func PlanWeights(plan *renderplan.Plan, encodedLen int) (decodedWeight, peakWeight uint64) {
	decodedWeight = weighValue(reflect.ValueOf(plan))
	return decodedWeight, decodedWeight + uint64(encodedLen)*PlanPeakEncodedFactor
}

// StaticWeights estimates the resident weight of one decoded static entry
// (props plus metadata) and its peak weight while decoding, for an entry whose
// JSON encoding is encodedLen bytes.
func StaticWeights(props, metadata any, encodedLen int) (decodedWeight, peakWeight uint64) {
	decodedWeight = weighValue(reflect.ValueOf(props)) + weighValue(reflect.ValueOf(metadata))
	return decodedWeight, decodedWeight + uint64(encodedLen)*StaticPeakEncodedFactor
}

// FallbackWeights returns the conservative estimates readers must use when a
// pack was written by an unknown weigher version: peak = encoded*8 for plans
// and encoded*5 for static entries, with the decoded share derived from the
// standard peak formula.
func FallbackWeights(content ContentType, encodedLen uint64) (decodedWeight, peakWeight uint64) {
	switch content {
	case ContentPlans:
		return encodedLen * (PlanFallbackPeakFactor - PlanPeakEncodedFactor), encodedLen * PlanFallbackPeakFactor
	case ContentStatic:
		return encodedLen * (StaticFallbackPeakFactor - StaticPeakEncodedFactor), encodedLen * StaticFallbackPeakFactor
	default:
		panic("pack: unknown content type")
	}
}

// Fixed 64-bit memory model so weights are identical on every build host.
const (
	weighWord      = 8
	weighScalar    = weighWord
	weighBool      = 1
	weighString    = 2 * weighWord // string header; content added per byte
	weighSlice     = 3 * weighWord // slice header; content added per element
	weighInterface = 2 * weighWord
	weighMapBase   = 6 * weighWord
	weighMapEntry  = 3 * weighWord // per-entry bucket overhead
)

// weighValue walks a decoded value and sums an estimate of its heap footprint.
// It is deterministic for acyclic values such as parsed render plans and
// JSON-decoded static entries; map iteration order does not affect the sum.
func weighValue(v reflect.Value) uint64 {
	if !v.IsValid() {
		return 0
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return weighWord
		}
		return weighWord + weighValue(v.Elem())
	case reflect.Interface:
		if v.IsNil() {
			return weighInterface
		}
		return weighInterface + weighValue(v.Elem())
	case reflect.String:
		return weighString + uint64(v.Len())
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return weighSlice + uint64(v.Len())
		}
		total := uint64(weighSlice)
		for i := range v.Len() {
			total += weighValue(v.Index(i))
		}
		return total
	case reflect.Array:
		var total uint64
		for i := range v.Len() {
			total += weighValue(v.Index(i))
		}
		return total
	case reflect.Map:
		total := uint64(weighMapBase)
		iter := v.MapRange()
		for iter.Next() {
			total += weighMapEntry + weighValue(iter.Key()) + weighValue(iter.Value())
		}
		return total
	case reflect.Struct:
		var total uint64
		for i := range v.NumField() {
			total += weighValue(v.Field(i))
		}
		return total
	case reflect.Bool:
		return weighBool
	default:
		return weighScalar
	}
}

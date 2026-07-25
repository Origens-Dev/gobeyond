package renderplan

// Intrinsic names are stable protocol identifiers for JavaScript operations
// with explicitly implemented Go equivalents.
const (
	IntrinsicDateGetFullYear    = "ecmascript.Date.prototype.getFullYear"
	IntrinsicDateGetUTCFullYear = "ecmascript.Date.prototype.getUTCFullYear"
	IntrinsicDateGetMonth       = "ecmascript.Date.prototype.getMonth"
	IntrinsicDateGetUTCMonth    = "ecmascript.Date.prototype.getUTCMonth"
	IntrinsicDateGetDate        = "ecmascript.Date.prototype.getDate"
	IntrinsicDateGetUTCDate     = "ecmascript.Date.prototype.getUTCDate"
	IntrinsicDateGetHours       = "ecmascript.Date.prototype.getHours"
	IntrinsicDateGetUTCHours    = "ecmascript.Date.prototype.getUTCHours"
	IntrinsicDateGetMinutes     = "ecmascript.Date.prototype.getMinutes"
	IntrinsicDateGetUTCMinutes  = "ecmascript.Date.prototype.getUTCMinutes"
	IntrinsicDateGetSeconds     = "ecmascript.Date.prototype.getSeconds"
	IntrinsicDateGetUTCSeconds  = "ecmascript.Date.prototype.getUTCSeconds"
)

type IntrinsicStability string

const (
	// IntrinsicPure depends only on its explicit arguments.
	IntrinsicPure IntrinsicStability = "pure"
	// IntrinsicRenderSnapshot reads one value captured at the start of a render.
	// The same instant is embedded in hydration JSON as renderNow so the browser
	// Vite rewrite can return identical numeric Date projections on first paint.
	IntrinsicRenderSnapshot IntrinsicStability = "render-snapshot"
)

type IntrinsicSpec struct {
	Arity     int
	Stability IntrinsicStability
}

var intrinsicSpecs = map[string]IntrinsicSpec{
	IntrinsicDateGetFullYear:    {Arity: 0, Stability: IntrinsicRenderSnapshot},
	IntrinsicDateGetUTCFullYear: {Arity: 0, Stability: IntrinsicRenderSnapshot},
	IntrinsicDateGetMonth:       {Arity: 0, Stability: IntrinsicRenderSnapshot},
	IntrinsicDateGetUTCMonth:    {Arity: 0, Stability: IntrinsicRenderSnapshot},
	IntrinsicDateGetDate:        {Arity: 0, Stability: IntrinsicRenderSnapshot},
	IntrinsicDateGetUTCDate:     {Arity: 0, Stability: IntrinsicRenderSnapshot},
	IntrinsicDateGetHours:       {Arity: 0, Stability: IntrinsicRenderSnapshot},
	IntrinsicDateGetUTCHours:    {Arity: 0, Stability: IntrinsicRenderSnapshot},
	IntrinsicDateGetMinutes:     {Arity: 0, Stability: IntrinsicRenderSnapshot},
	IntrinsicDateGetUTCMinutes:  {Arity: 0, Stability: IntrinsicRenderSnapshot},
	IntrinsicDateGetSeconds:     {Arity: 0, Stability: IntrinsicRenderSnapshot},
	IntrinsicDateGetUTCSeconds:  {Arity: 0, Stability: IntrinsicRenderSnapshot},
}

// IntrinsicDefinition reports the validation and evaluation contract for a
// compiler-recognized platform operation.
func IntrinsicDefinition(name string) (IntrinsicSpec, bool) {
	spec, ok := intrinsicSpecs[name]
	return spec, ok
}

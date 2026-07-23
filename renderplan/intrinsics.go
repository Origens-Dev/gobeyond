package renderplan

// Intrinsic names are stable protocol identifiers for JavaScript operations
// with explicitly implemented Go equivalents.
const (
	IntrinsicDateGetFullYear    = "ecmascript.Date.prototype.getFullYear"
	IntrinsicDateGetUTCFullYear = "ecmascript.Date.prototype.getUTCFullYear"
)

type IntrinsicStability string

const (
	// IntrinsicPure depends only on its explicit arguments.
	IntrinsicPure IntrinsicStability = "pure"
	// IntrinsicRenderSnapshot reads one value captured at the start of a render.
	IntrinsicRenderSnapshot IntrinsicStability = "render-snapshot"
)

type IntrinsicSpec struct {
	Arity     int
	Stability IntrinsicStability
}

var intrinsicSpecs = map[string]IntrinsicSpec{
	IntrinsicDateGetFullYear:    {Arity: 0, Stability: IntrinsicRenderSnapshot},
	IntrinsicDateGetUTCFullYear: {Arity: 0, Stability: IntrinsicRenderSnapshot},
}

// IntrinsicDefinition reports the validation and evaluation contract for a
// compiler-recognized platform operation.
func IntrinsicDefinition(name string) (IntrinsicSpec, bool) {
	spec, ok := intrinsicSpecs[name]
	return spec, ok
}

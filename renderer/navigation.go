package renderer

// FrameworkLocalsKey is the reserved locals namespace used by portable
// usePathname / useRoute bake paths (`__gobeyond.pathname`, `__gobeyond.route`).
const FrameworkLocalsKey = "__gobeyond"

// NavigationMeta is request-time route identity injected for portable nav hooks.
type NavigationMeta struct {
	RouteID  string
	Pathname string
	Params   map[string]string
}

// WithNavigation returns a renderer that seeds `__gobeyond` locals for plan
// evaluation. Hydration payloads should continue to use the original page
// props; the browser reads soft-navigation state instead of this namespace.
func (r *Renderer) WithNavigation(meta NavigationMeta) *Renderer {
	clone := *r
	clone.navigation = &meta
	return &clone
}

func navigationLocals(meta *NavigationMeta) map[string]any {
	if meta == nil {
		return nil
	}
	pathname := meta.Pathname
	if pathname == "" {
		pathname = "/"
	}
	params := meta.Params
	if params == nil {
		params = map[string]string{}
	}
	return map[string]any{
		FrameworkLocalsKey: map[string]any{
			"pathname": pathname,
			"route": map[string]any{
				"routeId":  meta.RouteID,
				"pathname": pathname,
				"params":   params,
			},
		},
	}
}

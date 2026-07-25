package renderer

import (
	"fmt"
	"html"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Origens-Dev/gobeyond/renderplan"
)

type Renderer struct {
	now        func() time.Time
	navigation *NavigationMeta
}

const maxRenderedBytes = 8 << 20

type renderBuffer struct {
	strings.Builder
	exceeded bool
}

func (buffer *renderBuffer) WriteString(value string) {
	if buffer.exceeded || buffer.Len()+len(value) > maxRenderedBytes {
		buffer.exceeded = true
		return
	}
	_, _ = buffer.Builder.WriteString(value)
}

func (buffer *renderBuffer) appendByte(value byte) {
	if buffer.exceeded || buffer.Len()+1 > maxRenderedBytes {
		buffer.exceeded = true
		return
	}
	_ = buffer.Builder.WriteByte(value)
}

func New() *Renderer { return &Renderer{now: time.Now} }

// Render validates and renders a complete plan. It never returns partial HTML.
// The render clock is captured once and must also be embedded in hydration data
// as renderNow so browser Date rewrites match Go (same class of contract as
// Next.js SSR: first paint uses the server snapshot).
func Render(plan *renderplan.Plan, props any) (string, error) {
	html, _, err := New().RenderAt(plan, props, time.Time{})
	return html, err
}

// RenderAt is like Render but uses an explicit render clock. A zero now uses
// the renderer's clock function (default time.Now).
func RenderAt(plan *renderplan.Plan, props any, now time.Time) (string, error) {
	html, _, err := New().RenderAt(plan, props, now)
	return html, err
}

func (r *Renderer) Render(plan *renderplan.Plan, props any) (string, error) {
	html, _, err := r.RenderAt(plan, props, time.Time{})
	return html, err
}

// RenderAt validates and renders a plan using now as the render-snapshot clock.
// It returns the HTML and the normalized UTC instant that callers must embed in
// hydration JSON as renderNow. A zero now uses r.now (or time.Now).
func (r *Renderer) RenderAt(plan *renderplan.Plan, props any, now time.Time) (string, time.Time, error) {
	if err := renderplan.Validate(plan); err != nil {
		return "", time.Time{}, err
	}
	if now.IsZero() {
		if r.now != nil {
			now = r.now()
		} else {
			now = time.Now()
		}
	}
	// Keep the clock's Location so local Date getters (getFullYear, …) match the
	// server zone. Hydration embeds this instant as RFC3339; UTC getters are
	// portable across browser timezones, local getters match when the browser
	// zone equals the server zone (same class of SSR caveat as Next.js).
	var out renderBuffer
	locals := map[string]any{}
	for key, value := range navigationLocals(r.navigation) {
		locals[key] = value
	}
	ctx := renderContext{env: environment{props: props, locals: locals, now: now}, namespace: renderplan.NamespaceHTML}
	if err := r.node(&out, plan.Root, ctx, "$.root"); err != nil {
		return "", time.Time{}, err
	}
	if out.exceeded {
		return "", time.Time{}, fail(CodeRender, "$.root", "rendered HTML exceeds the 8 MiB response budget")
	}
	return out.String(), now, nil
}

type renderContext struct {
	env       environment
	namespace renderplan.Namespace
	parentTag string
	selection map[string]bool
}

func (r *Renderer) node(out *renderBuffer, node renderplan.Node, ctx renderContext, path string) error {
	switch n := node.(type) {
	case *renderplan.Element:
		return r.element(out, n, ctx, path)
	case *renderplan.Text:
		value, err := evaluate(n.Value, ctx.env, path+".value")
		if err != nil {
			return err
		}
		text, err := renderText(value)
		if err != nil {
			return fail(CodeRender, path+".value", err.Error())
		}
		out.WriteString(escapeText(text))
		return nil
	case *renderplan.Fragment:
		return r.children(out, n.Children, ctx, path+".children")
	case *renderplan.Conditional:
		value, err := evaluate(n.Test, ctx.env, path+".test")
		if err != nil {
			return err
		}
		if truthy(value) {
			return r.node(out, n.Consequent, ctx, path+".consequent")
		}
		if n.Alternate != nil {
			return r.node(out, n.Alternate, ctx, path+".alternate")
		}
		return nil
	case *renderplan.Each:
		return r.each(out, n, ctx, path)
	case *renderplan.ClientOnly:
		if n.Fallback == nil {
			return nil
		}
		return r.node(out, n.Fallback, ctx, path+".fallback")
	case *renderplan.RawHTML:
		value, err := evaluate(n.Value, ctx.env, path+".value")
		if err != nil {
			return err
		}
		switch safe := value.(type) {
		case renderplan.SafeHTML:
			out.WriteString(safe.String())
		case *renderplan.SafeHTML:
			if safe == nil {
				return fail(CodeUnsafeHTML, path+".value", "SafeHTML is nil")
			}
			out.WriteString(safe.String())
		case string:
			// A literal rawHtml expression was authorized by the compiler. Values
			// loaded through props still require the SafeHTML trust marker.
			if _, literal := n.Value.(*renderplan.Literal); !literal {
				return fail(CodeUnsafeHTML, path+".value", "raw HTML from props requires renderplan.SafeHTML")
			}
			out.WriteString(safe)
		default:
			return fail(CodeUnsafeHTML, path+".value", "raw HTML requires renderplan.SafeHTML")
		}
		return nil
	default:
		return fail(CodeRender, path, fmt.Sprintf("unsupported node %T", node))
	}
}

// SafeHTML and TrustedHTML are renderer-level aliases for applications that do
// not otherwise need to import the render-plan model.
type SafeHTML = renderplan.SafeHTML

func TrustedHTML(value string) SafeHTML { return renderplan.TrustedHTML(value) }

func (r *Renderer) each(out *renderBuffer, n *renderplan.Each, ctx renderContext, path string) error {
	items, err := evaluate(n.Items, ctx.env, path+".items")
	if err != nil {
		return err
	}
	rv := reflect.ValueOf(items)
	for rv.IsValid() && (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface) {
		if rv.IsNil() {
			return evaluation(path+".items", "each items cannot be null")
		}
		rv = rv.Elem()
	}
	if !rv.IsValid() || (rv.Kind() != reflect.Array && rv.Kind() != reflect.Slice) {
		return evaluation(path+".items", "each items must be an array")
	}
	keys := map[string]struct{}{}
	for i := 0; i < rv.Len(); i++ {
		if out.exceeded {
			return fail(CodeRender, path, "rendered HTML exceeds the response budget")
		}
		locals := make(map[string]any, len(ctx.env.locals)+2)
		for key, value := range ctx.env.locals {
			locals[key] = value
		}
		locals[n.Item] = rv.Index(i).Interface()
		if n.Index != "" {
			locals[n.Index] = i
		}
		loop := ctx
		loop.env = environment{props: ctx.env.props, locals: locals, now: ctx.env.now}
		if n.When != nil {
			include, err := evaluate(n.When, loop.env, path+".when")
			if err != nil {
				return err
			}
			if !truthy(include) {
				continue
			}
		}
		key, err := evaluate(n.Key, loop.env, path+".key")
		if err != nil {
			return err
		}
		keyString, err := scalarString(key)
		if err != nil || key == nil {
			return evaluation(path+".key", "each key must be a non-null scalar")
		}
		if _, exists := keys[keyString]; exists {
			return evaluation(path+".key", fmt.Sprintf("duplicate key %q", keyString))
		}
		keys[keyString] = struct{}{}
		if err := r.node(out, n.Body, loop, fmt.Sprintf("%s.body[%d]", path, i)); err != nil {
			return err
		}
	}
	return nil
}

func (r *Renderer) children(out *renderBuffer, children []renderplan.Node, ctx renderContext, path string) error {
	for i := 0; i < len(children); i++ {
		if out.exceeded {
			return fail(CodeRender, path, "rendered HTML exceeds the response budget")
		}
		// Browsers insert tbody around direct table rows. Emit it explicitly so
		// the server DOM is stable before hydration.
		if strings.EqualFold(ctx.parentTag, "table") && isElementTag(children[i], "tr") {
			out.WriteString("<tbody>")
			for i < len(children) && isElementTag(children[i], "tr") {
				if err := r.node(out, children[i], withParent(ctx, "tbody"), fmt.Sprintf("%s[%d]", path, i)); err != nil {
					return err
				}
				i++
			}
			out.WriteString("</tbody>")
			i--
			continue
		}
		if i > 0 && isTextNode(children[i-1]) && isTextNode(children[i]) {
			out.WriteString("<!-- -->")
		}
		if err := r.node(out, children[i], ctx, fmt.Sprintf("%s[%d]", path, i)); err != nil {
			return err
		}
	}
	return nil
}

func (r *Renderer) element(out *renderBuffer, n *renderplan.Element, ctx renderContext, path string) error {
	tag := n.Tag
	lower := strings.ToLower(tag)
	namespace := n.Namespace
	if namespace == "" {
		namespace = ctx.namespace
	}
	if lower == "svg" {
		namespace = renderplan.NamespaceSVG
	}
	if isVoid(lower) && len(n.Children) > 0 {
		return fail(CodeRender, path+".children", fmt.Sprintf("void element <%s> cannot have children", tag))
	}
	attrs, err := r.attributes(n.Attributes, ctx.env, path+".attributes")
	if err != nil {
		return err
	}
	selection := ctx.selection
	if lower == "select" {
		selection = selectedValues(attrs)
		attrs = withoutAttribute(attrs, "value")
	}
	if lower == "option" && selection != nil {
		value := attributeString(attrs, "value")
		if value == "" {
			value = plainNodeText(n.Children, ctx.env)
		}
		if selection[value] {
			attrs = upsertBoolean(attrs, "selected")
		}
	}
	var textarea *string
	if lower == "textarea" {
		if value, ok := findAttribute(attrs, "value"); ok {
			str, err := scalarString(value.value)
			if err != nil {
				return fail(CodeRender, path+".attributes", "textarea value must be scalar")
			}
			textarea = &str
			attrs = withoutAttribute(attrs, "value")
		}
	}
	out.appendByte('<')
	out.WriteString(tag)
	for _, attr := range attrs {
		serialized, include, err := serializeAttribute(attr, namespace, path)
		if err != nil {
			return err
		}
		if include {
			out.appendByte(' ')
			out.WriteString(serialized)
		}
	}
	out.appendByte('>')
	if isVoid(lower) {
		return nil
	}
	childCtx := ctx
	childCtx.namespace = namespace
	childCtx.parentTag = lower
	childCtx.selection = selection
	if lower == "foreignobject" {
		childCtx.namespace = renderplan.NamespaceHTML
	}
	if textarea != nil {
		if strings.HasPrefix(*textarea, "\n") {
			// The HTML parser strips a textarea's first newline. React emits an
			// extra one so the DOM value remains identical during hydration.
			out.appendByte('\n')
		}
		out.WriteString(escapeText(*textarea))
	} else {
		if (lower == "pre" || lower == "listing") && firstNodeTextStartsWithNewline(n.Children, ctx.env) {
			// HTML parsing strips one leading LF in these elements. React emits a
			// compensating LF so the parsed text node still matches its VDOM.
			out.appendByte('\n')
		}
		if err := r.children(out, n.Children, childCtx, path+".children"); err != nil {
			return err
		}
	}
	out.WriteString("</")
	out.WriteString(tag)
	out.appendByte('>')
	return nil
}

type evaluatedAttribute struct {
	name  string
	mode  renderplan.AttributeMode
	value any
}

func (r *Renderer) attributes(attrs []renderplan.Attribute, env environment, path string) ([]evaluatedAttribute, error) {
	result := make([]evaluatedAttribute, 0, len(attrs))
	for i, attr := range attrs {
		name := reactAttributeName(attr.Name)
		if strings.HasPrefix(strings.ToLower(name), "on") || strings.EqualFold(name, "dangerouslySetInnerHTML") || name == "key" || name == "ref" {
			return nil, fail(CodeRender, fmt.Sprintf("%s[%d].name", path, i), "attribute is not portable server markup")
		}
		value, err := evaluate(attr.Value, env, fmt.Sprintf("%s[%d].value", path, i))
		if err != nil {
			return nil, err
		}
		mode := attr.Mode
		if mode == "" {
			mode = renderplan.AttributeString
		}
		if booleanAttributes[strings.ToLower(name)] {
			mode = renderplan.AttributeBoolean
		}
		result = append(result, evaluatedAttribute{name: name, mode: mode, value: value})
	}
	return result, nil
}

func serializeAttribute(attr evaluatedAttribute, namespace renderplan.Namespace, path string) (string, bool, error) {
	if attr.value == nil {
		return "", false, nil
	}
	name := attr.name
	if namespace == renderplan.NamespaceSVG {
		name = svgAttributeName(name)
	}
	lowerName := strings.ToLower(name)
	if enumeratedBooleanAttributes[lowerName] {
		if boolean, ok := attr.value.(bool); ok {
			return name + `="` + strconv.FormatBool(boolean) + `"`, true, nil
		}
	}
	if overloadedBooleanAttributes[lowerName] {
		if boolean, ok := attr.value.(bool); ok {
			if !boolean {
				return "", false, nil
			}
			return name + `=""`, true, nil
		}
	}
	switch attr.mode {
	case renderplan.AttributeBoolean:
		if !truthy(attr.value) {
			return "", false, nil
		}
		return name + `=""`, true, nil
	case renderplan.AttributeURL:
		value, err := scalarString(attr.value)
		if err != nil {
			return "", false, fail(CodeRender, path, "URL attribute must be scalar")
		}
		if err := validateURL(value); err != nil {
			return "", false, &Error{Code: CodeUnsafeURL, Path: path, Message: err.Error()}
		}
		return name + `="` + escapeAttribute(value) + `"`, true, nil
	case renderplan.AttributeStyle:
		value, err := renderStyle(attr.value)
		if err != nil {
			return "", false, fail(CodeRender, path, err.Error())
		}
		if value == "" {
			return "", false, nil
		}
		return name + `="` + escapeAttribute(value) + `"`, true, nil
	case renderplan.AttributeString:
		if boolean, ok := attr.value.(bool); ok && !strings.HasPrefix(strings.ToLower(name), "data-") && !strings.HasPrefix(strings.ToLower(name), "aria-") {
			_ = boolean
			return "", false, nil
		}
		value, err := scalarString(attr.value)
		if err != nil {
			return "", false, fail(CodeRender, path, "attribute value must be scalar")
		}
		return name + `="` + escapeAttribute(value) + `"`, true, nil
	default:
		return "", false, fail(CodeRender, path, "unsupported attribute mode")
	}
}

func renderStyle(value any) (string, error) {
	if ordered, ok := value.(orderedStyle); ok {
		return renderOrderedStyle(ordered)
	}
	rv := reflect.ValueOf(value)
	for rv.IsValid() && (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface) {
		if rv.IsNil() {
			return "", nil
		}
		rv = rv.Elem()
	}
	if !rv.IsValid() {
		return "", nil
	}
	if rv.Kind() != reflect.Map || rv.Type().Key().Kind() != reflect.String {
		return "", fmt.Errorf("style must be an object")
	}
	keys := rv.MapKeys()
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	var out strings.Builder
	for _, key := range keys {
		raw := rv.MapIndex(key)
		for raw.Kind() == reflect.Interface || raw.Kind() == reflect.Pointer {
			if raw.IsNil() {
				break
			}
			raw = raw.Elem()
		}
		if !raw.IsValid() {
			continue
		}
		name := cssName(key.String())
		var text string
		if number, ok := numberValue(raw.Interface()); ok && !unitlessCSS[strings.ToLower(name)] && number != 0 {
			text = formatJSNumber(number, 64) + "px"
		} else {
			var err error
			text, err = scalarString(raw.Interface())
			if err != nil {
				return "", fmt.Errorf("style %s must be scalar", key.String())
			}
		}
		if text == "" {
			continue
		}
		unsafe := strings.ToLower(text)
		if strings.ContainsAny(text, ";<>") || strings.Contains(unsafe, "expression(") || strings.Contains(unsafe, "javascript:") {
			return "", fmt.Errorf("style %s contains unsafe characters", key.String())
		}
		out.WriteString(name)
		out.WriteByte(':')
		out.WriteString(text)
		out.WriteByte(';')
	}
	return out.String(), nil
}

func renderOrderedStyle(properties orderedStyle) (string, error) {
	var out strings.Builder
	for _, property := range properties {
		name := cssName(property.name)
		text, err := styleValue(name, property.value)
		if err != nil {
			return "", fmt.Errorf("style %s: %w", property.name, err)
		}
		if text == "" {
			continue
		}
		out.WriteString(name)
		out.WriteByte(':')
		out.WriteString(text)
		out.WriteByte(';')
	}
	return out.String(), nil
}

func styleValue(name string, value any) (string, error) {
	if number, ok := numberValue(value); ok && !unitlessCSS[strings.ToLower(name)] && number != 0 {
		return formatJSNumber(number, 64) + "px", nil
	}
	text, err := scalarString(value)
	if err != nil {
		return "", fmt.Errorf("must be scalar")
	}
	unsafe := strings.ToLower(text)
	if strings.ContainsAny(text, ";<>") || strings.Contains(unsafe, "expression(") || strings.Contains(unsafe, "javascript:") {
		return "", fmt.Errorf("contains unsafe characters")
	}
	return text, nil
}

func validateURL(value string) error {
	trimmed := strings.TrimSpace(value)
	for _, r := range trimmed {
		if unicode.IsControl(r) {
			return fmt.Errorf("URL contains control characters")
		}
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("invalid URL")
	}
	if parsed.Scheme != "" {
		switch strings.ToLower(parsed.Scheme) {
		case "http", "https", "mailto", "tel":
		default:
			return fmt.Errorf("URL scheme %q is not allowed", parsed.Scheme)
		}
	}
	return nil
}
func escapeText(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(value)
}
func escapeAttribute(value string) string { return html.EscapeString(value) }
func renderText(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	if boolean, ok := value.(bool); ok {
		_ = boolean
		return "", nil
	}
	return scalarString(value)
}

func reactAttributeName(name string) string {
	if mapped, ok := map[string]string{"className": "class", "htmlFor": "for", "httpEquiv": "http-equiv", "acceptCharset": "accept-charset", "charSet": "charset", "crossOrigin": "crossorigin"}[name]; ok {
		return mapped
	}
	return name
}
func svgAttributeName(name string) string {
	if mapped, ok := map[string]string{
		"xlinkHref": "xlink:href", "xlinkActuate": "xlink:actuate", "xlinkArcrole": "xlink:arcrole", "xlinkRole": "xlink:role", "xlinkShow": "xlink:show", "xlinkTitle": "xlink:title", "xlinkType": "xlink:type",
		"xmlBase": "xml:base", "xmlLang": "xml:lang", "xmlSpace": "xml:space", "xmlnsXlink": "xmlns:xlink", "tabIndex": "tabindex", "viewbox": "viewBox",
		"accentHeight": "accent-height", "alignmentBaseline": "alignment-baseline", "arabicForm": "arabic-form", "baselineShift": "baseline-shift", "capHeight": "cap-height",
		"clipPath": "clip-path", "clipRule": "clip-rule", "colorInterpolation": "color-interpolation", "colorInterpolationFilters": "color-interpolation-filters", "colorProfile": "color-profile", "colorRendering": "color-rendering",
		"dominantBaseline": "dominant-baseline", "enableBackground": "enable-background", "fillOpacity": "fill-opacity", "fillRule": "fill-rule", "floodColor": "flood-color", "floodOpacity": "flood-opacity",
		"fontFamily": "font-family", "fontSize": "font-size", "fontSizeAdjust": "font-size-adjust", "fontStretch": "font-stretch", "fontStyle": "font-style", "fontVariant": "font-variant", "fontWeight": "font-weight",
		"glyphName": "glyph-name", "glyphOrientationHorizontal": "glyph-orientation-horizontal", "glyphOrientationVertical": "glyph-orientation-vertical", "horizAdvX": "horiz-adv-x", "horizOriginX": "horiz-origin-x",
		"imageRendering": "image-rendering", "letterSpacing": "letter-spacing", "lightingColor": "lighting-color", "markerEnd": "marker-end", "markerMid": "marker-mid", "markerStart": "marker-start",
		"overlinePosition": "overline-position", "overlineThickness": "overline-thickness", "paintOrder": "paint-order", "pointerEvents": "pointer-events", "renderingIntent": "rendering-intent", "shapeRendering": "shape-rendering",
		"stopColor": "stop-color", "stopOpacity": "stop-opacity", "strikethroughPosition": "strikethrough-position", "strikethroughThickness": "strikethrough-thickness", "strokeDasharray": "stroke-dasharray", "strokeDashoffset": "stroke-dashoffset",
		"strokeLinecap": "stroke-linecap", "strokeLinejoin": "stroke-linejoin", "strokeMiterlimit": "stroke-miterlimit", "strokeOpacity": "stroke-opacity", "strokeWidth": "stroke-width",
		"textAnchor": "text-anchor", "textDecoration": "text-decoration", "textRendering": "text-rendering", "transformOrigin": "transform-origin", "underlinePosition": "underline-position", "underlineThickness": "underline-thickness",
		"unicodeBidi": "unicode-bidi", "unicodeRange": "unicode-range", "unitsPerEm": "units-per-em", "vAlphabetic": "v-alphabetic", "vHanging": "v-hanging", "vIdeographic": "v-ideographic", "vMathematical": "v-mathematical",
		"vectorEffect": "vector-effect", "vertAdvY": "vert-adv-y", "vertOriginX": "vert-origin-x", "vertOriginY": "vert-origin-y", "wordSpacing": "word-spacing", "writingMode": "writing-mode", "xHeight": "x-height",
	}[name]; ok {
		return mapped
	}
	return name
}
func cssName(name string) string {
	if strings.HasPrefix(name, "--") {
		return name
	}
	for _, prefix := range []struct{ source, target string }{{"Webkit", "-webkit-"}, {"Moz", "-moz-"}, {"ms", "-ms-"}, {"O", "-o-"}} {
		source, target := prefix.source, prefix.target
		if strings.HasPrefix(name, source) {
			rest := strings.TrimPrefix(name, source)
			first, _ := utf8.DecodeRuneInString(rest)
			if unicode.IsUpper(first) {
				return target + strings.TrimPrefix(camelToKebab(rest), "-")
			}
		}
	}
	return camelToKebab(name)
}

func camelToKebab(name string) string {
	var out strings.Builder
	for _, r := range name {
		if unicode.IsUpper(r) {
			out.WriteByte('-')
			out.WriteRune(unicode.ToLower(r))
		} else {
			out.WriteRune(r)
		}
	}
	return out.String()
}
func isVoid(tag string) bool { return voidElements[tag] }
func isElementTag(node renderplan.Node, tag string) bool {
	element, ok := node.(*renderplan.Element)
	return ok && strings.EqualFold(element.Tag, tag)
}
func isTextNode(node renderplan.Node) bool                      { _, ok := node.(*renderplan.Text); return ok }
func withParent(ctx renderContext, parent string) renderContext { ctx.parentTag = parent; return ctx }
func plainNodeText(nodes []renderplan.Node, env environment) string {
	var out strings.Builder
	for _, node := range nodes {
		if text, ok := node.(*renderplan.Text); ok {
			value, err := evaluate(text.Value, env, "")
			if err == nil {
				s, _ := renderText(value)
				out.WriteString(s)
			}
		}
	}
	return out.String()
}
func firstNodeTextStartsWithNewline(nodes []renderplan.Node, env environment) bool {
	if len(nodes) == 0 {
		return false
	}
	text, ok := nodes[0].(*renderplan.Text)
	if !ok {
		return false
	}
	value, err := evaluate(text.Value, env, "")
	if err != nil {
		return false
	}
	rendered, err := renderText(value)
	return err == nil && strings.HasPrefix(rendered, "\n")
}
func selectedValues(attrs []evaluatedAttribute) map[string]bool {
	attr, ok := findAttribute(attrs, "value")
	if !ok {
		return nil
	}
	result := map[string]bool{}
	rv := reflect.ValueOf(attr.value)
	if rv.IsValid() && (rv.Kind() == reflect.Array || rv.Kind() == reflect.Slice) {
		for i := 0; i < rv.Len(); i++ {
			if value, err := scalarString(rv.Index(i).Interface()); err == nil {
				result[value] = true
			}
		}
	} else if value, err := scalarString(attr.value); err == nil {
		result[value] = true
	}
	return result
}
func findAttribute(attrs []evaluatedAttribute, name string) (evaluatedAttribute, bool) {
	for _, attr := range attrs {
		if strings.EqualFold(attr.name, name) {
			return attr, true
		}
	}
	return evaluatedAttribute{}, false
}
func withoutAttribute(attrs []evaluatedAttribute, name string) []evaluatedAttribute {
	result := attrs[:0]
	for _, attr := range attrs {
		if !strings.EqualFold(attr.name, name) {
			result = append(result, attr)
		}
	}
	return result
}
func attributeString(attrs []evaluatedAttribute, name string) string {
	attr, ok := findAttribute(attrs, name)
	if !ok {
		return ""
	}
	value, _ := scalarString(attr.value)
	return value
}
func upsertBoolean(attrs []evaluatedAttribute, name string) []evaluatedAttribute {
	for i := range attrs {
		if strings.EqualFold(attrs[i].name, name) {
			attrs[i].mode = renderplan.AttributeBoolean
			attrs[i].value = true
			return attrs
		}
	}
	return append(attrs, evaluatedAttribute{name: name, mode: renderplan.AttributeBoolean, value: true})
}

var voidElements = map[string]bool{"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true, "input": true, "link": true, "meta": true, "param": true, "source": true, "track": true, "wbr": true}
var booleanAttributes = map[string]bool{"allowfullscreen": true, "async": true, "autofocus": true, "autoplay": true, "checked": true, "controls": true, "default": true, "defer": true, "disabled": true, "formnovalidate": true, "hidden": true, "inert": true, "ismap": true, "itemscope": true, "loop": true, "multiple": true, "muted": true, "nomodule": true, "novalidate": true, "open": true, "playsinline": true, "readonly": true, "required": true, "reversed": true, "scoped": true, "seamless": true, "selected": true}
var enumeratedBooleanAttributes = map[string]bool{"contenteditable": true, "draggable": true, "spellcheck": true}
var overloadedBooleanAttributes = map[string]bool{"capture": true, "download": true}
var unitlessCSS = stringSet("animation-iteration-count aspect-ratio border-image-outset border-image-slice border-image-width box-flex box-flex-group box-ordinal-group column-count columns flex flex-grow flex-positive flex-shrink flex-negative flex-order grid-area grid-row grid-row-end grid-row-span grid-row-start grid-column grid-column-end grid-column-span grid-column-start font-weight line-clamp line-height opacity order orphans scale tab-size widows z-index zoom fill-opacity flood-opacity stop-opacity stroke-dasharray stroke-dashoffset stroke-miterlimit stroke-opacity stroke-width -moz-animation-iteration-count -moz-box-flex -moz-box-flex-group -moz-line-clamp -ms-animation-iteration-count -ms-flex -ms-zoom -ms-flex-grow -ms-flex-negative -ms-flex-order -ms-flex-positive -ms-flex-shrink -ms-grid-column -ms-grid-column-span -ms-grid-row -ms-grid-row-span -webkit-animation-iteration-count -webkit-box-flex -webkit-box-flex-group -webkit-box-ordinal-group -webkit-column-count -webkit-columns -webkit-flex -webkit-flex-grow -webkit-flex-positive -webkit-flex-shrink -webkit-line-clamp")

func stringSet(values string) map[string]bool {
	result := make(map[string]bool)
	for _, value := range strings.Fields(values) {
		result[value] = true
	}
	return result
}

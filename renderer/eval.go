package renderer

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/Origens-Dev/gobeyond/renderplan"
)

type environment struct {
	props  any
	locals map[string]any
	now    time.Time
}

type orderedStyleProperty struct {
	name  string
	value any
}

type orderedStyle []orderedStyleProperty

func evaluate(expr renderplan.Expression, env environment, path string) (any, error) {
	switch e := expr.(type) {
	case *renderplan.Literal:
		return e.Value, nil
	case *renderplan.Path:
		return evalPath(e, env, path)
	case *renderplan.Binary:
		return evalBinary(e, env, path)
	case *renderplan.Unary:
		value, err := evaluate(e.Operand, env, path+".operand")
		if err != nil {
			return nil, err
		}
		switch e.Operator {
		case "!":
			return !truthy(value), nil
		case "-":
			number, ok := numberValue(value)
			if !ok {
				return nil, evaluation(path, "unary - requires a number")
			}
			return -number, nil
		default:
			return nil, evaluation(path, "unsupported unary operator")
		}
	case *renderplan.Helper:
		return evalHelper(e, env, path)
	case *renderplan.Intrinsic:
		return evalIntrinsic(e, env, path)
	default:
		return nil, evaluation(path, fmt.Sprintf("unsupported expression %T", expr))
	}
}

func evalPath(expr *renderplan.Path, env environment, path string) (any, error) {
	segments := expr.Path
	var value any
	start := 0
	first := segments[0]
	if !first.IsIdx {
		if first.Name == "props" {
			value, start = env.props, 1
		} else if local, ok := env.locals[first.Name]; ok {
			value, start = local, 1
		} else {
			value = env.props
		}
	} else {
		value = env.props
	}
	for i := start; i < len(segments); i++ {
		var err error
		value, err = lookup(value, segments[i])
		if err != nil {
			return nil, evaluation(fmt.Sprintf("%s.path[%d]", path, i), err.Error())
		}
	}
	return value, nil
}

func lookup(value any, segment renderplan.PathSegment) (any, error) {
	if value == nil {
		return nil, fmt.Errorf("cannot read a property from null")
	}
	rv := reflect.ValueOf(value)
	for rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface {
		if rv.IsNil() {
			return nil, fmt.Errorf("cannot read a property from null")
		}
		rv = rv.Elem()
	}
	if segment.IsIdx {
		if rv.Kind() != reflect.Array && rv.Kind() != reflect.Slice {
			return nil, fmt.Errorf("integer segment requires an array")
		}
		if segment.Index < 0 || segment.Index >= rv.Len() {
			return nil, fmt.Errorf("array index %d is out of bounds", segment.Index)
		}
		return rv.Index(segment.Index).Interface(), nil
	}
	switch rv.Kind() {
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return nil, fmt.Errorf("object map must use string keys")
		}
		result := rv.MapIndex(reflect.ValueOf(segment.Name).Convert(rv.Type().Key()))
		if !result.IsValid() {
			return nil, fmt.Errorf("property %q does not exist", segment.Name)
		}
		return result.Interface(), nil
	case reflect.Struct:
		typeOf := rv.Type()
		for i := 0; i < rv.NumField(); i++ {
			field := typeOf.Field(i)
			if field.PkgPath != "" {
				continue
			}
			name := field.Name
			if tag := field.Tag.Get("json"); tag != "" {
				if parsed := strings.Split(tag, ",")[0]; parsed == "-" {
					continue
				} else if parsed != "" {
					name = parsed
				}
			}
			if name == segment.Name {
				return rv.Field(i).Interface(), nil
			}
		}
		return nil, fmt.Errorf("property %q does not exist", segment.Name)
	default:
		return nil, fmt.Errorf("property segment requires an object")
	}
}

func evalBinary(expr *renderplan.Binary, env environment, path string) (any, error) {
	left, err := evaluate(expr.Left, env, path+".left")
	if err != nil {
		return nil, err
	}
	switch expr.Operator {
	case "&&":
		if !truthy(left) {
			return left, nil
		}
		return evaluate(expr.Right, env, path+".right")
	case "||":
		if truthy(left) {
			return left, nil
		}
		return evaluate(expr.Right, env, path+".right")
	case "??":
		if left != nil {
			return left, nil
		}
		return evaluate(expr.Right, env, path+".right")
	}
	right, err := evaluate(expr.Right, env, path+".right")
	if err != nil {
		return nil, err
	}
	switch expr.Operator {
	case "+":
		ln, lok := numberValue(left)
		rn, rok := numberValue(right)
		if lok && rok {
			return ln + rn, nil
		}
		ls, lerr := scalarString(left)
		rs, rerr := scalarString(right)
		if lerr != nil || rerr != nil {
			return nil, evaluation(path, "+ requires numbers or scalar values")
		}
		return ls + rs, nil
	case "-", "*", "/", "%":
		ln, lok := numberValue(left)
		rn, rok := numberValue(right)
		if !lok || !rok {
			return nil, evaluation(path, expr.Operator+" requires numbers")
		}
		switch expr.Operator {
		case "-":
			return ln - rn, nil
		case "*":
			return ln * rn, nil
		case "/":
			return ln / rn, nil
		default:
			return math.Mod(ln, rn), nil
		}
	case "==", "!=":
		equal := valuesEqual(left, right)
		if expr.Operator == "!=" {
			equal = !equal
		}
		return equal, nil
	case "<", "<=", ">", ">=":
		cmp, ok := compareValues(left, right)
		if !ok {
			return nil, evaluation(path, expr.Operator+" requires two numbers or two strings")
		}
		switch expr.Operator {
		case "<":
			return cmp < 0, nil
		case "<=":
			return cmp <= 0, nil
		case ">":
			return cmp > 0, nil
		default:
			return cmp >= 0, nil
		}
	default:
		return nil, evaluation(path, "unsupported binary operator")
	}
}

func evalHelper(expr *renderplan.Helper, env environment, path string) (any, error) {
	args := make([]any, len(expr.Arguments))
	for i, arg := range expr.Arguments {
		value, err := evaluate(arg, env, fmt.Sprintf("%s.arguments[%d]", path, i))
		if err != nil {
			return nil, err
		}
		args[i] = value
	}
	require := func(count int) error {
		if len(args) != count {
			return evaluation(path, fmt.Sprintf("helper %s expects %d arguments", expr.Name, count))
		}
		return nil
	}
	switch expr.Name {
	case "style":
		if len(args)%2 != 0 {
			return nil, evaluation(path, "style requires name/value pairs")
		}
		style := make(orderedStyle, 0, len(args)/2)
		for index := 0; index < len(args); index += 2 {
			name, ok := args[index].(string)
			if !ok || name == "" {
				return nil, evaluation(path, "style property names must be strings")
			}
			style = append(style, orderedStyleProperty{name: name, value: args[index+1]})
		}
		return style, nil
	case "string":
		if err := require(1); err != nil {
			return nil, err
		}
		return scalarString(args[0])
	case "lower", "upper":
		if err := require(1); err != nil {
			return nil, err
		}
		value, err := scalarString(args[0])
		if err != nil {
			return nil, evaluation(path, err.Error())
		}
		if expr.Name == "lower" {
			return strings.ToLower(value), nil
		}
		return strings.ToUpper(value), nil
	case "url":
		if err := require(1); err != nil {
			return nil, err
		}
		value, err := scalarString(args[0])
		if err != nil {
			return nil, evaluation(path, err.Error())
		}
		return url.QueryEscape(value), nil
	case "join":
		if err := require(2); err != nil {
			return nil, err
		}
		separator, err := scalarString(args[1])
		if err != nil {
			return nil, evaluation(path, "join separator must be scalar")
		}
		rv := reflect.ValueOf(args[0])
		for rv.IsValid() && (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface) {
			if rv.IsNil() {
				return nil, evaluation(path, "join items cannot be null")
			}
			rv = rv.Elem()
		}
		if !rv.IsValid() || (rv.Kind() != reflect.Array && rv.Kind() != reflect.Slice) {
			return nil, evaluation(path, "join items must be an array")
		}
		values := make([]string, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			values[i], err = scalarString(rv.Index(i).Interface())
			if err != nil {
				return nil, evaluation(path, "join items must be scalar")
			}
		}
		return strings.Join(values, separator), nil
	default:
		return nil, evaluation(path, "unsupported helper")
	}
}

func evalIntrinsic(expr *renderplan.Intrinsic, env environment, path string) (any, error) {
	args := make([]any, len(expr.Arguments))
	for i, arg := range expr.Arguments {
		value, err := evaluate(arg, env, fmt.Sprintf("%s.arguments[%d]", path, i))
		if err != nil {
			return nil, err
		}
		args[i] = value
	}
	spec, ok := renderplan.IntrinsicDefinition(expr.Name)
	if !ok {
		return nil, evaluation(path, "unsupported intrinsic")
	}
	if len(args) != spec.Arity {
		return nil, evaluation(path, fmt.Sprintf("intrinsic %s expects %d arguments", expr.Name, spec.Arity))
	}
	switch expr.Name {
	case renderplan.IntrinsicDateGetFullYear:
		return env.now.Year(), nil
	case renderplan.IntrinsicDateGetUTCFullYear:
		return env.now.UTC().Year(), nil
	default:
		return nil, evaluation(path, "unsupported intrinsic")
	}
}

func truthy(value any) bool {
	if value == nil {
		return false
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return v != ""
	case json.Number:
		f, e := v.Float64()
		return e == nil && f != 0 && !math.IsNaN(f)
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.String:
		return rv.Len() != 0
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int() != 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return rv.Uint() != 0
	case reflect.Float32, reflect.Float64:
		f := rv.Float()
		return f != 0 && !math.IsNaN(f)
	case reflect.Pointer, reflect.Interface:
		return !rv.IsNil()
	}
	return true
}

func numberValue(value any) (float64, bool) {
	if n, ok := value.(json.Number); ok {
		f, e := n.Float64()
		return f, e == nil
	}
	rv := reflect.ValueOf(value)
	if !rv.IsValid() {
		return 0, false
	}
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(rv.Uint()), true
	case reflect.Float32, reflect.Float64:
		return rv.Float(), true
	}
	return 0, false
}
func scalarString(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	switch v := value.(type) {
	case string:
		return v, nil
	case []byte:
		return base64.StdEncoding.EncodeToString(v), nil
	case bool:
		if v {
			return "true", nil
		}
		return "false", nil
	case json.Number:
		number, err := v.Float64()
		if err != nil {
			return "", fmt.Errorf("invalid JSON number %q", v.String())
		}
		return formatJSNumber(number, 64), nil
	case time.Time:
		return v.Format(time.RFC3339Nano), nil
	case fmt.Stringer:
		return v.String(), nil
	}
	rv := reflect.ValueOf(value)
	for rv.IsValid() && (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface) {
		if rv.IsNil() {
			return "", nil
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.String:
		return rv.String(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(rv.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(rv.Uint(), 10), nil
	case reflect.Float32, reflect.Float64:
		return formatJSNumber(rv.Float(), rv.Type().Bits()), nil
	}
	return "", fmt.Errorf("value %T is not scalar", value)
}

// formatJSNumber uses JavaScript's decimal/exponent thresholds for finite,
// JSON-compatible numbers. Go's %g switches to exponent notation much sooner.
func formatJSNumber(value float64, bits int) string {
	if math.IsNaN(value) {
		return "NaN"
	}
	if math.IsInf(value, 1) {
		return "Infinity"
	}
	if math.IsInf(value, -1) {
		return "-Infinity"
	}
	if value == 0 {
		return "0"
	}
	absolute := math.Abs(value)
	if absolute >= 1e21 || absolute < 1e-6 {
		encoded := strconv.FormatFloat(value, 'e', -1, bits)
		parts := strings.SplitN(encoded, "e", 2)
		exponent := parts[1]
		sign := ""
		if strings.HasPrefix(exponent, "+") || strings.HasPrefix(exponent, "-") {
			sign, exponent = exponent[:1], exponent[1:]
		}
		exponent = strings.TrimLeft(exponent, "0")
		if exponent == "" {
			exponent = "0"
		}
		return parts[0] + "e" + sign + exponent
	}
	return strconv.FormatFloat(value, 'f', -1, bits)
}
func valuesEqual(a, b any) bool {
	an, aok := numberValue(a)
	bn, bok := numberValue(b)
	if aok && bok {
		return an == bn
	}
	if as, aok := stringValue(a); aok {
		if bs, bok := stringValue(b); bok {
			return as == bs
		}
	}
	return reflect.DeepEqual(a, b)
}
func compareValues(a, b any) (int, bool) {
	an, aok := numberValue(a)
	bn, bok := numberValue(b)
	if aok && bok {
		if an < bn {
			return -1, true
		}
		if an > bn {
			return 1, true
		}
		return 0, true
	}
	as, aok := stringValue(a)
	bs, bok := stringValue(b)
	if aok && bok {
		return strings.Compare(as, bs), true
	}
	return 0, false
}

func stringValue(value any) (string, bool) {
	rv := reflect.ValueOf(value)
	for rv.IsValid() && (rv.Kind() == reflect.Pointer || rv.Kind() == reflect.Interface) {
		if rv.IsNil() {
			return "", false
		}
		rv = rv.Elem()
	}
	if rv.IsValid() && rv.Kind() == reflect.String {
		return rv.String(), true
	}
	return "", false
}
func evaluation(path, message string) error { return fail(CodeEvaluation, path, message) }

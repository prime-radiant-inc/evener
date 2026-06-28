package schemagen

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"

	"pgregory.net/rapid"
)

// Default numeric ranges for unbounded integer/number schemas. The integer
// bound stays within float64's exact-integer range so a generated value
// round-trips through JSON without precision loss.
const (
	defaultIntLo = -(1 << 53)
	defaultIntHi = 1 << 53
)

const (
	defaultFloatLo = -1e308
	defaultFloatHi = 1e308
)

// schemaTypes returns the normalized list of JSON type names a schema allows.
// JSON Schema permits "type" as a single string or an array of strings; serf
// also writes native Go literals (string, []string, []any), so both wire and
// in-process shapes are accepted. An absent type returns nil (any type allowed).
func schemaTypes(schema map[string]any) []string {
	return stringList(schema["type"])
}

// enumValues returns the schema's enum members, or nil. Members are returned as
// generic any values so heterogeneous enums round-trip unchanged.
func enumValues(schema map[string]any) []any {
	return anyList(schema["enum"])
}

// additionalPropsAllowed reports whether the schema permits keys beyond those in
// properties. Absent means allowed (JSON Schema default); an explicit false
// closes the object; a subschema object is treated as open for generation.
func additionalPropsAllowed(schema map[string]any) bool {
	v, ok := schema["additionalProperties"]
	if !ok {
		return true
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return true
}

// chooseType picks one type to generate from the allowed set. An empty set means
// the schema is untyped, so any kind is fair game; at max depth the choice is
// restricted to scalars to bound recursion.
func chooseType(t *rapid.T, types []string, depth int) string {
	if len(types) == 0 {
		scalars := []string{"string", "integer", "number", "boolean", "null"}
		if depth < maxDepth {
			scalars = append(scalars, "object", "array")
		}
		return rapid.SampledFrom(scalars).Draw(t, "anytype")
	}
	if len(types) == 1 {
		return types[0]
	}
	return rapid.SampledFrom(types).Draw(t, "uniontype")
}

// asSchemaMap coerces a subschema value to map[string]any. JSON-decoded schemas
// are already map[string]any; this also tolerates a nil or non-map (returns an
// empty schema, i.e. "any").
func asSchemaMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// stringList normalizes a value that may be a single string, []string, or []any
// of strings into a []string. Non-string members are rendered with fmt so a
// best-effort list is always returned.
func stringList(v any) []string {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		return []string{x}
	case []string:
		return append([]string(nil), x...)
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			} else {
				out = append(out, fmt.Sprint(e))
			}
		}
		return out
	default:
		return nil
	}
}

// anyList normalizes an enum value (any typed slice, e.g. []string/[]any/[]int)
// into []any via reflection so heterogeneous enums are preserved.
func anyList(v any) []any {
	if v == nil {
		return nil
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Slice {
		return nil
	}
	out := make([]any, 0, rv.Len())
	for i := 0; i < rv.Len(); i++ {
		out = append(out, rv.Index(i).Interface())
	}
	return out
}

// sortedKeys returns a schema map's keys in deterministic order so generation is
// reproducible under a fixed seed regardless of Go map iteration order.
func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func containsAny(haystack []any, needle any) bool {
	for _, v := range haystack {
		if reflect.DeepEqual(v, needle) {
			return true
		}
	}
	return false
}

// typeCovers reports whether the allowed type set accepts a value of the given
// JSON kind. "integer" is covered by an allowed "number"; an empty set (untyped)
// covers everything.
func typeCovers(allowed []string, kind string) bool {
	if len(allowed) == 0 {
		return true
	}
	for _, a := range allowed {
		if a == kind {
			return true
		}
		if kind == "integer" && a == "number" {
			return true
		}
	}
	return false
}

// intBounds returns the inclusive [lo, hi] integer range a schema permits,
// honoring minimum/maximum and falling back to a wide default. A degenerate
// (inverted) range collapses to a single point so the generator cannot panic.
func intBounds(schema map[string]any) (int, int) {
	lo, hi := defaultIntLo, defaultIntHi
	if v, ok := numBound(schema, "minimum"); ok {
		lo = int(math.Ceil(v))
	}
	if v, ok := numBound(schema, "maximum"); ok {
		hi = int(math.Floor(v))
	}
	if lo > hi {
		lo = hi
	}
	return lo, hi
}

// floatBounds returns the inclusive [lo, hi] float range a schema permits.
func floatBounds(schema map[string]any) (float64, float64) {
	lo, hi := defaultFloatLo, defaultFloatHi
	if v, ok := numBound(schema, "minimum"); ok {
		lo = v
	}
	if v, ok := numBound(schema, "maximum"); ok {
		hi = v
	}
	if lo > hi {
		lo = hi
	}
	return lo, hi
}

// numBound reads a numeric schema keyword (minimum/maximum), tolerating the
// several Go shapes a literal or JSON-decoded number can take.
func numBound(schema map[string]any, key string) (float64, bool) {
	v, ok := schema[key]
	if !ok {
		return 0, false
	}
	return toFloat(v)
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

// unknownKey draws a property name that is unlikely to collide with a declared
// property, for additionalProperties exploration.
func unknownKey(t *rapid.T) string {
	return rapid.SampledFrom([]string{
		"__extra__", "x", "unexpected", "0", "$ref", "constructor", "__proto__",
	}).Draw(t, "unknown_key")
}

// Package schemagen turns a JSON Schema (the subset serf's tool and protocol
// surfaces actually use: type/properties/required/enum/additionalProperties/
// items) into a rapid generator of values. It generates BOTH schema-conforming
// values (Valid mode) and schema-adjacent ones (Adjacent mode: wrong types,
// missing-required, out-of-enum, extra-when-closed) so a property test can feed
// a real validator/handler adversarial-but-structured input.
//
// Like the promoter, this package is the portable core of the fuzzing toolkit:
// it imports only the standard library and pgregory.net/rapid, and NOTHING here
// may import any primeradiant.com/serf package. That structural boundary is the
// portability test — point it at any project's JSON Schema and it works.
package schemagen

import "pgregory.net/rapid"

// Mode selects what kind of value the generator produces.
type Mode int

const (
	// Valid generates values that conform to the schema (required present,
	// declared types, enum membership, no extra keys when closed).
	Valid Mode = iota
	// Adjacent generates schema-adjacent values: boundary and violating shapes
	// (wrong type, missing required, out-of-enum, extra key when closed). An
	// Adjacent value is not guaranteed to violate — additionalProperties:true and
	// type-free subschemas accept almost anything — but it explores the boundary.
	Adjacent
)

// maxDepth bounds recursion into untyped / additionalProperties:true subschemas
// so a generator over an open schema cannot diverge.
const maxDepth = 4

// FromJSONSchema builds a generator that yields a mix of schema-valid and
// schema-adjacent values for schema. It is the headline entry point; for a
// mode-specific generator (e.g. an oracle that needs known-valid input) use
// Generator.
func FromJSONSchema(schema map[string]any) *rapid.Generator[any] {
	return rapid.Custom(func(t *rapid.T) any {
		mode := Valid
		if rapid.Bool().Draw(t, "schemagen_adjacent") {
			mode = Adjacent
		}
		return genValue(t, schema, mode, 0)
	})
}

// Generator builds a generator that yields values in a single mode.
func Generator(schema map[string]any, mode Mode) *rapid.Generator[any] {
	return rapid.Custom(func(t *rapid.T) any {
		return genValue(t, schema, mode, 0)
	})
}

// genValue is the recursive core: produce one value for schema in the given mode.
func genValue(t *rapid.T, schema map[string]any, mode Mode, depth int) any {
	if schema == nil {
		schema = map[string]any{}
	}

	if enum := enumValues(schema); len(enum) > 0 {
		if mode == Valid {
			return rapid.SampledFrom(enum).Draw(t, "enum")
		}
		return genEnumViolation(t, enum)
	}

	types := schemaTypes(schema)

	if mode == Adjacent && rapid.Bool().Draw(t, "wrong_type") {
		return genWrongType(t, types)
	}

	switch chooseType(t, types, depth) {
	case "object":
		return genObject(t, schema, mode, depth)
	case "array":
		return genArray(t, schema, mode, depth)
	case "string":
		return genString(t, mode)
	case "integer":
		return genInteger(t, schema, mode)
	case "number":
		return genNumber(t, schema, mode)
	case "boolean":
		return rapid.Bool().Draw(t, "bool")
	case "null":
		return nil
	default:
		return genArbitraryJSON(t, depth)
	}
}

// genObject generates an object value. In Valid mode every required property is
// present with a conforming value, optional properties appear probabilistically,
// and no extra key is added when additionalProperties is false. In Adjacent mode
// one structural violation is introduced where a lever exists (drop a required
// key, add an unknown key under a closed schema, or corrupt one property).
func genObject(t *rapid.T, schema map[string]any, mode Mode, depth int) any {
	props := asSchemaMap(schema["properties"])
	required := stringList(schema["required"])
	open := additionalPropsAllowed(schema)

	obj := map[string]any{}

	// Adjacent levers available for this object, chosen up front so exactly one
	// fires (a second corruption could cancel the first).
	dropRequired := mode == Adjacent && len(required) > 0 && rapid.Bool().Draw(t, "drop_required")
	addUnknown := mode == Adjacent && !open && !dropRequired && rapid.Bool().Draw(t, "add_unknown")
	corruptProp := mode == Adjacent && len(props) > 0 && !dropRequired && !addUnknown

	skip := ""
	if dropRequired {
		skip = rapid.SampledFrom(required).Draw(t, "drop_which")
	}
	corrupt := ""
	if corruptProp {
		corrupt = rapid.SampledFrom(sortedKeys(props)).Draw(t, "corrupt_which")
	}

	for _, name := range sortedKeys(props) {
		sub := asSchemaMap(props[name])
		isRequired := contains(required, name)
		if name == skip {
			continue
		}
		if !isRequired && mode != Adjacent && !rapid.Bool().Draw(t, "include_"+name) {
			continue // optional property omitted in a valid object
		}
		if !isRequired && mode == Adjacent && !rapid.Bool().Draw(t, "include_"+name) {
			continue
		}
		propMode := Valid
		if name == corrupt {
			propMode = Adjacent
		}
		obj[name] = genValue(t, sub, propMode, depth+1)
	}

	if addUnknown {
		obj[unknownKey(t)] = genArbitraryJSON(t, depth+1)
	}
	// An open schema in Valid mode may still carry extra keys (allowed); add some
	// to exercise additionalProperties:true pass-throughs.
	if open && mode == Valid && rapid.Bool().Draw(t, "extra_open") {
		obj[unknownKey(t)] = genArbitraryJSON(t, depth+1)
	}
	return obj
}

// genArray generates an array value using the items subschema (or arbitrary JSON
// when items is absent).
func genArray(t *rapid.T, schema map[string]any, mode Mode, depth int) any {
	items := asSchemaMap(schema["items"])
	n := rapid.IntRange(0, 4).Draw(t, "array_len")
	out := make([]any, 0, n)
	for i := 0; i < n; i++ {
		if len(items) == 0 {
			out = append(out, genArbitraryJSON(t, depth+1))
			continue
		}
		out = append(out, genValue(t, items, mode, depth+1))
	}
	return out
}

// adversarialStrings seeds the string generator with values that probe common
// soft spots (empty, whitespace, path traversal, format-string, unicode edges).
var adversarialStrings = []string{
	"", " ", "\t\n", "0", "false", "null",
	"../../etc/passwd", "/absolute/path", "C:\\win",
	"%s%n", "{{.}}", "<script>", "'\"`",
	"\u00a0", "\ufeff", "\U0001f4a5", "a\x00b",
}

func genString(t *rapid.T, mode Mode) any {
	if rapid.Bool().Draw(t, "string_corpus") {
		return rapid.SampledFrom(adversarialStrings).Draw(t, "string_seed")
	}
	_ = mode
	return rapid.String().Draw(t, "string")
}

// adversarialInts seeds the integer generator with boundary magnitudes.
var adversarialInts = []int{0, 1, -1, 2, 100, -100, 1 << 31, -(1 << 31), 1 << 53}

func genInteger(t *rapid.T, schema map[string]any, mode Mode) any {
	lo, hi := intBounds(schema)
	if mode == Adjacent && rapid.Bool().Draw(t, "int_boundary") {
		// A fractional number is integer-adjacent: same numeric type, not an int.
		return rapid.SampledFrom([]float64{0.5, -0.5, 1e308}).Draw(t, "int_frac")
	}
	corpus := inRangeInts(lo, hi)
	if len(corpus) > 0 && rapid.Bool().Draw(t, "int_corpus") {
		return rapid.SampledFrom(corpus).Draw(t, "int")
	}
	return rapid.IntRange(lo, hi).Draw(t, "int_range")
}

// adversarialFloats seeds the number generator with boundary magnitudes.
var adversarialFloats = []float64{0, 1, -1, 0.5, -0.5, 3.14, 1e6, -1e6, 1e308}

func genNumber(t *rapid.T, schema map[string]any, mode Mode) any {
	_ = mode
	lo, hi := floatBounds(schema)
	corpus := inRangeFloats(lo, hi)
	if len(corpus) > 0 && rapid.Bool().Draw(t, "num_corpus") {
		return rapid.SampledFrom(corpus).Draw(t, "number")
	}
	return rapid.Float64Range(lo, hi).Draw(t, "num_range")
}

func inRangeInts(lo, hi int) []int {
	out := make([]int, 0, len(adversarialInts))
	for _, v := range adversarialInts {
		if v >= lo && v <= hi {
			out = append(out, v)
		}
	}
	return out
}

func inRangeFloats(lo, hi float64) []float64 {
	out := make([]float64, 0, len(adversarialFloats))
	for _, v := range adversarialFloats {
		if v >= lo && v <= hi {
			out = append(out, v)
		}
	}
	return out
}

// genArbitraryJSON produces any JSON value, bounded by depth. Used for untyped
// subschemas and additionalProperties:true pass-throughs.
func genArbitraryJSON(t *rapid.T, depth int) any {
	kinds := []string{"string", "integer", "number", "boolean", "null"}
	if depth < maxDepth {
		kinds = append(kinds, "object", "array")
	}
	switch rapid.SampledFrom(kinds).Draw(t, "json_kind") {
	case "object":
		n := rapid.IntRange(0, 3).Draw(t, "obj_len")
		m := map[string]any{}
		for i := 0; i < n; i++ {
			m[unknownKey(t)] = genArbitraryJSON(t, depth+1)
		}
		return m
	case "array":
		n := rapid.IntRange(0, 3).Draw(t, "arr_len")
		out := make([]any, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, genArbitraryJSON(t, depth+1))
		}
		return out
	case "integer":
		return genInteger(t, nil, Valid)
	case "number":
		return genNumber(t, nil, Valid)
	case "boolean":
		return rapid.Bool().Draw(t, "json_bool")
	case "null":
		return nil
	default:
		return genString(t, Valid)
	}
}

// genWrongType returns a value whose JSON kind is, where possible, NOT among the
// schema's allowed types — a guaranteed type violation for a closed type set.
func genWrongType(t *rapid.T, allowed []string) any {
	for _, kind := range []string{"boolean", "string", "array", "object", "null"} {
		if !typeCovers(allowed, kind) {
			switch kind {
			case "boolean":
				return rapid.Bool().Draw(t, "wt_bool")
			case "string":
				return genString(t, Adjacent)
			case "array":
				return []any{genString(t, Valid)}
			case "object":
				return map[string]any{"wrong": true}
			case "null":
				return nil
			}
		}
	}
	// Every common kind is allowed (very open schema): fall back to a number.
	return genNumber(t, nil, Adjacent)
}

// genEnumViolation returns a value unlikely to be a member of enum.
func genEnumViolation(t *rapid.T, enum []any) any {
	candidates := []any{
		"__not_in_enum__", "", 0, -1, true, false, nil,
		map[string]any{}, []any{},
	}
	for _, c := range candidates {
		if !containsAny(enum, c) {
			// rapid needs a deterministic draw to stay reproducible; bias toward
			// the sentinel string but let the seed pick among safe non-members.
			if rapid.Bool().Draw(t, "enum_pick") {
				return c
			}
		}
	}
	return "__not_in_enum__"
}

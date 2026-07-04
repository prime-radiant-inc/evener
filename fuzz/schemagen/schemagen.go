// Package schemagen turns a JSON Schema (the subset serf's tool and protocol
// surfaces actually use: type/properties/required/enum/additionalProperties/
// items) into generated values. It generates BOTH schema-conforming values
// (Valid mode) and schema-adjacent ones (Adjacent mode: wrong types,
// missing-required, out-of-enum, extra-when-closed) so a property test can feed
// a real validator/handler adversarial-but-structured input.
//
// The same generator definitions draw entropy from a Source (source.go), so one
// rule set drives both a rapid.Check target (rapid-backed Source, via
// FromJSONSchema/Generator) and a coverage-guided testing.F target (byte-backed
// Source, via Value/NewByteSource).
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
		s := rapidSource{t}
		mode := Valid
		if s.Bool("schemagen_adjacent") {
			mode = Adjacent
		}
		return genValue(s, schema, mode, 0)
	})
}

// Generator builds a generator that yields values in a single mode.
func Generator(schema map[string]any, mode Mode) *rapid.Generator[any] {
	return rapid.Custom(func(t *rapid.T) any {
		return genValue(rapidSource{t}, schema, mode, 0)
	})
}

// Value produces a single value for schema in the given mode, drawing entropy
// from an arbitrary Source. It is the source-driven core behind both the rapid
// entry points (via a rapid-backed Source) and the byte-fed testing.F targets
// (via NewByteSource).
func Value(s Source, schema map[string]any, mode Mode) any {
	return genValue(s, schema, mode, 0)
}

// genValue is the recursive core: produce one value for schema in the given mode.
func genValue(s Source, schema map[string]any, mode Mode, depth int) any {
	if schema == nil {
		schema = map[string]any{}
	}

	if enum := enumValues(schema); len(enum) > 0 {
		if mode == Valid {
			return draw(s, enum, "enum")
		}
		return genEnumViolation(s, enum)
	}

	types := schemaTypes(schema)

	if mode == Adjacent && s.Bool("wrong_type") {
		return genWrongType(s, types)
	}

	switch chooseType(s, types, depth) {
	case "object":
		return genObject(s, schema, mode, depth)
	case "array":
		return genArray(s, schema, mode, depth)
	case "string":
		return genString(s, schema, mode)
	case "integer":
		return genInteger(s, schema, mode)
	case "number":
		return genNumber(s, schema, mode)
	case "boolean":
		return s.Bool("bool")
	case "null":
		return nil
	default:
		return genArbitraryJSON(s, depth)
	}
}

// genObject generates an object value. In Valid mode every required property is
// present with a conforming value, optional properties appear probabilistically,
// and no extra key is added when additionalProperties is false. In Adjacent mode
// one structural violation is introduced where a lever exists (drop a required
// key, add an unknown key under a closed schema, or corrupt one property).
func genObject(s Source, schema map[string]any, mode Mode, depth int) any {
	props := asSchemaMap(schema["properties"])
	required := stringList(schema["required"])
	open := additionalPropsAllowed(schema)

	obj := map[string]any{}

	// Adjacent levers available for this object, chosen up front so exactly one
	// fires (a second corruption could cancel the first).
	dropRequired := mode == Adjacent && len(required) > 0 && s.Bool("drop_required")
	addUnknown := mode == Adjacent && !open && !dropRequired && s.Bool("add_unknown")
	corruptProp := mode == Adjacent && len(props) > 0 && !dropRequired && !addUnknown

	skip := ""
	if dropRequired {
		skip = draw(s, required, "drop_which")
	}
	corrupt := ""
	if corruptProp {
		corrupt = draw(s, sortedKeys(props), "corrupt_which")
	}

	for _, name := range sortedKeys(props) {
		sub := asSchemaMap(props[name])
		isRequired := contains(required, name)
		if name == skip {
			continue
		}
		if !isRequired && mode != Adjacent && !s.Bool("include_"+name) {
			continue // optional property omitted in a valid object
		}
		if !isRequired && mode == Adjacent && !s.Bool("include_"+name) {
			continue
		}
		propMode := Valid
		if name == corrupt {
			propMode = Adjacent
		}
		obj[name] = genValue(s, sub, propMode, depth+1)
	}

	if addUnknown {
		obj[unknownKey(s)] = genArbitraryJSON(s, depth+1)
	}
	// An open schema in Valid mode may still carry extra keys (allowed); add some
	// to exercise additionalProperties:true pass-throughs.
	if open && mode == Valid && s.Bool("extra_open") {
		obj[unknownKey(s)] = genArbitraryJSON(s, depth+1)
	}
	return obj
}

// genArray generates an array value using the items subschema (or arbitrary JSON
// when items is absent). The length always honors minItems/maxItems — in both
// modes, mirroring how genInteger/genNumber always honor minimum/maximum —
// because there is no separate lever for an array-length violation; ignoring
// the bounds would make Valid-mode output schema-invalid whenever the schema
// declares one.
func genArray(s Source, schema map[string]any, mode Mode, depth int) any {
	items := asSchemaMap(schema["items"])
	lo, hi := arrayLenBounds(schema)
	n := s.IntRange(lo, hi, "array_len")
	out := make([]any, 0, n)
	for i := 0; i < n; i++ {
		if len(items) == 0 {
			out = append(out, genArbitraryJSON(s, depth+1))
			continue
		}
		out = append(out, genValue(s, items, mode, depth+1))
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

// genString generates a string value, honoring the schema's minLength/
// maxLength (both the adversarial corpus and the free-form draw are clamped —
// see clampStringLen) so Valid-mode output stays schema-valid whenever the
// schema declares a length bound. schema may be nil (adjacent-type filler,
// untyped JSON exploration), in which case clamping is a no-op.
func genString(s Source, schema map[string]any, mode Mode) any {
	var raw string
	if s.Bool("string_corpus") {
		raw = draw(s, adversarialStrings, "string_seed")
	} else {
		_ = mode
		raw = s.String("string")
	}
	return clampStringLen(raw, schema)
}

// clampStringLen truncates or pads str (by rune count, so multi-byte runes
// are never split) to satisfy schema's minLength/maxLength. A nil schema or
// one declaring neither keyword returns str unchanged.
func clampStringLen(str string, schema map[string]any) string {
	lo, hi := stringLenBounds(schema)
	runes := []rune(str)
	if len(runes) > hi {
		runes = runes[:hi]
	}
	for len(runes) < lo {
		runes = append(runes, 'x')
	}
	return string(runes)
}

// adversarialInts seeds the integer generator with boundary magnitudes.
var adversarialInts = []int{0, 1, -1, 2, 100, -100, 1 << 31, -(1 << 31), 1 << 53}

func genInteger(s Source, schema map[string]any, mode Mode) any {
	lo, hi := intBounds(schema)
	if mode == Adjacent && s.Bool("int_boundary") {
		// A fractional number is integer-adjacent: same numeric type, not an int.
		return draw(s, []float64{0.5, -0.5, 1e308}, "int_frac")
	}
	corpus := inRangeInts(lo, hi)
	if len(corpus) > 0 && s.Bool("int_corpus") {
		return draw(s, corpus, "int")
	}
	return s.IntRange(lo, hi, "int_range")
}

// adversarialFloats seeds the number generator with boundary magnitudes.
var adversarialFloats = []float64{0, 1, -1, 0.5, -0.5, 3.14, 1e6, -1e6, 1e308}

func genNumber(s Source, schema map[string]any, mode Mode) any {
	_ = mode
	lo, hi := floatBounds(schema)
	corpus := inRangeFloats(lo, hi)
	if len(corpus) > 0 && s.Bool("num_corpus") {
		return draw(s, corpus, "number")
	}
	return s.Float64Range(lo, hi, "num_range")
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
func genArbitraryJSON(s Source, depth int) any {
	kinds := []string{"string", "integer", "number", "boolean", "null"}
	if depth < maxDepth {
		kinds = append(kinds, "object", "array")
	}
	switch draw(s, kinds, "json_kind") {
	case "object":
		n := s.IntRange(0, 3, "obj_len")
		m := map[string]any{}
		for i := 0; i < n; i++ {
			m[unknownKey(s)] = genArbitraryJSON(s, depth+1)
		}
		return m
	case "array":
		n := s.IntRange(0, 3, "arr_len")
		out := make([]any, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, genArbitraryJSON(s, depth+1))
		}
		return out
	case "integer":
		return genInteger(s, nil, Valid)
	case "number":
		return genNumber(s, nil, Valid)
	case "boolean":
		return s.Bool("json_bool")
	case "null":
		return nil
	default:
		return genString(s, nil, Valid)
	}
}

// genWrongType returns a value whose JSON kind is, where possible, NOT among the
// schema's allowed types — a guaranteed type violation for a closed type set.
func genWrongType(s Source, allowed []string) any {
	for _, kind := range []string{"boolean", "string", "array", "object", "null"} {
		if !typeCovers(allowed, kind) {
			switch kind {
			case "boolean":
				return s.Bool("wt_bool")
			case "string":
				return genString(s, nil, Adjacent)
			case "array":
				return []any{genString(s, nil, Valid)}
			case "object":
				return map[string]any{"wrong": true}
			case "null":
				return nil
			}
		}
	}
	// Every common kind is allowed (very open schema): fall back to a number.
	return genNumber(s, nil, Adjacent)
}

// genEnumViolation returns a value unlikely to be a member of enum.
func genEnumViolation(s Source, enum []any) any {
	candidates := []any{
		"__not_in_enum__", "", 0, -1, true, false, nil,
		map[string]any{}, []any{},
	}
	for _, c := range candidates {
		if !containsAny(enum, c) {
			// A deterministic draw keeps the rapid backend reproducible; bias toward
			// the sentinel string but let the source pick among safe non-members.
			if s.Bool("enum_pick") {
				return c
			}
		}
	}
	return "__not_in_enum__"
}

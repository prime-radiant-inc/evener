package schemagen

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"

	"pgregory.net/rapid"
)

// FuzzSchemaGeneration exercises the complete portable schema generator through
// deterministic byte streams. The seed corpus deliberately includes JSON wire
// schemas and native Go schema shapes because callers use both forms.
func FuzzSchemaGeneration(f *testing.F) {
	seeds := []struct {
		schema []byte
		data   []byte
		mode   byte
	}{
		{[]byte(`null`), nil, 0},
		{[]byte(`{"type":"object","additionalProperties":false,"properties":{"id":{"type":"integer","minimum":5,"maximum":2}},"required":["id"]}`), []byte{1, 0, 1, 0, 0}, 1},
		{[]byte(`{"type":"array","minItems":2,"maxItems":1}`), []byte{0xff, 1, 2, 3, 4}, 1},
		{[]byte(`{"type":"string","minLength":3,"maxLength":1}`), []byte{0, 0}, 1},
		{[]byte(`{"type":"number","minimum":4,"maximum":-4}`), []byte{1, 0xff, 0, 1}, 1},
		{[]byte(`{"type":["object","array","string","integer","number","boolean","null"]}`), []byte{1, 6}, 1},
		{[]byte(`{"enum":["__not_in_enum__","",0,-1,true,false,null,{},[]]}`), []byte{0}, 1},
		{[]byte(`{"type":"mystery"}`), []byte{0}, 1},
	}
	for _, seed := range seeds {
		f.Add(seed.schema, seed.data, seed.mode)
	}

	f.Fuzz(func(t *testing.T, schemaJSON, data []byte, modeByte byte) {
		var schema map[string]any
		if err := json.Unmarshal(schemaJSON, &schema); err != nil {
			schema = map[string]any{}
		}
		mode := Valid
		if modeByte&1 != 0 {
			mode = Adjacent
		}

		got := Value(NewByteSource(data), schema, mode)
		again := Value(NewByteSource(data), schema, mode)
		if !reflect.DeepEqual(got, again) {
			t.Fatalf("generation is not deterministic: %#v != %#v", got, again)
		}
		wire, err := json.Marshal(got)
		if err != nil {
			t.Fatalf("generated value is not JSON encodable: %v", err)
		}
		var roundTrip any
		if err := json.Unmarshal(wire, &roundTrip); err != nil {
			t.Fatalf("generated JSON does not round trip: %v", err)
		}
		if mode == Valid && supportedSchema(schema) {
			if ok, why := conforms(got, schema); !ok {
				t.Fatalf("valid generation does not conform: %s; value=%#v", why, got)
			}
		}

		exerciseNativeSchemas(t, data)
		exerciseSourceContracts(t)
		exerciseGeneratorBranches(t)
	})
}

func supportedSchema(schema map[string]any) bool {
	if schema == nil {
		return true
	}
	allowed := map[string]bool{
		"object": true, "array": true, "string": true, "integer": true,
		"number": true, "boolean": true, "null": true,
	}
	for _, typ := range schemaTypes(schema) {
		if !allowed[typ] {
			return false
		}
	}
	for _, pair := range [][2]string{{"minimum", "maximum"}, {"minItems", "maxItems"}, {"minLength", "maxLength"}} {
		lo, hasLo := numBound(schema, pair[0])
		hi, hasHi := numBound(schema, pair[1])
		if (schema[pair[0]] != nil && !hasLo) || (schema[pair[1]] != nil && !hasHi) ||
			(hasLo && hasHi && lo > hi) {
			return false
		}
	}
	propsValue, hasProps := schema["properties"]
	props, propsOK := propsValue.(map[string]any)
	if hasProps && !propsOK {
		return false
	}
	for _, name := range stringList(schema["required"]) {
		if _, ok := props[name]; !ok {
			return false
		}
	}
	for _, sub := range props {
		m, ok := sub.(map[string]any)
		if !ok || !supportedSchema(m) {
			return false
		}
	}
	if items, ok := schema["items"]; ok {
		m, mapOK := items.(map[string]any)
		if !mapOK || !supportedSchema(m) {
			return false
		}
	}
	return true
}

type fixedSource struct {
	bools []bool
	ints  []int
	str   string
}

func (s *fixedSource) Bool(string) bool {
	if len(s.bools) == 0 {
		return false
	}
	v := s.bools[0]
	s.bools = s.bools[1:]
	return v
}

func (s *fixedSource) Intn(n int, _ string) int {
	if n <= 0 || len(s.ints) == 0 {
		return 0
	}
	v := s.ints[0]
	s.ints = s.ints[1:]
	if v < 0 {
		v = -v
	}
	return v % n
}

func (s *fixedSource) IntRange(lo, hi int, label string) int {
	if lo >= hi {
		return lo
	}
	return lo + s.Intn(hi-lo+1, label)
}

func (s *fixedSource) Float64Range(lo, hi float64, _ string) float64 {
	if !(lo < hi) {
		return lo
	}
	return lo/2 + hi/2
}

func (s *fixedSource) String(string) string { return s.str }

func exerciseGeneratorBranches(t *testing.T) {
	t.Helper()

	if additionalPropsAllowed(map[string]any{}) != true ||
		additionalPropsAllowed(map[string]any{"additionalProperties": false}) != false ||
		additionalPropsAllowed(map[string]any{"additionalProperties": 1}) != true {
		t.Fatal("additionalProperties normalization failed")
	}
	if len(asSchemaMap(map[string]any{"x": 1})) != 1 || len(asSchemaMap(1)) != 0 {
		t.Fatal("subschema normalization failed")
	}
	if got := stringList([]string{"a"}); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("[]string normalization: %#v", got)
	}
	if stringList(1) != nil || anyList(1) != nil {
		t.Fatal("non-list normalized as list")
	}
	if got := anyList([]string{"a"}); !reflect.DeepEqual(got, []any{"a"}) {
		t.Fatalf("typed enum normalization: %#v", got)
	}
	if !reflect.DeepEqual(sortedKeys(map[string]any{"b": 1, "a": 2}), []string{"a", "b"}) {
		t.Fatal("map keys not sorted")
	}
	if !contains([]string{"a"}, "a") || contains([]string{"a"}, "b") {
		t.Fatal("string membership failed")
	}
	if !typeCovers(nil, "object") || !typeCovers([]string{"number"}, "integer") ||
		typeCovers([]string{"string"}, "integer") {
		t.Fatal("type coverage failed")
	}
	if lo, hi := intBounds(map[string]any{"minimum": 2}); lo != 2 || hi != defaultIntHi {
		t.Fatalf("minimum-only integer bounds = %d,%d", lo, hi)
	}
	if lo, hi := intBounds(map[string]any{"minimum": 5, "maximum": 2}); lo != 2 || hi != 2 {
		t.Fatalf("inverted integer bounds = %d,%d", lo, hi)
	}

	closed := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"required": map[string]any{"type": "string"},
			"optional": map[string]any{"type": "integer"},
		},
		"required": []string{"required"},
	}
	// Adjacent: wrong-type off, drop required on.
	_ = Value(&fixedSource{bools: []bool{false, true}}, closed, Adjacent)
	// Adjacent: drop off, add unknown on.
	_ = Value(&fixedSource{bools: []bool{false, false, true}, ints: []int{0, 0}}, closed, Adjacent)
	// Adjacent: corrupt a property and include the optional property.
	_ = Value(&fixedSource{bools: []bool{false, false, false, true}, ints: []int{1}}, closed, Adjacent)
	// Valid: omit then include optional, and add an extra key to an open object.
	_ = Value(&fixedSource{bools: []bool{false}}, closed, Valid)
	_ = Value(&fixedSource{bools: []bool{true}}, closed, Valid)
	open := map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "string"}}}
	_ = Value(&fixedSource{bools: []bool{true, true, false}, ints: []int{0, 0}}, open, Valid)

	// Items absent and present, with non-empty arrays.
	_ = genArray(&fixedSource{ints: []int{1, 0}}, map[string]any{"minItems": 1, "maxItems": 1}, Valid, 0)
	_ = genArray(&fixedSource{ints: []int{1}}, map[string]any{"items": map[string]any{"type": "null"}, "minItems": 1}, Valid, 0)

	if got := clampStringLen("abcd", map[string]any{"maxLength": 2}); got != "ab" {
		t.Fatalf("string truncation = %q", got)
	}
	_ = genInteger(&fixedSource{bools: []bool{true}, ints: []int{0}}, nil, Adjacent)
	_ = genInteger(&fixedSource{bools: []bool{true}, ints: []int{0}}, nil, Valid)

	// Select each arbitrary JSON kind; object and array receive length one.
	for kind := range 7 {
		_ = genArbitraryJSON(&fixedSource{ints: []int{kind, 1, 0, 4}, bools: []bool{false}}, 0)
	}

	_ = FromJSONSchema(map[string]any{"type": "string"}).Example(0)
	_ = Generator(map[string]any{"type": "string"}, Valid).Example(0)
}

func exerciseNativeSchemas(t *testing.T, data []byte) {
	t.Helper()
	schemas := []map[string]any{
		nil,
		{"type": []string{"integer", "null"}, "minimum": int64(-2), "maximum": float32(2)},
		{"type": []any{"string", 7}, "minLength": json.Number("2"), "maxLength": json.Number("4")},
		{"type": 7, "enum": []int{1, 2, 3}},
		{"type": "object", "properties": 7, "required": []any{"x", 9}, "additionalProperties": map[string]any{}},
		{"type": "array", "items": 7, "minItems": 2, "maxItems": 1},
		{"type": "number", "minimum": 3.0, "maximum": -3.0},
	}
	for _, schema := range schemas {
		for _, mode := range []Mode{Valid, Adjacent} {
			value := Value(NewByteSource(data), schema, mode)
			if _, err := json.Marshal(value); err != nil {
				t.Fatalf("native schema generated non-JSON value: %v", err)
			}
		}
	}

	// Exercise every wrong-type alternative against a progressively wider union.
	for _, allowed := range [][]string{
		{"string"},
		{"boolean"},
		{"boolean", "string"},
		{"boolean", "string", "array"},
		{"boolean", "string", "array", "object"},
		{"boolean", "string", "array", "object", "null"},
	} {
		_ = genWrongType(NewByteSource(data), allowed)
	}
}

func exerciseSourceContracts(t *testing.T) {
	t.Helper()
	bs := NewByteSource([]byte{0xff, 0x80, 0x01, 0, 2, 3, 4, 5, 6, 7})
	if got := bs.Intn(0, "zero"); got != 0 {
		t.Fatalf("Intn(0)=%d", got)
	}
	if got := bs.IntRange(3, 2, "inverted"); got != 3 {
		t.Fatalf("inverted IntRange=%d", got)
	}
	if got := bs.Float64Range(2, 2, "collapsed"); got != 2 {
		t.Fatalf("collapsed Float64Range=%v", got)
	}
	_ = bs.Intn(257, "wide")
	_ = bs.IntRange(-4, 4, "range")
	_ = bs.Float64Range(-1, 1, "float")
	_ = NewByteSource([]byte{2, 'a', 'b'}).String("string")

	g := rapid.Custom(func(rt *rapid.T) int {
		rs := rapidSource{rt}
		if draw[int](rs, nil, "empty") != 0 {
			t.Fatal("empty draw was nonzero")
		}
		_ = rs.Intn(0, "zero")
		_ = rs.Intn(2, "intn")
		_ = rs.IntRange(2, 2, "collapsed")
		_ = rs.IntRange(-2, 2, "range")
		_ = rs.Float64Range(math.Inf(1), math.Inf(1), "collapsed_float")
		_ = rs.Float64Range(-1, 1, "float")
		_ = rs.Bool("bool")
		_ = rs.String("string")
		return 0
	})
	_ = g.Example(0)
}

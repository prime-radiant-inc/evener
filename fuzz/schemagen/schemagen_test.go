package schemagen

import (
	"math"
	"reflect"
	"testing"
)

// Fixtures mirror the shapes serf's real tool schemas use (definitions.go):
// a closed object with required+optional scalars, an enum, a nullable union
// type written as []string, a nested closed object, and an open
// additionalProperties:true pass-through.
var fixtures = map[string]map[string]any{
	"read_file_like": {
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"file_path": map[string]any{"type": "string"},
			"offset":    map[string]any{"type": "integer"},
			"limit":     map[string]any{"type": "integer"},
		},
		"required": []string{"file_path"},
	},
	"enum_and_union": {
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"operation":            map[string]any{"type": "string", "enum": []string{"create", "clear"}},
			"progress_interval_ms": map[string]any{"type": []string{"integer", "null"}},
			"events":               map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"required": []string{"operation"},
	},
	"nested_closed": {
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"event_filter": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"tool_name": map[string]any{"type": "string"},
					"status":    map[string]any{"type": "string", "enum": []any{"ok", "error"}},
				},
			},
		},
	},
	"open_passthrough": {
		"type":                 "object",
		"additionalProperties": true,
		"properties": map[string]any{
			"task": map[string]any{"type": "string"},
		},
		"required": []string{"task"},
	},
	// Mirrors job_list.limit: an integer with a maximum bound (the constraint the
	// real validator caught when schemagen first ignored it).
	"bounded_int": {
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"limit": map[string]any{"type": "integer", "maximum": 100, "minimum": 1},
		},
	},
}

// TestValidMode_Conforms is the central valid-mode contract: across many seeds,
// every value the Valid generator produces satisfies the schema (required keys
// present, declared types, enum membership, no extra keys when closed).
func TestValidMode_Conforms(t *testing.T) {
	for name, schema := range fixtures {
		g := Generator(schema, Valid)
		for seed := 0; seed < 300; seed++ {
			v := g.Example(seed)
			if ok, why := conforms(v, schema); !ok {
				t.Fatalf("%s seed=%d: valid value does not conform: %s\nvalue=%#v", name, seed, why, v)
			}
		}
	}
}

// TestAdjacentMode_ProducesViolations proves Adjacent mode actually explores the
// boundary: for a closed schema with required fields and enums it yields a
// schema-violating value for a meaningful fraction of seeds.
func TestAdjacentMode_ProducesViolations(t *testing.T) {
	for _, name := range []string{"read_file_like", "enum_and_union", "nested_closed"} {
		schema := fixtures[name]
		g := Generator(schema, Adjacent)
		violations := 0
		const n = 300
		for seed := 0; seed < n; seed++ {
			if ok, _ := conforms(g.Example(seed), schema); !ok {
				violations++
			}
		}
		if violations == 0 {
			t.Fatalf("%s: Adjacent mode produced zero violations in %d draws", name, n)
		}
	}
}

// TestDeterminism proves a fixed seed yields a byte-identical value, for both
// modes and the mixed FromJSONSchema entry point — the property the promoter's
// flake-guard depends on.
func TestDeterminism(t *testing.T) {
	schema := fixtures["enum_and_union"]
	gens := map[string]interface{ Example(...int) any }{
		"valid":    Generator(schema, Valid),
		"adjacent": Generator(schema, Adjacent),
		"mixed":    FromJSONSchema(schema),
	}
	for label, g := range gens {
		for _, seed := range []int{0, 1, 7, 42, 99} {
			a := g.Example(seed)
			b := g.Example(seed)
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("%s seed=%d not deterministic:\n a=%#v\n b=%#v", label, seed, a, b)
			}
		}
	}
}

// TestByteSourceDeterminism is the byte-backed equivalent of TestDeterminism:
// the same byte stream must always produce a byte-identical value (the property
// a coverage-guided testing.F target's regression seeds depend on).
func TestByteSourceDeterminism(t *testing.T) {
	streams := [][]byte{
		{},
		{0x00},
		{0xff, 0x01, 0x80, 0x42, 0x13, 0x37},
		[]byte("the quick brown fox jumps over 0123456789"),
	}
	for name, schema := range fixtures {
		for _, mode := range []Mode{Valid, Adjacent} {
			for _, data := range streams {
				a := Value(NewByteSource(data), schema, mode)
				b := Value(NewByteSource(data), schema, mode)
				if !reflect.DeepEqual(a, b) {
					t.Fatalf("%s mode=%v data=%x not deterministic:\n a=%#v\n b=%#v",
						name, mode, data, a, b)
				}
			}
		}
	}
}

// TestByteSourceValidConforms proves the byte-backed Source drives the SAME
// generation rules as the rapid backend: every Valid value it produces, across
// many byte streams, conforms to the schema.
func TestByteSourceValidConforms(t *testing.T) {
	for name, schema := range fixtures {
		for seed := 0; seed < 300; seed++ {
			data := pseudoBytes(seed, 64)
			v := Value(NewByteSource(data), schema, Valid)
			if ok, why := conforms(v, schema); !ok {
				t.Fatalf("%s data=%x: byte-source valid value does not conform: %s\nvalue=%#v",
					name, data, why, v)
			}
		}
	}
}

// TestByteSourceExhaustionTerminates proves an empty stream still yields a
// well-formed value for every fixture (the exhaustion-default path) rather than
// looping or panicking.
func TestByteSourceExhaustionTerminates(t *testing.T) {
	for name, schema := range fixtures {
		v := Value(NewByteSource(nil), schema, Valid)
		if ok, why := conforms(v, schema); !ok {
			t.Fatalf("%s: exhausted byte source produced non-conforming value: %s\nvalue=%#v",
				name, why, v)
		}
	}
}

// pseudoBytes derives a deterministic byte stream from seed (a tiny LCG) so
// byte-source tests sweep many distinct streams without a rapid dependency.
func pseudoBytes(seed, n int) []byte {
	x := uint64(seed)*2862933555777941757 + 3037000493
	out := make([]byte, n)
	for i := range out {
		x = x*6364136223846793005 + 1442695040888963407
		out[i] = byte(x >> 33)
	}
	return out
}

// TestEnum_ValidIsMember confirms enum-typed Valid values are always members.
func TestEnum_ValidIsMember(t *testing.T) {
	schema := map[string]any{"type": "string", "enum": []string{"low", "medium", "high"}}
	g := Generator(schema, Valid)
	want := map[string]bool{"low": true, "medium": true, "high": true}
	for seed := 0; seed < 100; seed++ {
		v := g.Example(seed)
		s, ok := v.(string)
		if !ok || !want[s] {
			t.Fatalf("seed=%d: enum value %#v not a declared member", seed, v)
		}
	}
}

// TestRequired_AlwaysPresent confirms Valid objects never omit a required key.
func TestRequired_AlwaysPresent(t *testing.T) {
	schema := fixtures["read_file_like"]
	g := Generator(schema, Valid)
	for seed := 0; seed < 200; seed++ {
		m, ok := g.Example(seed).(map[string]any)
		if !ok {
			t.Fatalf("seed=%d: object schema produced non-object %#v", seed, g.Example(seed))
		}
		if _, has := m["file_path"]; !has {
			t.Fatalf("seed=%d: required key file_path missing from %#v", seed, m)
		}
	}
}

// --- independent structural conformance checker (the generator's oracle) ---
//
// This is deliberately written as a checker (consume + verify), not by reusing
// the generator's code, so a shared bug cannot make both agree. The
// authoritative cross-check against serf's real jsonschema compiler lives in the
// agent module's schema-aware tool fuzz target.

func conforms(v any, schema map[string]any) (bool, string) {
	if enum := enumValues(schema); len(enum) > 0 {
		if !containsAny(enum, v) {
			return false, "value not in enum"
		}
		return true, ""
	}
	types := schemaTypes(schema)
	if len(types) > 0 && !kindAllowed(v, types) {
		return false, "type mismatch: " + jsonKind(v)
	}
	if f, ok := toFloat(v); ok {
		if lo, has := numBound(schema, "minimum"); has && f < lo {
			return false, "below minimum"
		}
		if hi, has := numBound(schema, "maximum"); has && f > hi {
			return false, "above maximum"
		}
	}
	switch val := v.(type) {
	case map[string]any:
		return objectConforms(val, schema)
	case []any:
		items := asSchemaMap(schema["items"])
		if len(items) == 0 {
			return true, ""
		}
		for i, e := range val {
			if ok, why := conforms(e, items); !ok {
				return false, "item " + itoa(i) + ": " + why
			}
		}
	}
	return true, ""
}

func objectConforms(m map[string]any, schema map[string]any) (bool, string) {
	props := asSchemaMap(schema["properties"])
	for _, req := range stringList(schema["required"]) {
		if _, has := m[req]; !has {
			return false, "missing required " + req
		}
	}
	if !additionalPropsAllowed(schema) {
		for k := range m {
			if _, declared := props[k]; !declared {
				return false, "unexpected key " + k
			}
		}
	}
	for k, val := range m {
		if sub, declared := props[k]; declared {
			if ok, why := conforms(val, asSchemaMap(sub)); !ok {
				return false, k + ": " + why
			}
		}
	}
	return true, ""
}

// kindAllowed reports whether v's JSON kind is permitted by the type set. A
// whole-valued float counts as an integer (matching JSON Schema's numeric rule).
func kindAllowed(v any, types []string) bool {
	k := jsonKind(v)
	for _, want := range types {
		if want == k {
			return true
		}
		if want == "number" && k == "integer" {
			return true
		}
		if want == "integer" && k == "number" && isWholeFloat(v) {
			return true
		}
	}
	return false
}

func jsonKind(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case int:
		return "integer"
	case float64:
		if isWholeFloat(x) {
			return "integer"
		}
		return "number"
	case map[string]any:
		return "object"
	case []any:
		return "array"
	default:
		return "unknown"
	}
}

func isWholeFloat(v any) bool {
	f, ok := v.(float64)
	if !ok {
		return false
	}
	return !math.IsInf(f, 0) && !math.IsNaN(f) && f == math.Trunc(f)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

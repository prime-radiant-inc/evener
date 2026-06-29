package typegen

import (
	"bytes"
	"encoding/json"
	"go/parser"
	"go/token"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/serf/fuzz/schemagen"
)

// cleanInner / cleanStruct use only round-trippable field kinds (no []byte,
// json.RawMessage, any, or map, whose generated values do not reliably decode),
// so every Valid value decodes cleanly and the fixed-point oracle always runs.
type cleanInner struct {
	A string `json:"a"`
	B int    `json:"b,omitempty"`
}

type cleanStruct struct {
	Name   string     `json:"name"`
	Count  int        `json:"count"`
	Ratio  float64    `json:"ratio"`
	Flag   bool       `json:"flag"`
	Opt    *string    `json:"opt,omitempty"`
	Tags   []string   `json:"tags,omitempty"`
	Nested cleanInner `json:"nested"`
}

// trickyStruct exercises the encoding/json special cases the bridge must mirror:
// []byte (base64 string), json.RawMessage (raw, NOT base64), any (untyped), and
// a map (open object). Its generated values may not decode, so it is only held
// to the no-panic floor.
type trickyStruct struct {
	Blob   []byte            `json:"blob,omitempty"`
	Raw    json.RawMessage   `json:"raw,omitempty"`
	Extra  any               `json:"extra,omitempty"`
	Meta   map[string]string `json:"meta,omitempty"`
	Skip   string            `json:"-"`
	hidden string            //nolint:unused // verifies unexported fields are not emitted
}

// customLayer is a json.Marshaler whose JSON shape a naive struct walk would
// misrepresent — the stand-in for appwire's LaunchConfigLayer.
type customLayer struct {
	X string   `json:"x,omitempty"`
	Y []string `json:"y,omitempty"`
}

func (c customLayer) MarshalJSON() ([]byte, error) {
	type alias customLayer
	return json.Marshal(alias(c))
}

// holder reaches customLayer at top level, through a pointer, and through a map
// value, so the override must fire at each nested occurrence.
type holder struct {
	Layer customLayer            `json:"layer"`
	Ptr   *customLayer           `json:"ptr,omitempty"`
	ByMap map[string]customLayer `json:"by_map,omitempty"`
}

func TestSchemaFromType_Scalars(t *testing.T) {
	cases := []struct {
		val      any
		wantType any
	}{
		{"", "string"},
		{true, "boolean"},
		{int(0), "integer"},
		{int64(0), "integer"},
		{uint32(0), "integer"},
		{float64(0), "number"},
		{float32(0), "number"},
	}
	for _, c := range cases {
		got := SchemaFromType(reflect.TypeOf(c.val))
		if !reflect.DeepEqual(got["type"], c.wantType) {
			t.Errorf("%T: type = %#v, want %#v", c.val, got["type"], c.wantType)
		}
	}
}

func TestSchemaFromType_SpecialKinds(t *testing.T) {
	s := SchemaFromType(reflect.TypeOf(trickyStruct{}))
	props := s["properties"].(map[string]any)

	// []byte → base64 string.
	if got := props["blob"].(map[string]any)["type"]; got != "string" {
		t.Errorf("blob ([]byte) type = %#v, want string", got)
	}
	// json.RawMessage → untyped {} (NOT a base64 string). This is the regression
	// the spec flags: RawMessage is []byte but must pass raw JSON through.
	if raw := props["raw"].(map[string]any); len(raw) != 0 {
		t.Errorf("raw (json.RawMessage) = %#v, want untyped {}", raw)
	}
	// any → untyped {}.
	if ex := props["extra"].(map[string]any); len(ex) != 0 {
		t.Errorf("extra (any) = %#v, want untyped {}", ex)
	}
	// map[string]string → open object.
	meta := props["meta"].(map[string]any)
	if meta["type"] != "object" {
		t.Errorf("meta (map) type = %#v, want object", meta["type"])
	}
	if _, open := meta["additionalProperties"]; !open {
		t.Errorf("meta (map) missing additionalProperties")
	}
	// json:"-" omitted; unexported omitted.
	if _, has := props["Skip"]; has {
		t.Errorf("json:\"-\" field Skip leaked into schema")
	}
	if _, has := props["hidden"]; has {
		t.Errorf("unexported field hidden leaked into schema")
	}
}

func TestSchemaFromType_PointerNullableAndRequired(t *testing.T) {
	s := SchemaFromType(reflect.TypeOf(cleanStruct{}))
	props := s["properties"].(map[string]any)

	// *string → type ["string","null"].
	opt := props["opt"].(map[string]any)
	if !reflect.DeepEqual(opt["type"], []string{"string", "null"}) {
		t.Errorf("opt (*string) type = %#v, want [string null]", opt["type"])
	}

	// additionalProperties:false on the closed struct.
	if s["additionalProperties"] != false {
		t.Errorf("additionalProperties = %#v, want false", s["additionalProperties"])
	}

	// Required = non-omitempty, non-pointer/slice scalars (name, count, ratio,
	// flag, nested); opt (pointer) and tags (slice) are optional.
	req := toStringSet(s["required"])
	for _, want := range []string{"name", "count", "ratio", "flag", "nested"} {
		if !req[want] {
			t.Errorf("required missing %q (got %v)", want, s["required"])
		}
	}
	for _, notWant := range []string{"opt", "tags"} {
		if req[notWant] {
			t.Errorf("required wrongly contains optional %q", notWant)
		}
	}
}

// TestOverrideFiresAtNestedDepth proves a per-type override applies at top level
// AND when the type is reached transitively (pointer, map value).
func TestOverrideFiresAtNestedDepth(t *testing.T) {
	override := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"x": map[string]any{"type": "string"},
			"y": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"required": []string{},
	}
	reg := NewRegistry()
	reg.RegisterTypeSchema(reflect.TypeOf(customLayer{}), override)
	reg.RegisterType("holder", reflect.TypeOf(holder{}))

	s, ok := reg.Schema("holder")
	if !ok {
		t.Fatal("holder not registered")
	}
	props := s["properties"].(map[string]any)

	// Top-level field uses the override verbatim.
	if !reflect.DeepEqual(props["layer"], override) {
		t.Errorf("layer schema = %#v, want override", props["layer"])
	}
	// Pointer field: override widened to nullable.
	ptr := props["ptr"].(map[string]any)
	if !reflect.DeepEqual(ptr["type"], []string{"object", "null"}) {
		t.Errorf("ptr type = %#v, want [object null]", ptr["type"])
	}
	if _, has := ptr["properties"]; !has {
		t.Errorf("ptr lost the override's properties: %#v", ptr)
	}
	// Map value schema is the override.
	byMap := props["by_map"].(map[string]any)
	if !reflect.DeepEqual(byMap["additionalProperties"], override) {
		t.Errorf("byMap value schema = %#v, want override", byMap["additionalProperties"])
	}

	// The override must not have been mutated by the nullable widening.
	if !reflect.DeepEqual(override["type"], "object") {
		t.Errorf("override mutated: type = %#v", override["type"])
	}
}

// TestCustomMarshalerWithoutOverride confirms a json.Marshaler with no override
// downgrades to untyped {} (the safe §3.3.3 fallback).
func TestCustomMarshalerWithoutOverride(t *testing.T) {
	s := SchemaFromType(reflect.TypeOf(customLayer{}))
	if len(s) != 0 {
		t.Errorf("custom marshaler without override = %#v, want untyped {}", s)
	}
}

// TestValidRoundTrips proves Valid values for a round-trippable type decode
// cleanly and re-marshal to a fixed point, under BOTH a byte Source and a rapid
// Source — the core "generated value round-trips into the source struct" claim.
func TestValidRoundTrips(t *testing.T) {
	typ := reflect.TypeOf(cleanStruct{})
	schema := SchemaFromType(typ)

	check := func(label string, val any) {
		raw, err := json.Marshal(val)
		if err != nil {
			t.Fatalf("%s: marshal generated value: %v", label, err)
		}
		p := reflect.New(typ).Interface()
		if err := json.Unmarshal(raw, p); err != nil {
			t.Fatalf("%s: valid value did not decode into %s: %v\n raw=%s", label, typ, err, raw)
		}
		e1, err := json.Marshal(p)
		if err != nil {
			t.Fatalf("%s: re-marshal: %v", label, err)
		}
		p2 := reflect.New(typ).Interface()
		if err := json.Unmarshal(e1, p2); err != nil {
			t.Fatalf("%s: re-decode: %v", label, err)
		}
		e2, _ := json.Marshal(p2)
		if !bytes.Equal(e1, e2) {
			t.Fatalf("%s: not a fixed point:\n once=%s\n twice=%s", label, e1, e2)
		}
	}

	// Byte Source: sweep deterministic streams.
	for seed := 0; seed < 300; seed++ {
		data := pseudoBytes(seed, 64)
		check("byte", schemagen.Value(schemagen.NewByteSource(data), schema, schemagen.Valid))
	}
	// Rapid Source: same definitions via the rapid backend.
	g := GeneratorForType(typ, schemagen.Valid)
	for seed := 0; seed < 300; seed++ {
		check("rapid", g.Example(seed))
	}
}

// TestNoPanicOnTrickyType proves the special-case kinds never panic the
// decode/marshal path (the floor oracle), under both Valid and Adjacent modes.
func TestNoPanicOnTrickyType(t *testing.T) {
	typ := reflect.TypeOf(trickyStruct{})
	schema := SchemaFromType(typ)
	for _, mode := range []schemagen.Mode{schemagen.Valid, schemagen.Adjacent} {
		for seed := 0; seed < 300; seed++ {
			data := pseudoBytes(seed*7+int(mode), 48)
			val := schemagen.Value(schemagen.NewByteSource(data), schema, mode)
			raw, err := json.Marshal(val)
			if err != nil {
				continue
			}
			p := reflect.New(typ).Interface()
			_ = json.Unmarshal(raw, p) // may legitimately error; must not panic
		}
	}
}

// TestByteDeterminism proves the same bytes yield the same registry value.
func TestByteDeterminism(t *testing.T) {
	reg := NewRegistry()
	reg.RegisterType("clean", reflect.TypeOf(cleanStruct{}))
	for seed := 0; seed < 50; seed++ {
		data := pseudoBytes(seed, 64)
		a, _ := reg.Value("clean", schemagen.Valid, schemagen.NewByteSource(data))
		b, _ := reg.Value("clean", schemagen.Valid, schemagen.NewByteSource(data))
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("seed=%d not deterministic:\n a=%#v\n b=%#v", seed, a, b)
		}
	}
}

// TestRegistryNamesAndLookup covers the index surface.
func TestRegistryNamesAndLookup(t *testing.T) {
	reg := NewRegistry()
	reg.RegisterType("b", reflect.TypeOf(cleanStruct{}))
	reg.RegisterSchema("a", map[string]any{"type": "string"})
	if got := reg.Names(); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("Names() = %v, want [a b]", got)
	}
	if _, ok := reg.Value("missing", schemagen.Valid, schemagen.NewByteSource(nil)); ok {
		t.Errorf("Value on missing name reported ok")
	}
	if g := reg.Generator("missing", schemagen.Valid); g != nil {
		t.Errorf("Generator on missing name = %v, want nil", g)
	}
}

// TestNoSerfImport is the portability guard: no source file in this package may
// import a primeradiant.com/serf package other than the fuzz module's own.
func TestNoSerfImport(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse package dir: %v", err)
	}
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			for _, imp := range file.Imports {
				path := strings.Trim(imp.Path.Value, `"`)
				if strings.HasPrefix(path, "primeradiant.com/serf/") &&
					!strings.HasPrefix(path, "primeradiant.com/serf/fuzz/") {
					t.Errorf("%s imports forbidden serf package %q", name, path)
				}
			}
		}
	}
}

func toStringSet(v any) map[string]bool {
	out := map[string]bool{}
	for _, s := range v.([]string) {
		out[s] = true
	}
	return out
}

// pseudoBytes derives a deterministic byte stream from seed (a tiny LCG).
func pseudoBytes(seed, n int) []byte {
	x := uint64(seed)*2862933555777941757 + 3037000493
	out := make([]byte, n)
	for i := range out {
		x = x*6364136223846793005 + 1442695040888963407
		out[i] = byte(x >> 33)
	}
	return out
}

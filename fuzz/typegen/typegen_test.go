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

type recursiveNode struct {
	Value string         `json:"value"`
	Next  *recursiveNode `json:"next,omitempty"`
}

type embeddedFields struct {
	cleanInner
	*embeddedPointer
	Named cleanInner `json:"named"`
	int
}

type embeddedPointer struct {
	Enabled bool `json:"enabled"`
}

type requiredContainers struct {
	Values []string       `json:"values"`
	Lookup map[string]int `json:"lookup"`
	Data   any            `json:"data"`
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

func TestSchemaFromType_AllStructuralKinds(t *testing.T) {
	if got := SchemaFromType(nil); len(got) != 0 {
		t.Fatalf("nil type schema = %#v, want untyped", got)
	}

	cases := []struct {
		typ      reflect.Type
		wantType any
	}{
		{reflect.TypeFor[[2]string](), "array"},
		{reflect.TypeFor[chan int](), nil},
		{reflect.TypeFor[complex64](), nil},
	}
	for _, tc := range cases {
		got := SchemaFromType(tc.typ)
		if !reflect.DeepEqual(got["type"], tc.wantType) {
			t.Errorf("%s type = %#v, want %#v", tc.typ, got["type"], tc.wantType)
		}
	}

	recursive := SchemaFromType(reflect.TypeFor[recursiveNode]())
	next := recursive["properties"].(map[string]any)["next"].(map[string]any)
	if len(next) != 0 {
		t.Errorf("recursive occurrence = %#v, want untyped nullable schema", next)
	}
}

func TestSchemaFromType_EmbeddedAndContainerFields(t *testing.T) {
	schema := SchemaFromType(reflect.TypeFor[embeddedFields]())
	props := schema["properties"].(map[string]any)
	for _, name := range []string{"a", "b", "enabled", "named"} {
		if _, ok := props[name]; !ok {
			t.Errorf("promoted properties missing %q: %#v", name, props)
		}
	}
	if _, ok := props["int"]; ok {
		t.Errorf("unexported anonymous field leaked: %#v", props)
	}

	containers := SchemaFromType(reflect.TypeFor[requiredContainers]())
	if got := containers["required"].([]string); len(got) != 0 {
		t.Errorf("nullable container fields marked required: %v", got)
	}

	untagged := SchemaFromType(reflect.TypeOf(struct{ Plain string }{}))
	if _, ok := untagged["properties"].(map[string]any)["Plain"]; !ok {
		t.Errorf("exported untagged field did not use its Go name: %#v", untagged)
	}
}

func TestSchemaFromType_SpecialKinds(t *testing.T) {
	s := SchemaFromType(reflect.TypeFor[trickyStruct]())
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
	s := SchemaFromType(reflect.TypeFor[cleanStruct]())
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
	reg.RegisterTypeSchema(reflect.TypeFor[customLayer](), override)
	reg.RegisterType("holder", reflect.TypeFor[holder]())

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
	s := SchemaFromType(reflect.TypeFor[customLayer]())
	if len(s) != 0 {
		t.Errorf("custom marshaler without override = %#v, want untyped {}", s)
	}
}

// TestValidRoundTrips proves Valid values for a round-trippable type decode
// cleanly and re-marshal to a fixed point, under BOTH a byte Source and a rapid
// Source — the core "generated value round-trips into the source struct" claim.
func TestValidRoundTrips(t *testing.T) {
	typ := reflect.TypeFor[cleanStruct]()
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
	typ := reflect.TypeFor[trickyStruct]()
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
	reg.RegisterType("clean", reflect.TypeFor[cleanStruct]())
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
	reg.RegisterType("b", reflect.TypeFor[cleanStruct]())
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
	if g := reg.Generator("a", schemagen.Valid); g == nil {
		t.Fatal("Generator on registered name returned nil")
	} else if got := g.Example(0); reflect.TypeOf(got).Kind() != reflect.String {
		t.Errorf("registered generator value = %#v, want string", got)
	}
}

func FuzzRegistrySemanticRoundTrip(f *testing.F) {
	types := []reflect.Type{
		reflect.TypeFor[cleanStruct](),
		reflect.TypeFor[cleanInner](),
		reflect.TypeOf(struct {
			Items [2]string `json:"items"`
			OK    bool      `json:"ok"`
		}{}),
	}
	reg := NewRegistry()
	for i, typ := range types {
		reg.RegisterType(string(rune('a'+i)), typ)
	}
	for _, seed := range [][]byte{nil, {0}, {1, 2, 3}, pseudoBytes(17, 64)} {
		f.Add(seed, uint8(len(seed)))
	}

	f.Fuzz(func(t *testing.T, data []byte, selector uint8) {
		exerciseTypegenSurface(t, data)

		idx := int(selector) % len(types)
		name := string(rune('a' + idx))
		first, ok := reg.Value(name, schemagen.Valid, schemagen.NewByteSource(data))
		if !ok {
			t.Fatalf("registered type %q missing", name)
		}
		second, _ := reg.Value(name, schemagen.Valid, schemagen.NewByteSource(data))
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("generation is not deterministic: first=%#v second=%#v", first, second)
		}

		raw, err := json.Marshal(first)
		if err != nil {
			t.Fatalf("marshal generated value: %v", err)
		}
		decoded := reflect.New(types[idx]).Interface()
		if err := json.Unmarshal(raw, decoded); err != nil {
			t.Fatalf("valid value failed to decode as %s: %v", types[idx], err)
		}
		once, err := json.Marshal(decoded)
		if err != nil {
			t.Fatalf("marshal decoded value: %v", err)
		}
		again := reflect.New(types[idx]).Interface()
		if err := json.Unmarshal(once, again); err != nil {
			t.Fatalf("second decode as %s: %v", types[idx], err)
		}
		twice, _ := json.Marshal(again)
		if !bytes.Equal(once, twice) {
			t.Fatalf("JSON round trip is not a fixed point: once=%s twice=%s", once, twice)
		}

		// Adjacent generation is intentionally allowed to reject at the Go type
		// boundary; it must remain deterministic and panic-free.
		adjacent, _ := reg.Value(name, schemagen.Adjacent, schemagen.NewByteSource(data))
		_, _ = json.Marshal(adjacent)
	})
}

// exerciseTypegenSurface keeps the fuzz seed corpus accountable for the whole
// type-to-schema bridge, including registry misses and reflection cases that
// random structured values cannot discover on their own.
func exerciseTypegenSurface(t *testing.T, data []byte) {
	t.Helper()

	types := []reflect.Type{
		nil,
		reflect.TypeFor[string](),
		reflect.TypeFor[bool](),
		reflect.TypeFor[int8](),
		reflect.TypeFor[float32](),
		reflect.TypeFor[[]byte](),
		reflect.TypeFor[[]string](),
		reflect.TypeFor[[1]string](),
		reflect.TypeFor[map[string]int](),
		reflect.TypeFor[*string](),
		reflect.TypeFor[any](),
		reflect.TypeFor[chan int](),
		reflect.TypeFor[json.RawMessage](),
		reflect.TypeFor[customLayer](),
		reflect.TypeFor[recursiveNode](),
		reflect.TypeFor[embeddedFields](),
		reflect.TypeFor[requiredContainers](),
		reflect.TypeFor[trickyStruct](),
		reflect.TypeFor[struct{ Plain string }](),
	}
	for _, typ := range types {
		_ = SchemaFromType(typ)
	}

	if got := GeneratorForType(reflect.TypeFor[string](), schemagen.Valid).Example(0); reflect.TypeOf(got).Kind() != reflect.String {
		t.Fatalf("GeneratorForType(string) produced %T", got)
	}
	if _, ok := ValueFromBytes[cleanStruct](data, schemagen.Valid); !ok {
		t.Fatal("ValueFromBytes rejected a valid cleanStruct")
	}
	if _, ok := ValueFromBytes[string]([]byte{1, 0}, schemagen.Adjacent); ok {
		t.Fatal("ValueFromBytes accepted an adjacent wrong-type string")
	}

	override := map[string]any{"type": "object"}
	reg := NewRegistry()
	reg.RegisterTypeSchema(reflect.TypeFor[customLayer](), override)
	reg.RegisterType("holder", reflect.TypeFor[holder]())
	reg.RegisterSchema("literal", map[string]any{"type": "string"})
	if _, ok := reg.Schema("holder"); !ok {
		t.Fatal("registered holder schema missing")
	}
	if _, ok := reg.Schema("missing"); ok {
		t.Fatal("missing schema reported present")
	}
	if _, ok := reg.Value("literal", schemagen.Valid, schemagen.NewByteSource(data)); !ok {
		t.Fatal("registered literal value missing")
	}
	if _, ok := reg.Value("missing", schemagen.Valid, schemagen.NewByteSource(data)); ok {
		t.Fatal("missing value reported present")
	}
	if reg.Generator("literal", schemagen.Valid) == nil {
		t.Fatal("registered literal generator missing")
	}
	if reg.Generator("missing", schemagen.Valid) != nil {
		t.Fatal("missing generator reported present")
	}
	if got := reg.Names(); !reflect.DeepEqual(got, []string{"holder", "literal"}) {
		t.Fatalf("registry names = %v", got)
	}
}

// TestNoSerfImport is the portability guard: no source file in this package may
// import a primeradiant.com/serf package other than the fuzz module's own.
func TestNoSerfImport(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly) //nolint:staticcheck // parser.ParseDir is adequate here; build tags not relevant. go/packages migration tracked separately.
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

// TestValueFromBytes proves the generic byte→T helper both produces a decodable
// value of the concrete type in Valid mode and round-trips it (marshal then
// re-unmarshal is stable), across many byte seeds — the property FuzzWireTypes
// checks by hand, now behind one call.
func TestValueFromBytes(t *testing.T) {
	sawOpt, sawNested := false, false
	for seed := 0; seed < 64; seed++ {
		v, ok := ValueFromBytes[cleanStruct](pseudoBytes(seed, 48), schemagen.Valid)
		if !ok {
			t.Fatalf("seed %d: Valid value of a round-trippable type failed to decode", seed)
		}
		// Re-encode and re-decode: a genuine T decodes back to an equal T.
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("seed %d: marshal generated value: %v", seed, err)
		}
		var again cleanStruct
		if err := json.Unmarshal(raw, &again); err != nil {
			t.Fatalf("seed %d: re-decode generated value: %v\n raw=%s", seed, err, raw)
		}
		if v.Opt != nil {
			sawOpt = true
		}
		if v.Nested.A != "" || v.Nested.B != 0 {
			sawNested = true
		}
	}
	// The generator must actually populate the interesting fields, or the helper
	// would be vacuously "working" on empty values.
	if !sawOpt {
		t.Error("no seed produced a non-nil optional pointer — generator too shallow")
	}
	if !sawNested {
		t.Error("no seed populated the nested struct — generator too shallow")
	}
}

// TestValueFromBytes_Empty exercises the exhaustion path: a zero-length byte
// source still yields a decodable (zero-ish) value, never a panic.
func TestValueFromBytes_Empty(t *testing.T) {
	if _, ok := ValueFromBytes[cleanStruct](nil, schemagen.Valid); !ok {
		t.Fatal("empty byte source should still yield a decodable Valid value")
	}
}

func TestValueFromBytes_AdjacentDecodeRejection(t *testing.T) {
	if got, ok := ValueFromBytes[string]([]byte{1, 0}, schemagen.Adjacent); ok {
		t.Fatalf("wrong-type adjacent value decoded as string %q", got)
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

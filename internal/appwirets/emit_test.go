package main

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/appwire"
)

func TestEmitStruct(t *testing.T) {
	type Inner struct {
		A string `json:"a"`
	}
	type Sample struct {
		Inner
		Name string            `json:"name"`
		Opt  *int              `json:"opt,omitempty"`
		Tags []string          `json:"tags,omitempty"`
		Meta map[string]string `json:"meta,omitempty"`
		Raw  any               `json:"raw"`
		Skip string            `json:"-"`
	}
	got := emitInterface("Sample", reflect.TypeOf(Sample{}))
	want := `export interface Sample {
  a: string;
  name: string;
  opt?: number;
  tags?: string[];
  meta?: Record<string, string>;
  raw: unknown;
}
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestEmitInterface_UnexportedAndUntaggedFields(t *testing.T) {
	type Sample struct {
		Untagged string // no json tag at all: defaults to the Go field name
		hidden   string // unexported: must never reach the wire
	}
	got := emitInterface("Sample", reflect.TypeOf(Sample{hidden: "x"}))
	want := "export interface Sample {\n  Untagged: string;\n}\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestEmitInterface_PointerNullVsOptional(t *testing.T) {
	type Sample struct {
		Loose    *string `json:"loose"`
		Optional *string `json:"optional,omitempty"`
	}
	got := emitInterface("Sample", reflect.TypeOf(Sample{}))
	want := `export interface Sample {
  loose: string | null;
  optional?: string;
}
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestEmitInterface_NumericKinds(t *testing.T) {
	type Sample struct {
		I   int     `json:"i"`
		I8  int8    `json:"i8"`
		U   uint    `json:"u"`
		U64 uint64  `json:"u64"`
		F32 float32 `json:"f32"`
		F64 float64 `json:"f64"`
		B   bool    `json:"b"`
	}
	got := emitInterface("Sample", reflect.TypeOf(Sample{}))
	want := `export interface Sample {
  i: number;
  i8: number;
  u: number;
  u64: number;
  f32: number;
  f64: number;
  b: boolean;
}
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestEmitInterface_RawMessageBytesAndTime(t *testing.T) {
	type Sample struct {
		Raw   json.RawMessage `json:"raw"`
		Bytes []byte          `json:"bytes"`
		When  time.Time       `json:"when"`
	}
	got := emitInterface("Sample", reflect.TypeOf(Sample{}))
	want := `export interface Sample {
  raw: unknown;
  bytes: string;
  when: string;
}
`
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// Not exercised by the current AppWire catalog (no slice-of-pointer or
// map-of-pointer field exists), but typeExpr and registry.discover both
// unwrap a pointer found below the top field level, so a slice of pointers
// degrades to the pointee type rather than panicking on reflect.Pointer.
func TestEmitInterface_PointerInsideSliceUnwraps(t *testing.T) {
	type Item struct {
		V string `json:"v"`
	}
	type Sample struct {
		Items []*Item `json:"items"`
	}
	got := emitInterface("Sample", reflect.TypeOf(Sample{}))
	want := "export interface Sample {\n  items: Item[];\n}\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}

	reg := newRegistry()
	registerTopLevel(reg, Sample{}, "unused")
	found := false
	for _, name := range reg.order {
		if name == "Item" {
			found = true
		}
	}
	if !found {
		t.Fatalf("discover did not register Item through []*Item; got %v", reg.order)
	}
}

func TestEmitInterface_MapKeyIsAlsoMapped(t *testing.T) {
	type Sample struct {
		Counts map[int]string `json:"counts"`
	}
	got := emitInterface("Sample", reflect.TypeOf(Sample{}))
	want := "export interface Sample {\n  counts: Record<number, string>;\n}\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestEmitInterface_ZeroFields(t *testing.T) {
	type Empty struct{}
	got := emitInterface("Empty", reflect.TypeOf(Empty{}))
	want := "export interface Empty {\n}\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestEmitInterface_NilType(t *testing.T) {
	// A notification whose Payload is nil (an inline object with no
	// dedicated Go type) has no fields to reflect.
	got := emitInterface("Inline", nil)
	want := "export interface Inline {\n}\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestEmitInterface_EmbeddedPointerStructFlattens(t *testing.T) {
	type Inner struct {
		A string `json:"a"`
	}
	type Sample struct {
		*Inner
		B string `json:"b"`
	}
	got := emitInterface("Sample", reflect.TypeOf(Sample{}))
	want := "export interface Sample {\n  a: string;\n  b: string;\n}\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// An anonymous field with an EXPLICIT json tag name is a regular named
// field in encoding/json, not a flatten target — only a nameless embed
// flattens. This mirrors real Go JSON semantics rather than a simplified
// subset of them.
func TestEmitInterface_EmbeddedWithExplicitTagDoesNotFlatten(t *testing.T) {
	type Inner struct {
		A string `json:"a"`
	}
	type Sample struct {
		Inner `json:"inner"`
		B     string `json:"b"`
	}
	got := emitInterface("Sample", reflect.TypeOf(Sample{}))
	want := "export interface Sample {\n  inner: Inner;\n  b: string;\n}\n"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestTypeExprPanicsOnUnsupportedKind(t *testing.T) {
	type Sample struct {
		Ch chan int `json:"ch"`
	}
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for an unsupported field kind")
		}
	}()
	emitInterface("Sample", reflect.TypeOf(Sample{}))
}

func TestTypeExprPanicsOnAnonymousNestedStruct(t *testing.T) {
	type Sample struct {
		Foo struct {
			X string `json:"x"`
		} `json:"foo"`
	}
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for an anonymous nested struct field")
		}
	}()
	emitInterface("Sample", reflect.TypeOf(Sample{}))
}

func TestDeriveName(t *testing.T) {
	cases := []struct{ wire, suffix, want string }{
		{"thread/started", "Payload", "ThreadStartedPayload"},
		{"serf/steering/injected", "Payload", "SerfSteeringInjectedPayload"},
		{"warning", "Payload", "WarningPayload"},
		{"ping", "Params", "PingParams"},
		{"/leading/slash", "Payload", "LeadingSlashPayload"},
	}
	for _, c := range cases {
		if got := deriveName(c.wire, c.suffix); got != c.want {
			t.Errorf("deriveName(%q, %q) = %q, want %q", c.wire, c.suffix, got, c.want)
		}
	}
}

// A type reachable from two different top-level catalog entries must be
// discovered and registered exactly once, and the registry's final order
// must sort alphabetically once EmitCatalog is done walking.
func TestRegistryDiscoversSharedNestedTypesOnce(t *testing.T) {
	type Leaf struct {
		V string `json:"v"`
	}
	type Branch struct {
		L Leaf `json:"l"`
	}
	type A struct {
		B Branch `json:"b"`
	}
	type C struct {
		B Branch `json:"b"` // shares Branch (and transitively Leaf) with A
	}

	reg := newRegistry()
	registerTopLevel(reg, A{}, "unused")
	registerTopLevel(reg, C{}, "unused")

	occurrences := map[string]int{}
	for _, name := range reg.order {
		occurrences[name]++
	}
	for _, name := range []string{"A", "Branch", "C", "Leaf"} {
		if occurrences[name] != 1 {
			t.Errorf("%s registered %d times, want 1 (registered: %v)", name, occurrences[name], reg.order)
		}
	}

	gotNames := append([]string(nil), reg.order...)
	sort.Strings(gotNames)
	wantNames := []string{"A", "Branch", "C", "Leaf"}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("registered names = %v, want %v", gotNames, wantNames)
	}
}

// registerTopLevel falls back to the wire-derived name only when the value
// has no Go type name of its own (nil Payload); a named Go type always
// wins, even when a fallback is offered.
func TestRegisterTopLevelPrefersGoTypeName(t *testing.T) {
	type Named struct {
		X string `json:"x"`
	}
	reg := newRegistry()
	if got := registerTopLevel(reg, Named{}, "FallbackName"); got != "Named" {
		t.Errorf("named value used fallback: got %q", got)
	}
	if got := registerTopLevel(reg, nil, "FallbackName"); got != "FallbackName" {
		t.Errorf("nil value did not use fallback: got %q", got)
	}
}

// A pointer-valued Params/Result/Payload (none occur in the catalog today —
// entries are always struct literals) still resolves by the pointee's name,
// matching internal/appwiredoc's registerType's identical defensive unwrap.
func TestRegisterTopLevelUnwrapsPointerValue(t *testing.T) {
	type Named struct {
		X string `json:"x"`
	}
	reg := newRegistry()
	if got := registerTopLevel(reg, &Named{}, "FallbackName"); got != "Named" {
		t.Errorf("pointer value = %q, want %q", got, "Named")
	}
}

// A type reachable only through Kind()==Struct (time.Time) must be recognized
// as opaque by the registry's own discovery walk, not just by typeExpr's
// rendering — otherwise a phantom, unreferenced "Time" interface with zero
// fields (time.Time's fields are unexported) would leak into the output.
// json.RawMessage never reaches this check: its Kind() is Slice, so
// registry.discover takes the slice-of-bytes path instead.
func TestRegistryDoesNotIndependentlyRegisterOpaqueTypes(t *testing.T) {
	type Sample struct {
		Raw  json.RawMessage `json:"raw"`
		When time.Time       `json:"when"`
	}
	reg := newRegistry()
	registerTopLevel(reg, Sample{}, "unused")
	for _, name := range reg.order {
		if name == "Time" || name == "RawMessage" {
			t.Errorf("opaque type %s must not be independently registered; got order=%v", name, reg.order)
		}
	}
}

// discover's own anonymous-struct guard (not just typeExpr's) must fire when
// an anonymous nested struct field is reachable through the registry walk,
// not only when rendered directly via emitInterface.
func TestRegistryPanicsOnAnonymousNestedStruct(t *testing.T) {
	type Sample struct {
		Foo struct {
			X string `json:"x"`
		} `json:"foo"`
	}
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when discovering an anonymous nested struct field")
		}
	}()
	registerTopLevel(newRegistry(), Sample{}, "unused")
}

// EmitCatalog's output must stay internally consistent: the exact locked
// AnyNotification declaration, an entry in MethodTypes/NotificationTypes for
// every catalog name, and no interface emitted more than once.
func TestEmitCatalogStructuralInvariants(t *testing.T) {
	out := EmitCatalog()
	if !strings.HasPrefix(out, generatedHeader) {
		t.Fatal("missing generated header")
	}
	const wantAnyNotification = "export type AnyNotification = { [K in NotificationName]: { method: K; params: NotificationTypes[K] } }[NotificationName];\n"
	if !strings.HasSuffix(out, wantAnyNotification) {
		t.Fatal("missing the exact locked AnyNotification declaration at EOF")
	}

	seen := map[string]int{}
	for _, line := range strings.Split(out, "\n") {
		if name, ok := strings.CutPrefix(line, "export interface "); ok {
			name, _, _ = strings.Cut(name, " ")
			seen[name]++
		}
	}
	for name, n := range seen {
		if n != 1 {
			t.Errorf("interface %s emitted %d times, want 1", name, n)
		}
	}

	for _, m := range appwire.Methods {
		if !strings.Contains(out, fmt.Sprintf("\n  %q: { params:", m.Name)) {
			t.Errorf("MethodTypes missing entry for %q", m.Name)
		}
	}
	for _, n := range appwire.Notifications {
		if !strings.Contains(out, fmt.Sprintf("\n  %q: ", n.Name)) {
			t.Errorf("NotificationTypes missing entry for %q", n.Name)
		}
	}
}

// TestGeneratedFileCurrent is the drift test: types.gen.ts is committed, and
// this fails the moment the catalog changes without a regeneration, exactly
// like appwiredoc's docs/appwire-protocol.md is kept current by
// `make lint-generated`.
func TestGeneratedFileCurrent(t *testing.T) {
	want := EmitCatalog()
	got, err := os.ReadFile("../../cmd/serf-hub/frontend/src/protocol/types.gen.ts")
	if err != nil || string(got) != want {
		t.Fatal("types.gen.ts stale: run `make generate`")
	}
}

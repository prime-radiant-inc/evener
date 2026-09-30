package reflectfill_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/fuzz/reflectfill"
)

// fakeReporter captures Fatalf instead of aborting, so one Test can assert the
// filler's loud failure on an unhandled kind without failing the enclosing
// test. A real *testing.T.Fatalf calls runtime.Goexit; the fake records and
// returns, and it satisfies reflectfill.Reporter (Fatalf + Helper).
type fakeReporter struct {
	failed bool
}

func (r *fakeReporter) Helper() {}

func (r *fakeReporter) Fatalf(format string, args ...any) { r.failed = true }

type nested struct {
	Name string
	When time.Time
}

type fixture struct {
	Str    string
	Bool   bool
	Int    int
	Uint   uint
	Float  float64
	Ptr    *int
	Slice  []string
	Bytes  []byte
	Raw    json.RawMessage
	Map    map[string]int
	Nested nested
	Any    any
	Err    error
}

// TestFillPopulatesEveryField pins the property the whole-struct comparison and
// JSON-drift call sites rely on: after Fill, no exported field is left at its
// zero value — including time.Time, whose fields are unexported and so
// unreachable by reflection.
func TestFillPopulatesEveryField(t *testing.T) {
	f := &fixture{}
	reflectfill.Fill(t, reflect.ValueOf(f).Elem(), "fixture")

	if f.Str == "" || !f.Bool || f.Int == 0 || f.Uint == 0 || f.Float == 0 {
		t.Fatalf("scalar field left zero: %+v", f)
	}
	if f.Ptr == nil || *f.Ptr == 0 {
		t.Fatalf("pointer field = %v, want a non-zero pointee", f.Ptr)
	}
	if len(f.Slice) != 1 || f.Slice[0] == "" {
		t.Fatalf("slice field = %v, want one non-zero element", f.Slice)
	}
	if len(f.Bytes) != 1 || f.Bytes[0] == 0 {
		t.Fatalf("byte slice = %v, want one non-zero byte", f.Bytes)
	}
	if !json.Valid(f.Raw) || len(f.Raw) == 0 {
		t.Fatalf("RawMessage = %q, want valid non-empty JSON", f.Raw)
	}
	if len(f.Map) != 1 {
		t.Fatalf("map field = %v, want one entry", f.Map)
	}
	if f.Nested.Name == "" || f.Nested.When.IsZero() {
		t.Fatalf("nested struct = %+v, want every field non-zero including time.Time", f.Nested)
	}
	if f.Any == nil {
		t.Fatalf("any field = nil, want a populated value")
	}
	if f.Err == nil {
		t.Fatalf("error field = nil, want a non-nil error")
	}
}

// TestFillFailsLoudlyOnUnhandledKind pins the property that separates this
// filler from a silently-skipping one: an unhandled reflect kind reports
// through Fatalf rather than leaving the field zero.
func TestFillFailsLoudlyOnUnhandledKind(t *testing.T) {
	r := &fakeReporter{}
	var ch chan int
	reflectfill.Fill(r, reflect.ValueOf(&ch).Elem(), "chanField")
	if !r.failed {
		t.Fatal("filler silently skipped an unhandled kind instead of failing loudly")
	}
}

type stringer interface{ String() string }

// TestFillFailsLoudlyOnMethodBearingInterface pins that a non-empty interface
// field reports through Fatalf instead of panicking in reflect.Value.Set: only
// the empty `any` interface can be populated with the generic container value.
func TestFillFailsLoudlyOnMethodBearingInterface(t *testing.T) {
	r := &fakeReporter{}
	var s stringer
	reflectfill.Fill(r, reflect.ValueOf(&s).Elem(), "stringerField")
	if !r.failed {
		t.Fatal("filler panicked or skipped a method-bearing interface instead of failing loudly")
	}
}

// TestFillFailsLoudlyOnNonComparableMapKey pins that an interface-keyed map
// (whose generic filler value is a non-comparable map) reports through Fatalf
// instead of panicking inside reflect.Value.SetMapIndex.
func TestFillFailsLoudlyOnNonComparableMapKey(t *testing.T) {
	r := &fakeReporter{}
	var m map[any]int
	reflectfill.Fill(r, reflect.ValueOf(&m).Elem(), "anyKeyedMap")
	if !r.failed {
		t.Fatal("filler did not report a non-comparable map key")
	}
}

type recursiveNode struct {
	Next *recursiveNode
}

// TestFillFailsLoudlyOnRecursiveType pins that a self-referential type reports
// through Fatalf at the depth bound instead of overflowing the stack.
func TestFillFailsLoudlyOnRecursiveType(t *testing.T) {
	r := &fakeReporter{}
	var n recursiveNode
	reflectfill.Fill(r, reflect.ValueOf(&n).Elem(), "recursiveNode")
	if !r.failed {
		t.Fatal("filler did not report a recursive type")
	}
}

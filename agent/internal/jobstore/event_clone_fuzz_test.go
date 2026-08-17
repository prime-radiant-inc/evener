//go:build serffuzz

package jobstore

import (
	"reflect"
	"testing"
)

// FuzzCloneEventSharesNoMutableState drives cloneEvent against the bug a
// hand-written deep copy actually has: a forgotten field.
//
// cloneEvent names eight fields explicitly and copies the rest by assignment,
// and the nested helpers do the same again for Causal, WatchSendState and
// WatchEvent. Add a pointer or slice field to any of those structs, forget the
// corresponding line, and everything still compiles and still passes an equality
// test — the clone simply shares the original's memory. Cursor-held events are
// handed to callers that mutate them, so the symptom is one reader's edit
// appearing in another's event, long after the fact and nowhere near this file.
//
// The oracle is therefore structural rather than value-based: walk the original
// and the clone in parallel and require that every pointer, slice and map they
// both hold lives at a DIFFERENT address. That check does not need updating when
// a field is added, which is the whole point — it is looking for the field
// nobody remembered.
//
// Values are populated by reflection for the same reason. Hand-built fixtures
// only exercise the fields their author thought of, and a new field would arrive
// zero-valued, with nothing to alias.
func FuzzCloneEventSharesNoMutableState(f *testing.F) {
	f.Add(uint8(0))
	f.Add(uint8(1))
	f.Add(uint8(7))

	f.Fuzz(func(t *testing.T, variant uint8) {
		var event Event
		populate(reflect.ValueOf(&event).Elem(), int(variant%4)+2)

		clone := cloneEvent(event)

		if !reflect.DeepEqual(event, clone) {
			t.Fatal("cloneEvent produced a value that is not equal to its input")
		}
		assertDistinctStorage(t, reflect.ValueOf(event), reflect.ValueOf(clone), "Event")
	})
}

// populate fills every settable pointer, slice, map and interface reachable from
// v so there is something for a missed copy to alias. Unexported fields are left
// alone: they cannot be set, and a struct like time.Time owns its internals.
func populate(v reflect.Value, depth int) {
	if depth <= 0 || !v.CanSet() {
		return
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		populate(v.Elem(), depth-1)
	case reflect.Slice:
		if v.IsNil() {
			v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		}
		for i := range v.Len() {
			populate(v.Index(i), depth-1)
		}
	case reflect.Map:
		if v.IsNil() {
			m := reflect.MakeMap(v.Type())
			key := reflect.New(v.Type().Key()).Elem()
			val := reflect.New(v.Type().Elem()).Elem()
			populate(val, depth-1)
			m.SetMapIndex(key, val)
			v.Set(m)
		}
	case reflect.Interface:
		if v.IsNil() {
			// A JSON-decoded shape, which is what cloneJSONValue exists for.
			v.Set(reflect.ValueOf(any(map[string]any{"nested": []any{"leaf"}})))
		}
	case reflect.Struct:
		for i := range v.NumField() {
			populate(v.Field(i), depth-1)
		}
	}
}

// assertDistinctStorage requires that nothing mutable is shared between a and b.
func assertDistinctStorage(t *testing.T, a, b reflect.Value, path string) {
	t.Helper()
	if !a.IsValid() || !b.IsValid() || a.Kind() != b.Kind() {
		return
	}
	switch a.Kind() {
	case reflect.Pointer:
		if a.IsNil() || b.IsNil() {
			return
		}
		if a.Pointer() == b.Pointer() {
			t.Fatalf("%s: clone shares the original's pointer; cloneEvent is missing a copy for this field", path)
		}
		assertDistinctStorage(t, a.Elem(), b.Elem(), path+".*")
	case reflect.Slice:
		if a.IsNil() || b.IsNil() || a.Len() == 0 || b.Len() == 0 {
			return
		}
		if a.Pointer() == b.Pointer() {
			t.Fatalf("%s: clone shares the original's slice backing array", path)
		}
		for i := range min(a.Len(), b.Len()) {
			assertDistinctStorage(t, a.Index(i), b.Index(i), path+"[]")
		}
	case reflect.Map:
		if a.IsNil() || b.IsNil() {
			return
		}
		if a.Pointer() == b.Pointer() {
			t.Fatalf("%s: clone shares the original's map, so a write through either is seen by both", path)
		}
	case reflect.Interface:
		if a.IsNil() || b.IsNil() {
			return
		}
		assertDistinctStorage(t, a.Elem(), b.Elem(), path+".(iface)")
	case reflect.Struct:
		for i := range a.NumField() {
			// Unexported fields cannot be read safely here, and their owner is
			// responsible for its own internals.
			if !a.Field(i).CanInterface() || !b.Field(i).CanInterface() {
				continue
			}
			assertDistinctStorage(t, a.Field(i), b.Field(i), path+"."+a.Type().Field(i).Name)
		}
	}
}

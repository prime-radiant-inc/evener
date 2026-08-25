// Package aliascheck proves that a deep copy shares no mutable memory with the
// value it was copied from.
//
// It exists for the bug a hand-written deep copy actually has: a forgotten
// field. A clone function that names each pointer and slice explicitly and
// copies the rest by assignment keeps working — and keeps passing equality
// tests — when a new field is added and its line is not. The clone simply
// shares the original's memory, and the symptom appears later and elsewhere, as
// one holder's mutation showing up in another's value.
//
// The check is structural rather than value-based, so it needs no maintenance
// when a field is added: walk both values in parallel and require every
// pointer, slice and map they both hold to live at a different address. That is
// precisely a search for the field nobody remembered.
//
// Populate exists for the same reason. A hand-built fixture only exercises the
// fields its author thought of, and a newly added field arrives zero-valued
// with nothing for a missed copy to alias.
//
// Nothing here imports testing: FindSharedStorage returns a description so a
// caller reports it however it likes, which keeps this usable from a fuzz
// target, an ordinary test, or a Rapid property.
package aliascheck

import (
	"fmt"
	"reflect"
)

// Populate fills every settable pointer, slice, map and interface reachable
// from v, to depth, so a missed copy has something to alias. Unexported fields
// are left alone: they cannot be set, and a type like time.Time owns its
// internals.
func Populate(v reflect.Value, depth int) {
	if depth <= 0 || !v.CanSet() {
		return
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			v.Set(reflect.New(v.Type().Elem()))
		}
		Populate(v.Elem(), depth-1)
	case reflect.Slice:
		if v.IsNil() {
			v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		}
		for i := range v.Len() {
			Populate(v.Index(i), depth-1)
		}
	case reflect.Map:
		if v.IsNil() {
			m := reflect.MakeMap(v.Type())
			key := reflect.New(v.Type().Key()).Elem()
			val := reflect.New(v.Type().Elem()).Elem()
			Populate(val, depth-1)
			m.SetMapIndex(key, val)
			v.Set(m)
		}
	case reflect.Interface:
		if v.IsNil() {
			// A JSON-decoded shape: the container kind a clone has to rebuild
			// rather than assign.
			v.Set(reflect.ValueOf(any(map[string]any{"nested": []any{"leaf"}})))
		}
	case reflect.Struct:
		for _, field := range v.Fields() {
			Populate(field, depth-1)
		}
	}
}

// FindSharedStorage reports the path of the first mutable value a and b share,
// or "" when they share none. path names the root for readable output, e.g.
// "Event".
func FindSharedStorage(a, b reflect.Value, path string) string {
	if !a.IsValid() || !b.IsValid() || a.Kind() != b.Kind() {
		return ""
	}
	switch a.Kind() {
	case reflect.Pointer:
		if a.IsNil() || b.IsNil() {
			return ""
		}
		if a.Pointer() == b.Pointer() {
			return path + ": copy shares the original's pointer, so the copy is missing a deep copy for this field"
		}
		return FindSharedStorage(a.Elem(), b.Elem(), path+".*")
	case reflect.Slice:
		if a.IsNil() || b.IsNil() || a.Len() == 0 || b.Len() == 0 {
			return ""
		}
		if a.Pointer() == b.Pointer() {
			return path + ": copy shares the original's slice backing array"
		}
		for i := range min(a.Len(), b.Len()) {
			if found := FindSharedStorage(a.Index(i), b.Index(i), path+"[]"); found != "" {
				return found
			}
		}
	case reflect.Map:
		if a.IsNil() || b.IsNil() {
			return ""
		}
		if a.Pointer() == b.Pointer() {
			return path + ": copy shares the original's map, so a write through either is seen by both"
		}
		// A rebuilt map is not enough on its own. Copying entries across
		// (maps.Copy, or a loop that assigns the value unchanged) leaves any
		// pointer or slice INSIDE a value still shared, so the entries have to
		// be compared too. Iterate a's keys and look each one up in b, because
		// map order is not stable.
		for _, key := range a.MapKeys() {
			bv := b.MapIndex(key)
			if !bv.IsValid() {
				continue
			}
			if found := FindSharedStorage(a.MapIndex(key), bv, fmt.Sprintf("%s[%v]", path, key)); found != "" {
				return found
			}
		}
	case reflect.Interface:
		if a.IsNil() || b.IsNil() {
			return ""
		}
		return FindSharedStorage(a.Elem(), b.Elem(), path+".(iface)")
	case reflect.Struct:
		for i := range a.NumField() {
			// An unexported field cannot be read safely here, and its owner is
			// responsible for its own internals.
			if !a.Field(i).CanInterface() || !b.Field(i).CanInterface() {
				continue
			}
			if found := FindSharedStorage(a.Field(i), b.Field(i), path+"."+a.Type().Field(i).Name); found != "" {
				return found
			}
		}
	}
	return ""
}

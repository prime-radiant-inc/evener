// Package reflectfill fills every field reachable from a value non-zero, so a
// test that compares a whole struct, or marshals one, actually exercises every
// field — current and future — without a hand-written fixture to update each
// time a field is added.
//
// It fails loudly through its Reporter on an unhandled reflect kind rather than
// silently leaving a field at its zero value: a filler that skips a kind is a
// coverage test that silently stops covering it.
//
// # Reporter, not *testing.T
//
// Fill takes a Reporter — the two-method subset of *testing.T a caller uses —
// for the same reason fuzz/oracle does: testing.TB is sealed, so a fake that
// captures Fatalf (instead of aborting the goroutine) can only stand in through
// an interface. This package imports only the standard library and nothing from
// primeradiant.com/evener, keeping the fuzz module portable (see fuzz/go.mod).
package reflectfill

import (
	"encoding/json"
	"errors"
	"reflect"
	"time"
)

// Reporter is the slice of the testing API the filler uses. *testing.T
// satisfies it, as does a capturing fake.
type Reporter interface {
	// Fatalf reports a field the filler cannot populate.
	Fatalf(format string, args ...any)
	// Helper marks the caller as a test helper so failures point at the test,
	// not at this library.
	Helper()
}

var (
	timeType       = reflect.TypeFor[time.Time]()
	errorType      = reflect.TypeFor[error]()
	rawMessageType = reflect.TypeFor[json.RawMessage]()
)

// Fill sets every exported field reachable from v (an addressable value) to a
// representative non-zero value, recursing through pointers, slices, maps and
// nested structs. path names the root for readable failure output, e.g. "Entry".
//
// time.Time is filled directly, because its own fields are unexported and
// reflection cannot reach them. An unhandled kind reports through r.Fatalf.
func Fill(r Reporter, v reflect.Value, path string) {
	r.Helper()
	if v.Type() == timeType {
		v.Set(reflect.ValueOf(time.UnixMilli(1_700_000_000_000)))
		return
	}
	switch v.Kind() {
	case reflect.String:
		v.SetString(path)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(1)
	case reflect.Pointer:
		pointer := reflect.New(v.Type().Elem())
		Fill(r, pointer.Elem(), path)
		v.Set(pointer)
	case reflect.Interface:
		if v.Type() == errorType {
			v.Set(reflect.ValueOf(errors.New(path)))
			return
		}
		// Only the empty interface (any) can hold the generic container below.
		// A method-bearing interface needs a value that implements it, which
		// only the caller knows; report it loudly instead of panicking in Set.
		if v.Type().NumMethod() != 0 {
			r.Fatalf("%s: unhandled interface type %v — teach the filler this shape", path, v.Type())
			return
		}
		// A generic `any` field (e.g. a json:",omitempty" structured payload)
		// holds a JSON-serializable value.
		v.Set(reflect.ValueOf(map[string]any{"populated": true}))
	case reflect.Slice:
		// A raw message must hold valid JSON; any other byte slice marshals as
		// base64 and can hold anything.
		if v.Type() == rawMessageType {
			v.SetBytes([]byte(`{"populated":true}`))
			return
		}
		if v.Type().Elem().Kind() == reflect.Uint8 {
			v.SetBytes([]byte{1})
			return
		}
		element := reflect.New(v.Type().Elem())
		Fill(r, element.Elem(), path+"[0]")
		v.Set(reflect.Append(reflect.MakeSlice(v.Type(), 0, 1), element.Elem()))
	case reflect.Map:
		key := reflect.New(v.Type().Key())
		Fill(r, key.Elem(), path+".key")
		value := reflect.New(v.Type().Elem())
		Fill(r, value.Elem(), path+".value")
		entries := reflect.MakeMap(v.Type())
		entries.SetMapIndex(key.Elem(), value.Elem())
		v.Set(entries)
	case reflect.Struct:
		for i := range v.NumField() {
			// Unexported fields are unsettable and invisible to encoding/json,
			// so skipping them cannot hide a field from the marshaled shape.
			if !v.Type().Field(i).IsExported() {
				continue
			}
			Fill(r, v.Field(i), path+"."+v.Type().Field(i).Name)
		}
	default:
		r.Fatalf("%s: unhandled kind %v (type %v) — teach the filler this shape", path, v.Kind(), v.Type())
	}
}

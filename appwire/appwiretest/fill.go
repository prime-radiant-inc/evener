package appwiretest

import (
	"encoding/json"
	"reflect"
)

// FillNonZero sets every exported field of the struct pointed to by v to a
// non-zero value, recursing through pointers, nested structs and slices. A
// field-by-field mapping test fills its source this way so a field added to
// the source later is carried by the test without editing it.
func FillNonZero(v any) {
	fill(reflect.ValueOf(v).Elem(), "x")
}

// ZeroFields returns the names of the exported top-level fields of the struct
// pointed to by v that hold their zero value.
func ZeroFields(v any) []string {
	rv := reflect.ValueOf(v).Elem()
	var zero []string
	for i := range rv.NumField() {
		if rv.Type().Field(i).IsExported() && rv.Field(i).IsZero() {
			zero = append(zero, rv.Type().Field(i).Name)
		}
	}
	return zero
}

func fill(v reflect.Value, name string) {
	switch v.Kind() {
	case reflect.String:
		v.SetString(name)
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(7)
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem(), name)
	case reflect.Slice:
		if v.Type() == reflect.TypeFor[json.RawMessage]() {
			v.Set(reflect.ValueOf(json.RawMessage(`{"k":1}`)))
			return
		}
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fill(v.Index(0), name)
	case reflect.Struct:
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				fill(v.Field(i), v.Type().Field(i).Name)
			}
		}
	}
}

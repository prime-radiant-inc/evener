package registry

import (
	"reflect"
	"testing"
)

// fillEveryField sets every field of v, recursively, to a distinctive
// non-zero value, so a clone, merge or write that skips a field shows up as a
// zero. Unexported fields stay zero.
func fillEveryField(v reflect.Value) {
	switch v.Kind() {
	case reflect.Struct:
		for _, field := range v.Fields() {
			if field.CanSet() {
				fillEveryField(field)
			}
		}
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fillEveryField(v.Elem())
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fillEveryField(v.Index(0))
	case reflect.Map:
		v.Set(reflect.MakeMap(v.Type()))
		key := reflect.New(v.Type().Key()).Elem()
		fillEveryField(key)
		elem := reflect.New(v.Type().Elem()).Elem()
		fillEveryField(elem)
		v.SetMapIndex(key, elem)
	case reflect.Interface:
		v.Set(reflect.ValueOf("any"))
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int64:
		v.SetInt(7)
	case reflect.Float64:
		v.SetFloat(1.5)
	default:
		panic("fillEveryField: unhandled kind " + v.Kind().String())
	}
}

// sharedReferences lists the paths where a and b hold the same pointer, slice
// backing array or map, which a deep clone must not.
func sharedReferences(path string, a, b reflect.Value) []string {
	var shared []string
	switch a.Kind() {
	case reflect.Struct:
		for field := range a.Type().Fields() {
			shared = append(shared, sharedReferences(path+"."+field.Name, a.FieldByIndex(field.Index), b.FieldByIndex(field.Index))...)
		}
	case reflect.Pointer:
		if !a.IsNil() && a.Pointer() == b.Pointer() {
			return []string{path}
		}
		if !a.IsNil() {
			shared = append(shared, sharedReferences(path, a.Elem(), b.Elem())...)
		}
	case reflect.Slice:
		if a.Len() > 0 && a.Pointer() == b.Pointer() {
			return []string{path}
		}
		for i := range min(a.Len(), b.Len()) {
			shared = append(shared, sharedReferences(path, a.Index(i), b.Index(i))...)
		}
	case reflect.Map:
		if a.Len() > 0 && a.Pointer() == b.Pointer() {
			return []string{path}
		}
	}
	return shared
}

func TestCapsCloneKeepsEveryFieldAndSharesNothing(t *testing.T) {
	var caps Caps
	fillEveryField(reflect.ValueOf(&caps).Elem())
	clone := cloneCaps(caps)
	if !reflect.DeepEqual(caps, clone) {
		t.Fatalf("cloneCaps dropped a field:\n got %+v\nwant %+v", clone, caps)
	}
	if shared := sharedReferences("Caps", reflect.ValueOf(caps), reflect.ValueOf(clone)); len(shared) > 0 {
		t.Fatalf("cloneCaps shares memory with its source at %v", shared)
	}
}

func TestCapsMergeKeepsEveryField(t *testing.T) {
	var src, dst Caps
	fillEveryField(reflect.ValueOf(&src).Elem())
	mergeCaps(&dst, src, "test", nil)
	if !reflect.DeepEqual(src, dst) {
		t.Fatalf("mergeCaps onto an empty Caps dropped a field:\n got %+v\nwant %+v", dst, src)
	}
}

func TestTransportCloneKeepsEveryField(t *testing.T) {
	var transport Transport
	fillEveryField(reflect.ValueOf(&transport).Elem())
	clone := cloneTransport(transport)
	if !reflect.DeepEqual(transport, clone) {
		t.Fatalf("cloneTransport dropped a field:\n got %+v\nwant %+v", clone, transport)
	}
	if shared := sharedReferences("Transport", reflect.ValueOf(transport), reflect.ValueOf(clone)); len(shared) > 0 {
		t.Fatalf("cloneTransport shares memory with its source at %v", shared)
	}
}

func TestTransportMergeKeepsEveryField(t *testing.T) {
	var src, dst Transport
	fillEveryField(reflect.ValueOf(&src).Elem())
	mergeTransport(&dst, src)
	if !reflect.DeepEqual(src, dst) {
		t.Fatalf("mergeTransport onto an empty Transport dropped a field:\n got %+v\nwant %+v", dst, src)
	}
}

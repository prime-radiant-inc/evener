package aliascheck

import (
	"maps"
	"reflect"
	"strings"
	"testing"
	"time"
)

type inner struct {
	Names []string
	Count *int
}

type outer struct {
	Ptr      *inner
	Slice    []inner
	Map      map[string]*inner
	Iface    any
	Scalar   string
	Stamp    time.Time
	hidden   *inner //nolint:unused // present so the walker must skip unexported fields
	Recursed *outer
}

func deepCopy(t *testing.T, src outer) outer {
	t.Helper()
	dst := src
	if src.Ptr != nil {
		p := *src.Ptr
		p.Names = append([]string(nil), src.Ptr.Names...)
		if src.Ptr.Count != nil {
			c := *src.Ptr.Count
			p.Count = &c
		}
		dst.Ptr = &p
	}
	if src.Slice != nil {
		dst.Slice = make([]inner, len(src.Slice))
		for i, v := range src.Slice {
			v.Names = append([]string(nil), v.Names...)
			if v.Count != nil {
				c := *v.Count
				v.Count = &c
			}
			dst.Slice[i] = v
		}
	}
	if src.Map != nil {
		dst.Map = make(map[string]*inner, len(src.Map))
		for k, v := range src.Map {
			if v == nil {
				dst.Map[k] = nil
				continue
			}
			c := *v
			c.Names = append([]string(nil), v.Names...)
			dst.Map[k] = &c
		}
	}
	dst.Iface = deepCopyJSON(src.Iface)
	if src.Recursed != nil {
		// Terminates because Populate's depth bound makes the chain finite.
		r := deepCopy(t, *src.Recursed)
		dst.Recursed = &r
	}
	return dst
}

// deepCopyJSON rebuilds the decoded-JSON containers Populate puts in an
// interface field. Without it the fixture is not actually a deep copy, and the
// walker correctly says so.
func deepCopyJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = deepCopyJSON(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = deepCopyJSON(val)
		}
		return out
	default:
		return v
	}
}

func TestFindSharedStorageAcceptsAGenuineDeepCopy(t *testing.T) {
	var src outer
	Populate(reflect.ValueOf(&src).Elem(), 4)
	got := deepCopy(t, src)
	if shared := FindSharedStorage(reflect.ValueOf(src), reflect.ValueOf(got), "outer"); shared != "" {
		t.Fatalf("a genuine deep copy was reported as sharing storage: %s", shared)
	}
}

func TestFindSharedStorageNamesEachKindOfSharing(t *testing.T) {
	// Each case starts from a real deep copy and reintroduces exactly one
	// shared reference, so the report can only come from that one field.
	for _, tc := range []struct {
		name    string
		reshare func(src outer, dst *outer)
		want    string
	}{
		{"shared pointer", func(src outer, dst *outer) { dst.Ptr = src.Ptr }, "outer.Ptr"},
		{"shared slice backing array", func(src outer, dst *outer) { dst.Slice = src.Slice }, "outer.Slice"},
		{"shared map", func(src outer, dst *outer) { dst.Map = src.Map }, "outer.Map"},
		{
			// The map is rebuilt but its VALUES are carried across unchanged,
			// which is what maps.Copy and a naive rebuild loop both do.
			name: "shared pointer inside a rebuilt map",
			reshare: func(src outer, dst *outer) {
				dst.Map = make(map[string]*inner, len(src.Map))
				// Deliberately naive: this is the shape being detected.
				maps.Copy(dst.Map, src.Map)
			},
			want: "outer.Map[",
		},
		{
			name:    "shared slice nested behind a pointer",
			reshare: func(src outer, dst *outer) { dst.Ptr.Names = src.Ptr.Names },
			want:    "outer.Ptr.*.Names",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var src outer
			Populate(reflect.ValueOf(&src).Elem(), 4)
			dst := deepCopy(t, src)
			tc.reshare(src, &dst)

			shared := FindSharedStorage(reflect.ValueOf(src), reflect.ValueOf(dst), "outer")
			if shared == "" {
				t.Fatalf("%s went unreported", tc.name)
			}
			if !strings.Contains(shared, tc.want) {
				t.Fatalf("report %q does not name %q", shared, tc.want)
			}
		})
	}
}

func TestFindSharedStorageIsSafeOnNilAndMismatchedValues(t *testing.T) {
	var empty outer
	if shared := FindSharedStorage(reflect.ValueOf(empty), reflect.ValueOf(empty), "outer"); shared != "" {
		t.Fatalf("two zero values reported as sharing: %s", shared)
	}
	// Nothing is shared when either side is absent, and mismatched kinds are
	// not a comparison this can make.
	if shared := FindSharedStorage(reflect.Value{}, reflect.ValueOf(empty), "outer"); shared != "" {
		t.Fatalf("invalid value reported as sharing: %s", shared)
	}
	if shared := FindSharedStorage(reflect.ValueOf(1), reflect.ValueOf("one"), "x"); shared != "" {
		t.Fatalf("mismatched kinds reported as sharing: %s", shared)
	}
}

func TestPopulateFillsEveryReferenceKind(t *testing.T) {
	var v outer
	Populate(reflect.ValueOf(&v).Elem(), 4)

	if v.Ptr == nil {
		t.Error("pointer field left nil, so a missed copy would have nothing to alias")
	}
	if len(v.Slice) == 0 {
		t.Error("slice field left empty")
	}
	if v.Map == nil {
		t.Error("map field left nil")
	}
	if v.Iface == nil {
		t.Error("interface field left nil")
	}
	if v.Ptr != nil && v.Ptr.Count == nil {
		t.Error("pointer nested behind a pointer left nil, so the walk stops short of nested fields")
	}
	// Depth is a real bound: recursion must stop rather than run forever on a
	// self-referential type.
	var shallow outer
	Populate(reflect.ValueOf(&shallow).Elem(), 1)
	if shallow.Recursed != nil && shallow.Recursed.Recursed != nil {
		t.Error("depth bound did not stop the walk")
	}
}

// Package typegen bridges Go types to the JSON-Schema subset schemagen
// consumes: SchemaFromType reflects a reflect.Type into a map[string]any schema
// (mirroring encoding/json's marshalling rules), and Registry indexes many named
// types/schemas behind one source-driven generator interface.
//
// Like schemagen, typegen is the serf-agnostic core of the fuzzing toolkit: it
// imports only the standard library, pgregory.net/rapid, and its sibling
// schemagen — NOTHING here may import any primeradiant.com/serf package. Go
// types cross that boundary as reflect.Type (a stdlib interface carrying no
// import edge), so a serf-side test can hand its wire structs to a serf-free
// registry. That structural boundary is the portability test.
package typegen

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"

	"pgregory.net/rapid"

	"primeradiant.com/serf/fuzz/schemagen"
)

var (
	rawMessageType    = reflect.TypeOf(json.RawMessage(nil))
	jsonMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
)

// SchemaFromType converts a Go type into the map[string]any JSON-Schema subset
// schemagen consumes. It mirrors encoding/json's marshalling rules so a
// generated value, marshalled and re-decoded, round-trips into the same Go type.
// It applies no per-type overrides; the Registry threads its override table
// through the same recursion (see schemaFromType).
func SchemaFromType(t reflect.Type) map[string]any {
	return schemaFromType(t, nil, map[reflect.Type]bool{})
}

// GeneratorForType builds a rapid generator of values for t in the given mode.
func GeneratorForType(t reflect.Type, mode schemagen.Mode) *rapid.Generator[any] {
	return schemagen.Generator(SchemaFromType(t), mode)
}

// schemaFromType is the recursive core. overrides (may be nil) is consulted at
// every node before the kind switch, so a per-type override fires at nested
// depth. visited collapses a self-referential / mutually-recursive type to an
// untyped schema rather than expanding forever.
func schemaFromType(t reflect.Type, overrides map[reflect.Type]map[string]any, visited map[reflect.Type]bool) map[string]any {
	if t == nil {
		return map[string]any{}
	}
	// Dereference pointers first so an override / custom-marshaler keyed on the
	// value type still applies through a pointer (nil ↔ JSON null).
	if t.Kind() == reflect.Pointer {
		return nullable(schemaFromType(t.Elem(), overrides, visited))
	}
	if ov, ok := overrides[t]; ok {
		return cloneSchema(ov)
	}
	// json.RawMessage is []byte but passes raw JSON through, NOT base64 — detect
	// by exact type before the []byte rule.
	if t == rawMessageType {
		return map[string]any{}
	}
	// A custom json.Marshaler with no override has an unknown JSON shape; map it
	// to untyped (the harness drops the round-trip oracle for it). LaunchConfigLayer
	// is the only such type today, and it always carries an override.
	if implementsJSONMarshaler(t) {
		return map[string]any{}
	}

	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string"} // []byte → base64 string
		}
		return map[string]any{"type": "array", "items": schemaFromType(t.Elem(), overrides, visited)}
	case reflect.Array:
		return map[string]any{"type": "array", "items": schemaFromType(t.Elem(), overrides, visited)}
	case reflect.Map:
		// JSON object keyed by strings, all values one schema (an open object —
		// schemagen treats a subschema additionalProperties as open).
		return map[string]any{
			"type":                 "object",
			"additionalProperties": schemaFromType(t.Elem(), overrides, visited),
		}
	case reflect.Struct:
		return structSchema(t, overrides, visited)
	case reflect.Interface:
		return map[string]any{} // any → arbitrary JSON
	default:
		return map[string]any{} // chan/func/complex never appear on the wire
	}
}

// structSchema builds a closed object schema by walking exported fields. visited
// guards against cycles: a type already being expanded up the stack collapses to
// an untyped schema.
func structSchema(t reflect.Type, overrides map[reflect.Type]map[string]any, visited map[reflect.Type]bool) map[string]any {
	if visited[t] {
		return map[string]any{}
	}
	visited[t] = true
	defer delete(visited, t)

	props := map[string]any{}
	var required []string
	collectFields(t, overrides, visited, props, &required)
	sort.Strings(required)
	return map[string]any{
		"type":                 "object",
		"properties":           props,
		"required":             required,
		"additionalProperties": false,
	}
}

// collectFields populates props/required for t's exported fields, flattening
// promoted fields of anonymous (embedded) structs as encoding/json does.
func collectFields(t reflect.Type, overrides map[reflect.Type]map[string]any, visited map[reflect.Type]bool, props map[string]any, required *[]string) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue // explicitly skipped
		}
		name, opts := parseJSONTag(tag)

		// An anonymous struct with no explicit JSON name is promoted into the parent.
		if f.Anonymous && name == "" {
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				collectFields(ft, overrides, visited, props, required)
				continue
			}
		}
		if f.PkgPath != "" {
			continue // unexported, non-promotable
		}
		if name == "" {
			name = f.Name
		}
		props[name] = schemaFromType(f.Type, overrides, visited)
		if fieldRequired(f, opts) {
			*required = append(*required, name)
		}
	}
}

// fieldRequired reports whether a field must be present for a valid value. A
// field is required iff it has no omitempty AND its kind does not naturally
// encode to null/absent (pointer/slice/map/interface).
func fieldRequired(f reflect.StructField, opts map[string]bool) bool {
	if opts["omitempty"] {
		return false
	}
	switch f.Type.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Interface:
		return false
	}
	return true
}

// parseJSONTag splits a struct json tag into its name and option set.
func parseJSONTag(tag string) (name string, opts map[string]bool) {
	opts = map[string]bool{}
	if tag == "" {
		return "", opts
	}
	parts := strings.Split(tag, ",")
	name = parts[0]
	for _, o := range parts[1:] {
		opts[o] = true
	}
	return name, opts
}

// nullable widens a schema's type set to also permit JSON null, modelling a Go
// pointer (nil ↔ null). An untyped schema already accepts null.
func nullable(inner map[string]any) map[string]any {
	out := cloneSchema(inner)
	if tv, ok := out["type"].(string); ok {
		out["type"] = []string{tv, "null"}
	}
	return out
}

// cloneSchema shallow-copies a schema map so wrapping (e.g. nullable) never
// mutates a shared override schema.
func cloneSchema(s map[string]any) map[string]any {
	out := make(map[string]any, len(s))
	for k, v := range s {
		out[k] = v
	}
	return out
}

// implementsJSONMarshaler reports whether t or *t implements json.Marshaler
// (encoding/json uses the pointer method on an addressable value).
func implementsJSONMarshaler(t reflect.Type) bool {
	return t.Implements(jsonMarshalerType) || reflect.PointerTo(t).Implements(jsonMarshalerType)
}

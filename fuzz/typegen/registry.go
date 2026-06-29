package typegen

import (
	"reflect"
	"sort"

	"pgregory.net/rapid"

	"primeradiant.com/serf/fuzz/schemagen"
)

// Registry is a thin index of named wire types: each name maps to a JSON-Schema
// (extracted once via reflection, or supplied directly) that it hands to
// schemagen behind whichever Source the caller supplies. All value generation
// stays in schemagen; the registry adds the catalog dimension (many named types
// in one table), the reflect-intake path, and the per-type override table.
//
// It is serf-free: types enter as reflect.Type or as already-built JSON schemas,
// never as a serf import.
type Registry struct {
	entries   map[string]map[string]any
	overrides map[reflect.Type]map[string]any
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		entries:   map[string]map[string]any{},
		overrides: map[reflect.Type]map[string]any{},
	}
}

// RegisterTypeSchema records a hand-authored schema for a type, consulted by
// RegisterType at top level AND at every nested occurrence (so a custom
// json.Marshaler's true shape fires even when the type is reached transitively).
// Register overrides before the RegisterType calls that should observe them.
func (r *Registry) RegisterTypeSchema(t reflect.Type, schema map[string]any) {
	r.overrides[t] = schema
}

// RegisterType reflects t into a schema (honoring registered overrides) under
// name.
func (r *Registry) RegisterType(name string, t reflect.Type) {
	r.entries[name] = schemaFromType(t, r.overrides, map[reflect.Type]bool{})
}

// RegisterSchema records an already-built JSON schema under name, for surfaces
// that already have JSON (e.g. tool-argument Parameters).
func (r *Registry) RegisterSchema(name string, schema map[string]any) {
	r.entries[name] = schema
}

// Schema returns the schema registered under name.
func (r *Registry) Schema(name string) (map[string]any, bool) {
	s, ok := r.entries[name]
	return s, ok
}

// Value generates one value for name in the given mode, drawing entropy from s
// (the byte-Source path for testing.F targets). The bool reports whether name is
// registered.
func (r *Registry) Value(name string, mode schemagen.Mode, s schemagen.Source) (any, bool) {
	schema, ok := r.entries[name]
	if !ok {
		return nil, false
	}
	return schemagen.Value(s, schema, mode), true
}

// Generator returns a rapid generator for name (the rapid.Check path), or nil if
// name is not registered.
func (r *Registry) Generator(name string, mode schemagen.Mode) *rapid.Generator[any] {
	schema, ok := r.entries[name]
	if !ok {
		return nil
	}
	return schemagen.Generator(schema, mode)
}

// Names returns the registered names in sorted order, for deterministic harness
// iteration.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.entries))
	for k := range r.entries {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

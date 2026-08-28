package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"pgregory.net/rapid"

	"primeradiant.com/evener/fuzz/schemagen"
)

func TestProjectToolSchemaBehaviorMatrix(t *testing.T) {
	base := projectionFixture([]string{"a", "b", "c"}, []string{"b", "c"})
	projection, err := projectToolSchema(base)
	if err != nil {
		t.Fatalf("projectToolSchema: %v", err)
	}
	if projection.absent == nil || projection.present == nil {
		t.Fatalf("projection arms = %#v, want both arms", projection)
	}
	if _, ok := projection.absent["properties"].(map[string]any)["gate"]; ok {
		t.Fatal("absent projection retained trigger property")
	}
	presentProps := projection.present["properties"].(map[string]any)
	if got := presentProps["gate"].(map[string]any)["enum"]; !reflect.DeepEqual(got, []any{"b", "c"}) {
		t.Fatalf("present enum = %#v, want intersection", got)
	}
	if got := projection.present["required"]; !reflect.DeepEqual(got, []string{"gate", "mode"}) {
		t.Fatalf("present required = %#v", got)
	}

	original := compileProjectionSchema(t, base)
	for _, arm := range []map[string]any{projection.absent, projection.present} {
		projected := compileProjectionSchema(t, arm)
		runRapidProjectionValues(t, arm, original, projected)
		runByteProjectionValues(t, arm, original, projected)
	}
}

// The real registry schemas must be projection-free: no core tool ships a
// oneOf disjunction, so every schema goes through the plain generator path.
// (The delegate tool's former sandbox/sandbox_net oneOf was replaced by a
// single combined enum — an invalid combo is now unrepresentable rather than
// excluded by a conditional.) projectToolSchema itself stays covered by
// TestProjectToolSchemaBehaviorMatrix with synthetic fixtures.
func TestProjectToolSchemaUsesRealCapableToolShape(t *testing.T) {
	defs := coreToolSchemaDefs(t)
	if len(defs) == 0 {
		t.Fatal("capable fixture exposed no tool schemas")
	}
	for _, td := range defs {
		if _, err := projectToolSchema(td.params); !errors.Is(err, errProjectionNotNeeded) {
			t.Fatalf("tool %q schema must not need projection, got %v", td.name, err)
		}
		generator, err := projectedToolGenerator(td.params)
		if err != nil {
			t.Fatalf("tool %q generator: %v", td.name, err)
		}
		if generator == nil {
			t.Fatalf("tool %q produced no generator", td.name)
		}
	}
}

func TestProjectedToolGeneratorRejectsUnsupportedShapeSynchronously(t *testing.T) {
	schema := projectionFixture([]string{"a", "b"}, []string{"b"})
	schema["oneOf"].([]any)[1].(map[string]any)["required"] = []string{"mode"}
	original := compileProjectionSchema(t, schema)
	if original == nil {
		t.Fatal("compiled unsupported schema is nil")
	}
	if _, err := projectedToolGenerator(schema); err == nil {
		t.Fatal("projectedToolGenerator accepted unsupported overlapping shape")
	}
}

func TestProjectToolSchemaRemovesOptionalEmptyIntersection(t *testing.T) {
	schema := projectionFixture([]string{"a"}, []string{"a"})
	schema["properties"].(map[string]any)["optional"] = map[string]any{
		"type": "string",
		"enum": []string{"parent"},
	}
	schema["oneOf"].([]any)[1].(map[string]any)["properties"].(map[string]any)["optional"] = map[string]any{
		"enum": []string{"branch"},
	}

	projection, err := projectToolSchema(schema)
	if err != nil {
		t.Fatalf("projectToolSchema: %v", err)
	}
	presentProps := projection.present["properties"].(map[string]any)
	if _, ok := presentProps["optional"]; ok {
		t.Fatal("present projection retained optional empty-intersection property")
	}

	original := compileProjectionSchema(t, schema)
	present := compileProjectionSchema(t, projection.present)
	runRapidProjectionValues(t, projection.present, original, present)
	runByteProjectionValues(t, projection.present, original, present)
}

func TestProjectToolSchemaRejectsUnsupportedShapes(t *testing.T) {
	tests := []struct {
		name string
		edit func(map[string]any)
	}{
		{"overlapping required arm", func(s map[string]any) {
			s["oneOf"].([]any)[1].(map[string]any)["required"] = []string{"mode"}
		}},
		{"nested combinator", func(s map[string]any) {
			s["properties"].(map[string]any)["mode"].(map[string]any)["oneOf"] = []any{map[string]any{}}
		}},
		{"multi key exclusion", func(s map[string]any) {
			s["oneOf"].([]any)[0] = map[string]any{"not": map[string]any{"required": []string{"gate", "mode"}}}
		}},
		{"unsupported assertion", func(s map[string]any) { s["patternProperties"] = map[string]any{} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			schema := projectionFixture([]string{"a", "b"}, []string{"b"})
			tc.edit(schema)
			if _, err := projectToolSchema(schema); err == nil {
				t.Fatal("projectToolSchema accepted unsupported shape")
			}
		})
	}
}

func TestProjectToolSchemaRemovesUnsatisfiableArms(t *testing.T) {
	conflicting := projectionFixture([]string{"a"}, []string{"b"})
	projection, err := projectToolSchema(conflicting)
	if err != nil {
		t.Fatalf("conflicting enum should retain absent arm: %v", err)
	}
	if projection.absent == nil || projection.present != nil {
		t.Fatalf("conflicting projection = %#v", projection)
	}

	zero := projectionFixture([]string{"a"}, []string{"b"})
	zero["required"] = []string{"gate"}
	if _, err := projectToolSchema(zero); err == nil {
		t.Fatal("zero-arm projection did not return an error")
	}
}

func projectionFixture(parent, refinement []string) map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"gate": map[string]any{"type": "string", "enum": append([]string(nil), parent...)},
			"mode": map[string]any{"type": "string", "enum": []string{"x", "y"}},
		},
		"required": []string{},
		"oneOf": []any{
			map[string]any{"not": map[string]any{"required": []string{"gate"}}},
			map[string]any{
				"required":   []string{"gate", "mode"},
				"properties": map[string]any{"gate": map[string]any{"enum": append([]string(nil), refinement...)}},
			},
		},
	}
}

func compileProjectionSchema(t *testing.T, schema map[string]any) *jsonschema.Schema {
	t.Helper()
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	compiler := jsonschema.NewCompiler()
	const uri = "mem://projection.json"
	if err := compiler.AddResource(uri, bytes.NewReader(encoded)); err != nil {
		t.Fatalf("add schema resource: %v", err)
	}
	compiled, err := compiler.Compile(uri)
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return compiled
}

func runRapidProjectionValues(t *testing.T, schema map[string]any, original, projected *jsonschema.Schema) {
	t.Helper()
	gen := schemagen.Generator(schema, schemagen.Valid)
	rapid.Check(t, func(rt *rapid.T) {
		for range 4 {
			value := gen.Draw(rt, "rapid_projection")
			if err := projected.Validate(value); err != nil {
				rt.Fatalf("projected value rejected: %v; value=%#v", err, value)
			}
			if err := original.Validate(value); err != nil {
				rt.Fatalf("original value rejected: %v; value=%#v", err, value)
			}
		}
	})
}

func runByteProjectionValues(t *testing.T, schema map[string]any, original, projected *jsonschema.Schema) {
	t.Helper()
	for i := range 300 {
		value := schemagen.Value(schemagen.NewByteSource([]byte{byte(i), byte(i >> 8), byte(i >> 16), 0x51}), schema, schemagen.Valid)
		if err := projected.Validate(value); err != nil {
			t.Fatalf("byte projected value %d rejected: %v; value=%#v", i, err, value)
		}
		if err := original.Validate(value); err != nil {
			t.Fatalf("byte original value %d rejected: %v; value=%#v", i, err, value)
		}
	}
}

// schemaProjection contains the two concrete schemas for the only composed
// shape understood by the live schema fuzz target. A nil arm is unsatisfiable.
type schemaProjection struct {
	absent  map[string]any
	present map[string]any
}

var errProjectionNotNeeded = errors.New("schema projection not needed")

// projectToolSchema recognizes the narrow capability-derived disjunction. It
// rejects composition outside this shape instead of expanding schemagen.
func projectToolSchema(root map[string]any) (schemaProjection, error) {
	var out schemaProjection
	if root == nil {
		return out, errors.New("projection requires a closed object root")
	}
	if !projectionContainsCombinator(root) {
		return out, errProjectionNotNeeded
	}
	if _, ok := root["oneOf"]; !ok {
		return out, errors.New("unsupported composed schema")
	}
	if root["type"] != "object" || root["additionalProperties"] != false {
		return out, errors.New("projection requires a closed object root")
	}
	for key := range root {
		switch key {
		case "type", "additionalProperties", "properties", "required", "oneOf":
		default:
			return out, fmt.Errorf("unsupported root assertion %q", key)
		}
	}
	props, ok := projectionSchemaMap(root["properties"])
	if !ok {
		return out, errors.New("projection properties must be an object")
	}
	branches, ok := projectionBranches(root["oneOf"])
	if !ok || len(branches) != 2 {
		return out, errors.New("projection requires exactly two object branches")
	}
	for name, prop := range props {
		if err := rejectProjectionCombinator(prop); err != nil {
			return out, fmt.Errorf("property %q: %w", name, err)
		}
	}

	notIndex, reqIndex := -1, -1
	var trigger string
	for i, branch := range branches {
		if projectionSingletonNot(branch) {
			if notIndex >= 0 {
				return out, errors.New("projection has multiple not branches")
			}
			notIndex = i
			n := branch["not"].(map[string]any)
			trigger = projectionStrings(n["required"])[0]
			continue
		}
		if reqIndex >= 0 {
			return out, errors.New("projection has multiple required branches")
		}
		reqIndex = i
	}
	if notIndex < 0 || reqIndex < 0 || trigger == "" {
		return out, errors.New("projection requires one singleton not branch")
	}
	if _, ok := props[trigger]; !ok {
		return out, errors.New("projection trigger is not a root property")
	}

	requiredBranch := branches[reqIndex]
	for key := range requiredBranch {
		if key != "required" && key != "properties" {
			return out, fmt.Errorf("unsupported required-branch assertion %q", key)
		}
	}
	branchRequired := uniqueProjectionStrings(projectionStrings(requiredBranch["required"]))
	if len(branchRequired) < 2 || !containsProjectionString(branchRequired, trigger) {
		return out, errors.New("projection required branch must require trigger and dependent keys")
	}
	for _, name := range branchRequired {
		if _, ok := props[name]; !ok {
			return out, fmt.Errorf("projection required property %q is absent from root properties", name)
		}
	}
	refinements, err := projectionEnumRefinements(requiredBranch["properties"], props)
	if err != nil {
		return out, err
	}

	parentRequired := uniqueProjectionStrings(projectionStrings(root["required"]))
	if containsProjectionString(parentRequired, trigger) {
		out.absent = nil
	} else {
		out.absent = cloneProjectionSchema(root)
		delete(out.absent, "oneOf")
		absentProps := cloneProjectionSchemaMap(props)
		delete(absentProps, trigger)
		out.absent["properties"] = absentProps
		out.absent["required"] = withoutProjectionString(parentRequired, trigger)
	}

	present := cloneProjectionSchema(root)
	delete(present, "oneOf")
	presentProps := cloneProjectionSchemaMap(props)
	for name, enum := range refinements {
		p, ok := projectionSchemaMap(presentProps[name])
		if !ok {
			return out, fmt.Errorf("property refinement %q is absent from root properties", name)
		}
		p = cloneProjectionSchema(p)
		p["enum"] = enum
		presentProps[name] = p
	}
	present["properties"] = presentProps
	presentRequired := uniqueProjectionStrings(append(append([]string(nil), parentRequired...), branchRequired...))
	if projectionPresentUnsatisfiable(presentProps, presentRequired) {
		out.present = nil
	} else {
		present["required"] = presentRequired
		out.present = present
	}
	if out.absent == nil && out.present == nil {
		return schemaProjection{}, errors.New("projection has zero satisfiable arms")
	}
	return out, nil
}

func projectedToolGenerator(root map[string]any) (*rapid.Generator[any], error) {
	projection, err := projectToolSchema(root)
	if err != nil {
		if errors.Is(err, errProjectionNotNeeded) {
			return schemagen.Generator(root, schemagen.Valid), nil
		}
		return nil, err
	}
	var arms []*rapid.Generator[any]
	if projection.absent != nil {
		arms = append(arms, schemagen.Generator(projection.absent, schemagen.Valid))
	}
	if projection.present != nil {
		arms = append(arms, schemagen.Generator(projection.present, schemagen.Valid))
	}
	return rapid.Custom(func(t *rapid.T) any {
		i := rapid.IntRange(0, len(arms)-1).Draw(t, "schema_projection")
		return arms[i].Draw(t, "projected_args")
	}), nil
}

func projectionContainsCombinator(v any) bool {
	m, ok := projectionSchemaMap(v)
	if !ok {
		if list, ok := v.([]any); ok {
			return slices.ContainsFunc(list, projectionContainsCombinator)
		}
		return false
	}
	for _, key := range []string{"oneOf", "anyOf", "allOf", "not"} {
		if _, ok := m[key]; ok {
			return true
		}
	}
	if props, ok := projectionSchemaMap(m["properties"]); ok {
		for _, prop := range props {
			if projectionContainsCombinator(prop) {
				return true
			}
		}
	}
	for _, key := range []string{"additionalProperties", "items", "contains", "propertyNames", "if", "then", "else", "unevaluatedProperties", "unevaluatedItems"} {
		if projectionContainsCombinator(m[key]) {
			return true
		}
	}
	for _, key := range []string{"$defs", "definitions", "dependentSchemas"} {
		if defs, ok := projectionSchemaMap(m[key]); ok {
			for _, definition := range defs {
				if projectionContainsCombinator(definition) {
					return true
				}
			}
		}
	}
	return false
}

func projectionSingletonNot(branch map[string]any) bool {
	if len(branch) != 1 {
		return false
	}
	n, ok := branch["not"].(map[string]any)
	if !ok || len(n) != 1 {
		return false
	}
	return len(projectionStrings(n["required"])) == 1
}

func projectionEnumRefinements(raw any, parent map[string]any) (map[string][]any, error) {
	if raw == nil {
		return nil, nil
	}
	branch, ok := projectionSchemaMap(raw)
	if !ok {
		return nil, errors.New("required-branch properties must be an object")
	}
	out := make(map[string][]any, len(branch))
	for name, rawProp := range branch {
		prop, ok := projectionSchemaMap(rawProp)
		if !ok || len(prop) != 1 {
			return nil, fmt.Errorf("property refinement %q is unsupported", name)
		}
		enum, ok := projectionEnums(prop["enum"])
		if !ok || len(enum) == 0 {
			return nil, fmt.Errorf("property refinement %q must be a non-empty enum", name)
		}
		parentProp, ok := projectionSchemaMap(parent[name])
		if !ok {
			return nil, fmt.Errorf("property refinement %q is absent from root properties", name)
		}
		parentEnum, ok := projectionEnums(parentProp["enum"])
		if !ok || len(parentEnum) == 0 {
			return nil, fmt.Errorf("property refinement %q has no parent enum", name)
		}
		out[name] = projectionEnumIntersection(parentEnum, enum)
	}
	return out, nil
}

func projectionPresentUnsatisfiable(props map[string]any, required []string) bool {
	for name, prop := range props {
		if schema, ok := projectionSchemaMap(prop); ok {
			if enum, ok := projectionEnums(schema["enum"]); ok && len(enum) == 0 {
				if containsProjectionString(required, name) {
					return true
				}
				delete(props, name)
			}
		}
	}
	for _, name := range required {
		if _, ok := props[name]; !ok {
			return true
		}
		if prop, ok := projectionSchemaMap(props[name]); ok {
			if enum, ok := projectionEnums(prop["enum"]); ok && len(enum) == 0 {
				return true
			}
		}
	}
	return false
}

func rejectProjectionCombinator(v any) error {
	if list, ok := v.([]any); ok {
		for _, child := range list {
			if err := rejectProjectionCombinator(child); err != nil {
				return err
			}
		}
		return nil
	}
	m, ok := projectionSchemaMap(v)
	if !ok {
		return nil
	}
	for _, key := range []string{"oneOf", "anyOf", "allOf", "not"} {
		if _, found := m[key]; found {
			return fmt.Errorf("nested combinator %q is unsupported", key)
		}
	}
	for _, child := range m {
		if err := rejectProjectionCombinator(child); err != nil {
			return err
		}
	}
	return nil
}

func projectionSchemaMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

func projectionBranches(v any) ([]map[string]any, bool) {
	switch x := v.(type) {
	case []any:
		out := make([]map[string]any, 0, len(x))
		for _, item := range x {
			m, ok := projectionSchemaMap(item)
			if !ok {
				return nil, false
			}
			out = append(out, m)
		}
		return out, true
	case []map[string]any:
		return append([]map[string]any(nil), x...), true
	default:
		return nil, false
	}
}

func projectionStrings(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []string:
		return append([]string(nil), x...)
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func projectionEnums(v any) ([]any, bool) {
	switch x := v.(type) {
	case []any:
		return append([]any(nil), x...), true
	case []string:
		out := make([]any, len(x))
		for i := range x {
			out[i] = x[i]
		}
		return out, true
	case []bool:
		out := make([]any, len(x))
		for i := range x {
			out[i] = x[i]
		}
		return out, true
	default:
		return nil, false
	}
}

func projectionEnumIntersection(parent, refinement []any) []any {
	out := make([]any, 0, len(parent))
	for _, value := range parent {
		for _, candidate := range refinement {
			if reflect.DeepEqual(value, candidate) {
				out = append(out, value)
				break
			}
		}
	}
	return out
}

func uniqueProjectionStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func withoutProjectionString(values []string, remove string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != remove {
			out = append(out, value)
		}
	}
	return out
}

func containsProjectionString(values []string, want string) bool {
	return slices.Contains(values, want)
}

func cloneProjectionSchema(v map[string]any) map[string]any {
	out := make(map[string]any, len(v))
	for key, value := range v {
		out[key] = cloneProjectionValue(value)
	}
	return out
}

func cloneProjectionSchemaMap(v map[string]any) map[string]any { return cloneProjectionSchema(v) }

func cloneProjectionValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return cloneProjectionSchema(x)
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = cloneProjectionValue(x[i])
		}
		return out
	case []string:
		return append([]string(nil), x...)
	case []bool:
		return append([]bool(nil), x...)
	default:
		return v
	}
}

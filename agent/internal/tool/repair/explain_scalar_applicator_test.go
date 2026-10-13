package repair

import "testing"

// Issue #4099: objectCandidateSatisfies judges a nested property through
// candidateMatchesSchema, whose scalar path read only a schema's own keywords.
// A property defined through a combining applicator (anyOf/oneOf/allOf/not/...)
// was therefore reported as satisfied whatever the scalar value, so an object
// candidate could be reported as satisfying a schema it does not. The walk
// cannot evaluate an applicator for a scalar, so it refuses rather than
// asserts, and the candidate declines.
func TestObjectCandidateSatisfies_NestedScalarApplicator(t *testing.T) {
	t.Parallel()
	str := map[string]any{"type": "string"}
	num := map[string]any{"type": "integer"}
	// A nested object property the recursive check CAN evaluate: the fix must
	// not turn this into a blanket refusal of anything with a nested shape.
	nested := map[string]any{
		"type":       "object",
		"properties": map[string]any{"x": num},
	}
	for _, tc := range []struct {
		name  string
		value any
		prop  map[string]any
		want  bool
	}{
		// Refused: an applicator the scalar path does not evaluate. The branch
		// case is refused too: evaluating the branches would assert, and the
		// package's direction is to refuse when unsure.
		{"anyOf no branch matches", "x", map[string]any{"anyOf": []any{num}}, false},
		{"anyOf a branch matches", 1, map[string]any{"anyOf": []any{num}}, false},
		{"oneOf", "x", map[string]any{"oneOf": []any{num}}, false},
		{"allOf", 5, map[string]any{"allOf": []any{str}}, false},
		{"not", "x", map[string]any{"not": str}, false},
		{"$ref", "x", map[string]any{"$ref": "#/defs/x"}, false},
		{"pattern", "zzz", map[string]any{"pattern": "^a"}, false},
		// Unchanged: a schema speaking only its own keywords is still judged.
		{"own keyword matches", 1, num, true},
		{"own keyword rejects", "x", num, false},
		{"own numeric bound accepts", 7, map[string]any{"minimum": 5}, true},
		{"own numeric bound rejects", 1, map[string]any{"minimum": 5}, false},
		{"own const rejects a scalar", 1, map[string]any{"const": 2}, false},
		{"own type rejects a scalar", "x", map[string]any{"type": "object"}, false},
		// The own-keyword check must run before the container dispatch: the
		// object walk does not read the candidate's own const.
		{"own const rejects an object candidate", map[string]any{"x": 2}, map[string]any{"const": map[string]any{"x": 1}}, false},
		// Unchanged: a container value keeps its recursive check.
		{"nested object satisfies", map[string]any{"x": 1}, nested, true},
		{"nested object rejects", map[string]any{"x": "no"}, nested, false},
		{"object value with anyOf declines", map[string]any{}, map[string]any{"anyOf": []any{num}}, false},
		{"array value with anyOf declines", []any{}, map[string]any{"anyOf": []any{num}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			schema := map[string]any{"properties": map[string]any{"a": tc.prop}}
			got := objectCandidateSatisfies(map[string]any{"a": tc.value}, schema)
			if got != tc.want {
				t.Fatalf("objectCandidateSatisfies(a=%v) against %v = %v, want %v", tc.value, tc.prop, got, tc.want)
			}
		})
	}
}

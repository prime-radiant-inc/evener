package openaichat

import "testing"

// A typed nil required list is JSON null, not an empty list.
//
// Strict projection writes an empty required list for an object with no
// properties. For a schema that already advertises a closed object with no
// properties and a nil required slice, that rewrite is the only difference, and
// it costs two bytes: null is four and [] is two. Reporting the two as the same
// length keeps a count that is two bytes too low for every such object.
//
// The reorder case is the control: the projection only sorts the names there, so
// the two lists serialize to the same length and the count is reused.
func TestAnEmptyRequiredListIsNotANilOne(t *testing.T) {
	t.Parallel()
	var nilRequired []string
	advertised := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           map[string]any{},
		"required":             nilRequired,
	}
	projected := StrictifyJSONSchema(advertised)
	if same := SameSerializedLength(advertised, projected); same {
		t.Fatalf("a nil required list and the empty list projection writes were reported as the same length: "+
			"the projection emits %v where the schema advertised %v", projected["required"], advertised["required"])
	}

	// The control: the projection only reorders these names, so the bytes are the
	// same and the count must be reused.
	reordered := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"a": map[string]any{"type": "string"},
			"b": map[string]any{"type": "string"},
		},
		"required": []string{"b", "a"},
	}
	if !SameSerializedLength(reordered, StrictifyJSONSchema(reordered)) {
		t.Fatal("a projection that only reorders the same required names was reported as a different length")
	}
}

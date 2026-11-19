package openaichat

import (
	"encoding/json"
	"strings"
	"testing"
)

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

// A raw subschema and the map it decodes to are the same length only when their
// bytes are.
//
// A copy decodes a json.RawMessage child, because the walk and the projection
// read maps and would otherwise skip it. The decoded map re-encodes with the
// text the raw form escaped, so the two spellings differ in length even though
// they describe one schema. Reporting them as the same keeps a count that
// over-bills the decoded form the builder sends, which is what a request near an
// input cap is refused on.
//
// The control is the guarantee the comparison exists for: a copy of a plain
// map that changed nothing but a required list's order is the same length.
func TestARawSubschemaIsNotItsDecodedTwin(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"description":"` + strings.Repeat(`\u00e9`, 32) + `","type":"string"}`)
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode the raw subschema: %v", err)
	}
	original := map[string]any{
		"type":       "object",
		"properties": map[string]any{"target": raw},
	}
	projected := map[string]any{
		"type":       "object",
		"properties": map[string]any{"target": decoded},
	}
	if SameSerializedLength(original, projected) {
		t.Fatal("a raw subschema and the decoded form the builder sends were reported as the same length")
	}

	// The control: a copy that only reorders a required list is the same length.
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"a": map[string]any{"type": "string"},
			"b": map[string]any{"type": "string"},
		},
		// Deliberately unsorted, so the projection changes the value without
		// changing its serialized length.
		"required": []string{"b", "a"},
	}
	if !SameSerializedLength(schema, StrictifyJSONSchema(schema)) {
		t.Fatal("a copy that only reorders required names was reported as a different length")
	}
}

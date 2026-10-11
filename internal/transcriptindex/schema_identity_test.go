package transcriptindex

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// fingerprintOf hashes schemaFields(reflect.TypeFor[T]()) the same way
// schemaFieldFingerprint hashes transcript.Entry's fields, so a synthetic
// struct's fingerprint is comparable the same way.
func fingerprintOf[T any]() []string {
	return schemaFields(reflect.TypeFor[T]())
}

// TestSchemaFieldsIsSensitiveToShapeChanges pins schemaFields against the
// exact classes of shape change the schema-skew guard exists to catch: an
// added/removed field, a renamed json key, a tag option change, and an
// embedded-vs-named field of the identical type. A walk that silently no-ops
// on any of these would leave schemaID (and so currentProjection) unable to
// tell two builds' schemas apart, defeating the guard while every other test
// stayed green (roborev finding: the guard's core mechanism had no direct
// test).
func TestSchemaFieldsIsSensitiveToShapeChanges(t *testing.T) {
	type base struct {
		A string `json:"a"`
		B int    `json:"b"`
	}
	type addedField struct {
		A string `json:"a"`
		B int    `json:"b"`
		C bool   `json:"c"`
	}
	type removedField struct {
		A string `json:"a"`
	}
	type renamedJSONKey struct {
		A string `json:"a"`
		B int    `json:"renamed_b"`
	}
	type retypedField struct {
		A string `json:"a"`
		B string `json:"b"`
	}
	type tagOptionAdded struct {
		A string `json:"a"`
		B int    `json:"b,string"`
	}

	baseline := fingerprintOf[base]()
	cases := map[string][]string{
		"added field":      fingerprintOf[addedField](),
		"removed field":    fingerprintOf[removedField](),
		"renamed json key": fingerprintOf[renamedJSONKey](),
		"retyped field":    fingerprintOf[retypedField](),
		"tag option added": fingerprintOf[tagOptionAdded](),
	}
	for name, got := range cases {
		if slices.Equal(got, baseline) {
			t.Errorf("%s: schemaFields unchanged from the baseline, want a different field set\n  baseline: %v\n  got:      %v", name, baseline, got)
		}
	}

	// Two independently-reflected copies of the identical shape must match:
	// the walk is deterministic, not e.g. order-sensitive on map iteration.
	if got, want := fingerprintOf[base](), baseline; !slices.Equal(got, want) {
		t.Errorf("schemaFields(base) not stable across calls\n  first:  %v\n  second: %v", want, got)
	}
}

// TestSchemaFieldsDistinguishesEmbeddedFromNamed isolates exactly the
// anonymous bit schemaFields folds in: EmbeddedInner (embedded) and
// EmbeddedInner EmbeddedInner (a named field whose Go field name happens to
// equal its type's exported name, so it resolves to the identical json name,
// tag, and Type.String() as the embedded case, with no tag on either side)
// differ only in whether the field is anonymous. Both fields must be
// exported for this to isolate only that bit: an unexported field skips the
// unexported-and-not-anonymous branch entirely regardless of promotion,
// which would make the two cases differ for an unrelated reason.
// encoding/json decodes the exported pair differently regardless (an
// embedded field's own fields promote to this level; a named field's nest
// under its key), so schemaFields must tell them apart — a control
// comparison that also happened to differ in path, tag, json name, or
// export status (as earlier versions of this test did) could pass even if
// the anonymous bit were dropped entirely.
func TestSchemaFieldsDistinguishesEmbeddedFromNamed(t *testing.T) {
	type EmbeddedInner struct {
		X int `json:"x"`
	}
	type withEmbedded struct {
		EmbeddedInner
	}
	type withNamedOfSameName struct {
		EmbeddedInner EmbeddedInner
	}
	if slices.Equal(fingerprintOf[withEmbedded](), fingerprintOf[withNamedOfSameName]()) {
		t.Errorf("schemaFields did not distinguish an embedded field from a named field of the identical name, type, and tag")
	}
}

// TestSchemaFieldsRecordsPromotedChildrenAtTheParentLevel requires an
// embedded struct field's own children to be recorded at the embedding
// field's parent level (matching where encoding/json actually places them
// in the JSON object), not nested under the embedding field's own name —
// while a field of the identical type that is merely named the same but not
// embedded (encoding/json disables promotion for any anonymous field that
// carries an explicit json name) keeps its children nested, since it isn't
// promoted. A fingerprint that nested a promoted field's children would
// force a rebuild for an embedding-only refactor with an identical wire
// shape (roborev finding).
func TestSchemaFieldsRecordsPromotedChildrenAtTheParentLevel(t *testing.T) {
	type EmbeddedInner struct {
		X int `json:"x"`
	}
	type withEmbedded struct {
		EmbeddedInner
	}
	type withRenamedEmbedded struct {
		EmbeddedInner `json:"named"`
	}
	promoted := fingerprintOf[withEmbedded]()
	if !slices.ContainsFunc(promoted, func(s string) bool { return strings.HasPrefix(s, ".x:") }) {
		t.Errorf("schemaFields(withEmbedded) = %v, want a promoted child at \".x\" (the parent level), not nested under \".EmbeddedInner.x\"", promoted)
	}
	renamed := fingerprintOf[withRenamedEmbedded]()
	if !slices.ContainsFunc(renamed, func(s string) bool { return strings.HasPrefix(s, ".named.x:") }) {
		t.Errorf("schemaFields(withRenamedEmbedded) = %v, want the child nested under \".named.x\": a json tag renaming an embedded field turns off promotion", renamed)
	}
}

// TestSchemaFieldsRecordsKindAlongsideTypeString guards a narrower case than
// the shape-change test above: reflect.Type.String() names a defined type by
// its declared name alone ("pkg.ID"), the same before and after a
// hypothetical future edit that keeps the name but changes the underlying
// kind (type ID string -> type ID int) — a real, if rare, retyping that
// Type.String() alone cannot distinguish (two versions of the same type
// can't coexist in one build to compare directly, so this checks the
// descriptor's shape instead of a before/after diff).
func TestSchemaFieldsRecordsKindAlongsideTypeString(t *testing.T) {
	type withString struct {
		V string `json:"v"`
	}
	fields := fingerprintOf[withString]()
	want := ".v:string:string:v:anon=false"
	if !slices.Contains(fields, want) {
		t.Errorf("schemaFields(withString) = %v, want it to contain %q (the field's kind recorded alongside its Type.String())", fields, want)
	}
}

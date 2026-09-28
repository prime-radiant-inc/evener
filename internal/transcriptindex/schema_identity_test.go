package transcriptindex

import (
	"reflect"
	"slices"
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
	type embeddedInner struct {
		X int `json:"x"`
	}
	type withEmbedded struct {
		embeddedInner
	}
	type withNamedOfSameType struct {
		Inner embeddedInner `json:"inner"`
	}

	baseline := fingerprintOf[base]()
	cases := map[string][]string{
		"added field":               fingerprintOf[addedField](),
		"removed field":             fingerprintOf[removedField](),
		"renamed json key":          fingerprintOf[renamedJSONKey](),
		"retyped field":             fingerprintOf[retypedField](),
		"tag option added":          fingerprintOf[tagOptionAdded](),
		"embedded vs named of type": fingerprintOf[withNamedOfSameType](),
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

	// An embedded field's fingerprint differs from a same-named, same-typed
	// but embedded field elsewhere too (withEmbedded vs withNamedOfSameType),
	// confirming the anonymous bit is actually load-bearing and not just
	// present-but-ignored.
	if slices.Equal(fingerprintOf[withEmbedded](), fingerprintOf[withNamedOfSameType]()) {
		t.Errorf("schemaFields did not distinguish an embedded field from a named field of the identical type")
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

package llm

import (
	"reflect"
	"testing"
)

func TestMergeHeaders(t *testing.T) {
	if got := MergeHeaders(nil, nil); got != nil {
		t.Errorf("MergeHeaders(nil,nil) = %v, want nil", got)
	}
	// Override wins on collision; base survives when not overridden.
	got := MergeHeaders(
		map[string]string{"User-Agent": "evener-default", "X-Base": "b"},
		map[string]string{"User-Agent": "user-set"},
	)
	want := map[string]string{"User-Agent": "user-set", "X-Base": "b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("MergeHeaders = %#v, want %#v", got, want)
	}
}

// TestMergeHeaders_CaseInsensitiveCollision guards against the base and
// override maps disagreeing only on header-name case (e.g. "User-Agent" vs
// "user-agent") producing two coexisting map keys: HTTP header names are
// case-insensitive, so that would leave the winner at request time to depend
// on nondeterministic map iteration order. Keys must canonicalize to one
// entry with the override's value.
func TestMergeHeaders_CaseInsensitiveCollision(t *testing.T) {
	got := MergeHeaders(
		map[string]string{"User-Agent": "base-value"},
		map[string]string{"user-agent": "override-value"},
	)
	want := map[string]string{"User-Agent": "override-value"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("MergeHeaders = %#v, want %#v (single canonical key, override wins)", got, want)
	}
}

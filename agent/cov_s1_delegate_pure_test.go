package agent

import (
	"reflect"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

func TestS1Cov_delegateResultSchemaMap(t *testing.T) {
	if got := delegateResultSchemaMap(nil); got != nil {
		t.Fatalf("nil schema → %v, want nil", got)
	}
	if got := delegateResultSchemaMap(map[string]any{}); got != nil {
		t.Fatalf("empty map → %v, want nil", got)
	}
	// Non-empty map is cloned (distinct backing map, equal contents).
	src := map[string]any{"type": "object"}
	got := delegateResultSchemaMap(src)
	if !reflect.DeepEqual(got, src) {
		t.Fatalf("map clone = %v, want %v", got, src)
	}
	got["type"] = "mutated"
	if src["type"] != "object" {
		t.Fatal("clone must not alias the source map")
	}
	// A struct marshals then decodes into a map.
	if got := delegateResultSchemaMap(struct {
		Type string `json:"type"`
	}{Type: "object"}); got["type"] != "object" {
		t.Fatalf("struct schema → %v, want type=object", got)
	}
	// A value that marshals to an empty object decodes to nil.
	if got := delegateResultSchemaMap(struct{}{}); got != nil {
		t.Fatalf("empty-struct schema → %v, want nil", got)
	}
	// A value json cannot marshal → nil.
	if got := delegateResultSchemaMap(make(chan int)); got != nil {
		t.Fatalf("unmarshalable schema → %v, want nil", got)
	}
	// A value that marshals to a non-object → unmarshal into map fails → nil.
	if got := delegateResultSchemaMap("scalar"); got != nil {
		t.Fatalf("scalar schema → %v, want nil", got)
	}
}

func TestS1Cov_cloneDelegateResultSchema(t *testing.T) {
	if got := cloneDelegateResultSchema(nil); got != nil {
		t.Fatalf("nil → %v, want nil", got)
	}
	if got := cloneDelegateResultSchema(map[string]any{}); got != nil {
		t.Fatalf("empty map → %v, want nil", got)
	}
	// Unmarshalable value falls back to the original schema.
	ch := make(chan int)
	if got := cloneDelegateResultSchema(ch); got == nil {
		t.Fatal("unmarshalable schema must fall back to the original, not nil")
	}
	// A struct round-trips through JSON into a map.
	got := cloneDelegateResultSchema(struct {
		Type string `json:"type"`
	}{Type: "object"})
	m, ok := got.(map[string]any)
	if !ok || m["type"] != "object" {
		t.Fatalf("struct clone = %#v, want map with type=object", got)
	}
	// A value that marshals to an empty object → nil.
	if got := cloneDelegateResultSchema(struct{}{}); got != nil {
		t.Fatalf("empty-struct → %v, want nil", got)
	}
}

func TestS1Cov_subagentStatusFromJobStatus(t *testing.T) {
	cases := map[jobstore.Status]SubagentStatus{
		jobstore.StatusCompleted: SubagentCompleted,
		jobstore.StatusCancelled: SubagentCancelled,
		jobstore.StatusFailed:    SubagentFailed,
		jobstore.StatusRunning:   SubagentFailed, // default arm
	}
	for status, want := range cases {
		if got := subagentStatusFromJobStatus(status); got != want {
			t.Fatalf("subagentStatusFromJobStatus(%q) = %q, want %q", status, got, want)
		}
	}
}

package registry

import (
	"reflect"
	"testing"
)

func TestOrderedGlobKeys_TargetBeforeReferenceOnce(t *testing.T) {
	rows := map[string]Model{
		"claude-*":     {ID: "claude-*"},
		"*-opus-4-6":   {ID: "*-opus-4-6"},
		"claude-opus*": {ID: "claude-opus*"},
		"exact":        {ID: "exact"},
	}
	got := orderedGlobKeys(rows, "claude-opus-4-6", "claude-opus-4-6-base")
	// Shorter patterns first; every pattern at most once even when it
	// matches both ids. *-opus-4-6 matches only the reference, so it
	// trails the two target matches.
	want := []string{"claude-*", "claude-opus*", "*-opus-4-6"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("orderedGlobKeys = %q, want %q", got, want)
	}
	if got := orderedGlobKeys(rows, "claude-opus-4-6", ""); !reflect.DeepEqual(got, []string{"claude-*", "*-opus-4-6", "claude-opus*"}) {
		t.Fatalf("no altID must still order ref matches: %q", got)
	}
	if got := orderedGlobKeys(map[string]Model{"exact": {ID: "exact"}}, "exact", ""); len(got) != 0 {
		t.Fatalf("exact rows are never glob keys: %q", got)
	}
}

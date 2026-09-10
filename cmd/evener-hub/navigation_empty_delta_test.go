package hub

import (
	"encoding/json"
	"fmt"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/hubapi"
)

func TestNavigationDeltaEmptyPinCatalog(t *testing.T) {
	key := navigationResourceKey{Kind: navigationResourcePinCatalog, Limit: 50}
	version := func(revision uint64) appwire.NavigationReadBase {
		return appwire.NavigationReadBase{GenerationID: "g", Revision: revision, ETag: fmt.Sprintf("catalog-%d", revision)}
	}
	catalog := func(revision uint64, populated bool) hubapi.NavigationSnapshot {
		t.Helper()
		value := hubapi.NavigationPinSectionCatalog{GenerationID: "g", Revision: revision}
		if populated {
			value.PinSections = hubapi.NavigationArray[hubapi.NavigationPinSectionDescriptor]{{ID: "focus", Name: "Focus", Count: 1}}
		}
		snapshot, err := normalizeNavigationResource(key, value)
		if err != nil {
			t.Fatal(err)
		}
		if !populated && snapshot.Entities == nil {
			t.Fatal("empty catalog normalized to nil entities")
		}
		return snapshot
	}
	for _, tc := range []struct {
		name      string
		populated bool
	}{
		{"remove final section", true}, {"metadata-only empty catalog", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, current := catalog(1, tc.populated), catalog(2, false)
			delta, err := diffNavigationSnapshots(key, version(1), version(2), base, current)
			if err != nil {
				t.Fatal(err)
			}
			applied, err := applyNavigationDelta(base, delta)
			if err != nil {
				t.Fatal(err)
			}
			if applied.Entities == nil || len(applied.Entities) != 0 || len(applied.Containers) != 1 {
				t.Fatalf("empty catalog has %d entities and %d containers", len(applied.Entities), len(applied.Containers))
			}
			root := applied.Containers[0]
			if root.Owner.Kind != "resource_root" || root.Owner.Slot != "pin_sections" || len(root.Children) != 0 {
				t.Fatalf("empty catalog root = %+v", root)
			}
			var metadata navigationPagedMetadata
			if err := json.Unmarshal(applied.Metadata, &metadata); err != nil {
				t.Fatal(err)
			}
			if metadata.Revision != 2 {
				t.Fatalf("metadata revision = %d, want 2", metadata.Revision)
			}
		})
	}
}

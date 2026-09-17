package tui

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// The rule is appwire.MergeOutputImages's, with its own table there. What this
// package owes is that its page merge calls it: an empty list from the newer
// item is the hub saying the images are gone and must win, while a list the
// newer item says nothing about leaves the older page's own standing. Nothing
// covered this site before — reverting it to a length check left this suite
// green, which is why the test is here.
func TestMergeHubItemCallsTheSharedOutputImageRule(t *testing.T) {
	older := appwire.ThreadItem{
		ID:           "item_1",
		OutputImages: []appwire.OutputImage{{Name: "shot.png", SHA: "abc"}},
	}

	removed := mergeHubItem(older, appwire.ThreadItem{ID: "item_1", OutputImages: []appwire.OutputImage{}})
	if removed.OutputImages == nil || len(removed.OutputImages) != 0 {
		t.Fatalf("merged OutputImages=%+v, want the newer item's explicit empty list", removed.OutputImages)
	}

	unsaid := mergeHubItem(older, appwire.ThreadItem{ID: "item_1"})
	if len(unsaid.OutputImages) != 1 {
		t.Fatalf("merged OutputImages=%+v, want the older page's own kept", unsaid.OutputImages)
	}
}

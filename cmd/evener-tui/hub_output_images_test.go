package tui

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// The rule is appwire.MergeOutputImages's, with its own table there. What this
// package owes is that its page merge calls it: an empty list from the newer
// item is the hub saying the images are gone and must win, while a list the
// newer item says nothing about leaves the older page's own standing.
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

// Output images: see appwire.MergeOutputImages; input images: see appwire.MergeInputImages.
func TestMergeHubItemKeepsOlderInputImagesForAnEmptyList(t *testing.T) {
	older := appwire.ThreadItem{
		ID:     "item_1",
		Images: []appwire.InputItem{{Type: "image", MediaType: "image/png", Data: []byte("hi"), Name: "shot.png"}},
	}

	empty := mergeHubItem(older, appwire.ThreadItem{ID: "item_1", Images: []appwire.InputItem{}})
	if len(empty.Images) != 1 {
		t.Fatalf("merged Images=%+v for an empty incoming list, want the older item's own kept", empty.Images)
	}

	absent := mergeHubItem(older, appwire.ThreadItem{ID: "item_1"})
	if len(absent.Images) != 1 {
		t.Fatalf("merged Images=%+v for an absent list, want the older item's own kept", absent.Images)
	}

	replacement := []appwire.InputItem{{Type: "image", MediaType: "image/png", Data: []byte("bye"), Name: "other.png"}}
	newer := mergeHubItem(older, appwire.ThreadItem{ID: "item_1", Images: replacement})
	if len(newer.Images) != 1 || newer.Images[0].Name != "other.png" {
		t.Fatalf("merged Images=%+v, want the newer item's own images", newer.Images)
	}
}

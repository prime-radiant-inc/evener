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

// Input images follow the length rule, the same as everywhere else on this wire:
// nothing removes an item's input images (they are what the user sent), so an
// empty list and an absent one both mean "this page said nothing about images"
// and neither may erase what the older page holds. The hub's own merges read it
// that way (server/appwire_turns.go, internal/apptranscript/logical_turn.go), and
// so does the TypeScript reducer; a nil-only check here made an explicit
// images: [] erase the older item's images in the TUI alone.
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

package apptranscript

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// This merge mirrors server mergeAppThreadItem's field precedence (its own doc
// comment says so), and that one distinguishes an empty OutputImages list — the
// hub saying the pictures are gone — from no list at all. A length check reads
// the removal as "the later item said nothing" and puts the earlier item's
// images back, which is the one thing a removal must survive.
func TestMergeKeepsAnExplicitEmptyOutputImageList(t *testing.T) {
	existing := appwire.ThreadItem{
		ID:           "item_1",
		OutputImages: []appwire.OutputImage{{Name: "shot.png", SHA: "abc"}},
	}
	incoming := appwire.ThreadItem{ID: "item_1", OutputImages: []appwire.OutputImage{}}

	merged := mergeAppThreadItems(existing, incoming)
	if merged.OutputImages == nil || len(merged.OutputImages) != 0 {
		t.Fatalf("merged OutputImages=%+v, want the later item's explicit empty list", merged.OutputImages)
	}
}

// Absent still falls back, which is what the length check was reaching for.
func TestMergeKeepsExistingOutputImagesWhenTheLaterItemCarriesNone(t *testing.T) {
	existing := appwire.ThreadItem{
		ID:           "item_1",
		OutputImages: []appwire.OutputImage{{Name: "shot.png", SHA: "abc"}},
	}

	merged := mergeAppThreadItems(existing, appwire.ThreadItem{ID: "item_1"})
	if len(merged.OutputImages) != 1 {
		t.Fatalf("merged OutputImages=%+v, want the earlier item's own kept", merged.OutputImages)
	}
}

// Input images keep the length rule: nothing removes an item's input images
// (they are what the user sent), the field is omitempty, so an empty list and
// an absent one are the same thing on the wire and the merge says so once.
func TestMergeFallsBackForAnEmptyInputImageList(t *testing.T) {
	existing := appwire.ThreadItem{
		ID:     "item_1",
		Images: []appwire.InputItem{{Type: "image", Name: "in.png"}},
	}

	merged := mergeAppThreadItems(existing, appwire.ThreadItem{ID: "item_1", Images: []appwire.InputItem{}})
	if len(merged.Images) != 1 {
		t.Fatalf("merged Images=%+v, want the earlier item's own kept", merged.Images)
	}
}

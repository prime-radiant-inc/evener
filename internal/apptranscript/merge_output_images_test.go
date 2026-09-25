package apptranscript

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// Output images: see appwire.MergeOutputImages; input images: see appwire.MergeInputImages.
func TestMergeCallsTheSharedOutputImageRule(t *testing.T) {
	existing := appwire.ThreadItem{
		ID:           "item_1",
		OutputImages: []appwire.OutputImage{{Name: "shot.png", SHA: "abc"}},
	}

	removed := MergeThreadItems(existing, appwire.ThreadItem{ID: "item_1", OutputImages: []appwire.OutputImage{}})
	if removed.OutputImages == nil || len(removed.OutputImages) != 0 {
		t.Fatalf("merged OutputImages=%+v, want the later item's explicit empty list", removed.OutputImages)
	}

	unsaid := MergeThreadItems(existing, appwire.ThreadItem{ID: "item_1"})
	if len(unsaid.OutputImages) != 1 {
		t.Fatalf("merged OutputImages=%+v, want the earlier item's own kept", unsaid.OutputImages)
	}

	// Input images: see appwire.MergeInputImages (omitempty, nothing removes them).
	withInput := appwire.ThreadItem{ID: "item_1", Images: []appwire.InputItem{{Type: "image", Name: "in.png"}}}
	merged := MergeThreadItems(withInput, appwire.ThreadItem{ID: "item_1", Images: []appwire.InputItem{}})
	if len(merged.Images) != 1 {
		t.Fatalf("merged Images=%+v, want the earlier item's own kept", merged.Images)
	}
}

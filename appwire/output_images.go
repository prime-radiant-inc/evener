package appwire

import "slices"

// ThreadItem.OutputImages says three different things, and every copy, merge
// and default of the list has to keep them apart:
//
//   - nil: this item never had output images. It sends no key (the field is
//     omitzero), so a client is told nothing about images.
//   - non-nil and empty: the images are GONE. This is the hub's only way to say
//     so, and the frame carries "outputImages": [].
//   - non-empty: these are the images.
//
// The two output-image helpers below are the whole rule.
//
// Input images need none of this: nothing removes them (they are what the user
// sent) and Images is omitempty, so an empty list and an absent one encode
// identically. MergeInputImages owns that simpler rule so the readers share one
// check instead of each spelling out the length test.

// CloneOutputImages copies an item's output-image list, keeping nil nil and an
// empty list empty. slices.Clone is exactly this contract, pinned by
// TestCloneOutputImages here and by TestCloneThreadKeepsAnExplicitEmptyOutputImageList
// and TestCloneThreadLeavesAnAbsentOutputImageListAbsent in clone_output_images_test.go.
func CloneOutputImages(in []OutputImage) []OutputImage {
	return slices.Clone(in)
}

// MergeOutputImages folds a later item's output-image list onto an earlier one.
// A nil incoming list is the later item saying nothing about images, so the
// earlier list stands; any non-nil list wins, INCLUDING an empty one, because
// that is the removal.
func MergeOutputImages(existing, incoming []OutputImage) []OutputImage {
	if incoming == nil {
		return existing
	}
	return incoming
}

// MergeInputImages folds a later item's input-image list onto an earlier one.
// Images is omitempty and nothing removes input images, so an empty incoming
// list says nothing about them and the earlier list stands; a non-empty list
// wins. This is deliberately not MergeOutputImages' rule: output images have
// an explicit empty-list removal, input images do not.
func MergeInputImages(existing, incoming []InputItem) []InputItem {
	if len(incoming) == 0 {
		return existing
	}
	return incoming
}

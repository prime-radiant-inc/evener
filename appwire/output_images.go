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
// The two helpers below are the whole rule.
//
// Input images need none of this: nothing removes them (they are what the user
// sent) and Images is omitempty, so an empty list and an absent one encode
// identically. Those sites keep their length checks, deliberately.

// CloneOutputImages copies an item's output-image list, keeping nil nil and an
// empty list empty. slices.Clone is exactly this contract, pinned by
// TestSlicesCloneCarriesOutputImageListNilness: it answers nil for nil and a
// fresh non-nil empty slice for a non-nil empty one.
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

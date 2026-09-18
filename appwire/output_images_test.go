package appwire

import (
	"reflect"
	"slices"
	"testing"
)

// The one table for the rule every site now shares.
func TestMergeOutputImages(t *testing.T) {
	images := []OutputImage{{Name: "shot.png", SHA: "abc"}}
	replacement := []OutputImage{{Name: "other.png", SHA: "def"}}

	for _, tc := range []struct {
		name     string
		existing []OutputImage
		incoming []OutputImage
		want     []OutputImage
	}{
		{name: "an unsaid list keeps the existing images", existing: images, incoming: nil, want: images},
		{name: "an empty list removes them", existing: images, incoming: []OutputImage{}, want: []OutputImage{}},
		{name: "a fresh list replaces them", existing: images, incoming: replacement, want: replacement},
		{name: "an empty list stands on an item that had none", existing: nil, incoming: []OutputImage{}, want: []OutputImage{}},
		{name: "nothing said about an item that had none stays nothing", existing: nil, incoming: nil, want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := MergeOutputImages(tc.existing, tc.incoming)
			if (got == nil) != (tc.want == nil) {
				t.Fatalf("merged = %+v, want %+v", got, tc.want)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("merged = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// The clone half of the rule, and that it really copies rather than aliasing.
func TestCloneOutputImages(t *testing.T) {
	if cloned := CloneOutputImages(nil); cloned != nil {
		t.Fatalf("cloned nil = %+v, want nil: an item that never had images announces no removal", cloned)
	}
	empty := CloneOutputImages([]OutputImage{})
	if empty == nil || len(empty) != 0 {
		t.Fatalf("cloned empty = %+v, want a non-nil empty list: the removal must survive the copy", empty)
	}
	original := []OutputImage{{Name: "shot.png", SHA: "abc"}}
	cloned := CloneOutputImages(original)
	cloned[0].Name = "other.png"
	if original[0].Name != "shot.png" {
		t.Fatal("the clone shares the caller's array")
	}
}

// The input-image rule: Images is omitempty and nothing removes input images,
// so an empty (or absent) incoming list says nothing and the existing list
// stands; only a non-empty list replaces it.
func TestMergeInputImages(t *testing.T) {
	images := []InputItem{{Type: "image", Name: "in.png"}}
	replacement := []InputItem{{Type: "image", Name: "other.png"}}

	for _, tc := range []struct {
		name     string
		existing []InputItem
		incoming []InputItem
		want     []InputItem
	}{
		{name: "an absent list keeps the existing images", existing: images, incoming: nil, want: images},
		{name: "an empty list says nothing and keeps them too", existing: images, incoming: []InputItem{}, want: images},
		{name: "a fresh list replaces them", existing: images, incoming: replacement, want: replacement},
		{name: "an empty list on an item that had none stays nothing", existing: nil, incoming: []InputItem{}, want: nil},
		{name: "a fresh list on an item that had none wins", existing: nil, incoming: replacement, want: replacement},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := MergeInputImages(tc.existing, tc.incoming)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("merged = %+v, want %+v", got, tc.want)
			}
		})
	}
}

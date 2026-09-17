package appwire

import (
	"testing"
)

// An empty OutputImages list is the hub saying "the pictures are gone" (see
// ThreadItem.OutputImages), so every copy of a thread has to carry the
// difference between an empty list and no list. CloneThread is on the read
// path -- the hub clones the snapshot it answers with.
func TestCloneThreadKeepsAnExplicitEmptyOutputImageList(t *testing.T) {
	cloned := CloneThread(Thread{
		Turns: []Turn{{
			ID: "turn_1",
			Items: []ThreadItem{{
				ID:           "item_1",
				OutputImages: []OutputImage{},
			}},
		}},
	})

	item := cloned.Turns[0].Items[0]
	if item.OutputImages == nil {
		t.Fatal("cloned OutputImages is nil, so the removal this item announces is lost")
	}
	if len(item.OutputImages) != 0 {
		t.Fatalf("cloned OutputImages=%+v, want the empty list it was given", item.OutputImages)
	}
}

// Absent stays absent: an item that never had output images must not come back
// from a clone carrying an empty list, which would announce a removal that
// never happened.
func TestCloneThreadLeavesAnAbsentOutputImageListAbsent(t *testing.T) {
	cloned := CloneThread(Thread{
		Turns: []Turn{{ID: "turn_1", Items: []ThreadItem{{ID: "item_1"}}}},
	})

	if images := cloned.Turns[0].Items[0].OutputImages; images != nil {
		t.Fatalf("cloned OutputImages=%+v for an item that never had images, want nil", images)
	}
}

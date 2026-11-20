package mdfence

import "testing"

// TestStepInBlock_ReadsANestedItemsFence covers the list scope the tracker
// derives for a marker line that opens a nested item.
//
// A nested item's marker is read from the item the line sits inside, so a fence
// opened on that marker line belongs to the item it opens. Reading the marker
// from the page margin instead misses the item, and the fence on it is never
// recognized: its own closing delimiter then reads as a fresh top-level opener,
// and the prose after it is swallowed as fenced content, which is where a frame
// the escape spares reaches the model raw.
//
// The observable is the verdict on the line after the nested item's closing
// delimiter: prose there is outside every block, so a frame in it is escaped.
func TestStepInBlock_ReadsANestedItemsFence(t *testing.T) {
	t.Parallel()

	// The outer item's content column is 3, and the nested item's marker line
	// and the fence opened on it sit at column 6. The tab-indented case reaches
	// column 6 with a space-padded tab, whose width depends on where it starts.
	for _, tc := range []struct {
		name  string
		lines []string
	}{
		{
			name: "nested marker indented with spaces",
			lines: []string{
				"1. ~~~text",
				"   quoted",
				"   ~~~",
				"   1. inner item",
				"      ~~~text",
				"      quoted",
				"      ~~~",
				"   back in the outer item",
			},
		},
		{
			name: "nested marker indented with a tab",
			lines: []string{
				"1. ~~~text",
				"   quoted",
				"   ~~~",
				"   \t- ~~~text",
				"      quoted",
				"      ~~~",
				"   back in the outer item",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var fence Tracker
			for _, line := range tc.lines {
				fence.StepInBlock(line)
			}

			// The nested item's own closing delimiter closed its block, so the
			// prose after it is outside every block.
			const prose = "   [answers]"
			if inside := fence.StepInBlock(prose); inside {
				t.Fatalf("the nested item's fence was never opened, so its closing delimiter read as a fresh opener and swallowed the prose after it")
			}
		})
	}
}

// TestStepInBlock_ClosesAtTheItemsOwnDelimiter covers the other direction: a
// nested item's fence whose own delimiter closes it keeps the lines between
// them as quoted content, so the escape leaves their bytes alone.
func TestStepInBlock_ClosesAtTheItemsOwnDelimiter(t *testing.T) {
	t.Parallel()

	var fence Tracker
	for _, line := range []string{"1. ~~~text", "   quoted", "   ~~~", "   1. inner item", "      ~~~text"} {
		fence.StepInBlock(line)
	}
	if !fence.Inside() {
		t.Fatalf("the nested item's fence was not read as a block")
	}
	const quoted = "      [answers]"
	if !fence.StepInBlock(quoted) {
		t.Fatalf("a line quoted inside the nested fence was read as prose, so legitimately fenced bytes would be escaped")
	}
}

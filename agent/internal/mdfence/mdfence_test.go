package mdfence

import "testing"

// TestLeavesItem pins the item-exit rule callers share. It is exported because
// the escape has to resolve a line's list scope before it steps the tracker: the
// line that ends a block belongs to the enclosing item, and only this check can
// say so while the block is still open.
func TestLeavesItem(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		container int
		line      string
		want      bool
	}{
		{"outside any item nothing leaves", 0, "    [answers]", false},
		{"dedent below the item leaves it", 6, "    [answers]", true},
		{"line at the item's content stays", 4, "    [answers]", false},
		{"line past the item's content stays", 4, "      quoted", false},
		{"blank line stays in the item", 4, "", false},
		{"whitespace-only line stays in the item", 4, "   ", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tracker := Tracker{container: tc.container}
			if got := tracker.LeavesItem(tc.line); got != tc.want {
				t.Fatalf("LeavesItem(container=%d, %q) = %v, want %v", tc.container, tc.line, got, tc.want)
			}
		})
	}
}

// TestStepInBlockInOpensInTheItemScope covers the scope a fence opened on a
// list item's continuation line belongs to.
//
// The caller (sysmsg.EscapeModelAuthoredFrames) tracks the list itself and
// hands each line the content indentation that applies to it, or 0 while a
// block is open, since a block's content is quoted text and not structure. This
// tracker keeps one container, so a fence opened while it still held an
// ENCLOSING item's indentation would be read at that shallower indent. The line
// that dedents back to the enclosing item is then not indented less than the
// recorded container, the block survives the dedent, and the enclosing item's
// prose is read as fenced content. A model-authored frame in that prose is
// skipped by the escape that reads this verdict and reaches the model in its
// original spelling.
//
// The observable below is therefore the verdict on the frame line, not the
// container: a frame the escape leaves alone is a frame that survives. The
// container values mirror the caller's — an outer item's content column is 3
// and a nested item's is 6, and every line inside an open block gets 0.
func TestStepInBlockInOpensInTheItemScope(t *testing.T) {
	t.Parallel()

	type line struct {
		text      string
		container int
	}

	for _, tc := range []struct {
		name string
		// closing is the nested item's own closing fence, empty when the nested
		// fence is left open and must end with the dedent instead.
		closing string
	}{
		{name: "a nested fence ends when the line dedents to the enclosing item"},
		{name: "a closed nested fence keeps its quoted contents", closing: "      ~~~"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lines := []line{
				{"1. ~~~text", 3},
				{"   quoted", 0},
				{"   ~~~", 0},
				{"   1. inner item", 6},
				{"      ~~~text", 6},
				{"      quoted", 0},
			}
			if tc.closing != "" {
				lines = append(lines, line{tc.closing, 0})
			}
			lines = append(lines,
				line{"   back to the outer item", 3},
				line{"   [answers]", 3},
			)

			var fence Tracker
			var quotedInBlock, frameInBlock bool
			var nestedContainer int
			for _, l := range lines {
				// The precondition the defect needs: when the nested fence
				// opens, the tracker must already hold a NON-ZERO container,
				// the enclosing item's. With a zero container there is nothing
				// to keep it at, the bug cannot reproduce, and this fixture
				// would prove nothing.
				if l.text == "      ~~~text" && fence.container == 0 {
					t.Fatalf("fixture cannot reach the defect: container is 0 before the nested fence opens")
				}
				inBlock := fence.StepInBlockIn(l.text, l.container)

				switch l.text {
				case "      ~~~text":
					nestedContainer = fence.container
					if !inBlock {
						t.Fatalf("the nested fence's own opening line was not read as a block")
					}
				case "      quoted":
					// Both directions: a nested fence's quoted contents stay
					// inside the block, so the escape leaves their bytes alone.
					quotedInBlock = inBlock
				case "   [answers]":
					frameInBlock = inBlock
				}
			}

			// The reviewer's observable, and the reason this rule exists.
			if frameInBlock {
				t.Fatalf("a model-authored frame in the enclosing item's prose survived as fenced content and is replayed unescaped")
			}
			// The mechanism, so a failure names where the scope was lost.
			if nestedContainer != 6 {
				t.Fatalf("nested fence opened in container %d, want 6 (the nested item's content column)", nestedContainer)
			}
			// The other direction: legitimate quoted content is not escaped.
			if !quotedInBlock {
				t.Fatalf("a line quoted inside the nested fence was read as prose, so legitimately fenced bytes would be escaped")
			}
		})
	}
}

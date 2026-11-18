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
		// enclosing is the item that contains this line's item, which the
		// caller resolves after the line's dedent and before its own item
		// opens. It is the scope a fence opened on the line belongs to.
		enclosing int
	}

	for _, tc := range []struct {
		name string
		// closing is the nested item's own closing fence, empty when the nested
		// fence is left open and must end with the dedent instead.
		closing string
		// lines overrides the sequence above, for a shape whose scope is carried
		// differently: a fence opened on a nested item's MARKER line rather than
		// on its continuation line, where the line's own indentation is past the
		// enclosing item's content and the marker has to be read from there.
		lines []line
		// openText and frameText are the lines whose verdicts this case reads,
		// so a case is never satisfied by observables it does not drive.
		openText  string
		frameText string
	}{
		{
			name:      "a nested fence ends when the line dedents to the enclosing item",
			openText:  "      ~~~text",
			frameText: "   [answers]",
		},
		{
			name:      "a closed nested fence keeps its quoted contents",
			closing:   "      ~~~",
			openText:  "      ~~~text",
			frameText: "   [answers]",
		},
		{
			name:      "a fence on a nested marker line closes on its own delimiter",
			openText:  "    - ~~~text",
			frameText: "      [answers]",
			lines: []line{
				{text: "- outer", container: 2},
				{text: "    - ~~~text", container: 6, enclosing: 2},
				{text: "      quoted"},
				{text: "      ~~~"},
				{text: "      [answers]", container: 6},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			lines := tc.lines
			if lines == nil {
				lines = []line{
					{text: "1. ~~~text", container: 3},
					{text: "   quoted"},
					{text: "   ~~~"},
					{text: "   1. inner item", container: 6},
					{text: "      ~~~text", container: 6},
					{text: "      quoted"},
				}
			}
			if tc.closing != "" {
				lines = append(lines, line{text: tc.closing})
			}
			if tc.lines == nil {
				lines = append(lines,
					line{text: "   back to the outer item", container: 3},
					line{text: "   [answers]", container: 3},
				)
			}

			var fence Tracker
			var quotedInBlock, frameInBlock bool
			var nestedContainer int
			var opened bool
			for _, l := range lines {
				// The precondition the defect needs: when the nested fence
				// opens, the tracker must already hold a NON-ZERO container,
				// the enclosing item's. With a zero container there is nothing
				// to keep it at, the bug cannot reproduce, and this fixture
				// would prove nothing.
				if l.text == tc.openText && fence.container == 0 {
					t.Fatalf("fixture cannot reach the defect: container is 0 before the nested fence opens")
				}
				inBlock := fence.StepInBlockIn(l.text, l.container, l.enclosing)

				// Tagged on l.text: every case is exactly one comparison
				// against it, so the rewrite keeps the same order and the same
				// first-match behaviour. Unlike a C switch there is no
				// fallthrough either way, so no branch changes meaning.
				switch l.text {
				case tc.openText:
					opened = true
					nestedContainer = fence.container
					if !inBlock {
						t.Fatalf("the nested fence's own opening line was not read as a block")
					}
				case "      quoted":
					// Both directions: a nested fence's quoted contents stay
					// inside the block, so the escape leaves their bytes alone.
					quotedInBlock = inBlock
				case tc.frameText:
					frameInBlock = inBlock
				}
			}

			// A case whose frame line never ran cannot report on it.
			if !opened {
				t.Fatalf("fixture never stepped the opening line %q", tc.openText)
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

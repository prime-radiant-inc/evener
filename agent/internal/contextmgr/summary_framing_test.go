package contextmgr

import (
	"strings"
	"testing"
)

// A line of bare markers is framing, not content: counting it as content would
// store an empty summary as the only record of the folded history.
func TestSummarizeWithLLM_RejectsALineOfBareMarkers(t *testing.T) {
	reply := "## Progress\n" + summaryMarkerEnd + summaryMarkerStart
	if result, err := summarizeReplying(t, reply, ""); err == nil {
		t.Fatalf("summarizeWithLLM stored %q as a summary, want an error", result[0].Message.Text())
	}
}

// A model sometimes echoes the framing around its own heading. The check reads
// through a run of markers at the line's start, whichever markers they are, so
// the heading still counts and the content under it stands.
func TestSummarizeWithLLM_ReadsAHeadingBehindAnEchoedMarker(t *testing.T) {
	for name, reply := range map[string]string{
		"one marker":        summaryMarkerStart + " ## Progress\nFixed the auth bug.",
		"repeated marker":   summaryMarkerStart + summaryMarkerStart + " ## Progress\nFixed the auth bug.",
		"alternating ends":  summaryMarkerEnd + summaryMarkerStart + " ## Progress\nFixed the auth bug.",
		"marker at the end": summaryMarkerStart + " ## Progress " + summaryMarkerEnd + "\nFixed the auth bug.",
	} {
		t.Run(name, func(t *testing.T) {
			result, err := summarizeReplying(t, reply, "")
			if err != nil {
				t.Fatalf("summarizeWithLLM: %v", err)
			}
			if stored := result[0].Message.Text(); !strings.Contains(stored, "Fixed the auth bug.") {
				t.Fatalf("the heading behind the echoed markers was not read as the summary's own:\n%s", stored)
			}
		})
	}
}

// A reply whose only body is framing has summarized nothing, whether the markers
// sit on their own line, beside a heading, or inside a fenced quote.
func TestSummarizeWithLLM_RejectsFramingWithNoContent(t *testing.T) {
	for name, reply := range map[string]string{
		"markers alone":    summaryMarkerStart + "\n" + summaryMarkerEnd,
		"beside a heading": summaryMarkerStart + " ## Progress: " + summaryMarkerEnd,
		"bold heading":     "**Progress: " + summaryMarkerEnd + "**",
		"in a fence":       "## Progress\n```\n" + summaryMarkerEnd + "\n```",
	} {
		t.Run(name, func(t *testing.T) {
			if result, err := summarizeReplying(t, reply, ""); err == nil {
				t.Fatalf("summarizeWithLLM stored %q as a summary, want an error", result[0].Message.Text())
			}
		})
	}
}

// The check reads the reply and edits nothing: a summary that names the markers,
// or quotes the framing inside a fenced or indented block, is stored as the model
// wrote it.
func TestSummarizeWithLLM_StoresTheReplyAsWritten(t *testing.T) {
	for name, reply := range map[string]string{
		"named in prose":    "## Progress\nAdded constants " + summaryMarkerStart + " and " + summaryMarkerEnd + " for the framing.",
		"quoted in a fence": "## Progress\n~~~\n" + summaryMarkerStart + "\ntext\n" + summaryMarkerEnd + "\n~~~",
		"indented marker":   "## Progress\n    " + summaryMarkerEnd + "\nFixed the auth bug.",
		"list-item fence":   "## Progress\nSTATE\n- ~~~text\n  " + summaryMarkerEnd + "\n  ~~~",
		// A backtick fence's info string may not contain a backtick, so this is
		// an inline code span on a list item, not a fence (#4435).
		"inline code span": "## Progress\n- ```status=done```",
	} {
		t.Run(name, func(t *testing.T) {
			result, err := summarizeReplying(t, reply, "")
			if err != nil {
				t.Fatalf("summarizeWithLLM: %v", err)
			}
			if stored := result[0].Message.Text(); !strings.Contains(stored, reply) {
				t.Fatalf("the reply was not stored as written:\n%s", stored)
			}
		})
	}
}

// A reply may open with an indented block. Its indentation is Markdown's, not
// whitespace to trim: trimming it would turn the block's own ~~~ line into a
// fence opener and hide every section after it.
func TestSummarizeWithLLM_AcceptsAnIndentedBlockBeforeASection(t *testing.T) {
	reply := "    ~~~\n    example\n    ~~~\n\n## Progress\nFixed the auth bug."
	result, err := summarizeReplying(t, reply, "")
	if err != nil {
		t.Fatalf("summarizeWithLLM: %v", err)
	}
	if stored := result[0].Message.Text(); !strings.Contains(stored, "## Progress\nFixed the auth bug.") {
		t.Fatalf("the section after the indented block was not read:\n%s", stored)
	}
}

// An indented line is code, not a section: a reply that is nothing but an
// indented example, heading included, has summarized nothing.
func TestSummarizeWithLLM_RejectsAnIndentedExample(t *testing.T) {
	reply := "    ~~~\n    ## Progress\n    ~~~"
	if result, err := summarizeReplying(t, reply, ""); err == nil {
		t.Fatalf("summarizeWithLLM stored %q as a summary, want an error", result[0].Message.Text())
	}
}

// A fenced quote of the framing is the model's content, even when the block's
// own delimiter is indented by four spaces: Markdown reads that as code inside
// the block, not as a delimiter, so the block stays open and the quote stays.
func TestSummarizeWithLLM_KeepsAFencedQuoteWithAnIndentedDelimiter(t *testing.T) {
	quoted := "## Progress\n~~~\n    ~~~\n" + summaryMarkerEnd + "\n~~~\nFixed the auth bug."
	result, err := summarizeReplying(t, quoted, "")
	if err != nil {
		t.Fatalf("summarizeWithLLM: %v", err)
	}
	if stored := result[0].Message.Text(); !strings.Contains(stored, summaryMarkerEnd+"\n~~~") {
		t.Fatalf("the summary lost the framing it quoted:\n%s", stored)
	}
}

// The same Markdown rule decides where a block ends, so the section after such a
// block is read as a section and not as text inside it: reading the lines
// trimmed would close the block at the indented delimiter, reopen it at the real
// one, and swallow the heading.
func TestSummarizeWithLLM_ReadsASectionAfterAnIndentedDelimiter(t *testing.T) {
	reply := "~~~\n    ~~~\n~~~\n\n## Progress\nFixed the auth bug."
	result, err := summarizeReplying(t, reply, "")
	if err != nil {
		t.Fatalf("summarizeWithLLM: %v", err)
	}
	if stored := result[0].Message.Text(); !strings.Contains(stored, "## Progress\nFixed the auth bug.") {
		t.Fatalf("the section after the block was not read:\n%s", stored)
	}
}

// Markdown opens a fenced block on a list item's first line. The tracker read
// the "- ~~~text" line as ordinary text, so it never opened the block and the
// closing "  ~~~" line was read as a fresh opener: a reply whose whole body is
// such a fenced quote of the framing was accepted, its "[END SUMMARY]" line
// misjudged as the summary's own content (#4435).
func TestSummarizeWithLLM_RejectsAFencedQuoteInAListItem(t *testing.T) {
	reply := "## Progress\n- ~~~text\n  " + summaryMarkerEnd + "\n  ~~~"
	if result, err := summarizeReplying(t, reply, ""); err == nil {
		t.Fatalf("summarizeWithLLM stored %q as a summary, want an error; the %q line inside the list item's fence was read as reply content",
			result[0].Message.Text(), summaryMarkerEnd)
	}
}

// A list item's content indentation is the marker and the spaces that follow
// it, so an item whose fence opens two spaces past the marker reads its closing
// fence three columns further. The tracker anchored the closing window one
// column past the marker instead, so the block stayed open and its closing
// fence was read as content (#4435).
func TestSummarizeWithLLM_RejectsAFencedQuoteInAListItemWithSpacedMarker(t *testing.T) {
	reply := "## Progress\n-  ~~~text\n   " + summaryMarkerEnd + "\n      ~~~"
	if result, err := summarizeReplying(t, reply, ""); err == nil {
		t.Fatalf("summarizeWithLLM stored %q as a summary, want an error; the list item's closing fence was read as content",
			result[0].Message.Text())
	}
}

// A fence opened inside a list item does not swallow the block heading after
// it: with the tracker blind to the list item's fence, the closing line opened
// a block that stayed open, so the "### Agent" heading was read as text inside
// the user's block (#4435). Every list marker Markdown knows opens such a
// container, and the closing fence sits at the item's content indentation.
func TestParseMarkdownBlocks_KeepsTheHeadingAfterAListItemFence(t *testing.T) {
	for name, item := range map[string]struct{ open, content, close string }{
		"issue example": {"- ~~~text", "  " + summaryMarkerEnd, "  ~~~"},
		"one space":     {"- ~~~text", "  quoted", "  ~~~"},
		"two spaces":    {"-  ~~~text", "   quoted", "   ~~~"},
		"three spaces":  {"-   ~~~text", "    quoted", "    ~~~"},
		"four spaces":   {"-    ~~~text", "     quoted", "     ~~~"},
		"ordered":       {"1. ~~~text", "   quoted", "   ~~~"},
		"star":          {"* ~~~text", "  quoted", "  ~~~"},
		"plus":          {"+ ~~~text", "  quoted", "  ~~~"},
	} {
		t.Run(name, func(t *testing.T) {
			section := "### User\n\n" + item.open + "\n" + item.content + "\n" + item.close + "\n\n### Agent\n\nreply"
			blocks := parseMarkdownBlocks(section)
			if len(blocks) != 2 || blocks[0].Heading != "User" || blocks[1].Heading != "Agent" {
				t.Fatalf("blocks = %#v, want the User block followed by the Agent block; the list item's fence swallowed the heading", blocks)
			}
			if blocks[1].Text != "reply" {
				t.Fatalf("Agent block text = %q, want %q", blocks[1].Text, "reply")
			}
		})
	}
}

// A list item ends at a non-blank line indented less than its content, and the
// fence the item holds ends with it. The tracker kept such a block open, so a
// heading after it was read as text inside the block and swallowed (#4435).
func TestParseMarkdownBlocks_AHeadingEndsAListItemFence(t *testing.T) {
	for name, section := range map[string]string{
		"fence left open": "### User\n\n- ~~~text\n  " + summaryMarkerEnd + "\n\n### Agent\n\nreply",
		"closer too deep": "### User\n\n- ~~~text\n  quoted\n      ~~~\n\n### Agent\n\nreply",
	} {
		t.Run(name, func(t *testing.T) {
			blocks := parseMarkdownBlocks(section)
			if len(blocks) != 2 || blocks[0].Heading != "User" || blocks[1].Heading != "Agent" {
				t.Fatalf("blocks = %#v, want the User block followed by the Agent block; the list item swallowed the heading", blocks)
			}
		})
	}
}

// A second fence in the same list item is read at the item's indentation, not
// as a new top-level fence: closing the first fence does not end the item. The
// tracker reset the item indentation when a fence closed, so the second fence
// opened a top-level block that swallowed the heading after the item (#4435).
func TestParseMarkdownBlocks_ReadsASecondFenceInAListItem(t *testing.T) {
	section := "### User\n\n- ~~~text\n  quoted\n  ~~~\n  ~~~\n\n### Agent\n\nreply"
	blocks := parseMarkdownBlocks(section)
	if len(blocks) != 2 || blocks[0].Heading != "User" || blocks[1].Heading != "Agent" {
		t.Fatalf("blocks = %#v, want the User block followed by the Agent block; the item's second fence swallowed the heading", blocks)
	}
}

// The same shape as a reply: the section after the item is still read when the
// item holds two fences, so a valid summary is not rejected (#4435).
func TestSummarizeWithLLM_ReadsASectionAfterASecondFenceInAListItem(t *testing.T) {
	reply := "- ~~~text\n  ~~~\n  ~~~\n### Progress\nFixed the bug."
	result, err := summarizeReplying(t, reply, "")
	if err != nil {
		t.Fatalf("summarizeWithLLM: %v", err)
	}
	if stored := result[0].Message.Text(); !strings.Contains(stored, "Fixed the bug.") {
		t.Fatalf("the section after the list item's second fence was not read:\n%s", stored)
	}
}

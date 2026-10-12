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

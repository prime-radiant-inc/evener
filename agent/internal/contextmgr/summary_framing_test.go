package contextmgr

import (
	"strings"
	"testing"
)

// The stripper takes the framing off only where it frames: a line's start, a
// line's end, or a line that holds nothing else.
func TestStripSummaryFraming(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"empty":              {"", ""},
		"whitespace":         {" \n\t", ""},
		"framing alone":      {summaryMarkerStart + "\n" + summaryMarkerEnd, ""},
		"markers on a line":  {"## Progress\n" + summaryMarkerEnd + summaryMarkerStart, "## Progress"},
		"marker line alone":  {"## Progress\n" + summaryMarkerEnd + "\nFixed the auth bug.", "## Progress\nFixed the auth bug."},
		"no framing":         {"## Progress\nFixed the auth bug.", "## Progress\nFixed the auth bug."},
		"framed reply":       {summaryMarkerStart + "\n## Progress\nFixed it.\n" + summaryMarkerEnd, "## Progress\nFixed it."},
		"marker then text":   {summaryMarkerStart + " ## Progress\nFixed it.", "## Progress\nFixed it."},
		"text then marker":   {"## Progress: " + summaryMarkerEnd, "## Progress:"},
		"framing both ends":  {summaryMarkerStart + " ## Progress: " + summaryMarkerEnd, "## Progress:"},
		"framing no colon":   {summaryMarkerStart + " ## Progress " + summaryMarkerEnd, "## Progress"},
		"framing repeated":   {summaryMarkerStart + summaryMarkerStart + " ## Progress", "## Progress"},
		"indented delimiter": {"## Progress\n~~~\n    ~~~\n" + summaryMarkerEnd + "\n~~~", "## Progress\n~~~\n    ~~~\n" + summaryMarkerEnd + "\n~~~"},
		"marker in a line":   {"See the framing " + summaryMarkerEnd + " for the format.", "See the framing " + summaryMarkerEnd + " for the format."},
		"named in prose":     {"## Progress\nAdded constants " + summaryMarkerStart + " and " + summaryMarkerEnd + " for the framing.", "## Progress\nAdded constants " + summaryMarkerStart + " and " + summaryMarkerEnd + " for the framing."},
		"prose then marker":  {"## Progress\n- Renamed " + summaryMarkerStart, "## Progress\n- Renamed " + summaryMarkerStart},
		"marker then prose":  {summaryMarkerStart + " is the framing we write.", summaryMarkerStart + " is the framing we write."},
		"indented marker":    {"## Progress\n    " + summaryMarkerEnd + "\nFixed it.", "## Progress\n    " + summaryMarkerEnd + "\nFixed it."},
		"quoted in a fence":  {"## Progress\n```\n" + summaryMarkerStart + "\ntext\n" + summaryMarkerEnd + "\n```", "## Progress\n```\n" + summaryMarkerStart + "\ntext\n" + summaryMarkerEnd + "\n```"},
		"framing and blanks": {summaryMarkerStart + "\n\n## Progress\nFixed it.\n\n" + summaryMarkerEnd, "## Progress\nFixed it."},
	} {
		t.Run(name, func(t *testing.T) {
			if got := stripSummaryFraming(tc.in); got != tc.want {
				t.Fatalf("stripSummaryFraming(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A reply that echoes the framing is stored with the framing once: the turn's
// own wrap writes it, and the reply's copy is stripped before the reply is
// validated and stored. A substring assertion would not see the difference
// between one wrap and two, so this counts the markers.
func TestSummarizeWithLLM_EchoedFramingIsStoredOnce(t *testing.T) {
	reply := summaryMarkerStart + "\n## Progress\nFixed the auth bug.\n" + summaryMarkerEnd
	result, err := summarizeReplying(t, reply, "")
	if err != nil {
		t.Fatalf("summarizeWithLLM: %v", err)
	}
	stored := result[0].Message.Text()
	starts, ends := strings.Count(stored, summaryMarkerStart), strings.Count(stored, summaryMarkerEnd)
	if starts != 1 || ends != 1 {
		t.Fatalf("stored summary holds %d start markers and %d end markers, want one of each:\n%s", starts, ends, stored)
	}
	if !strings.Contains(stored, "Fixed the auth bug.") {
		t.Fatalf("the echoed framing took the summary's own text with it:\n%s", stored)
	}
}

// A framing marker sharing a line with a real heading does not hide it: the
// marker is stripped and the heading is read as a section.
func TestSummarizeWithLLM_ReadsAHeadingBehindAnEchoedMarker(t *testing.T) {
	result, err := summarizeReplying(t, summaryMarkerStart+" ## Progress\nFixed the auth bug.", "")
	if err != nil {
		t.Fatalf("summarizeWithLLM: %v", err)
	}
	stored := result[0].Message.Text()
	if !strings.Contains(stored, "## Progress\nFixed the auth bug.") {
		t.Fatalf("the heading behind the echoed marker was not read as the summary's own:\n%s", stored)
	}
}

// A line of bare markers is framing, not content: counting it as content would
// store an empty summary as the only record of the folded history.
func TestSummarizeWithLLM_RejectsALineOfBareMarkers(t *testing.T) {
	reply := "## Progress\n" + summaryMarkerEnd + summaryMarkerStart
	if result, err := summarizeReplying(t, reply, ""); err == nil {
		t.Fatalf("summarizeWithLLM stored %q as a summary, want an error", result[0].Message.Text())
	}
}

// The framing is stripped only where it frames. A summary that names the
// markers — a session summarizing its own work on them — keeps every word.
func TestSummarizeWithLLM_KeepsTheMarkersAReplyNames(t *testing.T) {
	named := "Added constants " + summaryMarkerStart + " and " + summaryMarkerEnd + " for the framing."
	result, err := summarizeReplying(t, "## Progress\n"+named, "")
	if err != nil {
		t.Fatalf("summarizeWithLLM: %v", err)
	}
	if stored := result[0].Message.Text(); !strings.Contains(stored, named) {
		t.Fatalf("the stripped reply lost the markers it names:\n%s", stored)
	}
}

// A fenced quote of the framing keeps its text (it is the model's content, not
// framing) and does not count as the summary's content either: a reply whose
// only body is a quoted marker has not summarized anything.
func TestSummarizeWithLLM_RejectsAFenceHoldingOnlyAMarker(t *testing.T) {
	reply := "## Progress\n```\n" + summaryMarkerEnd + "\n```"
	if result, err := summarizeReplying(t, reply, ""); err == nil {
		t.Fatalf("summarizeWithLLM stored %q as a summary, want an error", result[0].Message.Text())
	}
}

// A marker beside a heading is not content whichever end of the heading's line
// it sits on: a reply of framing alone has not summarized anything.
func TestSummarizeWithLLM_RejectsAMarkerBesideAHeadingWithNoContent(t *testing.T) {
	reply := summaryMarkerStart + " ## Progress: " + summaryMarkerEnd
	if result, err := summarizeReplying(t, reply, ""); err == nil {
		t.Fatalf("summarizeWithLLM stored %q as a summary, want an error", result[0].Message.Text())
	}
}

// The markers an echoing model wraps a heading's line in can sit at either end
// and repeat: the heading behind them is still the summary's own, and the
// content under it counts.
func TestSummarizeWithLLM_ReadsAHeadingBehindMarkersAtBothEnds(t *testing.T) {
	for name, reply := range map[string]string{
		"both ends":       summaryMarkerStart + " ## Progress " + summaryMarkerEnd + "\nFixed the auth bug.",
		"repeated marker": summaryMarkerStart + summaryMarkerStart + " ## Progress\nFixed the auth bug.",
	} {
		t.Run(name, func(t *testing.T) {
			result, err := summarizeReplying(t, reply, "")
			if err != nil {
				t.Fatalf("summarizeWithLLM: %v", err)
			}
			if stored := result[0].Message.Text(); !strings.Contains(stored, "## Progress\nFixed the auth bug.") {
				t.Fatalf("the heading behind the echoed markers was not read as the summary's own:\n%s", stored)
			}
		})
	}
}

// A fenced quote of the framing is the model's content, even when the block's
// own delimiter is indented by four spaces: Markdown reads that as code inside
// the block, not as a delimiter, so the quote's marker lines are not framing.
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

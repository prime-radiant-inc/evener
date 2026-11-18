package sysmsg

import (
	"strings"

	"primeradiant.com/evener/agent/internal/mdfence"
)

// Compaction turn sections. A checkpoint or summary turn is a user-role message
// whose body mixes three kinds of content, and only the third is escaped:
//
//   - harness framing: the turn's own markers, its headings, its fences;
//   - the human partner's words: ### User blocks in the conversation section,
//     carried byte for byte;
//   - model-authored sections: the summary's own text, working notes, earlier
//     summaries, and the agent's replies. These are the model's words standing
//     in a user channel, so a frame they invented must not keep the spelling a
//     harness frame opens with (#6493).
//
// Keying on the section rather than on the message's role is what makes this
// the boundary: the role of a compaction turn is always user, yet part of its
// body is the model speaking.
const (
	compactionConversationHeading = "## Conversation"
	compactionWorkingNotesHeading = "## Working Notes"
	compactionSummariesHeading    = "## Earlier Summaries"
	compactionSummaryHeading      = "## Summary"
	compactionAgentHeading        = "### Agent"
	compactionNoteHeading         = "### Note"
	compactionSummaryBlockHeading = "### Summary"
)

// modelAuthoredBlockHeadings names the block headings whose bodies hold
// model-authored text, per enclosing section.
var modelAuthoredBlockHeadings = map[string]map[string]bool{
	compactionConversationHeading: {compactionAgentHeading: true},
	compactionWorkingNotesHeading: {compactionNoteHeading: true},
	compactionSummariesHeading:    {compactionSummaryBlockHeading: true},
}

// compactionMarkers are the turn's own framing lines, which are the harness
// speaking rather than any author.
var compactionMarkers = map[string]bool{
	"[CONTEXT SUMMARY]":    true,
	"[END SUMMARY]":        true,
	"[CONTEXT CHECKPOINT]": true,
	"[END CHECKPOINT]":     true,
}

// isStructuralLine reports whether content is one of the turn's structural
// lines — a heading or a framing marker — which is never escaped.
func isStructuralLine(content string) bool {
	if strings.HasPrefix(content, "## ") || strings.HasPrefix(content, "### ") {
		return true
	}
	trimmed := strings.TrimRight(strings.TrimSpace(content), " \t")
	return compactionMarkers[trimmed] || IsReminderBlock(trimmed)
}

// EscapeCompactionModelSections returns a compaction turn's text with frames
// defused in its model-authored sections only. Harness framing and the human
// partner's own blocks are returned byte for byte, and a block that needs no
// escaping is not copied.
//
// A compaction turn written before the current layout declares none of these
// headings, and its whole body is the model's text: with no partner section to
// preserve, every non-structural line is escaped. The harness's own framing
// stays as it is either way, so the turn still reads as the checkpoint it is.
func EscapeCompactionModelSections(text string) string {
	if !strings.ContainsAny(text, "<[") {
		return text
	}
	var b strings.Builder
	created := false
	var fence mdfence.Tracker

	// The layout is whatever headings the text itself declares; a turn with
	// none has no partner sections to protect.
	modelTextThroughout := !hasCompactionSection(text)
	section := ""
	// A summary turn puts the model's text under its own heading, outside any
	// ### block, so that heading is itself a model-authored body.
	inModelSection := modelTextThroughout

	pos := 0
	for line := range strings.Lines(text) {
		content := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		wasInside := fence.Inside()
		isDelim := fence.Step(content)
		structural := isStructuralLine(content)

		// Headings are read outside a fence only: a "## " line inside a fenced
		// body is that body's text, not the turn's structure.
		if structural && !wasInside && !isDelim {
			switch {
			case strings.HasPrefix(content, "## "):
				section = strings.TrimSpace(content)
				inModelSection = section == compactionSummaryHeading
			case strings.HasPrefix(content, "### "):
				inModelSection = modelAuthoredBlockHeadings[section][strings.TrimSpace(content)]
			}
		}

		// The fence around a model-authored section is the harness's, not the
		// model quoting a frame, so the body inside it is escaped: the fence
		// exemption EscapeModelAuthoredFrames applies does not extend to it.
		// Indented and block-quoted lines are still the model quoting a frame
		// rather than emitting one, and stay exempt.
		out := line
		if !structural && inModelSection && !isDelim {
			if !indentedCode(content) && !isQuotedLine(content) {
				if escaped, ok := escapeFrameOpener(content); ok {
					out = escaped + line[len(content):]
				}
			}
		}

		if out != line {
			if !created {
				b.Grow(len(text))
				b.WriteString(text[:pos])
				created = true
			}
			b.WriteString(out)
		} else if created {
			b.WriteString(line)
		}
		pos += len(line)
	}
	if !created {
		return text
	}
	return b.String()
}

// hasCompactionSection reports whether text declares one of the current
// layout's section headings outside a fenced body.
func hasCompactionSection(text string) bool {
	var fence mdfence.Tracker
	for line := range strings.Lines(text) {
		content := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		inside := fence.Inside()
		if fence.StepInBlock(content) || inside {
			continue
		}
		switch strings.TrimSpace(content) {
		case compactionConversationHeading, compactionWorkingNotesHeading,
			compactionSummariesHeading, compactionSummaryHeading:
			return true
		}
	}
	return false
}

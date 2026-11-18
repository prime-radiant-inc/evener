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
//
// What this scan trusts, and what it cannot: it reads the turn's own headings
// to decide which region is whose. A region is the partner's only when it has
// exactly the shape the renderer writes — one fenced block, nothing else — so a
// legacy turn whose model-written prose happens to include "## Conversation"
// and "### User" still gets its body escaped. Text alone cannot tell a genuine
// partner block from a model that reproduced the renderer's shape exactly, and
// this function does not claim to.
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

// compactionBlockKind is who wrote the region a heading opens.
type compactionBlockKind int

const (
	// compactionBlockModel is a region whose body the model wrote.
	compactionBlockModel compactionBlockKind = iota
	// compactionBlockPartner is a region that claims to hold the human
	// partner's words. The claim is checked against the renderer's shape
	// before it is believed; see flushRegion.
	compactionBlockPartner
)

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
// partner's own blocks are returned byte for byte, and a turn that needs no
// escaping is not copied.
//
// Each model-authored region is escaped as a unit, not line by line, so the
// escape sees that region's own fenced blocks from their start: the fence the
// renderer puts around a model section is the harness's and does not protect
// what is inside it, while a fence the model drew inside its own text is an
// example it is quoting and keeps its bytes. That is the same reading the
// capture side applies before it fences the text, so a literal example reads
// the same at capture and at replay.
//
// A compaction turn written before the current layout declares none of these
// headings, and its whole body is the model's text: with no partner section to
// preserve, every region is escaped. The harness's own framing stays as it is
// either way, so the turn still reads as the checkpoint it is.
func EscapeCompactionModelSections(text string) string {
	if !strings.ContainsAny(text, "<[") {
		return text
	}

	// The layout is whatever headings the text itself declares; a turn with
	// none has no partner sections to protect.
	blockKind := compactionBlockModel
	section := ""

	var b strings.Builder
	created := false
	copied := 0
	regionStart, regionEnd := -1, -1

	// flushRegion escapes the region just ended. A region the layout says the
	// partner wrote is left alone only when it is the shape the renderer
	// writes; anything else under a "### User" heading is a legacy turn's own
	// prose and gets the same treatment as any other model text.
	flushRegion := func() {
		start, end, kind := regionStart, regionEnd, blockKind
		regionStart, regionEnd = -1, -1
		if start < 0 {
			return
		}
		body := text[start:end]
		if kind == compactionBlockPartner {
			if _, _, ok := fencedPayloadRange(body); ok {
				return
			}
		}
		escaped := escapeCompactionRegion(body)
		if escaped == body {
			return
		}
		if !created {
			b.Grow(len(text))
			b.WriteString(text[:start])
			created = true
		} else {
			b.WriteString(text[copied:start])
		}
		b.WriteString(escaped)
		copied = end
	}

	var fence mdfence.Tracker
	offset := 0
	for line := range strings.Lines(text) {
		content := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		wasInside := fence.Inside()
		isDelim := fence.Step(content)
		structural := isStructuralLine(content)

		// Headings are read outside a fence only: a "## " line inside a fenced
		// body is that body's text, not the turn's structure.
		if structural && !wasInside && !isDelim {
			flushRegion()
			switch {
			case strings.HasPrefix(content, "## "):
				section = strings.TrimSpace(content)
				blockKind = compactionBlockModel
			case strings.HasPrefix(content, "### "):
				heading := strings.TrimSpace(content)
				switch {
				case modelAuthoredBlockHeadings[section][heading]:
					blockKind = compactionBlockModel
				case section == compactionConversationHeading && heading == "### User":
					// Tentative: the body decides whether this is the
					// partner's block or a legacy turn's own text.
					blockKind = compactionBlockPartner
				default:
					blockKind = compactionBlockModel
				}
			}
		} else if structural {
			flushRegion()
		} else {
			if regionStart < 0 {
				regionStart = offset
			}
			regionEnd = offset + len(line)
		}
		offset += len(line)
	}
	flushRegion()

	if !created {
		return text
	}
	b.WriteString(text[copied:])
	return b.String()
}

// escapeCompactionRegion escapes one model-authored region. A region the
// renderer fenced is escaped from its payload, so the model's own fences
// inside it are read from the payload's start; a region with no such fence is
// escaped whole.
func escapeCompactionRegion(body string) string {
	payloadStart, payloadEnd, ok := fencedPayloadRange(body)
	if !ok {
		return EscapeModelAuthoredFrames(body)
	}
	payload := body[payloadStart:payloadEnd]
	escaped := EscapeModelAuthoredFrames(payload)
	if escaped == payload {
		return body
	}
	return body[:payloadStart] + escaped + body[payloadEnd:]
}

// fencedPayloadRange reports the byte range of the single fenced block that
// fills body, ignoring blank lines around it. It is the renderer's shape read
// back: writeMarkdownFence writes just such a block, so a partner block has
// one and a region that merely claims to be one does not. It mirrors
// contextmgr's soleFencedText, which cannot be shared because that package
// imports this one.
func fencedPayloadRange(body string) (start, end int, ok bool) {
	lines := strings.SplitAfter(body, "\n")
	var fence mdfence.Tracker
	start = -1
	offset := 0
	for i, line := range lines {
		content := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if content == "" && start < 0 {
			offset += len(line)
			continue
		}
		if start < 0 {
			if !fence.Step(content) {
				return 0, 0, false
			}
			start = offset + len(line)
			offset += len(line)
			continue
		}
		if fence.Step(content) {
			// The closing fence must be the last content in the region.
			for _, rest := range lines[i+1:] {
				if strings.TrimSpace(rest) != "" {
					return 0, 0, false
				}
			}
			return start, offset, true
		}
		offset += len(line)
	}
	return 0, 0, false
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

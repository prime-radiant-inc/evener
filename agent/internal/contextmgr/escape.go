package contextmgr

import (
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/mdfence"
	"primeradiant.com/evener/agent/internal/sysmsg"
	"primeradiant.com/evener/agent/schema"
)

// TurnFrameEscape returns the request-only escape to apply to t's text, or nil
// when t is the person's own words and must reach the model byte for byte.
// Model prose has the frames it could forge escaped, a compaction artifact's
// fenced bodies are read through, and harness framing a composed turn carries
// is kept. Both main-session replay and compaction helper prompts share this
// policy.
func TurnFrameEscape(t schema.Turn) func(string) string {
	switch t.Kind {
	case schema.TurnCheckpoint, schema.TurnSummary:
		return EscapeModelAuthoredBodies
	case schema.TurnAssistant:
		return sysmsg.EscapeModelAuthoredFrames
	case schema.TurnSystem:
		return sysmsg.EscapeAnswersMarker
	case schema.TurnUserInput:
		if !isAgentAuthoredByRecord(t) {
			return nil
		}
		return sysmsg.EscapeModelAuthoredFrames
	case schema.TurnSteering:
		switch {
		case isParentSteering(t), t.SteeringKind == events.SteeringKindNoteHandoff:
			// Parent steers and pre-compaction note handoffs are agent prose
			// with no harness frame opener, so escape them whole.
			return sysmsg.EscapeModelAuthoredFrames
		case t.SteeringKind != "":
			// A daemon-injected steer carries the harness's own framing.
			return sysmsg.EscapeAnswersMarker
		default:
			// A kindless, source-less steer is the person's own.
			return nil
		}
	}
	return nil
}

// EscapeModelAuthoredBodies returns a compaction artifact's text with the model
// prose it carries escaped inside the harness's outer fences. The artifact's
// own headings, fences, pointer lines and markers are the harness speaking and
// stay as written, as does the person's prose the artifact carries verbatim:
// escaping a reply the person typed would rewrite words that only look like a
// frame. A body's own fenced code is quoted content and stays as written too.
//
// The renderer fences the model's text under "## Working Notes", under each
// "## Earlier Summaries" block, under a "## Conversation" "### Agent" block,
// under the agent-authored entry heading, and under "## Summary" for the
// summarizer's own reply; the session-log strategy fences its log of
// model-written action summaries. Everything fenced is model text except the
// "## Conversation" "### User" blocks and the "Original prompt:" block, which
// hold what the person wrote.
func EscapeModelAuthoredBodies(text string) string {
	if !strings.ContainsAny(text, "[<") {
		return text
	}
	var out strings.Builder
	var fence mdfence.Tracker
	var body strings.Builder
	owner, block := "", ""
	inBody := false
	for i := 0; i < len(text); {
		end := len(text)
		if nl := strings.IndexByte(text[i:], '\n'); nl >= 0 {
			end = i + nl + 1
		}
		line := text[i:end]
		content := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		wasInside := fence.Inside()
		delim := fence.Step(content)
		i = end
		switch {
		case inBody:
			// Only a line the tracker reads as the closing fence ends the body:
			// a fence inside the body is shorter than the one the renderer picked,
			// so it reads as the body's own content.
			if !delim || fence.Inside() {
				body.WriteString(line)
				continue
			}
			out.WriteString(escapedFenceBody(owner, body.String()))
			body.Reset()
			inBody = false
		case !wasInside && delim && fence.Inside():
			inBody = !humanBody(owner, block)
		case !wasInside && !delim:
			// A heading or label selects what the next fence holds. Both are read
			// outside fences only, so a heading line a model wrote inside its own
			// body is that body's text.
			if rest, ok := strings.CutPrefix(content, "### "); ok {
				block = strings.TrimSpace(rest)
			} else if rest, ok := strings.CutPrefix(content, "## "); ok {
				owner, block = strings.TrimSpace(rest), ""
			} else if label, rest, ok := strings.Cut(content, ":"); ok && strings.TrimSpace(rest) == "" {
				switch strings.TrimSpace(label) {
				case "Original prompt", "Session log":
					owner, block = strings.TrimSpace(label), ""
				}
			}
			out.WriteString(sysmsg.EscapeAnswersMarker(line))
			continue
		}
		out.WriteString(line)
	}
	if inBody {
		// An artifact cut off inside a body still holds that body's text.
		out.WriteString(escapedFenceBody(owner, body.String()))
	}
	return out.String()
}

// escapedFenceBody escapes one fenced body. A body is model prose escaped whole
// — its own fenced code is quoted content and stays as written — except when it
// is a carried earlier summary: that body is itself a compaction turn, and the
// renderer fences the summarizer's reply under "## Summary" and the person's
// carried messages under "## Conversation" inside it. Read as a body of its
// own, its inner structure selects what is model prose, so the summarizer's
// reply is escaped in place instead of being skipped as nested quoted code.
//
// The body is recognised by the layout the renderer writes, not by a heading a
// model may have written: an older summary, which predates that layout and is
// the model's text throughout, keeps its own "## Summary" heading as the text
// it is. Only a body that reproduces the renderer's framing is read through.
func escapedFenceBody(owner, body string) string {
	if !strings.ContainsAny(body, "[<") {
		return body
	}
	if owner == "Earlier Summaries" && carriedSummaryOpening(body) {
		// The body is a summary turn in its own right, so its inner sections
		// select what is model prose. Recursing re-reads it from its head: the
		// tracker finds the end of the "## Summary" fence on its own, whether
		// the fold kept that fence or cut it at the body's tail.
		return EscapeModelAuthoredBodies(body)
	}
	return sysmsg.EscapeModelAuthoredFrames(body)
}

// carriedSummaryOpening reports whether text is the opening of a summary turn
// the renderer wrote, as a checkpoint fold carries it. A fold moves the
// summary's conversation section — and the turn's closing marker with it — into
// the checkpoint's own conversation, so the body it keeps is the turn's
// opening: the summary marker, the pointer line and partner-carry reminder the
// renderer may write, then the model's text fenced under
// summaryModelTextHeading. That opening is still the renderer's framing, so the
// reply under the heading is read through as model prose instead of as quoted
// code.
//
// The opening is recognised by that layout alone: the heading's first non-blank
// line must be a fence the renderer picked. Whether the closing fence survived
// the fold is not asked — a body trimmed at its tail keeps the same opening —
// and neither is what follows the fence's end, because the recursion reads the
// turn's own sections from what the fold kept.
func carriedSummaryOpening(text string) bool {
	start, body, _, ok := markdownSectionSpan(text, summaryModelTextHeading)
	if !ok || !isSummaryTurnOpening(text[:start]) {
		return false
	}
	for line := range strings.SplitSeq(text[body:], "\n") {
		if strings.TrimSpace(line) != "" {
			return renderedFenceOpener(line)
		}
	}
	return false
}

// renderedFenceOpener reports whether line is the opener writeMarkdownFence
// writes: three or more backticks with the info string "text" and nothing
// else.
func renderedFenceOpener(line string) bool {
	n := 0
	for n < len(line) && line[n] == '`' {
		n++
	}
	return n >= 3 && strings.TrimSpace(line[n:]) == "text"
}

// humanBody reports whether the fence opened under owner/block holds text the
// person wrote: the conversation entries the artifact carries verbatim from
// their own messages, and the session-log strategy's original prompt. Every
// other fenced body is model prose.
func humanBody(owner, block string) bool {
	return owner == "Original prompt" || (owner == "Conversation" && block == "User")
}

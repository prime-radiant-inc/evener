package contextmgr

import (
	"strings"

	"primeradiant.com/evener/agent/internal/mdfence"
	"primeradiant.com/evener/agent/internal/sysmsg"
)

// EscapeModelAuthoredBodies returns a compaction artifact's text with the model
// prose it carries escaped inside the harness's outer fences. The artifact's
// own headings, fences, pointer lines and markers are the harness speaking and
// stay as written, as does the person's prose the artifact carries verbatim:
// escaping a reply the person typed would rewrite words that only look like a
// frame. A body's own fenced code is quoted content and stays as written too.
//
// The renderer fences the model's text under "## Working Notes", under each
// "## Earlier Summaries" block, under a "## Conversation" "### Agent" block,
// and under "## Summary" for the summarizer's own reply; the session-log
// strategy fences its log of model-written action summaries. Everything fenced
// is model text except the "## Conversation" "### User" blocks and the
// "Original prompt:" block, which hold what the person wrote.
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
			out.WriteString(sysmsg.EscapeModelAuthoredFrames(body.String()))
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
		out.WriteString(sysmsg.EscapeModelAuthoredFrames(body.String()))
	}
	return out.String()
}

// humanBody reports whether the fence opened under owner/block holds text the
// person wrote: the conversation entries the artifact carries verbatim from
// their own messages, and the session-log strategy's original prompt. Every
// other fenced body is model prose.
func humanBody(owner, block string) bool {
	return owner == "Original prompt" || (owner == "Conversation" && block == "User")
}

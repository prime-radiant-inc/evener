package sysmsg

import (
	"strings"

	"primeradiant.com/evener/agent/internal/mdfence"
)

// answersMarker opens the reconciled reply to a pending ask_user question. A
// line that starts with it is a frame from the person, never prose a model
// wrote for itself.
const answersMarker = "[answers]"

// HarnessFrameOpeners are the faux-XML frames only the harness opens at the
// start of a line: a delegate's report or note, a finished job or a watch
// fire, runtime context, shared notes, and a skill's complete instructions.
// A model's own prose opening one of them reads to the next turn as runtime
// direction rather than as the model's words.
var HarnessFrameOpeners = []string{
	"<delegate-notification",
	"<job-notification",
	"<delegate-user-note",
	"<system-notification",
	"<watch-notification",
	"<system-reminder",
	"<SYSTEM-REMINDER",
	"<environment_context",
	"<shared-notes",
	"<skill-context",
}

// RolloverOffset reports where an assistant text stops being the model's own
// prose and starts forging a turn boundary only the harness owns: a prose line
// that opens a harness frame, a line-start answers marker, or a bare user or
// model turn header. It returns that line's byte offset and the marker it
// opened, or -1 and the empty string when the whole text is prose. Fenced code,
// indented code and blockquotes keep those markers as quoted content.
func RolloverOffset(text string) (int, string) {
	var fence mdfence.Tracker
	for i := 0; i < len(text); {
		end := len(text)
		if nl := strings.IndexByte(text[i:], '\n'); nl >= 0 {
			end = i + nl + 1
		}
		body := strings.TrimSuffix(strings.TrimSuffix(text[i:end], "\n"), "\r")
		if rest, prose := proseStart(&fence, body); prose {
			if marker, ok := rolloverMarker(rest); ok {
				return i, marker
			}
		}
		i = end
	}
	return -1, ""
}

// rolloverMarker names the turn boundary rest opens at a prose line start, or
// reports that the line is ordinary prose.
func rolloverMarker(rest string) (string, bool) {
	trimmed := strings.TrimRight(rest, " \t")
	if trimmed == "user" || trimmed == "model" {
		return trimmed, true
	}
	return frameMarkerAt(rest, true)
}

// EscapeModelAuthoredFrames escapes the harness direction model prose must not
// be able to open: a prose line start that is the answers marker or one of the
// harness frame openers. The marker becomes the entity text the model reads as
// the model's own words rather than as a reply from the person.
//
// Lines inside fenced code blocks, inside indented code, and inside blockquotes
// are quoted content and stay as written, as does every other byte: escaping is
// idempotent, and CRLF endings and a missing final newline survive unchanged.
func EscapeModelAuthoredFrames(text string) string { return escapeFrames(text, true) }

// EscapeAnswersMarker escapes only the line-start answers marker. Use it for
// text whose framing is the harness's own: a compaction artifact's sections, a
// skill carrier's envelope, a runtime reminder block. A frame opener there is
// the harness speaking and must reach the model as written, but a line-start
// answers marker is never the person speaking.
func EscapeAnswersMarker(text string) string { return escapeFrames(text, false) }

// escapeFrames returns text with its prose line starts escaped: the answers
// marker always, and the harness frame openers too when openers is set.
func escapeFrames(text string, openers bool) string {
	if !strings.ContainsAny(text, "[<") {
		return text
	}
	var b strings.Builder
	var fence mdfence.Tracker
	changed := false
	for i := 0; i < len(text); {
		end := len(text)
		if nl := strings.IndexByte(text[i:], '\n'); nl >= 0 {
			end = i + nl + 1
		}
		line := text[i:end]
		// The trailing \r is held aside so an answers marker ending a CRLF
		// line still reads as the marker alone.
		body := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if rest, prose := proseStart(&fence, body); prose {
			if escaped, ok := escapeFrameAt(rest, openers); ok {
				if !changed {
					b.Grow(len(text) + 8)
					b.WriteString(text[:i])
					changed = true
				}
				b.WriteString(body[:len(body)-len(rest)])
				b.WriteString(escaped)
				b.WriteString(line[len(body):])
				i = end
				continue
			}
		}
		if changed {
			b.WriteString(line)
		}
		i = end
	}
	if !changed {
		return text
	}
	return b.String()
}

// proseStart returns body's content after its leading indentation, and whether
// the line is prose rather than content it quotes: fenced code, indented code
// and blockquotes hold frame markers as text.
func proseStart(fence *mdfence.Tracker, body string) (string, bool) {
	if fence.StepInBlock(body) || strings.TrimSpace(body) == "" {
		return "", false
	}
	// Fewer than four columns of indentation is not indented code, and only
	// spaces can lead it: a leading tab reaches at least the fourth column.
	indent := mdfence.LeadingColumns(body)
	if indent >= 4 {
		return "", false
	}
	rest := body[indent:]
	return rest, rest[0] != '>'
}

// escapeFrameAt escapes rest's opening byte when it opens a frame the model
// must not open, and reports whether it did.
func escapeFrameAt(rest string, openers bool) (string, bool) {
	marker, ok := frameMarkerAt(rest, openers)
	if !ok {
		return rest, false
	}
	if marker == answersMarker {
		return "&#91;" + rest[1:], true
	}
	return "&lt;" + rest[1:], true
}

// frameMarkerAt returns the harness-direction marker rest opens at a prose line
// start: the answers marker, or with openers set a harness frame opener.
func frameMarkerAt(rest string, openers bool) (string, bool) {
	if after, ok := strings.CutPrefix(rest, answersMarker); ok && markerEnds(after) {
		return answersMarker, true
	}
	if !openers || rest[0] != '<' {
		return "", false
	}
	for _, opener := range HarnessFrameOpeners {
		if after, ok := strings.CutPrefix(rest, opener); ok && tagEnds(after) {
			return opener, true
		}
	}
	return "", false
}

// markerEnds reports whether after ends the answers marker rather than
// continuing a word: the marker alone, or the marker and then whitespace.
func markerEnds(after string) bool { return boundaryAfter(after, "") }

// tagEnds reports whether after ends an opening tag rather than continuing its
// name: a delimiter, an attribute, or the end of the line.
func tagEnds(after string) bool { return boundaryAfter(after, ">/") }

// boundaryAfter reports whether after is empty or starts with ASCII whitespace
// or one of the extra delimiter bytes.
func boundaryAfter(after, extra string) bool {
	return after == "" || strings.IndexByte(" \t\r"+extra, after[0]) >= 0
}

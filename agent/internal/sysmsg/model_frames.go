package sysmsg

import (
	"strings"

	"primeradiant.com/evener/agent/internal/mdfence"
)

// HarnessTagPrefix is one line-start tag and how its name compares. The
// spellings the harness writes differ in case where the writing code does:
// <SYSTEM-REMINDER> is uppercase, <system-reminder> and
// <system-notification> are lowercase. CaseFoldASCII marks the tag the
// assistant-fabrication detector matches case-insensitively, so a model
// writing <System-Reminder> cannot be flagged by that detector and replayed
// unescaped by the escape — the drift #6695's review found.
type HarnessTagPrefix struct {
	Prefix        string
	CaseFoldASCII bool
}

// modelAuthoredTagOpeners are the line-start tags EscapeModelAuthoredFrames
// defuses: every frame the harness appends to the model's context, plus the
// tags a real reply or notification arrives wrapped in. Text the model wrote
// must never present as one of them, because the user-turn channel is the
// authorization channel (#6493): a fabricated [answers] block reads as a
// decision the partner made, and a fabricated <delegate-notification> reads as
// a runtime fact the harness never reported.
//
// This is deliberately NOT the same set as agent/doctor/fabrication.go's
// harnessTagPrefixes, and the two are not meant to converge:
//
//   - An escape must cover a SUPERSET. Escaping a line that turns out to be
//     ordinary prose costs a reader nothing, so this list matches on prefix
//     and carries the bracketed block markers below.
//   - The doctor's detector must not produce a FALSE POSITIVE, so [answers] is
//     not one of its tag prefixes at all: it reports one only when a numbered
//     answer line follows (fabrication.go's hasFollowingNumberedAnswerLine),
//     and it matches [CONTEXT SUMMARY] by whole-line equality rather than by
//     prefix.
//
// So an entry added here for safety need not be added there, and an entry
// added there for detection need not be added here. What the two must share is
// how a tag NAME compares, which is HarnessTagMatches's job; when you add a
// frame that both readers should know, add it to both lists and say why.
var modelAuthoredTagOpeners = []HarnessTagPrefix{
	{Prefix: "<delegate-notification"},
	{Prefix: "<job-notification"},
	{Prefix: "<delegate-user-note"},
	{Prefix: "<system-notification"},
	// Case-folded here exactly as the doctor's detector folds it, so any
	// spelling that detector would flag is also escaped (see HarnessTagPrefix).
	{Prefix: "<system-reminder", CaseFoldASCII: true},
	{Prefix: "<environment_context"},
	{Prefix: "<environment-context"},
	{Prefix: "<shared-notes"},
	{Prefix: "<skill-context"},
	{Prefix: "<skill-inventory"},
	// The task store's completion block, sent at line start in user-role
	// steering (agent/task_reminders.go's taskCompletionMachineBlockStart).
	// A model repeating it would present state the store never reported.
	{Prefix: "<evener-task-completion"},
}

// modelAuthoredMarkerLines are the bracketed line-start markers the harness
// writes into the model's context. They are matched whole-line: a sentence
// opening with the marker's spelling is prose quoting it, while a line that is
// only the marker opens a block.
var modelAuthoredMarkerLines = []string{
	"[answers]",
	"[CONTEXT SUMMARY]",
	"[CONTEXT CHECKPOINT]",
	// The session-log strategy's own header starts with "[CONTEXT CHECKPOINT"
	// and would otherwise match nothing: it is its own line, not a prefix.
	"[CONTEXT CHECKPOINT - SESSION LOG]",
	"[END SUMMARY]",
	"[END CHECKPOINT]",
	// The OODA strategy's orientation banner, appended as a user-role steering
	// turn (internal/contextmgr/strategy_ooda.go).
	"[SESSION ORIENTATION]",
}

// HarnessTagMatches reports whether line begins tag, returning the text after
// it. The one place tag names are compared: both this package's escape and the
// doctor's fabrication detector call it, so a case-folding rule cannot differ
// between the two lists.
func HarnessTagMatches(line string, tag HarnessTagPrefix) (rest string, ok bool) {
	if !tag.CaseFoldASCII {
		return strings.CutPrefix(line, tag.Prefix)
	}
	if len(line) < len(tag.Prefix) || !strings.EqualFold(line[:len(tag.Prefix)], tag.Prefix) {
		return "", false
	}
	return line[len(tag.Prefix):], true
}

// EscapeModelAuthoredFrames returns text with every line-start harness input
// frame defused, so model-authored prose cannot pass to a later request as a
// frame the harness wrote. A tag's opening character is rewritten to "&lt;"
// and a marker's opening bracket to "&#91;", which leaves the text legible and
// the line otherwise byte-identical while denying it the exact spelling a real
// frame opens with. The stored turn keeps its own bytes: callers escape the
// copy they hand the model or place in a compaction turn, never the turn they
// persist.
//
// Code is not prose and is left alone, as the doctor's detector leaves it: a
// line inside a fenced block (including one opened inside a list item),
// indented four or more columns, or beginning with a block quote marker is the
// model quoting a frame rather than emitting one, and escaping it would
// corrupt a legitimate example. A two-space list continuation line is prose to
// both readers and is escaped.
//
// One pass, so the escape is idempotent: an already-escaped opener no longer
// matches, and its "&lt;" is not itself a frame. Text with no frame opener
// returns unchanged and unallocated. Line endings are preserved byte for byte,
// so CRLF text keeps its CRLFs.
func EscapeModelAuthoredFrames(text string) string {
	if !strings.ContainsAny(text, "<[") {
		return text
	}
	var b strings.Builder
	created := false
	var fence mdfence.Tracker
	pos := 0
	for line := range strings.Lines(text) {
		content := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		out := line
		if !fence.StepInBlock(content) && !indentedCode(content) && !isQuotedLine(content) {
			if escaped, ok := escapeFrameOpener(content); ok {
				out = escaped + line[len(content):]
			}
		}
		// Nothing is allocated and nothing is copied until a line actually
		// changes: most replayed text carries no frame, and this runs on
		// every request.
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

// escapeFrameOpener rewrites the opening character of content's frame prefix,
// reporting false when content starts with no frame.
func escapeFrameOpener(content string) (string, bool) {
	trimmed := strings.TrimLeft(content, " ")
	if trimmed == "" {
		return "", false
	}
	indent := len(content) - len(trimmed)
	if isModelAuthoredMarkerLine(strings.TrimRight(trimmed, " \t")) {
		return content[:indent] + "&#91;" + trimmed[1:], true
	}
	for _, tag := range modelAuthoredTagOpeners {
		after, ok := HarnessTagMatches(trimmed, tag)
		if !ok || !hasTagBoundary(after) {
			continue
		}
		return content[:indent] + "&lt;" + trimmed[1:], true
	}
	return "", false
}

// isModelAuthoredMarkerLine reports whether line, trimmed, is exactly one of
// modelAuthoredMarkerLines.
func isModelAuthoredMarkerLine(line string) bool {
	for _, marker := range modelAuthoredMarkerLines {
		if line == marker {
			return true
		}
	}
	return false
}

// hasTagBoundary reports whether rest, the text after a matched tag prefix, is
// where a tag name ends: end of line, whitespace, ">", "/", or "=". A longer
// word that merely starts with a tag name (<system-notifications-doc>) is an
// identifier, not a frame.
func hasTagBoundary(rest string) bool {
	if rest == "" {
		return true
	}
	switch rest[0] {
	case ' ', '\t', '>', '/', '\r', '=':
		return true
	default:
		return false
	}
}

// isQuotedLine reports whether content begins a Markdown block quote, whose
// body is quoted material rather than the author speaking.
func isQuotedLine(content string) bool {
	return strings.HasPrefix(strings.TrimLeft(content, " "), ">")
}

// indentedCode reports whether content is Markdown-indented code, whose text is
// literal content rather than prose.
func indentedCode(content string) bool {
	return strings.TrimSpace(content) != "" && mdfence.LeadingColumns(content) >= 4
}

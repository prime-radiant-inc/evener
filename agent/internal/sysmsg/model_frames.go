package sysmsg

import (
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"

	"primeradiant.com/evener/llm"
)

// SplitJoinedEscape splits escaped, the escape of strings.Join(originals, "\n"),
// back into one piece per original, by each original's line count.
//
// The escape rewrites a frame's opening character and never adds or removes a
// line or a line break, so a piece occupies exactly the lines its original did
// and the split is the join read backwards. ok is false when the escaped text
// does not have that shape, and the caller then leaves the text as stored
// rather than hand back text split somewhere it was not.
//
// Two readers need it, for different reasons: the replay projection writes each
// piece back into the part it came from, and checkpoint capture concatenates
// the pieces so the note keeps the boundaries the model's own text had. Sharing
// the splitter keeps them splitting the same scan the same way.
func SplitJoinedEscape(escaped string, originals []string) (pieces []string, ok bool) {
	return llm.SplitJoinedEscape(escaped, originals)
}

// harnessTagPrefix is one line-start tag and how its name compares. The
// spellings the harness writes differ in case where the writing code does:
// <SYSTEM-REMINDER> is uppercase, <system-reminder> and
// <system-notification> are lowercase. CaseFoldASCII marks the tag the
// assistant-fabrication detector matches case-insensitively, so a model
// writing <System-Reminder> cannot be flagged by that detector and replayed
// unescaped by the escape — the drift #6695's review found.
//
// This package keeps its own list and its own comparison. agent/doctor's
// fabrication detector keeps a separate list; the two are not shared, so a
// change here reaches the escape only.
type harnessTagPrefix struct {
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
// added there for detection need not be added here. The detector's list is its
// own; this package compares tag names through harnessTagMatches and the
// detector through its own rule, and neither reaches the other's list.
var modelAuthoredTagOpeners = []harnessTagPrefix{
	{Prefix: "<delegate-notification"},
	{Prefix: "<job-notification"},
	{Prefix: "<delegate-user-note"},
	{Prefix: "<system-notification"},
	// Case-folded here exactly as the doctor's detector folds it, so any
	// spelling that detector would flag is also escaped (see harnessTagPrefix).
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

// harnessTagMatches reports whether line begins tag, returning the text after
// it. This is this package's tag comparison, used by the escape alone; the
// doctor's fabrication detector keeps its own list and its own rule.
func harnessTagMatches(line string, tag harnessTagPrefix) (rest string, ok bool) {
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
// line inside a fenced block (including one opened inside a list item or a
// block quote), inside an indented code block, or beginning with a block quote
// marker is the model quoting a frame rather than emitting one, and escaping it
// would corrupt a legitimate example. A list continuation line is prose to both
// readers and is escaped.
//
// Which lines are code is the CommonMark reader's answer, not this function's:
// the text is parsed and the code blocks' own lines are taken from the parse
// (codeLineMask). The escape therefore cannot disagree with the provider's
// Markdown about a construct — a spaced thematic break, an ordered list that
// may not interrupt a paragraph, a setext underline — and a line the reader
// treats as code keeps every byte.
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
	code := codeLineMask(text)
	pos := 0
	index := 0
	// The last numbered answer line's offset, found once. A marker carrying a
	// note is a frame only when an answer line follows it, and "an answer
	// follows" is the same question as "the LAST answer is after it" — so one
	// scan answers it for every marker in the text. Scanning the remainder per
	// marker was quadratic, and a long reply full of note-bearing markers ran
	// that scan again on every request while it stayed in history (#6493).
	lastAnswer := lastAnswerOffset(text)
	for line := range strings.Lines(text) {
		content := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		out := line
		if !code[index] && !isQuotedLine(content) {
			if escaped, ok := escapeFrameOpener(content, pos+len(line) <= lastAnswer); ok {
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
		index++
	}
	if !created {
		return text
	}
	return b.String()
}

// escapeFrameOpener rewrites the opening character of content's frame prefix,
// reporting false when content starts with no frame.
//
// The indentation is skipped as bytes and written back unchanged, because it is
// the line's own Markdown indentation: a tab-indented continuation line under a
// two-digit ordered item reaches the item's content column, so it is prose to
// the code check and only a tab is left ahead of the frame. Skipping spaces
// alone left the tab in place and passed the frame through unescaped (#6493).
//
// answerFollows is true when a numbered answer line comes after this one. It is
// read only for the answer marker, which the client reads out of a reply with a
// note appended to the marker line as long as numbered answer lines follow:
// parseAskAnswerLines takes the answers from that text and
// linkedReplyAnswersThisCall admits it, while the stricter
// composedReplyCoversCall declines it and keeps the raw row. A marker alone on
// its line opens a block whether or not answers follow; a marker carrying a note
// is a frame only in that shape, so a sentence that merely mentions the marker
// stays prose.
//
// The caller computes answerFollows once for the whole text rather than scanning
// per line, which is what keeps a long reply linear.
func escapeFrameOpener(content string, answerFollows bool) (string, bool) {
	// The indentation and the trailing run are trimmed by the CLIENT's
	// whitespace set, not by a space-or-tab set. A marker followed only by a
	// nonbreaking space is a bare marker to the client — its `\s` matches that
	// space — but it was neither the whole-line marker nor a note-bearing one
	// here, so the marker survived even with a valid answer line under it
	// (#6493 review).
	trimmed := strings.TrimLeftFunc(content, isClientSpace)
	if trimmed == "" {
		return "", false
	}
	indent := len(content) - len(trimmed)
	// A suffix of nothing but that whitespace is the bare marker: the client
	// reads the same line whether the space after the marker is ordinary or
	// not, so the two must decide the same way here.
	if isModelAuthoredMarkerLine(strings.TrimRightFunc(trimmed, isClientSpace)) {
		return content[:indent] + "&#91;" + trimmed[1:], true
	}
	if isAnswerMarkerWithNote(trimmed, answerFollows) {
		return content[:indent] + "&#91;" + trimmed[1:], true
	}
	for _, tag := range modelAuthoredTagOpeners {
		after, ok := harnessTagMatches(trimmed, tag)
		if !ok || !hasTagBoundary(after) {
			continue
		}
		return content[:indent] + "&lt;" + trimmed[1:], true
	}
	return "", false
}

// answerMarker is the bracket the harness writes ahead of a composed answer
// reply. It is also the whole-line marker in modelAuthoredMarkerLines, and it is
// named here because this shape checks a prefix of the line rather than the
// whole of it.
const answerMarker = "[answers]"

// isAnswerMarkerWithNote reports whether line starts with the answer marker and
// carries a note after it, and answerFollows says a numbered answer line comes
// later in the reply.
//
// The client reads an edited reply as `[answers] <note>` followed by answer
// lines, and treats that as the composed reply's own content. A bare mention of
// the marker in a sentence does not qualify: the shape needs the marker at the
// line's start, a note after it, and at least one answer line under it.
//
// The answer line may be anywhere later in the reply, not only on the next
// non-blank line, because the client's parseAskAnswerLines reads every line of
// the reply: an edited draft routinely carries a commentary paragraph between
// the note and the answers, and stopping at the first line left that supported
// shape unescaped (#6493). parseAskAnswerLines is TypeScript
// (appwire-client/typescript/askShared.ts) and cannot be called from here, so
// the shape is matched in Go; the two must be kept in step.
func isAnswerMarkerWithNote(line string, answerFollows bool) bool {
	note, ok := strings.CutPrefix(line, answerMarker)
	if !ok || strings.TrimSpace(note) == "" {
		return false
	}
	return answerFollows
}

// lastAnswerOffset returns the byte offset at which the LAST numbered answer line
// in text starts, or a negative value when there is none.
//
// One scan answers "does an answer line follow this marker" for every marker in
// the text: an answer follows a marker exactly when the last answer does. Doing
// it per marker was quadratic in the length of the reply, and ran again on every
// request for as long as the reply stayed in history (#6493).
func lastAnswerOffset(text string) int {
	last := -1
	pos := 0
	for line := range strings.Lines(text) {
		if isAnswerLine(strings.TrimSpace(line)) {
			last = pos
		}
		pos += len(line)
	}
	return last
}

// isAnswerLine reports whether line is one numbered answer line in the client's
// composed form: an ordinal, a full stop, a separator, a bracketed header and the
// arrow the client writes before the resolution text.
//
// The accepted forms are the client's, character for character: its grammar is
// /^(\d+)\.\s(?:\[([^\]\r\n]*)\]|\[("(?:\\.|[^"\\])*")\])\s→\s/ in
// appwire-client/typescript/askShared.ts. Two narrowings here cost a review
// round each, so they are stated rather than left to the code:
//
//   - The separator after the ordinal is \s, matched as the CLIENT matches it
//     rather than as a space-or-tab test would: a nonbreaking space after the
//     ordinal is a separator to the client and not to either, so a reply whose
//     only answer was spaced that way went unrecognized and the marker it
//     answers stayed unescaped (#6493 review).
//   - The header's body is [^\]\r\n]*, which admits an EMPTY header.
//
// The header may also be a JSON string form the client accepts; only its
// brackets and arrow are checked here, because this decides whether a frame
// opener is escaped rather than parsing the reply.
func isAnswerLine(line string) bool {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 || i+1 >= len(line) || line[i] != '.' {
		return false
	}
	sep, sepLen := utf8.DecodeRuneInString(line[i+1:])
	if !isClientSpace(sep) {
		return false
	}
	rest, ok := strings.CutPrefix(line[i+1+sepLen:], "[")
	if !ok {
		return false
	}
	// The header body is anything but the delimiters, including nothing at all.
	header, after, ok := strings.Cut(rest, "]")
	if !ok || strings.ContainsAny(header, "\r\n") {
		return false
	}
	return strings.Contains(after, "→")
}

// isClientSpace reports whether r is one of the characters the client's \s
// matches.
//
// ECMAScript's \s is WhiteSpace plus LineTerminator: the tab, the vertical tab,
// the form feed, the byte-order mark, the line terminators, and every Unicode
// space separator — Zs, which includes both the ordinary space and the
// nonbreaking one. Go's \s is a different set and a space-or-tab test is
// narrower still, so the client's set is spelled out here: the scanner models
// the input the client accepts (#6493 review).
func isClientSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', '\ufeff', '\u2028', '\u2029':
		return true
	}
	return unicode.Is(unicode.Zs, r)
}

// isModelAuthoredMarkerLine reports whether line, trimmed, is exactly one of
// modelAuthoredMarkerLines.
func isModelAuthoredMarkerLine(line string) bool {
	return slices.Contains(modelAuthoredMarkerLines, line)
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

// markdownParser is the CommonMark reader the escape asks which lines are code.
// A goldmark parser keeps no per-parse state — every parse reads the shared
// parser's tables and writes its own context — so one parser serves concurrent
// callers.
var markdownParser = goldmark.DefaultParser()

// codeLineMask reports, for each line of source, whether the CommonMark reader
// parses that line as a code block's content. A fence's own delimiter lines
// need no entry: the reader keeps them out of the block's lines, and a line
// that begins with a fence character opens no frame whatever follows it.
//
// Tabs are expanded before the parse so tab-separated nested list markers align
// to four-column stops consistently with CommonMark column measurement.
func codeLineMask(source string) []bool {
	b := []byte(expandTabs(source))
	starts := lineStarts(b)
	mask := make([]bool, len(starts))
	doc := markdownParser.Parse(text.NewReader(b))
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if n.Kind() != ast.KindCodeBlock && n.Kind() != ast.KindFencedCodeBlock {
			return ast.WalkContinue, nil
		}
		lines := n.Lines()
		for i := 0; i < lines.Len(); i++ {
			mask[lineIndex(starts, lines.At(i).Start)] = true
		}
		return ast.WalkContinue, nil
	})
	return mask
}

// lineStarts returns the byte offset at which each line of b begins, so a line
// index and an offset convert into each other without a rescan.
func lineStarts(b []byte) []int {
	starts := make([]int, 1, 32)
	for i, c := range b {
		if c == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// lineIndex returns the index of the line pos falls on.
func lineIndex(starts []int, pos int) int {
	return max(sort.SearchInts(starts, pos+1)-1, 0)
}

// expandTabs returns source with every tab replaced by spaces to the next
// four-column stop, keeping every line break intact.
func expandTabs(source string) string {
	if !strings.Contains(source, "\t") {
		return source
	}
	var b strings.Builder
	b.Grow(len(source) + 8)
	column := 0
	for i := 0; i < len(source); {
		r, size := utf8.DecodeRuneInString(source[i:])
		switch r {
		case '\n':
			column = 0
		case '\r':
		case '\t':
			spaces := 4 - column%4
			for range spaces {
				b.WriteByte(' ')
			}
			column += spaces
			i += size
			continue
		default:
			column++
		}
		b.WriteString(source[i : i+size])
		i += size
	}
	return b.String()
}

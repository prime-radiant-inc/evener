// Package mdfence scans Markdown fenced code blocks and picks a fence that a
// body's own backtick runs cannot close.
package mdfence

import "strings"

// minFence is the minimum fenced-code-block fence length (CommonMark).
const minFence = 3

// Tracker follows Markdown fenced code blocks line by line. A block opens on a
// line starting with three or more backticks or tildes, and closes on a line of
// the same character, at least as long, with nothing after it.
//
// It also follows a fence opened INSIDE a single list item, the shape a model
// writing a bullet with a fenced example produces (#4435):
//
//   - ~~~text
//     quoted
//     ~~~
//
// Markdown opens the block on the item's first line. The item's content
// indentation is recorded when such a fence opens, and the matching closing
// fence is read at that indentation, so the item's own closing line no longer
// reads as a fresh, top-level opener.
//
// This stays a line-oriented subset, not a CommonMark block parser. Only one
// list container is modelled. A fence's own indentation is spaces; the item
// boundary counts columns, a tab reaching the next four-column stop. Only a
// closing fence at or within three columns past the content indentation closes
// a block; a non-blank line indented less than the content leaves the item, and
// the fence with it. Nested containers, block quotes and lazy continuation
// lines are not modelled. A numbered marker other than one cannot interrupt a
// paragraph in CommonMark; this tracker keeps no paragraph state, so it reads
// any list marker at the line's start the same way.
type Tracker struct {
	char   byte
	length int
	// container is the content indentation of the list item the tracker is
	// inside, which is the column a fence in the item opens at, or 0 outside a
	// list item. It outlives one block, so a second fence in the same item is
	// read at the item's indentation too, not as a new top-level fence.
	container int
}

// Inside reports whether the lines stepped so far leave a block open.
func (t *Tracker) Inside() bool { return t.length > 0 }

// StepInBlock advances over line and reports whether the line belongs to a
// block: its opening or closing delimiter, or content between them. It is the
// per-line skip predicate for a scan that keeps only text outside blocks.
func (t *Tracker) StepInBlock(line string) bool {
	return t.Step(line) || t.Inside()
}

// Step advances over line and reports whether it opened or closed a block.
func (t *Tracker) Step(line string) bool {
	// A non-blank line indented less than the list item's content leaves the
	// item, and any fence the item holds ends with it: the line is read on its
	// own from here, as a new opener or as ordinary content. A blank line stays
	// inside the item.
	if t.container > 0 && strings.TrimSpace(line) != "" && LeadingColumns(line) < t.container {
		t.length, t.container = 0, 0
	}
	if t.length == 0 {
		// A fence at the item's content indentation while the item holds (a
		// top-level fence is the container-0 case), else a fence on a new list
		// item's own line. A list fence is only looked for while no block is
		// open: a "- ~~~" line inside an open block is content.
		if char, n, _ := runAt(line, t.container); n != 0 {
			t.char, t.length = char, n
			return true
		}
		if char, n, container := runInList(line); n != 0 {
			t.char, t.length, t.container = char, n, container
			return true
		}
		return false
	}
	char, n, after := runAt(line, t.container)
	if n == 0 {
		return false
	}
	if char == t.char && n >= t.length && strings.TrimSpace(after) == "" {
		t.length = 0
		return true
	}
	return false
}

// runAt returns the fence a line starts with when it is indented by base
// columns of list-container indentation and then by up to three more spaces:
// its character, its length and the rest of the line. The length is 0 when the
// line is not indented that far or does not start with a fence.
func runAt(line string, base int) (char byte, n int, after string) {
	if len(line) < base || strings.TrimLeft(line[:base], " ") != "" {
		return 0, 0, ""
	}
	line = line[base:]
	indent := leadingSpaces(line)
	if indent > 3 || indent == len(line) || (line[indent] != '`' && line[indent] != '~') {
		return 0, 0, ""
	}
	char = line[indent]
	rest := strings.TrimLeft(line[indent:], string(char))
	n = len(line) - indent - len(rest)
	if n < minFence {
		return 0, 0, ""
	}
	// A backtick fence's info string may not contain a backtick, so a line like
	// "- ```status=done```" is an inline code span, not a fence (#4435).
	if char == '`' && strings.Contains(rest, "`") {
		return 0, 0, ""
	}
	return char, n, rest
}

// runInList returns the fence a line opens inside a single list item: a list
// marker, then one to four spaces, then a fence. container is the item's
// content indentation: the marker and the spaces after it, which is the column
// the fence opens at, and the column the matching closing fence is read from.
// Its fence is 0 when the line is not a fence on a list item.
func runInList(line string) (char byte, n, container int) {
	indent := leadingSpaces(line)
	if indent > 3 || indent == len(line) {
		return 0, 0, 0
	}
	marker, ok := listMarker(line[indent:])
	if !ok {
		return 0, 0, 0
	}
	after := line[indent+marker:]
	spaces := len(after) - len(strings.TrimLeft(after, " "))
	if spaces < 1 || spaces > 4 {
		return 0, 0, 0
	}
	char, n, _ = runAt(after[spaces:], 0)
	if n == 0 {
		return 0, 0, 0
	}
	// The item's content indentation is the marker and the spaces that follow
	// it; the opening fence sits at its start. A closing fence may sit up to
	// three columns further (runAt allows that), but not less.
	return char, n, indent + marker + spaces
}

// leadingSpaces returns the spaces line starts with. Fence indentation is
// spaces; a tab is content at this level, not indentation.
func leadingSpaces(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

// listMarker returns the length in bytes of the list marker line starts with:
// "-", "+" or "*", or one to nine digits followed by "." or ")". The space that
// must follow the marker is not part of it.
func listMarker(line string) (int, bool) {
	switch line[0] {
	case '-', '+', '*':
		return 1, true
	}
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 || i > 9 || i >= len(line) || (line[i] != '.' && line[i] != ')') {
		return 0, false
	}
	return i + 1, true
}

// LeadingColumns returns the columns of whitespace a line starts with, a tab
// reaching the next four-column stop. A blank line is all its whitespace.
func LeadingColumns(line string) int {
	columns := 0
	for _, r := range line {
		switch r {
		case ' ':
			columns++
		case '\t':
			columns += 4 - columns%4
		default:
			return columns
		}
	}
	return columns
}

// Pick returns a backtick fence guaranteed not to be closed by any backtick run
// inside body: one backtick longer than the longest run found, never fewer than
// minFence.
func Pick(body string) string {
	n := max(longestBacktickRun(body)+1, minFence)
	return strings.Repeat("`", n)
}

// longestBacktickRun returns the length of the longest run of consecutive
// backtick characters in s (0 if there are none).
func longestBacktickRun(s string) int {
	longest, run := 0, 0
	for _, r := range s {
		if r == '`' {
			run++
			if run > longest {
				longest = run
			}
			continue
		}
		run = 0
	}
	return longest
}

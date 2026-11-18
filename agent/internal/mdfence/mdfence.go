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

// StepInBlockIn advances over line within the list item whose content
// indentation is container, and reports whether the line belongs to a block.
//
// It is StepInBlock for a caller that already tracks list scope, and it exists
// because that caller knows something this tracker cannot infer from the line
// alone: the line is a continuation of an item rather than top-level text. A
// fence opened there belongs to the item exactly as one opened on the marker
// line does, and Markdown ends it when the item ends. Without the scope the
// fence reads as a document-level block that nothing closes, so every later
// top-level line is swallowed as its content (#6493 review).
//
// container is the scope for this line, 0 outside any item, so a caller must
// reset it on the line that leaves the item. It is adopted whenever no block is
// open: a block's own content may look like a list, and what it holds is quoted
// text rather than structure, so the scope is not changed while one is open.
//
// The supplied scope is the CURRENT item's, which may be nested inside the one
// already recorded, so adopting it is not the same as filling an empty slot. A
// fence opened on a nested item's continuation line belongs to that item, and
// keeping the enclosing item's indentation instead would leave the line that
// dedents back to the enclosing item at or past the recorded indent, so the
// block would survive the dedent and swallow that item's prose.
func (t *Tracker) StepInBlockIn(line string, container int) bool {
	// base is the item the line sits inside as this tracker recorded it, which
	// is the enclosing item when the line opens a nested one. A marker line
	// deeper than the page is still an item's marker line, and runInList reads
	// it from this indentation rather than from column zero (#6493).
	base := t.container
	if t.length == 0 {
		// The caller resolved this line's scope, so it applies as given: a fence
		// opened here belongs to the current item, which may be nested inside
		// the one recorded, and the line has not left anything.
		t.container = container
		return t.stepResolved(line, base) || t.Inside()
	} else if leavesItem(line, t.container) {
		// A block ends with the item that holds it, and the line that ends it
		// belongs to the enclosing scope the caller reports rather than to the
		// top level. Process it in that scope, so a fence it opens is read as
		// the enclosing item's.
		t.length, t.container = 0, container
		return t.stepResolved(line, base) || t.Inside()
	}
	return t.stepOpen(line) || t.Inside()
}

// leavesItem reports whether line is non-blank and indented less than the
// content indentation of the item the tracker is inside. A blank line stays
// inside the item: it neither ends one nor opens code.
func leavesItem(line string, container int) bool {
	return container > 0 && strings.TrimSpace(line) != "" && LeadingColumns(line) < container
}

// LeavesItem reports whether line leaves the list item the tracker is inside,
// and so ends any block that item holds.
//
// It is StepInBlockIn's own item-exit test, exported for a caller that must
// classify the same line and cannot wait for the step to be taken: the line
// that ends a block belongs to the enclosing item, so a caller deciding
// whether that line is indented code has to resolve its list scope before it
// steps, not go on using the empty scope an open block imposes.
func (t *Tracker) LeavesItem(line string) bool {
	return leavesItem(line, t.container)
}

// Step advances over line and reports whether it opened or closed a block.
func (t *Tracker) Step(line string) bool {
	// A line that leaves the item ends any fence the item holds: it is read on
	// its own from here, as a new opener or as ordinary content.
	base := t.container
	if leavesItem(line, t.container) {
		t.length, t.container = 0, 0
	}
	return t.stepResolved(line, base)
}

// stepResolved advances over line in the recorded scope, without deriving a
// dedent from it: the caller of StepInBlockIn has already resolved the line's
// scope and recorded it.
//
// base is the item the line sits inside as recorded before this line, which is
// the indentation a nested item's marker line is read from. It differs from
// t.container when the caller has already moved the tracker into the item the
// line opens.
func (t *Tracker) stepResolved(line string, base int) bool {
	if t.length == 0 {
		// A fence at the item's content indentation while the item holds (a
		// top-level fence is the container-0 case), else a fence on a new list
		// item's own line. A list fence is only looked for while no block is
		// open: a "- ~~~" line inside an open block is content.
		if char, n, _ := runAt(line, t.container); n != 0 {
			t.char, t.length = char, n
			return true
		}
		if char, n, container := runInList(line, base); n != 0 {
			t.char, t.length, t.container = char, n, container
			return true
		}
		return false
	}
	return t.stepOpen(line)
}

// stepOpen advances over line while a block is open, reporting whether the line
// closed it.
func (t *Tracker) stepOpen(line string) bool {
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
	offset, ok := indentEnd(line, base)
	if !ok {
		return 0, 0, ""
	}
	line = line[offset:]
	indent := LeadingSpaces(line)
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
//
// base is the content indentation of the item the marker sits inside: 0 at the
// top level, and the enclosing item's content indentation for a nested item,
// whose marker line is indented past the enclosing item's own text. A nested
// marker line is a list item's marker line like any other, so the fence on it
// belongs to the item it opens rather than to the page (#6493).
func runInList(line string, base int) (char byte, n, container int) {
	// The marker is read from the item the line sits inside. A line indented
	// less than that item's content has left it — a sibling item, or one
	// further out — so its marker is read from the page instead. Resolving this
	// here, after any dedent, is what keeps a sibling item's own fence readable:
	// searching from the previous item's column missed the marker, and the
	// item's closing delimiter then read as a fresh opener.
	offset, ok := indentEnd(line, base)
	if !ok {
		offset, ok = indentEnd(line, 0)
		if !ok {
			return 0, 0, 0
		}
	}
	baseColumns := LeadingColumns(line[:offset])
	line = line[offset:]
	indent := LeadingSpaces(line)
	if indent > 3 || indent == len(line) {
		return 0, 0, 0
	}
	marker, ok := ListMarker(line[indent:])
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
	//
	// It is counted in columns, since that is what every reader compares it
	// against: leavesItem and the escape's four-column code threshold both
	// measure the line in columns, and a tab in the prefix would put the two
	// measures asunder.
	return char, n, baseColumns + indent + marker + spaces
}

// LeadingSpaces returns the ASCII spaces line starts with. Fence indentation is
// spaces; a tab is content at this level, not indentation.
func LeadingSpaces(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

// ListMarker returns the length in bytes of the list marker line starts with:
// "-", "+" or "*", or one to nine digits followed by "." or ")". The space that
// must follow the marker is not part of it.
func ListMarker(line string) (int, bool) {
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

// indentEnd returns the byte offset at which line has consumed base columns of
// indentation, and whether it has that much.
//
// Indentation is counted in COLUMNS, because that is what it is: a tab reaches
// the next four-column stop, so one tab can supply the four columns of a
// two-digit ordered item's content indentation. Reading that prefix as literal
// spaces rejected the tab, so a fence or delimiter aligned with the item's
// content went unrecognized (#6493). The bytes themselves are untouched: the
// caller slices at the returned offset and keeps whatever it finds there.
func indentEnd(line string, base int) (int, bool) {
	if base <= 0 {
		return 0, true
	}
	columns := 0
	for i := 0; i < len(line); i++ {
		if columns >= base {
			return i, true
		}
		switch line[i] {
		case ' ':
			columns++
		case '\t':
			columns += 4 - columns%4
		default:
			return 0, false
		}
	}
	return 0, columns >= base
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

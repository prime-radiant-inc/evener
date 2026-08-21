// render.go turns parsed Targets into the markdown that lives inside each
// family doc's marked "## Targets" region, and splices that markdown back
// into the doc without disturbing anything outside the region — the doc's
// hand-written prose is the load-bearing content here, not the generated
// table.
package main

import (
	"bytes"
	"fmt"
	"strings"
)

// beginMarker returns the exact BEGIN comment line a family's marked
// region opens with (spec §3). It doubles as the search key RewriteRegion
// uses to find that family's region and nobody else's.
func beginMarker(family string) string {
	return beginMarkerPrefix + family + beginMarkerSuffix + "\n"
}

const (
	beginMarkerPrefix = "<!-- BEGIN GENERATED: make targets. Edit make/"
	beginMarkerSuffix = ".mk, then run `make generate`. -->"
	endMarker         = "<!-- END GENERATED -->"
)

// genericBeginPrefix is enough of the BEGIN marker to detect "some
// GENERATED region exists in this doc" without pinning it to one family,
// so RewriteRegion can tell "no marked region at all" apart from "a marked
// region, but for a different family" when its exact beginMarker(family)
// search misses.
const genericBeginPrefix = "<!-- BEGIN GENERATED: make targets. Edit "

type sourceLine struct {
	start, next int
	text        []byte
	ending      string
}

func walkSourceLines(src []byte, visit func(sourceLine) bool) {
	for offset := 0; offset < len(src); {
		lineEnd := bytes.IndexByte(src[offset:], '\n')
		if lineEnd == -1 {
			lineEnd = len(src) - offset
		}
		textEnd := offset + lineEnd
		next := textEnd
		if next < len(src) {
			next++
		}
		line := sourceLine{start: offset, next: next, text: src[offset:textEnd]}
		if len(line.text) > 0 && line.text[len(line.text)-1] == '\r' {
			line.text = line.text[:len(line.text)-1]
			textEnd--
		}
		line.ending = string(src[textEnd:next])
		if !visit(line) {
			return
		}
		offset = next
	}
}

// markerLineIndex returns the byte offset of marker when it appears as a
// complete line in src. Annotation text may contain the same literal string;
// only the structural marker line delimits a generated region.
func markerLineIndex(src []byte, marker string) int {
	start, _, _, ok := findMarkerLine(src, marker)
	if !ok {
		return -1
	}
	return start
}

// findMarkerLine locates one exact standalone marker line. next is the byte
// after its line ending, and lineEnding preserves the document's LF or CRLF.
func findMarkerLine(src []byte, marker string) (start, next int, lineEnding string, ok bool) {
	walkSourceLines(src, func(line sourceLine) bool {
		if !bytes.Equal(line.text, []byte(marker)) {
			return true
		}
		start, next, lineEnding, ok = line.start, line.next, line.ending, true
		return false
	})
	return start, next, lineEnding, ok
}

func validateGeneratedRegionMarkers(doc []byte) error {
	beginCount, validBeginCount, endCount := 0, 0, 0
	open := false
	var orderErr error
	walkSourceLines(doc, func(line sourceLine) bool {
		if bytes.HasPrefix(line.text, []byte(genericBeginPrefix)) {
			beginCount++
			if _, ok := generatedRegionFamily(line.text); ok {
				validBeginCount++
			}
			open = true
			return true
		}
		if bytes.Equal(line.text, []byte(endMarker)) {
			endCount++
			if !open && orderErr == nil {
				orderErr = fmt.Errorf("doc has an END marker before its GENERATED BEGIN marker")
			}
			open = false
		}
		return true
	})
	if beginCount == 0 && endCount == 0 {
		return nil
	}
	if orderErr != nil {
		return orderErr
	}
	if validBeginCount != beginCount {
		return fmt.Errorf("doc has %d malformed GENERATED BEGIN marker lines", beginCount-validBeginCount)
	}
	if beginCount != 1 {
		return fmt.Errorf("doc has %d GENERATED regions; each family doc must have exactly one", beginCount)
	}
	if endCount != 1 {
		return fmt.Errorf("doc has %d %q marker lines; each family doc must have exactly one", endCount, endMarker)
	}
	if open {
		return fmt.Errorf("doc has a GENERATED BEGIN marker with no following %q line", endMarker)
	}
	return nil
}

func generatedRegionFamily(line []byte) (string, bool) {
	after, ok := strings.CutPrefix(string(line), beginMarkerPrefix)
	if !ok {
		return "", false
	}
	family, ok := strings.CutSuffix(after, beginMarkerSuffix)
	return family, ok && family != ""
}

// generatedRegionFamilies returns every family named by a structurally valid
// standalone BEGIN marker line. It deliberately ignores marker-like text in
// generated table cells.
func generatedRegionFamilies(src []byte) []string {
	var families []string
	walkSourceLines(src, func(line sourceLine) bool {
		if family, ok := generatedRegionFamily(line.text); ok {
			families = append(families, family)
		}
		return true
	})
	return families
}

// RewriteRegion replaces the content of family's marked "## Targets"
// region inside doc with body, leaving everything else — including the
// BEGIN/END marker lines themselves — byte-identical. body is written with
// exactly one trailing newline before the END marker; an empty body (a
// family with zero targets, which does not happen today) leaves the region
// empty exactly as the six docs start out.
//
// It is a hard error if doc has no marked region for family at all, or if
// a BEGIN marker is found with no matching END before EOF.
func RewriteRegion(doc []byte, family string, body string) ([]byte, error) {
	begin := beginMarker(family)
	if err := validateGeneratedRegionMarkers(doc); err != nil {
		return nil, err
	}
	_, regionStart, lineEnding, found := findMarkerLine(doc, strings.TrimSuffix(begin, "\n"))
	if !found {
		if bytes.Contains(doc, []byte(genericBeginPrefix)) {
			return nil, fmt.Errorf("doc has a GENERATED region, but not one for family %q (want marker %q)", family, strings.TrimSuffix(begin, "\n"))
		}
		return nil, fmt.Errorf("doc has no marked region for family %q (want marker %q)", family, strings.TrimSuffix(begin, "\n"))
	}
	if lineEnding == "" {
		return nil, fmt.Errorf("family %q's BEGIN marker has no line ending before its body", family)
	}

	endIdx := markerLineIndex(doc[regionStart:], endMarker)
	if endIdx == -1 {
		return nil, fmt.Errorf("family %q's region has a BEGIN marker with no matching %q before EOF", family, endMarker)
	}
	regionEnd := regionStart + endIdx

	var replacement string
	if body != "" {
		body = strings.ReplaceAll(body, "\r\n", "\n")
		replacement = strings.ReplaceAll(body, "\n", lineEnding) + lineEnding
	}

	out := make([]byte, 0, len(doc)-(regionEnd-regionStart)+len(replacement))
	out = append(out, doc[:regionStart]...)
	out = append(out, replacement...)
	out = append(out, doc[regionEnd:]...)
	return out, nil
}

// hasFields reports whether t carries at least one structured field. A
// target with only a summary renders in the compact list instead of the
// wide table, per spec §3.
func (t Target) hasFields() bool {
	return t.Proves != "" || t.Trigger != "" || t.Requires != "" || t.FailsWhen != ""
}

// escapeCell makes s safe to place inside a single markdown table cell: a
// literal "|" would otherwise be read as a column separator and split the
// row. No annotation in make/*.mk contains one today, but the render must
// not silently corrupt the table if a future one does.
func escapeCell(s string) string {
	return strings.ReplaceAll(s, "|", "\\|")
}

// codeSpan wraps s in a backtick fence longer than any run it contains, so a
// valid Make target carrying backticks cannot terminate the Markdown span.
func codeSpan(s string) string {
	fence := "`"
	for strings.Contains(s, fence) {
		fence += "`"
	}
	return fence + s + fence
}

// command renders a target's name as the `make <name>` command cell shared
// by both table shapes. The whole span is escaped for its surrounding table.
func command(name string) string {
	return escapeCell(codeSpan("make " + name))
}

// Render turns targets (as returned by ParseFamily, in file order) into the
// markdown body for one family's marked "## Targets" region.
//
// Targets carrying at least one structured field render as rows of a wide
// six-column table. Targets with only a summary render as rows of a
// compact two-column list under an "Other targets" subheading, so a target
// like `clean` does not get four empty wide-table cells. If no target in
// the family has any structured field, only the compact list is emitted —
// never an empty (header-only) wide table above it, and with no "Other
// targets" subheading, since nothing above it exists for the list to be
// "other" than.
//
// The returned string has no trailing newline; RewriteRegion supplies the
// one blank line that separates it from the END marker.
func Render(targets []Target) string {
	var wide, compact []Target
	for _, t := range targets {
		if t.hasFields() {
			wide = append(wide, t)
		} else {
			compact = append(compact, t)
		}
	}

	var sections []string
	if len(wide) > 0 {
		sections = append(sections, renderWideTable(wide))
	}
	if len(compact) > 0 {
		sections = append(sections, renderCompactList(compact, len(wide) > 0))
	}
	return strings.Join(sections, "\n\n")
}

// wideTableHeader is the wide table's header row. Summary sits second, right
// after Command, so a row reads what-it-is, what-it-guarantees, when, needs,
// fails (ruling R25).
//
// Publishing the summary is what puts a gate target's summary under
// lint-generated at all. Without this column the wide table showed only the
// four structured fields, so a gate target's summary was required by
// TestEveryTargetHasASummaryAnnotation, printed by `make help`, and rendered
// in no doc — which left it gated by nothing, for 36 of the repository's 65
// targets. On a single-fact gate like lint-naming the summary and `proves`
// mildly restate each other; that overlap is inherent to single-fact gates
// and is not a reason to reword an annotation.
const wideTableHeader = "| Command | Summary | What it proves | Trigger | Requires | Fails when |\n" +
	"| --- | --- | --- | --- | --- | --- |"

func renderWideTable(targets []Target) string {
	var b strings.Builder
	b.WriteString(wideTableHeader)
	for _, t := range targets {
		fmt.Fprintf(&b, "\n| %s | %s | %s | %s | %s | %s |",
			command(t.Name), escapeCell(t.Summary), escapeCell(t.Proves),
			escapeCell(t.Trigger), escapeCell(t.Requires), escapeCell(t.FailsWhen))
	}
	return b.String()
}

// renderCompactList renders targets as the two-column Command/Summary list.
// withHeading controls the "### Other targets" subheading: it belongs only
// when a wide table precedes this list in the same region, so the list is
// genuinely "other" than something. A family whose targets all lack fields
// has no wide table, and the compact list is the whole region — no heading.
func renderCompactList(targets []Target, withHeading bool) string {
	var b strings.Builder
	if withHeading {
		b.WriteString("### Other targets\n\n")
	}
	b.WriteString("| Command | Summary |\n")
	b.WriteString("| --- | --- |")
	for _, t := range targets {
		fmt.Fprintf(&b, "\n| %s | %s |", command(t.Name), escapeCell(t.Summary))
	}
	return b.String()
}

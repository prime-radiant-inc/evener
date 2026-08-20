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
	return fmt.Sprintf("<!-- BEGIN GENERATED: make targets. Edit make/%s.mk, then run `make generate`. -->\n", family)
}

const endMarker = "<!-- END GENERATED -->"

// genericBeginPrefix is enough of the BEGIN marker to detect "some
// GENERATED region exists in this doc" without pinning it to one family,
// so RewriteRegion can tell "no marked region at all" apart from "a marked
// region, but for a different family" when its exact beginMarker(family)
// search misses.
const genericBeginPrefix = "<!-- BEGIN GENERATED: make targets. Edit "

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
	start := bytes.Index(doc, []byte(begin))
	if start == -1 {
		if bytes.Contains(doc, []byte(genericBeginPrefix)) {
			return nil, fmt.Errorf("doc has a GENERATED region, but not one for family %q (want marker %q)", family, strings.TrimSuffix(begin, "\n"))
		}
		return nil, fmt.Errorf("doc has no marked region for family %q (want marker %q)", family, strings.TrimSuffix(begin, "\n"))
	}
	regionStart := start + len(begin)

	endIdx := bytes.Index(doc[regionStart:], []byte(endMarker))
	if endIdx == -1 {
		return nil, fmt.Errorf("family %q's region has a BEGIN marker with no matching %q before EOF", family, endMarker)
	}
	regionEnd := regionStart + endIdx

	var replacement string
	if body != "" {
		replacement = body + "\n"
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

// command renders a target's name as the `make <name>` command cell shared
// by both table shapes.
func command(name string) string {
	return fmt.Sprintf("`make %s`", name)
}

// Render turns targets (as returned by ParseFamily, in file order) into the
// markdown body for one family's marked "## Targets" region.
//
// Targets carrying at least one structured field render as rows of a wide
// five-column table. Targets with only a summary render as rows of a
// compact two-column list under an "Other targets" subheading, so a target
// like `clean` does not get four empty wide-table cells. If no target in
// the family has any structured field, only the compact list is emitted —
// never an empty (header-only) wide table above it.
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
		sections = append(sections, renderCompactList(compact))
	}
	return strings.Join(sections, "\n\n")
}

func renderWideTable(targets []Target) string {
	var b strings.Builder
	b.WriteString("| Command | What it proves | Trigger | Requires | Fails when |\n")
	b.WriteString("| --- | --- | --- | --- | --- |")
	for _, t := range targets {
		fmt.Fprintf(&b, "\n| %s | %s | %s | %s | %s |",
			command(t.Name), escapeCell(t.Proves), escapeCell(t.Trigger), escapeCell(t.Requires), escapeCell(t.FailsWhen))
	}
	return b.String()
}

func renderCompactList(targets []Target) string {
	var b strings.Builder
	b.WriteString("### Other targets\n\n")
	b.WriteString("| Command | Summary |\n")
	b.WriteString("| --- | --- |")
	for _, t := range targets {
		fmt.Fprintf(&b, "\n| %s | %s |", command(t.Name), escapeCell(t.Summary))
	}
	return b.String()
}

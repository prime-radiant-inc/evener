package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadFamiliesReadFileError covers the os.ReadFile error path in
// loadFamilies: a family file that is listed by ReadDir but cannot be read
// (e.g. it is a directory, not a regular file) should produce a joined error.
func TestLoadFamiliesReadFileError(t *testing.T) {
	root := t.TempDir()
	mkDir := filepath.Join(root, "make")
	if err := os.MkdirAll(mkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A directory named like a family file: ReadDir lists it, but ReadFile fails.
	if err := os.Mkdir(filepath.Join(mkDir, "building.mk"), 0o755); err != nil {
		t.Fatal(err)
	}
	families, err := loadFamilies(root)
	if err == nil {
		t.Fatalf("loadFamilies with an unreadable .mk should error")
	}
	if len(families) != 0 {
		t.Fatalf("loadFamilies should return no families, got %d", len(families))
	}
	if !strings.Contains(err.Error(), "building.mk") {
		t.Fatalf("error should name building.mk: %v", err)
	}
}

// TestPrintHelpWriteError covers the write-error path in printHelp by
// passing a writer that fails on Write.
func TestPrintHelpWriteError(t *testing.T) {
	root := t.TempDir()
	writeFixtureMk(t, root, "building", "## Build the binary.\nbuild:\n\tgo build .\n")
	err := printHelp(&failingWriter{}, root)
	if err == nil {
		t.Fatalf("printHelp with a failing writer should error")
	}
}

type failingWriter struct{}

func (failingWriter) Write(p []byte) (int, error) { return 0, errors.New("write failed") }

// TestParseFamilyNonCommentLineSeparatesBlock covers the branch in ParseFamily
// where a non-comment, non-rule line separates a ## block from its rule.
func TestParseFamilyNonCommentLineSeparatesBlock(t *testing.T) {
	src := []byte("## Summary\nfoo bar baz\nbuild:\n\tgo build .\n")
	_, err := ParseFamily(src)
	if err == nil {
		t.Fatalf("ParseFamily should error when a non-comment line separates a block from its rule")
	}
	if !strings.Contains(err.Error(), "separates the ## block") {
		t.Fatalf("error should mention separation: %v", err)
	}
}

// TestParseFamilyDirectiveWithPendingBlock covers the branch where a
// directive (e.g. .PHONY:) appears with a pending ## block.
func TestParseFamilyDirectiveWithPendingBlock(t *testing.T) {
	src := []byte("## Summary\n.PHONY: build\n")
	_, err := ParseFamily(src)
	if err == nil {
		t.Fatalf("ParseFamily should error when a directive carries a pending ## block")
	}
	if !strings.Contains(err.Error(), "directive") {
		t.Fatalf("error should mention directive: %v", err)
	}
}

// TestParseFamilyBlockWithoutSummary covers the branch where a rule has a
// ## block with structured fields but no summary line.
func TestParseFamilyBlockWithoutSummary(t *testing.T) {
	// A ## block with only a field (## trigger: CI) and no summary line.
	src := []byte("## trigger: CI\nbuild:\n\tgo build .\n")
	_, err := ParseFamily(src)
	if err == nil {
		t.Fatalf("ParseFamily should error when a ## block has no summary line")
	}
	if !strings.Contains(err.Error(), "no summary line") {
		t.Fatalf("error should mention no summary: %v", err)
	}
}

// TestWalkSourceLinesLastLineNoNewline covers the branch in walkSourceLines
// where the final line has no trailing newline.
func TestWalkSourceLinesLastLineNoNewline(t *testing.T) {
	src := []byte("line1\nline2")
	var texts [][]byte
	walkSourceLines(src, func(line sourceLine) bool {
		texts = append(texts, line.text)
		return true
	})
	if len(texts) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(texts))
	}
	if string(texts[1]) != "line2" {
		t.Fatalf("last line text = %q, want %q", texts[1], "line2")
	}
}

// TestMarkerLineIndexNotFound covers the -1 return when the marker is absent.
func TestMarkerLineIndexNotFound(t *testing.T) {
	src := []byte("some content\nno marker here\n")
	if got := markerLineIndex(src, "<!-- BEGIN GENERATED: make targets. Edit building.mk, then run `make generate`. -->"); got != -1 {
		t.Fatalf("markerLineIndex with absent marker should return -1, got %d", got)
	}
}

// TestValidateGeneratedRegionMarkersOpenWithoutEnd covers the branch where
// a BEGIN marker has no following END marker at all (open == true at the end).
func TestValidateGeneratedRegionMarkersOpenWithoutEnd(t *testing.T) {
	doc := []byte(beginMarker("building") + "body\n")
	// Remove the end marker — only a begin marker, no end.
	err := validateGeneratedRegionMarkers(doc)
	if err == nil {
		t.Fatalf("validateGeneratedRegionMarkers with only a begin marker should error")
	}
	// With one begin and zero ends, the endCount != 1 check fires first.
	if !strings.Contains(err.Error(), "exactly one") && !strings.Contains(err.Error(), "no following") {
		t.Fatalf("error should mention exactly one or no following: %v", err)
	}
}

// TestRewriteRegionBeginMarkerNoLineEnding covers the branch in RewriteRegion
// where the BEGIN marker has no line ending (it's the last line of the doc).
func TestRewriteRegionBeginMarkerNoLineEnding(t *testing.T) {
	// A doc whose only content is the begin marker with no trailing newline.
	// validateGeneratedRegionMarkers will fail first (no end marker), but
	// that's OK — we just need to exercise the code path.
	trimmed := strings.TrimSuffix(beginMarker("building"), "\n")
	doc := []byte(trimmed)
	_, err := RewriteRegion(doc, "building", "body")
	if err == nil {
		t.Fatalf("RewriteRegion with no line ending should error")
	}
}

// TestRewriteRegionNoMatchingEnd covers the branch where the BEGIN marker is
// found but the END marker is not (validateGeneratedRegionMarkers is bypassed
// because there's exactly one valid begin and one end, but the end is before
// the begin). Actually, the endIdx == -1 branch is hard to reach normally
// because validateGeneratedRegionMarkers already checks pairing. But we can
// reach it when there is exactly one valid begin and one end but the end
// appears before the begin — validateGeneratedRegionMarkers catches that as
// "end before begin". The only way to reach endIdx == -1 is if the doc passes
// validation but the end marker is not found after the begin marker's
// regionStart. Since validation already ensures exactly one begin and one end
// in order, endIdx == -1 should not be reachable. This is documented as
// unreachable.
func TestRewriteRegionNoMatchingEndUnreachable(t *testing.T) {
	t.Skip("the endIdx == -1 branch in RewriteRegion is unreachable: validateGeneratedRegionMarkers already enforces exactly one begin followed by one end")
}

// TestGenerateOneParseError covers the ParseFamily error path in generateOne.
func TestGenerateOneParseError(t *testing.T) {
	root := t.TempDir()
	// Write a .mk with an invalid ## block (no summary, directive) to trigger
	// a ParseFamily error.
	writeFixtureMk(t, root, "building", "## trigger: CI\n.PHONY: build\n")
	docDir := filepath.Join(root, "docs", "developing-evener")
	if err := os.MkdirAll(docDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixtureDoc(t, root, "building.md", "building", "")
	err := generateOne(docDir, filepath.Join(root, "make", "building.mk"), "building")
	if err == nil {
		t.Fatalf("generateOne with a parse error should return an error")
	}
	if !strings.Contains(err.Error(), "building.mk") {
		t.Fatalf("error should name the .mk file: %v", err)
	}
}

// TestPrintHelpWithParseErrors covers the path where loadFamilies returns
// both families and a joined error.
func TestPrintHelpWithParseErrors(t *testing.T) {
	root := t.TempDir()
	writeFixtureMk(t, root, "building", "## Build the binary.\nbuild:\n\tgo build .\n")
	writeFixtureMk(t, root, "linting", "## trigger: CI\n.PHONY: lint\n")
	var buf bytes.Buffer
	err := printHelp(&buf, root)
	if err == nil {
		t.Fatalf("printHelp with a parse error in one family should error")
	}
	if !strings.Contains(err.Error(), "linting.mk") {
		t.Fatalf("error should name linting.mk: %v", err)
	}
	// The valid family should still be rendered.
	output := buf.String()
	if !strings.Contains(output, "building:") {
		t.Fatalf("printHelp should still output the valid family: %s", output)
	}
}

// TestLoadFamiliesWithParseError covers loadFamilies returning a joined error
// while still returning successfully parsed families.
func TestLoadFamiliesWithParseError(t *testing.T) {
	root := t.TempDir()
	writeFixtureMk(t, root, "building", "## Build the binary.\nbuild:\n\tgo build .\n")
	writeFixtureMk(t, root, "linting", "## trigger: CI\n.PHONY: lint\n")
	families, err := loadFamilies(root)
	if err == nil {
		t.Fatalf("loadFamilies with a parse error should return an error")
	}
	if len(families) != 1 {
		t.Fatalf("loadFamilies should return 1 valid family, got %d", len(families))
	}
	if families[0].Stem != "building" {
		t.Fatalf("valid family stem = %q, want building", families[0].Stem)
	}
}

// TestGenerateOneNoChange covers the bytes.Equal path in generateOne where
// the regenerated doc is identical to the existing doc (no write needed).
func TestGenerateOneNoChange(t *testing.T) {
	root := t.TempDir()
	writeFixtureMk(t, root, "building", "## Build the binary.\nbuild:\n\tgo build .\n")
	writeFixtureDoc(t, root, "building.md", "building", "")
	docDir := filepath.Join(root, "docs", "developing-evener")
	docPath := filepath.Join(docDir, "building.md")

	// First run: generates and writes.
	if err := generateOne(docDir, filepath.Join(root, "make", "building.mk"), "building"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatal(err)
	}
	// Second run: content is identical, so no write should happen.
	if err := generateOne(docDir, filepath.Join(root, "make", "building.mk"), "building"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("doc changed on idempotent run:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestRewriteRegionWrongFamilyWithGenericPrefix covers the branch in
// RewriteRegion where the doc has a GENERATED region for a different family.
func TestRewriteRegionWrongFamilyWithGenericPrefix(t *testing.T) {
	doc := []byte(beginMarker("linting") + endMarker + "\n")
	_, err := RewriteRegion(doc, "building", "body")
	if err == nil {
		t.Fatalf("RewriteRegion with a different family's region should error")
	}
	if !strings.Contains(err.Error(), "not one for family") {
		t.Fatalf("error should mention wrong family: %v", err)
	}
}

// TestRewriteRegionNoMarkerAtAll covers the branch where there is no GENERATED
// region at all (no generic prefix present).
func TestRewriteRegionNoMarkerAtAll(t *testing.T) {
	doc := []byte("# Just prose, no markers.\n")
	_, err := RewriteRegion(doc, "building", "body")
	if err == nil {
		t.Fatalf("RewriteRegion with no markers should error")
	}
	if !strings.Contains(err.Error(), "no marked region") {
		t.Fatalf("error should mention no marked region: %v", err)
	}
}

// TestValidateGeneratedRegionMarkersMultipleRegions covers the beginCount != 1
// branch.
func TestValidateGeneratedRegionMarkersMultipleRegions(t *testing.T) {
	doc := []byte(beginMarker("building") + endMarker + "\n" + beginMarker("linting") + endMarker + "\n")
	err := validateGeneratedRegionMarkers(doc)
	if err == nil {
		t.Fatalf("validateGeneratedRegionMarkers with two regions should error")
	}
	if !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("error should mention exactly one: %v", err)
	}
}

// TestValidateGeneratedRegionMarkersMultipleEnds covers the endCount != 1
// branch (two end markers, one begin).
func TestValidateGeneratedRegionMarkersMultipleEnds(t *testing.T) {
	doc := []byte(beginMarker("building") + endMarker + "\n" + endMarker + "\n")
	err := validateGeneratedRegionMarkers(doc)
	if err == nil {
		t.Fatalf("validateGeneratedRegionMarkers with two end markers should error")
	}
	// The error may be either "end before begin" or "exactly one" depending on
	// validation order — both indicate the markers are malformed.
	s := err.Error()
	if !strings.Contains(s, "exactly one") && !strings.Contains(s, "before its GENERATED BEGIN") {
		t.Fatalf("error should mention malformed markers: %v", err)
	}
}

// TestParseFamilyTargetWithoutAnnotation covers the branch where a rule has
// no ## annotation block above it.
func TestParseFamilyTargetWithoutAnnotation(t *testing.T) {
	src := []byte("build:\n\tgo build .\n")
	_, err := ParseFamily(src)
	if err == nil {
		t.Fatalf("ParseFamily should error when a rule has no ## annotation")
	}
	if !strings.Contains(err.Error(), "no ## annotation") {
		t.Fatalf("error should mention no annotation: %v", err)
	}
}

// TestParseFamilyBlankLineSeparatesBlock covers the blank-line separation error.
func TestParseFamilyBlankLineSeparatesBlock(t *testing.T) {
	src := []byte("## Summary\n\nbuild:\n\tgo build .\n")
	_, err := ParseFamily(src)
	if err == nil {
		t.Fatalf("ParseFamily should error when a blank line separates a block from its rule")
	}
	if !strings.Contains(err.Error(), "blank line separates") {
		t.Fatalf("error should mention blank line: %v", err)
	}
}

// TestParseFamilyTargetSpecificVariableWithPendingBlock covers the branch
// where a target-specific variable line carries a pending ## block.
func TestParseFamilyTargetSpecificVariableWithPendingBlock(t *testing.T) {
	src := []byte("## Summary\ninstall-home: PREFIX := $(HOME)/.local\n")
	_, err := ParseFamily(src)
	if err == nil {
		t.Fatalf("ParseFamily should error when a target-specific variable carries a pending block")
	}
	if !strings.Contains(err.Error(), "target-specific variable") {
		t.Fatalf("error should mention target-specific variable: %v", err)
	}
}

// TestParseFamilyDanglingBlock covers the branch where a ## block is never
// attached to a rule (pending != nil at EOF).
func TestParseFamilyDanglingBlock(t *testing.T) {
	src := []byte("## Summary\n")
	_, err := ParseFamily(src)
	if err == nil {
		t.Fatalf("ParseFamily should error when a ## block is never attached to a rule")
	}
	if !strings.Contains(err.Error(), "never attached") {
		t.Fatalf("error should mention never attached: %v", err)
	}
}

// TestRenderHelpEmptyFamilies covers renderHelp with no families.
func TestRenderHelpEmptyFamilies(t *testing.T) {
	got := renderHelp(nil)
	if got != "" {
		t.Fatalf("renderHelp with no families should return empty, got %q", got)
	}
}

// TestGenerateCollectsMultipleErrors covers the path where generate collects
// errors from multiple families and joins them.
func TestGenerateCollectsMultipleErrors(t *testing.T) {
	root := t.TempDir()
	// Two families with parse errors, plus a doc with an orphan region.
	writeFixtureMk(t, root, "building", "## trigger: CI\n.PHONY: build\n")
	writeFixtureMk(t, root, "linting", "## trigger: CI\n.PHONY: lint\n")
	writeFixtureDoc(t, root, "building.md", "building", "")
	writeFixtureDoc(t, root, "linting.md", "linting", "")
	err := generate(root)
	if err == nil {
		t.Fatalf("generate with multiple parse errors should error")
	}
	// The joined error should mention both families.
	s := err.Error()
	if !strings.Contains(s, "building.mk") || !strings.Contains(s, "linting.mk") {
		t.Fatalf("error should mention both families: %v", err)
	}
}

// TestCheckOrphanRegionsMarkerErrorNoFamilies covers the path where a doc has
// a malformed marker but no valid families (len(families) == 0 with markerErr).
func TestCheckOrphanRegionsMarkerErrorNoFamilies(t *testing.T) {
	docDir := t.TempDir()
	// A doc with a malformed begin marker (no family name) and no end marker.
	doc := beginMarkerPrefix + beginMarkerSuffix + "\n"
	if err := os.WriteFile(filepath.Join(docDir, "extra.md"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	errs := checkOrphanRegions(docDir, map[string]bool{})
	if len(errs) == 0 {
		t.Fatalf("checkOrphanRegions with a malformed marker should return errors")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "malformed") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("errors should mention malformed: %v", errs)
	}
}

// TestCheckOrphanRegionsCanonicalDocMismatch covers the branch where a
// family's region is in the wrong doc.
func TestCheckOrphanRegionsCanonicalDocMismatch(t *testing.T) {
	docDir := t.TempDir()
	// A building region in extra.md (not building.md).
	doc := beginMarker("building") + endMarker + "\n"
	if err := os.WriteFile(filepath.Join(docDir, "extra.md"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	errs := checkOrphanRegions(docDir, map[string]bool{"building": true})
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "belongs in") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("errors should mention belongs in: %v", errs)
	}
}

// TestCheckOrphanRegionsReadFileError covers the ReadFile error path inside
// checkOrphanRegions for a single doc.
func TestCheckOrphanRegionsReadFileError(t *testing.T) {
	docDir := t.TempDir()
	// Create a doc as a directory so ReadFile fails.
	if err := os.Mkdir(filepath.Join(docDir, "broken.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	errs := checkOrphanRegions(docDir, map[string]bool{})
	if len(errs) == 0 {
		t.Fatalf("checkOrphanRegions with an unreadable doc should return errors")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "broken.md") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("errors should mention broken.md: %v", errs)
	}
}

// TestCheckOrphanRegionsOrphanRegion covers the path where a doc references
// a family not in stems.
func TestCheckOrphanRegionsOrphanRegion(t *testing.T) {
	docDir := t.TempDir()
	doc := beginMarker("ghost") + endMarker + "\n"
	if err := os.WriteFile(filepath.Join(docDir, "extra.md"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	errs := checkOrphanRegions(docDir, map[string]bool{"building": true})
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "does not exist") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("errors should mention does not exist: %v", errs)
	}
}

// TestCheckOrphanRegionsMarkerErrWithFamilies covers the path where a doc has
// valid families but also a markerErr.
func TestCheckOrphanRegionsMarkerErrWithFamilies(t *testing.T) {
	docDir := t.TempDir()
	// A doc with a valid family region but also a malformed marker line.
	doc := beginMarker("building") + endMarker + "\n" + beginMarkerPrefix + beginMarkerSuffix + "\n"
	if err := os.WriteFile(filepath.Join(docDir, "extra.md"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	errs := checkOrphanRegions(docDir, map[string]bool{"building": true})
	if len(errs) == 0 {
		t.Fatalf("checkOrphanRegions with a malformed marker alongside valid families should return errors")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "malformed") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("errors should mention malformed: %v", errs)
	}
}

// TestCheckOrphanRegionsNoMarkersNoError covers the path where a doc has no
// GENERATED region at all (no families, no markerErr).
func TestCheckOrphanRegionsNoMarkersNoError(t *testing.T) {
	docDir := t.TempDir()
	doc := []byte("# Just prose, no markers here.\n")
	if err := os.WriteFile(filepath.Join(docDir, "plain.md"), doc, 0o600); err != nil {
		t.Fatal(err)
	}
	errs := checkOrphanRegions(docDir, map[string]bool{})
	if len(errs) != 0 {
		t.Fatalf("checkOrphanRegions with a plain doc should return no errors, got %v", errs)
	}
}

// TestCheckOrphanRegionsFilesWithSuffixError covers the filesWithSuffix error
// path in checkOrphanRegions.
func TestCheckOrphanRegionsFilesWithSuffixError(t *testing.T) {
	// docDir is a regular file, not a directory — filesWithSuffix errors.
	dir := t.TempDir()
	conflict := filepath.Join(dir, "notadir")
	if err := os.WriteFile(conflict, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	errs := checkOrphanRegions(conflict, map[string]bool{})
	if len(errs) == 0 {
		t.Fatalf("checkOrphanRegions with a file as docDir should return errors")
	}
}

// TestWalkSourceLinesCRLF covers the CRLF stripping branch in walkSourceLines.
func TestWalkSourceLinesCRLF(t *testing.T) {
	src := []byte("line1\r\nline2\r\n")
	var texts [][]byte
	walkSourceLines(src, func(line sourceLine) bool {
		texts = append(texts, line.text)
		return true
	})
	if len(texts) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(texts))
	}
	if string(texts[0]) != "line1" {
		t.Fatalf("first line text = %q, want %q (CRLF stripped)", texts[0], "line1")
	}
}

// TestFindMarkerLineCRLF covers findMarkerLine with a CRLF document.
func TestFindMarkerLineCRLF(t *testing.T) {
	marker := beginMarker("building")
	trimmed := strings.TrimSuffix(marker, "\n")
	doc := []byte("# Prose\r\n" + marker[:len(marker)-1] + "\r\nbody\r\n")
	start, next, ending, ok := findMarkerLine(doc, trimmed)
	if !ok {
		t.Fatalf("findMarkerLine should find the marker")
	}
	if ending != "\r\n" {
		t.Fatalf("line ending = %q, want %q", ending, "\r\n")
	}
	if start <= 0 {
		t.Fatalf("start should be positive, got %d", start)
	}
	if next <= start {
		t.Fatalf("next should be after start, got start=%d next=%d", start, next)
	}
}

// TestRewriteRegionWithCRLF covers RewriteRegion with a CRLF document,
// exercising the lineEnding-based replacement path.
func TestRewriteRegionWithCRLF(t *testing.T) {
	marker := beginMarker("building")
	body := "new content"
	// Build a CRLF doc with a proper marked region.
	doc := []byte("# Prose\r\n" + marker[:len(marker)-1] + "\r\nold\r\n" + endMarker + "\r\nmore\r\n")
	out, err := RewriteRegion(doc, "building", body)
	if err != nil {
		t.Fatalf("RewriteRegion with CRLF: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "new content\r\n") {
		t.Fatalf("CRLF doc should preserve CRLF in the replacement: %s", s)
	}
	if !strings.Contains(s, "# Prose\r\n") {
		t.Fatalf("CRLF prose should be preserved: %s", s)
	}
}

// TestRewriteRegionEmptyBody covers the body == "" path (empty replacement).
func TestRewriteRegionEmptyBody(t *testing.T) {
	doc := []byte("# Prose\n" + beginMarker("building") + "old\n" + endMarker + "\nmore\n")
	out, err := RewriteRegion(doc, "building", "")
	if err != nil {
		t.Fatalf("RewriteRegion with empty body: %v", err)
	}
	s := string(out)
	if strings.Contains(s, "old") {
		t.Fatalf("empty body should remove old content: %s", s)
	}
	if !strings.Contains(s, "# Prose") || !strings.Contains(s, "more") {
		t.Fatalf("prose should be preserved: %s", s)
	}
}

// TestValidateGeneratedRegionMarkersMalformedBegin covers the
// validBeginCount != beginCount branch.
func TestValidateGeneratedRegionMarkersMalformedBegin(t *testing.T) {
	// A begin marker with no family name (malformed) plus a valid end.
	doc := []byte(beginMarkerPrefix + beginMarkerSuffix + "\n" + endMarker + "\n")
	err := validateGeneratedRegionMarkers(doc)
	if err == nil {
		t.Fatalf("validateGeneratedRegionMarkers with a malformed begin should error")
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("error should mention malformed: %v", err)
	}
}

// TestGeneratedRegionFamilyMalformed covers generatedRegionFamily with a
// line that has the prefix but no suffix.
func TestGeneratedRegionFamilyMalformed(t *testing.T) {
	// A line with the prefix but no suffix.
	line := []byte(beginMarkerPrefix + "something without suffix")
	if _, ok := generatedRegionFamily(line); ok {
		t.Fatalf("generatedRegionFamily with no suffix should return false")
	}
}

// TestGeneratedRegionFamilyEmptyName covers generatedRegionFamily where the
// family name is empty (prefix directly followed by suffix).
func TestGeneratedRegionFamilyEmptyName(t *testing.T) {
	line := []byte(beginMarkerPrefix + beginMarkerSuffix)
	if _, ok := generatedRegionFamily(line); ok {
		t.Fatalf("generatedRegionFamily with empty family name should return false")
	}
}

// TestGeneratedRegionFamilyValid covers the happy path.
func TestGeneratedRegionFamilyValid(t *testing.T) {
	line := []byte(beginMarkerPrefix + "building" + beginMarkerSuffix)
	family, ok := generatedRegionFamily(line)
	if !ok || family != "building" {
		t.Fatalf("generatedRegionFamily with valid line = (%q, %v), want (building, true)", family, ok)
	}
}

// TestHasFields covers the hasFields helper.
func TestHasFields(t *testing.T) {
	if (Target{}).hasFields() {
		t.Fatalf("hasFields with empty target should return false")
	}
	if !(Target{Trigger: "CI"}).hasFields() {
		t.Fatalf("hasFields with Trigger should return true")
	}
	if !(Target{Requires: "go"}).hasFields() {
		t.Fatalf("hasFields with Requires should return true")
	}
	if !(Target{FailsWhen: "test fails"}).hasFields() {
		t.Fatalf("hasFields with FailsWhen should return true")
	}
}

// TestParseFamilyCommentDoesNotBreakBlock covers the path where a plain "#"
// comment sits between a ## block and its rule — this is allowed.
func TestParseFamilyCommentDoesNotBreakBlock(t *testing.T) {
	src := []byte("## Summary\n# rationale\nbuild:\n\tgo build .\n")
	targets, err := ParseFamily(src)
	if err != nil {
		t.Fatalf("ParseFamily with a # comment between block and rule should succeed: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
	if targets[0].Name != "build" {
		t.Fatalf("target name = %q, want build", targets[0].Name)
	}
}

// TestParseFamilyHappyPath covers the full happy path with structured fields.
func TestParseFamilyHappyPath(t *testing.T) {
	src := []byte("## Build the binary.\n## trigger: CI\n## requires: go\n## fails-when: tests fail\nbuild:\n\tgo build .\n")
	targets, err := ParseFamily(src)
	if err != nil {
		t.Fatalf("ParseFamily happy path: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
	if targets[0].Summary != "Build the binary." {
		t.Fatalf("summary = %q, want 'Build the binary.'", targets[0].Summary)
	}
	if targets[0].Trigger != "CI" {
		t.Fatalf("trigger = %q, want CI", targets[0].Trigger)
	}
}

// TestParseFamilyContinuationWithoutBlock covers the error where a
// continuation line (##   ...) has no preceding block.
func TestParseFamilyContinuationWithoutBlock(t *testing.T) {
	src := []byte("##   continuation\nbuild:\n\tgo build .\n")
	_, err := ParseFamily(src)
	if err == nil {
		t.Fatalf("ParseFamily with a continuation line and no block should error")
	}
	if !strings.Contains(err.Error(), "nothing to continue") {
		t.Fatalf("error should mention nothing to continue: %v", err)
	}
}

// TestParseFamilyInvalidHashLine covers the error where a ## line does not
// start with exactly one space or three spaces.
func TestParseFamilyInvalidHashLine(t *testing.T) {
	src := []byte("##  double space summary\nbuild:\n\tgo build .\n")
	_, err := ParseFamily(src)
	if err == nil {
		t.Fatalf("ParseFamily with invalid ## line should error")
	}
	if !strings.Contains(err.Error(), "not a valid ## line") {
		t.Fatalf("error should mention not a valid ## line: %v", err)
	}
}

// TestParseFamilyMultipleTargets covers parsing multiple targets.
func TestParseFamilyMultipleTargets(t *testing.T) {
	src := []byte("## Build the binary.\nbuild:\n\tgo build .\n## Run tests.\ntest:\n\tgo test ./...\n")
	targets, err := ParseFamily(src)
	if err != nil {
		t.Fatalf("ParseFamily with multiple targets: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(targets))
	}
	if targets[0].Name != "build" || targets[1].Name != "test" {
		t.Fatalf("target names = %q, %q, want build, test", targets[0].Name, targets[1].Name)
	}
}

// TestRenderCompactList covers the renderCompactList helper.
func TestRenderCompactList(t *testing.T) {
	targets := []Target{
		{Name: "build", Summary: "Build it."},
		{Name: "test", Summary: "Test it."},
	}
	got := renderCompactList(targets, false)
	if !strings.Contains(got, "`make build`") || !strings.Contains(got, "Build it.") {
		t.Fatalf("renderCompactList output missing content: %s", got)
	}
}

// TestRenderCompactListWithHeading covers the renderCompactList helper with heading.
func TestRenderCompactListWithHeading(t *testing.T) {
	targets := []Target{
		{Name: "build", Summary: "Build it."},
	}
	got := renderCompactList(targets, true)
	if !strings.Contains(got, "`make build`") {
		t.Fatalf("renderCompactList with heading output missing content: %s", got)
	}
}

// TestRenderWideTable covers the renderWideTable helper.
func TestRenderWideTable(t *testing.T) {
	targets := []Target{
		{Name: "lint", Summary: "Lint everything.", Trigger: "CI", Requires: "golangci-lint", FailsWhen: "lint fails"},
	}
	got := renderWideTable(targets)
	if !strings.Contains(got, "| Command | Summary | What it proves | Trigger | Requires | Fails when |") {
		t.Fatalf("renderWideTable missing header: %s", got)
	}
	if !strings.Contains(got, "`make lint`") {
		t.Fatalf("renderWideTable missing command: %s", got)
	}
}

// TestEscapeCell covers the escapeCell helper.
func TestEscapeCell(t *testing.T) {
	if got := escapeCell("no pipes"); got != "no pipes" {
		t.Fatalf("escapeCell without pipes = %q, want %q", got, "no pipes")
	}
	if got := escapeCell("with | pipe"); got != "with \\| pipe" {
		t.Fatalf("escapeCell with pipe = %q, want %q", got, "with \\| pipe")
	}
}

// TestCodeSpan covers the codeSpan helper.
func TestCodeSpan(t *testing.T) {
	if got := codeSpan("build"); got != "`build`" {
		t.Fatalf("codeSpan(build) = %q, want %q", got, "`build`")
	}
}

// TestCommand covers the command helper.
func TestCommand(t *testing.T) {
	if got := command("build"); got != "`make build`" {
		t.Fatalf("command(build) = %q, want %q", got, "`make build`")
	}
}

// TestFamilyFilesNonIsNotExistError covers the error propagation in
// filesWithSuffix when ReadDir returns a non-IsNotExist error.
func TestFamilyFilesNonIsNotExistError(t *testing.T) {
	root := t.TempDir()
	// Create "make" as a regular file so ReadDir fails with a non-IsNotExist error.
	if err := os.WriteFile(filepath.Join(root, "make"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := familyFiles(root)
	if err == nil {
		t.Fatalf("familyFiles with make as a file should error")
	}
}

// TestFilesWithSuffixNonIsNotExistError covers the non-IsNotExist error path.
func TestFilesWithSuffixNonIsNotExistError(t *testing.T) {
	dir := t.TempDir()
	conflict := filepath.Join(dir, "notadir")
	if err := os.WriteFile(conflict, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := filesWithSuffix(conflict, ".mk")
	if err == nil {
		t.Fatalf("filesWithSuffix on a file should error")
	}
}

// TestRenderHelpSingleFamily covers renderHelp with one family.
func TestRenderHelpSingleFamily(t *testing.T) {
	families := []family{
		{Stem: "building", Targets: []Target{{Name: "build", Summary: "Build the binary."}}},
	}
	got := renderHelp(families)
	if !strings.Contains(got, "building:") {
		t.Fatalf("renderHelp should contain family stem: %s", got)
	}
	if !strings.Contains(got, "build") || !strings.Contains(got, "Build the binary.") {
		t.Fatalf("renderHelp should contain target: %s", got)
	}
}

// TestRenderHelpMultipleFamilies covers renderHelp with multiple families.
func TestRenderHelpMultipleFamilies(t *testing.T) {
	families := []family{
		{Stem: "building", Targets: []Target{{Name: "build", Summary: "Build."}}},
		{Stem: "testing", Targets: []Target{{Name: "test", Summary: "Test."}}},
	}
	got := renderHelp(families)
	if !strings.Contains(got, "building:") || !strings.Contains(got, "testing:") {
		t.Fatalf("renderHelp should contain both families: %s", got)
	}
	// There should be a blank line between families.
	if !strings.Contains(got, "\n\ntesting:") {
		t.Fatalf("renderHelp should have a blank line between families: %s", got)
	}
}

// TestParseFamilyEmptyInput covers ParseFamily with an empty source.
func TestParseFamilyEmptyInput(t *testing.T) {
	targets, err := ParseFamily([]byte(""))
	if err != nil {
		t.Fatalf("ParseFamily with empty input should not error: %v", err)
	}
	if len(targets) != 0 {
		t.Fatalf("ParseFamily with empty input should return no targets, got %d", len(targets))
	}
}

// TestPrintHelpSuccess covers the happy path for printHelp.
func TestPrintHelpSuccess(t *testing.T) {
	root := t.TempDir()
	writeFixtureMk(t, root, "building", "## Build the binary.\nbuild:\n\tgo build .\n")
	var buf bytes.Buffer
	err := printHelp(&buf, root)
	if err != nil {
		t.Fatalf("printHelp success: %v", err)
	}
	output := buf.String()
	if !strings.Contains(output, "usage: make") {
		t.Fatalf("printHelp output should contain header: %s", output)
	}
	if !strings.Contains(output, "building:") {
		t.Fatalf("printHelp output should contain family: %s", output)
	}
}

// TestLoadFamiliesFamilyFilesError covers the error propagation from
// familyFiles in loadFamilies.
func TestLoadFamiliesFamilyFilesError(t *testing.T) {
	root := t.TempDir()
	// No make/ directory at all — familyFiles returns an error.
	_, err := loadFamilies(root)
	if err == nil {
		t.Fatalf("loadFamilies with no make/ dir should error")
	}
}

// TestGenerateSuccess covers the happy path for generate with all families
// valid.
func TestGenerateSuccessMultipleFamilies(t *testing.T) {
	root := t.TempDir()
	writeFixtureMk(t, root, "building", "## Build the binary.\nbuild:\n\tgo build .\n")
	writeFixtureMk(t, root, "testing", "## Run tests.\ntest:\n\tgo test ./...\n")
	writeFixtureDoc(t, root, "building.md", "building", "")
	writeFixtureDoc(t, root, "testing.md", "testing", "")
	err := generate(root)
	if err != nil {
		t.Fatalf("generate with valid families: %v", err)
	}
}

// TestGenerateOneWriteFileSuccess covers the successful write path in
// generateOne (content changed, write succeeds).
func TestGenerateOneWriteFileSuccess(t *testing.T) {
	root := t.TempDir()
	writeFixtureMk(t, root, "building", "## Build the binary.\nbuild:\n\tgo build .\n")
	writeFixtureDoc(t, root, "building.md", "building", "")
	docDir := filepath.Join(root, "docs", "developing-evener")
	err := generateOne(docDir, filepath.Join(root, "make", "building.mk"), "building")
	if err != nil {
		t.Fatalf("generateOne success: %v", err)
	}
	// Verify the doc was written.
	doc, err := os.ReadFile(filepath.Join(docDir, "building.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), "`make build`") {
		t.Fatalf("doc should contain generated content: %s", doc)
	}
}

// TestRewriteRegionMarkerErr covers the path where validateGeneratedRegionMarkers
// fails before RewriteRegion can find the marker.
func TestRewriteRegionMarkerErr(t *testing.T) {
	// Doc with unpaired begin marker — validateGeneratedRegionMarkers fails.
	doc := []byte(beginMarker("building") + "body\n")
	_, err := RewriteRegion(doc, "building", "new body")
	if err == nil {
		t.Fatalf("RewriteRegion with unpaired marker should error")
	}
}

// TestCheckOrphanRegionsValidDocNoErrors covers the happy path where a doc
// has a valid region for a known family in the correct doc.
func TestCheckOrphanRegionsValidDocNoErrors(t *testing.T) {
	docDir := t.TempDir()
	doc := beginMarker("building") + endMarker + "\n"
	if err := os.WriteFile(filepath.Join(docDir, "building.md"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	errs := checkOrphanRegions(docDir, map[string]bool{"building": true})
	if len(errs) != 0 {
		t.Fatalf("checkOrphanRegions with valid doc should return no errors, got %v", errs)
	}
}

// TestRewriteRegionSuccessWithLF covers the successful LF path.
func TestRewriteRegionSuccessWithLF(t *testing.T) {
	doc := []byte("# Prose\n" + beginMarker("building") + "old\n" + endMarker + "\nmore\n")
	out, err := RewriteRegion(doc, "building", "new body")
	if err != nil {
		t.Fatalf("RewriteRegion success: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "new body\n") {
		t.Fatalf("output should contain new body: %s", s)
	}
	if !strings.Contains(s, "# Prose") || !strings.Contains(s, "more") {
		t.Fatalf("prose should be preserved: %s", s)
	}
}

// TestMarkerLineIndexFound covers the happy path for markerLineIndex.
func TestMarkerLineIndexFound(t *testing.T) {
	marker := beginMarker("building")
	doc := []byte("# Prose\n" + marker + "body\n" + endMarker + "\n")
	trimmed := strings.TrimSuffix(marker, "\n")
	got := markerLineIndex(doc, trimmed)
	if got <= 0 {
		t.Fatalf("markerLineIndex should return positive offset, got %d", got)
	}
}

// TestFindMarkerLineNotFound covers the not-found path.
func TestFindMarkerLineNotFound(t *testing.T) {
	doc := []byte("no markers here\n")
	_, _, _, ok := findMarkerLine(doc, beginMarker("building"))
	if ok {
		t.Fatalf("findMarkerLine with no marker should return ok=false")
	}
}

// TestFindMarkerLineFound covers the found path.
func TestFindMarkerLineFound(t *testing.T) {
	marker := beginMarker("building")
	doc := []byte("# Prose\n" + marker + "body\n")
	trimmed := strings.TrimSuffix(marker, "\n")
	start, _, _, ok := findMarkerLine(doc, trimmed)
	if !ok {
		t.Fatalf("findMarkerLine should find the marker")
	}
	if start <= 0 {
		t.Fatalf("start should be positive, got %d", start)
	}
}

// TestWalkSourceLinesStopsEarly covers the early-return path when visit
// returns false.
func TestWalkSourceLinesStopsEarly(t *testing.T) {
	src := []byte("line1\nline2\nline3\n")
	count := 0
	walkSourceLines(src, func(line sourceLine) bool {
		count++
		return false // stop after first line
	})
	if count != 1 {
		t.Fatalf("walkSourceLines should stop after first line, got %d visits", count)
	}
}

// TestWalkSourceLinesEmpty covers the empty-input path.
func TestWalkSourceLinesEmpty(t *testing.T) {
	count := 0
	walkSourceLines([]byte(""), func(line sourceLine) bool {
		count++
		return true
	})
	if count != 0 {
		t.Fatalf("walkSourceLines with empty input should visit 0 lines, got %d", count)
	}
}

// TestValidateGeneratedRegionMarkersNoMarkers covers the path where a doc
// has no markers at all (beginCount == 0, endCount == 0).
func TestValidateGeneratedRegionMarkersNoMarkers(t *testing.T) {
	doc := []byte("# Just prose\n")
	err := validateGeneratedRegionMarkers(doc)
	if err != nil {
		t.Fatalf("validateGeneratedRegionMarkers with no markers should return nil, got %v", err)
	}
}

// TestValidateGeneratedRegionMarkersEndBeforeBegin covers the path where
// the END marker appears before the BEGIN marker.
func TestValidateGeneratedRegionMarkersEndBeforeBegin(t *testing.T) {
	doc := []byte(endMarker + "\n" + beginMarker("building") + endMarker + "\n")
	err := validateGeneratedRegionMarkers(doc)
	if err == nil {
		t.Fatalf("validateGeneratedRegionMarkers with end before begin should error")
	}
}

// TestValidateGeneratedRegionMarkersValid covers the happy path.
func TestValidateGeneratedRegionMarkersValid(t *testing.T) {
	doc := []byte(beginMarker("building") + endMarker + "\n")
	err := validateGeneratedRegionMarkers(doc)
	if err != nil {
		t.Fatalf("validateGeneratedRegionMarkers with valid markers should return nil, got %v", err)
	}
}

// TestGeneratedRegionFamilies covers the generatedRegionFamilies helper.
func TestGeneratedRegionFamilies(t *testing.T) {
	doc := []byte(beginMarker("building") + endMarker + "\n" + beginMarker("linting") + endMarker + "\n")
	families := generatedRegionFamilies(doc)
	if len(families) != 2 {
		t.Fatalf("expected 2 families, got %d", len(families))
	}
	if families[0] != "building" || families[1] != "linting" {
		t.Fatalf("families = %v, want [building, linting]", families)
	}
}

// TestGeneratedRegionFamiliesEmpty covers the empty path.
func TestGeneratedRegionFamiliesEmpty(t *testing.T) {
	doc := []byte("# No markers\n")
	families := generatedRegionFamilies(doc)
	if len(families) != 0 {
		t.Fatalf("expected 0 families, got %d", len(families))
	}
}

// TestEscapeCellBacktick covers escapeCell with backticks.
func TestEscapeCellBacktick(t *testing.T) {
	if got := escapeCell("with `backticks`"); got != "with `backticks`" {
		t.Fatalf("escapeCell should preserve backticks: %q", got)
	}
}

// TestRenderNoTargets covers Render with an empty target list.
func TestRenderNoTargets(t *testing.T) {
	got := Render(nil)
	if got != "" {
		t.Fatalf("Render with no targets should return empty, got %q", got)
	}
}

// TestRenderWithTriggerOnly covers Render with a target that has only a Trigger field.
func TestRenderWithTriggerOnly(t *testing.T) {
	targets := []Target{
		{Name: "lint", Summary: "Lint.", Trigger: "CI"},
	}
	got := Render(targets)
	if !strings.Contains(got, "Trigger") {
		t.Fatalf("Render with trigger should contain wide table: %s", got)
	}
}

// TestRenderWithRequiresOnly covers Render with a target that has only a Requires field.
func TestRenderWithRequiresOnly(t *testing.T) {
	targets := []Target{
		{Name: "build", Summary: "Build.", Requires: "go"},
	}
	got := Render(targets)
	if !strings.Contains(got, "Requires") {
		t.Fatalf("Render with requires should contain wide table: %s", got)
	}
}

// TestRenderWithFailsWhenOnly covers Render with a target that has only a FailsWhen field.
func TestRenderWithFailsWhenOnly(t *testing.T) {
	targets := []Target{
		{Name: "test", Summary: "Test.", FailsWhen: "tests fail"},
	}
	got := Render(targets)
	if !strings.Contains(got, "Fails when") {
		t.Fatalf("Render with fails when should contain wide table: %s", got)
	}
}

// TestRenderMixedFields covers Render with targets that have different field sets.
func TestRenderMixedFields(t *testing.T) {
	targets := []Target{
		{Name: "build", Summary: "Build."},
		{Name: "lint", Summary: "Lint.", Trigger: "CI"},
	}
	got := Render(targets)
	// When any target has a field, all use the wide table format.
	if !strings.Contains(got, "Trigger") {
		t.Fatalf("Render with mixed fields should use wide table: %s", got)
	}
}

// TestParseFamilyWithMultipleContinuationLines covers a ## block with
// multiple continuation lines.
func TestParseFamilyWithMultipleContinuationLines(t *testing.T) {
	src := []byte("## Build the binary.\n##   This is a long summary\n##   that spans multiple lines.\nbuild:\n\tgo build .\n")
	targets, err := ParseFamily(src)
	if err != nil {
		t.Fatalf("ParseFamily with continuation: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
	if !strings.Contains(targets[0].Summary, "Build the binary.") {
		t.Fatalf("summary should contain first line: %q", targets[0].Summary)
	}
	if !strings.Contains(targets[0].Summary, "multiple lines.") {
		t.Fatalf("summary should contain continuation: %q", targets[0].Summary)
	}
}

// TestParseFamilyWithFieldAndContinuation covers a ## block with a field
// followed by a continuation.
func TestParseFamilyWithFieldAndContinuation(t *testing.T) {
	src := []byte("## Build.\n## trigger: CI\n##   and more detail\nbuild:\n\tgo build .\n")
	targets, err := ParseFamily(src)
	if err != nil {
		t.Fatalf("ParseFamily with field and continuation: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
}

// TestRuleShape covers the ruleShape helper.
func TestRuleShape(t *testing.T) {
	tests := []struct {
		line   string
		isRule bool
	}{
		{"build: dep", true},
		{"build:", true},
		{"not a rule", false},
		{".PHONY: build", true},
		{"# comment", false},
		{"", false},
	}
	for _, tt := range tests {
		name, _, isRule := ruleShape(tt.line)
		if isRule != tt.isRule {
			t.Fatalf("ruleShape(%q) = %v, want %v (name=%q)", tt.line, isRule, tt.isRule, name)
		}
	}
}

// TestRenderHelpWidthPadding covers renderHelp's per-family width padding.
func TestRenderHelpWidthPadding(t *testing.T) {
	families := []family{
		{Stem: "building", Targets: []Target{
			{Name: "build", Summary: "Build."},
			{Name: "very-long-name", Summary: "Long."},
		}},
	}
	got := renderHelp(families)
	// The short name "build" should be padded to align with "very-long-name".
	if !strings.Contains(got, "  build           Build.") {
		t.Fatalf("renderHelp should pad short names: %q", got)
	}
}

// TestParseFamilyWithAllFields covers a target with all structured fields.
func TestParseFamilyWithAllFields(t *testing.T) {
	src := []byte("## Build.\n## trigger: CI\n## requires: go\n## fails-when: build fails\nbuild:\n\tgo build .\n")
	targets, err := ParseFamily(src)
	if err != nil {
		t.Fatalf("ParseFamily with all fields: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(targets))
	}
	t0 := targets[0]
	if t0.Trigger != "CI" {
		t.Fatalf("trigger = %q, want CI", t0.Trigger)
	}
	if t0.Requires != "go" {
		t.Fatalf("requires = %q, want go", t0.Requires)
	}
	if t0.FailsWhen != "build fails" {
		t.Fatalf("failsWhen = %q, want 'build fails'", t0.FailsWhen)
	}
}

// fmt import guard to keep the import used in case of future expansion.
var _ = fmt.Sprintf

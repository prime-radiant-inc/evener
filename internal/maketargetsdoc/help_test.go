package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestRenderHelpGroupsByFamily is the minimal shape task-12-brief.md Step 1
// asks for: output groups by family (in the order families are given, one
// blank-line-separated block per family) and prints one line per target.
func TestRenderHelpGroupsByFamily(t *testing.T) {
	families := []family{
		{Stem: "building", Targets: []Target{
			{Name: "build", Summary: "Build the binary."},
			{Name: "build-go", Summary: "Compile everything."},
		}},
		{Stem: "repo", Targets: []Target{
			{Name: "clean", Summary: "Remove the built binaries from the repo root."},
		}},
	}
	got := renderHelp(families)
	want := "building:\n" +
		"  build     Build the binary.\n" +
		"  build-go  Compile everything.\n" +
		"\n" +
		"repo:\n" +
		"  clean  Remove the built binaries from the repo root.\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestRenderHelpAlignsWithinFamilyNotAcrossFamilies pins that the summary
// column's width is computed per family: a long name in one family (like
// fuzzing's fuzz-oracle-audit-recheck) must not push every other family's
// column out too.
func TestRenderHelpAlignsWithinFamilyNotAcrossFamilies(t *testing.T) {
	families := []family{
		{Stem: "fuzzing", Targets: []Target{
			{Name: "fuzz", Summary: "Run the CI fuzz gate."},
			{Name: "fuzz-oracle-audit-recheck", Summary: "Verify the audit."},
		}},
		{Stem: "repo", Targets: []Target{
			{Name: "clean", Summary: "Remove the binaries."},
		}},
	}
	got := renderHelp(families)
	if !strings.Contains(got, "  clean  Remove the binaries.\n") {
		t.Fatalf("repo's short column got stretched by fuzzing's long name:\n%s", got)
	}
}

// TestRenderHelpEmptyFamiliesIsEmptyString: no families in, no output out —
// mirrors Render's empty-input behaviour (render_test.go).
func TestRenderHelpEmptyFamiliesIsEmptyString(t *testing.T) {
	if got := renderHelp(nil); got != "" {
		t.Fatalf("got %q, want empty string", got)
	}
}

// TestLoadFamiliesGroupsBySortedStem: loadFamilies walks make/*.mk the same
// way generate does (familyFiles), so the returned families come back
// in sorted-stem order regardless of the order the fixture files were
// written in.
func TestLoadFamiliesGroupsBySortedStem(t *testing.T) {
	root := t.TempDir()
	writeFixtureMk(t, root, "testing", "## Run the tests.\ntest:\n\t@true\n")
	writeFixtureMk(t, root, "building", "## Build the binary.\nbuild:\n\tgo build .\n")

	got, err := loadFamilies(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Stem != "building" || got[1].Stem != "testing" {
		t.Fatalf("got %+v, want building before testing", got)
	}
	if len(got[0].Targets) != 1 || got[0].Targets[0].Name != "build" {
		t.Fatalf("building family targets: %+v", got[0].Targets)
	}
}

// TestLoadFamiliesPropagatesParseErrorButKeepsGoodFamilies: reusing
// ParseFamily (parse.go) means a bad annotation in one family is a real
// parse error, not silently dropped — but per generate's "collect, don't
// stop" posture (main.go), one bad family must not hide a good one.
func TestLoadFamiliesPropagatesParseErrorButKeepsGoodFamilies(t *testing.T) {
	root := t.TempDir()
	writeFixtureMk(t, root, "building", "## Build the binary.\nbuild:\n\tgo build .\n")
	writeFixtureMk(t, root, "repo", "clean:\n\trm -f evener\n") // no ## block: a parse error

	got, err := loadFamilies(root)
	if err == nil {
		t.Fatal("expected an error: make/repo.mk has no annotations")
	}
	if !strings.Contains(err.Error(), "repo.mk") {
		t.Fatalf("error %q does not name the broken family", err)
	}
	if len(got) != 1 || got[0].Stem != "building" {
		t.Fatalf("expected the good family to survive the bad one's parse error: %+v", got)
	}
}

// TestPrintHelpWritesFamiliesToWriter is the end-to-end `-mode help` path:
// printHelp reads root's make/*.mk (via loadFamilies, which reuses
// ParseFamily) and writes the grouped listing to w.
func TestPrintHelpWritesFamiliesToWriter(t *testing.T) {
	root := t.TempDir()
	writeFixtureMk(t, root, "repo", "## Remove the built binaries from the repo root.\nclean:\n\trm -f evener\n")

	var buf bytes.Buffer
	if err := printHelp(&buf, root); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "repo:\n") {
		t.Fatalf("missing family header:\n%s", got)
	}
	if !strings.Contains(got, "clean") || !strings.Contains(got, "Remove the built binaries from the repo root.") {
		t.Fatalf("missing target line:\n%s", got)
	}
}

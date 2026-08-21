package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFixtureDoc writes a doc under root/docs/developing-evener/<name>
// with a GENERATED marker for family and the given (possibly empty) body,
// wrapped in hand-written prose on both sides — the shape every real doc
// in docs/developing-evener has today.
func writeFixtureDoc(t *testing.T, root, name, family, body string) {
	t.Helper()
	docDir := filepath.Join(root, "docs", "developing-evener")
	if err := os.MkdirAll(docDir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "# Prose above.\n\n## Targets\n\n" + beginMarker(family) + body + endMarker + "\nProse below.\n"
	if err := os.WriteFile(filepath.Join(docDir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeFixtureMk writes make/<stem>.mk under root with src as its content.
func writeFixtureMk(t *testing.T, root, stem, src string) {
	t.Helper()
	mkDir := filepath.Join(root, "make")
	if err := os.MkdirAll(mkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mkDir, stem+".mk"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestGenerateWritesAllFamilyDocs is the happy path: two annotated
// families, each with a destination doc carrying an empty marked region;
// generate fills both in and leaves the surrounding prose untouched.
func TestGenerateWritesAllFamilyDocs(t *testing.T) {
	root := t.TempDir()
	writeFixtureMk(t, root, "building", "## Build the binary.\nbuild:\n\tgo build .\n")
	writeFixtureMk(t, root, "linting", "## Lint everything.\n## trigger: CI.\nlint:\n\t@true\n")
	writeFixtureDoc(t, root, "building.md", "building", "")
	writeFixtureDoc(t, root, "linting.md", "linting", "")

	if err := generate(root); err != nil {
		t.Fatal(err)
	}

	building, err := os.ReadFile(filepath.Join(root, "docs", "developing-evener", "building.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(building), "`make build`") || !strings.Contains(string(building), "Build the binary.") {
		t.Fatalf("building.md missing generated content:\n%s", building)
	}
	if !strings.HasPrefix(string(building), "# Prose above.") || !strings.HasSuffix(string(building), "Prose below.\n") {
		t.Fatalf("building.md lost surrounding prose:\n%s", building)
	}

	linting, err := os.ReadFile(filepath.Join(root, "docs", "developing-evener", "linting.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(linting), "| Command | Summary | What it proves | Trigger | Requires | Fails when |") {
		t.Fatalf("linting.md missing the wide table (lint has a trigger field):\n%s", linting)
	}
}

// TestGenerateErrorsOnMkWithNoDestinationDoc: a .mk file whose stem is not
// in stemToDoc is a hard error — task-11-brief.md Step 3.
func TestGenerateErrorsOnMkWithNoDestinationDoc(t *testing.T) {
	root := t.TempDir()
	writeFixtureMk(t, root, "mystery", "## Do something.\nmystery-target:\n\t@true\n")

	err := generate(root)
	if err == nil {
		t.Fatal("expected an error: make/mystery.mk has no destination doc")
	}
	if !strings.Contains(err.Error(), "mystery") {
		t.Fatalf("error %q does not name the orphan family", err)
	}
}

// TestGenerateErrorsOnDocRegionWithNoMatchingMk: a doc's marked region can
// name a family whose make/*.mk does not exist (a stale or misspelled
// marker after a rename) — task-11-brief.md Step 3. A valid family
// alongside it must still be regenerated; one broken region does not block
// the rest.
func TestGenerateErrorsOnDocRegionWithNoMatchingMk(t *testing.T) {
	root := t.TempDir()
	writeFixtureMk(t, root, "building", "## Build the binary.\nbuild:\n\tgo build .\n")
	writeFixtureDoc(t, root, "building.md", "building", "")
	writeFixtureDoc(t, root, "linting.md", "linting", "") // no make/linting.mk exists

	err := generate(root)
	if err == nil {
		t.Fatal("expected an error: linting.md's region names make/linting.mk, which does not exist")
	}
	if !strings.Contains(err.Error(), "linting") {
		t.Fatalf("error %q does not name the orphan region", err)
	}

	building, readErr := os.ReadFile(filepath.Join(root, "docs", "developing-evener", "building.md"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(building), "`make build`") {
		t.Fatalf("building.md should still have been regenerated despite linting.md's orphan region:\n%s", building)
	}
}

// TestCheckOrphanRegionsInspectsEveryMarker catches an orphan hidden after a
// valid first region in a doc that no family directly rewrites.
func TestCheckOrphanRegionsInspectsEveryMarker(t *testing.T) {
	docDir := t.TempDir()
	doc := beginMarker("building") + endMarker + "\n" + beginMarker("ghost") + endMarker + "\n"
	if err := os.WriteFile(filepath.Join(docDir, "extra.md"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	errs := checkOrphanRegions(docDir, map[string]bool{"building": true})
	if len(errs) == 0 {
		t.Fatal("expected the second, orphaned ghost region to be reported")
	}
	foundGhost := false
	for _, err := range errs {
		if strings.Contains(err.Error(), "ghost") {
			foundGhost = true
			break
		}
	}
	if !foundGhost {
		t.Fatalf("orphan errors do not name ghost: %v", errs)
	}
}

// TestCheckOrphanRegionsTreatsDocDirectoryAsALiteralPath keeps orphan
// validation active when the checkout path itself contains glob syntax.
func TestCheckOrphanRegionsTreatsDocDirectoryAsALiteralPath(t *testing.T) {
	docDir := filepath.Join(t.TempDir(), "docs[1]")
	if err := os.Mkdir(docDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docDir, "extra.md"), []byte(beginMarker("ghost")+endMarker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	errs := checkOrphanRegions(docDir, map[string]bool{})
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "ghost") {
		t.Fatalf("literal-path orphan was not reported: %v", errs)
	}
}

// TestCheckOrphanRegionsRejectsUnpairedMarkers applies structural validation
// to marked docs that no family generator directly opens.
func TestCheckOrphanRegionsRejectsUnpairedMarkers(t *testing.T) {
	tests := map[string]string{
		"begin without end": beginMarker("building"),
		"end without begin": endMarker + "\n",
		"end before begin":  endMarker + "\n" + beginMarker("building"),
	}
	for name, doc := range tests {
		t.Run(name, func(t *testing.T) {
			docDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(docDir, "extra.md"), []byte(doc), 0o600); err != nil {
				t.Fatal(err)
			}

			errs := checkOrphanRegions(docDir, map[string]bool{"building": true})
			if len(errs) == 0 {
				t.Fatal("expected an unpaired marker error")
			}
		})
	}
}

// TestCheckOrphanRegionsRejectsMalformedBeginMarker keeps a marker-like line
// with no family name from bypassing both orphan and pairing validation.
func TestCheckOrphanRegionsRejectsMalformedBeginMarker(t *testing.T) {
	docDir := t.TempDir()
	doc := beginMarkerPrefix + beginMarkerSuffix + "\n" + endMarker + "\n"
	if err := os.WriteFile(filepath.Join(docDir, "extra.md"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	if errs := checkOrphanRegions(docDir, map[string]bool{}); len(errs) == 0 {
		t.Fatal("expected a malformed BEGIN marker error")
	}
}

// TestGenerateRejectsFamilyRegionOutsideCanonicalDoc prevents a second copy of
// one family's generated region from remaining stale in another Markdown file.
func TestGenerateRejectsFamilyRegionOutsideCanonicalDoc(t *testing.T) {
	root := t.TempDir()
	writeFixtureMk(t, root, "building", "## Build the binary.\nbuild:\n\tgo build .\n")
	writeFixtureDoc(t, root, "building.md", "building", "")
	writeFixtureDoc(t, root, "extra.md", "building", "stale\n")

	err := generate(root)
	if err == nil {
		t.Fatal("expected the duplicate building region in extra.md to be rejected")
	}
	if !strings.Contains(err.Error(), "extra.md") || !strings.Contains(err.Error(), "building.md") {
		t.Fatalf("error does not name the duplicate and canonical docs: %v", err)
	}
}

// TestGenerateContinuesPastPerFamilyParseFailure is make/repo.mk's exact
// current shape: a family whose rules carry no ## annotations yet fails to
// parse, and that failure must not be treated as fatal to the whole run —
// task-11-brief.md's "handle a family file that fails to parse sensibly."
// The doc it would have fed is left untouched, but every other family
// still gets regenerated.
func TestGenerateContinuesPastPerFamilyParseFailure(t *testing.T) {
	root := t.TempDir()
	writeFixtureMk(t, root, "building", "## Build the binary.\nbuild:\n\tgo build .\n")
	writeFixtureMk(t, root, "repo", "clean:\n\trm -f evener\n") // no ## block: unannotated, like the real make/repo.mk today
	writeFixtureDoc(t, root, "building.md", "building", "")
	writeFixtureDoc(t, root, "README.md", "repo", "")

	err := generate(root)
	if err == nil {
		t.Fatal("expected an error: make/repo.mk has no annotations")
	}
	if !strings.Contains(err.Error(), "repo.mk") {
		t.Fatalf("error %q does not name the unannotated family", err)
	}

	building, readErr := os.ReadFile(filepath.Join(root, "docs", "developing-evener", "building.md"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(building), "`make build`") {
		t.Fatalf("building.md should still have been regenerated despite repo.mk's parse failure:\n%s", building)
	}

	readme, readErr := os.ReadFile(filepath.Join(root, "docs", "developing-evener", "README.md"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	want := "# Prose above.\n\n## Targets\n\n" + beginMarker("repo") + endMarker + "\nProse below.\n"
	if string(readme) != want {
		t.Fatalf("README.md was modified despite repo.mk failing to parse:\ngot:\n%s\nwant:\n%s", readme, want)
	}
}

// TestGenerateIsIdempotent: running generate twice over the same fixture
// produces byte-identical output the second time — the property
// task-11-brief.md's verification step names explicitly, since a
// non-idempotent generator makes any future staleness gate flap.
func TestGenerateIsIdempotent(t *testing.T) {
	root := t.TempDir()
	writeFixtureMk(t, root, "building", "## Build the binary.\nbuild:\n\tgo build .\n")
	writeFixtureMk(t, root, "linting", "## Lint everything.\n## trigger: CI.\nlint:\n\t@true\n")
	writeFixtureDoc(t, root, "building.md", "building", "")
	writeFixtureDoc(t, root, "linting.md", "linting", "")

	if err := generate(root); err != nil {
		t.Fatal(err)
	}
	firstBuilding, err := os.ReadFile(filepath.Join(root, "docs", "developing-evener", "building.md"))
	if err != nil {
		t.Fatal(err)
	}
	firstLinting, err := os.ReadFile(filepath.Join(root, "docs", "developing-evener", "linting.md"))
	if err != nil {
		t.Fatal(err)
	}

	if err := generate(root); err != nil {
		t.Fatal(err)
	}
	secondBuilding, err := os.ReadFile(filepath.Join(root, "docs", "developing-evener", "building.md"))
	if err != nil {
		t.Fatal(err)
	}
	secondLinting, err := os.ReadFile(filepath.Join(root, "docs", "developing-evener", "linting.md"))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(firstBuilding, secondBuilding) {
		t.Fatalf("building.md changed on the second run:\nfirst:\n%s\nsecond:\n%s", firstBuilding, secondBuilding)
	}
	if !bytes.Equal(firstLinting, secondLinting) {
		t.Fatalf("linting.md changed on the second run:\nfirst:\n%s\nsecond:\n%s", firstLinting, secondLinting)
	}
}

// TestGenerateTreatsRootAsALiteralPath keeps filepath glob metacharacters in
// a checkout name from changing which make and documentation files are read.
func TestGenerateTreatsRootAsALiteralPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "repo[1]")
	writeFixtureMk(t, root, "building", "## Build the binary.\nbuild:\n\tgo build .\n")
	writeFixtureDoc(t, root, "building.md", "building", "")

	if err := generate(root); err != nil {
		t.Fatal(err)
	}
	doc, err := os.ReadFile(filepath.Join(root, "docs", "developing-evener", "building.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), "`make build`") {
		t.Fatalf("generated target missing from literal-root doc:\n%s", doc)
	}
}

// TestGenerateRejectsDirectoryNamedLikeFamilyFile preserves the old loud
// failure for a non-file entry matching make/*.mk instead of silently omitting
// it while processing the valid families beside it.
func TestGenerateRejectsDirectoryNamedLikeFamilyFile(t *testing.T) {
	root := t.TempDir()
	writeFixtureMk(t, root, "building", "## Build the binary.\nbuild:\n\tgo build .\n")
	writeFixtureDoc(t, root, "building.md", "building", "")
	badPath := filepath.Join(root, "make", "broken.mk")
	if err := os.Mkdir(badPath, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := generate(root); err == nil || !strings.Contains(err.Error(), "broken.mk") {
		t.Fatalf("matching directory was not reported: %v", err)
	}
}

// TestCheckOrphanRegionsRejectsDirectoryNamedLikeDoc preserves the same loud
// behavior for a docs entry matching *.md that cannot be read as a document.
func TestCheckOrphanRegionsRejectsDirectoryNamedLikeDoc(t *testing.T) {
	docDir := t.TempDir()
	badPath := filepath.Join(docDir, "broken.md")
	if err := os.Mkdir(badPath, 0o755); err != nil {
		t.Fatal(err)
	}

	errs := checkOrphanRegions(docDir, map[string]bool{})
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "broken.md") {
		t.Fatalf("matching directory was not reported: %v", errs)
	}
}

// TestGenerateErrorsWhenRootHoldsNoFamilyFiles pins the guard that keeps a
// wrong root loud. Reading a missing directory yields no family entries, so a
// generator pointed at the wrong tree — the mistake a //go:generate directive
// invites, since go generate runs it from the package's own directory — would
// rewrite nothing and exit zero, and lint-generated would then diff six
// unchanged docs and pass forever.
func TestGenerateErrorsWhenRootHoldsNoFamilyFiles(t *testing.T) {
	root := t.TempDir()
	writeFixtureDoc(t, root, "building.md", "building", "")

	err := generate(root)
	if err == nil {
		t.Fatal("generate succeeded with no make/*.mk under root; a wrong root must fail loudly, not regenerate nothing")
	}
	if !strings.Contains(err.Error(), "matched no family files") {
		t.Fatalf("error does not name the empty family set: %v", err)
	}
}

// TestPrintHelpErrorsWhenRootHoldsNoFamilyFiles is the same guard on the
// help path: `make help` listing nothing is a failure, not an empty repo.
func TestPrintHelpErrorsWhenRootHoldsNoFamilyFiles(t *testing.T) {
	root := t.TempDir()

	var buf bytes.Buffer
	err := printHelp(&buf, root)
	if err == nil {
		t.Fatal("printHelp succeeded with no make/*.mk under root")
	}
	if !strings.Contains(err.Error(), "matched no family files") {
		t.Fatalf("error does not name the empty family set: %v", err)
	}
}

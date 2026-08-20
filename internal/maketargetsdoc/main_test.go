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
	if !strings.Contains(string(linting), "| Command | What it proves | Trigger | Requires | Fails when |") {
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

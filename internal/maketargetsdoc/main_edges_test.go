package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFilesWithSuffixReadDirError covers the non-IsNotExist error path in
// filesWithSuffix by creating a regular file where a directory is expected.
func TestFilesWithSuffixReadDirError(t *testing.T) {
	// Create a regular file, then try to ReadDir it — this yields a non-IsNotExist error.
	dir := t.TempDir()
	conflict := filepath.Join(dir, "notadir")
	if err := os.WriteFile(conflict, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	paths, err := filesWithSuffix(conflict, ".mk")
	if err == nil {
		t.Fatalf("filesWithSuffix on a file should error, got paths=%v", paths)
	}
	if paths != nil {
		t.Fatalf("filesWithSuffix on a file should return nil paths, got %v", paths)
	}
}

// TestFamilyFilesReadDirError covers the error-propagation path in familyFiles
// when filesWithSuffix returns a non-IsNotExist error.
func TestFamilyFilesReadDirError(t *testing.T) {
	root := t.TempDir()
	// Create a regular file named "make" so filepath.Join(root, "make") is a
	// file, not a directory — ReadDir will fail with a non-IsNotExist error.
	if err := os.WriteFile(filepath.Join(root, "make"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := familyFiles(root)
	if err == nil {
		t.Fatalf("familyFiles with make/ as a file should error")
	}
}

// TestGenerateOneRewriteRegionError covers the RewriteRegion error path in
// generateOne by providing a doc with malformed markers.
func TestGenerateOneRewriteRegionError(t *testing.T) {
	root := t.TempDir()
	writeFixtureMk(t, root, "building", "## Build the binary.\nbuild:\n\tgo build .\n")
	docDir := filepath.Join(root, "docs", "developing-evener")
	if err := os.MkdirAll(docDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a doc with an unpaired begin marker so RewriteRegion fails.
	doc := beginMarker("building") + "no end marker here\n"
	if err := os.WriteFile(filepath.Join(docDir, "building.md"), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	err := generateOne(docDir, filepath.Join(root, "make", "building.mk"), "building")
	if err == nil {
		t.Fatalf("generateOne with malformed doc should error")
	}
}

// TestGenerateOneWriteFileError covers the os.WriteFile error path in
// generateOne by making the doc path unwritable.
func TestGenerateOneWriteFileError(t *testing.T) {
	root := t.TempDir()
	writeFixtureMk(t, root, "building", "## Build the binary.\nbuild:\n\tgo build .\n")
	docDir := filepath.Join(root, "docs", "developing-evener")
	if err := os.MkdirAll(docDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Write a valid doc with a proper marked region.
	writeFixtureDoc(t, root, "building.md", "building", "")
	docPath := filepath.Join(docDir, "building.md")

	// Make the doc read-only so the write fails. The generate function only
	// writes when the content changes, so the doc must produce different
	// content than what's already on disk (the empty body will change).
	if err := os.Chmod(docPath, 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(docPath, 0o644) })

	err := generateOne(docDir, filepath.Join(root, "make", "building.mk"), "building")
	if err == nil {
		t.Fatalf("generateOne with read-only doc should error on write")
	}
}

// TestCheckOrphanRegionsReadDirError covers the filesWithSuffix error path in
// checkOrphanRegions by passing a path that is a file, not a directory.
func TestCheckOrphanRegionsReadDirError(t *testing.T) {
	// Create a regular file and pass it as docDir — ReadDir will fail.
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

// TestGeneratePropagatesFamilyFilesError covers the error path in generate
// where familyFiles returns an error (make/ is a file, not a directory).
func TestGeneratePropagatesFamilyFilesError(t *testing.T) {
	root := t.TempDir()
	// Create "make" as a regular file so familyFiles fails.
	if err := os.WriteFile(filepath.Join(root, "make"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := generate(root)
	if err == nil {
		t.Fatalf("generate with make/ as a file should error")
	}
	if !strings.Contains(err.Error(), "make") {
		t.Fatalf("error should mention make: %v", err)
	}
}

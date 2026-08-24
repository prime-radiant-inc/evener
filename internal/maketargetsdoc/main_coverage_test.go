package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestFilesWithSuffixMissingDir covers the IsNotExist path.
func TestFilesWithSuffixMissingDir(t *testing.T) {
	paths, err := filesWithSuffix(filepath.Join(t.TempDir(), "nonexistent"), ".mk")
	if err != nil {
		t.Fatalf("filesWithSuffix on missing dir should return nil, nil, got %v, %v", paths, err)
	}
	if paths != nil {
		t.Fatalf("filesWithSuffix on missing dir should return nil paths, got %v", paths)
	}
}

// TestFilesWithSuffixEmpty covers a dir with no matching files.
func TestFilesWithSuffixEmpty(t *testing.T) {
	dir := t.TempDir()
	paths, err := filesWithSuffix(dir, ".mk")
	if err != nil {
		t.Fatalf("filesWithSuffix on empty dir: %v", err)
	}
	if len(paths) != 0 {
		t.Fatalf("filesWithSuffix on empty dir should return no paths, got %v", paths)
	}
}

// TestFilesWithSuffixWithMatches covers the positive path.
func TestFilesWithSuffixWithMatches(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.mk"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.mk"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "c.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	paths, err := filesWithSuffix(dir, ".mk")
	if err != nil {
		t.Fatalf("filesWithSuffix: %v", err)
	}
	if len(paths) != 2 {
		t.Fatalf("filesWithSuffix returned %d paths, want 2", len(paths))
	}
	// Should be sorted.
	if !strings.HasSuffix(paths[0], "a.mk") || !strings.HasSuffix(paths[1], "b.mk") {
		t.Fatalf("filesWithSuffix paths not sorted: %v", paths)
	}
}

// TestFamilyFilesNoMatch covers the error path where make/ has no .mk files.
func TestFamilyFilesNoMatch(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "make"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := familyFiles(root)
	if err == nil {
		t.Fatalf("familyFiles with no .mk files should error")
	}
}

// TestFamilyFilesMissingMakeDir covers the path where make/ doesn't exist.
func TestFamilyFilesMissingMakeDir(t *testing.T) {
	root := t.TempDir()
	_, err := familyFiles(root)
	if err == nil {
		t.Fatalf("familyFiles with missing make/ should error")
	}
}

// TestGenerateOneMissingMk covers the os.ReadFile error path in generateOne.
func TestGenerateOneMissingMk(t *testing.T) {
	docDir := filepath.Join(t.TempDir(), "docs", "developing-evener")
	if err := os.MkdirAll(docDir, 0o755); err != nil {
		t.Fatal(err)
	}
	err := generateOne(docDir, filepath.Join(t.TempDir(), "nonexistent.mk"), "building")
	if err == nil {
		t.Fatalf("generateOne with missing .mk should error")
	}
}

// TestGenerateOneMissingDoc covers the os.ReadFile error path for the doc.
func TestGenerateOneMissingDoc(t *testing.T) {
	root := t.TempDir()
	writeFixtureMk(t, root, "building", "# building targets\nbuild: ## Build the binary\n")
	docDir := filepath.Join(root, "docs", "developing-evener")
	if err := os.MkdirAll(docDir, 0o755); err != nil {
		t.Fatal(err)
	}
	err := generateOne(docDir, filepath.Join(root, "make", "building.mk"), "building")
	if err == nil {
		t.Fatalf("generateOne with missing doc should error")
	}
}

// TestGenerateOneUnknownStem covers the stem-not-in-stemToDoc path.
func TestGenerateOneUnknownStem(t *testing.T) {
	err := generateOne(t.TempDir(), "whatever", "unknown-stem")
	if err == nil {
		t.Fatalf("generateOne with unknown stem should error")
	}
	if !strings.Contains(err.Error(), "no destination doc") {
		t.Fatalf("generateOne unknown stem error = %v, want no destination doc", err)
	}
}

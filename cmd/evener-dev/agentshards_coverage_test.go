package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestEnvPositiveIntDefault covers the default-value path.
func TestEnvPositiveIntDefault(t *testing.T) {
	t.Setenv("TEST_POS_INT", "")
	n, err := envPositiveInt("TEST_POS_INT", 7)
	if err != nil || n != 7 {
		t.Fatalf("envPositiveInt default = %d, %v, want 7, nil", n, err)
	}
}

// TestEnvPositiveIntValid covers the valid-value path.
func TestEnvPositiveIntValid(t *testing.T) {
	t.Setenv("TEST_POS_INT", "42")
	n, err := envPositiveInt("TEST_POS_INT", 7)
	if err != nil || n != 42 {
		t.Fatalf("envPositiveInt valid = %d, %v, want 42, nil", n, err)
	}
}

// TestEnvPositiveIntInvalid covers the non-integer error path.
func TestEnvPositiveIntInvalid(t *testing.T) {
	t.Setenv("TEST_POS_INT", "not-a-number")
	_, err := envPositiveInt("TEST_POS_INT", 7)
	if err == nil {
		t.Fatalf("envPositiveInt with non-integer should error")
	}
}

// TestEnvPositiveIntZero covers the zero error path.
func TestEnvPositiveIntZero(t *testing.T) {
	t.Setenv("TEST_POS_INT", "0")
	_, err := envPositiveInt("TEST_POS_INT", 7)
	if err == nil {
		t.Fatalf("envPositiveInt with 0 should error")
	}
}

// TestEnvPositiveIntNegative covers the negative error path.
func TestEnvPositiveIntNegative(t *testing.T) {
	t.Setenv("TEST_POS_INT", "-3")
	_, err := envPositiveInt("TEST_POS_INT", 7)
	if err == nil {
		t.Fatalf("envPositiveInt with -3 should error")
	}
}

// TestEnvFlag covers the envFlag function.
func TestEnvFlag(t *testing.T) {
	t.Setenv("TEST_FLAG", "")
	if envFlag("TEST_FLAG") {
		t.Fatalf("envFlag empty should be false")
	}
	t.Setenv("TEST_FLAG", "0")
	if envFlag("TEST_FLAG") {
		t.Fatalf("envFlag 0 should be false")
	}
	t.Setenv("TEST_FLAG", "1")
	if !envFlag("TEST_FLAG") {
		t.Fatalf("envFlag 1 should be true")
	}
	t.Setenv("TEST_FLAG", "anything")
	if !envFlag("TEST_FLAG") {
		t.Fatalf("envFlag anything should be true")
	}
}

// TestInterrupterExitCodeNoSignal covers the zero-signal path.
func TestInterrupterExitCodeNoSignal(t *testing.T) {
	in := &interrupter{}
	if code := in.exitCode(); code != 0 {
		t.Fatalf("exitCode = %d, want 0", code)
	}
}

// TestInterrupterExitCodeWithSignal covers the 128+signal path.
func TestInterrupterExitCodeWithSignal(t *testing.T) {
	in := &interrupter{}
	in.interrupt(syscall.SIGTERM)
	if code := in.exitCode(); code != 143 {
		t.Fatalf("exitCode = %d, want 143", code)
	}
}

// TestInterrupterDoubleInterrupt covers the idempotent interrupt path.
func TestInterrupterDoubleInterrupt(t *testing.T) {
	in := &interrupter{}
	in.interrupt(syscall.SIGINT)
	in.interrupt(syscall.SIGTERM) // should be a no-op
	if code := in.exitCode(); code != 130 {
		t.Fatalf("exitCode = %d, want 130 (SIGINT)", code)
	}
}

// TestInterrupterAddAfterSignal covers the path where add is called after
// the interrupter has already been signaled.
func TestInterrupterAddAfterSignal(t *testing.T) {
	in := &interrupter{}
	in.interrupt(syscall.SIGTERM)
	// add after signal should not append to pgids; it should try to terminate.
	in.add(9999)
	if len(in.pgids) != 0 {
		t.Fatalf("pgids should be empty after add-with-signal, got %v", in.pgids)
	}
}

// TestFileHasContentEmptyPath covers the empty-path path.
func TestFileHasContentEmptyPath(t *testing.T) {
	if fileHasContent("") {
		t.Fatalf("fileHasContent(\"\") should be false")
	}
}

// TestFileHasContentMissingFile covers the missing-file path.
func TestFileHasContentMissingFile(t *testing.T) {
	if fileHasContent(filepath.Join(t.TempDir(), "nonexistent")) {
		t.Fatalf("fileHasContent on missing file should be false")
	}
}

// TestFileHasContentEmptyFile covers the zero-size path.
func TestFileHasContentEmptyFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(p, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if fileHasContent(p) {
		t.Fatalf("fileHasContent on empty file should be false")
	}
}

// TestFileHasContentNonEmpty covers the positive path.
func TestFileHasContentNonEmpty(t *testing.T) {
	p := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(p, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !fileHasContent(p) {
		t.Fatalf("fileHasContent on non-empty file should be true")
	}
}

// TestCopyFileToMissing covers the missing-file path.
func TestCopyFileToMissing(t *testing.T) {
	var sb strings.Builder
	if copyFileTo(&sb, filepath.Join(t.TempDir(), "nonexistent")) {
		t.Fatalf("copyFileTo on missing file should return false")
	}
}

// TestCopyFileToEmpty covers the empty-file path.
func TestCopyFileToEmpty(t *testing.T) {
	p := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(p, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	if copyFileTo(&sb, p) {
		t.Fatalf("copyFileTo on empty file should return false")
	}
}

// TestCopyFileToNonEmpty covers the positive path.
func TestCopyFileToNonEmpty(t *testing.T) {
	p := filepath.Join(t.TempDir(), "data")
	if err := os.WriteFile(p, []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	if !copyFileTo(&sb, p) {
		t.Fatalf("copyFileTo on non-empty file should return true")
	}
	if sb.String() != "content" {
		t.Fatalf("copyFileTo wrote %q, want %q", sb.String(), "content")
	}
}

// TestReplayMatchingMissingFile covers the read-error path.
func TestReplayMatchingMissingFile(t *testing.T) {
	var sb strings.Builder
	// Should not panic or write anything for a missing file.
	replayMatching(&sb, filepath.Join(t.TempDir(), "nonexistent"), surveyRedLine, 10)
	if sb.Len() != 0 {
		t.Fatalf("replayMatching on missing file should write nothing, got %q", sb.String())
	}
}

// TestReplayMatchingWithMatches covers the positive path.
func TestReplayMatchingWithMatches(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log")
	content := "--- FAIL: TestX\nok\npanic: something\n--- PASS: TestY\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	replayMatching(&sb, p, surveyRedLine, 10)
	lines := strings.Split(strings.TrimRight(sb.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("replayMatching wrote %d lines, want 2: %q", len(lines), sb.String())
	}
	if lines[0] != "--- FAIL: TestX" || lines[1] != "panic: something" {
		t.Fatalf("replayMatching wrote wrong lines: %q", sb.String())
	}
}

// TestReplayMatchingLimit covers the limit parameter.
func TestReplayMatchingLimit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "log")
	content := "--- FAIL: TestA\n--- FAIL: TestB\n--- FAIL: TestC\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	replayMatching(&sb, p, surveyRedLine, 2)
	lines := strings.Split(strings.TrimRight(sb.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("replayMatching with limit 2 wrote %d lines, want 2", len(lines))
	}
}

// TestCachedSurveyPathExplicit covers the explicit cacheDir path.
func TestCachedSurveyPathExplicit(t *testing.T) {
	cfg := shardsConfig{cacheDir: t.TempDir()}
	got := cfg.cachedSurveyPath("TestA\nTestB\n")
	if got == "" {
		t.Fatalf("cachedSurveyPath should not be empty with explicit cacheDir")
	}
	if !strings.HasSuffix(got, ".log") {
		t.Fatalf("cachedSurveyPath should end with .log: %q", got)
	}
}

// TestCachedSurveyPathEmptyGOCACHE covers the path where go env GOCACHE fails.
func TestCachedSurveyPathEmptyGOCACHE(t *testing.T) {
	cfg := shardsConfig{cacheDir: ""}
	// We can't easily make `go env GOCACHE` fail, but we can test with
	// a cacheDir that cannot be created (a path under a file).
	tmp := t.TempDir()
	conflict := filepath.Join(tmp, "file")
	if err := os.WriteFile(conflict, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.cacheDir = filepath.Join(conflict, "sub", "cache")
	got := cfg.cachedSurveyPath("TestA\n")
	if got != "" {
		t.Fatalf("cachedSurveyPath with unwritable cacheDir should return empty, got %q", got)
	}
}

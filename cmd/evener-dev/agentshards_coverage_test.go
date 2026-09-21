package dev

import (
	"fmt"
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

// writeSurveyLog writes content to a fresh file and returns its path.
func writeSurveyLog(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "survey.log")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// replayLines is replaySurveyFailures' output as lines, nil when it wrote
// nothing.
func replayLines(t *testing.T, path string, blocks int) []string {
	t.Helper()
	var sb strings.Builder
	replaySurveyFailures(&sb, path, blocks)
	if sb.Len() == 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(sb.String(), "\n"), "\n")
}

// TestReplaySurveyFailuresMissingFile covers the read-error path: a log that
// is not there writes nothing at all rather than a stray error.
func TestReplaySurveyFailuresMissingFile(t *testing.T) {
	if got := replayLines(t, filepath.Join(t.TempDir(), "nonexistent"), 10); got != nil {
		t.Fatalf("a missing survey log should write nothing, got %q", got)
	}
}

// TestReplaySurveyFailuresShowsAssertionContext is the issue #2121 contract:
// the excerpt carries the failing test's own output, not just its name. The
// assertion lines sit above the verdict (that is where t.Fatal writes them),
// a subtest's verdict is indented output of its parent, and a green test's
// logged noise stays out of the block entirely.
func TestReplaySurveyFailuresShowsAssertionContext(t *testing.T) {
	path := writeSurveyLog(t,
		"=== RUN   TestWrong\n"+
			"    thing_test.go:9: first line of the failure\n"+
			"    thing_test.go:10: the assertion that matters\n"+
			"--- FAIL: TestWrong (0.01s)\n"+
			"    --- FAIL: TestWrong/sub (0.01s)\n"+
			"=== RUN   TestGreen\n"+
			"    thing_test.go:30: green noise\n"+
			"--- PASS: TestGreen (0.00s)\n"+
			"ok  \tpkg\t0.01s\n")
	want := []string{
		"    thing_test.go:9: first line of the failure",
		"    thing_test.go:10: the assertion that matters",
		"--- FAIL: TestWrong (0.01s)",
		"    --- FAIL: TestWrong/sub (0.01s)",
	}
	got := replayLines(t, path, 10)
	if len(got) != len(want) {
		t.Fatalf("replayed %d lines, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d = %q, want %q; whole excerpt:\n%s", i, got[i], want[i], strings.Join(got, "\n"))
		}
	}
	if strings.Contains(strings.Join(got, "\n"), "green noise") {
		t.Fatalf("a green test's output reached the failure excerpt:\n%s", strings.Join(got, "\n"))
	}
}

// TestReplaySurveyFailuresPassingLogSaysNothing pins the other direction: a
// green survey log yields no excerpt, so a passing run's summary gains no
// empty failure block.
func TestReplaySurveyFailuresPassingLogSaysNothing(t *testing.T) {
	path := writeSurveyLog(t,
		"=== RUN   TestGreen\n"+
			"    thing_test.go:5: green noise\n"+
			"--- PASS: TestGreen (0.00s)\n"+
			"PASS\n"+
			"ok  \tpkg\t0.01s\n")
	if got := replayLines(t, path, 10); got != nil {
		t.Fatalf("a green survey log should produce no excerpt, got %q", got)
	}
}

// TestReplaySurveyFailuresShowsPanic covers the other marker: a panic's
// message is on its own line, so the marker replays legibly without the stack.
func TestReplaySurveyFailuresShowsPanic(t *testing.T) {
	path := writeSurveyLog(t,
		"=== RUN   TestPanics\n"+
			"--- FAIL: TestPanics (0.00s)\n"+
			"panic: boom as instructed\n"+
			"\n"+
			"goroutine 1 [running]:\n"+
			"\tpkg.TestPanics(0x0)\n"+
			"\t\tthing_test.go:21 +0x25\n")
	want := []string{"--- FAIL: TestPanics (0.00s)", "panic: boom as instructed"}
	got := replayLines(t, path, 10)
	if len(got) != len(want) {
		t.Fatalf("replayed %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestReplaySurveyFailuresBoundsOutput pins the two bounds. A failing test
// that logs without limit must not carry its whole log into the worktree's
// summary, and a suite with many failures must not either: the excerpt is a
// failure block, not the suite log it is an excerpt of.
func TestReplaySurveyFailuresBoundsOutput(t *testing.T) {
	var spam strings.Builder
	spam.WriteString("=== RUN   TestSpam\n")
	for i := range surveyContextBefore + 5 {
		_, _ = fmt.Fprintf(&spam, "    thing_test.go:%d: line %d\n", i, i)
	}
	spam.WriteString("--- FAIL: TestSpam (0.00s)\n")
	got := replayLines(t, writeSurveyLog(t, spam.String()), 10)
	if len(got) != surveyContextBefore+1 {
		t.Fatalf("replayed %d lines of a noisy failure, want %d:\n%s",
			len(got), surveyContextBefore+1, strings.Join(got, "\n"))
	}
	wantFirst := fmt.Sprintf("    thing_test.go:%d: line %d", 5, 5)
	if got[0] != wantFirst {
		t.Fatalf("excerpt starts at %q, want the last %d output lines (%q)", got[0], surveyContextBefore, wantFirst)
	}

	// The block count, and not the suite, is the other bound.
	got = replayLines(t, writeSurveyLog(t, "--- FAIL: TestA\n--- FAIL: TestB\n--- FAIL: TestC\n"), 2)
	if len(got) != 2 {
		t.Fatalf("replaySurveyFailures with 2 blocks wrote %d lines, want 2", len(got))
	}
}

// TestCachedSurveyPathExplicit covers the explicit cacheDir path.
func TestCachedSurveyPathExplicit(t *testing.T) {
	cfg := shardsConfig{cacheDir: t.TempDir()}
	got := cfg.cachedSurveyPath("TestA\nTestB\n", parsedFlags{}, "")
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
	got := cfg.cachedSurveyPath("TestA\n", parsedFlags{}, "")
	if got != "" {
		t.Fatalf("cachedSurveyPath with unwritable cacheDir should return empty, got %q", got)
	}
}

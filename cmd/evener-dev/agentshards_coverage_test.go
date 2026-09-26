package dev

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	if got := replayLines(t, path, 10); !slices.Equal(got, want) {
		t.Fatalf("replayed %q, want %q", got, want)
	}
}

// TestReplaySurveyFailuresFindsParentAssertionThroughSubtests is the issue
// #2121 regression: a parent can fail after its subtests and other parallel
// test output has been emitted. Its assertion is therefore outside the
// marker's nearby framework-delimited window, but must still reach the survey
// summary.
func TestReplaySurveyFailuresFindsParentAssertionThroughSubtests(t *testing.T) {
	for _, tc := range []struct {
		name      string
		assertion string
	}{
		{"TestRetirementTreeSettleDrainsPendingRootAttention", "    retirement_test.go:42: pending root attention was not drained"},
		{"TestRetirementDelegateIdleEntrypointsClaimFirst", "    retirement_test.go:84: first idle entrypoint did not claim"},
	} {
		var log strings.Builder
		fmt.Fprintf(&log, "=== RUN   %s\n", tc.name)
		log.WriteString(tc.assertion + "\n")
		fmt.Fprintf(&log, "=== RUN   %s/subtest\n", tc.name)
		for i := range surveyContextBefore + surveyContextAfter {
			_, _ = fmt.Fprintf(&log, "    subtest output line %d\n", i)
		}
		fmt.Fprintf(&log, "--- PASS: %s/subtest (0.00s)\n", tc.name)
		fmt.Fprintf(&log, "--- FAIL: %s (0.00s)\n", tc.name)

		gotLines := replayLines(t, writeSurveyLog(t, log.String()), 10)
		got := strings.Join(gotLines, "\n")
		if !strings.Contains(got, tc.assertion) {
			t.Errorf("%s parent assertion was omitted from replay: %q", tc.name, got)
		}
		if len(gotLines) > surveyContextBefore+1 {
			t.Errorf("%s replayed %d lines, want at most %d", tc.name, len(gotLines), surveyContextBefore+1)
		}
	}
}

// TestReplaySurveyFailuresExpandedBlockKeepsAfterContext covers the expanded
// parent path: its after-context can contain an indented nested failure, which
// must not be skipped when the expansion selects older parent diagnostics.
func TestReplaySurveyFailuresExpandedBlockKeepsAfterContext(t *testing.T) {
	path := writeSurveyLog(t,
		"=== RUN   TestParent\n"+
			"    parent_test.go:42: parent assertion\n"+
			"=== RUN   TestParent/subtest\n"+
			"--- PASS: TestParent/subtest (0.00s)\n"+
			"--- FAIL: TestParent (0.00s)\n"+
			"    --- FAIL: TestParent/subtest (0.00s)\n"+
			"        child_test.go:9: nested assertion\n"+
			"=== RUN   TestNext\n")

	want := []string{
		"    parent_test.go:42: parent assertion",
		"--- FAIL: TestParent (0.00s)",
		"    --- FAIL: TestParent/subtest (0.00s)",
		"        child_test.go:9: nested assertion",
	}
	if got := replayLines(t, path, 10); !slices.Equal(got, want) {
		t.Fatalf("expanded failure replayed %q, want parent and nested diagnostics %q", got, want)
	}
}

// TestReplaySurveyFailuresPrefersLateParentAssertion covers a parallel log in
// which earlier diagnostics from the parent or its subtests precede the
// parent's assertion, while another test's verdict sits immediately before
// the parent's verdict. The later assertion is the useful failure detail.
func TestReplaySurveyFailuresPrefersLateParentAssertion(t *testing.T) {
	const assertion = "    parent_test.go:99: late parent assertion"
	var log strings.Builder
	log.WriteString("=== RUN   TestParent\n")
	for i := range surveyContextBefore {
		fmt.Fprintf(&log, "    child_test.go:%d: earlier diagnostic\n", i+1)
	}
	log.WriteString(assertion + "\n")
	log.WriteString("=== RUN   TestParallelSibling\n")
	log.WriteString("--- PASS: TestParallelSibling (0.00s)\n")
	log.WriteString("--- FAIL: TestParent (0.00s)\n")

	got := strings.Join(replayLines(t, writeSurveyLog(t, log.String()), 10), "\n")
	if !strings.Contains(got, assertion) {
		t.Fatalf("late parent assertion was omitted from replay: %q", got)
	}
}

// TestReplaySurveyFailuresKeepsParentAssertionAheadOfNestedDiagnostics covers
// source-located t.Log/t.Error output from a nested subtest. Those lines are
// newer than the parent's assertion, but the parent assertion must still win
// a bounded diagnostic excerpt.
func TestReplaySurveyFailuresKeepsParentAssertionAheadOfNestedDiagnostics(t *testing.T) {
	const assertion = "    parent_test.go:99: parent assertion before nested output"
	var log strings.Builder
	log.WriteString("=== RUN   TestParent\n")
	log.WriteString(assertion + "\n")
	log.WriteString("=== RUN   TestParent/subtest\n")
	for i := range surveyContextBefore {
		fmt.Fprintf(&log, "    nested_test.go:%d: nested diagnostic\n", i+1)
	}
	log.WriteString("--- PASS: TestParent/subtest (0.00s)\n")
	log.WriteString("--- FAIL: TestParent (0.00s)\n")

	got := strings.Join(replayLines(t, writeSurveyLog(t, log.String()), 10), "\n")
	if !strings.Contains(got, assertion) {
		t.Fatalf("parent assertion was crowded out by nested diagnostics: %q", got)
	}
}

// TestReplaySurveyFailuresSeparatesInterleavedSiblingDiagnostics covers the
// top-level parallel shape from go test -v: a sibling resumes after the
// parent's assertion, emits source-located diagnostics, passes, and the
// parent then fails. The sibling's newer lines must not claim the parent slot.
func TestReplaySurveyFailuresSeparatesInterleavedSiblingDiagnostics(t *testing.T) {
	const assertion = "    parent_test.go:99: parent assertion before sibling output"
	var log strings.Builder
	log.WriteString("=== RUN   TestParent\n")
	log.WriteString("=== PAUSE TestParent\n")
	log.WriteString("=== RUN   TestSibling\n")
	log.WriteString("=== PAUSE TestSibling\n")
	log.WriteString("=== CONT  TestParent\n")
	log.WriteString(assertion + "\n")
	log.WriteString("=== CONT  TestSibling\n")
	for i := range surveyContextBefore {
		fmt.Fprintf(&log, "    sibling_test.go:%d: sibling diagnostic\n", i+1)
	}
	log.WriteString("--- PASS: TestSibling (0.00s)\n")
	log.WriteString("--- FAIL: TestParent (0.00s)\n")

	got := strings.Join(replayLines(t, writeSurveyLog(t, log.String()), 10), "\n")
	if !strings.Contains(got, assertion) {
		t.Fatalf("parent assertion was crowded out by sibling diagnostics: %q", got)
	}
}

// TestReplaySurveyFailuresUsesNameFrameForSiblingOwnership covers Go 1.27's
// real parallel shape: a sibling emits a diagnostic, testing switches output
// ownership with NAME, and the parent emits its assertion before more sibling
// diagnostics. The parent assertion must survive, while the first sibling
// diagnostic must not be promoted into the parent's diagnostic budget.
func TestReplaySurveyFailuresUsesNameFrameForSiblingOwnership(t *testing.T) {
	const (
		firstSibling = "    sibling_test.go:1: first sibling diagnostic"
		assertion    = "    parent_test.go:99: parent assertion after NAME"
	)
	var log strings.Builder
	log.WriteString("=== RUN   TestParent\n")
	log.WriteString("=== PAUSE TestParent\n")
	log.WriteString("=== RUN   TestSibling\n")
	log.WriteString("=== PAUSE TestSibling\n")
	log.WriteString("=== CONT  TestParent\n")
	log.WriteString("=== CONT  TestSibling\n")
	log.WriteString(firstSibling + "\n")
	log.WriteString("=== NAME  TestParent\n")
	log.WriteString(assertion + "\n")
	log.WriteString("=== NAME  TestSibling\n")
	for i := range surveyContextBefore + 1 {
		fmt.Fprintf(&log, "    sibling_test.go:%d: later sibling diagnostic\n", i+2)
	}
	log.WriteString("--- PASS: TestSibling (0.00s)\n")
	log.WriteString("--- FAIL: TestParent (0.00s)\n")

	got := strings.Join(replayLines(t, writeSurveyLog(t, log.String()), 10), "\n")
	if !strings.Contains(got, assertion) {
		t.Fatalf("parent assertion was omitted after NAME frame: %q", got)
	}
	if strings.Contains(got, firstSibling) {
		t.Fatalf("first sibling diagnostic was favored as parent output: %q", got)
	}
}

// TestReplaySurveyFailuresKeepsUnindentedFailureOutput is the D1 contract: a
// failing test's unindented direct output (fmt.Println, log.Print, a child
// process) sits with its verdict, and the excerpt must carry it. The framework
// frames those lines with unindented output of its own, so a block runs from
// the previous framework line to the next one rather than stopping at the
// first line that is not indented.
func TestReplaySurveyFailuresKeepsUnindentedFailureOutput(t *testing.T) {
	path := writeSurveyLog(t,
		"=== RUN   TestPrints\n"+
			"WORKER: building cache index\n"+
			"2026/09/21 21:28:27 worker: connecting to peer\n"+
			"    thing_test.go:12: a framed log line\n"+
			"    thing_test.go:13: the fatal message\n"+
			"--- FAIL: TestPrints (0.00s)\n"+
			"=== RUN   TestNext\n")
	want := []string{
		"WORKER: building cache index",
		"2026/09/21 21:28:27 worker: connecting to peer",
		"    thing_test.go:12: a framed log line",
		"    thing_test.go:13: the fatal message",
		"--- FAIL: TestPrints (0.00s)",
	}
	if got := replayLines(t, path, 10); !slices.Equal(got, want) {
		t.Fatalf("replayed %q, want the failure's own unindented output carried %q", got, want)
	}

	// The before bound still holds: a failure with more output ahead than a
	// block keeps shows only the last surveyContextBefore lines of it, and the
	// green test's own run line ends the block above.
	var noisy strings.Builder
	noisy.WriteString("=== RUN   TestNoisy\n")
	for i := range surveyContextBefore + surveyContextAfter {
		_, _ = fmt.Fprintf(&noisy, "WORKER: output line %d\n", i)
	}
	noisy.WriteString("--- FAIL: TestNoisy (0.00s)\n")
	got := replayLines(t, writeSurveyLog(t, noisy.String()), 10)
	if len(got) != surveyContextBefore+1 {
		t.Fatalf("replayed %d lines of a noisy failure, want %d:\n%s",
			len(got), surveyContextBefore+1, strings.Join(got, "\n"))
	}
	if wantFirst := fmt.Sprintf("WORKER: output line %d", surveyContextAfter); got[0] != wantFirst {
		t.Fatalf("excerpt starts at %q, want %q: the before bound keeps the %d output lines nearest the verdict", got[0], wantFirst, surveyContextBefore)
	}
}

// TestReplaySurveyFailuresKeepsVerdictLikeTestOutput is the review finding on
// the D1 fix: the boundary predicate matched `FAIL`, `PASS`, and `ok` by
// prefix, so a failing test's own `FAIL: ...`, `PASS: ...`, `FAILURE: ...`,
// `ok done`, or `PASSWORD=...` line was read as toolchain framing and became a
// boundary that cut the diagnosis out of the excerpt it exists to show. Only
// the toolchain's exact verdict forms may end a block.
func TestReplaySurveyFailuresKeepsVerdictLikeTestOutput(t *testing.T) {
	path := writeSurveyLog(t,
		"=== RUN   TestLooksRed\n"+
			"FAIL: fixture-gamma is green and only looks red\n"+
			"ok done\n"+
			"--- FAIL: TestLooksRed (0.00s)\n"+
			"PASS: peer up\n"+
			"FAILURE: cannot connect\n"+
			"PASSWORD=hunter2\n"+
			"=== RUN   TestNext\n")
	want := []string{
		"FAIL: fixture-gamma is green and only looks red",
		"ok done",
		"--- FAIL: TestLooksRed (0.00s)",
		"PASS: peer up",
		"FAILURE: cannot connect",
		"PASSWORD=hunter2",
	}
	if got := replayLines(t, path, 10); !slices.Equal(got, want) {
		t.Fatalf("a failing test's verdict-like output ended the block: replayed %q, want %q", got, want)
	}
}

// TestReplaySurveyFailuresVerdictFormsStillBound pins the other side of the
// same predicate: the toolchain's exact verdict forms still end a block. A
// test's own `ok done` is output, while `go test`'s summary `ok  \tpkg` is
// framing; the binary's bare `PASS` and the package verdict `FAIL\tpkg` follow
// suit. Under the old prefix match the first block ended at the `ok done` line,
// so this case was red too.
func TestReplaySurveyFailuresVerdictFormsStillBound(t *testing.T) {
	path := writeSurveyLog(t,
		"--- FAIL: TestFirst (0.00s)\n"+
			"ok done\n"+
			"ok  \tpkg\t0.01s\n"+
			"--- FAIL: TestSecond (0.00s)\n"+
			"    thing_test.go:1: the second failure\n"+
			"PASS\n"+
			"FAIL\tpkg\t0.01s\n")
	want := []string{
		"--- FAIL: TestFirst (0.00s)",
		"ok done",
		"--- FAIL: TestSecond (0.00s)",
		"    thing_test.go:1: the second failure",
	}
	if got := replayLines(t, path, 10); !slices.Equal(got, want) {
		t.Fatalf("the toolchain's exact verdict forms no longer bound a block: replayed %q, want %q", got, want)
	}
}

// TestReplaySurveyFailuresKeepsFramingLookalikes is the follow-up review
// finding: the boundary predicate matched `=== `, `--- `, and the verdict
// tokens by prefix, so a failing test's own unindented output shaped like the
// framing — a printed diff's `--- expected`, or a line that merely begins with
// `--- FAILURE:`, `FAIL `, `PASS `, or `ok  ` — was read as toolchain framing
// (or, for `--- FAILURE:`, as a failure marker) and cut the diagnosis out of
// the excerpt it exists to show. Only the toolchain's actual framing grammar
// may end a block or announce a failure, and every line here stays.
func TestReplaySurveyFailuresKeepsFramingLookalikes(t *testing.T) {
	path := writeSurveyLog(t,
		"=== RUN   TestLooksFramed\n"+
			"--- expected\n"+
			"--- FAILURE: not really a verdict\n"+
			"FAIL reason\n"+
			"PASS details\n"+
			"ok  details\n"+
			"--- FAIL: TestLooksFramed (0.00s)\n")
	want := []string{
		"--- expected",
		"--- FAILURE: not really a verdict",
		"FAIL reason",
		"PASS details",
		"ok  details",
		"--- FAIL: TestLooksFramed (0.00s)",
	}
	if got := replayLines(t, path, 10); !slices.Equal(got, want) {
		t.Fatalf("a failing test's framing-shaped output ended the block: replayed %q, want %q", got, want)
	}
}

// TestReplaySurveyFailuresMarkerlessTailWithTestOKPrint is the D2 contract:
// the excerpt runs only once the survey pass has already exited nonzero, so
// there is no green verdict to consult. A log with no failure marker whose
// last line is the failing test's own unindented `ok done` print — which the
// removed green-verdict check read as the toolchain's `ok  pkg` and suppressed
// the fallback for — must still print its bounded tail.
func TestReplaySurveyFailuresMarkerlessTailWithTestOKPrint(t *testing.T) {
	path := writeSurveyLog(t,
		"=== RUN   TestDies\n"+
			"worker: about to die\n"+
			"ok done\n")
	want := []string{
		"=== RUN   TestDies",
		"worker: about to die",
		"ok done",
	}
	if got := replayLines(t, path, 10); !slices.Equal(got, want) {
		t.Fatalf("a markerless red log ending in a test's own %q replayed %q, want its bounded tail %q", "ok done", got, want)
	}
}

// TestReplaySurveyFailuresMarkerlessFailureShowsTail covers the red log with no
// marker at all: a survey that dies with a fatal error, an os.Exit, or a kill
// leaves no `--- FAIL`/`panic:` block behind, and the excerpt must not be left
// empty. A bounded tail of the log stands in.
func TestReplaySurveyFailuresMarkerlessFailureShowsTail(t *testing.T) {
	path := writeSurveyLog(t,
		"=== RUN   TestExits\n"+
			"dying hard, with no verdict and no marker\n")
	want := []string{
		"=== RUN   TestExits",
		"dying hard, with no verdict and no marker",
	}
	if got := replayLines(t, path, 10); !slices.Equal(got, want) {
		t.Fatalf("a markerless red log replayed %q, want its bounded tail %q", got, want)
	}

	// The tail is bounded like a block: a crash dump must not carry its whole
	// stack into the summary. The excerpt keeps the last surveyTailLines lines.
	var crash strings.Builder
	crash.WriteString("=== RUN   TestCrashes\n")
	for i := range surveyTailLines + surveyContextAfter {
		_, _ = fmt.Fprintf(&crash, "    crash frame %d\n", i)
	}
	got := replayLines(t, writeSurveyLog(t, crash.String()), 10)
	if len(got) != surveyTailLines {
		t.Fatalf("a markerless crash replayed %d lines, want the bounded tail of %d:\n%s",
			len(got), surveyTailLines, strings.Join(got, "\n"))
	}
	if wantFirst := fmt.Sprintf("    crash frame %d", surveyContextAfter); got[0] != wantFirst {
		t.Fatalf("tail starts at %q, want %q: the excerpt is the END of the log", got[0], wantFirst)
	}
}

// TestReplaySurveyFailuresAdjacentFailuresDoNotOverlap pins block boundaries:
// two failures close enough that their context windows would overlap print each
// line once, not twice.
func TestReplaySurveyFailuresAdjacentFailuresDoNotOverlap(t *testing.T) {
	path := writeSurveyLog(t,
		"--- FAIL: TestFirst (0.00s)\n"+
			"    thing_test.go:1: first assertion\n"+
			"--- FAIL: TestSecond (0.00s)\n"+
			"    thing_test.go:2: second assertion\n")
	want := []string{
		"--- FAIL: TestFirst (0.00s)",
		"    thing_test.go:1: first assertion",
		"--- FAIL: TestSecond (0.00s)",
		"    thing_test.go:2: second assertion",
	}
	if got := replayLines(t, path, 10); !slices.Equal(got, want) {
		t.Fatalf("adjacent failures replayed %q, want each line once %q", got, want)
	}
}

// TestReplaySurveyFailuresShowsPanic covers the other marker: `panic:` is a
// framework line too, so the failing test's verdict block ends at the panic
// line and the panic's own block carries its message plus the stack head, up
// to the after bound.
func TestReplaySurveyFailuresShowsPanic(t *testing.T) {
	path := writeSurveyLog(t,
		"=== RUN   TestPanics\n"+
			"--- FAIL: TestPanics (0.00s)\n"+
			"panic: boom as instructed\n"+
			"\n"+
			"goroutine 1 [running]:\n"+
			"\tpkg.TestPanics(0x0)\n"+
			"\t\tthing_test.go:21 +0x25\n")
	want := []string{
		"--- FAIL: TestPanics (0.00s)",
		"panic: boom as instructed",
		"",
		"goroutine 1 [running]:",
		"\tpkg.TestPanics(0x0)",
		"\t\tthing_test.go:21 +0x25",
	}
	if got := replayLines(t, path, 10); !slices.Equal(got, want) {
		t.Fatalf("replayed %q, want %q", got, want)
	}
}

// TestReplaySurveyFailuresBoundsOutput pins the two bounds. A failing test
// that logs without limit must not carry its whole log into the worktree's
// summary, and a suite with many failures must not either: the excerpt is a
// failure block, not the suite log it is an excerpt of.
func TestReplaySurveyFailuresBoundsOutput(t *testing.T) {
	// More output ahead of the marker than a block keeps: the block starts
	// surveyContextBefore lines above the verdict, dropping the
	// surveyContextAfter lines that sit ahead of those.
	var spam strings.Builder
	spam.WriteString("=== RUN   TestSpam\n")
	for i := range surveyContextBefore + surveyContextAfter {
		_, _ = fmt.Fprintf(&spam, "    thing_test.go:%d: line %d\n", i, i)
	}
	spam.WriteString("--- FAIL: TestSpam (0.00s)\n")
	got := replayLines(t, writeSurveyLog(t, spam.String()), 10)
	if len(got) != surveyContextBefore+1 {
		t.Fatalf("replayed %d lines of a noisy failure, want %d:\n%s",
			len(got), surveyContextBefore+1, strings.Join(got, "\n"))
	}
	wantFirst := fmt.Sprintf("    thing_test.go:%d: line %d", surveyContextAfter, surveyContextAfter)
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
	cfg := shardsConfig{label: "agent", envPrefix: "AGENT", cacheDir: ""}
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

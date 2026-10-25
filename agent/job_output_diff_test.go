package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// syntheticUnifiedDiff builds a deterministic git-style unified diff. Every
// body line carries the hunk index so a test can tell one hunk's bytes from
// another's.
func syntheticUnifiedDiff(file string, hunks, linesPerHunk int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\nindex 1111111..2222222 100644\n--- a/%s\n+++ b/%s\n", file, file, file, file)
	line := 1
	for h := range hunks {
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@ func h%d()\n", line, linesPerHunk, line, linesPerHunk, h)
		for j := range linesPerHunk {
			switch j % 4 {
			case 0:
				fmt.Fprintf(&b, " context h%02d line %03d %s\n", h, j, strings.Repeat("c", 24))
			case 1:
				fmt.Fprintf(&b, "-removed h%02d line %03d %s\n", h, j, strings.Repeat("r", 24))
			case 2:
				fmt.Fprintf(&b, "+added h%02d line %03d %s\n", h, j, strings.Repeat("a", 24))
			case 3:
				fmt.Fprintf(&b, " context h%02d line %03d %s\n", h, j, strings.Repeat("d", 24))
			}
		}
		line += linesPerHunk
	}
	return b.String()
}

func testDiffWindow(t *testing.T, full string, startsMidStream bool) diffWindow {
	t.Helper()
	w, ok := unifiedDiffWindow(full, shellDigestHalfBytes, shellDigestHalfBytes, startsMidStream)
	if !ok {
		t.Fatalf("unifiedDiffWindow did not recognize a git-style unified diff")
	}
	if w.HeadStart < 0 || w.HeadEnd < w.HeadStart || w.HeadEnd > len(full) {
		t.Fatalf("invalid head window [%d,%d) for %d bytes", w.HeadStart, w.HeadEnd, len(full))
	}
	if w.HeadEnd-w.HeadStart > shellDigestHalfBytes {
		t.Fatalf("head window is %d bytes, want at most %d", w.HeadEnd-w.HeadStart, shellDigestHalfBytes)
	}
	if w.TailStart < w.HeadEnd || w.TailStart > len(full) {
		t.Fatalf("invalid tail start %d for head end %d and %d bytes", w.TailStart, w.HeadEnd, len(full))
	}
	if len(full)-w.TailStart > shellDigestHalfBytes {
		t.Fatalf("tail window is %d bytes, want at most %d", len(full)-w.TailStart, shellDigestHalfBytes)
	}
	return w
}

func offsetOf(t *testing.T, full, needle string) int {
	t.Helper()
	offset := strings.Index(full, needle)
	if offset < 0 {
		t.Fatalf("fixture has no %q", needle)
	}
	return offset
}

func markerBetween(digest, head, tail string) string {
	start := len(head)
	end := len(digest) - len(tail)
	if end < start {
		return ""
	}
	return digest[start:end]
}

var (
	omittedRangePattern = regexp.MustCompile(`omitted \[(\d+),(\d+)\)`)
	headStartPattern    = regexp.MustCompile(`head starts at byte (\d+)`)
)

func assertOmittedRange(t *testing.T, digest string, start, end int) {
	t.Helper()
	match := omittedRangePattern.FindStringSubmatch(digest)
	if match == nil {
		t.Fatalf("digest has no omitted byte range:\n%s", digest)
	}
	if match[1] != strconv.Itoa(start) || match[2] != strconv.Itoa(end) {
		t.Fatalf("omitted range = [%s,%s), want [%d,%d)", match[1], match[2], start, end)
	}
}

func assertHeadStartsAt(t *testing.T, digest string, want int) {
	t.Helper()
	match := headStartPattern.FindStringSubmatch(digest)
	if match == nil {
		t.Fatalf("digest does not state the visible head's byte offset:\n%s", digest)
	}
	if match[1] != strconv.Itoa(want) {
		t.Fatalf("head starts at byte %s, want %d", match[1], want)
	}
}

// digestSourcePrefix returns the length of the longest prefix of full the
// digest reproduces. No diff line begins with the marker's leading ellipsis, so
// this is the head window the digest selected.
func digestSourcePrefix(digest, full string) int {
	n := 0
	for n < len(full) && n < len(digest) && full[n] == digest[n] {
		n++
	}
	return n
}

// digestSourceSuffix is digestSourcePrefix from the end of full.
func digestSourceSuffix(digest, full string) int {
	n := 0
	for n < len(full) && n < len(digest) && full[len(full)-1-n] == digest[len(digest)-1-n] {
		n++
	}
	return n
}

func TestShellInlineDigestLargeDiffCutsAtHunkBoundaries(t *testing.T) {
	t.Parallel()
	full := syntheticUnifiedDiff("big.go", 30, 8)
	if len(full) <= shellRideWholeBytes {
		t.Fatalf("fixture is %d bytes, want more than the %d-byte ride-whole threshold", len(full), shellRideWholeBytes)
	}
	w := testDiffWindow(t, full, false)

	wantHeadEnd := offsetOf(t, full, "@@ -9,8")
	if w.HeadEnd != wantHeadEnd {
		t.Fatalf("head end = %d, want the second hunk header at %d", w.HeadEnd, wantHeadEnd)
	}
	if !strings.Contains(full[w.HeadStart:w.HeadEnd], "@@ -1,8") {
		t.Fatalf("head does not contain the first complete hunk:\n%s", full[w.HeadStart:w.HeadEnd])
	}
	wantTailStart := offsetOf(t, full, "@@ -233,8")
	if w.TailStart != wantTailStart {
		t.Fatalf("tail start = %d, want the last hunk header at %d", w.TailStart, wantTailStart)
	}
	if w.File != "b/big.go" {
		t.Fatalf("cut file = %q, want b/big.go", w.File)
	}
	if w.Hunk != "@@ -9,8 +9,8 @@ func h1()" {
		t.Fatalf("cut hunk = %q, want the second hunk header", w.Hunk)
	}

	digest := shellInlineDigest(full, int64(len(full)), 0)
	head, tail := full[w.HeadStart:w.HeadEnd], full[w.TailStart:]
	if got := digestSourcePrefix(digest, full); got != w.HeadEnd {
		t.Fatalf("digest head = %d bytes, want the boundary at %d; the old digest ends at %d mid-hunk", got, w.HeadEnd, got)
	}
	// The digest writes one separator newline before the tail; the byte before
	// it is the marker's ellipsis, so the source suffix match stops there.
	if got := digestSourceSuffix(digest, full); got != len(tail)+1 {
		t.Fatalf("digest tail = %d bytes, want exactly the %d-byte tail plus its separator", got, len(tail))
	}
	if !strings.HasPrefix(digest, head) {
		t.Fatalf("digest does not begin with the boundary-aligned head:\n%s", digest)
	}
	if !strings.HasSuffix(digest, tail) {
		t.Fatalf("digest does not end with the boundary-aligned tail:\n%s", digest)
	}
	marker := markerBetween(digest, head, tail)
	assertOmittedRange(t, marker, w.HeadEnd, w.TailStart)
	if !strings.Contains(marker, w.Hunk) {
		t.Fatalf("marker does not name the first omitted hunk %q:\n%s", w.Hunk, marker)
	}
	if strings.Contains(digest, "added h15") || strings.Contains(digest, "removed h15") {
		t.Fatalf("digest contains a body line from the omitted middle:\n%s", digest)
	}
}

func TestShellInlineDigestSingleHugeHunkCutsBeforeTheHunk(t *testing.T) {
	t.Parallel()
	full := syntheticUnifiedDiff("huge.go", 1, 400)
	if len(full) <= shellRideWholeBytes {
		t.Fatalf("fixture is %d bytes, want more than the %d-byte ride-whole threshold", len(full), shellRideWholeBytes)
	}
	w := testDiffWindow(t, full, false)
	wantHeadEnd := offsetOf(t, full, "@@ -1,400")
	if w.HeadEnd != wantHeadEnd {
		t.Fatalf("head end = %d, want the single hunk header at %d", w.HeadEnd, wantHeadEnd)
	}
	for _, header := range []string{"diff --git a/huge.go b/huge.go", "--- a/huge.go", "+++ b/huge.go"} {
		if !strings.Contains(full[:w.HeadEnd], header) {
			t.Fatalf("head lacks file header %q:\n%s", header, full[:w.HeadEnd])
		}
	}
	if w.TailStart <= w.HeadEnd {
		t.Fatalf("tail start = %d, want a line-aligned trailing peek after head end %d", w.TailStart, w.HeadEnd)
	}
	if w.TailStart > 0 && full[w.TailStart-1] != '\n' {
		t.Fatalf("tail start %d is not a line boundary", w.TailStart)
	}
	if strings.HasPrefix(full[w.TailStart:], "@@ ") || strings.HasPrefix(full[w.TailStart:], "diff --git ") {
		t.Fatalf("tail start %d is a diff boundary; this fixture should have none in the tail budget", w.TailStart)
	}

	digest := shellInlineDigest(full, int64(len(full)), 0)
	if got := digestSourcePrefix(digest, full); got != w.HeadEnd {
		t.Fatalf("digest head = %d bytes, want the hunk boundary at %d", got, w.HeadEnd)
	}
	if !strings.HasPrefix(digest, full[:w.HeadEnd]) {
		t.Fatalf("digest does not begin with the file header:\n%s", digest)
	}
	if !strings.HasSuffix(digest, full[w.TailStart:]) {
		t.Fatalf("digest does not end with the line-aligned trailing peek:\n%s", digest)
	}
	if !strings.Contains(digest, "@@ -1,400 +1,400 @@ func h0()") {
		t.Fatalf("digest does not name the omitted hunk:\n%s", digest)
	}
	assertOmittedRange(t, digest, w.HeadEnd, w.TailStart)
	if strings.Contains(full[:w.HeadEnd], "removed h00") || strings.Contains(full[:w.HeadEnd], "added h00") {
		t.Fatalf("head contains a body line from the single huge hunk:\n%s", full[:w.HeadEnd])
	}
}

func TestShellInlineDigestMixedDiffAndLogKeepsLineTail(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString(syntheticUnifiedDiff("a.go", 2, 8))
	for i := range 400 {
		fmt.Fprintf(&b, "log line %03d %s\n", i, strings.Repeat("L", 24))
	}
	full := b.String()
	w := testDiffWindow(t, full, false)
	if w.HeadEnd-w.HeadStart == 0 {
		t.Fatal("mixed output produced an empty head")
	}
	if w.TailStart <= w.HeadEnd {
		t.Fatalf("tail start = %d, want a trailing peek after head end %d", w.TailStart, w.HeadEnd)
	}
	if w.TailStart > 0 && full[w.TailStart-1] != '\n' {
		t.Fatalf("tail start %d is not a line boundary", w.TailStart)
	}

	digest := shellInlineDigest(full, int64(len(full)), 0)
	if !strings.HasSuffix(digest, full[w.TailStart:]) {
		t.Fatalf("digest does not end with the trailing log window:\n%s", digest)
	}
	if !strings.Contains(digest, "log line 399") {
		t.Fatalf("digest lost the last line of mixed output:\n%s", digest)
	}
}

func TestShellInlineDigestQuotedDiffInsideLogIsNotADiff(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	for i := range 100 {
		fmt.Fprintf(&b, "build log line %03d %s\n", i, strings.Repeat("L", 24))
	}
	b.WriteString(syntheticUnifiedDiff("quoted.go", 2, 8))
	for i := range 300 {
		fmt.Fprintf(&b, "later log line %03d %s\n", i, strings.Repeat("M", 24))
	}
	full := b.String()
	if _, ok := unifiedDiffWindow(full, shellDigestHalfBytes, shellDigestHalfBytes, false); ok {
		t.Fatal("a quoted diff inside ordinary output was treated as the output's own diff")
	}
	digest := shellInlineDigest(full, int64(len(full)), 0)
	if !strings.HasPrefix(digest, "build log line 000") {
		t.Fatalf("ordinary output lost its head:\n%s", digest)
	}
	if !strings.Contains(digest, "later log line 299") {
		t.Fatalf("ordinary output lost its tail:\n%s", digest)
	}
}

func TestShellInlineDigestHunklessSectionNamesFile(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString("diff --git a/small.go b/small.go\nindex 1..2 100644\n--- a/small.go\n+++ b/small.go\n@@ -1,2 +1,2 @@\n-old\n+new\n")
	b.WriteString("diff --git a/mode.go b/mode.go\nold mode 100644\nnew mode 100755\n")
	b.WriteString("diff --git a/later.go b/later.go\nindex 1..2 100644\n--- a/later.go\n+++ b/later.go\n@@ -1,2 +1,2 @@\n-old\n+new\n")
	full := b.String()
	modeAt := offsetOf(t, full, "diff --git a/mode.go b/mode.go")
	w, ok := unifiedDiffWindow(full, modeAt, shellDigestHalfBytes, false)
	if !ok {
		t.Fatal("unifiedDiffWindow did not recognize the diff")
	}
	if w.HeadEnd != modeAt {
		t.Fatalf("head end = %d, want the mode-only section start %d", w.HeadEnd, modeAt)
	}
	if w.File != "b/mode.go" {
		t.Fatalf("cut file = %q, want b/mode.go from the diff --git header", w.File)
	}
	if w.Hunk != "" {
		t.Fatalf("cut hunk = %q, want none in a hunkless section", w.Hunk)
	}
}

func TestShellInlineDigestHunkBodyPathLikeLinesStayBody(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString("diff --git a/sql.go b/sql.go\nindex 1111111..2222222 100644\n--- a/sql.go\n+++ b/sql.go\n")
	b.WriteString("@@ -1,60 +1,60 @@ func a()\n")
	// A deleted line whose content starts "-- " renders as "--- " at column 0.
	// It sits inside the hunk and must not become a cut boundary.
	b.WriteString("--- old sql comment\n")
	b.WriteString("+++ new sql comment\n")
	for i := range 40 {
		fmt.Fprintf(&b, " context line %02d %s\n", i, strings.Repeat("c", 24))
	}
	b.WriteString("@@ -100,60 +100,60 @@ func b()\n")
	for i := range 40 {
		fmt.Fprintf(&b, " context line %02d %s\n", i, strings.Repeat("d", 24))
	}
	full := b.String()
	w := testDiffWindow(t, full, false)
	wantHeadEnd := offsetOf(t, full, "@@ -1,60")
	if w.HeadEnd != wantHeadEnd {
		t.Fatalf("head end = %d, want the first hunk header at %d (body '--- ' must not be a boundary)", w.HeadEnd, wantHeadEnd)
	}
	if strings.Contains(full[:w.HeadEnd], "old sql comment") {
		t.Fatalf("head contains a hunk body line:\n%s", full[:w.HeadEnd])
	}
}

func TestShellInlineDigestTinyHunkLinesIgnoreThe200LineCap(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString("diff --git a/tiny b/tiny\nindex 1111111..2222222 100644\n--- a/tiny\n+++ b/tiny\n")
	b.WriteString("@@ -1,6000 +1,6000 @@\n")
	b.WriteString(strings.Repeat("+\n", 6000))
	full := b.String()
	if len(full) <= shellRideWholeBytes {
		t.Fatalf("fixture is %d bytes, want more than the %d-byte ride-whole threshold", len(full), shellRideWholeBytes)
	}
	w := testDiffWindow(t, full, false)
	wantHeadEnd := offsetOf(t, full, "@@ -1,6000")
	if w.HeadEnd != wantHeadEnd {
		t.Fatalf("head end = %d, want the hunk header at %d; the 200-line cap must not cut the head", w.HeadEnd, wantHeadEnd)
	}
	digest := shellInlineDigest(full, int64(len(full)), 0)
	if got := digestSourcePrefix(digest, full); got != w.HeadEnd {
		t.Fatalf("digest head = %d bytes, want the hunk boundary at %d", got, w.HeadEnd)
	}
	if !strings.HasPrefix(digest, full[:w.HeadEnd]) {
		t.Fatalf("digest does not begin with the file header:\n%s", digest)
	}
}

func TestShellInlineDigestEvictedPrefixStartsAtABoundary(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString(" context from an evicted hunk\n")
	b.WriteString("@@ -10,4 +10,4 @@ func kept()\n")
	for i := range 4 {
		fmt.Fprintf(&b, " kept line %02d %s\n", i, strings.Repeat("k", 24))
	}
	b.WriteString("@@ -20,4 +20,4 @@ func omitted()\n")
	for i := range 4 {
		fmt.Fprintf(&b, " omitted line %02d %s\n", i, strings.Repeat("o", 24))
	}
	full := b.String()
	const dropped = 500
	total := int64(len(full) + dropped)

	w, ok := unifiedDiffWindow(full, shellDigestHalfBytes, shellDigestHalfBytes, true)
	if !ok {
		t.Fatal("unifiedDiffWindow did not recognize a mid-stream hunk")
	}
	firstBoundary := offsetOf(t, full, "@@ -10,4")
	secondBoundary := offsetOf(t, full, "@@ -20,4")
	if w.HeadStart != firstBoundary || w.HeadEnd != secondBoundary {
		t.Fatalf("head window = [%d,%d), want the retained boundaries [%d,%d)", w.HeadStart, w.HeadEnd, firstBoundary, secondBoundary)
	}
	digest := shellInlineDigest(full, total, dropped)
	if !strings.HasPrefix(digest, full[w.HeadStart:w.HeadEnd]) {
		t.Fatalf("digest does not begin with the post-eviction head:\n%s", digest)
	}
	assertHeadStartsAt(t, digest, dropped+w.HeadStart)
	assertOmittedRange(t, digest, dropped+w.HeadEnd, dropped+w.TailStart)
}

func TestShellInlineDigestMidStreamHunkStartDoesNotCutBody(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString("@@ -1,6000 +1,6000 @@ func huge()\n")
	b.WriteString(strings.Repeat("+x\n", 3000))
	b.WriteString("diff --git a/z.go b/z.go\nindex 1..2 100644\n--- a/z.go\n+++ b/z.go\n@@ -1,2 +1,2 @@\n-old\n+new\n")
	full := b.String()
	w, ok := unifiedDiffWindow(full, shellDigestHalfBytes, shellDigestHalfBytes, true)
	if !ok {
		t.Fatal("unifiedDiffWindow did not recognize a mid-stream hunk start")
	}
	if w.HeadEnd != w.HeadStart {
		t.Fatalf("head window = [%d,%d), want empty when the retained start is a huge hunk header", w.HeadStart, w.HeadEnd)
	}
	if w.Hunk != "@@ -1,6000 +1,6000 @@ func huge()" {
		t.Fatalf("cut hunk = %q, want the huge hunk header", w.Hunk)
	}
	const dropped = 500
	digest := shellInlineDigest(full, int64(len(full)+dropped), dropped)
	if got := digestSourcePrefix(digest, full); got != 0 {
		t.Fatalf("digest head = %d bytes, want empty when the retained start is a huge hunk header", got)
	}
	if !strings.HasSuffix(digest, full[w.TailStart:]) {
		t.Fatalf("digest does not end with the tail beginning at %d:\n%s", w.TailStart, digest)
	}
	assertOmittedRange(t, digest, dropped+w.HeadEnd, dropped+w.TailStart)
}

func TestShellInlineDigestEvictedPrefixStartsOnGitMetadata(t *testing.T) {
	t.Parallel()
	full := "index 1111111..2222222 100644\n--- a/meta.go\n+++ b/meta.go\n@@ -1,2 +1,2 @@\n-old\n+new\n@@ -10,2 +10,2 @@\n-older\n+newer\n"
	w, ok := unifiedDiffWindow(full, shellDigestHalfBytes, shellDigestHalfBytes, true)
	if !ok {
		t.Fatal("a retained diff starting on a git metadata line was not recognized")
	}
	if w.HeadStart != offsetOf(t, full, "@@ -1,2") {
		t.Fatalf("head start = %d, want the first hunk header", w.HeadStart)
	}
	if w.File != "b/meta.go" {
		t.Fatalf("cut file = %q, want b/meta.go", w.File)
	}
}

func TestShellInlineDigestMidStreamHunkTooFarIsNotADiff(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString(" context from a retained hunk\n")
	for i := range 20 {
		fmt.Fprintf(&b, " context padding line %02d %s\n", i, strings.Repeat("p", 24))
	}
	b.WriteString("@@ -100,2 +100,2 @@\n-old\n+new\n")
	full := b.String()
	if _, ok := unifiedDiffWindow(full, shellDigestHalfBytes, shellDigestHalfBytes, true); ok {
		t.Fatal("a hunk header more than one head budget from the retained start reclassified log output")
	}
}

func TestShellInlineDigestMultiFileCutNamesNextFile(t *testing.T) {
	t.Parallel()
	full := syntheticUnifiedDiff("a.go", 2, 8) + syntheticUnifiedDiff("b.go", 1, 8)
	bAt := offsetOf(t, full, "diff --git a/b.go b/b.go")
	w, ok := unifiedDiffWindow(full, bAt, 100, false)
	if !ok {
		t.Fatal("unifiedDiffWindow did not recognize the multi-file diff")
	}
	if w.HeadEnd != bAt {
		t.Fatalf("head end = %d, want the second file start %d", w.HeadEnd, bAt)
	}
	if w.File != "b/b.go" {
		t.Fatalf("cut file = %q, want b/b.go", w.File)
	}
	if w.Hunk != "@@ -1,8 +1,8 @@ func h0()" {
		t.Fatalf("cut hunk = %q, want the second file's first hunk", w.Hunk)
	}
}

func TestShellInlineDigestRejectsLooseDiffMarkers(t *testing.T) {
	t.Parallel()
	lines := []string{
		"@@ -1 +1 @@",
		"--- not a diff header ---",
		"+++ not a diff header +++",
	}
	for i := range 400 {
		lines = append(lines, fmt.Sprintf("ordinary output line %03d %s", i, strings.Repeat("x", 24)))
	}
	full := strings.Join(lines, "\n") + "\n"
	if _, ok := unifiedDiffWindow(full, shellDigestHalfBytes, shellDigestHalfBytes, false); ok {
		t.Fatal("loose @@/---/+++ markers without a diff --git header were treated as a diff")
	}
	digest := shellInlineDigest(full, int64(len(full)), 0)
	if !strings.HasPrefix(digest, lines[0]) {
		t.Fatalf("non-diff digest lost its head:\n%s", digest)
	}
	if !strings.Contains(digest, lines[len(lines)-1]) {
		t.Fatalf("non-diff digest lost its tail:\n%s", digest)
	}
	if !strings.Contains(digest, "elided") {
		t.Fatalf("non-diff digest lost its elision marker:\n%s", digest)
	}
}

func TestShellToolLargeDiffInlineWindowCutsAtHunkBoundaries(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	full := syntheticUnifiedDiff("big.go", 30, 8)
	if len(full) <= shellRideWholeBytes {
		t.Fatalf("fixture is %d bytes, want more than the %d-byte ride-whole threshold", len(full), shellRideWholeBytes)
	}
	w := testDiffWindow(t, full, false)
	path := filepath.Join(t.TempDir(), "big.diff")
	if err := os.WriteFile(path, []byte(full), 0o600); err != nil {
		t.Fatalf("write diff fixture: %v", err)
	}

	args, err := json.Marshal(map[string]string{"command": "cat -- " + shellSingleQuote(path)})
	if err != nil {
		t.Fatalf("marshal shell args: %v", err)
	}
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "diff-window",
		Name:      "shell",
		Arguments: args,
	})
	if res.IsError {
		t.Fatalf("shell returned error: %s", res.Output)
	}
	var out shellToolResult
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal shell result: %v (output: %s)", err, res.Output)
	}
	if out.JobID == "" || out.Truncated == nil || !*out.Truncated {
		t.Fatalf("large diff result = {job_id:%q truncated:%v}, want a windowed durable job", out.JobID, out.Truncated)
	}
	if out.TotalBytes != int64(len(full)) || out.DroppedBytes != 0 {
		t.Fatalf("output bytes = {total:%d dropped:%d}, want {%d 0}", out.TotalBytes, out.DroppedBytes, len(full))
	}
	if out.Output == nil {
		t.Fatal("large diff result carries no inline output")
	}
	head, tail := full[w.HeadStart:w.HeadEnd], full[w.TailStart:]
	if got := digestSourcePrefix(*out.Output, full); got != w.HeadEnd {
		t.Fatalf("inline diff head = %d bytes, want the boundary at %d", got, w.HeadEnd)
	}
	if got := digestSourceSuffix(*out.Output, full); got != len(tail)+1 {
		t.Fatalf("inline diff tail = %d bytes, want exactly the %d-byte tail plus its separator", got, len(tail))
	}
	if !strings.HasPrefix(*out.Output, head) {
		t.Fatalf("inline diff window does not begin with the boundary-aligned head:\n%s", *out.Output)
	}
	if !strings.HasSuffix(*out.Output, tail) {
		t.Fatalf("inline diff window does not end with the boundary-aligned tail:\n%s", *out.Output)
	}
	assertOmittedRange(t, *out.Output, w.HeadEnd, w.TailStart)
	if strings.Contains(*out.Output, "added h15") || strings.Contains(*out.Output, "removed h15") {
		t.Fatalf("inline diff window contains a body line from the omitted middle:\n%s", *out.Output)
	}
}

// TestShellToolLargeDiffPreservesDefaultLimit pins that the diff-aware window
// does not change the registered shell output limit.
func TestShellToolLargeDiffPreservesDefaultLimit(t *testing.T) {
	t.Parallel()
	reg := newTestSession(t).reg
	if got := shellToolResultMaxChars(reg); got != shellToolResultDefaultMaxChars {
		t.Fatalf("shell result max chars = %d, want the unchanged %d", got, shellToolResultDefaultMaxChars)
	}
	if limit := reg.Get("shell").Limit; limit.MaxChars != 30_000 || limit.MaxLines != 512 || limit.Strategy != schema.TruncHeadTail {
		t.Fatalf("shell registry limit = %+v, want the unchanged default", limit)
	}
}

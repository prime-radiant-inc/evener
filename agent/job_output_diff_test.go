package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/agent/internal/agenttest"
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
	w, ok := unifiedDiffWindow(full, shellDigestHalfBytes, shellDigestHalfBytes, startsMidStream, false, 0)
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

func TestShellInlineDigestLongUnterminatedLineKeepsTail(t *testing.T) {
	t.Parallel()
	diff := syntheticUnifiedDiff("a.go", 2, 8)
	full := diff + strings.Repeat("L", 700)
	w := testDiffWindow(t, full, false)
	if w.TailStart <= w.HeadEnd {
		t.Fatalf("tail start = %d, want a trailing peek after head end %d", w.TailStart, w.HeadEnd)
	}
	if w.TailStart > 0 && full[w.TailStart-1] == '\n' {
		t.Fatalf("tail start %d is a line boundary; want the byte suffix of an unterminated line", w.TailStart)
	}
	digest := shellInlineDigest(full, int64(len(full)), 0)
	if !strings.HasSuffix(digest, full[w.TailStart:]) {
		t.Fatalf("digest does not end with the byte suffix:\n%s", digest)
	}
	if !strings.Contains(digest, strings.Repeat("L", 100)) {
		t.Fatalf("digest lost the long unterminated trailing line:\n%s", digest)
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
	if _, ok := unifiedDiffWindow(full, shellDigestHalfBytes, shellDigestHalfBytes, false, false, 0); ok {
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

func TestShellInlineDigestHunklessSectionReportsRangeWithoutFile(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString("diff --git a/small.go b/small.go\nindex 1..2 100644\n--- a/small.go\n+++ b/small.go\n@@ -1,2 +1,2 @@\n-old\n+new\n")
	b.WriteString("diff --git a/mode.go b/mode.go\nold mode 100644\nnew mode 100755\n")
	b.WriteString(strings.Repeat("literal 0000000 mode padding padding padding padding\n", 30))
	b.WriteString("diff --git a/later.go b/later.go\nindex 1..2 100644\n--- a/later.go\n+++ b/later.go\n@@ -1,2 +1,2 @@\n-old\n+new\n")
	full := b.String()
	modeAt := offsetOf(t, full, "diff --git a/mode.go b/mode.go")
	w, ok := unifiedDiffWindow(full, shellDigestHalfBytes, shellDigestHalfBytes, false, false, 0)
	if !ok {
		t.Fatal("unifiedDiffWindow did not recognize the diff")
	}
	if w.HeadEnd != modeAt {
		t.Fatalf("head end = %d, want the mode-only section start %d", w.HeadEnd, modeAt)
	}
	if w.File != "" {
		t.Fatalf("cut file = %q, want unknown for a hunkless section", w.File)
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

	w, ok := unifiedDiffWindow(full, shellDigestHalfBytes, shellDigestHalfBytes, true, false, 0)
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
}

func TestShellInlineDigestMidStreamHunkStartDoesNotCutBody(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString("@@ -1,6000 +1,6000 @@ func huge()\n")
	b.WriteString(strings.Repeat("+x\n", 3000))
	b.WriteString("diff --git a/z.go b/z.go\nindex 1..2 100644\n--- a/z.go\n+++ b/z.go\n@@ -1,2 +1,2 @@\n-old\n+new\n")
	full := b.String()
	w, ok := unifiedDiffWindow(full, shellDigestHalfBytes, shellDigestHalfBytes, true, false, 0)
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
}

func TestShellInlineDigestEvictedPrefixStartsOnGitMetadata(t *testing.T) {
	t.Parallel()
	full := "index 1111111..2222222 100644\n--- a/meta.go\n+++ b/meta.go\n@@ -1,2 +1,2 @@\n-old\n+new\n@@ -10,2 +10,2 @@\n-older\n+newer\n"
	w, ok := unifiedDiffWindow(full, shellDigestHalfBytes, shellDigestHalfBytes, true, false, 0)
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

func TestShellInlineDigestRetainedPathLikeBodyIsNotAFileName(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString("--- old sql comment\n")
	b.WriteString("+++ new sql comment\n")
	for i := range 4 {
		fmt.Fprintf(&b, " context padding %02d %s\n", i, strings.Repeat("c", 24))
	}
	b.WriteString("@@ -10,4 +10,4 @@ func later()\n")
	for i := range 4 {
		fmt.Fprintf(&b, " context kept %02d %s\n", i, strings.Repeat("k", 24))
	}
	b.WriteString("@@ -20,4 +20,4 @@ func omitted()\n")
	for i := range 4 {
		fmt.Fprintf(&b, " context omitted %02d %s\n", i, strings.Repeat("o", 24))
	}
	full := b.String()
	w, ok := unifiedDiffWindow(full, shellDigestHalfBytes, shellDigestHalfBytes, true, false, 0)
	if !ok {
		t.Fatal("unifiedDiffWindow did not recognize the retained diff")
	}
	if w.File != "" {
		t.Fatalf("cut file = %q, want unknown; path-like body lines are not a file header", w.File)
	}
	const dropped = 500
	digest := shellInlineDigest(full, int64(len(full)+dropped), dropped)
	if strings.Contains(digest, "old sql comment") || strings.Contains(digest, "new sql comment") {
		t.Fatalf("digest reports a path-like body line as a filename:\n%s", digest)
	}
}

func TestShellInlineDigestRetainedPartialLineIsNotMetadata(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	// A retained fragment of a removed "+index ..." hunk body line. Its leading
	// '+' was evicted with the prefix.
	b.WriteString("index 1111111..2222222 100644\n")
	b.WriteString("--- old sql comment\n")
	b.WriteString("+++ new sql comment\n")
	for i := range 4 {
		fmt.Fprintf(&b, " context padding %02d %s\n", i, strings.Repeat("c", 24))
	}
	b.WriteString("@@ -10,4 +10,4 @@ func later()\n")
	for i := range 4 {
		fmt.Fprintf(&b, " context kept %02d %s\n", i, strings.Repeat("k", 24))
	}
	b.WriteString("@@ -20,4 +20,4 @@ func omitted()\n")
	for i := range 4 {
		fmt.Fprintf(&b, " context omitted %02d %s\n", i, strings.Repeat("o", 24))
	}
	full := b.String()
	w, ok := unifiedDiffWindow(full, shellDigestHalfBytes, shellDigestHalfBytes, true, true, 0)
	if !ok {
		t.Fatal("unifiedDiffWindow did not recognize the retained diff")
	}
	if w.File != "" {
		t.Fatalf("cut file = %q, want unknown; the retained fragment is not metadata", w.File)
	}
	if want := offsetOf(t, full, "@@ -10,4"); w.HeadStart != want {
		t.Fatalf("head start = %d, want the first complete boundary %d", w.HeadStart, want)
	}
	const dropped = 500
	digest := shellInlineDigestFrom(full, int64(len(full)+dropped), dropped, true, 0)
	if strings.Contains(digest, "old sql comment") || strings.Contains(digest, "new sql comment") {
		t.Fatalf("digest reports a retained body fragment as a filename:\n%s", digest)
	}
}

func TestShellInlineDigestRetainedPartialDiffHeaderStillNamesFile(t *testing.T) {
	t.Parallel()
	// The retention cut removed only the leading "diff " of the file header.
	full := "git a/legit.go b/legit.go\nindex 1111111..2222222 100644\n--- a/legit.go\n+++ b/legit.go\n@@ -1,2 +1,2 @@\n-old\n+new\n@@ -10,2 +10,2 @@\n-older\n+newer\n"
	w, ok := unifiedDiffWindow(full, shellDigestHalfBytes, shellDigestHalfBytes, true, true, 0)
	if !ok {
		t.Fatal("unifiedDiffWindow did not recognize the retained header after a fragment")
	}
	if w.File != "b/legit.go" {
		t.Fatalf("cut file = %q, want b/legit.go from the retained file header", w.File)
	}
}

func TestShellInlineDigestLeadingHunklessSectionKeepsFileUnknown(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	b.WriteString("diff --git a/bin.dat b/bin.dat\nindex 1111111..2222222 100644\nGIT binary patch\nliteral 100000\n")
	for i := range 30 {
		fmt.Fprintf(&b, "binary line %02d %s\n", i, strings.Repeat("B", 40))
	}
	b.WriteString("diff --git a/text.go b/text.go\nindex 3333333..4444444 100644\n--- a/text.go\n+++ b/text.go\n@@ -1,4 +1,4 @@ func text()\n")
	for i := range 4 {
		fmt.Fprintf(&b, " context text %02d %s\n", i, strings.Repeat("t", 24))
	}
	full := b.String()
	textAt := offsetOf(t, full, "diff --git a/text.go")
	w, ok := unifiedDiffWindow(full, shellDigestHalfBytes, shellDigestHalfBytes, false, false, 0)
	if !ok {
		t.Fatal("unifiedDiffWindow did not recognize the diff")
	}
	if w.HeadEnd <= 0 || w.HeadEnd >= textAt {
		t.Fatalf("head end = %d, want a cut inside the leading hunkless section [0,%d)", w.HeadEnd, textAt)
	}
	if w.File != "" {
		t.Fatalf("cut file = %q, want unknown for a hunkless section", w.File)
	}
	if w.Hunk != "" {
		t.Fatalf("cut hunk = %q, want none in the hunkless section", w.Hunk)
	}
	digest := shellInlineDigest(full, int64(len(full)), 0)
	marker := markerBetween(digest, full[:w.HeadEnd], full[w.TailStart:])
	if strings.Contains(marker, "b/bin.dat") || strings.Contains(marker, "b/text.go") {
		t.Fatalf("marker guesses a filename for a hunkless section:\n%s", marker)
	}
}

func TestShellInlineDigestHunklessSectionKeepsFileUnknown(t *testing.T) {
	t.Parallel()
	small := "diff --git a/small.go b/small.go\nindex 1..2 100644\n--- a/small.go\n+++ b/small.go\n@@ -1,2 +1,2 @@\n-old\n+new\n"
	later := "diff --git a/later.go b/later.go\nindex 1..2 100644\n--- a/later.go\n+++ b/later.go\n@@ -1,2 +1,2 @@\n-old\n+new\n"
	cases := []struct {
		name   string
		header string
	}{
		{"destination contains b/", `diff --git a/foo "b/dir b/file.go"`},
		{"quoted spaces", `diff --git "a/old name.go" "b/new name.go"`},
		{"unquoted space", `diff --git a/foo b/unquoted space name`},
		{"quoted control and octal", `diff --git "a/old\nname" "b/new\377name"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			full := small + tc.header + "\nold mode 100644\nnew mode 100755\n" +
				strings.Repeat("literal 0000000 mode padding padding padding padding\n", 30) + later
			sectionAt := offsetOf(t, full, tc.header)
			w, ok := unifiedDiffWindow(full, shellDigestHalfBytes, shellDigestHalfBytes, false, false, 0)
			if !ok {
				t.Fatal("unifiedDiffWindow did not recognize the diff")
			}
			if w.HeadEnd != sectionAt {
				t.Fatalf("head end = %d, want the hunkless section start %d", w.HeadEnd, sectionAt)
			}
			if w.File != "" || w.Hunk != "" {
				t.Fatalf("cut = {%q %q}, want no guessed file or hunk", w.File, w.Hunk)
			}
			digest := shellInlineDigest(full, int64(len(full)), 0)
			marker := strings.TrimSpace(markerBetween(digest, full[:w.HeadEnd], full[w.TailStart:]))
			if !utf8.ValidString(marker) {
				t.Fatalf("marker is not valid UTF-8: %q", marker)
			}
			if strings.Contains(marker, "\n") {
				t.Fatalf("marker is not single-line: %q", marker)
			}
			if strings.Contains(marker, "b/dir") || strings.Contains(marker, "b/new") ||
				strings.Contains(marker, "unquoted") {
				t.Fatalf("marker guesses a hunkless filename: %q", marker)
			}
		})
	}
}

func TestShellInlineDiffDigestEscapesControlAndInvalidPathText(t *testing.T) {
	t.Parallel()
	full := "head\ntail\n"
	w := diffWindow{HeadStart: 0, HeadEnd: 5, TailStart: 5, File: "b/new\nname\xff", Hunk: "@@ -1 +1 @@\x00"}
	digest := renderDiffDigest(full, w, int64(len(full)), 0)
	marker := strings.TrimSpace(markerBetween(digest, "head\n", "tail\n"))
	if !utf8.ValidString(marker) {
		t.Fatalf("marker is not valid UTF-8: %q", marker)
	}
	if strings.Contains(marker, "\n") {
		t.Fatalf("marker is not single-line: %q", marker)
	}
	if !strings.Contains(marker, `b/new\nname\377`) {
		t.Fatalf("marker does not escape the path's newline and invalid byte: %q", marker)
	}
	if !strings.Contains(marker, `\000`) {
		t.Fatalf("marker does not escape the hunk's NUL byte: %q", marker)
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
	if _, ok := unifiedDiffWindow(full, shellDigestHalfBytes, shellDigestHalfBytes, true, false, 0); ok {
		t.Fatal("a hunk header more than one head budget from the retained start reclassified log output")
	}
}

func TestShellInlineDigestMultiFileCutNamesNextFile(t *testing.T) {
	t.Parallel()
	full := syntheticUnifiedDiff("a.go", 2, 8) + syntheticUnifiedDiff("b.go", 1, 8)
	bAt := offsetOf(t, full, "diff --git a/b.go b/b.go")
	w, ok := unifiedDiffWindow(full, bAt, 100, false, false, 0)
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
	if _, ok := unifiedDiffWindow(full, shellDigestHalfBytes, shellDigestHalfBytes, false, false, 0); ok {
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

func checkDiffDigestGolden(t *testing.T, name, digest string) {
	t.Helper()
	agenttest.CheckGolden(t, filepath.Join("testdata", "diffdigest", name+".md"), []byte(digest+"\n"), *updatePromptGoldens,
		"Regenerate with `go test ./agent -run '^TestDiffDigestGoldens$' -count=1 -update-prompt` and read the diff.")
}

// TestDiffDigestGoldens pins the complete model-facing digest prose for the
// standard, hunkless, and retained-prefix variants. The golden is the whole
// rendered digest, not fragment assertions against marker wording.
func TestDiffDigestGoldens(t *testing.T) {
	t.Parallel()
	plain := syntheticUnifiedDiff("big.go", 30, 8)
	checkDiffDigestGolden(t, "multi-hunk", shellInlineDigest(plain, int64(len(plain)), 0))

	var hunkless strings.Builder
	hunkless.WriteString("diff --git a/small.go b/small.go\nindex 1..2 100644\n--- a/small.go\n+++ b/small.go\n@@ -1,2 +1,2 @@\n-old\n+new\n")
	hunkless.WriteString("diff --git a/mode.go b/mode.go\nold mode 100644\nnew mode 100755\n")
	hunkless.WriteString(strings.Repeat("literal 0000000 mode padding padding padding padding\n", 30))
	hunkless.WriteString("diff --git a/later.go b/later.go\nindex 1..2 100644\n--- a/later.go\n+++ b/later.go\n@@ -1,2 +1,2 @@\n-old\n+new\n")
	hunklessFull := hunkless.String()
	checkDiffDigestGolden(t, "hunkless", shellInlineDigest(hunklessFull, int64(len(hunklessFull)), 0))

	var retained strings.Builder
	retained.WriteString(" context from an evicted hunk\n")
	retained.WriteString("@@ -10,4 +10,4 @@ func kept()\n")
	for i := range 4 {
		fmt.Fprintf(&retained, " kept line %02d %s\n", i, strings.Repeat("k", 24))
	}
	retained.WriteString("@@ -20,4 +20,4 @@ func omitted()\n")
	for i := range 4 {
		fmt.Fprintf(&retained, " omitted line %02d %s\n", i, strings.Repeat("o", 24))
	}
	retainedFull := retained.String()
	const dropped = 500
	checkDiffDigestGolden(t, "retained-prefix", shellInlineDigestFrom(retainedFull, int64(len(retainedFull)+dropped), dropped, false, 0))
}

// TestShellToolDiffDigestStaysWithinRegistryLines drives the real shell tool and
// inspects the model-facing rendered output. A 512-line tail of blank log lines
// would push the digest past the registry's line limit and make its truncator
// cut the selected hunks again; the digest must drop the optional tail instead
// and advertise the whole remainder as omitted.
func TestShellToolDiffDigestStaysWithinRegistryLines(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	full := syntheticUnifiedDiff("big.go", 30, 8) + strings.Repeat("\n", 600)
	path := filepath.Join(t.TempDir(), "big.diff")
	if err := os.WriteFile(path, []byte(full), 0o600); err != nil {
		t.Fatalf("write diff fixture: %v", err)
	}
	args, err := json.Marshal(map[string]string{"command": "cat -- " + shellSingleQuote(path)})
	if err != nil {
		t.Fatalf("marshal shell args: %v", err)
	}
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "diff-lines",
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
	if out.Output == nil {
		t.Fatal("large diff result carries no inline output")
	}
	maxLines := shellToolResultMaxLines(s.reg)
	if maxLines != 512 {
		t.Fatalf("shell line limit = %d, want the unchanged 512", maxLines)
	}
	w, ok := unifiedDiffWindow(full, shellDigestHalfBytes, shellDigestHalfBytes, false, false, maxLines)
	if !ok {
		t.Fatal("unifiedDiffWindow did not recognize the diff")
	}
	if w.TailStart != len(full) {
		t.Fatalf("tail start = %d, want the tail dropped (%d) to fit %d lines", w.TailStart, len(full), maxLines)
	}
	if !strings.HasPrefix(*out.Output, full[:w.HeadEnd]) {
		t.Fatalf("digest does not begin with the boundary-aligned head:\n%s", *out.Output)
	}
	if got := strings.Count(*out.Output, "\n") + 1; got > maxLines {
		t.Fatalf("digest has %d lines, want at most %d", got, maxLines)
	}
	// The registry must not have re-truncated the digest. formatShellResult is
	// the tool's own render; a registry cut would differ from it.
	if res.Output != formatShellResult(out) {
		t.Fatalf("registry changed the rendered shell result:\nwant:\n%s\ngot:\n%s", formatShellResult(out), res.Output)
	}
}

package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// pinReadFile writes content to path and pins its mtime, so repeat tracking
// sees the same stamp across rewrites a test wants to keep invisible to it.
func pinReadFile(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// repeatReadFile runs one read_file call in s.
func repeatReadFile(t *testing.T, s *Session, id, path string, args map[string]any) tool.ExecResult {
	t.Helper()
	args["file_path"] = path
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	res := s.execTool(t.Context(), llm.ToolCallData{ID: id, Name: "read_file", Arguments: raw}, "")
	if res.IsError {
		t.Fatalf("read_file %s failed: %s", id, res.Output)
	}
	return res
}

// sessionRound is the session's round counter, the turn a repeat read reports.
func sessionRound(s *Session) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.totalRounds
}

// A repeat read is marked unchanged for any range the session already read,
// including a subset of an earlier read, while an uncovered range is not.
// Exact repeats of a rewritten file stay unchanged only while the bytes match.
func TestRepeatReadOfUnchangedFileIsMarkedUnchanged(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	mtime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	pinReadFile(t, path, "one\ntwo\nthree\n", mtime)
	s := newSession(t, withEnv(execenv.NewLocalExecutionEnvironment(dir)))

	firstTurn := sessionRound(s)
	first := repeatReadFile(t, s, "r1", path, map[string]any{"offset": 1})
	if first.UnchangedSinceTurn != nil {
		t.Fatal("first read must not be marked unchanged")
	}
	s.mu.Lock()
	s.totalRounds += 3
	s.mu.Unlock()

	subset := repeatReadFile(t, s, "r2", path, map[string]any{"offset": 2})
	if subset.UnchangedSinceTurn == nil || *subset.UnchangedSinceTurn != firstTurn {
		t.Fatalf("subset read: UnchangedSinceTurn=%v, want the earlier read's turn", subset.UnchangedSinceTurn)
	}
	for _, line := range []string{"two", "three"} {
		if !strings.Contains(subset.Output, line) {
			t.Fatalf("subset read must still return line %q: %q", line, subset.Output)
		}
	}

	exact := repeatReadFile(t, s, "r3", path, map[string]any{"offset": 1})
	if exact.UnchangedSinceTurn == nil || *exact.UnchangedSinceTurn != firstTurn {
		t.Fatal("an exact repeat of the first range is a repeat")
	}

	// Rewriting with a new size invalidates even though the mtime is pinned.
	pinReadFile(t, path, "one\ntwo\nthree\nfour\n", mtime)
	if changed := repeatReadFile(t, s, "r4", path, map[string]any{"offset": 1}); changed.UnchangedSinceTurn != nil {
		t.Fatal("a changed file must not be marked unchanged")
	}
	// Same size, same mtime, different bytes: the content hash catches it.
	pinReadFile(t, path, "one\ntwo\nthree\nFOUR\n", mtime)
	if rewritten := repeatReadFile(t, s, "r6", path, map[string]any{"offset": 1}); rewritten.UnchangedSinceTurn != nil {
		t.Fatal("a same-size, same-mtime rewrite must not be marked unchanged")
	}
	if again := repeatReadFile(t, s, "r5", path, map[string]any{"offset": 1}); again.UnchangedSinceTurn == nil {
		t.Fatal("re-reading after the rewrite is a repeat of the new read")
	}

	// An uncovered range is not a repeat, even in the same file state.
	otherDir := t.TempDir()
	otherPath := filepath.Join(otherDir, "notes.txt")
	pinReadFile(t, otherPath, "one\ntwo\nthree\n", mtime)
	other := newSession(t, withEnv(execenv.NewLocalExecutionEnvironment(otherDir)))
	if head := repeatReadFile(t, other, "h1", otherPath, map[string]any{"limit": 2}); head.UnchangedSinceTurn != nil {
		t.Fatal("first bounded read must not be marked unchanged")
	}
	tail := repeatReadFile(t, other, "h2", otherPath, map[string]any{"offset": 3})
	if tail.UnchangedSinceTurn != nil {
		t.Fatal("a range no earlier read covered is not a repeat")
	}
	if !strings.Contains(oneLine(tail.Output, 3), "three") {
		t.Fatalf("uncovered read must return line 3: %q", tail.Output)
	}
}

// oneLine is the row of output whose number is line, so a test can assert the
// content of a specific returned line.
func oneLine(output string, line int) string {
	prefix := strconv.Itoa(line) + "\t"
	for row := range strings.SplitSeq(output, "\n") {
		if after, ok := strings.CutPrefix(row, prefix); ok {
			return after
		}
	}
	return ""
}

// Four repeats of one covered range keep returning the file's whole content,
// always attributed to the first read's turn, and the note escalates once the
// same range has been repeated three or more times.
func TestRepeatReadEscalatesHintAfterThreeRepeatsAndNeverStubs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	pinReadFile(t, path, "alpha-one\nbeta-two\ngamma-three\n", time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC))
	s := newSession(t, withEnv(execenv.NewLocalExecutionEnvironment(dir)))

	firstTurn := sessionRound(s)
	first := repeatReadFile(t, s, "e1", path, map[string]any{"offset": 1, "limit": 3})
	if first.UnchangedSinceTurn != nil {
		t.Fatal("first read must not be marked unchanged")
	}
	base := first.Output
	var hints []string
	for i := 2; i <= 5; i++ {
		res := repeatReadFile(t, s, "e"+strconv.Itoa(i), path, map[string]any{"offset": 1, "limit": 3})
		if res.UnchangedSinceTurn == nil || *res.UnchangedSinceTurn != firstTurn {
			t.Fatalf("repeat %d: UnchangedSinceTurn=%v, want turn %d", i, res.UnchangedSinceTurn, firstTurn)
		}
		for _, line := range []string{"alpha-one", "beta-two", "gamma-three"} {
			if !strings.Contains(res.Output, line) {
				t.Fatalf("repeat %d dropped content %q: %q", i, line, res.Output)
			}
		}
		if !strings.HasPrefix(res.Output, base) {
			t.Fatalf("repeat %d does not keep the original content first: %q", i, res.Output)
		}
		note := res.Output[len(base):]
		if strings.LastIndex(note, "[Unchanged") < 0 {
			t.Fatalf("repeat %d has no repeat note", i)
		}
		hints = append(hints, note)
	}
	// Calls 4 and 5 are the third and fourth repeats: the note must name the
	// count instead of repeating the plain note from earlier repeats.
	for count := 3; count <= 4; count++ {
		note := hints[count-1]
		hint := note[strings.LastIndex(note, "[Unchanged"):]
		if !strings.Contains(hint, strconv.Itoa(count)) {
			t.Fatalf("repeat %d note %q does not name the repeat count %d", count, hint, count)
		}
	}
	if hints[2] == hints[1] {
		t.Fatal("the third repeat must emit a stronger note than the second")
	}
}

// A subset read is invalidated by either a size/mtime change or a same-size,
// same-mtime rewrite of the exact range.
func TestRepeatReadSubsetInvalidatedByDiskChangeAndExactSameStampRewrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	mtime := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	var b strings.Builder
	for i := 1; i <= 20; i++ {
		b.WriteString("line-")
		b.WriteString(strconv.Itoa(i))
		b.WriteString("\n")
	}
	pinReadFile(t, path, b.String(), mtime)
	s := newSession(t, withEnv(execenv.NewLocalExecutionEnvironment(dir)))

	if first := repeatReadFile(t, s, "s1", path, map[string]any{"offset": 1, "limit": 20}); first.UnchangedSinceTurn != nil {
		t.Fatal("first read must not be marked unchanged")
	}
	// A new size discards the tracked spans: a subset read is fresh.
	pinReadFile(t, path, b.String()+"line-21\n", mtime)
	if changed := repeatReadFile(t, s, "s2", path, map[string]any{"offset": 5, "limit": 6}); changed.UnchangedSinceTurn != nil {
		t.Fatal("a subset read after a disk change must not be marked unchanged")
	}

	// Same size and mtime, different bytes: the exact range is fresh.
	otherDir := t.TempDir()
	otherPath := filepath.Join(otherDir, "notes.txt")
	pinReadFile(t, otherPath, b.String(), mtime)
	other := newSession(t, withEnv(execenv.NewLocalExecutionEnvironment(otherDir)))
	if first := repeatReadFile(t, other, "s3", otherPath, map[string]any{"offset": 1, "limit": 20}); first.UnchangedSinceTurn != nil {
		t.Fatal("first read must not be marked unchanged")
	}
	rewritten := strings.Replace(b.String(), "line-7\n", "LINE-7\n", 1)
	if len(rewritten) != len(b.String()) {
		t.Fatalf("fixture rewrite changed size: %d != %d", len(rewritten), len(b.String()))
	}
	pinReadFile(t, otherPath, rewritten, mtime)
	if again := repeatReadFile(t, other, "s4", otherPath, map[string]any{"offset": 1, "limit": 20}); again.UnchangedSinceTurn != nil {
		t.Fatal("a same-size, same-mtime rewrite of the exact range must not be marked unchanged")
	}
}

// Compaction clears the repeat spans, because the earlier read left the
// context, but keeps the read-before-write record.
func TestRepeatReadClearedOnCompactionPreservesReadBeforeWrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	pinReadFile(t, path, "one\ntwo\nthree\n", time.Date(2026, 4, 5, 6, 7, 8, 0, time.UTC))
	s := newSession(t, withEnv(execenv.NewLocalExecutionEnvironment(dir)))

	if first := repeatReadFile(t, s, "c1", path, map[string]any{"offset": 1}); first.UnchangedSinceTurn != nil {
		t.Fatal("first read must not be marked unchanged")
	}
	if second := repeatReadFile(t, s, "c2", path, map[string]any{"offset": 1}); second.UnchangedSinceTurn == nil {
		t.Fatal("repeat before compaction must be marked unchanged")
	}

	s.mu.Lock()
	snapLen, snapRevision := len(s.history), s.historyRevision
	_, published := s.publishFoldedHistory(snapLen, snapRevision, slices.Clone(s.history))
	s.mu.Unlock()
	if !published {
		t.Fatal("publishFoldedHistory refused to publish")
	}

	if after := repeatReadFile(t, s, "c3", path, map[string]any{"offset": 1}); after.UnchangedSinceTurn != nil {
		t.Fatal("compaction must clear prior reads: the earlier content left the context")
	}
	if warning := s.readBeforeWriteWarning(path); warning != "" {
		t.Fatalf("compaction dropped the read-before-write record: %q", warning)
	}
	raw, err := json.Marshal(map[string]any{"file_path": path, "content": "one\ntwo\nthree\nfour\n"})
	if err != nil {
		t.Fatal(err)
	}
	if write := s.execTool(t.Context(), llm.ToolCallData{ID: "c4", Name: "write_file", Arguments: raw}, ""); write.IsError {
		t.Fatalf("write_file failed: %s", write.Output)
	}
}

// Past-EOF and unnumbered-tail reads carry no comparable range, so they are
// never repeat reads. Image repeats hash the image bytes, not the header text.
func TestRepeatReadSkipsPastEOFAndUnnumberedTailAndHashesImageBytes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	t.Run("past-eof", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(dir, "short.txt")
		pinReadFile(t, path, "one\ntwo\nthree\n", time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC))
		s := newSession(t, withEnv(execenv.NewLocalExecutionEnvironment(dir)))
		first := repeatReadFile(t, s, "p1", path, map[string]any{"offset": 100})
		if !first.ReadPastEOF {
			t.Fatalf("past-EOF scenario did not run: %+v", first)
		}
		if second := repeatReadFile(t, s, "p2", path, map[string]any{"offset": 100}); second.UnchangedSinceTurn != nil {
			t.Fatal("a past-EOF read must not be tracked as a repeat")
		}
	})

	t.Run("unnumbered-tail", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(dir, "large.log")
		body := strings.Repeat("tail line 0123456789\n", int(execenv.ReadTotalLinesScanCap/20)+1024)
		pinReadFile(t, path, body, time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC))
		s := newSession(t, withEnv(execenv.NewLocalExecutionEnvironment(dir)))
		first := repeatReadFile(t, s, "u1", path, map[string]any{"offset": -3})
		if !first.ReadUnnumberedTail {
			t.Fatalf("unnumbered-tail scenario did not run: %+v", first)
		}
		if second := repeatReadFile(t, s, "u2", path, map[string]any{"offset": -3}); second.UnchangedSinceTurn != nil {
			t.Fatal("an unnumbered tail read must not be tracked as a repeat")
		}
	})

	t.Run("image-bytes", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		path := filepath.Join(dir, "pixel.png")
		mtime := time.Date(2026, 6, 7, 8, 9, 10, 0, time.UTC)
		red, blue := testPNG(t, color.RGBA{R: 255, A: 255}), testPNG(t, color.RGBA{B: 255, A: 255})
		size := max(len(red), len(blue))
		red, blue = padBytes(red, size), padBytes(blue, size)
		pinReadFile(t, path, string(red), mtime)
		s := newSession(t, withEnv(execenv.NewLocalExecutionEnvironment(dir)))

		if first := repeatReadFile(t, s, "i1", path, map[string]any{}); first.UnchangedSinceTurn != nil || len(first.ImageData) == 0 {
			t.Fatalf("first image read: unchanged=%v image bytes=%d", first.UnchangedSinceTurn, len(first.ImageData))
		}
		if again := repeatReadFile(t, s, "i2", path, map[string]any{}); again.UnchangedSinceTurn == nil {
			t.Fatal("re-reading unchanged image bytes must be marked unchanged")
		}
		pinReadFile(t, path, string(blue), mtime)
		if rewritten := repeatReadFile(t, s, "i3", path, map[string]any{}); rewritten.UnchangedSinceTurn != nil {
			t.Fatal("a same-size, same-mtime image rewrite must not be marked unchanged")
		}
	})
}

// testPNG encodes a 2x2 image filled with c.
func testPNG(t *testing.T, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	for y := range 2 {
		for x := range 2 {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// padBytes appends zero bytes after a PNG's IEND to size, which decoders skip.
func padBytes(b []byte, size int) []byte {
	if len(b) >= size {
		return b
	}
	return append(slices.Clone(b), make([]byte, size-len(b))...)
}

// A large memory page keeps its long-page note now that memory_read returns a
// tool.TextResult, and the generated index keeps exactly one continuation
// footer.
func TestMemoryReadLargePagePreservesSizeNoteWithTextResult(t *testing.T) {
	t.Parallel()
	s, scope := memoryWritesSession(t)
	long := "# Long\n" + strings.Repeat("opaque-long-line-93\n", 300)
	seedMemoryPage(t, scope, "long.md", long)
	res := memoryExec(t, s, "memory_read", map[string]any{"scope": "personal", "file_path": "long.md"})
	if res.IsError {
		t.Fatal(res.Output)
	}
	if !strings.Contains(res.Output, "# Long") {
		t.Fatalf("page content missing: %q", res.Output)
	}
	if want := memoryPageSizeNote(len(long), s.memoryGardeningSkillAvailable()); !strings.HasSuffix(res.Output, want) {
		t.Fatalf("large page read lost its size note: %q", res.Output[max(0, len(res.Output)-160):])
	}

	for i := range 4 {
		seedMemoryPage(t, scope, "p"+strconv.Itoa(i)+".md", "---\ndescription: opaque page\ntags: [t]\nupdated: 2026-10-01\n---\n")
	}
	index := memoryExec(t, s, "memory_read", map[string]any{"scope": "personal", "file_path": "MEMORY.md", "offset": 1, "limit": 2})
	if index.IsError {
		t.Fatal(index.Output)
	}
	if body, footer, ok := strings.Cut(index.Output, "\n\n"); !ok || body == "" || footer == "" {
		t.Fatalf("sliced index missing its single footer: %q", index.Output)
	}
	if strings.Count(index.Output, "\n\n") != 1 {
		t.Fatalf("sliced index emitted more than one footer: %q", index.Output)
	}
}

// An engine-paged read_file result already stops cleanly at 48 KB with an
// offset= continuation footer; the session must not also treat it as a
// registry-truncated output and append a redundant artifact/read_transcript
// pointer holding only the identical page.
func TestPagedReadFileDoesNotAttachRedundantArtifact(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "large.txt")
	const totalLines = 2000
	body := strings.Repeat(strings.Repeat("a", 60)+"\n", totalLines)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newSession(t, withEnv(execenv.NewLocalExecutionEnvironment(dir)))
	res := repeatReadFile(t, s, "r-big", path, map[string]any{})
	if res.Truncated {
		t.Fatalf("engine-paged read set ExecResult.Truncated=true; want false (registry did not cut it)")
	}
	if !strings.Contains(res.Output, "offset=") {
		t.Fatalf("engine-paged read missing offset= continuation footer: tail=%q", res.Output[max(0, len(res.Output)-200):])
	}
	if strings.Contains(res.Output, "artifact:") || strings.Contains(res.Output, "read_transcript(") {
		t.Fatalf("engine-paged read attached a redundant artifact/read_transcript hint: tail=%q", res.Output[max(0, len(res.Output)-240):])
	}
}

// memory_read on MEMORY.md must derive its continuation footer from the lines
// NumberLines actually emitted, both when the 48 KB byte cap cuts a large
// generated index and when a negative offset selects a tail slice.
func TestMemoryReadIndexFooterMatchesByteCappedAndNegativeOffsetOutput(t *testing.T) {
	t.Parallel()
	s, scope := memoryWritesSession(t)
	for i := range 360 {
		name := fmt.Sprintf("p%03d.md", i)
		desc := fmt.Sprintf("when handling memory index entry %03d across a long descriptive trigger clause %s", i, strings.Repeat("x", 90))
		seedMemoryPage(t, scope, name, "---\ndescription: "+desc+"\ntags: [t]\nupdated: 2026-10-01\n---\n")
	}

	full := memoryExec(t, s, "memory_read", map[string]any{"scope": "personal", "file_path": "MEMORY.md"})
	if full.IsError {
		t.Fatal(full.Output)
	}
	rowsBody, footer, hasFooter := strings.Cut(full.Output, "\n\n")
	if !hasFooter || footer == "" {
		t.Fatalf("byte-capped MEMORY.md read emitted no continuation footer (len=%d)", len(full.Output))
	}
	lines := strings.Split(strings.TrimSuffix(rowsBody, "\n"), "\n")
	lastRowNum, _, ok := strings.Cut(lines[len(lines)-1], "\t")
	if !ok {
		lastRowNum, _, ok = strings.Cut(lines[len(lines)-1], ":")
	}
	lastShown, err := strconv.Atoi(strings.TrimSpace(lastRowNum))
	if !ok || err != nil || lastShown <= 0 || lastShown >= 360 {
		t.Fatalf("expected byte-capped index to stop before line 360; last line=%q", lines[len(lines)-1])
	}
	wantNext := fmt.Sprintf("offset=%d", lastShown+1)
	if !strings.Contains(footer, wantNext) {
		t.Fatalf("byte-capped MEMORY.md footer = %q, want %q", footer, wantNext)
	}

	tail := memoryExec(t, s, "memory_read", map[string]any{"scope": "personal", "file_path": "MEMORY.md", "offset": -50, "limit": 10})
	if tail.IsError {
		t.Fatal(tail.Output)
	}
	tailBody, tailFooter, hasTailFooter := strings.Cut(tail.Output, "\n\n")
	if !hasTailFooter || tailFooter == "" {
		t.Fatalf("negative-offset MEMORY.md slice missing footer: %q", tail.Output)
	}
	tailLines := strings.Split(strings.TrimSuffix(tailBody, "\n"), "\n")
	firstTok, _, _ := strings.Cut(tailLines[0], "\t")
	lastTok, _, _ := strings.Cut(tailLines[len(tailLines)-1], "\t")
	firstTail, _ := strconv.Atoi(strings.TrimSpace(firstTok))
	lastTail, _ := strconv.Atoi(strings.TrimSpace(lastTok))
	if firstTail <= 1 || lastTail != firstTail+9 {
		t.Fatalf("negative-offset slice rows = %d..%d, want 10 rows near tail", firstTail, lastTail)
	}
	wantRange := fmt.Sprintf("%d-%d", firstTail, lastTail)
	wantTailNext := fmt.Sprintf("offset=%d", lastTail+1)
	if !strings.Contains(tailFooter, wantRange) || !strings.Contains(tailFooter, wantTailNext) {
		t.Fatalf("negative-offset MEMORY.md footer = %q, want range %q and %q", tailFooter, wantRange, wantTailNext)
	}
}

// Following a clamped line's own offset=N limit=1 recovery hint must not be
// flagged as an unchanged repeat of the earlier clamped read.
func TestRepeatReadDoesNotFlagLimitOneUncutEscapeAfterClampedRead(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "minified.js")
	body := "header\n" + strings.Repeat("z", 5000) + "\nfooter\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s := newSession(t, withEnv(execenv.NewLocalExecutionEnvironment(dir)))

	first := repeatReadFile(t, s, "r-clamped", path, map[string]any{})
	if first.UnchangedSinceTurn != nil {
		t.Fatal("first read must not be marked unchanged")
	}
	escape := repeatReadFile(t, s, "r-escape", path, map[string]any{"offset": 2, "limit": 1})
	if escape.UnchangedSinceTurn != nil {
		t.Fatalf("offset=2 limit=1 uncut recovery after clamped read must not be flagged unchanged (UnchangedSinceTurn=%d, output tail=%q)",
			*escape.UnchangedSinceTurn, escape.Output[max(0, len(escape.Output)-160):])
	}
	if !strings.Contains(escape.Output, strings.Repeat("z", 5000)) {
		t.Fatal("offset=2 limit=1 did not return the full uncut line")
	}
}

// Writing, editing, or patching a file invalidates any pre-write repeat spans
// for that path, even when the mutation preserves the file's size and mtime
// and the follow-up read only inspects a subset range.
func TestReadFileRepeatInvalidatedBySessionWrites(t *testing.T) {
	t.Parallel()
	mtime := time.Date(2026, 7, 8, 9, 10, 11, 0, time.UTC)
	var initialBuilder strings.Builder
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&initialBuilder, "line-%02d\n", i)
	}
	initial := initialBuilder.String()
	updated := strings.Replace(initial, "line-07\n", "LINE-07\n", 1)
	if len(updated) != len(initial) {
		t.Fatalf("fixture rewrite changed size: %d != %d", len(updated), len(initial))
	}

	cases := []struct {
		name   string
		mutate func(t *testing.T, s *Session, path string)
	}{
		{
			name: "write_file",
			mutate: func(t *testing.T, s *Session, path string) {
				raw, err := json.Marshal(map[string]any{"file_path": path, "content": updated})
				if err != nil {
					t.Fatal(err)
				}
				if res := s.execTool(t.Context(), llm.ToolCallData{ID: "w1", Name: "write_file", Arguments: raw}, ""); res.IsError {
					t.Fatalf("write_file failed: %s", res.Output)
				}
			},
		},
		{
			name: "edit_file",
			mutate: func(t *testing.T, s *Session, path string) {
				raw, err := json.Marshal(map[string]any{
					"file_path": path,
					"edits":     []any{map[string]any{"old_string": "line-07", "new_string": "LINE-07"}},
				})
				if err != nil {
					t.Fatal(err)
				}
				if res := s.execTool(t.Context(), llm.ToolCallData{ID: "e1", Name: "edit_file", Arguments: raw}, ""); res.IsError {
					t.Fatalf("edit_file failed: %s", res.Output)
				}
			},
		},
		{
			name: "apply_patch",
			mutate: func(t *testing.T, s *Session, path string) {
				patch := "*** Begin Patch\n*** Update File: notes.txt\n@@\n line-06\n-line-07\n+LINE-07\n line-08\n*** End Patch\n"
				raw, err := json.Marshal(map[string]any{"patch": patch})
				if err != nil {
					t.Fatal(err)
				}
				if res := s.execTool(t.Context(), llm.ToolCallData{ID: "p1", Name: "apply_patch", Arguments: raw}, ""); res.IsError {
					t.Fatalf("apply_patch failed: %s", res.Output)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, "notes.txt")
			pinReadFile(t, path, initial, mtime)
			s := newSession(t, withEnv(execenv.NewLocalExecutionEnvironment(dir)))

			if first := repeatReadFile(t, s, "r1", path, map[string]any{"offset": 1, "limit": 20}); first.UnchangedSinceTurn != nil {
				t.Fatal("first read must not be marked unchanged")
			}
			tc.mutate(t, s, path)
			// Pin mtime back to the pre-write timestamp to simulate a same-tick
			// rewrite of identical byte size, where only write-side invalidation
			// prevents a subset read from matching the stale pre-write span.
			if err := os.Chtimes(path, mtime, mtime); err != nil {
				t.Fatal(err)
			}
			after := repeatReadFile(t, s, "r2", path, map[string]any{"offset": 5, "limit": 6})
			if after.UnchangedSinceTurn != nil {
				t.Fatalf("%s did not invalidate priorReads: subset read falsely marked unchanged since turn %d", tc.name, *after.UnchangedSinceTurn)
			}
			if !strings.Contains(after.Output, "LINE-07") {
				t.Fatalf("subset read after %s missing updated line: %q", tc.name, after.Output)
			}
		})
	}
}

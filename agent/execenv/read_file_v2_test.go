package execenv

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/sandbox"
)

func TestReadFileHeadOnlyPagingNeverDropsMiddle(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	t.Cleanup(env.Cleanup)

	const total = 1500
	var sb strings.Builder
	for i := 1; i <= total; i++ {
		fmt.Fprintf(&sb, "line-%04d-%s\n", i, strings.Repeat("x", 49))
	}
	path := filepath.Join(dir, "big.txt")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	var reader RichFileReader = env
	out, meta, err := reader.ReadFileRichCtx(t.Context(), "big.txt", nil, nil)
	if err != nil {
		t.Fatalf("ReadFileRichCtx: %v", err)
	}
	if len(out) > DefaultReadMaxBytes+512 {
		t.Fatalf("first page = %d bytes, want <= %d", len(out), DefaultReadMaxBytes+512)
	}
	if !meta.Truncated || meta.TruncatedBy != "chars" {
		t.Fatalf("meta = %+v, want Truncated=true TruncatedBy=chars", meta)
	}
	if meta.StartLine != 1 || meta.EndLine <= 0 || meta.EndLine >= total || meta.TotalLines != total {
		t.Fatalf("meta range = %+v, want StartLine=1, 0<EndLine<%d, TotalLines=%d", meta, total, total)
	}
	if !strings.Contains(out, "1\tline-0001-") {
		t.Fatalf("first page missing line 1: %.200q", out)
	}
	if !strings.Contains(out, fmt.Sprintf("%d\tline-%04d-", meta.EndLine, meta.EndLine)) {
		t.Fatalf("first page missing EndLine %d", meta.EndLine)
	}
	if strings.Contains(out, fmt.Sprintf("line-%04d-", meta.EndLine+1)) {
		t.Fatalf("first page leaked line %d past EndLine", meta.EndLine+1)
	}

	// Page seamlessly to EOF via offset = meta.EndLine + 1.
	covered := make([]bool, total+1)
	markRange := func(m ReadFileMeta, body string) {
		for n := m.StartLine; n <= m.EndLine; n++ {
			if !strings.Contains(body, fmt.Sprintf("%d\tline-%04d-", n, n)) {
				t.Fatalf("page [%d..%d] missing line %d", m.StartLine, m.EndLine, n)
			}
			if covered[n] {
				t.Fatalf("line %d covered twice", n)
			}
			covered[n] = true
		}
	}
	markRange(meta, out)

	next := meta.EndLine + 1
	for next <= total {
		pageOut, pageMeta, err := reader.ReadFileRichCtx(t.Context(), "big.txt", &next, nil)
		if err != nil {
			t.Fatalf("page offset=%d: %v", next, err)
		}
		wholeOut, _, wholeMeta, err := env.ReadFileRichAndBytesCtx(t.Context(), "big.txt", &next, nil)
		if err != nil {
			t.Fatalf("whole page offset=%d: %v", next, err)
		}
		if pageOut != wholeOut || pageMeta != wholeMeta {
			t.Fatalf("offset=%d diverged between ReadFileRichCtx and ReadFileRichAndBytesCtx", next)
		}
		if pageMeta.StartLine != next {
			t.Fatalf("page StartLine = %d, want %d", pageMeta.StartLine, next)
		}
		markRange(pageMeta, pageOut)
		if pageMeta.EndLine == total {
			if pageMeta.Truncated {
				t.Fatalf("final page reported Truncated=true: %+v", pageMeta)
			}
			break
		}
		if !pageMeta.Truncated {
			t.Fatalf("intermediate page [%d..%d] reported Truncated=false", pageMeta.StartLine, pageMeta.EndLine)
		}
		next = pageMeta.EndLine + 1
	}
	for i := 1; i <= total; i++ {
		if !covered[i] {
			t.Fatalf("line %d was dropped during paging", i)
		}
	}
}

func TestReadFileLineClampingAndSingleLineEscape(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	t.Cleanup(env.Cleanup)

	t.Run("clamps 5000-rune UTF-8 line and keeps line 2", func(t *testing.T) {
		t.Parallel()
		long := strings.Repeat("é", 5000)
		p := filepath.Join(dir, "utf8-long.txt")
		if err := os.WriteFile(p, []byte(long+"\nsecond line\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		out, meta, err := env.ReadFileRichCtx(t.Context(), p, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !utf8.ValidString(out) {
			t.Fatal("clamped output is not valid UTF-8")
		}
		if meta.ClampedLines != 1 || meta.StartLine != 1 || meta.EndLine != 2 || meta.TotalLines != 2 {
			t.Fatalf("meta = %+v", meta)
		}
		if !strings.Contains(out, "2\tsecond line") {
			t.Fatalf("line 2 missing from clamped output: %.300q", out)
		}
		firstLine, _, _ := strings.Cut(out, "\n")
		if strings.Contains(firstLine, strings.Repeat("é", DefaultReadMaxLineChars+1)) {
			t.Fatalf("line 1 exceeded %d runes before clamp", DefaultReadMaxLineChars)
		}
		if !strings.Contains(firstLine, strings.Repeat("é", DefaultReadMaxLineChars)) {
			t.Fatalf("line 1 did not preserve full %d runes", DefaultReadMaxLineChars)
		}
	})

	t.Run("preserves short non-UTF-8 raw bytes verbatim", func(t *testing.T) {
		t.Parallel()
		raw := "caf\xe9 \xff\xfe\n"
		p := filepath.Join(dir, "latin1.txt")
		if err := os.WriteFile(p, []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
		out, meta, err := env.ReadFileRichCtx(t.Context(), p, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if meta.ClampedLines != 0 {
			t.Fatalf("short non-UTF-8 line clamped: %+v", meta)
		}
		if !strings.Contains(out, "caf\xe9 \xff\xfe") {
			t.Fatalf("non-UTF-8 bytes corrupted: %q", out)
		}
	})

	t.Run("offset=1 limit=1 returns 5000-rune line uncut", func(t *testing.T) {
		t.Parallel()
		long := strings.Repeat("é", 5000)
		p := filepath.Join(dir, "escape-5k.txt")
		if err := os.WriteFile(p, []byte(long+"\nline2\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		off, lim := 1, 1
		out, meta, err := env.ReadFileRichCtx(t.Context(), p, &off, &lim)
		if err != nil {
			t.Fatal(err)
		}
		if meta.ClampedLines != 0 || meta.Truncated {
			t.Fatalf("limit=1 escape meta = %+v", meta)
		}
		if !strings.Contains(out, long) {
			t.Fatalf("limit=1 escape did not return full 5000 runes")
		}
	})

	t.Run("offset=1 limit=1 on 60000-byte UTF-8 line slices cleanly", func(t *testing.T) {
		t.Parallel()
		huge := strings.Repeat("é", 30000) // 60,000 bytes
		p := filepath.Join(dir, "escape-60k.txt")
		if err := os.WriteFile(p, []byte(huge+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		off, lim := 1, 1
		out, meta, err := env.ReadFileRichCtx(t.Context(), p, &off, &lim)
		if err != nil {
			t.Fatal(err)
		}
		if !utf8.ValidString(out) {
			t.Fatal("byte-sliced UTF-8 output is invalid UTF-8")
		}
		if !meta.Truncated || meta.TruncatedBy != "chars" {
			t.Fatalf("meta = %+v, want Truncated=true TruncatedBy=chars", meta)
		}
		firstLine, _, _ := strings.Cut(out, "\n")
		if len(firstLine)+1 > DefaultReadMaxBytes {
			t.Fatalf("sliced row len = %d, want <= %d", len(firstLine)+1, DefaultReadMaxBytes)
		}
		_, footer, ok := strings.Cut(out, "\n")
		if !ok || !strings.Contains(footer, "1") || !strings.Contains(footer, "60000") {
			t.Fatalf("footer should include line number 1 and byte length 60000: %.300q", out)
		}
		wholeOut, _, wholeMeta, err := env.ReadFileRichAndBytesCtx(t.Context(), p, &off, &lim)
		if err != nil || wholeOut != out || wholeMeta != meta {
			t.Fatalf("ReadFileRichCtx diverged from ReadFileRichAndBytesCtx: err=%v meta=%+v", err, wholeMeta)
		}
	})
}

// Not parallel: mutates package-level ReadTotalLinesScanCap.
func TestReadFileEmptyAndPastEOFNotices(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	t.Cleanup(env.Cleanup)

	emptyPath := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(emptyPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	out, meta, err := env.ReadFileRichCtx(t.Context(), emptyPath, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out == "" {
		t.Fatal("empty file returned silent empty string")
	}
	if meta.StartLine != 0 || meta.EndLine != 0 || meta.TotalLines != 0 || meta.PastEOF {
		t.Fatalf("empty file meta = %+v", meta)
	}
	wholeOut, _, wholeMeta, err := env.ReadFileRichAndBytesCtx(t.Context(), emptyPath, nil, nil)
	if err != nil || wholeOut != out || wholeMeta != meta {
		t.Fatalf("empty file equivalence failed: err=%v out=%q meta=%+v", err, wholeOut, wholeMeta)
	}

	threePath := filepath.Join(dir, "three.txt")
	if err := os.WriteFile(threePath, []byte("a\nb\nc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	off := 50
	out, meta, err = env.ReadFileRichCtx(t.Context(), threePath, &off, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out == "" {
		t.Fatal("past EOF returned silent empty string")
	}
	if !meta.PastEOF || meta.TotalLines != 3 || meta.StartLine != 0 || meta.EndLine != 0 {
		t.Fatalf("past EOF meta = %+v", meta)
	}
	if !strings.Contains(out, "3") || !strings.Contains(out, "50") {
		t.Fatalf("past EOF notice missing line counts: %q", out)
	}
	wholeOut, _, wholeMeta, err = env.ReadFileRichAndBytesCtx(t.Context(), threePath, &off, nil)
	if err != nil || wholeOut != out || wholeMeta != meta {
		t.Fatalf("past EOF equivalence failed: err=%v out=%q meta=%+v", err, wholeOut, wholeMeta)
	}

	origCap := ReadTotalLinesScanCap
	ReadTotalLinesScanCap = 1024
	t.Cleanup(func() { ReadTotalLinesScanCap = origCap })

	largePath := filepath.Join(dir, "over-cap.txt")
	largeBody := strings.Repeat("thirty chars line content!!\n", 50) // 1,400 bytes > 1,024
	if err := os.WriteFile(largePath, []byte(largeBody), 0o644); err != nil {
		t.Fatal(err)
	}
	off = 999
	out, meta, err = env.ReadFileRichCtx(t.Context(), largePath, &off, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.PastEOF || meta.TotalLines != 50 || meta.StartLine != 0 {
		t.Fatalf("over-cap past EOF meta = %+v, want PastEOF=true TotalLines=50", meta)
	}
	wholeOut, _, wholeMeta, err = env.ReadFileRichAndBytesCtx(t.Context(), largePath, &off, nil)
	if err != nil || wholeOut != out || wholeMeta != meta {
		t.Fatalf("over-cap past EOF equivalence failed: err=%v", err)
	}
}

func TestReadFileCompactTabAwarePrefixes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	t.Cleanup(env.Cleanup)

	spacePath := filepath.Join(dir, "spaces.go")
	if err := os.WriteFile(spacePath, []byte("func main() {\n    return\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := env.ReadFileCtx(t.Context(), spacePath, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantSpace := "1\tfunc main() {\n2\t    return\n3\t}\n"
	if out != wantSpace {
		t.Fatalf("space-indented output = %q, want %q", out, wantSpace)
	}

	tabPath := filepath.Join(dir, "tabs.go")
	if err := os.WriteFile(tabPath, []byte("func main() {\n\treturn\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = env.ReadFileCtx(t.Context(), tabPath, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantTab := "1:func main() {\n2:\treturn\n3:}\n"
	if out != wantTab {
		t.Fatalf("tab-indented output = %q, want %q", out, wantTab)
	}
}

// Not parallel: mutates package-level ReadTotalLinesScanCap.
func TestReadFileNegativeOffsetTailAndLargeFileEquivalence(t *testing.T) {
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	t.Cleanup(env.Cleanup)

	// 10-line file with offset=-3
	tenPath := filepath.Join(dir, "ten.txt")
	var sb strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&sb, "line-%d\n", i)
	}
	if err := os.WriteFile(tenPath, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	off := -3
	out, meta, err := env.ReadFileRichCtx(t.Context(), tenPath, &off, nil)
	if err != nil {
		t.Fatal(err)
	}
	if meta.StartLine != 8 || meta.EndLine != 10 || meta.TotalLines != 10 || !meta.TailMode || meta.Truncated {
		t.Fatalf("offset=-3 meta = %+v", meta)
	}
	for _, want := range []string{"8\tline-8\n", "9\tline-9\n", "10\tline-10\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
	if strings.Contains(out, "7\tline-7") {
		t.Fatalf("unexpected line 7 in %q", out)
	}
	wholeOut, _, wholeMeta, err := env.ReadFileRichAndBytesCtx(t.Context(), tenPath, &off, nil)
	if err != nil || wholeOut != out || wholeMeta != meta {
		t.Fatalf("offset=-3 equivalence failed: err=%v", err)
	}

	// 150-line file with offset=-100, limit=5 -> lines 51-55 forward.
	sb.Reset()
	for i := 1; i <= 150; i++ {
		fmt.Fprintf(&sb, "row-%03d\n", i)
	}
	f150 := filepath.Join(dir, "f150.txt")
	if err := os.WriteFile(f150, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	off, lim := -100, 5
	out, meta, err = env.ReadFileRichCtx(t.Context(), f150, &off, &lim)
	if err != nil {
		t.Fatal(err)
	}
	if meta.StartLine != 51 || meta.EndLine != 55 || meta.TotalLines != 150 || !meta.TailMode {
		t.Fatalf("offset=-100 limit=5 meta = %+v", meta)
	}
	if !strings.Contains(out, "51\trow-051\n") || !strings.Contains(out, "55\trow-055\n") || strings.Contains(out, "56\trow-056") {
		t.Fatalf("unexpected rows for offset=-100 limit=5: %q", out)
	}
	wholeOut, _, wholeMeta, err = env.ReadFileRichAndBytesCtx(t.Context(), f150, &off, &lim)
	if err != nil || wholeOut != out || wholeMeta != meta {
		t.Fatalf("offset=-100 limit=5 equivalence failed: err=%v", err)
	}

	// offset=-500 (no limit) where 500 lines exceed DefaultReadMaxBytes -> tail-most lines ending at 600.
	sb.Reset()
	pad := strings.Repeat("z", 190)
	for i := 1; i <= 600; i++ {
		fmt.Fprintf(&sb, "n-%04d-%s\n", i, pad)
	}
	f600 := filepath.Join(dir, "f600.txt")
	if err := os.WriteFile(f600, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	off = -500
	out, meta, err = env.ReadFileRichCtx(t.Context(), f600, &off, nil)
	if err != nil {
		t.Fatal(err)
	}
	if meta.EndLine != 600 || meta.StartLine <= 101 || meta.TotalLines != 600 || !meta.Truncated || meta.TruncatedBy != "chars" || !meta.TailMode {
		t.Fatalf("tail byte-cap meta = %+v", meta)
	}
	if len(out) > DefaultReadMaxBytes+512 {
		t.Fatalf("tail output = %d bytes, want <= %d", len(out), DefaultReadMaxBytes+512)
	}
	if !strings.Contains(out, "600\tn-0600-") || !strings.Contains(out, fmt.Sprintf("%d\tn-%04d-", meta.StartLine, meta.StartLine)) {
		t.Fatalf("tail missing expected end or start lines: %.200q", out)
	}
	if strings.Contains(out, fmt.Sprintf("n-%04d-", meta.StartLine-1)) {
		t.Fatalf("tail leaked earlier line %d", meta.StartLine-1)
	}
	wholeOut, _, wholeMeta, err = env.ReadFileRichAndBytesCtx(t.Context(), f600, &off, nil)
	if err != nil || wholeOut != out || wholeMeta != meta {
		t.Fatalf("tail byte-cap equivalence failed: err=%v", err)
	}

	// Large-file (>ReadTotalLinesScanCap) tail read behavior.
	origCap := ReadTotalLinesScanCap
	ReadTotalLinesScanCap = 4096
	t.Cleanup(func() { ReadTotalLinesScanCap = origCap })

	sb.Reset()
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&sb, "tail-row-%04d-%s\n", i, strings.Repeat("y", 20))
	}
	bigTail := filepath.Join(dir, "big-tail.txt")
	if err := os.WriteFile(bigTail, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	// offset=-5 does not reach byte 0 -> unnumbered lines.
	off = -5
	out, meta, err = env.ReadFileRichCtx(t.Context(), bigTail, &off, nil)
	if err != nil {
		t.Fatal(err)
	}
	if meta.StartLine != 0 || meta.EndLine != 0 || meta.TotalLines != 0 || !meta.TailMode {
		t.Fatalf("unnumbered large tail meta = %+v", meta)
	}
	if strings.Contains(out, "1\t") || strings.Contains(out, "1:") || strings.Contains(out, "296\t") {
		t.Fatalf("unnumbered tail emitted line numbers: %q", out)
	}
	for i := 296; i <= 300; i++ {
		if !strings.Contains(out, fmt.Sprintf("tail-row-%04d-", i)) {
			t.Fatalf("unnumbered tail missing line %d: %q", i, out)
		}
	}
	if strings.Contains(out, "tail-row-0295-") {
		t.Fatalf("unnumbered tail included line 295: %q", out)
	}
	wholeOut, _, wholeMeta, err = env.ReadFileRichAndBytesCtx(t.Context(), bigTail, &off, nil)
	if err != nil || wholeOut != out || wholeMeta != meta {
		t.Fatalf("unnumbered large tail equivalence failed: err=%v out=%q wholeOut=%q", err, out, wholeOut)
	}

	// Sandbox parity for negative offset on >ReadTotalLinesScanCap.
	sbEnv, _, worktree := sandboxedEnv(t, sandbox.ModeRestricted)
	sbBigTail := filepath.Join(worktree, "sb-big-tail.txt")
	if err := os.WriteFile(sbBigTail, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	sbOut, sbMeta, sbErr := sbEnv.ReadFileRichCtx(t.Context(), sbBigTail, &off, nil)
	offPlain := NewLocalExecutionEnvironment(worktree)
	t.Cleanup(offPlain.Cleanup)
	plainOut, plainMeta, plainErr := offPlain.ReadFileRichCtx(t.Context(), sbBigTail, &off, nil)
	if sbErr != nil || plainErr != nil || sbOut != plainOut || sbMeta != plainMeta {
		t.Fatalf("sandbox parity failed for negative offset: sbErr=%v plainErr=%v sbMeta=%+v plainMeta=%+v", sbErr, plainErr, sbMeta, plainMeta)
	}

	// offset=-9999 reaches byte 0 -> real 1-based line numbers.
	off = -9999
	out, meta, err = env.ReadFileRichCtx(t.Context(), bigTail, &off, nil)
	if err != nil {
		t.Fatal(err)
	}
	if meta.StartLine != 1 || meta.EndLine != 300 || meta.TotalLines != 300 || !meta.TailMode {
		t.Fatalf("full-reach large tail meta = %+v", meta)
	}
	if !strings.Contains(out, "1\ttail-row-0001-") || !strings.Contains(out, "300\ttail-row-0300-") {
		t.Fatalf("full-reach large tail missing numbered lines: %.200q", out)
	}
	wholeOut, _, wholeMeta, err = env.ReadFileRichAndBytesCtx(t.Context(), bigTail, &off, nil)
	if err != nil || wholeOut != out || wholeMeta != meta {
		t.Fatalf("full-reach large tail equivalence failed: err=%v", err)
	}

	// NUL byte in the last 64 KiB of a >ReadTotalLinesScanCap file still fails with errReadBinaryFile.
	nulTail := filepath.Join(dir, "nul-tail.bin")
	nulContent := sb.String() + "bad\x00tail\n"
	if err := os.WriteFile(nulTail, []byte(nulContent), 0o644); err != nil {
		t.Fatal(err)
	}
	off = -5
	_, _, err = env.ReadFileRichCtx(t.Context(), nulTail, &off, nil)
	if !errors.Is(err, errReadBinaryFile) {
		t.Fatalf("NUL in tail err = %v, want errReadBinaryFile", err)
	}
	_, _, _, err = env.ReadFileRichAndBytesCtx(t.Context(), nulTail, &off, nil)
	if !errors.Is(err, errReadBinaryFile) {
		t.Fatalf("whole-file NUL in tail err = %v, want errReadBinaryFile", err)
	}
}

// Not parallel: mutates package-level ReadTotalLinesScanCap.
func TestReadFileLargeFileNonEOFOmitsTotalLinesScan(t *testing.T) {
	origCap := ReadTotalLinesScanCap
	origWhole := readWindowWholeBelow
	ReadTotalLinesScanCap = 4096
	readWindowWholeBelow = 1024
	t.Cleanup(func() { ReadTotalLinesScanCap = origCap })
	t.Cleanup(func() { readWindowWholeBelow = origWhole })

	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	t.Cleanup(env.Cleanup)

	var sb strings.Builder
	for i := 1; i <= 500; i++ {
		fmt.Fprintf(&sb, "line-%04d-%s\n", i, strings.Repeat("a", 30)) // > 20 KB
	}
	path := filepath.Join(dir, "large.txt")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	counter := &bytesCountedFS{Fs: afero.NewOsFs()}
	env.SetFs(counter)

	off, lim := 1, 5
	out, meta, err := env.ReadFileRichCtx(t.Context(), "large.txt", &off, &lim)
	if err != nil {
		t.Fatal(err)
	}
	if meta.TotalLines != 0 || meta.StartLine != 1 || meta.EndLine != 5 || meta.Truncated {
		t.Fatalf("meta = %+v, want TotalLines=0 StartLine=1 EndLine=5 Truncated=false", meta)
	}
	if counter.Bytes() > readWindowChunk+readWindowHeadBytes {
		t.Fatalf("large non-EOF window read %d bytes, expected single-chunk window scan (<= %d)", counter.Bytes(), readWindowChunk+readWindowHeadBytes)
	}

	plain := NewLocalExecutionEnvironment(dir)
	t.Cleanup(plain.Cleanup)
	wholeOut, _, wholeMeta, err := plain.ReadFileRichAndBytesCtx(t.Context(), "large.txt", &off, &lim)
	if err != nil {
		t.Fatal(err)
	}
	if out != wholeOut || meta != wholeMeta {
		t.Fatalf("equivalence failed: out=%q wholeOut=%q meta=%+v wholeMeta=%+v", out, wholeOut, meta, wholeMeta)
	}
}

func TestNumberLinesNegativeOffsetKeepsTailMostLines(t *testing.T) {
	t.Parallel()
	var sb strings.Builder
	pad := strings.Repeat("z", 190)
	for i := 1; i <= 600; i++ {
		fmt.Fprintf(&sb, "n-%04d-%s\n", i, pad)
	}
	text := sb.String()
	off := -500

	gotStr := NumberLines(text, &off, nil)
	gotBytes := NumberLinesBytes([]byte(text), &off, nil)
	if gotStr != gotBytes {
		t.Fatalf("NumberLines vs NumberLinesBytes diverged for negative offset")
	}
	if len(gotStr) > DefaultReadMaxBytes {
		t.Fatalf("NumberLines len = %d, want <= %d", len(gotStr), DefaultReadMaxBytes)
	}
	if !strings.Contains(gotStr, "600\tn-0600-") {
		t.Fatalf("NumberLines negative offset dropped final line 600; head starts with %.120q", gotStr)
	}
	if strings.Contains(gotStr, "101\tn-0101-") {
		t.Fatalf("NumberLines negative offset kept oldest line 101 instead of tail-most lines")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "tail.txt")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	env := NewLocalExecutionEnvironment(dir)
	t.Cleanup(env.Cleanup)
	richOut, _, err := env.ReadFileRichCtx(t.Context(), path, &off, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(richOut, gotStr) {
		t.Fatalf("ReadFileRichCtx tail rows do not match NumberLines rows")
	}
}

// Not parallel: mutates package-level ReadTotalLinesScanCap.
func TestReadFileLongPathStaysUnderRegistryCap(t *testing.T) {
	// The fixture path is ~2,500 bytes, past macOS PATH_MAX (1024), so it lives
	// in an in-memory filesystem where that length is legal on every platform.
	baseDir := "/work"
	memfs := afero.NewMemMapFs()
	// Build a ~2,500-byte valid path under baseDir (each component < 255 bytes).
	seg := strings.Repeat("d", 100)
	var relParts []string
	for len(filepath.Join(relParts...)) < 2450 {
		relParts = append(relParts, seg)
	}
	relPath := filepath.Join(append(relParts, "target.txt")...)
	if len(relPath) < 2400 {
		t.Fatalf("fixture relPath len = %d, want >= 2400", len(relPath))
	}
	fullPath := filepath.Join(baseDir, relPath)
	if err := memfs.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}

	env := NewLocalExecutionEnvironment(baseDir).SetFs(memfs)
	t.Cleanup(env.Cleanup)

	// Case 1: single-line overflow (60,000 bytes) with limit=1.
	if err := afero.WriteFile(memfs, fullPath, []byte(strings.Repeat("x", 60000)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	off, lim := 1, 1
	out, meta, err := env.ReadFileRichCtx(t.Context(), relPath, &off, &lim)
	if err != nil {
		t.Fatal(err)
	}
	if rc := utf8.RuneCountInString(out); rc > 50_000 {
		t.Fatalf("single-line overflow with %d-byte path produced %d runes, want <= 50000", len(relPath), rc)
	}
	if !meta.Truncated || meta.TruncatedBy != "chars" {
		t.Fatalf("meta = %+v, want Truncated=true TruncatedBy=chars", meta)
	}
	if !strings.Contains(out, relPath) || !strings.Contains(out, "60000") {
		t.Fatalf("single-line overflow footer missing path or byte count")
	}
	wholeOut, _, wholeMeta, err := env.ReadFileRichAndBytesCtx(t.Context(), relPath, &off, &lim)
	if err != nil || wholeOut != out || wholeMeta != meta {
		t.Fatalf("single-line overflow equivalence failed: err=%v len=%d/%d", err, len(out), len(wholeOut))
	}

	// Case 2: unnumbered large-file tail (>ReadTotalLinesScanCap) exceeding 48 KB,
	// with the fixture itself past readWindowWholeBelow so this read takes
	// scanTailWindowSeekerRich rather than the whole-file renderTextReadRich path.
	origCap := ReadTotalLinesScanCap
	ReadTotalLinesScanCap = 4096
	t.Cleanup(func() { ReadTotalLinesScanCap = origCap })

	var sb strings.Builder
	pad := strings.Repeat("y", 190)
	for i := 1; i <= 720; i++ {
		fmt.Fprintf(&sb, "n-%04d-%s\n", i, pad)
	}
	if int64(sb.Len()) <= readWindowWholeBelow || sb.Len() < 140_000 {
		t.Fatalf("tail fixture is %d bytes, want > readWindowWholeBelow (%d) and >= 140000", sb.Len(), readWindowWholeBelow)
	}
	if err := afero.WriteFile(memfs, fullPath, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	off = -500
	out, meta, err = env.ReadFileRichCtx(t.Context(), relPath, &off, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rc := utf8.RuneCountInString(out); rc > 50_000 {
		t.Fatalf("unnumbered tail with %d-byte path produced %d runes, want <= 50000", len(relPath), rc)
	}
	if !meta.TailMode || meta.StartLine != 0 || !meta.Truncated || meta.TruncatedBy != "chars" {
		t.Fatalf("tail meta = %+v", meta)
	}
	if !strings.Contains(out, relPath) || !strings.Contains(out, "n-0720-") {
		t.Fatalf("unnumbered tail missing path header or final line")
	}
	wholeOut, _, wholeMeta, err = env.ReadFileRichAndBytesCtx(t.Context(), relPath, &off, nil)
	if err != nil || wholeOut != out || wholeMeta != meta {
		t.Fatalf("unnumbered tail equivalence failed: err=%v len=%d/%d", err, len(out), len(wholeOut))
	}
}

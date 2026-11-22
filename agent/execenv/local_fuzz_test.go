package execenv

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/spf13/afero"
	"primeradiant.com/evener/fuzz/oracle"
)

// fuzzRoot is the logical working directory both fuzz filesystem lanes are
// rooted at. It is a path that does not exist on any real machine, so
// ensureUnderRoot's EvalSymlinks probe takes its not-found fallback identically
// on the MemMapFs lane and on the BasePathFs-over-OsFs lane — the two lanes see
// byte-identical logical paths, which is what makes the differential sound.
const fuzzRoot = "/fuzzroot"

// fuzzTarget is the single file every filesystem fuzzer seeds and edits/reads.
const fuzzTarget = "target.txt"

// newFuzzEnv builds a LocalExecutionEnvironment rooted at fuzzRoot over the
// given afero.Fs, with fuzzRoot pre-created so WriteFile/EditFile's parent-dir
// handling has somewhere to land. It seeds fuzzTarget with content. The
// environment never spawns a subprocess in these tests: only ReadFile /
// WriteFile / EditFile are exercised, all of which route through the injected
// fs.
func newFuzzEnv(t *testing.T, fs afero.Fs, content []byte) (*LocalExecutionEnvironment, string) {
	t.Helper()
	if err := fs.MkdirAll(fuzzRoot, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	abs := filepath.Join(fuzzRoot, fuzzTarget)
	if err := afero.WriteFile(fs, abs, content, 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	env := NewLocalExecutionEnvironment(fuzzRoot).SetFs(fs)
	return env, abs
}

// FuzzEditFile drives EditFile — exact replace, whitespace-normalized fuzzy
// fallback, and the nearest-region diagnostic on a miss — over an in-memory
// filesystem, checking three oracles after every call:
//
//   - never panic (the harness catches any panic as a failure);
//   - ATOMICITY: when EditFile reports failure, the file is byte-for-byte
//     unchanged (a failed edit must never partially write);
//   - CORE-REPLACE TEETH: when the edit succeeds AND the original contained
//     oldString verbatim (so the exact, non-fuzzy branch ran) AND newString does
//     not itself contain oldString (so a replacement cannot reintroduce it), the
//     resulting bytes equal the independently computed LF strings.Replace /
//     ReplaceAll result on LF inputs, or preserve BOM + CRLF line endings on
//     CRLF/BOM inputs. The guard skips the fuzzy branch (whose match we do not
//     try to predict), lone-CR collisions, and the newString⊇oldString trap that would make
//     "oldString gone" a false expectation.
//
// SAFETY: MemMapFs only; no real disk, no subprocess.
func FuzzEditFile(f *testing.F) {
	f.Add("hello world\n", "world", "there", false)
	f.Add("a\na\na\n", "a", "b", true)
	f.Add("a\na\na\n", "a", "b", false)
	f.Add("  indented  line  \n", "indented line", "X", false)
	f.Add("one\ntwo\nthree\n", "two\nthree", "TWO\nTHREE", false)
	f.Add("keep exactly this line here", "keep this line", "y", false)
	f.Add("x", "x", "xx", true)
	f.Add("", "", "", false)
	f.Add("abc", "", "z", true)
	f.Add("\xEF\xBB\xBFhello\r\nworld\r\n", "hello", "HI\nTHERE", false)
	f.Add("a\r\nb\nc\r\n", "a\r\n", "A1\r\nA2\r\n", false)

	f.Fuzz(func(t *testing.T, content, oldString, newString string, replaceAll bool) {
		fs := afero.NewMemMapFs()
		env, abs := newFuzzEnv(t, fs, []byte(content))

		before := readOrEmpty(t, fs, abs)
		msg, err := env.EditFile(fuzzTarget, oldString, newString, replaceAll)
		after := readOrEmpty(t, fs, abs)

		if err != nil {
			if !bytes.Equal(before, after) {
				t.Fatalf("EditFile failed but mutated the file (atomicity broken)\n old=%q\n new=%q\n before=%q\n after=%q\n err=%v",
					oldString, newString, before, after, err)
			}
			return
		}

		if !strings.HasPrefix(msg, "edited ") {
			t.Fatalf("EditFile success message not well-formed: %q", msg)
		}

		// Core-replace teeth: only when the exact (non-fuzzy) branch ran and a
		// replacement cannot reintroduce oldString.
		bom := []byte(nil)
		body := before
		if bytes.HasPrefix(before, []byte{0xEF, 0xBB, 0xBF}) {
			bom = before[:3]
			body = before[3:]
		}
		lfBody := strings.ReplaceAll(string(body), "\r\n", "\n")
		oldLF := strings.ReplaceAll(oldString, "\r\n", "\n")
		newLF := strings.ReplaceAll(newString, "\r\n", "\n")
		if oldLF != "" && strings.Contains(lfBody, oldLF) && !strings.Contains(newLF, oldLF) {
			pureLF := len(bom) == 0 && !bytes.Contains(body, []byte("\r\n")) && !strings.Contains(oldString, "\r\n") && !strings.Contains(newString, "\r\n")
			if pureLF {
				var expected string
				if replaceAll {
					expected = strings.ReplaceAll(string(before), oldString, newString)
				} else {
					// Non-replaceAll success implies the exact-match count was 1.
					expected = strings.Replace(string(before), oldString, newString, 1)
				}
				if string(after) != expected {
					t.Fatalf("EditFile exact-replace mismatch\n old=%q new=%q replaceAll=%v\n before=%q\n after=%q\n want=%q",
						oldString, newString, replaceAll, before, after, expected)
				}
				return
			}
			if !bytes.HasPrefix(after, bom) {
				t.Fatalf("EditFile dropped leading BOM\n before=%q\n after=%q", before, after)
			}
			if !strings.Contains(newLF, "\r") {
				var expectedLF string
				if replaceAll {
					expectedLF = strings.ReplaceAll(lfBody, oldLF, newLF)
				} else {
					expectedLF = strings.Replace(lfBody, oldLF, newLF, 1)
				}
				if gotLF := strings.ReplaceAll(string(after[len(bom):]), "\r\n", "\n"); gotLF != expectedLF {
					t.Fatalf("EditFile CRLF/BOM LF-normalized mismatch\n old=%q new=%q replaceAll=%v\n before=%q\n after=%q\n wantLF=%q",
						oldString, newString, replaceAll, before, after, expectedLF)
				}
				pureCRLF := bytes.Contains(body, []byte("\r\n")) && !bytes.Contains(bytes.ReplaceAll(body, []byte("\r\n"), nil), []byte("\n"))
				if pureCRLF {
					wantCRLF := string(bom) + strings.ReplaceAll(expectedLF, "\n", "\r\n")
					if string(after) != wantCRLF {
						t.Fatalf("EditFile CRLF-preserving mismatch\n old=%q new=%q replaceAll=%v\n before=%q\n after=%q\n want=%q",
							oldString, newString, replaceAll, before, after, wantCRLF)
					}
				}
			}
		}
	})
}

// FuzzEditFileDifferential is the seam guard: the identical EditFile operation
// run through two environments whose ONLY difference is the injected afero.Fs —
// one an OS filesystem sandboxed under a t.TempDir (BasePathFs over OsFs), the
// other a pure in-memory MemMapFs — must agree on (error-ness, returned
// message, resulting file bytes). Production defaults to OsFs, so any divergence
// means the memory lane and the disk lane compute EditFile differently, which
// would make every MemMapFs-based fuzzer above unsound.
//
// SAFETY: the OS lane writes only beneath a t.TempDir (BasePathFs pins every
// path under it); the mem lane never touches disk. No subprocess.
func FuzzEditFileDifferential(f *testing.F) {
	f.Add("hello world\n", "world", "there", false)
	f.Add("a\na\na\n", "a", "b", true)
	f.Add("  indented  line  \n", "indented line", "X", false)
	f.Add("one\ntwo\nthree\n", "two\nthree", "TWO\nTHREE", false)
	f.Add("nomatch here", "totally absent string", "y", false)
	f.Add("", "", "", true)
	f.Add("\xEF\xBB\xBFone\r\ntwo\r\n", "two", "TWO\nLINE", false)

	f.Fuzz(func(t *testing.T, content, oldString, newString string, replaceAll bool) {
		osFs := afero.NewBasePathFs(afero.NewOsFs(), t.TempDir())
		memFs := afero.NewMemMapFs()

		osEnv, osAbs := newFuzzEnv(t, osFs, []byte(content))
		memEnv, memAbs := newFuzzEnv(t, memFs, []byte(content))

		osMsg, osErr := osEnv.EditFile(fuzzTarget, oldString, newString, replaceAll)
		memMsg, memErr := memEnv.EditFile(fuzzTarget, oldString, newString, replaceAll)

		if (osErr == nil) != (memErr == nil) {
			t.Fatalf("error parity broken: os=%v mem=%v", osErr, memErr)
		}
		if osMsg != memMsg {
			t.Fatalf("message diverged: os=%q mem=%q", osMsg, memMsg)
		}
		osBytes := readOrEmpty(t, osFs, osAbs)
		memBytes := readOrEmpty(t, memFs, memAbs)
		if !bytes.Equal(osBytes, memBytes) {
			t.Fatalf("resulting bytes diverged across filesystems\n os =%q\n mem=%q", osBytes, memBytes)
		}
		if osErr == nil {
			if !strings.HasPrefix(osMsg, "edited ") {
				t.Fatalf("successful EditFile returned unexpected message %q", osMsg)
			}
			if bytes.Equal(osBytes, []byte(content)) {
				t.Fatalf("successful EditFile left bytes unchanged: %q", osBytes)
			}
		}
	})
}

// FuzzReadFileWindow drives ReadFile's line windowing (offset/limit clamping,
// CRLF normalization, base64 image/document short-circuit, NUL binary
// rejection) over an in-memory filesystem. Oracles:
//
//   - never panic;
//   - DETERMINISM: two reads of the same file with the same window are equal;
//   - a NUL-byte file errors, a NUL-free file does not;
//   - the base64 short-circuit round-trips: the encoded payload decodes back to
//     the exact file bytes;
//   - the text window is EXACTLY the reference window: line numbers start at the
//     clamped offset, are contiguous, and each row's text is the corresponding
//     CRLF-normalized file line — reconstructed independently and compared byte
//     for byte.
//
// SAFETY: MemMapFs only; no real disk, no subprocess.
func FuzzReadFileWindow(f *testing.F) {
	f.Add("one\ntwo\nthree\n", 1, 2000)
	f.Add("a\r\nb\r\nc", 2, 1)
	f.Add("only line", 0, 0)
	f.Add("l1\nl2\nl3\nl4\nl5", 3, 2)
	f.Add("past end", 99, 5)
	f.Add("has\x00nul", 1, 10)
	f.Add(string([]byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 1, 2}), 1, 10)
	f.Add("%PDF-1.7 stuff", 1, 10)
	f.Add("", 1, 2000)

	f.Fuzz(func(t *testing.T, content string, offset, limit int) {
		// Bound input size so a multi-megabyte all-newlines file (millions of
		// numbered rows, formatted three times per exec) can't turn a correct
		// but O(lines) read into a multi-second exec. The logic under test is
		// fully exercised by modest inputs; this only keeps the search fast.
		if len(content) > 1<<16 {
			return
		}
		fs := afero.NewMemMapFs()
		env, _ := newFuzzEnv(t, fs, []byte(content))

		var op, lp *int
		if offset != 0 {
			op = &offset
		}
		if limit != 0 {
			lp = &limit
		}

		read := func(struct{}) readResult {
			out, err := env.ReadFile(fuzzTarget, op, lp)
			return readResult{out: out, errStr: errString(err)}
		}
		oracle.Deterministic(t, read, struct{}{}, func(a, b readResult) bool { return a == b })

		out, err := env.ReadFile(fuzzTarget, op, lp)

		hasNUL := bytes.IndexByte([]byte(content), 0) >= 0
		if err != nil {
			if !hasNUL {
				t.Fatalf("ReadFile errored on a NUL-free file: %v (content=%q)", err, content)
			}
			return
		}
		if hasNUL {
			// A NUL-free short-circuit (image/doc) is the only way a NUL file
			// avoids the binary error; verify that's what happened.
			if !strings.HasPrefix(out, "[image:") && !strings.HasPrefix(out, "[document:") {
				t.Fatalf("ReadFile accepted a NUL file as text: content=%q out=%q", content, out)
			}
		}

		// base64 short-circuit: payload must decode back to the file bytes.
		if strings.HasPrefix(out, "[image:") || strings.HasPrefix(out, "[document:") {
			_, payload, ok := strings.Cut(out, "\n")
			if !ok {
				t.Fatalf("base64 short-circuit missing newline header: %q", out)
			}
			decoded, decErr := base64.StdEncoding.DecodeString(payload)
			if decErr != nil {
				t.Fatalf("base64 payload failed to decode: %v", decErr)
			}
			if !bytes.Equal(decoded, []byte(content)) {
				t.Fatalf("base64 payload != file bytes\n want=%q\n got =%q", content, decoded)
			}
			return
		}

		// Text window: ReadFileRichCtx and ReadFileRichAndBytesCtx must match,
		// and rows/metadata must match independent reference reconstruction.
		richOut, meta, rerr := env.ReadFileRichCtx(context.Background(), fuzzTarget, op, lp)
		if rerr != nil || richOut != out {
			t.Fatalf("ReadFileRichCtx diverged: rich=%q err=%v out=%q", richOut, rerr, out)
		}
		wholeOut, _, wholeMeta, werr := env.ReadFileRichAndBytesCtx(context.Background(), fuzzTarget, op, lp)
		if werr != nil || wholeOut != richOut || wholeMeta != meta {
			t.Fatalf("ReadFileRichAndBytesCtx diverged: wholeErr=%v meta=%+v wholeMeta=%+v", werr, meta, wholeMeta)
		}
		verifyFuzzedReferenceWindow(t, content, op, lp, out, meta)
	})
}

// verifyFuzzedReferenceWindow re-derives ReadFile v2's text-window rows and
// metadata from first principles and compares them against the engine's output.
func verifyFuzzedReferenceWindow(t *testing.T, content string, offsetLine, limitLines *int, got string, meta ReadFileMeta) {
	t.Helper()
	if content == "" {
		if got == "" || meta.StartLine != 0 || meta.EndLine != 0 || meta.TotalLines != 0 || meta.PastEOF {
			t.Fatalf("empty file mismatch: got=%q meta=%+v", got, meta)
		}
		return
	}

	var s string
	if strings.HasSuffix(content, "\r\n") {
		s = content[:len(content)-2]
	} else {
		s = strings.TrimSuffix(content, "\n")
	}
	lines := strings.Split(s, "\n")
	for i := range lines {
		if i < len(lines)-1 {
			lines[i] = strings.TrimSuffix(lines[i], "\r")
		}
	}
	total := len(lines)

	tailMode := offsetLine != nil && *offsetLine < 0
	singleLineEscape := limitLines != nil && *limitLines == 1
	explicitLimit := limitLines != nil && *limitLines > 0
	effLimit := DefaultReadLines
	if explicitLimit {
		effLimit = *limitLines
	}

	var start int
	if tailMode {
		var tailN int64
		if *offsetLine == math.MinInt {
			tailN = math.MaxInt64
		} else {
			tailN = -int64(*offsetLine)
		}
		if !explicitLimit && tailN > int64(DefaultReadLines) {
			tailN = int64(DefaultReadLines)
		}
		if int64(total) <= tailN {
			start = 1
		} else {
			start = total - int(tailN) + 1
		}
	} else {
		start = 1
		if offsetLine != nil && *offsetLine > 0 {
			start = *offsetLine
		}
		if start > total {
			if got == "" || !meta.PastEOF || meta.TotalLines != total || meta.StartLine != 0 {
				t.Fatalf("past EOF mismatch (start=%d total=%d): got=%q meta=%+v", start, total, got, meta)
			}
			return
		}
	}

	end := min(start-1+effLimit, total)
	type refRow struct {
		num     int
		content string
	}
	var cand []refRow
	for i := start; i <= end; i++ {
		line := lines[i-1]
		if !singleLineEscape && len(line) > DefaultReadMaxLineChars {
			pos, cut, runes := 0, -1, 0
			for pos < len(line) {
				_, sz := utf8.DecodeRuneInString(line[pos:])
				pos += sz
				runes++
				if runes == DefaultReadMaxLineChars {
					cut = pos
				}
			}
			if runes > DefaultReadMaxLineChars {
				line = fmt.Sprintf("%s... [line truncated: %d chars total; read with offset=%d limit=1 for full line]", line[:cut], runes, i)
			}
		}
		cand = append(cand, refRow{num: i, content: line})
	}
	var kept []refRow
	if tailMode && !explicitLimit {
		sum := 0
		idx := len(cand)
		for i, row := range slices.Backward(cand) {
			rl := len(fmt.Sprintf("%d\t%s\n", row.num, row.content))
			if i < len(cand)-1 && sum+rl > DefaultReadMaxBytes {
				break
			}
			sum += rl
			idx = i
			if i == len(cand)-1 && sum > DefaultReadMaxBytes {
				break
			}
		}
		kept = cand[idx:]
	} else {
		sum := 0
		for _, r := range cand {
			rl := len(fmt.Sprintf("%d\t%s\n", r.num, r.content))
			if len(kept) > 0 && sum+rl > DefaultReadMaxBytes {
				break
			}
			kept = append(kept, r)
			sum += rl
		}
	}
	if len(kept) == 1 && len(fmt.Sprintf("%d\t%s\n", kept[0].num, kept[0].content)) > DefaultReadMaxBytes {
		if !meta.Truncated || meta.TruncatedBy != "chars" || meta.StartLine != kept[0].num || meta.EndLine != kept[0].num {
			t.Fatalf("single-line overflow meta = %+v", meta)
		}
		return
	}
	sep := "\t"
	for _, r := range kept {
		if strings.HasPrefix(r.content, "\t") {
			sep = ":"
			break
		}
	}
	var want strings.Builder
	for _, r := range kept {
		fmt.Fprintf(&want, "%d%s%s\n", r.num, sep, r.content)
	}
	if !strings.HasPrefix(got, want.String()) {
		t.Fatalf("ReadFile window mismatch\n content=%q offset=%v limit=%v\n got =%q\n wantPrefix=%q",
			content, offsetLine, limitLines, got, want.String())
	}
	if meta.StartLine != kept[0].num || meta.EndLine != kept[len(kept)-1].num || meta.TotalLines != total || meta.TailMode != tailMode {
		t.Fatalf("ReadFile meta mismatch (total=%d tail=%v): got=%+v wantStart=%d wantEnd=%d",
			total, tailMode, meta, kept[0].num, kept[len(kept)-1].num)
	}
}

// FuzzDetectImageFormat exercises the pure byte-sniffing classifier over a
// fuzzed path and payload. Oracles:
//
//   - never panic;
//   - the result is always one of the known format tokens (or "");
//   - DETERMINISM: same (path, data) → same token;
//   - EXTENSION TEETH: when the path carries a recognized image extension, the
//     token is exactly that extension's mapping (the extension branch dominates
//     the magic-byte branch).
//
// SAFETY: pure function; no filesystem, no subprocess.
func FuzzDetectImageFormat(f *testing.F) {
	f.Add("a.png", []byte("whatever"))
	f.Add("b.JPG", []byte{0xFF, 0xD8, 0xFF})
	f.Add("noext", []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A})
	f.Add("x.gif", []byte("GIF89a"))
	f.Add("magic", []byte("GIF87a1234"))
	f.Add("", []byte{})
	f.Add("weird.SvG", []byte("<svg>"))

	known := map[string]bool{
		"": true, "png": true, "jpeg": true, "gif": true,
		"webp": true, "bmp": true, "tiff": true,
	}
	extMap := map[string]string{
		".png": "png", ".jpg": "jpeg", ".jpeg": "jpeg",
		".gif": "gif", ".webp": "webp", ".bmp": "bmp",
		".tif": "tiff", ".tiff": "tiff",
	}

	f.Fuzz(func(t *testing.T, path string, data []byte) {
		got := detectImageFormat(path, data)
		if !known[got] {
			t.Fatalf("detectImageFormat returned unknown token %q for path=%q", got, path)
		}
		oracle.Deterministic(t, func(struct{}) string { return detectImageFormat(path, data) },
			struct{}{}, func(a, b string) bool { return a == b })

		ext := strings.ToLower(filepath.Ext(path))
		if want, ok := extMap[ext]; ok && got != want {
			t.Fatalf("extension %q should force token %q, got %q", ext, want, got)
		}
	})
}

// readResult bundles ReadFile's output and error string for the determinism
// oracle's equality comparison.
type readResult struct {
	out    string
	errStr string
}

// readOrEmpty reads a file through fs, returning empty bytes when it is absent
// (an EditFile failure that never wrote leaves the seeded file in place, so this
// only returns empty if a lane genuinely has no file).
func readOrEmpty(t *testing.T, fs afero.Fs, abs string) []byte {
	t.Helper()
	data, err := afero.ReadFile(fs, abs)
	if err != nil {
		return nil
	}
	return data
}

// errString renders an error as a stable string for equality comparison.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

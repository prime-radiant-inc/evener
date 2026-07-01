package execenv

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/serf/fuzz/oracle"
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
//     resulting bytes equal the independently computed strings.Replace /
//     ReplaceAll result. The guard skips the fuzzy branch (whose match we do not
//     try to predict) and the newString⊇oldString trap that would make
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
		if oldString != "" && strings.Contains(string(before), oldString) && !strings.Contains(newString, oldString) {
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
			nl := strings.IndexByte(out, '\n')
			if nl < 0 {
				t.Fatalf("base64 short-circuit missing newline header: %q", out)
			}
			decoded, decErr := base64.StdEncoding.DecodeString(out[nl+1:])
			if decErr != nil {
				t.Fatalf("base64 payload failed to decode: %v", decErr)
			}
			if !bytes.Equal(decoded, []byte(content)) {
				t.Fatalf("base64 payload != file bytes\n want=%q\n got =%q", content, decoded)
			}
			return
		}

		// Text window: reconstruct the reference window and compare exactly.
		want := referenceWindow(content, op, lp)
		if out != want {
			t.Fatalf("ReadFile window mismatch\n content=%q offset=%v limit=%v\n got =%q\n want=%q",
				content, op, lp, out, want)
		}
	})
}

// referenceWindow re-derives ReadFile's text-window output from first
// principles: CRLF→LF normalize, split on '\n', clamp start/limit exactly as the
// production code does, and emit "%4d\t<line>\n" per row. It is the reference
// implementation the window oracle compares against.
func referenceWindow(content string, offsetLine, limitLines *int) string {
	s := strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(s, "\n")

	start := 1
	if offsetLine != nil && *offsetLine > 0 {
		start = *offsetLine
	}
	limit := 2000
	if limitLines != nil && *limitLines > 0 {
		limit = *limitLines
	}
	if start > len(lines) {
		return ""
	}
	end := start - 1 + limit
	if end > len(lines) {
		end = len(lines)
	}
	var b strings.Builder
	for i := start; i <= end; i++ {
		b.WriteString(fmt.Sprintf("%4d\t%s\n", i, lines[i-1]))
	}
	return b.String()
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
		"webp": true, "bmp": true, "svg": true, "ico": true,
	}
	extMap := map[string]string{
		".png": "png", ".jpg": "jpeg", ".jpeg": "jpeg",
		".gif": "gif", ".webp": "webp", ".bmp": "bmp",
		".svg": "svg", ".ico": "ico",
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

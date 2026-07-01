package execenv

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"primeradiant.com/serf/fuzz/oracle"
)

// egrep_file is one visible file the reference scan knows about: its path
// relative to the search root and its raw bytes. The reference enumerates these
// directly (rather than re-walking the tree) so it is an INDEPENDENT oracle for
// grepNative's walk — if grepNative wrongly surfaced a hidden file, a skipped
// binary, or a glob-filtered file, its output would carry a line the reference
// never produced and the equality check would fire.
type egrep_file struct {
	rel     string
	content []byte
}

// egrep_scan re-derives grepNative's per-line matching from first principles for
// the three output modes, over the fixed set of visible files in the fixed walk
// order (root files lexically, then the visible subdir). It mirrors exactly the
// production predicates — binary (NUL) skip, glob match on the base name with
// filepath.Match's error swallowed to "no match", raw split on '\n' with no CRLF
// normalization, regex per line — but shares no code with grepNative.
//
// It returns the ordered content-mode matches ("rel:lineno:line"), the per-file
// match counts, and the ordered list of files that had at least one match. The
// caller applies the maxResults cap to these, matching grepNative's cross-file
// running cap.
func egrep_scan(files []egrep_file, re *regexp.Regexp, globFilter string) (content []string, counts map[string]int, withMatches []string) {
	counts = map[string]int{}
	for _, f := range files {
		if bytes.IndexByte(f.content, 0) >= 0 {
			continue // binary skip
		}
		if globFilter != "" {
			matched, _ := filepath.Match(globFilter, filepath.Base(f.rel))
			if !matched {
				continue
			}
		}
		fileHad := false
		lines := strings.Split(string(f.content), "\n")
		for i, line := range lines {
			if re.MatchString(line) {
				content = append(content, fmt.Sprintf("%s:%d:%s", f.rel, i+1, line))
				counts[f.rel]++
				fileHad = true
			}
		}
		if fileHad {
			withMatches = append(withMatches, f.rel)
		}
	}
	return content, counts, withMatches
}

// egrep_countOutput renders the reference count-mode output the way grepNative
// does: "rel:count" for every file with a nonzero count, sorted lexically.
func egrep_countOutput(counts map[string]int) string {
	var out []string
	for file, n := range counts {
		out = append(out, fmt.Sprintf("%s:%d", file, n))
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}

// egrep_effMax mirrors grepNative's maxResults defaulting: any value <= 0
// becomes 100.
func egrep_effMax(maxResults int) int {
	if maxResults <= 0 {
		return 100
	}
	return maxResults
}

// FuzzEgrepGrepNative drives grepNative — the native (ripgrep-absent) text
// search in agent/execenv/local.go — over a fuzzed tree of files, a fuzzed
// regex, and the full option space (case-insensitivity, glob filter, output
// mode, maxResults cap). grepNative reads the REAL OS filesystem directly (it
// does not route through the afero SetFs seam), so the tree is materialized
// under a t.TempDir; nothing escapes the sandbox and no subprocess is spawned
// (rg is bypassed by calling grepNative directly).
//
// The tree fixes a layout that exercises every walk branch: two root files with
// distinct extensions (glob + multi-file ordering), a visible subdir file
// (recursion + relPath joining), a hidden file and a hidden dir (both must be
// skipped). Contents A/B/C are fuzzed; the hidden entries are constant.
//
// Oracles:
//   - NEVER PANIC (any panic fails the test).
//   - REGEX-ERROR PARITY: grepNative errors iff the (flag-prefixed) pattern
//     fails to compile; on error it yields no output.
//   - REFERENCE EQUALITY (content mode, cap lifted): the output equals, byte for
//     byte and in order, an independent line scan of the visible, non-binary,
//     glob-passing files — proving matches are real regex hits AT their claimed
//     file:line, that none are missing, and that hidden/binary/filtered files are
//     correctly excluded.
//   - CAP CONSISTENCY: with the fuzzed (possibly small/zero/negative) maxResults,
//     content-mode output equals the first effMax lines of the uncapped output —
//     the exact cross-file running-cap contract.
//   - COUNT / FILES-WITH-MATCHES parity: both modes equal their reference
//     renderings derived from the same scan.
//   - DETERMINISM: a repeated content-mode call returns identical output.
func FuzzEgrepGrepNative(f *testing.F) {
	f.Add("hello world\nfoo\nhello again", "bar\nhello", "sub hello", "hel", uint8(0))
	f.Add("a\na\na", "a\na", "a", "b\nb", uint8(0b0000_0011))             // mode bits
	f.Add("MixedCase\nline", "other", "CASE", "case", uint8(0b0000_0001)) // case-insensitive
	f.Add("x1\nx2\nx3\nx4\nx5", "y", "z", "x", uint8(0b1110_0000))        // tiny maxResults
	f.Add("alpha\nbeta", "gamma", "delta", "^a", uint8(0b0000_1000))      // glob bits
	f.Add("has\x00nul\nvisible", "clean", "more", "vis", uint8(0))        // binary skip on file A
	f.Add("", "", "", "", uint8(0))
	f.Add("aaa", "bbb", "ccc", "(unclosed", uint8(0)) // invalid regex

	f.Fuzz(func(t *testing.T, contentA, contentB, contentC, pattern string, opts uint8) {
		// Bound sizes so an all-newlines megabyte (millions of formatted rows) or
		// a pathological program-sized pattern can't turn a correct O(n) scan into
		// a multi-second exec. The logic is fully exercised by modest inputs.
		if len(contentA)+len(contentB)+len(contentC) > 1<<16 || len(pattern) > 4096 {
			return
		}

		caseInsensitive := opts&0b0000_0001 != 0
		mode := []string{"content", "count", "files_with_matches"}[((opts>>1)&0b11)%3]
		globFilter := []string{"", "*", "*.txt", "*.md", "["}[((opts>>3)&0b111)%5]
		fuzzMax := int(opts>>5) - 1 // spans -1..6, covering <=0 default and small caps

		root := t.TempDir()
		// Visible files (fuzzed content) in the fixed lexical walk order.
		visible := []egrep_file{
			{rel: "a.txt", content: []byte(contentA)},
			{rel: "b.md", content: []byte(contentB)},
			{rel: filepath.Join("sub", "c.txt"), content: []byte(contentC)},
		}
		for _, vf := range visible {
			abs := filepath.Join(root, vf.rel)
			if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", abs, err)
			}
			if err := os.WriteFile(abs, vf.content, 0o644); err != nil {
				t.Fatalf("write %s: %v", abs, err)
			}
		}
		// Entries that MUST be skipped: a hidden file and a hidden directory.
		mustSkip(t, filepath.Join(root, ".hidden.txt"), []byte(pattern+"\nshadow"))
		mustSkip(t, filepath.Join(root, ".git", "config"), []byte(pattern))

		env := NewLocalExecutionEnvironment(root)

		flags := ""
		if caseInsensitive {
			flags = "(?i)"
		}
		refRe, compileErr := regexp.Compile(flags + pattern)

		// Regex-error parity: grepNative's sole error path is regex compilation.
		_, contentErr := env.grepNative(pattern, root, globFilter, caseInsensitive, 1<<20, "content")
		if (compileErr != nil) != (contentErr != nil) {
			t.Fatalf("regex-error parity broken: compile=%v grepNative=%v (pattern=%q ci=%v)",
				compileErr, contentErr, pattern, caseInsensitive)
		}
		if compileErr != nil {
			return // invalid regex: nothing more to check
		}

		refContent, refCounts, refFiles := egrep_scan(visible, refRe, globFilter)

		// Reference equality (content mode, cap lifted).
		gotFull, err := env.grepNative(pattern, root, globFilter, caseInsensitive, 1<<20, "content")
		if err != nil {
			t.Fatalf("grepNative content errored unexpectedly: %v", err)
		}
		wantFull := strings.Join(refContent, "\n")
		if gotFull != wantFull {
			t.Fatalf("content-mode reference mismatch\n pattern=%q ci=%v glob=%q\n got =%q\n want=%q",
				pattern, caseInsensitive, globFilter, gotFull, wantFull)
		}

		// Determinism: repeat the same call, expect identical output.
		oracle.Deterministic(t, func(struct{}) string {
			out, _ := env.grepNative(pattern, root, globFilter, caseInsensitive, 1<<20, "content")
			return out
		}, struct{}{}, func(a, b string) bool { return a == b })

		// Cap consistency: fuzzed maxResults keeps exactly the first effMax matches.
		gotCapped, err := env.grepNative(pattern, root, globFilter, caseInsensitive, fuzzMax, "content")
		if err != nil {
			t.Fatalf("grepNative capped content errored: %v", err)
		}
		eff := egrep_effMax(fuzzMax)
		capped := refContent
		if len(capped) > eff {
			capped = capped[:eff]
		}
		if wantCapped := strings.Join(capped, "\n"); gotCapped != wantCapped {
			t.Fatalf("cap consistency broken (max=%d eff=%d)\n got =%q\n want=%q",
				fuzzMax, eff, gotCapped, wantCapped)
		}

		// Soundness spot-check independent of the reference: every emitted content
		// line is a genuine regex hit at its claimed position.
		if gotCapped != "" {
			for _, ln := range strings.Split(gotCapped, "\n") {
				egrep_verifyMatchLine(t, ln, refRe, visible)
			}
		}

		// Count-mode parity.
		gotCount, err := env.grepNative(pattern, root, globFilter, caseInsensitive, 1<<20, "count")
		if err != nil {
			t.Fatalf("grepNative count errored: %v", err)
		}
		if want := egrep_countOutput(refCounts); gotCount != want {
			t.Fatalf("count-mode reference mismatch\n got =%q\n want=%q", gotCount, want)
		}

		// Files-with-matches parity (ordered, cap lifted).
		gotFiles, err := env.grepNative(pattern, root, globFilter, caseInsensitive, 1<<20, "files_with_matches")
		if err != nil {
			t.Fatalf("grepNative files_with_matches errored: %v", err)
		}
		if want := strings.Join(refFiles, "\n"); gotFiles != want {
			t.Fatalf("files_with_matches reference mismatch\n got =%q\n want=%q", gotFiles, want)
		}

		// Exercise the fuzz-selected mode too, so the selector's branches are all
		// reachable; its output already validated above by the per-mode oracles.
		if _, err := env.grepNative(pattern, root, globFilter, caseInsensitive, fuzzMax, mode); err != nil {
			t.Fatalf("grepNative mode=%q errored: %v", mode, err)
		}
	})
}

// mustSkip writes a file grepNative must never surface (a hidden file or a file
// under a hidden directory), creating parents as needed.
func mustSkip(t *testing.T, abs string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", abs, err)
	}
	if err := os.WriteFile(abs, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", abs, err)
	}
}

// egrep_verifyMatchLine asserts a content-mode output line "rel:lineno:text" is a
// real hit: text is the (lineno-1)th '\n'-split line of the file at rel, and the
// regex matches it. It is a reference-independent soundness check.
func egrep_verifyMatchLine(t *testing.T, ln string, re *regexp.Regexp, files []egrep_file) {
	t.Helper()
	// Split off "rel:lineno:" — rel may itself contain ':' only if a filename
	// did, which our fixed layout never does, so the first two colons delimit.
	first := strings.IndexByte(ln, ':')
	if first < 0 {
		t.Fatalf("malformed content line (no colon): %q", ln)
	}
	rest := ln[first+1:]
	second := strings.IndexByte(rest, ':')
	if second < 0 {
		t.Fatalf("malformed content line (one colon): %q", ln)
	}
	rel := ln[:first]
	text := rest[second+1:]
	if !re.MatchString(text) {
		t.Fatalf("emitted line does not match the pattern: %q", ln)
	}
	for _, fl := range files {
		if fl.rel != rel {
			continue
		}
		lineNoStr := rest[:second]
		var lineNo int
		if _, err := fmt.Sscanf(lineNoStr, "%d", &lineNo); err != nil {
			t.Fatalf("bad line number %q in %q", lineNoStr, ln)
		}
		lines := strings.Split(string(fl.content), "\n")
		if lineNo < 1 || lineNo > len(lines) {
			t.Fatalf("line number %d out of range for %s in %q", lineNo, rel, ln)
		}
		if lines[lineNo-1] != text {
			t.Fatalf("emitted text != actual file line\n line=%q\n file=%q", text, lines[lineNo-1])
		}
		return
	}
	t.Fatalf("emitted rel %q is not one of the visible files (leaked a skipped file?): %q", rel, ln)
}

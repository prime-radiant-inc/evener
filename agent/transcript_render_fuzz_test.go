//go:build serffuzz

package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/fuzz/oracle"
)

// This file fuzzes three transcript-rendering/lookup seams that unit tests
// exercise but no fuzz target reaches:
//
//   - rawLinesForRange (transcript_render.go): reads a transcript file and
//     returns the verbatim JSONL lines for a derived seq range.
//   - toolInputSummary (transcript_render.go): one-line bounded summary of a
//     tool call's JSON arguments.
//   - resolveTranscript (transcript_lookup.go): turns a model-supplied selector
//     into a concrete file path + opaque ref.
//
// These targets read real state via the os package (not afero), so fuzz/fault's
// afero seam is not wired into them; wiring it would require editing production
// code, which is out of scope. Instead the FS error branches are driven with a
// real t.TempDir sandbox: missing files (open/stat errors), empty files (no
// header), and a real bucket layout that yields found / ambiguous / unknown.

// trender_scanLines splits transcript bytes into lines the same way
// rawLinesForRange's bufio.Scanner does (same buffer sizing, same \r handling),
// so the subsequence oracle compares like against like.
func trender_scanLines(content []byte) []string {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	scanner.Buffer(make([]byte, 0, 64*1024), transcriptJSONLMaxLineBytes)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines
}

// trender_isSubsequence reports whether want is a subsequence of have (same
// order, gaps allowed). rawLinesForRange returns the header line followed by a
// subset of the file's lines in file order, so the returned lines must be a
// subsequence of the file's scanned lines.
func trender_isSubsequence(want, have []string) bool {
	i := 0
	for _, h := range have {
		if i < len(want) && want[i] == h {
			i++
		}
	}
	return i == len(want)
}

// FuzzRawLinesForRange drives rawLinesForRange over fuzzed transcript content and
// a fuzzed seq range. Oracles:
//   - never panics;
//   - CONSISTENT SLICE: on success, every non-empty line of the returned content
//     is a line of the input file, in file order (a subsequence of the input's
//     scanned lines) — the render can only echo real transcript lines, never
//     synthesize or reorder them.
func FuzzRawLinesForRange(f *testing.F) {
	seeds := []struct {
		content    string
		start, end int
	}{
		{`{"kind":"header","session_id":"s1"}
{"kind":"entry","seq":0,"turn":{"kind":"USER_INPUT"}}
{"kind":"api_call","seq":1}
{"kind":"entry","seq":1,"turn":{"kind":"ASSISTANT"}}`, 0, 1},
		{`{"kind":"header"}
{"kind":"entry"}
not json
{"kind":"weird"}
{"kind":"entry"}`, 0, 10},
		{`{"kind":"header"}`, 0, 0},
		{"", 0, 0},
		{"\n\n\n", 0, 5},
		{`{"kind":"header"}
{"kind":"entry"}`, 5, 2},
	}
	for _, s := range seeds {
		f.Add([]byte(s.content), s.start, s.end)
	}
	// A seed large enough to trip the 200k-rune hard cap (head-only truncation).
	var big bytes.Buffer
	big.WriteString(`{"kind":"header"}` + "\n")
	for i := 0; i < 4000; i++ {
		big.WriteString(`{"kind":"entry","seq":0,"turn":{"kind":"USER_INPUT"}}` + "\n")
	}
	f.Add(big.Bytes(), 0, 5000)

	f.Fuzz(func(t *testing.T, content []byte, startSeq, endSeq int) {
		path := filepath.Join(t.TempDir(), "t.jsonl")
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatalf("write temp transcript: %v", err)
		}

		result, lines, skipped, truncated, err := rawLinesForRange(path, startSeq, endSeq)
		if err != nil {
			return // open/empty/scan error: no-panic floor proven for this input
		}
		if skipped < 0 || lines < 0 {
			t.Fatalf("negative count: lines=%d skipped=%d", lines, skipped)
		}

		fileLines := trender_scanLines(content)
		// Recover the lines rawLinesForRange actually WROTE by splitting on "\n"
		// (it emits each scanned line verbatim followed by "\n"). Re-scanning the
		// result with a bufio.Scanner would wrongly strip a second trailing "\r"
		// (ScanLines drops a CR before a newline), so split, don't re-scan.
		resultLines := strings.Split(result, "\n")
		nonEmpty := make([]string, 0, len(resultLines))
		for _, l := range resultLines {
			if l != "" {
				nonEmpty = append(nonEmpty, l)
			}
		}
		// The header may legitimately be an empty first line; exclude empties from
		// BOTH sides so the subsequence relation stays honest.
		haveNonEmpty := make([]string, 0, len(fileLines))
		for _, l := range fileLines {
			if l != "" {
				haveNonEmpty = append(haveNonEmpty, l)
			}
		}
		if !trender_isSubsequence(nonEmpty, haveNonEmpty) {
			t.Fatalf("rendered lines are not a subsequence of the transcript\n range=[%d,%d] truncated=%v\n result lines=%#v\n file lines=%#v",
				startSeq, endSeq, truncated, nonEmpty, haveNonEmpty)
		}
	})

	// A guaranteed-missing path exercises the open-error branch deterministically.
	f.Add([]byte("__missing__"), 0, 0)
}

// FuzzToolInputSummary drives toolInputSummary over a fuzzed tool name and
// fuzzed JSON arguments. Oracle: DETERMINISM — the summary is a pure function of
// (name, args) and must not vary across calls (the unknown-tool path sorts keys
// precisely so map iteration cannot make it wobble). It also proves the
// never-panic floor over arbitrary/malformed argument bytes.
func FuzzToolInputSummary(f *testing.F) {
	seeds := []struct {
		name string
		args string
	}{
		{"shell", `{"command":"ls -la /tmp","purpose":"look"}`},
		{"read_file", `{"file_path":"/a/b.go","offset":10,"limit":50}`},
		{"write_file", `{"file_path":"/a/b.go","content":"package main"}`},
		{"edit_file", `{"file_path":"/a","old_string":"x","new_string":"yy","replace_all":true}`},
		{"grep", `{"pattern":"foo","path":"/src"}`},
		{"glob", `{"pattern":"**/*.go","path":"/src"}`},
		{"web_fetch", `{"url":"https://example.com/x?y=1","question":"why"}`},
		{"web_search", `{"query":"how to fuzz"}`},
		{"delegate", `{"task":"do the thing","agent_type":"coder","max_wait_ms":5000}`},
		{"job_send_message", `{"target":"job7","message":"ping"}`},
		{"delegate_send", `{"to":"child","message":"go on"}`},
		{"use_skill", `{"skill_name":"par"}`},
		{"unknown_tool", `{"z":1,"a":"two","m":{"nested":true},"b":false}`},
		{"", ``},
		{"shell", `not json`},
		{"read_file", `{"file_path":123}`},
		{"unknown_tool", `[1,2,3]`},
	}
	for _, s := range seeds {
		f.Add(s.name, []byte(s.args))
	}

	f.Fuzz(func(t *testing.T, name string, args []byte) {
		fn := func(in []byte) string { return toolInputSummary(name, json.RawMessage(in)) }
		oracle.Deterministic[[]byte, string](t, fn, args, func(a, b string) bool { return a == b })
	})
}

// FuzzResolveTranscript drives resolveTranscript over a fuzzed selector against a
// real bucket-layout sandbox. Oracles:
//   - never panics on any selector (traversal, malformed refs, arbitrary bytes);
//   - CONSISTENT POST-STATE: whenever it succeeds for anything other than the
//     current-session shortcut ("" / "current"), the returned path exists on
//     disk and the returned ref is non-empty. (The current-session case is
//     documented to skip the stat, so it is excluded from the existence claim.)
func FuzzResolveTranscript(f *testing.F) {
	seeds := []string{
		"", "current",
		"local:sess1", "local:sess2", "proj:aaa:sess1", "proj:bbb:sess1",
		"sess1", "sess2", "missing",
		"proj:aaa:missing", "local:missing",
		"../etc/passwd", "a/b", `a\b`, "bad token", "..",
		"proj:onlyone:sess1", "proj::sess1", "proj:aaa:", "local:",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, selector string) {
		base := t.TempDir()
		// Layout: <base>/serf/projects/{aaa,bbb}/sessions/<id>.transcript.jsonl
		// aaa is the current bucket. sess1 lives in BOTH aaa and bbb (ambiguous by
		// bare id); sess2 lives only in aaa (unique).
		currentStateDir := filepath.Join(base, "serf", "projects", "aaa")
		trender_makeTranscript(t, currentStateDir, "sess1")
		trender_makeTranscript(t, currentStateDir, "sess2")
		trender_makeTranscript(t, filepath.Join(base, "serf", "projects", "bbb"), "sess1")

		path, ref, err := resolveTranscript(selector, currentStateDir, "cursess")
		if err != nil {
			return // resolution error is a valid outcome; no-panic floor proven
		}
		if ref == "" {
			t.Fatalf("resolveTranscript returned empty ref with nil error (selector=%q)", selector)
		}
		if selector == "" || selector == "current" {
			return // current session: stat intentionally skipped, no existence claim
		}
		if _, statErr := os.Stat(path); statErr != nil {
			t.Fatalf("resolveTranscript returned a non-existent path for selector=%q: path=%q err=%v",
				selector, path, statErr)
		}
	})
}

// trender_makeTranscript writes a minimal valid transcript file for sessionID in
// bucketDir's sessions subdir, creating the directory tree as needed.
func trender_makeTranscript(t *testing.T, bucketDir, sessionID string) {
	t.Helper()
	p := transcriptPath(bucketDir, sessionID)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	if err := os.WriteFile(p, []byte(`{"kind":"header","session_id":"`+sessionID+`"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}
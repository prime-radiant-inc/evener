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
	"unicode/utf8"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/fuzz/oracle"
	"primeradiant.com/serf/llm"
)

const (
	trenderCurrentProject = "project-a-0123456789"
	trenderOtherProject   = "project-b-0123456789"
	trenderCurrentSession = "02wMz5TxvEMoJEDTDGOTil"
	trenderLocalSession   = "02wMz5Txv2enqVTitaig6F"
	trenderSharedSession  = "02wMz5Txv733WHFsVy66SR"
	trenderRemoteSession  = "02wMz5Txv5aIxgf9yVdd0N"
	trenderMissingSession = "02wMz5TxvBRJC3228LTWod"
	trenderParentSession  = "02wMz5TxvCu3kdckfnw0Gh"
)

// This file fuzzes four transcript-rendering/lookup seams that unit tests
// exercise but no fuzz target reaches:
//
//   - rawLinesForRange (transcript_render.go): reads a transcript file and
//     returns semantic-only JSONL lines for a derived seq range.
//   - toolInputSummary (transcript_render.go): one-line bounded summary of a
//     tool call's JSON arguments.
//   - renderTranscript (transcript_render.go): renders typed transcript turns,
//     ranges, tool-result pairings, and expanded pins into bounded markdown.
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
// order, gaps allowed). The semantic header may be rewritten to exclude its
// system-prompt copy, while selected entry lines remain verbatim and ordered.
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
		{`{"kind":"header","format_version":2,"session_id":"s1","system_prompt":"private"}
{"kind":"entry","seq":0,"turn":{"kind":"USER_INPUT"}}
{"kind":"entry","seq":1,"turn":{"kind":"ASSISTANT"}}`, 0, 1},
		{`{"kind":"header","format_version":2,"session_id":"s2"}
{"kind":"entry"}
not json
{"kind":"weird"}
{"kind":"entry"}`, 0, 10},
		{`{"kind":"header","format_version":2,"session_id":"s3"}`, 0, 0},
		{"", 0, 0},
		{"\n\n\n", 0, 5},
		{`{"kind":"header","format_version":2,"session_id":"s4"}
{"kind":"entry"}`, 5, 2},
	}
	for _, s := range seeds {
		f.Add([]byte(s.content), s.start, s.end)
	}
	// A seed large enough to trip the 200k-rune hard cap (head-only truncation).
	var big bytes.Buffer
	big.WriteString(`{"kind":"header","format_version":2,"session_id":"big"}` + "\n")
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
		if len(nonEmpty) == 0 {
			t.Fatal("successful semantic JSONL render omitted its header")
		}
		var header map[string]any
		if err := json.Unmarshal([]byte(nonEmpty[0]), &header); err != nil || header["kind"] != "header" {
			t.Fatalf("semantic header = %q, error %v", nonEmpty[0], err)
		}
		if _, leaked := header["system_prompt"]; leaked {
			t.Fatalf("semantic header retained system_prompt: %q", nonEmpty[0])
		}
		if !trender_isSubsequence(nonEmpty[1:], haveNonEmpty) {
			t.Fatalf("rendered entry lines are not a subsequence of the transcript\n range=[%d,%d] truncated=%v\n result lines=%#v\n file lines=%#v",
				startSeq, endSeq, truncated, nonEmpty[1:], haveNonEmpty)
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

// FuzzRenderTranscriptProgram drives the full typed markdown-rendering path
// using a small finite input language. The language constructs valid and
// deliberately malformed transcript shapes without decoding arbitrary wire
// data, so each replay remains bounded and deterministic. Oracles:
//
//   - DETERMINISM: rendering and its provenance metadata are stable;
//   - ACCOUNTING: rendered + elided turns equals the supplied entry count;
//   - BOUNDS: the hard-cap contract holds even for a pinned full result;
//   - STRUCTURE: every successful render retains its document header.
func FuzzRenderTranscriptProgram(f *testing.F) {
	for scenario := byte(0); scenario < 13; scenario++ {
		f.Add([]byte{scenario}, "fuzz payload")
	}
	// Exercise the bulk byte bound at the first byte of a multibyte rune.
	f.Add([]byte{5}, strings.Repeat("a", 47)+string(rune(0x20AC)))

	f.Fuzz(func(t *testing.T, program []byte, payload string) {
		scenario := trenderScenario(program)
		header, entries, rangeSpec, opt := trender_program(program, payload)

		got, meta := renderTranscript(header, entries, rangeSpec, opt)
		again, againMeta := renderTranscript(header, entries, rangeSpec, opt)
		if got != again || meta != againMeta {
			t.Fatalf("renderTranscript was not deterministic for program=%x", program)
		}
		if meta.TurnsTotal != len(entries) {
			t.Fatalf("TurnsTotal = %d, want %d", meta.TurnsTotal, len(entries))
		}
		if meta.TurnsRendered+meta.ElidedTurns != meta.TurnsTotal {
			t.Fatalf("rendered + elided = %d + %d, want %d", meta.TurnsRendered, meta.ElidedTurns, meta.TurnsTotal)
		}
		if meta.TurnsRendered == 0 {
			if meta.FirstRendered != 0 || meta.LastRendered != -1 {
				t.Fatalf("empty render span = [%d,%d], want [0,-1]", meta.FirstRendered, meta.LastRendered)
			}
		} else if meta.FirstRendered < 0 || meta.LastRendered < meta.FirstRendered || meta.LastRendered >= len(entries) {
			t.Fatalf("invalid render span = [%d,%d] for %d entries", meta.FirstRendered, meta.LastRendered, len(entries))
		} else if meta.LastRendered-meta.FirstRendered+1 != meta.TurnsRendered {
			t.Fatalf("render span [%d,%d] has %d turns, metadata says %d", meta.FirstRendered, meta.LastRendered, meta.LastRendered-meta.FirstRendered+1, meta.TurnsRendered)
		}
		if meta.TurnsRendered < meta.TurnsTotal && !meta.Truncated {
			t.Fatal("elided turns did not set Truncated")
		}
		if !strings.HasPrefix(got, "# Transcript: ") {
			t.Fatalf("missing document header: %q", got)
		}
		if len([]rune(got)) > hardCapChars {
			t.Fatalf("rendered %d runes, exceeds hard cap %d", len([]rune(got)), hardCapChars)
		}
		if !utf8.ValidString(got) {
			t.Fatal("rendered markdown is not valid UTF-8")
		}
		if opt.fullResultFor != nil {
			trenderAssertPagedExpansion(t, header, entries, rangeSpec, opt)
		}

		switch scenario {
		case 7:
			if lines := strings.Count(got, "full line"); lines < 35 {
				t.Fatalf("in-range pinned result was condensed to %d lines, want all 35", lines)
			}
		case 8:
			if !strings.Contains(got, "pinned turn 0 (full result, outside range)") {
				t.Fatalf("result-seq pin did not render its out-of-range owner: %q", got)
			}
			if lines := strings.Count(got, "outside line"); lines < 35 {
				t.Fatalf("out-of-range pinned result was condensed to %d lines, want all 35", lines)
			}
		case 9:
			if !meta.Truncated {
				t.Fatal("hard-cap scenario did not set Truncated")
			}
			if !strings.Contains(got, "content truncated at the 200,000-character hard cap") {
				t.Fatalf("hard-cap scenario omitted truncation marker: %q", got)
			}
		}
	})
}

func TestTrenderExpansionOracleDistinguishesNeighboringRecords(t *testing.T) {
	header, entries, _, opt := trender_program([]byte{8}, "oracle")
	path := filepath.Join(t.TempDir(), "oracle.transcript.jsonl")
	header.SessionID = "oracle"
	w, err := transcript.NewWriter(path, header)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if err := w.Append(entry.Turn); err != nil {
			_ = w.Close()
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	want := trenderExpectedPairedExpansionJSONL(t, path, entries, *opt.fullResultFor)
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	records := bytes.SplitAfter(persisted, []byte{'\n'})
	wrongSpan := append(bytes.Clone(records[1]), records[2]...)
	const verifiedPageBytes = 2 * 64
	wantPrefix := want[:min(len(want), verifiedPageBytes)]
	wrongPrefix := wrongSpan[:min(len(wrongSpan), verifiedPageBytes)]
	if len(want) == len(wrongSpan) && bytes.Equal(wantPrefix, wrongPrefix) {
		t.Fatal("independent expansion oracle cannot reject a neighboring span through the checked pages or total_bytes")
	}
}

func trenderExpectedPairedExpansionJSONL(t *testing.T, path string, entries []transcript.Entry, pin int) []byte {
	t.Helper()
	if len(entries) < 2 {
		t.Fatalf("paired expansion fixture has %d entries, want at least 2", len(entries))
	}
	if pin != 0 && pin != 1 {
		t.Fatalf("paired expansion pin = %d, want assistant 0 or its result 1", pin)
	}
	if entries[0].Turn.Kind != schema.TurnAssistant || entries[1].Turn.Kind != schema.TurnToolResults {
		t.Fatalf("paired expansion fixture starts with %s/%s, want ASSISTANT/TOOL_RESULTS", entries[0].Turn.Kind, entries[1].Turn.Kind)
	}

	callID := ""
	for _, part := range entries[0].Turn.Message.Content {
		if part.Kind == llm.ContentToolCall && part.ToolCall != nil && part.ToolCall.ID != "" {
			if callID != "" {
				t.Fatal("paired expansion fixture has more than one owned tool call")
			}
			callID = part.ToolCall.ID
		}
	}
	resultCallID := ""
	for _, part := range entries[1].Turn.Message.Content {
		if part.Kind == llm.ContentToolResult && part.ToolResult != nil && part.ToolResult.ToolCallID != "" {
			if resultCallID != "" {
				t.Fatal("paired expansion fixture has more than one owned tool result")
			}
			resultCallID = part.ToolResult.ToolCallID
		}
	}
	if callID == "" || resultCallID != callID {
		t.Fatalf("paired expansion ownership = call %q/result %q", callID, resultCallID)
	}

	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	records := bytes.SplitAfter(persisted, []byte{'\n'})
	if len(records) != len(entries)+2 || len(records[len(records)-1]) != 0 {
		t.Fatalf("persisted records = %d with trailing bytes %d, want header + %d newline-terminated entries", len(records)-1, len(records[len(records)-1]), len(entries))
	}
	if pin == 0 {
		return append(bytes.Clone(records[1]), records[2]...)
	}
	return bytes.Clone(records[2])
}

func trenderAssertPagedExpansion(t *testing.T, header transcript.Header, entries []transcript.Entry, rangeSpec string, opt renderOpts) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "paged.transcript.jsonl")
	header.SessionID = "paged"
	w, err := transcript.NewWriter(path, header)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if err := w.Append(entry.Turn); err != nil {
			_ = w.Close()
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	want := trenderExpectedPairedExpansionJSONL(t, path, entries, *opt.fullResultFor)

	const pageBytes = 64
	firstAny, err := readMarkdownPage(path, "local:paged", opt.meta, rangeSpec, opt.fullResultFor, 0, pageBytes)
	if err != nil {
		t.Fatal(err)
	}
	first, ok := firstAny.(readMarkdownEnvelope)
	if !ok || first.Expansion == nil {
		t.Fatalf("first expansion page = %#v", firstAny)
	}
	firstBytes, err := decodeTranscriptExpansion(first.Expansion)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBytes, want[:len(firstBytes)]) {
		t.Fatalf("first expansion page for pin %d does not match persisted transcript JSONL: got %q want %q", *opt.fullResultFor, firstBytes, want[:len(firstBytes)])
	}
	if first.Expansion.Representation != transcriptV2JSONLRepresentation || first.Expansion.TotalBytes != len(want) {
		t.Fatalf("first expansion metadata = %#v, want representation %q and %d bytes", first.Expansion, transcriptV2JSONLRepresentation, len(want))
	}
	if len(want) <= len(firstBytes) {
		if first.Continuation != nil {
			t.Fatalf("complete expansion returned continuation %#v", first.Continuation)
		}
		return
	}
	if first.Continuation == nil || first.Continuation.OffsetBytes != len(firstBytes) {
		t.Fatalf("first continuation = %#v, want offset %d", first.Continuation, len(firstBytes))
	}

	secondAny, err := readMarkdownPage(path, "local:paged", opt.meta, rangeSpec, opt.fullResultFor, first.Continuation.OffsetBytes, pageBytes)
	if err != nil {
		t.Fatal(err)
	}
	second, ok := secondAny.(readMarkdownEnvelope)
	if !ok || second.Expansion == nil {
		t.Fatalf("second expansion page = %#v", secondAny)
	}
	secondBytes, err := decodeTranscriptExpansion(second.Expansion)
	if err != nil {
		t.Fatal(err)
	}
	start := first.Continuation.OffsetBytes
	if !bytes.Equal(secondBytes, want[start:start+len(secondBytes)]) {
		t.Fatal("second expansion page does not continue persisted transcript JSONL")
	}
}

func trenderScenario(program []byte) byte {
	if len(program) == 0 {
		return 0
	}
	return program[0] % 13
}

func trender_program(program []byte, payload string) (transcript.Header, []transcript.Entry, string, renderOpts) {
	scenario := trenderScenario(program)
	payload = trender_boundedPayload(payload)
	bulkPayload := trenderUTF8Prefix(payload, 48)

	header := transcript.Header{Task: "task " + payload}
	opt := renderOpts{meta: schema.SessionMeta{
		Name:           "session " + payload,
		OriginalPrompt: "prompt " + payload,
	}}

	part := func(kind llm.ContentKind, text string) llm.ContentPart {
		return llm.ContentPart{Kind: kind, Text: text}
	}
	call := func(id, name, args string) llm.ContentPart {
		return llm.ContentPart{
			Kind: llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{
				ID:        id,
				Name:      name,
				Arguments: json.RawMessage(args),
			},
		}
	}
	result := func(id, name, content string, isError bool) llm.ContentPart {
		return llm.ContentPart{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID: id,
				Name:       name,
				Content:    content,
				IsError:    isError,
			},
		}
	}
	entry := func(kind schema.TurnKind, parts ...llm.ContentPart) transcript.Entry {
		return transcript.Entry{
			Kind: "entry",
			Turn: schema.Turn{
				Kind:    kind,
				Message: llm.Message{Content: parts},
			},
		}
	}

	switch scenario {
	case 0:
		return header, nil, "", opt
	case 1:
		entries := []transcript.Entry{
			entry(schema.TurnUserInput, part(llm.ContentText, "user "+payload)),
			entry(schema.TurnAssistant,
				llm.ContentPart{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "think " + payload}},
				llm.ContentPart{Kind: llm.ContentRedThinking},
				part(llm.ContentText, "assistant "+payload),
				call("call-shell", "shell", `{"command":"printf hi","purpose":"inspect"}`),
				call("call-result", "communicate", `{"message":"done"}`),
				llm.ContentPart{Kind: llm.ContentToolCall},
			),
			entry(schema.TurnToolResults,
				result("call-shell", "shell", "line one\n```\nline two", false),
				result("call-result", "communicate", `{"accepted":true}`, false),
				result("orphan", "read_file", `{"path":"/tmp/a","ok":true}`, true),
			),
			entry(schema.TurnSteering, part(llm.ContentText, "steer\nsecond line")),
			entry(schema.TurnSummary, part(llm.ContentText, "summary")),
			entry(schema.TurnCheckpoint),
			entry(schema.TurnSystem),
			entry(schema.TurnTool),
			entry(schema.TurnKind("FUTURE")),
		}
		return header, entries, "", opt
	case 2:
		entries := []transcript.Entry{
			entry(schema.TurnAssistant,
				call("one", "read_file", `{"file_path":"/tmp/a","offset":1,"limit":2}`),
				call("two", "reply", `{"message":"custom result"}`),
			),
			entry(schema.TurnToolResults,
				result("one", "read_file", `{"nested":{"x":1},"items":[true,false]}`, false),
				result("one", "read_file", "later duplicate is ignored", true),
				result("two", "reply", `{"accepted":true}`, false),
				result("", "missing", "dropped", false),
			),
		}
		opt.resultToolName = "reply"
		return header, entries, "0-1", opt
	case 3:
		bodyBytes, err := json.Marshal(struct {
			JobID            string          `json:"job_id"`
			Status           string          `json:"status"`
			Reason           string          `json:"reason"`
			TranscriptRef    string          `json:"transcript_ref"`
			Output           string          `json:"output"`
			StructuredResult map[string]bool `json:"structured_result"`
			Delivered        bool            `json:"delivered"`
		}{
			JobID:            "job-1",
			Status:           "completed",
			Reason:           "done",
			TranscriptRef:    "local:child",
			Output:           payload,
			StructuredResult: map[string]bool{"ok": true},
			Delivered:        true,
		})
		if err != nil {
			panic(err)
		}
		entries := []transcript.Entry{
			entry(schema.TurnAssistant, call("job", "delegate", `{"task":"child"}`)),
			entry(schema.TurnToolResults, result("job", "delegate", string(bodyBytes), false)),
		}
		return header, entries, "last:2", opt
	case 4:
		entries := []transcript.Entry{
			entry(schema.TurnAssistant, call("job", "delegate_send", `not json`)),
			entry(schema.TurnToolResults, result("job", "delegate_send", `{"job_id":"job-2","status":"failed","extra":{"kept":true}}`, true)),
			entry(schema.TurnToolResults, result("orphan", "unknown", strings.Repeat("line\n", 36), false)),
		}
		return header, entries, "last:99", opt
	case 5:
		entries := trender_manyEntries(48, strings.Repeat("old "+bulkPayload+" ", 140))
		return header, entries, "", opt
	case 6:
		entries := trender_manyEntries(48, strings.Repeat("front "+bulkPayload+" ", 140))
		return header, entries, "start:48", opt
	case 7:
		header, opt = trenderPinnedFixture()
		entries := []transcript.Entry{
			entry(schema.TurnAssistant, call("pinned", "read_file", `{"file_path":"/tmp/pin"}`)),
			entry(schema.TurnToolResults, result("pinned", "read_file", strings.Repeat("full line\n", 35), false)),
		}
		pin := 0
		opt.fullResultFor = &pin
		return header, entries, "0-1", opt
	case 8:
		header, opt = trenderPinnedFixture()
		entries := []transcript.Entry{
			entry(schema.TurnAssistant, call("pinned", "shell", `{"command":"echo pin"}`)),
			entry(schema.TurnToolResults, result("pinned", "shell", strings.Repeat("outside line\n", 35), false)),
		}
		entries = append(entries, trender_manyEntries(45, "later fixture")...)
		// Pinning the result entry (rather than its assistant owner) exercises
		// the result-to-call ownership lookup used by expand_turn.
		pin := 1
		opt.fullResultFor = &pin
		return header, entries, "last:1", opt
	case 9:
		header, opt = trenderPinnedFixture()
		entries := []transcript.Entry{
			entry(schema.TurnAssistant, call("pinned", "read_file", `{"file_path":"/tmp/pin"}`)),
			entry(schema.TurnToolResults, result("pinned", "read_file", strings.Repeat("very long pinned result\n", 11000), false)),
		}
		pin := 0
		opt.fullResultFor = &pin
		return header, entries, "0-1", opt
	case 10:
		return header, []transcript.Entry{entry(schema.TurnUserInput, part(llm.ContentText, payload))}, "last:0", opt
	case 11:
		return header, trender_manyEntries(3, payload), "invalid-range", opt
	default:
		entries := []transcript.Entry{
			entry(schema.TurnAssistant,
				part(llm.ContentText, payload),
				call("bad", "unknown_tool", `{"z":1,"a":"two"}`),
			),
			entry(schema.TurnToolResults, result("bad", "unknown_tool", "plain body", false)),
		}
		return header, entries, "2-1", opt
	}
}

func trenderPinnedFixture() (transcript.Header, renderOpts) {
	return transcript.Header{Task: "pinned fixture"}, renderOpts{meta: schema.SessionMeta{
		Name:           "pinned fixture",
		OriginalPrompt: "pinned fixture",
	}}
}

func trender_manyEntries(n int, body string) []transcript.Entry {
	entries := make([]transcript.Entry, 0, n)
	for i := 0; i < n; i++ {
		kind := schema.TurnUserInput
		role := llm.RoleUser
		if i%2 == 1 {
			kind = schema.TurnAssistant
			role = llm.RoleAssistant
		}
		entries = append(entries, transcript.Entry{
			Kind: "entry",
			Turn: schema.Turn{
				Kind: kind,
				Message: llm.Message{
					Role:    role,
					Content: []llm.ContentPart{{Kind: llm.ContentText, Text: body}},
				},
			},
		})
	}
	return entries
}

func trender_boundedPayload(payload string) string {
	const maxBytes = 256
	if len(payload) > maxBytes {
		payload = payload[:maxBytes]
	}
	payload = strings.ToValidUTF8(payload, "?")
	if payload == "" {
		return "payload"
	}
	return payload
}

func trenderUTF8Prefix(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	limit := maxBytes
	for limit > 0 && !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
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
		"local:" + trenderSharedSession, "local:" + trenderLocalSession,
		"proj:" + trenderCurrentProject + ":" + trenderSharedSession,
		"proj:" + trenderOtherProject + ":" + trenderSharedSession,
		trenderSharedSession, trenderLocalSession, trenderMissingSession,
		"proj:" + trenderCurrentProject + ":" + trenderMissingSession,
		"local:" + trenderMissingSession,
		"../etc/passwd", "a/b", `a\b`, "bad token", "..",
		"proj:onlyone:" + trenderSharedSession, "proj::" + trenderSharedSession,
		"proj:" + trenderCurrentProject + ":", "local:",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, selector string) {
		base := t.TempDir()
		// The shared session lives in both valid project buckets (ambiguous by bare
		// ID); the local session lives only in the current bucket.
		currentStateDir := filepath.Join(base, "serf", "projects", trenderCurrentProject)
		trender_makeTranscript(t, currentStateDir, trenderSharedSession)
		trender_makeTranscript(t, currentStateDir, trenderLocalSession)
		trender_makeTranscript(t, filepath.Join(base, "serf", "projects", trenderOtherProject), trenderSharedSession)

		path, ref, err := resolveTranscript(selector, currentStateDir, trenderCurrentSession)
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
	if err := os.WriteFile(p, []byte(`{"kind":"header","format_version":2,"session_id":"`+sessionID+`"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}

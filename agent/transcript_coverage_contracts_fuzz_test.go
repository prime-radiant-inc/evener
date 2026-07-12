//go:build serffuzz

package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// FuzzTranscriptCoverageContracts complements the stateful transcript programs
// with bounded error and formatting cases. Every file it reads is rooted in a
// per-run temporary directory; no provider, process, or ambient state is used.
func FuzzTranscriptCoverageContracts(f *testing.F) {
	for _, seed := range [][]byte{nil, {0}, {1, 2, 3}, {255, 17, 0, 99}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, program []byte) {
		// Keep the public tool path in this target, including the shell-job arm.
		stmRunJobTranscriptContracts(t, program)
		_ = tdrpRun(t, program)
		tccReadContracts(t)
		tccLookupContracts(t)
		tccRenderContracts(t)
	})
}

func tccReadContracts(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	missing := filepath.Join(root, "missing.jsonl")
	if _, _, _, err := readTranscript(missing); err == nil {
		t.Fatal("readTranscript accepted a missing file")
	}
	if _, err := readTranscriptFull(missing); err == nil {
		t.Fatal("readTranscriptFull accepted a missing file")
	}
	if _, err := readStrictChildTranscript(missing, "child", 64); err == nil {
		t.Fatal("strict reader accepted a missing file")
	}

	empty := tccWrite(t, root, "empty", "")
	badHeader := tccWrite(t, root, "bad-header", "not-json\n")
	wrongKind := tccWrite(t, root, "wrong-kind", `{"kind":"entry","session_id":"child"}`+"\n")
	for _, path := range []string{empty, badHeader} {
		if _, _, _, err := readTranscript(path); err == nil {
			t.Fatalf("readTranscript accepted %s", filepath.Base(path))
		}
		if _, err := readTranscriptFull(path); err == nil {
			t.Fatalf("readTranscriptFull accepted %s", filepath.Base(path))
		}
	}
	for _, path := range []string{empty, badHeader, wrongKind} {
		if _, err := readStrictChildTranscript(path, "child", 64); err == nil {
			t.Fatalf("strict reader accepted %s", filepath.Base(path))
		}
	}

	header := `{"kind":"header","session_id":"child"}` + "\n"
	entry := `{"kind":"entry","turn":{"kind":"USER_INPUT","message":{"role":"user","content":[{"kind":"text","text":"hello"}]}}}` + "\n"
	apiCall := `{"kind":"api_call"}` + "\n"
	badEntry := `{"kind":"entry","turn":"bad"}` + "\n"
	badAPI := `{"kind":"api_call","request":false}` + "\n"
	valid := tccWrite(t, root, "valid", header+"\n"+entry+apiCall+badEntry+badAPI+"{broken}\n"+`{"kind":"future"}`+"\n")
	gotHeader, entries, skipped, err := readTranscript(valid)
	if err != nil || gotHeader.SessionID != "child" || len(entries) != 1 || skipped != 2 {
		t.Fatalf("lenient read = header=%#v entries=%d skipped=%d err=%v", gotHeader, len(entries), skipped, err)
	}
	full, err := readTranscriptFull(valid)
	if err != nil || len(full.Entries) != 1 || len(full.APICalls) != 1 || full.Skipped != 4 {
		t.Fatalf("full read = %#v err=%v", full, err)
	}
	if got := ResumeHistory(full.Entries); len(got) != 1 {
		t.Fatalf("resume history without compaction = %d turns", len(got))
	}
	compacted := []transcript.Entry{
		{Turn: schema.Turn{Kind: schema.TurnUserInput}},
		{Turn: schema.Turn{Kind: schema.TurnSummary}},
		{Turn: schema.Turn{Kind: schema.TurnAssistant}},
	}
	if got := ResumeHistory(compacted); len(got) != 2 || got[0].Kind != schema.TurnSummary {
		t.Fatalf("resume history after compaction = %#v", got)
	}

	strictValid := tccWrite(t, root, "strict-valid", header+entry+apiCall)
	strict, err := readStrictChildTranscript(strictValid, "child", 0)
	if err != nil || len(strict.Entries) != 1 || len(strict.APICalls) != 1 {
		t.Fatalf("strict read = %#v err=%v", strict, err)
	}
	validated, err := validateStrictChildTranscript(strictValid, "child", 0)
	if err != nil || validated.SessionID != "child" {
		t.Fatalf("strict validation = %#v err=%v", validated, err)
	}
	if _, err := readStrictChildTranscript(strictValid, "other", 0); !errors.Is(err, errStrictChildTranscriptSessionMismatch) {
		t.Fatalf("strict mismatch error = %v", err)
	}
	if data, err := readStrictChildTranscriptWithOptions(strictValid, "child", false, 0); err != nil || len(data.Entries) != 0 || len(data.APICalls) != 0 {
		t.Fatalf("validation-only strict read = %#v err=%v", data, err)
	}

	strictCases := map[string]string{
		"unknown":        header + `{"kind":"future"}` + "\n",
		"bad-line":       header + "not-json\n",
		"bad-entry":      header + `{"kind":"entry","turn":"bad"}` + "\n",
		"bad-api":        header + `{"kind":"api_call","request":false}` + "\n",
		"partial-tail":   header + `{"kind":`,
		"partial-entry":  header + `{"kind":"entry","turn":"bad"}`,
		"partial-api":    header + `{"kind":"api_call","request":false}`,
		"blank-and-tail": header + "\n" + entry,
	}
	for name, body := range strictCases {
		path := tccWrite(t, root, name, body)
		data, err := readStrictChildTranscript(path, "child", 0)
		if strings.HasPrefix(name, "partial-") {
			if err != nil || data.Skipped != 1 {
				t.Fatalf("partial tail = %#v err=%v", data, err)
			}
			continue
		}
		if name == "blank-and-tail" {
			if err != nil || len(data.Entries) != 1 {
				t.Fatalf("blank/final entry = %#v err=%v", data, err)
			}
			continue
		}
		if err == nil {
			t.Fatalf("strict reader accepted %s", name)
		}
	}

	long := tccWrite(t, root, "long", header+strings.Repeat("x", 80)+"\n")
	if _, err := readStrictChildTranscript(long, "child", 48); !errors.Is(err, errStrictChildTranscriptCorrupt) {
		t.Fatalf("oversized strict line error = %v", err)
	}
	longHeader := tccWrite(t, root, "long-header", strings.Repeat("x", 80)+"\n")
	if _, err := readStrictChildTranscript(longHeader, "child", 48); !errors.Is(err, errStrictChildTranscriptCorrupt) {
		t.Fatalf("oversized strict header error = %v", err)
	}
	line, err := readStrictTranscriptLine(bufio.NewReader(bytes.NewBufferString("fragment")), 64)
	if string(line) != "fragment" || !errors.Is(err, io.EOF) {
		t.Fatalf("final strict line = %q err=%v", line, err)
	}
	fragmented := strings.Repeat("x", 70<<10)
	line, err = readStrictTranscriptLine(bufio.NewReader(bytes.NewBufferString(fragmented)), 80<<10)
	if string(line) != fragmented || !errors.Is(err, io.EOF) {
		t.Fatalf("fragmented strict line length=%d err=%v", len(line), err)
	}

	for _, rangeSpec := range []string{"", "0-0", "last:1", "bad"} {
		if _, err := readMarkdown(valid, "local:child", schema.SessionMeta{ID: "child"}, rangeSpec, nil); err != nil {
			t.Fatalf("readMarkdown(%q): %v", rangeSpec, err)
		}
		if _, err := readOutline(valid, "local:child", rangeSpec); err != nil {
			t.Fatalf("readOutline(%q): %v", rangeSpec, err)
		}
		if _, err := readRaw(valid, "local:child", rangeSpec); err != nil {
			t.Fatalf("readRaw(%q): %v", rangeSpec, err)
		}
	}
	for _, fn := range []func() error{
		func() error {
			_, err := readMarkdown(missing, "local:child", schema.SessionMeta{}, "", nil)
			return err
		},
		func() error { _, err := readOutline(missing, "local:child", ""); return err },
		func() error { _, err := readRaw(missing, "local:child", ""); return err },
	} {
		if err := fn(); err == nil {
			t.Fatal("transcript format reader accepted a missing file")
		}
	}
	if _, _, _, _, err := rawLinesForRange(missing, 0, 1); err == nil {
		t.Fatal("rawLinesForRange accepted a missing file")
	}
	if _, _, _, _, err := rawLinesForRange(empty, 0, 1); err == nil {
		t.Fatal("rawLinesForRange accepted an empty file")
	}
	if content, lines, rawSkipped, truncated, err := rawLinesForRange(valid, 0, 0); err != nil || content == "" || lines < 2 || rawSkipped < 1 || truncated {
		t.Fatalf("raw range = lines=%d skipped=%d truncated=%v err=%v", lines, rawSkipped, truncated, err)
	}
	largeLine := `{"kind":"entry","padding":"` + strings.Repeat("x", hardCapChars) + `"}` + "\n"
	large := tccWrite(t, root, "large-raw", header+largeLine)
	if _, _, _, truncated, err := rawLinesForRange(large, 0, 0); err != nil || !truncated {
		t.Fatalf("large raw range truncated=%v err=%v", truncated, err)
	}
}

func tccLookupContracts(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	current := filepath.Join(root, "serf", "projects", "aaa")
	other := filepath.Join(root, "serf", "projects", "bbb")
	trender_makeTranscript(t, current, "current")
	trender_makeTranscript(t, current, "localonly")
	trender_makeTranscript(t, current, "both")
	trender_makeTranscript(t, other, "remoteonly")
	trender_makeTranscript(t, other, "both")
	if err := os.WriteFile(filepath.Join(root, "serf", "projects", "not-a-dir"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		selector string
		wantErr  bool
	}{
		{"", false}, {"current", false}, {"local:localonly", false},
		{"proj:bbb:remoteonly", false}, {"local:missing", true},
		{"proj:bbb:missing", true}, {"localonly", false}, {"remoteonly", false},
		{"both", true}, {"unknown", true}, {"../bad", true}, {"bad token", true},
	}
	for _, tc := range cases {
		_, _, err := resolveTranscript(tc.selector, current, "current")
		if (err != nil) != tc.wantErr {
			t.Fatalf("resolveTranscript(%q) err=%v wantErr=%v", tc.selector, err, tc.wantErr)
		}
	}
	deps := &toolDeps{stateDir: current, sessionID: "current", currentMeta: func() schema.SessionMeta { return schema.SessionMeta{ID: "current", Name: "live"} }}
	if meta := resolvedSessionMeta(deps, transcriptPath(current, "current"), encodeRef("", "current")); meta.Name != "live" {
		t.Fatalf("live resolved metadata = %#v", meta)
	}
	if meta := resolvedSessionMeta(deps, transcriptPath(current, "missing"), encodeRef("", "missing")); meta.ID != "missing" {
		t.Fatalf("fallback resolved metadata = %#v", meta)
	}
	for _, format := range []string{"", formatMarkdown, formatOutline, formatJSONL} {
		if _, err := execReadSessionTranscript(deps, map[string]any{"transcript_ref": "local:current", "format": format}); err != nil {
			t.Fatalf("execReadSessionTranscript(%q): %v", format, err)
		}
	}
	if _, err := execReadSessionTranscript(deps, map[string]any{"transcript_ref": "local:current", "format": "future"}); err == nil {
		t.Fatal("execReadSessionTranscript accepted unknown format")
	}
	flat := filepath.Join(root, "flat")
	if _, _, err := resolveTranscript("proj:bbb:remoteonly", flat, "current"); err == nil {
		t.Fatal("project ref resolved from flat state dir")
	}
	if got := stateHomeFor(current); got != root {
		t.Fatalf("stateHomeFor = %q, want %q", got, root)
	}
	for _, malformed := range []string{flat, filepath.Join(root, "other", "projects", "x")} {
		if stateHomeFor(malformed) != "" {
			t.Fatalf("stateHomeFor(%q) should be empty", malformed)
		}
	}
	buckets, err := enumerateBuckets(root)
	if err != nil || len(buckets) != 2 {
		t.Fatalf("enumerateBuckets = %v, %v", buckets, err)
	}
	if _, err := enumerateBuckets("["); err == nil {
		t.Fatal("enumerateBuckets accepted malformed glob root")
	}

	parentCases := []struct {
		selector string
		flat     bool
		wantErr  bool
	}{
		{"", false, false}, {"current", false, false}, {"local:parent", false, false},
		{"proj:bbb:parent", false, false}, {"parent", false, false},
		{"bad token", false, true}, {"proj:bbb:parent", true, true},
	}
	for _, tc := range parentCases {
		dir := current
		if tc.flat {
			dir = flat
		}
		_, _, _, err := parentBucketAndID(tc.selector, dir, "current")
		if (err != nil) != tc.wantErr {
			t.Fatalf("parentBucketAndID(%q) err=%v wantErr=%v", tc.selector, err, tc.wantErr)
		}
	}
}

func tccRenderContracts(t *testing.T) {
	t.Helper()
	// Replay every finite renderer scenario so this target retains full rendering
	// coverage even when the mutating byte program selects only one tool scenario.
	for scenario := byte(0); scenario < 13; scenario++ {
		header, entries, rangeSpec, opt := trender_program([]byte{scenario}, "coverage")
		content, meta := renderTranscript(header, entries, rangeSpec, opt)
		if !strings.HasPrefix(content, "# Transcript:") || meta.TurnsTotal != len(entries) {
			t.Fatalf("renderer scenario %d returned invalid envelope", scenario)
		}
	}

	for _, spec := range []string{"", "1", "1-2", "last:2", "start:2", "0", "2-1", "bad", "1-2-3"} {
		_, _, _ = parseRangeErr(spec, 3)
		_, _ = parseRange(spec, 3)
	}
	for _, value := range []any{"text", true, false, float64(2), 2.5, json.Number("7"), nil, []any{1}, map[string]any{"x": 1}} {
		_ = scalarString(value)
	}

	toolArgs := map[string]string{
		"shell":            `{"command":"echo hello"}`,
		"read_file":        `{"file_path":"/tmp/a","offset":2,"limit":3}`,
		"write_file":       `{"file_path":"/tmp/a","content":"body"}`,
		"edit_file":        `{"file_path":"/tmp/a","old_string":"a","new_string":"bb","replace_all":true}`,
		"grep":             `{"pattern":"needle","path":"agent"}`,
		"glob":             `{"pattern":"*.go","path":"agent"}`,
		"web_fetch":        `{"url":"https://example.test/path","question":"why"}`,
		"web_search":       `{"query":"serf"}`,
		"delegate":         `{"task":"inspect","agent_type":"researcher","max_wait_ms":10}`,
		"job_send_message": `{"target":"job-1","message":"hello"}`,
		"delegate_send":    `{"to":"child","message":"hello"}`,
		"use_skill":        `{"skill_name":"testing"}`,
		"unknown":          `{"z":3,"a":"one","b":true,"nested":{"hidden":true}}`,
	}
	for name, raw := range toolArgs {
		if got := toolInputSummary(name, json.RawMessage(raw)); strings.Contains(got, "\n") {
			t.Fatalf("toolInputSummary(%q) returned multiple lines: %q", name, got)
		}
	}
	for _, raw := range []json.RawMessage{nil, json.RawMessage("{"), json.RawMessage(`{"purpose":false}`), json.RawMessage(`{"intent":"why"}`), json.RawMessage(`{"description":"what"}`)} {
		_ = toolPurpose(raw)
		_ = parseArgs(raw)
	}
	_ = pathSegment("")
	_ = pathSegment("agent")
	_ = quoteIfSet("")
	_ = quoteIfSet("x")
	_ = hostOf("")
	_ = hostOf("not a url")
	_ = hostOf("https://example.test/path")
	_ = truncRunes("short", 20)
	_ = truncRunes("longer", 3)

	for _, raw := range []string{"", "text", "1", "{", `{"x":1}`, `[1,true]`, `{"x":1} trailing`} {
		_, _ = prettyJSON(raw)
		_ = hasNonJobResultKeys(raw)
		_ = jobResultMetadata(raw)
	}
	if _, ok := prettyJSONValue(make(chan int)); ok {
		t.Fatal("prettyJSONValue encoded an unsupported value")
	}
	for _, raw := range []string{
		`{"job_id":"j","status":"completed","transcript_ref":"local:c","output":"ok","delivered":true}`,
		`{"status":"failed","reason":"bad","structured_result":{"ok":false}}`,
		`{"status":"completed","extra":"preserved"}`,
		"not-json",
	} {
		_, _ = jobResultBody(raw)
	}

	var b strings.Builder
	writeResultToolMessage(&b, &llm.ToolCallData{Arguments: json.RawMessage(`{"message":"done"}`)})
	writeResultToolMessage(&b, &llm.ToolCallData{Arguments: json.RawMessage(`{"other":1}`)})
	writeResultToolMessage(&b, &llm.ToolCallData{Arguments: json.RawMessage("{")})
	writeToolResultBody(&b, "delegate", nil, false)
	writeToolResultBody(&b, "shell", &llm.ToolResultData{Content: map[string]any{"ok": true}}, false)
	writeFencedBody(&b, strings.Repeat("line\n", 35), false)
	writeFencedBody(&b, "```nested```", true)
	writeCompactNote(&b, "Summary", schema.Turn{})
	writeCompactNote(&b, "Summary", schema.Turn{Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentText, Text: strings.Repeat("x", 140)}}}})

	for _, content := range []string{"", "# Header", "# Header\n\nBody"} {
		_ = spliceAfterHeader(content, "\nmarker\n")
		_, _ = applyHardCap(content)
	}
	_ = spliceWindowLine("# Transcript\n\nbody", readMeta{})
	_ = spliceWindowLine("# Transcript\n\nbody", readMeta{TurnsRendered: 1, TurnsTotal: 3, FirstRendered: 1, LastRendered: 1})
	_ = spliceRangeWarning("# Transcript\n\nbody", "bad range")

	entries := []transcript.Entry{{Kind: "entry", Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call", Name: "shell"}}}}}}, {Kind: "entry", Turn: schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call", Name: "shell", Content: "ok"}}}}}}}
	for _, pin := range []int{-1, 0, 1, 2, 9} {
		_, _, _ = resolvePinnedSpan(entries, pin)
		if pin >= 0 && pin < len(entries) {
			_, _ = owningAssistantSeq(entries, pin)
		}
	}
	_, _ = resultSeqForCall(entries, "missing")
	_, _ = callOwnerSeq(entries, "missing")
	if effectiveResultToolName(renderOpts{}) == "" {
		t.Fatal("default result tool name is empty")
	}
	if got := effectiveResultToolName(renderOpts{resultToolName: "reply"}); got != "reply" {
		t.Fatalf("custom result tool name = %q", got)
	}

	if tools := transcriptTools(nil); len(tools) != 1 {
		t.Fatalf("transcriptTools(nil) = %d tools", len(tools))
	}
	if _, err := readJobTranscript(nil, "job:x", "", ""); err == nil {
		t.Fatal("readJobTranscript accepted nil dependencies")
	}
	if got := renderShellJobTranscript(nil, "", 0, 0); !strings.Contains(got, "# Shell Job") {
		t.Fatalf("nil shell job render = %q", got)
	}
	if got := renderShellJobTranscript(nil, "partial", 10, 3); !strings.Contains(got, "dropped_bytes: 3") {
		t.Fatalf("dropped shell job render = %q", got)
	}
}

func tccWrite(t *testing.T, root, name, content string) string {
	t.Helper()
	path := filepath.Join(root, name+".jsonl")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

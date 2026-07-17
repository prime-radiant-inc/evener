//go:build serffuzz

package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// FuzzTranscriptRenderLookupExact drives finite error and boundary states that
// arbitrary transcript bytes cannot reliably construct.
func FuzzTranscriptRenderLookupExact(f *testing.F) {
	f.Add([]byte("seed"))
	f.Fuzz(func(t *testing.T, data []byte) {
		payload := strings.ToValidUTF8(string(data), "?")
		if len(payload) > 32 {
			payload = payload[:32]
		}
		rleRenderEdges(t, payload)
		rleLookupEdges(t)
	})
}

func rleRenderEdges(t *testing.T, payload string) {
	t.Helper()
	for scenario := byte(0); scenario < 13; scenario++ {
		header, entries, rangeSpec, opt := trender_program([]byte{scenario}, payload)
		_, _ = renderTranscript(header, entries, rangeSpec, opt)
		if opt.fullResultFor != nil {
			trenderAssertPagedExpansion(t, header, entries, rangeSpec, opt)
		}
	}
	for _, tc := range []struct{ name, args string }{
		{"shell", `{"command":"ls","purpose":"inspect"}`},
		{"read_file", `{"file_path":"/tmp/a","offset":1,"limit":2}`},
		{"write_file", `{"file_path":"/tmp/a","content":"x"}`},
		{"edit_file", `{"file_path":"/tmp/a","old_string":"x","new_string":"y","replace_all":true}`},
		{"grep", `{"pattern":"x","path":"/tmp"}`}, {"glob", `{"pattern":"*.go","path":"/tmp"}`},
		{"web_fetch", `{"url":"https://example.com/x","question":"q"}`}, {"web_search", `{"query":"q"}`},
		{"delegate", `{"task":"t","agent_type":"coder","max_wait_ms":3}`},
		{"job_send_message", `{"target":"j","message":"m"}`}, {"delegate_send", `{"to":"j","message":"m"}`},
		{"use_skill", `{"skill_name":"s"}`}, {"unknown", `{"a":1,"b":true,"c":"x","d":null}`},
	} {
		_ = toolInputSummary(tc.name, []byte(tc.args))
	}
	rleRenderContracts(t)
	optWithConfig := renderOpts{}
	optWithConfig.meta.Config.ResultToolName = "reply"
	_ = effectiveResultToolName(optWithConfig)
	_, _, _ = parseRangeErr("start:0", 2)
	_, _, _ = clampRange(0, -1, 2)
	_, _, _ = clampRange(0, 9, 2)
	_, _, _ = parseDashRange("none")

	root := t.TempDir()
	for name, body := range map[string]string{
		"empty":       "",
		"long-header": strings.Repeat("x", transcriptJSONLMaxLineBytes+1),
		"long-body":   "{}\n" + strings.Repeat("x", transcriptJSONLMaxLineBytes+1),
	} {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, _, _, _ = rawLinesForRange(path, 0, 1)
	}
	largePath := filepath.Join(root, "large")
	largeLine := strings.Repeat("x", hardCapChars)
	if err := os.WriteFile(largePath, []byte("{\"kind\":\"header\",\"format_version\":2,\"session_id\":\"large\"}\n{\"kind\":\"entry\",\"x\":\""+largeLine+"\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, _ = rawLinesForRange(largePath, 0, 0)
	_, _, _, _, _ = rawLinesForRange(filepath.Join(root, "missing"), 0, 0)
	unsupportedPath := filepath.Join(root, "unsupported-mixed")
	unsupported := "{\"kind\":\"header\",\"format_version\":2,\"session_id\":\"mixed\"}\n{\"kind\":\"entry\"}\n{\"kind\":\"api_call\"}\n"
	if err := os.WriteFile(unsupportedPath, []byte(unsupported), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, _ = rawLinesForRange(unsupportedPath, 0, 1)
	capPath := filepath.Join(root, "cap")
	capBody := "{\"kind\":\"header\",\"format_version\":2,\"session_id\":\"cap\"}\n{\"kind\":\"entry\",\"x\":\"" + strings.Repeat("x", hardCapChars-100) + "\"}\n{\"kind\":\"entry\",\"x\":\"" + strings.Repeat("y", 200) + "\"}\n"
	if err := os.WriteFile(capPath, []byte(capBody), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, _, _, _ = rawLinesForRange(capPath, 0, 1)

	text := func(kind schema.TurnKind, value string) transcript.Entry {
		return transcript.Entry{Turn: schema.Turn{Kind: kind, Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentText, Text: value}}}}}
	}
	entries := []transcript.Entry{text(schema.TurnUserInput, payload)}
	badPin := 0
	_ = renderOutOfRangePin(entries, 1, 1, renderOpts{fullResultFor: &badPin})
	_, _, _ = resolvePinnedSpan(entries, 0)
	_, _ = owningAssistantSeq(entries, 0)

	pinned := 0
	_ = budgetedStart(transcript.Header{}, []transcript.Entry{
		text(schema.TurnAssistant, strings.Repeat("x", convBudgetChars+1)),
		text(schema.TurnUserInput, "tail"),
	}, 0, 1, renderOpts{fullResultFor: &pinned})
	_, _ = applyHardCap(strings.Repeat("€", hardCapChars))

	idx := resultIndex{byCallID: map[string]pairedResult{
		"b": {ownerSeq: 2, result: &llm.ToolResultData{Name: "b"}},
		"a": {ownerSeq: 1, result: &llm.ToolResultData{Name: "a"}},
		"c": {ownerSeq: 1, result: &llm.ToolResultData{Name: "c"}},
	}, consumed: map[string]bool{}}
	var b strings.Builder
	writeUnpairedResults(&b, &idx, renderOpts{})
	_, _ = jobResultBody(`{"job_id":"j","status":"completed"}`)
	_, _ = jobResultBody(`{"transcript_ref":"local:child","status":"completed"}`)

	oldEncode := encodeTranscriptJSON
	encodeTranscriptJSON = func(any) (string, error) { return "", errors.New("encode") }
	t.Cleanup(func() { encodeTranscriptJSON = oldEncode })
	_, _ = prettyJSONValue(map[string]any{"x": 1})
	_, _ = prettyJSON(`{"x":1}`)
	_, _ = jobResultBody(`{"job_id":"j","structured_result":{"x":1}}`)
	_ = unknownToolSummary(nil)
}

func rleLookupEdges(t *testing.T) {
	t.Helper()
	rleLookupContracts(t)
	base := t.TempDir()
	current := filepath.Join(base, "serf", "projects", trenderCurrentProject)
	trender_makeTranscript(t, current, trenderCurrentSession)
	_, _, _ = resolveTranscript("local:", current, trenderCurrentSession)

	oldGlob := transcriptBucketGlob
	transcriptBucketGlob = func(string) ([]string, error) { return nil, errors.New("glob") }
	_, _, _ = resolveTranscript(trenderMissingSession, current, trenderCurrentSession)
	transcriptBucketGlob = oldGlob
	t.Cleanup(func() { transcriptBucketGlob = oldGlob })
	_, _, _, _ = parentBucketAndID("local:", current, trenderCurrentSession)
}

func rleRenderContracts(t *testing.T) {
	t.Helper()
	for _, spec := range []string{"", "1", "1-2", "last:2", "start:2", "0", "2-1", "bad", "1-2-3"} {
		_, _, _ = parseRangeErr(spec, 3)
		_, _ = parseRange(spec, 3)
	}
	for _, value := range []any{"text", true, false, float64(2), 2.5, json.Number("7"), nil, []any{1}, map[string]any{"x": 1}} {
		_ = scalarString(value)
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

	var b strings.Builder
	writeResultToolMessage(&b, &llm.ToolCallData{Arguments: json.RawMessage(`{"message":"done"}`)})
	writeResultToolMessage(&b, &llm.ToolCallData{Arguments: json.RawMessage(`{"other":1}`)})
	writeResultToolMessage(&b, &llm.ToolCallData{Arguments: json.RawMessage("{")})
	writeToolResultBody(&b, "delegate", nil, false)
	writeToolResultBody(&b, "shell", &llm.ToolResultData{Content: map[string]any{"ok": true}}, false)
	writeFencedBody(&b, strings.Repeat("line\n", 35), false)
	writeFencedBody(&b, "```nested```", true)
	writeCompactNote(&b, "Summary", schema.Turn{}, false)
	writeCompactNote(&b, "Summary", schema.Turn{Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentText, Text: strings.Repeat("x", 140)}}}}, false)

	for _, content := range []string{"", "# Header", "# Header\n\nBody"} {
		_ = spliceAfterHeader(content, "\nmarker\n")
		_, _ = applyHardCap(content)
	}
	_ = spliceWindowLine("# Transcript\n\nbody", readMeta{})
	_ = spliceWindowLine("# Transcript\n\nbody", readMeta{TurnsRendered: 1, TurnsTotal: 3, FirstRendered: 1, LastRendered: 1})
	_ = spliceRangeWarning("# Transcript\n\nbody", "bad range")

	entries := []transcript.Entry{
		{Kind: "entry", Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call", Name: "shell"}}}}}},
		{Kind: "entry", Turn: schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call", Name: "shell", Content: "ok"}}}}}},
	}
	for _, pin := range []int{-1, 0, 1, 2, 9} {
		_, _, _ = resolvePinnedSpan(entries, pin)
		if pin >= 0 && pin < len(entries) {
			_, _ = owningAssistantSeq(entries, pin)
		}
	}
	_, _ = resultSeqForCall(entries, "missing")
	_, _ = callOwnerSeq(entries, "missing")

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

func rleLookupContracts(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	current := filepath.Join(root, "serf", "projects", trenderCurrentProject)
	other := filepath.Join(root, "serf", "projects", trenderOtherProject)
	trender_makeTranscript(t, current, trenderCurrentSession)
	trender_makeTranscript(t, current, trenderLocalSession)
	trender_makeTranscript(t, current, trenderSharedSession)
	trender_makeTranscript(t, other, trenderRemoteSession)
	trender_makeTranscript(t, other, trenderSharedSession)
	if err := os.WriteFile(filepath.Join(root, "serf", "projects", "not-a-dir"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		selector string
		wantErr  bool
	}{
		{"", false}, {"current", false}, {"local:" + trenderLocalSession, false},
		{"proj:" + trenderOtherProject + ":" + trenderRemoteSession, false}, {"local:" + trenderMissingSession, true},
		{"proj:" + trenderOtherProject + ":" + trenderMissingSession, true}, {trenderLocalSession, false}, {trenderRemoteSession, false},
		{trenderSharedSession, true}, {trenderMissingSession, true}, {"../bad", true}, {"bad token", true},
	}
	for _, tc := range cases {
		_, _, err := resolveTranscript(tc.selector, current, trenderCurrentSession)
		if (err != nil) != tc.wantErr {
			t.Fatalf("resolveTranscript(%q) err=%v wantErr=%v", tc.selector, err, tc.wantErr)
		}
	}

	deps := &toolDeps{stateDir: current, sessionID: trenderCurrentSession, currentMeta: func() schema.SessionMeta { return schema.SessionMeta{ID: trenderCurrentSession, Name: "live"} }}
	if meta := resolvedSessionMeta(deps, transcriptPath(current, trenderCurrentSession), encodeRef("", trenderCurrentSession)); meta.Name != "live" {
		t.Fatalf("live resolved metadata = %#v", meta)
	}
	if meta := resolvedSessionMeta(deps, transcriptPath(current, trenderMissingSession), encodeRef("", trenderMissingSession)); meta.ID != trenderMissingSession {
		t.Fatalf("fallback resolved metadata = %#v", meta)
	}
	for _, format := range []string{"", formatMarkdown, formatOutline, formatJSONL} {
		if _, err := execReadSessionTranscript(deps, map[string]any{"transcript_ref": encodeRef("", trenderCurrentSession), "format": format}); err != nil {
			t.Fatalf("execReadSessionTranscript(%q): %v", format, err)
		}
	}
	if _, err := execReadSessionTranscript(deps, map[string]any{"transcript_ref": encodeRef("", trenderCurrentSession), "format": "future"}); err == nil {
		t.Fatal("execReadSessionTranscript accepted unknown format")
	}

	flat := filepath.Join(root, "flat")
	if _, _, err := resolveTranscript("proj:"+trenderOtherProject+":"+trenderRemoteSession, flat, trenderCurrentSession); err == nil {
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

	for _, tc := range []struct {
		selector string
		flat     bool
		wantErr  bool
	}{
		{"", false, false}, {"current", false, false}, {"local:" + trenderParentSession, false, false},
		{"proj:" + trenderOtherProject + ":" + trenderParentSession, false, false}, {trenderParentSession, false, false},
		{"bad token", false, true}, {"proj:" + trenderOtherProject + ":" + trenderParentSession, true, true},
	} {
		dir := current
		if tc.flat {
			dir = flat
		}
		_, _, _, err := parentBucketAndID(tc.selector, dir, trenderCurrentSession)
		if (err != nil) != tc.wantErr {
			t.Fatalf("parentBucketAndID(%q) err=%v wantErr=%v", tc.selector, err, tc.wantErr)
		}
	}
}

//go:build serffuzz

package agent

import (
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
			_ = renderExactTurnExpansion(entries, *opt.fullResultFor, opt)
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
	tccRenderContracts(t)
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
	tccLookupContracts(t)
	base := t.TempDir()
	current := filepath.Join(base, "serf", "projects", "aaa")
	trender_makeTranscript(t, current, "here")
	_, _, _ = resolveTranscript("local:", current, "cur")

	oldGlob := transcriptBucketGlob
	transcriptBucketGlob = func(string) ([]string, error) { return nil, errors.New("glob") }
	_, _, _ = resolveTranscript("missing", current, "cur")
	transcriptBucketGlob = oldGlob
	t.Cleanup(func() { transcriptBucketGlob = oldGlob })
	_, _, _, _ = parentBucketAndID("local:", current, "cur")
}

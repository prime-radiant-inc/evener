//go:build serffuzz

package agent

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

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

func TestRLETranscriptRenderScenariosAssertSemantics(t *testing.T) {
	rleAssertRenderScenarios(t, "review")
}

func rleAssertRenderScenarios(t *testing.T, payload string) {
	t.Helper()
	for scenario := byte(0); scenario < 13; scenario++ {
		header, entries, rangeSpec, opt := trender_program([]byte{scenario}, payload)
		got, meta := renderTranscript(header, entries, rangeSpec, opt)
		if !strings.HasPrefix(got, "# Transcript: ") {
			t.Fatalf("scenario %d omitted the transcript header: %q", scenario, got)
		}
		if meta.TurnsTotal != len(entries) || meta.TurnsRendered+meta.ElidedTurns != len(entries) {
			t.Fatalf("scenario %d metadata = %#v for %d entries", scenario, meta, len(entries))
		}
		if !utf8.ValidString(got) || len([]rune(got)) > hardCapChars {
			t.Fatalf("scenario %d rendered invalid or oversized markdown", scenario)
		}
		if opt.fullResultFor != nil {
			trenderAssertPagedExpansion(t, header, entries, rangeSpec, opt)
		}

		switch scenario {
		case 1:
			if !strings.Contains(got, "assistant "+payload) || !strings.Contains(got, "[ok] `shell`") || !strings.Contains(got, "Tool results without a shown call") {
				t.Fatalf("scenario 1 lost assistant text, paired tool status, or orphan result: %q", got)
			}
		case 2:
			if !strings.Contains(got, "custom result") || !strings.Contains(got, "[ok] `read_file`") {
				t.Fatalf("scenario 2 lost result-tool text or ID-paired result: %q", got)
			}
		case 3:
			if !strings.Contains(got, "job_id=job-1") || !strings.Contains(got, "transcript_ref=local:child") {
				t.Fatalf("scenario 3 lost job metadata: %q", got)
			}
		case 7:
			if strings.Count(got, "full line") < 35 {
				t.Fatalf("scenario 7 condensed its pinned result: %q", got)
			}
		case 8:
			if !strings.Contains(got, "pinned turn 0") || strings.Count(got, "outside line") < 35 {
				t.Fatalf("scenario 8 lost its out-of-range owned result: %q", got)
			}
		case 9:
			if !meta.Truncated || !strings.Contains(got, "content truncated at the 200,000-character hard cap") {
				t.Fatalf("scenario 9 did not report hard-cap truncation: %#v", meta)
			}
		}
	}
}

func rleRenderEdges(t *testing.T, payload string) {
	t.Helper()
	rleAssertRenderScenarios(t, payload)
	for _, tc := range []struct{ name, args, want string }{
		{"shell", `{"command":"ls","purpose":"inspect"}`, "ls"},
		{"read_file", `{"file_path":"/tmp/a","offset":1,"limit":2}`, "/tmp/a (offset 1, limit 2)"},
		{"write_file", `{"file_path":"/tmp/a","content":"x"}`, "/tmp/a (1 bytes)"},
		{"edit_file", `{"file_path":"/tmp/a","old_string":"x","new_string":"y","replace_all":true}`, "/tmp/a (replace 1→1 chars, all)"},
		{"grep", `{"pattern":"x","path":"/tmp"}`, "`x` in /tmp"},
		{"glob", `{"pattern":"*.go","path":"/tmp"}`, "`*.go` in /tmp"},
		{"web_fetch", `{"url":"https://example.com/x","question":"q"}`, "example.com `q`"},
		{"web_search", `{"query":"q"}`, "`q`"},
		{"delegate", `{"task":"t","agent_type":"coder","max_wait_ms":3}`, "t type=coder max_wait_ms=3"},
		{"job_send_message", `{"target":"j","message":"m"}`, "j `m`"},
		{"delegate_send", `{"to":"j","message":"m"}`, "j `m`"},
		{"use_skill", `{"skill_name":"s"}`, "s"},
		{"unknown", `{"a":1,"b":true,"c":"x","d":null}`, "a=1 b=true c=x"},
	} {
		if got := toolInputSummary(tc.name, []byte(tc.args)); got != tc.want {
			t.Fatalf("toolInputSummary(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
	rleRenderContracts(t)
	optWithConfig := renderOpts{}
	optWithConfig.meta.Config.ResultToolName = "reply"
	if got := effectiveResultToolName(optWithConfig); got != "reply" {
		t.Fatalf("effective result tool = %q, want reply", got)
	}
	if _, _, err := parseRangeErr("start:0", 2); !errors.Is(err, errBadRange) {
		t.Fatalf("start:0 error = %v, want malformed range", err)
	}
	if lo, hi, _ := clampRange(0, -1, 2); lo != 0 || hi != 0 {
		t.Fatalf("clampRange(0,-1,2) = %d,%d", lo, hi)
	}
	if lo, hi, _ := clampRange(0, 9, 2); lo != 0 || hi != 1 {
		t.Fatalf("clampRange(0,9,2) = %d,%d", lo, hi)
	}
	if _, _, ok := parseDashRange("none"); ok {
		t.Fatal("parseDashRange accepted a dashless value")
	}

	root := t.TempDir()
	for name, body := range map[string]string{
		"empty":       "",
		"long-header": strings.Repeat("x", transcriptJSONLMaxLineBytes+1),
		"long-body":   `{"kind":"header","format_version":2,"session_id":"long-body"}` + "\n" + strings.Repeat("x", transcriptJSONLMaxLineBytes+1),
	} {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, _, _, err := rawLinesForRange(path, 0, 1); err == nil {
			t.Fatalf("rawLinesForRange accepted %s fixture", name)
		}
	}
	largePath := filepath.Join(root, "large")
	largeLine := strings.Repeat("x", hardCapChars)
	if err := os.WriteFile(largePath, []byte("{\"kind\":\"header\",\"format_version\":2,\"session_id\":\"large\"}\n{\"kind\":\"entry\",\"x\":\""+largeLine+"\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	largeContent, largeLines, largeSkipped, largeTruncated, err := rawLinesForRange(largePath, 0, 0)
	if err != nil || !largeTruncated || largeLines != 1 || largeSkipped != 0 || !strings.Contains(largeContent, `"session_id":"large"`) {
		t.Fatalf("large raw range = lines %d skipped %d truncated %v err %v", largeLines, largeSkipped, largeTruncated, err)
	}
	if _, _, _, _, err := rawLinesForRange(filepath.Join(root, "missing"), 0, 0); err == nil {
		t.Fatal("rawLinesForRange accepted a missing transcript")
	}
	unsupportedPath := filepath.Join(root, "unsupported-mixed")
	unsupported := "{\"kind\":\"header\",\"format_version\":2,\"session_id\":\"mixed\"}\n{\"kind\":\"entry\"}\n{\"kind\":\"api_call\"}\n"
	if err := os.WriteFile(unsupportedPath, []byte(unsupported), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := rawLinesForRange(unsupportedPath, 0, 1); err == nil {
		t.Fatal("rawLinesForRange accepted an API-call record in semantic JSONL")
	}
	capPath := filepath.Join(root, "cap")
	capBody := "{\"kind\":\"header\",\"format_version\":2,\"session_id\":\"cap\"}\n{\"kind\":\"entry\",\"x\":\"" + strings.Repeat("x", hardCapChars-100) + "\"}\n{\"kind\":\"entry\",\"x\":\"" + strings.Repeat("y", 200) + "\"}\n"
	if err := os.WriteFile(capPath, []byte(capBody), 0o600); err != nil {
		t.Fatal(err)
	}
	capContent, capLines, capSkipped, capTruncated, err := rawLinesForRange(capPath, 0, 1)
	if err != nil || !capTruncated || capLines != 2 || capSkipped != 0 || strings.Contains(capContent, strings.Repeat("y", 20)) {
		t.Fatalf("capped raw range = lines %d skipped %d truncated %v err %v", capLines, capSkipped, capTruncated, err)
	}

	text := func(kind schema.TurnKind, value string) transcript.Entry {
		return transcript.Entry{Turn: schema.Turn{Kind: kind, Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentText, Text: value}}}}}
	}
	entries := []transcript.Entry{text(schema.TurnUserInput, payload)}
	badPin := 0
	if got := renderOutOfRangePin(entries, 1, 1, renderOpts{fullResultFor: &badPin}); !strings.Contains(got, "pinned turn 0") || !strings.Contains(got, payload) {
		t.Fatalf("out-of-range user pin = %q", got)
	}
	if first, last, ok := resolvePinnedSpan(entries, 0); !ok || first != 0 || last != 0 {
		t.Fatalf("user pinned span = %d,%d,%v", first, last, ok)
	}
	if owner, ok := owningAssistantSeq(entries, 0); ok {
		t.Fatalf("user turn unexpectedly owned by assistant %d", owner)
	}

	pinned := 0
	if first := budgetedStart(transcript.Header{}, []transcript.Entry{
		text(schema.TurnAssistant, strings.Repeat("x", convBudgetChars+1)),
		text(schema.TurnUserInput, "tail"),
	}, 0, 1, renderOpts{fullResultFor: &pinned}); first != 0 {
		t.Fatalf("budgetedStart dropped pinned turn: %d", first)
	}
	unicodeAtCap := strings.Repeat("€", hardCapChars)
	if truncated, got := applyHardCap(unicodeAtCap); truncated || got != unicodeAtCap {
		t.Fatalf("rune-exact hard-cap input changed: truncated=%v runes=%d", truncated, len([]rune(got)))
	}

	idx := resultIndex{byCallID: map[string]pairedResult{
		"b": {ownerSeq: 2, result: &llm.ToolResultData{Name: "b"}},
		"a": {ownerSeq: 1, result: &llm.ToolResultData{Name: "a"}},
		"c": {ownerSeq: 1, result: &llm.ToolResultData{Name: "c"}},
	}, consumed: map[string]bool{}}
	var b strings.Builder
	writeUnpairedResults(&b, &idx, renderOpts{})
	resultText := b.String()
	aPos, cPos, bPos := strings.Index(resultText, "`a`"), strings.Index(resultText, "`c`"), strings.Index(resultText, "`b`")
	if aPos < 0 || cPos <= aPos || bPos <= cPos {
		t.Fatalf("unpaired results are not ordered by owner then call ID: %q", resultText)
	}
	if got, ok := jobResultBody(`{"job_id":"j","status":"completed"}`); !ok || !strings.Contains(got, "job_id=j status=completed") || !strings.Contains(got, "transcript_ref=(none)") {
		t.Fatalf("job result without ref = %q,%v", got, ok)
	}
	if got, ok := jobResultBody(`{"transcript_ref":"local:child","status":"completed"}`); !ok || !strings.Contains(got, "job_id=(none)") || !strings.Contains(got, "transcript_ref=local:child") {
		t.Fatalf("job result without ID = %q,%v", got, ok)
	}
	rleAssertEncodeFailures(t)
	if got := unknownToolSummary(nil); got != "" {
		t.Fatalf("unknownToolSummary(nil) = %q", got)
	}
}

func rleAssertEncodeFailures(t *testing.T) {
	t.Helper()
	oldEncode := encodeTranscriptJSON
	encodeTranscriptJSON = func(any) (string, error) { return "", errors.New("encode") }
	defer func() { encodeTranscriptJSON = oldEncode }()
	if got, ok := prettyJSONValue(map[string]any{"x": 1}); ok || got != "" {
		t.Fatalf("prettyJSONValue encode failure = %q,%v", got, ok)
	}
	if got, ok := prettyJSON(`{"x":1}`); ok || got != "" {
		t.Fatalf("prettyJSON encode failure = %q,%v", got, ok)
	}
	if got, ok := jobResultBody(`{"job_id":"j","structured_result":{"x":1}}`); !ok || !strings.Contains(got, "structured_result:\nmap[x:1]") {
		t.Fatalf("job structured-result fallback = %q,%v", got, ok)
	}
}

func rleLookupEdges(t *testing.T) {
	t.Helper()
	rleLookupContracts(t)
	base := t.TempDir()
	current := filepath.Join(base, "serf", "projects", trenderCurrentProject)
	trender_makeTranscript(t, current, trenderCurrentSession)
	if _, _, err := resolveTranscript("local:", current, trenderCurrentSession); err == nil {
		t.Fatal("resolveTranscript accepted an empty local session ID")
	}

	oldGlob := transcriptBucketGlob
	globErr := errors.New("glob")
	transcriptBucketGlob = func(string) ([]string, error) { return nil, globErr }
	_, _, err := resolveTranscript(trenderMissingSession, current, trenderCurrentSession)
	transcriptBucketGlob = oldGlob
	if !errors.Is(err, globErr) {
		t.Fatalf("bucket glob error = %v, want %v", err, globErr)
	}
	if _, _, _, err := parentBucketAndID("local:", current, trenderCurrentSession); err == nil {
		t.Fatal("parentBucketAndID accepted an empty local session ID")
	}
}

func rleRenderContracts(t *testing.T) {
	t.Helper()
	ranges := []struct {
		spec               string
		wantStart, wantEnd int
		strictOK           bool
	}{
		{"", 0, 2, true}, {"1", 0, 2, false}, {"1-2", 1, 2, true},
		{"last:2", 1, 2, true}, {"start:2", 0, 1, true}, {"0", 0, 2, false},
		{"2-1", 2, 1, true}, {"bad", 0, 2, false}, {"1-2-3", 0, 2, false},
	}
	for _, tc := range ranges {
		start, end, err := parseRangeErr(tc.spec, 3)
		if tc.strictOK {
			if err != nil || start != tc.wantStart || end != tc.wantEnd {
				t.Fatalf("parseRangeErr(%q) = %d,%d,%v", tc.spec, start, end, err)
			}
		} else if !errors.Is(err, errBadRange) {
			t.Fatalf("parseRangeErr(%q) error = %v, want malformed range", tc.spec, err)
		}
		if fallbackStart, fallbackEnd := parseRange(tc.spec, 3); fallbackStart != tc.wantStart || fallbackEnd != tc.wantEnd {
			t.Fatalf("parseRange(%q) = %d,%d, want %d,%d", tc.spec, fallbackStart, fallbackEnd, tc.wantStart, tc.wantEnd)
		}
	}

	for _, tc := range []struct {
		value any
		want  string
	}{
		{"text", "text"}, {true, "true"}, {false, "false"}, {float64(2), "2"}, {2.5, "2.5"}, {json.Number("7"), "7"},
		{nil, ""}, {[]any{1}, ""}, {map[string]any{"x": 1}, ""},
	} {
		if got := scalarString(tc.value); got != tc.want {
			t.Fatalf("scalarString(%v) = %q, want %q", tc.value, got, tc.want)
		}
	}

	for _, tc := range []struct {
		raw         json.RawMessage
		wantPurpose string
		wantParsed  bool
	}{
		{nil, "", false}, {json.RawMessage("{"), "", false},
		{json.RawMessage(`{"purpose":false}`), "false", true},
		{json.RawMessage(`{"intent":"why"}`), "why", true},
		{json.RawMessage(`{"description":"what"}`), "what", true},
	} {
		if got := toolPurpose(tc.raw); got != tc.wantPurpose {
			t.Fatalf("toolPurpose(%q) = %q, want %q", tc.raw, got, tc.wantPurpose)
		}
		if parsed := parseArgs(tc.raw); (parsed != nil) != tc.wantParsed {
			t.Fatalf("parseArgs(%q) parsed=%v, wantParsed=%v", tc.raw, parsed, tc.wantParsed)
		}
	}

	for _, tc := range []struct{ name, got, want string }{
		{"empty path", pathSegment(""), ""}, {"path", pathSegment("agent"), "in agent"},
		{"empty quote", quoteIfSet(""), ""}, {"quote", quoteIfSet("x"), "`x`"},
		{"empty host", hostOf(""), ""}, {"raw host", hostOf("not a url"), "not a url"},
		{"URL host", hostOf("https://example.test/path"), "example.test"},
		{"short truncation", truncRunes("short", 20), "short"}, {"long truncation", truncRunes("longer", 3), "lon…"},
	} {
		if tc.got != tc.want {
			t.Fatalf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}

	for _, tc := range []struct {
		raw      string
		wantJSON bool
	}{
		{"", false}, {"text", false}, {"1", false}, {"{", false},
		{`{"x":1}`, true}, {`[1,true]`, true}, {`{"x":1} trailing`, false},
	} {
		pretty, ok := prettyJSON(tc.raw)
		if ok != tc.wantJSON {
			t.Fatalf("prettyJSON(%q) = %q,%v", tc.raw, pretty, ok)
		}
		if ok && !json.Valid([]byte(pretty)) {
			t.Fatalf("prettyJSON(%q) returned invalid JSON: %q", tc.raw, pretty)
		}
	}
	if !hasNonJobResultKeys(`{"x":1}`) || hasNonJobResultKeys(`{"job_id":"j","status":"done"}`) {
		t.Fatal("job-result key classification is incorrect")
	}
	if got := jobResultMetadata(`{"started_job_id":"j","delivered":true,"structured_result_reason":{"ignored":true}}`); got != "started_job_id=j delivered=true" {
		t.Fatalf("jobResultMetadata = %q", got)
	}

	var resultMessage strings.Builder
	writeResultToolMessage(&resultMessage, &llm.ToolCallData{Arguments: json.RawMessage(`{"message":"done"}`)})
	writeResultToolMessage(&resultMessage, &llm.ToolCallData{Arguments: json.RawMessage(`{"other":1}`)})
	writeResultToolMessage(&resultMessage, &llm.ToolCallData{Arguments: json.RawMessage("{")})
	if got := resultMessage.String(); got != "done\n{\"other\":1}\n{\n" {
		t.Fatalf("result-tool fallbacks = %q", got)
	}

	var resultBodies strings.Builder
	writeToolResultBody(&resultBodies, "delegate", nil, false)
	writeToolResultBody(&resultBodies, "shell", &llm.ToolResultData{Content: map[string]any{"ok": true}}, false)
	if got := resultBodies.String(); !strings.Contains(got, "```\n") || !strings.Contains(got, "map[ok:true]") {
		t.Fatalf("tool result fallbacks = %q", got)
	}
	var longFence strings.Builder
	writeFencedBody(&longFence, strings.Repeat("line\n", 35), false)
	if !strings.Contains(longFence.String(), "[5 lines elided]") {
		t.Fatalf("long fenced body lacks exact elision: %q", longFence.String())
	}
	var nestedFence strings.Builder
	writeFencedBody(&nestedFence, "```nested```", true)
	if !strings.Contains(nestedFence.String(), "````") {
		t.Fatalf("nested fenced body did not lengthen its fence: %q", nestedFence.String())
	}
	var notes strings.Builder
	writeCompactNote(&notes, "Summary", schema.Turn{}, false)
	writeCompactNote(&notes, "Summary", schema.Turn{Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentText, Text: strings.Repeat("x", 140)}}}}, false)
	if got := notes.String(); !strings.HasPrefix(got, "> [Summary]\n") || !strings.Contains(got, strings.Repeat("x", compactNoteMaxLen)+"…") {
		t.Fatalf("compact summary notes = %q", got)
	}

	content := "# Transcript\n\n## Turn 1 — User\nbody"
	spliced := spliceAfterHeader(content, "\nmarker\n")
	if marker, turn := strings.Index(spliced, "marker"), strings.Index(spliced, "## Turn 1"); marker < 0 || marker > turn {
		t.Fatalf("marker was not placed before the first turn: %q", spliced)
	}
	if truncated, got := applyHardCap(content); truncated || got != content {
		t.Fatalf("short content changed under hard cap: %q,%v", got, truncated)
	}
	if got := spliceWindowLine(content, readMeta{}); got != content {
		t.Fatalf("empty window metadata changed content: %q", got)
	}
	if got := spliceWindowLine(content, readMeta{TurnsRendered: 1, TurnsTotal: 3, FirstRendered: 1, LastRendered: 1}); !strings.Contains(got, "Showing turns 1–1 of 3") {
		t.Fatalf("window metadata was not rendered: %q", got)
	}
	if got := spliceRangeWarning(content, "bad range"); !strings.Contains(got, "> [range warning] bad range") {
		t.Fatalf("range warning was not rendered: %q", got)
	}

	entries := []transcript.Entry{
		{Kind: "entry", Turn: schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call", Name: "shell"}}}}}},
		{Kind: "entry", Turn: schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call", Name: "shell", Content: "ok"}}}}}},
	}
	for _, pin := range []int{-1, 2, 9} {
		if _, _, ok := resolvePinnedSpan(entries, pin); ok {
			t.Fatalf("resolvePinnedSpan accepted pin %d", pin)
		}
	}
	for _, pin := range []int{0, 1} {
		if first, last, ok := resolvePinnedSpan(entries, pin); !ok || first != 0 || last != 1 {
			t.Fatalf("resolvePinnedSpan(%d) = %d,%d,%v", pin, first, last, ok)
		}
		if owner, ok := owningAssistantSeq(entries, pin); !ok || owner != 0 {
			t.Fatalf("owningAssistantSeq(%d) = %d,%v", pin, owner, ok)
		}
	}
	if seq, ok := resultSeqForCall(entries, "call"); !ok || seq != 1 {
		t.Fatalf("resultSeqForCall(call) = %d,%v", seq, ok)
	}
	if seq, ok := callOwnerSeq(entries, "call"); !ok || seq != 0 {
		t.Fatalf("callOwnerSeq(call) = %d,%v", seq, ok)
	}
	if _, ok := resultSeqForCall(entries, "missing"); ok {
		t.Fatal("resultSeqForCall found a missing call")
	}
	if _, ok := callOwnerSeq(entries, "missing"); ok {
		t.Fatal("callOwnerSeq found a missing call")
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
		selector          string
		wantPath, wantRef string
		wantErr           bool
	}{
		{"", transcriptPath(current, trenderCurrentSession), encodeRef("", trenderCurrentSession), false},
		{"current", transcriptPath(current, trenderCurrentSession), encodeRef("", trenderCurrentSession), false},
		{"local:" + trenderLocalSession, transcriptPath(current, trenderLocalSession), encodeRef("", trenderLocalSession), false},
		{"proj:" + trenderOtherProject + ":" + trenderRemoteSession, transcriptPath(other, trenderRemoteSession), encodeRef(trenderOtherProject, trenderRemoteSession), false},
		{"local:" + trenderMissingSession, "", "", true},
		{"proj:" + trenderOtherProject + ":" + trenderMissingSession, "", "", true},
		{trenderLocalSession, transcriptPath(current, trenderLocalSession), encodeRef("", trenderLocalSession), false},
		{trenderRemoteSession, transcriptPath(other, trenderRemoteSession), encodeRef(trenderOtherProject, trenderRemoteSession), false},
		{trenderSharedSession, "", "", true}, {trenderMissingSession, "", "", true}, {"../bad", "", "", true}, {"bad token", "", "", true},
	}
	for _, tc := range cases {
		path, ref, err := resolveTranscript(tc.selector, current, trenderCurrentSession)
		if (err != nil) != tc.wantErr {
			t.Fatalf("resolveTranscript(%q) err=%v wantErr=%v", tc.selector, err, tc.wantErr)
		}
		if err == nil && (path != tc.wantPath || ref != tc.wantRef) {
			t.Fatalf("resolveTranscript(%q) = %q,%q, want %q,%q", tc.selector, path, ref, tc.wantPath, tc.wantRef)
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
		got, err := execReadSessionTranscript(deps, map[string]any{"transcript_ref": encodeRef("", trenderCurrentSession), "format": format})
		if err != nil {
			t.Fatalf("execReadSessionTranscript(%q): %v", format, err)
		}
		rleAssertReadEnvelope(t, got, format, encodeRef("", trenderCurrentSession))
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
		selector                      string
		flat                          bool
		wantBucket, wantID, wantScope string
		wantErr                       bool
	}{
		{"", false, current, trenderCurrentSession, scopeCurrentProject, false},
		{"current", false, current, trenderCurrentSession, scopeCurrentProject, false},
		{"local:" + trenderParentSession, false, current, trenderParentSession, scopeCurrentProject, false},
		{"proj:" + trenderOtherProject + ":" + trenderParentSession, false, other, trenderParentSession, scopeAllProjects, false},
		{trenderParentSession, false, current, trenderParentSession, scopeCurrentProject, false},
		{"bad token", false, "", "", "", true},
		{"proj:" + trenderOtherProject + ":" + trenderParentSession, true, "", "", "", true},
	} {
		dir := current
		if tc.flat {
			dir = flat
		}
		bucket, id, scope, err := parentBucketAndID(tc.selector, dir, trenderCurrentSession)
		if (err != nil) != tc.wantErr {
			t.Fatalf("parentBucketAndID(%q) err=%v wantErr=%v", tc.selector, err, tc.wantErr)
		}
		if err == nil && (bucket != tc.wantBucket || id != tc.wantID || scope != tc.wantScope) {
			t.Fatalf("parentBucketAndID(%q) = %q,%q,%q, want %q,%q,%q", tc.selector, bucket, id, scope, tc.wantBucket, tc.wantID, tc.wantScope)
		}
	}
}

func rleAssertReadEnvelope(t *testing.T, got any, requestedFormat, wantRef string) {
	t.Helper()
	if requestedFormat == "" {
		requestedFormat = formatMarkdown
	}
	switch requestedFormat {
	case formatMarkdown:
		envelope, ok := got.(readMarkdownEnvelope)
		if !ok || envelope.TranscriptRef != wantRef || envelope.Format != formatMarkdown || envelope.ContentType != "text/markdown" || envelope.Meta.TurnsTotal != 0 || !strings.HasPrefix(envelope.Content, "# Transcript: ") {
			t.Fatalf("markdown envelope = %#v", got)
		}
	case formatOutline:
		envelope, ok := got.(readOutlineEnvelope)
		if !ok || envelope.TranscriptRef != wantRef || envelope.Format != formatOutline || envelope.TurnsTotal != 0 || envelope.Hint == "" {
			t.Fatalf("outline envelope = %#v", got)
		}
	case formatJSONL:
		envelope, ok := got.(readRawEnvelope)
		if !ok || envelope.TranscriptRef != wantRef || envelope.Format != formatJSONL || envelope.ContentType != "application/x-ndjson" || envelope.Meta.LinesReturned != 1 || !strings.Contains(envelope.Content, `"format_version":2`) {
			t.Fatalf("JSONL envelope = %#v", got)
		}
	default:
		t.Fatalf("unexpected requested format %q", requestedFormat)
	}
}

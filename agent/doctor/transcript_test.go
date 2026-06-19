package doctor

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

func assistantText(s string) llm.ContentPart {
	return llm.ContentPart{Kind: llm.ContentText, Text: s}
}

func toolCall(name, args string) llm.ContentPart {
	return llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
		ID: "tc-" + name, Name: name, Arguments: json.RawMessage(args)}}
}

// writeRichSession writes a real transcript (header + turns + api_calls) plus a
// meta file, via serf's own transcript.Writer so the bytes match production.
func writeRichSession(t *testing.T, bucket, sid string, turns []schema.Turn, apiCalls []transcript.APICall, meta schema.SessionMeta) {
	t.Helper()
	path := filepath.Join(bucket, "sessions", sid+".transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range turns {
		if err := w.Append(turn); err != nil {
			t.Fatal(err)
		}
	}
	for _, ac := range apiCalls {
		if err := w.AppendAPICall(ac); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	meta.ID = sid
	if err := schema.SaveSessionMeta(bucket, meta); err != nil {
		t.Fatal(err)
	}
}

func countFixture(t *testing.T) (base, sid string) {
	t.Helper()
	base = t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid = sidA
	turns := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{assistantText("please read the file")}}),
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			assistantText("I'll read it. Note: I will not call delegate_send myself."),
			toolCall("read_file", `{"path":"x"}`),
		}}),
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			toolCall("communicate", `{"message":"done"}`),
		}}),
	}
	apiCalls := []transcript.APICall{
		{SystemPrompt: "available tools: read_file, delegate_send. policy: do not call delegate_send."},
	}
	writeRichSession(t, bucket, sid, turns, apiCalls, schema.SessionMeta{})
	return base, sid
}

func TestCount_StructuralVsMentions(t *testing.T) {
	base, sid := countFixture(t)

	rf, err := Count(base, sid, "read_file")
	if err != nil {
		t.Fatal(err)
	}
	if rf.Calls != 1 {
		t.Errorf("read_file calls = %d, want 1", rf.Calls)
	}

	ds, err := Count(base, sid, "delegate_send")
	if err != nil {
		t.Fatal(err)
	}
	if ds.Calls != 0 {
		t.Errorf("delegate_send calls = %d, want 0 (it is only mentioned, never invoked)", ds.Calls)
	}
	if ds.MentionsAssistantText < 1 {
		t.Errorf("delegate_send assistant-text mentions = %d, want >=1", ds.MentionsAssistantText)
	}
	if ds.MentionsAPICalls < 2 {
		t.Errorf("delegate_send api_call mentions = %d, want >=2", ds.MentionsAPICalls)
	}
}

func TestCount_RenderDisambiguates(t *testing.T) {
	base, sid := countFixture(t)
	ds, _ := Count(base, sid, "delegate_send")
	out := RenderCount(ds)
	if !strings.Contains(out, "0 calls") {
		t.Errorf("render should say 0 calls: %q", out)
	}
	if !strings.Contains(out, "mention") {
		t.Errorf("render should disambiguate with mentions: %q", out)
	}
}

func TestTranscript_OutlineAndElision(t *testing.T) {
	base, sid := countFixture(t)
	r, err := Transcript(base, sid, TranscriptOpts{Format: "outline"})
	if err != nil {
		t.Fatal(err)
	}
	if r.TurnsTotal != 3 {
		t.Errorf("TurnsTotal = %d, want 3", r.TurnsTotal)
	}
	if r.TurnsRendered != 3 || r.Elided != 0 {
		t.Errorf("rendered/elided = %d/%d, want 3/0", r.TurnsRendered, r.Elided)
	}
	out := RenderTranscript(r, "outline")
	if !strings.Contains(out, "USER_INPUT") || !strings.Contains(out, "read_file") {
		t.Errorf("outline missing turns/tools:\n%s", out)
	}
	if !strings.Contains(out, "turns_total=3") {
		t.Errorf("outline missing honest footer:\n%s", out)
	}
}

func TestTranscript_RangeLastN(t *testing.T) {
	base, sid := countFixture(t)
	r, err := Transcript(base, sid, TranscriptOpts{Range: "last:1"})
	if err != nil {
		t.Fatal(err)
	}
	if r.TurnsRendered != 1 || r.Elided != 2 {
		t.Errorf("last:1 rendered/elided = %d/%d, want 1/2", r.TurnsRendered, r.Elided)
	}
	if r.Turns[0].Index != 3 {
		t.Errorf("last:1 should show turn index 3, got %d", r.Turns[0].Index)
	}
}

func TestTranscript_ResultToolDefaultsToCommunicate(t *testing.T) {
	base, sid := countFixture(t)
	r, _ := Transcript(base, sid, TranscriptOpts{})
	if r.ResultTool != "communicate" {
		t.Errorf("ResultTool = %q, want communicate", r.ResultTool)
	}
	// The communicate call (turn 3) must be flagged as the result tool.
	last := r.Turns[len(r.Turns)-1]
	if len(last.ToolCalls) != 1 || !last.ToolCalls[0].IsResult {
		t.Errorf("communicate call should be flagged IsResult: %+v", last.ToolCalls)
	}
}

func TestTranscript_ResultToolFromMeta(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := sidB
	meta := schema.SessionMeta{Config: schema.ConfigSnapshot{ResultToolName: "submit_answer"}}
	turns := []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			toolCall("submit_answer", `{"ok":true}`),
		}}),
	}
	writeRichSession(t, bucket, sid, turns, nil, meta)

	r, err := Transcript(base, sid, TranscriptOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if r.ResultTool != "submit_answer" {
		t.Errorf("ResultTool = %q, want submit_answer (from meta)", r.ResultTool)
	}
	if !r.Turns[0].ToolCalls[0].IsResult {
		t.Error("submit_answer should be flagged as the result tool")
	}
}

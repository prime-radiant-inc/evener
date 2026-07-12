package apptranscript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

// FuzzProjectTurn drives the real apptranscript.ProjectTurn seam: it decodes
// fuzzed bytes into a schema.Turn (the typed transcript turn the reload path
// hands ProjectTurn) and projects it into AppWire thread items. ProjectTurn
// dispatches on turn.Kind (checkpoint/summary/userInput/steering/assistant/
// tool/toolResults) and walks the message content parts (text, thinking,
// web_search, tool calls/results, images, audio, documents), so a structurally
// varied turn exercises every projection branch. The oracle is floor "no panic"
// plus re-serializability: projected items go on the wire, so each must marshal
// cleanly.
func FuzzProjectTurn(f *testing.F) {
	exerciseTranscriptSurface(f)

	f.Add([]byte(`{"kind":"USER_INPUT","message":{"role":"user","content":[{"kind":"text","text":"hello"}]}}`))
	f.Add([]byte(`{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"text","text":"hi"},{"kind":"thinking","thinking":{"text":"hmm"}}]}}`))
	f.Add([]byte(`{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"tool_call","tool_call":{"id":"c1","name":"shell","arguments":{"command":"ls"}}}]}}`))
	f.Add([]byte(`{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"tool_call","tool_call":{"id":"c2","name":"communicate","arguments":{"message":"done"}}}]}}`))
	f.Add([]byte(`{"kind":"TOOL_RESULTS","message":{"role":"tool","content":[{"kind":"tool_result","tool_result":{"tool_call_id":"c1","name":"shell","content":"ok"}}]}}`))
	f.Add([]byte(`{"kind":"SUMMARY","message":{"role":"system","content":[{"kind":"text","text":"summary"}]}}`))
	f.Add([]byte(`{"kind":"STEERING","message":{"role":"user","content":[{"kind":"text","text":"steer"}]}}`))
	f.Add([]byte(`{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"web_search","web_search":{"query":"q","raw":{"action":{"query":"q"},"content":[{"type":"web_search_result","url":"http://x","title":"T"}]}}}]}}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`not json`))
	f.Add([]byte(`{"kind":"CHECKPOINT","message":{"content":[]}}`))
	f.Add([]byte(`{"kind":"STEERING","message":{"content":[{"kind":"image","image":{"data":"AQ==","media_type":"image/png"}},{"kind":"audio"},{"kind":"document"}]}}`))
	f.Add([]byte(`{"kind":"USER_INPUT","message":{"content":[{"kind":"image","image":{"data":"AQ==","media_type":"image/png"}},{"kind":"audio","audio":{"media_type":"audio/wav","url":"audio"}},{"kind":"document","document":{"media_type":"text/plain","file_name":"a.txt","url":"doc"}}]}}`))
	f.Add([]byte(`{"kind":"ASSISTANT","message":{"content":[{"kind":"text"},{"kind":"thinking"},{"kind":"redacted_thinking"},{"kind":"web_search"},{"kind":"tool_call"}]}}`))
	f.Add([]byte(`{"kind":"ASSISTANT","message":{"content":[{"kind":"web_search","web_search":{"raw":{"action":{"query":"action"},"groundingChunks":[{"web":{"title":"","uri":"https://chunk"}}],"content":[{"type":"other","title":"skip","url":"skip"},{"title":"title"}]}}},{"kind":"tool_call","tool_call":{"id":"c3","name":"communicate","arguments":{"output":{"message":"sent"}}}}]}}`))
	f.Add([]byte(`{"kind":"TOOL","message":{"content":[{"kind":"text"},{"kind":"tool_result"},{"kind":"tool_result","tool_result":{"tool_call_id":"c2","name":"communicate"}},{"kind":"tool_result","tool_result":{"tool_call_id":"c3","name":"shell","is_error":true,"content":{"error":"bad"}}}]}}`))

	f.Fuzz(func(t *testing.T, raw []byte) {
		var turn schema.Turn
		if err := json.Unmarshal(raw, &turn); err != nil {
			return // rejected input
		}
		toolNames := map[string]string{}
		items := ProjectTurn("turn_1", 1, turn, toolNames, nil, nil)
		for _, item := range items {
			if _, err := json.Marshal(item); err != nil {
				t.Fatalf("projected item failed to marshal: %v\n raw=%q\n item=%#v", err, raw, item)
			}
		}
	})
}

func exerciseTranscriptSurface(t testing.TB) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.jsonl")
	ScanPrelude(missing, 1<<20)
	TurnsFromFile(missing, 1<<20, nil)

	path := filepath.Join(dir, "session.jsonl")
	data := []byte("not json\n" +
		`{"kind":"header","system_prompt":" system "}` + "\n" +
		`{"kind":"api_call","request":"invalid"}` + "\n" +
		`{"kind":"api_call","error":" failed ","source":"provider","title":"Failure"}` + "\n" +
		`{"kind":"api_call"}` + "\n" +
		`{"kind":"entry","turn":{"timestamp":"2023-11-14T22:13:20Z","usage":{"input_tokens":1}}}` + "\n" +
		`{"kind":"entry","turn":` + "\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	ScanPrelude(path, 1<<20)
	TurnsFromFile(path, 1<<20, func(raw json.RawMessage, turnID string, turnIndex int) []appwire.ThreadItem {
		return []appwire.ThreadItem{{Type: "agentMessage", ID: "item", TurnID: turnID}}
	})
	TurnsFromFile(path, 1<<20, func(json.RawMessage, string, int) []appwire.ThreadItem { return nil })

	PreludeTurn(transcript.Header{}, nil)
	PreludeTurn(transcript.Header{}, &transcript.APICall{SystemPrompt: " fallback "})
	PreludeTurn(transcript.Header{}, &transcript.APICall{Request: llm.APILogRequest{Tools: []llm.ToolDefinition{{Name: "shell"}}}})
	FormatTools(llm.APILogRequest{})
	FormatTools(llm.APILogRequest{Tools: []llm.ToolDefinition{{Name: "shell"}}})
	FormatTools(llm.APILogRequest{Tools: []llm.ToolDefinition{{Name: "bad", Parameters: map[string]any{"bad": make(chan int)}}}})
	CompactionDescription("checkpoint")
	ImagePlaceholder(0)
	ImagePlaceholder(1)
	ImagePlaceholder(2)
	CommunicateMessageFromArguments([]byte(`{`))
	CommunicateMessageFromArguments([]byte(`{}`))
	ToolIntentFromArguments(nil)
	ToolIntentFromArguments([]byte(`{"intent":1,"purpose":" ","description":"desc"}`))
	StringifyToolContent(nil)
	StringifyToolContent("text")
	StringifyToolContent(make(chan int))
	DefaultImageProjector(llm.ImageData{MediaType: "image/png"})
	ImagesFromContent(nil, nil)
	AttachmentsFromContent([]llm.ContentPart{{Kind: llm.ContentAudio}, {Kind: llm.ContentDocument}})
	WebSearchProjection(nil)
	WebSearchProjection(&llm.WebSearchData{Raw: []byte(`{"input":{"query":"input"},"webSearchQueries":["gemini"],"groundingChunks":[{"web":{"title":"Title","uri":"https://example"}}],"content":[{"type":"ignored"}]}`)})
	webSearchResultLine("title", "")
	webSearchResultLine("", "")
	firstNonEmpty(" ", "value")
	firstNonEmpty(" ")
	ProjectTurn("turn_direct", 1, schema.Turn{}, nil, nil, nil)
	ProjectTurn("turn_direct", 2, schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Content: []llm.ContentPart{{
		Kind: llm.ContentToolResult,
		ToolResult: &llm.ToolResultData{
			ToolCallID: "call",
			Content:    "ok",
		},
	}}}}, map[string]string{"call": "shell"}, nil, func(*llm.ToolResultData) []appwire.OutputImage {
		return []appwire.OutputImage{{Name: "output.png"}}
	})

	cache := NewTurnCache()
	cache.max = 1
	cache.TurnsFromFile(path, 1<<20, nil)
	cache.TurnsFromFile(path, 1<<20, nil)
	cache.TurnsFromFile(missing, 1<<20, nil)
	second := filepath.Join(dir, "second.jsonl")
	if err := os.WriteFile(second, []byte(`{"kind":"header"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cache.TurnsFromFile(second, 1<<20, nil)
	if err := os.Chtimes(path, time.Unix(1, 0), time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	cache.TurnsFromFile(path, 1<<20, nil)
}

package transcriptindex

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func TestVoicePendingAsk_IndexedMatchedOrigin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	dir := filepath.Join(t.TempDir(), "index")
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: "instance"})
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	origin := &schema.ConversationOutputProvenance{Version: 1, InstanceID: "instance", RunGeneration: 7, RequestID: "actual-request", FrontierID: "actual-frontier"}
	call := schema.Turn{Format: schema.TurnFormatIdentity, Kind: schema.TurnAssistant, TurnID: "execution", TurnKind: schema.TurnSpanExecution, RoundID: "round", ConversationProvenance: origin, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "exact-call", Name: "ask_user", Arguments: json.RawMessage(`{"question":"original"}`)}}}}}
	if err := writer.Append(call); err != nil {
		t.Fatal(err)
	}
	index, err := Open(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { index.Close() }()
	initial, err := index.Latest(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(initial.Candidates) != 1 {
		t.Fatalf("opener items=%d", len(initial.Candidates))
	}
	opener := initial.Candidates[0].Item.ID
	canonical := json.RawMessage(`{"version":1,"questions":[{"question":"executed","options":[{"label":"2","detail":"d2"},{"label":"1","detail":"d1"}]}]}`)
	result := schema.Turn{Format: schema.TurnFormatIdentity, Kind: schema.TurnToolResults, TurnID: "execution", TurnKind: schema.TurnSpanExecution, RoundID: "round", Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{Name: "ask_user", ToolCallID: "exact-call", Content: "ack", ToolState: canonical}}}}}
	if err := writer.Append(result); err != nil {
		t.Fatal(err)
	}
	// Reopen after append exercises persisted indexed projection, then repeat the
	// read after closing both owners to exercise cold restoration of the same ID.
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}
	index, err = Open(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		window, err := index.Latest(10)
		if err != nil {
			t.Fatal(err)
		}
		if len(window.Candidates) != 1 {
			t.Fatalf("matched items=%d", len(window.Candidates))
		}
		item := window.Candidates[0].Item
		p := item.ConversationProvenance
		if item.ID != opener || item.CallID != "exact-call" || item.TurnID != "execution" || item.Status != appwire.TurnStatusCompleted || string(item.Raw) != string(canonical) {
			t.Fatalf("matched call identity/content=%+v", item)
		}
		if p == nil || p.SourceItemID != item.ID || p.RequestID != origin.RequestID || p.FrontierID != origin.FrontierID || p.RunGeneration != origin.RunGeneration {
			t.Fatalf("final indexed origin=%+v itemID=%s", p, item.ID)
		}
	}
	check()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}
	index, err = Open(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	check()
}

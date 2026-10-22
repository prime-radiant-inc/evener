package apptranscript_test

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

// Use the real transcript writer and reload its file after losing the live
// stream. A fresh full projection is the resync a returning Manager receives.
func TestManagerObservationTerminalEvidence(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	appendCommunicate := func(callID, reason string, known bool, causal *provenance.Causal) {
		t.Helper()
		turn := schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "t_1", Kind: schema.TurnCommunicate,
			Communicate: &schema.CommunicateInfo{CallID: callID, EndTurn: true, EndReason: reason, Message: callID}}
		// Exercise the persisted JSON schema, including an older unstamped
		// entry. Before the schema supports these fields the decoder loses them.
		if known {
			encoded, err := json.Marshal(turn)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatal(err)
			}
			fields["causal_known"] = json.RawMessage("true")
			if causal != nil {
				fields["causal"], err = json.Marshal(causal)
				if err != nil {
					t.Fatal(err)
				}
			}
			encoded, err = json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encoded, &turn); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Append(turn); err != nil {
			t.Fatal(err)
		}
	}
	causal := provenance.WithWatch(nil, "watch-A", "generation-A", "delivery-A", "source-A", "job-A")
	appendCommunicate("accepted", "needs_response", true, causal)
	appendCommunicate("rejected", "done", true, nil)
	appendCommunicate("unknown", "done", false, nil)
	results := schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "t_1", Kind: schema.TurnToolResults,
		Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "rejected", Name: "communicate", Content: `{"accepted":false,"end_turn":true}`}},
			{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "accepted", Name: "communicate", Content: `{"accepted":true,"end_turn":true}`, ToolState: json.RawMessage(`{"communicate_end_reason":"needs_response"}`)}},
		}}}
	if err := writer.Append(results); err != nil {
		t.Fatal(err)
	}
	// Reuse the unknown call ID in another logical turn. Its accepted result
	// must not leak backward across the turn/generation boundary.
	if err := writer.Append(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "t_2", Kind: schema.TurnCommunicate,
		Communicate: &schema.CommunicateInfo{CallID: "unknown", EndTurn: true, EndReason: "done", Message: "later"}}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Append(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "t_2", Kind: schema.TurnToolResults,
		Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{ToolCallID: "unknown", Name: "communicate", Content: `{"accepted":true,"end_turn":true}`},
		}}}}); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	// The index reloads the on-disk entries and projects the real item-window
	// resync, without consulting a lost event or a different call's outcome.
	index, err := transcriptindex.Open(path, filepath.Join(t.TempDir(), "index"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	window, err := index.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, candidate := range window.Candidates {
		item := candidate.Item
		if item.Type != "agentMessage" || item.CallID == "" {
			continue
		}
		if item.TurnID == "t_2" {
			if item.Observation == nil || item.Observation.TerminalAccepted == nil || !*item.Observation.TerminalAccepted {
				t.Fatalf("later generation lost its own result: %+v", item)
			}
			continue
		}
		encoded, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		var projected struct {
			Observation *appwire.SessionObservationMetadata `json:"observation"`
		}
		if err := json.Unmarshal(encoded, &projected); err != nil {
			t.Fatal(err)
		}
		found[item.CallID] = true
		if projected.Observation == nil {
			t.Fatalf("resync lost observation for %s", item.CallID)
		}
		got := projected.Observation
		if !got.EndTurn {
			t.Fatalf("%s lost end_turn", item.CallID)
		}
		switch item.CallID {
		case "accepted":
			if !got.CausalKnown || !reflect.DeepEqual(got.Causal, causal) || got.EndReason != "needs_response" || got.TerminalAccepted == nil || !*got.TerminalAccepted {
				t.Fatalf("accepted evidence = %+v", *got)
			}
		case "rejected":
			if !got.CausalKnown || got.Causal != nil || got.EndReason != "done" || got.TerminalAccepted == nil || *got.TerminalAccepted {
				t.Fatalf("rejected evidence = %+v", *got)
			}
		case "unknown":
			if got.CausalKnown || got.Causal != nil || got.TerminalAccepted != nil {
				t.Fatalf("old unstamped entry fabricated evidence = %+v", *got)
			}
		}
	}
	if !found["accepted"] || !found["rejected"] || !found["unknown"] {
		t.Fatalf("resync missing communicated calls: %+v", found)
	}
}

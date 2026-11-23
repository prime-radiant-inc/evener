package apptranscript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func quietYieldCommentary(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var payload struct {
		Commentary string `json:"commentary"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode quiet_yield raw %s: %v", raw, err)
	}
	return payload.Commentary
}

func writeTranscriptEntries(t *testing.T, entries ...schema.Turn) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	header, err := json.Marshal(transcript.Header{Kind: "header", FormatVersion: transcript.FormatVersion})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(append(header, '\n')); err != nil {
		t.Fatal(err)
	}
	for i, turn := range entries {
		line, err := json.Marshal(transcript.Entry{Kind: "entry", Seq: i + 1, Turn: turn})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestQuietYieldProjectionAndCompanionDemotion(t *testing.T) {
	t.Parallel()
	ts := time.Unix(1_790_000_000, 0).UTC()
	quietState := json.RawMessage(`{"communicate_accepted":true,"communicate_end_turn":true,"communicate_quiet":true}`)
	author := &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "root-session"}}

	for _, format := range []int{0, schema.TurnFormatIdentity} {
		name := "legacy"
		if format == schema.TurnFormatIdentity {
			name = "identity"
		}
		t.Run(name+"/clean_yield", func(t *testing.T) {
			t.Parallel()
			turnID := "turn_1"
			entries := []schema.Turn{
				{Format: format, TurnID: turnID, TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("wait"), Timestamp: ts},
				{Format: format, TurnID: turnID, RoundID: "r1", Authorship: author.Clone(), Kind: schema.TurnAssistant, Timestamp: ts.Add(time.Second), Message: llm.Message{
					Role:    llm.RoleAssistant,
					Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_q1", Name: "communicate", Arguments: json.RawMessage(`{}`)}}},
				}},
				{Format: format, TurnID: turnID, RoundID: "r1", Kind: schema.TurnToolResults, Timestamp: ts.Add(2 * time.Second), Message: llm.Message{
					Role:    llm.RoleTool,
					Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_q1", Name: "communicate", ToolState: quietState}}},
				}},
			}
			if format == schema.TurnFormatIdentity {
				entries = append(entries, schema.Turn{Format: format, TurnID: turnID, Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}})
			}
			path := writeTranscriptEntries(t, entries...)
			reg := NewToolCallRegistry()
			turns, err := ItemTurnsFromFile(path, transcript.DefaultMaxLineBytes, func(turn schema.Turn, id string, idx int) []appwire.ThreadItem {
				items, _ := ProjectEntryParts(id, idx, turn, reg, nil, nil)
				return items
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(turns) != 1 || len(turns[0].Items) != 2 {
				t.Fatalf("turns=%+v, want 1 turn with userMessage + quiet_yield", turns)
			}
			q := turns[0].Items[1]
			if q.Type != "systemMessage" || q.EventKind != appwire.ThreadItemEventKindQuietYield || q.Status != appwire.TurnStatusCompleted {
				t.Fatalf("quiet item=%+v, want completed systemMessage with EventKind=%q", q, appwire.ThreadItemEventKindQuietYield)
			}
			if q.CallID != "call_q1" || q.RoundID != "r1" || q.Text == "" || q.StartedAt != nil {
				t.Fatalf("quiet item fields=%+v, want CallID=call_q1 RoundID=r1 non-empty Text nil StartedAt", q)
			}
			if q.CompletedAt == nil || *q.CompletedAt != ts.Add(2*time.Second).UnixMilli() {
				t.Fatalf("CompletedAt=%v, want %d", q.CompletedAt, ts.Add(2*time.Second).UnixMilli())
			}
			if len(q.Raw) != 0 {
				t.Fatalf("clean quiet yield Raw=%s, want empty", q.Raw)
			}
			if q.Authorship == nil || q.Authorship.Author.SessionRef != "local:root-session" {
				t.Fatalf("Authorship=%+v, want local:root-session", q.Authorship)
			}
		})

		t.Run(name+"/companion_prose_demoted_keeps_earlier_round_and_siblings", func(t *testing.T) {
			t.Parallel()
			turnID := "turn_1"
			entries := []schema.Turn{
				{Format: format, TurnID: turnID, TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("watch"), Timestamp: ts},
				// Earlier round in the same logical turn: its assistant prose must remain visible.
				{Format: format, TurnID: turnID, RoundID: "r_early", Kind: schema.TurnAssistant, Timestamp: ts.Add(time.Second), Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						{Kind: llm.ContentText, Text: "earlier-round-sentinel"},
						{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_watch", Name: "job_watch", Arguments: json.RawMessage(`{}`)}},
					},
				}},
				{Format: format, TurnID: turnID, RoundID: "r_early", Kind: schema.TurnToolResults, Timestamp: ts.Add(2 * time.Second), Message: llm.Message{
					Role:    llm.RoleTool,
					Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_watch", Name: "job_watch", Content: "armed"}}},
				}},
				// Quiet round in the same logical turn: thinking & sibling tool survive, companion text is demoted.
				{Format: format, TurnID: turnID, RoundID: "r_quiet", Authorship: author.Clone(), Kind: schema.TurnAssistant, Timestamp: ts.Add(3 * time.Second), Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "quiet-thought-sentinel"}},
						{Kind: llm.ContentText, Text: "  companion-prose-1  "},
						{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_sibling", Name: "job_list", Arguments: json.RawMessage(`{}`)}},
						{Kind: llm.ContentText, Text: "companion-prose-2"},
						{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_q2", Name: "communicate", Arguments: json.RawMessage(`{}`)}},
					},
				}},
				{Format: format, TurnID: turnID, RoundID: "r_quiet", Kind: schema.TurnToolResults, Timestamp: ts.Add(4 * time.Second), Message: llm.Message{
					Role: llm.RoleTool,
					Content: []llm.ContentPart{
						{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_sibling", Name: "job_list", Content: "none"}},
						{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_q2", Name: "communicate", ToolState: quietState}},
					},
				}},
			}
			if format == schema.TurnFormatIdentity {
				entries = append(entries, schema.Turn{Format: format, TurnID: turnID, Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}})
			}
			path := writeTranscriptEntries(t, entries...)
			reg := NewToolCallRegistry()
			turns, err := ItemTurnsFromFile(path, transcript.DefaultMaxLineBytes, func(turn schema.Turn, id string, idx int) []appwire.ThreadItem {
				items, _ := ProjectEntryParts(id, idx, turn, reg, nil, nil)
				return items
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(turns) != 1 {
				t.Fatalf("turns=%d, want 1", len(turns))
			}
			var agents []string
			var reasonings []string
			var tools []string
			var quiets []appwire.ThreadItem
			for _, it := range turns[0].Items {
				switch {
				case it.Type == "agentMessage":
					agents = append(agents, it.Text)
				case it.Type == "reasoning":
					reasonings = append(reasonings, it.Text)
				case it.Type == "commandExecution":
					tools = append(tools, it.ToolName)
				case it.Type == "systemMessage" && it.EventKind == appwire.ThreadItemEventKindQuietYield:
					quiets = append(quiets, it)
				}
			}
			if len(agents) != 1 || agents[0] != "earlier-round-sentinel" {
				t.Fatalf("agentMessages=%q, want only earlier-round-sentinel (companion prose demoted)", agents)
			}
			if len(reasonings) != 1 || reasonings[0] != "quiet-thought-sentinel" {
				t.Fatalf("reasonings=%q, want quiet-thought-sentinel preserved", reasonings)
			}
			if len(tools) != 2 {
				t.Fatalf("tools=%v, want 2 sibling tools preserved", tools)
			}
			if len(quiets) != 1 {
				t.Fatalf("quiets=%d, want 1", len(quiets))
			}
			if got := quietYieldCommentary(t, quiets[0].Raw); got != "companion-prose-1\n\ncompanion-prose-2" {
				t.Fatalf("commentary=%q, want demoted joined companion prose", got)
			}
		})

		t.Run(name+"/quiet_note_renders_as_item_text", func(t *testing.T) {
			t.Parallel()
			turnID := "turn_1"
			noteState := json.RawMessage(`{"communicate_accepted":true,"communicate_end_turn":true,"communicate_quiet":true,"communicate_quiet_note":"quiet-note-sentinel"}`)
			entries := []schema.Turn{
				{Format: format, TurnID: turnID, TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("wait"), Timestamp: ts},
				{Format: format, TurnID: turnID, RoundID: "r1", Kind: schema.TurnAssistant, Timestamp: ts.Add(time.Second), Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						{Kind: llm.ContentText, Text: "companion-note-prose"},
						{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_qn", Name: "communicate", Arguments: json.RawMessage(`{}`)}},
					},
				}},
				{Format: format, TurnID: turnID, RoundID: "r1", Kind: schema.TurnToolResults, Timestamp: ts.Add(2 * time.Second), Message: llm.Message{
					Role:    llm.RoleTool,
					Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_qn", Name: "communicate", ToolState: noteState}}},
				}},
			}
			if format == schema.TurnFormatIdentity {
				entries = append(entries, schema.Turn{Format: format, TurnID: turnID, Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}})
			}
			path := writeTranscriptEntries(t, entries...)
			reg := NewToolCallRegistry()
			turns, err := ItemTurnsFromFile(path, transcript.DefaultMaxLineBytes, func(turn schema.Turn, id string, idx int) []appwire.ThreadItem {
				items, _ := ProjectEntryParts(id, idx, turn, reg, nil, nil)
				return items
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(turns) != 1 || len(turns[0].Items) != 2 {
				t.Fatalf("turns=%+v, want 1 turn with 2 items", turns)
			}
			q := turns[0].Items[1]
			if q.EventKind != appwire.ThreadItemEventKindQuietYield || q.Text != "quiet-note-sentinel" {
				t.Fatalf("quiet item=%+v, want Text=%q", q, "quiet-note-sentinel")
			}
			if got := quietYieldCommentary(t, q.Raw); got != "companion-note-prose" {
				t.Fatalf("commentary=%q, want companion-note-prose", got)
			}
		})
	}
}

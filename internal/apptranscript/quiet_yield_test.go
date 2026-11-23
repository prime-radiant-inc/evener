package apptranscript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	}

	t.Run("explicit_communicate_update_survives_when_companion_prose_is_demoted", func(t *testing.T) {
		t.Parallel()
		turnID := "turn_update_and_quiet"
		updateState := json.RawMessage(`{"communicate_accepted":true,"communicate_end_turn":false}`)
		quietState := json.RawMessage(`{"communicate_accepted":true,"communicate_end_turn":true,"communicate_quiet":true}`)
		entries := []schema.Turn{
			{Format: schema.TurnFormatIdentity, TurnID: turnID, TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("start"), Timestamp: ts},
			{Format: schema.TurnFormatIdentity, TurnID: turnID, RoundID: "r1", Kind: schema.TurnAssistant, Timestamp: ts.Add(time.Second), Message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{
					{Kind: llm.ContentText, Text: "Working on X"},
					{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_msg", Name: "communicate", Arguments: json.RawMessage(`{"message":"Working on X","end_turn":false}`)}},
					{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_quiet", Name: "communicate", Arguments: json.RawMessage(`{}`)}},
				},
			}},
			{Format: schema.TurnFormatIdentity, TurnID: turnID, Kind: schema.TurnCommunicate, Timestamp: ts.Add(2 * time.Second), Communicate: &schema.CommunicateInfo{
				CallID: "call_msg", EndTurn: false, Message: "Working on X",
			}},
			{Format: schema.TurnFormatIdentity, TurnID: turnID, RoundID: "r1", Kind: schema.TurnToolResults, Timestamp: ts.Add(3 * time.Second), Message: llm.Message{
				Role: llm.RoleTool,
				Content: []llm.ContentPart{
					{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_msg", Name: "communicate", Content: `{"accepted":true,"end_turn":false}`, ToolState: updateState}},
					{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_quiet", Name: "communicate", Content: `{"accepted":true,"end_turn":true}`, ToolState: quietState}},
				},
			}},
			{Format: schema.TurnFormatIdentity, TurnID: turnID, Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}},
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
		var msgs, quiets []appwire.ThreadItem
		for _, it := range turns[0].Items {
			if it.Type == "agentMessage" {
				msgs = append(msgs, it)
			}
			if it.EventKind == appwire.ThreadItemEventKindQuietYield {
				quiets = append(quiets, it)
			}
		}
		if len(msgs) != 1 || msgs[0].Text != "Working on X" || msgs[0].CallID != "call_msg" || msgs[0].Observation == nil || msgs[0].Observation.EndTurn {
			t.Fatalf("agentMessages=%+v, want single communicate update Working on X with CallID=call_msg EndTurn=false", msgs)
		}
		if len(quiets) != 1 || quiets[0].CallID != "call_quiet" || quietYieldCommentary(t, quiets[0].Raw) != "Working on X" {
			t.Fatalf("quiets=%+v, want quiet_yield with commentary Working on X", quiets)
		}
	})

	t.Run("healed_communicate_update_survives_when_companion_prose_is_demoted", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name       string
			format     int
			authorship *schema.TranscriptAuthorship
		}{
			{name: "legacy", format: 0},
			{name: "inherited", format: schema.TurnFormatIdentity, authorship: &schema.TranscriptAuthorship{Inherited: true, Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "root"}}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				turnID := "turn_healed_and_quiet"
				updateState := json.RawMessage(`{"communicate_accepted":true,"communicate_end_turn":false}`)
				quietState := json.RawMessage(`{"communicate_accepted":true,"communicate_end_turn":true,"communicate_quiet":true}`)
				entries := []schema.Turn{
					{Format: tc.format, TurnID: turnID, TurnKind: schema.TurnSpanExecution, Authorship: tc.authorship, Kind: schema.TurnUserInput, Message: llm.User("start"), Timestamp: ts},
					{Format: tc.format, TurnID: turnID, RoundID: "r1", Authorship: tc.authorship, Kind: schema.TurnAssistant, Timestamp: ts.Add(time.Second), Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentText, Text: "Working on X"},
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_healed", Name: "communicate", Arguments: json.RawMessage(`{}`), RawArguments: `{"message":"Working on X","end_turn":false}`}},
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_quiet", Name: "communicate", Arguments: json.RawMessage(`{}`)}},
						},
					}},
					{Format: tc.format, TurnID: turnID, RoundID: "r1", Authorship: tc.authorship, Kind: schema.TurnToolResults, Timestamp: ts.Add(2 * time.Second), Message: llm.Message{
						Role: llm.RoleTool,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_healed", Name: "communicate", Content: `{"accepted":true,"end_turn":false}`, ToolState: updateState}},
							{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_quiet", Name: "communicate", Content: `{"accepted":true,"end_turn":true}`, ToolState: quietState}},
						},
					}},
				}
				if tc.format == schema.TurnFormatIdentity {
					entries = append(entries, schema.Turn{Format: tc.format, TurnID: turnID, Authorship: tc.authorship, Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}})
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
				var msgs []appwire.ThreadItem
				for _, it := range turns[0].Items {
					if it.Type == "agentMessage" {
						msgs = append(msgs, it)
					}
				}
				if len(msgs) != 1 || msgs[0].Text != "Working on X" || msgs[0].CallID != "call_healed" {
					t.Fatalf("agentMessages=%+v, want single healed communicate Working on X", msgs)
				}
			})
		}
	})

	t.Run("provisional_prose_flushes_on_error_result_and_turn_completion", func(t *testing.T) {
		t.Parallel()
		t.Run("error_result", func(t *testing.T) {
			t.Parallel()
			turnID := "turn_err_flush"
			inheritedAuthor := &schema.TranscriptAuthorship{Inherited: true, Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "root-session"}}
			entries := []schema.Turn{
				{Format: schema.TurnFormatIdentity, TurnID: turnID, TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("start"), Timestamp: ts},
				{Format: schema.TurnFormatIdentity, TurnID: turnID, RoundID: "r1", Authorship: inheritedAuthor.Clone(), Kind: schema.TurnAssistant, Timestamp: ts.Add(time.Second), Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						{Kind: llm.ContentText, Text: "Companion before error"},
						{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_q_err", Name: "communicate", Arguments: json.RawMessage(`{}`)}},
					},
				}},
				{Format: schema.TurnFormatIdentity, TurnID: turnID, RoundID: "r1", Kind: schema.TurnToolResults, Timestamp: ts.Add(2 * time.Second), Message: llm.Message{
					Role: llm.RoleTool,
					Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
						ToolCallID: "call_q_err", Name: "communicate", IsError: true, PrevalOnly: true, Content: "cannot end turn",
					}}},
				}},
				{Format: schema.TurnFormatIdentity, TurnID: turnID, Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}},
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
			var msgs, errs []appwire.ThreadItem
			for _, it := range turns[0].Items {
				if it.Type == "agentMessage" {
					msgs = append(msgs, it)
				}
				if it.Type == "commandExecution" && it.PrevalOnly {
					errs = append(errs, it)
				}
			}
			if len(msgs) != 1 || msgs[0].Text != "Companion before error" {
				t.Fatalf("agentMessages=%+v, want Companion before error flushed", msgs)
			}
			if msgs[0].Authorship == nil || msgs[0].Authorship.Author.SessionRef != "local:root-session" {
				t.Fatalf("Authorship=%+v, want local:root-session", msgs[0].Authorship)
			}
			if len(errs) != 1 || errs[0].CallID != "call_q_err" {
				t.Fatalf("errs=%+v, want preval error item", errs)
			}
		})

		t.Run("interrupted_completion_without_result", func(t *testing.T) {
			t.Parallel()
			turnID := "turn_interrupted_flush"
			entries := []schema.Turn{
				{Format: schema.TurnFormatIdentity, TurnID: turnID, TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("start"), Timestamp: ts},
				{Format: schema.TurnFormatIdentity, TurnID: turnID, RoundID: "r1", Authorship: author.Clone(), Kind: schema.TurnAssistant, Timestamp: ts.Add(time.Second), Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						{Kind: llm.ContentText, Text: "Companion before crash"},
						{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_q_crash", Name: "communicate", Arguments: json.RawMessage(`{}`)}},
					},
				}},
				{Format: schema.TurnFormatIdentity, TurnID: turnID, Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnInterrupted}},
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
			var msgs []appwire.ThreadItem
			for _, it := range turns[0].Items {
				if it.Type == "agentMessage" {
					msgs = append(msgs, it)
				}
			}
			if len(msgs) != 1 || msgs[0].Text != "Companion before crash" {
				t.Fatalf("agentMessages=%+v, want Companion before crash flushed on TurnCompletion", msgs)
			}
			if msgs[0].Authorship == nil || msgs[0].Authorship.Author.SessionRef != "local:root-session" {
				t.Fatalf("Authorship=%+v, want local:root-session", msgs[0].Authorship)
			}
		})

		t.Run("preToolUse_blanked_message_demotes_companion_prose", func(t *testing.T) {
			t.Parallel()
			for _, withSiblingUpdate := range []bool{false, true} {
				name := "standalone"
				if withSiblingUpdate {
					name = "with_sibling_update"
				}
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					turnID := "turn_hook_blanked"
					quietState := json.RawMessage(`{"communicate_accepted":true,"communicate_end_turn":true,"communicate_quiet":true}`)
					content := []llm.ContentPart{{Kind: llm.ContentText, Text: "Companion before hook"}}
					if withSiblingUpdate {
						content = append(content, llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_msg", Name: "communicate", Arguments: json.RawMessage(`{"message":"Companion before hook","end_turn":false}`)}})
					}
					content = append(content, llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_hook_blanked", Name: "communicate", Arguments: json.RawMessage(`{"message":"original"}`)}})
					entries := []schema.Turn{
						{Format: schema.TurnFormatIdentity, TurnID: turnID, TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("start"), Timestamp: ts},
						{Format: schema.TurnFormatIdentity, TurnID: turnID, RoundID: "r1", Authorship: author.Clone(), Kind: schema.TurnAssistant, Timestamp: ts.Add(time.Second), Message: llm.Message{Role: llm.RoleAssistant, Content: content}},
					}
					var resultParts []llm.ContentPart
					if withSiblingUpdate {
						entries = append(entries, schema.Turn{Format: schema.TurnFormatIdentity, TurnID: turnID, Kind: schema.TurnCommunicate, Communicate: &schema.CommunicateInfo{CallID: "call_msg", EndTurn: false, Message: "Companion before hook"}, Timestamp: ts.Add(1500 * time.Millisecond)})
						resultParts = append(resultParts, llm.ContentPart{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_msg", Name: "communicate", Content: `{"accepted":true,"end_turn":false}`}})
					}
					resultParts = append(resultParts, llm.ContentPart{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_hook_blanked", Name: "communicate", Content: `{"accepted":true,"end_turn":true}`, ToolState: quietState}})
					entries = append(entries,
						schema.Turn{Format: schema.TurnFormatIdentity, TurnID: turnID, RoundID: "r1", Kind: schema.TurnToolResults, Timestamp: ts.Add(2 * time.Second), Message: llm.Message{Role: llm.RoleTool, Content: resultParts}},
						schema.Turn{Format: schema.TurnFormatIdentity, TurnID: turnID, Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}},
					)
					path := writeTranscriptEntries(t, entries...)
					reg := NewToolCallRegistry()
					turns, err := ItemTurnsFromFile(path, transcript.DefaultMaxLineBytes, func(turn schema.Turn, id string, idx int) []appwire.ThreadItem {
						items, _ := ProjectEntryParts(id, idx, turn, reg, nil, nil)
						return items
					})
					if err != nil {
						t.Fatal(err)
					}
					var msgs, quiets []appwire.ThreadItem
					for _, it := range turns[0].Items {
						if it.Type == "agentMessage" {
							msgs = append(msgs, it)
						}
						if it.EventKind == appwire.ThreadItemEventKindQuietYield {
							quiets = append(quiets, it)
						}
					}
					if withSiblingUpdate {
						if len(msgs) != 1 || msgs[0].CallID != "call_msg" || msgs[0].Text != "Companion before hook" {
							t.Fatalf("withSiblingUpdate agentMessages=%+v, want single call_msg update", msgs)
						}
					} else if len(msgs) != 0 {
						t.Fatalf("standalone hook-blanked agentMessages=%+v, want none", msgs)
					}
					if len(quiets) != 1 || quietYieldCommentary(t, quiets[0].Raw) != "Companion before hook" {
						t.Fatalf("quiets=%+v, want quiet_yield with Companion before hook commentary", quiets)
					}
				})
			}
		})

		t.Run("preToolUse_supplied_message_on_blank_communicate_preserves_distinct_and_dedupes_matching_prose", func(t *testing.T) {
			t.Parallel()
			for _, tc := range []struct {
				name      string
				prose     string
				hookReply string
				wantTexts []string
			}{
				{name: "distinct", prose: "Distinct companion prose", hookReply: "Hook supplied reply", wantTexts: []string{"Hook supplied reply", "Distinct companion prose"}},
				{name: "matching", prose: "Same hook reply", hookReply: "Same hook reply", wantTexts: []string{"Same hook reply"}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					turnID := "turn_hook_supplied"
					deliveredState := json.RawMessage(`{"communicate_accepted":true,"communicate_end_turn":true}`)
					entries := []schema.Turn{
						{Format: schema.TurnFormatIdentity, TurnID: turnID, TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("go"), Timestamp: ts},
						{Format: schema.TurnFormatIdentity, TurnID: turnID, RoundID: "r1", Authorship: author.Clone(), Kind: schema.TurnAssistant, Timestamp: ts.Add(time.Second), Message: llm.Message{
							Role: llm.RoleAssistant,
							Content: []llm.ContentPart{
								{Kind: llm.ContentText, Text: tc.prose},
								{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_q", Name: "communicate", Arguments: json.RawMessage(`{}`)}},
							},
						}},
						{Format: schema.TurnFormatIdentity, TurnID: turnID, Kind: schema.TurnCommunicate, Timestamp: ts.Add(1500 * time.Millisecond), Communicate: &schema.CommunicateInfo{CallID: "call_q", EndTurn: true, Message: tc.hookReply}},
						{Format: schema.TurnFormatIdentity, TurnID: turnID, RoundID: "r1", Kind: schema.TurnToolResults, Timestamp: ts.Add(2 * time.Second), Message: llm.Message{
							Role:    llm.RoleTool,
							Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_q", Name: "communicate", Content: `{"accepted":true,"end_turn":true}`, ToolState: deliveredState}}},
						}},
						{Format: schema.TurnFormatIdentity, TurnID: turnID, Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}},
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
					var got []string
					for _, it := range turns[0].Items {
						if it.Type == "agentMessage" {
							got = append(got, it.Text)
						}
					}
					if len(got) != len(tc.wantTexts) {
						t.Fatalf("agentMessages=%q, want %q", got, tc.wantTexts)
					}
					for i := range got {
						if got[i] != tc.wantTexts[i] {
							t.Fatalf("agentMessages[%d]=%q, want %q (all=%q)", i, got[i], tc.wantTexts[i], got)
						}
					}
				})
			}
		})
	})
}

func TestQuietYieldNoteAndNextEnum(t *testing.T) {
	t.Parallel()
	ts := time.Unix(1_790_000_000, 0).UTC()
	author := &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "root-session"}}

	t.Run("possibly_quiet_call_recognizes_next_enum", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name string
			args string
			want bool
		}{
			{name: "next_after_message_quiet", args: `{"message":"Waiting on CI","next":"log_quietly_and_end_turn"}`, want: true},
			{name: "next_before_message_quiet", args: `{"next":"log_quietly_and_end_turn","message":"Waiting on CI"}`, want: true},
			{name: "escaped_message_and_quiet_next", args: `{"message":"Waiting on \"CI\"","next":"log_quietly_and_end_turn"}`, want: true},
			{name: "output_message_with_quiet_next", args: `{"output":{"message":"Nested"},"next":"log_quietly_and_end_turn"}`, want: true},
			{name: "send_and_end_turn", args: `{"message":"Done","next":"send_and_end_turn"}`, want: false},
			{name: "send_and_continue", args: `{"message":"Working","next":"send_and_continue"}`, want: false},
			{name: "escaped_message_send_and_end_turn", args: `{"message":"Said \"hi\"","next":"send_and_end_turn"}`, want: false},
			{name: "empty_object", args: `{}`, want: true},
			{name: "whitespace_message", args: `{"message":"   "}`, want: true},
			{name: "malformed_truncated", args: `{"message":"hi",`, want: true},
		} {
			call := &llm.ToolCallData{ID: "c1", Name: "communicate", Arguments: json.RawMessage(tc.args)}
			if got := PossiblyQuietCommunicateCall(call, "communicate"); got != tc.want {
				t.Errorf("%s (%s): PossiblyQuietCommunicateCall = %v, want %v", tc.name, tc.args, got, tc.want)
			}
		}
	})

	for _, format := range []int{0, schema.TurnFormatIdentity} {
		name := "legacy"
		if format == schema.TurnFormatIdentity {
			name = "identity"
		}
		t.Run(name+"/note_round_trips_and_empty_note_keeps_fallback", func(t *testing.T) {
			t.Parallel()
			const wantNote = "Waiting on CI job_123"
			for _, tc := range []struct {
				sub      string
				noteJSON string
				wantNote string
			}{
				{sub: "with_note", noteJSON: `,"communicate_quiet_note":"Waiting on CI job_123"`, wantNote: wantNote},
				{sub: "empty_note", noteJSON: ``, wantNote: ""},
			} {
				t.Run(tc.sub, func(t *testing.T) {
					t.Parallel()
					turnID := "turn_note"
					state := json.RawMessage(`{"communicate_accepted":true,"communicate_end_turn":true,"communicate_quiet":true` + tc.noteJSON + `}`)
					entries := []schema.Turn{
						{Format: format, TurnID: turnID, TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("wait"), Timestamp: ts},
						{Format: format, TurnID: turnID, RoundID: "r1", Authorship: author.Clone(), Kind: schema.TurnAssistant, Timestamp: ts.Add(time.Second), Message: llm.Message{
							Role: llm.RoleAssistant,
							Content: []llm.ContentPart{
								{Kind: llm.ContentText, Text: "companion-prose"},
								{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_q", Name: "communicate", Arguments: json.RawMessage(`{"message":"Waiting on CI job_123","next":"log_quietly_and_end_turn"}`)}},
							},
						}},
						{Format: format, TurnID: turnID, RoundID: "r1", Kind: schema.TurnToolResults, Timestamp: ts.Add(2 * time.Second), Message: llm.Message{
							Role:    llm.RoleTool,
							Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_q", Name: "communicate", ToolState: state}}},
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
					var quiets []appwire.ThreadItem
					for _, it := range turns[0].Items {
						if it.Type == "agentMessage" {
							t.Fatalf("unexpected agentMessage alongside quiet yield: %+v", it)
						}
						if it.EventKind == appwire.ThreadItemEventKindQuietYield {
							quiets = append(quiets, it)
						}
					}
					if len(quiets) != 1 {
						t.Fatalf("quiets=%+v, want 1", quiets)
					}
					if tc.wantNote != "" {
						if quiets[0].Text != tc.wantNote {
							t.Fatalf("Text=%q, want %q", quiets[0].Text, tc.wantNote)
						}
					} else if strings.TrimSpace(quiets[0].Text) == "" {
						t.Fatalf("empty note yielded blank Text: %+v", quiets[0])
					}
					if got := quietYieldCommentary(t, quiets[0].Raw); got != "companion-prose" {
						t.Fatalf("commentary=%q, want companion-prose", got)
					}
				})
			}
		})

		t.Run(name+"/bare_text_quiet_yield_assistant_entry", func(t *testing.T) {
			t.Parallel()
			turnID := "turn_bare_quiet"
			longMultiline := "Waiting on subagent\n" + strings.Repeat("x", 260)
			entries := []schema.Turn{
				{Format: format, TurnID: turnID, TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("go"), Timestamp: ts},
				{Format: format, TurnID: turnID, RoundID: "r_single", Authorship: author.Clone(), Kind: schema.TurnAssistant, QuietYield: true, Timestamp: ts.Add(time.Second), Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "quiet-thought"}},
						{Kind: llm.ContentText, Text: "Waiting on build"},
					},
				}},
				{Format: format, TurnID: turnID, RoundID: "r_multi", Authorship: author.Clone(), Kind: schema.TurnAssistant, QuietYield: true, Timestamp: ts.Add(2 * time.Second), Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						{Kind: llm.ContentText, Text: longMultiline},
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
			var reasonings, quiets []appwire.ThreadItem
			for _, it := range turns[0].Items {
				switch {
				case it.Type == "agentMessage":
					t.Fatalf("QuietYield TurnAssistant emitted agentMessage: %+v", it)
				case it.Type == "reasoning":
					reasonings = append(reasonings, it)
				case it.EventKind == appwire.ThreadItemEventKindQuietYield:
					quiets = append(quiets, it)
				}
			}
			if len(reasonings) != 1 || reasonings[0].Text != "quiet-thought" {
				t.Fatalf("reasonings=%+v, want quiet-thought", reasonings)
			}
			if len(quiets) != 2 {
				t.Fatalf("quiets=%+v, want 2", quiets)
			}
			if quiets[0].Text != "Waiting on build" || len(quiets[0].Raw) != 0 || quiets[0].RoundID != "r_single" {
				t.Fatalf("single-line quiet=%+v, want Text=\"Waiting on build\" empty Raw RoundID=r_single", quiets[0])
			}
			if quiets[0].Authorship == nil || quiets[0].Authorship.Author.SessionRef != "local:root-session" {
				t.Fatalf("Authorship=%+v, want local:root-session", quiets[0].Authorship)
			}
			wantNorm := schema.NormalizeQuietNote(longMultiline)
			if quiets[1].Text != wantNorm || quiets[1].RoundID != "r_multi" {
				t.Fatalf("long multiline quiet=%+v, want Text=%q RoundID=r_multi", quiets[1], wantNorm)
			}
			if got := quietYieldCommentary(t, quiets[1].Raw); got != longMultiline {
				t.Fatalf("long multiline commentary=%q, want full original text", got)
			}
		})
	}
}

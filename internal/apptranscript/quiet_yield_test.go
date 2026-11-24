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

// quietYieldArgs is the call the producer records for a quiet yield.
var quietYieldArgs = json.RawMessage(`{"message":"waiting on CI","next":"log_quietly_and_end_turn"}`)

func quietToolState(t *testing.T, note, commentary string) json.RawMessage {
	t.Helper()
	yes := true
	raw, err := json.Marshal(schema.CommunicateToolState{
		Accepted: &yes, EndTurn: &yes, Quiet: &yes,
		QuietNote: note, QuietCommentary: commentary,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

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

// projectQuietFixture projects entries through one registry, as a sequential
// transcript read does.
func projectQuietFixture(t *testing.T, entries []schema.Turn) []appwire.ThreadItem {
	t.Helper()
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
	return turns[0].Items
}

func TestQuietYieldProjectionAndCompanionDemotion(t *testing.T) {
	t.Parallel()
	ts := time.Unix(1_790_000_000, 0).UTC()
	author := &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "root-session"}}
	quietState := quietToolState(t, "", "")

	for _, format := range []int{0, schema.TurnFormatIdentity} {
		name := "legacy"
		if format == schema.TurnFormatIdentity {
			name = "identity"
		}
		t.Run(name+"/clean_yield", func(t *testing.T) {
			t.Parallel()
			turnID := "turn_1"
			// Legacy `{}` exercises the empty-object fallback. Identity entries
			// carry the enum the producer writes.
			args := json.RawMessage(`{}`)
			if format == schema.TurnFormatIdentity {
				args = quietYieldArgs
			}
			entries := []schema.Turn{
				{Format: format, TurnID: turnID, TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("wait"), Timestamp: ts},
				{Format: format, TurnID: turnID, RoundID: "r1", Authorship: author.Clone(), Kind: schema.TurnAssistant, Timestamp: ts.Add(time.Second), Message: llm.Message{
					Role:    llm.RoleAssistant,
					Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_q1", Name: "communicate", Arguments: args}}},
				}},
				{Format: format, TurnID: turnID, RoundID: "r1", Kind: schema.TurnToolResults, Timestamp: ts.Add(2 * time.Second), Message: llm.Message{
					Role:    llm.RoleTool,
					Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_q1", Name: "communicate", ToolState: quietState}}},
				}},
			}
			if format == schema.TurnFormatIdentity {
				entries = append(entries, schema.Turn{Format: format, TurnID: turnID, Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}})
			}
			items := projectQuietFixture(t, entries)
			if len(items) != 2 {
				t.Fatalf("items=%+v, want userMessage + quiet_yield", items)
			}
			q := items[1]
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
			args := json.RawMessage(`{}`)
			if format == schema.TurnFormatIdentity {
				args = quietYieldArgs
			}
			const companion = "companion-prose-1\n\ncompanion-prose-2"
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
				// Quiet round in the same logical turn: thinking & sibling tool
				// survive; the producer stamped the prose as the quiet companion.
				{Format: format, TurnID: turnID, RoundID: "r_quiet", Authorship: author.Clone(), Kind: schema.TurnAssistant, QuietCompanion: true, Timestamp: ts.Add(3 * time.Second), Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "quiet-thought-sentinel"}},
						{Kind: llm.ContentText, Text: "  companion-prose-1  "},
						{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_sibling", Name: "job_list", Arguments: json.RawMessage(`{}`)}},
						{Kind: llm.ContentText, Text: "companion-prose-2"},
						{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_q2", Name: "communicate", Arguments: args}},
					},
				}},
				{Format: format, TurnID: turnID, RoundID: "r_quiet", Kind: schema.TurnToolResults, Timestamp: ts.Add(4 * time.Second), Message: llm.Message{
					Role: llm.RoleTool,
					Content: []llm.ContentPart{
						{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_sibling", Name: "job_list", Content: "none"}},
						{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_q2", Name: "communicate", ToolState: quietToolState(t, "waiting on CI", companion)}},
					},
				}},
			}
			if format == schema.TurnFormatIdentity {
				entries = append(entries, schema.Turn{Format: format, TurnID: turnID, Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}})
			}
			items := projectQuietFixture(t, entries)
			var agents []string
			var reasonings []string
			var tools []string
			var quiets []appwire.ThreadItem
			for _, it := range items {
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
				t.Fatalf("agentMessages=%q, want only earlier-round-sentinel (companion prose suppressed)", agents)
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
			if got := quietYieldCommentary(t, quiets[0].Raw); got != companion {
				t.Fatalf("commentary=%q, want stamped companion prose", got)
			}
		})
	}

	t.Run("explicit_communicate_update_survives_alongside_quiet_companion", func(t *testing.T) {
		t.Parallel()
		turnID := "turn_update_and_quiet"
		updateState := json.RawMessage(`{"communicate_accepted":true,"communicate_end_turn":false}`)
		items := projectQuietFixture(t, []schema.Turn{
			{Format: schema.TurnFormatIdentity, TurnID: turnID, TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("start"), Timestamp: ts},
			{Format: schema.TurnFormatIdentity, TurnID: turnID, RoundID: "r1", Kind: schema.TurnAssistant, QuietCompanion: true, Timestamp: ts.Add(time.Second), Message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{
					{Kind: llm.ContentText, Text: "Working on X"},
					{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_msg", Name: "communicate", Arguments: json.RawMessage(`{"message":"Working on X","next":"send_and_continue"}`)}},
					{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_quiet", Name: "communicate", Arguments: quietYieldArgs}},
				},
			}},
			{Format: schema.TurnFormatIdentity, TurnID: turnID, Kind: schema.TurnCommunicate, Timestamp: ts.Add(2 * time.Second), Communicate: &schema.CommunicateInfo{
				CallID: "call_msg", EndTurn: false, Message: "Working on X",
			}},
			{Format: schema.TurnFormatIdentity, TurnID: turnID, RoundID: "r1", Kind: schema.TurnToolResults, Timestamp: ts.Add(3 * time.Second), Message: llm.Message{
				Role: llm.RoleTool,
				Content: []llm.ContentPart{
					{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_msg", Name: "communicate", Content: `{"accepted":true,"end_turn":false}`, ToolState: updateState}},
					{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_quiet", Name: "communicate", Content: `{"accepted":true,"end_turn":true}`, ToolState: quietToolState(t, "waiting on CI", "Working on X")}},
				},
			}},
			{Format: schema.TurnFormatIdentity, TurnID: turnID, Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}},
		})
		var msgs, quiets []appwire.ThreadItem
		for _, it := range items {
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

	t.Run("healed_communicate_update_survives_alongside_quiet_companion", func(t *testing.T) {
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
				args := json.RawMessage(`{}`)
				if tc.format == schema.TurnFormatIdentity {
					args = quietYieldArgs
				}
				entries := []schema.Turn{
					{Format: tc.format, TurnID: turnID, TurnKind: schema.TurnSpanExecution, Authorship: tc.authorship, Kind: schema.TurnUserInput, Message: llm.User("start"), Timestamp: ts},
					{Format: tc.format, TurnID: turnID, RoundID: "r1", Authorship: tc.authorship, Kind: schema.TurnAssistant, QuietCompanion: true, Timestamp: ts.Add(time.Second), Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentText, Text: "Working on X"},
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_healed", Name: "communicate", Arguments: json.RawMessage(`{}`), RawArguments: `{"message":"Working on X","next":"send_and_continue"}`}},
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_quiet", Name: "communicate", Arguments: args}},
						},
					}},
					{Format: tc.format, TurnID: turnID, RoundID: "r1", Authorship: tc.authorship, Kind: schema.TurnToolResults, Timestamp: ts.Add(2 * time.Second), Message: llm.Message{
						Role: llm.RoleTool,
						Content: []llm.ContentPart{
							{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_healed", Name: "communicate", Content: `{"accepted":true,"end_turn":false}`, ToolState: updateState}},
							{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_quiet", Name: "communicate", Content: `{"accepted":true,"end_turn":true}`, ToolState: quietToolState(t, "waiting on CI", "Working on X")}},
						},
					}},
				}
				if tc.format == schema.TurnFormatIdentity {
					entries = append(entries, schema.Turn{Format: tc.format, TurnID: turnID, Authorship: tc.authorship, Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}})
				}
				var msgs []appwire.ThreadItem
				for _, it := range projectQuietFixture(t, entries) {
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

	t.Run("companion_prose_stays_suppressed_when_quiet_call_never_settles", func(t *testing.T) {
		t.Parallel()
		t.Run("preval_rejection", func(t *testing.T) {
			t.Parallel()
			turnID := "turn_preval_quiet"
			inheritedAuthor := &schema.TranscriptAuthorship{Inherited: true, Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "root-session"}}
			items := projectQuietFixture(t, []schema.Turn{
				{Format: schema.TurnFormatIdentity, TurnID: turnID, TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("start"), Timestamp: ts},
				{Format: schema.TurnFormatIdentity, TurnID: turnID, RoundID: "r1", Authorship: inheritedAuthor.Clone(), Kind: schema.TurnAssistant, QuietCompanion: true, Timestamp: ts.Add(time.Second), Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						{Kind: llm.ContentText, Text: "Companion before preval rejection"},
						{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_q_preval", Name: "communicate", Arguments: json.RawMessage(`{"message":"Waiting","next":"log_quietly_and_end_turn","output":{"data":{"answer":1}}}`)}},
					},
				}},
				{Format: schema.TurnFormatIdentity, TurnID: turnID, RoundID: "r1", Kind: schema.TurnToolResults, Timestamp: ts.Add(2 * time.Second), Message: llm.Message{
					Role: llm.RoleTool,
					Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
						ToolCallID: "call_q_preval", Name: "communicate", IsError: true, PrevalOnly: true, Content: "cannot end turn",
					}}},
				}},
				{Format: schema.TurnFormatIdentity, TurnID: turnID, Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}},
			})
			var msgs, errs []appwire.ThreadItem
			for _, it := range items {
				if it.Type == "agentMessage" {
					msgs = append(msgs, it)
				}
				if it.Type == "commandExecution" && it.PrevalOnly {
					errs = append(errs, it)
				}
			}
			if len(msgs) != 0 {
				t.Fatalf("agentMessages=%+v, want companion prose suppressed after the pre-validation rejection", msgs)
			}
			if len(errs) != 1 || errs[0].CallID != "call_q_preval" || errs[0].Status != appwire.TurnStatusFailed {
				t.Fatalf("errs=%+v, want one failed pre-validation item for call_q_preval", errs)
			}
		})

		t.Run("runtime_rejection", func(t *testing.T) {
			t.Parallel()
			turnID := "turn_runtime_quiet"
			inheritedAuthor := &schema.TranscriptAuthorship{Inherited: true, Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "root-session"}}
			items := projectQuietFixture(t, []schema.Turn{
				{Format: schema.TurnFormatIdentity, TurnID: turnID, TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("start"), Timestamp: ts},
				{Format: schema.TurnFormatIdentity, TurnID: turnID, RoundID: "r1", Authorship: inheritedAuthor.Clone(), Kind: schema.TurnAssistant, QuietCompanion: true, Timestamp: ts.Add(time.Second), Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						{Kind: llm.ContentText, Text: "Companion before runtime rejection"},
						{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_q_runtime", Name: "communicate", Arguments: quietYieldArgs}},
					},
				}},
				{Format: schema.TurnFormatIdentity, TurnID: turnID, RoundID: "r1", Kind: schema.TurnToolResults, Timestamp: ts.Add(2 * time.Second), Message: llm.Message{
					Role: llm.RoleTool,
					Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
						ToolCallID: "call_q_runtime", Name: "communicate", IsError: true, PrevalOnly: false, Content: "no wake source",
					}}},
				}},
				{Format: schema.TurnFormatIdentity, TurnID: turnID, RoundID: "r2", Authorship: inheritedAuthor.Clone(), Kind: schema.TurnAssistant, Timestamp: ts.Add(3 * time.Second), Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_final", Name: "communicate", Arguments: json.RawMessage(`{"message":"Final answer","next":"send_and_end_turn"}`)}},
					},
				}},
				{Format: schema.TurnFormatIdentity, TurnID: turnID, Kind: schema.TurnCommunicate, Timestamp: ts.Add(4 * time.Second), Communicate: &schema.CommunicateInfo{
					CallID: "call_final", EndTurn: true, Message: "Final answer",
				}},
				{Format: schema.TurnFormatIdentity, TurnID: turnID, Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}},
			})
			var msgs, tools []appwire.ThreadItem
			for _, it := range items {
				if it.Type == "agentMessage" {
					msgs = append(msgs, it)
				}
				if it.Type == "commandExecution" {
					tools = append(tools, it)
				}
			}
			if len(msgs) != 1 || msgs[0].Text != "Final answer" || msgs[0].CallID != "call_final" {
				t.Fatalf("agentMessages=%+v, want only the round 2 reply", msgs)
			}
			if len(tools) != 0 {
				t.Fatalf("tool items=%+v, want the runtime rejection kept out of history", tools)
			}
		})

		t.Run("interrupted_completion_without_result", func(t *testing.T) {
			t.Parallel()
			turnID := "turn_interrupted_quiet"
			items := projectQuietFixture(t, []schema.Turn{
				{Format: schema.TurnFormatIdentity, TurnID: turnID, TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("start"), Timestamp: ts},
				{Format: schema.TurnFormatIdentity, TurnID: turnID, RoundID: "r1", Authorship: author.Clone(), Kind: schema.TurnAssistant, QuietCompanion: true, Timestamp: ts.Add(time.Second), Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						{Kind: llm.ContentText, Text: "Companion before crash"},
						{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_q_crash", Name: "communicate", Arguments: quietYieldArgs}},
					},
				}},
				{Format: schema.TurnFormatIdentity, TurnID: turnID, Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnInterrupted}},
			})
			for _, it := range items {
				if it.Type == "agentMessage" {
					t.Fatalf("agentMessage=%+v, want companion prose suppressed on an interrupted quiet turn", it)
				}
			}
		})
	})

	t.Run("hook_supplied_message_and_companion_prose_stay_visible", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name      string
			prose     string
			hookReply string
			wantTexts []string
		}{
			{name: "distinct", prose: "Distinct companion prose", hookReply: "Hook supplied reply", wantTexts: []string{"Distinct companion prose", "Hook supplied reply"}},
			// The hook's reply repeats the assistant prose, so the echo rule
			// renders the text once.
			{name: "matching", prose: "Same hook reply", hookReply: "Same hook reply", wantTexts: []string{"Same hook reply"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				turnID := "turn_hook_supplied"
				deliveredState := json.RawMessage(`{"communicate_accepted":true,"communicate_end_turn":true}`)
				items := projectQuietFixture(t, []schema.Turn{
					{Format: schema.TurnFormatIdentity, TurnID: turnID, TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("go"), Timestamp: ts},
					{Format: schema.TurnFormatIdentity, TurnID: turnID, RoundID: "r1", Authorship: author.Clone(), Kind: schema.TurnAssistant, Timestamp: ts.Add(time.Second), Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentText, Text: tc.prose},
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_q", Name: "communicate", Arguments: json.RawMessage(`{"message":"original","next":"send_and_end_turn"}`)}},
						},
					}},
					{Format: schema.TurnFormatIdentity, TurnID: turnID, Kind: schema.TurnCommunicate, Timestamp: ts.Add(1500 * time.Millisecond), Communicate: &schema.CommunicateInfo{CallID: "call_q", EndTurn: true, Message: tc.hookReply}},
					{Format: schema.TurnFormatIdentity, TurnID: turnID, RoundID: "r1", Kind: schema.TurnToolResults, Timestamp: ts.Add(2 * time.Second), Message: llm.Message{
						Role:    llm.RoleTool,
						Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_q", Name: "communicate", Content: `{"accepted":true,"end_turn":true}`, ToolState: deliveredState}}},
					}},
					{Format: schema.TurnFormatIdentity, TurnID: turnID, Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}},
				})
				var got []string
				for _, it := range items {
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
}

func TestQuietYieldNoteAndNextEnum(t *testing.T) {
	t.Parallel()
	ts := time.Unix(1_790_000_000, 0).UTC()
	author := &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "root-session"}}

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
					state := json.RawMessage(`{"communicate_accepted":true,"communicate_end_turn":true,"communicate_quiet":true,"communicate_quiet_commentary":"companion-prose"` + tc.noteJSON + `}`)
					args := json.RawMessage(`{}`)
					if format == schema.TurnFormatIdentity {
						args = quietYieldArgs
					}
					entries := []schema.Turn{
						{Format: format, TurnID: turnID, TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("wait"), Timestamp: ts},
						{Format: format, TurnID: turnID, RoundID: "r1", Authorship: author.Clone(), Kind: schema.TurnAssistant, QuietCompanion: true, Timestamp: ts.Add(time.Second), Message: llm.Message{
							Role: llm.RoleAssistant,
							Content: []llm.ContentPart{
								{Kind: llm.ContentText, Text: "companion-prose"},
								{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_q", Name: "communicate", Arguments: args}},
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
					var quiets []appwire.ThreadItem
					for _, it := range projectQuietFixture(t, entries) {
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
					Role:    llm.RoleAssistant,
					Content: []llm.ContentPart{{Kind: llm.ContentText, Text: longMultiline}},
				}},
			}
			if format == schema.TurnFormatIdentity {
				entries = append(entries, schema.Turn{Format: format, TurnID: turnID, Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}})
			}
			var reasonings, quiets []appwire.ThreadItem
			for _, it := range projectQuietFixture(t, entries) {
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

// A quiet yield whose call was recorded with empty JSON arguments (a legacy
// communicate({}) or a PreToolUse-rewritten call) still shows the assistant
// authorship: the quiet notice carries no message of its own, so authorship
// comes from the announcing assistant entry, not from the arguments.
func TestQuietYieldKeepsAuthorshipForEmptyCallArguments(t *testing.T) {
	t.Parallel()
	ts := time.Unix(1_790_000_000, 0).UTC()
	author := &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "root-session"}}
	for _, format := range []int{0, schema.TurnFormatIdentity} {
		name := "legacy"
		if format == schema.TurnFormatIdentity {
			name = "identity"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			entries := []schema.Turn{
				{Format: format, TurnID: "turn_q", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("wait"), Timestamp: ts},
				{Format: format, TurnID: "turn_q", RoundID: "r1", Authorship: author.Clone(), Kind: schema.TurnAssistant, Timestamp: ts.Add(time.Second), Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_q", Name: "communicate", Arguments: json.RawMessage(`{}`)}},
					},
				}},
				{Format: format, TurnID: "turn_q", RoundID: "r1", Kind: schema.TurnToolResults, Timestamp: ts.Add(2 * time.Second), Message: llm.Message{
					Role:    llm.RoleTool,
					Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_q", Name: "communicate", ToolState: quietToolState(t, "waiting", "")}}},
				}},
			}
			if format == schema.TurnFormatIdentity {
				entries = append(entries, schema.Turn{Format: format, TurnID: "turn_q", Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}})
			}
			var quiets []appwire.ThreadItem
			for _, it := range projectQuietFixture(t, entries) {
				if it.EventKind == appwire.ThreadItemEventKindQuietYield {
					quiets = append(quiets, it)
				}
			}
			if len(quiets) != 1 {
				t.Fatalf("quiets=%+v, want 1", quiets)
			}
			if quiets[0].Authorship == nil || quiets[0].Authorship.Author.SessionRef != "local:root-session" {
				t.Fatalf("quiet yield Authorship=%+v, want local:root-session", quiets[0].Authorship)
			}
		})
	}
}

// A communicate delivery that repeats the announcing entry's prose is an
// echo even when that prose carries surrounding whitespace or is followed by
// a whitespace-only text part after the call.
func TestAssistantEchoIgnoresTrailingWhitespace(t *testing.T) {
	t.Parallel()
	ts := time.Unix(1_790_000_000, 0).UTC()
	call := llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
		ID: "call_msg", Name: "communicate", Arguments: json.RawMessage(`{"message":"All done.","next":"send_and_end_turn"}`),
	}}
	for _, tc := range []struct {
		name    string
		content []llm.ContentPart
	}{
		{
			name: "trailing_newline",
			content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "All done.\n"},
				call,
			},
		},
		{
			name: "trailing_whitespace_part",
			content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "All done."},
				call,
				{Kind: llm.ContentText, Text: " \n "},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			items := projectQuietFixture(t, []schema.Turn{
				{Format: schema.TurnFormatIdentity, TurnID: "turn_1", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("go"), Timestamp: ts},
				{Format: schema.TurnFormatIdentity, TurnID: "turn_1", RoundID: "r1", Kind: schema.TurnAssistant, Timestamp: ts.Add(time.Second), Message: llm.Message{
					Role:    llm.RoleAssistant,
					Content: tc.content,
				}},
				{Format: schema.TurnFormatIdentity, TurnID: "turn_1", Kind: schema.TurnCommunicate, Timestamp: ts.Add(1500 * time.Millisecond), Communicate: &schema.CommunicateInfo{
					CallID: "call_msg", EndTurn: true, Message: "All done.",
				}},
			})
			shown := 0
			for _, it := range items {
				if it.Type != "agentMessage" {
					continue
				}
				if it.CallID == "call_msg" {
					t.Fatalf("agentMessage=%+v, want the delivery suppressed as an echo", it)
				}
				if strings.TrimSpace(it.Text) == "All done." {
					shown++
				}
			}
			if shown != 1 {
				t.Fatalf("items=%+v, want the prose shown once", items)
			}
		})
	}
}

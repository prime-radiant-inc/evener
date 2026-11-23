package transcriptindex

import (
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

func quietYieldRawCommentary(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var payload struct {
		Commentary string `json:"commentary"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode quiet_yield raw %s: %v", raw, err)
	}
	return payload.Commentary
}

func quietYieldFixture(withCompanion bool) fixture {
	quietState := json.RawMessage(`{"communicate_accepted":true,"communicate_end_turn":true,"communicate_quiet":true}`)
	author := &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "root-session"}}
	content := []llm.ContentPart{
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "quiet-thought-sentinel"}},
	}
	if withCompanion {
		content = append(content, llm.ContentPart{Kind: llm.ContentText, Text: "companion-sentinel"})
	}
	content = append(content,
		llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_sibling", Name: "job_list", Arguments: json.RawMessage(`{}`)}},
		llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_quiet", Name: "communicate", Arguments: json.RawMessage(`{}`)}},
	)
	return fixture{
		name: "quiet-yield",
		lines: []fixtureLine{
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("go")}, 1)),
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", RoundID: "r_1", Kind: schema.TurnAssistant, Message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{
					{Kind: llm.ContentText, Text: "earlier-round-sentinel"},
					{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_watch", Name: "job_watch", Arguments: json.RawMessage(`{}`)}},
				},
			}}, 2)),
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", RoundID: "r_1", Kind: schema.TurnToolResults, Message: llm.Message{
				Role:    llm.RoleTool,
				Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_watch", Name: "job_watch", Content: "ok"}}},
			}}, 3)),
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", RoundID: "r_2", Authorship: author, Kind: schema.TurnAssistant, Message: llm.Message{
				Role:    llm.RoleAssistant,
				Content: content,
			}}, 4)),
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", RoundID: "r_2", Kind: schema.TurnToolResults, Message: llm.Message{
				Role: llm.RoleTool,
				Content: []llm.ContentPart{
					{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_sibling", Name: "job_list", Content: "none"}},
					{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_quiet", Name: "communicate", ToolState: quietState}},
				},
			}}, 5)),
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}}, 6)),
		},
	}
}

func TestQuietYieldIndexParityColdAndIncremental(t *testing.T) {
	t.Parallel()
	for _, withCompanion := range []bool{false, true} {
		name := "clean"
		if withCompanion {
			name = "companion"
		}
		t.Run(name+"/cold_equals_apptranscript", func(t *testing.T) {
			t.Parallel()
			fx := quietYieldFixture(withCompanion)
			path := writeFixture(t, fx)
			dir := t.TempDir()
			x := openIndex(t, path, dir)
			w, err := x.Latest(40)
			if err != nil {
				t.Fatal(err)
			}
			ref := referenceCandidates(t, path)
			if !reflect.DeepEqual(w.Candidates, ref) {
				t.Fatalf("index candidates diverge from reference:\nindex: %s\nref:   %s", dump(w.Candidates), dump(ref))
			}

			reg := apptranscript.NewToolCallRegistry()
			fullTurns, err := apptranscript.ItemTurnsFromFile(path, transcript.DefaultMaxLineBytes, func(turn schema.Turn, id string, idx int) []appwire.ThreadItem {
				items, _ := apptranscript.ProjectEntryParts(id, idx, turn, reg, apptranscript.AddressedImageProjector, apptranscript.ToolResultOutputImages)
				return items
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(fullTurns) != 1 || len(fullTurns[0].Items) != len(w.Candidates) {
				t.Fatalf("fullTurns=%d items=%d, index=%d", len(fullTurns), len(fullTurns[0].Items), len(w.Candidates))
			}
			last := w.Candidates[len(w.Candidates)-1].Item
			if last.Type != "systemMessage" || last.EventKind != appwire.ThreadItemEventKindQuietYield {
				t.Fatalf("last item=%+v, want quiet_yield systemMessage", last)
			}
			if last.Authorship == nil || last.Authorship.Author.SessionRef != "local:root-session" {
				t.Fatalf("Authorship=%+v, want local:root-session", last.Authorship)
			}
			if withCompanion {
				if got := quietYieldRawCommentary(t, last.Raw); got != "companion-sentinel" {
					t.Fatalf("commentary=%q, want companion-sentinel", got)
				}
				for _, c := range w.Candidates {
					if c.Item.Type == "agentMessage" && c.Item.Text == "companion-sentinel" {
						t.Fatalf("companion prose still rendered as agentMessage: %+v", c.Item)
					}
				}
			}
		})
	}

	t.Run("split_catchup_rotates_incarnation_and_demotes", func(t *testing.T) {
		t.Parallel()
		fx := quietYieldFixture(true)
		path, lines := writeHeaderOnly(t, fx)
		for _, line := range lines[:4] {
			appendBytes(t, path, line)
		}
		dir := t.TempDir()
		x := openIndex(t, path, dir)
		before, err := x.Latest(40)
		if err != nil {
			t.Fatal(err)
		}
		sawCompanionBefore := false
		for _, c := range before.Candidates {
			if c.Item.Type == "agentMessage" && c.Item.Text == "companion-sentinel" {
				sawCompanionBefore = true
			}
		}
		if !sawCompanionBefore {
			t.Fatalf("before TurnToolResults, expected provisional companion agentMessage, got %+v", before.Candidates)
		}
		for _, line := range lines[4:] {
			appendBytes(t, path, line)
		}
		catchUp(t, x)
		after, err := x.Latest(40)
		if err != nil {
			t.Fatal(err)
		}
		if after.Incarnation == before.Incarnation {
			t.Fatalf("split CatchUpTo kept incarnation %q; want rotated incarnation so live clients discard demoted agentMessage", after.Incarnation)
		}
		ref := referenceCandidates(t, path)
		if !reflect.DeepEqual(after.Candidates, ref) {
			t.Fatalf("after split catchup diverges from reference:\nindex: %s\nref:   %s", dump(after.Candidates), dump(ref))
		}
	})

	t.Run("batch_catchup_preserves_incarnation_and_demotes", func(t *testing.T) {
		t.Parallel()
		fx := quietYieldFixture(true)
		path, lines := writeHeaderOnly(t, fx)
		for _, line := range lines[:3] {
			appendBytes(t, path, line)
		}
		dir := t.TempDir()
		x := openIndex(t, path, dir)
		before, err := x.Latest(40)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range lines[3:] {
			appendBytes(t, path, line)
		}
		catchUp(t, x)
		after, changes, err := x.LatestSince(40, appwire.SnapshotIdentity{Incarnation: before.Incarnation, Length: before.Length})
		if err != nil {
			t.Fatal(err)
		}
		if after.Incarnation != before.Incarnation || changes == nil {
			t.Fatalf("batch CatchUpTo rotated incarnation (%q -> %q) unnecessarily", before.Incarnation, after.Incarnation)
		}
		for _, c := range changes.Items {
			if c.Item.Type == "agentMessage" && c.Item.Text == "companion-sentinel" {
				t.Fatalf("ChangedSince emitted demoted companion agentMessage: %+v", c.Item)
			}
		}
		ref := referenceCandidates(t, path)
		if !reflect.DeepEqual(after.Candidates, ref) {
			t.Fatalf("after batch catchup diverges from reference:\nindex: %s\nref:   %s", dump(after.Candidates), dump(ref))
		}
	})

	t.Run("readat_cut_between_assistant_and_quiet_result_keeps_pre_cut_message", func(t *testing.T) {
		t.Parallel()
		fx := quietYieldFixture(true)
		path, lines := writeHeaderOnly(t, fx)
		for _, line := range lines[:4] {
			appendBytes(t, path, line)
		}
		dir := t.TempDir()
		x := openIndex(t, path, dir)
		preCut, err := x.Latest(40)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range lines[4:] {
			appendBytes(t, path, line)
		}
		catchUp(t, x)
		var cutWindow Window
		if err := x.ReadAt(preCut.Length, func(s *Snapshot) error {
			var readErr error
			cutWindow, readErr = s.Latest(40)
			return readErr
		}); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(cutWindow.Candidates, preCut.Candidates) {
			t.Fatalf("historical cut diverges from pre-result state:\ncut: %s\npre: %s", dump(cutWindow.Candidates), dump(preCut.Candidates))
		}
	})

	t.Run("historical_cut_fallback_replay_handles_settled_quiet_companion", func(t *testing.T) {
		t.Parallel()
		fx := quietYieldFixture(true)
		fx.lines = append(fx.lines,
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_2", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("next")}, 7)),
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_2", RoundID: "r_3", Kind: schema.TurnAssistant, Message: llm.Message{
				Role:    llm.RoleAssistant,
				Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_nameless", Name: "job_list", Arguments: json.RawMessage(`{}`)}}},
			}}, 8)),
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_2", RoundID: "r_3", Kind: schema.TurnToolResults, Message: llm.Message{
				Role:    llm.RoleTool,
				Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_nameless", Content: "ok"}}},
			}}, 9)),
		)
		path, lines := writeHeaderOnly(t, fx)
		for _, line := range lines[:8] {
			appendBytes(t, path, line)
		}
		x := openIndex(t, path, t.TempDir())
		preCut, err := x.Latest(40)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range lines[8:] {
			appendBytes(t, path, line)
		}
		catchUp(t, x)
		var cutWindow Window
		if err := x.ReadAt(preCut.Length, func(s *Snapshot) error {
			var readErr error
			cutWindow, readErr = s.Latest(40)
			return readErr
		}); err != nil {
			t.Fatalf("ReadAt fallback failed: %v", err)
		}
		if !reflect.DeepEqual(cutWindow.Candidates, preCut.Candidates) {
			t.Fatalf("historical cut diverges from expected:\ncut: %s\npre: %s", dump(cutWindow.Candidates), dump(preCut.Candidates))
		}
	})

	t.Run("explicit_communicate_update_survives_cold_incremental_and_cut", func(t *testing.T) {
		t.Parallel()
		updateState := json.RawMessage(`{"communicate_accepted":true,"communicate_end_turn":false}`)
		quietState := json.RawMessage(`{"communicate_accepted":true,"communicate_end_turn":true,"communicate_quiet":true}`)
		fx := fixture{
			name: "quiet-update-echo",
			lines: []fixtureLine{
				entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("go")}, 1)),
				entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", RoundID: "r_1", Kind: schema.TurnAssistant, Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						{Kind: llm.ContentText, Text: "Working on X"},
						{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_msg", Name: "communicate", Arguments: json.RawMessage(`{"message":"Working on X","end_turn":false}`)}},
						{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_quiet", Name: "communicate", Arguments: json.RawMessage(`{}`)}},
					},
				}}, 2)),
				entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", Kind: schema.TurnCommunicate, Communicate: &schema.CommunicateInfo{
					CallID: "call_msg", EndTurn: false, Message: "Working on X",
				}}, 3)),
				entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", RoundID: "r_1", Kind: schema.TurnToolResults, Message: llm.Message{
					Role: llm.RoleTool,
					Content: []llm.ContentPart{
						{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_msg", Name: "communicate", Content: `{"accepted":true,"end_turn":false}`, ToolState: updateState}},
						{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_quiet", Name: "communicate", Content: `{"accepted":true,"end_turn":true}`, ToolState: quietState}},
					},
				}}, 4)),
				entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}}, 5)),
			},
		}
		coldPath := writeFixture(t, fx)
		cold := openIndex(t, coldPath, t.TempDir())
		coldWin, err := cold.Latest(40)
		if err != nil {
			t.Fatal(err)
		}
		ref := referenceCandidates(t, coldPath)
		if !reflect.DeepEqual(coldWin.Candidates, ref) {
			t.Fatalf("cold diverges from reference:\ncold: %s\nref:  %s", dump(coldWin.Candidates), dump(ref))
		}
		var msgs, quiets []appwire.ThreadItem
		for _, cand := range coldWin.Candidates {
			if cand.Item.Type == "agentMessage" {
				msgs = append(msgs, cand.Item)
			}
			if cand.Item.EventKind == appwire.ThreadItemEventKindQuietYield {
				quiets = append(quiets, cand.Item)
			}
		}
		if len(msgs) != 1 || msgs[0].Text != "Working on X" || msgs[0].CallID != "call_msg" || msgs[0].Observation == nil || msgs[0].Observation.EndTurn {
			t.Fatalf("agentMessages=%+v, want single Working on X update with CallID=call_msg EndTurn=false", msgs)
		}
		if len(quiets) != 1 || quietYieldRawCommentary(t, quiets[0].Raw) != "Working on X" {
			t.Fatalf("quiets=%+v, want quiet_yield with commentary", quiets)
		}

		incPath, lines := writeHeaderOnly(t, fx)
		for _, line := range lines[:3] {
			appendBytes(t, incPath, line)
		}
		inc := openIndex(t, incPath, t.TempDir())
		preCut, err := inc.Latest(40)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range lines[3:] {
			appendBytes(t, incPath, line)
		}
		catchUp(t, inc)
		incWin, err := inc.Latest(40)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(incWin.Candidates, coldWin.Candidates) {
			t.Fatalf("incremental diverges from cold:\ninc:  %s\ncold: %s", dump(incWin.Candidates), dump(coldWin.Candidates))
		}
		var cutWin Window
		if err := inc.ReadAt(preCut.Length, func(s *Snapshot) error {
			var readErr error
			cutWin, readErr = s.Latest(40)
			return readErr
		}); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(cutWin.Candidates, preCut.Candidates) {
			t.Fatalf("historical cut before quiet result diverges from preCut:\ncut: %s\npre: %s", dump(cutWin.Candidates), dump(preCut.Candidates))
		}
	})
}

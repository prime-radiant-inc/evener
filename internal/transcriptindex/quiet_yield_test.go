package transcriptindex

import (
	"encoding/json"
	"reflect"
	"strings"
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
	quiet := true
	commentary := ""
	if withCompanion {
		commentary = "companion-sentinel"
	}
	state, err := json.Marshal(schema.CommunicateToolState{
		Accepted: &quiet, EndTurn: &quiet, Quiet: &quiet, QuietCommentary: commentary,
	})
	if err != nil {
		panic(err)
	}
	quietState := json.RawMessage(state)
	author := &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "root-session"}}
	content := []llm.ContentPart{
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "quiet-thought-sentinel"}},
	}
	if withCompanion {
		content = append(content, llm.ContentPart{Kind: llm.ContentText, Text: "companion-sentinel"})
	}
	content = append(content,
		llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_sibling", Name: "job_list", Arguments: json.RawMessage(`{}`)}},
		llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
			ID: "call_quiet", Name: "communicate",
			Arguments: json.RawMessage(`{"message":"waiting on CI","next":"log_quietly_and_end_turn"}`),
		}},
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
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", RoundID: "r_2", Authorship: author, Kind: schema.TurnAssistant, QuietCompanion: withCompanion, Message: llm.Message{
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

	t.Run("split_catchup_keeps_companion_suppressed_and_preserves_incarnation", func(t *testing.T) {
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
		for _, c := range before.Candidates {
			if c.Item.Type == "agentMessage" && c.Item.Text == "companion-sentinel" {
				t.Fatalf("before TurnToolResults, companion prose must stay suppressed, got %+v", c.Item)
			}
		}
		for _, line := range lines[4:] {
			appendBytes(t, path, line)
		}
		catchUp(t, x)
		after, changes, err := x.LatestSince(40, appwire.SnapshotIdentity{Incarnation: before.Incarnation, Length: before.Length})
		if err != nil {
			t.Fatal(err)
		}
		if after.Incarnation != before.Incarnation || changes == nil {
			t.Fatalf("split CatchUpTo rotated incarnation (%q -> %q) or lost incremental changes", before.Incarnation, after.Incarnation)
		}
		ref := referenceCandidates(t, path)
		if !reflect.DeepEqual(after.Candidates, ref) {
			t.Fatalf("after split catchup diverges from reference:\nindex: %s\nref:   %s", dump(after.Candidates), dump(ref))
		}
	})

	t.Run("batch_catchup_preserves_incarnation_and_suppresses_prose", func(t *testing.T) {
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
				t.Fatalf("ChangedSince emitted suppressed companion agentMessage: %+v", c.Item)
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
		quietState := json.RawMessage(`{"communicate_accepted":true,"communicate_end_turn":true,"communicate_quiet":true,"communicate_quiet_commentary":"Working on X"}`)
		fx := fixture{
			name: "quiet-update-echo",
			lines: []fixtureLine{
				entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("go")}, 1)),
				entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", RoundID: "r_1", Kind: schema.TurnAssistant, QuietCompanion: true, Message: llm.Message{
					Role: llm.RoleAssistant,
					Content: []llm.ContentPart{
						{Kind: llm.ContentText, Text: "Working on X"},
						{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_msg", Name: "communicate", Arguments: json.RawMessage(`{"message":"Working on X","next":"send_and_continue"}`)}},
						{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_quiet", Name: "communicate", Arguments: json.RawMessage(`{"message":"waiting","next":"log_quietly_and_end_turn"}`)}},
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

	t.Run("split_catchup_keeps_companion_suppressed_on_error_result_without_rotation", func(t *testing.T) {
		t.Parallel()
		fx := quietYieldFixture(true)
		fx.lines[3].turn.Authorship = &schema.TranscriptAuthorship{Inherited: true, Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "root-session"}}
		fx.lines[4].turn.Message.Content[1] = llm.ContentPart{
			Kind: llm.ContentToolResult,
			ToolResult: &llm.ToolResultData{
				ToolCallID: "call_quiet", Name: "communicate", IsError: true, PrevalOnly: true, Content: "cannot end turn",
			},
		}
		path, lines := writeHeaderOnly(t, fx)
		for _, line := range lines[:4] {
			appendBytes(t, path, line)
		}
		x := openIndex(t, path, t.TempDir())
		before, err := x.Latest(40)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range before.Candidates {
			if c.Item.Type == "agentMessage" && c.Item.Text == "companion-sentinel" {
				t.Fatalf("before error result, companion prose must stay suppressed, got %+v", c.Item)
			}
		}
		for _, line := range lines[4:] {
			appendBytes(t, path, line)
		}
		catchUp(t, x)
		after, changes, err := x.LatestSince(40, appwire.SnapshotIdentity{Incarnation: before.Incarnation, Length: before.Length})
		if err != nil {
			t.Fatal(err)
		}
		if after.Incarnation != before.Incarnation || changes == nil {
			t.Fatalf("error CatchUpTo rotated incarnation (%q -> %q)", before.Incarnation, after.Incarnation)
		}
		ref := referenceCandidates(t, path)
		if !reflect.DeepEqual(after.Candidates, ref) {
			t.Fatalf("after error catchup diverges from reference:\nindex: %s\nref:   %s", dump(after.Candidates), dump(ref))
		}
		var sawProse, sawErr bool
		for _, c := range after.Candidates {
			if c.Item.Type == "agentMessage" && c.Item.Text == "companion-sentinel" {
				sawProse = true
			}
			if c.Item.Type == "commandExecution" && c.Item.CallID == "call_quiet" && c.Item.PrevalOnly {
				sawErr = true
			}
		}
		if sawProse || !sawErr {
			t.Fatalf("sawProse=%v sawErr=%v, want suppressed prose and the preval error item", sawProse, sawErr)
		}
	})

	t.Run("companion_stays_suppressed_on_turn_completion_without_result", func(t *testing.T) {
		t.Parallel()
		fx := quietYieldFixture(true)
		fx.lines = append(fx.lines[:4], entryLine(at(schema.Turn{
			Format: schema.TurnFormatIdentity, TurnID: "turn_1", Kind: schema.TurnCompletion,
			Completion: &schema.TurnCompletionInfo{Status: schema.TurnInterrupted},
		}, 5)))
		path, lines := writeHeaderOnly(t, fx)
		for _, line := range lines[:4] {
			appendBytes(t, path, line)
		}
		x := openIndex(t, path, t.TempDir())
		before, err := x.Latest(40)
		if err != nil {
			t.Fatal(err)
		}
		appendBytes(t, path, lines[4])
		catchUp(t, x)
		after, changes, err := x.LatestSince(40, appwire.SnapshotIdentity{Incarnation: before.Incarnation, Length: before.Length})
		if err != nil {
			t.Fatal(err)
		}
		if after.Incarnation != before.Incarnation || changes == nil {
			t.Fatalf("interrupted CatchUpTo rotated incarnation (%q -> %q)", before.Incarnation, after.Incarnation)
		}
		ref := referenceCandidates(t, path)
		if !reflect.DeepEqual(after.Candidates, ref) {
			t.Fatalf("after interrupted catchup diverges from reference:\nindex: %s\nref:   %s", dump(after.Candidates), dump(ref))
		}
		var sawProse bool
		for _, c := range after.Candidates {
			if c.Item.Type == "agentMessage" && c.Item.Text == "companion-sentinel" {
				sawProse = true
			}
		}
		if sawProse {
			t.Fatalf("expected companion-sentinel to stay suppressed on TurnCompletion, got %+v", after.Candidates)
		}
	})
}

func TestQuietYieldPreToolUseBlankedMessageLeavesProseVisible(t *testing.T) {
	t.Parallel()
	for _, sibling := range []bool{false, true} {
		for _, split := range []bool{false, true} {
			name := "standalone"
			if sibling {
				name = "with_sibling_update"
			}
			if split {
				name += "/split_catchup"
			} else {
				name += "/cold"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				fx := quietYieldFixture(true)
				// A PreToolUse hook rewrites a non-quiet communicate({message:"original"})
				// into a quiet yield. The producer saw a non-quiet call, so it did not
				// stamp QuietCompanion and the companion prose stays visible.
				fx.lines[3].turn.QuietCompanion = false
				fx.lines[3].turn.Message.Content[3] = llm.ContentPart{
					Kind:     llm.ContentToolCall,
					ToolCall: &llm.ToolCallData{ID: "call_quiet", Name: "communicate", Arguments: json.RawMessage(`{"message":"original","next":"send_and_end_turn"}`)},
				}
				fx.lines[4].turn.Message.Content[1].ToolResult.ToolState = json.RawMessage(`{"communicate_accepted":true,"communicate_end_turn":true,"communicate_quiet":true}`)
				if sibling {
					fx.lines[3].turn.Message.Content = append(fx.lines[3].turn.Message.Content, llm.ContentPart{
						Kind:     llm.ContentToolCall,
						ToolCall: &llm.ToolCallData{ID: "call_msg", Name: "communicate", Arguments: json.RawMessage(`{"message":"companion-sentinel","next":"send_and_continue"}`)},
					})
					comm := entryLine(at(schema.Turn{
						Format: schema.TurnFormatIdentity, TurnID: "turn_1", Kind: schema.TurnCommunicate,
						Communicate: &schema.CommunicateInfo{CallID: "call_msg", EndTurn: false, Message: "companion-sentinel"},
					}, 5))
					fx.lines[4].turn.Message.Content = append(fx.lines[4].turn.Message.Content, llm.ContentPart{
						Kind: llm.ContentToolResult,
						ToolResult: &llm.ToolResultData{
							ToolCallID: "call_msg", Name: "communicate", Content: `{"accepted":true,"end_turn":false}`,
							ToolState: json.RawMessage(`{"communicate_accepted":true,"communicate_end_turn":false}`),
						},
					})
					fx.lines = append(fx.lines[:4], append([]fixtureLine{comm}, fx.lines[4:]...)...)
				}
				path, lines := writeHeaderOnly(t, fx)
				var x *Index
				if split {
					splitAt := 4
					if sibling {
						splitAt = 5
					}
					for _, line := range lines[:splitAt] {
						appendBytes(t, path, line)
					}
					x = openIndex(t, path, t.TempDir())
					for _, line := range lines[splitAt:] {
						appendBytes(t, path, line)
					}
					catchUp(t, x)
				} else {
					for _, line := range lines {
						appendBytes(t, path, line)
					}
					x = openIndex(t, path, t.TempDir())
				}
				w, err := x.Latest(40)
				if err != nil {
					t.Fatal(err)
				}
				ref := referenceCandidates(t, path)
				if !reflect.DeepEqual(w.Candidates, ref) {
					t.Fatalf("candidates diverge from reference:\nindex: %s\nref:   %s", dump(w.Candidates), dump(ref))
				}
				var msgs, quiets int
				for _, c := range w.Candidates {
					if c.Item.Type == "agentMessage" && c.Item.Text == "companion-sentinel" {
						msgs++
					}
					if c.Item.EventKind == appwire.ThreadItemEventKindQuietYield {
						quiets++
						if len(c.Item.Raw) != 0 {
							t.Fatalf("raw=%s, want no commentary payload", c.Item.Raw)
						}
					}
				}
				if msgs != 1 {
					t.Fatalf("companion prose msgs=%d, want 1 visible bubble", msgs)
				}
				if quiets != 1 {
					t.Fatalf("quiets=%d, want 1", quiets)
				}
			})
		}
	}
}

func TestQuietYieldPreToolUseSuppliedMessageOnBlankCommunicate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		endTurn   bool
		prose     string
		hookReply string
		toolState json.RawMessage
		wantMsgs  []string
	}{
		{
			name: "distinct", endTurn: false,
			prose: "Distinct companion prose", hookReply: "Hook supplied reply",
			toolState: json.RawMessage(`{"communicate_accepted":true,"communicate_end_turn":false}`),
			wantMsgs:  []string{"Distinct companion prose", "Hook supplied reply"},
		},
		{
			name: "matching", endTurn: false,
			prose: "Same hook reply", hookReply: "Same hook reply",
			toolState: json.RawMessage(`{"communicate_accepted":true,"communicate_end_turn":false}`),
			wantMsgs:  []string{"Same hook reply"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fx := fixture{
				name: "hook-supplied-" + tc.name,
				lines: []fixtureLine{
					entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("go")}, 1)),
					entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", RoundID: "r1", Kind: schema.TurnAssistant, Message: llm.Message{
						Role: llm.RoleAssistant,
						Content: []llm.ContentPart{
							{Kind: llm.ContentText, Text: tc.prose},
							{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_q", Name: "communicate", Arguments: json.RawMessage(`{"message":"original","next":"send_and_end_turn"}`)}},
						},
					}}, 2)),
					entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", Kind: schema.TurnCommunicate, Communicate: &schema.CommunicateInfo{CallID: "call_q", EndTurn: tc.endTurn, Message: tc.hookReply}}, 3)),
					entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", RoundID: "r1", Kind: schema.TurnToolResults, Message: llm.Message{
						Role:    llm.RoleTool,
						Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_q", Name: "communicate", Content: `{"accepted":true}`, ToolState: tc.toolState}}},
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
			var got []string
			for _, c := range coldWin.Candidates {
				if c.Item.Type == "agentMessage" {
					got = append(got, c.Item.Text)
				}
			}
			if !reflect.DeepEqual(got, tc.wantMsgs) {
				t.Fatalf("cold agentMessages=%q, want %q", got, tc.wantMsgs)
			}
			// Incremental catchup split after TurnCommunicate (with reopen).
			incPath, lines := writeHeaderOnly(t, fx)
			for _, line := range lines[:3] {
				appendBytes(t, incPath, line)
			}
			dir := t.TempDir()
			inc := openIndex(t, incPath, dir)
			if err := inc.Close(); err != nil {
				t.Fatal(err)
			}
			inc = openIndex(t, incPath, dir)
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
		})
	}
}

func TestQuietYieldNextEnumNoteAndZeroIncarnationRotation(t *testing.T) {
	t.Parallel()
	const waitNote = "Waiting on CI job_123"
	quietState := json.RawMessage(`{"communicate_accepted":true,"communicate_end_turn":true,"communicate_quiet":true,"communicate_quiet_note":"Waiting on CI job_123","communicate_quiet_commentary":"companion-prose"}`)
	author := &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "root-session"}}
	fx := fixture{
		name: "quiet-next-enum-note",
		lines: []fixtureLine{
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("go")}, 1)),
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", RoundID: "r_1", Authorship: author, Kind: schema.TurnAssistant, QuietCompanion: true, Message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{
					{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "quiet-thought"}},
					{Kind: llm.ContentText, Text: "companion-prose"},
					{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
						ID:        "call_q",
						Name:      "communicate",
						Arguments: json.RawMessage(`{"message":"Waiting on CI job_123","next":"log_quietly_and_end_turn"}`),
					}},
				},
			}}, 2)),
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", RoundID: "r_1", Kind: schema.TurnToolResults, Message: llm.Message{
				Role:    llm.RoleTool,
				Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_q", Name: "communicate", ToolState: quietState}}},
			}}, 3)),
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}}, 4)),
		},
	}

	path, lines := writeHeaderOnly(t, fx)
	for _, line := range lines[:2] {
		appendBytes(t, path, line)
	}
	x := openIndex(t, path, t.TempDir())
	before, err := x.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range before.Candidates {
		if c.Item.Type == "agentMessage" && c.Item.Text == "companion-prose" {
			t.Fatalf("companion prose leaked before TurnToolResults: %+v", c.Item)
		}
	}
	for _, line := range lines[2:] {
		appendBytes(t, path, line)
	}
	catchUp(t, x)
	after, changes, err := x.LatestSince(40, appwire.SnapshotIdentity{Incarnation: before.Incarnation, Length: before.Length})
	if err != nil {
		t.Fatal(err)
	}
	if after.Incarnation != before.Incarnation || changes == nil {
		t.Fatalf("Incarnation rotated (%q -> %q) or changes nil=%v", before.Incarnation, after.Incarnation, changes == nil)
	}
	ref := referenceCandidates(t, path)
	if !reflect.DeepEqual(after.Candidates, ref) {
		t.Fatalf("index diverges from reference:\nindex: %s\nref:   %s", dump(after.Candidates), dump(ref))
	}
	var quiets int
	for _, c := range after.Candidates {
		if c.Item.Type == "agentMessage" {
			t.Fatalf("unexpected agentMessage after quiet settle: %+v", c.Item)
		}
		if c.Item.EventKind == appwire.ThreadItemEventKindQuietYield {
			quiets++
			if c.Item.Text != waitNote {
				t.Fatalf("quiet item Text=%q, want %q", c.Item.Text, waitNote)
			}
			if got := quietYieldRawCommentary(t, c.Item.Raw); got != "companion-prose" {
				t.Fatalf("commentary=%q, want companion-prose", got)
			}
		}
	}
	if quiets != 1 {
		t.Fatalf("quiets=%d, want 1", quiets)
	}
}

func TestBareTextQuietYieldAssistantParity(t *testing.T) {
	t.Parallel()
	const multiline = "Waiting on child\nSecond detail line"
	author := &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "root-session"}}
	fx := fixture{
		name: "bare-text-quiet-assistant",
		lines: []fixtureLine{
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("go")}, 1)),
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", RoundID: "r_bare_1", Authorship: author, Kind: schema.TurnAssistant, QuietYield: true, Message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{
					{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "quiet-thought"}},
					{Kind: llm.ContentText, Text: multiline},
				},
			}}, 2)),
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}}, 3)),
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_2", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("next")}, 4)),
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_2", RoundID: "r_bare_2", Authorship: author, Kind: schema.TurnAssistant, QuietYield: true, Message: llm.Message{
				Role:    llm.RoleAssistant,
				Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "Single line note"}},
			}}, 5)),
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_2", Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}}, 6)),
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_3", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("order")}, 7)),
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_3", RoundID: "r_bare_3", Authorship: author, Kind: schema.TurnAssistant, QuietYield: true, Message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{
					{Kind: llm.ContentText, Text: "Text before thought"},
					{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "trailing-thought"}},
				},
			}}, 8)),
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_3", Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}}, 9)),
		},
	}

	path, lines := writeHeaderOnly(t, fx)
	for _, line := range lines[:3] {
		appendBytes(t, path, line)
	}
	x := openIndex(t, path, t.TempDir())
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
		t.Fatalf("Incarnation rotated (%q -> %q) or changes nil=%v", before.Incarnation, after.Incarnation, changes == nil)
	}
	ref := referenceCandidates(t, path)
	if !reflect.DeepEqual(after.Candidates, ref) {
		t.Fatalf("incremental diverges from reference:\nindex: %s\nref:   %s", dump(after.Candidates), dump(ref))
	}
	cold := openIndex(t, path, t.TempDir())
	coldWin, err := cold.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(coldWin.Candidates, ref) {
		t.Fatalf("cold diverges from reference:\ncold: %s\nref:  %s", dump(coldWin.Candidates), dump(ref))
	}
	for i, c := range coldWin.Candidates {
		if _, found, err := cold.findItem(c.Position); err != nil || !found {
			t.Fatalf("candidate %d (%s) at position %+v not found by binary search (found=%v err=%v)", i, c.Item.ID, c.Position, found, err)
		}
	}
	var reasonings, quiets []appwire.ThreadItem
	for _, c := range coldWin.Candidates {
		switch {
		case c.Item.Type == "agentMessage":
			t.Fatalf("QuietYield TurnAssistant emitted agentMessage: %+v", c.Item)
		case c.Item.Type == "reasoning":
			reasonings = append(reasonings, c.Item)
		case c.Item.EventKind == appwire.ThreadItemEventKindQuietYield:
			quiets = append(quiets, c.Item)
		}
	}
	if len(reasonings) != 2 || reasonings[0].Text != "quiet-thought" || reasonings[1].Text != "trailing-thought" {
		t.Fatalf("reasonings=%+v, want [quiet-thought, trailing-thought]", reasonings)
	}
	if len(quiets) != 3 {
		t.Fatalf("quiets=%+v, want 3", quiets)
	}
	if quiets[0].RoundID != "r_bare_1" || quiets[0].Text != schema.NormalizeQuietNote(multiline) {
		t.Fatalf("first quiet=%+v, want RoundID=r_bare_1 normalized multiline", quiets[0])
	}
	if got := quietYieldRawCommentary(t, quiets[0].Raw); got != multiline {
		t.Fatalf("first commentary=%q, want %q", got, multiline)
	}
	if quiets[1].RoundID != "r_bare_2" || quiets[1].Text != "Single line note" || len(quiets[1].Raw) != 0 {
		t.Fatalf("second quiet=%+v, want RoundID=r_bare_2 Text=\"Single line note\" empty Raw", quiets[1])
	}
	if quiets[2].RoundID != "r_bare_3" || quiets[2].Text != "Text before thought" || len(quiets[2].Raw) != 0 {
		t.Fatalf("third quiet=%+v, want RoundID=r_bare_3 Text=\"Text before thought\" empty Raw", quiets[2])
	}
}

// A quiet yield whose recorded call carries empty JSON arguments (a legacy
// communicate({}) or a PreToolUse-rewritten call) keeps the announcing
// assistant entry's authorship on the projected notice, cold and after a
// split catch-up.
func TestQuietYieldEmptyCallArgumentsKeepsAuthorship(t *testing.T) {
	t.Parallel()
	author := &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "root-session"}}
	quietState := json.RawMessage(`{"communicate_accepted":true,"communicate_end_turn":true,"communicate_quiet":true,"communicate_quiet_note":"waiting"}`)
	fx := fixture{
		name: "quiet-empty-call-arguments",
		lines: []fixtureLine{
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("go")}, 1)),
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", RoundID: "r_1", Authorship: author, Kind: schema.TurnAssistant, Message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{
					{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_q", Name: "communicate", Arguments: json.RawMessage(`{}`)}},
				},
			}}, 2)),
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", RoundID: "r_1", Kind: schema.TurnToolResults, Message: llm.Message{
				Role:    llm.RoleTool,
				Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_q", Name: "communicate", ToolState: quietState}}},
			}}, 3)),
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}}, 4)),
		},
	}
	for _, split := range []bool{false, true} {
		name := "cold"
		if split {
			name = "split_catchup"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path, lines := writeHeaderOnly(t, fx)
			var x *Index
			if split {
				for _, line := range lines[:2] {
					appendBytes(t, path, line)
				}
				x = openIndex(t, path, t.TempDir())
				for _, line := range lines[2:] {
					appendBytes(t, path, line)
				}
				catchUp(t, x)
			} else {
				for _, line := range lines {
					appendBytes(t, path, line)
				}
				x = openIndex(t, path, t.TempDir())
			}
			w, err := x.Latest(40)
			if err != nil {
				t.Fatal(err)
			}
			ref := referenceCandidates(t, path)
			if !reflect.DeepEqual(w.Candidates, ref) {
				t.Fatalf("candidates diverge from reference:\nindex: %s\nref:   %s", dump(w.Candidates), dump(ref))
			}
			var quiets []appwire.ThreadItem
			for _, c := range w.Candidates {
				if c.Item.EventKind == appwire.ThreadItemEventKindQuietYield {
					quiets = append(quiets, c.Item)
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

// A quiet identity communicate call the unpublished tail announces may be new
// or a re-announcement of one the cut already holds pending, and the announce
// leaves no committed record that tells them apart, so a cut read must replay
// the prefix instead of deriving state.
func TestReadAtCutFallsBackOnAQuietIdentityCallInTheTail(t *testing.T) {
	t.Parallel()
	author := &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "root-session"}}
	quietArgs := json.RawMessage(`{"message":"waiting","next":"log_quietly_and_end_turn"}`)
	prefix := fixture{lines: []fixtureLine{
		entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("go")}, 1)),
		entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", RoundID: "r_1", Authorship: author, Kind: schema.TurnAssistant, Message: llm.Message{
			Role:    llm.RoleAssistant,
			Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_q", Name: "communicate", Arguments: quietArgs}}},
		}}, 2)),
	}}
	path, lines := writeHeaderOnly(t, prefix)
	for _, line := range lines {
		appendBytes(t, path, line)
	}
	x := openIndex(t, path, t.TempDir())
	catchUp(t, x)
	expected, err := x.Latest(3)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := x.ChangedSince(0)
	if err != nil {
		t.Fatal(err)
	}
	appendBytes(t, path, encodeEntry(t, 3, at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", RoundID: "r_1", Authorship: author, Kind: schema.TurnAssistant, Message: llm.Message{
		Role:    llm.RoleAssistant,
		Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_q", Name: "communicate", Arguments: quietArgs}}},
	}}, 3)))
	appendBytes(t, path, encodeEntry(t, 4, at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", RoundID: "r_1", Kind: schema.TurnToolResults, Message: llm.Message{
		Role: llm.RoleTool,
		Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
			ToolCallID: "call_q", Name: "communicate",
			ToolState: json.RawMessage(`{"communicate_accepted":true,"communicate_end_turn":true,"communicate_quiet":true,"communicate_quiet_note":"waiting"}`),
		}}},
	}}, 4)))
	other := openIndex(t, path, t.TempDir())
	catchUp(t, other)
	if err := x.ReadAt(expected.Length, func(s *Snapshot) error {
		latest, err := s.Latest(3)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(latest, expected) {
			t.Fatalf("cut latest mismatch:\ngot  %+v\nwant %+v", latest, expected)
		}
		gotChanges, err := s.ChangedSince(0)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(gotChanges, changes) {
			t.Fatalf("cut changes mismatch:\ngot  %+v\nwant %+v", gotChanges, changes)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// A second round that repeats the first round's text through a communicate
// call is not an echo of its own assistant entry: that entry shows no text, so
// the delivery keeps its own item and the accepted reply's evidence links to
// it.
func TestCommunicateEchoScopesToItsAwaitingEntry(t *testing.T) {
	t.Parallel()
	author := &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "root-session"}}
	fx := fixture{
		name: "two-round-echo",
		lines: []fixtureLine{
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("go")}, 1)),
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", RoundID: "r_1", Authorship: author, Kind: schema.TurnAssistant, Message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{
					{Kind: llm.ContentText, Text: "Checking now"},
					{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_shell", Name: "shell", Arguments: json.RawMessage(`{"command":"date"}`)}},
				},
			}}, 2)),
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", RoundID: "r_1", Kind: schema.TurnToolResults, Message: llm.Message{
				Role:    llm.RoleTool,
				Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "call_shell", Name: "shell", Content: "ok"}}},
			}}, 3)),
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", RoundID: "r_2", Authorship: author, Kind: schema.TurnAssistant, Message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
					ID: "call_msg", Name: "communicate", Arguments: json.RawMessage(`{"message":"Checking now","next":"send_and_end_turn"}`),
				}}},
			}}, 4)),
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", Kind: schema.TurnCommunicate, AcceptedReply: true, Communicate: &schema.CommunicateInfo{
				CallID: "call_msg", EndTurn: true, Message: "Checking now", EndReason: "done",
			}}, 5)),
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{
				Status: schema.TurnCompleted, AcceptedCommunicateCallID: "call_msg",
			}}, 6)),
		},
	}
	path := writeFixture(t, fx)
	x := openIndex(t, path, t.TempDir())
	w, err := x.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	ref := referenceCandidates(t, path)
	if !reflect.DeepEqual(w.Candidates, ref) {
		t.Fatalf("candidates diverge from reference:\nindex: %s\nref:   %s", dump(w.Candidates), dump(ref))
	}
	var delivered []appwire.ThreadItem
	for _, c := range w.Candidates {
		if c.Item.Type == "agentMessage" && c.Item.Text == "Checking now" && c.Item.CallID == "call_msg" {
			delivered = append(delivered, c.Item)
		}
	}
	if len(delivered) != 1 {
		t.Fatalf("items=%s, want exactly one agentMessage carrying call_msg", dump(w.Candidates))
	}
	if delivered[0].Observation == nil || delivered[0].Observation.TerminalAccepted == nil || !*delivered[0].Observation.TerminalAccepted {
		t.Fatalf("delivered item Observation=%+v, want terminal-accepted evidence", delivered[0].Observation)
	}
}

// A corrupt TurnCommunicate entry carrying no payload must index without
// panicking: the reader has nothing to deliver, so it delivers nothing.
func TestTurnCommunicateWithoutPayloadDoesNotPanic(t *testing.T) {
	t.Parallel()
	fx := fixture{
		name: "communicate-nil-payload",
		lines: []fixtureLine{
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("go")}, 1)),
			entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", Kind: schema.TurnCommunicate}, 2)),
		},
	}
	path := writeFixture(t, fx)
	x := openIndex(t, path, t.TempDir())
	w, err := x.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	ref := referenceCandidates(t, path)
	if !reflect.DeepEqual(w.Candidates, ref) {
		t.Fatalf("candidates diverge from reference:\nindex: %s\nref:   %s", dump(w.Candidates), dump(ref))
	}
	for _, c := range w.Candidates {
		if c.Item.Type == "agentMessage" {
			t.Fatalf("item=%+v, want no delivery from a payload-less TurnCommunicate", c.Item)
		}
	}
}

// The echo rule compares trimmed prose: a trailing newline, or a
// whitespace-only text part after the call, still makes the delivery a repeat.
func TestCommunicateEchoIgnoresTrailingWhitespace(t *testing.T) {
	t.Parallel()
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
			fx := fixture{
				name: "echo-" + tc.name,
				lines: []fixtureLine{
					entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Message: llm.User("go")}, 1)),
					entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", RoundID: "r_1", Kind: schema.TurnAssistant, Message: llm.Message{
						Role:    llm.RoleAssistant,
						Content: tc.content,
					}}, 2)),
					entryLine(at(schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "turn_1", Kind: schema.TurnCommunicate, Communicate: &schema.CommunicateInfo{
						CallID: "call_msg", EndTurn: true, Message: "All done.",
					}}, 3)),
				},
			}
			path := writeFixture(t, fx)
			x := openIndex(t, path, t.TempDir())
			w, err := x.Latest(40)
			if err != nil {
				t.Fatal(err)
			}
			ref := referenceCandidates(t, path)
			if !reflect.DeepEqual(w.Candidates, ref) {
				t.Fatalf("candidates diverge from reference:\nindex: %s\nref:   %s", dump(w.Candidates), dump(ref))
			}
			shown := 0
			for _, c := range w.Candidates {
				if c.Item.Type != "agentMessage" {
					continue
				}
				if strings.TrimSpace(c.Item.Text) == "All done." {
					shown++
				}
			}
			if shown != 1 {
				t.Fatalf("candidates=%s, want the prose shown once", dump(w.Candidates))
			}
		})
	}
}

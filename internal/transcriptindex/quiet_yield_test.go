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
}

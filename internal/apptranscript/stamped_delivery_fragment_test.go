package apptranscript_test

import (
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// deliveryInsideExecutionTranscript writes the shape a standalone steering
// delivery produces when it lands INSIDE a running execution, through the real
// writer's own placements: the execution names itself, records a tool call,
// another writer delivers a standalone steering turn, and the execution records
// the call's result and one more assistant entry before it completes.
//
// It is the fixture the transcript index already covers as "delivery inside an
// execution" (internal/transcriptindex/fixture_test.go), written through the
// writer so every entry carries the id the writer stamped at write time.
func deliveryInsideExecutionTranscript(t *testing.T, withUsage bool) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "delivery_fragment"})
	if err != nil {
		t.Fatal(err)
	}
	record := func(turn schema.Turn, place transcript.Placement) {
		t.Helper()
		if withUsage && turn.Kind == schema.TurnAssistant {
			turn.Usage = llm.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}
		}
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: place}); err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
	}
	w.BeginExecution("t_run", false)
	record(schema.NewTurn(schema.TurnUserInput, llm.User("deliver")), transcript.PlaceSession)
	record(schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "d1", Name: "shell", Arguments: []byte(`{"cmd":"ls"}`)}},
	}}), transcript.PlaceSession)
	// A standalone delivery, recorded the way a cold writer records one.
	record(schema.NewTurn(schema.TurnSteering, llm.User("a delivered note")), transcript.PlaceDelivery)
	record(schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "d1", Name: "shell", Content: "listing"}},
	}}}, transcript.PlaceSession)
	record(schema.NewTurn(schema.TurnAssistant, llm.Assistant("after the delivery")), transcript.PlaceSession)
	record(schema.Turn{Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}}, transcript.PlaceCompletion)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func fullTurns(t *testing.T, path string) []appwire.Turn {
	t.Helper()
	reg := apptranscript.NewToolCallRegistry()
	turns, err := apptranscript.ItemTurnsFromFile(path, transcript.DefaultMaxLineBytes, func(turn schema.Turn, turnID string, entryIndex int) []appwire.ThreadItem {
		items, _ := apptranscript.ProjectEntryParts(turnID, entryIndex, turn, reg, nil, apptranscript.ToolResultOutputImages)
		return items
	})
	if err != nil {
		t.Fatalf("ItemTurnsFromFile: %v", err)
	}
	return turns
}

// A delivery that lands between a call and its result does not split the call.
// The whole-file read must settle the result onto the call the execution
// announced, keeping ONE command row at the call's position, exactly as the
// index does: two rows for one call id is a client holding an in-progress call
// that never settles beside a second row that already completed.
func TestDeliveryInsideExecutionKeepsOneCommandRow(t *testing.T) {
	path := deliveryInsideExecutionTranscript(t, false)
	turns := fullTurns(t, path)

	rows := 0
	for _, turn := range turns {
		for _, item := range turn.Items {
			if item.Type == "commandExecution" && item.CallID == "d1" {
				rows++
				if item.Status != appwire.TurnStatusCompleted {
					t.Errorf("command d1 status = %q in turn %q, want completed: the result must settle the call it announced", item.Status, turn.ID)
				}
				if item.Output == "" {
					t.Errorf("command d1 in turn %q carries no output: the result did not settle into this row", turn.ID)
				}
			}
		}
	}
	if rows != 1 {
		t.Fatalf("command d1 appears in %d rows, want 1: the full read split one call across the delivery and emitted a second row for its result", rows)
	}

	// The index is the oracle for the same transcript.
	candidates := indexCandidates(t, path)
	indexRows := 0
	for _, candidate := range candidates {
		if candidate.Item.Type == "commandExecution" && candidate.Item.CallID == "d1" {
			indexRows++
		}
	}
	if indexRows != rows {
		t.Fatalf("index kept %d command rows for d1 and the full read kept %d: the two readers disagree", indexRows, rows)
	}
}

// A delivery that lands between two runs of one execution does not halve the
// turn's usage. Every fragment of the turn reports the EXECUTION's total, which
// is what the index reports, so a client that reconciles turn metadata by id
// does not keep a later fragment's partial figure and underreport tokens.
func TestDeliveryInsideExecutionReportsWholeTurnUsage(t *testing.T) {
	path := deliveryInsideExecutionTranscript(t, true)
	turns := fullTurns(t, path)

	var fragments int
	for _, turn := range turns {
		if turn.ID != "t_run" {
			continue
		}
		fragments++
		if turn.Usage == nil {
			t.Fatalf("fragment %d of t_run carries no usage, want the execution's total", fragments)
		}
		if turn.Usage.InputTokens != 20 || turn.Usage.OutputTokens != 10 {
			t.Errorf("fragment %d of t_run usage = {in %d, out %d}, want the execution's {20 10}: a fragment reporting only its own half is a saved turn that underreports", fragments, turn.Usage.InputTokens, turn.Usage.OutputTokens)
		}
	}
	if fragments < 2 {
		t.Fatalf("t_run projected as %d fragment(s), want at least 2: the fixture did not split the execution", fragments)
	}

	// A delivery's own usage is its own: it must not absorb the execution's.
	for _, turn := range turns {
		if turn.ID == "t_run" || turn.Usage == nil {
			continue
		}
		if turn.Usage.InputTokens == 20 {
			t.Errorf("turn %q carries the execution's usage total", turn.ID)
		}
	}
}

// The delivery keeps its own position among the fragments: it is neither
// absorbed into the execution nor moved behind it.
func TestDeliveryInsideExecutionKeepsItsOwnTurn(t *testing.T) {
	path := deliveryInsideExecutionTranscript(t, false)
	turns := fullTurns(t, path)

	// Three turns, in file order: the execution's first fragment, the delivery
	// standing on its own between them, and the execution's second fragment. A
	// projection that DROPS the delivery would emit the two fragments with the
	// same flags and the same ids, so the count and the middle turn are what
	// prove the delivery survived.
	if len(turns) != 3 {
		t.Fatalf("projection returned %d turns, want 3: two fragments of the execution around one delivery", len(turns))
	}
	if turns[1].ID == "t_run" || turns[1].ID == "" {
		t.Fatalf("middle turn = %q, want the delivery in a turn of its own between the fragments", turns[1].ID)
	}
	var delivered int
	for _, item := range turns[1].Items {
		if item.Type == "steering" {
			delivered++
		}
	}
	if delivered != 1 || len(turns[1].Items) != 1 {
		t.Fatalf("middle turn %q = %d items with %d steering, want the delivery's single steering item", turns[1].ID, len(turns[1].Items), delivered)
	}

	var ids []string
	for _, turn := range turns {
		ids = append(ids, turn.ID)
	}
	if want := []string{"t_run", turns[1].ID, "t_run"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("turn order = %v, want %v", ids, want)
	}
	var seen int
	for i, id := range ids {
		if id == "t_run" {
			seen++
		}
		if i > 0 && i < len(ids)-1 && id != "t_run" {
			// The delivery sits between two fragments of the execution.
			if !reflect.DeepEqual([]string{ids[i-1], ids[i+1]}, []string{"t_run", "t_run"}) {
				t.Fatalf("turn order = %v, want the delivery between two t_run fragments", ids)
			}
		}
	}
	if seen < 2 {
		t.Fatalf("turn order = %v, want t_run recorded as two fragments split by the delivery", ids)
	}

	// Each fragment says where it sits in the turn: the first has later items,
	// the last has earlier ones, and the delivery between them has neither.
	// A client paging one fragment relies on those flags to know it does not
	// hold the whole turn.
	first, last := ids[0], ids[len(ids)-1]
	if first != "t_run" || last != "t_run" {
		t.Fatalf("turn order = %v, want t_run at both ends of the delivery", ids)
	}
	for _, turn := range turns {
		switch {
		case turn.ID == "t_run" && turn.Items[0].Type == "userMessage":
			if turn.ItemsView != appwire.TurnItemsViewFragment || !turn.HasLaterItems || turn.HasEarlierItems {
				t.Errorf("first fragment = view %q earlier=%v later=%v, want fragment with later items only", turn.ItemsView, turn.HasEarlierItems, turn.HasLaterItems)
			}
		case turn.ID == "t_run":
			if turn.ItemsView != appwire.TurnItemsViewFragment || turn.HasLaterItems || !turn.HasEarlierItems {
				t.Errorf("last fragment = view %q earlier=%v later=%v, want fragment with earlier items only", turn.ItemsView, turn.HasEarlierItems, turn.HasLaterItems)
			}
		default:
			if turn.ItemsView != appwire.TurnItemsViewFull || turn.HasEarlierItems || turn.HasLaterItems {
				t.Errorf("delivery turn %q = view %q earlier=%v later=%v, want a whole turn of its own", turn.ID, turn.ItemsView, turn.HasEarlierItems, turn.HasLaterItems)
			}
		}
	}
}

// reusedCallIDTranscript writes two assistant rounds on either side of a
// standalone delivery that reuse one tool-call id — a provider that numbers
// calls per round rather than per turn. The index keeps two calls there, each
// settled by the result that answers it.
func reusedCallIDTranscript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "reused_call"})
	if err != nil {
		t.Fatal(err)
	}
	record := func(turn schema.Turn, place transcript.Placement) {
		t.Helper()
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: place}); err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
	}
	call := func(cmd, output string) []llm.ContentPart {
		return []llm.ContentPart{
			{Kind: llm.ContentText, Text: cmd},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "d1", Name: "shell", Arguments: []byte(`{"cmd":"` + cmd + `"}`)}},
		}
	}
	w.BeginExecution("t_reuse", false)
	record(schema.NewTurn(schema.TurnUserInput, llm.User("run twice")), transcript.PlaceSession)
	record(schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: call("first command", "")}), transcript.PlaceSession)
	record(schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "d1", Name: "shell", Content: "first output"}},
	}}}, transcript.PlaceSession)
	// A standalone delivery between the two rounds.
	record(schema.NewTurn(schema.TurnSteering, llm.User("a delivered note")), transcript.PlaceDelivery)
	record(schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: call("second command", "")}), transcript.PlaceSession)
	record(schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "d1", Name: "shell", Content: "second output"}},
	}}}, transcript.PlaceSession)
	record(schema.Turn{Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}}, transcript.PlaceCompletion)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// Two rounds that REUSE a call id are two calls, not one: the whole-file read
// keeps a row for each announcement, and each result settles its own call. It
// must also not move the second round's output ahead of the delivery, which is
// what folding the second call into the first row did (#1166 review).
func TestReusedCallIDAcrossADeliveryKeepsBothCalls(t *testing.T) {
	path := reusedCallIDTranscript(t)
	turns := fullTurns(t, path)

	var outputs []string
	for _, turn := range turns {
		for _, item := range turn.Items {
			if item.Type == "commandExecution" && item.CallID == "d1" {
				outputs = append(outputs, item.Output)
			}
		}
	}
	if len(outputs) != 2 {
		t.Fatalf("call d1 kept %d rows (%v), want 2: two announcements that reuse one call id are two calls", len(outputs), outputs)
	}
	if outputs[0] != "first output" || outputs[1] != "second output" {
		t.Fatalf("call d1 outputs = %v, want [first output second output]: each result must settle the announcement it answers, in file order", outputs)
	}

	// The same transcript through the index: it is the oracle for how many
	// calls a reused id makes and what each one says.
	var indexed []string
	for _, candidate := range indexCandidates(t, path) {
		if candidate.Item.Type == "commandExecution" && candidate.Item.CallID == "d1" {
			indexed = append(indexed, candidate.Item.Output)
		}
	}
	if !reflect.DeepEqual(indexed, outputs) {
		t.Fatalf("index kept call d1 as %v and the full read kept it as %v: the two readers disagree", indexed, outputs)
	}
}

// hiddenCallTranscript writes a shell round whose result settles, then a
// delivery, then an assistant round whose ONLY tool call is a communicate —
// which projects no opener item — reusing the shell call's id, then that
// communicate's rejection. The index resolves the rejection against the latest
// assistant entry that announced a call, so it never folds a hidden call's
// result into a visible row of another call.
func hiddenCallTranscript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "hidden_call"})
	if err != nil {
		t.Fatal(err)
	}
	record := func(turn schema.Turn, place transcript.Placement) {
		t.Helper()
		if _, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: place}); err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
	}
	w.BeginExecution("t_hidden", false)
	record(schema.NewTurn(schema.TurnUserInput, llm.User("run once")), transcript.PlaceSession)
	record(schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "d1", Name: "shell", Arguments: []byte(`{"cmd":"ls"}`)}},
	}}), transcript.PlaceSession)
	record(schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "d1", Name: "shell", Content: "listing"}},
	}}}, transcript.PlaceSession)
	// A standalone delivery between the rounds.
	record(schema.NewTurn(schema.TurnSteering, llm.User("a delivered note")), transcript.PlaceDelivery)
	// The next round's only call is a communicate, which projects no opener:
	// only its result renders, so the call itself has no row to settle into.
	hiddenCall := schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "d1", Name: "communicate", Arguments: []byte(`{}`), RawArguments: `{message: "hello"}`}},
	}})
	hiddenCall.Authorship = &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent}, Inherited: true}
	record(hiddenCall, transcript.PlaceSession)
	record(schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "d1", Name: "communicate", IsError: true, PrevalOnly: true}},
	}}}, transcript.PlaceSession)
	record(schema.Turn{Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}}, transcript.PlaceCompletion)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// A rejected communicate that reuses a settled shell call's id is its own
// call. Its announcement projects no row, so the whole-file merge must NOT
// fold the rejection into the shell row: that would delete the command and
// move the rejection ahead of the delivery. With no opener row to settle, the
// rejection keeps its recorded position.
func TestHiddenCommunicateReusingACallIDKeepsTheShellRow(t *testing.T) {
	path := hiddenCallTranscript(t)
	turns := fullTurns(t, path)

	var shell []appwire.ThreadItem
	var rejected []appwire.ThreadItem
	for _, turn := range turns {
		for _, item := range turn.Items {
			if item.Type != "commandExecution" {
				continue
			}
			switch item.ToolName {
			case "shell":
				shell = append(shell, item)
			case "communicate":
				rejected = append(rejected, item)
			}
		}
	}
	if len(shell) != 1 {
		t.Fatalf("shell call kept %d rows, want 1: the rejection folded into it and deleted the command", len(shell))
	}
	if shell[0].Output != "listing" || shell[0].ToolName != "shell" {
		t.Fatalf("shell row = {name %q output %q}, want the shell command's own settled row", shell[0].ToolName, shell[0].Output)
	}
	if len(rejected) != 1 {
		t.Fatalf("rejected communicate kept %d rows, want 1: a call with no opener row keeps its recorded position", len(rejected))
	}
	if rejected[0].Status != appwire.TurnStatusFailed {
		t.Errorf("rejection status = %q, want failed", rejected[0].Status)
	}

	// The index is the oracle: one shell row, one rejected communicate row.
	var indexShell, indexRejected int
	for _, candidate := range indexCandidates(t, path) {
		if candidate.Item.Type != "commandExecution" {
			continue
		}
		switch candidate.Item.ToolName {
		case "shell":
			indexShell++
		case "communicate":
			indexRejected++
		}
	}
	if indexShell != len(shell) || indexRejected != len(rejected) {
		t.Fatalf("index kept %d shell and %d communicate rows and the full read kept %d and %d: the two readers disagree", indexShell, indexRejected, len(shell), len(rejected))
	}
}

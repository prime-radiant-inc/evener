package apptranscript_test

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// deliveryShellRound is the round the delivery fixture records on the entry
// that ANNOUNCES its shell call. The entry that ANSWERS it records no round of
// its own, exactly as a session records a tool round (agent/session.go sets
// RoundID on assistant entries only, and the index's own "delivery inside an
// execution" fixture does the same), so a settled row's round says whose
// identity it carries.
const deliveryShellRound = "round_shell"

// recordedCall is what the fixture's own writes reported about one tool call:
// the 1-based entry index and content part of the entry that announced the call
// and of the entry that answered it, the round each recorded, and the result
// payload the fixture wrote.
type recordedCall struct {
	openerEntry, openerPart int
	openerRound             string
	resultEntry, resultPart int
	resultRound             string
	output                  string
}

// openerItemID is the id the projector mints for the announcing entry's call
// item, item_tool_<entry>_<part> (apptranscript's toolCallItem). It comes from
// the fixture's recording, never from a reader's settled result.
func (c recordedCall) openerItemID() string {
	return fmt.Sprintf("item_tool_%d_%d", c.openerEntry, c.openerPart)
}

// resultItemID is the id the projector mints for the answering entry's own
// item, item_tool_result_<entry>_<part>. A settled row must not carry it: that
// id names the entry the result was recorded in.
func (c recordedCall) resultItemID() string {
	return fmt.Sprintf("item_tool_result_%d_%d", c.resultEntry, c.resultPart)
}

// deliveryInsideExecutionTranscript writes the shape a standalone steering
// delivery produces when it lands INSIDE a running execution, through the real
// writer's own placements: the execution names itself, records a tool call,
// another writer delivers a standalone steering turn, and the execution records
// the call's result and one more assistant entry before it completes.
//
// It is the fixture the transcript index already covers as "delivery inside an
// execution" (internal/transcriptindex/fixture_test.go), written through the
// writer so every entry carries the id the writer stamped at write time.
//
// The announcing entry records deliveryShellRound and the answering entry
// records none. The fixture returns what the writer reported for both, plus the
// result payload it wrote, so a caller states the expected row from the
// recording instead of from either reader's answer.
func deliveryInsideExecutionTranscript(t *testing.T, withUsage bool) (path string, call recordedCall) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "session.transcript.jsonl")
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: "delivery_fragment"})
	if err != nil {
		t.Fatal(err)
	}
	record := func(turn schema.Turn, place transcript.Placement) transcript.Record {
		t.Helper()
		if withUsage && turn.Kind == schema.TurnAssistant {
			turn.Usage = llm.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}
		}
		rec, err := w.Record(turn, transcript.RecordOptions{Door: transcript.DoorBuffered, Place: place})
		if err != nil {
			t.Fatalf("record %s: %v", turn.Kind, err)
		}
		if !rec.Recorded {
			t.Fatalf("record %s: the writer recorded nothing", turn.Kind)
		}
		return rec
	}
	call.output = "listing"
	w.BeginExecution("t_run", false)
	record(schema.NewTurn(schema.TurnUserInput, llm.User("deliver")), transcript.PlaceSession)
	opener := schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "d1", Name: "shell", Arguments: []byte(`{"cmd":"ls"}`)}},
	}})
	opener.RoundID = deliveryShellRound
	openerRecord := record(opener, transcript.PlaceSession)
	// A standalone delivery, recorded the way a cold writer records one.
	record(schema.NewTurn(schema.TurnSteering, llm.User("a delivered note")), transcript.PlaceDelivery)
	// The answering entry records no round: only assistant entries carry one.
	resultRecord := record(schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "d1", Name: "shell", Content: call.output}},
	}}}, transcript.PlaceSession)
	record(schema.NewTurn(schema.TurnAssistant, llm.Assistant("after the delivery")), transcript.PlaceSession)
	record(schema.Turn{Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}}, transcript.PlaceCompletion)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// The projector numbers entries from 1 (the header is not an entry), and
	// each entry here holds its call or its result at content part 0.
	call.openerEntry, call.openerPart = int(openerRecord.Ordinal)+1, 0
	call.resultEntry, call.resultPart = int(resultRecord.Ordinal)+1, 0
	// What the transcript actually holds, which is what a settled row must
	// carry or must not inherit.
	call.openerRound, call.resultRound = openerRecord.Turn.RoundID, resultRecord.Turn.RoundID
	return path, call
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

// visibleItemsFromTurns flattens the whole-file read's visible items in the
// order a client receives them: turn by turn, each turn's items in order.
func visibleItemsFromTurns(turns []appwire.Turn) []appwire.ThreadItem {
	var items []appwire.ThreadItem
	for _, turn := range turns {
		items = append(items, turn.Items...)
	}
	return items
}

// visibleItemsFromCandidates flattens the bounded index's items in arrival
// order, which is the order the live client receives them in.
func visibleItemsFromCandidates(candidates []appitempaging.TranscriptItemCandidate) []appwire.ThreadItem {
	items := make([]appwire.ThreadItem, 0, len(candidates))
	for _, candidate := range candidates {
		items = append(items, candidate.Item)
	}
	return items
}

// assertSettledShellRow proves one reader kept ONE completed command row for
// the fixture's call: at the position it was announced, before the standalone
// delivery, carrying the ANNOUNCING entry's id, turn and round and the payload
// the ANSWERING entry recorded.
func assertSettledShellRow(t *testing.T, reader string, items []appwire.ThreadItem, call recordedCall) {
	t.Helper()
	var shell appwire.ThreadItem
	shellAt, deliveryAt, shellRows := -1, -1, 0
	for i, item := range items {
		switch {
		case item.Type == "commandExecution" && item.CallID == "d1":
			shellRows++
			shell, shellAt = item, i
		case item.Type == "steering":
			deliveryAt = i
		}
	}
	if shellRows != 1 {
		t.Fatalf("%s: call d1 kept %d command rows, want 1: one call is one row, at the entry that announced it", reader, shellRows)
	}
	if deliveryAt < 0 {
		t.Fatalf("%s: no steering row for the fixture's delivery", reader)
	}
	if shellAt > deliveryAt {
		t.Errorf("%s: settled row at item %d sits after the delivery at item %d: settling the call must not move its row behind the delivery", reader, shellAt, deliveryAt)
	}
	if want := call.openerItemID(); shell.ID != want {
		t.Errorf("%s: settled row id = %q, want %q, the announcing entry's (entry %d, part %d); %q is the answering entry's own item", reader, shell.ID, want, call.openerEntry, call.openerPart, call.resultItemID())
	}
	if shell.TurnID != "t_run" {
		t.Errorf("%s: settled row turn = %q, want t_run, the turn the call was announced in", reader, shell.TurnID)
	}
	if shell.RoundID != call.openerRound {
		t.Errorf("%s: settled row round = %q, want the announcing entry's %q (the answering entry recorded %q): settling a call keeps the identity it was announced with", reader, shell.RoundID, call.openerRound, call.resultRound)
	}
	if shell.Status != appwire.TurnStatusCompleted {
		t.Errorf("%s: settled row status = %q, want completed: the result must settle the call it announced", reader, shell.Status)
	}
	if shell.Output != call.output {
		t.Errorf("%s: settled row output = %q, want the result's own payload %q", reader, shell.Output, call.output)
	}
}

// assertIndexSettledRowPosition proves the index kept the settled row at the
// ANNOUNCING entry's position, which differs from the ANSWERING entry's: the
// answer was recorded at its own entry and part, and the row never moved there.
func assertIndexSettledRowPosition(t *testing.T, candidates []appitempaging.TranscriptItemCandidate, call recordedCall) {
	t.Helper()
	want := appwire.ThreadItemPosition{Entry: uint64(call.openerEntry), Item: uint32(call.openerPart)}
	answered := appwire.ThreadItemPosition{Entry: uint64(call.resultEntry), Item: uint32(call.resultPart)}
	if want == answered {
		t.Fatalf("the announcing and answering entries recorded the same position %+v", want)
	}
	rows := 0
	for _, candidate := range candidates {
		if candidate.Item.Type != "commandExecution" || candidate.Item.CallID != "d1" {
			continue
		}
		rows++
		if candidate.Position != want {
			t.Errorf("index settled row position = %+v, want the announcing entry's %+v (the answering entry's is %+v): completing a call must not move its row", candidate.Position, want, answered)
		}
	}
	if rows != 1 {
		t.Fatalf("the index published %d rows for call d1, want its one settled row at %+v", rows, want)
	}
}

// A delivery that lands between a call and its result does not split the call.
// The whole-file read must settle the result onto the call the execution
// announced, keeping ONE command row at the call's position, exactly as the
// index does: two rows for one call id is a client holding an in-progress call
// that never settles beside a second row that already completed.
//
// Settling must also keep the ANNOUNCING entry's identity. The announcing entry
// records a round and the answering entry records none, so a row that took its
// id or round from the answer names an entry the command was never recorded in,
// which is what the index already refuses to do (#1166).
func TestDeliveryInsideExecutionKeepsOneCommandRow(t *testing.T) {
	path, call := deliveryInsideExecutionTranscript(t, false)
	if call.openerItemID() == call.resultItemID() {
		t.Fatalf("the announcing and answering entries derive the same item id %q", call.openerItemID())
	}
	if call.openerRound != deliveryShellRound {
		t.Fatalf("announcing entry recorded round %q, want %q: the identity under test must be what the transcript records", call.openerRound, deliveryShellRound)
	}
	if call.resultRound != "" {
		t.Fatalf("answering entry recorded round %q, want none: a row settled by its identity would keep the announcing entry's round", call.resultRound)
	}

	// The whole-file read.
	assertSettledShellRow(t, "full read", visibleItemsFromTurns(fullTurns(t, path)), call)

	// The index is the oracle for the same transcript, and keeps the row at the
	// announcing entry's position.
	candidates := indexCandidates(t, path)
	assertSettledShellRow(t, "bounded index", visibleItemsFromCandidates(candidates), call)
	assertIndexSettledRowPosition(t, candidates, call)
}

// A delivery that lands between two runs of one execution does not halve the
// turn's usage. Every fragment of the turn reports the EXECUTION's total, which
// is what the index reports, so a client that reconciles turn metadata by id
// does not keep a later fragment's partial figure and underreport tokens.
func TestDeliveryInsideExecutionReportsWholeTurnUsage(t *testing.T) {
	path, _ := deliveryInsideExecutionTranscript(t, true)
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
	path, _ := deliveryInsideExecutionTranscript(t, false)
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
// settled by the result that answers it. It returns the transcript and the two
// result payloads it wrote, in file order, so a caller can compare each
// projected row against the payload written for it rather than against copy.
func reusedCallIDTranscript(t *testing.T) (path string, outputs []string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "session.transcript.jsonl")
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
	call := func(cmd string) []llm.ContentPart {
		return []llm.ContentPart{
			{Kind: llm.ContentText, Text: cmd},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "d1", Name: "shell", Arguments: []byte(fmt.Sprintf(`{"cmd":%q}`, cmd))}},
		}
	}
	// The two results carry distinct payloads so a row settled by the wrong
	// announcement is visible from the payload alone.
	first, second := "payload-from-the-first-round", "payload-from-the-second-round"
	outputs = []string{first, second}
	w.BeginExecution("t_reuse", false)
	record(schema.NewTurn(schema.TurnUserInput, llm.User("run twice")), transcript.PlaceSession)
	record(schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: call("first command")}), transcript.PlaceSession)
	record(schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "d1", Name: "shell", Content: first}},
	}}}, transcript.PlaceSession)
	// A standalone delivery between the two rounds.
	record(schema.NewTurn(schema.TurnSteering, llm.User("a delivered note")), transcript.PlaceDelivery)
	record(schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: call("second command")}), transcript.PlaceSession)
	record(schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "d1", Name: "shell", Content: second}},
	}}}, transcript.PlaceSession)
	record(schema.Turn{Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{Status: schema.TurnCompleted}}, transcript.PlaceCompletion)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path, outputs
}

// Two rounds that REUSE a call id are two calls, not one: the whole-file read
// keeps a row for each announcement, and each result settles its own call. It
// must also not move the second round's output ahead of the delivery, which is
// what folding the second call into the first row did (#1166 review).
func TestReusedCallIDAcrossADeliveryKeepsBothCalls(t *testing.T) {
	path, wrote := reusedCallIDTranscript(t)
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
	if !reflect.DeepEqual(outputs, wrote) {
		t.Fatalf("call d1 outputs = %v, want %v: each result must settle the announcement it answers, in file order", outputs, wrote)
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
// result into a visible row of another call. It returns the transcript and the
// shell result payload it wrote, so a caller compares against that payload
// rather than against copy.
func hiddenCallTranscript(t *testing.T) (path, shellPayload string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "session.transcript.jsonl")
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
	shellPayload = "payload-from-the-shell-call"
	w.BeginExecution("t_hidden", false)
	record(schema.NewTurn(schema.TurnUserInput, llm.User("run once")), transcript.PlaceSession)
	record(schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "d1", Name: "shell", Arguments: []byte(`{"cmd":"ls"}`)}},
	}}), transcript.PlaceSession)
	record(schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
		{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: "d1", Name: "shell", Content: shellPayload}},
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
	return path, shellPayload
}

// A rejected communicate that reuses a settled shell call's id is its own
// call. Its announcement projects no row, so the whole-file merge must NOT
// fold the rejection into the shell row: that would delete the command and
// move the rejection ahead of the delivery. With no opener row to settle, the
// rejection keeps its recorded position.
func TestHiddenCommunicateReusingACallIDKeepsTheShellRow(t *testing.T) {
	path, shellPayload := hiddenCallTranscript(t)
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
	// The row must still carry the payload written for the SHELL call, not the
	// rejection's: a row whose output is empty or belongs to another call has
	// been settled by the wrong entry.
	if shell[0].Output != shellPayload || shell[0].ToolName != "shell" {
		t.Fatalf("shell row = {name %q output %q}, want the shell command's own settled row (%q)", shell[0].ToolName, shell[0].Output, shellPayload)
	}
	if len(rejected) != 1 {
		t.Fatalf("rejected communicate kept %d rows, want 1: a call with no opener row keeps its recorded position", len(rejected))
	}
	if rejected[0].Status != appwire.TurnStatusFailed {
		t.Errorf("rejection status = %q, want failed", rejected[0].Status)
	}

	// The index is the oracle: one shell row, one rejected communicate row.
	candidates := indexCandidates(t, path)
	var indexShell, indexRejected int
	for _, candidate := range candidates {
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

	// The visible order is the recording's: the settled shell row, then the
	// standalone delivery, then the rejection at its own entry. A rejection
	// folded into the shell row would delete the command and leave the
	// rejection ahead of the delivery, which is what this order distinguishes.
	assertShellDeliveryRejectionOrder(t, "full read", visibleItemsFromTurns(turns))
	assertShellDeliveryRejectionOrder(t, "bounded index", visibleItemsFromCandidates(candidates))
}

// assertShellDeliveryRejectionOrder proves one reader keeps the fixture's
// three visible rows in the order it recorded them: the settled shell row,
// the standalone delivery, then the rejected communicate.
func assertShellDeliveryRejectionOrder(t *testing.T, reader string, items []appwire.ThreadItem) {
	t.Helper()
	shellAt, deliveryAt, rejectedAt := -1, -1, -1
	for i, item := range items {
		switch {
		case item.Type == "commandExecution" && item.ToolName == "shell":
			shellAt = i
		case item.Type == "steering":
			deliveryAt = i
		case item.Type == "commandExecution" && item.ToolName == "communicate":
			rejectedAt = i
		}
	}
	if shellAt < 0 || deliveryAt < 0 || rejectedAt < 0 {
		t.Fatalf("%s: visible rows at %d include shell=%v delivery=%v rejection=%v, want all three", reader, len(items), shellAt >= 0, deliveryAt >= 0, rejectedAt >= 0)
	}
	if !(shellAt < deliveryAt && deliveryAt < rejectedAt) {
		t.Fatalf("%s: visible order puts shell at %d, the delivery at %d and the rejection at %d, want shell before delivery before rejection", reader, shellAt, deliveryAt, rejectedAt)
	}
}

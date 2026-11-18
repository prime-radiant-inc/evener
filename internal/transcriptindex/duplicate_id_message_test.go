package transcriptindex

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
	"primeradiant.com/evener/internal/apptranscript"
)

// TestACompletionWithoutAPayloadClosesTheTurn covers a stamped TURN_COMPLETION
// the writer and decoder accept with no payload at all. The index folds the
// absent payload as zero-valued info, so the turn is complete with a zero
// duration. Whole-file replay skipped the fold entirely, which left the run at
// its previous status — and because the interrupt pass still ran, an
// unanswered call in that turn read interrupted while the index reported a
// completed turn (#1166 review).
func TestACompletionWithoutAPayloadClosesTheTurn(t *testing.T) {
	path := writeFixture(t, newFormatFixture(t, "completion without a payload"))
	indexed := turnOf(t, allCandidates(t, openIndex(t, path, t.TempDir())), "turn_m25")
	replayed := replayedTurnOf(t, path, "turn_m25")

	if indexed.Status != appwire.TurnStatusCompleted {
		t.Fatalf("index status = %q, want completed: the payload folds as zero-valued info", indexed.Status)
	}
	if indexed.DurationMS == nil || *indexed.DurationMS != 0 {
		t.Errorf("index duration = %v, want 0: a completion without a payload states zero", indexed.DurationMS)
	}
	if replayed.Status != indexed.Status {
		t.Errorf("replay status = %q, index = %q: an omitted payload is not a skipped fold",
			replayed.Status, indexed.Status)
	}
	if (replayed.DurationMS == nil) != (indexed.DurationMS == nil) {
		t.Fatalf("replay duration = %v, index = %v: the readers disagree on whether a duration exists",
			replayed.DurationMS, indexed.DurationMS)
	}
	if replayed.DurationMS != nil && *replayed.DurationMS != *indexed.DurationMS {
		t.Errorf("replay duration = %d, index = %d", *replayed.DurationMS, *indexed.DurationMS)
	}
}

// TestAReplacedAwaitingEntryAgreesOnTheResultItem covers the seam a duplicate
// provider call id opens between the two readers. An inherited assistant entry
// records a command and then a communicate under ONE id. A later assistant
// entry takes over as the turn's awaiting entry (stampFold advances it on every
// entry recording a call), so by the time the unnamed result arrives the index
// no longer resolves it from the announcing entry — it falls back to the
// pending-communicate state and indexes a MESSAGE. The whole-file read instead
// replays the announcing entry, where the first-part rule registers the
// command, and shows a command result.
//
// Whichever answer is right, both readers must give it: "what does a duplicate
// id mean here" cannot have one answer for the index and another for replay.
func TestAReplacedAwaitingEntryAgreesOnTheResultItem(t *testing.T) {
	fx := fixture{
		name:   "duplicate id replaced awaiting",
		header: transcript.Header{SessionID: "dup_replaced", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"},
		lines: []fixtureLine{
			entryLine(opens("turn_dup", schema.TurnSpanExecution, user("go"))),
			// The announcing entry: a command, then a communicate, one id.
			entryLine(inTurn("turn_dup", inheritedAuthorship(assistant(
				call("call_dup", "read_file", `{"path":"a"}`),
				call("call_dup", "communicate", `{"message":"hi"}`),
			)))),
			// A later assistant entry takes over as the awaiting entry without
			// recording that id itself.
			entryLine(inTurn("turn_dup", assistant(call("call_other", "shell", `{"cmd":"true"}`)))),
			// The unnamed result for the reused id arrives after the takeover.
			entryLine(inTurn("turn_dup", results(result("call_dup", "", "payload")))),
			entryLine(completion("turn_dup", schema.TurnCompleted, 99, 10)),
		},
	}
	path := writeFixture(t, fx)
	indexed := turnItemsOf(t, allCandidates(t, openIndex(t, path, t.TempDir())), "turn_dup")
	replayed := replayedTurnOf(t, path, "turn_dup")

	indexedItems := itemShapes(indexed)
	replayedItems := itemShapes(replayed.Items)
	if len(indexedItems) != len(replayedItems) {
		t.Fatalf("item counts differ: index %v, replay %v", indexedItems, replayedItems)
	}
	for i := range indexedItems {
		if indexedItems[i] != replayedItems[i] {
			t.Fatalf("item %d differs: index %s, replay %s", i, indexedItems[i], replayedItems[i])
		}
	}
	// Agreement alone is not the assertion: the readers could agree on the
	// wrong answer, and did. By the time the result arrives the announcing
	// entry is no longer the turn's awaiting entry, so the result settles the
	// deferred communicate and the earlier command stays unanswered.
	assertSettledOutcome(t, "index", indexed)
	assertSettledOutcome(t, "replay", replayed.Items)
}

// assertSettledOutcome states the one answer both readers must give for the
// duplicate-id fixture: an inherited MESSAGE carries the result, and the
// command the same id announced first is left in progress.
//
// An OLDER transcript is the opposite answer, and keeps it: a format-0 entry
// has no identity to be awaiting, so the legacy index and the legacy projector
// both resolve a nameless result by last-name-wins. See
// TestALegacyDuplicateIdKeepsLastWinsOnBothReaders.
func assertSettledOutcome(t *testing.T, reader string, items []appwire.ThreadItem) {
	t.Helper()
	message := ""
	command := ""
	for _, item := range items {
		if item.CallID != "call_dup" {
			continue
		}
		switch item.Type {
		case "agentMessage":
			if item.Text != "" {
				message = item.Text
			}
			if item.Status != appwire.TurnStatusCompleted {
				t.Errorf("%s: inherited message status = %q, want completed", reader, item.Status)
			}
		case "commandExecution":
			command = item.Status
		}
	}
	if message == "" {
		t.Errorf("%s: no inherited message carries the result: %v", reader, itemShapes(items))
	}
	if command != appwire.TurnStatusInProgress {
		t.Errorf("%s: the earlier command reads %q, want inProgress: a result for a reused id settles only the awaiting call",
			reader, command)
	}
}

// TestARejectedCommunicateDoesNotSettleAnotherToolsCall covers the settlement
// half of the duplicate-id seam. An inherited stamped batch reserves read_file
// and also defers a communicate under the SAME id. A prevalidation rejection of
// that communicate projects as a failed commandExecution — a real row, because
// live never delivered it and dropping it hides the rejection. The reservation
// belongs to read_file, so the rejection must stay standalone; absorbing it
// into the reserved call omits the rejection row and can replace the command's
// name and status. Both readers share one settlement guard (#1166 review).
func TestARejectedCommunicateDoesNotSettleAnotherToolsCall(t *testing.T) {
	rejected := result("call_shared", "communicate", "")
	rejected.ToolResult.IsError = true
	rejected.ToolResult.PrevalOnly = true
	fx := fixture{
		name:   "rejected communicate keeps its own row",
		header: transcript.Header{SessionID: "rejected_shared", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"},
		lines: []fixtureLine{
			entryLine(opens("turn_rej", schema.TurnSpanExecution, user("go"))),
			entryLine(inTurn("turn_rej", inheritedAuthorship(assistant(
				call("call_shared", "read_file", `{"path":"a"}`),
				callRaw("call_shared", "communicate", `{message: "bad"}`),
			)))),
			// The command's own result, unnamed, which settles the read_file.
			entryLine(inTurn("turn_rej", results(result("call_shared", "", "the bytes")))),
			// A rejection recorded for the SAME id: a distinct call.
			entryLine(inTurn("turn_rej", results(rejected))),
			entryLine(completion("turn_rej", schema.TurnCompleted, 99, 10)),
		},
	}
	path := writeFixture(t, fx)
	indexed := turnItemsOf(t, allCandidates(t, openIndex(t, path, t.TempDir())), "turn_rej")
	var replayed []appwire.ThreadItem
	for _, turn := range replayedTurns(t, path) {
		if turn.ID == "turn_rej" {
			replayed = append(replayed, turn.Items...)
		}
	}

	for _, reader := range []struct {
		name  string
		items []appwire.ThreadItem
	}{{"index", indexed}, {"replay", replayed}} {
		rejections, tool := 0, ""
		for _, item := range reader.items {
			if item.CallID != "call_shared" {
				continue
			}
			if item.PrevalOnly {
				rejections++
			}
			if item.Type == "commandExecution" && !item.PrevalOnly {
				tool = item.ToolName
			}
		}
		if rejections != 1 {
			t.Errorf("%s: rejection rows = %d, want 1: a rejected communicate is its own row (items %s)",
				reader.name, rejections, itemShapes(reader.items))
		}
		if tool != "read_file" {
			t.Errorf("%s: reserved call tool = %q, want read_file: the rejection must not replace it (items %s)",
				reader.name, tool, itemShapes(reader.items))
		}
	}
}

// TestReplayKeepsEachTurnsAwaitingBatchApart covers the turn-scoped half of the
// duplicate-id seam. Whole-file replay threads ONE registry through the file,
// so turn A's awaiting batch survives into turn B and is then replaced by it.
// An unnamed result for A arriving after B announced its own calls is resolved
// against B's batch: A's pending communicate names it, so replay renders A's
// deferred message and loses the command output the index settled from A's own
// batch. The batch is per TURN, not per file (#1166 review).
func TestReplayKeepsEachTurnsAwaitingBatchApart(t *testing.T) {
	fx := fixture{
		name:   "awaiting batch is per turn",
		header: transcript.Header{SessionID: "batch_scope", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"},
		lines: []fixtureLine{
			// Turn A: a command and a deferred communicate under one id.
			entryLine(opens("turn_a", schema.TurnSpanExecution, user("go"))),
			entryLine(inTurn("turn_a", inheritedAuthorship(assistant(
				call("call_shared", "read_file", `{"path":"a"}`),
				call("call_shared", "communicate", `{"message":"deferred"}`),
			)))),
			// Turn B announces a different batch and completes.
			entryLine(opens("turn_b", schema.TurnSpanExecution, user("again"))),
			entryLine(inTurn("turn_b", assistant(call("call_b", "shell", `{"cmd":"true"}`)))),
			entryLine(inTurn("turn_b", results(result("call_b", "", "ok")))),
			entryLine(completion("turn_b", schema.TurnCompleted, 97, 10)),
			// Back in A: the unnamed result for the reused id.
			entryLine(inTurn("turn_a", results(result("call_shared", "", "the bytes")))),
			entryLine(completion("turn_a", schema.TurnCompleted, 99, 10)),
		},
	}
	path := writeFixture(t, fx)
	// Replay may split turn_a into several groups, and the defect shows up in
	// the later one, so read every group of the turn rather than the first.
	var items []appwire.ThreadItem
	for _, turn := range replayedTurns(t, path) {
		if turn.ID == "turn_a" {
			items = append(items, turn.Items...)
		}
	}
	command, message := "", ""
	for _, item := range items {
		if item.CallID != "call_shared" {
			continue
		}
		switch item.Type {
		case "commandExecution":
			if item.ToolName != "" {
				command = item.ToolName
			}
		case "agentMessage":
			message = item.Text
		}
	}
	if command != "read_file" {
		t.Errorf("replay turn_a: command tool = %q, want read_file: turn A's own batch reserves it (items %s)",
			command, itemShapes(items))
	}
	if message != "" {
		t.Errorf("replay turn_a: rendered %q from turn B's batch: the batch belongs to the turn that announced it",
			message)
	}
}

// TestALegacyDuplicateIdKeepsLastWinsOnBothReaders covers the format-0 half of
// the duplicate-id seam. An older entry announces a deferred communicate and
// then a read_file under ONE call id. The legacy index seeds the nameless
// result with read_file (last-name-wins, which is what format-0 lookups have
// always meant), so the result must render as the command and must NOT be
// consumed by the pending communicate: consuming it there drops the command's
// output and renders an assistant message the assistant never sent
// (#1166 review).
func TestALegacyDuplicateIdKeepsLastWinsOnBothReaders(t *testing.T) {
	// Every line is legacy: no inTurn, no identity format, no TurnID. That is
	// exactly the shape under test, and a format-0 entry belongs to whichever
	// legacy group the grouper placed it in.
	announce := assistant(
		call("call_legacy", "communicate", `{"message":"deferred"}`),
		call("call_legacy", "read_file", `{"path":"a"}`),
	)
	fx := fixture{
		name:   "legacy duplicate id keeps last wins",
		header: transcript.Header{SessionID: "legacy_dup", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"},
		lines: []fixtureLine{
			entryLine(user("go")),
			entryLine(announce),
			entryLine(results(result("call_legacy", "", "the bytes"))),
		},
	}
	path := writeFixture(t, fx)
	candidates := allCandidates(t, openIndex(t, path, t.TempDir()))
	callTurn := ""
	for _, c := range candidates {
		if c.Item.CallID == "call_legacy" {
			callTurn = c.TurnID
			break
		}
	}
	if callTurn == "" {
		t.Fatalf("no turn carries the call: %s", dump(candidates))
	}
	indexed := turnItemsOf(t, candidates, callTurn)
	replayed := replayedTurnOf(t, path, callTurn)

	for _, reader := range []struct {
		name  string
		items []appwire.ThreadItem
	}{{"index", indexed}, {"replay", replayed.Items}} {
		command, status, misclassified := "", "", false
		for _, item := range reader.items {
			if item.CallID != "call_legacy" {
				continue
			}
			switch item.Type {
			case "commandExecution":
				if item.PrevalOnly {
					continue
				}
				command, status = item.ToolName, item.Status
			case "agentMessage":
				// The result the legacy rule names read_file must not surface as
				// an assistant message. (The deferred communicate's own flushed
				// text is a different item and both readers agree on it.)
				if strings.Contains(item.Text, "the bytes") {
					misclassified = true
				}
			}
		}
		if command != "read_file" {
			t.Errorf("%s: command tool = %q, want read_file: a legacy id resolves by last-name-wins (items %s)",
				reader.name, command, itemShapes(reader.items))
		}
		if status != appwire.TurnStatusCompleted {
			t.Errorf("%s: command status = %q, want completed: its result settled it (items %s)",
				reader.name, status, itemShapes(reader.items))
		}
		if misclassified {
			t.Errorf("%s: the result rendered as an assistant message: the legacy rule names it read_file (items %s)",
				reader.name, itemShapes(reader.items))
		}
	}
}

// TestTheIndexSettlesTheReservedCommandUnderADuplicateId covers the index's own
// answer for a duplicated id: an inherited entry announces a visible command
// and then a communicate under ONE id, and the result names no tool. The
// reservation makes the command the call that result belongs to, and the
// builder resolves that name when it builds the item. The projection must use
// the builder's answer rather than its pending-communicate state, or the
// command stays unanswered and the result opens a standalone row — and a later
// result for the deferred communicate must still render its message, which a
// fix that simply preferred the command everywhere would break (#1166 review).
func TestTheIndexSettlesTheReservedCommandUnderADuplicateId(t *testing.T) {
	fx := fixture{
		name:   "index settles the reserved command",
		header: transcript.Header{SessionID: "reserved_cmd", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"},
		lines: []fixtureLine{
			entryLine(opens("turn_cmd", schema.TurnSpanExecution, user("go"))),
			entryLine(inTurn("turn_cmd", inheritedAuthorship(assistant(
				call("call_pair", "read_file", `{"path":"a"}`),
				call("call_pair", "communicate", `{"message":"kept"}`),
			)))),
			// The command's result, unnamed: it belongs to the reserved part.
			entryLine(inTurn("turn_cmd", results(result("call_pair", "", "the bytes")))),
			// The deferred communicate's own result, which must still deliver.
			entryLine(inTurn("turn_cmd", results(result("call_pair", "communicate", "ok")))),
			entryLine(completion("turn_cmd", schema.TurnCompleted, 96, 10)),
		},
	}
	path := writeFixture(t, fx)
	items := turnItemsOf(t, allCandidates(t, openIndex(t, path, t.TempDir())), "turn_cmd")

	var command, message *appwire.ThreadItem
	for i := range items {
		switch {
		case items[i].Type == "commandExecution" && items[i].CallID == "call_pair":
			command = &items[i]
		case items[i].Type == "agentMessage" && items[i].CallID == "call_pair":
			message = &items[i]
		}
	}
	if command == nil {
		t.Fatalf("the reserved command is missing from %v", itemShapes(items))
	}
	if command.Status != appwire.TurnStatusCompleted {
		t.Errorf("reserved command status = %q, want completed: its result settled it at its own position",
			command.Status)
	}
	if command.ToolName != "read_file" {
		t.Errorf("reserved command tool = %q, want read_file: the entry reserved its FIRST part", command.ToolName)
	}
	if command.Position.Entry != 2 || command.Position.Item != 0 {
		t.Errorf("reserved command position = %+v, want entry 2 item 0: a settled call keeps its announcement's place",
			command.Position)
	}
	if message == nil {
		t.Fatalf("the deferred communicate's message is missing from %v: preferring the command everywhere would drop it",
			itemShapes(items))
	}
}

// TestAnUnrelatedResultDoesNotConsumeTheDeferredDelivery covers the other side
// of a reused provider call id: consumption. An inherited entry defers a
// communicate under an id a command already used, and a result for that id
// arrives naming the command. That result is not the delivery's own, so it must
// leave the deferred state alone — consuming it there drops the message from
// both readers, which read the pending state the same way.
func TestAnUnrelatedResultDoesNotConsumeTheDeferredDelivery(t *testing.T) {
	fx := fixture{
		name:   "unrelated result keeps the delivery",
		header: transcript.Header{SessionID: "kept_delivery", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"},
		lines: []fixtureLine{
			entryLine(opens("turn_m26", schema.TurnSpanExecution, user("go"))),
			entryLine(inTurn("turn_m26", inheritedAuthorship(assistant(
				call("call_shared", "read_file", `{"path":"a"}`),
				call("call_shared", "communicate", `{"message":"kept"}`),
			)))),
			// The command's own result, naming itself: it is not the delivery's.
			entryLine(inTurn("turn_m26", results(result("call_shared", "read_file", "read it")))),
			// The delivery's own result follows it.
			entryLine(inTurn("turn_m26", results(result("call_shared", "communicate", "ok")))),
			entryLine(completion("turn_m26", schema.TurnCompleted, 97, 10)),
		},
	}
	path := writeFixture(t, fx)
	indexed := turnItemsOf(t, allCandidates(t, openIndex(t, path, t.TempDir())), "turn_m26")
	replayed := replayedTurnOf(t, path, "turn_m26")

	indexedItems := itemShapes(indexed)
	replayedItems := itemShapes(replayed.Items)
	if len(indexedItems) != len(replayedItems) {
		t.Fatalf("item counts differ: index %v, replay %v", indexedItems, replayedItems)
	}
	for i := range indexedItems {
		if indexedItems[i] != replayedItems[i] {
			t.Fatalf("item %d differs: index %s, replay %s", i, indexedItems[i], replayedItems[i])
		}
	}
	// The delivery is the point of the fixture: if either reader consumed it
	// with the unrelated command result, neither has a message to show.
	delivered := false
	for _, item := range indexed {
		if item.Type == "agentMessage" && item.CallID == "call_shared" {
			delivered = true
		}
	}
	if !delivered {
		t.Fatalf("the delivered message is missing from %v: a command result consumed a delivery that was not its own", indexedItems)
	}
}

// inheritedAuthorship marks turn as an inherited entry: an agent turn carried
// into this session from the session it names, which is the shape that makes a
// communicate call deferred rather than live.
func inheritedAuthorship(turn schema.Turn) schema.Turn {
	turn.Authorship = &schema.TranscriptAuthorship{
		Author:    schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "parent"},
		Inherited: true,
	}
	return turn
}

// turnItemsOf gathers one turn's items in page order. A candidate carries the
// turn shell plus its OWN item, so a turn's items do not live in any one
// candidate.
func turnItemsOf(t testing.TB, candidates []appitempaging.TranscriptItemCandidate, turnID string) []appwire.ThreadItem {
	t.Helper()
	var items []appwire.ThreadItem
	for _, c := range candidates {
		if c.TurnID == turnID {
			items = append(items, c.Item)
		}
	}
	if len(items) == 0 {
		t.Fatalf("no item of turn %s", turnID)
	}
	return items
}

// replayedTurnOf projects the file the way the hub's whole-file fallback does:
// ProjectTurn through ItemTurnsFromFile, flushed as every production caller
// flushes it.
func replayedTurnOf(t testing.TB, path, turnID string) appwire.Turn {
	t.Helper()
	for _, turn := range replayedTurns(t, path) {
		if turn.ID == turnID {
			return turn
		}
	}
	t.Fatalf("whole-file read has no turn %q", turnID)
	return appwire.Turn{}
}

// replayedTurns is the whole-file read's own turn list, groups included.
func replayedTurns(t testing.TB, path string) []appwire.Turn {
	t.Helper()
	reg := apptranscript.NewToolCallRegistry()
	turns, err := apptranscript.ItemTurnsFromFile(path, testMaxLineBytes, func(turn schema.Turn, id string, entryIndex int) []appwire.ThreadItem {
		return apptranscript.ProjectTurn(id, entryIndex, turn, reg, nil, apptranscript.ToolResultOutputImages)
	})
	if err != nil {
		t.Fatalf("ItemTurnsFromFile: %v", err)
	}
	apptranscript.FlushUnpairedCommunicates(&turns, reg)
	return turns
}

// itemShapes is the comparable identity of an item: what it claims to BE, not
// its wording, so a difference in type or name shows and a difference in
// formatting does not.
func itemShapes(items []appwire.ThreadItem) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Type+"/"+item.ToolName+"/"+item.CallID+"/"+item.Status)
	}
	return out
}

// TestAProviderAliasCallSettlesItsCanonicalResult covers the alias half of the
// settlement seam, and it is the everyday shape rather than a corner: an
// OpenAI surface announces exec_command and the result records shell.
//
// The writer records the two names from different places on purpose. The
// assistant turn persists the response message, so it keeps the name the
// provider made the model say; dispatch canonicalizes a COPY (ToolCalls
// returns copies), and the result turn records that canonical name. Settlement
// therefore cannot compare spellings: exec_command and shell are one tool, and
// a guard that says otherwise emits a second row for a single call and lets a
// later completion mark the successful announcement interrupted — a failure
// the user never had (#1166 review).
func TestAProviderAliasCallSettlesItsCanonicalResult(t *testing.T) {
	fx := fixture{
		name:   "provider alias settles its canonical result",
		header: transcript.Header{SessionID: "alias_settles", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"},
		lines: []fixtureLine{
			entryLine(opens("turn_alias", schema.TurnSpanExecution, user("go"))),
			entryLine(inTurn("turn_alias", assistant(
				call("call_alias", "exec_command", `{"cmd":"ls"}`),
			))),
			entryLine(inTurn("turn_alias", results(result("call_alias", "shell", "a.txt")))),
			entryLine(completion("turn_alias", schema.TurnCompleted, 99, 10)),
		},
	}
	path := writeFixture(t, fx)
	indexed := turnItemsOf(t, allCandidates(t, openIndex(t, path, t.TempDir())), "turn_alias")
	replayed := replayedTurnOf(t, path, "turn_alias")

	for _, reader := range []struct {
		name  string
		items []appwire.ThreadItem
	}{{"index", indexed}, {"replay", replayed.Items}} {
		rows, settled, interrupted := 0, "", false
		for _, item := range reader.items {
			if item.CallID != "call_alias" {
				continue
			}
			rows++
			if item.Type == "commandExecution" && item.Status == appwire.TurnStatusCompleted {
				settled = item.ToolName
			}
			if item.Status == appwire.TurnStatusInterrupted {
				interrupted = true
			}
		}
		// One call, one row: the announcement the client already saw, settled
		// where it stands. A second row is the standalone result.
		if rows != 1 {
			t.Errorf("%s: rows for one aliased call = %d, want 1: exec_command and shell are the same tool, not two calls (items %s)",
				reader.name, rows, itemShapes(reader.items))
		}
		if settled == "" {
			t.Errorf("%s: no completed command for the call: the result did not settle the announcement (items %s)",
				reader.name, itemShapes(reader.items))
		}
		if interrupted {
			t.Errorf("%s: the settled call reads interrupted: settlement never matched, so the completion closed it as unanswered (items %s)",
				reader.name, itemShapes(reader.items))
		}
	}
}

// TestAReadBackFoldKeepsTheReservedCommandOwnOutput covers read-back's half of
// the shared-id fold. Two VISIBLE tools reuse one id in an inherited batch, and
// their named results arrive in one TURN_TOOL_RESULTS entry. The builder
// settles the reservation's own part (the first recorded call) and keeps the
// other result as its own row.
//
// Read-back reprojects that whole contributor entry, and a fold that merges
// every result carrying the id overwrites the reserved command's name and
// output with the other tool's — while the other tool ALSO keeps its own row.
// The user sees one command reporting another tool's output. The fold has to ask
// the question the builder asked: does this result belong to this call, not
// merely does it carry this id (#1166 review).
func TestAReadBackFoldKeepsTheReservedCommandOwnOutput(t *testing.T) {
	fx := fixture{
		name:   "read back fold keeps the reserved output",
		header: transcript.Header{SessionID: "fold_reserved", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"},
		lines: []fixtureLine{
			entryLine(opens("turn_fold", schema.TurnSpanExecution, user("go"))),
			entryLine(inTurn("turn_fold", inheritedAuthorship(assistant(
				call("call_twice", "read_file", `{"path":"a"}`),
				call("call_twice", "shell", `{"cmd":"ls"}`),
			)))),
			// Both results in ONE entry, each naming its own tool.
			entryLine(inTurn("turn_fold", results(
				result("call_twice", "read_file", "the bytes"),
				result("call_twice", "shell", "a.txt"),
			))),
			entryLine(completion("turn_fold", schema.TurnCompleted, 99, 10)),
		},
	}
	path := writeFixture(t, fx)
	indexed := turnItemsOf(t, allCandidates(t, openIndex(t, path, t.TempDir())), "turn_fold")
	replayed := replayedTurnOf(t, path, "turn_fold")

	for _, reader := range []struct {
		name  string
		items []appwire.ThreadItem
	}{{"index", indexed}, {"replay", replayed.Items}} {
		reserved, other := "", ""
		for _, item := range reader.items {
			if item.CallID != "call_twice" || item.Type != "commandExecution" {
				continue
			}
			switch item.ToolName {
			case "read_file":
				reserved = item.Output
			case "shell":
				other = item.Output
			}
		}
		if reserved != "the bytes" {
			t.Errorf("%s: reserved call output = %q, want %q: the other tool's result must not overwrite it (items %s)",
				reader.name, reserved, "the bytes", itemShapes(reader.items))
		}
		if other != "a.txt" {
			t.Errorf("%s: the other tool's own row is missing its output: got %q (items %s)",
				reader.name, other, itemShapes(reader.items))
		}
	}
}

// TestAReadBackKeepsTheRecordedDeliveryPart covers read-back when ONE results
// entry carries both halves of a reused id: the unnamed result for the
// reserved command, then the named result for the deferred communicate.
//
// The builder resolves each part in transcript order. The unnamed part settles
// the reserved read_file and leaves the pending delivery alone; the second,
// named part is the delivery, and the builder records it at its own part. That
// record is what read-back must reproduce.
//
// Read-back reprojects the entry with a registry rebuilt from the persisted
// contributor name alone, so it cannot see the reservation the builder had. An
// unnamed first part then falls through to the pending-communicate state, takes
// the delivery's arguments, and the recorded delivery part projects nothing —
// leaving the page claiming a part that is not there, which reads as corruption
// and rebuilds to the same record. The test therefore asserts on the record's
// own answer: the page returns BOTH items, before and after a rebuild.
func TestAReadBackKeepsTheRecordedDeliveryPart(t *testing.T) {
	fx := fixture{
		name:   "read back keeps the recorded delivery part",
		header: transcript.Header{SessionID: "readback_delivery", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"},
		lines: []fixtureLine{
			entryLine(opens("turn_both", schema.TurnSpanExecution, user("go"))),
			entryLine(inTurn("turn_both", inheritedAuthorship(assistant(
				call("call_both", "read_file", `{"path":"a"}`),
				call("call_both", "communicate", `{"message":"the delivery"}`),
			)))),
			// One entry, two parts: the command's unnamed result, then the
			// delivery's named one.
			entryLine(inTurn("turn_both", results(
				result("call_both", "", "the bytes"),
				result("call_both", "communicate", "the delivery"),
			))),
			entryLine(completion("turn_both", schema.TurnCompleted, 99, 10)),
		},
	}
	path := writeFixture(t, fx)
	dir := t.TempDir()

	assertBoth := func(t *testing.T, label string, items []appwire.ThreadItem) {
		t.Helper()
		command, delivered := "", false
		for _, item := range items {
			switch item.Type {
			case "commandExecution":
				if item.ToolName == "read_file" && item.Status == appwire.TurnStatusCompleted {
					command = item.ToolName
				}
			case "agentMessage":
				if strings.Contains(item.Text, "the delivery") {
					delivered = true
				}
			}
		}
		if command == "" {
			t.Errorf("%s: the reserved command is missing or unsettled (items %s)",
				label, itemShapes(items))
		}
		if !delivered {
			t.Errorf("%s: the recorded delivery part did not render: the unnamed result consumed state that belonged to it (items %s)",
				label, itemShapes(items))
		}
	}

	first := openIndex(t, path, dir)
	assertBoth(t, "first read", turnItemsOf(t, allCandidates(t, first), "turn_both"))

	// The record itself is what is wrong, so a rebuild must not "fix" it: the
	// same shape comes back.
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	rebuilt := openIndex(t, path, dir)
	assertBoth(t, "after rebuild", turnItemsOf(t, allCandidates(t, rebuilt), "turn_both"))
}

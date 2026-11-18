package transcriptindex

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
)

// TestAReplacedBatchDoesNotNameAStaleResult pins the boundary of the name
// tables a stamped result may read.
//
// A turn's awaiting entry is the LATEST assistant entry that recorded a call.
// A later batch replaces that turn's reservation wholesale (stampFold moves
// AwaitingOffset; the projection's awaitingNames does the same), so an id the
// new batch does not mention is no longer awaiting anything: the index resolves
// such a result to no name at all. Replay kept a cumulative name table across
// the whole file, so it named the same result from an announcement it had
// already replaced — and when that stale name was communicate, the result
// entered the delivery branch with no arguments to render and was dropped
// entirely (#1166 review).
func TestAReplacedBatchDoesNotNameAStaleResult(t *testing.T) {
	fx := fixture{
		name:   "replaced batch",
		header: transcript.Header{SessionID: "replaced_batch", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"},
		lines: []fixtureLine{
			entryLine(opens("turn_batch", schema.TurnSpanExecution, user("go"))),
			entryLine(inTurn("turn_batch", assistant(call("call_stale", "read_file", `{"path":"a"}`)))),
			// A later entry records a different call, so it becomes the turn's
			// awaiting entry and drops call_stale from the batch.
			entryLine(inTurn("turn_batch", assistant(call("call_live", "read_file", `{"path":"b"}`)))),
			entryLine(inTurn("turn_batch", results(
				result("call_stale", "", "the stale bytes"),
				result("call_live", "", "the live bytes"),
			))),
			entryLine(completion("turn_batch", schema.TurnCompleted, 99, 10)),
		},
	}
	path := writeFixture(t, fx)

	indexed := turnItemsOf(t, allCandidates(t, openIndex(t, path, t.TempDir())), "turn_batch")
	replayed := replayedTurnOf(t, path, "turn_batch")

	for _, reader := range []struct {
		name  string
		items []appwire.ThreadItem
	}{{"index", indexed}, {"replay", replayed.Items}} {
		var stale, live *appwire.ThreadItem
		for i := range reader.items {
			switch reader.items[i].CallID {
			case "call_stale":
				stale = &reader.items[i]
			case "call_live":
				live = &reader.items[i]
			}
		}
		if stale == nil {
			t.Errorf("%s: the replaced batch's result is missing (items %s): its own row is still owed",
				reader.name, itemShapes(reader.items))
		} else if stale.ToolName != "" {
			t.Errorf("%s: stale result tool = %q, want no name: the current batch does not mention its id",
				reader.name, stale.ToolName)
		}
		if live == nil {
			t.Fatalf("%s: the current batch's result is missing (items %s)", reader.name, itemShapes(reader.items))
		}
		if live.ToolName != "read_file" {
			t.Errorf("%s: live result tool = %q, want read_file: its own batch announced it",
				reader.name, live.ToolName)
		}
	}
}

// TestAFreshStampedBatchKeepsItsRejectedCommunicate pins the one communicate
// shape a FRESH stamped session can hold and the inherited gate does not
// describe: one batch recording a visible command and a communicate under the
// same provider id, where the communicate is rejected at prevalidation.
//
// The rejection is a row in its own right. Live suppresses a rejected
// communicate entirely, so the transcript's TOOL_RESULTS entry is the only
// record it happened, and a reader that drops it shows a call that simply
// vanished. Both readers must keep it, with the arguments the model sent.
func TestAFreshStampedBatchKeepsItsRejectedCommunicate(t *testing.T) {
	rejected := result("call_fresh_rej", "communicate", "invalid arguments")
	rejected.ToolResult.IsError = true
	rejected.ToolResult.PrevalOnly = true
	fx := fixture{
		name:   "fresh stamped rejected communicate",
		header: transcript.Header{SessionID: "fresh_rejected", CreatedAt: fixtureClock, ProfileID: "p", Model: "m"},
		lines: []fixtureLine{
			entryLine(opens("turn_fresh", schema.TurnSpanExecution, user("go"))),
			entryLine(inTurn("turn_fresh", assistant(
				call("call_fresh_rej", "read_file", `{"path":"a"}`),
				call("call_fresh_rej", "communicate", `{"message":"hi"}`),
			))),
			entryLine(inTurn("turn_fresh", results(rejected))),
			entryLine(completion("turn_fresh", schema.TurnCompleted, 99, 10)),
		},
	}
	path := writeFixture(t, fx)

	indexed := turnItemsOf(t, allCandidates(t, openIndex(t, path, t.TempDir())), "turn_fresh")
	replayed := replayedTurnOf(t, path, "turn_fresh")

	for _, reader := range []struct {
		name  string
		items []appwire.ThreadItem
	}{{"index", indexed}, {"replay", replayed.Items}} {
		var rejection *appwire.ThreadItem
		for i := range reader.items {
			it := &reader.items[i]
			if it.CallID == "call_fresh_rej" && it.PrevalOnly {
				rejection = it
			}
		}
		if rejection == nil {
			t.Errorf("%s: the rejected communicate's row is missing (items %s): a rejected call must stay visible",
				reader.name, itemShapes(reader.items))
			continue
		}
		if rejection.ToolName != "communicate" {
			t.Errorf("%s: rejection tool = %q, want communicate", reader.name, rejection.ToolName)
		}
		if rejection.Status != appwire.TurnStatusFailed {
			t.Errorf("%s: rejection status = %q, want failed", reader.name, rejection.Status)
		}
		if rejection.ArgumentsJSON == "" {
			t.Errorf("%s: rejection carries no arguments: the sent bytes are the only record of what was refused",
				reader.name)
		}
	}
}

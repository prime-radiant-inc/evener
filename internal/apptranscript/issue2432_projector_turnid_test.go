package apptranscript

import (
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// recordedProjection captures the turnID argument an EntryProjector received
// for each entry, in entry order, so a test can assert which logical-turn id
// each entry was projected under — independent of the post-hoc override in
// groupedAppTurnProjection (items[j].TurnID = group.turnID) that masks the
// argument the projector actually saw.
type recordedProjection struct {
	turnIDs []string
	kinds   []schema.TurnKind
}

// recordingProjector returns an EntryProjector that records the turnID argument
// (and entry kind) it is invoked with for each entry, in call order, and emits
// no items (the recorded argument is the whole point; items would only be
// overwritten after the fact by groupedAppTurnProjection anyway).
func recordingProjector(rec *recordedProjection) EntryProjector {
	return func(turn schema.Turn, turnID string, turnIndex int) []appwire.ThreadItem {
		rec.turnIDs = append(rec.turnIDs, turnID)
		rec.kinds = append(rec.kinds, turn.Kind)
		return nil
	}
}

// steeringOpenerOwnerFixture builds a transcript where a USER_INPUT opens a
// logical turn (turn_user), an ASSISTANT continues it, and then a TurnSteering
// entry whose OwningTurnID names a DIFFERENT turn (turn_other) OPENS its own
// group per appendEntry case 2. appendEntry will buffer the steering entry
// under group turnID "turn_other"; the question is which turnID the projector
// receives for that entry.
func steeringOpenerOwnerFixture() []transcript.Entry {
	return []transcript.Entry{
		// 1. USER_INPUT opens group turn_user.
		{Kind: "entry", Seq: 1, Turn: schema.Turn{
			Kind: schema.TurnUserInput, StableTurnID: "turn_user", Message: llm.User("first"),
		}},
		// 2. ASSISTANT continues turn_user.
		{Kind: "entry", Seq: 2, Turn: schema.Turn{
			Kind: schema.TurnAssistant, StableTurnID: "turn_a1",
			Message: llm.Message{Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "reply"},
			}},
		}},
		// 3. TurnSteering with OwningTurnID "turn_other" (differs from the open
		// group's "turn_user") opens its own group with turnID "turn_other"
		// per appendEntry case 2.
		{Kind: "entry", Seq: 3, Turn: schema.Turn{
			Kind: schema.TurnSteering, StableTurnID: "mutation_s1", OwningTurnID: "turn_other",
			Message: llm.User("steer to other"),
		}},
	}
}

// steeringOpenerGoalFixture builds a transcript where a USER_INPUT opens a
// logical turn (turn_user), an ASSISTANT continues it, and then a TurnSteering
// entry with GoalContinuation set OPENS its own group per appendEntry case 1.
// appendEntry will buffer the goal steering under its own persistedTurnID
// ("turn_goal"); the question is which turnID the projector receives.
func steeringOpenerGoalFixture() []transcript.Entry {
	return []transcript.Entry{
		// 1. USER_INPUT opens group turn_user.
		{Kind: "entry", Seq: 1, Turn: schema.Turn{
			Kind: schema.TurnUserInput, StableTurnID: "turn_user", Message: llm.User("first"),
		}},
		// 2. ASSISTANT continues turn_user.
		{Kind: "entry", Seq: 2, Turn: schema.Turn{
			Kind: schema.TurnAssistant, StableTurnID: "turn_a1",
			Message: llm.Message{Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: "reply"},
			}},
		}},
		// 3. TurnSteering with GoalContinuation opens its own group with turnID
		// equal to its persistedTurnID ("turn_goal") per appendEntry case 1.
		{Kind: "entry", Seq: 3, Turn: schema.Turn{
			Kind: schema.TurnSteering, StableTurnID: "turn_goal",
			GoalContinuation: &schema.GoalContinuationInfo{Text: "goal notice"},
			Message:          llm.User("goal continue"),
		}},
	}
}

// TestProjectorSeesSteeringOwnerOpenerTurnID asserts that an EntryProjector is
// invoked with the SAME turn id appendEntry will buffer the entry's group under.
// A TurnSteering entry whose OwningTurnID differs from the open group opens a
// new group (appendEntry case 2) with turnID == OwningTurnID; the projector must
// receive that owner id, not the previously open group's id. Before the fix,
// appendProjectedEntry projected the steering entry under the open group's id
// (continuesLogicalTurn(TurnSteering) is true), so the projector recorded the
// wrong turn. groupedAppTurnProjection's post-hoc override masked the mismatch
// for items, but any projector side effect keyed on the turnID argument would
// attach to the wrong logical turn. See issue #2432.
func TestProjectorSeesSteeringOwnerOpenerTurnID(t *testing.T) {
	entries := steeringOpenerOwnerFixture()
	var rec recordedProjection
	if _, err := ItemTurnsFromEntries(transcript.Header{}, entries, recordingProjector(&rec)); err != nil {
		t.Fatalf("ItemTurnsFromEntries: %v", err)
	}
	if len(rec.turnIDs) != len(entries) {
		t.Fatalf("projector invoked %d times, want %d; turnIDs=%v", len(rec.turnIDs), len(entries), rec.turnIDs)
	}
	// Entry 3 (the owner-differs steering) must be projected under "turn_other"
	// (its OwningTurnID, the group it opens), not "turn_user" (the prior open
	// group).
	const steeringEntryIdx = 2 // 0-based: entries[2]
	if got, want := rec.turnIDs[steeringEntryIdx], "turn_other"; got != want {
		t.Fatalf("steering owner-opener projected under turnID %q, want %q (the group it opens); recorded turnIDs=%v", got, want, rec.turnIDs)
	}
}

// TestProjectorSeesSteeringGoalOpenerTurnID asserts the goal-continuation case:
// a TurnSteering with GoalContinuation opens its own group (appendEntry case 1)
// with turnID equal to its own persistedTurnID ("turn_goal"); the projector must
// receive that id, not the prior open group's. See issue #2432.
func TestProjectorSeesSteeringGoalOpenerTurnID(t *testing.T) {
	entries := steeringOpenerGoalFixture()
	var rec recordedProjection
	if _, err := ItemTurnsFromEntries(transcript.Header{}, entries, recordingProjector(&rec)); err != nil {
		t.Fatalf("ItemTurnsFromEntries: %v", err)
	}
	if len(rec.turnIDs) != len(entries) {
		t.Fatalf("projector invoked %d times, want %d; turnIDs=%v", len(rec.turnIDs), len(entries), rec.turnIDs)
	}
	const steeringEntryIdx = 2 // 0-based: entries[2]
	if got, want := rec.turnIDs[steeringEntryIdx], "turn_goal"; got != want {
		t.Fatalf("steering goal-opener projected under turnID %q, want %q (the group it opens); recorded turnIDs=%v", got, want, rec.turnIDs)
	}
}

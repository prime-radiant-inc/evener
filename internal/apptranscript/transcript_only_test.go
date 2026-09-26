package apptranscript

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/schema/schematest"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// transcriptOnlyProjectionFixture covers every grouping shape: client-mutation
// turns with tool rounds, a standalone hook and model switch, owned steering,
// a failure, and interrupted steering.
func transcriptOnlyProjectionFixture() []transcript.Entry {
	entries := logicalTurnFixture()
	hook := schema.Turn{Kind: schema.TurnHookCompleted, Message: llm.User("hook"), Hook: &schema.HookInfo{Event: "Stop"}}
	modelSwitch := schema.Turn{Kind: schema.TurnModelSwitch, Message: llm.User("switched"), ModelSwitch: &schema.ModelSwitchInfo{OldModel: "a", NewModel: "b"}}
	failure := schema.Turn{Kind: schema.TurnFailure, Message: llm.System("boom"), Error: &schema.TurnFailureInfo{Message: "boom"}}
	for _, turn := range []schema.Turn{
		hook,
		modelSwitch,
		{Kind: schema.TurnUserInput, Message: llm.User("third"), StableTurnID: "turn_m3"},
		{Kind: schema.TurnSteering, Message: llm.User("owned steer"), OwningTurnID: "turn_m3"},
		failure,
		{Kind: schema.TurnUserInput, Message: llm.User("fourth")},
		{Kind: schema.TurnSteering, Message: llm.User("interrupted"), SteeringKind: events.SteeringKindInterrupted},
	} {
		entries = append(entries, transcript.Entry{Kind: "entry", Seq: len(entries) + 1, Turn: turn})
	}
	return entries
}

func interleavedEntries(entries []transcript.Entry) []transcript.Entry {
	turns := make([]schema.Turn, len(entries))
	for i, entry := range entries {
		turns[i] = entry.Turn
	}
	var out []transcript.Entry
	for i, turn := range schematest.InterleaveTranscriptOnly(turns) {
		out = append(out, transcript.Entry{Kind: "entry", Seq: i + 1, Turn: turn})
	}
	return out
}

// normalizeEntryIndexIdentity blanks what today's projection derives from an
// entry's line index: the turn id of a group whose opener has no stable id,
// item ids, entry indexes and the keys built from them. A transcript-only
// line consumes an index like any entry line, so these shift by design; every
// other field, and every stable turn id, must not.
func normalizeEntryIndexIdentity(t *testing.T, turns []appwire.Turn) string {
	t.Helper()
	out := make([]appwire.Turn, len(turns))
	for i, turn := range turns {
		stable := strings.HasPrefix(turn.ID, "turn_m") || turn.ID == appwire.SystemPreludeTurnID
		if !stable {
			turn.ID = fmt.Sprintf("group_%d", i)
		}
		items := make([]appwire.ThreadItem, len(turn.Items))
		for j, item := range turn.Items {
			item.ID, item.TranscriptEntryIndex, item.TurnID = "", 0, turn.ID
			if !stable {
				item.TranscriptKey = ""
			}
			items[j] = item
		}
		turn.Items = items
		out[i] = turn
	}
	data, err := json.MarshalIndent(out, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestFileProjectionPassesOverTranscriptOnlyEntries(t *testing.T) {
	plain := writeEntries(t, transcriptOnlyProjectionFixture()...)
	interleaved := writeEntries(t, interleavedEntries(transcriptOnlyProjectionFixture())...)
	want := requireItemTurnsFromFile(t, plain, testMaxLineBytes, sequentialTestProjector())
	got := requireItemTurnsFromFile(t, interleaved, testMaxLineBytes, sequentialTestProjector())
	if w, g := normalizeEntryIndexIdentity(t, want), normalizeEntryIndexIdentity(t, got); g != w {
		t.Fatalf("full projection differs:\n got %s\nwant %s", g, w)
	}
}

func TestProjectTurnPartsProjectsNothingForTranscriptOnlyKinds(t *testing.T) {
	for _, sample := range schematest.TranscriptOnlySamples() {
		if items, parts := ProjectTurnParts("turn_1", 1, sample, map[string]string{}, nil, nil); len(items) != 0 || len(parts) != 0 {
			t.Errorf("%s projected %v", sample.Kind, items)
		}
	}
}

// Derived totals (usage, failed tool calls) read every entry line; the
// transcript-only entries carry no usage and no tool parts, so they add
// nothing.
func TestDerivedTotalsIgnoreTranscriptOnlyEntries(t *testing.T) {
	fixture := transcriptOnlyProjectionFixture()
	fixture[1].Turn.Usage = llm.Usage{InputTokens: 7, OutputTokens: 3, TotalTokens: 10}
	failed := toolResultEntry(0, "call_a", "read_file", "boom")
	failed.Turn.Message.Content[0].ToolResult.IsError = true
	fixture = append(fixture, failed)
	for i := range fixture {
		fixture[i].Seq = i + 1
	}
	plain := writeEntries(t, fixture...)
	interleaved := writeEntries(t, interleavedEntries(fixture)...)
	wantUsage, wantFailed, err := NewTurnCache().DerivedTotalsFromFile(plain, testMaxLineBytes, 0)
	if err != nil {
		t.Fatal(err)
	}
	if wantUsage == nil || wantFailed == 0 {
		t.Fatalf("the fixture counts nothing (usage %v, failed %d)", wantUsage, wantFailed)
	}
	gotUsage, gotFailed, err := NewTurnCache().DerivedTotalsFromFile(interleaved, testMaxLineBytes, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotUsage, wantUsage) || gotFailed != wantFailed {
		t.Fatalf("totals = (%+v, %d), want (%+v, %d)", gotUsage, gotFailed, wantUsage, wantFailed)
	}
}

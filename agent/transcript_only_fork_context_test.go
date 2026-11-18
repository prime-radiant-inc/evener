package agent

import (
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/schema/schematest"
	"primeradiant.com/evener/llm"
)

// A parent that compacted before forking hands the child an original, a
// compaction marker, and the copy the fold re-appended after it. The child
// records all three, as it must: its own resume anchors on the last marker and
// keeps only the copy. But the inherited turn is one turn, so the child's
// public view must show it once — the copy stays a copy, naming its original in
// the child's own entry coordinates rather than losing the relation and taking
// a public position of its own.
func TestDelegateContextKeepsFoldCopyRelation(t *testing.T) {
	t.Parallel()
	const text = "one message, folded before the fork"
	original := schema.NewTurn(schema.TurnUserInput, llm.User(text))
	marker := schema.NewTurn(schema.TurnCheckpoint, llm.User("[CONTEXT CHECKPOINT]\nfolded\n[END CHECKPOINT]"))
	// The copy names its original's index in the parent's entries: the
	// model-switch record above shifts it to 1, while the child records the
	// original at 0.
	ordinal := uint64(1)
	copied := schema.NewTurn(schema.TurnUserInput, llm.User(text))
	copied.OriginalOrdinal = &ordinal

	// A model-switch record before the original is dropped from the inherited
	// conversation, so the original's child position (0) differs from its parent
	// ordinal (1): carrying the parent index over unchanged cannot pass, and an
	// ordinal naming the marker instead would show up here too.
	switchTurn := schema.NewTurn(schema.TurnModelSwitch, llm.System("model-marker"))
	inherited := delegateContextEntries(entriesOf([]schema.Turn{switchTurn, original, marker, copied}))
	if len(inherited) != 3 {
		t.Fatalf("child inherited %d records, want the original, the marker and its copy", len(inherited))
	}
	wantOriginal := -1
	copyAt := -1
	for i := range inherited {
		turn := inherited[i].Turn
		if turn.OriginalOrdinal != nil {
			copyAt = i
			continue
		}
		if wantOriginal < 0 && strings.Contains(turn.Message.Text(), text) {
			wantOriginal = i
		}
	}
	if wantOriginal < 0 {
		t.Fatal("the child did not inherit the original")
	}
	if copyAt < 0 {
		t.Fatal("the inherited copy lost its original_ordinal: the child renders the turn twice")
	}
	if wantOriginal != 0 {
		t.Fatal("the fixture did not shift the original off ordinal 0, so it cannot tell a remapped ordinal from the parent's")
	}
	if got := int(*inherited[copyAt].Turn.OriginalOrdinal); got != wantOriginal {
		t.Fatalf("the copy names entry %d, want %d: the inherited original's own position in the child", got, wantOriginal)
	}

	public := publicTranscriptEntries(inherited)
	if len(public) != 2 {
		t.Fatalf("child's public view holds %d entries, want 2 (the original and the marker; the copy repeats the original)", len(public))
	}
	seen := 0
	for _, entry := range public {
		seen += strings.Count(entry.Turn.Message.Text(), text)
	}
	if seen != 1 {
		t.Fatalf("child's public view shows the inherited fold pair %d times, want exactly 1", seen)
	}

	// The retained record is what the child's own resume reads, and it must
	// still supply the turn: dropping the copy would leave the anchor window
	// with nothing to present after the marker.
	resumed := ResumeHistory(inherited)
	found := 0
	for _, turn := range resumed {
		found += strings.Count(turn.Message.Text(), text)
	}
	if found != 1 {
		t.Fatalf("child's resumed history carries the inherited turn %d times, want exactly 1", found)
	}
}

// A forked delegate inherits the same conversation, cut at the same round,
// whether or not its parent's transcript holds transcript-only entries.
func TestDelegateContextSkipsTranscriptOnlyEntries(t *testing.T) {
	complete := transcriptOnlyFixtureTurns()[:3] // user, call, its result
	open := transcriptOnlyFixtureTurns()[:4]     // ... and a call with no result yet
	for name, plainTurns := range map[string][]schema.Turn{"complete": complete, "open round": open} {
		t.Run(name, func(t *testing.T) {
			want := delegateContextEntries(entriesOf(plainTurns))
			got := delegateContextEntries(entriesOf(schematest.InterleaveTranscriptOnly(plainTurns)))
			if len(got) != len(want) {
				t.Fatalf("inherited %d entries, want %d", len(got), len(want))
			}
			for i := range got {
				if !reflect.DeepEqual(got[i].Turn, want[i].Turn) {
					t.Fatalf("entry %d = %+v, want %+v", i, got[i].Turn, want[i].Turn)
				}
			}
		})
	}
}

func TestConversationSignalsIgnoreTranscriptOnlyEntries(t *testing.T) {
	plain := []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User("u")), schema.NewTurn(schema.TurnAssistant, llm.Assistant("a"))}
	want, _ := (&Session{history: plain}).conversationSignals()
	if got, _ := (&Session{history: schematest.InterleaveTranscriptOnly(plain)}).conversationSignals(); got != want {
		t.Fatalf("history turns = %d, want %d", got, want)
	}
}

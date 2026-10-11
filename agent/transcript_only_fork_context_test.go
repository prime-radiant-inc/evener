package agent

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/schema/schematest"
	"primeradiant.com/evener/llm"
)

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

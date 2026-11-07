package agent

import (
	"errors"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
)

func TestVoicePendingAsk_ReplayTailUsesOriginalRecords(t *testing.T) {
	t.Parallel()
	ordinal := uint64(1)
	entries := []transcript.Entry{
		{Turn: schema.Turn{Kind: schema.TurnAssistant, TurnID: "older"}},
		{Turn: schema.Turn{Kind: schema.TurnAssistant, TurnID: "pending-a"}},
		{Turn: schema.Turn{Kind: schema.TurnToolResults, TurnID: "pending-a"}},
		{Turn: schema.Turn{Kind: schema.TurnSummary}},
		{Turn: schema.Turn{Kind: schema.TurnAssistant, TurnID: "pending-a", OriginalOrdinal: &ordinal}},
		{Turn: schema.Turn{Kind: schema.TurnAssistant, TurnID: "pending-b"}},
	}
	tail, err := pendingAskReplayTail(entries, []string{"pending-b", "pending-a", "pending-a"})
	if err != nil {
		t.Fatal(err)
	}
	var got []uint64
	for _, turn := range tail {
		if turn.OriginalOrdinal == nil {
			t.Fatal("copy has no original identity")
		}
		got = append(got, *turn.OriginalOrdinal)
	}
	if !reflect.DeepEqual(got, []uint64{1, 2, 5}) {
		t.Fatalf("replayed duplicate, marker or wrong tail: %v", got)
	}
	if tail, err := pendingAskReplayTail(entries, []string{"missing"}); !errors.Is(err, errPendingAskSourceMissing) || tail != nil {
		t.Fatalf("missing source fabricated tail: %+v err=%v", tail, err)
	}
}

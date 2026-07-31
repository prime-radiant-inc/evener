package transcript

import (
	"testing"

	"primeradiant.com/serf/appwire"
)

// TestApplyThreadItemCarriesTranscriptEntryIndexOntoUserMessages (kata e6q0)
// pins the field the TUI's fork draft reads as its divergence position.
//
// thread/fork's sourceTurnId is a 1-based index into the parent transcript's
// ENTRY list, and appwire.ThreadItem.TranscriptEntryIndex is the only field
// that names it. TurnIndex is the id-derived turn number, which coincides with
// the entry index only on a transcript replayed from disk; every live minter
// numbers turns off its own counter, so the two diverge the moment a turn
// contains more than one entry. Both are pinned here against the same item so
// a future change cannot quietly collapse one into the other.
func TestApplyThreadItemCarriesTranscriptEntryIndexOntoUserMessages(t *testing.T) {
	// A live projector-minted turn: the second user input opens turn_2 but is
	// transcript entry 3 (entry 1 = user, 2 = assistant, 3 = user).
	live := appwire.ThreadItem{
		Type:                 "userMessage",
		ID:                   "user_2",
		TurnID:               "turn_2",
		TranscriptEntryIndex: 3,
		Text:                 "second task",
	}

	t.Run("fresh append", func(t *testing.T) {
		reducer := NewTranscriptReducer(nil, nil, nil)
		reducer.ApplyThreadItem(live, TurnIndexFromID(live.TurnID), true)
		assertUserEntry(t, reducer.Messages(), 3, 2)
	})

	// The composer's optimistic echo carries no transcript position; the
	// authoritative item that reconciles with it must supply one.
	t.Run("reconciled with the composer echo", func(t *testing.T) {
		reducer := NewTranscriptReducer(nil, nil, nil)
		reducer.ApplyUserMessageEcho("second task")
		reducer.ApplyThreadItem(live, TurnIndexFromID(live.TurnID), true)
		assertUserEntry(t, reducer.Messages(), 3, 2)
	})

	// item/started then item/completed for the same id: the second pass
	// updates the row in place and must not drop the entry index.
	t.Run("updated in place by item id", func(t *testing.T) {
		reducer := NewTranscriptReducer(nil, nil, nil)
		reducer.ApplyThreadItem(live, TurnIndexFromID(live.TurnID), false)
		reducer.ApplyThreadItem(live, TurnIndexFromID(live.TurnID), true)
		assertUserEntry(t, reducer.Messages(), 3, 2)
	})

	// No entry index on the wire means "no persisted transcript position",
	// never entry 0 — the fork draft refuses on it rather than guessing.
	t.Run("absent stays zero", func(t *testing.T) {
		reducer := NewTranscriptReducer(nil, nil, nil)
		reducer.ApplyThreadItem(appwire.ThreadItem{
			Type:   "userMessage",
			ID:     "user_1",
			TurnID: "turn_1",
			Text:   "first task",
		}, 1, true)
		assertUserEntry(t, reducer.Messages(), 0, 1)
	})

	// A client-authored input is minted outside the entry-index namespace on
	// purpose (kata rk09), so its id resolves to no turn index at all — but the
	// hub still stamps the entry it occupies, and that entry is a real
	// divergence position. The fork draft reads the entry index, so these rows
	// are forkable rather than refused.
	t.Run("client-mutation turn id still names an entry", func(t *testing.T) {
		reducer := NewTranscriptReducer(nil, nil, nil)
		item := appwire.ThreadItem{
			Type:                 "userMessage",
			ID:                   "user_m",
			TurnID:               appwire.ClientMutationTurnID(7),
			TranscriptEntryIndex: 4,
			Text:                 "queued task",
		}
		reducer.ApplyThreadItem(item, TurnIndexFromID(item.TurnID), true)
		assertUserEntry(t, reducer.Messages(), 4, 0)
	})
}

func assertUserEntry(t *testing.T, messages []ChatMessage, wantEntry, wantTurn int) {
	t.Helper()
	if len(messages) != 1 {
		t.Fatalf("messages len=%d, want 1: %+v", len(messages), messages)
	}
	msg := messages[0]
	if msg.Kind != MsgUser {
		t.Fatalf("message kind=%v, want MsgUser: %+v", msg.Kind, msg)
	}
	if msg.TranscriptEntryIndex != wantEntry {
		t.Fatalf("TranscriptEntryIndex=%d, want %d — the fork draft cuts the child at this entry", msg.TranscriptEntryIndex, wantEntry)
	}
	if msg.TurnIndex != wantTurn {
		t.Fatalf("TurnIndex=%d, want %d", msg.TurnIndex, wantTurn)
	}
}

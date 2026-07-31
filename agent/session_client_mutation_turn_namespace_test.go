package agent

import (
	"fmt"
	"testing"

	"primeradiant.com/serf/appwire"
)

// TestClientMutation_ReservedTurnIDsCannotNameATranscriptEntry (kata rk09)
// pins the property that keeps a reserved user-turn id out of somebody else's
// turn: it must not be an id the transcript's own entry-index numbering can
// produce.
//
// internal/apptranscript numbers a persisted turn by its ENTRY INDEX
// ("turn_<index>"), and a session accumulates entries several times faster
// than it accumulates client mutations — one user reply is one mutation but
// three to five entries (the input, each model round, tool results). So a
// reservation minted off the mutation counter always names a LOW number, and
// once a restart reseeds the served snapshot from the transcript every low
// number is already taken by an unrelated early entry. The reply's turn then
// merges into that entry's turn, taking the whole agent response with it.
//
// Fencing this counter the way internal/appprojector fences its own
// (SeedPersistedTurns, kata eptj) does not work here: the entry index
// outgrows the mutation counter, so a fenced reservation falls behind and
// collides again within a few turns. The two namespaces have to be disjoint
// by construction.
func TestClientMutation_ReservedTurnIDsCannotNameATranscriptEntry(t *testing.T) {
	// A long session: far more persisted entries than client mutations, which
	// is the ordinary shape (and the one the collision needs).
	const transcriptEntries = 500
	entryForID := map[string]int{}
	for entryIndex := 1; entryIndex <= transcriptEntries; entryIndex++ {
		entryForID[fmt.Sprintf("turn_%d", entryIndex)] = entryIndex
	}

	snapshot := &clientMutationSnapshot{}
	for range 100 {
		record := &clientMutationRecord{}
		reserveClientMutationTurnID(snapshot, record)
		if entryIndex, collides := entryForID[record.StableTurnID]; collides {
			t.Fatalf("reservation %d minted turn id %q, which transcript entry %d already carries — the reply merges into that entry's turn after any restart",
				snapshot.NextTurnSequence, record.StableTurnID, entryIndex)
		}
		if record.StableTurnID == appwire.SystemPreludeTurnID {
			t.Fatalf("reservation %d minted the synthetic prelude id %q", snapshot.NextTurnSequence, record.StableTurnID)
		}
	}
}

// TestClientMutation_StartReceiptTurnIDIsOutsideTheEntryIndexNamespace (kata
// rk09) checks the same invariant through the public path a browser takes to
// answer a pending ask, so a reservation site that mints its own id instead
// of going through reserveClientMutationTurnID cannot reintroduce the
// collision unnoticed.
func TestClientMutation_StartReceiptTurnIDIsOutsideTheEntryIndexNamespace(t *testing.T) {
	sess := newTestSession(t)
	lifecycle, ok := any(sess).(clientMutationStartLifecycle)
	if !ok {
		t.Fatal("session has no durable start lifecycle")
	}
	lifecycle.SetClientMutationStartWakeFunc(func() {})

	response, err := lifecycle.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "namespace-check",
		Input:            []appwire.InputItem{{Type: "text", Text: "answer"}},
	})
	if err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	if response.Receipt.TurnID == "" {
		t.Fatal("start receipt carried no turn id")
	}
	for entryIndex := 1; entryIndex <= 500; entryIndex++ {
		if response.Receipt.TurnID == fmt.Sprintf("turn_%d", entryIndex) {
			t.Fatalf("start receipt turn id %q is the id transcript entry %d carries", response.Receipt.TurnID, entryIndex)
		}
	}
}

package agent

import (
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

// A kindless ordinary steering record rebuilt from the durable journal must keep
// every byte the user typed, even when the text imitates the exact shape a
// shared-notes update writes. The journal record persists the mutation method,
// and turn/steer is not notes/human/set, so provenance decides and the
// note-origin guess never strips a normal steer.
func TestRestoredKindlessOrdinarySteerWithNotePrefixKeepsBytes(t *testing.T) {
	s := newDurableHumanNoteSession(t)
	defer s.Close()
	const text = "human updated their whiteboard: \x1b[31mred\x1b[0m bytes"

	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		req := testClientMutationRequest(t, clientMutationMethodSteer, "cm-steer", appwire.TurnSteerParams{
			ClientMutationID: "cm-steer",
			Input:            []appwire.InputItem{{Type: "text", Text: text}},
		})
		snapshot.Journal["cm-steer"] = clientMutationRecord{
			ClientMutationID:  req.ClientMutationID,
			Method:            req.Method,
			Payload:           req.Payload,
			PayloadHash:       req.PayloadHash,
			OperationState:    clientMutationOperationApplied,
			ExecutionState:    "accepted",
			ProjectionState:   appwire.MutationProjectionPending,
			AttemptGeneration: 1,
		}
		snapshot.PendingExecutions["cm-steer"] = appwire.PendingMutation{
			ClientMutationID: "cm-steer",
			Method:           clientMutationMethodSteer,
			Input:            []appwire.InputItem{{Type: "text", Text: text}},
			ExecutionState:   "accepted",
		}
		snapshot.SteeringOrder = append(snapshot.SteeringOrder, "cm-steer")
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Re-read the journal from disk and rebuild the runtime queue, the restart
	// path that produced the finding.
	reloadHumanNoteStore(t, s)
	s.mu.Lock()
	queue := append([]steeringMessage(nil), s.steeringQueue...)
	s.mu.Unlock()
	if len(queue) != 1 {
		t.Fatalf("restored steering queue = %+v, want one entry", queue)
	}
	if queue[0].Text != text {
		t.Fatalf("restored ordinary steer = %q, want it verbatim (%q)", queue[0].Text, text)
	}
	if !strings.ContainsRune(queue[0].Text, '\x1b') {
		t.Fatalf("restored ordinary steer lost its control bytes: %q", queue[0].Text)
	}
}

// A kindless note-origin record persisted before SteeringKind existed still
// normalizes: notes/human/set is the persisted mutation method, so provenance
// proves the text came from the whiteboard and the load path strips it.
func TestRebuiltKindlessNoteSteerWithMethodStillNormalizes(t *testing.T) {
	snapshot := clientMutationSnapshot{
		SteeringOrder: []string{"cm-note"},
		Journal: map[string]clientMutationRecord{
			"cm-note": {ClientMutationID: "cm-note", Method: clientMutationMethodNotesHumanSet, ExecutionState: "accepted"},
		},
		PendingExecutions: clientMutationPendingExecutions{
			"cm-note": appwire.PendingMutation{
				ExecutionState: "accepted",
				Input:          []appwire.InputItem{{Type: "text", Text: "human updated their whiteboard: \x1b[31mnote\x1b[0m"}},
			},
		},
	}

	entries := clientSteeringFromSnapshot(snapshot)
	if len(entries) != 1 {
		t.Fatalf("rebuilt %d steering entries, want 1", len(entries))
	}
	if strings.ContainsRune(entries[0].Text, '\x1b') {
		t.Fatalf("kindless note-origin steer was not normalized: %q", entries[0].Text)
	}
	if !strings.Contains(entries[0].Text, "note") {
		t.Fatalf("kindless note-origin steer lost its content: %q", entries[0].Text)
	}
}

// A record older than both SteeringKind and a usable method has no provenance at
// all, so the write-path text shape remains the only marker: a prefix-shaped
// entry still normalizes, while text that does not imitate the shape keeps its
// bytes. This pins the fallback that must survive for truly old records.
func TestRebuiltSteerWithoutProvenanceKeepsPrefixFallback(t *testing.T) {
	rebuild := func(text string) string {
		snapshot := clientMutationSnapshot{
			SteeringOrder: []string{"cm-legacy"},
			Journal:       map[string]clientMutationRecord{"cm-legacy": {ClientMutationID: "cm-legacy"}},
			PendingExecutions: clientMutationPendingExecutions{
				"cm-legacy": appwire.PendingMutation{
					ExecutionState: "accepted",
					Input:          []appwire.InputItem{{Type: "text", Text: text}},
				},
			},
		}
		entries := clientSteeringFromSnapshot(snapshot)
		if len(entries) != 1 {
			t.Fatalf("rebuilt %d steering entries, want 1", len(entries))
		}
		return entries[0].Text
	}

	if got := rebuild("human updated their whiteboard: \x1b[31mlegacy\x1b[0m"); strings.ContainsRune(got, '\x1b') {
		t.Fatalf("provenance-free prefix-shaped steer was not normalized: %q", got)
	}
	const ordinary = "run the tests\twith \x1b[31mred\x1b[0m lines"
	if got := rebuild(ordinary); got != ordinary {
		t.Fatalf("provenance-free ordinary steer = %q, want it verbatim (%q)", got, ordinary)
	}
}

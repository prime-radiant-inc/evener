package agent

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
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

// The history copy handed to a resumed request or an inherited prefix is escaped
// from the turns alone, so a kindless steering turn keeps no kind to decide it.
// The journal record that wrote the turn says what it was and is still on disk
// when the copy is rebuilt, so the copy consults it exactly as the rebuilt queue
// entry does: an ordinary steer whose text imitates the note prefix keeps every
// byte, and a note-origin entry whose text lost the write-path shape still
// normalizes. Only a record that persisted neither field keeps the shape
// fallback.
func TestRestoredHistoryCopyKeepsOrdinarySteerThatImitatesTheNotePrefix(t *testing.T) {
	t.Parallel()
	const sessionID = "01KNOTEPROVENANCEESCAPE000"
	const text = "human updated their whiteboard: \x1b[31mred\x1b[0m bytes"
	stateDir := t.TempDir()

	// The record an ordinary client steer leaves in the journal: the mutation
	// method names the steer, and the steer was incorporated before the restart.
	store, err := newClientMutationStore(stateDir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.mutate(func(snapshot *clientMutationSnapshot) error {
		req := testClientMutationRequest(t, clientMutationMethodSteer, "cm-steer", appwire.TurnSteerParams{
			ClientMutationID: "cm-steer",
			Input:            []appwire.InputItem{{Type: "text", Text: text}},
		})
		snapshot.Journal["cm-steer"] = clientMutationRecord{
			ClientMutationID:  "cm-steer",
			Method:            clientMutationMethodSteer,
			PayloadHash:       req.PayloadHash,
			OperationState:    clientMutationOperationTerminal,
			ExecutionState:    "incorporated",
			ProjectionState:   appwire.MutationProjectionReflected,
			AttemptGeneration: 1,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	meta := schema.SessionMeta{
		ID:        sessionID,
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    (SessionConfig{NoProjectPrompts: true}).toSnapshot(),
	}
	restored, err := RestoreSessionFromMetaWithConfig(
		c,
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		meta,
		RestoreSessionConfig{
			StateDir: stateDir,
			resumeHistory: []schema.Turn{{
				Kind:             schema.TurnSteering,
				ClientMutationID: "cm-steer",
				Message:          llm.User(text),
			}},
		},
	)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer restored.Close()

	if got := modelBoundText(restored); !strings.Contains(got, text) {
		t.Fatalf("restored model context = %q, want the ordinary steer verbatim (%q)", got, text)
	}
}

// The history copy decides a kindless steering turn the same way the rebuilt
// queue entry does: the journal record that wrote it wins over the text shape,
// in both directions. Without a record the kind decides, and without either the
// write-path shape is the last marker left.
func TestHistoryCopyDecidesKindlessSteerProvenanceFromTheRecord(t *testing.T) {
	steer := func(id, text string) schema.Turn {
		return schema.Turn{Kind: schema.TurnSteering, ClientMutationID: id, Message: llm.User(text)}
	}
	origins := map[string]steeringOrigin{
		"cm-steer": {method: clientMutationMethodSteer},
		"cm-note":  {method: clientMutationMethodNotesHumanSet},
		"cm-kind":  {kind: events.SteeringKindHumanNote},
	}
	cases := map[string]struct {
		turn     schema.Turn
		stripped bool
	}{
		"ordinary steer imitating the note prefix keeps its bytes": {
			turn: steer("cm-steer", "human updated their whiteboard: \x1b[31mred\x1b[0m bytes"),
		},
		"note-origin steer that kept no kind of its own still normalizes": {
			turn:     steer("cm-note", "human updated their whiteboard: \x1b[31mnote\x1b[0m"),
			stripped: true,
		},
		"a record that kept only the kind decides a turn the shape would spare": {
			turn:     steer("cm-kind", "the whiteboard note was updated\x1b[31m without the write shape"),
			stripped: true,
		},
		"a record with no provenance of its own keeps the shape fallback": {
			turn:     steer("cm-old", "human updated their whiteboard: \x1b[31mlegacy\x1b[0m"),
			stripped: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			out := escapeNotesHistoryTurns([]schema.Turn{tc.turn}, origins)
			got := out[0].Message.Text()
			if tc.stripped {
				if strings.ContainsRune(got, 0x1b) {
					t.Fatalf("history copy = %q, want the note-origin control bytes stripped", got)
				}
				return
			}
			if want := tc.turn.Message.Text(); got != want {
				t.Fatalf("history copy = %q, want it verbatim (%q)", got, want)
			}
		})
	}
}

// The steer method decides, whatever an id spells: a steer whose id ends in the
// retired derivation keeps every byte the user typed and its empty kind, because
// nothing but a recorded kind or method is evidence (see steeringOriginFromJournal).
func TestRebuiltSteerWithNoteSteerSuffixButNoOuterRecordKeepsBytes(t *testing.T) {
	const id = "cm-impostor/note-steer"
	const text = "human updated their whiteboard: \x1b[31mtyped\x1b[0m bytes"
	snapshot := clientMutationSnapshot{
		SteeringOrder: []string{id},
		Journal: map[string]clientMutationRecord{
			id: {ClientMutationID: id, Method: clientMutationMethodSteer, ExecutionState: "accepted"},
		},
		PendingExecutions: clientMutationPendingExecutions{
			id: appwire.PendingMutation{
				ExecutionState: "accepted",
				Input:          []appwire.InputItem{{Type: "text", Text: text}},
			},
		},
	}

	entries := clientSteeringFromSnapshot(snapshot)
	if len(entries) != 1 {
		t.Fatalf("rebuilt %d steering entries, want 1", len(entries))
	}
	if entries[0].Text != text {
		t.Fatalf("unproven steer = %q, want it verbatim (%q)", entries[0].Text, text)
	}
	if entries[0].Kind != "" {
		t.Fatalf("unproven steer kind = %q, want the recorded empty kind", entries[0].Kind)
	}
}

// A turn that kept the note kind stays note-origin even when its journal record
// did not. The strip exists to keep note text out of the model copy, so evidence
// from either side decides and the record cannot demote a turn the write path
// already marked; the reverse -- a record that proves note origin -- is covered
// by the table test above.
func TestHistoryCopyKeepsTheTurnsOwnNoteKindOverAKindlessRecord(t *testing.T) {
	const text = "human updated their whiteboard: \x1b[31mnote\x1b[0m"
	cases := map[string]struct {
		turn    schema.Turn
		origins map[string]steeringOrigin
	}{
		"kindless record leaves the turn's note kind standing": {
			turn: schema.Turn{
				Kind: schema.TurnSteering, SteeringKind: events.SteeringKindHumanNote,
				ClientMutationID: "cm-note", Message: llm.User(text),
			},
			origins: map[string]steeringOrigin{"cm-note": {method: clientMutationMethodSteer}},
		},
		"a record that proves note origin still strips": {
			turn: schema.Turn{
				Kind: schema.TurnSteering, SteeringKind: events.SteeringKindInterrupted,
				ClientMutationID: "cm-note", Message: llm.User(text),
			},
			origins: map[string]steeringOrigin{"cm-note": {kind: events.SteeringKindHumanNote}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			out := escapeNotesHistoryTurns([]schema.Turn{tc.turn}, tc.origins)
			if got := out[0].Message.Text(); strings.ContainsRune(got, 0x1b) {
				t.Fatalf("history copy = %q, want the note-origin control bytes stripped", got)
			}
		})
	}
}

// The either-side-wins rule holds in both directions: a kindless note record
// decides a turn whose kind says something else, exactly as a turn that kept the
// note kind decides a record that kept none. The record's own rule decides it:
// with no kind, notes/human/set is the note write (isHumanNoteSteer).
func TestHistoryCopyKeepsARecordsNoteMethodOverAnUnrelatedTurnKind(t *testing.T) {
	turn := schema.Turn{
		Kind: schema.TurnSteering, SteeringKind: events.SteeringKindInterrupted,
		ClientMutationID: "cm-note",
		Message:          llm.User("human updated their whiteboard: \x1b[31mnote\x1b[0m"),
	}
	origins := map[string]steeringOrigin{"cm-note": {method: clientMutationMethodNotesHumanSet}}

	out := escapeNotesHistoryTurns([]schema.Turn{turn}, origins)
	if got := out[0].Message.Text(); strings.ContainsRune(got, 0x1b) {
		t.Fatalf("history copy = %q, want the note-origin control bytes stripped", got)
	}
}

// A note record beside a steer is not evidence about that steer: the note write's
// own record carries the note kind (or, when it changed nothing, a kindless
// notes/human/set), and no rule reads it to classify another mutation's record, so
// a steer whose id merely imitates an old derivation keeps its bytes and its empty
// kind.
func TestRebuiltSteerImitatingTheGrammarBesideAModernNoteRecordKeepsBytes(t *testing.T) {
	const id = "cm-note/note-steer"
	const text = "human updated their whiteboard: \x1b[31mforged\x1b[0m bytes"
	snapshot := clientMutationSnapshot{
		SteeringOrder: []string{id},
		Journal: map[string]clientMutationRecord{
			"cm-note": {
				ClientMutationID: "cm-note", Method: clientMutationMethodNotesHumanSet,
				SteeringKind: events.SteeringKindHumanNote, ExecutionState: "accepted",
			},
			id: {ClientMutationID: id, Method: clientMutationMethodSteer, ExecutionState: "accepted"},
		},
		PendingExecutions: clientMutationPendingExecutions{
			id: appwire.PendingMutation{
				ExecutionState: "accepted",
				Input:          []appwire.InputItem{{Type: "text", Text: text}},
			},
		},
	}

	entries := clientSteeringFromSnapshot(snapshot)
	if len(entries) != 1 {
		t.Fatalf("rebuilt %d steering entries, want 1", len(entries))
	}
	if entries[0].Text != text {
		t.Fatalf("steer beside a modern note record = %q, want it verbatim (%q)", entries[0].Text, text)
	}
	if entries[0].Kind != "" {
		t.Fatalf("steer beside a modern note record kind = %q, want the recorded empty kind", entries[0].Kind)
	}
}

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

// Before the atomic note acceptance, the note delivery minted its steer under the
// outer note mutation's id plus "/note-steer", and the kind stamp only arrived
// later, so such an inner record can keep the steer method and no kind at all.
// The outer record is the durable proof that the text came from the whiteboard:
// the rebuilt queue entry normalizes it and carries the note kind.
func TestRebuiltLegacyNoteSteerCorrelatesWithItsOuterRecord(t *testing.T) {
	const innerID = "cm-legacy-note/note-steer"
	snapshot := clientMutationSnapshot{
		SteeringOrder: []string{innerID},
		Journal: map[string]clientMutationRecord{
			"cm-legacy-note": {ClientMutationID: "cm-legacy-note", Method: clientMutationMethodNotesHumanSet, ExecutionState: "accepted"},
			innerID:          {ClientMutationID: innerID, Method: clientMutationMethodSteer, ExecutionState: "accepted"},
		},
		PendingExecutions: clientMutationPendingExecutions{
			innerID: appwire.PendingMutation{
				ExecutionState: "accepted",
				Input:          []appwire.InputItem{{Type: "text", Text: "human updated their whiteboard: \x1b[31mlegacy\x1b[0m note"}},
			},
		},
	}

	entries := clientSteeringFromSnapshot(snapshot)
	if len(entries) != 1 {
		t.Fatalf("rebuilt %d steering entries, want 1", len(entries))
	}
	if strings.ContainsRune(entries[0].Text, 0x1b) {
		t.Fatalf("legacy note steer was not normalized: %q", entries[0].Text)
	}
	if !strings.Contains(entries[0].Text, "legacy") {
		t.Fatalf("legacy note steer lost its content: %q", entries[0].Text)
	}
	if entries[0].Kind != events.SteeringKindHumanNote {
		t.Fatalf("legacy note steer kind = %q, want %q", entries[0].Kind, events.SteeringKindHumanNote)
	}
}

// The suffix alone is not proof: a steer whose id merely ends that way, with no
// outer note record behind it, keeps the steer method's decision and every byte
// the user typed.
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

// The same correlation decides the model copy a restart hands to the request:
// the inner steer's id and the outer note record are both on disk, so the copy
// normalizes the note text instead of trusting the steer method alone.
func TestRestoredHistoryCopyCorrelatesLegacyNoteSteerWithItsOuterRecord(t *testing.T) {
	t.Parallel()
	const sessionID = "01KLEGACYNOTESTEER0000000"
	const innerID = "cm-legacy-note/note-steer"
	const text = "human updated their whiteboard: \x1b[31mlegacy\x1b[0m note"
	stateDir := t.TempDir()

	store, err := newClientMutationStore(stateDir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.mutate(func(snapshot *clientMutationSnapshot) error {
		outer := testClientMutationRequest(t, clientMutationMethodNotesHumanSet, "cm-legacy-note", struct{ Note string }{Note: "legacy note"})
		inner := testClientMutationRequest(t, clientMutationMethodSteer, innerID, appwire.TurnSteerParams{
			ClientMutationID: innerID,
			Input:            []appwire.InputItem{{Type: "text", Text: text}},
		})
		snapshot.Journal["cm-legacy-note"] = clientMutationRecord{
			ClientMutationID:  "cm-legacy-note",
			Method:            outer.Method,
			Payload:           outer.Payload,
			PayloadHash:       outer.PayloadHash,
			OperationState:    clientMutationOperationApplied,
			ExecutionState:    "accepted",
			ProjectionState:   appwire.MutationProjectionPending,
			AttemptGeneration: 1,
		}
		snapshot.Journal[innerID] = clientMutationRecord{
			ClientMutationID:  innerID,
			Method:            inner.Method,
			Payload:           inner.Payload,
			PayloadHash:       inner.PayloadHash,
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
				ClientMutationID: innerID,
				Message:          llm.User(text),
			}},
		},
	)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer restored.Close()

	got := modelBoundText(restored)
	if strings.ContainsRune(got, 0x1b) {
		t.Fatalf("restored model context = %q, want the legacy note controls stripped", got)
	}
	if !strings.Contains(got, "legacy") {
		t.Fatalf("restored model context lost the note text: %q", got)
	}
}

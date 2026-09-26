package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// A kindless ordinary steering record rebuilt from the durable journal must keep
// every byte the user typed, even when the text imitates the exact shape a
// shared-notes update writes. The journal record persists the mutation method,
// and turn/steer is not notes/human/set, so provenance decides and the
// note-origin guess never strips a normal steer.
func TestRestoredKindlessOrdinarySteerWithNotePrefixKeepsBytes(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	// The kind follows the same evidence: the entry (and the steering/injected
	// event derived from it) carries the note label, not an empty kind.
	if entries[0].Kind != events.SteeringKindHumanNote {
		t.Fatalf("kindless note-origin steer kind = %q, want %q", entries[0].Kind, events.SteeringKindHumanNote)
	}
}

// A record older than both SteeringKind and a usable method has no provenance at
// all, so the write-path text shape remains the only marker: a prefix-shaped
// entry still normalizes, while text that does not imitate the shape keeps its
// bytes. This pins the fallback that must survive for truly old records.
func TestRebuiltSteerWithoutProvenanceKeepsPrefixFallback(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// An inherited prefix is decided by its own turns: the child's journal is not in
// reach when a forked session escapes its history, so a child mutation that reuses
// a parent's client mutation id cannot classify a parent's steer. Note-shaped text
// is still normalized by the write-path shape, and a steer that does not imitate
// it keeps every byte.
func TestInheritedHistoryIsEscapedWithoutAJournalInReach(t *testing.T) {
	t.Parallel()
	const id = "cm-parent"
	const ordinary = "run the tests \x1b[31mnow\x1b[0m"
	inherited := []transcript.Entry{
		{Turn: schema.Turn{Kind: schema.TurnSteering, ClientMutationID: id, Message: llm.User("human updated their whiteboard: \x1b[31mnote\x1b[0m")}},
		{Turn: schema.Turn{Kind: schema.TurnSteering, ClientMutationID: id, Message: llm.User(ordinary)}},
	}

	out := escapeInheritedHistory(inherited)
	if len(out) != 2 {
		t.Fatalf("inherited history = %d turns, want 2", len(out))
	}
	if got := out[0].Message.Text(); strings.ContainsRune(got, 0x1b) {
		t.Fatalf("note-shaped inherited steer was not normalized: %q", got)
	}
	if got := out[1].Message.Text(); got != ordinary {
		t.Fatalf("ordinary inherited steer = %q, want it verbatim (%q)", got, ordinary)
	}
}

// A restored fork's inherited prefix belongs to the parent's session, so the
// child's journal must not decide it even when an id collides -- while the
// session's own turns still consult that journal. meta.DivergenceTurn names where
// the child's history diverges, so everything before it is inherited.
func TestRestoredForkEscapesItsInheritedPrefixWithoutTheChildJournal(t *testing.T) {
	t.Parallel()
	const sessionID = "01KRESTOFEPREFIXBOUND00000"
	// The inherited turn's id collides with a child record that claims the note
	// write; the child's own turn has its own note-origin record.
	const inheritedID = "cm-collides"
	const ownID = "cm-own"
	const inheritedText = "run the inherited tests\x1b[31mwith red lines\x1b[0m"
	const ownText = "run the child tests\x1b[31mwith red lines\x1b[0m"
	stateDir := t.TempDir()

	store, err := newClientMutationStore(stateDir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.mutate(func(snapshot *clientMutationSnapshot) error {
		colliding := testClientMutationRequest(t, clientMutationMethodNotesHumanSet, inheritedID, struct{ Note string }{Note: "a note this session never wrote"})
		own := testClientMutationRequest(t, clientMutationMethodNotesHumanSet, ownID, struct{ Note string }{Note: "own note"})
		for _, req := range []clientMutationRequest{colliding, own} {
			snapshot.Journal[req.ClientMutationID] = clientMutationRecord{
				ClientMutationID:  req.ClientMutationID,
				Method:            req.Method,
				Payload:           req.Payload,
				PayloadHash:       req.PayloadHash,
				OperationState:    clientMutationOperationTerminal,
				ExecutionState:    "incorporated",
				ProjectionState:   appwire.MutationProjectionReflected,
				AttemptGeneration: 1,
			}
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
		// The child diverged after its first turn: one inherited turn precedes it.
		ParentSessionID: "01KPARENT0000000000000000",
		DivergenceTurn:  2,
	}
	restored, err := RestoreSessionFromMetaWithConfig(
		c,
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		meta,
		RestoreSessionConfig{
			StateDir: stateDir,
			resumeHistory: []schema.Turn{
				{Kind: schema.TurnSteering, ClientMutationID: inheritedID, Message: llm.User(inheritedText)},
				{Kind: schema.TurnSteering, ClientMutationID: ownID, Message: llm.User(ownText)},
			},
		},
	)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer restored.Close()

	got := modelBoundText(restored)
	if !strings.Contains(got, inheritedText) {
		t.Fatalf("inherited prefix was decided by the child journal: %q", got)
	}
	if strings.Contains(got, ownText) {
		t.Fatalf("the session's own note-origin turn kept its controls: %q", got)
	}
}

// A compacted fork keeps its own turns' provenance: DivergenceTurn indexes the
// full transcript, so a history that resumes partway through it has to shift the
// bound -- otherwise the child's own turns are escaped as if they were the
// parent's, and a kindless note-origin turn keeps its controls.
func TestRestoredCompactedForkKeepsItsOwnTurnProvenance(t *testing.T) {
	t.Parallel()
	const sessionID = "01KCOMPACTEDFORKBOUND00000"
	const ownID = "cm-own-after-compaction"
	const ownText = "run the child tests\x1b[31mwith red lines\x1b[0m"
	stateDir := t.TempDir()

	store, err := newClientMutationStore(stateDir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.mutate(func(snapshot *clientMutationSnapshot) error {
		own := testClientMutationRequest(t, clientMutationMethodNotesHumanSet, ownID, struct{ Note string }{Note: "own note"})
		snapshot.Journal[ownID] = clientMutationRecord{
			ClientMutationID:  ownID,
			Method:            own.Method,
			Payload:           own.Payload,
			PayloadHash:       own.PayloadHash,
			OperationState:    clientMutationOperationTerminal,
			ExecutionState:    "incorporated",
			ProjectionState:   appwire.MutationProjectionReflected,
			AttemptGeneration: 1,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// The child's transcript: two inherited turns, a compaction checkpoint, then
	// the child's own steering turn.
	path := filepath.Join(stateDir, sessionsSubdir, sessionID+".transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	tw, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("new transcript writer: %v", err)
	}
	for _, turn := range []schema.Turn{
		{Kind: schema.TurnSteering, ClientMutationID: "cm-parent-one", Message: llm.User("parent one")},
		{Kind: schema.TurnSteering, ClientMutationID: "cm-parent-two", Message: llm.User("parent two")},
		{Kind: schema.TurnCheckpoint, Message: llm.User("checkpoint the child wrote after inheriting")},
		{Kind: schema.TurnSteering, ClientMutationID: ownID, Message: llm.User(ownText)},
	} {
		if err := tw.AppendDurable(turn); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	meta := schema.SessionMeta{
		ID:        sessionID,
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    (SessionConfig{NoProjectPrompts: true}).toSnapshot(),
		// Two inherited turns precede the child's own history.
		ParentSessionID: "01KPARENT0000000000000000",
		DivergenceTurn:  3,
	}
	restored, err := RestoreSessionFromMetaWithConfig(
		c,
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		meta,
		RestoreSessionConfig{StateDir: stateDir},
	)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer restored.Close()

	got := modelBoundText(restored)
	if strings.Contains(got, ownText) {
		t.Fatalf("the child's own note-origin turn kept its controls through a compacted resume: %q", got)
	}
}

// A fork boundary measured against the raw transcript must survive repair: an
// orphaned tool call inside the inherited prefix splices a synthetic result into
// the resumed history, and a boundary that ignores the insertion slides one turn
// early -- handing the prefix's steering turn to the child's journal, whose
// colliding record says note and strips bytes the parent wrote.
func TestRestoredForkBoundaryCountsRepairInsertions(t *testing.T) {
	t.Parallel()
	const sessionID = "01KFORKREPAIRBOUND00000000"
	const collideID = "cm-collide-notes"
	const inheritedText = "keep \x1b[31mthese\x1b[0m parent bytes"
	stateDir := t.TempDir()

	store, err := newClientMutationStore(stateDir, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.mutate(func(snapshot *clientMutationSnapshot) error {
		collide := testClientMutationRequest(t, clientMutationMethodNotesHumanSet, collideID, struct{ Note string }{Note: "child note"})
		snapshot.Journal[collideID] = clientMutationRecord{
			ClientMutationID:  collideID,
			Method:            collide.Method,
			Payload:           collide.Payload,
			PayloadHash:       collide.PayloadHash,
			OperationState:    clientMutationOperationTerminal,
			ExecutionState:    "incorporated",
			ProjectionState:   appwire.MutationProjectionReflected,
			AttemptGeneration: 1,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// The child's transcript. The first three turns are inherited: an assistant
	// turn whose tool call was never answered (repair splices a synthetic result
	// for it before the parent's user turn), the parent's user turn, and a
	// kindless parent steering turn whose id collides with a child note record.
	// The child's own turn follows.
	path := filepath.Join(stateDir, sessionsSubdir, sessionID+".transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	tw, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("new transcript writer: %v", err)
	}
	for _, turn := range []schema.Turn{
		{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call-orphan", Name: "read_file", Arguments: []byte("{}")}},
		}}},
		{Kind: schema.TurnUserInput, Message: llm.User("parent user turn")},
		{Kind: schema.TurnSteering, ClientMutationID: collideID, Message: llm.User(inheritedText)},
		{Kind: schema.TurnUserInput, Message: llm.User("child own turn")},
	} {
		if err := tw.AppendDurable(turn); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}

	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	meta := schema.SessionMeta{
		ID:        sessionID,
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    (SessionConfig{NoProjectPrompts: true}).toSnapshot(),
		// Three inherited turns precede the child's own history.
		ParentSessionID: "01KPARENT0000000000000000",
		DivergenceTurn:  4,
	}
	restored, err := RestoreSessionFromMetaWithConfig(
		c,
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		meta,
		RestoreSessionConfig{StateDir: stateDir},
	)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	defer restored.Close()

	got := modelBoundText(restored)
	if !strings.Contains(got, inheritedText) {
		t.Fatalf("the inherited steer lost bytes to the child's colliding record: %q", got)
	}
}

// A note record beside a steer is not evidence about that steer: the note write's
// own record carries the note kind (or, when it changed nothing, a kindless
// notes/human/set), and no rule reads it to classify another mutation's record, so
// a steer whose id merely imitates an old derivation keeps its bytes and its empty
// kind.
func TestRebuiltSteerImitatingTheGrammarBesideAModernNoteRecordKeepsBytes(t *testing.T) {
	t.Parallel()
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

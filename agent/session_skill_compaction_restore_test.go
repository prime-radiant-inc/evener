package agent

import (
	"context"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// restoreSkillCompactionSession resumes the session persisted under stateDir
// through the real restore path (LoadSessionMeta + RestoreSessionFromMeta),
// the same plumbing a restart uses.
func restoreSkillCompactionSession(t *testing.T, stateDir, id string) *Session {
	t.Helper()
	meta, err := schema.LoadSessionMeta(stateDir, id)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	restored, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	t.Cleanup(func() { restored.Close() })
	return restored
}

// TestSkillCompactionRestore_ForcedOperationReArmedBeforeDispatch: an
// unpublished forced operation persisted before the round tail ever dispatched
// must resume with its transient round trigger re-armed from the operation's
// own instructions, so the next round tail dispatches the fold. Restart is not
// cancellation: the operation itself stays pending.
func TestSkillCompactionRestore_ForcedOperationReArmedBeforeDispatch(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}), withoutGitSnapshot())
	id := s.Meta().ID
	if _, err := s.requestSkillCompaction(context.Background(), "opaque-note", "opaque-instructions",
		schema.SkillReloadSelection{State: "valid", Names: []string{}}); err != nil {
		t.Fatal(err)
	}
	// Crash before the round tail ran: no fold, no publication, no consumption.
	s.Close()

	restored := restoreSkillCompactionSession(t, stateDir, id)
	instructions, armed := restored.takeForceRequest()
	if !armed || instructions != "opaque-instructions" {
		t.Fatalf("restored forced operation must re-arm the round trigger from its instructions, got %q, %v", instructions, armed)
	}
	op := restored.pendingSkillCompactionSnapshot()
	if op == nil || op.Origin != "forced" || op.Phase != "pending" || op.Instructions != "opaque-instructions" {
		t.Fatalf("restored operation = %+v, want the pending forced operation", op)
	}
}

// TestSkillCompactionRestore_AutomaticLatchRestoredWithoutForceFold: a
// persisted pending automatic operation restores its elicitation latch through
// the restored operation itself — no transient trigger is armed, no fold is
// forced, and pressure alone must not re-elicit over it.
func TestSkillCompactionRestore_AutomaticLatchRestoredWithoutForceFold(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}), withoutGitSnapshot())
	id := s.Meta().ID
	_, capturedNoteGen := s.pinnedNoteSnapshot()
	// Selection-only acceptance keeps the note empty, so only the restored
	// operation can latch a later elicitation.
	accepted, err := s.acceptAutomaticSkillCompaction(context.Background(), capturedNoteGen, "",
		schema.SkillReloadSelection{State: "valid", Names: []string{}})
	if err != nil || !accepted {
		t.Fatalf("automatic acceptance: accepted=%v err=%v", accepted, err)
	}
	s.Close()

	restored := restoreSkillCompactionSession(t, stateDir, id)
	if _, armed := restored.takeForceRequest(); armed {
		t.Fatal("an automatic operation must restore without forcing a fold")
	}
	op := restored.pendingSkillCompactionSnapshot()
	if op == nil || op.Origin != "automatic" || op.Phase != "pending" {
		t.Fatalf("restored automatic operation = %+v", op)
	}

	called := false
	restored.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) {
		called = true
		return "ELICITED — MUST NOT FIRE", nil
	}
	seedSessionHistory(t, restored, 10)
	forcePressureAbove(t, restored, restored.contextMgr.CheckpointThreshold)
	before := len(currentHistory(t, restored))
	restored.maybeElicitNoteBeforeCompaction(context.Background(), currentHistory(t, restored), 0)
	if called {
		t.Fatal("the restored automatic operation must latch elicitation")
	}
	restored.applyPendingForceCompact(context.Background())
	if len(currentHistory(t, restored)) != before {
		t.Fatal("restoring an automatic operation must not force a fold")
	}
}

// TestSkillCompactionRestore_PublishedOperationCompletesAtRestore: a slot
// persisted in the published phase can only be the live claim→delivery-flip
// window artifact (R19) — the live transaction clears the slot before its
// own save; only a concurrent save inside that window can persist it. So
// restore COMPLETES the delivery instead of resuming it: the slot and its
// selection clear, the publication's handoff advances to delivered, the
// fold is never re-armed, the elicitation latch is gone, and the cycle
// reopens for a fresh generation.
func TestSkillCompactionRestore_PublishedOperationCompletesAtRestore(t *testing.T) {
	t.Parallel()
	meta := schema.SessionMeta{
		ID:        "resume-published-compaction",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Skills: &schema.SkillLifecycleSnapshot{
			Inventory:        map[string]schema.SkillInventoryEntry{},
			NextOperationGen: 7,
			PinnedNoteGen:    3,
			PendingCompaction: &schema.SkillCompactionOperation{
				Generation:     7,
				Origin:         "forced",
				Instructions:   "opaque-instructions",
				NoteGeneration: 3,
				Selection:      schema.SkillReloadSelection{State: "valid", Names: []string{}},
				Phase:          "published",
				PublicationID:  "pub-opaque-1",
			},
			PendingHandoffs: []schema.SkillCompactionReceipt{{
				Revision:  9,
				SessionID: "resume-published-compaction",
				Operation: schema.SkillCompactionOperation{Generation: 7, Origin: "forced", Phase: "published", PublicationID: "pub-opaque-1"},
				Phase:     skillCompactionReceiptPublished,
			}},
		},
	}
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	restored, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, t.TempDir())
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	t.Cleanup(func() { restored.Close() })

	if _, armed := restored.takeForceRequest(); armed {
		t.Fatal("a completed delivery is never re-armed: restart must not arm a fold")
	}
	if op := restored.pendingSkillCompactionSnapshot(); op != nil {
		t.Fatalf("restore must complete the window artifact's delivery, still holding %+v", op)
	}
	if sel := cycleSkillReloadSelection(restored); sel.State != "absent" {
		t.Fatalf("the completed delivery must have consumed the selection, got %+v", sel)
	}
	handoffs := pendingHandoffsSnapshot(restored)
	if len(handoffs) != 1 || handoffs[0].Operation.Generation != 7 ||
		handoffs[0].Operation.PublicationID != "pub-opaque-1" {
		t.Fatalf("restored handoffs = %+v, want the window artifact's handoff preserved", handoffs)
	}
	if handoffs[0].Phase != skillCompactionReceiptDelivered {
		t.Fatalf("restore must advance the window artifact's handoff to delivered, got %+v", handoffs[0])
	}
	restored.mu.Lock()
	noteGen := restored.pinnedNoteGen
	restored.mu.Unlock()
	accepted, err := restored.acceptAutomaticSkillCompaction(context.Background(), noteGen, "post-restore note",
		schema.SkillReloadSelection{State: "absent"})
	if err != nil {
		t.Fatalf("acceptAutomaticSkillCompaction: %v", err)
	}
	if !accepted {
		t.Fatal("the completed delivery must unlatch note elicitation")
	}
	if _, err := restored.requestSkillCompaction(context.Background(), "", "",
		schema.SkillReloadSelection{State: "absent"}); err != nil {
		t.Fatalf("clearing the post-restore note: %v", err)
	}
	next, err := restored.requestSkillCompaction(context.Background(), "reopened", "",
		schema.SkillReloadSelection{State: "absent"})
	if err != nil {
		t.Fatalf("the cycle must reopen after restore completes the delivery: %v", err)
	}
	if next <= 7 {
		t.Fatalf("the reopened cycle must mint a generation beyond 7, got %d", next)
	}
}

// TestSkillCompactionRestore_MissingLegacyMetadataResumesWithoutBackfill: a
// session persisted before lifecycle metadata existed resumes with fresh
// compaction state — no operation is reconstructed from history, no trigger is
// armed, and elicitation behaves as in a brand-new session.
func TestSkillCompactionRestore_MissingLegacyMetadataResumesWithoutBackfill(t *testing.T) {
	t.Parallel()
	meta := schema.SessionMeta{
		ID:        "resume-legacy-compaction",
		ProfileID: "openai",
		Model:     "gpt-5.2",
		// Skills nil: legacy sessions predate lifecycle metadata.
	}
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	restored, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, t.TempDir())
	if err != nil {
		t.Fatalf("legacy session must resume without lifecycle metadata: %v", err)
	}
	t.Cleanup(func() { restored.Close() })

	if restored.pendingSkillCompactionSnapshot() != nil {
		t.Fatal("no compaction operation may be backfilled for a legacy session")
	}
	if _, armed := restored.takeForceRequest(); armed {
		t.Fatal("a legacy session must not arm a compaction")
	}

	called := false
	restored.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) {
		called = true
		return "STUB legacy note", nil
	}
	seedSessionHistory(t, restored, 10)
	forcePressureAbove(t, restored, restored.contextMgr.CheckpointThreshold)
	restored.maybeElicitNoteBeforeCompaction(context.Background(), currentHistory(t, restored), 0)
	if !called {
		t.Fatal("a legacy session must elicit as a fresh session does — no phantom latch")
	}
	if got := restored.PinnedNote(); got != "STUB legacy note" {
		t.Fatalf("legacy session note = %q", got)
	}
}

// TestSkillCompactionRestore_ReloadSelectionRoundTrip pins that a compaction
// cycle's reload selection survives save and the real restart path. The
// selection lives on the operation that owns it — the single authority, with no
// second persisted copy to drift — so this asserts the operation the restart
// rebuilt.
func TestSkillCompactionRestore_ReloadSelectionRoundTrip(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	s := newSession(t, withConfig(SessionConfig{StateDir: stateDir}), withoutGitSnapshot())
	id := s.Meta().ID
	s.skillLifecycle.Inventory["scope:probe"] = schema.SkillInventoryEntry{Ordinary: &schema.OrdinarySkillActivation{Description: "opaque-probe"}}
	if _, err := s.requestSkillCompaction(context.Background(), "opaque-note", "",
		schema.SkillReloadSelection{State: "valid", Names: []string{"scope:probe"}}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	restored := restoreSkillCompactionSession(t, stateDir, id)
	sel := cycleSkillReloadSelection(restored)
	if sel.State != "valid" || !reflect.DeepEqual(sel.Names, []string{"scope:probe"}) {
		t.Fatalf("pending selection = %+v, want valid [scope:probe] across restart", sel)
	}
}

// TestSkillCompactionRestore_PublishedSlotWithoutReceiptStillDelivers pins the
// crash window between a winning fold's claim and its transcript receipt write:
// the claim mutates the in-memory slot and handoff under one s.mu critical
// section, a routine metadata save inside that window persists BOTH, and a
// crash before commitTranscriptsLocked means no receipt ever reached the
// transcript.
//
// The claim is atomic with its handoff, so this state is not a selection with
// no surviving record: the coalesced handoff still carries the operation and
// its selected names, and the delivery path processes a handoff by selection
// state, not by phase. This pins that the selection survives and is actually
// delivered (its complete body admitted), and that the cycle reopens rather
// than wedging — a naive "retain the published slot" rule would leave the slot
// occupied, and requestSkillCompaction refuses every new intent while a
// published operation owns it.
func TestSkillCompactionRestore_PublishedSlotWithoutReceiptStillDelivers(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	stateDir := t.TempDir()
	writeSkillMD(t, root, "opaque", "---\nname: opaque\ndescription: fixture\n---\nBODY_window_retry")
	s := newSession(t, withDir(root), withConfig(SessionConfig{StateDir: stateDir}), withoutGitSnapshot())
	seedNumberedSessionHistory(t, s, 12)
	plantOrdinaryRecord(t, s, root, "opaque", true)

	// The window artifact exactly as the live claim plus a routine metadata save
	// would persist it: the claimed slot and its coalesced handoff, and no
	// receipt anywhere in the transcript.
	selection := schema.SkillReloadSelection{State: "valid", Names: []string{"opaque"}}
	s.mu.Lock()
	revision := s.skillLifecycle.Revision
	s.skillLifecycle.PendingCompaction = &schema.SkillCompactionOperation{
		Generation:    1,
		Origin:        skillCompactionOriginForced,
		Phase:         skillCompactionPhasePublished,
		PublicationID: "pub-window-no-receipt",
		Selection:     selection,
	}
	s.skillLifecycle.PendingHandoffs = []schema.SkillCompactionReceipt{{
		Revision:  revision + 1,
		SessionID: s.id,
		Phase:     skillCompactionReceiptPublished,
		Operation: schema.SkillCompactionOperation{
			Generation:    1,
			Origin:        skillCompactionOriginForced,
			Phase:         skillCompactionPhasePublished,
			PublicationID: "pub-window-no-receipt",
			Selection:     selection,
		},
	}}
	s.mu.Unlock()
	if err := s.saveMeta(); err != nil {
		t.Fatalf("save the window artifact: %v", err)
	}
	id := s.Meta().ID
	s.Close()

	restored := restoreSkillCompactionSession(t, stateDir, id)
	op := restored.pendingSkillCompactionSnapshot()
	handoffs := pendingHandoffsSnapshot(restored)
	defer restored.Close()
	if len(handoffs) != 1 || !reflect.DeepEqual(handoffs[0].Operation.Selection.Names, []string{"opaque"}) {
		t.Fatalf("the window artifact's handoff must survive restore with its selection, got slot=%+v handoffs=%+v", op, handoffs)
	}
	batch, outcomes, staged, err := restored.prepareCompactedSkillReloads(context.Background())
	if err != nil {
		t.Fatalf("prepareCompactedSkillReloads: %v", err)
	}
	deliverable := batch != nil && len(batch.Items) == 1 && skillContentIdentity(batch.Items[0]).Name == "opaque"
	if !deliverable {
		t.Fatalf("restore silently discarded the window artifact's selected reload: slot=%+v handoffs=%+v batch=%+v outcomes=%+v", op, handoffs, batch, outcomes)
	}
	// The reload is not merely preparable: admitting it publishes the complete
	// body's carrier, so the model actually receives the selection.
	if err := restored.admitCompactedSkillReloads(context.Background(), restored.profile, &llm.TokenBudget{}, staged, batch, outcomes); err != nil {
		t.Fatalf("admitCompactedSkillReloads: %v", err)
	}
	carriers := 0
	for _, state := range skillTurnStates(restored) {
		for _, obligation := range state.Obligations {
			if obligation.Identity.Name == "opaque" {
				carriers++
			}
		}
	}
	if carriers != 1 {
		t.Fatalf("admitted %d carriers for the window artifact's reload, want exactly one", carriers)
	}
	// The cycle must reopen: the slot is free for a fresh intent, so recovery
	// never wedges compaction the way retaining a published slot would.
	if _, err := restored.requestSkillCompaction(context.Background(), "after-window", "",
		schema.SkillReloadSelection{State: "absent"}); err != nil {
		t.Fatalf("the cycle must reopen after the window recovery: %v", err)
	}
}

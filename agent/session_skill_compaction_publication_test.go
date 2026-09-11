package agent

// Task 8: claim operations only in winning actual fold publication. These
// tests drive the REAL fold machinery end to end — the real summarizer fixture
// (a scripted provider at the LLM boundary only), real publication
// transactions, real metadata persistence, and real restarts — and assert the
// typed compaction-operation lifecycle around publication: a fold claims
// exactly the generation its requesting caller captured, checkpoint-only
// actual compaction counts, an unchanged publication claims nothing, a losing
// attempt claims nothing, retry exhaustion cancels only the forced owner's
// generation, and an unrelated fold never adopts a pending forced intent.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// publicationWarningsContaining counts captured warnings whose message contains
// substr. The substrings asserted in this file are machine tokens (reason
// codes), never natural-language prose.
func publicationWarningsContaining(evs *[]events.SessionEvent, mu *sync.Mutex, substr string) int {
	mu.Lock()
	defer mu.Unlock()
	n := 0
	for _, ev := range *evs {
		if d, ok := ev.Data.(events.WarningData); ok && strings.Contains(d.Message, substr) {
			n++
		}
	}
	return n
}

// disableSessionNaming uses the established nameSessionFromTextFunc test seam
// to silence the session-namer's cheap-model calls, so provider counts in
// these tests measure the summarizer (the fold's own LLM boundary) only.
func disableSessionNaming(s *Session) {
	s.nameSessionFromTextFunc = func(context.Context, string, string) error { return nil }
}

// historyContainsSubstring reports whether any turn's text contains substr.
// The summarize layer wraps the model's reply in its own [CONTEXT SUMMARY]
// envelope, so fold results are matched by content substring, never exact
// full-text equality.
func historyContainsSubstring(history []schema.Turn, substr string) bool {
	for _, turn := range history {
		if strings.Contains(turn.Message.Text(), substr) {
			return true
		}
	}
	return false
}

// loadSkillsSnapshot loads the persisted lifecycle snapshot from the real
// StateDir, the same way a restart reads it.
func loadSkillsSnapshot(t *testing.T, stateDir, id string) *schema.SkillLifecycleSnapshot {
	t.Helper()
	loaded, err := schema.LoadSessionMeta(stateDir, id)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	return loaded.Skills
}

// pendingHandoffsSnapshot returns a detached copy of the session's live
// pending handoff receipts.
func pendingHandoffsSnapshot(s *Session) []schema.SkillCompactionReceipt {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]schema.SkillCompactionReceipt, len(s.skillLifecycle.PendingHandoffs))
	for i, handoff := range s.skillLifecycle.PendingHandoffs {
		handoff.Operation.Selection.Names = slices.Clone(handoff.Operation.Selection.Names)
		out[i] = handoff
	}
	return out
}

// publicationReceiptsFromTranscript reads the durable transcript and returns
// every typed compaction handoff receipt attached to a turn — the record a
// restart reconciles from, including pre-marker entries.
func publicationReceiptsFromTranscript(t *testing.T, stateDir, id string) []schema.SkillCompactionReceipt {
	t.Helper()
	data, err := readTranscriptFull(transcriptPath(stateDir, id))
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	var receipts []schema.SkillCompactionReceipt
	for _, entry := range data.Entries {
		if state := entry.Turn.SkillState; state != nil && state.Compaction != nil {
			receipt := *state.Compaction
			receipt.Operation.Selection.Names = slices.Clone(receipt.Operation.Selection.Names)
			receipts = append(receipts, receipt)
		}
	}
	return receipts
}

// restoreForPublication resumes the session persisted under stateDir through
// the real restore path with the named scripted cheap-model provider, so the
// restored session can drive real folds too.
func restoreForPublication(t *testing.T, stateDir, id, provider string, responder func(llm.Request) llm.Response) *Session {
	t.Helper()
	meta, err := schema.LoadSessionMeta(stateDir, id)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	c.Register(&agenttest.ScriptedAdapter{Provider: provider, Responder: responder})
	restored, err := RestoreSessionFromMeta(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	t.Cleanup(func() { restored.Close() })
	return restored
}

// TestSkillCompaction_CheckpointOnly pins that a checkpoint-only actual
// compaction counts as a real claim: an automatic operation accepted by this
// round's elicitation is claimed by the round's own fold even when only the
// deterministic checkpoint layer runs (no LLM summarization), the claim
// consumes the operation and its reload selection durably, and the cycle
// reopens — a second forced request is accepted with a fresh generation and
// elicitation is no longer latched.
func TestSkillCompaction_CheckpointOnly(t *testing.T) {
	t.Parallel()
	var summarizeCalls atomic.Int32
	stateDir := t.TempDir()
	s := newScriptedSummaryCompactSession(t, "ckpt-only-cheap", func(llm.Request) llm.Response {
		summarizeCalls.Add(1)
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]")}
	}, withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: stateDir}))
	id := s.Meta().ID
	disableSessionNaming(s)
	s.skillLifecycle.Inventory["scope:probe"] = schema.SkillInventoryEntry{Ordinary: &schema.OrdinarySkillActivation{Description: "opaque-probe"}}
	s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) {
		return "keep the token OPAQUE-77\n<skill-reload-selection>{\"reload_skills\":[\"scope:probe\"]}</skill-reload-selection>", nil
	}
	seedNumberedSessionHistory(t, s, 20)
	forcePressureAbove(t, s, 0.85) // > CheckpointThreshold(0.80), < SummarizeThreshold(0.95): Layer 1 only

	var rt events.RoundTimings
	if _, _, _, _, _, _, err := s.prepareModelRequestWithError(context.Background(), 0, &rt); err != nil {
		t.Fatalf("prepareModelRequestWithError: %v", err)
	}

	if n := summarizeCalls.Load(); n != 0 {
		t.Fatalf("checkpoint-only fold must not call the summarizer, got %d call(s)", n)
	}
	// The elicited automatic operation must be claimed and consumed by the
	// winning checkpoint-only publication — not left pending.
	if op := s.pendingSkillCompactionSnapshot(); op != nil {
		t.Fatalf("checkpoint-only actual compaction must claim and consume the elicited operation; still holding %+v", op)
	}
	if skills := loadSkillsSnapshot(t, stateDir, id); skills != nil && skills.PendingCompaction != nil {
		t.Fatalf("claimed operation must not stay in the persisted slot, disk says %+v", skills.PendingCompaction)
	}
	// The reload selection belonged to this cycle and was consumed by the
	// publication that claimed it.
	if sel := s.pendingSkillReloadSelection(); sel.State != "absent" {
		t.Fatalf("a published compaction consumes its cycle's reload selection, still holding %+v", sel)
	}
	// The winning publication recorded exactly one typed handoff receipt —
	// coalesced by publication identity — whose delivery completed with the
	// claimed operation.
	handoffs := pendingHandoffsSnapshot(s)
	if len(handoffs) != 1 {
		t.Fatalf("the winning publication must record exactly one handoff receipt, got %d", len(handoffs))
	}
	handoff := handoffs[0]
	if handoff.Phase != skillCompactionReceiptDelivered || handoff.Operation.Generation == 0 || handoff.Operation.PublicationID == "" || handoff.SessionID != id {
		t.Fatalf("delivered handoff = %+v", handoff)
	}
	receipts := publicationReceiptsFromTranscript(t, stateDir, id)
	if len(receipts) != 1 || receipts[0].Operation.PublicationID != handoff.Operation.PublicationID ||
		receipts[0].Phase != skillCompactionReceiptPublished || receipts[0].Operation.Generation != handoff.Operation.Generation {
		t.Fatalf("transcript receipts = %+v, want the checkpoint marker carrying the claim's published receipt %+v", receipts, handoff)
	}
	if skills := loadSkillsSnapshot(t, stateDir, id); skills == nil || skills.PendingCompaction != nil ||
		len(skills.PendingHandoffs) != 1 || skills.PendingHandoffs[0].Phase != skillCompactionReceiptDelivered {
		t.Fatalf("persisted lifecycle after delivery = %+v", skills)
	}
	// The cycle reopens: a fresh forced request mints a NEW generation.
	second, err := s.requestSkillCompaction(context.Background(), "second-cycle", "",
		schema.SkillReloadSelection{State: "absent"})
	if err != nil {
		t.Fatalf("the cycle must reopen after a delivered claim, second request rejected: %v", err)
	}
	if second == 0 {
		t.Fatal("the reopened cycle's request must mint a fresh generation")
	}
	if _, ok := s.takeForceRequest(); !ok {
		t.Fatal("the reopened cycle's second request must arm its own round trigger")
	}
}

// TestSkillCompaction_DeferredAutomatic pins the deferral semantics of an
// automatic operation: an unchanged publication leaves it pending for a later
// fold, the next ACTUAL compaction claims it, and an intent superseded while
// its fold is parked in flight is never resurrected by that fold's publication.
func TestSkillCompaction_DeferredAutomatic(t *testing.T) {
	t.Run("deferred operation claimed by the next actual fold", func(t *testing.T) {
		t.Parallel()
		stateDir := t.TempDir()
		s := newScriptedSummaryCompactSession(t, "defer-auto-cheap", func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]")}
		}, withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: stateDir}))
		id := s.Meta().ID
		disableSessionNaming(s)
		seedNumberedSessionHistory(t, s, 20)
		_, capturedNoteGen := s.pinnedNoteSnapshot()
		accepted, err := s.acceptAutomaticSkillCompaction(context.Background(), capturedNoteGen, "deferred-note",
			schema.SkillReloadSelection{State: "valid", Names: []string{"scope:probe"}})
		if err != nil || !accepted {
			t.Fatalf("automatic acceptance: accepted=%v err=%v", accepted, err)
		}
		first := s.pendingSkillCompactionSnapshot()
		if first == nil {
			t.Fatal("test setup: automatic operation not accepted")
		}

		// A low-pressure model request publishes the history unchanged: no
		// compaction layer runs, so the deferred intent must survive it.
		var rt events.RoundTimings
		if _, _, _, _, _, _, err := s.prepareModelRequestWithError(context.Background(), 0, &rt); err != nil {
			t.Fatalf("prepareModelRequestWithError (unchanged): %v", err)
		}
		for _, turn := range currentHistory(t, s) {
			if turn.Kind == schema.TurnCheckpoint || turn.Kind == schema.TurnSummary {
				t.Fatal("test setup: the low-pressure round must not compact")
			}
		}
		if op := s.pendingSkillCompactionSnapshot(); op == nil || op.Generation != first.Generation || op.Phase != "pending" {
			t.Fatalf("an unchanged publication must leave the deferred intent pending, got %+v", op)
		}
		if got := s.PinnedNote(); got != "deferred-note" {
			t.Fatalf("an unchanged publication must not consume the pinned note, got %q", got)
		}

		// The next actual (checkpoint-only) compaction claims the deferred
		// operation and consumes it.
		forcePressureAbove(t, s, 0.85)
		if _, _, _, _, _, _, err := s.prepareModelRequestWithError(context.Background(), 0, &rt); err != nil {
			t.Fatalf("prepareModelRequestWithError (actual): %v", err)
		}
		if op := s.pendingSkillCompactionSnapshot(); op != nil {
			t.Fatalf("the deferred operation must be claimed and consumed by the next actual compaction, still holding %+v", op)
		}
		if skills := loadSkillsSnapshot(t, stateDir, id); skills != nil && skills.PendingCompaction != nil {
			t.Fatalf("the claimed deferred operation must not stay in the persisted slot, disk says %+v", skills.PendingCompaction)
		}
		if sel := s.pendingSkillReloadSelection(); sel.State != "absent" {
			t.Fatalf("the claiming publication must consume the deferred cycle's selection, still holding %+v", sel)
		}
		if n := countSteering(currentHistory(t, s), "deferred-note"); n != 1 {
			t.Fatalf("deferred note handed off %d time(s), want exactly 1", n)
		}
		if got := s.PinnedNote(); got != "" {
			t.Fatalf("the claiming fold hands the note forward and consumes it, got %q", got)
		}
		// The cycle reopens for a fresh acceptance with a fresh generation.
		_, capturedNoteGen = s.pinnedNoteSnapshot()
		accepted, err = s.acceptAutomaticSkillCompaction(context.Background(), capturedNoteGen, "next-cycle-note",
			schema.SkillReloadSelection{State: "valid", Names: []string{}})
		if err != nil || !accepted {
			t.Fatalf("the cycle must reopen after the delivered claim: accepted=%v err=%v", accepted, err)
		}
		if op := s.pendingSkillCompactionSnapshot(); op == nil || op.Generation <= first.Generation {
			t.Fatalf("reopened acceptance must mint a fresh generation, got %+v after %d", op, first.Generation)
		}
	})

	t.Run("superseded mid-flight intent is not resurrected", func(t *testing.T) {
		t.Parallel()
		// The brief's exact parked-summarizer fixture: the actual fold is
		// driven by Compact (whose ForceCompact runs the summarize layer
		// unconditionally), parked inside the provider call, and released only
		// after the pending intent was cleared mid-flight.
		entered, release := make(chan struct{}), make(chan struct{})
		var summarizeCalls atomic.Int32
		stateDir := t.TempDir()
		s := newScriptedSummaryCompactSession(t, "anthropic", func(llm.Request) llm.Response {
			if summarizeCalls.Add(1) == 1 {
				close(entered)
				<-release
			}
			return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nSUMMARY_f1e9\n[END SUMMARY]")}
		}, withoutGitSnapshot(), withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: stateDir}))
		id := s.Meta().ID
		disableSessionNaming(s)
		seedNumberedSessionHistory(t, s, 20)
		// Plant the pending automatic operation this fold runs against.
		_, capturedNoteGen := s.pinnedNoteSnapshot()
		accepted, err := s.acceptAutomaticSkillCompaction(context.Background(), capturedNoteGen, "parked-note",
			schema.SkillReloadSelection{State: "valid", Names: []string{"scope:probe"}})
		if err != nil || !accepted {
			t.Fatalf("automatic acceptance: accepted=%v err=%v", accepted, err)
		}
		pending := s.pendingSkillCompactionSnapshot()
		if pending == nil || pending.Origin != "automatic" {
			t.Fatalf("test setup: no pending automatic operation, got %+v", pending)
		}

		compactErr := make(chan error, 1)
		go func() {
			compactErr <- s.Compact(context.Background())
		}()
		<-entered // the actual fold is parked mid-summarize

		// Clearing the note retires the pending automatic operation
		// (superseded_note) while the fold is still in flight.
		clearGen, err := s.requestSkillCompaction(context.Background(), "", "",
			schema.SkillReloadSelection{State: "absent"})
		if err != nil {
			t.Fatalf("clear during parked fold: %v", err)
		}
		if clearGen != 0 {
			t.Fatalf("clear must not mint an operation generation, got %d", clearGen)
		}
		if op := s.pendingSkillCompactionSnapshot(); op != nil {
			t.Fatalf("clear must retire the parked fold's automatic operation, still holding %+v", op)
		}
		if skills := loadSkillsSnapshot(t, stateDir, id); skills == nil || skills.PendingCompaction != nil {
			t.Fatal("the mid-flight supersession must be durably retired before the fold publishes")
		}

		close(release)
		if err := <-compactErr; err != nil {
			t.Fatalf("Compact (parked): %v", err)
		}
		if n := summarizeCalls.Load(); n != 1 {
			t.Fatalf("provider called %d time(s), want exactly 1", n)
		}
		// The parked fold's publication is real (the history folded), but it
		// must NOT resurrect the superseded intent.
		if op := s.pendingSkillCompactionSnapshot(); op != nil {
			t.Fatalf("a publication must not resurrect a mid-flight superseded operation, got %+v", op)
		}
		if skills := loadSkillsSnapshot(t, stateDir, id); skills != nil && skills.PendingCompaction != nil {
			t.Fatalf("the superseded operation must stay retired on disk, got %+v", skills.PendingCompaction)
		}
		if n := countSteering(currentHistory(t, s), "parked-note"); n != 1 {
			t.Fatalf("the captured note's handoff steering must survive the publication, found %d", n)
		}
		if got := s.PinnedNote(); got != "" {
			t.Fatalf("the cleared note must stay cleared after the parked fold published, got %q", got)
		}
		// The supersession and the winner's reminder are both recorded as
		// typed receipts: the cancelled operation's retirement (with its own
		// generation and reason) and the publication's no-operation handoff —
		// neither overwrites the other.
		handoffs := pendingHandoffsSnapshot(s)
		var cancelled, reminder int
		for _, h := range handoffs {
			switch {
			case h.Phase == skillCompactionReceiptCancelled && h.Operation.Generation == pending.Generation && h.Reason == skillCompactionCancelSupersededNote:
				cancelled++
			case h.Phase == skillCompactionReceiptPublished && h.Operation.Generation == 0 && h.Reason == skillCompactionReminderNoOperation:
				reminder++
			}
		}
		if cancelled != 1 || reminder != 1 {
			t.Fatalf("handoffs = %+v, want one superseded_note cancellation for generation %d and one no-operation reminder (cancelled=%d reminder=%d)", handoffs, pending.Generation, cancelled, reminder)
		}
		// The cycle reopens: a fresh acceptance mints a generation beyond the
		// superseded one.
		_, capturedNoteGen = s.pinnedNoteSnapshot()
		reaccepted, err := s.acceptAutomaticSkillCompaction(context.Background(), capturedNoteGen, "after-park",
			schema.SkillReloadSelection{State: "valid", Names: []string{}})
		if err != nil || !reaccepted {
			t.Fatalf("the cycle must reopen after the superseded publication: accepted=%v err=%v", reaccepted, err)
		}
		if op := s.pendingSkillCompactionSnapshot(); op == nil || op.Generation <= pending.Generation {
			t.Fatalf("reopened acceptance must mint a fresh generation beyond %d, got %+v", pending.Generation, op)
		}
	})
}

// TestSkillCompaction_UnchangedPublication pins the no-op rule: a model
// request that publishes the history unchanged (no compaction layer ran — a
// pressure drop or short history) claims nothing, consumes nothing, cancels
// nothing, and writes no new intent state.
func TestSkillCompaction_UnchangedPublication(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	var summarizeCalls atomic.Int32
	s := newScriptedSummaryCompactSession(t, "unchanged-cheap", func(llm.Request) llm.Response {
		summarizeCalls.Add(1)
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]")}
	}, withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: stateDir}))
	id := s.Meta().ID
	disableSessionNaming(s)
	seedNumberedSessionHistory(t, s, 20)
	_, capturedNoteGen := s.pinnedNoteSnapshot()
	accepted, err := s.acceptAutomaticSkillCompaction(context.Background(), capturedNoteGen, "unchanged-note",
		schema.SkillReloadSelection{State: "valid", Names: []string{"scope:probe"}})
	if err != nil || !accepted {
		t.Fatalf("automatic acceptance: accepted=%v err=%v", accepted, err)
	}
	before := s.pendingSkillCompactionSnapshot()
	if before == nil {
		t.Fatal("test setup: no pending operation")
	}

	var rt events.RoundTimings
	if _, _, _, _, _, _, err := s.prepareModelRequestWithError(context.Background(), 0, &rt); err != nil {
		t.Fatalf("prepareModelRequestWithError: %v", err)
	}

	if n := summarizeCalls.Load(); n != 0 {
		t.Fatalf("an unchanged publication must not call the provider, got %d call(s)", n)
	}
	op := s.pendingSkillCompactionSnapshot()
	if op == nil || op.Generation != before.Generation || op.Phase != "pending" {
		t.Fatalf("an unchanged publication must leave the intent untouched, got %+v (was %+v)", op, before)
	}
	if got := s.PinnedNote(); got != "unchanged-note" {
		t.Fatalf("an unchanged publication must not consume the note, got %q", got)
	}
	if sel := s.pendingSkillReloadSelection(); sel.State != "valid" || len(sel.Names) != 1 || sel.Names[0] != "scope:probe" {
		t.Fatalf("an unchanged publication must retain the pending selection, got %+v", sel)
	}
	skills := loadSkillsSnapshot(t, stateDir, id)
	if skills == nil || skills.PendingCompaction == nil || skills.PendingCompaction.Phase != "pending" {
		t.Fatalf("an unchanged publication must not rewrite the persisted intent, disk says %+v", skills)
	}
	if len(skills.PendingHandoffs) != 0 {
		t.Fatalf("an unchanged publication persists no handoff, got %+v", skills.PendingHandoffs)
	}
	if handoffs := pendingHandoffsSnapshot(s); len(handoffs) != 0 {
		t.Fatalf("an unchanged publication records no handoff, got %+v", handoffs)
	}
	if _, ok := s.takeForceRequest(); ok {
		t.Fatal("an unchanged publication must not arm any compaction trigger")
	}
}

// TestSkillCompaction_LosingAttempt pins that a fold attempt losing the
// publication race claims nothing, and the retry's winning publication claims
// the forced operation exactly once: the losing attempt leaves the operation
// pending, the winning retry consumes it, and no cancellation fires.
func TestSkillCompaction_LosingAttempt(t *testing.T) {
	t.Parallel()
	competingResult := func(n int32) []schema.Turn {
		turns := []schema.Turn{schema.NewTurn(schema.TurnSummary, llm.User(fmt.Sprintf("[CONTEXT SUMMARY]\ncompeting %d\n[END SUMMARY]", n)))}
		for i := range 7 {
			turns = append(turns, schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("competing filler %d-%d", n, i))))
		}
		return turns
	}
	var summarizeCalls atomic.Int32
	var s *Session
	stateDir := t.TempDir()
	s = newScriptedSummaryCompactSession(t, "losing-attempt-cheap", func(llm.Request) llm.Response {
		n := summarizeCalls.Add(1)
		if n == 1 {
			// The first attempt's summarize call races a competing publish in
			// the unlocked fold window, so that attempt must lose.
			s.mu.Lock()
			if _, ok := s.publishFoldedHistory(len(s.history), s.historyRevision, competingResult(n)); !ok {
				t.Error("test setup: the simulated competing publish itself unexpectedly conflicted")
			}
			s.mu.Unlock()
			return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nlosing summary\n[END SUMMARY]")}
		}
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nwinning summary\n[END SUMMARY]")}
	}, withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: stateDir}))
	id := s.Meta().ID
	disableSessionNaming(s)
	evs, evMu, done := collectEvents(s)
	seedNumberedSessionHistory(t, s, 20)

	forced, err := s.requestSkillCompaction(context.Background(), "keep", "opaque-instructions",
		schema.SkillReloadSelection{State: "valid", Names: []string{"scope:probe"}})
	if err != nil {
		t.Fatalf("requestSkillCompaction: %v", err)
	}
	s.applyPendingForceCompact(context.Background())

	if n := summarizeCalls.Load(); n != 2 {
		t.Fatalf("provider called %d time(s), want exactly 2 (one per attempt)", n)
	}
	if !historyContainsSubstring(currentHistory(t, s), "winning summary") {
		t.Fatalf("test setup: the retry attempt must win the publication race (calls=%d)", summarizeCalls.Load())
	}
	// The losing attempt claimed nothing; the winning retry claimed and
	// consumed the forced operation exactly once.
	if op := s.pendingSkillCompactionSnapshot(); op != nil {
		t.Fatalf("the winning retry must claim and consume generation %d, still holding %+v", forced, op)
	}
	if skills := loadSkillsSnapshot(t, stateDir, id); skills != nil && skills.PendingCompaction != nil {
		t.Fatalf("the claimed forced operation must not stay in the persisted slot, disk says %+v", skills.PendingCompaction)
	}
	if sel := s.pendingSkillReloadSelection(); sel.State != "absent" {
		t.Fatalf("the winning publication must consume the forced cycle's selection, still holding %+v", sel)
	}
	// The losing attempt recorded nothing; the winning retry recorded
	// exactly one delivered handoff, and every marker receipt attached to
	// its publication's turns (checkpoint and summary alike) names that
	// same publication.
	handoffs := pendingHandoffsSnapshot(s)
	if len(handoffs) != 1 || handoffs[0].Phase != skillCompactionReceiptDelivered ||
		handoffs[0].Operation.Generation != forced || handoffs[0].Operation.PublicationID == "" {
		t.Fatalf("the winning retry records exactly one delivered handoff for generation %d, got %+v", forced, handoffs)
	}
	receipts := publicationReceiptsFromTranscript(t, stateDir, id)
	if len(receipts) == 0 {
		t.Fatalf("transcript receipts = none, want the winning attempt's markers for generation %d", forced)
	}
	for _, r := range receipts {
		if r.Operation.Generation != forced || r.Operation.PublicationID != handoffs[0].Operation.PublicationID {
			t.Fatalf("transcript receipts = %+v, want every marker on the winning publication for generation %d", receipts, forced)
		}
	}
	if _, ok := s.takeForceRequest(); ok {
		t.Fatal("a delivered claim must not leave a round trigger armed")
	}
	second, err := s.requestSkillCompaction(context.Background(), "next", "",
		schema.SkillReloadSelection{State: "absent"})
	if err != nil {
		t.Fatalf("the cycle must reopen after the delivered claim: %v", err)
	}
	if second <= forced {
		t.Fatalf("the reopened cycle must mint a generation beyond %d, got %d", forced, second)
	}

	s.Close()
	<-done
	if n := publicationWarningsContaining(evs, evMu, skillCompactionCancelForcedNotPublished); n != 0 {
		t.Fatalf("a winning retry must not cancel the forced operation, got %d forced_not_published notice(s)", n)
	}
}

// TestSkillCompaction_CompetingForced pins the competing-winner rule: an
// unrelated manual fold publishes its own actual compaction without adopting
// the pending forced operation (whose selection it must never touch), and the
// forced operation is then claimed only by its own requesting dispatch.
func TestSkillCompaction_CompetingForced(t *testing.T) {
	t.Parallel()
	var summarizeCalls atomic.Int32
	stateDir := t.TempDir()
	s := newScriptedSummaryCompactSession(t, "competing-forced-cheap", func(llm.Request) llm.Response {
		n := summarizeCalls.Add(1)
		return llm.Response{Message: llm.Assistant(fmt.Sprintf("[CONTEXT SUMMARY]\nfold %d summary\n[END SUMMARY]", n))}
	}, withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: stateDir}))
	id := s.Meta().ID
	disableSessionNaming(s)
	evs, evMu, done := collectEvents(s)
	seedNumberedSessionHistory(t, s, 20)

	forced, err := s.requestSkillCompaction(context.Background(), "", "opaque-instructions",
		schema.SkillReloadSelection{State: "valid", Names: []string{"scope:probe"}})
	if err != nil {
		t.Fatalf("requestSkillCompaction: %v", err)
	}

	// An unrelated manual fold publishes a real compaction in the window
	// before the forced operation's own dispatch. It must not adopt the
	// forced intent: the operation stays exactly as it was.
	if err := s.Compact(context.Background()); err != nil {
		t.Fatalf("Compact (unrelated winner): %v", err)
	}
	op := s.pendingSkillCompactionSnapshot()
	if op == nil || op.Generation != forced || op.Origin != "forced" || op.Phase != "pending" {
		t.Fatalf("an unrelated manual fold must not adopt or consume the pending forced operation, got %+v", op)
	}
	if sel := s.pendingSkillReloadSelection(); sel.State != "valid" || len(sel.Names) != 1 || sel.Names[0] != "scope:probe" {
		t.Fatalf("an unrelated manual fold must not consume the forced operation's selection, got %+v", sel)
	}
	// The forced operation's round trigger must survive the unrelated winner
	// (read without consuming it — the tail dispatch below needs it).
	s.mu.Lock()
	triggerArmed := s.forceRequested
	s.mu.Unlock()
	if !triggerArmed {
		t.Fatal("the forced operation's own round trigger must survive the unrelated winner")
	}

	// Seed fresh post-winner history past PreserveRecentTurns so the forced
	// operation's own fold is genuine, then dispatch it at the round tail.
	s.mu.Lock()
	for i := range 8 {
		s.history = append(s.history, schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("post-winner turn %d", i))))
	}
	s.mu.Unlock()
	s.applyPendingForceCompact(context.Background())

	if n := summarizeCalls.Load(); n != 2 {
		t.Fatalf("provider called %d time(s), want exactly 2 (one per fold)", n)
	}
	// The forced operation's own requesting dispatch claims and consumes it.
	if op := s.pendingSkillCompactionSnapshot(); op != nil {
		t.Fatalf("the forced operation's own dispatch must claim and consume generation %d, still holding %+v", forced, op)
	}
	if skills := loadSkillsSnapshot(t, stateDir, id); skills != nil && skills.PendingCompaction != nil {
		t.Fatalf("the claimed forced operation must not stay in the persisted slot, disk says %+v", skills.PendingCompaction)
	}
	if sel := s.pendingSkillReloadSelection(); sel.State != "absent" {
		t.Fatalf("the forced operation's claiming publication must consume its selection, still holding %+v", sel)
	}
	// The competing winner's handoff and the forced claim coexist as two
	// distinct typed receipts — the reminder did not overwrite the losing
	// forced operation while it was still pending, and the claim did not
	// overwrite the reminder.
	handoffs := pendingHandoffsSnapshot(s)
	if len(handoffs) != 2 {
		t.Fatalf("the unrelated winner's reminder and the forced claim must coexist as two handoffs, got %+v", handoffs)
	}
	if handoffs[0].Operation.Generation != 0 || handoffs[0].Reason != skillCompactionReminderNoOperation ||
		handoffs[0].Phase != skillCompactionReceiptPublished || handoffs[0].Operation.PublicationID == "" {
		t.Fatalf("the unrelated winner's handoff must be the no-operation reminder, got %+v", handoffs[0])
	}
	if handoffs[1].Operation.Generation != forced || handoffs[1].Phase != skillCompactionReceiptDelivered ||
		handoffs[1].Operation.PublicationID == "" || handoffs[1].Operation.PublicationID == handoffs[0].Operation.PublicationID {
		t.Fatalf("the forced claim's delivered handoff = %+v (after reminder %+v)", handoffs[1], handoffs[0])
	}
	second, err := s.requestSkillCompaction(context.Background(), "next", "",
		schema.SkillReloadSelection{State: "absent"})
	if err != nil {
		t.Fatalf("the cycle must reopen after the forced claim: %v", err)
	}
	if second <= forced {
		t.Fatalf("the reopened cycle must mint a generation beyond %d, got %d", forced, second)
	}

	s.Close()
	<-done
	if n := publicationWarningsContaining(evs, evMu, skillCompactionCancelForcedNotPublished); n != 0 {
		t.Fatalf("a claimed forced operation must not be cancelled, got %d forced_not_published notice(s)", n)
	}
}

// TestSkillCompaction_ConcurrentSteering pins that a steering turn recorded
// while the claiming fold is parked in flight survives the publication
// (merged into live history, durably re-represented after the marker for
// restart) alongside the fold's own typed receipt — the claim machinery
// disturbs neither the merged-back tail nor the append-tail rewriting.
func TestSkillCompaction_ConcurrentSteering(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	var summarizeCalls atomic.Int32
	stateDir := t.TempDir()
	s := newScriptedSummaryCompactSession(t, "concurrent-steer-cheap", func(llm.Request) llm.Response {
		if summarizeCalls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nSUMMARY_f1e9\n[END SUMMARY]")}
	}, withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: stateDir}))
	id := s.Meta().ID
	disableSessionNaming(s)
	seedNumberedSessionHistory(t, s, 20)
	forced, err := s.requestSkillCompaction(context.Background(), "steer-note", "opaque-instructions",
		schema.SkillReloadSelection{State: "valid", Names: []string{"scope:probe"}})
	if err != nil {
		t.Fatalf("requestSkillCompaction: %v", err)
	}

	dispatch := make(chan struct{})
	go func() {
		defer close(dispatch)
		s.applyPendingForceCompact(context.Background())
	}()
	<-entered // the forced fold is parked inside the summarize call

	// A steering turn recorded while the fold is in flight (the provider
	// call holds no locks) must survive the fold's publication.
	const steerText = "steering recorded while the forced fold was parked"
	turn := schema.NewTurn(schema.TurnSteering, llm.User(steerText))
	s.recordTurn(turn, turn)

	close(release)
	<-dispatch

	if n := summarizeCalls.Load(); n != 1 {
		t.Fatalf("provider called %d time(s), want exactly 1", n)
	}
	if op := s.pendingSkillCompactionSnapshot(); op != nil {
		t.Fatalf("the parked fold's publication must claim and consume generation %d, still holding %+v", forced, op)
	}
	if n := countSteering(currentHistory(t, s), steerText); n != 1 {
		t.Fatalf("concurrently recorded steering appears %d time(s) in live history, want exactly 1", n)
	}
	handoffs := pendingHandoffsSnapshot(s)
	if len(handoffs) != 1 || handoffs[0].Phase != skillCompactionReceiptDelivered || handoffs[0].Operation.Generation != forced {
		t.Fatalf("handoffs = %+v, want one delivered receipt for generation %d", handoffs, forced)
	}
	receipts := publicationReceiptsFromTranscript(t, stateDir, id)
	if len(receipts) == 0 {
		t.Fatalf("transcript receipts = none, want the publication markers for generation %d", forced)
	}
	for _, r := range receipts {
		if r.Operation.Generation != forced || r.Operation.PublicationID != handoffs[0].Operation.PublicationID {
			t.Fatalf("transcript receipts = %+v, want every marker on the publication for generation %d", receipts, forced)
		}
	}
	// The merged-back steering turn must be durably represented after the
	// marker, so a restart keeps it.
	data, err := readTranscriptFull(transcriptPath(stateDir, id))
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	if n := countSteering(ResumeHistory(data.Entries), steerText); n != 1 {
		t.Fatalf("concurrently recorded steering appears %d time(s) in the resumed history, want exactly 1", n)
	}
}

// TestSkillCompaction_FinalSummary pins the coalescing rule: a fold that
// publishes BOTH a checkpoint and a summary phase records ONE handoff for
// that winning publication — the final (summary) phase's receipt — attached
// by publication identity, never per-marker list position, and restart
// reconciliation preserves that final handoff.
func TestSkillCompaction_FinalSummary(t *testing.T) {
	t.Parallel()
	var summarizeCalls atomic.Int32
	stateDir := t.TempDir()
	s := newScriptedSummaryCompactSession(t, "final-summary-cheap", func(llm.Request) llm.Response {
		summarizeCalls.Add(1)
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nSUMMARY_f1e9\n[END SUMMARY]")}
	}, withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: stateDir}))
	id := s.Meta().ID
	disableSessionNaming(s)
	seedNumberedSessionHistory(t, s, 20)
	forced, err := s.requestSkillCompaction(context.Background(), "final-note", "opaque-instructions",
		schema.SkillReloadSelection{State: "valid", Names: []string{"scope:probe"}})
	if err != nil {
		t.Fatalf("requestSkillCompaction: %v", err)
	}
	s.applyPendingForceCompact(context.Background())

	if n := summarizeCalls.Load(); n != 1 {
		t.Fatalf("provider called %d time(s), want exactly 1", n)
	}
	if op := s.pendingSkillCompactionSnapshot(); op != nil {
		t.Fatalf("the fold must claim and consume generation %d, still holding %+v", forced, op)
	}
	// Both the checkpoint and the summary phase of this single publication
	// carry the SAME receipt (one publication identity), and the handoff list
	// coalesces them into exactly one final handoff.
	receipts := publicationReceiptsFromTranscript(t, stateDir, id)
	if len(receipts) != 2 {
		t.Fatalf("transcript receipts = %+v, want the checkpoint and summary phases' receipts", receipts)
	}
	if receipts[0].Operation.PublicationID == "" || receipts[0].Operation.PublicationID != receipts[1].Operation.PublicationID ||
		receipts[0].Operation.Generation != forced || receipts[1].Operation.Generation != forced {
		t.Fatalf("both phases must carry the same publication identity for generation %d, got %+v", forced, receipts)
	}
	handoffs := pendingHandoffsSnapshot(s)
	if len(handoffs) != 1 || handoffs[0].Phase != skillCompactionReceiptDelivered || handoffs[0].Operation.Generation != forced ||
		handoffs[0].Operation.PublicationID != receipts[0].Operation.PublicationID {
		t.Fatalf("handoffs = %+v, want the publication's one coalesced delivered handoff for generation %d", handoffs, forced)
	}
	if skills := loadSkillsSnapshot(t, stateDir, id); skills == nil || len(skills.PendingHandoffs) != 1 ||
		skills.PendingHandoffs[0].Phase != skillCompactionReceiptDelivered || skills.PendingHandoffs[0].Operation.Generation != forced {
		t.Fatalf("persisted handoffs = %+v, want the one coalesced delivered handoff for generation %d", skills.PendingHandoffs, forced)
	}

	// Restart preserves the final handoff after the final checkpoint/summary:
	// the (already current) metadata is not duplicated by reconciliation.
	s.Close()
	restored := restoreForPublication(t, stateDir, id, "final-summary-cheap", func(llm.Request) llm.Response {
		t.Error("restored session must not fold")
		return llm.Response{}
	})
	if op := restored.pendingSkillCompactionSnapshot(); op != nil {
		t.Fatalf("delivered generation %d must stay consumed across restart, got %+v", forced, op)
	}
	restoredHandoffs := pendingHandoffsSnapshot(restored)
	if len(restoredHandoffs) != 1 || restoredHandoffs[0].Operation.Generation != forced ||
		restoredHandoffs[0].Operation.PublicationID != receipts[0].Operation.PublicationID {
		t.Fatalf("restored handoffs = %+v, want the final handoff preserved exactly once for generation %d", restoredHandoffs, forced)
	}
}

// TestSkillCompaction_RestartBeforePublish pins the pre-publication restart:
// an operation persisted before any fold published restores still pending and
// re-armed (no receipts anywhere), and the RESTORED session's own dispatch
// then claims and delivers it — the reopened flow proven across a restart.
func TestSkillCompaction_RestartBeforePublish(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	s := newScriptedSummaryCompactSession(t, "restart-before-cheap", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nSUMMARY_f1e9\n[END SUMMARY]")}
	}, withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: stateDir}))
	id := s.Meta().ID
	disableSessionNaming(s)
	seedNumberedSessionHistory(t, s, 20)
	forced, err := s.requestSkillCompaction(context.Background(), "restart-note", "restart-instructions",
		schema.SkillReloadSelection{State: "valid", Names: []string{"scope:probe"}})
	if err != nil {
		t.Fatalf("requestSkillCompaction: %v", err)
	}
	if receipts := publicationReceiptsFromTranscript(t, stateDir, id); len(receipts) != 0 {
		t.Fatalf("before any publication there must be no receipts, got %+v", receipts)
	}
	// Crash before the round tail ever dispatched the fold.
	s.Close()

	restored := restoreForPublication(t, stateDir, id, "restart-before-cheap", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nSUMMARY_f1e9\n[END SUMMARY]")}
	})
	disableSessionNaming(restored)
	op := restored.pendingSkillCompactionSnapshot()
	if op == nil || op.Generation != forced || op.Phase != "pending" {
		t.Fatalf("restart before publish must restore the operation pending, got %+v", op)
	}
	if handoffs := pendingHandoffsSnapshot(restored); len(handoffs) != 0 {
		t.Fatalf("restart before publish must carry no handoffs, got %+v", handoffs)
	}
	restored.mu.Lock()
	armed := restored.forceRequested
	restored.mu.Unlock()
	if !armed {
		t.Fatal("restart before publish must re-arm the forced operation's round trigger")
	}

	// The restored session's own dispatch publishes the fold and claims the
	// restored generation — the reopened flow, proven across the restart.
	restored.applyPendingForceCompact(context.Background())
	if op := restored.pendingSkillCompactionSnapshot(); op != nil {
		t.Fatalf("the restored dispatch must claim and consume generation %d, still holding %+v", forced, op)
	}
	handoffs := pendingHandoffsSnapshot(restored)
	if len(handoffs) != 1 || handoffs[0].Phase != skillCompactionReceiptDelivered || handoffs[0].Operation.Generation != forced {
		t.Fatalf("restored handoffs = %+v, want one delivered receipt for generation %d", handoffs, forced)
	}
	if skills := loadSkillsSnapshot(t, stateDir, id); skills == nil || skills.PendingCompaction != nil ||
		len(skills.PendingHandoffs) != 1 || skills.PendingHandoffs[0].Phase != skillCompactionReceiptDelivered {
		t.Fatalf("persisted lifecycle after the restored claim = %+v", skills)
	}
	second, err := restored.requestSkillCompaction(context.Background(), "after-restart", "",
		schema.SkillReloadSelection{State: "absent"})
	if err != nil {
		t.Fatalf("the cycle must reopen across restart after the delivered claim: %v", err)
	}
	if second <= forced {
		t.Fatalf("the reopened cycle must mint a generation beyond %d, got %d", forced, second)
	}
}

// TestSkillCompaction_RestartAfterPublish pins the post-publication restart:
// when the metadata save never landed (a crash between the winning publication
// and its save), the durable transcript receipts reconcile the stale snapshot
// BEFORE ResumeHistory — the claimed operation resumes delivery-only with its
// publication identity, never re-armed, and the final handoff is preserved.
func TestSkillCompaction_RestartAfterPublish(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	s := newScriptedSummaryCompactSession(t, "restart-after-cheap", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nSUMMARY_f1e9\n[END SUMMARY]")}
	}, withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: stateDir}))
	id := s.Meta().ID
	disableSessionNaming(s)
	seedNumberedSessionHistory(t, s, 20)
	forced, err := s.requestSkillCompaction(context.Background(), "crash-note", "crash-instructions",
		schema.SkillReloadSelection{State: "valid", Names: []string{"scope:probe"}})
	if err != nil {
		t.Fatalf("requestSkillCompaction: %v", err)
	}
	// Snapshot the pre-publication metadata bytes — the stale state a crash
	// between the winning publication and its metadata save would leave.
	metaPath := filepath.Join(stateDir, sessionsSubdir, id+".meta.json")
	stale, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read stale metadata: %v", err)
	}

	s.applyPendingForceCompact(context.Background()) // the winning publication claims and delivers
	s.Close()
	if err := os.WriteFile(metaPath, stale, 0o600); err != nil {
		t.Fatalf("rewind metadata to the stale snapshot: %v", err)
	}

	restored := restoreForPublication(t, stateDir, id, "restart-after-cheap", func(llm.Request) llm.Response {
		t.Error("a recovered publication must not fold again")
		return llm.Response{}
	})
	// The transcript receipts reconciled the stale snapshot and COMPLETED the
	// interrupted delivery (R18): the live transaction defines
	// delivery-complete as the slot cleared, the selection consumed, and the
	// publication's coalesced handoff advanced to delivered — a crash before
	// the delivery save must not change the post-recovery state.
	op := restored.pendingSkillCompactionSnapshot()
	if op != nil {
		t.Fatalf("reconciliation must complete generation %d's delivery, still holding %+v", forced, op)
	}
	restored.mu.Lock()
	armed := restored.forceRequested
	noteGen := restored.pinnedNoteGen
	restored.mu.Unlock()
	if armed {
		t.Fatal("a completed delivery is never re-armed: restart must not arm a fold")
	}
	// The claiming publication consumed the cycle's selection; the stale
	// snapshot's copy must not survive to attach to another fold.
	if sel := restored.pendingSkillReloadSelection(); sel.State != "absent" {
		t.Fatalf("the completed delivery must have consumed the stale selection, got %+v", sel)
	}
	// The final handoff is preserved from the transcript receipts — delivered.
	handoffs := pendingHandoffsSnapshot(restored)
	if len(handoffs) != 1 || handoffs[0].Operation.Generation != forced ||
		handoffs[0].Operation.PublicationID == "" || handoffs[0].SessionID != id {
		t.Fatalf("restored handoffs = %+v, want the completed handoff for generation %d", handoffs, forced)
	}
	if handoffs[0].Phase != skillCompactionReceiptDelivered {
		t.Fatalf("reconciliation must advance the interrupted handoff to delivered, got %+v", handoffs[0])
	}
	// The resumed history anchors on the publication's summary marker.
	if !historyContainsSubstring(currentHistory(t, restored), "SUMMARY_f1e9") {
		t.Fatal("the restored history must anchor on the publication's summary marker")
	}
	// The cycle reopened: the note-elicitation latch is gone — a fresh
	// automatic acceptance is taken — and a fresh forced request mints a
	// generation beyond the recovered one.
	accepted, err := restored.acceptAutomaticSkillCompaction(context.Background(), noteGen, "recovery-note",
		schema.SkillReloadSelection{State: "absent"})
	if err != nil {
		t.Fatalf("acceptAutomaticSkillCompaction: %v", err)
	}
	if !accepted {
		t.Fatal("a completed delivery must unlatch note elicitation for a fresh automatic acceptance")
	}
	if _, err := restored.requestSkillCompaction(context.Background(), "", "",
		schema.SkillReloadSelection{State: "absent"}); err != nil {
		t.Fatalf("clearing the recovery note: %v", err)
	}
	second, err := restored.requestSkillCompaction(context.Background(), "after-recovery", "",
		schema.SkillReloadSelection{State: "absent"})
	if err != nil {
		t.Fatalf("the cycle must reopen after reconciliation completes the delivery: %v", err)
	}
	if second <= forced {
		t.Fatalf("the reopened cycle must mint a generation beyond %d, got %d", forced, second)
	}
}

// TestSkillCompaction_StaleSnapshot pins the reconciliation's staleness
// contract against real two-cycle flows and typed receipt fixtures: a stale
// snapshot cannot repeat a delivered operation (a delivered generation is
// never resurrected into the slot), never advances a receipt onto another
// generation's intent, and preserves each publication's final handoff in
// order.
func TestSkillCompaction_StaleSnapshot(t *testing.T) {
	t.Run("real two-cycle rewind", func(t *testing.T) {
		t.Parallel()
		stateDir := t.TempDir()
		s := newScriptedSummaryCompactSession(t, "stale-snap-cheap", func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nSUMMARY_f1e9\n[END SUMMARY]")}
		}, withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: stateDir}))
		id := s.Meta().ID
		disableSessionNaming(s)
		seedNumberedSessionHistory(t, s, 20)

		// Cycle 1: its forced operation is claimed and delivered by its own
		// fold — the delivered generation must never be repeated.
		first, err := s.requestSkillCompaction(context.Background(), "first-note", "first-instructions",
			schema.SkillReloadSelection{State: "valid", Names: []string{"scope:probe"}})
		if err != nil {
			t.Fatalf("requestSkillCompaction (first): %v", err)
		}
		s.applyPendingForceCompact(context.Background())
		if op := s.pendingSkillCompactionSnapshot(); op != nil {
			t.Fatalf("test setup: first cycle not consumed, got %+v", op)
		}
		// Cycle 2: requested (pending) — snapshot its metadata bytes, then
		// let its own fold publish and deliver.
		second, err := s.requestSkillCompaction(context.Background(), "second-note", "second-instructions",
			schema.SkillReloadSelection{State: "valid", Names: []string{"scope:probe"}})
		if err != nil {
			t.Fatalf("requestSkillCompaction (second): %v", err)
		}
		metaPath := filepath.Join(stateDir, sessionsSubdir, id+".meta.json")
		stale, err := os.ReadFile(metaPath)
		if err != nil {
			t.Fatalf("read stale metadata: %v", err)
		}
		s.applyPendingForceCompact(context.Background())
		s.Close()
		// Rewind the metadata: the snapshot now claims the second operation
		// is still pending, while the transcript holds both publications'
		// receipts.
		if err := os.WriteFile(metaPath, stale, 0o600); err != nil {
			t.Fatalf("rewind metadata to the stale snapshot: %v", err)
		}

		restored := restoreForPublication(t, stateDir, id, "stale-snap-cheap", func(llm.Request) llm.Response {
			t.Error("the restored session must not fold")
			return llm.Response{}
		})
		// The FIRST (delivered) generation is not resurrected into the slot,
		// and the SECOND's interrupted delivery is COMPLETED by
		// reconciliation (R18): a crash before its delivery save must not
		// change the post-recovery state.
		op := restored.pendingSkillCompactionSnapshot()
		if op != nil {
			t.Fatalf("reconciliation must complete generation %d's delivery without resurrecting generation %d, still holding %+v", second, first, op)
		}
		if sel := restored.pendingSkillReloadSelection(); sel.State != "absent" {
			t.Fatalf("the completed delivery must have consumed the stale selection, got %+v", sel)
		}
		// Both publications' final handoffs are preserved, coalesced by
		// publication identity, the final publication's handoff last.
		handoffs := pendingHandoffsSnapshot(restored)
		if len(handoffs) != 2 {
			t.Fatalf("restored handoffs = %+v, want one final handoff per publication", handoffs)
		}
		if handoffs[0].Operation.Generation != first || handoffs[1].Operation.Generation != second {
			t.Fatalf("restored handoffs = %+v, want generation %d then %d in publication order", handoffs, first, second)
		}
		if handoffs[0].Operation.PublicationID == "" || handoffs[0].Operation.PublicationID == handoffs[1].Operation.PublicationID {
			t.Fatalf("each publication keeps its own identity, got %+v", handoffs)
		}
		if handoffs[0].Phase != skillCompactionReceiptDelivered || handoffs[1].Phase != skillCompactionReceiptDelivered {
			t.Fatalf("both recovered handoffs must be delivered, got %+v", handoffs)
		}
	})

	t.Run("cancelled receipt never attaches to another fold's intent", func(t *testing.T) {
		t.Parallel()
		// Typed receipt fixtures for the reconciler's declared input domain:
		// a cancelled receipt for one generation must not retire — or leak
		// the selection of — a DIFFERENT generation's pending intent.
		stateDir := t.TempDir()
		id := identifier.MustNewSessionID()
		if err := os.MkdirAll(filepath.Join(stateDir, sessionsSubdir), 0o755); err != nil {
			t.Fatalf("mkdir sessions: %v", err)
		}
		writer, err := transcript.NewWriter(transcriptPath(stateDir, id), transcript.Header{SessionID: id, ProfileID: "openai", Model: "gpt-5.2"})
		if err != nil {
			t.Fatalf("create transcript: %v", err)
		}
		cancelledTurn := schema.NewTurn(schema.TurnUserInput, llm.User("fixture entry carrying a cancelled receipt"))
		cancelledTurn.SkillState = &schema.SkillTurnState{Compaction: &schema.SkillCompactionReceipt{
			Revision:  50,
			SessionID: id,
			Operation: schema.SkillCompactionOperation{Generation: 7, Origin: "forced", Phase: "pending", PublicationID: ""},
			Phase:     skillCompactionReceiptCancelled,
			Reason:    skillCompactionCancelForcedNotPublished,
		}}
		if err := writer.AppendDurable(cancelledTurn); err != nil {
			t.Fatalf("append fixture: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("close transcript: %v", err)
		}
		meta := schema.SessionMeta{
			ID:        id,
			ProfileID: "openai",
			Model:     "gpt-5.2",
			Skills: &schema.SkillLifecycleSnapshot{
				Inventory:        map[string]schema.SkillInventoryEntry{},
				Revision:         10,
				NextOperationGen: 8,
				PendingCompaction: &schema.SkillCompactionOperation{
					Generation:     8,
					Origin:         "forced",
					NoteGeneration: 4,
					Selection:      schema.SkillReloadSelection{State: "valid", Names: []string{"scope:probe"}},
					Phase:          "pending",
				},
				PendingSelection: &schema.SkillReloadSelection{State: "valid", Names: []string{"scope:probe"}},
			},
		}
		if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
			t.Fatalf("save stale metadata: %v", err)
		}
		restored := restoreForPublication(t, stateDir, id, "stale-cancelled-cheap", func(llm.Request) llm.Response {
			t.Error("the restored session must not fold")
			return llm.Response{}
		})
		// Generation 8's pending intent and its selection are untouched by
		// generation 7's cancellation: a retired selection never attaches to
		// another fold.
		op := restored.pendingSkillCompactionSnapshot()
		if op == nil || op.Generation != 8 || op.Phase != "pending" {
			t.Fatalf("a cancelled receipt for generation 7 must not retire generation 8's intent, got %+v", op)
		}
		if sel := restored.pendingSkillReloadSelection(); sel.State != "valid" || len(sel.Names) != 1 || sel.Names[0] != "scope:probe" {
			t.Fatalf("generation 8's selection must survive generation 7's cancellation, got %+v", sel)
		}
		handoffs := pendingHandoffsSnapshot(restored)
		if len(handoffs) != 1 || handoffs[0].Operation.Generation != 7 || handoffs[0].Phase != skillCompactionReceiptCancelled ||
			handoffs[0].Reason != skillCompactionCancelForcedNotPublished {
			t.Fatalf("restored handoffs = %+v, want the cancelled receipt for generation 7 recorded alone", handoffs)
		}
	})

	t.Run("delivered receipt cannot be repeated", func(t *testing.T) {
		t.Parallel()
		// A delivered receipt for the snapshot's own pending generation must
		// clear it — the stale snapshot cannot repeat an operation the
		// receipts show was already delivered — and consume its selection.
		stateDir := t.TempDir()
		id := identifier.MustNewSessionID()
		if err := os.MkdirAll(filepath.Join(stateDir, sessionsSubdir), 0o755); err != nil {
			t.Fatalf("mkdir sessions: %v", err)
		}
		writer, err := transcript.NewWriter(transcriptPath(stateDir, id), transcript.Header{SessionID: id, ProfileID: "openai", Model: "gpt-5.2"})
		if err != nil {
			t.Fatalf("create transcript: %v", err)
		}
		deliveredTurn := schema.NewTurn(schema.TurnSummary, llm.User("[CONTEXT SUMMARY]\nfixture delivered receipt\n[END SUMMARY]"))
		deliveredTurn.SkillState = &schema.SkillTurnState{Compaction: &schema.SkillCompactionReceipt{
			Revision:  60,
			SessionID: id,
			Operation: schema.SkillCompactionOperation{Generation: 8, Origin: "forced", Phase: "published", PublicationID: "fold-9"},
			Phase:     skillCompactionReceiptDelivered,
		}}
		if err := writer.AppendDurable(deliveredTurn); err != nil {
			t.Fatalf("append fixture: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("close transcript: %v", err)
		}
		meta := schema.SessionMeta{
			ID:        id,
			ProfileID: "openai",
			Model:     "gpt-5.2",
			Skills: &schema.SkillLifecycleSnapshot{
				Inventory:        map[string]schema.SkillInventoryEntry{},
				Revision:         10,
				NextOperationGen: 8,
				PendingCompaction: &schema.SkillCompactionOperation{
					Generation:     8,
					Origin:         "forced",
					NoteGeneration: 4,
					Selection:      schema.SkillReloadSelection{State: "valid", Names: []string{"scope:probe"}},
					Phase:          "pending",
				},
				PendingSelection: &schema.SkillReloadSelection{State: "valid", Names: []string{"scope:probe"}},
			},
		}
		if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
			t.Fatalf("save stale metadata: %v", err)
		}
		restored := restoreForPublication(t, stateDir, id, "stale-delivered-cheap", func(llm.Request) llm.Response {
			t.Error("the restored session must not fold")
			return llm.Response{}
		})
		if op := restored.pendingSkillCompactionSnapshot(); op != nil {
			t.Fatalf("a delivered operation cannot be repeated into the slot, got %+v", op)
		}
		if sel := restored.pendingSkillReloadSelection(); sel.State != "absent" {
			t.Fatalf("the delivered operation's selection must be consumed, got %+v", sel)
		}
		handoffs := pendingHandoffsSnapshot(restored)
		if len(handoffs) != 1 || handoffs[0].Operation.Generation != 8 || handoffs[0].Phase != skillCompactionReceiptDelivered {
			t.Fatalf("restored handoffs = %+v, want the delivered receipt for generation 8 alone", handoffs)
		}
	})
}

// TestSkillCompaction_RestartHandoffIdentity pins the publication identity's
// restart stability: a restored session's own next publication must never
// reuse a prior publication's identity, so the coalesced handoff list keeps
// both final handoffs instead of silently overwriting the restored one.
func TestSkillCompaction_RestartHandoffIdentity(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	summary := func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nSUMMARY_9d2f\n[END SUMMARY]")}
	}
	s := newScriptedSummaryCompactSession(t, "handoff-id-cheap", summary,
		withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: stateDir}))
	id := s.Meta().ID
	disableSessionNaming(s)
	seedNumberedSessionHistory(t, s, 20)
	first, err := s.requestSkillCompaction(context.Background(), "first-note", "first-instructions",
		schema.SkillReloadSelection{State: "valid", Names: []string{"scope:probe"}})
	if err != nil {
		t.Fatalf("requestSkillCompaction (first): %v", err)
	}
	s.applyPendingForceCompact(context.Background())
	if op := s.pendingSkillCompactionSnapshot(); op != nil {
		t.Fatalf("test setup: the first cycle must be delivered, still holding %+v", op)
	}
	s.Close()

	restored := restoreForPublication(t, stateDir, id, "handoff-id-cheap", summary)
	disableSessionNaming(restored)
	before := pendingHandoffsSnapshot(restored)
	if len(before) != 1 || before[0].Operation.Generation != first || before[0].Operation.PublicationID == "" {
		t.Fatalf("restored handoffs = %+v, want the first publication's delivered handoff", before)
	}
	// The restored session runs its own next cycle: the new publication must
	// carry a DIFFERENT identity, so its handoff appends beside the restored
	// one instead of coalescing over it.
	seedNumberedSessionHistory(t, restored, 20)
	second, err := restored.requestSkillCompaction(context.Background(), "second-note", "second-instructions",
		schema.SkillReloadSelection{State: "valid", Names: []string{"scope:probe"}})
	if err != nil {
		t.Fatalf("requestSkillCompaction (second): %v", err)
	}
	restored.applyPendingForceCompact(context.Background())
	if second <= first {
		t.Fatalf("test setup: the second cycle must mint generation %d beyond %d", second, first)
	}

	handoffs := pendingHandoffsSnapshot(restored)
	if len(handoffs) != 2 {
		t.Fatalf("both publications' final handoffs must survive, got %+v", handoffs)
	}
	if handoffs[0].Operation.Generation != first || handoffs[1].Operation.Generation != second {
		t.Fatalf("handoffs = %+v, want generation %d then %d in publication order", handoffs, first, second)
	}
	if handoffs[0].Operation.PublicationID == "" || handoffs[0].Operation.PublicationID == handoffs[1].Operation.PublicationID {
		t.Fatalf("a post-restart publication must not reuse a restored publication's identity, got %+v", handoffs)
	}
	if handoffs[0].Phase != skillCompactionReceiptDelivered || handoffs[1].Phase != skillCompactionReceiptDelivered {
		t.Fatalf("both handoffs must be delivered, got %+v", handoffs)
	}
}

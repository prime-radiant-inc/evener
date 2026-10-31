package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/appwire"
)

func TestRetirementAcceptedStartBlocksBeforeRunnerWake(t *testing.T) {
	t.Parallel()
	testRetirementPendingStart(t, false)
}

func TestRetirementClaimedStartBlocksBeforeTurn(t *testing.T) {
	t.Parallel()
	testRetirementPendingStart(t, true)
}

// Removing pending/claimed durable input from the predicate would allow a
// process retirement to strand input before its runner has executed anything.
func testRetirementPendingStart(t *testing.T, claimFirst bool) {
	root := newQueuePersistTestSession(t, t.TempDir())
	defer root.Close()
	c, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	p := appwire.TurnStartParams{ClientMutationID: "retirement-start-race", Input: []appwire.InputItem{{Type: "text", Text: "opaque-input"}}}
	response, err := root.AcceptClientMutationStart(p)
	if err != nil {
		t.Fatal(err)
	}
	if claimFirst {
		claimed, ok, err := root.claimClientMutationStart()
		if err != nil || !ok || claimed.StableTurnID != response.Turn.ID {
			t.Fatalf("claim: %#v %v %v", claimed, ok, err)
		}
	}
	claim, snapshot, err := c.TryClaim(true)
	if err != nil {
		t.Fatal(err)
	}
	if claim != nil {
		t.Fatal("retired accepted or claimed input before runner wake")
	}
	if !slices.ContainsFunc(snapshot.Blockers, func(b RetirementBlocker) bool { return b.Category == "input" && b.SessionID == root.ID() }) {
		t.Fatalf("missing input blocker: %+v", snapshot)
	}
	replay, err := root.AcceptClientMutationStart(p)
	if err != nil {
		t.Fatal(err)
	}
	if response.Receipt.Disposition != appwire.MutationDispositionApplied || replay.Receipt.Disposition != appwire.MutationDispositionReplayed || response.Turn.ID == "" || replay.Turn.ID != response.Turn.ID || response.Receipt.TurnID != response.Turn.ID || replay.Receipt.TurnID != response.Turn.ID || response.Receipt.ProjectionState != appwire.MutationProjectionPending || replay.Receipt.ProjectionState != appwire.MutationProjectionPending {
		t.Fatalf("wrong acceptance/replay identity or receipt: %#v %#v", response, replay)
	}
	journal := root.clientMutations.snapshot()
	pending := journal.PendingExecutions[p.ClientMutationID]
	wantAccepted := uint64(0)
	if claimFirst {
		wantAccepted = 1
	}
	if pending.TurnID != response.Turn.ID || !reflect.DeepEqual(pending.Input, p.Input) || len(journal.PendingExecutions) != 1 || journal.AcceptedTurns != wantAccepted {
		t.Fatalf("replay duplicated or changed input: %#v", journal)
	}
}

func retirementRefusalRoot(t *testing.T) (*Session, *RetirementController) {
	t.Helper()
	root := newQueuePersistTestSession(t, t.TempDir())
	t.Cleanup(root.Close)
	// Persist real rejected history so refusal must preserve an existing primary
	// journal, not merely leave an absent file absent.
	_, err := root.AcceptClientMutationCancelQueued(appwire.TurnCancelQueuedParams{ClientMutationID: "seed-rejected", Index: 0})
	if err == nil {
		t.Fatal("expected empty queue rejection")
	}
	c, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	claim, snapshot, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("claim: %v %+v", err, snapshot)
	}
	return root, c
}

func TestRetirementClaimFirstPreservesOriginalJournal(t *testing.T) {
	t.Parallel()
	input := []appwire.InputItem{{Type: "text", Text: "opaque-input"}}
	cases := []struct {
		name   string
		effect func(*Session) error
	}{
		{"start", func(s *Session) error {
			_, err := s.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: "refused-start", Input: input})
			return err
		}},
		{"queue", func(s *Session) error {
			_, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: "refused-queue", Input: input})
			return err
		}},
		{"steer", func(s *Session) error {
			_, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{ClientMutationID: "refused-steer", Input: input})
			return err
		}},
		{"drain", func(s *Session) error {
			_, err := s.AcceptClientMutationDrainAsSteer(appwire.TurnDrainAsSteerParams{ClientMutationID: "refused-drain", Input: input})
			return err
		}},
		{"promote", func(s *Session) error {
			_, err := s.AcceptClientMutationPromoteQueuedAsSteer(appwire.TurnPromoteQueuedAsSteerParams{ClientMutationID: "refused-promote"})
			return err
		}},
		{"cancel", func(s *Session) error {
			_, err := s.AcceptClientMutationCancelQueued(appwire.TurnCancelQueuedParams{ClientMutationID: "refused-cancel"})
			return err
		}},
		{"interrupt", func(s *Session) error {
			_, err := s.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{ClientMutationID: "refused-interrupt"}, func() { t.Error("refused interrupt invoked callback") })
			return err
		}},
		{"pre-turn-claim", func(s *Session) error {
			_, ok, err := s.claimClientMutationStart()
			if ok {
				t.Error("refused claim returned work")
			}
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := retirementRefusalRoot(t)
			before := root.clientMutations.snapshot()
			path := clientMutationFilePath(root.clientMutations.stateDir, root.clientMutations.sessionID)
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.effect(root); !errors.Is(err, ErrRetirementUnavailable) {
				t.Errorf("effect error = %v, want retirement unavailable", err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(original, after) {
				t.Error("refused effect changed original journal bytes")
			}
			if !reflect.DeepEqual(before, root.clientMutations.snapshot()) {
				t.Error("refused effect changed journal snapshot")
			}
		})
	}
}

func TestRetirementEffectRefusalReportsError(t *testing.T) {
	t.Parallel()
	root, _ := retirementRefusalRoot(t)
	effects, ok := any(root).(interface {
		SetReasoningEffort(string) error
		Steer(string) error
	})
	if !ok {
		t.Fatal("reasoning and steering effects do not report lifecycle errors")
	}
	beforeEffort := root.cfg.ReasoningEffort
	beforeQueue := root.SteeringQueueSnapshot()
	if err := effects.SetReasoningEffort("high"); !errors.Is(err, ErrRetirementUnavailable) {
		t.Errorf("reasoning error: %v", err)
	}
	if err := effects.Steer("opaque-input"); !errors.Is(err, ErrRetirementUnavailable) {
		t.Errorf("steer error: %v", err)
	}
	if root.cfg.ReasoningEffort != beforeEffort || !reflect.DeepEqual(beforeQueue, root.SteeringQueueSnapshot()) {
		t.Fatal("refused effect changed session")
	}
}

func TestRetirementAdmissionDirectEffects(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		effect func(*Session) error
	}{
		{"model", func(s *Session) error { return s.SetModel("gpt-5.2") }},
		{"vision", func(s *Session) error { return s.SetVisionModel("off") }},
		{"compact", func(s *Session) error { return s.Compact(context.Background()) }},
		{"goal", func(s *Session) error { _, err := s.SetGoal(context.Background(), "opaque-goal"); return err }},
		{"enqueue", func(s *Session) error { return s.Enqueue(context.Background(), "opaque-input") }},
		{"enqueue-images", func(s *Session) error { return s.EnqueueWithImages(context.Background(), "opaque-input", nil) }},
		{"drain", func(s *Session) error { return s.DrainAsSteer(context.Background()) }},
		{"drain-input", func(s *Session) error { return s.DrainAsSteerWithInput(context.Background(), "opaque-input", nil) }},
		{"promote", func(s *Session) error { return s.PromoteQueuedAsSteer(context.Background(), 0, "") }},
		{"cancel", func(s *Session) error { _, _, err := s.CancelQueued(context.Background(), 0, ""); return err }},
		{"turn", func(s *Session) error {
			_, err := s.ProcessInput(context.Background(), "opaque-input", nil)
			return err
		}},
		{"steer-kind", func(s *Session) error { return s.SteerKind("opaque-input", "") }},
		{"steer-task", func(s *Session) error { return s.SteerTaskCompletion("opaque-input", nil) }},
		{"steer-provenance", func(s *Session) error { return s.SteerWithProvenance("opaque-input", nil, "") }},
		{"steer-images", func(s *Session) error { return s.SteerWithImages("opaque-input", nil) }},
		{"steer-user", func(s *Session) error { return s.SteerFromUser("opaque-input") }},
		{"steer-user-images", func(s *Session) error { return s.SteerFromUserWithImages("opaque-input", nil) }},
		{"hook", func(s *Session) error { return s.deliverHookContext("opaque-input") }},
		{"fold", func(s *Session) error { return s.steerKindForFold("opaque-input", "", ungatedFoldRevision) }},
		{"task-tool-dependency", func(s *Session) error { return newToolDeps(s).sendTaskCompletionSteering("opaque-input", nil) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, _ := retirementRefusalRoot(t)
			before := root.clientMutations.snapshot()
			if err := tc.effect(root); !errors.Is(err, ErrRetirementUnavailable) {
				t.Errorf("effect error = %v, want retirement unavailable", err)
			}
			if !reflect.DeepEqual(before, root.clientMutations.snapshot()) {
				t.Error("direct effect changed durable journal")
			}
		})
	}
}

func TestRetirementAdmissionNotificationStaysPending(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("retried notification observed") },
	}}
	root := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir()}), withAdapter(adapter))
	c := retirementEvidenceController(t, root)
	claim, state, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("claim: %+v %v", state, err)
	}
	original, err := os.ReadFile(root.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	// Retain the exact producer-owned delivery for retry. The admission owner,
	// not the low-level transcript writer, must refuse before any custody effect.
	n := notification{ID: "retirement-refused-notice", Message: llm.UserMachinery("opaque-refused-notice")}
	if err := root.delegateController.enqueueNotification(root.ID(), n); !errors.Is(err, ErrRetirementUnavailable) {
		t.Fatalf("controller-addressed notification was not refused: %v", err)
	}
	after, err := os.ReadFile(root.TranscriptPath())
	if err != nil || !bytes.Equal(original, after) {
		t.Fatalf("refused notification changed receiver transcript: %v", err)
	}
	if len(root.SteeringQueueSnapshot()) != 0 {
		t.Fatal("refused notification enqueued")
	}
	assertAttentionResponseReceipts(t, root, n.ID, 0)
	if err := c.Abort(claim, ""); err != nil {
		t.Fatal(err)
	}
	if err := root.delegateController.enqueueNotification(root.ID(), n); err != nil {
		t.Fatal(err)
	}
	retirementNotificationCustody(t, root, n.ID, 0)
	if _, err := root.ProcessInputKind(t.Context(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	if !requestsContain(adapter.Requests(), "opaque-refused-notice") {
		t.Fatal("the retained delivery did not reach the model after Abort")
	}
	retirementNotificationCustody(t, root, n.ID, 1)
	assertRetirementEvidenceEligible(t, c)
}

// retirementObstructReceiverTranscript refuses the real transport destination.
// Callers register their finalizer/Close cleanup first, so restoration runs
// before it even when the helper exits after rename but before replacement.
func retirementObstructReceiverTranscript(t *testing.T, root *Session) ([]byte, func() error) {
	t.Helper()
	path := root.TranscriptPath()
	backup := path + ".retirement-test-backup"
	beforeFault, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	moved := false
	repairTranscript := func() error {
		if moved {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			if err := os.Rename(backup, path); err != nil {
				return err
			}
			moved = false
			restored, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(beforeFault, restored) {
				return fmt.Errorf("receiver restoration changed original bytes: %w", err)
			}
		}
		if _, err := os.Stat(backup); !os.IsNotExist(err) {
			return fmt.Errorf("receiver backup remains: %w", err)
		}
		return nil
	}
	t.Cleanup(func() {
		if err := repairTranscript(); err != nil {
			t.Error(err)
		}
	})
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	moved = true // Restoration is owed even if making the obstruction fails.
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return beforeFault, repairTranscript
}

// The owning source is a real terminal watch-send journal. Its normal
// controller-addressed acceptance must preserve that source when preparing
// refuses admission. Production firing leases prevent new source production
// behind a granted claim, so that separate boundary is tested above.
func TestRetirementAdmissionWatchSourcePreserved(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClockAt(time.Unix(1_700_000_000, 0))
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("retained watch source observed") },
	}}
	root := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), clock: clk}), withAdapter(adapter))
	c := retirementEvidenceController(t, root)
	jm := root.jobManager
	rec, err := jm.createShell(createShellOpts{Command: "retirement-refused-watch-shell"})
	if err != nil {
		t.Fatal(err)
	}
	registered, err := jm.configureWatch(watchArgs{Target: rec.JobID, Source: rec.JobID, OutputMatch: "never-seen", Send: &watchSendArgs{To: "caller", Message: "opaque-retained-watch-notice"}})
	if err != nil {
		t.Fatal(err)
	}
	jm.mu.Lock()
	shellDone := jm.running[rec.JobID].done
	jm.mu.Unlock()
	t.Cleanup(func() {
		// The claim worker's defer has unlocked/joined, and the later-registered
		// obstruction cleanup has repaired transport before finalization.
		if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "fixture-settled", nil); err != nil {
			t.Error(err)
		}
		awaitSignal(t, shellDone, "terminal shell cleanup finalizer")
		root.Close()
		clk.Drain()
	})
	// Terminal production now dispatches synchronously. Refuse only its real
	// filesystem destination, then repair it BEFORE the preparing interval.
	beforeFault, repairTranscript := retirementObstructReceiverTranscript(t, root)
	finalizeErr := jm.finalize(rec.JobID, jobstore.StatusCompleted, "fixture-settled", nil)
	var pathErr *os.PathError
	if !errors.As(finalizeErr, &pathErr) || pathErr.Path != root.TranscriptPath() {
		t.Fatalf("terminal custody did not encounter the receiver filesystem fault: %v", finalizeErr)
	}
	if err := repairTranscript(); err != nil {
		t.Fatal(err)
	}
	shells, err := jm.store.Load()
	if err != nil || shells[rec.JobID] == nil {
		t.Fatalf("original terminal shell missing: %+v err=%v", shells, err)
	}
	originalShell := *shells[rec.JobID]
	if originalShell.Status != jobstore.StatusCompleted || originalShell.TerminalGen == "" || originalShell.NotifyState != jobstore.NotifyPending {
		t.Fatalf("original terminal shell not retained for custody retry: %+v", originalShell)
	}
	originalSource, err := jm.store.LoadWatchSends()
	if err != nil || len(originalSource.Pending) != 1 {
		t.Fatalf("real producer source missing: %+v err=%v", originalSource, err)
	}
	var source jobstore.WatchSendState
	for _, pending := range originalSource.Pending {
		source = *pending
	}
	if source.Key.WatchID != registered.WatchID || source.Key.WatchTarget != rec.JobID || source.Key.WatchGeneration == "" || source.DeliveryID == "" || source.ReceiverSessionID != root.ID() || !source.EndNotice {
		t.Fatalf("terminal watch source identity changed: %+v", source)
	}
	id := fmt.Sprintf("watch:%s:%d", source.DeliveryID, source.UpdateSeq)
	shellID := stableShellAttentionID(rec.JobID, originalShell.TerminalGen)
	assertAttentionResponseReceipts(t, root, id, 0)
	assertAttentionResponseReceipts(t, root, shellID, 0)
	originalReceiver, err := os.ReadFile(root.TranscriptPath())
	if err != nil || !bytes.Equal(beforeFault, originalReceiver) {
		t.Fatalf("fault gave receiver custody or damaged original bytes: %v", err)
	}
	jm.mu.Lock()
	transportRetryArmed := jm.watchTransportRetry.active
	jm.mu.Unlock()
	c.mu.Lock()
	activeBeforeClaim := len(c.active)
	c.mu.Unlock()
	if !transportRetryArmed || activeBeforeClaim != 0 || len(adapter.Requests()) != 0 || !clk.Now().Equal(time.Unix(1_700_000_000, 0)) {
		t.Fatalf("preparing precondition: transport retry=%t active leases=%d requests=%d clock=%v", transportRetryArmed, activeBeforeClaim, len(adapter.Requests()), clk.Now())
	}
	preparing := make(chan *RetirementClaim, 1)
	c.preparingObserved = func(claim *RetirementClaim) {
		select {
		case preparing <- claim:
		default:
		}
	}
	jm.mu.Lock()
	unlockJobs := sync.OnceFunc(jm.mu.Unlock)
	type claimResult struct {
		claim *RetirementClaim
		err   error
	}
	done := make(chan claimResult, 1)
	go func() { claim, _, err := c.TryClaim(true); done <- claimResult{claim, err} }()
	joinClaim := sync.OnceValue(func() claimResult {
		unlockJobs()
		select {
		case result := <-done:
			return result
		// TRIPWIRE: releasing jm.mu lets real evidence collection return, only a hang reaches this guard.
		case <-time.After(5 * time.Second):
			t.Fatal("claim evidence worker did not return")
			return claimResult{}
		}
	})
	defer joinClaim()
	var observed *RetirementClaim
	select {
	case observed = <-preparing:
	// TRIPWIRE: TryClaim reports its actual published claim, only a hang reaches this guard.
	case <-time.After(5 * time.Second):
		t.Fatal("watch source preparing claim was not observed")
	}
	c.mu.Lock()
	current := observed != nil && observed.controller == c && observed.root == root && observed.generation == c.generation && c.root == root && c.claim == observed && c.phase == "preparing"
	c.mu.Unlock()
	if !current {
		t.Fatalf("observed claim is not the current preparing identity: %p", observed)
	}
	// This is the real transport acceptance function and its installed
	// controller route, not a substituted enqueue callback or writer.
	if err := jm.acceptWatchFrame(source); !errors.Is(err, ErrRetirementUnavailable) {
		t.Fatalf("producer did not report controller admission refusal: %v", err)
	}
	afterSource, err := jm.store.LoadWatchSends()
	if err != nil || !reflect.DeepEqual(originalSource, afterSource) {
		t.Fatalf("refusal lost or changed source: %+v err=%v", afterSource, err)
	}
	afterReceiver, err := os.ReadFile(root.TranscriptPath())
	if err != nil || !bytes.Equal(originalReceiver, afterReceiver) {
		t.Fatalf("refusal wrote receiver custody: %v", err)
	}
	result := joinClaim()
	if result.err != nil || result.claim != nil {
		t.Fatalf("pending source escaped claim evidence: claim=%v err=%v", result.claim, result.err)
	}
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "fixture-settled", nil); err != nil {
		t.Fatal(err)
	}
	awaitSignal(t, shellDone, "same terminal shell custody finalizer")
	shells, err = jm.store.Load()
	if err != nil || shells[rec.JobID] == nil {
		t.Fatalf("retried terminal shell missing: %+v err=%v", shells, err)
	}
	shell := shells[rec.JobID]
	if shell.TerminalGen != originalShell.TerminalGen || shell.Status != originalShell.Status || shell.Reason != originalShell.Reason || shell.NotifyState != jobstore.NotifyDelivered {
		t.Fatalf("custody retry changed original terminal shell: before=%+v after=%+v", originalShell, shell)
	}
	finished := producerSourceEvents(t, jm, jobstore.EventJobFinished)
	if len(finished) != 1 || finished[0].JobID != rec.JobID || finished[0].TerminalGen != originalShell.TerminalGen {
		t.Fatalf("custody retry duplicated or replaced terminal production: %+v", finished)
	}
	retirementNotificationCustody(t, root, shellID, 0)
	if err := jm.dispatchProducedWatchSendsFrom(jm.store.LoadWatchSends); err != nil {
		t.Fatal(err)
	}
	retirementNotificationCustody(t, root, id, 0)
	settled, err := jm.store.LoadWatchSends()
	if err != nil || len(settled.Pending) != 0 {
		t.Fatalf("retained source retry did not settle: %+v err=%v", settled, err)
	}
	if _, err := root.ProcessInputKind(t.Context(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	if !requestsContain(adapter.Requests(), "opaque-retained-watch-notice") {
		t.Fatal("original retained source never reached the provider")
	}
	retirementNotificationCustody(t, root, id, 1)
	retirementNotificationCustody(t, root, shellID, 1)
	requestsBeforeRetry := len(adapter.Requests())
	// Let the actual fault-created retry inspect the repaired, settled source.
	clk.Advance(jobNotificationRetryInitialDelay)
	clk.Drain()
	jm.mu.Lock()
	retrying := jm.watchTransportRetry.active
	jm.mu.Unlock()
	c.mu.Lock()
	activeAfterRetry := len(c.active)
	c.mu.Unlock()
	root.attentionMu.Lock()
	callbacks := root.attentionCallbacks
	root.attentionMu.Unlock()
	if retrying || activeAfterRetry != 0 || callbacks != 0 || len(adapter.Requests()) != requestsBeforeRetry {
		t.Fatalf("settled source retry left work: retry=%t leases=%d callbacks=%d requests=%d->%d", retrying, activeAfterRetry, callbacks, requestsBeforeRetry, len(adapter.Requests()))
	}
	retirementNotificationCustody(t, root, id, 1)
	retirementNotificationCustody(t, root, shellID, 1)
	afterRetry, err := jm.store.LoadWatchSends()
	if err != nil || !reflect.DeepEqual(settled, afterRetry) {
		t.Fatalf("settled retry changed watch source: %+v err=%v", afterRetry, err)
	}
	if pending, err := root.pendingNotificationIDs(); err != nil || len(pending) != 0 {
		t.Fatalf("settled retry created receiver work: %v err=%v", pending, err)
	}
	assertRetirementEvidenceEligible(t, c)
}

// Low-level synced custody can outlive a resident runtime. This checks that
// particular accepted append, not a general admission policy or daemon wake.
func TestRetirementAdmissionLateCustodySurvivesRestore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	root := newQueuePersistTestSession(t, dir)
	t.Cleanup(root.Close)
	c := retirementEvidenceController(t, root)
	claim, state, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("claim: %+v %v", state, err)
	}
	prepared, err := c.Prepare(t.Context(), claim)
	if err != nil {
		t.Fatal(err)
	}
	const id = "retirement-late-preserved"
	if err := root.enqueueSystemNotificationFromSource(id, "opaque-late-preserved-notice"); err != nil {
		t.Fatal(err)
	}
	if err := c.Commit(claim); err != nil {
		t.Fatal(err)
	}
	if err := c.DrainReaders(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := root.ReleaseForRetirement(t.Context(), prepared); err != nil {
		t.Fatal(err)
	}
	retirementNotificationCustody(t, root, id, 0)
	restored := restoreQueuePersistTestSession(t, dir, root.ID())
	t.Cleanup(restored.Close)
	if restored.ID() != root.ID() {
		t.Fatal("retirement changed receiver identity")
	}
	retirementNotificationCustody(t, restored, id, 0)
	var wakes atomic.Int32
	restored.SetNotifyFunc(func() { wakes.Add(1) })
	defer restored.SetNotifyFunc(nil)
	if wakes.Load() != 1 {
		t.Fatalf("normal callback registration did not wake retained custody: %d", wakes.Load())
	}
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return finalResponse("restored notice observed") },
	}}
	restored.client.Register(adapter)
	if _, err := restored.ProcessInputKind(t.Context(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	if !requestsContain(adapter.Requests(), "opaque-late-preserved-notice") {
		t.Fatal("restored custody never reached provider")
	}
	retirementNotificationCustody(t, restored, id, 1)
}

// The only substituted dependency is the OS filesystem. Every store serializer,
// validation, write, fsync, rename, projection and replay path remains real.
type retirementBarrierFS struct {
	afero.Fs
	entered chan struct{}
	resume  chan struct{}
	once    sync.Once
}
type retirementBarrierFile struct {
	afero.File
	fs *retirementBarrierFS
}

func (f *retirementBarrierFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := f.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &retirementBarrierFile{File: file, fs: f}, nil
}
func (f *retirementBarrierFile) Write(data []byte) (int, error) {
	f.fs.once.Do(func() { close(f.fs.entered); <-f.fs.resume })
	return f.File.Write(data)
}

func TestRetirementAdmissionFilesystemBarrier(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"start", "queue", "steer", "drain", "promote", "cancel", "interrupt", "pre-turn-claim"} {
		t.Run(name, func(t *testing.T) {
			root := newQueuePersistTestSession(t, t.TempDir())
			defer root.Close()
			input := []appwire.InputItem{{Type: "text", Text: "opaque-input"}}
			if name == "pre-turn-claim" || name == "interrupt" {
				if _, err := root.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: "seed-start", Input: input}); err != nil {
					t.Fatal(err)
				}
			}
			if name == "promote" || name == "cancel" {
				if _, err := root.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: "seed-queue", Input: input}); err != nil {
					t.Fatal(err)
				}
			}
			if err := root.ensureClientMutationStore(); err != nil {
				t.Fatal(err)
			}
			c, err := NewRetirementController(0, clock.Real())
			if err != nil {
				t.Fatal(err)
			}
			if err := c.AttachRoot(root); err != nil {
				t.Fatal(err)
			}
			var releaseOnce sync.Once
			fs := &retirementBarrierFS{Fs: root.clientMutations.fs, entered: make(chan struct{}), resume: make(chan struct{})}
			resume := func() { releaseOnce.Do(func() { close(fs.resume) }) }
			defer resume()
			root.clientMutations.fs = fs
			effect := func() (any, error) {
				switch name {
				case "start":
					return root.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: "barrier-start", Input: input})
				case "queue":
					return root.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: "barrier-queue", Input: input})
				case "steer":
					return root.AcceptClientMutationSteer(appwire.TurnSteerParams{ClientMutationID: "barrier-steer", Input: input})
				case "drain":
					return root.AcceptClientMutationDrainAsSteer(appwire.TurnDrainAsSteerParams{ClientMutationID: "barrier-drain", Input: input})
				case "promote":
					return root.AcceptClientMutationPromoteQueuedAsSteer(appwire.TurnPromoteQueuedAsSteerParams{ClientMutationID: "barrier-promote"})
				case "cancel":
					return root.AcceptClientMutationCancelQueued(appwire.TurnCancelQueuedParams{ClientMutationID: "barrier-cancel"})
				case "interrupt":
					return root.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{ClientMutationID: "barrier-interrupt"}, nil)
				case "pre-turn-claim":
					item, ok, err := root.claimClientMutationStart()
					if !ok && err == nil {
						return item, errors.New("no start claimed")
					}
					return item, err
				default:
					panic("unknown test case")
				}
			}
			type result struct {
				value any
				err   error
			}
			done := make(chan result, 1)
			go func() { v, err := effect(); done <- result{v, err} }()
			select {
			case <-fs.entered:
			case r := <-done:
				t.Fatalf("effect missed write barrier: %v", r.err)
				// TRIPWIRE: fixture I/O/provider entry takes milliseconds; completion is the channel above, not this bound.
			case <-time.After(5 * time.Second):
				t.Fatal("write barrier was not reached")
			}
			// The claim must return before the filesystem is released, not wait for I/O.
			claimDone := make(chan error, 1)
			go func() {
				claim, snapshot, err := c.TryClaim(true)
				if err == nil && (claim != nil || !slices.ContainsFunc(snapshot.Blockers, func(b RetirementBlocker) bool { return b.Category == "input" && b.SessionID == root.ID() })) {
					err = errors.New("write did not hold input admission")
				}
				claimDone <- err
			}()
			select {
			case err := <-claimDone:
				if err != nil {
					t.Fatal(err)
				}
				// TRIPWIRE: fixture I/O/provider entry takes milliseconds; completion is the channel above, not this bound.
			case <-time.After(5 * time.Second):
				t.Fatal("claim waited on mutation journal I/O")
			}
			resume()
			first := <-done
			if first.err != nil {
				t.Fatal(first.err)
			}
			store := root.clientMutations
			// Cold-load the primary file through the production loader, not a snapshot
			// copied from the writer. Restore normalization is deliberately retained.
			cold, err := newClientMutationStore(store.stateDir, store.sessionID)
			if err != nil {
				t.Fatal(err)
			}
			before := store.snapshot()
			if name == "pre-turn-claim" {
				pending := before.PendingExecutions["seed-start"]
				if pending.ExecutionState != "claimed" || pending.TurnID == "" || !reflect.DeepEqual(pending.Input, input) {
					t.Fatalf("claim lost durable payload: %#v", pending)
				}
				if cold.snapshot().PendingExecutions["seed-start"].TurnID != pending.TurnID {
					t.Fatal("cold load changed claimed identity")
				}
				root.clientMutations = cold
				replay, err := root.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: "seed-start", Input: input})
				if err != nil || replay.Receipt.Disposition != appwire.MutationDispositionReplayed || replay.Receipt.ProjectionState != appwire.MutationProjectionPending || replay.Turn.ID != pending.TurnID || replay.Receipt.TurnID != pending.TurnID {
					t.Fatalf("cold pre-turn retry: %#v %v", replay, err)
				}
				if len(cold.snapshot().Journal) != len(before.Journal) {
					t.Fatal("cold pre-turn retry duplicated intent")
				}
				return
			}
			root.clientMutations = cold
			replay, replayErr := effect()
			if replayErr != nil {
				t.Fatal(replayErr)
			}
			var accepted, replayed struct{ Receipt appwire.MutationReceipt }
			b, err := json.Marshal(first.value)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(b, &accepted); err != nil {
				t.Fatal(err)
			}
			b, err = json.Marshal(replay)
			if err != nil {
				t.Fatal(err)
			}
			if err = json.Unmarshal(b, &replayed); err != nil {
				t.Fatal(err)
			}
			if accepted.Receipt.Disposition != appwire.MutationDispositionApplied || replayed.Receipt.Disposition != appwire.MutationDispositionReplayed || accepted.Receipt.TurnID != replayed.Receipt.TurnID || !reflect.DeepEqual(accepted.Receipt.QueueEntryIDs, replayed.Receipt.QueueEntryIDs) {
				t.Fatalf("cold retry changed identity: %#v %#v", accepted, replayed)
			}
			if len(before.Journal) != len(cold.snapshot().Journal) {
				t.Fatal("retry duplicated journal intent")
			}
		})
	}
}

func TestRetirementStartReplayExecutesOnce(t *testing.T) {
	t.Parallel()
	root := newQueuePersistTestSession(t, t.TempDir())
	defer root.Close()
	entered, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(resume) }) }
	defer unblock()
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{func(llm.Request) llm.Response { close(entered); <-resume; return finalResponse("opaque-result") }}}
	root.client.Register(adapter)
	root.profile = withTestSessionNamer(root.client, root.profile)
	c, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err = c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	params := appwire.TurnStartParams{ClientMutationID: "replay-executes-once", Input: []appwire.InputItem{{Type: "text", Text: "opaque-input"}}}
	accepted, err := root.AcceptClientMutationStart(params)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := root.AcceptClientMutationStart(params)
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Receipt.Disposition != appwire.MutationDispositionApplied || replay.Receipt.Disposition != appwire.MutationDispositionReplayed || accepted.Receipt.ProjectionState != appwire.MutationProjectionPending || replay.Receipt.ProjectionState != appwire.MutationProjectionPending || accepted.Turn.ID == "" || replay.Turn.ID != accepted.Turn.ID {
		t.Fatalf("pending acceptance/replay: %#v %#v", accepted, replay)
	}
	done := make(chan error, 1)
	go func() {
		_, ok, err := root.ProcessClientMutationStart(context.Background(), nil)
		if !ok && err == nil {
			err = errors.New("runner did not claim input")
		}
		done <- err
	}()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("runner missed provider: %v", err)
	// TRIPWIRE: fixture I/O/provider entry takes milliseconds; completion is the channel above, not this bound.
	case <-time.After(5 * time.Second):
		t.Fatal("provider not entered")
	}
	if claim, _, err := c.TryClaim(true); err != nil || claim != nil {
		t.Fatalf("active turn claimed: %v", err)
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	reflected, err := root.AcceptClientMutationStart(params)
	if err != nil {
		t.Fatal(err)
	}
	if reflected.Receipt.Disposition != appwire.MutationDispositionReplayed || reflected.Receipt.ProjectionState != appwire.MutationProjectionReflected || reflected.Turn.ID != accepted.Turn.ID || reflected.Receipt.TurnID != accepted.Turn.ID {
		t.Fatalf("settled replay: %#v", reflected)
	}
	if _, ok, err := root.ProcessClientMutationStart(context.Background(), nil); err != nil || ok {
		t.Fatalf("replay reran: %v %v", ok, err)
	}
	if got := len(adapter.Requests()); got != 1 {
		t.Fatalf("provider executions = %d, want one", got)
	}
	var users []schema.Turn
	root.mu.Lock()
	for _, turn := range root.history {
		if turn.Kind == schema.TurnUserInput {
			users = append(users, turn)
		}
	}
	root.mu.Unlock()
	if len(users) != 1 || users[0].ClientMutationID != params.ClientMutationID || users[0].StableTurnID != accepted.Turn.ID || users[0].Message.Text() != params.Input[0].Text {
		t.Fatalf("user input identity: %#v", users)
	}
	// Check the original primary transcript independently, not a reconstruction
	// from the in-memory history or the acceptance response.
	primary, err := os.ReadFile(root.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(primary), []byte("\n"))
	header, err := transcript.DecodeHeader(lines[0])
	if err != nil || header.SessionID != root.ID() {
		t.Fatalf("primary header: %#v %v", header, err)
	}
	var primaryUsers []schema.Turn
	for _, line := range lines[1:] {
		entry, err := transcript.DecodeEntry(line)
		if err != nil {
			t.Fatal(err)
		}
		if entry.Turn.Kind == schema.TurnUserInput {
			primaryUsers = append(primaryUsers, entry.Turn)
		}
	}
	if len(primaryUsers) != 1 || primaryUsers[0].ClientMutationID != params.ClientMutationID || primaryUsers[0].StableTurnID != accepted.Turn.ID || primaryUsers[0].Message.Text() != params.Input[0].Text {
		t.Fatalf("primary transcript user input identity: %#v", primaryUsers)
	}
	settled := root.clientMutations.snapshot()
	if len(settled.PendingExecutions) != 0 || settled.AcceptedTurns != 1 || len(settled.Journal) != 1 {
		t.Fatalf("settlement did not retain one executed intent: %#v", settled)
	}
	if claim, snapshot, err := retirementClaimAfterFirstTurn(root, c); err != nil || claim == nil {
		t.Fatalf("terminal history blocked retirement: %v %+v", err, snapshot)
	}
}

func TestRetirementClaimFirstPrivateInputHandoffs(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"queue-claim", "steering-carrier-claim", "turn-owned-steer"} {
		t.Run(name, func(t *testing.T) {
			root, _ := retirementRefusalRoot(t)
			before := root.clientMutations.snapshot()
			path := clientMutationFilePath(root.clientMutations.stateDir, root.clientMutations.sessionID)
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			drainRetirementTestEvents(root)
			switch name {
			case "queue-claim":
				if got := root.popQueueHead(); got.ClientMutationID != "" {
					t.Fatal("refused queue claim returned input")
				}
			case "steering-carrier-claim":
				if _, ok := root.claimSteeringCarrierTurn(); ok {
					t.Fatal("refused carrier claimed")
				}
			case "turn-owned-steer":
				if root.trySteerTurnOwnedMessage(steeringMessage{Text: "opaque-input"}, &struct{ _ byte }{}) {
					t.Fatal("refused turn-owned steering appended")
				}
			}
			if name == "queue-claim" || name == "steering-carrier-claim" {
				if !slices.Contains(drainRetirementTestEvents(root), events.EventWarning) {
					t.Error("refused private claim did not report admission failure")
				}
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(original, after) || !reflect.DeepEqual(before, root.clientMutations.snapshot()) || len(root.SteeringQueueSnapshot()) != 0 {
				t.Fatal("refused private handoff changed input")
			}
		})
	}
}

func drainRetirementTestEvents(root *Session) []events.EventKind {
	var kinds []events.EventKind
	for {
		select {
		case ev := <-root.Events():
			kinds = append(kinds, ev.Kind)
		default:
			return kinds
		}
	}
}

func TestRetirementAdmissionFollowUp(t *testing.T) {
	t.Parallel()
	root, _ := retirementRefusalRoot(t)
	enqueue, ok := any(root).(interface{ FollowUp(string) error })
	if !ok {
		t.Fatal("FollowUp cannot report retirement refusal")
	}
	before := append([]string(nil), root.followups...)
	if err := enqueue.FollowUp("opaque-followup"); !errors.Is(err, ErrRetirementUnavailable) {
		t.Fatalf("FollowUp error: %v", err)
	}
	if !reflect.DeepEqual(before, root.followups) {
		t.Fatal("refused followup changed queue")
	}
}

func TestRetirementAcceptedFollowUpBlocks(t *testing.T) {
	t.Parallel()
	root := newQueuePersistTestSession(t, t.TempDir())
	defer root.Close()
	if err := root.FollowUp("opaque-followup"); err != nil {
		t.Fatal(err)
	}
	c, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err = c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	claim, snapshot, err := c.TryClaim(true)
	if err != nil || claim != nil || !slices.ContainsFunc(snapshot.Blockers, func(b RetirementBlocker) bool { return b.Category == "input" && b.SessionID == root.ID() }) {
		t.Fatalf("accepted followup lost blocker: %v %+v", err, snapshot)
	}
}

func TestRetirementReasoningPersistenceError(t *testing.T) {
	t.Parallel()
	root := newQueuePersistTestSession(t, t.TempDir())
	defer root.Close()
	root.cfg.testOnly.metaFS = afero.NewReadOnlyFs(afero.NewOsFs())
	if err := root.SetReasoningEffort("high"); err == nil {
		t.Fatal("reasoning setter hid metadata persistence failure")
	}
	if root.ReasoningEffort() != "high" {
		t.Fatal("setter must report save failure after mutation")
	}
}

// The serve loop probes for a runnable durable start after every message it
// processes, including right after the turn that just settled. A probe that
// finds nothing to run is not admitted work: it must leave the settled instant,
// and so the idle deadline, where the settlement put it. Counting it as work
// restarted the interval at whenever the probe happened to run, which under
// load was after virtual time had already moved on.
func TestRetirementIdleStartProbeKeepsInterval(t *testing.T) {
	t.Parallel()
	root, c, settled := newSettledRetirementRoot(t)
	if _, processed, err := root.ProcessClientMutationStart(context.Background(), nil); err != nil || processed {
		t.Fatalf("idle probe = processed %v, err %v; want nothing to run", processed, err)
	}
	if got := c.Snapshot().EligibleSince; !got.Equal(settled) {
		t.Fatalf("idle probe moved the settled instant from %v to %v; it is not admitted work", settled, got)
	}
}

// A queued-input wake that finds no queued message and no user steering is the
// same kind of no-op as an idle start probe: it must not restart the idle
// interval either. Wakes are unconditional and coalesce, so one can arrive
// after the work it was sent for has already run.
func TestRetirementIdleQueuedInputWakeKeepsInterval(t *testing.T) {
	t.Parallel()
	root, c, settled := newSettledRetirementRoot(t)
	if _, processed, err := root.ProcessPendingUserInput(context.Background(), nil); err != nil || processed {
		t.Fatalf("idle wake = processed %v, err %v; want nothing to run", processed, err)
	}
	if got := c.Snapshot().EligibleSince; !got.Equal(settled) {
		t.Fatalf("idle wake moved the settled instant from %v to %v; it is not admitted work", settled, got)
	}
}

// User steering that a wake cannot carry -- parked by a failed attempt, or held
// by a Stop -- makes the wake a no-op just as an empty queue does, so it must
// not restart the idle interval either. The steer is a real durable one that a
// wake would carry but for the block, so each case fails if its own gate goes.
func TestRetirementUncarriableSteeringWakeKeepsInterval(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		block func(t *testing.T, root *Session)
	}{
		{"parked", func(_ *testing.T, root *Session) { root.parkSteering() }},
		{"held", func(t *testing.T, root *Session) {
			if err := root.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
				snapshot.SteeringHeld = true
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, c, _ := newSettledRetirementRoot(t)
			if _, err := root.AcceptClientMutationSteer(appwire.TurnSteerParams{
				ClientMutationID: "retirement-uncarriable-steer",
				Input:            []appwire.InputItem{{Type: "text", Text: "opaque-steer"}},
			}); err != nil {
				t.Fatal(err)
			}
			if !root.hasPendingUserInputToRun() {
				t.Fatal("an accepted idle steer is not work for a wake; the block below would prove nothing")
			}
			tc.block(t, root)
			// Accepting the steer was admitted work; settle again after it.
			settled := settleRetirement(c)
			if _, processed, err := root.ProcessPendingUserInput(context.Background(), nil); err != nil || processed {
				t.Fatalf("wake = processed %v, err %v; want the steer left for the user", processed, err)
			}
			if got := c.Snapshot().EligibleSince; !got.Equal(settled) {
				t.Fatalf("wake moved the settled instant from %v to %v; it ran nothing", settled, got)
			}
		})
	}
}

// newSettledRetirementRoot returns an idle root with its client mutation store
// open, attached to a controller whose settled instant is already recorded, so
// the call under test is the only thing that can move it.
func newSettledRetirementRoot(t *testing.T) (*Session, *RetirementController, time.Time) {
	t.Helper()
	root := newQueuePersistTestSession(t, t.TempDir())
	t.Cleanup(root.Close)
	if err := root.ensureClientMutationStore(); err != nil {
		t.Fatal(err)
	}
	clk := agenttest.NewFakeClock()
	c, err := NewRetirementController(time.Hour, clk)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	return root, c, settleRetirement(c)
}

// settleRetirement records the controller's settled instant the way Run's
// evaluate does, and returns it.
func settleRetirement(c *RetirementController) time.Time {
	settled := c.clock.Now()
	c.mu.Lock()
	c.eligibleSince = settled
	c.mu.Unlock()
	return settled
}

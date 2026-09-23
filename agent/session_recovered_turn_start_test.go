package agent

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
)

// recoveredTurnSession builds the reported flow's durable state: a turn/start
// accepted into a session whose process then died before the turn was ever
// claimed. It returns the restored session (closed by t.Cleanup), the stable id
// of the turn this process inherited, and the client mutation id the dead
// prompt was accepted under.
func recoveredTurnSession(t *testing.T) (restored *Session, inheritedTurnID, deadMutationID string) {
	t.Helper()
	dir := t.TempDir()
	crashed := newQueuePersistTestSession(t, dir)
	id := crashed.ID()
	deadMutationID = "cm-dead-turn"
	started, err := crashed.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: deadMutationID,
		Input:            []appwire.InputItem{{Type: "text", Text: "the prompt that died mid-turn"}},
	})
	if err != nil {
		t.Fatalf("AcceptClientMutationStart (dead turn): %v", err)
	}
	// The process dies here: the turn/start is durably accepted and names the
	// active turn, but nothing ever ran it.
	crashed.Close()

	restored = restoreQueuePersistTestSession(t, dir, id)
	t.Cleanup(restored.Close)
	if got := restored.recoveredTurnID; got != started.Turn.ID {
		t.Fatalf("recoveredTurnID = %q, want the inherited turn %q", got, started.Turn.ID)
	}
	if got := restored.clientMutations.snapshot().ActiveTurnID; got != started.Turn.ID {
		t.Fatalf("restored ActiveTurnID = %q, want the inherited turn %q", got, started.Turn.ID)
	}
	return restored, started.Turn.ID, deadMutationID
}

// recoveredIncorporatedTurnSession builds the durable state the live mid-turn
// e2e leaves behind: the dead process had CLAIMED the start and INCORPORATED
// its user transcript entry before it was killed with the model round in
// flight. Unlike recoveredTurnSession, the inherited pending execution reads
// "incorporated" after restore and never becomes "claimed" again -- restore
// only resets a CLAIMED start to accepted for the runner to reclaim, and the
// runner's reclaim of an incorporated start leaves the incorporation mark as it
// found it.
func recoveredIncorporatedTurnSession(t *testing.T) (restored *Session, inheritedTurnID, deadMutationID string) {
	t.Helper()
	dir := t.TempDir()
	crashed := newQueuePersistTestSession(t, dir)
	id := crashed.ID()
	deadMutationID = "cm-dead-turn"
	started, err := crashed.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: deadMutationID,
		Input:            []appwire.InputItem{{Type: "text", Text: "the prompt that died mid-turn"}},
	})
	if err != nil {
		t.Fatalf("AcceptClientMutationStart (dead turn): %v", err)
	}
	claimed, ok, err := crashed.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("claim the dead turn: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
	if claimed.StableTurnID != started.Turn.ID {
		t.Fatalf("claim = %q, want the dead turn %q", claimed.StableTurnID, started.Turn.ID)
	}
	// The user input turn is written to the transcript and its store mark set
	// before the model round, so a kill during the round leaves the execution
	// incorporated.
	if err := crashed.markClaimedUserTranscriptIncorporated(deadMutationID); err != nil {
		t.Fatalf("mark the dead turn incorporated: %v", err)
	}
	crashed.Close()

	restored = restoreQueuePersistTestSession(t, dir, id)
	t.Cleanup(restored.Close)
	if got := restored.recoveredTurnID; got != started.Turn.ID {
		t.Fatalf("recoveredTurnID = %q, want the inherited turn %q", got, started.Turn.ID)
	}
	return restored, started.Turn.ID, deadMutationID
}

func isClientMutationConflict(err error) bool {
	var wire appwire.WireError
	return errors.As(err, &wire) && wire.Code == appwire.CodeConflict
}

func acceptFollowUp(t *testing.T, s *Session, id, text string) appwire.TurnStartResponse {
	t.Helper()
	response, err := s.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: id,
		Input:            []appwire.InputItem{{Type: "text", Text: text}},
	})
	if err != nil {
		t.Fatalf("turn/start behind the recovered turn was refused: %v", err)
	}
	return response
}

// TestAcceptBehindRecoveredTurnRefusedUntilTheInheritedTurnIsClaimed pins the
// narrowed admission rule. Restore resets a claimed start to "accepted" so the
// runner can reclaim it; until that reclaim happens the inherited turn is not
// running, and a follow-up admitted into the window would make two starts
// simultaneously claimable -- the race the review named. The follow-up is
// admitted the moment the inherited turn is claimed.
func TestAcceptBehindRecoveredTurnRefusedUntilTheInheritedTurnIsClaimed(t *testing.T) {
	restored, inheritedTurnID, deadMutationID := recoveredTurnSession(t)

	pending := restored.clientMutations.snapshot().PendingExecutions[deadMutationID]
	if pending.ExecutionState != "accepted" {
		t.Fatalf("inherited pending state = %q, want it merely accepted before any claim", pending.ExecutionState)
	}
	// A send refused in the pre-claim window is durably rejected under its own
	// mutation id; the live client returns the text to the composer and a later
	// send carries a fresh id, which is what the second half of this test models.
	if _, err := restored.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-refused-before-claim",
		Input:            []appwire.InputItem{{Type: "text", Text: "the follow-up"}},
	}); !isClientMutationConflict(err) {
		t.Fatalf("turn/start behind the merely-accepted inherited turn = %v, want Conflict(\"turn is already active\")", err)
	}
	if _, ok := restored.clientMutations.snapshot().PendingExecutions["cm-refused-before-claim"]; ok {
		t.Fatal("the refused follow-up was left pending")
	}

	claimed, ok, err := restored.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("claim the inherited turn: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
	if claimed.StableTurnID != inheritedTurnID {
		t.Fatalf("claim = %q, want the inherited turn %q", claimed.StableTurnID, inheritedTurnID)
	}

	response := acceptFollowUp(t, restored, "cm-follow-up", "the follow-up")
	if response.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("receipt disposition = %q, want applied", response.Receipt.Disposition)
	}
	pending, ok = restored.clientMutations.snapshot().PendingExecutions["cm-follow-up"]
	if !ok || pending.ExecutionState != "accepted" {
		t.Fatalf("follow-up pending = %#v ok=%v, want durably accepted", pending, ok)
	}
}

// TestAcceptBehindRecoveredTurnAdmittedOnceItRuns covers the durable state the
// live mid-turn e2e leaves: an INCORPORATED inherited start is already running,
// so the follow-up is admitted behind it. Requiring the literal "claimed" state
// here would refuse the caller's prompt for the whole life of the inherited
// turn, because restore never rewinds an incorporated start to accepted and the
// runner's reclaim leaves it incorporated.
func TestAcceptBehindRecoveredTurnAdmittedOnceItRuns(t *testing.T) {
	restored, inheritedTurnID, deadMutationID := recoveredIncorporatedTurnSession(t)

	pending := restored.clientMutations.snapshot().PendingExecutions[deadMutationID]
	if pending.ExecutionState != "incorporated" {
		t.Fatalf("inherited pending state = %q, want incorporated", pending.ExecutionState)
	}
	response := acceptFollowUp(t, restored, "cm-new-prompt", "the prompt sent after the crash")
	if response.Receipt.Disposition != appwire.MutationDispositionApplied {
		t.Fatalf("receipt disposition = %q, want applied", response.Receipt.Disposition)
	}
	if got := restored.clientMutations.snapshot().ActiveTurnID; got != inheritedTurnID {
		t.Fatalf("ActiveTurnID after the follow-up was admitted = %q, want the inherited running turn %q", got, inheritedTurnID)
	}
}

// TestAcceptBehindRecoveredTurnKeepsTheRunningName pins the ownership rule: an
// accepted follow-up does NOT rename the active slot. The slot names the turn
// that is RUNNING, so repointing it at an accepted-but-unrun follow-up would
// aim a Stop at the wrong turn and clear the guard the follow-up was admitted
// through. The follow-up names the slot when it is CLAIMED instead.
func TestAcceptBehindRecoveredTurnKeepsTheRunningName(t *testing.T) {
	restored, inheritedTurnID, _ := recoveredTurnSession(t)
	if _, ok, err := restored.claimClientMutationStart(); err != nil || !ok {
		t.Fatalf("claim the inherited turn: ok=%v err=%v", ok, err)
	}
	response := acceptFollowUp(t, restored, "cm-follow-up", "the follow-up")
	if response.Turn.ID == inheritedTurnID {
		t.Fatalf("the follow-up reused the inherited turn id %q", inheritedTurnID)
	}
	if got := restored.clientMutations.snapshot().ActiveTurnID; got != inheritedTurnID {
		t.Fatalf("ActiveTurnID after accepting the follow-up = %q, want the inherited running turn %q", got, inheritedTurnID)
	}
}

// TestClaimBehindRecoveredTurnTakesTheInheritedTurnFirstThenTheFollowUp pins
// FIFO claim order. Ranging over a Go map has no ordering guarantee, so the
// inherited turn and the follow-up admitted behind it were claimable in
// arbitrary order and the serve loop runs whatever it claims. The reserved turn
// sequence is the order the user spoke: the inherited turn was reserved before
// the crash, so it is claimed first and the follow-up runs after it.
func TestClaimBehindRecoveredTurnTakesTheInheritedTurnFirstThenTheFollowUp(t *testing.T) {
	restored, inheritedTurnID, _ := recoveredTurnSession(t)

	first, ok, err := restored.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("first claim: claimed=%#v ok=%v err=%v", first, ok, err)
	}
	if first.StableTurnID != inheritedTurnID {
		t.Fatalf("first claim = %q, want the inherited turn %q claimed first", first.StableTurnID, inheritedTurnID)
	}

	response := acceptFollowUp(t, restored, "cm-follow-up", "the follow-up")
	followUpTurnID := response.Turn.ID

	second, ok, err := restored.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("second claim: claimed=%#v ok=%v err=%v", second, ok, err)
	}
	if second.StableTurnID != followUpTurnID {
		t.Fatalf("second claim = %q, want the follow-up %q claimed next", second.StableTurnID, followUpTurnID)
	}
}

// TestClaimAfterRecoveredTurnReleasedNamesTheClaimedTurnActive pins the claim's
// authority over the slot when a free-slot grab races it.
//
// Accept names the slot only when it is free, so once the recovered turn
// settles and clears the slot, a NEW follow-up can be accepted into it and name
// the slot before the serve loop claims the OLDEST pending start. The claim
// must re-name the slot for the turn it is about to run, or a Stop fences the
// free-slot grab -- dropping its prompt -- while the turn actually running is
// never stopped.
func TestClaimAfterRecoveredTurnReleasedNamesTheClaimedTurnActive(t *testing.T) {
	restored, _, deadMutationID := recoveredTurnSession(t)

	// The inherited turn runs and the older follow-up is admitted behind it, so
	// it does NOT take the slot.
	if _, ok, err := restored.claimClientMutationStart(); err != nil || !ok {
		t.Fatalf("claim the inherited turn: ok=%v err=%v", ok, err)
	}
	olderResponse := acceptFollowUp(t, restored, "cm-older-follow-up", "the older follow-up")
	olderTurnID := olderResponse.Turn.ID

	// The inherited turn settles through the ordinary path, releasing the slot.
	if err := restored.markClaimedUserTranscriptIncorporated(deadMutationID); err != nil {
		t.Fatalf("mark inherited turn incorporated: %v", err)
	}
	if err := restored.completeClientMutationTurn(deadMutationID); err != nil {
		t.Fatalf("complete inherited turn: %v", err)
	}
	if got := restored.clientMutations.snapshot().ActiveTurnID; got != "" {
		t.Fatalf("ActiveTurnID after the inherited turn settled = %q, want the slot released", got)
	}

	// The newer follow-up is accepted into the now-free slot and the accept-side
	// rule names it.
	newerResponse := acceptFollowUp(t, restored, "cm-newer-follow-up", "the newer follow-up")
	newerTurnID := newerResponse.Turn.ID
	if got := restored.clientMutations.snapshot().ActiveTurnID; got != newerTurnID {
		t.Fatalf("ActiveTurnID after the free-slot grab = %q, want %q", got, newerTurnID)
	}

	// The claim takes the OLDEST pending start and is authoritative about the
	// slot: it re-names it for the turn about to run.
	claimed, ok, err := restored.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("claim the older follow-up: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
	if claimed.StableTurnID != olderTurnID {
		t.Fatalf("claim = %q, want the OLDEST pending start %q", claimed.StableTurnID, olderTurnID)
	}
	if got := restored.clientMutations.snapshot().ActiveTurnID; got != olderTurnID {
		t.Fatalf("ActiveTurnID after the claim = %q, want the claimed turn %q (the free-slot grab named %q)",
			got, olderTurnID, newerTurnID)
	}

	// A Stop now fences the turn that is actually running.
	interrupt, err := restored.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "cm-stop",
	}, func() {})
	if err != nil {
		t.Fatalf("interrupt after the claim: %v", err)
	}
	if interrupt.Receipt.TurnID != olderTurnID {
		t.Fatalf("Stop fenced %q, want the claimed running turn %q", interrupt.Receipt.TurnID, olderTurnID)
	}
	snapshot := restored.clientMutations.snapshot()
	if record := snapshot.Journal["cm-older-follow-up"]; record.ExecutionState != "interrupted" {
		t.Fatalf("older follow-up after the Stop = %#v, want it interrupted", record)
	}
	if newer, ok := snapshot.PendingExecutions["cm-newer-follow-up"]; !ok || newer.ExecutionState != "accepted" {
		t.Fatalf("newer follow-up after the Stop = %#v ok=%v, want it still accepted", newer, ok)
	}
}

// TestInterruptWhileRecoveredTurnRunsFencesTheInheritedTurn pins the Stop
// target: the accepted follow-up must not steal the Stop from the running
// inherited turn. The interrupt names no turn; it fences whatever the durable
// slot names. If accepting the follow-up renamed the slot, a Stop would fence
// the follow-up -- cancelling the running turn while marking the user's
// brand-new pending start interrupted and clearing the slot, silently dropping
// the prompt they just sent.
func TestInterruptWhileRecoveredTurnRunsFencesTheInheritedTurn(t *testing.T) {
	restored, inheritedTurnID, _ := recoveredTurnSession(t)

	if _, ok, err := restored.claimClientMutationStart(); err != nil || !ok {
		t.Fatalf("claim the inherited turn: ok=%v err=%v", ok, err)
	}
	followUpResponse := acceptFollowUp(t, restored, "cm-follow-up", "the follow-up")
	followUpTurnID := followUpResponse.Turn.ID

	cancels := 0
	interrupt, err := restored.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "cm-stop",
	}, func() { cancels++ })
	if err != nil {
		t.Fatalf("interrupt while the inherited turn runs: %v", err)
	}
	if cancels != 1 {
		t.Fatalf("interrupt cancelled %d times, want 1", cancels)
	}
	if interrupt.Receipt.TurnID != inheritedTurnID {
		t.Fatalf("Stop fenced %q, want the inherited running turn %q (the follow-up is %q)",
			interrupt.Receipt.TurnID, inheritedTurnID, followUpTurnID)
	}
	snapshot := restored.clientMutations.snapshot()
	if record := snapshot.Journal["cm-dead-turn"]; record.ExecutionState != "interrupted" {
		t.Fatalf("inherited turn record after the Stop = %#v, want it interrupted", record)
	}
	if _, still := snapshot.PendingExecutions["cm-dead-turn"]; still {
		t.Fatal("the interrupted inherited turn remained a pending execution")
	}
	followUp, ok := snapshot.PendingExecutions["cm-follow-up"]
	if !ok || followUp.ExecutionState != "accepted" {
		t.Fatalf("follow-up pending after the Stop = %#v ok=%v, want it still accepted", followUp, ok)
	}
	if record := snapshot.Journal["cm-follow-up"]; record.ExecutionState == "interrupted" ||
		record.OperationState == clientMutationOperationTerminal {
		t.Fatalf("the Stop retired the follow-up's pending start: %#v", record)
	}
}

// TestClaimDuringInterruptFenceLeavesTheFollowUpPending is the race the review
// found: a Stop that lands between the claim's armed callback and the claim
// itself.
//
// An interrupt fence names the turn a Stop is ending and is finalized against
// that turn alone. A start claim that slips in behind the fence would take the
// follow-up admitted behind the fenced turn: the Stop's cancellation unwinds
// the fenced turn, finalization never reaches the follow-up, and the claimed
// start is left with no runner to run it and no interrupt to release it. The
// claim refuses while the fence exists, and the follow-up stays pending until
// the fence is finalized -- and is claimable the moment it clears.
func TestClaimDuringInterruptFenceLeavesTheFollowUpPending(t *testing.T) {
	restored, inheritedTurnID, _ := recoveredTurnSession(t)

	if _, ok, err := restored.claimClientMutationStart(); err != nil || !ok {
		t.Fatalf("claim the inherited turn: ok=%v err=%v", ok, err)
	}
	followUpResponse := acceptFollowUp(t, restored, "cm-follow-up", "the follow-up")
	followUpTurnID := followUpResponse.Turn.ID

	// The Stop races this claim. Its fence is durable -- set by the accept below
	// -- while the claim is already in flight, and it finalizes only after the
	// claim has run. cancelAndWait is what holds it open, and the hook is the
	// claim's OWN armed callback, so the fence is provably set in the window
	// between that callback and the claim's serializer.
	fenceSet := make(chan struct{})
	releaseCancel := make(chan struct{})
	stopDone := make(chan error, 1)
	restored.cfg.testOnly.clientMutationStartClaiming = func() {
		restored.cfg.testOnly.clientMutationStartClaiming = nil
		go func() {
			_, stopErr := restored.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
				ClientMutationID: "cm-stop",
			}, func() {
				close(fenceSet)
				<-releaseCancel
			})
			stopDone <- stopErr
		}()
		<-fenceSet
	}

	claimed, ok, err := restored.claimClientMutationStart()
	if err != nil {
		t.Fatalf("claim during the interrupt fence: %v", err)
	}
	if ok {
		t.Fatalf("claim during the interrupt fence claimed %q, want nothing claimed", claimed.StableTurnID)
	}

	snapshot := restored.clientMutations.snapshot()
	if snapshot.InterruptFence == nil {
		t.Fatal("the fence was not set when the claim ran; the race was not reached")
	}
	followUp, ok := snapshot.PendingExecutions["cm-follow-up"]
	if !ok || followUp.ExecutionState != "accepted" {
		t.Fatalf("follow-up during the fence = %#v ok=%v, want it still accepted and unclaimed", followUp, ok)
	}
	if got := snapshot.ActiveTurnID; got != inheritedTurnID {
		t.Fatalf("ActiveTurnID during the fence = %q, want the fenced turn %q still running", got, inheritedTurnID)
	}

	// The Stop finalizes. It retires the fenced turn alone, so the follow-up is
	// still there, still accepted, and claimable the moment the fence is gone.
	close(releaseCancel)
	if err := <-stopDone; err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	snapshot = restored.clientMutations.snapshot()
	if snapshot.InterruptFence != nil {
		t.Fatalf("fence after the Stop finalized = %#v, want nil", snapshot.InterruptFence)
	}
	followUp, ok = snapshot.PendingExecutions["cm-follow-up"]
	if !ok || followUp.ExecutionState != "accepted" {
		t.Fatalf("follow-up after the fence finalized = %#v ok=%v, want it still accepted", followUp, ok)
	}

	claimed, ok, err = restored.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("claim after the fence finalized: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
	if claimed.StableTurnID != followUpTurnID {
		t.Fatalf("claim after the fence finalized = %q, want the follow-up %q", claimed.StableTurnID, followUpTurnID)
	}
	if got := restored.clientMutations.snapshot().ActiveTurnID; got != followUpTurnID {
		t.Fatalf("ActiveTurnID after the follow-up is claimed = %q, want %q", got, followUpTurnID)
	}
}

// TestClearedInterruptFenceWakesThePendingFollowUp pins the fix for the
// stranding the fence guard trades for. The claim path refuses every start
// while a fence exists, so a follow-up admitted behind the recovered turn waits
// for the fence to clear -- and nothing else wakes the start path: the Stop
// parked the queue and the steering wake only serves steering. Without an
// explicit start wake the follow-up is accepted and never runs.
func TestClearedInterruptFenceWakesThePendingFollowUp(t *testing.T) {
	restored, _, _ := recoveredTurnSession(t)

	if _, ok, err := restored.claimClientMutationStart(); err != nil || !ok {
		t.Fatalf("claim the inherited turn: ok=%v err=%v", ok, err)
	}
	followUpResponse := acceptFollowUp(t, restored, "cm-follow-up", "the follow-up")
	followUpTurnID := followUpResponse.Turn.ID

	woke := make(chan struct{}, 8)
	restored.SetClientMutationStartWakeFunc(func() {
		select {
		case woke <- struct{}{}:
		default:
		}
	})
	// Installing the wake fires it once if a start is already runnable; drain
	// that so the signal below can only be the fence clearing.
drain:
	for {
		select {
		case <-woke:
		default:
			break drain
		}
	}

	if _, err := restored.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{
		ClientMutationID: "cm-stop",
	}, func() {}); err != nil {
		t.Fatalf("interrupt while the follow-up waits: %v", err)
	}

	select {
	case <-woke:
	default:
		t.Fatal("clearing the interrupt fence did not wake the pending follow-up start")
	}

	claimed, ok, err := restored.claimClientMutationStart()
	if err != nil || !ok {
		t.Fatalf("claim after the fence cleared: claimed=%#v ok=%v err=%v", claimed, ok, err)
	}
	if claimed.StableTurnID != followUpTurnID {
		t.Fatalf("claim after the fence cleared = %q, want the follow-up %q", claimed.StableTurnID, followUpTurnID)
	}
}

// TestInterruptFenceHoldsTheFollowUpOutOfTheRunnableCheck pins the runnable
// predicate to the claim guard it feeds. The claim path refuses EVERY start
// while an interrupt fence exists, so the runnable check must report the same
// thing. The old check skipped only a pending start whose turn id equalled the
// fence's ExpectedTurnID -- the fenced turn -- so a follow-up admitted behind
// the running recovered turn still read as runnable: ProcessClientMutationStart
// armed cancellation for it, the claim then refused it, and the runner was
// cleared, a spurious arm/clear on every wake. hasRunnableClientMutationStart
// also feeds sessionWorkPending and WireState, so the session read as processing
// while the fence blocked its only pending work.
//
// The fence here names the RUNNING inherited turn, which is exactly the case the
// equality-only check let through: the follow-up's id differs from
// ExpectedTurnID, so it was reported runnable under the fence.
func TestInterruptFenceHoldsTheFollowUpOutOfTheRunnableCheck(t *testing.T) {
	restored, inheritedTurnID, _ := recoveredTurnSession(t)

	if _, ok, err := restored.claimClientMutationStart(); err != nil || !ok {
		t.Fatalf("claim the inherited turn: ok=%v err=%v", ok, err)
	}
	followUpResponse := acceptFollowUp(t, restored, "cm-follow-up", "the follow-up")
	followUpTurnID := followUpResponse.Turn.ID

	if id, runnable := restored.runnableClientMutationStartTurnID(); !runnable || id != followUpTurnID {
		t.Fatalf("before the fence: runnable=(%q,%v), want the follow-up %q runnable", id, runnable, followUpTurnID)
	}

	// A Stop fences the inherited turn, which is still running. The follow-up is
	// a different turn, so the fence does not name it.
	if err := restored.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.InterruptFence = &clientMutationInterruptFence{
			ClientMutationID: "cm-stop",
			ExpectedTurnID:   inheritedTurnID,
		}
		return nil
	}); err != nil {
		t.Fatalf("arm interrupt fence: %v", err)
	}

	if id, runnable := restored.runnableClientMutationStartTurnID(); runnable {
		t.Fatalf("under the fence: runnable=(%q,%v), want no start runnable while the fence exists", id, runnable)
	}
	if restored.hasRunnableClientMutationStart() {
		t.Fatal("hasRunnableClientMutationStart reported work while the fence blocks the only pending start")
	}

	disarmTestInterruptFence(t, restored)

	if id, runnable := restored.runnableClientMutationStartTurnID(); !runnable || id != followUpTurnID {
		t.Fatalf("after the fence cleared: runnable=(%q,%v), want the follow-up %q runnable", id, runnable, followUpTurnID)
	}
	if !restored.hasRunnableClientMutationStart() {
		t.Fatal("hasRunnableClientMutationStart did not report the follow-up runnable after the fence cleared")
	}
}

// TestAcceptBehindProcessLocalTurnStillRefused pins the other half of the rule:
// a turn the session started in THIS process is not inherited work, so a
// turn/start while it is active keeps today's refusal.
func TestAcceptBehindProcessLocalTurnStillRefused(t *testing.T) {
	s := newTestSession(t)
	if _, err := s.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-live-turn",
		Input:            []appwire.InputItem{{Type: "text", Text: "the turn this process started"}},
	}); err != nil {
		t.Fatalf("AcceptClientMutationStart: %v", err)
	}
	if s.recoveredTurnID != "" {
		t.Fatalf("recoveredTurnID = %q, want empty for a process-local turn", s.recoveredTurnID)
	}
	if _, err := s.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-refused",
		Input:            []appwire.InputItem{{Type: "text", Text: "a prompt while a live turn runs"}},
	}); !isClientMutationConflict(err) {
		t.Fatalf("turn/start during a process-local turn = %v, want Conflict(\"turn is already active\")", err)
	}
}

// TestQueueOriginInheritedTurnLeavesNoRecoveredTurn pins the queue-origin
// exception. A queued message that was mid-run at the crash also leaves
// ActiveTurnID set, but it is not a recovered USER turn: the claim path prefers
// starts, so a follow-up start admitted behind it would never be claimed. It
// leaves recoveredTurnID empty and the follow-up stays refused, the pre-fix
// behaviour.
func TestQueueOriginInheritedTurnLeavesNoRecoveredTurn(t *testing.T) {
	dir := t.TempDir()
	crashed := newQueuePersistTestSession(t, dir)
	id := crashed.ID()
	const queueMutationID = "cm-queued-turn"

	if _, err := crashed.AcceptClientMutationQueue(appwire.TurnQueueParams{
		ClientMutationID: queueMutationID,
		Input:            []appwire.InputItem{{Type: "text", Text: "a queued message that died mid-run"}},
	}); err != nil {
		t.Fatalf("turn/queue: %v", err)
	}
	queued := crashed.popQueueHead()
	if queued.ClientMutationID != queueMutationID {
		t.Fatalf("popQueueHead = %#v, want the queued message %q", queued, queueMutationID)
	}
	queueTurnID := queued.StableTurnID
	if queueTurnID == "" {
		t.Fatal("the claimed queued message has no stable turn id")
	}
	// Its user turn was incorporated before the process died.
	if err := crashed.markClaimedUserTranscriptIncorporated(queueMutationID); err != nil {
		t.Fatalf("mark the queued turn incorporated: %v", err)
	}
	crashed.Close()

	restored := restoreQueuePersistTestSession(t, dir, id)
	t.Cleanup(restored.Close)
	if got := restored.recoveredTurnID; got != "" {
		t.Fatalf("recoveredTurnID = %q, want empty for a queue-origin inherited turn", got)
	}
	if got := restored.clientMutations.snapshot().ActiveTurnID; got != queueTurnID {
		t.Fatalf("restored ActiveTurnID = %q, want the inherited queue turn %q", got, queueTurnID)
	}
	if _, err := restored.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID: "cm-follow-up",
		Input:            []appwire.InputItem{{Type: "text", Text: "the follow-up"}},
	}); !isClientMutationConflict(err) {
		t.Fatalf("turn/start behind an inherited queue turn = %v, want Conflict(\"turn is already active\")", err)
	}
}

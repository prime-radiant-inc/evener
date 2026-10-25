package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/internal/contextmgr"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

func TestDelegateAttention_LaunchClaimDoesNotObserve(t *testing.T) {
	const (
		sessionID = "child-dlg_target"
		firstID   = "delegate:delivery-attention-1"
		secondID  = "delegate:delivery-attention-2"
	)
	c, runtime, path := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, firstID, secondID)
	beforeRevision := c.durable["dlg_target"].ProjectionRevision
	reservation, err := c.ReserveAttention(runtime, firstID)
	if err != nil {
		t.Fatalf("ReserveAttention: %v", err)
	}

	if err := runtime.acceptDelegateAttention(reservation); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	blockedAggregate := c.durable["dlg_target"]
	blockedGeneration := blockedAggregate.Generation
	blockedNeedsAttention := blockedAggregate.NeedsAttention
	_, firstStillUnresolved := attentionWakeIDsOf(c, "dlg_target")[firstID]
	c.mu.Unlock()
	if blockedGeneration != 0 || !blockedNeedsAttention || !firstStillUnresolved {
		t.Fatalf("acceptance published before start commit: generation=%d needs=%t unresolved=%#v", blockedGeneration, blockedNeedsAttention, attentionWakeIDsOf(c, "dlg_target"))
	}
	if _, err := c.ReserveAttention(runtime, secondID); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("second reservation bypassed first claim: %v", err)
	}
	first, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("commit first attention: %v", err)
	}
	if runtime.attentionAcceptedID != firstID {
		t.Fatalf("claimed trigger = %q, want %q", runtime.attentionAcceptedID, firstID)
	}
	aggregate := c.durable["dlg_target"]
	if aggregate.Generation != 1 || aggregate.Trigger != delegatestore.TriggerAttention || !aggregate.CurrentRunOpen || !aggregate.NeedsAttention || aggregate.ProjectionRevision != beforeRevision+1 {
		t.Fatalf("nonfinal acceptance aggregate = %#v, want generation 1 open with unchanged true attention", aggregate)
	}
	if got := attentionWakeIDsOf(c, "dlg_target"); !reflect.DeepEqual(got, map[string]struct{}{secondID: {}}) {
		t.Fatalf("unclaimed wake IDs after first launch = %#v", got)
	}
	fold, err := readDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("read attention fold: %v", err)
	}
	if got := fold.pendingIDs(); !reflect.DeepEqual(got, []string{firstID, secondID}) || len(fold.resolutions) != 0 {
		t.Fatalf("launch changed model observation: pending=%v resolutions=%v", got, fold.resolutions)
	}
	if _, err := c.FinishGeneration(first.lease, delegateFinish{outcome: delegatestore.OutcomeCompleted, reason: "first handled"}); err != nil {
		t.Fatalf("FinishGeneration first: %v", err)
	}
	wakeDone := make(chan struct{}, 1)
	runtime.SetNotifyFunc(func() { wakeDone <- struct{}{} })
	// Registration dispatches existing pending content synchronously. Consume
	// that distinct event before finalization is allowed to emit its own wake.
	notificationAwait(t, wakeDone)
	reportFinalizeTailDone(t, c, first.lease, runtime)
	// Finalization rechecks the real pending IDs asynchronously. Wait until that
	// wake has published them before claiming the second ID or inspecting maps.
	notificationAwait(t, wakeDone)
	secondReservation, err := c.ReserveAttention(runtime, secondID)
	if err != nil {
		t.Fatalf("ReserveAttention second: %v", err)
	}
	if err := runtime.acceptDelegateAttention(secondReservation); err != nil {
		t.Fatalf("accept final attention: %v", err)
	}
	second, err := c.CommitStart(secondReservation)
	if err != nil {
		t.Fatalf("commit final attention: %v", err)
	}
	c.mu.Lock()
	finalAggregate := *c.durable["dlg_target"]
	finalWakeIDs := maps.Clone(attentionWakeIDsOf(c, "dlg_target"))
	c.mu.Unlock()
	if finalAggregate.Generation != 2 || !finalAggregate.NeedsAttention || !reflect.DeepEqual(finalWakeIDs, map[string]struct{}{firstID: {}}) {
		t.Fatalf("final acceptance aggregate=%#v unresolved=%#v", finalAggregate, finalWakeIDs)
	}
	fold, err = readDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("read final attention fold: %v", err)
	}
	if got := fold.pendingIDs(); !reflect.DeepEqual(got, []string{firstID, secondID}) || len(fold.resolutions) != 0 {
		t.Fatalf("second launch changed model observation: pending=%v resolutions=%v", got, fold.resolutions)
	}
	events, err := c.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	last := events[len(events)-1]
	if last.Kind != delegatestore.EventDelegateRunStarted || last.RunStarted.Generation != second.lease.generation {
		t.Fatalf("second launch journal tail = %#v", last)
	}
}

// TestReadExistingDelegateAttentionFoldMemoizesAndInvalidatesOnAppend pins the
// read-path fold memo: a stable transcript is folded once and then served
// from the memo, the memo is keyed to the expected session id, and an append
// (the only change an append-only transcript can have) recomputes so the fold
// reflects the new bytes. The status sweeps the hub runs per thread/read fold
// every eligible delegate child, so this memo is what keeps a delegate-heavy
// past session's click cost off the child transcripts' total size.
func TestReadExistingDelegateAttentionFoldMemoizesAndInvalidatesOnAppend(t *testing.T) {
	const (
		sessionID   = "child-memo-fold"
		attentionID = "delegate:delivery-memo-fold"
	)
	stateDir := t.TempDir()
	path := transcriptPath(stateDir, sessionID)
	now := time.Unix(1700000000, 0).UTC()
	writer, err := transcript.NewWriter(path, transcript.Header{
		SessionID: sessionID, CreatedAt: now, ProfileID: "openai", Model: "gpt-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	steering := schema.NewTurn(schema.TurnSteering, llm.User("memoized attention"))
	steering.AttentionID = attentionID
	if err := writer.AppendDurable(steering); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	compute := readExistingDelegateAttentionFoldCompute
	calls := 0
	readExistingDelegateAttentionFoldCompute = func(path, sessionID string) (delegateAttentionFold, int64, error) {
		calls++
		return compute(path, sessionID)
	}
	t.Cleanup(func() { readExistingDelegateAttentionFoldCompute = compute })

	fold, err := readExistingDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if got := fold.pendingIDs(); !reflect.DeepEqual(got, []string{attentionID}) {
		t.Fatalf("first read pending = %v, want [%s]", got, attentionID)
	}
	if calls != 1 {
		t.Fatalf("first read computed %d times, want 1", calls)
	}

	again, err := readExistingDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("memoized read: %v", err)
	}
	if got := again.pendingIDs(); !reflect.DeepEqual(got, []string{attentionID}) {
		t.Fatalf("memoized read pending = %v, want [%s]", got, attentionID)
	}
	if calls != 1 {
		t.Fatalf("memoized read recomputed (calls=%d, want 1): the fold was not served from the memo", calls)
	}

	// The memo is keyed to the expected session id as well as the path: another
	// session id must recompute (and then fail the transcript's header check).
	if _, err := readExistingDelegateAttentionFold(path, "child-other"); err == nil {
		t.Fatal("read with a foreign session id unexpectedly succeeded")
	}
	if calls != 2 {
		t.Fatalf("foreign-session read computed %d times, want 2", calls)
	}

	// An append changes the file identity: the memo invalidates and the fold
	// reflects the appended resolution.
	appender, _, err := transcript.OpenWriterForSession(path, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	resolution := schema.NewTurn(schema.TurnAttentionResolution, llm.User(""))
	resolution.AttentionResolution = &schema.AttentionResolutionInfo{
		AttentionID: attentionID, Disposition: string(delegateAttentionConsumed),
	}
	if err := appender.AppendDurable(resolution); err != nil {
		t.Fatal(err)
	}
	if err := appender.Close(); err != nil {
		t.Fatal(err)
	}
	resolved, err := readExistingDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("post-append read: %v", err)
	}
	if got := resolved.pendingIDs(); len(got) != 0 {
		t.Fatalf("post-append pending = %v, want none", got)
	}
	if calls != 3 {
		t.Fatalf("post-append read computed %d times, want 3", calls)
	}
}

// TestReadExistingDelegateAttentionFoldRacingAppendStaysVisible pins the
// ordering invariant behind the memo's consumed-through offset: the offset
// must never claim bytes the fold did not read. The compute seam folds the
// file as it stands and then lands an entry after the fold's EOF, before the
// reader's size check — if that check stats the file after the fold, the
// memo swallows the entry and serves the turn-less fold as an
// unchanged-transcript hit indefinitely; statting before the fold makes the
// next read see growth and refold.
func TestReadExistingDelegateAttentionFoldRacingAppendStaysVisible(t *testing.T) {
	const (
		sessionID   = "child-memo-fold-race"
		attentionID = "delegate:delivery-fold-race"
		racingID    = "delegate:delivery-fold-race-late"
	)
	stateDir := t.TempDir()
	path := transcriptPath(stateDir, sessionID)
	now := time.Unix(1700000000, 0).UTC()
	writer, err := transcript.NewWriter(path, transcript.Header{
		SessionID: sessionID, CreatedAt: now, ProfileID: "openai", Model: "gpt-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	steering := schema.NewTurn(schema.TurnSteering, llm.User("raced attention"))
	steering.AttentionID = attentionID
	if err := writer.AppendDurable(steering); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	// The compute seam folds the file as it stands, then an entry lands after
	// the fold's EOF but before the reader's size check.
	compute := readExistingDelegateAttentionFoldCompute
	readExistingDelegateAttentionFoldCompute = func(path, sessionID string) (delegateAttentionFold, int64, error) {
		fold, consumed, err := compute(path, sessionID)
		if err != nil {
			return delegateAttentionFold{}, 0, err
		}
		appender, _, err := transcript.OpenWriterForSession(path, sessionID)
		if err != nil {
			return delegateAttentionFold{}, 0, err
		}
		racing := schema.NewTurn(schema.TurnSteering, llm.User("attention that landed during the read"))
		racing.AttentionID = racingID
		if err := appender.AppendDurable(racing); err != nil {
			return delegateAttentionFold{}, 0, err
		}
		if err := appender.Close(); err != nil {
			return delegateAttentionFold{}, 0, err
		}
		return fold, consumed, nil
	}
	t.Cleanup(func() { readExistingDelegateAttentionFoldCompute = compute })

	fold, err := readExistingDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("racing read: %v", err)
	}
	if got := fold.pendingIDs(); !reflect.DeepEqual(got, []string{attentionID}) {
		t.Fatalf("racing read pending = %v, want [%s]: the seam folds the pre-append bytes", got, attentionID)
	}
	// The race window is closed: whatever the next read does, it must not
	// re-run the seam.
	readExistingDelegateAttentionFoldCompute = compute

	// The next read must see the entry that landed during the first fold: a
	// memo that claimed the appended bytes without reading them would serve
	// the turn-less fold as an unchanged-transcript hit indefinitely.
	after, err := readExistingDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("post-race read: %v", err)
	}
	if got := after.pendingIDs(); !reflect.DeepEqual(got, []string{attentionID, racingID}) {
		t.Fatalf("post-race pending = %v, want [%s %s]: the entry that raced the fold was swallowed by the memo", got, attentionID, racingID)
	}
}

// TestReadExistingDelegateAttentionFoldRemovalRacingTheFoldErrors pins the
// strict side of the same boundary: readDelegateAttentionFold is
// missing-as-empty for historical callers, so when a transcript is removed
// between the reader's size stat and the fold's open, the compute returns a
// successful empty fold. A fresh fold served without a post-read stat would
// read as "no pending attention" for a transcript that no longer exists —
// the strict boundary must error instead.
func TestReadExistingDelegateAttentionFoldRemovalRacingTheFoldErrors(t *testing.T) {
	const sessionID = "child-memo-fold-removal"
	stateDir := t.TempDir()
	path := transcriptPath(stateDir, sessionID)
	now := time.Unix(1700000000, 0).UTC()
	writer, err := transcript.NewWriter(path, transcript.Header{
		SessionID: sessionID, CreatedAt: now, ProfileID: "openai", Model: "gpt-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	steering := schema.NewTurn(schema.TurnSteering, llm.User("attention on a transcript that will vanish"))
	steering.AttentionID = "delegate:delivery-fold-removal"
	if err := writer.AppendDurable(steering); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	// The compute seam removes the transcript inside the fold's own window —
	// after the reader's size stat, before the fold's open — and then runs
	// the real reader, which returns missing-as-empty.
	compute := readExistingDelegateAttentionFoldCompute
	readExistingDelegateAttentionFoldCompute = func(path, sessionID string) (delegateAttentionFold, int64, error) {
		if err := os.Remove(path); err != nil {
			return delegateAttentionFold{}, 0, err
		}
		return compute(path, sessionID)
	}
	t.Cleanup(func() { readExistingDelegateAttentionFoldCompute = compute })

	if _, err := readExistingDelegateAttentionFold(path, sessionID); err == nil {
		t.Fatal("a removal racing the fold read as an empty fold instead of erroring")
	}
}

// TestReadExistingDelegateAttentionFoldRepairRacingTheFoldSucceeds pins the
// reader's side of a repair racing the fold: a crashed writer leaves a torn
// trailing line, and recovery truncates it — possibly while a status sweep is
// folding the same transcript. The fold reads the repaired content, so the
// read must serve that fold; reporting an offset the repair invalidated
// (a size stat'd before the read) instead makes the next foldcache tail probe
// reach past the truncated file and fails the read.
func TestReadExistingDelegateAttentionFoldRepairRacingTheFoldSucceeds(t *testing.T) {
	const (
		sessionID   = "child-memo-fold-repair"
		attentionID = "delegate:delivery-fold-repair"
	)
	stateDir := t.TempDir()
	path := transcriptPath(stateDir, sessionID)
	now := time.Unix(1700000000, 0).UTC()
	writer, err := transcript.NewWriter(path, transcript.Header{
		SessionID: sessionID, CreatedAt: now, ProfileID: "openai", Model: "gpt-5",
	})
	if err != nil {
		t.Fatal(err)
	}
	steering := schema.NewTurn(schema.TurnSteering, llm.User("attention under a racing repair"))
	steering.AttentionID = attentionID
	if err := writer.AppendDurable(steering); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	repaired, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// A crashed writer's torn trailing line — bytes with no terminating
	// newline — extends the file past what recovery will keep.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"turn":{"kind":"steering","truncated`); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// The compute seam runs the repair inside the read's window: the torn tail
	// is truncated away before the fold opens the file.
	compute := readExistingDelegateAttentionFoldCompute
	readExistingDelegateAttentionFoldCompute = func(path, sessionID string) (delegateAttentionFold, int64, error) {
		if err := os.Truncate(path, repaired.Size()); err != nil {
			return delegateAttentionFold{}, 0, err
		}
		return compute(path, sessionID)
	}
	t.Cleanup(func() { readExistingDelegateAttentionFoldCompute = compute })

	fold, err := readExistingDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("read racing a repair: %v", err)
	}
	if got := fold.pendingIDs(); !reflect.DeepEqual(got, []string{attentionID}) {
		t.Fatalf("read racing a repair pending = %v, want [%s]", got, attentionID)
	}
	// The next read, over the repaired file and the real compute, must also
	// succeed: the repair is done, the transcript is whole again.
	readExistingDelegateAttentionFoldCompute = compute
	after, err := readExistingDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("read after the repair: %v", err)
	}
	if got := after.pendingIDs(); !reflect.DeepEqual(got, []string{attentionID}) {
		t.Fatalf("read after the repair pending = %v, want [%s]", got, attentionID)
	}
}

func TestDelegateAttention_StopLeavesBoundAttentionForDiscard(t *testing.T) {
	t.Run("stop wins before acceptance", func(t *testing.T) {
		const (
			sessionID   = "child-dlg_target"
			attentionID = "delegate:delivery-stopped"
		)
		c, runtime, path := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, attentionID)
		if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target"); err != nil {
			t.Fatalf("StopSubtree: %v", err)
		}
		if _, err := c.ReserveAttention(runtime, attentionID); !errors.Is(err, errDelegateTargetBusy) {
			t.Fatalf("ReserveAttention after stop = %v, want target busy", err)
		}
		fold, err := readDelegateAttentionFold(path, sessionID)
		if err != nil {
			t.Fatalf("read attention fold: %v", err)
		}
		if pending := fold.pendingIDs(); !reflect.DeepEqual(pending, []string{attentionID}) {
			t.Fatalf("pending attention for stop discard = %#v", pending)
		}
		if c.durable["dlg_target"].NeedsAttention {
			t.Fatal("lifecycle stop failed to fold attention false")
		}
		events, err := c.store.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if events[len(events)-1].Kind != delegatestore.EventDelegateSubtreeStopRequested {
			t.Fatalf("lifecycle clear appended a separate attention event: %#v", events[len(events)-2:])
		}
	})

	t.Run("stop waits after acceptance admission", func(t *testing.T) {
		const (
			sessionID   = "child-dlg_target"
			attentionID = "delegate:delivery-admitted"
		)
		c, runtime, path := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, attentionID)
		reservation, err := c.ReserveAttention(runtime, attentionID)
		if err != nil {
			t.Fatalf("ReserveAttention: %v", err)
		}
		if err := runtime.acceptDelegateAttention(reservation); err != nil {
			t.Fatal(err)
		}
		stopped := make(chan error, 1)
		go func() {
			_, _, _, stopErr := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
			stopped <- stopErr
		}()
		select {
		case err := <-stopped:
			t.Fatalf("stop returned before admitted acceptance committed: %v", err)
		default:
		}
		if _, err := c.CommitStart(reservation); err != nil {
			t.Fatalf("commit admitted attention: %v", err)
		}
		if err := <-stopped; err != nil {
			t.Fatalf("StopSubtree after acceptance: %v", err)
		}
		fold, err := readDelegateAttentionFold(path, sessionID)
		if err != nil {
			t.Fatalf("read admitted attention fold: %v", err)
		}
		if got := fold.pendingIDs(); !reflect.DeepEqual(got, []string{attentionID}) || len(fold.resolutions) != 0 {
			t.Fatalf("admitted launch consumed unobserved notification: pending=%v resolutions=%v", got, fold.resolutions)
		}
		events, err := c.store.Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if events[len(events)-2].Kind != delegatestore.EventDelegateRunStarted || events[len(events)-1].Kind != delegatestore.EventDelegateSubtreeStopRequested {
			t.Fatalf("acceptance/stop ordering = %#v", events[len(events)-2:])
		}
	})
}

func newDelegateAttentionAcceptanceHarness(t *testing.T, sessionID string, fs afero.Fs, attentionIDs ...string) (*delegateTreeController, *Session, string) {
	t.Helper()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	path := transcriptPath(c.stateDir, sessionID)
	var writer *transcript.Writer
	var err error
	if fs == nil {
		writer, err = transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
	} else {
		writer, err = transcript.NewWriterWithFS(fs, path, transcript.Header{SessionID: sessionID})
	}
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	for _, attentionID := range attentionIDs {
		attention := schema.NewTurn(schema.TurnSteering, llm.User("durable attention "+attentionID))
		attention.AttentionID = attentionID
		if err := writer.AppendDurable(attention); err != nil {
			t.Fatalf("append attention %q: %v", attentionID, err)
		}
	}
	runtime := &Session{id: sessionID, stateDir: c.stateDir, delegateController: c, owningDelegateID: "dlg_target"}
	runtime.attachTranscript(writer)
	c.mu.Lock()
	if _, err := c.appendLocked(delegatestore.Event{
		Kind:       delegatestore.EventDelegateAttentionChanged,
		DelegateID: "dlg_target",
		AttentionChanged: &delegatestore.DelegateAttentionChanged{
			NeedsAttention: true,
		},
	}); err != nil {
		c.mu.Unlock()
		t.Fatalf("append attention projection: %v", err)
	}
	c.live["dlg_target"] = &delegateLiveState{runtime: runtime}
	for _, attentionID := range attentionIDs {
		c.noteDelegateAttentionLocked("dlg_target", attentionID)
	}
	c.mu.Unlock()
	return c, runtime, path
}

func attachDelegateAttentionTranscriptForTest(t *testing.T, c *delegateTreeController, runtime *Session, delegateID, attentionID string) {
	t.Helper()
	c.mu.Lock()
	aggregate := c.durable[delegateID]
	c.mu.Unlock()
	if aggregate == nil {
		t.Fatalf("delegate %q is absent", delegateID)
	}
	sessionID := aggregate.Descriptor.ChildSessionID
	path := transcriptPath(c.stateDir, sessionID)
	writer := runtime.attachedTranscript()
	if writer == nil {
		var err error
		writer, err = transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
		if err != nil {
			t.Fatalf("create attention transcript: %v", err)
		}
		t.Cleanup(func() { _ = writer.Close() })
		runtime.id = sessionID
		runtime.stateDir = c.stateDir
		runtime.delegateController = c
		runtime.owningDelegateID = delegateID
		runtime.attachTranscript(writer)
	} else if runtime.id != sessionID || runtime.stateDir != c.stateDir || runtime.delegateController != c || runtime.owningDelegateID != delegateID {
		t.Fatal("attention transcript is bound to a different receiver")
	}
	if attentionID == "" {
		return
	}
	turn := schema.NewTurn(schema.TurnSteering, llm.User("attention "+attentionID))
	turn.AttentionID = attentionID
	if err := writer.AppendDurable(turn); err != nil {
		t.Fatalf("append attention: %v", err)
	}
	c.noteDelegateAttention(delegateID, attentionID)
}

func TestDelegateAttention_LaunchClaimRetryLeavesGenerationStoppable(t *testing.T) {
	const (
		sessionID   = "child-dlg_target"
		attentionID = "attention-resolution-failure"
	)
	c, runtime, path := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, attentionID)
	reservation, err := c.ReserveAttention(runtime, attentionID)
	if err != nil {
		t.Fatalf("ReserveAttention: %v", err)
	}
	if err := runtime.acceptDelegateAttention(reservation); err != nil {
		t.Fatal(err)
	}
	// Repeating the claim before CommitStart must retain the same generation.
	runtime.attentionMu.Lock()
	latched := runtime.attentionAcceptedID
	runtime.attentionMu.Unlock()
	if latched != attentionID {
		t.Fatalf("trigger latched before start commit = %q, want %q", latched, attentionID)
	}
	fold, err := readDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("read attention claim: %v", err)
	}
	if got := fold.pendingIDs(); !reflect.DeepEqual(got, []string{attentionID}) || len(fold.resolutions) != 0 {
		t.Fatalf("launch claimed observation: pending=%v resolutions=%v", got, fold.resolutions)
	}
	if aggregate := c.durable["dlg_target"]; aggregate.Generation != 0 || aggregate.CurrentRunOpen || !aggregate.NeedsAttention {
		t.Fatalf("uncommitted claim admitted generation: %#v", aggregate)
	}
	if len(c.reservations) != 1 || c.live["dlg_target"].binding != nil {
		t.Fatalf("acceptance lost retry claim or installed binding: reservations=%#v live=%#v", c.reservations, c.live["dlg_target"])
	}
	if err := runtime.acceptDelegateAttention(reservation); err != nil {
		t.Fatalf("retry attention claim: %v", err)
	}
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("commit retried attention generation: %v", err)
	}
	if started.lease.generation != 1 {
		t.Fatalf("retried launch generation = %d, want 1", started.lease.generation)
	}
	if _, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target"); err != nil {
		t.Fatalf("StopSubtree after recovered acceptance: %v", err)
	}
	if aggregate := c.durable["dlg_target"]; aggregate.Phase != delegatestore.PhaseStopping || aggregate.Generation != started.lease.generation {
		t.Fatalf("recovered attention generation is not stoppable: %#v", aggregate)
	}
}

func TestDelegateAttention_ArmClaimSpansFoldAndOpen(t *testing.T) {
	const (
		sessionID   = "child-dlg_target"
		attentionID = "interleaved-attention"
	)
	c, runtime, path := newDelegateAttentionAcceptanceHarness(t, sessionID, nil, attentionID)
	reservation, err := c.ReserveAttention(runtime, attentionID)
	if err != nil {
		t.Fatalf("ReserveAttention: %v", err)
	}

	foldEntered := make(chan struct{})
	releaseFold := make(chan struct{})
	var foldOnce sync.Once
	t.Cleanup(func() {
		select {
		case <-releaseFold:
		default:
			close(releaseFold)
		}
	})
	runtime.cfg.testOnly.delegateAttentionReadFold = func(path, sessionID string) (delegateAttentionFold, error) {
		wait := false
		foldOnce.Do(func() {
			close(foldEntered)
			wait = true
		})
		if wait {
			<-releaseFold
		}
		return readDelegateAttentionFold(path, sessionID)
	}

	armDone := make(chan error, 1)
	go func() { armDone <- runtime.requestNotificationWake() }()
	<-foldEntered

	// The reservation is the controller-side blocker that used to deadlock
	// armDelegateAttentionOnce while it held attentionMu. The acceptance path
	// must claim the exact ID before it can close that blocker.
	consumeDone := make(chan error, 1)
	go func() {
		err := runtime.acceptDelegateAttention(reservation)
		if err == nil {
			_, err = c.CommitStart(reservation)
		}
		consumeDone <- err
	}()
	close(releaseFold)
	// TRIPWIRE: this one-second ceiling only detects a deadlock; normal arm and acceptance completion is awaited on channels, never paced by the timer.
	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
	select {
	case err := <-armDone:
		if err != nil {
			t.Fatalf("arm attention: %v", err)
		}
	case <-timeout.C:
		t.Fatal("arm attention remained blocked on an accepted reservation")
	}
	select {
	case err := <-consumeDone:
		if err != nil {
			t.Fatalf("consume attention: %v", err)
		}
	case <-timeout.C:
		t.Fatal("attention acceptance remained blocked behind arm")
	}

	fold, err := readDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("read attention fold: %v", err)
	}
	if got := fold.pendingIDs(); !reflect.DeepEqual(got, []string{attentionID}) || len(fold.resolutions) != 0 {
		t.Fatalf("claim resolved unobserved attention: pending=%v resolutions=%v", got, fold.resolutions)
	}
	events, err := c.store.Load()
	if err != nil {
		t.Fatalf("load controller journal: %v", err)
	}
	state, err := delegatestore.Fold(events)
	if err != nil {
		t.Fatalf("fold controller journal: %v", err)
	}
	if aggregate := state["dlg_target"]; aggregate.Generation != 1 || !aggregate.CurrentRunOpen {
		t.Fatalf("interleaved arm changed the claimed generation: %#v", aggregate)
	}
	if _, err := c.ReserveAttention(runtime, attentionID); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("interleaved arm admitted a second live generation: %v", err)
	}
}

func TestDelegateAttention_RecoveryStopWaitsForOldRunnerBeforeReuse(t *testing.T) {
	providerEntered := make(chan struct{})
	releaseProvider := make(chan struct{})
	successorEntered := make(chan struct{})
	releaseSuccessor := make(chan struct{})
	var releaseProviderOnce sync.Once
	var releaseSuccessorOnce sync.Once
	t.Cleanup(func() {
		releaseProviderOnce.Do(func() { close(releaseProvider) })
		releaseSuccessorOnce.Do(func() { close(releaseSuccessor) })
	})

	fixture := newColdStableDelegateFixture(t, "")
	adapter := &fakeErrAdapter{
		name: "openai",
		steps: []func(llm.Request) (llm.Response, error){
			func(llm.Request) (llm.Response, error) {
				close(providerEntered)
				<-releaseProvider
				return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 403, "terminal provider failure", nil, nil)
			},
			func(llm.Request) (llm.Response, error) {
				close(successorEntered)
				<-releaseSuccessor
				return finalResponse("successor completed after recovery"), nil
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	fixture.client = client
	root := restoreSupervisionRoot(t, fixture, nil)
	started := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start recovery-fenced run", 0)
	if started.result.Err != nil {
		t.Fatalf("start stable delegate: %v", started.result.Err)
	}
	<-providerEntered
	child := root.subagents.get(fixture.childID)
	if child == nil || child.sess == nil {
		t.Fatalf("stable child %q was not tracked", fixture.childID)
	}
	child.mu.Lock()
	oldDone := child.done
	child.mu.Unlock()

	finalStatePublished := make(chan struct{})
	releaseFinalizer := make(chan struct{})
	var releaseFinalizerOnce sync.Once
	t.Cleanup(func() { releaseFinalizerOnce.Do(func() { close(releaseFinalizer) }) })
	recoveryErr := make(chan error, 1)
	updateSessionTestConfig(child.sess, func(cfg *testConfig) {
		cfg.subagentAfterFinalStatePublish = func(*subagent) {
			controller := root.delegateController
			lease := delegateLease{delegateID: fixture.delegateID, generation: 1}
			controller.mu.Lock()
			var claim *delegateSettlementClaim
			for _, candidate := range controller.settlementClaims {
				if candidate != nil && candidate.lease == lease {
					claim = candidate
					break
				}
			}
			controller.mu.Unlock()
			if claim == nil {
				recoveryErr <- errors.New("terminal runner published without its exact settlement claim")
			} else {
				recoveryErr <- controller.RequireFinalizationRecovery(claim)
			}
			close(finalStatePublished)
			<-releaseFinalizer
		}
	})
	releaseProviderOnce.Do(func() { close(releaseProvider) })
	<-finalStatePublished
	if err := <-recoveryErr; err != nil {
		t.Fatalf("latch exact finalization recovery: %v", err)
	}

	controller := root.delegateController
	stopResult, cancelPlan, stopPlans, err := controller.StopSubtree(rootDelegateActor(root.delegateRootSessionID), fixture.delegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	executeDelegateCancelPlan(cancelPlan)
	if err := root.executeDelegateMutationPlans(stopPlans); err != nil {
		t.Fatalf("execute stop plans: %v", err)
	}
	for i := range 2 {
		evidence, err := collectDelegateReconcileEvidence(fixture.stateDir, controller.ReconcileRequirements())
		if err != nil {
			t.Fatalf("collect recovery evidence %d: %v", i, err)
		}
		plans, err := controller.Reconcile(evidence)
		if err != nil {
			t.Fatalf("reconcile paused recovery runner %d: %v", i, err)
		}
		if err := root.executeDelegateMutationPlans(plans); err != nil {
			t.Fatalf("execute paused recovery plans %d: %v", i, err)
		}
	}
	controller.mu.Lock()
	aggregate := controller.durable[fixture.delegateID]
	stop := controller.stop
	controller.mu.Unlock()
	if aggregate == nil || !aggregate.CurrentRunOpen {
		t.Fatalf("recovery stop released generation before its old runner quiesced: %#v", aggregate)
	}
	select {
	case <-stopResult.done:
		t.Fatal("recovery stop completed before its old runner quiesced")
	default:
	}
	if stop == nil {
		t.Fatal("recovery stop disappeared while its old runner was paused")
	}

	for {
		select {
		case <-stop.progress:
			continue
		default:
			goto progressDrained
		}
	}

progressDrained:
	releaseFinalizerOnce.Do(func() { close(releaseFinalizer) })
	<-oldDone
	<-stop.progress
	updateSessionTestConfig(child.sess, func(cfg *testConfig) {
		cfg.subagentAfterFinalStatePublish = nil
	})
	for i := range 2 {
		evidence, err := collectDelegateReconcileEvidence(fixture.stateDir, controller.ReconcileRequirements())
		if err != nil {
			t.Fatalf("collect quiesced recovery evidence %d: %v", i, err)
		}
		plans, err := controller.Reconcile(evidence)
		if err != nil {
			t.Fatalf("reconcile quiesced recovery runner %d: %v", i, err)
		}
		if err := root.executeDelegateMutationPlans(plans); err != nil {
			t.Fatalf("execute quiesced recovery plans %d: %v", i, err)
		}
	}
	select {
	case <-stopResult.done:
	default:
		t.Fatal("recovery stop remained pending after its old runner quiesced")
	}

	continued := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start successor after recovery", 0)
	if continued.result.Err != nil || continued.result.Action != "started" {
		t.Fatalf("start successor after recovery = %#v", continued.result)
	}
	<-successorEntered
	successor := root.subagents.get(fixture.childID)
	if successor != child {
		t.Fatalf("successor runtime = %p, want retained runtime %p", successor, child)
	}
	successor.mu.Lock()
	successorDone := successor.done
	successor.mu.Unlock()
	select {
	case <-successorDone:
		t.Fatal("old recovery runner prematurely closed the successor completion channel")
	default:
	}
	releaseSuccessorOnce.Do(func() { close(releaseSuccessor) })
	<-successorDone
}

func TestDelegateAttention_StabilizationRetryPreservesFailedBarrier(t *testing.T) {
	stateDir := t.TempDir()
	const (
		sessionID   = "stabilization-retry"
		attentionID = "attention-stabilization-retry"
	)
	fs := newAttentionAmbiguousSyncFS()
	path := transcriptPath(stateDir, sessionID)
	writer, err := transcript.NewWriterWithFS(fs, path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	runtime := &Session{id: sessionID, stateDir: stateDir}
	runtime.attachTranscript(writer)
	t.Cleanup(func() { _ = runtime.closeAttachedTranscript() })
	attention := schema.NewTurn(schema.TurnSteering, llm.User("pending attention"))
	attention.AttentionID = attentionID
	if err := writer.AppendDurable(attention); err != nil {
		t.Fatalf("append attention: %v", err)
	}
	fs.failNextAmbiguousDurability(3)
	if err := runtime.resolveAttentionDurably([]string{attentionID}, delegateAttentionConsumed); err == nil {
		t.Fatal("ambiguous resolution unexpectedly succeeded")
	}
	openCalls := 0
	runtime.cfg.testOnly.delegateAttentionOpenWriter = func(path, expectedSessionID string) (*transcript.Writer, []transcript.Entry, error) {
		openCalls++
		if openCalls == 1 {
			return nil, nil, errors.New("injected stabilization reopen failure")
		}
		return transcript.OpenWriterForSessionWithFS(fs, path, expectedSessionID)
	}
	if err := runtime.stabilizeAttentionForStop(attentionID); err == nil {
		t.Fatal("first stabilization unexpectedly survived injected reopen failure")
	}
	if err := runtime.stabilizeAttentionForStop(attentionID); err == nil {
		t.Fatal("stabilization retry accepted the readable marker without retrying the failed durability barrier")
	}
	if err := runtime.stabilizeAttentionForStop(attentionID); err != nil {
		t.Fatalf("stabilization after recovered barrier: %v", err)
	}
	if got := fs.successfulSyncsAfterFailure(); got == 0 {
		t.Fatal("stabilization retry established no successful transcript barrier")
	}
}

func TestDelegateAttention_RecoveryRetainedRuntimePreservesFailedToolCount(t *testing.T) {
	stateDir := t.TempDir()
	const (
		sessionID   = "stabilization-failure-count"
		attentionID = "attention-failure-count"
	)
	fs := newAttentionAmbiguousSyncFS()
	path := transcriptPath(stateDir, sessionID)
	writer, err := transcript.NewWriterWithFS(fs, path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	writer.SyncInterval = time.Hour
	writer.TrackFailures(nil, 0)
	runtime := &Session{id: sessionID, stateDir: stateDir}
	runtime.attachTranscript(writer)
	t.Cleanup(func() { _ = runtime.closeAttachedTranscript() })
	failed := schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("failed-call-1", "probe", "failed", true))
	if err := writer.AppendDurable(failed); err != nil {
		t.Fatalf("append first failed result: %v", err)
	}
	attention := schema.NewTurn(schema.TurnSteering, llm.User("pending attention"))
	attention.AttentionID = attentionID
	if err := writer.AppendDurable(attention); err != nil {
		t.Fatalf("append attention: %v", err)
	}
	fs.failNextResolutionDurability()
	if err := runtime.resolveAttentionDurably([]string{attentionID}, delegateAttentionConsumed); err == nil {
		t.Fatal("ambiguous resolution unexpectedly succeeded")
	}
	runtime.cfg.testOnly.delegateAttentionOpenWriter = func(path, expectedSessionID string) (*transcript.Writer, []transcript.Entry, error) {
		return transcript.OpenWriterForSessionWithFS(fs, path, expectedSessionID)
	}
	if err := runtime.stabilizeAttentionForStop(attentionID); err != nil {
		t.Fatalf("stabilize attention: %v", err)
	}
	if got, measured := runtime.FailedToolCallsSnapshot(); !measured || got != 1 {
		t.Fatalf("failed-tool count after stabilization = %d measured=%v, want 1 true", got, measured)
	}
	syncsBeforeAppend := fs.successfulSyncsAfterFailure()
	failed = schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("failed-call-2", "probe", "failed again", true))
	if err := runtime.writeTranscript(failed); err != nil {
		t.Fatalf("append retained-runtime failed result: %v", err)
	}
	if got := fs.successfulSyncsAfterFailure(); got != syncsBeforeAppend {
		t.Fatalf("retained writer lost sync interval: successful syncs %d -> %d", syncsBeforeAppend, got)
	}
	if got, measured := runtime.FailedToolCallsSnapshot(); !measured || got != 2 {
		t.Fatalf("failed-tool count after retained-runtime append = %d measured=%v, want 2 true", got, measured)
	}
}

func TestDelegateAttention_ColdResolutionValidatesBatchBeforeAppend(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	const (
		sessionID   = "cold-resolution"
		attentionID = "attention-present"
	)
	path := transcriptPath(stateDir, sessionID)
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	attention := schema.NewTurn(schema.TurnSteering, llm.User("pending attention"))
	attention.AttentionID = attentionID
	if err := writer.AppendDurable(attention); err != nil {
		t.Fatalf("append attention: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}

	err = appendColdAttentionResolution(path, sessionID, []string{attentionID, "attention-missing"}, delegateAttentionDiscarded)
	if err == nil {
		t.Fatal("cold resolution accepted a batch containing an unknown attention ID")
	}
	fold, err := readDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("read attention after rejected batch: %v", err)
	}
	if pending := fold.pendingIDs(); !reflect.DeepEqual(pending, []string{attentionID}) {
		t.Fatalf("rejected batch partially resolved attention: %#v", pending)
	}
}

func TestDelegateAttention_ColdNotificationRetryReestablishesDurabilityBeforeSourceAck(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	const (
		sessionID   = "cold-notification-durability"
		attentionID = "delegate:delivery-durability"
	)
	path := transcriptPath(stateDir, sessionID)
	fs := newAttentionAmbiguousSyncFS()
	writer, err := transcript.NewWriterWithFS(fs, path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close initial transcript: %v", err)
	}
	open := func(path, expectedSessionID string) (*transcript.Writer, []transcript.Entry, error) {
		return transcript.OpenWriterForSessionWithFS(fs, path, expectedSessionID)
	}

	fs.failNextAmbiguousDurability(2)
	if _, err := appendColdDelegateNotificationDurablyWithOpen(path, sessionID, attentionID, "durable packet", time.Unix(100, 0), open); err == nil {
		t.Fatal("ambiguous cold notification append unexpectedly succeeded")
	}
	fold, err := readDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("read ambiguous notification: %v", err)
	}
	if _, visible := fold.content[attentionID]; !visible {
		t.Fatal("fault fixture did not leave the notification readable")
	}
	if _, err := appendColdDelegateNotificationDurablyWithOpen(path, sessionID, attentionID, "durable packet", time.Unix(100, 0), open); err != nil {
		t.Fatalf("retry existing cold notification: %v", err)
	}
	if got := fs.successfulSyncsAfterFailure(); got == 0 {
		t.Fatal("existing cold notification was accepted without a renewed durability barrier")
	}
}

func TestDelegateAttention_ColdResolutionRetryReestablishesDurabilityBeforeStopAck(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	const (
		sessionID   = "cold-resolution-durability"
		attentionID = "attention-durability"
	)
	path := transcriptPath(stateDir, sessionID)
	fs := newAttentionAmbiguousSyncFS()
	writer, err := transcript.NewWriterWithFS(fs, path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	attention := schema.NewTurn(schema.TurnSteering, llm.User("pending attention"))
	attention.AttentionID = attentionID
	if err := writer.AppendDurable(attention); err != nil {
		t.Fatalf("append attention: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close initial transcript: %v", err)
	}
	open := func(path, expectedSessionID string) (*transcript.Writer, []transcript.Entry, error) {
		return transcript.OpenWriterForSessionWithFS(fs, path, expectedSessionID)
	}

	fs.failNextAmbiguousDurability(2)
	if err := appendColdAttentionResolutionWithOpen(path, sessionID, []string{attentionID}, delegateAttentionDiscarded, open); err == nil {
		t.Fatal("ambiguous cold resolution append unexpectedly succeeded")
	}
	fold, err := readDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("read ambiguous resolution: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != delegateAttentionDiscarded {
		t.Fatalf("fault fixture resolution = %q, want readable discard", got)
	}
	if err := appendColdAttentionResolutionWithOpen(path, sessionID, []string{attentionID}, delegateAttentionDiscarded, open); err != nil {
		t.Fatalf("retry existing cold resolution: %v", err)
	}
	if got := fs.successfulSyncsAfterFailure(); got == 0 {
		t.Fatal("existing cold resolution was accepted without a renewed durability barrier")
	}
}

func TestDelegateAttention_ColdCallerCommitRequiresRenewedDurabilityBeforeSourceAck(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	plans := c.ReplayDeliveries()
	if len(plans) != 1 {
		t.Fatalf("ReplayDeliveries = %#v", plans)
	}

	fs := newAttentionAmbiguousSyncFS()
	path := transcriptPath(c.stateDir, "root-session")
	writer, err := transcript.NewWriterWithFS(fs, path, transcript.Header{SessionID: "root-session"})
	if err != nil {
		t.Fatalf("create caller transcript: %v", err)
	}
	commit := schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("delegate-call", "delegate_send", "done", false))
	commit.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{ToolCallID: "delegate-call", DeliveryID: plans[0].deliveryID}}
	fs.failNextAmbiguousDurability(3)
	// The commit's whole line lands but its fsync fails: a record, so
	// AppendDurable returns nil, tracked as dirty debt (not poison — the writer
	// stays usable). Durability is unestablished, and the cold replay below is
	// what must not source-ack until a renewed barrier confirms it; Close's own
	// flush fails too, surfacing the unsynced debt.
	if err := writer.AppendDurable(commit); err != nil {
		t.Fatalf("ambiguous caller commit append error = %v, want nil: the whole line is a record", err)
	}
	if writer.Poisoned() {
		t.Fatal("a retained whole line poisoned the writer; retained is debt the next fsync settles")
	}
	if err := writer.Close(); err == nil {
		t.Fatal("ambiguous caller commit close unexpectedly succeeded")
	}
	c.attentionOpen = func(path, expectedSessionID string) (*transcript.Writer, []transcript.Entry, error) {
		return transcript.OpenWriterForSessionWithFS(fs, path, expectedSessionID)
	}

	if _, err := prepareColdDelegateDeliveryReplay(c, plans); err == nil {
		t.Fatal("cold replay acknowledged a readable caller commit without a renewed durability barrier")
	}
	c.mu.Lock()
	remaining := len(c.durable["dlg_target"].PendingDeliveries)
	c.mu.Unlock()
	if remaining != 1 {
		t.Fatalf("failed caller-commit barrier acknowledged source head: remaining=%d", remaining)
	}
	if pending, err := prepareColdDelegateDeliveryReplay(c, plans); err != nil || len(pending) != 0 {
		t.Fatalf("retry caller-commit barrier = pending %#v err %v", pending, err)
	}
	if got := fs.successfulSyncsAfterFailure(); got == 0 {
		t.Fatal("caller commit source ack had no successful renewed durability barrier")
	}
}

func TestDelegateAttention_LiveNotificationRetryReestablishesDurabilityBeforeSourceAck(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	fs := newAttentionAmbiguousSyncFS()
	path := transcriptPath(c.stateDir, "root-session")
	writer, err := transcript.NewWriterWithFS(fs, path, transcript.Header{SessionID: "root-session"})
	if err != nil {
		t.Fatalf("create root transcript: %v", err)
	}
	root := &Session{id: "root-session", stateDir: c.stateDir, delegateController: c}
	root.cfg.testOnly.delegateAttentionOpenWriter = func(path, expectedSessionID string) (*transcript.Writer, []transcript.Entry, error) {
		return transcript.OpenWriterForSessionWithFS(fs, path, expectedSessionID)
	}
	root.attachTranscript(writer)
	c.rootRuntime = root
	t.Cleanup(func() { _ = root.attachedTranscript().Close() })

	plans := c.ReplayDeliveries()
	if len(plans) != 1 {
		t.Fatalf("ReplayDeliveries = %#v", plans)
	}
	fs.failNextAmbiguousDurability(3)
	if _, err := deliverDelegatePacket(plans[0], root); err == nil {
		t.Fatal("ambiguous live notification append unexpectedly succeeded")
	}
	plans = c.ReplayDeliveries()
	if len(plans) != 1 {
		t.Fatalf("ReplayDeliveries after ambiguous append = %#v", plans)
	}
	if _, err := deliverDelegatePacket(plans[0], root); err == nil {
		t.Fatal("live retry source-acked readable notification without renewing the failed durability barrier")
	}
	c.mu.Lock()
	remaining := len(c.durable["dlg_target"].PendingDeliveries)
	c.mu.Unlock()
	if remaining != 1 {
		t.Fatalf("failed live barrier acknowledged source head: remaining=%d", remaining)
	}
	plans = c.ReplayDeliveries()
	if len(plans) != 1 {
		t.Fatalf("ReplayDeliveries after failed barrier = %#v", plans)
	}
	if _, err := deliverDelegatePacket(plans[0], root); err != nil {
		t.Fatalf("live notification retry after recovered barrier: %v", err)
	}
	c.mu.Lock()
	remaining = len(c.durable["dlg_target"].PendingDeliveries)
	c.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("live notification remained pending after recovered barrier: %d", remaining)
	}
	if err := root.writeTranscriptDurable(schema.NewTurn(schema.TurnUserInput, llm.User("ordinary turn after recovery"))); err != nil {
		t.Fatalf("append ordinary turn after recovery: %v", err)
	}
	entries := readAttentionTranscriptEntries(t, path)
	for i, entry := range entries {
		if entry.Seq != i {
			t.Fatalf("entry %d sequence after live recovery = %d, want %d", i, entry.Seq, i)
		}
	}
}

func TestDelegateAttention_LiveResolutionRetryReestablishesDurabilityBeforeSettlement(t *testing.T) {
	stateDir := t.TempDir()
	const (
		sessionID   = "live-resolution-durability"
		attentionID = "attention-live-resolution"
	)
	fs := newAttentionAmbiguousSyncFS()
	path := transcriptPath(stateDir, sessionID)
	writer, err := transcript.NewWriterWithFS(fs, path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	runtime := &Session{id: sessionID, stateDir: stateDir}
	runtime.cfg.testOnly.delegateAttentionOpenWriter = func(path, expectedSessionID string) (*transcript.Writer, []transcript.Entry, error) {
		return transcript.OpenWriterForSessionWithFS(fs, path, expectedSessionID)
	}
	runtime.attachTranscript(writer)
	t.Cleanup(func() { _ = runtime.attachedTranscript().Close() })
	attention := schema.NewTurn(schema.TurnSteering, llm.User("pending attention"))
	attention.AttentionID = attentionID
	if err := writer.AppendDurable(attention); err != nil {
		t.Fatalf("append attention: %v", err)
	}
	fs.failNextAmbiguousDurability(3)
	if err := runtime.resolveAttentionDurably([]string{attentionID}, delegateAttentionConsumed); err == nil {
		t.Fatal("ambiguous live resolution unexpectedly succeeded")
	}
	if err := runtime.resolveAttentionDurably([]string{attentionID}, delegateAttentionConsumed); err == nil {
		t.Fatal("live resolution retry accepted readable marker without renewing the failed durability barrier")
	}
	if err := runtime.resolveAttentionDurably([]string{attentionID}, delegateAttentionConsumed); err != nil {
		t.Fatalf("live resolution after recovered barrier: %v", err)
	}
	if got := fs.successfulSyncsAfterFailure(); got == 0 {
		t.Fatal("live resolution settlement had no successful renewed barrier")
	}
}

func TestDelegateAttention_ResolutionMarkerDoesNotSplitToolCallAndResult(t *testing.T) {
	t.Parallel()
	const callID = "call-with-resolution"
	for _, test := range []struct {
		name           string
		preserveRecent int
	}{
		{name: "cutoff on result", preserveRecent: 2},
		{name: "cutoff on resolution", preserveRecent: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			attention := schema.NewTurn(schema.TurnSteering, llm.User("delegate completed"))
			attention.AttentionID = "delegate:delivery-1"
			history := []schema.Turn{
				schema.NewTurn(schema.TurnUserInput, llm.User("task")),
				schema.NewTurn(schema.TurnAssistant, llm.Assistant("earlier")),
				attention,
				schema.NewTurn(schema.TurnAssistant, delegateAttentionToolCall(callID)),
				delegateAttentionResolutionTurn(attention.AttentionID, delegateAttentionConsumed),
				schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed(callID, "probe", "real result", false)),
				schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent")),
			}
			manager := contextmgr.NewManager(NewOpenAIProfile("gpt-5.2"), nil, cheapmodel.New(nil))
			manager.PreserveRecentTurns = test.preserveRecent
			manager.ForceCompact(context.Background(), &history, "", func(events.EventKind, events.EventData) {})

			messages := expandHistory(history, replayScope{})
			callIndex, resultIndex, resultCount := -1, -1, 0
			for i, message := range messages {
				if message.Text() == "Attention resolved." {
					t.Fatalf("resolution marker reached provider history: %#v", messages)
				}
				for _, part := range message.Content {
					if part.ToolCall != nil && part.ToolCall.ID == callID {
						callIndex = i
					}
					if part.ToolResult != nil && part.ToolResult.ToolCallID == callID {
						resultIndex = i
						resultCount++
						if part.ToolResult.IsError || part.ToolResult.Content != "real result" {
							t.Fatalf("projected tool result = %#v, want real non-error result", part.ToolResult)
						}
					}
				}
			}
			if callIndex < 0 || resultIndex != callIndex+1 || resultCount != 1 {
				t.Fatalf("projected exchange call=%d result=%d count=%d; messages=%#v", callIndex, resultIndex, resultCount, messages)
			}
		})
	}
}

func TestDelegateAttention_PublicTranscriptReadsExcludePrivateResolutionMetadata(t *testing.T) {
	stateDir := t.TempDir()
	const (
		sessionID   = "public-attention-read"
		attentionID = "private-attention-correlation"
		deliveryID  = "private-delivery-correlation"
		callID      = "public-tool-call"
	)
	path := transcriptPath(stateDir, sessionID)
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	attentionContent, err := delegateNotificationContent(delegateDeliveryPlan{
		delegateID: "dlg_public_source",
		deliveryID: deliveryID,
		packet:     delegateControllerReportedPacket("visible attention content"),
	})
	if err != nil {
		t.Fatalf("render delegate notification: %v", err)
	}
	attention := schema.NewTurn(schema.TurnSteering, llm.User(attentionContent))
	attention.AttentionID = attentionID
	turns := []schema.Turn{
		attention,
		schema.NewTurn(schema.TurnAssistant, delegateAttentionToolCall(callID)),
		delegateAttentionResolutionTurn(attentionID, delegateAttentionConsumed),
	}
	results := schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed(callID, "probe", "visible result", false))
	results.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{ToolCallID: callID, DeliveryID: deliveryID}}
	turns = append(turns, results)
	for _, turn := range turns {
		if err := writer.AppendDurable(turn); err != nil {
			_ = writer.Close()
			t.Fatalf("append transcript turn: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}

	rawValue, err := readRaw(path, "", "local:"+sessionID, "")
	if err != nil {
		t.Fatalf("read raw transcript: %v", err)
	}
	raw, ok := rawValue.(readRawEnvelope)
	if !ok {
		t.Fatalf("raw transcript type = %T", rawValue)
	}
	pin := 1
	expandedValue, err := readMarkdownPage(path, "", "local:"+sessionID, schema.SessionMeta{}, "", &pin, 0, maxExpansionBytes)
	if err != nil {
		t.Fatalf("expand transcript turn: %v", err)
	}
	expanded, ok := expandedValue.(readMarkdownEnvelope)
	if !ok || expanded.Expansion == nil {
		t.Fatalf("expanded transcript = %#v", expandedValue)
	}
	outlineValue, err := readOutline(path, "", "local:"+sessionID, "")
	if err != nil {
		t.Fatalf("read transcript outline: %v", err)
	}
	outline, ok := outlineValue.(readOutlineEnvelope)
	if !ok {
		t.Fatalf("outline transcript type = %T", outlineValue)
	}
	for surface, content := range map[string]string{
		"jsonl":       raw.Content,
		"markdown":    expanded.Content,
		"expand_turn": expanded.Expansion.Data,
		"outline":     outline.Content,
	} {
		for _, private := range []string{attentionID, deliveryID, "Attention resolved.", "attention_resolution", "ATTENTION_RESOLUTION", "delegate_delivery_commits"} {
			if strings.Contains(content, private) {
				t.Fatalf("%s leaked private resolution metadata %q: %s", surface, private, content)
			}
		}
		if surface == "outline" {
			continue
		}
		visibleContent := []string{"visible result"}
		if surface != "markdown" {
			visibleContent = append(visibleContent, callID)
		}
		for _, visible := range visibleContent {
			if !strings.Contains(content, visible) {
				t.Fatalf("%s dropped public tool-round content %q: %s", surface, visible, content)
			}
		}
	}
	providerHistory := expandHistory(turns, replayScope{})
	providerJSON, err := json.Marshal(providerHistory)
	if err != nil {
		t.Fatalf("marshal provider history: %v", err)
	}
	if strings.Contains(string(providerJSON), deliveryID) {
		t.Fatalf("provider history leaked private delivery ID %q: %s", deliveryID, providerJSON)
	}
	if !strings.Contains(string(providerJSON), "visible attention content") {
		t.Fatalf("provider history dropped delegate packet content: %s", providerJSON)
	}

	privateSnippets, opened := contentSnippets(stateDir, sessionID, "Attention resolved.", "attention resolved.")
	if !opened {
		t.Fatal("find_session_transcripts content scan did not open the real transcript")
	}
	if len(privateSnippets) != 0 {
		t.Fatalf("find_session_transcripts exposed the private resolution marker: %#v", privateSnippets)
	}
	visibleSnippets, opened := contentSnippets(stateDir, sessionID, "visible result", "visible result")
	if !opened || len(visibleSnippets) != 1 || visibleSnippets[0].Seq != 1 {
		t.Fatalf("find_session_transcripts visible tool-result query = opened %v, snippets %#v", opened, visibleSnippets)
	}

	rawWindowValue, err := readRaw(path, "", "local:"+sessionID, "last:2")
	if err != nil {
		t.Fatalf("read ranged raw transcript: %v", err)
	}
	rawWindow := rawWindowValue.(readRawEnvelope)
	windowLines := strings.Split(strings.TrimSpace(rawWindow.Content), "\n")
	windowKinds := make([]schema.TurnKind, 0, len(windowLines)-1)
	for _, line := range windowLines[1:] {
		entry, err := transcript.DecodeEntry([]byte(line))
		if err != nil {
			t.Fatalf("decode ranged public entry: %v", err)
		}
		windowKinds = append(windowKinds, entry.Turn.Kind)
	}
	if want := []schema.TurnKind{schema.TurnAssistant, schema.TurnToolResults}; !reflect.DeepEqual(windowKinds, want) {
		t.Fatalf("ranged public JSONL kinds = %#v, want %#v", windowKinds, want)
	}

	markdownWindowValue, err := readMarkdownPage(path, "", "local:"+sessionID, schema.SessionMeta{}, "last:2", nil, 0, 0)
	if err != nil {
		t.Fatalf("read ranged markdown transcript: %v", err)
	}
	markdownWindow := markdownWindowValue.(readMarkdownEnvelope)
	if !strings.Contains(markdownWindow.Content, "## Turn 1 — Assistant") || !strings.Contains(markdownWindow.Content, "visible result") || strings.Contains(markdownWindow.Content, "Tool results without a shown call") {
		t.Fatalf("ranged markdown split the public tool round: %s", markdownWindow.Content)
	}

	outlineWindowValue, err := readOutline(path, "", "local:"+sessionID, "last:2")
	if err != nil {
		t.Fatalf("read ranged outline transcript: %v", err)
	}
	outlineWindow := outlineWindowValue.(readOutlineEnvelope)
	if !strings.Contains(outlineWindow.Content, "1 · Assistant · probe") {
		t.Fatalf("ranged outline split the public tool round: %s", outlineWindow.Content)
	}

	findSessionID := sessionID + "-find"
	findPath := transcriptPath(stateDir, findSessionID)
	findWriter, err := transcript.NewWriter(findPath, transcript.Header{SessionID: findSessionID})
	if err != nil {
		t.Fatalf("create searchable transcript: %v", err)
	}
	findAttention := schema.NewTurn(schema.TurnSteering, llm.User("search preface"))
	findAttention.AttentionID = "private-find-attention"
	for _, turn := range []schema.Turn{
		findAttention,
		delegateAttentionResolutionTurn(findAttention.AttentionID, delegateAttentionConsumed),
		delegateAttentionResolutionTurn(findAttention.AttentionID, delegateAttentionConsumed),
		schema.NewTurn(schema.TurnUserInput, llm.User("later public match")),
	} {
		if err := findWriter.AppendDurable(turn); err != nil {
			_ = findWriter.Close()
			t.Fatalf("append searchable transcript: %v", err)
		}
	}
	if err := findWriter.Close(); err != nil {
		t.Fatalf("close searchable transcript: %v", err)
	}
	findSnippets, opened := contentSnippets(stateDir, findSessionID, "later public match", "later public match")
	if !opened || len(findSnippets) != 1 || findSnippets[0].Seq != 1 {
		t.Fatalf("public find sequence = opened %v snippets %#v, want public seq 1", opened, findSnippets)
	}
	findPin := findSnippets[0].Seq
	findExpansionValue, err := readMarkdownPage(findPath, "", "local:"+findSessionID, schema.SessionMeta{}, "", &findPin, 0, maxExpansionBytes)
	if err != nil {
		t.Fatalf("expand public find sequence: %v", err)
	}
	findExpansion := findExpansionValue.(readMarkdownEnvelope)
	if findExpansion.Expansion == nil || !strings.Contains(findExpansion.Expansion.Data, "later public match") {
		t.Fatalf("public find sequence expanded the wrong turn: %#v", findExpansion.Expansion)
	}
}

func TestDelegateAttention_PublicJSONLPreservesExactToolResultNumbers(t *testing.T) {
	stateDir := t.TempDir()
	const (
		sessionID = "public-exact-number"
		callID    = "large-number-call"
		largeID   = uint64(9007199254740993)
	)
	path := transcriptPath(stateDir, sessionID)
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	for _, turn := range []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, delegateAttentionToolCall(callID)),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed(callID, "probe", map[string]any{"id": largeID}, false)),
	} {
		if err := writer.AppendDurable(turn); err != nil {
			_ = writer.Close()
			t.Fatalf("append transcript turn: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}

	rawValue, err := readRaw(path, "", "local:"+sessionID, "")
	if err != nil {
		t.Fatalf("read raw transcript: %v", err)
	}
	raw := rawValue.(readRawEnvelope)
	pin := 0
	expandedValue, err := readMarkdownPage(path, "", "local:"+sessionID, schema.SessionMeta{}, "", &pin, 0, maxExpansionBytes)
	if err != nil {
		t.Fatalf("expand transcript turn: %v", err)
	}
	expanded := expandedValue.(readMarkdownEnvelope)
	for surface, content := range map[string]string{
		"jsonl":       raw.Content,
		"expand_turn": expanded.Expansion.Data,
	} {
		if !strings.Contains(content, "9007199254740993") {
			t.Fatalf("%s rounded exact tool-result integer: %s", surface, content)
		}
		if strings.Contains(content, "9007199254740992") {
			t.Fatalf("%s emitted rounded tool-result integer: %s", surface, content)
		}
	}
}

func TestDelegateAttention_HistoryRepairCannotCreateOrphanedToolResult(t *testing.T) {
	t.Parallel()
	const callID = "call-with-resolution"
	attention := schema.NewTurn(schema.TurnSteering, llm.User("delegate completed"))
	attention.AttentionID = "delegate:delivery-1"
	history := []schema.Turn{
		attention,
		schema.NewTurn(schema.TurnAssistant, delegateAttentionToolCall(callID)),
		delegateAttentionResolutionTurn(attention.AttentionID, delegateAttentionConsumed),
		schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed(callID, "probe", "real result", false)),
	}
	repaired, repairs := repairOrphanedToolResults(history)
	if repairs != 0 {
		t.Fatalf("repair count = %d, want 0; history=%#v", repairs, repaired)
	}
	if !reflect.DeepEqual(repaired, history) {
		t.Fatalf("repair changed valid history:\n got %#v\nwant %#v", repaired, history)
	}
	results := 0
	for _, turn := range repaired {
		for _, part := range turn.Message.Content {
			if part.ToolResult != nil && part.ToolResult.ToolCallID == callID {
				results++
				if part.ToolResult.IsError || part.ToolResult.Content != "real result" {
					t.Fatalf("tool result = %#v, want real non-error result", part.ToolResult)
				}
			}
		}
	}
	if results != 1 {
		t.Fatalf("matching tool results = %d, want 1", results)
	}
}

func TestDelegateAttention_RestartFoldIsProviderFreeAndReadOnly(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	const sessionID = "cold-attention"
	path := transcriptPath(stateDir, sessionID)
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	attention := schema.NewTurn(schema.TurnSteering, llm.User("cold attention"))
	attention.AttentionID = "attention-cold"
	if err := writer.AppendDurable(attention); err != nil {
		t.Fatalf("append attention: %v", err)
	}
	if err := writer.AppendDurable(delegateAttentionResolutionTurn(attention.AttentionID, delegateAttentionConsumed)); err != nil {
		t.Fatalf("append resolution: %v", err)
	}
	toolResults := schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call-cold", "delegate_send", "done", false))
	toolResults.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{ToolCallID: "call-cold", DeliveryID: "dlg/delivery/1"}}
	if err := writer.AppendDurable(toolResults); err != nil {
		t.Fatalf("append delivery commit: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile before: %v", err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat before: %v", err)
	}

	fold, err := readDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("read cold fold: %v", err)
	}
	if pending := fold.pendingIDs(); len(pending) != 0 {
		t.Fatalf("pending attention = %#v, want none", pending)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile after: %v", err)
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat after: %v", err)
	}
	if !bytes.Equal(after, before) || afterInfo.Size() != beforeInfo.Size() || !afterInfo.ModTime().Equal(beforeInfo.ModTime()) {
		t.Fatalf("cold fold mutated transcript: before=%d/%s after=%d/%s", beforeInfo.Size(), beforeInfo.ModTime(), afterInfo.Size(), afterInfo.ModTime())
	}
	missing := filepath.Join(stateDir, sessionsSubdir, "missing.transcript.jsonl")
	if fold, err := readDelegateAttentionFold(missing, "missing"); err != nil || len(fold.pendingIDs()) != 0 {
		t.Fatalf("missing transcript fold = %#v, %v", fold, err)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing transcript was created: %v", err)
	}

	invalid := schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call-real", "delegate_send", "done", false))
	invalid.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{ToolCallID: "call-wrong", DeliveryID: "dlg/delivery/1"}}
	if _, err := foldDelegateAttention([]transcript.Entry{{Turn: invalid}}); err == nil {
		t.Fatal("cold fold accepted delivery metadata whose ToolCallID is absent from its TurnToolResults")
	}
	duplicateCall := schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call-real", "delegate_send", "done", false))
	duplicateCall.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{
		{ToolCallID: "call-real", DeliveryID: "dlg/delivery/1"},
		{ToolCallID: "call-real", DeliveryID: "dlg/delivery/2"},
	}
	// One delegate_send wait may carry earlier results ahead of its own, so
	// its turn commits several deliveries to one call (#3906)...
	if _, err := foldDelegateAttention([]transcript.Entry{{Turn: duplicateCall}}); err != nil {
		t.Fatalf("cold fold refused one ToolCallID committing several deliveries in its turn: %v", err)
	}
	// ...but a later turn can't commit another delivery to that call.
	reusedCall := schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call-real", "delegate_send", "done", false))
	reusedCall.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{ToolCallID: "call-real", DeliveryID: "dlg/delivery/3"}}
	if _, err := foldDelegateAttention([]transcript.Entry{{Turn: duplicateCall}, {Turn: reusedCall}}); err == nil {
		t.Fatal("cold fold accepted a later turn's delivery on an earlier turn's ToolCallID")
	}
}

func TestDelegateAttention_RestartReplaysCallerDeliveryCommitWithoutDuplicateToolResult(t *testing.T) {
	stateDir := t.TempDir()
	workspace := t.TempDir()
	const (
		rootSessionID   = "root-session"
		ownerDelegateID = "dlg_parent"
		ownerSessionID  = "child-dlg_parent"
	)
	storePath := filepath.Join(jobsDir(stateDir, rootSessionID), "delegates.jsonl")
	store, err := delegatestore.Open(storePath)
	if err != nil {
		t.Fatalf("open delegate store: %v", err)
	}
	c, err := openDelegateTreeController(delegateTreeControllerConfig{
		store:         store,
		rootSessionID: rootSessionID,
		stateDir:      stateDir,
		turnLimit:     2,
		driveLimit:    1,
		now:           func() time.Time { return time.Unix(100, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("open controller: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ownerCreated := delegateControllerCreatedEvent(ownerDelegateID, "")
	ownerCreated.Created.Descriptor.ResolvedProfileID = "openai"
	ownerCreated.Created.Descriptor.ResolvedModel = "gpt-5.2"
	ownerCreated.Created.Descriptor.WorkingDir = workspace
	ownerCreated.Created.Descriptor.LocalEnvPolicy = "default"
	c.mu.Lock()
	_, err = c.appendLocked(ownerCreated)
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("seed owner delegate: %v", err)
	}
	seedDelegateControllerIdle(t, c, "dlg_target", ownerDelegateID)
	seedDelegateControllerDelivery(t, c, "dlg_target")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	plans := c.ReplayDeliveries()
	if len(plans) != 1 {
		t.Fatalf("initial nested delivery plans = %#v, want serialized head", plans)
	}
	firstPlan := plans[0]
	_, admitted, err := c.BeginDelivery(firstPlan)
	if err != nil || !admitted {
		t.Fatalf("BeginDelivery = admitted:%t err:%v", admitted, err)
	}
	fs := newAttentionSyncBarrierFS()
	path := transcriptPath(stateDir, ownerSessionID)
	writer, err := transcript.NewWriterWithFS(fs, path, transcript.Header{SessionID: ownerSessionID})
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	// Preserve the historical inline-only/no-state shape. New carrier writes
	// require presentation proof, but restore must still read old records.
	carrier := schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("delegate-send-N", "delegate_send", `{"status":"completed"}`, false))
	carrier.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{ToolCallID: "delegate-send-N", DeliveryID: firstPlan.deliveryID}}
	fs.arm()
	appendDone := make(chan error, 1)
	go func() { appendDone <- writer.AppendDurable(carrier) }()
	select {
	case <-fs.syncEntered:
	case err := <-appendDone:
		t.Fatalf("caller tool result returned before fsync barrier: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close crashed controller store: %v", err)
	}
	fs.release()
	if err := <-appendDone; err != nil {
		t.Fatalf("caller carrier persistence retried source acknowledgment: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close crashed transcript writer: %v", err)
	}
	crashedFold, err := readDelegateAttentionFold(path, ownerSessionID)
	if err != nil {
		t.Fatalf("read crashed caller transcript: %v", err)
	}
	if got := crashedFold.deliveryCommits[firstPlan.deliveryID]; got != "delegate-send-N" {
		t.Fatalf("crashed caller commit = %q, want exact tool call", got)
	}
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID:              ownerSessionID,
		ParentSessionID: rootSessionID,
		ProfileID:       "openai",
		Model:           "gpt-5.2",
		IsSubagent:      true,
	}); err != nil {
		t.Fatalf("save owner metadata: %v", err)
	}

	adapter := &delegateAttentionPanicProvider{}
	client := llm.NewClient()
	client.Register(adapter)
	fresh := &Session{
		id:       rootSessionID,
		stateDir: stateDir,
		client:   client,
		cfg: SessionConfig{
			StateDir:                   stateDir,
			MaxConcurrentDelegateTurns: 2,
			testOnly: testConfig{sessionInitFault: func(point string) error {
				panic("cold delivery replay constructed a Session at " + point)
			}},
		},
	}
	if err := fresh.bootstrapDelegateResources(); err != nil {
		t.Fatalf("bootstrap fresh controller: %v", err)
	}
	t.Cleanup(func() { _ = fresh.closeOwnedDelegateStore() })
	fresh.delegateController.mu.Lock()
	pending := append([]delegatestore.PendingDelivery(nil), fresh.delegateController.durable["dlg_target"].PendingDeliveries...)
	claims := len(fresh.delegateController.deliveryClaims)
	fresh.delegateController.mu.Unlock()
	if len(pending) != 1 || pending[0].DeliveryID != delegateDeliveryID("dlg_target", 2) || claims != 0 {
		t.Fatalf("cold replay pending=%#v claims=%d, want unclaimed source N+1 until attachment", pending, claims)
	}
	if err := fresh.closeOwnedDelegateStore(); err != nil {
		t.Fatalf("close transport-only bootstrap: %v", err)
	}

	// Attachment permits execution. Unlike the provider-free bootstrap above,
	// restore the root through its production constructor, and hold the actual
	// child request so custody and source settlement can be checked before response.
	rootPath := transcriptPath(stateDir, rootSessionID)
	reopened, err := transcript.NewWriter(rootPath, transcript.Header{SessionID: rootSessionID})
	if err != nil {
		t.Fatalf("create root transcript: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close root transcript before restore: %v", err)
	}
	entered, release := make(chan llm.Request, 1), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	executionAdapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			entered <- req
			<-release
			return finalResponse("replay-observed-76")
		},
	}}
	executionClient := llm.NewClient()
	executionClient.Register(executionAdapter)
	profile := withTestSessionNamer(executionClient, NewOpenAIProfile("gpt-5.2"))
	root, err := RestoreSessionFromMetaWithConfig(executionClient, profile, execenv.NewLocalExecutionEnvironment(workspace), schema.SessionMeta{
		ID:        rootSessionID,
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config:    schema.ConfigSnapshot{MaxSubagentDepth: 2},
	}, RestoreSessionConfig{
		StateDir:    stateDir,
		ForceRealIO: true,
		testOnly: testConfig{
			skipGitSnapshot:            true,
			minimalSystemPrompt:        true,
			sandboxProber:              bwrapCapableProber(workspace),
			disableDelegateIdleRelease: true,
		},
	})
	if err != nil {
		unblock()
		t.Fatalf("restore execution-capable root: %v", err)
	}
	t.Cleanup(root.Close)
	started := false
	defer func() {
		unblock()
		if started {
			waitForStableSupervisionRun(t, root, ownerSessionID)
			root.sendersWG.Wait()
		}
	}()
	var req llm.Request
	select {
	case req = <-entered:
		started = true
	// TRIPWIRE: only bounds a missing automatic replay wake, not its ordering.
	case <-time.After(10 * time.Second):
		t.Fatal("restored caller replay did not launch its initialized receiver")
	}
	observationRequest(t, req, []string{"seed"}, nil)
	root.delegateController.mu.Lock()
	runtime := root.delegateController.live[ownerDelegateID].runtime
	root.delegateController.mu.Unlock()
	if runtime == nil || runtime.subagents == nil || root.subagents == nil || root.jobManager == nil {
		t.Fatal("production restore did not initialize the root and addressed receiver")
	}
	secondID := delegateAttentionID(delegateDeliveryID("dlg_target", 2))
	assertAttentionResponseReceipts(t, runtime, secondID, 0)

	fold, err := readDelegateAttentionFold(path, ownerSessionID)
	if err != nil {
		t.Fatalf("read final caller fold: %v", err)
	}
	entries := readAttentionTranscriptEntries(t, path)
	toolResults, firstAttention, secondAttention := 0, 0, 0
	for _, entry := range entries {
		turn := entry.Turn
		if turn.Kind == schema.TurnToolResults {
			toolResults++
		}
		if turn.AttentionID == delegateAttentionID(firstPlan.deliveryID) {
			firstAttention++
		}
		if turn.AttentionID == delegateAttentionID(delegateDeliveryID("dlg_target", 2)) {
			secondAttention++
		}
	}
	if toolResults != 1 || firstAttention != 0 || secondAttention != 1 {
		t.Fatalf("replayed transcript counts tool_results=%d N_attention=%d N+1_attention=%d", toolResults, firstAttention, secondAttention)
	}
	if len(fold.pendingIDs()) != 1 || fold.pendingIDs()[0] != delegateAttentionID(delegateDeliveryID("dlg_target", 2)) {
		t.Fatalf("final pending attention = %#v, want N+1 only", fold.pendingIDs())
	}
	if got := fold.deliveryCommits[firstPlan.deliveryID]; got != "delegate-send-N" {
		t.Fatalf("recovered caller commit=%q, want original tool call", got)
	}
	root.delegateController.mu.Lock()
	remaining := len(root.delegateController.durable["dlg_target"].PendingDeliveries)
	claims = len(root.delegateController.deliveryClaims)
	root.delegateController.mu.Unlock()
	if remaining != 0 || claims != 0 {
		t.Fatalf("after N+1 replay pending=%d claims=%d, want neither", remaining, claims)
	}
	unblock()
	waitForStableSupervisionRun(t, root, ownerSessionID)
	root.sendersWG.Wait()
	assertAttentionResponseReceipts(t, runtime, secondID, 1)
	assertNotificationPending(t, runtime, []string{})
	if got := supervisionRequestCount(executionAdapter); got != 1 {
		t.Fatalf("provider requests=%d, want one canonical N+1 response", got)
	}
}

func TestDelegateAttention_BootstrapDrainsRestoredStopAttention(t *testing.T) {
	stateDir := t.TempDir()
	const (
		rootSessionID  = "root-session"
		delegateID     = "dlg_target"
		childSessionID = "child-dlg_target"
		attentionID    = "attention-before-restart-stop"
	)
	storePath := filepath.Join(jobsDir(stateDir, rootSessionID), "delegates.jsonl")
	store, err := delegatestore.Open(storePath)
	if err != nil {
		t.Fatalf("open delegate store: %v", err)
	}
	c, err := openDelegateTreeController(delegateTreeControllerConfig{
		store:         store,
		rootSessionID: rootSessionID,
		stateDir:      stateDir,
		turnLimit:     1,
		driveLimit:    1,
		now:           func() time.Time { return time.Unix(100, 0).UTC() },
	})
	if err != nil {
		_ = store.Close()
		t.Fatalf("open controller: %v", err)
	}
	seedDelegateControllerRunning(t, c, delegateID, "")
	writeDelegateAttentionTranscript(t, transcriptPath(stateDir, childSessionID), childSessionID, attentionID)
	// Persist the historical missing-bit request, then let bootstrap restore it
	// from the real journal. A new Stop intentionally preserves this attention.
	appendDelegateControllerStopRequest(t, c, delegateID)
	if err := store.Close(); err != nil {
		t.Fatalf("close crashed delegate store: %v", err)
	}

	// This partial Session is provider-free bootstrap only. Do not attach it as
	// an execution runtime; initialized new-Stop release is covered separately.
	fresh := &Session{
		id:       rootSessionID,
		stateDir: stateDir,
		cfg: SessionConfig{
			StateDir:                   stateDir,
			MaxConcurrentDelegateTurns: 1,
		},
	}
	if err := fresh.bootstrapDelegateResources(); err != nil {
		t.Fatalf("bootstrap restored stop: %v", err)
	}
	t.Cleanup(func() { _ = fresh.closeOwnedDelegateStore() })

	fresh.delegateController.mu.Lock()
	aggregate := fresh.delegateController.durable[delegateID]
	stop := fresh.delegateController.stop
	claims := len(fresh.delegateController.deliveryClaims)
	fresh.delegateController.mu.Unlock()
	if stop != nil || aggregate == nil || aggregate.PendingStopSeq != 0 || aggregate.CurrentRunOpen || aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeStopped {
		t.Fatalf("bootstrap restored stop state = stop:%#v aggregate:%#v", stop, aggregate)
	}
	fold, err := readDelegateAttentionFold(transcriptPath(stateDir, childSessionID), childSessionID)
	if err != nil {
		t.Fatalf("read stopped child attention: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != delegateAttentionDiscarded {
		t.Fatalf("restored stop attention disposition = %q, want discarded", got)
	}
	resolutions := 0
	for _, entry := range readAttentionTranscriptEntries(t, transcriptPath(stateDir, childSessionID)) {
		if entry.Turn.Kind == schema.TurnAttentionResolution {
			resolution := entry.Turn.AttentionResolution
			if resolution == nil || resolution.AttentionID != attentionID || resolution.Disposition != string(delegateAttentionDiscarded) {
				t.Fatalf("unexpected historical attention resolution: %+v", resolution)
			}
			resolutions++
		}
	}
	if resolutions != 1 || len(fold.pendingIDs()) != 0 || len(fold.observed) != 0 {
		t.Fatalf("historical cleanup resolutions=%d pending=%v observed=%v", resolutions, fold.pendingIDs(), fold.observed)
	}
	pendingIDs := pendingDeliveryIDs(fresh.delegateController, delegateID)
	if claims != 0 || !reflect.DeepEqual(pendingIDs, []string{delegateDeliveryID(delegateID, 1)}) {
		t.Fatalf("stop source pending=%v claims=%d", pendingIDs, claims)
	}

}

func TestDelegateAttention_RestoreReconcilesColdCommitBeforeProviderMetadata(t *testing.T) {
	stateDir := t.TempDir()
	rootSessionID := identifier.MustNewSessionID()
	storePath := filepath.Join(jobsDir(stateDir, rootSessionID), "delegates.jsonl")
	store, err := delegatestore.Open(storePath)
	if err != nil {
		t.Fatalf("open delegate store: %v", err)
	}
	c, err := openDelegateTreeController(delegateTreeControllerConfig{
		store:         store,
		rootSessionID: rootSessionID,
		stateDir:      stateDir,
		turnLimit:     2,
		driveLimit:    1,
		now:           func() time.Time { return time.Unix(100, 0).UTC() },
	})
	if err != nil {
		_ = store.Close()
		t.Fatalf("open controller: %v", err)
	}
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	if err := store.Close(); err != nil {
		t.Fatalf("close seeded delegate store: %v", err)
	}

	const callID = "cold-replay-before-provider"
	path := transcriptPath(stateDir, rootSessionID)
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: rootSessionID})
	if err != nil {
		t.Fatalf("create caller transcript: %v", err)
	}
	result := schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed(callID, "delegate_send", "done", false))
	result.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{
		ToolCallID: callID,
		DeliveryID: delegateDeliveryID("dlg_target", 1),
	}}
	if err := writer.AppendDurable(result); err != nil {
		_ = writer.Close()
		t.Fatalf("append caller delivery commit: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close caller transcript: %v", err)
	}

	var probeErr error
	adapter := &delegateAttentionLiveModelsAdapter{
		onList: func() {
			events, err := delegatestore.ReadEvents(storePath)
			if err != nil {
				probeErr = err
				return
			}
			state, err := delegatestore.Fold(events)
			if err != nil {
				probeErr = err
				return
			}
			if aggregate := state["dlg_target"]; aggregate == nil || len(aggregate.PendingDeliveries) != 0 {
				probeErr = fmt.Errorf("provider metadata observed unreconciled delivery: %#v", aggregate)
			}
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	restored, err := RestoreSessionFromMetaWithConfig(
		client,
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		schema.SessionMeta{ID: rootSessionID, ProfileID: "openai", Model: "gpt-5.2", CreatedAt: time.Unix(100, 0).UTC()},
		RestoreSessionConfig{
			StateDir: stateDir,
			testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
		},
	)
	if err != nil {
		t.Fatalf("restore session: %v", err)
	}
	t.Cleanup(restored.Close)
	if adapter.listCalls != 1 {
		t.Fatalf("provider metadata calls = %d, want 1 after cold replay", adapter.listCalls)
	}
	if probeErr != nil {
		t.Fatal(probeErr)
	}
}

func TestDelegateAttention_RestartReplayRequiresDeliveryIDFromExactPair(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	plans := c.ReplayDeliveries()
	if len(plans) != 1 {
		t.Fatalf("ReplayDeliveries = %#v", plans)
	}

	path := transcriptPath(c.stateDir, "root-session")
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: "root-session"})
	if err != nil {
		t.Fatalf("create caller transcript: %v", err)
	}
	turn := schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("delegate-call", "delegate_send", "done", false))
	turn.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{
		ToolCallID: "delegate-call",
		DeliveryID: "dlg_other/delivery/1",
	}}
	if err := writer.AppendDurable(turn); err != nil {
		_ = writer.Close()
		t.Fatalf("append caller delivery commit: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close caller transcript: %v", err)
	}

	pending, err := prepareColdDelegateDeliveryReplay(c, plans)
	if err != nil {
		t.Fatalf("prepare cold replay: %v", err)
	}
	if len(pending) != 1 || pending[0].deliveryID != plans[0].deliveryID {
		t.Fatalf("cold replay accepted mismatched delivery pair: %#v", pending)
	}
	c.mu.Lock()
	durable := append([]delegatestore.PendingDelivery(nil), c.durable["dlg_target"].PendingDeliveries...)
	c.mu.Unlock()
	if len(durable) != 1 || durable[0].DeliveryID != plans[0].deliveryID {
		t.Fatalf("mismatched pair acknowledged source head: %#v", durable)
	}
}

func TestDelegateAttention_ColdReplayRefoldsNextSameOwnerCommit(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 3, 1)
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child_a", "dlg_parent")
	seedDelegateControllerIdle(t, c, "dlg_child_b", "dlg_parent")
	seedDelegateControllerDelivery(t, c, "dlg_child_a")
	seedDelegateControllerDelivery(t, c, "dlg_child_b")

	path := transcriptPath(c.stateDir, "child-dlg_parent")
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: "child-dlg_parent"})
	if err != nil {
		t.Fatalf("create nested caller transcript: %v", err)
	}
	message := llm.ToolResultNamed("call-a", "delegate_send", "first", false)
	second := llm.ToolResultNamed("call-b", "delegate_send", "second", false)
	message.Content = append(message.Content, second.Content...)
	results := schema.NewTurn(schema.TurnToolResults, message)
	results.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{
		{ToolCallID: "call-a", DeliveryID: delegateDeliveryID("dlg_child_a", 1)},
		{ToolCallID: "call-b", DeliveryID: delegateDeliveryID("dlg_child_b", 1)},
	}
	if err := writer.AppendDurable(results); err != nil {
		_ = writer.Close()
		t.Fatalf("append nested caller commits: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close nested caller transcript: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read nested caller transcript: %v", err)
	}

	plans := c.ReplayDeliveries()
	if len(plans) != 1 || plans[0].ownerDelegateID != "dlg_parent" {
		t.Fatalf("serialized cold replay plans = %#v", plans)
	}
	pending, err := prepareColdDelegateDeliveryReplay(c, plans)
	if err != nil {
		t.Fatalf("prepare cold replay: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("already-committed successor remained pending: %#v", pending)
	}
	c.mu.Lock()
	remainingA := len(c.durable["dlg_child_a"].PendingDeliveries)
	remainingB := len(c.durable["dlg_child_b"].PendingDeliveries)
	c.mu.Unlock()
	if remainingA != 0 || remainingB != 0 {
		t.Fatalf("cold committed deliveries remain: child_a=%d child_b=%d", remainingA, remainingB)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reread nested caller transcript: %v", err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("cold commit replay mutated the nested caller transcript")
	}
}

func TestDelegateAttention_ColdReplayRefoldsMixedSameOwnerCommits(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 3, 1)
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child_a", "dlg_parent")
	seedDelegateControllerIdle(t, c, "dlg_child_b", "dlg_parent")
	seedDelegateControllerDelivery(t, c, "dlg_child_a")
	seedDelegateControllerDelivery(t, c, "dlg_child_b")

	path := transcriptPath(c.stateDir, "child-dlg_parent")
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: "child-dlg_parent"})
	if err != nil {
		t.Fatalf("create nested caller transcript: %v", err)
	}
	results := schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("call-b", "delegate_send", "second", false))
	results.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{
		ToolCallID: "call-b",
		DeliveryID: delegateDeliveryID("dlg_child_b", 1),
	}}
	if err := writer.AppendDurable(results); err != nil {
		_ = writer.Close()
		t.Fatalf("append second caller commit: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close nested caller transcript: %v", err)
	}

	plans := c.ReplayDeliveries()
	if len(plans) != 1 || plans[0].delegateID != "dlg_child_a" {
		t.Fatalf("initial serialized cold replay plans = %#v", plans)
	}
	pending, err := prepareColdDelegateDeliveryReplay(c, plans)
	if err != nil {
		t.Fatalf("prepare mixed cold replay: %v", err)
	}
	if len(pending) != 1 || pending[0].delegateID != "dlg_child_a" {
		t.Fatalf("mixed cold replay pending = %#v, want only uncommitted child A", pending)
	}
	pump := &Session{delegateController: c}
	c.rootRuntime = pump
	if err := pump.executeDelegateMutationPlans(delegateMutationPlans{deliveries: pending}); err != nil {
		t.Fatalf("flush mixed cold replay: %v", err)
	}

	fold, err := readDelegateAttentionFold(path, "child-dlg_parent")
	if err != nil {
		t.Fatalf("read mixed cold replay fold: %v", err)
	}
	aID := delegateAttentionID(delegateDeliveryID("dlg_child_a", 1))
	bID := delegateAttentionID(delegateDeliveryID("dlg_child_b", 1))
	if _, exists := fold.content[bID]; exists {
		t.Fatalf("already-committed child B gained duplicate attention %q", bID)
	}
	if pendingIDs := fold.pendingIDs(); !reflect.DeepEqual(pendingIDs, []string{aID}) {
		t.Fatalf("mixed cold replay pending attention = %#v, want only child A", pendingIDs)
	}
	c.mu.Lock()
	remainingA := len(c.durable["dlg_child_a"].PendingDeliveries)
	remainingB := len(c.durable["dlg_child_b"].PendingDeliveries)
	c.mu.Unlock()
	if remainingA != 0 || remainingB != 0 {
		t.Fatalf("mixed cold replay retained source deliveries: child_a=%d child_b=%d", remainingA, remainingB)
	}
}

func TestDelegateAttention_LiveReplayHonorsDurableCallerCommit(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")

	const sessionID = "root-session"
	path := transcriptPath(c.stateDir, sessionID)
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("create caller transcript: %v", err)
	}
	root := &Session{id: sessionID, stateDir: c.stateDir, delegateController: c}
	root.attachTranscript(writer)
	t.Cleanup(func() { _ = root.closeAttachedTranscript() })
	c.rootRuntime = root

	deliveryID := delegateDeliveryID("dlg_target", 1)
	results := schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("delegate-call", "delegate_send", "done", false))
	results.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{
		ToolCallID: "delegate-call",
		DeliveryID: deliveryID,
	}}
	if err := root.writeTranscriptDurable(results); err != nil {
		t.Fatalf("append durable caller commit: %v", err)
	}

	plans := c.ReplayDeliveries()
	if len(plans) != 1 {
		t.Fatalf("live replay plans = %#v, want one", plans)
	}
	if err := root.executeDelegateMutationPlans(delegateMutationPlans{deliveries: plans}); err != nil {
		t.Fatalf("execute live replay: %v", err)
	}

	fold, err := readDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("read live caller fold: %v", err)
	}
	if _, exists := fold.content[delegateAttentionID(deliveryID)]; exists {
		t.Fatalf("durably committed live delivery %q gained duplicate attention", deliveryID)
	}
	c.mu.Lock()
	remaining := len(c.durable["dlg_target"].PendingDeliveries)
	c.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("durably committed live delivery remained pending: %d", remaining)
	}
	root.mu.Lock()
	wakeIDs := len(notificationTestPending(t, root))
	wakeArmed := root.notificationSchedule.hinted
	root.mu.Unlock()
	if wakeIDs != 0 || wakeArmed {
		t.Fatalf("durably committed live delivery armed phantom root attention: ids=%d armed=%t", wakeIDs, wakeArmed)
	}
}

func TestDelegateAttention_PostAckArmReadFailureRetriesExactID(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	clock := agenttest.NewFakeClock()
	root := newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1, clock: clock}),
		withSteps(func(llm.Request) llm.Response {
			return communicateResponse(true, "retried exact attention")
		}),
	)
	c := root.delegateController
	created := delegateControllerCreatedEvent("dlg_target", "")
	created.Created.Descriptor.OwnerSessionID = root.ID()
	childID := created.Created.Descriptor.ChildSessionID
	childWriter, err := transcript.NewWriter(transcriptPath(stateDir, childID), transcript.Header{SessionID: childID})
	if err != nil {
		t.Fatal(err)
	}
	if err := childWriter.Close(); err != nil {
		t.Fatal(err)
	}
	c.mu.Lock()
	_, err = c.appendLocked(created)
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("seed target delegate: %v", err)
	}
	seedDelegateControllerDelivery(t, c, "dlg_target")
	plans := c.ReplayDeliveries()
	if len(plans) != 1 {
		t.Fatalf("delivery plans = %#v, want one", plans)
	}

	injected := errors.New("injected post-ack attention fold failure")
	root.cfg.testOnly.delegateAttentionReadFold = func(path, sessionID string) (delegateAttentionFold, error) {
		// Fail every verification after source acknowledgment until recovery,
		// rather than depending on the number of best-effort fold reads.
		if !c.deliveryPending("dlg_target", plans[0].deliveryID) {
			return delegateAttentionFold{}, injected
		}
		return readDelegateAttentionFold(path, sessionID)
	}
	wakes := make(chan struct{}, 2)
	root.SetNotifyFunc(func() { wakes <- struct{}{} })
	retryTimersBefore := clock.BlockedCount()
	root.mu.Lock()
	receiverRetryBefore := root.notificationSchedule.retry.generation
	root.mu.Unlock()
	err = root.executeDelegateMutationPlans(delegateMutationPlans{deliveries: plans})
	if !errors.Is(err, injected) {
		t.Fatalf("delivery error = %v, want post-ack fold failure", err)
	}
	c.mu.Lock()
	remaining := len(c.durable["dlg_target"].PendingDeliveries)
	receipts := len(c.deliveries)
	c.mu.Unlock()
	if remaining != 0 || receipts != 0 {
		t.Fatalf("source after injected arm failure = pending:%d receipts:%d, want acknowledged and released", remaining, receipts)
	}
	select {
	case <-wakes:
		t.Fatal("post-ack fold failure emitted an unverified wake")
	default:
	}
	root.mu.Lock()
	receiverRetry := root.notificationSchedule.retry
	root.mu.Unlock()
	root.delegateDeliveryMu.Lock()
	transportRetry := root.delegateDeliveryRetry
	root.delegateDeliveryMu.Unlock()
	if !receiverRetry.active || receiverRetry.generation != receiverRetryBefore+1 || !transportRetry.active {
		t.Fatalf("post-ack recovery receiver=%+v transport=%+v, want one receiver retry and transport replay hint", receiverRetry, transportRetry)
	}
	if got := clock.BlockedCount(); got != retryTimersBefore+2 {
		t.Fatalf("post-ack recovery timers = %d, want baseline %d plus receiver retry and transport replay hint", got, retryTimersBefore)
	}
	root.cfg.testOnly.delegateAttentionReadFold = nil
	clock.Advance(jobNotificationRetryInitialDelay)
	<-wakes
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("consume retried exact attention: %v", err)
	}
	attentionID := delegateAttentionID(delegateDeliveryID("dlg_target", 1))
	fold, err := readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read retried attention fold: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != delegateAttentionConsumed {
		t.Fatalf("retried attention resolution = %q, want consumed", got)
	}
}

func TestDelegateAttention_ColdPostAckReadFailureRetriesExactID(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	clock := agenttest.NewFakeClock()
	reached := make(chan llm.Request, 1)
	fixture.adapter.steps = []func(llm.Request) llm.Response{func(req llm.Request) llm.Response {
		reached <- req
		return finalResponse("cold read recovery handled")
	}}
	root := restoreSupervisionRoot(t, fixture, clock)
	c := root.delegateController
	const attentionID = "watch:cold-post-ack:1"
	const body = "cold-post-ack-body-13ae"
	path := transcriptPath(c.stateDir, fixture.childID)
	if _, err := appendColdDelegateAttentionMessageDurablyWithOpen(path, fixture.childID, attentionID, llm.User(body), clock.Now(), c.attentionOpen); err != nil {
		t.Fatal(err)
	}
	retryTimersBefore := clock.BlockedCount()
	backup := path + ".readable"
	if err := os.Rename(path, backup); err != nil {
		t.Fatalf("hide cold attention transcript: %v", err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		_ = os.Rename(backup, path)
		t.Fatalf("install cold attention read fault: %v", err)
	}
	err := c.armColdDelegateAttention(fixture.delegateID, attentionID)
	if err == nil {
		t.Fatal("cold post-ack attention fold unexpectedly succeeded")
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove cold attention read fault: %v", err)
	}
	if err := os.Rename(backup, path); err != nil {
		t.Fatalf("restore cold attention transcript: %v", err)
	}
	if got := clock.BlockedCount(); got != retryTimersBefore {
		t.Fatalf("cold fold failure created a second arm mirror timer: got %d, want baseline %d", got, retryTimersBefore)
	}
	c.mu.Lock()
	unverified := len(attentionWakeIDsOf(c, fixture.delegateID))
	c.mu.Unlock()
	if root.sessionWorkPending() || unverified != 0 || len(fixture.adapter.Requests()) != 0 {
		t.Fatalf("failed cold fold published unverified work: root=%t unresolved=%d requests=%d", root.sessionWorkPending(), unverified, len(fixture.adapter.Requests()))
	}
	if err := c.armColdDelegateAttention(fixture.delegateID, attentionID); err != nil {
		t.Fatalf("caller retry cold attention: %v", err)
	}
	select {
	case req := <-reached:
		observationRequest(t, req, []string{body}, nil)
	case <-time.After(5 * time.Second):
		c.mu.Lock()
		generation := c.durable[fixture.delegateID].Generation
		receiver := c.live[fixture.delegateID].runtime
		c.mu.Unlock()
		receiver.mu.Lock()
		schedule := receiver.notificationSchedule
		receiver.mu.Unlock()
		t.Fatalf("caller retry did not reach the exact cold receiver's provider: generation=%d schedule=%+v", generation, schedule)
	}
	waitForStableSupervisionRun(t, root, fixture.childID)
	fold, err := readDelegateAttentionFold(path, fixture.childID)
	if err != nil {
		t.Fatal(err)
	}
	if _, observed := fold.observed[attentionID]; !observed || len(fold.pendingIDs()) != 0 {
		t.Fatal("caller retry did not successfully observe the exact durable notification")
	}
}

func TestDelegateAttention_SettledResidentChildStartsExactAttentionGeneration(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	attentionEntered := make(chan struct{})
	releaseAttention := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseAttention) }) })
	attentionContent := `<job-notification event="watch_send">drive exact attention</job-notification>`
	attentionSeen := false
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return communicateResponse(true, "initial run complete") },
		func(request llm.Request) llm.Response {
			attentionSeen = requestContainsText(request, attentionContent)
			close(attentionEntered)
			<-releaseAttention
			return communicateResponse(true, "attention handled")
		},
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	// The generation this test asserts is published by the attention wake's
	// start commit, on the owner's drive path. armDelegateAttention below does
	// not promise to have done that by the time it returns: it either opens
	// the wake itself or leaves it armed for that drive path, and both are
	// correct. Reading the aggregate straight after the arm therefore asserts
	// one of the two schedules rather than the rule, which is how this fixture
	// failed on a loaded runner (#1294). This observer is the edge the read
	// actually depends on -- it fires with the generation committed and the
	// run goroutine not yet started.
	attentionCommitted := make(chan struct{})
	var committedOnce sync.Once
	updateSessionTestConfig(root, func(cfg *testConfig) {
		cfg.delegateAttentionStartCommitted = func(*subagent) {
			committedOnce.Do(func() { close(attentionCommitted) })
		}
	})
	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "warm retained runtime", 60_000)
	if outcome.result.Err != nil || outcome.commit == nil {
		t.Fatalf("initial stable run = %#v", outcome)
	}
	plans, err := outcome.commit.Complete(true)
	if err != nil {
		t.Fatalf("acknowledge initial stable result: %v", err)
	}
	if err := root.executeDelegateMutationPlans(plans); err != nil {
		t.Fatalf("execute initial delivery acknowledgement: %v", err)
	}
	sub := root.subagents.get(fixture.childID)
	if sub == nil || sub.sess == nil {
		t.Fatal("initial stable run retained no resident child")
	}
	const attentionID = "watch:resident-attention:1"
	if appended, err := sub.sess.appendDelegateNotificationDurably(attentionID, attentionContent); err != nil || !appended {
		t.Fatalf("append resident attention = appended:%t err:%v", appended, err)
	}
	if err := sub.sess.requestNotificationWake(); err != nil {
		t.Fatalf("arm resident attention: %v", err)
	}
	<-attentionCommitted
	root.delegateController.mu.Lock()
	aggregate := root.delegateController.durable[fixture.delegateID]
	generation := aggregate.Generation
	trigger := aggregate.Trigger
	open := aggregate.CurrentRunOpen
	root.delegateController.mu.Unlock()
	if generation != 2 || trigger != delegatestore.TriggerAttention || !open {
		t.Fatalf("attention wake durable generation = generation:%d trigger:%q open:%t, want 2/attention/open", generation, trigger, open)
	}
	<-attentionEntered
	if !attentionSeen {
		t.Fatal("attention generation provider request omitted the exact durable attention")
	}
	releaseOnce.Do(func() { close(releaseAttention) })
	waitForStableSupervisionRun(t, root, fixture.childID)
	fold, err := readDelegateAttentionFold(transcriptPath(fixture.stateDir, fixture.childID), fixture.childID)
	if err != nil {
		t.Fatalf("read settled child attention: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != delegateAttentionConsumed {
		t.Fatalf("settled child attention resolution = %q, want consumed", got)
	}
}

func TestDelegateAttention_RestoreSessionStartCountsColdReplayAppend(t *testing.T) {
	stateDir := t.TempDir()
	const sessionID = "01ATTENTIONREPLAYCOUNT01"
	store, err := delegatestore.Open(filepath.Join(jobsDir(stateDir, sessionID), "delegates.jsonl"))
	if err != nil {
		t.Fatalf("open delegate store: %v", err)
	}
	c, err := openDelegateTreeController(delegateTreeControllerConfig{
		store:         store,
		rootSessionID: sessionID,
		stateDir:      stateDir,
		turnLimit:     2,
		driveLimit:    1,
		now:           func() time.Time { return time.Unix(100, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("open controller: %v", err)
	}
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	firstDeliveryID := delegateDeliveryID("dlg_target", 1)
	if err := store.Close(); err != nil {
		t.Fatalf("close delegate store: %v", err)
	}

	path := transcriptPath(stateDir, sessionID)
	writer, err := transcript.NewWriter(path, transcript.Header{
		SessionID: sessionID,
		ProfileID: "openai",
		Model:     "gpt-5.2",
	})
	if err != nil {
		t.Fatalf("create caller transcript: %v", err)
	}
	results := schema.NewTurn(schema.TurnToolResults, llm.ToolResultNamed("delegate-call", "delegate_send", "done", false))
	results.DelegateDeliveryCommits = []schema.DelegateDeliveryCommit{{ToolCallID: "delegate-call", DeliveryID: firstDeliveryID}}
	if err := writer.AppendDurable(results); err != nil {
		_ = writer.Close()
		t.Fatalf("append committed caller result: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close caller transcript: %v", err)
	}

	meta := schema.SessionMeta{
		ID:        sessionID,
		ProfileID: "openai",
		Model:     "gpt-5.2",
		Config: (SessionConfig{
			MaxConcurrentDelegateTurns: 2,
		}).toSnapshot(),
	}
	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(stateDir), meta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	start, ok := resumeTurnSeedFindSessionStart(t, restored)
	if !ok {
		t.Fatal("restored session emitted no SESSION_START")
	}
	_, entries, _, err := readTranscript(path, "")
	if err != nil {
		t.Fatalf("read restored transcript: %v", err)
	}
	if len(entries) != 2 || entries[1].Turn.AttentionID != delegateAttentionID(delegateDeliveryID("dlg_target", 2)) {
		t.Fatalf("restored transcript entries = %#v, want committed N plus N+1 attention", entries)
	}
	if start.TranscriptEntries != len(entries) {
		t.Fatalf("SESSION_START transcript entries = %d, durable entries after replay = %d", start.TranscriptEntries, len(entries))
	}
}

func TestDelegateAttention_StopDrainDoesNotResolveAttentionTwice(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	const (
		sessionID   = "child-dlg_target"
		attentionID = "attention-before-stop"
	)
	path := transcriptPath(c.stateDir, sessionID)
	writeDelegateAttentionTranscript(t, path, sessionID, attentionID)

	result := restoreHistoricalDelegateStop(t, c, "dlg_target")
	// This root is only the nonexecuting historical cleanup mutation executor.
	root := &Session{delegateController: c}
	if err := c.drainStop(context.Background(), c.stopForResult(result), root); err != nil {
		t.Fatalf("drain stop with root mutation executor: %v", err)
	}
	pending, err := readPendingDelegateAttention(path, sessionID)
	if err != nil {
		t.Fatalf("read attention after drain: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending attention after drain = %#v", pending)
	}
	resolutions := 0
	for _, entry := range readAttentionTranscriptEntries(t, path) {
		if entry.Turn.Kind == schema.TurnAttentionResolution {
			resolution := entry.Turn.AttentionResolution
			if resolution == nil || resolution.AttentionID != attentionID || resolution.Disposition != string(delegateAttentionDiscarded) {
				t.Fatalf("unexpected historical attention resolution: %+v", resolution)
			}
			resolutions++
		}
	}
	if resolutions != 1 {
		t.Fatalf("historical attention resolutions=%d, want exactly one", resolutions)
	}
}

func TestDelegateAttention_BootstrapFlushRoutesNestedDeliveryToOwner(t *testing.T) {
	const (
		rootSessionID  = "root-session"
		ownerSessionID = "child-dlg_parent"
		body           = "nested-owner-payload-6a93"
	)
	stateDir := t.TempDir()
	store, err := delegatestore.Open(delegateResourceStorePath(stateDir, rootSessionID))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	c, err := openDelegateTreeController(delegateTreeControllerConfig{store: store, rootSessionID: rootSessionID, stateDir: stateDir, turnLimit: 2, driveLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	for _, item := range []struct{ id, parent, owner string }{{"dlg_parent", "", rootSessionID}, {"dlg_child", "dlg_parent", ownerSessionID}} {
		created := delegateControllerCreatedEvent(item.id, item.parent)
		d := &created.Created.Descriptor
		d.OwnerSessionID, d.ResolvedProfileID, d.ResolvedModel = item.owner, "openai", "gpt-5.2"
		d.WorkingDir, d.LocalEnvPolicy = workspace, "default"
		c.mu.Lock()
		_, err := c.appendLocked(created)
		c.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		writeEmptyAttentionTranscript(t, transcriptPath(c.stateDir, d.ChildSessionID), d.ChildSessionID)
		if err := schema.SaveSessionMeta(c.stateDir, schema.SessionMeta{ID: d.ChildSessionID, ParentSessionID: item.owner, ProfileID: "openai", Model: "gpt-5.2", IsSubagent: true}); err != nil {
			t.Fatal(err)
		}
	}
	lease := delegateLease{delegateID: "dlg_child", generation: 1}
	c.mu.Lock()
	_, err = c.appendLocked(
		delegateControllerRunStartedEvent("dlg_child", 1, delegatestore.TriggerOwnerInput, c.now()),
		delegatestore.Event{Kind: delegatestore.EventDelegateTerminalPrepared, DelegateID: "dlg_child", TerminalPrepared: &delegatestore.TerminalPrepared{Generation: 1, Packet: delegateControllerReportedPacket(body)}},
		delegateRunFinishedEvent(lease, delegatestore.OutcomeCompleted, delegatestore.DispositionReported, "", c.now(), delegateDeliveryID("dlg_child", 1), nil),
	)
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	plans := c.ReplayDeliveries()
	if len(plans) != 1 || plans[0].ownerDelegateID != "dlg_parent" {
		t.Fatalf("nested delivery plans = %#v", plans)
	}
	attentionID := delegateAttentionID(plans[0].deliveryID)
	if err := c.store.Close(); err != nil {
		t.Fatal(err)
	}
	rootPath, ownerPath := transcriptPath(c.stateDir, rootSessionID), transcriptPath(c.stateDir, ownerSessionID)
	writeEmptyAttentionTranscript(t, rootPath, rootSessionID)
	entered, release := make(chan llm.Request, 1), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{func(req llm.Request) llm.Response {
		entered <- req
		<-release
		return finalResponse("nested observed")
	}}}
	client := llm.NewClient()
	client.Register(adapter)
	profile := withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2"))
	meta := schema.SessionMeta{
		ID: rootSessionID, ProfileID: "openai", Model: "gpt-5.2", Config: schema.ConfigSnapshot{MaxSubagentDepth: 2},
	}
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatal(err)
	}
	root, err := RestoreSessionFromMetaWithConfig(client, profile, execenv.NewLocalExecutionEnvironment(workspace), meta, RestoreSessionConfig{StateDir: c.stateDir, ForceRealIO: true, testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, sandboxProber: bwrapCapableProber(workspace), disableDelegateIdleRelease: true}})
	if err != nil {
		unblock()
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	started := false
	defer func() {
		unblock()
		if started {
			waitForStableSupervisionRun(t, root, ownerSessionID)
			root.sendersWG.Wait()
		}
	}()
	var req llm.Request
	select {
	case req = <-entered:
		started = true
	// TRIPWIRE: bounds a missing automatic nested-owner wake, not ordering.
	case <-time.After(10 * time.Second):
		t.Fatal("nested replay did not automatically launch owner")
	}
	ownerFold, err := readDelegateAttentionFold(ownerPath, ownerSessionID)
	if err != nil {
		t.Fatalf("read owner attention: %v", err)
	}
	if pending := ownerFold.pendingIDs(); !reflect.DeepEqual(pending, []string{attentionID}) {
		t.Fatalf("owner pending attention = %#v, want %q", pending, attentionID)
	}
	message := ownerFold.content[attentionID]
	if !strings.Contains(message.Text(), body) {
		t.Fatal("nested owner custody lost original opaque body")
	}
	found := 0
	for _, m := range req.Messages {
		if reflect.DeepEqual(m, message) {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("canonical owner request original message occurrences=%d, want 1", found)
	}
	root.delegateController.mu.Lock()
	runtime := root.delegateController.live["dlg_parent"].runtime
	remaining := len(root.delegateController.durable["dlg_child"].PendingDeliveries)
	claims := len(root.delegateController.deliveryClaims)
	root.delegateController.mu.Unlock()
	if runtime == nil || runtime.subagents == nil || root.subagents == nil || root.jobManager == nil || remaining != 0 || claims != 0 {
		t.Fatalf("initialized nested replay runtime=%p pending=%d claims=%d", runtime, remaining, claims)
	}
	journal, err := root.delegateController.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	acks := 0
	for _, event := range journal {
		if event.Kind == delegatestore.EventDelegateDeliveryAcknowledged && event.DelegateID == "dlg_child" && event.DeliveryAcknowledged.DeliveryID == plans[0].deliveryID {
			acks++
		}
	}
	if acks != 1 {
		t.Fatalf("nested source durable acknowledgments=%d want 1", acks)
	}
	assertAttentionResponseReceipts(t, runtime, attentionID, 0)
	rootFold, err := readDelegateAttentionFold(rootPath, rootSessionID)
	if err != nil {
		t.Fatalf("read root attention: %v", err)
	}
	if _, leaked := rootFold.content[attentionID]; leaked || len(rootFold.pendingIDs()) != 0 {
		t.Fatalf("nested delivery leaked to root attention: %#v", rootFold.pendingIDs())
	}
	unblock()
	waitForStableSupervisionRun(t, root, ownerSessionID)
	root.sendersWG.Wait()
	assertAttentionResponseReceipts(t, runtime, attentionID, 1)
	if supervisionRequestCount(adapter) != 1 {
		t.Fatal("nested replay did not execute exactly one canonical request")
	}
	canonical := 0
	for _, e := range readAttentionTranscriptEntries(t, ownerPath) {
		if e.Turn.AttentionID == attentionID {
			canonical++
			if !reflect.DeepEqual(e.Turn.Message, message) {
				t.Fatal("nested message changed across response")
			}
		}
	}
	if canonical != 1 {
		t.Fatalf("nested canonical records=%d want 1", canonical)
	}
}

func TestDelegateAttention_ColdDeliveryClaimFencesOwnerRestore(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
	seedDelegateControllerDelivery(t, c, "dlg_child")

	plans := c.ReplayDeliveries()
	if len(plans) != 1 || plans[0].ownerDelegateID != "dlg_parent" {
		t.Fatalf("cold owner delivery plans = %#v", plans)
	}
	reservation, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_parent")
	if err != nil {
		t.Fatalf("ReserveStart owner: %v", err)
	}
	if _, err := c.CommitStart(reservation); !errors.Is(err, errDelegateTargetBusy) {
		t.Fatalf("CommitStart with cold delivery claim = %v, want target busy", err)
	}
	c.mu.Lock()
	aggregate := c.durable["dlg_parent"]
	phase, generation := aggregate.Phase, aggregate.Generation
	c.mu.Unlock()
	if phase != delegatestore.PhaseIdle || generation != 0 {
		t.Fatalf("owner mutated across cold delivery claim: phase=%s generation=%d", phase, generation)
	}
}

func TestDelegateAttention_RestoringOwnerDefersColdDeliveryUntilRuntimeAttach(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
	seedDelegateControllerDelivery(t, c, "dlg_child")

	reservation, err := c.ReserveStart(rootDelegateActor("root-session"), "dlg_parent")
	if err != nil {
		t.Fatalf("ReserveStart owner: %v", err)
	}
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("CommitStart owner: %v", err)
	}
	if plans := c.ReplayDeliveries(); len(plans) != 0 {
		t.Fatalf("delivery entered cold owner transcript during restore: %#v", plans)
	}

	ownerPath := transcriptPath(c.stateDir, "child-dlg_parent")
	ownerWriter, err := transcript.NewWriter(ownerPath, transcript.Header{SessionID: "child-dlg_parent"})
	if err != nil {
		t.Fatalf("create owner transcript: %v", err)
	}
	t.Cleanup(func() { _ = ownerWriter.Close() })
	owner := &Session{id: "child-dlg_parent", stateDir: c.stateDir, delegateController: c}
	owner.attachTranscript(ownerWriter)
	if err := attachRuntimeWithHandoffForTest(c, started.lease, owner); err != nil {
		t.Fatalf("AttachRuntime owner: %v", err)
	}
	claim, err := c.BeginStartInput(started.lease)
	if err != nil {
		t.Fatalf("BeginStartInput owner: %v", err)
	}
	plans, err := c.CompleteStartInput(claim, true, delegateFinish{})
	if err != nil {
		t.Fatalf("CompleteStartInput owner: %v", err)
	}
	if len(plans.deliveries) != 1 || plans.deliveries[0].receiver != owner || plans.deliveries[0].ownerDelegateID != "dlg_parent" {
		t.Fatalf("post-attach owner deliveries = %#v", plans.deliveries)
	}
}

func TestDelegateAttention_ColdOwnerSerializesTranscriptDeliveries(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 3, 1)
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child_a", "dlg_parent")
	seedDelegateControllerIdle(t, c, "dlg_child_b", "dlg_parent")
	seedDelegateControllerDelivery(t, c, "dlg_child_a")
	seedDelegateControllerDelivery(t, c, "dlg_child_b")

	ownerPath := transcriptPath(c.stateDir, "child-dlg_parent")
	ownerWriter, err := transcript.NewWriter(ownerPath, transcript.Header{SessionID: "child-dlg_parent"})
	if err != nil {
		t.Fatalf("create owner transcript: %v", err)
	}
	if err := ownerWriter.Close(); err != nil {
		t.Fatalf("close owner transcript: %v", err)
	}

	plans := c.ReplayDeliveries()
	if len(plans) != 1 {
		t.Fatalf("simultaneous cold owner plans = %#v, want one receiver receipt", plans)
	}
	next, err := deliverDelegatePacket(plans[0], plans[0].receiver)
	if err != nil {
		t.Fatalf("deliver first cold packet: %v", err)
	}
	if len(next.deliveries) != 1 || next.deliveries[0].ownerDelegateID != "dlg_parent" {
		t.Fatalf("next cold owner delivery = %#v", next.deliveries)
	}
	if _, err := deliverDelegatePacket(next.deliveries[0], next.deliveries[0].receiver); err != nil {
		t.Fatalf("deliver second cold packet: %v", err)
	}
	fold, err := readDelegateAttentionFold(ownerPath, "child-dlg_parent")
	if err != nil {
		t.Fatalf("read owner transcript: %v", err)
	}
	if got := len(fold.pendingIDs()); got != 2 {
		t.Fatalf("serialized cold owner attention count = %d, want 2", got)
	}
}

func TestDelegateAttention_FailedFlushRetainsDeliveryForLiveRetry(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")

	path := transcriptPath(c.stateDir, "root-session")
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: "root-session"})
	if err != nil {
		t.Fatalf("create root transcript: %v", err)
	}
	root := &Session{id: "root-session", stateDir: c.stateDir, delegateController: c}
	root.attachTranscript(writer)
	c.rootRuntime = root
	plans := c.ReplayDeliveries()
	if len(plans) != 1 {
		t.Fatalf("ReplayDeliveries = %#v", plans)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close transcript writer: %v", err)
	}
	if err := root.executeDelegateMutationPlans(delegateMutationPlans{deliveries: plans}); err == nil {
		t.Fatal("flush with closed transcript writer succeeded")
	}
	replay := c.ReplayDeliveries()
	if len(replay) != 1 || replay[0].deliveryID != plans[0].deliveryID || replay[0].claim == plans[0].claim {
		t.Fatalf("source retry=%#v", replay)
	}
	c.mu.Lock()
	pending := append([]delegatestore.PendingDelivery(nil), c.durable["dlg_target"].PendingDeliveries...)
	c.mu.Unlock()
	if len(pending) != 1 || pending[0].DeliveryID != plans[0].deliveryID {
		t.Fatalf("durable delivery after failed flush = %#v", pending)
	}
}

func TestDelegateAttention_FailedMutationQueueRetainsOtherOwners(t *testing.T) {
	stateDir, workspace := t.TempDir(), t.TempDir()
	entered, release := make(chan llm.Request, 1), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{func(req llm.Request) llm.Response {
		entered <- req
		<-release
		return finalResponse("nested observed")
	}}}
	client := llm.NewClient()
	client.Register(adapter)
	profile := withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2"))
	meta := schema.SessionMeta{ID: "root-session", ProfileID: "openai", Model: "gpt-5.2", Config: schema.ConfigSnapshot{MaxSubagentDepth: 2}}
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatal(err)
	}
	writeEmptyAttentionTranscript(t, transcriptPath(stateDir, meta.ID), meta.ID)
	clock := agenttest.NewFakeClock()
	root, err := RestoreSessionFromMetaWithConfig(client, profile, execenv.NewLocalExecutionEnvironment(workspace), meta, RestoreSessionConfig{StateDir: stateDir, ForceRealIO: true, clock: clock, testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, sandboxProber: bwrapCapableProber(workspace), disableDelegateIdleRelease: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(root.Close)
	started := false
	defer func() {
		unblock()
		if started {
			waitForStableSupervisionRun(t, root, "child-dlg_parent")
			root.sendersWG.Wait()
		}
	}()
	c := root.delegateController
	for _, item := range []struct{ id, parent, owner string }{{"dlg_a_root", "", meta.ID}, {"dlg_parent", "", meta.ID}, {"dlg_z_child", "dlg_parent", "child-dlg_parent"}} {
		created := delegateControllerCreatedEvent(item.id, item.parent)
		d := &created.Created.Descriptor
		d.OwnerSessionID, d.ResolvedProfileID, d.ResolvedModel = item.owner, "openai", "gpt-5.2"
		d.WorkingDir, d.LocalEnvPolicy = workspace, "default"
		c.mu.Lock()
		_, err := c.appendLocked(created)
		c.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		writeEmptyAttentionTranscript(t, transcriptPath(stateDir, d.ChildSessionID), d.ChildSessionID)
		if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{ID: d.ChildSessionID, ParentSessionID: item.owner, ProfileID: "openai", Model: "gpt-5.2", IsSubagent: true}); err != nil {
			t.Fatal(err)
		}
	}
	seedDelegateControllerDelivery(t, c, "dlg_a_root")
	seedDelegateControllerDelivery(t, c, "dlg_z_child")

	fs := newAttentionFailNextSyncFS()
	original := root.transcript
	if err := original.Close(); err != nil {
		t.Fatal(err)
	}
	rootWriter, err := transcript.NewWriterWithFS(fs, transcriptPath(stateDir, meta.ID), transcript.Header{SessionID: meta.ID})
	if err != nil {
		t.Fatal(err)
	}
	root.attachTranscript(rootWriter)
	wakes := make(chan struct{}, 1)
	root.SetNotifyFunc(func() {
		select {
		case wakes <- struct{}{}:
		default:
		}
	})
	ownerPath := transcriptPath(c.stateDir, "child-dlg_parent")
	plans := c.ReplayDeliveries()
	if len(plans) != 2 || plans[0].delegateID != "dlg_a_root" || plans[1].delegateID != "dlg_z_child" {
		t.Fatalf("multi-owner replay plans = %#v", plans)
	}
	// Keep this receiver unavailable across any internal writer health retry.
	// Only the filesystem fault is cleared below, never the transport itself.
	var rearm func()
	rearm = func() { fs.failNextSyncWith(rearm) }
	fs.failNextSyncWith(rearm)
	if err := root.executeDelegateMutationPlans(delegateMutationPlans{deliveries: plans}); !errors.Is(err, fs.syncErr) {
		t.Fatalf("multi-owner delivery error=%v, want injected source failure", err)
	}
	var req llm.Request
	select {
	case req = <-entered:
		started = true
	// TRIPWIRE: bounds a missing automatic owner wake, not ordering.
	case <-time.After(10 * time.Second):
		t.Fatal("unrelated owner's delivery did not automatically launch its runtime")
	}
	c.mu.Lock()
	rootPending, childPending := len(c.durable["dlg_a_root"].PendingDeliveries), len(c.durable["dlg_z_child"].PendingDeliveries)
	runtime := c.live["dlg_parent"].runtime
	c.mu.Unlock()
	if rootPending != 1 || childPending != 0 {
		t.Fatal("failed source lost its head or unrelated source did not transfer custody")
	}
	journal, err := c.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	var acknowledged []string
	for _, event := range journal {
		if event.Kind == delegatestore.EventDelegateDeliveryAcknowledged {
			acknowledged = append(acknowledged, event.DelegateID)
		}
	}
	if len(acknowledged) != 1 || acknowledged[0] != "dlg_z_child" {
		t.Fatalf("source journal acknowledgments = %v, want only dlg_z_child", acknowledged)
	}
	entries := notificationRawEntries(t, ownerPath)
	if len(entries) != 1 || entries[0].Turn.Kind != schema.TurnSteering || entries[0].Turn.AttentionID != "delegate:dlg_z_child/delivery/1" {
		t.Fatalf("unrelated owner's raw transcript = %+v, want exactly one canonical delivery", entries)
	}
	observed := 0
	for _, message := range req.Messages {
		if reflect.DeepEqual(message, entries[0].Turn.Message) {
			observed++
		}
	}
	if observed != 1 {
		t.Fatalf("unrelated owner request delivery occurrences=%d, want exactly one", observed)
	}
	assertAttentionResponseReceipts(t, runtime, "delegate:dlg_z_child/delivery/1", 0)
	unblock()
	waitForStableSupervisionRun(t, root, "child-dlg_parent")
	root.sendersWG.Wait()
	assertAttentionResponseReceipts(t, runtime, "delegate:dlg_z_child/delivery/1", 1)
	finished := latestDelegateControllerRunFinished(t, c, "dlg_parent")
	if finished.Outcome.Status != delegatestore.OutcomeCompleted || finished.Disposition != delegatestore.DispositionReported {
		t.Errorf("unrelated generation finish=%+v, want completed/reported despite failed sibling source", finished)
	}
	if supervisionRequestCount(adapter) != 1 {
		t.Fatal("unrelated owner did not execute exactly one canonical request")
	}
	if !root.delegateDeliveryResiduePending() {
		t.Fatal("failed source has no automatic root-pump retry")
	}
	// The child completion may already have notified the root. Its runner and
	// senders are joined, so consume that edge while the failed source is still
	// unavailable. The assertion below must observe a new automatic-retry edge.
	select {
	case <-wakes:
	default:
	}
	fs.mu.Lock()
	fs.failNext, fs.onFail = false, nil
	fs.mu.Unlock()
	clock.Advance(jobNotificationRetryInitialDelay)
	clock.Drain()
	if got := pendingDeliveryIDs(c, "dlg_a_root"); len(got) != 0 {
		t.Fatalf("automatic root retry left failed source owed: %v", got)
	}
	rootEntries := notificationRawEntries(t, transcriptPath(stateDir, meta.ID))
	recovered := 0
	for _, entry := range rootEntries {
		if entry.Turn.AttentionID == "delegate:dlg_a_root/delivery/1" {
			recovered++
		}
	}
	if recovered != 1 {
		t.Fatalf("recovered source custody records=%d, want exactly one", recovered)
	}
	select {
	case <-wakes:
	default:
		t.Fatal("automatic root retry did not wake recovered receiver")
	}
}

func TestDelegateAttention_InheritedReplayWithoutRootRuntimeRetainsRetry(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_source", "")
	seedDelegateControllerDelivery(t, c, "dlg_source")
	path := transcriptPath(c.stateDir, "root-session")
	writeEmptyAttentionTranscript(t, path, "root-session")
	child := &Session{id: "child-dlg_source", stateDir: c.stateDir, delegateController: c, clock: agenttest.NewFakeClock(), cfg: SessionConfig{spawn: spawnConfig{delegateController: c}}}
	if err := child.replayDelegateDeliveries(); !errors.Is(err, errDelegateDeliveryReceiverUnavailable) {
		t.Fatalf("missing-root replay error=%v, want unavailable receiver", err)
	}
	if len(pendingDeliveryIDs(c, "dlg_source")) != 1 || !child.delegateDeliveryResiduePending() {
		t.Fatal("missing root runtime silently orphaned the source pump")
	}
	entries := notificationRawEntries(t, path)
	if len(entries) != 0 {
		t.Fatalf("missing-root replay fabricated receiver custody=%+v", entries)
	}
}

func TestDelegateAttention_RootRestoreRejectsOtherOwnersInvalidTranscript(t *testing.T) {
	c, journalPath := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_owner", "")
	seedDelegateControllerIdle(t, c, "dlg_source", "dlg_owner")
	seedDelegateControllerDelivery(t, c, "dlg_source")
	writeEmptyAttentionTranscript(t, transcriptPath(c.stateDir, "root-session"), "root-session")
	writeEmptyAttentionTranscript(t, transcriptPath(c.stateDir, "child-dlg_owner"), "wrong-owner")
	writeEmptyAttentionTranscript(t, transcriptPath(c.stateDir, "child-dlg_source"), "child-dlg_source")
	copyDelegateJournalForBootstrap(t, c, journalPath)

	// Bootstrap's evidence fold has no typed identity error. Prove this header
	// is the refusing boundary by repairing only it and reaching a sentinel at
	// the existing post-bootstrap construction seam with the same configuration.
	afterBootstrap := errors.New("reached construction after global bootstrap")
	constructionCalls := 0
	cfg := RestoreSessionConfig{StateDir: c.stateDir, ForceRealIO: true, testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, sessionInitFault: func(point string) error {
		if point == "builtin_agents" {
			constructionCalls++
			return afterBootstrap
		}
		return nil
	}}}
	meta := schema.SessionMeta{ID: "root-session", ProfileID: "openai", Model: "gpt-5.2", Config: schema.ConfigSnapshot{MaxSubagentDepth: 2}}
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	client := newAskRestoreClient()
	restored, err := RestoreSessionFromMetaWithConfig(client, NewOpenAIProfile("gpt-5.2"), env, meta, cfg)
	if restored != nil {
		restored.Close()
		t.Fatal("root restore swallowed another owner's replay failure")
	}
	if err == nil || errors.Is(err, afterBootstrap) || constructionCalls != 0 {
		t.Fatalf("root restore error=%v, want global bootstrap identity refusal", err)
	}
	store, err := delegatestore.Open(filepath.Join(jobsDir(c.stateDir, "root-session"), "delegates.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	events, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Kind == delegatestore.EventDelegateDeliveryAcknowledged {
			t.Fatal("failed global source was acknowledged")
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	writeEmptyAttentionTranscript(t, transcriptPath(c.stateDir, "child-dlg_owner"), "child-dlg_owner")
	restored, err = RestoreSessionFromMetaWithConfig(client, NewOpenAIProfile("gpt-5.2"), env, meta, cfg)
	if restored != nil {
		restored.Close()
		t.Fatal("post-bootstrap sentinel unexpectedly restored a session")
	}
	if !errors.Is(err, afterBootstrap) || constructionCalls != 1 {
		t.Fatalf("valid-owner control error=%v construction calls=%d, want post-bootstrap sentinel once", err, constructionCalls)
	}
}

func TestDelegateAttention_InheritedRestoreRejectsInvalidOwnTranscript(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(fmt.Sprintf("malformed=%t", malformed), func(t *testing.T) {
			c, _ := newDelegateControllerTestHarness(t, 2, 1)
			seedDelegateControllerIdle(t, c, "dlg_owner", "")
			seedDelegateControllerIdle(t, c, "dlg_source", "dlg_owner")
			seedDelegateControllerDelivery(t, c, "dlg_source")
			const sessionID = "child-dlg_owner"
			path := transcriptPath(c.stateDir, sessionID)
			writeEmptyAttentionTranscript(t, path, "wrong-owner")
			if malformed {
				if err := os.WriteFile(path, []byte("not JSON\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			client := llm.NewClient()
			adapter := &fakeAdapter{name: "openai"}
			client.Register(adapter)
			restored, err := RestoreSessionFromMetaWithConfig(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), schema.SessionMeta{ID: sessionID, ProfileID: "openai", Model: "gpt-5.2", IsSubagent: true}, RestoreSessionConfig{StateDir: c.stateDir, spawn: spawnConfig{parentSessionID: "root-session", delegateController: c, delegateRootSessionID: "root-session", owningDelegateID: "dlg_owner"}, testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true}})
			if restored != nil {
				restored.Close()
				t.Fatal("invalid own transcript restored")
			}
			if malformed {
				var syntaxErr *json.SyntaxError
				if !errors.As(err, &syntaxErr) || syntaxErr.Offset != 2 {
					t.Fatalf("inherited restore error=%v, want own header JSON syntax error at byte 2", err)
				}
			} else {
				var mismatch *transcript.SessionIDMismatchError
				if !errors.As(err, &mismatch) || mismatch.HeaderSessionID != "wrong-owner" || mismatch.RequestedSessionID != sessionID {
					t.Fatalf("inherited restore error=%v, want own transcript identity refusal", err)
				}
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !bytes.Equal(before, after) || len(pendingDeliveryIDs(c, "dlg_source")) != 1 || supervisionRequestCount(adapter) != 0 {
				t.Fatal("invalid own transcript was changed, acknowledged, or sent to a model")
			}
		})
	}
}

func TestDelegateAttention_FailedTerminalDeliveryRetriesFromRootPump(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")

	fs := newAttentionFailNextSyncFS()
	rootPath := transcriptPath(c.stateDir, "root-session")
	rootWriter, err := transcript.NewWriterWithFS(fs, rootPath, transcript.Header{SessionID: "root-session"})
	if err != nil {
		t.Fatalf("create root transcript: %v", err)
	}
	t.Cleanup(func() { _ = rootWriter.Close() })
	clock := agenttest.NewFakeClock()
	root := &Session{id: "root-session", stateDir: c.stateDir, delegateController: c, events: make(chan events.SessionEvent, 16), profile: NewOpenAIProfile("gpt-5.2"), clock: clock}
	root.contextMgr = contextmgr.NewManager(root.profile, nil, cheapmodel.New(nil))
	root.sessionCtx, root.cancelFunc = context.WithCancel(context.Background())
	t.Cleanup(root.cancelFunc)
	root.attachTranscript(rootWriter)
	c.rootRuntime = root
	wakes := make(chan struct{}, 4)
	root.SetNotifyFunc(func() { wakes <- struct{}{} })
	child := &Session{id: "child-dlg_target", stateDir: c.stateDir, delegateController: c}

	lease, _ := startDelegateDeliveryGeneration(t, c, "dlg_target", false)
	plans := finishDelegateDeliveryGeneration(t, c, lease, "terminal result")
	if len(plans.deliveries) != 1 {
		t.Fatalf("terminal delivery plans = %#v", plans.deliveries)
	}
	fs.failNextSync()
	if err := child.executeDelegateMutationPlans(plans); err == nil {
		t.Fatal("terminal delivery unexpectedly survived injected owner transcript failure")
	}
	if child.delegateDeliveryResiduePending() || !root.delegateDeliveryResiduePending() {
		t.Fatal("source retry is not owned by root pump")
	}
	// The timer retries transport directly. It does not require a model turn.
	fs.failNextSync()
	clock.Advance(jobNotificationRetryInitialDelay)
	clock.Drain()
	if len(pendingDeliveryIDs(c, "dlg_target")) != 1 {
		t.Fatal("failed retry lost source head")
	}
	clock.Advance(2 * jobNotificationRetryInitialDelay)
	clock.Drain()
	if len(pendingDeliveryIDs(c, "dlg_target")) != 0 {
		t.Fatal("paced retry did not transfer custody")
	}
	select {
	case <-wakes:
	default:
		t.Fatal("successful retry did not wake receiver")
	}
}

func TestDelegateAttention_DeliveryDuringProcessingTakesCustodyAndArmsWake(t *testing.T) {
	newProcessingDelegateDeliveryFixture(t, agenttest.NewFakeClock())
}

func newProcessingDelegateDeliveryFixture(t *testing.T, clock *agenttest.FakeClock) *Session {
	t.Helper()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	// SetNotifyFunc also scans this resumable child, whose descriptor names
	// an existing transcript even though this delivery is bound for the root.
	childWriter, err := transcript.NewWriter(transcriptPath(c.stateDir, "child-dlg_target"), transcript.Header{SessionID: "child-dlg_target"})
	if err != nil {
		t.Fatalf("create child transcript: %v", err)
	}
	if err := childWriter.Close(); err != nil {
		t.Fatalf("close child transcript: %v", err)
	}
	path := transcriptPath(c.stateDir, "root-session")
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: "root-session"})
	if err != nil {
		t.Fatalf("create root transcript: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	root := &Session{id: "root-session", stateDir: c.stateDir, delegateController: c, state: SessionProcessing, clock: clock}
	root.attachTranscript(writer)
	c.rootRuntime = root
	var recoveries atomic.Int64
	root.cfg.testOnly.afterSessionWakeRecoveryScheduled = func(string, error) {
		// The counter remains safe even if a broken fixture lets this callback
		// escape cleanup, unlike a callback into the producer subtest's T.
		recoveries.Add(1)
	}
	t.Cleanup(func() {
		// Fence and join the controller before writer, store and directory
		// cleanup. Drain already dispatched callbacks as well as delayed ones.
		if err := c.Close(context.Background()); err != nil {
			t.Errorf("close processing fixture controller: %v", err)
		}
		clock.Drain()
		clock.Advance(jobNotificationRetryMaxDelay)
		clock.Drain()
		if got := clock.PendingCallbacks(); got != 0 {
			t.Errorf("processing fixture callbacks after Close=%d, want 0", got)
		}
		if got := recoveries.Load(); got != 0 {
			t.Errorf("processing fixture scheduled %d unexpected receiver recoveries", got)
		}
	})
	wakes := make(chan struct{}, 1)
	root.SetNotifyFunc(func() {
		select {
		case wakes <- struct{}{}:
		default:
		}
	})
	plans := c.ReplayDeliveries()
	if len(plans) != 1 {
		t.Fatalf("ReplayDeliveries = %#v", plans)
	}
	_, deferred, err := root.acceptDelegateDeliveryPlan(plans[0])
	if err != nil || deferred {
		t.Fatalf("accept processing delivery = deferred %v err %v", deferred, err)
	}
	select {
	case <-wakes:
	default:
		t.Fatal("processing deferral emitted no autonomous wake")
	}
	if !root.sessionWorkPending() {
		t.Fatal("processing deferral did not expose autonomous work")
	}
	ids, err := root.pendingNotificationIDs()
	if err != nil || len(ids) != 1 || ids[0] != delegateAttentionID(delegateDeliveryID("dlg_target", 1)) {
		t.Fatalf("processing delivery canonical custody: ids=%v err=%v", ids, err)
	}
	if pending := pendingDeliveryIDs(c, "dlg_target"); len(pending) != 0 {
		t.Fatalf("processing delivery retained source debt: %v", pending)
	}
	return root
}

func TestRootDelegateAttention_DurableAppendWaitsForSourceSettlementBeforeWake(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	root := newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}),
	)
	wakes := make(chan struct{}, 1)
	root.SetNotifyFunc(func() { wakes <- struct{}{} })

	const attentionID = "delegate:dlg_wake/delivery/1"
	if appended, err := root.appendDelegateNotificationDurably(attentionID, `<delegate-notification delegate_id="dlg_wake">done</delegate-notification>`); err != nil || !appended {
		t.Fatalf("append root attention = appended:%t err:%v", appended, err)
	}
	select {
	case <-wakes:
		t.Fatal("durable root attention woke before its source settled")
	default:
	}
	if err := root.requestNotificationWake(); err != nil {
		t.Fatalf("arm settled root attention: %v", err)
	}
	select {
	case <-wakes:
	default:
		t.Fatal("settled root attention emitted no autonomous wake")
	}
	if !root.sessionWorkPending() {
		t.Fatal("durable root attention is absent from root work-pending state")
	}
}

func TestRootDelegateAttention_SuccessfulNotificationConsumesExactIDs(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	const (
		attentionID = "delegate:dlg_consume/delivery/1"
		content     = `<delegate-notification delegate_id="dlg_consume">complete</delegate-notification>`
	)
	requestSawAttention := false
	root := newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}),
		withSteps(func(req llm.Request) llm.Response {
			requestSawAttention = requestContainsText(req, content)
			return toolCallResponse(communicateCall("root-attention-consumed", "completion received"))
		}),
	)
	if _, err := root.appendDelegateNotificationDurably(attentionID, content); err != nil {
		t.Fatalf("append root attention: %v", err)
	}
	if err := root.requestNotificationWake(); err != nil {
		t.Fatalf("arm root attention: %v", err)
	}
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("process root attention: %v", err)
	}
	if !requestSawAttention {
		t.Fatal("root notification turn did not deliver durable attention to the model")
	}
	fold, err := readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read root attention fold: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != delegateAttentionConsumed {
		t.Fatalf("root attention disposition = %q, want consumed", got)
	}
	if pending := fold.pendingIDs(); len(pending) != 0 {
		t.Fatalf("root attention remains pending after successful turn: %#v", pending)
	}
}

// A delivery appended and armed while a root turn is already running is
// presented to the model by that turn's later rounds. Resolving only the IDs
// snapshotted at turn begin leaves it pending, and the wake it armed then runs
// a whole notification turn whose request carries nothing new — the model
// answers an already-answered context and the user sees the closing message
// twice. The turn that already presented the item must consume it at finish.
func TestRootDelegateAttention_MidTurnArmedAttentionConsumedWithoutRedundantWake(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	const (
		firstID       = "delegate:dlg_midturn/delivery/1"
		firstContent  = `<delegate-notification delegate_id="dlg_midturn">first</delegate-notification>`
		secondID      = "delegate:dlg_midturn/delivery/2"
		secondContent = `<delegate-notification delegate_id="dlg_midturn">second lands mid-turn</delegate-notification>`
	)
	var root *Session
	var (
		calls            int
		roundTwoSawBoth  bool
		midTurnAppendErr error
		midTurnArmErr    error
	)
	root = newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}),
		withSteps(
			func(llm.Request) llm.Response {
				calls++
				// A second delivery lands while this turn is in flight — the
				// delegate-notification path appends and arms it against the
				// running turn, and the next round's request already carries it.
				if midTurnAppendErr == nil {
					var appended bool
					appended, midTurnAppendErr = root.appendDelegateNotificationDurably(secondID, secondContent)
					if midTurnAppendErr == nil && !appended {
						midTurnAppendErr = errors.New("mid-turn attention append was a no-op")
					}
				}
				if midTurnAppendErr == nil {
					midTurnArmErr = root.requestNotificationWake()
				}
				return toolCallResponse(llm.ToolCallData{ID: "round-one", Name: "task_list", Arguments: json.RawMessage(`{}`), Type: "function"})
			},
			func(req llm.Request) llm.Response {
				calls++
				roundTwoSawBoth = requestContainsText(req, firstContent) && requestContainsText(req, secondContent)
				return toolCallResponse(communicateCall("mid-turn-complete", "both deliveries handled"))
			},
		),
	)
	if _, err := root.appendDelegateNotificationDurably(firstID, firstContent); err != nil {
		t.Fatalf("append first root attention: %v", err)
	}
	if err := root.requestNotificationWake(); err != nil {
		t.Fatalf("arm first root attention: %v", err)
	}
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("process root attention: %v", err)
	}
	if midTurnAppendErr != nil {
		t.Fatalf("mid-turn append: %v", midTurnAppendErr)
	}
	if midTurnArmErr != nil {
		t.Fatalf("mid-turn arm: %v", midTurnArmErr)
	}
	if !roundTwoSawBoth {
		t.Fatal("the in-flight turn's later round did not present both deliveries to the model")
	}
	fold, err := readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read root attention fold: %v", err)
	}
	for _, id := range []string{firstID, secondID} {
		if got := fold.resolutions[id]; got != delegateAttentionConsumed {
			t.Fatalf("attention %q disposition = %q, want consumed", id, got)
		}
	}
	if pending := fold.pendingIDs(); len(pending) != 0 {
		t.Fatalf("mid-turn-covered attention remains pending after the turn that saw it: %#v", pending)
	}
	if root.hasPendingNotifications() {
		t.Fatal("covered attention still arms a wake: a redundant notification turn would re-ask the model with nothing new")
	}
	if calls != 2 {
		t.Fatalf("provider calls = %d, want exactly the in-flight turn's two rounds", calls)
	}
}

// The mirror boundary: a delivery appended after the turn's final request was
// built is durable and resident in history, but no model call this turn
// presented it. Finishing the turn must NOT consume it — its armed wake fires
// and a real notification turn presents it before resolution.
func TestRootDelegateAttention_PostBuildArmedAttentionStaysPendingForWake(t *testing.T) {
	stateDir := t.TempDir()
	const (
		firstID      = "delegate:dlg_unseen/delivery/1"
		firstContent = `<delegate-notification delegate_id="dlg_unseen">first</delegate-notification>`
		lateID       = "delegate:dlg_unseen/delivery/2"
		lateContent  = `<delegate-notification delegate_id="dlg_unseen">late lands after the final request was built</delegate-notification>`
	)
	var root *Session
	var (
		calls         int
		wakeSawLate   bool
		lateAppendErr error
		lateArmErr    error
	)
	root = newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}),
		withSteps(
			func(llm.Request) llm.Response {
				calls++
				// Lands DURING the turn's final model call: the steering turn is
				// durable and retained in history, but no request this turn
				// presented it to the model.
				if lateAppendErr == nil {
					_, lateAppendErr = root.appendDelegateNotificationDurably(lateID, lateContent)
				}
				if lateAppendErr == nil {
					lateArmErr = root.requestNotificationWake()
				}
				return toolCallResponse(communicateCall("unseen-turn-complete", "first handled"))
			},
			func(req llm.Request) llm.Response {
				calls++
				wakeSawLate = requestContainsText(req, lateContent)
				return toolCallResponse(communicateCall("unseen-wake-complete", "late handled"))
			},
		),
	)
	if _, err := root.appendDelegateNotificationDurably(firstID, firstContent); err != nil {
		t.Fatalf("append first root attention: %v", err)
	}
	if err := root.requestNotificationWake(); err != nil {
		t.Fatalf("arm first root attention: %v", err)
	}
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("process first root attention turn: %v", err)
	}
	if lateAppendErr != nil {
		t.Fatalf("late append: %v", lateAppendErr)
	}
	if lateArmErr != nil {
		t.Fatalf("late arm: %v", lateArmErr)
	}
	fold, err := readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read root attention fold: %v", err)
	}
	if got := fold.resolutions[firstID]; got != delegateAttentionConsumed {
		t.Fatalf("first attention disposition = %q, want consumed", got)
	}
	if pending := fold.pendingIDs(); !reflect.DeepEqual(pending, []string{lateID}) {
		t.Fatalf("pending after the turn that never saw the late delivery = %#v, want exactly that delivery", pending)
	}
	if !root.hasPendingNotifications() {
		t.Fatal("unseen attention lost its wake: its content would never reach the model")
	}
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("process wake turn for the late delivery: %v", err)
	}
	if !wakeSawLate {
		t.Fatal("the wake turn did not present the late delivery to the model before consuming it")
	}
	fold, err = readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read root attention fold after wake: %v", err)
	}
	if pending := fold.pendingIDs(); len(pending) != 0 {
		t.Fatalf("late delivery remains pending after its wake turn: %#v", pending)
	}
}

// A notification wake accepted on pending steering (or a job notification)
// begins with an EMPTY attention snapshot. A delivery armed while that turn
// runs is presented by the turn's later rounds exactly like the notification
// case, so the turn must consume it at finish; a guard that skips finish when
// the snapshot is empty leaves it for a redundant wake with nothing new to say.
func TestRootDelegateAttention_EmptySnapshotNotificationTurnConsumesCoveredDelivery(t *testing.T) {
	stateDir := t.TempDir()
	const (
		attentionID = "delegate:dlg_emptysnap/delivery/1"
		content     = `<delegate-notification delegate_id="dlg_emptysnap">lands during a steering wake</delegate-notification>`
	)
	var root *Session
	var (
		calls       int
		roundTwoSaw bool
		appendErr   error
		armErr      error
	)
	root = newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}),
		withSteps(
			func(llm.Request) llm.Response {
				calls++
				if appendErr == nil {
					_, appendErr = root.appendDelegateNotificationDurably(attentionID, content)
				}
				if appendErr == nil {
					armErr = root.requestNotificationWake()
				}
				return toolCallResponse(llm.ToolCallData{ID: "round-one", Name: "task_list", Arguments: json.RawMessage(`{}`), Type: "function"})
			},
			func(req llm.Request) llm.Response {
				calls++
				roundTwoSaw = requestContainsText(req, content)
				return toolCallResponse(communicateCall("empty-snap-complete", "steering and delivery handled"))
			},
		),
	)
	// A pending steering item is what accepts this notification turn; no root
	// attention is armed, so the begin snapshot is empty.
	root.SteerKind("unrelated steering wake", events.SteeringKindNotification)
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("process steering-wake notification turn: %v", err)
	}
	if appendErr != nil {
		t.Fatalf("mid-turn append: %v", appendErr)
	}
	if armErr != nil {
		t.Fatalf("mid-turn arm: %v", armErr)
	}
	if calls != 2 {
		t.Fatalf("provider calls = %d, want the accepted turn's two rounds", calls)
	}
	if !roundTwoSaw {
		t.Fatal("the accepted turn's later round did not present the mid-turn delivery")
	}
	fold, err := readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read root attention fold: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != delegateAttentionConsumed {
		t.Fatalf("attention disposition = %q, want consumed by the covering turn", got)
	}
	if root.hasPendingNotifications() {
		t.Fatal("covered attention still arms a wake after the turn that presented it")
	}
}

// A user turn that incorporates a mid-turn delivery consumes it at finish,
// exactly like a notification turn: the delivery's content rode the turn's
// later requests, and its armed wake would carry nothing new.
func TestRootDelegateAttention_UserTurnConsumesCoveredMidTurnDelivery(t *testing.T) {
	stateDir := t.TempDir()
	const (
		attentionID = "delegate:dlg_userturn/delivery/1"
		content     = `<delegate-notification delegate_id="dlg_userturn">lands during a user turn</delegate-notification>`
	)
	var root *Session
	var (
		roundTwoSaw bool
		thirdRan    bool
		appendErr   error
		armErr      error
	)
	root = newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}),
		withSteps(
			func(llm.Request) llm.Response {
				if appendErr == nil {
					_, appendErr = root.appendDelegateNotificationDurably(attentionID, content)
				}
				if appendErr == nil {
					armErr = root.requestNotificationWake()
				}
				return toolCallResponse(llm.ToolCallData{ID: "round-one", Name: "task_list", Arguments: json.RawMessage(`{}`), Type: "function"})
			},
			func(req llm.Request) llm.Response {
				roundTwoSaw = requestContainsText(req, content)
				return toolCallResponse(communicateCall("user-turn-complete", "question answered and delivery handled"))
			},
			// A third call would be the drain selector's backstop notification
			// turn — the failure mode this test exists to rule out.
			func(llm.Request) llm.Response {
				thirdRan = true
				return toolCallResponse(communicateCall("backstop-complete", "should not run"))
			},
		),
	)
	if _, err := root.ProcessInput(context.Background(), "status update please", nil); err != nil {
		t.Fatalf("process user turn: %v", err)
	}
	if appendErr != nil {
		t.Fatalf("mid-turn append: %v", appendErr)
	}
	if armErr != nil {
		t.Fatalf("mid-turn arm: %v", armErr)
	}
	if !roundTwoSaw {
		t.Fatal("the user turn's later round did not present the mid-turn delivery")
	}
	// The user turn must have consumed the delivery itself. Without this
	// guard, a regression that skips coverage consumption would still pass:
	// the turn-tail drain selector sees the delivery pending and runs a
	// backstop notification turn that consumes it.
	if thirdRan {
		t.Fatal("a third model turn ran: the drain selector's backstop consumed the delivery, not the user turn")
	}
	fold, err := readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read root attention fold: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != delegateAttentionConsumed {
		t.Fatalf("attention disposition = %q, want consumed by the covering user turn", got)
	}
	if root.hasPendingNotifications() {
		t.Fatal("covered attention still arms a wake after the user turn that presented it")
	}
}

// A user turn that presents attention armed before the turn began consumes it
// at finish (#4247), the way a delegate does: the model has seen the message
// that would have woken it, so a notification turn would carry nothing new.
func TestRootDelegateAttention_UserTurnConsumesAttentionArmedBeforeTurn(t *testing.T) {
	stateDir := t.TempDir()
	const (
		attentionID = "delegate:dlg_preturn/delivery/1"
		content     = `<delegate-notification delegate_id="dlg_preturn">landed before the user turn</delegate-notification>`
	)
	var (
		root     *Session
		requests int
		sawIt    bool
	)
	root = newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}),
		withSteps(
			func(req llm.Request) llm.Response {
				requests++
				sawIt = requestContainsText(req, content)
				return toolCallResponse(communicateCall("user-turn-complete", "answered"))
			},
			// A second request would be the notification turn #4247 removes.
			func(llm.Request) llm.Response {
				requests++
				return toolCallResponse(communicateCall("notification-turn", "should not run"))
			},
		),
	)
	if _, err := root.appendDelegateNotificationDurably(attentionID, content); err != nil {
		t.Fatalf("append pre-turn attention: %v", err)
	}
	if err := root.requestNotificationWake(); err != nil {
		t.Fatalf("arm pre-turn attention: %v", err)
	}
	if _, err := root.ProcessInput(context.Background(), "status update please", nil); err != nil {
		t.Fatalf("process user turn: %v", err)
	}
	if !sawIt {
		t.Fatal("the user turn's request did not present the pre-armed delivery")
	}
	if requests != 1 {
		t.Fatalf("provider requests = %d, want 1: the pre-armed delivery earned a notification turn", requests)
	}
	fold, err := readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read root attention fold: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != delegateAttentionConsumed {
		t.Fatalf("attention disposition = %q, want consumed by the user turn", got)
	}
	if root.hasPendingNotifications() {
		t.Fatal("the pre-armed delivery still holds a wake after the turn that presented it")
	}
}

// Attention armed before the turn that no settled request of the turn
// presents (its transcript entry lands after the request was built) keeps its
// notification turn.
func TestRootDelegateAttention_PreArmedAttentionNotPresentedKeepsItsNotificationTurn(t *testing.T) {
	stateDir := t.TempDir()
	const (
		attentionID = "delegate:dlg_unseen/delivery/1"
		content     = `<delegate-notification delegate_id="dlg_unseen">appended after the turn's only request</delegate-notification>`
	)
	var (
		root            *Session
		requests        int
		notificationSaw bool
		appendErr       error
	)
	root = newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}),
		withSteps(
			func(llm.Request) llm.Response {
				requests++
				_, appendErr = root.appendDelegateNotificationDurably(attentionID, content)
				return toolCallResponse(communicateCall("user-turn-complete", "answered"))
			},
			func(req llm.Request) llm.Response {
				requests++
				notificationSaw = requestContainsText(req, content)
				return toolCallResponse(communicateCall("notification-turn", "handled"))
			},
		),
	)
	// Armed in the wake set before the turn; its durable entry does not exist
	// yet, so no request of the user turn can present it.
	root.requestNotificationWake()
	if _, err := root.ProcessInput(context.Background(), "status update please", nil); err != nil {
		t.Fatalf("process user turn: %v", err)
	}
	if appendErr != nil {
		t.Fatalf("append: %v", appendErr)
	}
	if requests != 2 {
		t.Fatalf("provider requests = %d, want 2: the unpresented item keeps its notification turn", requests)
	}
	if !notificationSaw {
		t.Fatal("the notification turn did not present the item")
	}
	if root.hasPendingNotifications() {
		t.Fatal("the notification turn left the item pending")
	}
}

// Coverage credits every presented delivery, whether armed before the turn
// began or during it (#4247): the model has seen the message that would have
// woken it, wherever the steering turn sits in history and whether or not its
// arm has landed yet.
func TestRootDelegateAttention_CoverageIncludesAttentionArmedBeforeTurnBegin(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	const (
		preTurnID  = "delegate:dlg_boundary/delivery/1"
		midTurnID  = "delegate:dlg_boundary/delivery/2"
		unarmedID  = "delegate:dlg_boundary/delivery/3"
		preContent = `<delegate-notification delegate_id="dlg_boundary">armed before the turn</delegate-notification>`
		midContent = `<delegate-notification delegate_id="dlg_boundary">armed mid-turn</delegate-notification>`
	)
	root := newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}),
	)
	if _, err := root.appendDelegateNotificationDurably(preTurnID, preContent); err != nil {
		t.Fatalf("append pre-turn attention: %v", err)
	}
	if err := root.requestNotificationWake(); err != nil {
		t.Fatalf("arm pre-turn attention: %v", err)
	}
	// Turn start.
	root.resetAttentionCoverage()
	if _, err := root.appendDelegateNotificationDurably(midTurnID, midContent); err != nil {
		t.Fatalf("append mid-turn attention: %v", err)
	}
	if err := root.requestNotificationWake(); err != nil {
		t.Fatalf("arm mid-turn attention: %v", err)
	}
	// Appended but deliberately NOT armed: the arm can lag the append past the
	// mark, and coverage must not depend on it.
	if _, err := root.appendDelegateNotificationDurably(unarmedID, midContent); err != nil {
		t.Fatalf("append unarmed attention: %v", err)
	}
	steeringFor := func(id, text string) schema.Turn {
		turn := schema.NewTurn(schema.TurnSteering, llm.User(text))
		turn.AttentionID = id
		return turn
	}
	// All three steering turns sit anywhere in the presented history; position
	// is not the boundary.
	historyTurns := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("earlier context")),
		steeringFor(preTurnID, preContent),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("earlier answer")),
		steeringFor(midTurnID, midContent),
		steeringFor(unarmedID, midContent),
	}
	root.stagePresentedAttentionCoverage(context.Background(), historyTurns)
	root.attentionMu.Lock()
	staged := maps.Clone(root.attentionStagedIDs)
	root.attentionMu.Unlock()
	for _, id := range []string{preTurnID, midTurnID, unarmedID} {
		if _, ok := staged[id]; !ok {
			t.Fatalf("presented attention %q was not staged", id)
		}
	}
	// Staging is candidacy, not credit: only a settled round promotes.
	root.promoteStagedAttentionCoverage(llm.Request{})
	root.attentionMu.Lock()
	covered := root.attentionCoveredIDs
	root.attentionMu.Unlock()
	for _, id := range []string{preTurnID, midTurnID, unarmedID} {
		if _, ok := covered[id]; !ok {
			t.Fatalf("presented attention %q was not marked covered", id)
		}
	}
}

// rootWithMidTurnAttention returns a root whose turn has begun and one
// attention item armed after that, plus a presented history carrying it.
func rootWithMidTurnAttention(t *testing.T, attentionID, content string) (*Session, []schema.Turn) {
	t.Helper()
	stateDir := t.TempDir()
	root := newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}),
	)
	root.resetAttentionCoverage()
	if _, err := root.appendDelegateNotificationDurably(attentionID, content); err != nil {
		t.Fatalf("append attention: %v", err)
	}
	if err := root.requestNotificationWake(); err != nil {
		t.Fatalf("arm attention: %v", err)
	}
	steering := schema.NewTurn(schema.TurnSteering, llm.User(content))
	steering.AttentionID = attentionID
	return root, []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("context")),
		steering,
	}
}

func rootCoveredAttention(root *Session) map[string]struct{} {
	root.attentionMu.Lock()
	defer root.attentionMu.Unlock()
	return maps.Clone(root.attentionCoveredIDs)
}

// A responses-continuation delta request carries only new items; older
// steering turns live in server-side state this session cannot verify, so a
// round that settles as a delta credits nothing. The items keep their wake
// for a full-history turn.
func TestRootDelegateAttention_SettledDeltaCreditsNothing(t *testing.T) {
	t.Parallel()
	const attentionID = "delegate:dlg_delta/delivery/1"
	root, historyTurns := rootWithMidTurnAttention(t, attentionID,
		`<delegate-notification delegate_id="dlg_delta">delta skip</delegate-notification>`)
	delta := llm.Request{HistoryMode: llm.HistoryModeResponsesDelta}

	root.stagePresentedAttentionCoverage(context.Background(), historyTurns)
	root.promoteStagedAttentionCoverage(delta)
	if covered := rootCoveredAttention(root); len(covered) != 0 {
		t.Fatalf("settled delta credited coverage: %v, want nothing", covered)
	}

	// The full-history round that follows credits normally.
	full := llm.Request{HistoryMode: llm.HistoryModeFullHistory}
	root.stagePresentedAttentionCoverage(context.Background(), historyTurns)
	root.promoteStagedAttentionCoverage(full)
	if _, ok := rootCoveredAttention(root)[attentionID]; !ok {
		t.Fatal("settled full-history request did not credit the presented attention")
	}
}

// A full-history attempt that failed leaves no candidate behind for the
// round's retry: when the retry settles as a delta, which carried only new
// items, nothing is credited.
func TestRootDelegateAttention_FailedFullAttemptLeavesNothingForASettledDelta(t *testing.T) {
	t.Parallel()
	root, historyTurns := rootWithMidTurnAttention(t, "delegate:dlg_retry/delivery/1",
		`<delegate-notification delegate_id="dlg_retry">retried as a delta</delegate-notification>`)

	// The full-history attempt stages, then fails: no promotion.
	root.stagePresentedAttentionCoverage(context.Background(), historyTurns)
	// The retry builds and settles as a delta.
	delta := llm.Request{HistoryMode: llm.HistoryModeResponsesDelta}
	root.stagePresentedAttentionCoverage(context.Background(), historyTurns)
	root.promoteStagedAttentionCoverage(delta)
	if covered := rootCoveredAttention(root); len(covered) != 0 {
		t.Fatalf("a delta retry credited the failed attempt's candidates: %v, want nothing", covered)
	}
}

// A delta request whose call falls back to a full-history request settles as
// that request, which carried every turn the round presents, so the round
// credits them.
func TestRootDelegateAttention_FullHistoryFallbackCreditsWhatItCarried(t *testing.T) {
	t.Parallel()
	const attentionID = "delegate:dlg_fallback/delivery/1"
	root, historyTurns := rootWithMidTurnAttention(t, attentionID,
		`<delegate-notification delegate_id="dlg_fallback">carried by the fallback</delegate-notification>`)

	root.stagePresentedAttentionCoverage(context.Background(), historyTurns)
	root.promoteStagedAttentionCoverage(llm.Request{HistoryMode: llm.HistoryModeFullHistoryFallback})
	if _, ok := rootCoveredAttention(root)[attentionID]; !ok {
		t.Fatal("the settled full-history fallback did not credit the attention it carried")
	}
}

// Coverage is credit for presentation in a SETTLED call, not for being in a
// built request. A round whose call fails — after which a retry's compaction
// may fold the steering turn away — must leave the item unconsumed: staged
// but never promoted, so finish skips it and its wake survives.
func TestRootDelegateAttention_CoverageCreditsOnlySettledRounds(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	const (
		attentionID = "delegate:dlg_settled/delivery/1"
		content     = `<delegate-notification delegate_id="dlg_settled">must be settled to count</delegate-notification>`
	)
	root := newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}),
	)
	root.resetAttentionCoverage()
	if _, err := root.appendDelegateNotificationDurably(attentionID, content); err != nil {
		t.Fatalf("append attention: %v", err)
	}
	if err := root.requestNotificationWake(); err != nil {
		t.Fatalf("arm attention: %v", err)
	}
	steering := schema.NewTurn(schema.TurnSteering, llm.User(content))
	steering.AttentionID = attentionID
	historyTurns := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("context")),
		steering,
	}

	// The request build stages the item; the round's call then FAILS (a
	// content-filter retry whose compaction folds the steering turn away is
	// the production shape). No promotion happens, and finish must not
	// consume: the model never saw it in a settled call.
	root.stagePresentedAttentionCoverage(context.Background(), historyTurns)
	root.attentionMu.Lock()
	if _, ok := root.attentionStagedIDs[attentionID]; !ok {
		root.attentionMu.Unlock()
		t.Fatal("built request did not stage the presented attention")
	}
	if len(root.attentionCoveredIDs) != 0 {
		root.attentionMu.Unlock()
		t.Fatal("staging alone credited coverage before any call settled")
	}
	root.attentionMu.Unlock()
	if err := root.finishAttentionTurn(nil, nil); err != nil {
		t.Fatalf("finish with no settled coverage: %v", err)
	}
	fold, err := readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read fold after unsettled finish: %v", err)
	}
	if pending := fold.pendingIDs(); !reflect.DeepEqual(pending, []string{attentionID}) {
		t.Fatalf("unsettled coverage was consumed: pending = %#v, want the delivery kept", pending)
	}

	// The retry's round settles with the item presented. Its response receipt
	// syncs before promotion, and finish repairs the derived marker.
	root.stagePresentedAttentionCoverage(context.Background(), historyTurns)
	if err := root.appendAssistantTurn(llm.Response{Message: llm.Assistant("settled response")}, ModelAttemptMetadata{}, root.stagedNotificationObservationIDs(llm.Request{})...); err != nil {
		t.Fatal(err)
	}
	root.promoteStagedAttentionCoverage(llm.Request{})
	if err := root.finishAttentionTurn(nil, nil); err != nil {
		t.Fatalf("finish with settled coverage: %v", err)
	}
	fold, err = readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read fold after settled finish: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != delegateAttentionConsumed {
		t.Fatalf("settled coverage disposition = %q, want consumed", got)
	}
}

// An accepted notification turn with an EMPTY begin snapshot can only fail on
// coverage — and coverage failures warn instead of failing the turn. The item
// remains observed, and the paced retry repairs its missing derived marker
// without repeating either successful provider round.
func TestRootDelegateAttention_EmptySnapshotCoverageFailureWarnsAndRetries(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	clock := agenttest.NewFakeClock()
	const (
		attentionID = "delegate:dlg_emptyfail/delivery/1"
		content     = `<delegate-notification delegate_id="dlg_emptyfail">coverage resolution will fail</delegate-notification>`
	)
	var root *Session
	requests := 0
	root = newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1, clock: clock}),
		withSteps(
			func(llm.Request) llm.Response {
				requests++
				if _, err := root.appendDelegateNotificationDurably(attentionID, content); err != nil {
					t.Errorf("mid-turn append: %v", err)
				}
				if err := root.requestNotificationWake(); err != nil {
					t.Errorf("mid-turn arm: %v", err)
				}
				return toolCallResponse(llm.ToolCallData{ID: "round-one", Name: "task_list", Arguments: json.RawMessage(`{}`), Type: "function"})
			},
			func(llm.Request) llm.Response {
				requests++
				return toolCallResponse(communicateCall("empty-fail-turn", "steering handled"))
			},
			func(llm.Request) llm.Response {
				requests++
				t.Error("index repair repeated successful model work")
				return toolCallResponse(communicateCall("empty-fail-retry", "delivery consumed by the paced retry wake"))
			},
		),
	)
	installResolutionSyncFailureWriter(t, root)
	wakes := make(chan struct{}, 2)
	root.SetNotifyFunc(func() { wakes <- struct{}{} })

	root.SteerKind("unrelated steering wake", events.SteeringKindNotification)
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("empty-snapshot turn failed with the coverage-only resolution error: %v", err)
	}
	fold, err := readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read fold after failed coverage resolution: %v", err)
	}
	if pending := fold.pendingIDs(); len(pending) != 0 {
		t.Fatalf("observed notification became pending after index failure: %v", pending)
	}
	if _, observed := fold.observed[attentionID]; !observed || len(fold.resolutions) != 0 {
		t.Fatalf("failed index repair lost its response receipt or wrote a marker: observed=%v resolutions=%v", fold.observed, fold.resolutions)
	}
	if !root.sessionWorkPending() {
		t.Fatal("failed coverage resolution left no autonomous work to recover consumption")
	}
	clock.Advance(jobNotificationRetryInitialDelay)
	clock.Drain()
	fold, err = readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read fold after paced retry: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != delegateAttentionConsumed {
		t.Fatalf("attention disposition = %q, want consumed by the paced retry wake", got)
	}
	if requests != 2 {
		t.Fatalf("provider requests across index retry = %d, want 2 original rounds", requests)
	}
}

// A turn that panics never settled: its coverage must NOT be consumed during
// unwinding. The recover defer marks err before re-panicking, and the finish
// defer — unwinding right after — reads it and skips the success path.
func TestRootDelegateAttention_PanicUnwindDoesNotConsumeCoverage(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	const (
		attentionID = "delegate:dlg_panic/delivery/1"
		content     = `<delegate-notification delegate_id="dlg_panic">panic must not consume</delegate-notification>`
	)
	root := newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}),
	)
	if _, err := root.appendDelegateNotificationDurably(attentionID, content); err != nil {
		t.Fatalf("append attention: %v", err)
	}
	root.attentionMu.Lock()
	root.attentionCoveredIDs = map[string]struct{}{attentionID: {}}
	root.attentionMu.Unlock()
	panicErr := errors.New("injected turn panic")
	ctx := context.WithValue(context.Background(), sessionLifecycleFaultsKey{}, map[string]error{"panic": panicErr})
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("injected panic did not propagate")
			}
		}()
		_, _ = root.ProcessInputKind(ctx, "question", nil, EntryUserInput)
	}()
	fold, err := readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read fold after panicking turn: %v", err)
	}
	if pending := fold.pendingIDs(); !reflect.DeepEqual(pending, []string{attentionID}) {
		t.Fatalf("panic unwinding consumed coverage: pending = %#v, want the delivery kept", pending)
	}
}

// A derived marker failure must not fail the user's successful turn. The armed
// wake repairs its durable receipt without another provider request.
func TestRootDelegateAttention_CoveredResolutionFailureDoesNotFailUserTurn(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	clock := agenttest.NewFakeClock()
	const (
		attentionID = "delegate:dlg_resfail/delivery/1"
		content     = `<delegate-notification delegate_id="dlg_resfail">resolution will fail</delegate-notification>`
	)
	var root *Session
	requests := 0
	root = newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1, clock: clock}),
		withSteps(
			func(llm.Request) llm.Response {
				requests++
				if _, err := root.appendDelegateNotificationDurably(attentionID, content); err != nil {
					t.Errorf("mid-turn append: %v", err)
				}
				if err := root.requestNotificationWake(); err != nil {
					t.Errorf("mid-turn arm: %v", err)
				}
				return toolCallResponse(llm.ToolCallData{ID: "round-one", Name: "task_list", Arguments: json.RawMessage(`{}`), Type: "function"})
			},
			func(req llm.Request) llm.Response {
				requests++
				if !requestContainsText(req, content) {
					t.Error("successful covering request omitted the notification")
				}
				return toolCallResponse(communicateCall("resolution-failure-turn", "answer stands"))
			},
			func(llm.Request) llm.Response {
				requests++
				t.Error("receipt repair issued another provider request")
				return toolCallResponse(communicateCall("resolution-failure-wake", "delivery consumed by the wake"))
			},
		),
	)
	installResolutionSyncFailureWriter(t, root)
	if _, err := root.ProcessInput(context.Background(), "question", nil); err != nil {
		t.Fatalf("covering user turn (or its recovery wake) failed with the coverage resolution error: %v", err)
	}
	clock.Advance(jobNotificationRetryInitialDelay)
	clock.Drain()
	if requests != 2 {
		t.Fatalf("provider requests across receipt repair = %d, want 2 original rounds", requests)
	}
	fold, err := readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read root attention fold: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != delegateAttentionConsumed {
		t.Fatalf("attention disposition = %q, want consumed by the recovery wake after the failed coverage resolution", got)
	}
	if _, observed := fold.observed[attentionID]; !observed {
		t.Fatal("repaired marker has no durable response receipt")
	}
}

// Memory-only coverage is not a receipt, even with a selected wake snapshot.
// Consult durable evidence within a bounded number of reads and keep the retry.
func TestRootDelegateAttention_FailedTurnWithSnapshotConsultsDurableReceipts(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	root := newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}),
	)
	const (
		snapshotID = "delegate:dlg_snapshotfail/delivery/1"
		coveredID  = "delegate:dlg_coveredfail/delivery/1"
	)
	for _, attentionID := range []string{snapshotID, coveredID} {
		if _, err := root.appendDelegateNotificationDurably(attentionID, `<delegate-notification delegate_id="dlg_`+attentionID+`">fold must not be read</delegate-notification>`); err != nil {
			t.Fatalf("append attention %q: %v", attentionID, err)
		}
	}
	root.attentionMu.Lock()
	root.attentionCoveredIDs = map[string]struct{}{coveredID: {}}
	root.attentionMu.Unlock()

	var foldReads int
	root.cfg.testOnly.delegateAttentionReadFold = func(path, sessionID string) (delegateAttentionFold, error) {
		foldReads++
		return readDelegateAttentionFold(path, sessionID)
	}
	defer func() { root.cfg.testOnly.delegateAttentionReadFold = nil }()

	if err := root.finishAttentionTurn([]string{snapshotID}, errors.New("turn failed")); err != nil {
		t.Fatalf("failed turn with snapshot: %v", err)
	}
	if foldReads < 1 || foldReads > 3 {
		t.Fatalf("failed turn with a non-empty snapshot read the fold %d times, want 1..3", foldReads)
	}
	fold, err := readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatal(err)
	}
	if pending := fold.pendingIDs(); !reflect.DeepEqual(pending, []string{snapshotID, coveredID}) || len(fold.resolutions) != 0 {
		t.Fatalf("memory-only coverage acknowledged a notification: pending=%v resolutions=%v", pending, fold.resolutions)
	}
	if !root.sessionWorkPending() {
		t.Fatal("failed turn left no paced retry for the still-pending attention")
	}
}

// Without a durable response, memory-only coverage must neither acknowledge
// the item nor suppress the wake acceptance already armed.
func TestRootDelegateAttention_FailedEmptySnapshotMemoryOnlyCoverageKeepsWake(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	root := newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}),
	)
	const coveredID = "delegate:dlg_coveredgate/delivery/1"
	if _, err := root.appendDelegateNotificationDurably(coveredID, `<delegate-notification delegate_id="dlg_coveredgate">gate needs the fold</delegate-notification>`); err != nil {
		t.Fatalf("append attention: %v", err)
	}
	if err := root.requestNotificationWake(); err != nil {
		t.Fatal(err)
	}
	root.attentionMu.Lock()
	root.attentionCoveredIDs = map[string]struct{}{coveredID: {}}
	root.attentionMu.Unlock()

	var foldReads int
	root.cfg.testOnly.delegateAttentionReadFold = func(path, sessionID string) (delegateAttentionFold, error) {
		foldReads++
		return readDelegateAttentionFold(path, sessionID)
	}
	defer func() { root.cfg.testOnly.delegateAttentionReadFold = nil }()

	if err := root.finishAttentionTurn(nil, errors.New("turn failed")); err != nil {
		t.Fatalf("failed empty-snapshot turn: %v", err)
	}
	if foldReads < 1 || foldReads > 3 {
		t.Fatalf("failed empty-snapshot turn read the fold %d times, want 1..3", foldReads)
	}
	fold, err := readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatal(err)
	}
	if pending := fold.pendingIDs(); !reflect.DeepEqual(pending, []string{coveredID}) || len(fold.resolutions) != 0 || (!root.notificationSchedule.hinted && !root.notificationSchedule.retry.active) {
		t.Fatalf("memory-only coverage lost unobserved work: pending=%v resolutions=%v wake=%t", pending, fold.resolutions, root.notificationSchedule.hinted)
	}
	if !root.sessionWorkPending() {
		t.Fatal("failed empty-snapshot turn left no paced retry for the covered-but-pending attention")
	}
}

// The liveness counterpart to the fold-read pair above. A FAILED turn with an
// empty snapshot and nothing covered leaves a set wake flag untouched — and
// that is correct: the flag is a coalescing suppressor, not the liveness
// carrier. The arm's own guaranteed notify kick is what refires, driving a
// fresh notification turn whose begin snapshot is non-empty and which
// consumes the item. This test answers the adversarial claim that the early
// return strands the flag: the flag stays set precisely so the in-flight
// kick's turn cannot be suppressed into redundancy.
func TestRootDelegateAttention_FailedEmptyCoverageTurnRefiresViaArmKick(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	clock := agenttest.NewFakeClock()
	const attentionID = "delegate:dlg_refire/delivery/1"
	var root *Session
	root = newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1, clock: clock}),
		withSteps(
			// The covering turn: appends and arms the attention mid-turn, then
			// its round settles — the notification turn that consumes it comes
			// later, from the arm's kick.
			func(llm.Request) llm.Response {
				if _, err := root.appendDelegateNotificationDurably(attentionID, `<delegate-notification delegate_id="dlg_refire">refire me</delegate-notification>`); err != nil {
					t.Errorf("mid-turn append: %v", err)
				}
				if err := root.requestNotificationWake(); err != nil {
					t.Errorf("mid-turn arm: %v", err)
				}
				return toolCallResponse(communicateCall("refire-covering", "covered mid-turn"))
			},
			// The turn driven by the arm's kick. Its begin snapshot is
			// non-empty, so the finish path resolves and consumes.
			func(llm.Request) llm.Response {
				return toolCallResponse(communicateCall("refire-consumer", "consumed by the arm kick turn"))
			},
		),
	)
	wakes := make(chan struct{}, 2)
	root.SetNotifyFunc(func() { wakes <- struct{}{} })

	// A user turn (not the notification turn) presents and covers the item;
	// it succeeds, and its finish consumes the covered delivery. That leaves
	// nothing pending, so no kick is stranded.
	if _, err := root.ProcessInputKind(context.Background(), "question", nil, EntryUserInput); err != nil {
		t.Fatalf("covering user turn: %v", err)
	}
	fold, err := readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read fold after covering turn: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != delegateAttentionConsumed {
		t.Fatalf("attention disposition = %q, want consumed by the covering turn", got)
	}
	if root.sessionWorkPending() {
		t.Fatal("consumed attention still reports autonomous work pending")
	}

	// A failed empty turn must not manufacture a new notification wake.
	if err := root.finishAttentionTurn(nil, errors.New("turn failed")); err != nil {
		t.Fatalf("failed empty turn: %v", err)
	}
	root.mu.Lock()
	retrying := root.notificationSchedule.retry.active
	root.mu.Unlock()
	if retrying || root.hasPendingNotifications() {
		t.Fatal("empty failed turn manufactured notification work")
	}
}

func TestRootDelegateAttention_FailedIndexRepairRearmsWithoutRepeatingModelWork(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	clock := agenttest.NewFakeClock()
	requests := 0
	root := newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1, clock: clock}),
		withSteps(
			func(llm.Request) llm.Response {
				requests++
				return toolCallResponse(communicateCall("root-attention-first", "first attempt"))
			},
			func(llm.Request) llm.Response {
				requests++
				t.Error("index repair repeated successful model work")
				return toolCallResponse(communicateCall("root-attention-retry", "retry complete"))
			},
		),
	)
	installResolutionSyncFailureWriter(t, root)
	wakes := make(chan struct{}, 2)
	root.SetNotifyFunc(func() { wakes <- struct{}{} })

	const attentionID = "delegate:dlg_retry/delivery/1"
	if _, err := root.appendDelegateNotificationDurably(attentionID, `<delegate-notification delegate_id="dlg_retry">retry me</delegate-notification>`); err != nil {
		t.Fatalf("append root attention: %v", err)
	}
	if err := root.requestNotificationWake(); err != nil {
		t.Fatalf("arm root attention: %v", err)
	}
	select {
	case <-wakes:
	default:
		t.Fatal("initial root attention wake absent")
	}
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err == nil || !strings.Contains(err.Error(), "injected root attention resolution sync failure") {
		t.Fatalf("first root attention turn error = %v, want injected resolution failure", err)
	}
	fold, err := readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read failed root attention fold: %v", err)
	}
	if pending := fold.pendingIDs(); len(pending) != 0 {
		t.Fatalf("observed notification became pending after index failure: %v", pending)
	}
	if _, observed := fold.observed[attentionID]; !observed || len(fold.resolutions) != 0 {
		t.Fatalf("index fault lost response receipt or retained marker: observed=%v resolutions=%v", fold.observed, fold.resolutions)
	}
	if !root.sessionWorkPending() {
		t.Fatal("failed root attention consumption cleared autonomous work")
	}
	clock.Advance(jobNotificationRetryInitialDelay)
	clock.Drain()
	fold, err = readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read retried root attention fold: %v", err)
	}
	if pending := fold.pendingIDs(); len(pending) != 0 {
		t.Fatalf("pending after successful retry = %#v", pending)
	}
	if requests != 1 || fold.resolutions[attentionID] != delegateAttentionConsumed {
		t.Fatalf("retry did not repair only the index: requests=%d resolutions=%v", requests, fold.resolutions)
	}
}

func TestRootDelegateAttention_RestoreRearmsPendingIDsWithoutProviderCall(t *testing.T) {
	stateDir := t.TempDir()
	rootID := identifier.MustNewSessionID()
	if err := os.MkdirAll(filepath.Join(stateDir, sessionsSubdir), 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	const attentionID = "delegate:dlg_restore/delivery/1"
	writer, err := transcript.NewWriter(transcriptPath(stateDir, rootID), transcript.Header{SessionID: rootID, ProfileID: "openai", Model: "gpt-5.2"})
	if err != nil {
		t.Fatalf("create root transcript: %v", err)
	}
	attention := schema.NewTurn(schema.TurnSteering, llm.User(`<delegate-notification delegate_id="dlg_restore">restore me</delegate-notification>`))
	attention.AttentionID = attentionID
	attention.StableTurnID = newQueueEntryID()
	if err := writer.AppendDurable(attention); err != nil {
		_ = writer.Close()
		t.Fatalf("append pending root attention: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close root transcript: %v", err)
	}
	meta := schema.SessionMeta{ID: rootID, ProfileID: "openai", Model: "gpt-5.2"}
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatalf("save root metadata: %v", err)
	}
	client := llm.NewClient()
	adapter := &delegateAttentionLiveModelsAdapter{}
	client.Register(adapter)
	restored, err := RestoreSessionFromMeta(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(stateDir), meta, stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer restored.Close()
	wakes := make(chan struct{}, 1)
	restored.SetNotifyFunc(func() { wakes <- struct{}{} })
	select {
	case <-wakes:
	default:
		t.Fatal("restored pending root attention emitted no provider-free wake")
	}
	if !restored.sessionWorkPending() {
		t.Fatal("restored pending root attention is absent from autonomous work")
	}
}

func TestDelegateAttention_RestartRearmsColdChildAndDrainsExactAttention(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	const (
		owedID            = "watch:restart-attention:owed"
		owedContent       = `<job-notification event="watch_send">recover consumed owed generation</job-notification>`
		remainingID       = "watch:restart-attention:remaining"
		remainingContent  = `<job-notification event="watch_send">remain unresolved while owed generation runs</job-notification>`
		historicalID      = "watch:restart-attention:historical"
		historicalContent = `<job-notification event="watch_send">historical zero marker launches nothing</job-notification>`
	)
	childPath := transcriptPath(fixture.stateDir, fixture.childID)
	for index, item := range []struct {
		id      string
		content string
	}{
		{id: owedID, content: owedContent},
		{id: remainingID, content: remainingContent},
		{id: historicalID, content: historicalContent},
	} {
		if appended, err := appendColdDelegateNotificationDurablyWithOpen(
			childPath,
			fixture.childID,
			item.id,
			item.content,
			time.Unix(1_700_000_300+int64(index), 0).UTC(),
			transcript.OpenWriterForSession,
		); err != nil || !appended {
			t.Fatalf("append cold delegate attention %q = appended:%t err:%v", item.id, appended, err)
		}
	}
	writer, _, err := transcript.OpenWriterForSession(childPath, fixture.childID)
	if err != nil {
		t.Fatalf("open child attention transcript: %v", err)
	}
	// A compaction landed after the three items arrived, so the resumed
	// history starts at its summary and holds none of them.
	if err := writer.AppendDurable(schema.NewTurn(schema.TurnSummary, llm.User("[CONTEXT SUMMARY]\nfold after the items\n[END SUMMARY]"))); err != nil {
		_ = writer.Close()
		t.Fatalf("append compaction marker: %v", err)
	}
	owedResolution := delegateAttentionResolutionTurn(owedID, delegateAttentionConsumed)
	owedResolution.AttentionResolution.ResumeGeneration = 1
	if err := writer.AppendDurable(owedResolution); err != nil {
		_ = writer.Close()
		t.Fatalf("append owed consumed marker: %v", err)
	}
	if err := writer.AppendDurable(delegateAttentionResolutionTurn(historicalID, delegateAttentionConsumed)); err != nil {
		_ = writer.Close()
		t.Fatalf("append historical consumed marker: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close child attention transcript: %v", err)
	}
	store, err := delegatestore.Open(delegateResourceStorePath(fixture.stateDir, fixture.meta.ID))
	if err != nil {
		t.Fatalf("open delegate store: %v", err)
	}
	events, err := store.Load()
	if err != nil {
		_ = store.Close()
		t.Fatalf("load delegate store: %v", err)
	}
	state, err := delegatestore.Fold(events)
	if err != nil {
		_ = store.Close()
		t.Fatalf("fold delegate store: %v", err)
	}
	if _, _, err := store.AppendBatch(state, []delegatestore.Event{{
		Kind:       delegatestore.EventDelegateAttentionChanged,
		DelegateID: fixture.delegateID,
		AttentionChanged: &delegatestore.DelegateAttentionChanged{
			NeedsAttention: true,
		},
	}}); err != nil {
		_ = store.Close()
		t.Fatalf("append initial attention projection: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close delegate store: %v", err)
	}

	attentionEntered := make(chan struct{})
	releaseAttention := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseAttention) }) })
	attentionSeen := false
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(request llm.Request) llm.Response {
			attentionSeen = requestContainsText(request, owedContent)
			close(attentionEntered)
			<-releaseAttention
			return communicateResponse(true, "recovered owed generation handled")
		},
		func(request llm.Request) llm.Response {
			if !requestContainsText(request, remainingContent) {
				t.Errorf("successor attention request omitted remaining unresolved input")
			}
			return communicateResponse(true, "remaining attention handled")
		},
		func(llm.Request) llm.Response {
			return communicateResponse(true, "root drained recovered completions")
		},
	}
	root := restoreSupervisionRoot(t, fixture, nil)
	<-attentionEntered
	root.delegateController.mu.Lock()
	aggregate := root.delegateController.durable[fixture.delegateID]
	generation := aggregate.Generation
	trigger := aggregate.Trigger
	open := aggregate.CurrentRunOpen
	needsAttention := aggregate.NeedsAttention
	unresolved := maps.Clone(attentionWakeIDsOf(root.delegateController, fixture.delegateID))
	root.delegateController.mu.Unlock()
	if generation != 1 || trigger != delegatestore.TriggerAttention || !open || !needsAttention {
		t.Fatalf("owed attention generation = generation:%d trigger:%q open:%t needs:%t, want 1/attention/open/true", generation, trigger, open, needsAttention)
	}
	if !reflect.DeepEqual(unresolved, map[string]struct{}{remainingID: {}}) {
		t.Fatalf("restart unresolved IDs = %#v, want only %q", unresolved, remainingID)
	}
	if !attentionSeen {
		t.Fatal("owed generation provider request omitted the exact consumed attention")
	}
	fold, err := readDelegateAttentionFold(childPath, fixture.childID)
	if err != nil {
		t.Fatalf("read restarted attention fold: %v", err)
	}
	if fold.resumeGenerations[owedID] != 1 || fold.resumeGenerations[historicalID] != 0 || fold.resolutions[historicalID] != delegateAttentionConsumed {
		t.Fatalf("restart resolution generations = %#v resolutions=%#v", fold.resumeGenerations, fold.resolutions)
	}
	// The recovered generation's first request is in flight and nothing has
	// presented the trigger yet, so a fold now must still keep its turn.
	root.delegateController.mu.Lock()
	recovered := root.delegateController.live[fixture.delegateID].binding.runtime
	root.delegateController.mu.Unlock()
	recovered.attentionMu.Lock()
	heldTrigger := recovered.attentionAcceptedID
	recovered.attentionMu.Unlock()
	if heldTrigger != owedID {
		t.Fatalf("recovered generation holds trigger %q before its first request settles, want %q", heldTrigger, owedID)
	}
	releaseOnce.Do(func() { close(releaseAttention) })
	waitForStableSupervisionRun(t, root, fixture.childID)

	t.Run("rejects exact non-attention generation", func(t *testing.T) {
		fixture := newColdStableDelegateFixture(t, "")
		const attentionID = "watch:restart-attention:trigger-conflict"
		childPath := transcriptPath(fixture.stateDir, fixture.childID)
		if appended, err := appendColdDelegateNotificationDurablyWithOpen(childPath, fixture.childID, attentionID, "trigger conflict", time.Unix(1_700_000_400, 0).UTC(), transcript.OpenWriterForSession); err != nil || !appended {
			t.Fatalf("append conflicting attention = appended:%t err:%v", appended, err)
		}
		writer, _, err := transcript.OpenWriterForSession(childPath, fixture.childID)
		if err != nil {
			t.Fatalf("open conflicting transcript: %v", err)
		}
		resolution := delegateAttentionResolutionTurn(attentionID, delegateAttentionConsumed)
		resolution.AttentionResolution.ResumeGeneration = 1
		if err := writer.AppendDurable(resolution); err != nil {
			_ = writer.Close()
			t.Fatalf("append conflicting consumed marker: %v", err)
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("close conflicting transcript: %v", err)
		}
		store, err := delegatestore.Open(delegateResourceStorePath(fixture.stateDir, fixture.meta.ID))
		if err != nil {
			t.Fatalf("open conflicting store: %v", err)
		}
		events, err := store.Load()
		if err != nil {
			_ = store.Close()
			t.Fatalf("load conflicting store: %v", err)
		}
		state, err := delegatestore.Fold(events)
		if err != nil {
			_ = store.Close()
			t.Fatalf("fold conflicting store: %v", err)
		}
		lease := delegateLease{delegateID: fixture.delegateID, generation: 1}
		packet := delegateControllerReportedPacket("owner generation")
		_, _, err = store.AppendBatch(state, []delegatestore.Event{
			delegateControllerRunStartedEvent(fixture.delegateID, 1, delegatestore.TriggerOwnerInput, time.Unix(1_700_000_401, 0).UTC()),
			{
				Kind:       delegatestore.EventDelegateTerminalPrepared,
				DelegateID: fixture.delegateID,
				TerminalPrepared: &delegatestore.TerminalPrepared{
					Generation: 1,
					Packet:     packet,
				},
			},
			delegateRunFinishedEvent(lease, delegatestore.OutcomeCompleted, delegatestore.DispositionReported, "", time.Unix(1_700_000_402, 0).UTC(), delegateDeliveryID(fixture.delegateID, 1), nil),
		})
		if closeErr := store.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			t.Fatalf("append conflicting owner generation: %v", err)
		}
		if _, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir); err == nil || !strings.Contains(err.Error(), "trigger") {
			t.Fatalf("restore with exact non-attention generation = %v, want trigger corruption", err)
		}
		rootFold, err := readDelegateAttentionFold(transcriptPath(fixture.stateDir, fixture.meta.ID), fixture.meta.ID)
		if err != nil {
			t.Fatalf("read root delivery replay after corruption: %v", err)
		}
		deliveryAttentionID := delegateAttentionID(delegateDeliveryID(fixture.delegateID, 1))
		if _, deliveredBeforeAdmission := rootFold.content[deliveryAttentionID]; !deliveredBeforeAdmission {
			t.Fatalf("prepared delivery %q was not drained before owed-generation scan", deliveryAttentionID)
		}
	})
}

func TestDelegateAttention_PrelaunchRecoveryDoesNotWaitForUnlaunchedRunner(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	c := root.delegateController
	actor := rootDelegateActor(root.id)
	seedDelegateControllerIdle(t, c, "dlg_nested_source", fixture.delegateID)
	seedDelegateControllerDelivery(t, c, "dlg_nested_source")
	fs := newAttentionFailNextSyncFS()
	root.delegateRestoreBeforeSideEffects = func(child *Session) {
		child.mu.Lock()
		oldWriter := child.transcript
		child.mu.Unlock()
		if err := oldWriter.Close(); err != nil {
			t.Fatalf("close restored child transcript: %v", err)
		}
		writer, err := transcript.NewWriterWithFS(fs, transcriptPath(child.stateDir, child.id), transcript.Header{SessionID: child.id})
		if err != nil {
			t.Fatalf("replace child transcript: %v", err)
		}
		child.attachTranscript(writer)
		child.cfg.testOnly.delegateInitialInputAppend = func(*Session) {
			fs.failSyncAfter(1, func() {
				if err := c.store.Close(); err != nil {
					t.Errorf("close delegate store at delivery fault: %v", err)
				}
			})
		}
	}

	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start successor", 0)
	if outcome.result.Err == nil || !strings.Contains(outcome.result.Err.Error(), "store is closed") {
		t.Fatalf("prelaunch double failure = %#v, want durable store failure", outcome.result)
	}
	if got := len(fixture.adapter.Requests()); got != 0 {
		t.Fatalf("prelaunch failure reached provider %d times", got)
	}

	reopened, err := delegatestore.Open(delegateResourceStorePath(fixture.stateDir, fixture.meta.ID))
	if err != nil {
		t.Fatalf("reopen delegate store: %v", err)
	}
	c.mu.Lock()
	c.store = reopened
	c.mu.Unlock()
	stop, cancelPlan, stopPlans, err := c.StopSubtree(actor, fixture.delegateID)
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	if err := root.executeDelegateMutationPlans(stopPlans); err != nil {
		t.Fatalf("execute stop plans: %v", err)
	}
	executeDelegateCancelPlan(cancelPlan)
	for range 3 {
		plans, err := c.Reconcile(emptyDelegateReconcileEvidence(c))
		if err != nil {
			t.Fatalf("Reconcile prelaunch recovery: %v", err)
		}
		if err := root.executeDelegateMutationPlans(plans); err != nil {
			t.Fatalf("execute prelaunch recovery plans: %v", err)
		}
	}
	select {
	case <-stop.done:
	default:
		c.mu.Lock()
		live := c.live[fixture.delegateID]
		aggregate := c.durable[fixture.delegateID]
		c.mu.Unlock()
		t.Fatalf("prelaunch recovery waited for an unlaunched runner: live=%#v aggregate=%#v", live, aggregate)
	}
}

func delegateAttentionToolCall(callID string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
		Kind: llm.ContentToolCall,
		ToolCall: &llm.ToolCallData{
			ID:        callID,
			Name:      "probe",
			Type:      "function",
			Arguments: json.RawMessage(`{}`),
		},
	}}}
}

func readAttentionTranscriptEntries(t *testing.T, path string) []transcript.Entry {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile transcript: %v", err)
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte{'\n'})
	entries := make([]transcript.Entry, 0, len(lines)-1)
	for _, line := range lines[1:] {
		entry, err := transcript.DecodeEntry(line)
		if err != nil {
			t.Fatalf("DecodeEntry: %v", err)
		}
		entries = append(entries, entry)
	}
	return entries
}

type attentionSyncBarrierFS struct {
	afero.Fs
	mu          sync.Mutex
	armed       bool
	syncEntered chan struct{}
	allowSync   chan struct{}
	enterOnce   sync.Once
	releaseOnce sync.Once
	// atSync, when set, runs on the syncing goroutine just before the barrier
	// is entered.
	atSync func()
}

type delegateAttentionLiveModelsAdapter struct {
	delegateAttentionPanicProvider
	onList    func()
	listCalls int
}

type delegateAttentionPanicProvider struct{}

var _ llm.ProviderAdapter = (*delegateAttentionPanicProvider)(nil)
var _ llm.LiveModelLister = (*delegateAttentionPanicProvider)(nil)

func (*delegateAttentionPanicProvider) Name() string { return "openai" }

func (*delegateAttentionPanicProvider) Complete(context.Context, llm.Request) (llm.Response, error) {
	panic("cold delivery replay called provider Complete")
}

func (*delegateAttentionPanicProvider) Stream(context.Context, llm.Request) (llm.Stream, error) {
	panic("cold delivery replay called provider Stream")
}

func (*delegateAttentionPanicProvider) LiveModels(context.Context) ([]registry.Model, error) {
	panic("cold delivery replay listed provider models")
}

func (a *delegateAttentionLiveModelsAdapter) LiveModels(context.Context) ([]registry.Model, error) {
	a.listCalls++
	if a.onList != nil {
		a.onList()
	}
	return nil, nil
}

func newAttentionSyncBarrierFS() *attentionSyncBarrierFS {
	return &attentionSyncBarrierFS{
		Fs:          afero.NewOsFs(),
		syncEntered: make(chan struct{}),
		allowSync:   make(chan struct{}),
	}
}

func (fs *attentionSyncBarrierFS) Create(name string) (afero.File, error) {
	file, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return &attentionSyncBarrierFile{File: file, fs: fs}, nil
}

func (fs *attentionSyncBarrierFS) arm() {
	fs.mu.Lock()
	fs.armed = true
	fs.mu.Unlock()
}

func (fs *attentionSyncBarrierFS) release() {
	fs.releaseOnce.Do(func() { close(fs.allowSync) })
}

type attentionSyncBarrierFile struct {
	afero.File
	fs *attentionSyncBarrierFS
}

func (file *attentionSyncBarrierFile) Sync() error {
	if err := file.File.Sync(); err != nil {
		return err
	}
	file.fs.mu.Lock()
	armed := file.fs.armed
	file.fs.mu.Unlock()
	if !armed {
		return nil
	}
	if file.fs.atSync != nil {
		file.fs.atSync()
	}
	file.fs.enterOnce.Do(func() { close(file.fs.syncEntered) })
	<-file.fs.allowSync
	return nil
}

type attentionFailNextSyncFS struct {
	afero.Fs
	mu        sync.Mutex
	failNext  bool
	failAfter int
	onFail    func()
	syncErr   error
}

func installResolutionSyncFailureWriter(t *testing.T, s *Session) {
	t.Helper()
	installResolutionSyncFailuresWriter(t, s, 1)
}

// injectedFailures is how many syncs the writer has failed so far.
func (fs *rootAttentionResolutionFailFS) injectedFailures() int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.failed
}

// installResolutionSyncFailuresWriter fails the sync of the session's first
// failures attention resolution writes.
//
// The session may still be running: a delegate reads idle before its run's
// tail has finished, and that tail checks its writer's health without
// attentionMu. So the replacement opens on the file's shared tail while the
// original is still open, the way reopenAttentionTranscriptDurably does, and
// the original stays open until the test ends, so a health check that already
// read it never finds it closed.
func installResolutionSyncFailuresWriter(t *testing.T, s *Session, failures int) *rootAttentionResolutionFailFS {
	t.Helper()
	fs := &rootAttentionResolutionFailFS{Fs: afero.NewOsFs(), failures: failures}
	path := transcriptPath(s.stateDir, s.id)
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	old := s.attachedTranscript()
	if old == nil {
		t.Fatal("root attention session has no transcript writer")
	}
	t.Cleanup(func() {
		if err := old.Close(); err != nil {
			t.Errorf("close original root attention writer: %v", err)
		}
	})
	replacement, entries, err := transcript.OpenWriterForSessionWithFS(fs, path, s.id)
	if err != nil {
		t.Fatalf("open resolution-failing root attention writer: %v", err)
	}
	replacement.TrackFailures(entries, s.fork.divergence)
	s.mu.Lock()
	s.setTranscriptLocked(replacement)
	s.mu.Unlock()
	return fs
}

type rootAttentionResolutionFailFS struct {
	afero.Fs
	mu       sync.Mutex
	armed    bool
	failures int
	failed   int
}

func (fs *rootAttentionResolutionFailFS) Create(name string) (afero.File, error) {
	file, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return &rootAttentionResolutionFailFile{File: file, fs: fs}, nil
}

func (fs *rootAttentionResolutionFailFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := fs.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &rootAttentionResolutionFailFile{File: file, fs: fs}, nil
}

type rootAttentionResolutionFailFile struct {
	afero.File
	fs *rootAttentionResolutionFailFS
}

func (file *rootAttentionResolutionFailFile) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte(`"kind":"ATTENTION_RESOLUTION"`)) {
		file.fs.mu.Lock()
		if file.fs.failed < file.fs.failures {
			file.fs.armed = true
		}
		file.fs.mu.Unlock()
	}
	return file.File.Write(p)
}

func (file *rootAttentionResolutionFailFile) Sync() error {
	file.fs.mu.Lock()
	fail := file.fs.armed && file.fs.failed < file.fs.failures
	if fail {
		file.fs.armed = false
		file.fs.failed++
	}
	file.fs.mu.Unlock()
	if fail {
		return errors.New("injected root attention resolution sync failure")
	}
	return file.File.Sync()
}

func newAttentionFailNextSyncFS() *attentionFailNextSyncFS {
	return &attentionFailNextSyncFS{Fs: afero.NewOsFs(), syncErr: errors.New("injected attention resolution sync failure")}
}

func (fs *attentionFailNextSyncFS) Create(name string) (afero.File, error) {
	file, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return &attentionFailNextSyncFile{File: file, fs: fs}, nil
}

func (fs *attentionFailNextSyncFS) failNextSync() {
	fs.failNextSyncWith(nil)
}

func (fs *attentionFailNextSyncFS) failNextSyncWith(onFail func()) {
	fs.failSyncAfter(0, onFail)
}

func (fs *attentionFailNextSyncFS) failSyncAfter(successfulSyncs int, onFail func()) {
	fs.mu.Lock()
	fs.failNext = true
	fs.failAfter = successfulSyncs
	fs.onFail = onFail
	fs.mu.Unlock()
}

type attentionFailNextSyncFile struct {
	afero.File
	fs *attentionFailNextSyncFS
}

type attentionAmbiguousSyncFS struct {
	afero.Fs
	mu                   sync.Mutex
	failSyncs            int
	failNextTruncate     bool
	failureInjected      bool
	successfulAfterFault int
}

func newAttentionAmbiguousSyncFS() *attentionAmbiguousSyncFS {
	return &attentionAmbiguousSyncFS{Fs: afero.NewOsFs()}
}

func (fs *attentionAmbiguousSyncFS) Create(name string) (afero.File, error) {
	file, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return &attentionAmbiguousSyncFile{File: file, fs: fs}, nil
}

func (fs *attentionAmbiguousSyncFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := fs.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &attentionAmbiguousSyncFile{File: file, fs: fs}, nil
}

func (fs *attentionAmbiguousSyncFS) failNextResolutionDurability() {
	// Two syncs: the durable append's own fsync, and the AppendSynced barrier
	// that would otherwise settle the retained record. Failing both is what
	// makes the resolution genuinely non-durable now that a whole line is debt
	// the next fsync clears rather than a poison.
	fs.failNextAmbiguousDurability(2)
}

func (fs *attentionAmbiguousSyncFS) failNextAmbiguousDurability(syncFailures int) {
	fs.mu.Lock()
	fs.failSyncs = syncFailures
	fs.failNextTruncate = true
	fs.failureInjected = true
	fs.successfulAfterFault = 0
	fs.mu.Unlock()
}

func (fs *attentionAmbiguousSyncFS) successfulSyncsAfterFailure() int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return fs.successfulAfterFault
}

type attentionAmbiguousSyncFile struct {
	afero.File
	fs *attentionAmbiguousSyncFS
}

func (file *attentionAmbiguousSyncFile) Sync() error {
	file.fs.mu.Lock()
	if file.fs.failSyncs > 0 {
		file.fs.failSyncs--
		file.fs.mu.Unlock()
		return errors.New("injected attention resolution sync failure")
	}
	failureInjected := file.fs.failureInjected
	file.fs.mu.Unlock()
	if err := file.File.Sync(); err != nil {
		return err
	}
	if failureInjected {
		file.fs.mu.Lock()
		file.fs.successfulAfterFault++
		file.fs.mu.Unlock()
	}
	return nil
}

func (file *attentionAmbiguousSyncFile) Truncate(size int64) error {
	file.fs.mu.Lock()
	if file.fs.failNextTruncate {
		file.fs.failNextTruncate = false
		file.fs.mu.Unlock()
		return errors.New("injected attention resolution rollback failure")
	}
	file.fs.mu.Unlock()
	return file.File.Truncate(size)
}

func (file *attentionFailNextSyncFile) Sync() error {
	file.fs.mu.Lock()
	fail := file.fs.failNext
	if fail && file.fs.failAfter > 0 {
		file.fs.failAfter--
		file.fs.mu.Unlock()
		return file.File.Sync()
	}
	file.fs.failNext = false
	onFail := file.fs.onFail
	file.fs.onFail = nil
	file.fs.mu.Unlock()
	if fail {
		if onFail != nil {
			onFail()
		}
		return file.fs.syncErr
	}
	return file.File.Sync()
}

func TestStableDelegateAttention_ReachableColdOwnerRetainsAttentionAfterRestoreFailure(t *testing.T) {
	c, journalPath := newDelegateControllerTestHarness(t, 2, 1)
	seedStableAttentionRepairDelegate(t, c, "dlg_owner", "", true)
	const attentionID = "delegate:reachable-owner"
	ownerPath := transcriptPath(c.stateDir, "child-dlg_owner")
	writeDelegateAttentionTranscript(t, ownerPath, "child-dlg_owner", attentionID)
	writeEmptyAttentionTranscript(t, transcriptPath(c.stateDir, "root-session"), "root-session")
	copyDelegateJournalForBootstrap(t, c, journalPath)

	metaPath := filepath.Join(c.stateDir, sessionsSubdir, "child-dlg_owner.meta.json")
	fresh := newAttentionRepairRoot(c.stateDir, nil)
	fresh.cfg.testOnly.delegateRestoreReadFile = func(path string) ([]byte, error) {
		if path == metaPath {
			return nil, &os.PathError{Op: "read", Path: path, Err: syscall.EIO}
		}
		return os.ReadFile(path)
	}
	if err := fresh.bootstrapDelegateResources(); err == nil {
		t.Fatal("bootstrap accepted a transient cold-owner restore inspection failure")
	}
	ownerFold, err := readDelegateAttentionFold(ownerPath, "child-dlg_owner")
	if err != nil {
		t.Fatalf("read reachable owner attention: %v", err)
	}
	if pending := ownerFold.pendingIDs(); !reflect.DeepEqual(pending, []string{attentionID}) {
		t.Fatalf("reachable cold owner attention after transient failure = %#v, want retained", pending)
	}
	rootFold, err := readDelegateAttentionFold(transcriptPath(c.stateDir, "root-session"), "root-session")
	if err != nil {
		t.Fatalf("read root attention: %v", err)
	}
	if _, escalated := rootFold.content[attentionID]; escalated {
		t.Fatal("transient cold-owner restore failure escalated attention to root")
	}
}

func TestStableDelegateAttention_UnreachableOwnerTransfersToNearestReachableAncestor(t *testing.T) {
	c, journalPath := newDelegateControllerTestHarness(t, 3, 1)
	seedStableAttentionRepairDelegate(t, c, "dlg_parent", "", true)
	seedStableAttentionRepairDelegate(t, c, "dlg_child", "dlg_parent", false)
	const attentionID = "delegate:unreachable-child"
	parentPath := transcriptPath(c.stateDir, "child-dlg_parent")
	childPath := transcriptPath(c.stateDir, "child-dlg_child")
	writeEmptyAttentionTranscript(t, parentPath, "child-dlg_parent")
	writeDelegateAttentionTranscript(t, childPath, "child-dlg_child", attentionID)
	writeEmptyAttentionTranscript(t, transcriptPath(c.stateDir, "root-session"), "root-session")
	copyDelegateJournalForBootstrap(t, c, journalPath)

	fresh := newAttentionRepairRoot(c.stateDir, nil)
	if err := fresh.bootstrapDelegateResources(); err != nil {
		t.Fatalf("bootstrap unreachable attention repair: %v", err)
	}
	t.Cleanup(func() { _ = fresh.closeOwnedDelegateStore() })
	parentFold, err := readDelegateAttentionFold(parentPath, "child-dlg_parent")
	if err != nil {
		t.Fatalf("read ancestor attention: %v", err)
	}
	if pending := parentFold.pendingIDs(); !reflect.DeepEqual(pending, []string{attentionID}) || parentFold.content[attentionID].Text() != "attention" {
		t.Fatalf("nearest ancestor attention = pending:%#v content:%#v", pending, parentFold.content[attentionID])
	}
	childFold, err := readDelegateAttentionFold(childPath, "child-dlg_child")
	if err != nil {
		t.Fatalf("read child attention: %v", err)
	}
	if got := childFold.resolutions[attentionID]; got != delegateAttentionDiscarded {
		t.Fatalf("unreachable child resolution = %q, want discarded after transfer", got)
	}
	rootFold, err := readDelegateAttentionFold(transcriptPath(c.stateDir, "root-session"), "root-session")
	if err != nil {
		t.Fatalf("read root attention: %v", err)
	}
	if _, present := rootFold.content[attentionID]; present {
		t.Fatal("attention skipped the nearest reachable delegate ancestor")
	}
}

func TestStableDelegateAttention_AncestorFsyncPrecedesChildDiscard(t *testing.T) {
	c, journalPath := newDelegateControllerTestHarness(t, 3, 1)
	seedStableAttentionRepairDelegate(t, c, "dlg_parent", "", true)
	seedStableAttentionRepairDelegate(t, c, "dlg_child", "dlg_parent", false)
	const attentionID = "delegate:ordered-transfer"
	parentPath := transcriptPath(c.stateDir, "child-dlg_parent")
	childPath := transcriptPath(c.stateDir, "child-dlg_child")
	writeEmptyAttentionTranscript(t, parentPath, "child-dlg_parent")
	writeDelegateAttentionTranscript(t, childPath, "child-dlg_child", attentionID)
	writeEmptyAttentionTranscript(t, transcriptPath(c.stateDir, "root-session"), "root-session")
	copyDelegateJournalForBootstrap(t, c, journalPath)

	fs := newAttentionPathOrderFS()
	fresh := newAttentionRepairRoot(c.stateDir, nil)
	fresh.cfg.testOnly.delegateAttentionOpenWriter = func(path, expectedSessionID string) (*transcript.Writer, []transcript.Entry, error) {
		return transcript.OpenWriterForSessionWithFS(fs, path, expectedSessionID)
	}
	if err := fresh.bootstrapDelegateResources(); err != nil {
		t.Fatalf("bootstrap ordered attention repair: %v", err)
	}
	t.Cleanup(func() { _ = fresh.closeOwnedDelegateStore() })
	paths := fs.syncedPaths()
	parentIndex, childIndex := pathIndex(paths, parentPath), pathIndex(paths, childPath)
	if parentIndex < 0 || childIndex < 0 || parentIndex >= childIndex {
		t.Fatalf("attention repair fsync order = %#v, want ancestor %q before child %q", paths, parentPath, childPath)
	}
}

func TestStableDelegateAttention_ConsumedEntryIsNeverEscalated(t *testing.T) {
	c, journalPath := newDelegateControllerTestHarness(t, 3, 1)
	seedStableAttentionRepairDelegate(t, c, "dlg_parent", "", true)
	seedStableAttentionRepairDelegate(t, c, "dlg_child", "dlg_parent", false)
	const attentionID = "delegate:already-consumed"
	parentPath := transcriptPath(c.stateDir, "child-dlg_parent")
	childPath := transcriptPath(c.stateDir, "child-dlg_child")
	writeEmptyAttentionTranscript(t, parentPath, "child-dlg_parent")
	writeDelegateAttentionTranscript(t, childPath, "child-dlg_child", attentionID)
	if err := appendColdAttentionResolution(childPath, "child-dlg_child", []string{attentionID}, delegateAttentionConsumed); err != nil {
		t.Fatalf("append consumed resolution: %v", err)
	}
	writeEmptyAttentionTranscript(t, transcriptPath(c.stateDir, "root-session"), "root-session")
	copyDelegateJournalForBootstrap(t, c, journalPath)

	fresh := newAttentionRepairRoot(c.stateDir, nil)
	if err := fresh.bootstrapDelegateResources(); err != nil {
		t.Fatalf("bootstrap consumed attention repair: %v", err)
	}
	t.Cleanup(func() { _ = fresh.closeOwnedDelegateStore() })
	parentFold, err := readDelegateAttentionFold(parentPath, "child-dlg_parent")
	if err != nil {
		t.Fatalf("read ancestor attention: %v", err)
	}
	if _, present := parentFold.content[attentionID]; present {
		t.Fatal("consumed child attention was escalated")
	}
	childFold, err := readDelegateAttentionFold(childPath, "child-dlg_child")
	if err != nil {
		t.Fatalf("read consumed child attention: %v", err)
	}
	if got := childFold.resolutions[attentionID]; got != delegateAttentionConsumed {
		t.Fatalf("consumed child resolution changed to %q", got)
	}
}

func TestStableDelegateAttention_StartupRepairUsesNoProvider(t *testing.T) {
	c, journalPath := newDelegateControllerTestHarness(t, 2, 1)
	seedStableAttentionRepairDelegate(t, c, "dlg_child", "", false)
	const attentionID = "delegate:provider-free-transfer"
	childPath := transcriptPath(c.stateDir, "child-dlg_child")
	rootPath := transcriptPath(c.stateDir, "root-session")
	writeDelegateAttentionTranscript(t, childPath, "child-dlg_child", attentionID)
	writeEmptyAttentionTranscript(t, rootPath, "root-session")
	copyDelegateJournalForBootstrap(t, c, journalPath)

	client := llm.NewClient()
	client.Register(&delegateAttentionPanicProvider{})
	fresh := newAttentionRepairRoot(c.stateDir, client)
	fresh.cfg.testOnly.sessionInitFault = func(point string) error {
		panic("startup attention repair constructed a Session at " + point)
	}
	if err := fresh.bootstrapDelegateResources(); err != nil {
		t.Fatalf("provider-free startup attention repair: %v", err)
	}
	t.Cleanup(func() { _ = fresh.closeOwnedDelegateStore() })
	rootFold, err := readDelegateAttentionFold(rootPath, "root-session")
	if err != nil {
		t.Fatalf("read root attention: %v", err)
	}
	if pending := rootFold.pendingIDs(); !reflect.DeepEqual(pending, []string{attentionID}) {
		t.Fatalf("provider-free root transfer = %#v, want %q", pending, attentionID)
	}
	childFold, err := readDelegateAttentionFold(childPath, "child-dlg_child")
	if err != nil {
		t.Fatalf("read child attention: %v", err)
	}
	if got := childFold.resolutions[attentionID]; got != delegateAttentionDiscarded {
		t.Fatalf("provider-free child discard = %q", got)
	}
}

func seedStableAttentionRepairDelegate(t *testing.T, c *delegateTreeController, id, parentID string, reachable bool) {
	t.Helper()
	event := delegateControllerCreatedEvent(id, parentID)
	event.Created.Descriptor.ResolvedProfileID = "openai"
	event.Created.Descriptor.ResolvedModel = "gpt-5.2"
	c.mu.Lock()
	_, err := c.appendLocked(event)
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("seed attention repair delegate %s: %v", id, err)
	}
	if !reachable {
		return
	}
	parentSessionID := "root-session"
	if parentID != "" {
		parentSessionID = "child-" + parentID
	}
	if err := schema.SaveSessionMeta(c.stateDir, schema.SessionMeta{
		ID:              "child-" + id,
		ParentSessionID: parentSessionID,
		ProfileID:       "openai",
		Model:           "gpt-5.2",
		IsSubagent:      true,
	}); err != nil {
		t.Fatalf("save reachable delegate metadata %s: %v", id, err)
	}
}

func writeEmptyAttentionTranscript(t *testing.T, path, sessionID string) {
	t.Helper()
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("create empty attention transcript %s: %v", sessionID, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close empty attention transcript %s: %v", sessionID, err)
	}
}

func copyDelegateJournalForBootstrap(t *testing.T, c *delegateTreeController, sourcePath string) {
	t.Helper()
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatalf("read delegate journal: %v", err)
	}
	if err := c.store.Close(); err != nil {
		t.Fatalf("close delegate journal: %v", err)
	}
	destination := filepath.Join(jobsDir(c.stateDir, "root-session"), "delegates.jsonl")
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatalf("create bootstrap delegate directory: %v", err)
	}
	if err := os.WriteFile(destination, raw, 0o644); err != nil {
		t.Fatalf("write bootstrap delegate journal: %v", err)
	}
}

func newAttentionRepairRoot(stateDir string, client *llm.Client) *Session {
	return &Session{
		id:       "root-session",
		stateDir: stateDir,
		client:   client,
		state:    SessionIdle,
		cfg: SessionConfig{
			StateDir:                   stateDir,
			MaxConcurrentDelegateTurns: 4,
		},
	}
}

type attentionPathOrderFS struct {
	afero.Fs
	mu    sync.Mutex
	paths []string
}

func newAttentionPathOrderFS() *attentionPathOrderFS {
	return &attentionPathOrderFS{Fs: afero.NewOsFs()}
}

func (fs *attentionPathOrderFS) Create(name string) (afero.File, error) {
	file, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return &attentionPathOrderFile{File: file, fs: fs, path: filepath.Clean(name)}, nil
}

func (fs *attentionPathOrderFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := fs.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return &attentionPathOrderFile{File: file, fs: fs, path: filepath.Clean(name)}, nil
}

func (fs *attentionPathOrderFS) syncedPaths() []string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]string(nil), fs.paths...)
}

type attentionPathOrderFile struct {
	afero.File
	fs   *attentionPathOrderFS
	path string
}

func (file *attentionPathOrderFile) Sync() error {
	if err := file.File.Sync(); err != nil {
		return err
	}
	file.fs.mu.Lock()
	file.fs.paths = append(file.fs.paths, file.path)
	file.fs.mu.Unlock()
	return nil
}

func pathIndex(paths []string, want string) int {
	want = filepath.Clean(want)
	for index, path := range paths {
		if filepath.Clean(path) == want {
			return index
		}
	}
	return -1
}

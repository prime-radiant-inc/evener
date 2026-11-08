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
	"slices"
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

func TestDelegateAttention_ResolutionFsyncPrecedesSourceAck(t *testing.T) {
	const (
		sessionID = "child-dlg_target"
		firstID   = "delegate:delivery-attention-1"
		secondID  = "delegate:delivery-attention-2"
	)
	fs := newAttentionSyncBarrierFS()
	c, runtime, path := newDelegateAttentionAcceptanceHarness(t, sessionID, fs, firstID, secondID)
	beforeRevision := c.durable["dlg_target"].ProjectionRevision
	reservation, err := c.ReserveAttention(runtime, firstID)
	if err != nil {
		t.Fatalf("ReserveAttention: %v", err)
	}

	// The trigger is latched by the time its consumed marker is being made
	// durable, so a fold that sees the item resolved already knows to keep it.
	// The hook runs inside the resolution's attentionMu section.
	var latchedDuringResolution string
	fs.atSync = func() { latchedDuringResolution = runtime.attentionAcceptedID }
	fs.arm()
	type startResult struct {
		started delegateStartCommit
		err     error
	}
	done := make(chan startResult, 1)
	go func() {
		startErr := runtime.acceptDelegateAttention(reservation)
		var started delegateStartCommit
		if startErr == nil {
			started, startErr = c.CommitStart(reservation)
		}
		done <- startResult{started: started, err: startErr}
	}()
	select {
	case <-fs.syncEntered:
	case result := <-done:
		t.Fatalf("attention acceptance returned before fsync: %#v", result)
	}
	c.mu.Lock()
	blockedAggregate := c.durable["dlg_target"]
	blockedGeneration := blockedAggregate.Generation
	blockedNeedsAttention := blockedAggregate.NeedsAttention
	_, firstStillUnresolved := attentionWakeIDsOf(c, "dlg_target")[firstID]
	c.mu.Unlock()
	if blockedGeneration != 0 || !blockedNeedsAttention || !firstStillUnresolved {
		t.Fatalf("acceptance published before resolution fsync: generation=%d needs=%t unresolved=%#v", blockedGeneration, blockedNeedsAttention, attentionWakeIDsOf(c, "dlg_target"))
	}
	fs.release()
	first := <-done
	if first.err != nil {
		t.Fatalf("accept first attention: %v", first.err)
	}
	if latchedDuringResolution != firstID {
		t.Fatalf("trigger latched while its consumed marker was made durable = %q, want %q", latchedDuringResolution, firstID)
	}
	aggregate := c.durable["dlg_target"]
	if aggregate.Generation != 1 || aggregate.Trigger != delegatestore.TriggerAttention || !aggregate.CurrentRunOpen || !aggregate.NeedsAttention || aggregate.ProjectionRevision != beforeRevision+1 {
		t.Fatalf("nonfinal acceptance aggregate = %#v, want generation 1 open with unchanged true attention", aggregate)
	}
	if got := attentionWakeIDsOf(c, "dlg_target"); !reflect.DeepEqual(got, map[string]struct{}{secondID: {}}) {
		t.Fatalf("unresolved IDs after nonfinal consumption = %#v", got)
	}
	fold, err := readDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("read attention fold: %v", err)
	}
	if got := fold.resolutions[firstID]; got != delegateAttentionConsumed || fold.resumeGenerations[firstID] != 1 {
		t.Fatalf("first resolution = %q generation %d, want consumed/1", got, fold.resumeGenerations[firstID])
	}
	if _, err := c.FinishGeneration(first.started.lease, delegateFinish{outcome: delegatestore.OutcomeCompleted, reason: "first handled"}); err != nil {
		t.Fatalf("FinishGeneration first: %v", err)
	}
	reportFinalizeTailDone(t, c, first.started.lease, runtime)
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
	aggregate = c.durable["dlg_target"]
	if aggregate.Generation != 2 || aggregate.NeedsAttention || len(attentionWakeIDsOf(c, "dlg_target")) != 0 {
		t.Fatalf("final acceptance aggregate=%#v unresolved=%#v", aggregate, attentionWakeIDsOf(c, "dlg_target"))
	}
	fold, err = readDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("read final attention fold: %v", err)
	}
	if got := fold.resolutions[secondID]; got != delegateAttentionConsumed || fold.resumeGenerations[secondID] != 2 {
		t.Fatalf("second resolution = %q generation %d, want consumed/2", got, fold.resumeGenerations[secondID])
	}
	events, err := c.store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tail := events[len(events)-2:]
	if tail[0].Kind != delegatestore.EventDelegateAttentionChanged || tail[0].AttentionChanged == nil || tail[0].AttentionChanged.NeedsAttention || tail[1].Kind != delegatestore.EventDelegateRunStarted || tail[1].RunStarted.Generation != second.lease.generation {
		t.Fatalf("final-clear/run-start batch tail = %#v", tail)
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
		fs := newAttentionSyncBarrierFS()
		c, runtime, path := newDelegateAttentionAcceptanceHarness(t, sessionID, fs, attentionID)
		reservation, err := c.ReserveAttention(runtime, attentionID)
		if err != nil {
			t.Fatalf("ReserveAttention: %v", err)
		}
		fs.arm()
		accepted := make(chan error, 1)
		go func() {
			acceptErr := runtime.acceptDelegateAttention(reservation)
			if acceptErr == nil {
				_, acceptErr = c.CommitStart(reservation)
			}
			accepted <- acceptErr
		}()
		<-fs.syncEntered
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
		fs.release()
		if err := <-accepted; err != nil {
			t.Fatalf("accept admitted attention: %v", err)
		}
		if err := <-stopped; err != nil {
			t.Fatalf("StopSubtree after acceptance: %v", err)
		}
		fold, err := readDelegateAttentionFold(path, sessionID)
		if err != nil {
			t.Fatalf("read admitted attention fold: %v", err)
		}
		if fold.resolutions[attentionID] != delegateAttentionConsumed || fold.resumeGenerations[attentionID] != 1 {
			t.Fatalf("admitted resolution = %q generation %d", fold.resolutions[attentionID], fold.resumeGenerations[attentionID])
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
	runtime := &Session{id: sessionID, stateDir: c.stateDir, delegateController: c}
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
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatalf("create attention transcript: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	runtime.id = sessionID
	runtime.stateDir = c.stateDir
	runtime.delegateController = c
	runtime.owningDelegateID = delegateID
	runtime.attachTranscript(writer)
	turn := schema.NewTurn(schema.TurnSteering, llm.User("attention "+attentionID))
	turn.AttentionID = attentionID
	if err := writer.AppendDurable(turn); err != nil {
		t.Fatalf("append attention: %v", err)
	}
	c.noteDelegateAttention(delegateID, attentionID)
}

func TestDelegateAttention_ResolutionFailureLeavesGenerationStoppable(t *testing.T) {
	const (
		sessionID   = "child-dlg_target"
		attentionID = "attention-resolution-failure"
	)
	fs := newAttentionAmbiguousSyncFS()
	c, runtime, path := newDelegateAttentionAcceptanceHarness(t, sessionID, fs, attentionID)
	reservation, err := c.ReserveAttention(runtime, attentionID)
	if err != nil {
		t.Fatalf("ReserveAttention: %v", err)
	}
	fs.failNextResolutionDurability()
	if err := runtime.acceptDelegateAttention(reservation); err == nil {
		t.Fatal("consumed attention resolution unexpectedly survived injected sync failure")
	}
	// The marker may have landed, so the retry still needs the trigger's turn.
	runtime.attentionMu.Lock()
	latched := runtime.attentionAcceptedID
	runtime.attentionMu.Unlock()
	if latched != attentionID {
		t.Fatalf("trigger latched after the ambiguous failure = %q, want %q", latched, attentionID)
	}
	fold, err := readDelegateAttentionFold(path, sessionID)
	if err != nil {
		t.Fatalf("read ambiguous attention resolution: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != delegateAttentionConsumed || fold.resumeGenerations[attentionID] != 1 {
		t.Fatalf("page-cache-visible attention resolution = %q generation %d, want consumed/1", got, fold.resumeGenerations[attentionID])
	}
	if aggregate := c.durable["dlg_target"]; aggregate.Generation != 0 || aggregate.CurrentRunOpen || !aggregate.NeedsAttention {
		t.Fatalf("failed resolution admitted generation: %#v", aggregate)
	}
	if len(c.reservations) != 1 || c.live["dlg_target"].binding != nil {
		t.Fatalf("failed resolution lost retry claim or installed binding: reservations=%#v live=%#v", c.reservations, c.live["dlg_target"])
	}
	if err := runtime.acceptDelegateAttention(reservation); err != nil {
		t.Fatalf("retry ambiguous attention resolution: %v", err)
	}
	started, err := c.CommitStart(reservation)
	if err != nil {
		t.Fatalf("commit retried attention generation: %v", err)
	}
	if got := fs.successfulSyncsAfterFailure(); got == 0 {
		t.Fatal("attention retry established no successful child-transcript durability barrier")
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
	runtime.owningDelegateID = "dlg_target"
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
	go func() { armDone <- runtime.armDelegateAttentionOnce(attentionID) }()
	<-foldEntered

	// The reservation is the controller-side blocker that used to deadlock
	// armDelegateAttentionOnce while it held attentionMu. The acceptance path
	// must resolve the exact ID before it can close that blocker.
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
	if got := fold.resolutions[attentionID]; got != delegateAttentionConsumed {
		t.Fatalf("attention resolution = %q, want consumed", got)
	}
	events, err := c.store.Load()
	if err != nil {
		t.Fatalf("load controller journal: %v", err)
	}
	state, err := delegatestore.Fold(events)
	if err != nil {
		t.Fatalf("fold controller journal: %v", err)
	}
	if state["dlg_target"].NeedsAttention || len(attentionWakeIDsOf(c, "dlg_target")) != 0 {
		t.Fatalf("consumed attention reopened: state=%#v wakeIDs=%#v", state["dlg_target"], attentionWakeIDsOf(c, "dlg_target"))
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

	fixture := newColdDelegateFixture(t, "")
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
				return agenttest.FinalResponse("successor completed after recovery"), nil
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

	rawValue, err := readRaw(context.Background(), path, "", "local:"+sessionID, "")
	if err != nil {
		t.Fatalf("read raw transcript: %v", err)
	}
	raw, ok := rawValue.(readRawEnvelope)
	if !ok {
		t.Fatalf("raw transcript type = %T", rawValue)
	}
	pin := 1
	expandedValue, err := readMarkdownPage(context.Background(), path, "", "local:"+sessionID, schema.SessionMeta{}, "", &pin, 0, maxExpansionBytes)
	if err != nil {
		t.Fatalf("expand transcript turn: %v", err)
	}
	expanded, ok := expandedValue.(readMarkdownEnvelope)
	if !ok || expanded.Expansion == nil {
		t.Fatalf("expanded transcript = %#v", expandedValue)
	}
	outlineValue, err := readOutline(context.Background(), path, "", "local:"+sessionID, "")
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

	privateSnippets, opened := contentSnippets(context.Background(), stateDir, sessionID, "Attention resolved.", "attention resolved.")
	if !opened {
		t.Fatal("find_session_transcripts content scan did not open the real transcript")
	}
	if len(privateSnippets) != 0 {
		t.Fatalf("find_session_transcripts exposed the private resolution marker: %#v", privateSnippets)
	}
	visibleSnippets, opened := contentSnippets(context.Background(), stateDir, sessionID, "visible result", "visible result")
	if !opened || len(visibleSnippets) != 1 || visibleSnippets[0].Seq != 1 {
		t.Fatalf("find_session_transcripts visible tool-result query = opened %v, snippets %#v", opened, visibleSnippets)
	}

	rawWindowValue, err := readRaw(context.Background(), path, "", "local:"+sessionID, "last:2")
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

	markdownWindowValue, err := readMarkdownPage(context.Background(), path, "", "local:"+sessionID, schema.SessionMeta{}, "last:2", nil, 0, 0)
	if err != nil {
		t.Fatalf("read ranged markdown transcript: %v", err)
	}
	markdownWindow := markdownWindowValue.(readMarkdownEnvelope)
	if !strings.Contains(markdownWindow.Content, "## Turn 1 — Assistant") || !strings.Contains(markdownWindow.Content, "visible result") || strings.Contains(markdownWindow.Content, "Tool results without a shown call") {
		t.Fatalf("ranged markdown split the public tool round: %s", markdownWindow.Content)
	}

	outlineWindowValue, err := readOutline(context.Background(), path, "", "local:"+sessionID, "last:2")
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
	findSnippets, opened := contentSnippets(context.Background(), stateDir, findSessionID, "later public match", "later public match")
	if !opened || len(findSnippets) != 1 || findSnippets[0].Seq != 1 {
		t.Fatalf("public find sequence = opened %v snippets %#v, want public seq 1", opened, findSnippets)
	}
	findPin := findSnippets[0].Seq
	findExpansionValue, err := readMarkdownPage(context.Background(), findPath, "", "local:"+findSessionID, schema.SessionMeta{}, "", &findPin, 0, maxExpansionBytes)
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

	rawValue, err := readRaw(context.Background(), path, "", "local:"+sessionID, "")
	if err != nil {
		t.Fatalf("read raw transcript: %v", err)
	}
	raw := rawValue.(readRawEnvelope)
	pin := 0
	expandedValue, err := readMarkdownPage(context.Background(), path, "", "local:"+sessionID, schema.SessionMeta{}, "", &pin, 0, maxExpansionBytes)
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
	missing := filepath.Join(stateDir, schema.SessionsSubdir, "missing.transcript.jsonl")
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
	token, admitted, err := c.BeginDelivery(firstPlan)
	if err != nil || !admitted {
		t.Fatalf("BeginDelivery = admitted:%t err:%v", admitted, err)
	}
	commit := &delegateToolResultCommit{controller: c, token: token, deliveryID: firstPlan.deliveryID}

	fs := newAttentionSyncBarrierFS()
	path := transcriptPath(stateDir, ownerSessionID)
	writer, err := transcript.NewWriterWithFS(fs, path, transcript.Header{SessionID: ownerSessionID})
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	original := &Session{id: ownerSessionID, stateDir: stateDir, delegateController: c}
	original.attachTranscript(writer)
	c.rootRuntime = original
	original.queueDelegateDeliveryCommit("delegate-send-N", commit)
	fs.arm()
	appendDone := make(chan error, 1)
	go func() { appendDone <- appendDelegateToolResultFixture(original, "delegate-send-N") }()
	select {
	case <-fs.syncEntered:
	case err := <-appendDone:
		t.Fatalf("caller tool result returned before fsync barrier: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close crashed controller store: %v", err)
	}
	fs.release()
	if err := <-appendDone; err == nil {
		t.Fatal("crashed process unexpectedly acknowledged through its closed delegate store")
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
	fresh.delegateDeliveryMu.Lock()
	queued := append([]delegateDeliveryPlan(nil), fresh.pendingDelegateDeliveries...)
	fresh.delegateDeliveryMu.Unlock()
	if len(pending) != 1 || pending[0].DeliveryID != delegateDeliveryID("dlg_target", 2) || claims != 1 || len(queued) != 1 || queued[0].deliveryID != pending[0].DeliveryID {
		t.Fatalf("cold replay pending=%#v claims=%d queued=%#v, want only claimed N+1 queued for attachment", pending, claims, queued)
	}

	rootPath := transcriptPath(stateDir, rootSessionID)
	reopened, err := transcript.NewWriter(rootPath, transcript.Header{SessionID: rootSessionID})
	if err != nil {
		t.Fatalf("create root transcript: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	fresh.attachTranscript(reopened)
	if err := fresh.flushPendingDelegateDeliveries(); err != nil {
		t.Fatalf("release N+1 after transcript attachment: %v", err)
	}

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
	fresh.delegateController.mu.Lock()
	remaining := len(fresh.delegateController.durable["dlg_target"].PendingDeliveries)
	fresh.delegateController.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("pending deliveries after N+1 replay = %d, want 0", remaining)
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
	if _, _, _, err := c.StopSubtree(rootDelegateActor(rootSessionID), delegateID); err != nil {
		_ = store.Close()
		t.Fatalf("persist stop request: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close crashed delegate store: %v", err)
	}

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
	fresh.delegateDeliveryMu.Lock()
	queued := append([]delegateDeliveryPlan(nil), fresh.pendingDelegateDeliveries...)
	fresh.delegateDeliveryMu.Unlock()
	if claims != 1 || len(queued) != 1 || queued[0].deliveryID != delegateDeliveryID(delegateID, 1) {
		t.Fatalf("restored stop delivery claims=%d queued=%#v, want one preserved terminal delivery", claims, queued)
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
	pump.pendingDelegateDeliveries = append(pump.pendingDelegateDeliveries, pending...)
	if err := pump.flushPendingDelegateDeliveries(); err != nil {
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
	root.attentionMu.Lock()
	wakeIDs := len(root.rootAttentionWakeIDs)
	wakeArmed := root.rootAttentionWake
	root.attentionMu.Unlock()
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
			return agenttest.CommunicateResponse(true, "retried exact attention")
		}),
	)
	c := root.delegateController
	c.mu.Lock()
	created := delegateControllerCreatedEvent("dlg_target", "")
	created.Created.Descriptor.OwnerSessionID = root.ID()
	_, err := c.appendLocked(created)
	c.mu.Unlock()
	if err != nil {
		t.Fatalf("seed target delegate: %v", err)
	}
	seedDelegateControllerDelivery(t, c, "dlg_target")
	plans := c.ReplayDeliveries()
	if len(plans) != 1 {
		t.Fatalf("delivery plans = %#v, want one", plans)
	}

	readCalls := 0
	injected := errors.New("injected post-ack attention fold failure")
	root.cfg.testOnly.delegateAttentionReadFold = func(path, sessionID string) (delegateAttentionFold, error) {
		readCalls++
		if readCalls == 3 {
			return delegateAttentionFold{}, injected
		}
		return readDelegateAttentionFold(path, sessionID)
	}
	wakes := make(chan struct{}, 2)
	root.SetNotifyFunc(func() { wakes <- struct{}{} })
	retryTimersBefore := clock.BlockedCount()
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
	if got := clock.BlockedCount(); got != retryTimersBefore+1 {
		t.Fatalf("post-ack fold failure retry timers = %d, want baseline %d plus one", got, retryTimersBefore)
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
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	const attentionID = "watch:cold-post-ack:1"
	path := transcriptPath(c.stateDir, "child-dlg_target")
	writeDelegateAttentionTranscript(t, path, "child-dlg_target", attentionID)

	clock := agenttest.NewFakeClock()
	root := &Session{
		id:                     "root-session",
		stateDir:               c.stateDir,
		state:                  SessionIdle,
		clock:                  clock,
		delegateController:     c,
		delegateRootSessionID:  "root-session",
		ownsDelegateController: true,
	}
	c.rootRuntime = root
	wakes := make(chan struct{}, 1)
	root.SetNotifyFunc(func() { wakes <- struct{}{} })
	retryTimersBefore := clock.BlockedCount()

	backup := path + ".readable"
	if err := os.Rename(path, backup); err != nil {
		t.Fatalf("hide cold attention transcript: %v", err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		_ = os.Rename(backup, path)
		t.Fatalf("install cold attention read fault: %v", err)
	}
	err := c.armColdDelegateAttention("dlg_target", attentionID)
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
	if root.sessionWorkPending() || len(attentionWakeIDsOf(c, "dlg_target")) != 0 {
		t.Fatalf("failed cold fold published unverified attention: work=%t unresolved=%#v", root.sessionWorkPending(), attentionWakeIDsOf(c, "dlg_target"))
	}
	if err := c.armColdDelegateAttention("dlg_target", attentionID); err != nil {
		t.Fatalf("caller retry cold attention: %v", err)
	}
	<-wakes
	delegateID, gotAttentionID, pending := c.nextIdleDelegateAttention()
	if !pending || delegateID != "dlg_target" || gotAttentionID != attentionID {
		t.Fatalf("retried cold attention = delegate:%q attention:%q pending:%t", delegateID, gotAttentionID, pending)
	}
}

func TestDelegateAttention_SettledResidentChildStartsExactAttentionGeneration(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	attentionEntered := make(chan struct{})
	releaseAttention := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseAttention) }) })
	attentionContent := `<job-notification event="watch_send">drive exact attention</job-notification>`
	attentionSeen := false
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return agenttest.CommunicateResponse(true, "initial run complete") },
		func(request llm.Request) llm.Response {
			attentionSeen = requestContainsText(request, attentionContent)
			close(attentionEntered)
			<-releaseAttention
			return agenttest.CommunicateResponse(true, "attention handled")
		},
	})
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
	if err := sub.sess.armDelegateAttention(attentionID); err != nil {
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
	_, entries, _, err := readTranscript(context.Background(), path, "")
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

	result, _, _, err := c.StopSubtree(rootDelegateActor("root-session"), "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
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
}

func TestDelegateAttention_BootstrapFlushRoutesNestedDeliveryToOwner(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 2, 1)
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_child", "dlg_parent")
	seedDelegateControllerDelivery(t, c, "dlg_child")

	const (
		rootSessionID  = "root-session"
		ownerSessionID = "child-dlg_parent"
	)
	ownerPath := transcriptPath(c.stateDir, ownerSessionID)
	ownerWriter, err := transcript.NewWriter(ownerPath, transcript.Header{SessionID: ownerSessionID})
	if err != nil {
		t.Fatalf("create owner transcript: %v", err)
	}
	if err := ownerWriter.Close(); err != nil {
		t.Fatalf("close owner transcript: %v", err)
	}
	rootPath := transcriptPath(c.stateDir, rootSessionID)
	rootWriter, err := transcript.NewWriter(rootPath, transcript.Header{SessionID: rootSessionID})
	if err != nil {
		t.Fatalf("create root transcript: %v", err)
	}
	t.Cleanup(func() { _ = rootWriter.Close() })
	root := &Session{id: rootSessionID, stateDir: c.stateDir, delegateController: c}
	root.attachTranscript(rootWriter)
	c.rootRuntime = root

	plans := c.ReplayDeliveries()
	if len(plans) != 1 || plans[0].ownerDelegateID != "dlg_parent" {
		t.Fatalf("nested delivery plans = %#v", plans)
	}
	root.pendingDelegateDeliveries = append(root.pendingDelegateDeliveries, plans[0])
	if err := root.flushPendingDelegateDeliveries(); err != nil {
		t.Fatalf("flush pending nested delivery: %v", err)
	}

	attentionID := delegateAttentionID(plans[0].deliveryID)
	ownerFold, err := readDelegateAttentionFold(ownerPath, ownerSessionID)
	if err != nil {
		t.Fatalf("read owner attention: %v", err)
	}
	if pending := ownerFold.pendingIDs(); !reflect.DeepEqual(pending, []string{attentionID}) {
		t.Fatalf("owner pending attention = %#v, want %q", pending, attentionID)
	}
	rootFold, err := readDelegateAttentionFold(rootPath, rootSessionID)
	if err != nil {
		t.Fatalf("read root attention: %v", err)
	}
	if pending := rootFold.pendingIDs(); len(pending) != 0 {
		t.Fatalf("nested delivery leaked to root attention: %#v", pending)
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
	if err := c.AttachRuntime(started.lease, owner); err != nil {
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
	root.pendingDelegateDeliveries = append(root.pendingDelegateDeliveries, plans[0])
	if err := writer.Close(); err != nil {
		t.Fatalf("close transcript writer: %v", err)
	}
	if err := root.flushPendingDelegateDeliveries(); err == nil {
		t.Fatal("flush with closed transcript writer succeeded")
	}
	root.delegateDeliveryMu.Lock()
	queued := append([]delegateDeliveryPlan(nil), root.pendingDelegateDeliveries...)
	root.delegateDeliveryMu.Unlock()
	if len(queued) != 1 || queued[0].deliveryID != plans[0].deliveryID || queued[0].claim == plans[0].claim {
		t.Fatalf("retry queue = %#v, want fresh claim for %q", queued, plans[0].deliveryID)
	}
	c.mu.Lock()
	pending := append([]delegatestore.PendingDelivery(nil), c.durable["dlg_target"].PendingDeliveries...)
	c.mu.Unlock()
	if len(pending) != 1 || pending[0].DeliveryID != plans[0].deliveryID {
		t.Fatalf("durable delivery after failed flush = %#v", pending)
	}
}

func TestDelegateAttention_FailedMutationQueueRetainsOtherOwners(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 3, 1)
	seedDelegateControllerIdle(t, c, "dlg_a_root", "")
	seedDelegateControllerIdle(t, c, "dlg_parent", "")
	seedDelegateControllerIdle(t, c, "dlg_z_child", "dlg_parent")
	seedDelegateControllerDelivery(t, c, "dlg_a_root")
	seedDelegateControllerDelivery(t, c, "dlg_z_child")

	rootPath := transcriptPath(c.stateDir, "root-session")
	rootWriter, err := transcript.NewWriter(rootPath, transcript.Header{SessionID: "root-session"})
	if err != nil {
		t.Fatalf("create root transcript: %v", err)
	}
	root := &Session{id: "root-session", stateDir: c.stateDir, delegateController: c}
	root.attachTranscript(rootWriter)
	c.rootRuntime = root
	ownerPath := transcriptPath(c.stateDir, "child-dlg_parent")
	ownerWriter, err := transcript.NewWriter(ownerPath, transcript.Header{SessionID: "child-dlg_parent"})
	if err != nil {
		t.Fatalf("create nested owner transcript: %v", err)
	}
	if err := ownerWriter.Close(); err != nil {
		t.Fatalf("close nested owner transcript: %v", err)
	}
	plans := c.ReplayDeliveries()
	if len(plans) != 2 || plans[0].delegateID != "dlg_a_root" || plans[1].delegateID != "dlg_z_child" {
		t.Fatalf("multi-owner replay plans = %#v", plans)
	}
	if err := rootWriter.Close(); err != nil {
		t.Fatalf("close root transcript writer: %v", err)
	}
	if err := root.executeDelegateMutationPlans(delegateMutationPlans{deliveries: plans}); err == nil {
		t.Fatal("multi-owner delivery execution unexpectedly succeeded")
	}
	root.delegateDeliveryMu.Lock()
	queued := append([]delegateDeliveryPlan(nil), root.pendingDelegateDeliveries...)
	root.delegateDeliveryMu.Unlock()
	if len(queued) != 2 || queued[0].delegateID != "dlg_a_root" || queued[1].delegateID != "dlg_z_child" {
		t.Fatalf("multi-owner retry queue = %#v, want failed head plus unprocessed owner", queued)
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
	child.delegateDeliveryMu.Lock()
	childQueued := len(child.pendingDelegateDeliveries)
	child.delegateDeliveryMu.Unlock()
	root.delegateDeliveryMu.Lock()
	rootQueued := len(root.pendingDelegateDeliveries)
	root.delegateDeliveryMu.Unlock()
	if childQueued != 0 || rootQueued != 1 {
		t.Fatalf("delivery retry ownership child=%d root=%d, want child=0 root=1", childQueued, rootQueued)
	}
	select {
	case <-wakes:
	default:
		t.Fatal("root retry drive emitted no wake")
	}
	if !root.sessionWorkPending() {
		t.Fatal("root retry drive did not expose autonomous work")
	}

	fs.failNextSync()
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("consume first root delivery wake: %v", err)
	}
	c.mu.Lock()
	remainingAfterRetryFailure := len(c.durable["dlg_target"].PendingDeliveries)
	c.mu.Unlock()
	if remainingAfterRetryFailure != 1 {
		t.Fatalf("pending deliveries after second transient failure = %d, want 1", remainingAfterRetryFailure)
	}
	select {
	case <-wakes:
		t.Fatal("second transient failure spun an immediate retry")
	default:
	}
	root.delegateDeliveryMu.Lock()
	retryActive := root.delegateDeliveryRetry.active
	retryDelay := root.delegateDeliveryRetry.delay
	root.delegateDeliveryMu.Unlock()
	if !retryActive || retryDelay != 0 {
		t.Fatalf("paced root retry state active=%v delay=%v, want active with initial delay", retryActive, retryDelay)
	}
	clock.Advance(jobNotificationRetryInitialDelay)
	select {
	case <-wakes:
	// TRIPWIRE: in-process notify channel with a fake clock already advanced
	// past the retry delay; only fires on a genuine hang.
	case <-time.After(30 * time.Second):
		t.Fatal("second transient failure did not re-arm a paced wake")
	}
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("consume paced root delivery wake: %v", err)
	}
	c.mu.Lock()
	remaining := len(c.durable["dlg_target"].PendingDeliveries)
	c.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("pending terminal deliveries after root retry = %d, want 0", remaining)
	}
}

func TestDelegateAttention_DeliveryDeferredDuringProcessingArmsWake(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	seedDelegateControllerDelivery(t, c, "dlg_target")
	path := transcriptPath(c.stateDir, "root-session")
	writer, err := transcript.NewWriter(path, transcript.Header{SessionID: "root-session"})
	if err != nil {
		t.Fatalf("create root transcript: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	root := &Session{id: "root-session", stateDir: c.stateDir, delegateController: c, state: SessionProcessing}
	root.attachTranscript(writer)
	c.rootRuntime = root
	wakes := make(chan struct{}, 1)
	root.SetNotifyFunc(func() { wakes <- struct{}{} })
	plans := c.ReplayDeliveries()
	if len(plans) != 1 {
		t.Fatalf("ReplayDeliveries = %#v", plans)
	}
	_, deferred, err := root.acceptDelegateDeliveryPlan(plans[0])
	if err != nil || !deferred {
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
	if err := root.armDelegateAttention(attentionID); err != nil {
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
			return agenttest.ToolCallResponse(communicateCall("root-attention-consumed", "completion received"))
		}),
	)
	if _, err := root.appendDelegateNotificationDurably(attentionID, content); err != nil {
		t.Fatalf("append root attention: %v", err)
	}
	if err := root.armDelegateAttention(attentionID); err != nil {
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
					midTurnArmErr = root.armDelegateAttention(secondID)
				}
				return agenttest.ToolCallResponse(llm.ToolCallData{ID: "round-one", Name: "task_list", Arguments: json.RawMessage(`{}`), Type: "function"})
			},
			func(req llm.Request) llm.Response {
				calls++
				roundTwoSawBoth = requestContainsText(req, firstContent) && requestContainsText(req, secondContent)
				return agenttest.ToolCallResponse(communicateCall("mid-turn-complete", "both deliveries handled"))
			},
		),
	)
	if _, err := root.appendDelegateNotificationDurably(firstID, firstContent); err != nil {
		t.Fatalf("append first root attention: %v", err)
	}
	if err := root.armDelegateAttention(firstID); err != nil {
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
	if root.hasPendingRootDelegateAttention() {
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
					lateArmErr = root.armDelegateAttention(lateID)
				}
				return agenttest.ToolCallResponse(communicateCall("unseen-turn-complete", "first handled"))
			},
			func(req llm.Request) llm.Response {
				calls++
				wakeSawLate = requestContainsText(req, lateContent)
				return agenttest.ToolCallResponse(communicateCall("unseen-wake-complete", "late handled"))
			},
		),
	)
	if _, err := root.appendDelegateNotificationDurably(firstID, firstContent); err != nil {
		t.Fatalf("append first root attention: %v", err)
	}
	if err := root.armDelegateAttention(firstID); err != nil {
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
	if !root.hasPendingRootDelegateAttention() {
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
					armErr = root.armDelegateAttention(attentionID)
				}
				return agenttest.ToolCallResponse(llm.ToolCallData{ID: "round-one", Name: "task_list", Arguments: json.RawMessage(`{}`), Type: "function"})
			},
			func(req llm.Request) llm.Response {
				calls++
				roundTwoSaw = requestContainsText(req, content)
				return agenttest.ToolCallResponse(communicateCall("empty-snap-complete", "steering and delivery handled"))
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
	if root.hasPendingRootDelegateAttention() {
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
					armErr = root.armDelegateAttention(attentionID)
				}
				return agenttest.ToolCallResponse(llm.ToolCallData{ID: "round-one", Name: "task_list", Arguments: json.RawMessage(`{}`), Type: "function"})
			},
			func(req llm.Request) llm.Response {
				roundTwoSaw = requestContainsText(req, content)
				return agenttest.ToolCallResponse(communicateCall("user-turn-complete", "question answered and delivery handled"))
			},
			// A third call would be the drain selector's backstop notification
			// turn — the failure mode this test exists to rule out.
			func(llm.Request) llm.Response {
				thirdRan = true
				return agenttest.ToolCallResponse(communicateCall("backstop-complete", "should not run"))
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
	if root.hasPendingRootDelegateAttention() {
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
				return agenttest.ToolCallResponse(communicateCall("user-turn-complete", "answered"))
			},
			// A second request would be the notification turn #4247 removes.
			func(llm.Request) llm.Response {
				requests++
				return agenttest.ToolCallResponse(communicateCall("notification-turn", "should not run"))
			},
		),
	)
	if _, err := root.appendDelegateNotificationDurably(attentionID, content); err != nil {
		t.Fatalf("append pre-turn attention: %v", err)
	}
	if err := root.armDelegateAttention(attentionID); err != nil {
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
	if root.hasPendingRootDelegateAttention() {
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
				return agenttest.ToolCallResponse(communicateCall("user-turn-complete", "answered"))
			},
			func(req llm.Request) llm.Response {
				requests++
				notificationSaw = requestContainsText(req, content)
				return agenttest.ToolCallResponse(communicateCall("notification-turn", "handled"))
			},
		),
	)
	// Armed in the wake set before the turn; its durable entry does not exist
	// yet, so no request of the user turn can present it.
	root.armRootDelegateAttention(attentionID)
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
	if root.hasPendingRootDelegateAttention() {
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
	if err := root.armDelegateAttention(preTurnID); err != nil {
		t.Fatalf("arm pre-turn attention: %v", err)
	}
	// Turn start.
	root.resetAttentionCoverage()
	if _, err := root.appendDelegateNotificationDurably(midTurnID, midContent); err != nil {
		t.Fatalf("append mid-turn attention: %v", err)
	}
	if err := root.armDelegateAttention(midTurnID); err != nil {
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
	if err := root.armDelegateAttention(attentionID); err != nil {
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
	if err := root.armDelegateAttention(attentionID); err != nil {
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
	if err := root.finishRootDelegateAttentionTurn(nil, nil); err != nil {
		t.Fatalf("finish with no settled coverage: %v", err)
	}
	fold, err := readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read fold after unsettled finish: %v", err)
	}
	if pending := fold.pendingIDs(); !reflect.DeepEqual(pending, []string{attentionID}) {
		t.Fatalf("unsettled coverage was consumed: pending = %#v, want the delivery kept", pending)
	}

	// The retry's round settles with the item presented: promotion credits
	// it, and finish consumes.
	root.stagePresentedAttentionCoverage(context.Background(), historyTurns)
	root.promoteStagedAttentionCoverage(llm.Request{})
	if err := root.finishRootDelegateAttentionTurn(nil, nil); err != nil {
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
// stays pending, and because the turn declined to honor the wake it was
// flagged for, finish arranges a paced retry even while the flag is set (the
// drain skips the notification rung right after a notification turn).
func TestRootDelegateAttention_EmptySnapshotCoverageFailureWarnsAndRetries(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	clock := agenttest.NewFakeClock()
	const (
		attentionID = "delegate:dlg_emptyfail/delivery/1"
		content     = `<delegate-notification delegate_id="dlg_emptyfail">coverage resolution will fail</delegate-notification>`
	)
	var root *Session
	root = newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1, clock: clock}),
		withSteps(
			func(llm.Request) llm.Response {
				if _, err := root.appendDelegateNotificationDurably(attentionID, content); err != nil {
					t.Errorf("mid-turn append: %v", err)
				}
				if err := root.armDelegateAttention(attentionID); err != nil {
					t.Errorf("mid-turn arm: %v", err)
				}
				return agenttest.ToolCallResponse(llm.ToolCallData{ID: "round-one", Name: "task_list", Arguments: json.RawMessage(`{}`), Type: "function"})
			},
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(communicateCall("empty-fail-turn", "steering handled"))
			},
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(communicateCall("empty-fail-retry", "delivery consumed by the paced retry wake"))
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
	if pending := fold.pendingIDs(); !reflect.DeepEqual(pending, []string{attentionID}) {
		t.Fatalf("pending after failed coverage resolution = %#v, want the delivery kept", pending)
	}
	if !root.sessionWorkPending() {
		t.Fatal("failed coverage resolution left no autonomous work to recover consumption")
	}
	clock.Advance(jobNotificationRetryInitialDelay)
	select {
	case <-wakes:
	// TRIPWIRE: in-process notify channel with a fake clock already advanced
	// past the retry delay; only fires on a genuine hang.
	case <-time.After(30 * time.Second):
		t.Fatal("declined wake arranged no paced retry")
	}
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("paced-retry wake turn: %v", err)
	}
	fold, err = readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read fold after paced retry: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != delegateAttentionConsumed {
		t.Fatalf("attention disposition = %q, want consumed by the paced retry wake", got)
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

// Coverage consumption is an optimization over the wake path: when its
// resolution append fails, a covering NON-notification turn still succeeds —
// the item stays pending, its armed wake fires, and the follow-up
// notification turn consumes it — instead of failing the user's turn with
// the optimization's error. The injected writer fails exactly one
// ATTENTION_RESOLUTION sync: the covering turn's own attempt.
func TestRootDelegateAttention_CoveredResolutionFailureDoesNotFailUserTurn(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	const (
		attentionID = "delegate:dlg_resfail/delivery/1"
		content     = `<delegate-notification delegate_id="dlg_resfail">resolution will fail</delegate-notification>`
	)
	var root *Session
	var wakeTurnSaw bool
	root = newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}),
		withSteps(
			func(llm.Request) llm.Response {
				if _, err := root.appendDelegateNotificationDurably(attentionID, content); err != nil {
					t.Errorf("mid-turn append: %v", err)
				}
				if err := root.armDelegateAttention(attentionID); err != nil {
					t.Errorf("mid-turn arm: %v", err)
				}
				return agenttest.ToolCallResponse(llm.ToolCallData{ID: "round-one", Name: "task_list", Arguments: json.RawMessage(`{}`), Type: "function"})
			},
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(communicateCall("resolution-failure-turn", "answer stands"))
			},
			// The armed wake's follow-up notification turn, which the failed
			// coverage resolution left the item pending for.
			func(req llm.Request) llm.Response {
				wakeTurnSaw = requestContainsText(req, content)
				return agenttest.ToolCallResponse(communicateCall("resolution-failure-wake", "delivery consumed by the wake"))
			},
		),
	)
	installResolutionSyncFailureWriter(t, root)
	if _, err := root.ProcessInput(context.Background(), "question", nil); err != nil {
		t.Fatalf("covering user turn (or its recovery wake) failed with the coverage resolution error: %v", err)
	}
	if !wakeTurnSaw {
		t.Fatal("the recovery wake turn did not present the delivery before consuming it")
	}
	fold, err := readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read root attention fold: %v", err)
	}
	if got := fold.resolutions[attentionID]; got != delegateAttentionConsumed {
		t.Fatalf("attention disposition = %q, want consumed by the recovery wake after the failed coverage resolution", got)
	}
}

// A failed turn never resolves, so on that path the union serves only as the
// emptiness gate that arms the paced-retry backstop. A non-empty begin
// snapshot already passes that gate; unioning it with the fold would read the
// fold only to discard the result. Skip it.
func TestRootDelegateAttention_FailedTurnWithSnapshotSkipsFoldRead(t *testing.T) {
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
	root.rootAttentionWakeIDs = map[string]struct{}{snapshotID: {}, coveredID: {}}
	root.attentionCoveredIDs = map[string]struct{}{coveredID: {}}
	root.attentionMu.Unlock()

	var foldReads int
	root.cfg.testOnly.delegateAttentionReadFold = func(path, sessionID string) (delegateAttentionFold, error) {
		foldReads++
		return readDelegateAttentionFold(path, sessionID)
	}
	defer func() { root.cfg.testOnly.delegateAttentionReadFold = nil }()

	if err := root.finishRootDelegateAttentionTurn([]string{snapshotID}, errors.New("turn failed")); err != nil {
		t.Fatalf("failed turn with snapshot: %v", err)
	}
	if foldReads != 0 {
		t.Fatalf("failed turn with a non-empty snapshot read the fold %d times, want 0", foldReads)
	}
	if !root.sessionWorkPending() {
		t.Fatal("failed turn left no paced retry for the still-pending attention")
	}
}

// The empty-snapshot counterpart is the backstop, and it MUST keep reading
// the fold: a settled round presented mid-turn attention before the turn
// failed, the drain skips the notification rung right after a notification
// turn, and only this gate arms the paced retry that eventually consumes the
// item.
func TestRootDelegateAttention_FailedEmptySnapshotTurnStillReadsFold(t *testing.T) {
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
	root.attentionMu.Lock()
	root.rootAttentionWakeIDs = map[string]struct{}{coveredID: {}}
	root.attentionCoveredIDs = map[string]struct{}{coveredID: {}}
	root.attentionMu.Unlock()

	var foldReads int
	root.cfg.testOnly.delegateAttentionReadFold = func(path, sessionID string) (delegateAttentionFold, error) {
		foldReads++
		return readDelegateAttentionFold(path, sessionID)
	}
	defer func() { root.cfg.testOnly.delegateAttentionReadFold = nil }()

	if err := root.finishRootDelegateAttentionTurn(nil, errors.New("turn failed")); err != nil {
		t.Fatalf("failed empty-snapshot turn: %v", err)
	}
	if foldReads != 1 {
		t.Fatalf("failed empty-snapshot turn read the fold %d times, want exactly 1 (the backstop gate)", foldReads)
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
				if err := root.armDelegateAttention(attentionID); err != nil {
					t.Errorf("mid-turn arm: %v", err)
				}
				return agenttest.ToolCallResponse(communicateCall("refire-covering", "covered mid-turn"))
			},
			// The turn driven by the arm's kick. Its begin snapshot is
			// non-empty, so the finish path resolves and consumes.
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(communicateCall("refire-consumer", "consumed by the arm kick turn"))
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

	// Now the adversarial shape itself: a failed turn, empty snapshot,
	// nothing covered, wake flag set. The finish path must leave the flag
	// set (it is the coalescing suppressor) and the liveness carrier — the
	// arm's kick — is still in flight.
	root.attentionMu.Lock()
	wakeFlagBefore := root.rootAttentionWake
	root.rootAttentionWake = true
	root.attentionMu.Unlock()
	if err := root.finishRootDelegateAttentionTurn(nil, errors.New("turn failed")); err != nil {
		t.Fatalf("failed empty turn: %v", err)
	}
	root.attentionMu.Lock()
	wakeFlagAfter := root.rootAttentionWake
	root.attentionMu.Unlock()
	if !wakeFlagAfter {
		t.Fatal("empty failed turn cleared the wake flag: the in-flight arm kick's turn would be suppressed into a redundant notification")
	}
	if wakeFlagBefore {
		t.Fatal("test setup: wake flag was already set before the adversarial injection")
	}
}

func TestRootDelegateAttention_FailedConsumptionRemainsPendingAndRearms(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	clock := agenttest.NewFakeClock()
	root := newSession(t,
		withDir(stateDir),
		withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1, clock: clock}),
		withSteps(
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(communicateCall("root-attention-first", "first attempt"))
			},
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(communicateCall("root-attention-retry", "retry complete"))
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
	if err := root.armDelegateAttention(attentionID); err != nil {
		t.Fatalf("arm root attention: %v", err)
	}
	select {
	case <-wakes:
	default:
		t.Fatal("initial root attention wake absent")
	}
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err == nil || !errors.Is(err, errInjectedRootAttentionResolutionSyncFailure) {
		t.Fatalf("first root attention turn error = %v, want injected resolution failure", err)
	}
	fold, err := readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read failed root attention fold: %v", err)
	}
	if pending := fold.pendingIDs(); !reflect.DeepEqual(pending, []string{attentionID}) {
		t.Fatalf("pending after failed consumption = %#v, want exact attention", pending)
	}
	if !root.sessionWorkPending() {
		t.Fatal("failed root attention consumption cleared autonomous work")
	}
	clock.Advance(jobNotificationRetryInitialDelay)
	select {
	case <-wakes:
	// TRIPWIRE: in-process notify channel with a fake clock already advanced
	// past the retry delay; only fires on a genuine hang.
	case <-time.After(30 * time.Second):
		t.Fatal("failed root attention consumption did not re-arm a paced wake")
	}
	if _, err := root.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatalf("retry root attention turn: %v", err)
	}
	fold, err = readDelegateAttentionFold(transcriptPath(stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatalf("read retried root attention fold: %v", err)
	}
	if pending := fold.pendingIDs(); len(pending) != 0 {
		t.Fatalf("pending after successful retry = %#v", pending)
	}
}

func TestRootDelegateAttention_RestoreRearmsPendingIDsWithoutProviderCall(t *testing.T) {
	stateDir := t.TempDir()
	rootID := identifier.MustNewSessionID()
	if err := os.MkdirAll(schema.SessionsDir(stateDir), 0o755); err != nil {
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
	fixture := newColdDelegateFixture(t, "")
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
	fixture.adapter.setSteps([]func(llm.Request) llm.Response{
		func(request llm.Request) llm.Response {
			attentionSeen = requestContainsText(request, owedContent)
			close(attentionEntered)
			<-releaseAttention
			return agenttest.CommunicateResponse(true, "recovered owed generation handled")
		},
		func(request llm.Request) llm.Response {
			if !requestContainsText(request, remainingContent) {
				t.Errorf("successor attention request omitted remaining unresolved input")
			}
			return agenttest.CommunicateResponse(true, "remaining attention handled")
		},
		func(llm.Request) llm.Response {
			return agenttest.CommunicateResponse(true, "root drained recovered completions")
		},
	})
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
		fixture := newColdDelegateFixture(t, "")
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

// newStrandedStopFixture builds a running delegate generation that is
// recovery-required with no runner, plus update deliveries whose process-local
// admissions the stop tracks. The deliveries are owned by dlg_owner, which the
// fixture leaves without a resident runtime (unreachable) or gives a stub
// runtime, so a test can choose whether replay could re-plan the receipts.
type strandedStopOptions struct {
	updates        int
	retryable      bool
	inline         bool
	ownerReachable bool
	otherPending   bool
	otherWaiter    bool
	staleReceipt   bool
	topLevel       bool
	realRoot       bool
}

type strandedStopFixture struct {
	controller  *delegateTreeController
	storePath   string
	stop        delegateStopResult
	deliveries  []delegateDeliveryToken
	notifies    *int32
	root        *Session
	otherWaiter *delegateInlineWaiter
}

func newStrandedStopFixture(t *testing.T, opts strandedStopOptions) *strandedStopFixture {
	t.Helper()
	c, path := newDelegateControllerTestHarness(t, 2, 2)
	fixture := &strandedStopFixture{controller: c, storePath: path}
	ownerID := "dlg_owner"
	parentID := ownerID
	actor := delegateActor{lease: &delegateLease{delegateID: ownerID, generation: 1}}
	if opts.topLevel {
		ownerID = ""
		parentID = ""
		actor = rootDelegateActor(c.rootSessionID)
	} else {
		seedDelegateControllerRunning(t, c, "dlg_owner", "")
	}
	seedDelegateControllerRunning(t, c, "dlg_target", parentID)
	if opts.otherPending {
		seedDelegateControllerIdle(t, c, "dlg_other", parentID)
		seedDelegateControllerDelivery(t, c, "dlg_other")
		if opts.otherWaiter {
			waiter := &delegateInlineWaiter{resolution: make(chan delegateInlineResolution, 1)}
			c.mu.Lock()
			c.live["dlg_other"] = &delegateLiveState{waiters: map[uint64]*delegateInlineWaiter{1: waiter}}
			c.mu.Unlock()
			fixture.otherWaiter = waiter
		}
	}
	owner := &Session{id: "owner-runtime", delegateController: c}
	if opts.ownerReachable {
		count := new(int32)
		fixture.notifies = count
		owner.notifyFunc = func() { atomic.AddInt32(count, 1) }
	}
	if opts.realRoot {
		writer, err := transcript.NewWriter(transcriptPath(c.stateDir, c.rootSessionID), transcript.Header{SessionID: c.rootSessionID})
		if err != nil {
			t.Fatalf("create root transcript: %v", err)
		}
		t.Cleanup(func() { _ = writer.Close() })
		root := &Session{id: c.rootSessionID, stateDir: c.stateDir, state: SessionIdle, delegateController: c}
		root.attachTranscript(writer)
		fixture.root = root
		c.mu.Lock()
		c.rootRuntime = root
		c.mu.Unlock()
	}
	c.mu.Lock()
	if !opts.topLevel && opts.ownerReachable {
		c.live["dlg_owner"].runtime = owner
	}
	c.live["dlg_target"].recoveryRequired = true
	c.live["dlg_target"].finalizationRecoveryRequired = true
	c.live["dlg_target"].recoveryRunnerPending = false
	deliveryIDs := make([]string, 0, opts.updates)
	for i := range opts.updates {
		appended, err := c.appendLocked(delegatestore.Event{
			Kind:       delegatestore.EventDelegateUpdatePosted,
			TS:         c.now(),
			DelegateID: "dlg_target",
			UpdatePosted: &delegatestore.UpdatePosted{
				Generation: 1,
				Message:    fmt.Sprintf("stranded update %d", i),
			},
		})
		if err != nil {
			c.mu.Unlock()
			t.Fatalf("append pending update %d: %v", i, err)
		}
		deliveryIDs = append(deliveryIDs, delegatestore.UpdateDeliveryID("dlg_target", appended[0].Seq))
	}
	deliveries := make([]delegateDeliveryToken, 0, len(deliveryIDs))
	for i, deliveryID := range deliveryIDs {
		if opts.staleReceipt {
			deliveryID = fmt.Sprintf("dlg_target/update/%d", 999+i)
		}
		c.nextToken++
		token := delegateDeliveryToken{processID: c.nextToken, deliveryID: deliveryID}
		c.deliveries[token.processID] = &delegateDeliveryAdmission{
			token:      token,
			delegateID: "dlg_target",
			ownerID:    ownerID,
			claim:      delegateDeliveryClaimToken{processID: token.processID, deliveryID: deliveryID},
			retryable:  opts.retryable,
			inline:     opts.inline,
		}
		deliveries = append(deliveries, token)
	}
	c.mu.Unlock()

	// A nested target's parent owns the stop. Leaving dlg_owner's runtime nil
	// keeps its deliveries unreachable while its live binding still authorizes
	// the stop; a top-level target is authorized by the root actor.
	stop, cancelPlan, _, err := c.StopSubtree(actor, "dlg_target")
	if err != nil {
		t.Fatalf("StopSubtree: %v", err)
	}
	executeDelegateCancelPlan(cancelPlan)
	c.mu.Lock()
	tracked := 0
	if c.stop != nil {
		tracked = len(c.stop.deliveries)
	}
	c.mu.Unlock()
	if tracked != len(deliveries) {
		t.Fatalf("stop tracks %d delivery admissions, want %d", tracked, len(deliveries))
	}
	fixture.stop = stop
	fixture.deliveries = deliveries
	return fixture
}

func (f *strandedStopFixture) drain(t *testing.T, timeout time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return f.controller.drainStop(ctx, f.controller.stopForResult(f.stop), f.root)
}

// drainUntilParked runs a real drain and cancels it once it is parked on stop
// progress, so a test can assert the drain is waiting without a wall-clock
// sleep.
func (f *strandedStopFixture) drainUntilParked(t *testing.T) error {
	t.Helper()
	parked := make(chan struct{})
	var once sync.Once
	hook := func() { once.Do(func() { close(parked) }) }
	savedWait := observeDelegateStopWait.Load()
	observeDelegateStopWait.Store(&hook)
	defer observeDelegateStopWait.Store(savedWait)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.controller.drainStop(ctx, f.controller.stopForResult(f.stop), f.root) }()
	select {
	case <-parked:
	case err := <-done:
		t.Fatalf("drain returned before parking: %v", err)
	case <-time.After(2 * time.Second): // TRIPWIRE: the drain signals parked synchronously before blocking; 2s only fires on a genuine hang.
		t.Fatal("drain did not park")
	}
	cancel()
	select {
	case err := <-done:
		return err
	case <-time.After(2 * time.Second): // TRIPWIRE: cancel wakes the parked drain; 2s only fires on a genuine hang.
		t.Fatal("drain did not return after cancel")
		return nil
	}
}

func (f *strandedStopFixture) acknowledged(t *testing.T) map[string]bool {
	t.Helper()
	events, err := delegatestore.ReadEvents(f.storePath)
	if err != nil {
		t.Fatalf("read delegate events: %v", err)
	}
	acknowledged := make(map[string]bool)
	for _, event := range events {
		if event.Kind == delegatestore.EventDelegateDeliveryAcknowledged && event.DeliveryAcknowledged != nil {
			acknowledged[event.DeliveryAcknowledged.DeliveryID] = true
		}
	}
	return acknowledged
}

func pendingDelivery(aggregate *delegatestore.Aggregate, deliveryID string) bool {
	for _, pending := range aggregate.PendingDeliveries {
		if pending.DeliveryID == deliveryID {
			return true
		}
	}
	return false
}

// delegateEventsAcknowledgeDelivery reports whether events include the durable
// acknowledgment for deliveryID.
func delegateEventsAcknowledgeDelivery(events []delegatestore.Event, deliveryID string) bool {
	return slices.ContainsFunc(events, func(event delegatestore.Event) bool {
		return event.Kind == delegatestore.EventDelegateDeliveryAcknowledged &&
			event.DeliveryAcknowledged != nil &&
			event.DeliveryAcknowledged.DeliveryID == deliveryID
	})
}

// requireNoRetryArmed fails when the stop armed a delivery-ack retry.
func (f *strandedStopFixture) requireNoRetryArmed(t *testing.T) {
	t.Helper()
	f.controller.mu.Lock()
	stop := f.controller.stop
	f.controller.mu.Unlock()
	if stop == nil {
		t.Fatal("stop state was cleared before the permanent acknowledgment failure")
	}
	if stop.retryArmed {
		t.Fatal("permanent acknowledgment failure armed a retry")
	}
}

func drainSessionWarnings(s *Session) []events.WarningData {
	var warnings []events.WarningData
	for {
		select {
		case event := <-s.events:
			if warning, ok := event.Data.(events.WarningData); ok && event.Kind == events.EventWarning {
				warnings = append(warnings, warning)
			}
		default:
			return warnings
		}
	}
}

// A prelaunch generation with the recovery latch set has no runner to wait for:
// the stop's recovery reconcile finishes it directly.
func TestDelegateAttention_PrelaunchRecoveryDoesNotWaitForUnlaunchedRunner(t *testing.T) {
	f := newStrandedStopFixture(t, strandedStopOptions{})
	if err := f.drain(t, 2*time.Second); err != nil {
		t.Fatalf("prelaunch recovery waited for an unlaunched runner: %v", err)
	}
	if !channelClosed(f.stop.done) {
		t.Fatal("prelaunch recovery stop did not complete")
	}
	f.controller.mu.Lock()
	aggregate := f.controller.durable["dlg_target"]
	live := f.controller.live["dlg_target"]
	f.controller.mu.Unlock()
	if aggregate == nil || aggregate.Phase != delegatestore.PhaseIdle || aggregate.CurrentRunOpen {
		t.Fatalf("prelaunch recovery aggregate = %#v, want the unlaunched generation closed", aggregate)
	}
	if live == nil || live.binding != nil || live.recoveryRequired || live.finalizationRecoveryRequired {
		t.Fatalf("prelaunch recovery live = %#v, want the generation released", live)
	}
}

// A retryable admission whose owner has no usable runtime cannot be re-planned
// by replay, so the stop's own acknowledgment retry is the only resolver. The
// test asserts the durable acknowledgment event, not merely that the pending
// delivery vanished.
func TestDelegateAttention_PrelaunchStopRetriesStrandedDeliveryAck(t *testing.T) {
	f := newStrandedStopFixture(t, strandedStopOptions{updates: 1, retryable: true})
	if plans := f.controller.ReplayDeliveries(); len(plans) != 0 {
		t.Fatalf("owner replay planned %d delivery parcel(s) for an unreachable owner; the stop retry must be the only resolver", len(plans))
	}
	if err := f.drain(t, 2*time.Second); err != nil {
		t.Fatalf("prelaunch stop waited for the stranded delivery acknowledgment: %v", err)
	}
	if !channelClosed(f.stop.done) {
		t.Fatal("prelaunch stop did not complete")
	}
	c := f.controller
	c.mu.Lock()
	aggregate := c.durable["dlg_target"]
	receipts := len(c.deliveries)
	c.mu.Unlock()
	if aggregate == nil || aggregate.Phase != delegatestore.PhaseIdle {
		t.Fatalf("stopped aggregate = %#v, want the generation closed", aggregate)
	}
	if pendingDelivery(aggregate, f.deliveries[0].deliveryID) {
		t.Fatalf("stopped aggregate still queues the update delivery %q: %#v", f.deliveries[0].deliveryID, aggregate.PendingDeliveries)
	}
	if receipts != 0 {
		t.Fatalf("delivery admissions after the stop = %d, want 0", receipts)
	}
	if !f.acknowledged(t)[f.deliveries[0].deliveryID] {
		t.Fatalf("delivery %q has no durable acknowledgment event", f.deliveries[0].deliveryID)
	}
}

// A non-retryable admission is still in flight: the stop must keep waiting for
// its executor rather than acknowledging it itself.
func TestDelegateAttention_PrelaunchStopWaitsForInFlightDelivery(t *testing.T) {
	f := newStrandedStopFixture(t, strandedStopOptions{updates: 1})
	if err := f.drainUntilParked(t); !errors.Is(err, context.Canceled) {
		t.Fatalf("stop with an in-flight delivery = %v, want the drain to keep waiting", err)
	}
	if channelClosed(f.stop.done) {
		t.Fatal("stop completed with an in-flight delivery")
	}
	c := f.controller
	c.mu.Lock()
	receipt := c.deliveries[1]
	aggregate := c.durable["dlg_target"]
	c.mu.Unlock()
	if receipt == nil || receipt.retryable {
		t.Fatalf("in-flight receipt = %#v, want the non-retryable admission preserved", receipt)
	}
	if aggregate == nil || aggregate.CurrentRunOpen {
		t.Fatalf("aggregate = %#v, want the recovery generation settled while the stop waits", aggregate)
	}
	if f.acknowledged(t)[f.deliveries[0].deliveryID] {
		t.Fatal("an in-flight delivery was acknowledged without its executor")
	}
}

// A retryable inline admission belongs to the tool reply carrying its commit;
// the stop must leave it to that reply.
func TestDelegateAttention_PrelaunchStopLeavesInlineDeliveryToReply(t *testing.T) {
	f := newStrandedStopFixture(t, strandedStopOptions{updates: 1, retryable: true, inline: true})
	if err := f.drainUntilParked(t); !errors.Is(err, context.Canceled) {
		t.Fatalf("stop with an inline delivery = %v, want the drain to keep waiting for the tool reply", err)
	}
	if channelClosed(f.stop.done) {
		t.Fatal("stop completed and acknowledged an inline delivery on its own")
	}
	c := f.controller
	c.mu.Lock()
	receipt := c.deliveries[1]
	c.mu.Unlock()
	if receipt == nil || !receipt.retryable || !receipt.inline {
		t.Fatalf("inline receipt = %#v, want it left retryable for its tool reply", receipt)
	}
	if f.acknowledged(t)[f.deliveries[0].deliveryID] {
		t.Fatal("the stop acknowledged an inline delivery")
	}
}

// The retried acknowledgment must wake the owner exactly as a normal delivery
// commit does.
func TestDelegateAttention_PrelaunchStopWakesRetriedDeliveryOwner(t *testing.T) {
	f := newStrandedStopFixture(t, strandedStopOptions{updates: 1, retryable: true, ownerReachable: true})
	if err := f.drain(t, 2*time.Second); err != nil {
		t.Fatalf("prelaunch stop waited for the stranded delivery acknowledgment: %v", err)
	}
	if got := atomic.LoadInt32(f.notifies); got != 1 {
		t.Fatalf("owner wake count = %d, want 1 after the retried acknowledgment", got)
	}
	attentionID := delegateAttentionID(f.deliveries[0].deliveryID)
	f.controller.mu.Lock()
	state := f.controller.attention["dlg_owner"]
	_, noted := state.wakeIDs[attentionID]
	f.controller.mu.Unlock()
	if !noted {
		t.Fatalf("owner attention %q was not recorded for the retried acknowledgment", attentionID)
	}
}

// A tracked receipt whose pending head moved on is dropped, not retried
// forever, and it must not fabricate an acknowledgment.
func TestDelegateAttention_PrelaunchStopDropsStaleDeliveryReceipt(t *testing.T) {
	f := newStrandedStopFixture(t, strandedStopOptions{updates: 1, retryable: true, staleReceipt: true})
	if err := f.drain(t, 2*time.Second); err != nil {
		t.Fatalf("prelaunch stop waited for a stale delivery receipt: %v", err)
	}
	if !channelClosed(f.stop.done) {
		t.Fatal("prelaunch stop did not complete with a stale receipt")
	}
	c := f.controller
	c.mu.Lock()
	receipts := len(c.deliveries)
	c.mu.Unlock()
	if receipts != 0 {
		t.Fatalf("delivery admissions after the stale receipt = %d, want 0", receipts)
	}
	if f.acknowledged(t)[f.deliveries[0].deliveryID] {
		t.Fatalf("stale receipt %q produced an acknowledgment", f.deliveries[0].deliveryID)
	}
}

// A transient acknowledgment append failure must park the stop drain and retry
// once the append succeeds, without orphaning the earlier retry's claim or the
// inline waiter that claim took from the replay plan.
func TestDelegateAttention_PrelaunchStopRetriesAfterTransientAckFailure(t *testing.T) {
	f := newStrandedStopFixture(t, strandedStopOptions{
		updates: 2, retryable: true, topLevel: true, realRoot: true, otherPending: true, otherWaiter: true,
	})
	c := f.controller
	var failed atomic.Bool
	fault := func(events []delegatestore.Event) error {
		if !delegateEventsAcknowledgeDelivery(events, f.deliveries[1].deliveryID) {
			return nil
		}
		failed.Store(true)
		return errors.New("test: transient acknowledgment append failure")
	}
	c.appendFault = fault
	parked := make(chan struct{})
	var parkOnce sync.Once
	waitHook := func() {
		if failed.Load() {
			parkOnce.Do(func() { close(parked) })
		}
	}
	savedWait := observeDelegateStopWait.Load()
	observeDelegateStopWait.Store(&waitHook)
	defer observeDelegateStopWait.Store(savedWait)
	// TRIPWIRE: the drain normally parks and retries within ~100ms; 5s only
	// fires on a genuine hang, not scheduler contention under a loaded suite.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.drainStop(ctx, c.stopForResult(f.stop), f.root) }()
	select {
	case <-parked:
	case err := <-done:
		t.Fatalf("drain exited on the transient acknowledgment failure: %v", err)
	case <-time.After(2 * time.Second): // TRIPWIRE: the failed attempt signals the wait it then parks on; 2s only fires on a genuine hang.
		t.Fatal("drain did not park after the transient acknowledgment failure")
	}
	c.mu.Lock()
	claims := len(c.deliveryClaims)
	first := c.deliveries[f.deliveries[0].processID]
	second := c.deliveries[f.deliveries[1].processID]
	c.mu.Unlock()
	if claims != 0 {
		t.Fatalf("transient acknowledgment failure orphaned %d delivery claim(s)", claims)
	}
	if first != nil {
		t.Fatal("the first acknowledgment did not land")
	}
	if second == nil || !second.retryable {
		t.Fatalf("second receipt = %#v, want it left retryable", second)
	}
	if f.otherWaiter == nil {
		t.Fatal("fixture did not register the replay waiter")
	}
	var resolution delegateInlineResolution
	// TRIPWIRE: dropping the failed batch's claims resolves its waiter synchronously; 2s only fires on a genuine hang.
	awaitWithin(t, 2*time.Second, "dropped replay waiter resolution", func() {
		resolution = <-f.otherWaiter.resolution
	})
	if !resolution.fallback {
		t.Fatalf("dropped replay waiter resolution = %#v, want the fallback", resolution)
	}
	c.mu.Lock()
	c.appendFault = nil
	c.mu.Unlock()
	var drainErr error
	// TRIPWIRE: the armed retry fires after 50ms; 5s only fires on a genuine hang.
	awaitWithin(t, 5*time.Second, "prelaunch stop finishing after the append recovered", func() {
		drainErr = <-done
	})
	if drainErr != nil {
		t.Fatalf("prelaunch stop did not finish after the append recovered: %v", drainErr)
	}
	if !channelClosed(f.stop.done) {
		t.Fatal("prelaunch stop did not complete after the retry")
	}
	acknowledged := f.acknowledged(t)
	for _, token := range f.deliveries {
		if !acknowledged[token.deliveryID] {
			t.Fatalf("delivery %q has no durable acknowledgment after the retry", token.deliveryID)
		}
	}
	if !acknowledged[delegateDeliveryID("dlg_other", 1)] {
		t.Fatalf("replayed delivery %q has no durable acknowledgment", delegateDeliveryID("dlg_other", 1))
	}
}

// A closed store makes the stop's acknowledgment retry permanent: production
// never swaps c.store, so the drain must return the error rather than poll the
// same failure until its context expires.
func TestDelegateAttention_PrelaunchStopReturnsClosedStoreAckError(t *testing.T) {
	f := newStrandedStopFixture(t, strandedStopOptions{updates: 2, retryable: true, topLevel: true, realRoot: true})
	c := f.controller
	var closeOnce sync.Once
	c.appendFault = func(events []delegatestore.Event) error {
		if delegateEventsAcknowledgeDelivery(events, f.deliveries[1].deliveryID) {
			closeOnce.Do(func() {
				if err := c.store.Close(); err != nil {
					t.Errorf("close delegate store before the second acknowledgment: %v", err)
				}
			})
		}
		return nil
	}
	err := f.drain(t, 2*time.Second)
	if !errors.Is(err, delegatestore.ErrStoreClosed) {
		t.Fatalf("stop drain error = %v, want the permanent closed-store error", err)
	}
	if channelClosed(f.stop.done) {
		t.Fatal("stop completed despite the permanent acknowledgment failure")
	}
	f.requireNoRetryArmed(t)
	c.mu.Lock()
	second := c.deliveries[f.deliveries[1].processID]
	c.mu.Unlock()
	if second == nil || !second.retryable {
		t.Fatalf("second receipt = %#v, want it left retryable", second)
	}
}

// A store latched unusable by a failed rollback reports ErrStoreUnusable just
// as permanently as a closed one: production never swaps c.store, so the drain
// must return the error instead of re-arming its retry. The fault injects the
// wrapped sentinel a latched store returns; the store package pins the latch.
func TestDelegateAttention_PrelaunchStopReturnsUnusableStoreAckError(t *testing.T) {
	f := newStrandedStopFixture(t, strandedStopOptions{updates: 1, retryable: true, topLevel: true, realRoot: true})
	c := f.controller
	unusable := fmt.Errorf("test: store latched unusable: %w", delegatestore.ErrStoreUnusable)
	c.appendFault = func(events []delegatestore.Event) error {
		if delegateEventsAcknowledgeDelivery(events, f.deliveries[0].deliveryID) {
			return unusable
		}
		return nil
	}
	err := f.drain(t, 2*time.Second)
	if !errors.Is(err, delegatestore.ErrStoreUnusable) {
		t.Fatalf("stop drain error = %v, want the permanent unusable-store error", err)
	}
	f.requireNoRetryArmed(t)
}

// Every failed acknowledgment attempt gives back the progress token it queued
// before parking, even when an earlier failure already armed the retry timer.
// A token left queued would spin the drain through a full reconcile until the
// armed timer fired.
func TestDelegateAttention_RearmDrainsProgressWhenRetryAlreadyArmed(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	stop := &delegateStopState{progress: make(chan struct{}, 1), retryArmed: true}
	stop.progress <- struct{}{}
	c.rearmStopDeliveryRetryLocked(stop)
	select {
	case <-stop.progress:
		t.Fatal("rearm left the failed attempt's progress token queued")
	default:
	}
}

// A wake failure is best effort: it must not drop the reconcile batch's plans,
// so the stop still completes and the replayed delivery is acknowledged. Only
// the batch's own post-ack wake fails here; the replayed delivery's wake must
// reach the root normally.
func TestDelegateAttention_PrelaunchStopSurvivesDeliveryWakeFailure(t *testing.T) {
	f := newStrandedStopFixture(t, strandedStopOptions{
		updates: 1, retryable: true, topLevel: true, realRoot: true, otherPending: true,
	})
	if f.root == nil {
		t.Fatal("fixture did not create the root runtime")
	}
	f.root.events = make(chan events.SessionEvent, 4)
	var armCalls atomic.Int32
	updateSessionTestConfig(f.root, func(cfg *testConfig) {
		cfg.delegateAttentionArmFault = func() error {
			if armCalls.Add(1) == 1 {
				return errors.New("test: root arm refused")
			}
			return nil
		}
	})
	if err := f.drain(t, 3*time.Second); err != nil {
		t.Fatalf("prelaunch stop did not survive the delivery wake failure: %v", err)
	}
	if !channelClosed(f.stop.done) {
		t.Fatal("prelaunch stop did not complete after the wake failure")
	}
	acknowledged := f.acknowledged(t)
	if !acknowledged[f.deliveries[0].deliveryID] {
		t.Fatalf("delivery %q has no durable acknowledgment", f.deliveries[0].deliveryID)
	}
	if !acknowledged[delegateDeliveryID("dlg_other", 1)] {
		t.Fatalf("replayed delivery %q has no durable acknowledgment", delegateDeliveryID("dlg_other", 1))
	}
	if len(drainSessionWarnings(f.root)) == 0 {
		t.Fatal("wake failure produced no warning on the root")
	}
}

// A real root executes the retry's plans: the replayed delivery is written and
// acknowledged, not merely planned.
func TestDelegateAttention_PrelaunchStopExecutesRetryPlans(t *testing.T) {
	f := newStrandedStopFixture(t, strandedStopOptions{
		updates: 1, retryable: true, topLevel: true, realRoot: true, otherPending: true,
	})
	if err := f.drain(t, 3*time.Second); err != nil {
		t.Fatalf("prelaunch stop waited for the stranded delivery acknowledgment: %v", err)
	}
	if !channelClosed(f.stop.done) {
		t.Fatal("prelaunch stop did not complete")
	}
	acknowledged := f.acknowledged(t)
	if !acknowledged[f.deliveries[0].deliveryID] {
		t.Fatalf("delivery %q has no durable acknowledgment", f.deliveries[0].deliveryID)
	}
	if !acknowledged[delegateDeliveryID("dlg_other", 1)] {
		t.Fatalf("replayed delivery %q has no durable acknowledgment", delegateDeliveryID("dlg_other", 1))
	}
	f.controller.mu.Lock()
	pending := len(f.controller.durable["dlg_other"].PendingDeliveries)
	f.controller.mu.Unlock()
	if pending != 0 {
		t.Fatalf("replayed delivery still pending after the stop: %d", pending)
	}
}

// The shared wake helper covers the root arm, the cold notifier, and the
// typed-nil root runtime.
func TestDelegateAttention_WakeDeliveryReceiverBranches(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	var notified atomic.Int32
	root := &Session{id: c.rootSessionID, stateDir: c.stateDir, delegateController: c}
	root.notifyFunc = func() { notified.Add(1) }
	c.mu.Lock()
	c.rootRuntime = root
	c.mu.Unlock()
	if err := c.wakeDeliveryReceiver(root, "delegate:missing"); err != nil {
		t.Fatalf("root receiver wake: %v", err)
	}
	if err := c.wakeDeliveryReceiver(coldDelegateDeliveryReceiver{}, "delegate:missing"); err != nil {
		t.Fatalf("cold receiver wake: %v", err)
	}
	if got := notified.Load(); got != 1 {
		t.Fatalf("root wake count = %d, want 1 from the cold receiver", got)
	}
	var typedNil *Session
	if err := c.wakeDeliveryReceiver(typedNil, "delegate:missing"); err != nil {
		t.Fatalf("typed-nil root wake: %v", err)
	}
}

// A wake failure is best effort: it is logged, and it must not be returned as
// the reconciliation's error because that would drop the ack's plans.
func TestDelegateAttention_ReportsDeliveryWakeErrors(t *testing.T) {
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	root := &Session{id: c.rootSessionID, stateDir: c.stateDir, delegateController: c, events: make(chan events.SessionEvent, 1)}
	c.rootRuntime = root
	c.reportDelegateWakeError(errors.New("wake failed"))
	event := <-root.events
	warning, ok := event.Data.(events.WarningData)
	if event.Kind != events.EventWarning || !ok || warning.Message == "" {
		t.Fatalf("wake failure event = %#v, want a warning", event)
	}
}

// A failed stop-tracked acknowledgment must wake a parked drain to retry it.
func TestDelegateAttention_FailedStopDeliveryAckSignalsStopProgress(t *testing.T) {
	f := newStrandedStopFixture(t, strandedStopOptions{updates: 1, retryable: true})
	c := f.controller
	c.mu.Lock()
	select {
	case <-c.stop.progress:
	default:
	}
	if err := c.store.Close(); err != nil {
		c.mu.Unlock()
		t.Fatalf("close delegate store: %v", err)
	}
	if _, err := c.completeDeliveryLocked(f.deliveries[0], true); err == nil {
		c.mu.Unlock()
		t.Fatal("acknowledgment after the store close succeeded")
	}
	select {
	case <-c.stop.progress:
	default:
		c.mu.Unlock()
		t.Fatal("failed stop delivery acknowledgment did not signal stop progress")
	}
	c.mu.Unlock()
}

// replaceChildTranscriptWithFS swaps a restored child's transcript writer for
// one on fs, so a send test can inject a sync fault in the start-input path.
func replaceChildTranscriptWithFS(t *testing.T, fs afero.Fs) func(*Session) {
	t.Helper()
	return func(child *Session) {
		t.Helper()
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
	}
}

// A send that fails before launchSubagentRun owns no runner, so the failed send
// itself must not reach the provider. Releasing its committed start re-drives
// the child, and that re-drive can launch a notification or attention turn
// which calls the provider on its own goroutine. The total request count after
// the send therefore does not witness the failed send: sample the count at the
// rollback boundary, after every failed-send step and before the rollback
// releases the claim and re-drives the child, and require zero there. The
// re-drive's own notification turn reaches the provider after that sample, and
// the failed send's input never appears in a request, so the test also proves
// it distinguishes the two.
//
// Regression for the provider-count arm of #4844.
func TestDelegateAttention_PrelaunchFailedSendDoesNotReachProvider(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	fs := newAttentionFailNextSyncFS()
	var atRollback int
	var redrivenChild string
	root.cfg.testOnly.delegateSendRollbackBeforeRedrive = func(childSessionID string) {
		atRollback = len(fixture.adapter.Requests())
		redrivenChild = childSessionID
	}
	replaceTranscript := replaceChildTranscriptWithFS(t, fs)
	root.delegateRestoreBeforeSideEffects = func(child *Session) {
		replaceTranscript(child)
		child.cfg.testOnly.delegateInitialInputAppend = func(*Session) {
			// Fail the input write's own fsync, before launchSubagentRun. The
			// failure batch that closes the generation appends to the store,
			// which stays open.
			fs.failNextSync()
		}
	}

	const input = "start successor"
	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, input, 0)
	if outcome.result.Err == nil || !errors.Is(outcome.result.Err, errInjectedAttentionFailNextSync) {
		t.Fatalf("prelaunch send = %#v, want the injected input-write failure", outcome.result)
	}
	if redrivenChild != fixture.childID {
		t.Fatalf("failed send re-drove child %q, want %q", redrivenChild, fixture.childID)
	}
	if atRollback != 0 {
		t.Fatalf("prelaunch failed send reached provider %d times before its re-drive", atRollback)
	}
	root.sendersWG.Wait()
	requests := fixture.adapter.Requests()
	if len(requests) == 0 {
		t.Fatal("re-driven notification turn reached provider 0 times; the test cannot distinguish the failed send from the notification turn")
	}
	for _, request := range requests {
		for _, message := range request.Messages {
			if strings.Contains(message.Text(), input) {
				t.Fatalf("failed send's input reached the provider: %q", message.Text())
			}
		}
	}
}

// Once the start is committed and its input durable, an ack-only delivery
// failure is requeued and warned about instead of failing the start or latching
// finalization recovery. The stop repair stays the backstop for durable states.
func TestDelegateAttention_StartDeliveryReplayFailureDoesNotFailCommittedStart(t *testing.T) {
	fixture := newColdDelegateFixture(t, "")
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	c := root.delegateController
	seedDelegateControllerIdle(t, c, "dlg_nested_source", fixture.delegateID)
	seedDelegateControllerDelivery(t, c, "dlg_nested_source")
	fs := newAttentionFailNextSyncFS()
	replaceTranscript := replaceChildTranscriptWithFS(t, fs)
	root.delegateRestoreBeforeSideEffects = func(child *Session) {
		replaceTranscript(child)
		child.cfg.testOnly.delegateInitialInputAppend = func(*Session) {
			// The input write and the notification write land; closing the store
			// on the notification's sync leaves only the acknowledgment append to
			// fail.
			fs.succeedSyncAfter(1, func() {
				if err := c.store.Close(); err != nil {
					t.Errorf("close delegate store at delivery acknowledgment: %v", err)
				}
			})
		}
	}

	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "start successor", 0)
	if outcome.result.Err != nil || outcome.result.Action != "started" {
		t.Fatalf("committed start with an ack-only delivery failure = %#v, want the start to continue", outcome.result)
	}
	warnings := drainSessionWarnings(root)
	foundWarning := false
	for _, warning := range warnings {
		if warning.Code == events.WarningCodeDelegateStartDeliveryReplay {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Fatalf("committed start warnings = %#v, want code %q", warnings, events.WarningCodeDelegateStartDeliveryReplay)
	}
	reopened, err := delegatestore.Open(delegateResourceStorePath(fixture.stateDir, fixture.meta.ID))
	if err != nil {
		t.Fatalf("reopen delegate store: %v", err)
	}
	c.mu.Lock()
	c.store = reopened
	c.mu.Unlock()
	stop, cancelPlan, _, err := c.StopSubtree(rootDelegateActor(root.id), fixture.delegateID)
	if err != nil {
		t.Fatalf("StopSubtree after the committed start: %v", err)
	}
	executeDelegateCancelPlan(cancelPlan)
	// TRIPWIRE: the stopped launch normally settles in milliseconds; 10s only
	// fires on a genuine hang, not scheduler contention under a loaded suite.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.drainStop(ctx, c.stopForResult(stop), root); err != nil {
		t.Fatalf("stop after the committed start: %v", err)
	}
	if !channelClosed(stop.done) {
		t.Fatal("stop after the committed start did not complete")
	}
	stored, err := delegatestore.ReadEvents(delegateResourceStorePath(fixture.stateDir, fixture.meta.ID))
	if err != nil {
		t.Fatalf("read delegate events: %v", err)
	}
	wantDelivery := delegateDeliveryID("dlg_nested_source", 1)
	acknowledged := false
	for _, event := range stored {
		if event.Kind == delegatestore.EventDelegateDeliveryAcknowledged && event.DeliveryAcknowledged != nil && event.DeliveryAcknowledged.DeliveryID == wantDelivery {
			acknowledged = true
		}
	}
	if !acknowledged {
		t.Fatalf("delivery %q has no durable acknowledgment after the stop", wantDelivery)
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

func (fs *attentionSyncBarrierFS) rearm() {
	fs.mu.Lock()
	fs.armed = true
	fs.syncEntered = make(chan struct{})
	fs.allowSync = make(chan struct{})
	fs.enterOnce = sync.Once{}
	fs.releaseOnce = sync.Once{}
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
	succeed   bool
	onFail    func()
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

// errInjectedRootAttentionResolutionSyncFailure is the injected sync failure
// the resolution-retry tests match with errors.Is. The production path wraps
// it, so the test never matches its text.
var errInjectedRootAttentionResolutionSyncFailure = errors.New("injected root attention resolution sync failure")

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
		return errInjectedRootAttentionResolutionSyncFailure
	}
	return file.File.Sync()
}

func newAttentionFailNextSyncFS() *attentionFailNextSyncFS {
	return &attentionFailNextSyncFS{Fs: afero.NewOsFs()}
}

// errInjectedAttentionFailNextSync is the sync failure attentionFailNextSyncFS
// injects. The production path wraps it, so tests match it with errors.Is.
var errInjectedAttentionFailNextSync = errors.New("injected attention resolution sync failure")

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
	fs.succeed = false
	fs.onFail = onFail
	fs.mu.Unlock()
}

// succeedSyncAfter lets successfulSyncs syncs through, then runs onSync on the
// next one and still reports it durable: the receiver-side write commits and a
// later append is the operation that fails.
func (fs *attentionFailNextSyncFS) succeedSyncAfter(successfulSyncs int, onSync func()) {
	fs.mu.Lock()
	fs.failNext = true
	fs.failAfter = successfulSyncs
	fs.succeed = true
	fs.onFail = onSync
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
	succeed := file.fs.succeed
	file.fs.failNext = false
	onFail := file.fs.onFail
	file.fs.onFail = nil
	file.fs.mu.Unlock()
	if fail {
		if onFail != nil {
			onFail()
		}
		if succeed {
			return file.File.Sync()
		}
		return errInjectedAttentionFailNextSync
	}
	return file.File.Sync()
}

func TestDelegateAttention_ReachableColdOwnerRetainsAttentionAfterRestoreFailure(t *testing.T) {
	c, journalPath := newDelegateControllerTestHarness(t, 2, 1)
	seedStableAttentionRepairDelegate(t, c, "dlg_owner", "", true)
	const attentionID = "delegate:reachable-owner"
	ownerPath := transcriptPath(c.stateDir, "child-dlg_owner")
	writeDelegateAttentionTranscript(t, ownerPath, "child-dlg_owner", attentionID)
	writeEmptyAttentionTranscript(t, transcriptPath(c.stateDir, "root-session"), "root-session")
	copyDelegateJournalForBootstrap(t, c, journalPath)

	metaPath := filepath.Join(c.stateDir, schema.SessionsSubdir, "child-dlg_owner.meta.json")
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

func TestDelegateAttention_UnreachableOwnerTransfersToNearestReachableAncestor(t *testing.T) {
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

func TestDelegateAttention_AncestorFsyncPrecedesChildDiscard(t *testing.T) {
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

func TestDelegateAttention_ConsumedEntryIsNeverEscalated(t *testing.T) {
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

func TestDelegateAttention_StartupRepairUsesNoProvider(t *testing.T) {
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

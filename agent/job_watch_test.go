package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// watchCustodyProof keeps the destination's real transcript unavailable until
// the test has inspected source production. Its fake source clock prevents a
// paced retry from racing the controlled filesystem recovery.
type watchCustodyProof struct {
	root      *Session
	jm        *jobManager
	adapter   *messagingAdapter
	fs        *watchCustodyFaultFS
	observed  int
	epoch     int
	faultBase int
}

type watchCustodyFaultFS struct {
	*notificationFaultFS
	mu       sync.Mutex
	failures []error
}

func (fs *watchCustodyFaultFS) OpenFile(name string, flag int, mode os.FileMode) (afero.File, error) {
	file, err := fs.Fs.OpenFile(name, flag, mode)
	if err != nil {
		return nil, err
	}
	return &watchCustodyFaultFile{File: &notificationFaultFile{File: file, fs: fs.notificationFaultFS}, fs: fs}, nil
}

type watchCustodyFaultFile struct {
	afero.File
	fs *watchCustodyFaultFS
}

func (f *watchCustodyFaultFile) Sync() error {
	err := f.File.Sync()
	if err != nil {
		f.fs.mu.Lock()
		f.fs.failures = append(f.fs.failures, err)
		f.fs.mu.Unlock()
	}
	return err
}

func (p *watchCustodyProof) hold(cause error) {
	p.fs.mu.Lock()
	p.faultBase = len(p.fs.failures)
	p.fs.mu.Unlock()
	p.epoch++
	p.fs.syncErr = cause
}

func (p *watchCustodyProof) requireSyncFailure(t *testing.T, seq uint64) {
	t.Helper()
	p.fs.mu.Lock()
	defer p.fs.mu.Unlock()
	failures := p.fs.failures[p.faultBase:]
	if p.fs.syncErr == nil || len(failures) == 0 {
		t.Fatalf("destination Sync failures epoch=%d seq=%d = %d, want actual injected failure before recovery", p.epoch, seq, len(failures))
	}
	for _, err := range failures {
		if !errors.Is(err, p.fs.syncErr) {
			t.Fatalf("destination Sync returned %T %v, want injected cause %T %v", err, err, p.fs.syncErr, p.fs.syncErr)
		}
	}
	t.Logf("destination_sync_fault epoch=%d seq=%d failures=%d cause_type=%T cause_is_injected=true", p.epoch, seq, len(failures), p.fs.syncErr)
}

func newWatchCustodyProof(t *testing.T) *watchCustodyProof {
	t.Helper()
	adapter := newMessagingAdapter()
	root := newMessagingRoot(t, adapter, nil)
	if _, err := root.ProcessInput(context.Background(), "custody-init-71b2", nil); err != nil {
		t.Fatal(err)
	}
	if err := root.closeAttachedTranscript(); err != nil {
		t.Fatal(err)
	}
	fs := &watchCustodyFaultFS{notificationFaultFS: &notificationFaultFS{Fs: afero.NewOsFs()}}
	writer, _, err := transcript.OpenWriterForSessionWithFS(fs, transcriptPath(root.stateDir, root.ID()), root.ID())
	if err != nil {
		t.Fatal(err)
	}
	root.attachTranscript(writer)
	c := root.delegateController
	created := delegateControllerCreatedEvent("dlg_source", "")
	created.Created.Descriptor.ChildSessionID = testChildSessionID
	created.Created.Descriptor.TranscriptRef = "local:" + testChildSessionID
	created.Created.Descriptor.OwnerSessionID = root.ID()
	c.mu.Lock()
	_, err = c.appendLocked(created, delegateControllerRunStartedEvent("dlg_source", 1, delegatestore.TriggerInitial, c.now()))
	c.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	source, err := NewSession(root.client, root.profile, execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		StateDir: root.stateDir,
		clock:    agenttest.NewFakeClock(),
		spawn: spawnConfig{
			sessionID: testChildSessionID, delegateController: c,
			delegateRootSessionID: root.ID(), owningDelegateID: "dlg_source",
			parentSessionID: root.ID(), depth: 1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// This source has no run-loop goroutine. Settle its seeded lease through
		// the controller before root.Close tries to cancel and join that loop.
		fs.syncErr = nil
		lease := delegateLease{delegateID: "dlg_source", generation: 1}
		plans, err := c.FinishGeneration(lease, delegateFinish{outcome: delegatestore.OutcomeCancelled, reason: "test_cleanup"})
		if err != nil {
			t.Error(err)
		} else if err := root.executeDelegateMutationPlans(plans); err != nil {
			t.Error(err)
		}
		if err := c.ReportFinalizationQuiesced(lease, source); err != nil {
			t.Error(err)
		}
		source.Close()
	})
	c.mu.Lock()
	c.live["dlg_source"] = &delegateLiveState{runtime: source, binding: &delegateRuntimeBinding{
		runtime: source, lease: delegateLease{delegateID: "dlg_source", generation: 1}, cancel: func() {}, ready: true,
		evidence: &delegateGenerationEvidence{requirement: delegateCompletionReportRequired},
	}}
	c.mu.Unlock()
	proof := &watchCustodyProof{root: root, jm: source.jobManager, adapter: adapter, fs: fs}
	proof.hold(errors.New("custody-fault-02ac"))
	return proof
}

func (p *watchCustodyProof) install(t *testing.T, args watchArgs) *watchConfig {
	t.Helper()
	args.ReceiverSessionID = p.root.ID()
	args.StableReceiver = true
	args.SourceDelegateID, args.SourceGeneration = "dlg_source", 1
	args.Send = nil // Production derives the authorized stable receiver send.
	if _, err := p.jm.configureWatch(args); err != nil {
		t.Fatal(err)
	}
	return onlyWatchConfigForTest(t, p.jm)
}

func (p *watchCustodyProof) callbackArgs(args watchArgs) watchArgs {
	args.ReceiverSessionID = p.root.ID()
	args.SourceDelegateID, args.SourceGeneration = "dlg_source", 1
	return args
}

func (p *watchCustodyProof) pending(t *testing.T, cfg *watchConfig, seq uint64, depth int) jobstore.WatchSendState {
	t.Helper()
	produced := producerSourceEvents(t, p.jm, jobstore.EventWatchSendPending)
	pending := loadWatchSendRecord(t, p.jm).Pending
	if len(produced) != int(seq) || len(pending) != 1 || len(producerSourceEvents(t, p.jm, jobstore.EventWatchSendDropped)) != 0 || len(producerSourceEvents(t, p.jm, jobstore.EventWatchSendDelivered)) != p.observed {
		t.Fatalf("source produced=%d pending=%d, want %d/1 without settlement or drop", len(produced), len(pending), seq)
	}
	var state jobstore.WatchSendState
	for _, s := range pending {
		state = *s
	}
	if state.Key.WatchID != cfg.watchID || state.Key.WatchGeneration != cfg.generation || state.UpdateSeq != seq || state.DeliveryID == "" || state.SelfInfluenceDepth != depth || state.ReceiverSessionID != p.root.ID() || state.StableReceiver != cfg.stableReceiver || state.SourceDelegateID != cfg.sourceDelegateID || state.SourceDelegateGeneration != cfg.sourceGeneration || !reflect.DeepEqual(state.Provenance, produced[len(produced)-1].WatchSend.Provenance) {
		t.Fatalf("source identity/classification = %+v", state)
	}
	if producerRecordCount(t, p.root) != p.observed {
		t.Fatal("failed destination fsync granted canonical custody")
	}
	return state
}

func (p *watchCustodyProof) observe(t *testing.T, state jobstore.WatchSendState) {
	t.Helper()
	p.requireSyncFailure(t, state.UpdateSeq)
	woke := make(chan struct{}, 1)
	p.root.SetNotifyFunc(func() {
		select {
		case woke <- struct{}{}:
		default:
		}
	})
	p.fs.syncErr = nil
	p.jm.dispatchProducedWatchSends()
	waitForTestSignal(t, woke, "recovered canonical watch receiver wake")
	id := "watch:" + state.DeliveryID + ":" + strconv.FormatUint(state.UpdateSeq, 10)
	path := transcriptPath(p.root.stateDir, p.root.ID())
	dropped := producerSourceEvents(t, p.jm, jobstore.EventWatchSendDropped)
	delivered := producerSourceEvents(t, p.jm, jobstore.EventWatchSendDelivered)
	if len(loadWatchSendRecord(t, p.jm).Pending) != 0 || len(delivered) != p.observed+1 || len(dropped) != int(state.UpdateSeq)-p.observed-1 {
		t.Fatal("recovered receiver did not settle exactly one source frame")
	}
	ack := delivered[len(delivered)-1].WatchSend
	if ack.Key != state.Key || ack.DeliveryID != state.DeliveryID || ack.UpdateSeq != state.UpdateSeq {
		t.Fatalf("source acknowledgement = %+v, want exact produced identity %+v", ack, state)
	}
	for i, event := range dropped {
		produced := producerSourceEvents(t, p.jm, jobstore.EventWatchSendPending)[i].WatchSend
		if event.WatchSend.DeliveryID != produced.DeliveryID || event.WatchSend.UpdateSeq != produced.UpdateSeq || event.WatchSend.DeliveryID == state.DeliveryID {
			t.Fatal("only an exact superseded pre-custody frame may be dropped")
		}
	}
	// The wire-only delivery_id field is intentionally absent from the model's
	// frame. Preserve all other source data, including its trigger provenance.
	var content []string
	for line := range strings.SplitSeq(state.Frame, "\n") {
		if !strings.HasPrefix(line, "delivery_id:") {
			content = append(content, line)
		}
	}
	wantContent := strings.Join(content, "\n")
	accepted := 0
	for _, entry := range notificationRawEntries(t, path) {
		if entry.Turn.AttentionID == id {
			accepted++
			if entry.Turn.Kind != schema.TurnSteering || entry.Turn.Message.Text() != wantContent || !reflect.DeepEqual(entry.Turn.NotificationProvenance, state.Provenance) {
				t.Fatalf("canonical custody = %+v", entry.Turn)
			}
		}
		for _, observedID := range entry.Turn.ObservedNotificationIDs {
			if observedID == id {
				t.Fatal("custody claimed observation before a response")
			}
		}
	}
	if accepted != 1 {
		t.Fatalf("canonical records for %s = %d, want 1", id, accepted)
	}
	assertNotificationPending(t, p.root, []string{id})
	before := len(p.adapter.requestsFor("root"))
	p.adapter.script("root", func(req llm.Request) llm.Response {
		observationRequest(t, req, []string{wantContent}, nil)
		return finalResponse("custody-response-930e")
	})
	if _, err := p.root.ProcessInput(context.Background(), "custody-observe-a8f0", nil); err != nil {
		t.Fatal(err)
	}
	if len(p.adapter.requestsFor("root")) != before+1 {
		t.Fatal("observation did not use exactly one successful provider request")
	}
	assertNotificationObservedOnce(t, path, id)
	assertNotificationPending(t, p.root, []string{})
	p.jm.dispatchProducedWatchSends()
	if producerRecordCount(t, p.root) != p.observed+1 || len(producerSourceEvents(t, p.jm, jobstore.EventWatchSendDelivered)) != p.observed+1 {
		t.Fatal("transport replay duplicated canonical custody or source settlement")
	}
	p.observed++
}

// onSessionEventKD drives the jobManager's session-event entry point with a kind
// and data, wrapping them in a SessionEvent envelope the way Session.emit does.
// Tests that need to set provenance on the event build a full events.SessionEvent
// literal and call jm.onSessionEvent directly instead.
func onSessionEventKD(jm *jobManager, kind events.EventKind, data events.EventData) {
	jm.onSessionEvent(events.SessionEvent{Kind: kind, SessionID: jm.sessionID, Data: data})
}

// drainAndAccept advances watch delivery the way the live loop does: one drain
// pass (delegate targets deliver + caller pendings re-token) followed by one
// notification accept (caller tokens render by key and settle). Use it in
// Session-based tests to drive a full delivery cycle; pure-jm tests assert on
// pending state instead (the new observable contract at the jobManager level).
func drainAndAccept(t *testing.T, s *Session) {
	t.Helper()
	if err := s.drainPendingWatchSends(context.Background()); err != nil {
		t.Fatalf("drain: %v", err)
	}
	_ = s.acceptNotificationInput(context.Background(), "") // ok to no-op on empty queue
}

// installWatchBelowValidation installs a watch directly into jm.watches the way
// configureWatch does AFTER newWatchConfig succeeds, but WITHOUT the validation
// layer (target/send checks and the feedback-loop guard). It exists so tests can
// exercise the live firing+delivery path (onSessionEvent -> recordWatchSendsAnd
// Kick) for caller-self watch shapes that the create-path guard now rejects.
// newWatchConfig itself runs no loop guard, so this install is legal below
// validation. The install sequence mirrors configureWatch: build cfg, lock,
// initProgressStop, assign, unlock, startProgressTimer (the timer no-ops for
// events-only configs where progressIntervalMS == 0).
func installWatchBelowValidation(t *testing.T, jm *jobManager, a watchArgs) {
	t.Helper()
	if a.Send != nil {
		a.Send.To = strings.TrimSpace(a.Send.To)
	}
	cfg, err := newWatchConfig(a, jm.now(), "")
	if err != nil {
		t.Fatalf("newWatchConfig(%+v): %v", a, err)
	}
	sendTo := ""
	if a.Send != nil {
		sendTo = a.Send.To
	}
	key := watchKey{VisibleSessionID: jm.sessionID, Target: a.Target, SendTo: sendTo}
	jm.mu.Lock()
	stop := cfg.initProgressStop()
	jm.watches[key] = cfg
	jm.mu.Unlock()
	jm.startProgressTimer(key, cfg, stop)
}

func waitForTestSignal(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	// TRIPWIRE: callers signal in-process with no real I/O; this only fires on
	// a genuine hang.
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func loadWatchSendRecord(t *testing.T, jm *jobManager) jobstore.WatchSendRecord {
	t.Helper()
	return jobstore.FoldWatchSends(loadJobStoreEvents(t, jm))
}

func restoredWatchSendPendingEvents(sessionID, watchedJobID, sendTo string, now time.Time) []jobstore.Event {
	return []jobstore.Event{{
		Kind: jobstore.EventWatchSendPending,
		TS:   now,
		WatchSend: &jobstore.WatchSendState{
			Key: jobstore.WatchSendKey{
				VisibleSessionID:        sessionID,
				WatchTarget:             watchedJobID,
				ResolvedWatchedIdentity: watchedJobID,
				ResolvedSendTo:          sendTo,
				WatchGeneration:         "watch_restore_generation",
			},
			DeliveryID:      "delivery_restore_pending",
			UpdateSeq:       1,
			Message:         "restored observe",
			Frame:           "restored observe\n\ndelivery_id: delivery_restore_pending",
			TriggerIdentity: watchedJobID,
			TriggerReason:   "output_match: ready",
			CreatedAt:       now,
			UpdatedAt:       now,
		},
	}}
}

func loadJobStoreEvents(t *testing.T, jm *jobManager) []jobstore.Event {
	t.Helper()
	b, err := os.ReadFile(jm.dir + "/jobs.jsonl")
	if err != nil {
		t.Fatalf("read jobs.jsonl: %v", err)
	}
	var events []jobstore.Event
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e jobstore.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("parse event %q: %v", line, err)
		}
		events = append(events, e)
	}
	return events
}

func installCallerSendWatchWithPending(t *testing.T, jm *jobManager) *watchConfig {
	t.Helper()
	// This is the feedback-loop shape (caller target, communicate,
	// send.to=caller) that configureWatch now rejects (TestValidateWatchDeliveryLoop
	// asserts the rejection). Install below validation to exercise the caller-send
	// pending/busy-delivery mechanics this helper's callers depend on.
	installWatchBelowValidation(t, jm, watchArgs{
		Target: runtimeMessageAliasCaller,
		Events: []string{"communicate"},
		Send:   &watchSendArgs{To: runtimeMessageAliasCaller, Message: "ping"},
	})
	onSessionEventKD(jm, events.EventCommunicate, nil)
	key := watchKey{
		VisibleSessionID: jm.sessionID,
		Target:           runtimeMessageAliasCaller,
		SendTo:           runtimeMessageAliasCaller,
	}
	cfg := jm.watches[key]
	if cfg == nil {
		t.Fatal("installCallerSendWatchWithPending: watch config not found")
	}
	if len(cfg.pendingOrder) == 0 {
		t.Fatal("installCallerSendWatchWithPending: no pending send after busy delivery")
	}
	return cfg
}

// installCallerSendWatchWithCurrentFrame installs a caller-send watch, drives it
// to updateSeq 2 (one busy fire creates pending @1, a second coalesces to @2),
// then stamps a deterministic Frame on the single pending entry so render-by-key
// assertions can match exact frame text. Returns the cfg, the pending key, and
// the pending entry's DeliveryID.
func installCallerSendWatchWithCurrentFrame(t *testing.T, jm *jobManager, frame string) (*watchConfig, jobstore.WatchSendKey, string) {
	t.Helper()
	cfg := installCallerSendWatchWithPending(t, jm)
	onSessionEventKD(jm, events.EventCommunicate, nil) // bump updateSeq 1 -> 2
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if len(cfg.pendingOrder) != 1 {
		t.Fatalf("want exactly one pending entry, got %d", len(cfg.pendingOrder))
	}
	key := cfg.pendingOrder[0]
	state := cfg.pending[key]
	if state == nil {
		t.Fatal("pending entry missing for key")
	}
	if state.UpdateSeq != 2 {
		t.Fatalf("pending updateSeq = %d, want 2 after two fires", state.UpdateSeq)
	}
	state.Frame = frame
	return cfg, key, state.DeliveryID
}

// The v2 re-route this section once tested — the parent's drain re-tokening a
// child's caller-targeted pending onto the parent's rail — is deleted in T15.
// Its replacement, TestDrainDoesNotReRouteChildCallerPendings, lives in
// job_delegate_drivedown_test.go and pins the new behavior: a mid-owner caller
// send renders in the mid's own drive turn, never on the parent's rail.

// terminalShellWithOutput creates a shell job, writes output to it, finalizes it
// completed, and returns the (now store-only) job_id. After finalize the job has
// been removed from jm.running, so a watch attached afterward must resolve its
// terminal status from the store and scan retained output via grepOutput.
func terminalShellWithOutput(t *testing.T, jm *jobManager, output string) string {
	t.Helper()
	rec, err := jm.createShell(createShellOpts{Command: "x"})
	if err != nil {
		t.Fatalf("createShell: %v", err)
	}
	if output != "" {
		if _, err := jm.appendJobOutput(rec.JobID, jm.running[rec.JobID].output, []byte(output)); err != nil {
			t.Fatalf("append output: %v", err)
		}
	}
	code := 0
	if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "exit_zero", &code); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	jm.mu.Lock()
	_, stillRunning := jm.running[rec.JobID]
	jm.mu.Unlock()
	if stillRunning {
		t.Fatalf("job %q still in jm.running after finalize; terminal catch-up tests assume store-only", rec.JobID)
	}
	return rec.JobID
}

// TestTerminalCatchupNoSendFiresNotification covers spec §7.1 "Terminal target":
// an output_match-only watch on a terminal job whose retained output already
// contains a match performs a one-shot catch-up — fires exactly one notification,
// installs no live watch, and reports terminal_catchup with the terminal status.
func onlyWatchConfigForTest(t *testing.T, jm *jobManager) *watchConfig {
	t.Helper()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if len(jm.watches) != 1 {
		t.Fatalf("watch count = %d, want 1", len(jm.watches))
	}
	for _, cfg := range jm.watches {
		return cfg
	}
	panic("unreachable")
}

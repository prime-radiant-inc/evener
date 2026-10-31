package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
)

// Refuse custody before any bytes are written. Production routing, persistence,
// retries and acknowledgement remain real, including the ordinary owner report.
type watchOrderingWriteFS struct {
	afero.Fs
	blocked  atomic.Bool
	rejected atomic.Int64
}

type watchOrderingWriteFile struct {
	afero.File
	fs *watchOrderingWriteFS
}

func (fs *watchOrderingWriteFS) OpenFile(path string, flag int, perm os.FileMode) (afero.File, error) {
	f, err := fs.Fs.OpenFile(path, flag, perm)
	if err != nil {
		return nil, err
	}
	return &watchOrderingWriteFile{File: f, fs: fs}, nil
}

func (f *watchOrderingWriteFile) Write(p []byte) (int, error) {
	if entry, err := transcript.DecodeEntry(p); err == nil && strings.HasPrefix(entry.Turn.AttentionID, "watch:") && f.fs.blocked.Load() {
		f.fs.rejected.Add(1)
		return 0, os.ErrPermission
	}
	return f.File.Write(p)
}

func proveStableWatchRegistrationClear(t *testing.T) {
	t.Helper()
	const marker, payload = "WATCH-PRESERVE-97", "terminal-payload-97"
	a := finiteUpdateAdapter(t, marker)
	sourceGate, receiverGate := make(chan struct{}), make(chan struct{})
	releaseSource := sync.OnceFunc(func() { close(sourceGate) })
	releaseReceiver := sync.OnceFunc(func() { close(receiverGate) })
	a.finiteScript(marker, updateGate(sourceGate, updateReply(toolCallResponse(communicateCallArgs("terminal-97", map[string]any{"message": payload, "end_turn": true})))))
	a.finiteScript("root", updateGate(receiverGate, updateReply(finalResponse("response-97"))))
	clk := agenttest.NewFakeClock()
	root := newMessagingRoot(t, a, func(cfg *SessionConfig) {
		cfg.clock = clk
		cfg.ForceRealIO = true
		cfg.testOnly.disableDelegateIdleRelease = true
	})
	ctx, cancel := context.WithCancel(context.Background())
	var createDone, processDone chan struct{}
	fs := &watchOrderingWriteFS{Fs: afero.NewOsFs()}
	// Registered after root construction: join all users before root.Close owns
	// controller, manager and transcript closure, including on assertion failure.
	t.Cleanup(func() {
		cancel()
		releaseSource()
		releaseReceiver()
		if createDone != nil {
			<-createDone
		}
		root.sendersWG.Wait()
		if processDone != nil {
			<-processDone
		}
		fs.blocked.Store(false)
	})
	if err := root.closeAttachedTranscript(); err != nil {
		t.Fatal(err)
	}
	replacement, entries, err := transcript.OpenWriterForSessionWithFS(fs, root.TranscriptPath(), root.ID())
	if err != nil {
		t.Fatal(err)
	}
	replacement.TrackFailures(entries, root.fork.divergence)
	root.attachTranscript(replacement)
	fs.blocked.Store(true)

	created := make(chan delegateResult, 1)
	createDone = make(chan struct{})
	go func() {
		defer close(createDone)
		created <- root.createDelegate(ctx, delegateArgs{Task: marker})
	}()
	request := a.waitForRequests(t, marker, 1)[0]
	sub := root.getSub(request.SessionID)
	if sub == nil || sub.sess == nil {
		t.Fatal("source provider request has no real child runtime")
	}
	source := sub.sess
	jm := source.jobManager
	if _, err := jobWatchToolWithContext(ctx, root, map[string]any{"operation": "create", "source": source.owningDelegateID, "events": []any{"communicate"}}, 4096); err != nil {
		t.Fatal(err)
	}
	watchID, generation := soleStableWatchRegistration(t, jm, root, source)
	releaseSource()
	result := <-created
	<-createDone
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if result.ChildSessionID != source.ID() {
		t.Fatalf("created source=%s provider source=%s", result.ChildSessionID, source.ID())
	}
	root.sendersWG.Wait() // Encloses actual tool execution and generation settlement.
	pending := jm.pendingWatchSendDeliveries(nil)
	if len(pending) != 1 {
		t.Fatalf("produced pending frames=%d, want 1", len(pending))
	}
	state := pending[0].state
	requireStableWatchState(t, state, watchID, generation, root, source, false)
	requireTerminalWatchFrame(t, state.Frame, watchID, state.DeliveryID, payload)
	originalReceipt := jm.stableWatchReceipt(state.DeliveryID)
	if originalReceipt == nil || fs.rejected.Load() == 0 {
		t.Fatal("filesystem refusal did not leave the original delivery receipt owned")
	}
	watchAttention := stableWatchAttentionID(state)
	// The ordinary report is a separate message, not a watch end notice.
	reportAttention := "delegate:" + source.owningDelegateID + "/delivery/1"
	ids := []string{watchAttention, reportAttention}
	before := readAttentionTranscriptEntries(t, root.TranscriptPath())
	if err := watchUnobservedError(before, []string{watchAttention}, 0); err != nil {
		t.Fatal(err)
	}
	if err := watchUnobservedError(before, []string{reportAttention}, 1); err != nil {
		t.Fatal(err)
	}
	requireWatchSettlement(t, jm, state, 0)

	if _, err := jobWatchToolWithContext(ctx, root, map[string]any{"operation": "clear", "watch_id": watchID}, 4096); err != nil {
		t.Fatal(err)
	}
	cleared := loadJobStoreEvents(t, jm)
	if err := watchProductionBeforeClearError(cleared, state); err != nil {
		t.Fatal(err)
	}
	requireWatchSettlement(t, jm, state, 0)
	pending = jm.pendingWatchSendDeliveries(nil)
	if jm.watchCount() != 0 || len(pending) != 1 || pending[0].state.DeliveryID != state.DeliveryID || pending[0].state.UpdateSeq != state.UpdateSeq || jm.stableWatchReceipt(state.DeliveryID) != originalReceipt {
		t.Fatal("public clear lost pending identity, retained a registration or replaced its receipt")
	}
	if err := watchUnobservedError(readAttentionTranscriptEntries(t, root.TranscriptPath()), []string{watchAttention}, 0); err != nil {
		t.Fatal(err)
	}

	fs.blocked.Store(false)
	// Own the paced transport retry before asserting synchronous redrains.
	clk.Advance(jobNotificationRetryInitialDelay)
	clk.Drain()
	for range 2 { // A second real drain must be idempotent.
		if _, err := source.drainJobManagerWatchSends(ctx, jm); err != nil {
			t.Fatal(err)
		}
		requireWatchSettlement(t, jm, state, 1)
		if len(jm.pendingWatchSendDeliveries(nil)) != 0 || jm.stableWatchReceipt(state.DeliveryID) != nil {
			t.Fatal("successful custody retained source cursor or receipt")
		}
		if err := watchUnobservedError(readAttentionTranscriptEntries(t, root.TranscriptPath()), ids, 1); err != nil {
			t.Fatal(err)
		}
	}

	processed := make(chan error, 1)
	processDone = make(chan struct{})
	go func() {
		defer close(processDone)
		_, err := root.ProcessInputKind(ctx, "", nil, EntryNotification)
		processed <- err
	}()
	rootRequest := a.waitForRequests(t, "root", 1)[0]
	if !incidentDelegatePayload(rootRequest, source.owningDelegateID, payload) {
		t.Fatal("root request lost ordinary report identity or opaque payload")
	}
	fields := incidentWatchFields(rootRequest, watchID)
	if fields["event.kind"] != "communicate" || fields["event.end_turn"] != "true" || fields["event.message"] != payload {
		t.Fatalf("root request lost terminal watch identity or payload: %v", fields)
	}
	gated := readAttentionTranscriptEntries(t, root.TranscriptPath())
	if err := watchUnobservedError(gated, ids, 1); err != nil {
		t.Fatal(err)
	}
	releaseReceiver()
	if err := <-processed; err != nil {
		t.Fatal(err)
	}
	<-processDone
	a.mu.Lock()
	responses := slices.Clone(a.answered["root"])
	a.mu.Unlock()
	if len(responses) != 1 || responses[0].ID == "" || len(a.requestsFor("root")) != 1 || len(a.requestsFor(marker)) != 1 {
		t.Fatal("finite script did not complete exactly one source and one root response")
	}
	finished := readAttentionTranscriptEntries(t, root.TranscriptPath())
	for _, id := range ids {
		if err := incidentCustodyError(finished, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := watchResponseCoverageError(finished, responses[0].ID, ids); err != nil {
		t.Fatal(err)
	}
	if err := watchProductionBeforeClearError(loadJobStoreEvents(t, jm), state); err != nil {
		t.Fatal(err)
	}
	requireWatchSettlement(t, jm, state, 1)
	watchPreservationNegativeControls(t, cleared, gated, finished, state, responses[0].ID, ids)
	t.Logf("watch=%s generation=%s delivery=%s update=%d: production before clear, acceptance=1 acknowledgement=1 dropped=0 evicted=0, response=%s covers watch=%s and report=%s once", watchID, generation, state.DeliveryID, state.UpdateSeq, responses[0].ID, watchAttention, reportAttention)
}

func soleStableWatchRegistration(t *testing.T, jm *jobManager, root, source *Session) (string, string) {
	t.Helper()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	if len(jm.watches) != 1 {
		t.Fatalf("active registrations=%d, want 1", len(jm.watches))
	}
	for _, cfg := range jm.watches {
		if cfg.watchID == "" || cfg.generation == "" || !cfg.stableReceiver || cfg.receiverSessionID != root.ID() || cfg.receiverDelegateID != "" || cfg.sourceDelegateID != source.owningDelegateID || cfg.sourceGeneration != 1 || !slices.Equal(cfg.events, []string{"communicate"}) {
			t.Fatal("public watch lost exact source/receiver binding")
		}
		return cfg.watchID, cfg.generation
	}
	panic("unreachable")
}

func requireStableWatchState(t *testing.T, state jobstore.WatchSendState, watchID, generation string, root, source *Session, endNotice bool) {
	t.Helper()
	if state.Key.WatchID != watchID || state.Key.WatchGeneration != generation || state.Key.VisibleSessionID != source.ID() || state.Key.ResolvedWatchedIdentity == "" || state.DeliveryID == "" || state.UpdateSeq != 1 || !state.StableReceiver || state.ReceiverSessionID != root.ID() || state.ReceiverDelegateID != "" || state.SourceDelegateID != source.owningDelegateID || state.SourceDelegateGeneration != 1 || state.EndNotice != endNotice {
		t.Fatalf("incorrect stable delivery identity: %+v", state)
	}
}

func requireTerminalWatchFrame(t *testing.T, frame, watchID, deliveryID, payload string) {
	t.Helper()
	_, body, ok := strings.Cut(frame, "Watch frame\n")
	if !ok {
		t.Fatal("missing machine watch frame")
	}
	// The frame is line-oriented, not YAML: trigger prose can contain colons.
	// This fixture's opaque payload is one line, so read exact machine fields.
	fields := map[string]string{}
	inEvent := false
	for line := range strings.SplitSeq(body, "\n") {
		if line == "event:" {
			inEvent = true
			continue
		}
		key, value, found := strings.Cut(line, ": ")
		if !found {
			continue
		}
		if inEvent && strings.HasPrefix(key, "  ") {
			fields["event."+strings.TrimSpace(key)] = value
		} else if !strings.HasPrefix(key, " ") {
			fields[key] = value
		}
	}
	if fields["watch_id"] != watchID || fields["delivery_id"] != deliveryID || fields["event.kind"] != "communicate" || fields["event.message"] != payload || fields["event.end_turn"] != "true" || fields["event.truncated"] != "false" {
		t.Fatalf("incorrect terminal event: %+v", fields)
	}
}

func requireWatchSettlement(t *testing.T, jm *jobManager, state jobstore.WatchSendState, delivered int) {
	t.Helper()
	events := loadJobStoreEvents(t, jm)
	for kind, want := range map[jobstore.EventKind]int{jobstore.EventWatchSendDelivered: delivered, jobstore.EventWatchSendDropped: 0, jobstore.EventWatchSendEvicted: 0} {
		if got := countExactWatchSendEvents(events, kind, state); got != want {
			t.Fatalf("delivery=%s update=%d kind=%s records=%d want=%d", state.DeliveryID, state.UpdateSeq, kind, got, want)
		}
	}
}

// Independent physical-record checks, not another attention fold. Pending rows
// may repeat on retry, but their immutable identity and payload must not change.
func watchProductionBeforeClearError(events []jobstore.Event, state jobstore.WatchSendState) error {
	firstPending, clearAt, clears := -1, -1, 0
	for i, event := range events {
		if event.Kind == jobstore.EventWatchSendPending && event.WatchSend != nil && event.WatchSend.Key.WatchID == state.Key.WatchID && event.WatchSend.Key.WatchGeneration == state.Key.WatchGeneration {
			got := *event.WatchSend
			if got.Key != state.Key || got.DeliveryID != state.DeliveryID || got.UpdateSeq != state.UpdateSeq || got.Frame != state.Frame || got.EndNotice != state.EndNotice || got.StableReceiver != state.StableReceiver || got.ReceiverSessionID != state.ReceiverSessionID || got.ReceiverDelegateID != state.ReceiverDelegateID || got.SourceDelegateID != state.SourceDelegateID || got.SourceDelegateGeneration != state.SourceDelegateGeneration {
				return errors.New("terminal pending identity or payload changed")
			}
			if firstPending < 0 {
				firstPending = i
			}
		}
		if event.Kind == jobstore.EventWatchCleared && event.WatchID == state.Key.WatchID && event.Watch != nil && event.Watch.Generation == state.Key.WatchGeneration {
			clearAt, clears = i, clears+1
		}
	}
	if firstPending < 0 || clears != 1 || clearAt <= firstPending {
		return fmt.Errorf("production=%d clear=%d clear records=%d", firstPending, clearAt, clears)
	}
	return nil
}

func watchUnobservedError(entries []transcript.Entry, ids []string, wantAccepted int) error {
	for _, id := range ids {
		accepted := 0
		for _, entry := range entries {
			turn := entry.Turn
			if turn.AttentionID == id {
				accepted++
			}
			if slices.Contains(turn.ObservedNotificationIDs, id) || (turn.AttentionResolution != nil && turn.AttentionResolution.AttentionID == id) {
				return fmt.Errorf("notification=%s observed or resolved before successful response", id)
			}
		}
		if accepted != wantAccepted {
			return fmt.Errorf("notification=%s acceptance=%d want=%d", id, accepted, wantAccepted)
		}
	}
	return nil
}

func watchResponseCoverageError(entries []transcript.Entry, responseID string, ids []string) error {
	seen := 0
	want := slices.Clone(ids)
	slices.Sort(want)
	for _, entry := range entries {
		turn := entry.Turn
		if turn.Kind == schema.TurnAssistant && turn.ResponseID == responseID {
			seen++
			got := slices.Clone(turn.ObservedNotificationIDs)
			slices.Sort(got)
			if !slices.Equal(got, want) {
				return fmt.Errorf("response=%s observed=%v want=%v", responseID, got, want)
			}
		} else {
			for _, id := range ids {
				if slices.Contains(turn.ObservedNotificationIDs, id) {
					return fmt.Errorf("notification=%s covered by another physical record", id)
				}
			}
		}
	}
	if seen != 1 {
		return fmt.Errorf("response=%s physical records=%d want=1", responseID, seen)
	}
	return nil
}

// These controls falsify copied evidence only. The pre-write filesystem refusal
// above is the production transport fault, not these oracle checks.
func watchPreservationNegativeControls(t *testing.T, source []jobstore.Event, gated, finished []transcript.Entry, state jobstore.WatchSendState, responseID string, ids []string) {
	t.Helper()
	for _, mutation := range []string{"missing production", "missing clear", "reversed production and clear"} {
		t.Run("oracle rejects "+mutation, func(t *testing.T) {
			cloned := incidentCopy(t, source)
			pendingAt, clearAt, changed := -1, -1, 0
			for i, event := range cloned {
				if exactWatchSendEvent(event, jobstore.EventWatchSendPending, state) {
					if pendingAt < 0 {
						pendingAt = i
					}
					if mutation == "missing production" {
						cloned[i] = jobstore.Event{}
						changed++
					}
				}
				if event.Kind == jobstore.EventWatchCleared && event.WatchID == state.Key.WatchID && event.Watch != nil && event.Watch.Generation == state.Key.WatchGeneration {
					clearAt = i
					if mutation == "missing clear" {
						cloned[i] = jobstore.Event{}
						changed++
					}
				}
			}
			if mutation == "reversed production and clear" && pendingAt >= 0 && clearAt > pendingAt {
				cloned[pendingAt], cloned[clearAt] = cloned[clearAt], cloned[pendingAt]
				changed++
			}
			if changed == 0 || watchProductionBeforeClearError(cloned, state) == nil {
				t.Fatal("corrupt source order survived physical-record oracle")
			}
		})
	}
	for _, mutation := range []string{"missing acceptance", "duplicate acceptance", "incomplete response coverage", "extra response coverage", "coverage on wrong response"} {
		t.Run("oracle rejects "+mutation, func(t *testing.T) {
			cloned := incidentCopy(t, finished)
			changed := 0
			for i, entry := range cloned {
				if entry.Turn.AttentionID == ids[0] {
					switch mutation {
					case "missing acceptance":
						cloned[i] = transcript.Entry{}
						changed++
					case "duplicate acceptance":
						cloned = append(cloned, entry)
						changed++
					}
				}
				if entry.Turn.ResponseID == responseID {
					switch mutation {
					case "incomplete response coverage":
						cloned[i].Turn.ObservedNotificationIDs = slices.Clone(ids[1:])
						changed++
					case "extra response coverage":
						cloned[i].Turn.ObservedNotificationIDs = append(slices.Clone(ids), "foreign-notification-97")
						changed++
					case "coverage on wrong response":
						cloned[i].Turn.ResponseID = "foreign-response-97"
						changed++
					}
				}
			}
			custodyErr := incidentCustodyError(cloned, ids[0])
			coverageErr := watchResponseCoverageError(cloned, responseID, ids)
			if changed != 1 || (custodyErr == nil && coverageErr == nil) {
				t.Fatal("corrupt receiver evidence survived physical-record oracle")
			}
		})
	}
	t.Run("oracle rejects early observation", func(t *testing.T) {
		cloned := incidentCopy(t, gated)
		cloned = append(cloned, transcript.Entry{Turn: schema.Turn{Kind: schema.TurnAssistant, ResponseID: "early-response-97", ObservedNotificationIDs: slices.Clone(ids)}})
		if watchUnobservedError(cloned, ids, 1) == nil {
			t.Fatal("early observed IDs survived held-response oracle")
		}
	})
}

// This component cut has no outstanding ordinary delivery receipt. It tests
// manager restore policy against a resident controller, not runtime bootstrap.
func reopenSettledWatchManager(t *testing.T, fixture *stableWatchRuntimeFixture) {
	t.Helper()
	if len(fixture.sourceJM.pendingWatchSendDeliveries(nil)) != 0 {
		t.Fatal("component reopen requires settled source sends")
	}
	if _, err := fixture.controller.FinishGeneration(delegateLease{delegateID: "dlg_source", generation: 1}, delegateFinish{}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.sourceJM.closeStoreOnly(); err != nil {
		t.Fatal(err)
	}
	fixture.sourceJM = nil
	reopened, err := newJobManager(fixture.controller.stateDir, fixture.source.ID(), nil)
	if err != nil {
		t.Fatal(err)
	}
	fixture.sourceJM, fixture.source.jobManager = reopened, reopened
	reopened.delegateController = fixture.controller
}

func requireRuntimeLostWatchClear(t *testing.T, jm *jobManager, watchID, generation string) {
	t.Helper()
	clears := 0
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.Kind == jobstore.EventWatchCleared && event.WatchID == watchID && event.Watch != nil && event.Watch.Generation == generation {
			clears++
			if event.Watch.EndReason != "runtime_lost" {
				t.Fatalf("registration end reason=%s want runtime_lost", event.Watch.EndReason)
			}
		}
	}
	if clears != 1 || jm.watchCount() != 0 {
		t.Fatalf("restore clears=%d active registrations=%d", clears, jm.watchCount())
	}
}

func soleProducedWatchState(t *testing.T, jm *jobManager, watchID, generation string) jobstore.WatchSendState {
	t.Helper()
	var state jobstore.WatchSendState
	for _, event := range loadJobStoreEvents(t, jm) {
		if event.Kind != jobstore.EventWatchSendPending || event.WatchSend == nil || event.WatchSend.Key.WatchID != watchID || event.WatchSend.Key.WatchGeneration != generation {
			continue
		}
		if state.DeliveryID != "" && (event.WatchSend.DeliveryID != state.DeliveryID || event.WatchSend.UpdateSeq != state.UpdateSeq) {
			t.Fatal("registration produced multiple immutable send identities")
		}
		state = *event.WatchSend
	}
	if state.DeliveryID == "" {
		t.Fatal("registration has no physically produced send")
	}
	return state
}

func requireSettledComponentWatch(t *testing.T, fixture *stableWatchRuntimeFixture, state jobstore.WatchSendState) {
	t.Helper()
	requireWatchSettlement(t, fixture.sourceJM, state, 1)
	if len(fixture.sourceJM.pendingWatchSendDeliveries(nil)) != 0 || fixture.sourceJM.stableWatchReceipt(state.DeliveryID) != nil {
		t.Fatal("settled component send retained a cursor or receipt")
	}
	if err := watchUnobservedError(readAttentionTranscriptEntries(t, fixture.rootTranscriptPath), []string{stableWatchAttentionID(state)}, 1); err != nil {
		t.Fatal(err)
	}
}

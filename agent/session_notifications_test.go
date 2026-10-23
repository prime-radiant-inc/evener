package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func TestNotificationColdStopWaitsForWriterClose(t *testing.T) {
	t.Parallel()
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	seedDelegateControllerIdle(t, c, "dlg_owner", "")
	sessionID := c.durable["dlg_owner"].Descriptor.ChildSessionID
	path := transcriptPath(c.stateDir, sessionID)
	w, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	fs := &notificationColdBarrierFS{Fs: afero.NewOsFs(), writeEntered: make(chan struct{}), writeRelease: make(chan struct{}), closeEntered: make(chan struct{}), closeRelease: make(chan struct{})}
	unblockWrite := sync.OnceFunc(func() { close(fs.writeRelease) })
	unblockClose := sync.OnceFunc(func() { close(fs.closeRelease) })
	c.attentionOpen = func(path, id string) (*transcript.Writer, []transcript.Entry, error) {
		return transcript.OpenWriterForSessionWithFS(fs, path, id)
	}
	n := notification{ID: "stop-cold", Message: llm.User("stop-cold-body")}
	done := make(chan struct{})
	var acceptanceErr error
	go func() { acceptanceErr = c.enqueueNotification(sessionID, n); close(done) }()
	defer func() { unblockWrite(); unblockClose(); <-done }()
	<-fs.writeEntered
	stop, _, _, err := c.StopSubtree(rootDelegateActor(c.rootSessionID), "dlg_owner")
	if err != nil {
		t.Fatal(err)
	}
	reconcile := func() delegateMutationPlans {
		t.Helper()
		evidence, err := collectDelegateReconcileEvidence(c.stateDir, c.ReconcileRequirements())
		if err != nil {
			t.Fatal(err)
		}
		plans, err := c.Reconcile(evidence)
		if err != nil {
			t.Fatal(err)
		}
		return plans
	}
	assertStillOwned := func(stage string) {
		t.Helper()
		plans := reconcile()
		select {
		case <-stop.done:
			t.Fatalf("Stop completed before cold writer %s", stage)
		default:
		}
		if len(plans.attention) != 0 {
			t.Fatalf("Stop planned a second cold writer before %s: %+v", stage, plans.attention)
		}
		if err := c.enqueueNotification(sessionID, notification{ID: "second", Message: llm.User("second")}); err == nil {
			t.Fatal("second cold enqueue admitted during Stop")
		}
		fs.mu.Lock()
		defer fs.mu.Unlock()
		if fs.active != 1 || fs.peak != 1 {
			t.Fatalf("cold writers before %s: active=%d peak=%d", stage, fs.active, fs.peak)
		}
	}
	assertStillOwned("append")
	unblockWrite()
	<-fs.closeEntered
	assertNotificationRecords(t, path, n.ID, n.Message, 1)
	assertStillOwned("close")
	unblockClose()
	<-done
	if acceptanceErr != nil {
		t.Fatal(acceptanceErr)
	}
	for range 3 {
		plans := reconcile()
		for _, cleanup := range plans.attention {
			if err := c.executeDelegateAttentionCleanup(cleanup); err != nil {
				t.Fatal(err)
			}
		}
	}
	select {
	case <-stop.done:
	default:
		t.Fatal("Stop did not complete after writer close and cleanup")
	}
	assertNotificationRecords(t, path, n.ID, n.Message, 1)
	resolutions := 0
	for _, entry := range notificationRawEntries(t, path) {
		if resolution := entry.Turn.AttentionResolution; resolution != nil && resolution.AttentionID == n.ID {
			resolutions++
			if resolution.Disposition != string(delegateAttentionDiscarded) {
				t.Fatalf("Stop disposition = %q", resolution.Disposition)
			}
		}
	}
	if resolutions != 1 {
		t.Fatalf("raw Stop resolutions = %d, want 1", resolutions)
	}
}

// Only the external transcript file is blocked. The real controller, writer,
// sync, Stop admission, evidence collector and cleanup all run unchanged.
type notificationColdBarrierFS struct {
	afero.Fs
	mu                                                     sync.Mutex
	active, peak, opens                                    int
	writeOnce, closeOnce                                   sync.Once
	writeEntered, writeRelease, closeEntered, closeRelease chan struct{}
}

func (fs *notificationColdBarrierFS) OpenFile(path string, flag int, mode os.FileMode) (afero.File, error) {
	f, err := fs.Fs.OpenFile(path, flag, mode)
	if err != nil || flag&os.O_RDWR == 0 {
		return f, err
	}
	fs.mu.Lock()
	fs.active++
	fs.opens++
	fs.peak = max(fs.peak, fs.active)
	fs.mu.Unlock()
	return &notificationColdBarrierFile{File: f, fs: fs}, nil
}

type notificationColdBarrierFile struct {
	afero.File
	fs *notificationColdBarrierFS
}

func (f *notificationColdBarrierFile) Write(p []byte) (int, error) {
	f.fs.writeOnce.Do(func() { close(f.fs.writeEntered); <-f.fs.writeRelease })
	return f.File.Write(p)
}

func (f *notificationColdBarrierFile) Close() error {
	f.fs.closeOnce.Do(func() { close(f.fs.closeEntered); <-f.fs.closeRelease })
	err := f.File.Close()
	f.fs.mu.Lock()
	f.fs.active--
	f.fs.mu.Unlock()
	return err
}

func TestNotificationSourceAckFailureRetainsReceiverWake(t *testing.T) {
	t.Parallel()
	root, path := notificationReceiverForTest(t, nil)
	c := root.delegateController
	seedDelegateControllerIdle(t, c, "dlg_source", "")
	seedDelegateControllerDelivery(t, c, "dlg_source")
	plans := c.ReplayDeliveries()
	if len(plans) != 1 {
		t.Fatalf("delivery plans = %d", len(plans))
	}
	journalPath := filepath.Join(c.stateDir, "delegate-events.jsonl")
	before, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.store.Close(); err != nil {
		t.Fatal(err)
	}
	wakes := 0
	root.SetNotifyFunc(func() { wakes++ })
	if _, err := deliverDelegatePacket(plans[0], root); err == nil {
		t.Fatal("closed source journal accepted acknowledgment")
	}
	if wakes != 1 {
		t.Fatalf("durable receiver wakes after source-ack failure = %d, want 1", wakes)
	}
	after, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) || len(c.durable["dlg_source"].PendingDeliveries) != 1 {
		t.Fatal("source acknowledged on failure")
	}
	attentionID := "delegate:dlg_source/delivery/1"
	assertNotificationPending(t, root, []string{attentionID})
	entries := notificationRawEntries(t, path)
	if len(entries) != 1 || entries[0].Turn.AttentionID != attentionID {
		t.Fatalf("accepted raw records = %+v", entries)
	}
	reopened, err := delegatestore.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	c.store = reopened
	retry := c.ReplayDeliveries()
	if len(retry) != 1 {
		t.Fatalf("retry plans = %d", len(retry))
	}
	if _, err := deliverDelegatePacket(retry[0], root); err != nil {
		t.Fatal(err)
	}
	assertNotificationRecords(t, path, attentionID, entries[0].Turn.Message, 1)
	if len(c.durable["dlg_source"].PendingDeliveries) != 0 || wakes != 2 {
		t.Fatalf("successful retry lost custody transfer or readiness, wakes = %d", wakes)
	}
}

func TestNotificationColdSourceAckFailureRetainsReceiverRecovery(t *testing.T) {
	t.Parallel()
	root, _ := notificationReceiverForTest(t, nil)
	c := root.delegateController
	seedDelegateControllerIdle(t, c, "dlg_owner", "")
	seedDelegateControllerIdle(t, c, "dlg_source", "dlg_owner")
	seedDelegateControllerDelivery(t, c, "dlg_source")
	seedDelegateControllerDelivery(t, c, "dlg_source")
	sessionID := c.durable["dlg_owner"].Descriptor.ChildSessionID
	path := transcriptPath(c.stateDir, sessionID)
	writeEmptyAttentionTranscript(t, path, sessionID)
	released := make(chan struct{})
	close(released)
	fs := &notificationColdBarrierFS{Fs: afero.NewOsFs(), writeEntered: make(chan struct{}), writeRelease: released, closeEntered: make(chan struct{}), closeRelease: released}
	c.attentionOpen = func(path, id string) (*transcript.Writer, []transcript.Entry, error) {
		return transcript.OpenWriterForSessionWithFS(fs, path, id)
	}
	plans := c.ReplayDeliveries()
	if len(plans) != 1 {
		t.Fatalf("plans = %d", len(plans))
	}
	first := plans[0]
	journalPath := filepath.Join(c.stateDir, "delegate-events.jsonl")
	before, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.store.Close(); err != nil {
		t.Fatal(err)
	}
	wakes := 0
	root.SetNotifyFunc(func() { wakes++ })
	if _, err := deliverColdDelegatePacket(first); err == nil {
		t.Fatal("closed source journal accepted acknowledgment")
	}
	after, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || len(c.durable["dlg_source"].PendingDeliveries) != 2 || c.durable["dlg_source"].PendingDeliveries[0].DeliveryID != first.deliveryID {
		t.Fatal("failed source acknowledgment lost FIFO custody")
	}
	attentionID := delegateAttentionID(first.deliveryID)
	entries := notificationRawEntries(t, path)
	if len(entries) != 1 || entries[0].Turn.AttentionID != attentionID {
		t.Fatalf("cold accepted records = %+v", entries)
	}
	if wakes == 0 {
		t.Error("cold receiver was not woken after durable acceptance and source-ack failure")
	}
	if delegateID, selectedID, ok := c.nextIdleDelegateAttention(); !ok || delegateID != "dlg_owner" || selectedID != attentionID {
		t.Errorf("cold recovery selection = %q, %q, %v", delegateID, selectedID, ok)
	}
	coldRetry := c.ReplayDeliveries()
	if len(coldRetry) != 1 || coldRetry[0].deliveryID != first.deliveryID {
		t.Fatalf("cold retry did not retain FIFO head: %+v", coldRetry)
	}
	if _, err := deliverColdDelegatePacket(coldRetry[0]); err == nil {
		t.Fatal("cold retry acknowledged while source journal remained closed")
	}
	if fs.opens != 1 || fs.active != 0 {
		t.Fatalf("accepted cold writer reopened for source-ack retry: opens=%d active=%d", fs.opens, fs.active)
	}
	assertNotificationRecords(t, path, attentionID, entries[0].Turn.Message, 1)
	// Reopen the actual receiver while source acknowledgment is still failing.
	// Admission must be possible now, not merely after that journal is repaired.
	w, _, err := transcript.OpenWriterForSession(path, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	owner := &Session{id: sessionID, stateDir: c.stateDir, delegateController: c, owningDelegateID: "dlg_owner"}
	owner.attachTranscript(w)
	if err := c.AttachIdleRuntime("dlg_owner", owner); err != nil {
		t.Fatal(err)
	}
	assertNotificationPending(t, owner, []string{attentionID})
	reservation, err := c.ReserveAttention(owner, attentionID)
	if err != nil {
		t.Fatalf("durable receiver recovery blocked by failed source acknowledgment: %v", err)
	}
	retry := c.ReplayDeliveries()
	if len(retry) != 1 || retry[0].deliveryID != first.deliveryID {
		t.Fatalf("failed source head retry = %+v", retry)
	}
	if _, err := deliverColdDelegatePacket(retry[0]); err == nil {
		t.Fatal("retry acknowledged while source journal remained closed")
	}
	if err := c.AbortStart(reservation); err != nil {
		t.Fatal(err)
	}
	assertNotificationRecords(t, path, attentionID, entries[0].Turn.Message, 1)
	reopened, err := delegatestore.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	c.store = reopened
	retry = c.ReplayDeliveries()
	if len(retry) != 1 || retry[0].deliveryID != first.deliveryID {
		t.Fatalf("repaired source retry = %+v", retry)
	}
	next, err := deliverColdDelegatePacket(retry[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(c.durable["dlg_source"].PendingDeliveries) != 1 || len(next.deliveries) != 1 || next.deliveries[0].deliveryID != c.durable["dlg_source"].PendingDeliveries[0].DeliveryID {
		t.Fatal("successful head acknowledgment did not release exactly the next FIFO delivery")
	}
	assertNotificationRecords(t, path, attentionID, entries[0].Turn.Message, 1)
	events, err := c.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	acks := 0
	for _, event := range events {
		if event.DeliveryAcknowledged != nil && event.DeliveryAcknowledged.DeliveryID == first.deliveryID {
			acks++
		}
	}
	if acks != 1 {
		t.Fatalf("raw source acknowledgments = %d, want 1", acks)
	}
}

// Cold acceptance must survive reopen and become operative only at a model
// boundary that selects it. A notification arriving inside the provider's first
// call cannot cause that call's tool events, but joins the next request.
func TestNotificationCausalActivationAfterColdReopen(t *testing.T) {
	t.Parallel()
	adapter := newMessagingAdapter()
	original := newMessagingRoot(t, adapter, nil)
	stateDir, id := original.stateDir, original.ID()
	original.Close()
	cold := coldDelegateDeliveryReceiver{stateDir: stateDir, transcriptRef: encodeRef("", id)}
	accepted := notification{ID: "cold-causal", Message: llm.User("cold-causal-body"), Provenance: testProvenance("watch_cold", "wg_cold")}
	if _, err := cold.appendNotificationDurably(accepted); err != nil {
		t.Fatal(err)
	}
	assertNotificationRecords(t, transcriptPath(stateDir, id), accepted.ID, accepted.Message, 1)
	assertNotificationProvenance(t, transcriptPath(stateDir, id), accepted.Provenance)
	meta, err := schema.LoadSessionMeta(stateDir, id)
	if err != nil {
		t.Fatal(err)
	}
	client := llm.NewClient()
	client.Register(adapter)
	reopened, err := RestoreSessionFromMetaWithConfig(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{StateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	if got := reopened.activeCausalProvenance(); got != nil {
		t.Fatalf("acceptance/restore activated provenance: %+v", got)
	}
	late := notification{ID: "late-causal", Message: llm.User("late-causal-body"), Provenance: testProvenance("watch_late", "wg_late")}
	adapter.script("root", func(req llm.Request) llm.Response {
		if !requestContainsText(req, "cold-causal-body") || requestContainsText(req, "late-causal-body") {
			t.Fatalf("first request did not select exactly the cold notification: %+v", req.Messages)
		}
		if err := reopened.enqueueNotification(late); err != nil {
			t.Fatal(err)
		}
		if provenance.ContainsWatch(reopened.activeCausalProvenance(), "watch_late", "wg_late") {
			t.Fatal("acceptance activated unpresented provenance")
		}
		assertNotificationPending(t, reopened, []string{accepted.ID, late.ID})
		return jobListResponse("cold-call")
	}, func(req llm.Request) llm.Response {
		if !requestContainsText(req, "cold-causal-body") || !requestContainsText(req, "late-causal-body") {
			t.Fatalf("second request omitted accepted history: %+v", req.Messages)
		}
		return jobListResponse("late-call")
	}, func(llm.Request) llm.Response { return finalResponse("causal-done") })
	var toolEvents []events.SessionEvent
	drained := make(chan struct{})
	reopened.ConsumeEventsLossless(func(ev events.SessionEvent) {
		if ev.Kind == events.EventToolCallEnd {
			if data, ok := ev.Data.(events.ToolCallEndData); ok && (data.CallID == "cold-call" || data.CallID == "late-call") {
				toolEvents = append(toolEvents, ev)
			}
		}
	}, func() { close(drained) })
	_, runErr := reopened.processInputWithProvenance(context.Background(), "continue", nil, testProvenance("watch_input", "wg_input"))
	reopened.Close()
	<-drained
	if runErr != nil {
		t.Fatal(runErr)
	}
	if len(toolEvents) != 2 {
		t.Fatalf("tool events = %d, want 2", len(toolEvents))
	}
	for i, ev := range toolEvents {
		if !provenance.ContainsWatch(ev.Provenance, "watch_cold", "wg_cold") || !provenance.ContainsWatch(ev.Provenance, "watch_input", "wg_input") {
			t.Fatalf("tool event %d lost cold/input causal union: %+v", i, ev.Provenance)
		}
		if got := provenance.ContainsWatch(ev.Provenance, "watch_late", "wg_late"); got != (i == 1) {
			t.Fatalf("tool event %d late origin = %v", i, got)
		}
	}
}

// These cases catch missing custody, wakes before fsync, lost retry wakes,
// duplicate appends, overwritten payloads, and transport acknowledgments on error.
func TestNotificationAcceptance(t *testing.T) {
	t.Parallel()
	n := notification{ID: "notification-test-1", Message: llm.User("payload-7c41"), Provenance: testProvenance("watch_accept", "wg_accept")}
	t.Run("resident idle", func(t *testing.T) {
		s, path := notificationReceiverForTest(t, nil)
		wakes := 0
		s.SetNotifyFunc(func() { wakes++; assertNotificationRecords(t, path, n.ID, n.Message, 1) })
		if err := s.enqueueNotification(n); err != nil {
			t.Fatal(err)
		}
		if wakes != 1 {
			t.Fatalf("wakes = %d, want 1", wakes)
		}
		assertNotificationPending(t, s, []string{n.ID})
		assertNotificationProvenance(t, path, n.Provenance)
	})
	t.Run("resident running", func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer unblock()
		adapter := newMessagingAdapter()
		adapter.script("root", func(llm.Request) llm.Response { close(entered); <-release; return finalResponse("payload-end") })
		s := newMessagingRoot(t, adapter, nil)
		done := make(chan error, 1)
		go func() { _, err := s.ProcessInput(context.Background(), "payload-start", nil); done <- err }()
		<-entered
		wakes := 0
		s.SetNotifyFunc(func() { wakes++ })
		err := s.enqueueNotification(n)
		if err == nil {
			assertNotificationRecords(t, transcriptPath(s.stateDir, s.ID()), n.ID, n.Message, 1)
			assertNotificationPending(t, s, []string{n.ID})
		}
		unblock()
		if runErr := <-done; runErr != nil {
			t.Fatal(runErr)
		}
		if err != nil {
			t.Fatal(err)
		}
		if wakes == 0 {
			t.Fatal("running receiver did not request readiness")
		}
	})
	t.Run("cold receiver", func(t *testing.T) {
		c, _ := newDelegateControllerTestHarness(t, 2, 1)
		seedDelegateControllerIdle(t, c, "dlg_target", "")
		root, _ := notificationReceiverForTest(t, nil)
		c.rootRuntime = root
		wakes := 0
		root.SetNotifyFunc(func() { wakes++ })
		sessionID := c.durable["dlg_target"].Descriptor.ChildSessionID
		path := transcriptPath(c.stateDir, sessionID)
		writer, err := transcript.NewWriter(path, transcript.Header{SessionID: sessionID})
		if err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := c.enqueueNotification(sessionID, n); err != nil {
			t.Fatal(err)
		}
		if err := c.enqueueNotification(sessionID, n); err != nil {
			t.Fatal(err)
		}
		assertNotificationRecords(t, path, n.ID, n.Message, 1)
		assertNotificationProvenance(t, path, n.Provenance)
		fold, err := readDelegateAttentionFold(path, sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(fold.pendingIDs(), []string{n.ID}) {
			t.Fatalf("pending = %v", fold.pendingIDs())
		}
		if wakes != 2 {
			t.Fatalf("cold wakes = %d, want 2", wakes)
		}
		changed := n
		changed.Message = llm.User("payload-conflict")
		if err := c.enqueueNotification(sessionID, changed); err == nil {
			t.Fatal("cold conflict accepted")
		}
		assertNotificationRecords(t, path, n.ID, n.Message, 1)
		if err := c.enqueueNotification("foreign-session", n); !errors.Is(err, errDelegateDeliveryReceiverUnavailable) {
			t.Fatalf("foreign receiver error = %v", err)
		}
	})
	t.Run("duplicate ID and body repairs wake", func(t *testing.T) {
		s, path := notificationReceiverForTest(t, nil)
		if err := s.enqueueNotification(n); err != nil {
			t.Fatal(err)
		}
		// A lost wake must not turn an idempotent retry into a silent no-op.
		wakes := 0
		s.SetNotifyFunc(func() { wakes++ })
		wakes = 0 // Registration itself rechecks durable work.
		if err := s.enqueueNotification(n); err != nil {
			t.Fatal(err)
		}
		if wakes != 1 {
			t.Fatalf("retry wakes = %d, want 1", wakes)
		}
		assertNotificationRecords(t, path, n.ID, n.Message, 1)
	})
	t.Run("conflicting duplicate body", func(t *testing.T) {
		s, path := notificationReceiverForTest(t, nil)
		if err := s.enqueueNotification(n); err != nil {
			t.Fatal(err)
		}
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		conflict := n
		conflict.Message = llm.User("payload-conflict")
		wakes := 0
		s.SetNotifyFunc(func() { wakes++ })
		wakes = 0
		if err := s.enqueueNotification(conflict); err == nil {
			t.Fatal("conflict accepted")
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, after) || wakes != 0 {
			t.Fatalf("conflict changed transcript or woke receiver, wakes=%d", wakes)
		}
		assertNotificationRecords(t, path, n.ID, n.Message, 1)
	})
	t.Run("immutable duplicate metadata", func(t *testing.T) {
		s, path := notificationReceiverForTest(t, nil)
		if err := s.enqueueNotification(n); err != nil {
			t.Fatal(err)
		}
		retry := n
		retry.Provenance = testProvenance("watch_other", "wg_other")
		if err := s.enqueueNotification(retry); err != nil {
			t.Fatal(err)
		}
		assertNotificationProvenance(t, path, n.Provenance)
	})
	t.Run("sync before wake", func(t *testing.T) {
		fs := newAttentionSyncBarrierFS()
		s, path := notificationReceiverForTest(t, fs)
		woke := make(chan struct{}, 1)
		s.SetNotifyFunc(func() { woke <- struct{}{} })
		fs.arm()
		defer fs.release()
		done := make(chan error, 1)
		go func() { done <- s.enqueueNotification(n) }()
		select {
		case <-fs.syncEntered:
		case err := <-done:
			t.Fatalf("returned before sync: %v", err)
		}
		select {
		case <-woke:
			t.Fatal("wake preceded sync")
		default:
		}
		fs.release()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		select {
		case <-woke:
		default:
			t.Fatal("no wake after sync")
		}
		assertNotificationRecords(t, path, n.ID, n.Message, 1)
	})
	for _, fault := range []string{"append failure", "sync failure", "fold-read failure"} {
		t.Run(fault, func(t *testing.T) {
			fs := &notificationFaultFS{Fs: afero.NewOsFs()}
			s, path := notificationReceiverForTest(t, fs)
			wakes := 0
			s.SetNotifyFunc(func() { wakes++ })
			switch fault {
			case "append failure":
				fs.writeErr = syscall.ENOSPC
			case "sync failure":
				fs.syncErr = syscall.EIO
			case "fold-read failure":
				// A directory at the transcript path fails the actual reader, not an
				// internal fold seam. Keep the original file to restore after the check.
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			err := s.enqueueNotification(n)
			if err == nil {
				t.Fatal("failed receiver accepted custody")
			}
			if fault == "append failure" && !errors.Is(err, syscall.ENOSPC) {
				t.Fatal(err)
			}
			if fault == "sync failure" && !errors.Is(err, syscall.EIO) {
				t.Fatal(err)
			}
			if fault == "fold-read failure" {
				if _, err := s.pendingNotificationIDs(); err == nil {
					t.Fatal("pending query swallowed read failure")
				}
				if _, err := s.hasUnackedNotifications(); err == nil {
					t.Fatal("readiness query swallowed read failure")
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(path+".saved", path); err != nil {
					t.Fatal(err)
				}
			}
			if wakes != 0 {
				t.Fatalf("failure wakes = %d", wakes)
			}
			assertNotificationRecords(t, path, n.ID, n.Message, 0)
			// Exercise the real source journal, admission, and transport completion.
			c := s.delegateController
			seedDelegateControllerIdle(t, c, "dlg_source", "")
			lease, _ := startDelegateDeliveryGeneration(t, c, "dlg_source", false)
			plans := finishDelegateDeliveryGeneration(t, c, lease, "payload-report")
			if fault == "fold-read failure" {
				if err := os.Rename(path, path+".saved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			_, deliveryErr := deliverDelegatePacket(plans.deliveries[0], s)
			if deliveryErr == nil {
				t.Fatal("transport accepted failed receiver")
			}
			events, err := c.store.Load()
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if event.Kind == delegatestore.EventDelegateDeliveryAcknowledged {
					t.Fatal("source custody acknowledged on failure")
				}
			}
			if len(c.durable["dlg_source"].PendingDeliveries) != 1 {
				t.Fatal("source obligation lost")
			}
			if fault == "fold-read failure" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(path+".saved", path); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	t.Run("pending acceptance order and durable acknowledgment", func(t *testing.T) {
		s, path := notificationReceiverForTest(t, nil)
		for _, id := range []string{"notification-z", "notification-a"} {
			item := n
			item.ID = id
			if err := s.enqueueNotification(item); err != nil {
				t.Fatal(err)
			}
		}
		assertNotificationPending(t, s, []string{"notification-z", "notification-a"})
		if err := s.ackNotifications([]string{"notification-z"}); err != nil {
			t.Fatal(err)
		}
		assertNotificationPending(t, s, []string{"notification-a"})
		entries := notificationRawEntries(t, path)
		var resolved []string
		for _, entry := range entries {
			if entry.Turn.AttentionResolution != nil {
				resolved = append(resolved, entry.Turn.AttentionResolution.AttentionID)
			}
		}
		if !reflect.DeepEqual(resolved, []string{"notification-z"}) {
			t.Fatalf("raw resolutions = %v", resolved)
		}
	})
	for _, route := range []string{"mutation", "pump"} {
		t.Run("failed target does not block unrelated target/"+route, func(t *testing.T) {
			fs := &notificationFaultFS{Fs: afero.NewOsFs()}
			root, _ := notificationReceiverForTest(t, fs)
			c := root.delegateController
			seedDelegateControllerIdle(t, c, "dlg_owner", "")
			ownerID := c.durable["dlg_owner"].Descriptor.ChildSessionID
			ownerPath := transcriptPath(c.stateDir, ownerID)
			writer, err := transcript.NewWriter(ownerPath, transcript.Header{SessionID: ownerID})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = writer.Close() })
			owner := &Session{id: ownerID, stateDir: c.stateDir, delegateController: c, owningDelegateID: "dlg_owner"}
			owner.attachTranscript(writer)
			c.live["dlg_owner"] = &delegateLiveState{runtime: owner}
			seedDelegateControllerIdle(t, c, "dlg_bad", "")
			seedDelegateControllerIdle(t, c, "dlg_good", "dlg_owner")
			badLease, _ := startDelegateDeliveryGeneration(t, c, "dlg_bad", false)
			bad := finishDelegateDeliveryGeneration(t, c, badLease, "payload-bad")
			seedDelegateControllerDelivery(t, c, "dlg_good")
			good := c.ReplayDeliveries()
			fs.writeErr = syscall.ENOSPC
			pending := append(bad.deliveries, good...)
			if route == "pump" {
				root.pendingDelegateDeliveries = pending
				err = root.flushPendingDelegateDeliveries()
			} else {
				err = root.executeDelegateMutationPlans(delegateMutationPlans{deliveries: pending})
			}
			if !errors.Is(err, syscall.ENOSPC) {
				t.Fatalf("delivery error = %v", err)
			}
			if len(c.durable["dlg_bad"].PendingDeliveries) != 1 || len(c.durable["dlg_good"].PendingDeliveries) != 0 {
				t.Fatal("unrelated delivery blocked or failed source acknowledged")
			}
			entries := notificationRawEntries(t, ownerPath)
			if len(entries) != 1 || entries[0].Turn.AttentionID != "delegate:dlg_good/delivery/1" {
				t.Fatalf("unrelated raw records = %#v", entries)
			}
		})
	}
}

func notificationReceiverForTest(t *testing.T, fs afero.Fs) (*Session, string) {
	t.Helper()
	c, _ := newDelegateControllerTestHarness(t, 4, 2)
	path := transcriptPath(c.stateDir, c.rootSessionID)
	if fs == nil {
		fs = afero.NewOsFs()
	}
	writer, err := transcript.NewWriterWithFS(fs, path, transcript.Header{SessionID: c.rootSessionID})
	if err != nil {
		t.Fatal(err)
	}
	s := &Session{id: c.rootSessionID, stateDir: c.stateDir, delegateController: c}
	s.attachTranscript(writer)
	c.rootRuntime = s
	t.Cleanup(func() { _ = s.attachedTranscript().Close(); _ = writer.Close() })
	return s, path
}

func notificationRawEntries(t *testing.T, path string) []transcript.Entry {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), transcript.DefaultMaxLineBytes)
	if !scanner.Scan() {
		t.Fatal("missing raw header")
	}
	var entries []transcript.Entry
	for scanner.Scan() {
		var entry transcript.Entry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return entries
}

func assertNotificationRecords(t *testing.T, path, id string, message llm.Message, count int) {
	t.Helper()
	found := 0
	for _, entry := range notificationRawEntries(t, path) {
		if entry.Turn.AttentionID != id {
			continue
		}
		found++
		if entry.Turn.Kind != schema.TurnSteering || !reflect.DeepEqual(entry.Turn.Message, message) {
			t.Fatalf("raw notification = %#v", entry.Turn)
		}
	}
	if found != count {
		t.Fatalf("raw notification records = %d, want %d", found, count)
	}
}

func assertNotificationProvenance(t *testing.T, path string, want *provenance.Causal) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Read the persisted JSON key independently of both the fold and Turn's
	// codec, so a parser/encoder pair cannot hide a missing on-disk field.
	lines := bytes.SplitN(data, []byte("\n"), 3)
	if len(lines) < 3 {
		t.Fatal("missing raw notification entry")
	}
	var raw struct {
		Turn struct {
			Provenance *provenance.Causal `json:"notification_provenance"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(lines[1], &raw); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(raw.Turn.Provenance, want) {
		t.Fatalf("persisted provenance = %#v, want %#v", raw.Turn.Provenance, want)
	}
}

func assertNotificationPending(t *testing.T, s *Session, want []string) {
	t.Helper()
	ids, err := s.pendingNotificationIDs()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ids, want) {
		t.Fatalf("pending = %v, want %v", ids, want)
	}
	pending, err := s.hasUnackedNotifications()
	if err != nil || pending != (len(want) != 0) {
		t.Fatalf("unacked = %t, %v", pending, err)
	}
}

type notificationFaultFS struct {
	afero.Fs
	writeErr, syncErr error
}

func (fs *notificationFaultFS) Create(name string) (afero.File, error) {
	file, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return &notificationFaultFile{File: file, fs: fs}, nil
}

type notificationFaultFile struct {
	afero.File
	fs *notificationFaultFS
}

func (file *notificationFaultFile) Write(p []byte) (int, error) {
	if file.fs.writeErr != nil {
		return 0, file.fs.writeErr
	}
	return file.File.Write(p)
}
func (file *notificationFaultFile) Sync() error {
	if file.fs.syncErr != nil {
		return file.fs.syncErr
	}
	return file.File.Sync()
}

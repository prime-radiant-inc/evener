package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func producerSourceEvents(t *testing.T, jm *jobManager, kind jobstore.EventKind) []jobstore.Event {
	t.Helper()
	f, err := os.Open(filepath.Join(jm.dir, "jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var found []jobstore.Event
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		var e jobstore.Event
		if err := json.Unmarshal(scan.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		if e.Kind == kind {
			found = append(found, e)
		}
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	return found
}

// These tests read canonical receiver records independently of source folds.
func producerRecordCount(t *testing.T, s *Session) int {
	t.Helper()
	count := 0
	for _, e := range notificationRawEntries(t, transcriptPath(s.stateDir, s.ID())) {
		if e.Turn.AttentionID != "" {
			count++
		}
	}
	return count
}

func producerTimer(t *testing.T, s *Session, send bool) (watchKey, *watchConfig) {
	t.Helper()
	jm := s.jobManager
	jm.clock = agenttest.NewFakeClockAt(time.Unix(1700000000, 0))
	a := watchArgs{Operation: "create", Source: "self", Target: runtimeMessageAliasCaller, AfterSeconds: 60, Note: "payload-5a9e"}
	if send {
		a.Send = &watchSendArgs{To: runtimeMessageAliasCaller, Message: "payload-8d2f"}
	}
	result, err := jm.configureWatch(a)
	if err != nil {
		t.Fatal(err)
	}
	jm.mu.Lock()
	key, cfg, ok := jm.watchConfigByIDLocked(result.WatchID)
	jm.mu.Unlock()
	if !ok {
		t.Fatal("timer not registered")
	}
	return key, cfg
}

type producerQueryFaultFS struct {
	afero.Fs
	syncErr, closeErr error
	syncCount         *int
}

func (fs producerQueryFaultFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := fs.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return producerQueryFaultFile{File: file, syncErr: fs.syncErr, closeErr: fs.closeErr, syncCount: fs.syncCount}, nil
}

type producerQueryFaultFile struct {
	afero.File
	syncErr, closeErr error
	syncCount         *int
}

func (file producerQueryFaultFile) Close() error {
	return errors.Join(file.File.Close(), file.closeErr)
}

func (file producerQueryFaultFile) Sync() error {
	if file.syncCount != nil {
		*file.syncCount++
	}
	if file.syncErr != nil {
		return file.syncErr
	}
	return file.File.Sync()
}

func TestNotificationProducers(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"absent then recovery", "closed before custody", "closed before custody sync failure", "closed before custody close failure", "closed before custody missing file", "custody then closed"} {
		t.Run(boundary, func(t *testing.T) {
			if boundary == "custody then closed" {
				testProducerCustodyThenClosed(t)
				return
			}
			s, _ := notificationReceiverForTest(t, nil)
			c := s.delegateController
			const delegateID = "dlg_late"
			const receiverID = "child-dlg_late"
			if boundary != "absent then recovery" {
				seedDelegateControllerIdle(t, c, delegateID, "")
			}
			path := transcriptPath(s.stateDir, receiverID)
			writer, err := transcript.NewWriter(path, transcript.Header{SessionID: receiverID})
			if err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			jm, err := newJobManager(s.stateDir, s.ID(), s.enqueueJobNotificationAndNotify)
			if err != nil {
				t.Fatal(err)
			}
			s.jobManager, jm.retirementOwner, jm.delegateController = jm, s, c
			t.Cleanup(func() { jm.close() })
			if strings.HasPrefix(boundary, "closed before custody") {
				closeDelegateControllerResumability(t, c, delegateID, "permanent test closure")
			}
			switch boundary {
			case "closed before custody sync failure":
				fs := producerQueryFaultFS{Fs: afero.NewOsFs(), syncErr: errors.New("controlled custody query sync failure")}
				c.attentionOpen = func(path, id string) (*transcript.Writer, []transcript.Entry, error) {
					return transcript.OpenWriterForSessionWithFS(fs, path, id)
				}
			case "closed before custody close failure":
				c.attentionOpen = func(path, id string) (*transcript.Writer, []transcript.Entry, error) {
					return transcript.OpenWriterForSessionWithFS(producerQueryFaultFS{Fs: afero.NewOsFs(), closeErr: errors.New("controlled custody query close failure")}, path, id)
				}
			case "closed before custody missing file":
				if err := os.Rename(path, path+".held"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := jm.configureWatch(watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"communicate"}, ReceiverSessionID: receiverID, ReceiverDelegateID: delegateID}); err != nil {
				t.Fatal(err)
			}
			jm.onSessionEvent(events.SessionEvent{SessionID: s.ID(), Kind: events.EventCommunicate, Data: events.CommunicateData{Message: "closure-boundary"}})
			if strings.HasPrefix(boundary, "closed before custody") {
				if boundary != "closed before custody" {
					if len(producerSourceEvents(t, jm, jobstore.EventWatchSendDropped)) != 0 {
						t.Fatal("query failure treated as permanent absence")
					}
					c.attentionOpen = nil
					if boundary == "closed before custody missing file" {
						if err := os.Rename(path+".held", path); err != nil {
							t.Fatal(err)
						}
					}
					jm.dispatchProducedWatchSends()
				}
				if len(notificationRawEntries(t, path)) != 0 || len(producerSourceEvents(t, jm, jobstore.EventWatchSendDropped)) != 1 {
					t.Fatal("unaccepted closed destination must have zero custody and one durable drop")
				}
				return
			}
			if len(producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)) != 0 || len(producerSourceEvents(t, jm, jobstore.EventWatchSendDropped)) != 0 {
				t.Fatal("uncertain source must remain pending")
			}
			if boundary == "absent then recovery" {
				if len(notificationRawEntries(t, path)) != 0 {
					t.Fatal("missing member accepted custody")
				}
				seedDelegateControllerIdle(t, c, delegateID, "")
			}
			jm.dispatchProducedWatchSends()
			jm.dispatchProducedWatchSends()
			if len(notificationRawEntries(t, path)) != 1 || len(producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)) != 1 || len(producerSourceEvents(t, jm, jobstore.EventWatchSendDropped)) != 0 {
				t.Fatal("recovery must settle one custody without drop or duplicate")
			}
			t.Log("receiver records=1, source delivered=1, dropped=0 after real recovery")
		})
	}
	t.Run("system notice survives terminal communicate", func(t *testing.T) {
		adapter := newMessagingAdapter()
		s := newMessagingRoot(t, adapter, nil)
		entered, gate := make(chan struct{}), make(chan struct{})
		release := sync.OnceFunc(func() { close(gate) })
		defer release()
		adapter.script("root", func(llm.Request) llm.Response {
			if !s.enqueueSystemNotification("terminal-notice-5") {
				t.Error("system notice rejected")
			}
			return toolCallResponse(communicateCallArgs("terminal", map[string]any{"message": "done", "end_turn": true}))
		}, func(llm.Request) llm.Response {
			close(entered)
			<-gate
			return toolCallResponse(communicateCallArgs("observed", map[string]any{"message": "observed", "end_turn": true}))
		})
		done := make(chan error, 1)
		go func() { _, err := s.ProcessInput(context.Background(), "start", nil); done <- err }()
		awaitSignal(t, entered, "next provider request for system notice")
		ids, err := s.pendingNotificationIDs()
		if err != nil || len(ids) != 1 || producerRecordCount(t, s) != 1 || len(adapter.requestsFor("root")) != 2 {
			t.Fatalf("after terminal pending=%v err=%v records=%d requests=%d", ids, err, producerRecordCount(t, s), len(adapter.requestsFor("root")))
		}
		// A source retry retains the original identity and cannot create a second notice.
		if err := s.enqueueSystemNotificationFromSource(ids[0], "terminal-notice-5"); err != nil {
			t.Fatal(err)
		}
		if producerRecordCount(t, s) != 1 {
			t.Fatal("source retry duplicated canonical notice")
		}
		release()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if ids, err := s.pendingNotificationIDs(); err != nil || len(ids) != 0 {
			t.Fatalf("after next request pending=%v err=%v", ids, err)
		}
		t.Log("terminal result leaves receiver=1 pending=1 during next request, identity retry receiver=1, next successful response pending=0")
	})
	t.Run("observer update readiness report", func(t *testing.T) {
		adapter := newMessagingAdapter("OBSERVER-5")
		gate := make(chan struct{})
		reportEntered := make(chan struct{})
		release := sync.OnceFunc(func() { close(gate) })
		defer release()
		adapter.script("root", func(llm.Request) llm.Response {
			return delegateToolCall("observer", map[string]any{"prompt": "OBSERVER-5", "watch_parent": true})
		}, func(llm.Request) llm.Response {
			<-gate
			return jobListResponse("safe-boundary")
		})
		adapter.script("OBSERVER-5", func(llm.Request) llm.Response {
			return updateResponse("update", "payload-update")
		}, func(llm.Request) llm.Response {
			return toolCallResponse(communicateCallArgs("ready", map[string]any{"message": "payload-ready", "end_turn": false, "readiness": "ready"}))
		}, func(llm.Request) llm.Response {
			close(reportEntered)
			return toolCallResponse(communicateCallArgs("report", map[string]any{"message": "payload-report", "end_turn": true}))
		})
		s := newMessagingRoot(t, adapter, nil)
		done := make(chan error, 1)
		go func() { _, err := s.ProcessInput(context.Background(), "start observer", nil); done <- err }()
		awaitSignal(t, reportEntered, "observer report request")
		s.subagents.mu.Lock()
		var child *subagent
		for _, candidate := range s.subagents.subs {
			child = candidate
		}
		s.subagents.mu.Unlock()
		if child == nil {
			t.Fatal("observer runtime missing")
		}
		waitSubagentRun(t, child)
		if len(adapter.requestsFor("OBSERVER-5")) != 3 {
			t.Fatal("observer did not use three real producer turns")
		}
		release()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if producerRecordCount(t, s) != 3 {
			t.Fatal("observer packets duplicated")
		}
		for _, payload := range []string{"payload-update", "payload-ready", "payload-report"} {
			count := 0
			for _, entry := range notificationRawEntries(t, transcriptPath(s.stateDir, s.ID())) {
				if entry.Turn.AttentionID != "" && strings.Contains(entry.Turn.Message.Text(), payload) {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("observer payload %q canonical records=%d", payload, count)
			}
		}
		if ids, err := s.pendingNotificationIDs(); err != nil || len(ids) != 0 {
			t.Fatalf("observer pending=%v, %v", ids, err)
		}
		t.Log("observer provider requests=3, receiver records=3, successful holder boundary observes all 3")
	})
	t.Run("running receiver", func(t *testing.T) {
		adapter := newMessagingAdapter()
		entered, release := make(chan struct{}), make(chan struct{})
		adapter.script("root", func(llm.Request) llm.Response {
			close(entered)
			<-release
			return jobListResponse("boundary")
		})
		s := newMessagingRoot(t, adapter, nil)
		done := make(chan error, 1)
		go func() { _, err := s.ProcessInput(context.Background(), "start", nil); done <- err }()
		<-entered
		key, cfg := producerTimer(t, s, false)
		s.jobManager.fireProgressTick(key, cfg)
		count := producerRecordCount(t, s)
		pending, err := s.pendingNotificationIDs()
		close(release)
		if doneErr := <-done; doneErr != nil {
			t.Fatal(doneErr)
		}
		if count != 1 || err != nil || len(pending) != 1 {
			t.Fatalf("in-flight custody records=%d pending=%v err=%v", count, pending, err)
		}
		if ids, err := s.pendingNotificationIDs(); err != nil || len(ids) != 0 {
			t.Fatalf("successful boundary pending=%v, %v", ids, err)
		}
		if got := len(adapter.requestsFor("root")); got != 2 {
			t.Fatalf("provider requests=%d, want 2", got)
		}
		t.Log("during provider request receiver=1, pending=1; after safe next request provider requests=2, pending=0")
	})
	t.Run("cold receiver without resident runtime", func(t *testing.T) {
		s, _ := notificationReceiverForTest(t, nil)
		c := s.delegateController
		seedDelegateControllerIdle(t, c, "dlg_cold", "")
		c.mu.Lock()
		receiverID := c.durable["dlg_cold"].Descriptor.ChildSessionID
		c.mu.Unlock()
		path := transcriptPath(c.stateDir, receiverID)
		writer, err := transcript.NewWriter(path, transcript.Header{SessionID: receiverID})
		if err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		jm, err := newJobManager(s.stateDir, s.ID(), s.enqueueJobNotificationAndNotify)
		if err != nil {
			t.Fatal(err)
		}
		s.jobManager, jm.retirementOwner, jm.delegateController = jm, s, c
		t.Cleanup(func() { jm.close() })
		jm.clock = agenttest.NewFakeClockAt(time.Unix(1700000000, 0))
		result, err := jm.configureWatch(watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"communicate"}, ReceiverSessionID: receiverID, ReceiverDelegateID: "dlg_cold"})
		if err != nil {
			t.Fatal(err)
		}
		_ = result
		jm.onSessionEvent(events.SessionEvent{SessionID: s.ID(), Kind: events.EventCommunicate, Data: events.CommunicateData{Message: "cold payload"}})
		entries := notificationRawEntries(t, path)
		if len(entries) != 1 || entries[0].Turn.AttentionID == "" {
			t.Fatalf("cold receiver entries=%#v", entries)
		}
		if producerRecordCount(t, s) != 0 || len(producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)) != 1 {
			t.Fatal("cold delivery lost receiver identity or failed custody ack")
		}
		t.Log("cold receiver records=1, source delivered=1, holder records=0, resident receiver absent")
	})
	t.Run("retry after custody before source ack", func(t *testing.T) {
		s, _ := notificationReceiverForTest(t, nil)
		jm, err := newJobManager(s.stateDir, s.ID(), s.enqueueJobNotificationAndNotify)
		if err != nil {
			t.Fatal(err)
		}
		s.jobManager, jm.retirementOwner = jm, s
		jm.delegateController = s.delegateController
		t.Cleanup(func() { jm.close() })
		key, cfg := producerTimer(t, s, false)
		s.SetNotifyFunc(func() { _ = jm.store.Close() })
		jm.fireProgressTick(key, cfg)
		s.SetNotifyFunc(nil)
		if producerRecordCount(t, s) != 1 || len(producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)) != 0 {
			t.Fatal("custody/source-ack boundary not reached")
		}
		restored, err := newJobManager(s.stateDir, s.ID(), s.enqueueJobNotificationAndNotify)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { restored.close() })
		s.jobManager, restored.retirementOwner = restored, s
		restored.delegateController = s.delegateController
		restored.dispatchProducedWatchSends()
		if producerRecordCount(t, s) != 1 || len(producerSourceEvents(t, restored, jobstore.EventWatchSendDelivered)) != 1 {
			t.Fatal("source-only retry duplicated custody or failed acknowledgment")
		}
		t.Log("receiver records=1 before and after reopen, source delivered=0 then 1")
	})
	t.Run("accepted frame immutable during next firing", func(t *testing.T) {
		s := newMessagingRoot(t, newMessagingAdapter(), nil)
		jm := s.jobManager
		if _, err := jm.configureWatch(watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"communicate"}}); err != nil {
			t.Fatal(err)
		}
		var acceptedID, acceptedText string
		s.SetNotifyFunc(func() {
			s.SetNotifyFunc(nil)
			for _, entry := range notificationRawEntries(t, transcriptPath(s.stateDir, s.ID())) {
				if entry.Turn.AttentionID != "" {
					acceptedID, acceptedText = entry.Turn.AttentionID, entry.Turn.Message.Text()
				}
			}
			jm.onSessionEvent(events.SessionEvent{SessionID: s.ID(), Kind: events.EventCommunicate, Data: events.CommunicateData{Message: "immutable-second"}})
		})
		jm.onSessionEvent(events.SessionEvent{SessionID: s.ID(), Kind: events.EventCommunicate, Data: events.CommunicateData{Message: "immutable-first"}})
		s.SetNotifyFunc(nil)
		var ids []string
		for _, entry := range notificationRawEntries(t, transcriptPath(s.stateDir, s.ID())) {
			if entry.Turn.AttentionID != "" {
				ids = append(ids, entry.Turn.AttentionID)
				if entry.Turn.AttentionID == acceptedID && entry.Turn.Message.Text() != acceptedText {
					t.Fatalf("accepted frame content changed: %s", entry.Turn.Message.Text())
				}
			}
		}
		if acceptedID == "" || len(ids) != 2 || ids[0] != acceptedID || ids[0] == ids[1] || len(producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)) != 2 {
			t.Fatalf("accepted identities=%v source delivered=%d", ids, len(producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)))
		}
	})
	for _, kind := range []string{"timer", "session event", "output match", "shell exit", "explicit send", "system"} {
		t.Run(kind, func(t *testing.T) {
			adapter := newMessagingAdapter()
			s := newMessagingRoot(t, adapter, nil)
			jm := s.jobManager
			var eventWatchID string
			switch kind {
			case "timer", "explicit send":
				key, cfg := producerTimer(t, s, kind == "explicit send")
				jm.fireProgressTick(key, cfg)
			case "session event":
				result, err := jm.configureWatch(watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"communicate"}})
				if err != nil {
					t.Fatal(err)
				}
				eventWatchID = result.WatchID
				s.emit(events.EventCommunicate, events.CommunicateData{Message: "payload-f1c3"})
			case "output match", "shell exit":
				rec, err := jm.createShell(createShellOpts{Command: "payload-42d9"})
				if err != nil {
					t.Fatal(err)
				}
				if kind == "output match" {
					if _, err := jm.configureWatch(watchArgs{Target: rec.JobID, OutputMatch: "payload-7b1e"}); err != nil {
						t.Fatal(err)
					}
					jm.feedJobOutput(rec.JobID, []byte("payload-7b1e"), 12)
				} else {
					if err := jm.finalize(rec.JobID, jobstore.StatusCompleted, "", nil); err != nil {
						t.Fatal(err)
					}
				}
			case "system":
				if !s.enqueueSystemNotification("payload-a19c") {
					t.Fatal("notice rejected")
				}
			}
			if got := producerRecordCount(t, s); got != 1 {
				t.Fatalf("canonical receiver records = %d, want 1 before holder execution", got)
			}
			ids, err := s.pendingNotificationIDs()
			if err != nil || len(ids) != 1 {
				t.Fatalf("pending IDs = %v, %v", ids, err)
			}
			if len(adapter.requestsFor("root")) != 0 {
				t.Fatal("transport executed holder")
			}
			for _, entry := range notificationRawEntries(t, transcriptPath(s.stateDir, s.ID())) {
				if len(entry.Turn.ObservedNotificationIDs) != 0 {
					t.Fatal("transport/source ack fabricated model observation")
				}
			}
			if eventWatchID != "" {
				if _, err := jm.clearWatchByID(eventWatchID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
				t.Fatal(err)
			}
			if got := len(adapter.requestsFor("root")); got != 1 {
				t.Fatalf("provider requests = %d, want 1", got)
			}
			pending, err := s.pendingNotificationIDs()
			if err != nil || len(pending) != 0 {
				t.Fatalf("after successful request pending = %v, %v", pending, err)
			}
			observed := 0
			for _, entry := range notificationRawEntries(t, transcriptPath(s.stateDir, s.ID())) {
				for _, id := range entry.Turn.ObservedNotificationIDs {
					if id != ids[0] {
						t.Fatalf("assistant observed unrelated ID %q", id)
					}
					observed++
				}
			}
			if observed != 1 {
				t.Fatalf("persisted assistant observation count=%d", observed)
			}
			t.Logf("canonical records=1, provider requests=1, observed pending=0")
		})
	}
}

func testProducerCustodyThenClosed(t *testing.T) {
	adapter := newMessagingAdapter("CUSTODY-CLOSE-731a")
	root := newMessagingRoot(t, adapter, nil)
	observed := make(chan struct{})
	adapter.script("root", func(llm.Request) llm.Response {
		return delegateToolResponse("custody-child", "CUSTODY-CLOSE-731a")
	}, func(llm.Request) llm.Response { return finalResponse("root launched child") })
	adapter.script("CUSTODY-CLOSE-731a", func(llm.Request) llm.Response {
		return finalResponse("initial child report")
	}, func(req llm.Request) llm.Response {
		observationRequest(t, req, []string{"closure-boundary-731a"}, nil)
		close(observed)
		return finalResponse("child observed closure frame")
	})
	if _, err := root.ProcessInput(context.Background(), "create a real custody receiver", nil); err != nil {
		t.Fatal(err)
	}
	child := waitForSoleChildIdle(t, root)
	waitForStableSupervisionRun(t, root, child.ID())
	// Isolate this source's store from the real session's shell/watch manager.
	// Its delivery still uses the controller and exact retained child callback.
	sourceDir := t.TempDir()
	jm, err := newJobManager(sourceDir, root.ID(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { jm.close() })
	jm.retirementOwner, jm.delegateController = root, root.delegateController
	ackError := make(chan error, 1)
	appendEvent := jm.appendEvent
	jm.appendEvent = func(event jobstore.Event) error {
		if event.Kind == jobstore.EventWatchSendDelivered {
			accepted := 0
			for _, entry := range notificationRawEntries(t, child.TranscriptPath()) {
				if entry.Turn.AttentionID == stableWatchAttentionID(*event.WatchSend) {
					accepted++
				}
			}
			if accepted != 1 {
				t.Errorf("source acknowledgement preceded exact receiver custody: accepted=%d", accepted)
			}
			if err := jm.store.Close(); err != nil {
				t.Errorf("close source store: %v", err)
			}
			err := appendEvent(event)
			select {
			case ackError <- err:
			default:
			}
			return err
		}
		return appendEvent(event)
	}
	if _, err := jm.configureWatch(watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"communicate"}, Note: "closure-boundary-731a", ReceiverSessionID: child.ID(), ReceiverDelegateID: child.owningDelegateID}); err != nil {
		t.Fatal(err)
	}
	jm.onSessionEvent(events.SessionEvent{SessionID: root.ID(), Kind: events.EventCommunicate, Data: events.CommunicateData{Message: "closure-boundary-731a"}})
	if err := <-ackError; err == nil {
		t.Fatal("closed source store unexpectedly acknowledged custody")
	}
	awaitSignal(t, observed, "real child successful request")
	waitForStableSupervisionRun(t, root, child.ID())
	pending := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
	if len(pending) != 1 || len(producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)) != 0 || len(producerSourceEvents(t, jm, jobstore.EventWatchSendDropped)) != 0 {
		t.Fatal("failed acknowledgement did not retain its exact source obligation")
	}
	id := stableWatchAttentionID(*pending[0].WatchSend)
	requireOwnedDrainAttentionObservedOnce(t, child, id)
	before := len(adapter.requestsFor("CUSTODY-CLOSE-731a"))
	closeDelegateControllerResumability(t, root.delegateController, child.owningDelegateID, "closed after custody")
	restored, err := newJobManager(sourceDir, root.ID(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { restored.close() })
	restored.retirementOwner, restored.delegateController = root, root.delegateController
	restored.dispatchProducedWatchSends()
	restored.dispatchProducedWatchSends()
	delivered := producerSourceEvents(t, restored, jobstore.EventWatchSendDelivered)
	if len(delivered) != 1 || stableWatchAttentionID(*delivered[0].WatchSend) != id || len(producerSourceEvents(t, restored, jobstore.EventWatchSendDropped)) != 0 {
		t.Fatal("closed accepted receiver did not settle its exact source once without drops")
	}
	requireOwnedDrainAttentionObservedOnce(t, child, id)
	if len(adapter.requestsFor("CUSTODY-CLOSE-731a")) != before || before != 2 {
		t.Fatal("source replay caused a second provider observation")
	}
}

func TestAcceptedWatchNotificationSurvivesCancellation(t *testing.T) {
	t.Parallel()
	for _, afterAcceptance := range []bool{false, true} {
		t.Run(map[bool]string{false: "during receiver sync", true: "after acceptance"}[afterAcceptance], func(t *testing.T) {
			fs := newAttentionSyncBarrierFS()
			s, _ := notificationReceiverForTest(t, fs)
			jm, err := newJobManager(s.stateDir, s.ID(), s.enqueueJobNotificationAndNotify)
			if err != nil {
				t.Fatal(err)
			}
			s.jobManager = jm
			jm.retirementOwner = s
			t.Cleanup(func() { jm.close() })
			jm.clock = agenttest.NewFakeClockAt(time.Unix(1700000000, 0))
			result, err := jm.configureWatch(watchArgs{Operation: "create", Source: "self", Target: runtimeMessageAliasCaller, RepeatSeconds: 60, Note: "retained-clear-payload"})
			if err != nil {
				t.Fatal(err)
			}
			jm.mu.Lock()
			key, cfg, live := jm.watchConfigByIDLocked(result.WatchID)
			jm.mu.Unlock()
			if !live {
				t.Fatal("repeating registration missing")
			}
			if afterAcceptance {
				jm.fireProgressTick(key, cfg)
				if got := producerRecordCount(t, s); got != 1 {
					t.Fatalf("canonical records before clear = %d", got)
				}
			} else {
				fs.arm()
				released := false
				defer func() {
					if !released {
						fs.release()
					}
				}()
				done := make(chan struct{})
				go func() { defer close(done); jm.fireProgressTick(key, cfg) }()
				select {
				case <-fs.syncEntered:
				case <-done:
					t.Fatal("callback returned without canonical receiver sync")
				}
				if len(producerSourceEvents(t, jm, jobstore.EventWatchSendPending)) != 1 || len(producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)) != 0 {
					t.Fatal("source ack preceded receiver Sync")
				}
				if _, err := jm.clearWatchByID(cfg.watchID); err != nil {
					t.Fatal(err)
				}
				fs.release()
				released = true
				<-done
			}
			if _, err := jm.clearWatchByID(cfg.watchID); err != nil {
				t.Fatal(err)
			}
			if got := producerRecordCount(t, s); got != 1 {
				t.Fatalf("canonical records after clear = %d, want 1", got)
			}
			jm.fireProgressTick(key, cfg)
			if got := producerRecordCount(t, s); got != 1 {
				t.Fatalf("late callback produced records = %d", got)
			}
			if len(producerSourceEvents(t, jm, jobstore.EventWatchSendPending)) != 1 || len(producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)) != 1 || len(producerSourceEvents(t, jm, jobstore.EventWatchCleared)) != 1 || len(producerSourceEvents(t, jm, jobstore.EventWatchSendDropped)) != 0 {
				t.Fatal("clear lost produced frame or allowed another source firing")
			}
			ids, err := s.pendingNotificationIDs()
			if err != nil || len(ids) != 1 {
				t.Fatalf("clear lost accepted message: %v, %v", ids, err)
			}
		})
	}
}

func TestWatchProductionSurvivesRestart(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"session event", "output match"} {
		t.Run(kind+" source failure", func(t *testing.T) {
			s := newMessagingRoot(t, newMessagingAdapter(), nil)
			jm := s.jobManager
			a := watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"communicate"}}
			var jobID string
			if kind == "output match" {
				rec, err := jm.createShell(createShellOpts{Command: "payload-42d9"})
				if err != nil {
					t.Fatal(err)
				}
				jobID = rec.JobID
				a = watchArgs{Target: jobID, OutputMatch: "payload-7b1e"}
			}
			result, err := jm.configureWatch(a)
			if err != nil {
				t.Fatal(err)
			}
			jm.mu.Lock()
			_, cfg, _ := jm.watchConfigByIDLocked(result.WatchID)
			jm.mu.Unlock()
			if err := jm.store.Close(); err != nil {
				t.Fatal(err)
			}
			if kind == "session event" {
				s.emit(events.EventCommunicate, events.CommunicateData{Message: "payload-f1c3"})
			} else {
				jm.feedJobOutput(jobID, []byte("payload-7b1e"), 12)
			}
			if cfg.conditionFires != 0 || cfg.deliveries != 0 {
				t.Fatalf("failed production spent budget: fires=%d deliveries=%d", cfg.conditionFires, cfg.deliveries)
			}
			reopened, err := jobstore.Open(filepath.Join(jm.dir, "jobs.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			jm.store, jm.appendEvent, jm.appendEvents = reopened, reopened.Append, reopened.AppendBatch
			jm.dispatchProducedWatchSends()
			if producerRecordCount(t, s) != 1 || cfg.conditionFires != 1 || cfg.deliveries != 1 {
				t.Fatalf("recovered callback receiver=%d fires=%d deliveries=%d", producerRecordCount(t, s), cfg.conditionFires, cfg.deliveries)
			}
		})
	}
	for _, send := range []bool{false, true} {
		t.Run(map[bool]string{false: "callback", true: "explicit send"}[send]+" reopen produced frames", func(t *testing.T) {
			fs := &notificationFaultFS{Fs: afero.NewOsFs()}
			s, path := notificationReceiverForTest(t, fs)
			jm, err := newJobManager(s.stateDir, s.ID(), s.enqueueJobNotificationAndNotify)
			if err != nil {
				t.Fatal(err)
			}
			s.jobManager, jm.retirementOwner = jm, s
			jm.delegateController = s.delegateController
			t.Cleanup(func() { jm.close() })
			fs.writeErr = errors.New("receiver unavailable")
			for range 2 {
				key, cfg := producerTimer(t, s, send)
				jm.fireProgressTick(key, cfg)
				if _, err := jm.clearWatchByID(cfg.watchID); err != nil {
					t.Fatal(err)
				}
			}
			pending := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
			if len(pending) != 2 || len(producerSourceEvents(t, jm, jobstore.EventWatchCleared)) != 2 || producerRecordCount(t, s) != 0 {
				t.Fatalf("before reopen pending=%d, ended=%d, receiver=%d", len(pending), len(producerSourceEvents(t, jm, jobstore.EventWatchCleared)), producerRecordCount(t, s))
			}
			if pending[0].WatchSend.DeliveryID == pending[1].WatchSend.DeliveryID || pending[0].WatchSend.TimerSlot == pending[1].WatchSend.TimerSlot {
				t.Fatal("separate timer registrations lost source identity")
			}
			if err := jm.closeStoreOnly(); err != nil {
				t.Fatal(err)
			}
			if err := s.attachedTranscript().Close(); err != nil {
				t.Fatal(err)
			}
			writer, _, err := transcript.OpenWriterForSession(path, s.ID())
			if err != nil {
				t.Fatal(err)
			}
			s.mu.Lock()
			s.transcript, s.transcriptReady = writer, true
			s.mu.Unlock()
			restored, err := newJobManager(s.stateDir, s.ID(), s.enqueueJobNotificationAndNotify)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { restored.close() })
			s.jobManager, restored.retirementOwner = restored, s
			restored.delegateController = s.delegateController
			restored.dispatchProducedWatchSends()
			restored.dispatchProducedWatchSends()
			if got := producerRecordCount(t, s); got != 2 {
				t.Fatalf("reopened receiver records=%d, want 2", got)
			}
			acks := producerSourceEvents(t, restored, jobstore.EventWatchSendDelivered)
			if len(acks) != 2 {
				t.Fatalf("source acks=%d, want 2", len(acks))
			}
			for _, p := range pending {
				matches := 0
				for _, ack := range acks {
					if p.WatchSend.DeliveryID == ack.WatchSend.DeliveryID && p.WatchSend.TimerSlot == ack.WatchSend.TimerSlot {
						matches++
					}
				}
				if matches != 1 {
					t.Fatal("replay changed or duplicated immutable delivery identity")
				}
			}
			t.Log("before reopen source pending=2, ended=2, receiver=0; after reopen receiver=2, source delivered=2, second pump adds 0")
		})
		t.Run(map[bool]string{false: "callback", true: "explicit send"}[send], func(t *testing.T) {
			s := newMessagingRoot(t, newMessagingAdapter(), nil)
			jm := s.jobManager
			key, cfg := producerTimer(t, s, send)
			// A real closed source descriptor fails the append at the filesystem boundary.
			if err := jm.store.Close(); err != nil {
				t.Fatal(err)
			}
			if !jm.fireProgressTick(key, cfg) {
				t.Error("failed source append consumed one-shot")
			}
			jm.mu.Lock()
			_, _, live := jm.watchConfigByIDLocked(cfg.watchID)
			jm.mu.Unlock()
			if !live || cfg.firedPendingEnd || cfg.deliveries != 0 {
				t.Fatalf("failed production spent firing: live=%t fired=%t deliveries=%d", live, cfg.firedPendingEnd, cfg.deliveries)
			}
			if got := producerRecordCount(t, s); got != 0 {
				t.Fatalf("receiver accepted unproduced frame: %d", got)
			}
			reopened, err := jobstore.Open(filepath.Join(jm.dir, "jobs.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			jm.store, jm.appendEvent, jm.appendEvents = reopened, reopened.Append, reopened.AppendBatch
			jm.fireProgressTick(key, cfg)
			if producerRecordCount(t, s) != 1 || len(producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)) != 1 {
				t.Fatal("reopened source did not retry exactly one firing")
			}
		})
	}
}

func reopenProducerSource(t *testing.T, jm *jobManager) {
	t.Helper()
	reopened, err := jobstore.Open(filepath.Join(jm.dir, "jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	jm.store, jm.appendEvent, jm.appendEvents = reopened, reopened.Append, reopened.AppendBatch
}

func TestCallbackOnlySettledChainTripsRunawayFuse(t *testing.T) {
	for _, send := range []bool{false, true} {
		for _, sourceFailure := range []bool{false, true} {
			t.Run(fmt.Sprintf("send=%t/source failure=%t", send, sourceFailure), func(t *testing.T) {
				s := newMessagingRoot(t, newMessagingAdapter(), nil)
				jm := s.jobManager
				a := watchArgs{Source: "self", Target: runtimeMessageAliasCaller, Events: []string{"communicate"}}
				if send {
					a.Send = &watchSendArgs{To: runtimeMessageAliasCaller, Message: "causal send"}
				}
				if _, err := jm.configureWatch(a); err != nil {
					t.Fatal(err)
				}
				var cause *provenance.Causal
				for prior := 0; prior <= 8; prior++ {
					if jm.hasPendingWatchSends() {
						t.Fatal("earlier delivery has not settled before the next causal event")
					}
					if sourceFailure && prior == 8 {
						if err := jm.store.Close(); err != nil {
							t.Fatal(err)
						}
					}
					jm.onSessionEvent(events.SessionEvent{SessionID: s.ID(), Kind: events.EventCommunicate, Data: events.CommunicateData{Message: "causal callback"}, Provenance: cause})
					if sourceFailure && prior == 8 {
						if producerRecordCount(t, s) != 8 || len(producerSourceEvents(t, jm, jobstore.EventWatchSendPending)) != 8 {
							t.Fatal("source append fault did not hold the ninth frame before custody")
						}
						reopenProducerSource(t, jm)
						jm.dispatchProducedWatchSends() // No new source event.
					}
					pending := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
					last := pending[len(pending)-1].WatchSend
					if len(pending) != prior+1 || last.SelfInfluenceDepth != prior {
						t.Errorf("settled priors=%d pending rows=%d persisted fuse depth=%d", prior, len(pending), last.SelfInfluenceDepth)
					}
					cause = provenance.Clone(last.Provenance)
				}
				drops := producerSourceEvents(t, jm, jobstore.EventWatchSendDropped)
				acks := producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)
				if producerRecordCount(t, s) != 8 || len(acks) != 8 || len(drops) != 1 || drops[0].WatchSend.DiagnosticReason != "runaway" {
					t.Fatalf("fuse receiver=%d acks=%d drops=%d, want 8/8/1 runaway", producerRecordCount(t, s), len(acks), len(drops))
				}
			})
		}
	}
}

func TestColdCustodyQueryCloseOwnsAdmission(t *testing.T) {
	for _, closeFailure := range []bool{false, true} {
		name := "close success"
		if closeFailure {
			name = "close failure"
		}
		t.Run(name, func(t *testing.T) {
			s, _ := notificationReceiverForTest(t, nil)
			c := s.delegateController
			seedDelegateControllerIdle(t, c, "dlg_owner", "")
			id := c.durable["dlg_owner"].Descriptor.ChildSessionID
			path := transcriptPath(s.stateDir, id)
			writeEmptyAttentionTranscript(t, path, id)
			released := make(chan struct{})
			close(released)
			fs := &notificationColdBarrierFS{Fs: afero.NewOsFs(), writeEntered: make(chan struct{}), writeRelease: released, closeEntered: make(chan struct{}), closeRelease: make(chan struct{})}
			unblock := sync.OnceFunc(func() { close(fs.closeRelease) })
			var queryFS afero.Fs = fs
			if closeFailure {
				queryFS = producerQueryFaultFS{Fs: fs, closeErr: errors.New("controlled query Close failure")}
			}
			c.attentionOpen = func(path, id string) (*transcript.Writer, []transcript.Entry, error) {
				return transcript.OpenWriterForSessionWithFS(queryFS, path, id)
			}
			jm, err := newJobManager(s.stateDir, s.ID(), s.enqueueJobNotificationAndNotify)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { jm.close() })
			s.jobManager, jm.retirementOwner, jm.delegateController = jm, s, c
			if _, err := jm.configureWatch(watchArgs{Source: "self", Target: runtimeMessageAliasCaller, Events: []string{"communicate"}, ReceiverSessionID: id, ReceiverDelegateID: "dlg_owner"}); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			go func() {
				jm.onSessionEvent(events.SessionEvent{SessionID: s.ID(), Kind: events.EventCommunicate, Data: events.CommunicateData{Message: "query ownership"}})
				close(done)
			}()
			defer func() { unblock(); <-done }()
			<-fs.closeEntered
			reservation, err := c.ReserveStart(rootDelegateActor(c.rootSessionID), "dlg_owner")
			if err == nil {
				if _, err = c.CommitStart(reservation); err == nil {
					t.Error("start committed while custody query writer Close was held")
				}
			}
			if _, _, err := c.idleDelegateRestoreCommit("dlg_owner"); err == nil {
				t.Error("cold restore admitted while custody query writer Close was held")
			}
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
			plans := reconcile()
			select {
			case <-stop.done:
				t.Error("Stop completed while query Close was held")
			default:
			}
			if len(plans.attention) != 0 {
				t.Error("Stop scheduled a second writer while query Close was held")
			}
			fs.mu.Lock()
			active, peak, opens := fs.active, fs.peak, fs.opens
			fs.mu.Unlock()
			if active != 1 || peak != 1 || opens != 1 {
				t.Fatalf("query writers active/peak/opens=%d/%d/%d, want 1 each", active, peak, opens)
			}
			unblock()
			<-done
			if len(producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)) != 0 || len(producerSourceEvents(t, jm, jobstore.EventWatchSendDropped)) != 0 || !jm.hasPendingWatchSends() || len(notificationRawEntries(t, path)) != 0 {
				t.Fatal("query Close/Stop uncertainty acknowledged, dropped or accepted the frame")
			}
			c.attentionOpen = nil
			for range 3 {
				for _, cleanup := range reconcile().attention {
					if err := c.executeDelegateAttentionCleanup(cleanup); err != nil {
						t.Fatal(err)
					}
				}
			}
			select {
			case <-stop.done:
			default:
				t.Fatal("Stop did not complete after query writer Close")
			}
			if _, _, err := c.idleDelegateRestoreCommit("dlg_owner"); err != nil {
				t.Fatalf("restore remained blocked after query admission release: %v", err)
			}
		})
	}
}

func TestRestoredWatchDispatchVisitsUnavailableRecipientsOnce(t *testing.T) {
	for _, count := range []int{1, 4, 8} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s, _ := notificationReceiverForTest(t, nil)
			c := s.delegateController
			queries, syncs := 0, 0
			queryFS := producerQueryFaultFS{Fs: afero.NewOsFs(), syncErr: errors.New("controlled unavailable cold recipient Sync"), syncCount: &syncs}
			c.attentionOpen = func(path, id string) (*transcript.Writer, []transcript.Entry, error) {
				queries++
				return transcript.OpenWriterForSessionWithFS(queryFS, path, id)
			}
			jm, err := newJobManager(s.stateDir, s.ID(), s.enqueueJobNotificationAndNotify)
			if err != nil {
				t.Fatal(err)
			}
			s.jobManager, jm.retirementOwner, jm.delegateController = jm, s, c
			for i := range count {
				delegateID := fmt.Sprintf("dlg_unavailable%d", i)
				seedDelegateControllerIdle(t, c, delegateID, "")
				id := c.durable[delegateID].Descriptor.ChildSessionID
				writeEmptyAttentionTranscript(t, transcriptPath(s.stateDir, id), id)
				if _, err := jm.configureWatch(watchArgs{Source: "self", Target: runtimeMessageAliasCaller, Events: []string{"communicate"}, ReceiverSessionID: id, ReceiverDelegateID: delegateID}); err != nil {
					t.Fatal(err)
				}
			}
			jm.onSessionEvent(events.SessionEvent{SessionID: s.ID(), Kind: events.EventCommunicate, Data: events.CommunicateData{Message: "restore unavailable batch"}})
			if err := jm.closeStoreOnly(); err != nil {
				t.Fatal(err)
			}
			restored, err := newJobManager(s.stateDir, s.ID(), s.enqueueJobNotificationAndNotify)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { restored.close() })
			s.jobManager, restored.retirementOwner, restored.delegateController = restored, s, c
			queries, syncs = 0, 0
			if err := s.retryRestoredPendingWatchSends(context.Background()); err != nil {
				t.Fatal(err)
			}
			if queries != count || syncs != count {
				t.Errorf("retained=%d delivery/query attempts=%d Sync attempts=%d, want %d each", count, queries, syncs, count)
			}
			queries, sourceFolds := 0, 0
			syncs = 0
			if err := s.retryRestoredPendingWatchSendsFrom(func() (jobstore.WatchSendRecord, error) {
				sourceFolds++
				return restored.store.LoadWatchSends()
			}); err != nil {
				t.Fatal(err)
			}
			if sourceFolds != 1 || queries != count || syncs != count {
				t.Fatalf("retained=%d source folds=%d delivery/query attempts=%d Sync attempts=%d, want 1/%d/%d", count, sourceFolds, queries, syncs, count, count)
			}
			t.Logf("retained=%d source folds=%d delivery/query attempts=%d failed Sync attempts=%d", count, sourceFolds, queries, syncs)
			if got := len(producerSourceEvents(t, restored, jobstore.EventWatchSendDelivered)); got != 0 {
				t.Fatalf("unavailable recipients acknowledged %d frames", got)
			}
		})
	}
}

func TestCommittedSourceAckRetryDoesNotHoldColdRestore(t *testing.T) {
	s, _ := notificationReceiverForTest(t, nil)
	c := s.delegateController
	seedDelegateControllerIdle(t, c, "dlg_owner", "")
	seedDelegateControllerIdle(t, c, "dlg_source", "dlg_owner")
	seedDelegateControllerDelivery(t, c, "dlg_source")
	id := c.durable["dlg_owner"].Descriptor.ChildSessionID
	path := transcriptPath(s.stateDir, id)
	writeEmptyAttentionTranscript(t, path, id)
	plans := c.ReplayDeliveries()
	if len(plans) != 1 {
		t.Fatalf("actual pending source packets=%d, want 1", len(plans))
	}
	if err := c.store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := deliverColdDelegatePacket(plans[0]); err == nil {
		t.Fatal("closed source acknowledged the canonical receiver write")
	}
	if len(notificationRawEntries(t, path)) != 1 || len(c.ReplayDeliveries()) != 1 {
		t.Fatal("source-only retry lost receiver custody or the source obligation")
	}
	if _, _, err := c.idleDelegateRestoreCommit("dlg_owner"); err != nil {
		t.Fatalf("committed receiver custody still holds cold restore: %v", err)
	}
}

type producerNoticeFS struct {
	afero.Fs
	afterSync func()
	writeErr  error
}

func (fs *producerNoticeFS) Create(name string) (afero.File, error) {
	f, err := fs.Fs.Create(name)
	if err != nil {
		return nil, err
	}
	return &producerNoticeFile{File: f, fs: fs}, nil
}

type producerNoticeFile struct {
	afero.File
	fs *producerNoticeFS
}

func (f *producerNoticeFile) Write(p []byte) (int, error) {
	if f.fs.writeErr != nil {
		return 0, f.fs.writeErr
	}
	return f.File.Write(p)
}

func (f *producerNoticeFile) Sync() error {
	if err := f.File.Sync(); err != nil {
		return err
	}
	if hook := f.fs.afterSync; hook != nil {
		f.fs.afterSync = nil
		hook()
	}
	return nil
}

func TestRestartSystemNoticeRetainsSourceIdentity(t *testing.T) {
	for _, settled := range []bool{true, false} {
		t.Run(map[bool]string{true: "fired and settled", false: "fired but unaccepted"}[settled], func(t *testing.T) {
			fs := &producerNoticeFS{Fs: afero.NewOsFs()}
			s, _ := notificationReceiverForTest(t, fs)
			restore := func() *jobManager {
				t.Helper()
				jm, err := newJobManager(s.stateDir, s.ID(), s.enqueueJobNotificationAndNotify)
				if err != nil {
					t.Fatal(err)
				}
				s.jobManager, jm.retirementOwner, jm.delegateController = jm, s, s.delegateController
				jm.notifySystem = s.routeSystemNotification
				return jm
			}
			jm := restore()
			if _, err := jm.configureWatch(watchArgs{Source: "self", Target: runtimeMessageAliasCaller, Events: []string{"communicate"}}); err != nil {
				t.Fatal(err)
			}
			if !settled {
				fs.writeErr = errors.New("controlled original callback custody failure")
			}
			jm.onSessionEvent(events.SessionEvent{SessionID: s.ID(), Kind: events.EventCommunicate, Data: events.CommunicateData{Message: "before restart"}})
			original := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
			if len(original) != 1 || producerRecordCount(t, s) != map[bool]int{true: 1, false: 0}[settled] {
				t.Fatal("original callback did not reach the requested custody boundary")
			}
			if err := jm.closeStoreOnly(); err != nil {
				t.Fatal(err)
			}
			jm = restore()
			fs.writeErr = errors.New("controlled restart notice custody failure")
			_ = jm.noticeUnrestoredWatchEnds()
			pending := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
			identities := make(map[string]*jobstore.WatchSendState)
			for _, row := range pending {
				identities[row.WatchSend.DeliveryID] = row.WatchSend
			}
			if len(identities) != 2 {
				t.Fatalf("crash cut original+notice identities=%d, want two immutable IDs", len(identities))
			}
			lost := producerSourceEvents(t, jm, jobstore.EventWatchCleared)
			if len(lost) != 1 || lost[0].Watch.EndReason != "runtime_lost" {
				t.Fatal("restart notice lacks its durable runtime_lost source batch")
			}
			for _, state := range identities {
				want := lost[0].Seq
				if state.DeliveryID == original[0].WatchSend.DeliveryID {
					want = 0
				}
				if state.Key.RestartSeq != want || state.Key.WatchID != original[0].WatchSend.Key.WatchID || state.Key.WatchGeneration != original[0].WatchSend.Key.WatchGeneration {
					t.Fatalf("source key=%+v, want original watch/generation and restart sequence %d", state.Key, want)
				}
			}
			if err := jm.closeStoreOnly(); err != nil {
				t.Fatal(err)
			}
			jm = restore()
			t.Cleanup(func() { jm.close() })
			fs.writeErr = nil
			_ = jm.noticeUnrestoredWatchEnds()
			jm.dispatchProducedWatchSends()
			_ = jm.noticeUnrestoredWatchEnds()
			jm.dispatchProducedWatchSends()
			acks := producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)
			entries := notificationRawEntries(t, transcriptPath(s.stateDir, s.ID()))
			for _, row := range producerSourceEvents(t, jm, jobstore.EventWatchSendPending) {
				prior := identities[row.WatchSend.DeliveryID]
				if prior == nil || prior.Key != row.WatchSend.Key || prior.UpdateSeq != row.WatchSend.UpdateSeq {
					t.Fatal("repeated source reconstruction changed the restart batch identity")
				}
			}
			if len(entries) != 2 || len(acks) != 2 {
				t.Fatalf("after second restart original+notice receiver=%d receipts=%d, want 2/2", len(entries), len(acks))
			}
			for _, state := range identities {
				accepted, acked := 0, 0
				for _, entry := range entries {
					if entry.Turn.AttentionID == stableWatchAttentionID(*state) {
						accepted++
					}
				}
				for _, ack := range acks {
					if ack.WatchSend.DeliveryID == state.DeliveryID {
						acked++
					}
				}
				if accepted != 1 || acked != 1 {
					t.Fatalf("source payload %s accepted=%d receipts=%d", state.DeliveryID, accepted, acked)
				}
			}
		})
	}
	for _, boundary := range []string{"before source pending", "before custody", "after custody before arm"} {
		t.Run(boundary, func(t *testing.T) {
			fs := &producerNoticeFS{Fs: afero.NewOsFs()}
			s, path := notificationReceiverForTest(t, fs)
			jm, err := newJobManager(s.stateDir, s.ID(), s.enqueueJobNotificationAndNotify)
			if err != nil {
				t.Fatal(err)
			}
			s.jobManager, jm.retirementOwner, jm.delegateController = jm, s, s.delegateController
			jm.notifySystem = s.routeSystemNotification
			if _, err := jm.configureWatch(watchArgs{Source: "self", Target: runtimeMessageAliasCaller, Events: []string{"communicate"}}); err != nil {
				t.Fatal(err)
			}
			if err := jm.closeStoreOnly(); err != nil {
				t.Fatal(err)
			}
			restore := func() *jobManager {
				t.Helper()
				r, err := newJobManager(s.stateDir, s.ID(), s.enqueueJobNotificationAndNotify)
				if err != nil {
					t.Fatal(err)
				}
				s.jobManager, r.retirementOwner, r.delegateController = r, s, s.delegateController
				r.notifySystem = s.routeSystemNotification
				t.Cleanup(func() { r.close() })
				return r
			}
			jm = restore()
			if len(producerSourceEvents(t, jm, jobstore.EventWatchCleared)) != 1 {
				t.Fatal("actual restart did not retain its runtime-lost source event")
			}
			if boundary == "before source pending" {
				if err := jm.store.Close(); err != nil {
					t.Fatal(err)
				}
			} else if boundary == "before custody" {
				fs.writeErr = errors.New("controlled notice write failure")
			} else {
				fs.afterSync = func() {
					if err := os.Rename(path, path+".held"); err != nil {
						t.Fatal(err)
					}
				}
			}
			_ = jm.noticeUnrestoredWatchEnds()
			fs.writeErr = nil
			if boundary == "after custody before arm" {
				if err := os.Rename(path+".held", path); err != nil {
					t.Fatal(err)
				}
				if producerRecordCount(t, s) != 1 {
					t.Fatal("arm fault did not occur after actual canonical custody")
				}
			}
			if boundary == "after custody before arm" {
				_ = jm.noticeUnrestoredWatchEnds()
				if records := producerRecordCount(t, s); records != 1 {
					t.Errorf("actual restart route retry canonical=%d, want 1", records)
				}
			}
			if err := jm.closeStoreOnly(); err != nil && boundary != "before source pending" {
				t.Fatal(err)
			}
			jm = restore()
			_ = jm.noticeUnrestoredWatchEnds()
			jm.dispatchProducedWatchSends()
			_ = jm.noticeUnrestoredWatchEnds()
			jm.dispatchProducedWatchSends()
			if records, acks := producerRecordCount(t, s), len(producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)); records != 1 || acks != 1 {
				t.Fatalf("restart source recovery canonical=%d source acks=%d, want 1 each", records, acks)
			}
			pending, err := s.pendingNotificationIDs()
			if err != nil || len(pending) != 1 {
				t.Fatalf("notice pending=%v err=%v", pending, err)
			}
		})
	}
}

func TestCallbackOneShotSourceRecoveryBeforeNextTick(t *testing.T) {
	s := newMessagingRoot(t, newMessagingAdapter(), nil)
	jm := s.jobManager
	key, cfg := producerTimer(t, s, false)
	if err := jm.store.Close(); err != nil {
		t.Fatal(err)
	}
	jm.fireProgressTick(key, cfg)
	reopenProducerSource(t, jm)
	jm.dispatchProducedWatchSends()
	jm.fireProgressTick(key, cfg)
	jm.dispatchProducedWatchSends()
	if records, acks, ends := producerRecordCount(t, s), len(producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)), len(producerSourceEvents(t, jm, jobstore.EventWatchCleared)); records != 1 || acks != 1 || ends != 1 {
		t.Fatalf("after pump then tick receiver=%d delivered=%d ended=%d, want 1 each", records, acks, ends)
	}
}

func TestWatchSourceFailureRetainsAllMatchedFirings(t *testing.T) {
	for _, kind := range []string{"event send", "output send", "budget boundary", "two callbacks"} {
		t.Run(kind, func(t *testing.T) {
			s := newMessagingRoot(t, newMessagingAdapter(), nil)
			jm := s.jobManager
			var configs []*watchConfig
			var jobID string
			if kind == "output send" {
				rec, err := jm.createShell(createShellOpts{Command: "not executed"})
				if err != nil {
					t.Fatal(err)
				}
				jobID = rec.JobID
			}
			count := 1
			if kind == "two callbacks" {
				count = 2
			}
			for i := range count {
				a := watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"communicate"}, Send: &watchSendArgs{To: runtimeMessageAliasCaller, Message: "retained source"}}
				if kind == "output send" {
					a.Events, a.Target, a.OutputMatch = nil, jobID, "MATCH"
				}
				if kind == "two callbacks" {
					a.Send = nil
					if i == 1 {
						seedDelegateControllerIdle(t, s.delegateController, "dlg_second", "")
						a.ReceiverSessionID, a.ReceiverDelegateID = "child-dlg_second", "dlg_second"
						w, err := transcript.NewWriter(transcriptPath(s.stateDir, a.ReceiverSessionID), transcript.Header{SessionID: a.ReceiverSessionID})
						if err != nil {
							t.Fatal(err)
						}
						if err := w.Close(); err != nil {
							t.Fatal(err)
						}
					}
				}
				r, err := jm.configureWatch(a)
				if err != nil {
					t.Fatal(err)
				}
				jm.mu.Lock()
				_, cfg, _ := jm.watchConfigByIDLocked(r.WatchID)
				jm.mu.Unlock()
				configs = append(configs, cfg)
			}
			fire := func() {
				if jobID != "" {
					jm.feedJobOutput(jobID, []byte("MATCH"), 5)
				} else {
					s.emit(events.EventCommunicate, events.CommunicateData{Message: "one actual source event"})
				}
			}
			before := 0
			if kind == "budget boundary" {
				before = watchDeliveryBudget - 1
				for range before {
					fire()
				}
			}
			if err := jm.store.Close(); err != nil {
				t.Fatal(err)
			}
			fire()
			for _, cfg := range configs {
				if cfg.conditionFires != before || cfg.deliveries != before || cfg.budgetTripped {
					t.Errorf("failed source consumed firing: fires=%d deliveries=%d tripped=%t, want %d", cfg.conditionFires, cfg.deliveries, cfg.budgetTripped, before)
				}
			}
			reopenProducerSource(t, jm)
			jm.dispatchProducedWatchSends()
			for _, eventKind := range []jobstore.EventKind{jobstore.EventWatchSendPending, jobstore.EventWatchSendDelivered} {
				rows := producerSourceEvents(t, jm, eventKind)
				conditionRows := 0
				for _, row := range rows {
					if strings.HasPrefix(row.WatchSend.TriggerReason, "event: ") || strings.HasPrefix(row.WatchSend.TriggerReason, "output_match: ") {
						conditionRows++
					}
				}
				wantTotal := before + count
				if kind == "budget boundary" {
					wantTotal++ // The durable condition fire also produces its budget-end notice.
				}
				if conditionRows != before+count || len(rows) != wantTotal {
					t.Fatalf("after recovery %s condition=%d total=%d, want %d/%d", eventKind, conditionRows, len(rows), before+count, wantTotal)
				}
			}
			if kind == "two callbacks" && len(notificationRawEntries(t, transcriptPath(s.stateDir, "child-dlg_second"))) != 1 {
				t.Fatal("later callback recipient lost its source event")
			}
		})
	}
}

func TestCallbackCustodyAckRetrySurvivesNextFiring(t *testing.T) {
	for _, send := range []bool{false, true} {
		t.Run(map[bool]string{false: "callback", true: "caller send"}[send], func(t *testing.T) {
			adapter := newMessagingAdapter()
			s := newMessagingRoot(t, adapter, nil)
			jm := s.jobManager
			jm.clock = agenttest.NewFakeClockAt(time.Unix(1700000000, 0))
			a := watchArgs{Source: "self", Target: runtimeMessageAliasCaller, RepeatSeconds: 60}
			if send {
				a.Send = &watchSendArgs{To: runtimeMessageAliasCaller, Message: "repeat"}
			}
			r, err := jm.configureWatch(a)
			if err != nil {
				t.Fatal(err)
			}
			jm.mu.Lock()
			key, cfg, _ := jm.watchConfigByIDLocked(r.WatchID)
			jm.mu.Unlock()
			s.SetNotifyFunc(func() { _ = jm.store.Close() })
			jm.fireProgressTick(key, cfg)
			s.SetNotifyFunc(nil)
			if producerRecordCount(t, s) != 1 || len(producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)) != 0 {
				t.Fatal("failed to hold source ack after custody")
			}
			reopenProducerSource(t, jm)
			appendOne := jm.store.Append
			jm.appendEvent = func(event jobstore.Event) error {
				if err := appendOne(event); err != nil {
					return err
				}
				if event.Kind == jobstore.EventWatchSendPending {
					return jm.store.Close() // B is durable, A's receipt is still faulted.
				}
				return nil
			}
			jm.fireProgressTick(key, cfg)
			if rows, acks := producerSourceEvents(t, jm, jobstore.EventWatchSendPending), producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered); len(rows) != 2 || len(acks) != 0 {
				t.Fatalf("crash cut durable pending=%d receipts=%d, want 2/0", len(rows), len(acks))
			}
			if producerRecordCount(t, s) != 1 {
				t.Fatal("crash cut must retain A's custody without accepting B")
			}
			restored, err := newJobManager(s.stateDir, s.ID(), s.enqueueJobNotificationAndNotify)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { restored.close() })
			s.jobManager, restored.retirementOwner, restored.delegateController = restored, s, s.delegateController
			path := transcriptPath(s.stateDir, s.ID())
			if err := os.Rename(path, path+".held"); err != nil {
				t.Fatal(err)
			}
			restored.dispatchProducedWatchSends()
			if err := os.Rename(path+".held", path); err != nil {
				t.Fatal(err)
			}
			fold, err := restored.store.LoadWatchSends()
			if err != nil || len(fold.Pending) != 1 || len(fold.Superseded) != 1 || len(producerSourceEvents(t, restored, jobstore.EventWatchSendDelivered)) != 0 {
				t.Fatalf("uncertain custody query changed current or predecessor work: %+v, %v", fold, err)
			}
			restored.dispatchProducedWatchSends()
			restored.dispatchProducedWatchSends()
			acks := producerSourceEvents(t, restored, jobstore.EventWatchSendDelivered)
			entries := notificationRawEntries(t, transcriptPath(s.stateDir, s.ID()))
			if len(acks) != 2 || len(entries) != 2 || len(adapter.requestsFor("root")) != 0 {
				t.Fatalf("after real crash accepted records=%d source acks=%d provider requests=%d, want 2/2/0", len(entries), len(acks), len(adapter.requestsFor("root")))
			}
			for _, entry := range entries {
				matches := 0
				for _, ack := range acks {
					if entry.Turn.AttentionID == stableWatchAttentionID(*ack.WatchSend) {
						matches++
					}
				}
				if matches != 1 {
					t.Fatal("accepted identity lost its unique source acknowledgement")
				}
			}
		})
	}
}

func TestSupersededUnacceptedWatchFrameNeverRedelivers(t *testing.T) {
	for _, send := range []bool{false, true} {
		t.Run(fmt.Sprintf("send=%t", send), func(t *testing.T) {
			fs := &producerNoticeFS{Fs: afero.NewOsFs()}
			s, _ := notificationReceiverForTest(t, fs)
			jm, err := newJobManager(s.stateDir, s.ID(), s.enqueueJobNotificationAndNotify)
			if err != nil {
				t.Fatal(err)
			}
			s.jobManager, jm.retirementOwner, jm.delegateController = jm, s, s.delegateController
			a := watchArgs{Source: "self", Target: runtimeMessageAliasCaller, Events: []string{"communicate"}}
			if send {
				a.Send = &watchSendArgs{To: runtimeMessageAliasCaller, Message: "coalesced send"}
			}
			if _, err := jm.configureWatch(a); err != nil {
				t.Fatal(err)
			}
			fs.writeErr = errors.New("controlled pre-custody failure")
			for range 2 {
				jm.onSessionEvent(events.SessionEvent{SessionID: s.ID(), Kind: events.EventCommunicate, Data: events.CommunicateData{Message: "coalescing before custody"}})
			}
			pending := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
			if len(pending) != 2 || producerRecordCount(t, s) != 0 {
				t.Fatal("did not retain two produced, unaccepted payloads")
			}
			if err := jm.closeStoreOnly(); err != nil {
				t.Fatal(err)
			}
			restored, err := newJobManager(s.stateDir, s.ID(), s.enqueueJobNotificationAndNotify)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { restored.close() })
			s.jobManager, restored.retirementOwner, restored.delegateController = restored, s, s.delegateController
			fs.writeErr = nil
			restored.dispatchProducedWatchSends()
			restored.dispatchProducedWatchSends()
			entries := notificationRawEntries(t, transcriptPath(s.stateDir, s.ID()))
			acks := producerSourceEvents(t, restored, jobstore.EventWatchSendDelivered)
			if len(entries) != 1 || len(acks) != 1 || entries[0].Turn.AttentionID != stableWatchAttentionID(*pending[1].WatchSend) || acks[0].WatchSend.DeliveryID != pending[1].WatchSend.DeliveryID {
				t.Fatalf("unaccepted predecessor resurfaced: receiver=%d receipts=%d", len(entries), len(acks))
			}
		})
	}
}

func TestCandidateOnlyWatchReceiptsRecoverThroughHostPumps(t *testing.T) {
	for _, route := range []string{"restore pump", "drain pump"} {
		t.Run(route, func(t *testing.T) {
			adapter := newMessagingAdapter()
			s := newMessagingRoot(t, adapter, nil)
			jm := s.jobManager
			jm.clock = agenttest.NewFakeClockAt(time.Unix(1700000000, 0))
			r, err := jm.configureWatch(watchArgs{Source: "self", Target: runtimeMessageAliasCaller, RepeatSeconds: 60})
			if err != nil {
				t.Fatal(err)
			}
			jm.mu.Lock()
			key, cfg, _ := jm.watchConfigByIDLocked(r.WatchID)
			jm.mu.Unlock()
			s.SetNotifyFunc(func() { _ = jm.store.Close() })
			jm.fireProgressTick(key, cfg)
			s.SetNotifyFunc(nil)
			original := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)[0].WatchSend
			reopenProducerSource(t, jm)
			var faultOriginalAck func(jobstore.Event) error
			faultOriginalAck = func(event jobstore.Event) error {
				if event.Kind == jobstore.EventWatchSendDelivered && event.WatchSend.DeliveryID == original.DeliveryID {
					if err := jm.store.Close(); err != nil {
						return err
					}
					err := jm.store.Append(event) // Actual closed-descriptor source failure.
					reopenProducerSource(t, jm)
					jm.appendEvent = faultOriginalAck
					return err
				}
				return jm.store.Append(event)
			}
			jm.appendEvent = faultOriginalAck
			jm.fireProgressTick(key, cfg)
			// A's descriptor replacement makes the earlier dispatch's bound load
			// stale. Complete B through its actual transport, not A's retry batch.
			produced := producerSourceEvents(t, jm, jobstore.EventWatchSendPending)
			if len(produced) != 2 {
				t.Fatal("B was not source-produced before the crash fixture")
			}
			if accepted, err := jm.deliverPendingWatchSend(cfg, *produced[1].WatchSend, false); err != nil || !accepted {
				t.Fatalf("B custody/receipt failed: accepted=%t err=%v", accepted, err)
			}
			before, err := jm.store.LoadWatchSends()
			acks := producerSourceEvents(t, jm, jobstore.EventWatchSendDelivered)
			if err != nil || len(before.Pending) != 0 || len(before.Superseded) != 1 || len(acks) != 1 || acks[0].WatchSend.DeliveryID == original.DeliveryID || producerRecordCount(t, s) != 2 {
				t.Fatalf("candidate-only crash cut: pending=%d candidates=%d receipts=%d err=%v", len(before.Pending), len(before.Superseded), len(acks), err)
			}
			if err := jm.closeStoreOnly(); err != nil {
				t.Fatal(err)
			}
			restored, err := newJobManager(s.stateDir, s.ID(), s.enqueueJobNotificationAndNotify)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { restored.close() })
			s.jobManager, restored.retirementOwner, restored.delegateController = restored, s, s.delegateController
			if len(restored.pendingWatchSendDeliveries(nil)) != 0 {
				t.Fatal("reconstructed manager unexpectedly has a current head")
			}
			reconstructed, err := restored.store.LoadWatchSends()
			if err != nil || len(reconstructed.Superseded) != 1 || reconstructed.Superseded[original.DeliveryID] == nil || restored.watchSendDelivered(original.DeliveryID) {
				t.Fatalf("reconstructed candidate missing or falsely settled: candidates=%d delivered=%t err=%v", len(reconstructed.Superseded), restored.watchSendDelivered(original.DeliveryID), err)
			}
			pump := func() error {
				if route == "restore pump" {
					return s.retryRestoredPendingWatchSends(context.Background())
				}
				_, err := s.drainPendingWatchSendsReport(context.Background())
				return err
			}
			custodyErr := errors.New("controlled resident custody Sync failure")
			if err := s.attachedTranscript().Close(); err != nil {
				t.Fatal(err)
			}
			path := transcriptPath(s.stateDir, s.ID())
			writer, _, err := transcript.OpenWriterForSessionWithFS(producerQueryFaultFS{Fs: afero.NewOsFs(), syncErr: custodyErr}, path, s.ID())
			if err != nil {
				t.Fatal(err)
			}
			s.attachTranscript(writer)
			if err := s.attachedTranscript().EstablishDurability(); !errors.Is(err, custodyErr) {
				t.Fatalf("resident fault did not reach its actual writer: %v", err)
			}
			queryErr := pump()
			if err := writer.Close(); err != nil && !errors.Is(err, custodyErr) {
				t.Fatal(err)
			}
			writer, _, err = transcript.OpenWriterForSession(path, s.ID())
			if err != nil {
				t.Fatal(err)
			}
			s.attachTranscript(writer)
			if !errors.Is(queryErr, custodyErr) {
				t.Fatalf("candidate-only host pump did not propagate custody Sync uncertainty: %v", queryErr)
			}
			if len(producerSourceEvents(t, restored, jobstore.EventWatchSendDelivered)) != 1 {
				t.Fatal("uncertain custody falsely settled the predecessor")
			}
			restored.appendEvent = func(event jobstore.Event) error {
				if event.Kind != jobstore.EventWatchSendDelivered {
					return restored.store.Append(event)
				}
				if err := restored.store.Close(); err != nil {
					return err
				}
				err := restored.store.Append(event)
				reopenProducerSource(t, restored)
				return err
			}
			if err := pump(); err == nil {
				t.Fatal("candidate-only host pump did not propagate source-ack failure")
			}
			if len(producerSourceEvents(t, restored, jobstore.EventWatchSendDelivered)) != 1 {
				t.Fatal("failed source ack falsely settled the predecessor")
			}
			if err := pump(); err != nil {
				t.Fatal(err)
			}
			if err := pump(); err != nil {
				t.Fatal(err)
			}
			acks = producerSourceEvents(t, restored, jobstore.EventWatchSendDelivered)
			after, err := restored.store.LoadWatchSends()
			if err != nil || len(after.Pending) != 0 || len(after.Superseded) != 0 || len(acks) != 2 || producerRecordCount(t, s) != 2 || len(adapter.requestsFor("root")) != 0 {
				t.Fatalf("host source-only recovery: pending=%d candidates=%d receipts=%d provider=%d err=%v", len(after.Pending), len(after.Superseded), len(acks), len(adapter.requestsFor("root")), err)
			}
			if got := len(producerSourceEvents(t, restored, jobstore.EventWatchSendPending)); got != len(produced) {
				t.Fatalf("host pump produced %d new payloads", got-len(produced))
			}
			for _, state := range produced {
				count := 0
				for _, ack := range acks {
					if ack.WatchSend.DeliveryID == state.WatchSend.DeliveryID {
						count++
					}
				}
				if count != 1 {
					t.Fatalf("delivery %s source receipts=%d, want exactly one", state.WatchSend.DeliveryID, count)
				}
			}
			t.Log("candidate-only reconstruction: heads=0 candidates=1 canonical=2 receipts=1; uncertain Sync and failed source ack retained receipt; repaired host pumps: candidates=0 canonical=2 exact receipts=2 provider requests=0 new payloads=0")
		})
	}
}

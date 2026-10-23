package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
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
}

func (fs producerQueryFaultFS) OpenFile(name string, flag int, perm os.FileMode) (afero.File, error) {
	file, err := fs.Fs.OpenFile(name, flag, perm)
	if err != nil {
		return nil, err
	}
	return producerQueryFaultFile{File: file, syncErr: fs.syncErr, closeErr: fs.closeErr}, nil
}

type producerQueryFaultFile struct {
	afero.File
	syncErr, closeErr error
}

func (file producerQueryFaultFile) Close() error {
	return errors.Join(file.File.Close(), file.closeErr)
}

func (file producerQueryFaultFile) Sync() error {
	if file.syncErr != nil {
		return file.syncErr
	}
	return file.File.Sync()
}

func TestNotificationProducers(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"absent then recovery", "closed before custody", "closed before custody sync failure", "closed before custody close failure", "closed before custody missing file", "custody then closed"} {
		t.Run(boundary, func(t *testing.T) {
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
			if boundary == "custody then closed" {
				s.SetNotifyFunc(func() { _ = jm.store.Close() })
			}
			if _, err := jm.configureWatch(watchArgs{Target: runtimeMessageAliasCaller, Events: []string{"communicate"}, ReceiverSessionID: receiverID, ReceiverDelegateID: delegateID}); err != nil {
				t.Fatal(err)
			}
			jm.onSessionEvent(events.SessionEvent{SessionID: s.ID(), Kind: events.EventCommunicate, Data: events.CommunicateData{Message: "closure-boundary"}})
			s.SetNotifyFunc(nil)
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
			} else {
				if len(notificationRawEntries(t, path)) != 1 {
					t.Fatal("receiver did not accept before source ack failure")
				}
				closeDelegateControllerResumability(t, c, delegateID, "closed after custody")
				restored, err := newJobManager(s.stateDir, s.ID(), s.enqueueJobNotificationAndNotify)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { restored.close() })
				jm = restored
				s.jobManager, jm.retirementOwner, jm.delegateController = jm, s, c
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

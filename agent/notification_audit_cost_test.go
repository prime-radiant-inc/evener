package agent

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// Measurements, not a latency budget. All counters wrap actual filesystem calls;
// no production admission or durability rule is replaced for this audit.
type notificationAuditFS struct {
	afero.Fs
	opens, reads, bytes, syncs int
	syncErr                    error
}

func (fs *notificationAuditFS) OpenFile(path string, flag int, mode os.FileMode) (afero.File, error) {
	fs.opens++
	f, err := fs.Fs.OpenFile(path, flag, mode)
	if err != nil {
		return nil, err
	}
	return &notificationAuditFile{File: f, fs: fs}, nil
}
func (fs *notificationAuditFS) Open(path string) (afero.File, error) {
	fs.opens++
	f, err := fs.Fs.Open(path)
	if err != nil {
		return nil, err
	}
	return &notificationAuditFile{File: f, fs: fs}, nil
}

type notificationAuditFile struct {
	afero.File
	fs *notificationAuditFS
}

func (f *notificationAuditFile) Read(p []byte) (int, error) {
	n, err := f.File.Read(p)
	f.fs.reads++
	f.fs.bytes += n
	return n, err
}
func (f *notificationAuditFile) Sync() error {
	f.fs.syncs++
	if f.fs.syncErr != nil {
		return f.fs.syncErr
	}
	return f.File.Sync()
}

func TestNotificationAuditCost(t *testing.T) {
	for _, history := range []int{1, 32, 256} {
		t.Run(fmt.Sprintf("receipts_%d", history), func(t *testing.T) {
			s := observationSession(t, &skillContinuationAdapter{}, false)
			observationEnqueue(t, s, "cost-receipt", "cost-payload")
			writer := s.attachedTranscript()
			for i := range history {
				turn := schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant(fmt.Sprintf("history-%d", i))}
				if i == history-1 {
					turn.ObservedNotificationIDs = []string{"cost-receipt"}
				}
				if err := writer.AppendSynced(turn); err != nil {
					t.Fatal(err)
				}
			}
			fs := &notificationAuditFS{Fs: afero.NewOsFs()}
			w, entries, err := transcript.OpenWriterForSessionWithFS(fs, s.TranscriptPath(), s.ID())
			if err != nil {
				t.Fatal(err)
			}
			w.TrackFailures(entries, s.fork.divergence)
			s.mu.Lock()
			s.setTranscriptLocked(w)
			s.mu.Unlock()
			t.Cleanup(func() { _ = writer.Close() })
			fs.opens, fs.reads, fs.bytes, fs.syncs = 0, 0, 0, 0
			start := time.Now()
			for range 5 {
				ids, err := s.pendingNotificationIDs()
				if err != nil || len(ids) != 0 {
					t.Fatalf("receipt authority pending=%v err=%v", ids, err)
				}
			}
			if fs.syncs != 5 {
				t.Fatalf("five authoritative queries performed %d barriers, want5", fs.syncs)
			}
			info, err := os.Stat(s.TranscriptPath())
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("history=%d transcript_bytes=%d queries=5 filesystem_barriers=%d writer_opens=%d writer_reads=%d writer_read_bytes=%d elapsed=%s (fold reader uses its own OS handle)", history, info.Size(), fs.syncs, fs.opens, fs.reads, fs.bytes, time.Since(start))
		})
	}
	for _, count := range []int{1, 8, 32} {
		t.Run(fmt.Sprintf("restart_candidates_%d", count), func(t *testing.T) {
			s, _ := notificationReceiverForTest(t, nil)
			c := s.delegateController
			fs := &notificationAuditFS{Fs: afero.NewOsFs(), syncErr: errors.New("audit receiver remains unavailable")}
			queries := 0
			c.attentionOpen = func(path, id string) (*transcript.Writer, []transcript.Entry, error) {
				queries++
				return transcript.OpenWriterForSessionWithFS(fs, path, id)
			}
			jm, err := newJobManager(s.stateDir, s.ID(), s.enqueueJobNotificationAndNotify)
			if err != nil {
				t.Fatal(err)
			}
			s.jobManager, jm.retirementOwner, jm.delegateController = jm, s, c
			seedDelegateControllerIdle(t, c, "dlg_cost", "")
			id := c.durable["dlg_cost"].Descriptor.ChildSessionID
			writeEmptyAttentionTranscript(t, transcriptPath(s.stateDir, id), id)
			if _, err := jm.configureWatch(watchArgs{Source: "self", Target: runtimeMessageAliasCaller, Events: []string{"communicate"}, ReceiverSessionID: id, ReceiverDelegateID: "dlg_cost"}); err != nil {
				t.Fatal(err)
			}
			// Repeated real producer events grow immutable predecessor history. A failed
			// receiver durability barrier keeps every candidate unacknowledged.
			failSyncs := 0
			c.attentionOpen = func(path, id string) (*transcript.Writer, []transcript.Entry, error) {
				return transcript.OpenWriterForSessionWithFS(producerQueryFaultFS{Fs: afero.NewOsFs(), syncErr: errors.New("audit receiver held"), syncCount: &failSyncs}, path, id)
			}
			for i := range count {
				jm.onSessionEvent(events.SessionEvent{SessionID: s.ID(), Kind: events.EventCommunicate, Data: events.CommunicateData{Message: fmt.Sprintf("history-%d", i)}})
			}
			if err := jm.closeStoreOnly(); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			restored, err := newJobManager(s.stateDir, s.ID(), s.enqueueJobNotificationAndNotify)
			if err != nil {
				t.Fatal(err)
			}
			reopenElapsed := time.Since(start)
			t.Cleanup(func() { restored.close() })
			s.jobManager, restored.retirementOwner, restored.delegateController = restored, s, c
			c.attentionOpen = func(path, id string) (*transcript.Writer, []transcript.Entry, error) {
				queries++
				return transcript.OpenWriterForSessionWithFS(fs, path, id)
			}
			fold, err := restored.store.LoadWatchSends()
			if err != nil {
				t.Fatal(err)
			}
			candidates := len(fold.Pending) + len(fold.Superseded)
			if candidates != count {
				t.Fatalf("source candidates=%d want%d", candidates, count)
			}
			queries = 0
			fs.opens, fs.reads, fs.bytes, fs.syncs = 0, 0, 0, 0
			loads := 0
			start = time.Now()
			err = s.retryRestoredPendingWatchSendsFrom(func() (jobstore.WatchSendRecord, error) { loads++; return restored.store.LoadWatchSends() })
			if (count == 1 && err != nil) || (count > 1 && !errors.Is(err, fs.syncErr)) {
				t.Fatalf("candidate query error=%v, want exact held durability boundary for predecessors", err)
			}
			if loads != 1 || queries == 0 || fs.syncs == 0 {
				t.Fatalf("source_loads=%d receiver_queries=%d receiver_barriers=%d", loads, queries, fs.syncs)
			}
			t.Logf("source_candidates=%d source_loads=%d receiver_queries=%d receiver_fs_opens=%d receiver_fs_reads=%d receiver_read_bytes=%d receiver_barriers=%d source_reopen=%s dispatch=%s", candidates, loads, queries, fs.opens, fs.reads, fs.bytes, fs.syncs, reopenElapsed, time.Since(start))
		})
	}
}

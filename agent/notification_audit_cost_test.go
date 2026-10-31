package agent

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
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
	beforeSync                 func() error
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
	if f.fs.beforeSync != nil {
		if err := f.fs.beforeSync(); err != nil {
			return err
		}
	}
	if f.fs.syncErr != nil {
		return f.fs.syncErr
	}
	return f.File.Sync()
}

func TestNotificationCustodyUsesValidatedHandle(t *testing.T) {
	t.Parallel()
	s := observationSession(t, &skillContinuationAdapter{}, false)
	observationEnqueue(t, s, "handle-custody", "opaque-handle-payload")
	path := s.TranscriptPath()
	original := s.attachedTranscript()
	fs := &notificationAuditFS{Fs: afero.NewOsFs()}
	writer, entries, err := transcript.OpenWriterForSessionWithFS(fs, path, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	writer.TrackFailures(entries, s.fork.divergence)
	s.mu.Lock()
	s.setTranscriptLocked(writer)
	s.mu.Unlock()
	t.Cleanup(func() { _ = original.Close() })
	query := func(want bool, wantErr bool) {
		t.Helper()
		accepted, err := s.notificationSourceAccepted("handle-custody")
		if accepted != want || (err != nil) != wantErr {
			t.Fatalf("custody=%t err=%v want=%t error=%t", accepted, err, want, wantErr)
		}
	}
	query(true, false)
	replacement, err := transcript.NewWriter(path+".replacement", transcript.Header{SessionID: s.ID()})
	if err != nil {
		t.Fatal(err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatal(err)
	}
	fs.beforeSync = func() error {
		fs.beforeSync = nil
		if err := os.Rename(path, path+".held"); err != nil {
			return err
		}
		return os.Rename(path+".replacement", path)
	}
	query(true, false) // Fold the file that crossed the barrier, not its replacement.
	query(false, true) // A later open of the replacement is not original custody.
	if len(notificationRawEntries(t, path)) != 0 {
		t.Fatal("replacement acquired original attention")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".held", path); err != nil {
		t.Fatal(err)
	}
	query(true, false)
	held := errors.New("controlled resident custody Sync failure")
	fs.syncErr = held
	if accepted, err := s.notificationSourceAccepted("handle-custody"); accepted || !errors.Is(err, held) {
		t.Fatalf("failed barrier custody=%t err=%v", accepted, err)
	}
	fs.syncErr = nil
	query(true, false)
}

func TestNotificationCustodyQueryReadCost(t *testing.T) {
	t.Parallel()
	if runtime.GOOS != "linux" {
		t.Skip("kernel per-thread read accounting requires Linux")
	}
	// Count real reads on this OS thread only, excluding unrelated goroutines.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	readCounts := func() (bytes, calls uint64) {
		t.Helper()
		data, err := os.ReadFile("/proc/thread-self/io")
		if err != nil {
			t.Fatal(err)
		}
		foundBytes, foundCalls := false, false
		for line := range strings.SplitSeq(string(data), "\n") {
			if strings.HasPrefix(line, "rchar:") {
				if _, err := fmt.Sscanf(line, "rchar: %d", &bytes); err != nil {
					t.Fatal(err)
				}
				foundBytes = true
			}
			if strings.HasPrefix(line, "syscr:") {
				if _, err := fmt.Sscanf(line, "syscr: %d", &calls); err != nil {
					t.Fatal(err)
				}
				foundCalls = true
			}
		}
		if !foundBytes || !foundCalls {
			t.Fatal("kernel read accounting fields are unavailable")
		}
		return bytes, calls
	}
	s := observationSession(t, &skillContinuationAdapter{}, false)
	observationEnqueue(t, s, "cost-custody", "opaque-custody-payload")
	writer := s.attachedTranscript()
	for range 512 {
		if err := writer.AppendSynced(schema.Turn{Kind: schema.TurnAssistant, Message: llm.Assistant(strings.Repeat("x", 1024))}); err != nil {
			t.Fatal(err)
		}
	}
	query := func(id string, want bool) {
		t.Helper()
		got, err := s.notificationSourceAccepted(id)
		if err != nil || got != want {
			t.Fatalf("custody=%t want=%t err=%v", got, want, err)
		}
	}
	query("cost-custody", true) // Warm the actual session custody path.
	info, err := os.Stat(s.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, appended := range []bool{false, true} {
		if appended {
			turn := schema.NewTurn(schema.TurnSteering, llm.UserMachinery("opaque-appended-payload"))
			turn.AttentionID = "cost-appended"
			if err := writer.AppendSynced(turn); err != nil {
				t.Fatal(err)
			}
		}
		beforeBytes, beforeCalls := readCounts()
		for range 8 {
			query("cost-custody", true)
			query("cost-absent", false)
			query("cost-appended", appended)
		}
		afterBytes, afterCalls := readCounts()
		bytes, calls := afterBytes-beforeBytes, afterCalls-beforeCalls
		t.Logf("history_bytes=%d appended=%t queries=24 actual_read_bytes=%d actual_read_calls=%d", info.Size(), appended, bytes, calls)
		// Each query may verify two 4096-byte cursor tails and read the appended
		// record once. Allow a fixed 64KiB for proc accounting and runtime reads,
		// never a history-sized allowance or an elapsed-time threshold.
		if bytes == 0 || calls == 0 || bytes > 256*1024 {
			t.Errorf("24 warmed queries reread %d bytes of %d-byte history", bytes, info.Size())
		}
	}
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
			if !errors.Is(err, fs.syncErr) {
				t.Fatalf("candidate query error=%v, want exact held durability boundary for predecessors", err)
			}
			if loads != 1 || queries != count || fs.syncs != count {
				t.Fatalf("source_loads=%d receiver_queries=%d receiver_barriers=%d", loads, queries, fs.syncs)
			}
			after, err := restored.store.LoadWatchSends()
			if err != nil || len(after.Pending) != 1 || len(after.Superseded) != count-1 {
				t.Fatalf("retained pending=%d predecessors=%d err=%v", len(after.Pending), len(after.Superseded), err)
			}
			if len(producerSourceEvents(t, restored, jobstore.EventWatchSendDelivered)) != 0 || len(producerSourceEvents(t, restored, jobstore.EventWatchSendDropped)) != 0 || len(notificationRawEntries(t, transcriptPath(s.stateDir, id))) != 0 {
				t.Fatal("failed receiver barrier manufactured custody or settled source debt")
			}
			t.Logf("source_candidates=%d source_loads=%d receiver_queries=%d receiver_fs_opens=%d receiver_fs_reads=%d receiver_read_bytes=%d receiver_barriers=%d source_reopen=%s dispatch=%s", candidates, loads, queries, fs.opens, fs.reads, fs.bytes, fs.syncs, reopenElapsed, time.Since(start))
		})
	}
}

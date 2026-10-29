package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// The fault boundary is the real transcript file's Sync, not admission,
// observation, or the drain itself. Atomic switches also cover async writers.
type drainSyncFaultFS struct {
	afero.Fs
	all, resolution atomic.Bool
	err             error
}

func (fs *drainSyncFaultFS) OpenFile(path string, flag int, mode os.FileMode) (afero.File, error) {
	file, err := fs.Fs.OpenFile(path, flag, mode)
	if err != nil {
		return nil, err
	}
	return &drainSyncFaultFile{File: file, fs: fs}, nil
}

type drainSyncFaultFile struct {
	afero.File
	fs *drainSyncFaultFS
}

func (file *drainSyncFaultFile) Sync() error {
	if file.fs.all.Load() {
		return file.fs.err
	}
	if file.fs.resolution.Load() {
		data, err := os.ReadFile(file.Name())
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(`"attention_resolution":`)) {
			return file.fs.err
		}
	}
	return file.File.Sync()
}

func drainInstallSyncFault(t *testing.T, sess *Session) *drainSyncFaultFS {
	t.Helper()
	fs := &drainSyncFaultFS{Fs: afero.NewOsFs(), err: errors.New("drain transcript sync fault-731a")}
	original := sess.attachedTranscript()
	writer, entries, err := transcript.OpenWriterForSessionWithFS(fs, sess.TranscriptPath(), sess.ID())
	if err != nil {
		t.Fatal(err)
	}
	writer.TrackFailures(entries, sess.fork.divergence)
	sess.mu.Lock()
	sess.setTranscriptLocked(writer)
	sess.mu.Unlock()
	t.Cleanup(func() {
		fs.all.Store(false)
		fs.resolution.Store(false)
		_ = original.Close()
	})
	return fs
}

func TestRematerializeShellCustodyRetry(t *testing.T) {
	t.Parallel()
	adapter := newMessagingAdapter()
	sess := newMessagingRoot(t, adapter, nil)
	// Initialize the real writer before attaching its external fault boundary.
	if _, err := sess.ProcessInput(context.Background(), "initialize custody control", nil); err != nil {
		t.Fatal(err)
	}
	fs := drainInstallSyncFault(t, sess)
	seedOwnedDurablePending(t, sess.jobManager, "shell-custody-retry", jobstore.JobShell)
	fs.all.Store(true)
	if err := sess.rematerializeDurablePendings(); !errors.Is(err, fs.err) {
		t.Fatalf("custody failure = %v, want injected Sync error", err)
	}
	requireNotificationState(t, sess.jobManager, "shell-custody-retry", jobstore.NotifyPending)
	fs.all.Store(false)
	assertNotificationPending(t, sess, []string{})
	for range 2 {
		if err := sess.rematerializeDurablePendings(); err != nil {
			t.Fatal(err)
		}
	}
	requireNotificationState(t, sess.jobManager, "shell-custody-retry", jobstore.NotifyDelivered)
	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	requireDrainShellObserved(t, sess, "shell-custody-retry")
	if got := len(adapter.requestsFor("root")); got != 2 {
		t.Fatalf("custody retry provider requests=%d, want initialization plus one observation", got)
	}
}

func TestDrainAnnouncementReceiptIndexRepairIsNotObservation(t *testing.T) {
	adapter := newMessagingAdapter()
	clock := agenttest.NewFakeClock()
	sess := newMessagingRoot(t, adapter, func(cfg *SessionConfig) {
		cfg.TurnEndsProcess = true
		cfg.clock = clock
	})
	seedOwnedDurablePending(t, sess.jobManager, "shell-index-repair", jobstore.JobShell)
	if err := sess.rematerializeDurablePendings(); err != nil {
		t.Fatal(err)
	}
	fs := drainInstallSyncFault(t, sess)
	fs.resolution.Store(true)
	// This is a real successful provider response, with only the later derived
	// resolution append faulted. Its raw observation receipt remains canonical.
	if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil && !errors.Is(err, fs.err) {
		t.Fatalf("initial response = %v", err)
	}
	sess.attentionMu.Lock()
	fold, err := sess.delegateAttentionFoldLocked()
	sess.attentionMu.Unlock()
	id := "shell:shell-index-repair:gen-shell-index-repair"
	_, observed := fold.observed[id]
	if err != nil || !observed || len(fold.resolutions) != 0 {
		t.Fatalf("fault did not isolate receipt from resolution: observed=%t resolutions=%v err=%v", observed, fold.resolutions, err)
	}
	startControlledBackgroundShell(t, sess, "receipt-index-repair-control")
	// Entry repairs this real index debt. Neither that repair nor the following
	// announcement responses earn any new canonical observation identity.
	fs.resolution.Store(false)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result, err := sess.drainJobTreeWith(ctx, drainWakeForTest(sess), feedRechecks(ctx), sess.kickDriveTree, sess.ProcessInputKind)
	if err != nil || result != "" {
		t.Fatalf("receipt-index repair promoted housekeeping: %q, %v", result, err)
	}
	sess.attentionMu.Lock()
	fold, err = sess.delegateAttentionFoldLocked()
	sess.attentionMu.Unlock()
	if err != nil || len(fold.observed) != 1 || fold.resolutions[id] != delegateAttentionConsumed {
		t.Fatalf("entry did not repair the existing receipt: observed=%v resolutions=%v err=%v", fold.observed, fold.resolutions, err)
	}
	if got := len(adapter.requestsFor("root")); got != 3 {
		t.Fatalf("provider requests=%d, want prior observation and two announcements", got)
	}
	if warnings := collectStallWarnings(sess); len(warnings) != 1 {
		t.Fatalf("warning events = %+v, want only declined announcements", warnings)
	}
}

func TestDrainAnnouncementDoesNotPromoteHousekeeping(t *testing.T) {
	// Deliberate provider failures emit a daemon warning. Capture and assert it
	// while sequential tests own the default logger, including session cleanup.
	for _, persistent := range []bool{false, true} {
		name := "memory"
		if persistent {
			name = "persistent"
		}
		for _, mode := range []string{"announcement_only", "prior_observed", "failed_provider", "input_error", "empty_answer"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				var logged diagnosticLogCapture
				var sess *Session
				previous := slog.Default()
				slog.SetDefault(slog.New(slog.NewJSONHandler(&logged, nil)))
				// Register before any resources, so their LIFO cleanup completes
				// before validation. Restore even if validation calls Fatal.
				t.Cleanup(func() {
					defer slog.SetDefault(previous)
					captured := logged.String()
					if mode == "failed_provider" {
						var record struct {
							Level   string `json:"level"`
							Session string `json:"session"`
						}
						// One JSON object pins exactly one log record, including
						// teardown. Failure copy is not a classification API.
						if err := json.Unmarshal([]byte(captured), &record); err != nil || record.Level != "WARN" || record.Session != sess.ID() {
							t.Fatalf("expected one provider warning: %q, err=%v", captured, err)
						}
					} else if captured != "" {
						t.Fatalf("unexpected daemon log: %s", captured)
					}
				})
				adapter := newMessagingAdapter()
				answer := func(context.Context, llm.Request) (llm.Response, error) {
					return finalResponse("housekeeping-is-not-the-answer-731a"), nil
				}
				steps := []func(context.Context, llm.Request) (llm.Response, error){answer, answer}
				if mode == "prior_observed" {
					steps = append(steps, answer)
				}
				if mode == "failed_provider" {
					steps = []func(context.Context, llm.Request) (llm.Response, error){
						func(context.Context, llm.Request) (llm.Response, error) {
							return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 401, "fixture rejection", nil, nil)
						},
					}
				}
				adapter.finiteScript("root", steps...)
				sess = newMessagingRoot(t, adapter, func(cfg *SessionConfig) {
					cfg.TurnEndsProcess = true
					if !persistent {
						cfg.StateDir = ""
					}
				})
				if mode == "prior_observed" {
					seedOwnedDurablePending(t, sess.jobManager, "earlier-shell", jobstore.JobShell)
					if err := sess.rematerializeDurablePendings(); err != nil {
						t.Fatal(err)
					}
					if _, err := sess.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
						t.Fatal(err)
					}
					requireDrainShellObserved(t, sess, "earlier-shell")
				}
				jobID, release := startControlledBackgroundShell(t, sess, "announcement-control-731a")
				// TRIPWIRE: scripted provider and controlled shell, no live service.
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				completed := false
				var providerErr error
				process := func(ctx context.Context, input string, images []ImageAttachment, kind EntryKind) (string, error) {
					if mode == "failed_provider" || mode == "input_error" || mode == "empty_answer" {
						if !completed {
							finalizeShell(t, sess.jobManager, jobID, release)
							completed = true
						}
					}
					res, err := sess.ProcessInputKind(ctx, input, images, kind)
					if mode == "failed_provider" {
						providerErr = err
					}
					if mode == "input_error" && err == nil {
						return res, errors.New("post-response input failure-731a")
					}
					if mode == "empty_answer" {
						return "", err
					}
					return res, err
				}
				result, err := sess.drainJobTreeWith(ctx, drainWakeForTest(sess), feedRechecks(ctx), sess.kickDriveTree, process)
				if err != nil || result != "" {
					t.Fatalf("housekeeping promoted: result=%q err=%v", result, err)
				}
				reqs := adapter.requestsFor("root")
				wantRequests, wantObserved := 2, 0
				if mode == "prior_observed" {
					wantRequests, wantObserved = 3, 1
				} else if completed {
					wantRequests = 1
					if mode != "failed_provider" {
						wantObserved = 1
						requireDrainShellObserved(t, sess, jobID)
					}
					drainRequestNotice(t, reqs[0], map[string]string{"job_id": jobID, "status": "command_exited_nonzero", "exit_code": "143"})
				}
				if len(reqs) != wantRequests {
					t.Fatalf("provider requests=%d, want %d", len(reqs), wantRequests)
				}
				sess.attentionMu.Lock()
				fold, foldErr := sess.delegateAttentionFoldLocked()
				sess.attentionMu.Unlock()
				if foldErr != nil || len(fold.observed) != wantObserved {
					t.Fatalf("observations=%d, want %d, err=%v", len(fold.observed), wantObserved, foldErr)
				}
				if mode == "failed_provider" {
					recs, err := sess.jobManager.store.Load()
					if err != nil {
						t.Fatal(err)
					}
					id := "shell:" + jobID + ":" + recs[jobID].TerminalGen
					if pending := fold.pendingIDs(); len(pending) != 1 || pending[0] != id {
						t.Fatalf("failed request pending IDs = %v, want %s", pending, id)
					}
					var typed llm.Error
					if llm.Kind(providerErr) != llm.KindAuthentication || !errors.As(providerErr, &typed) || typed.StatusCode() != 401 {
						t.Fatalf("provider error = %v, want typed HTTP 401 authentication failure", providerErr)
					}
					sess.mu.Lock()
					paused := sess.notificationSchedule.paused
					sess.mu.Unlock()
					if !paused || sess.notificationsRunnable() {
						t.Fatal("permanent provider failure did not pause pending receiver work")
					}
				}
				warnings := collectStallWarnings(sess)
				wantWarnings := 1 // declining both announcements, or injected input error
				switch mode {
				case "failed_provider":
					wantWarnings = 2 // provider pause and failed announcement turn
				case "empty_answer":
					wantWarnings = 0
				}
				if len(warnings) != wantWarnings {
					t.Fatalf("warning events=%+v, want %d", warnings, wantWarnings)
				}
				if mode == "failed_provider" {
					paused := 0
					for _, warning := range warnings {
						if data, ok := warning.Data.(events.WarningData); ok && data.Code == events.WarningCodeAttentionPaused {
							paused++
						}
					}
					if paused != 1 {
						t.Fatalf("pause warning events=%d, want exactly one", paused)
					}
				}
			})
		}
	}
}

// drainRequestNotice finds a real user notification by its structured fields,
// regardless of tool-result carriers or other notifications in that request.
func drainRequestNotice(t *testing.T, req llm.Request, want map[string]string) map[string]string {
	t.Helper()
	var found []map[string]string
	for _, message := range req.Messages {
		text := strings.TrimSpace(message.Text())
		if message.Role != llm.RoleUser || !strings.HasPrefix(text, "<job-notification ") {
			continue
		}
		token, err := xml.NewDecoder(strings.NewReader(text)).Token()
		if err != nil {
			t.Fatalf("notification header: %v", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "job-notification" {
			t.Fatalf("notification header = %#v", token)
		}
		attrs := make(map[string]string, len(start.Attr))
		for _, attr := range start.Attr {
			attrs[attr.Name.Local] = attr.Value
		}
		found = append(found, attrs)
		matches := true
		for key, value := range want {
			if attrs[key] != value {
				matches = false
				break
			}
		}
		if matches {
			return attrs
		}
	}
	t.Fatalf("provider request has notifications %v, want fields %v", found, want)
	return nil
}

// requireDrainShellObserved checks the canonical successful-response receipt,
// not the source acknowledgement or the legacy notification queue.
func requireDrainShellObserved(t *testing.T, sess *Session, jobID string) {
	t.Helper()
	recs, err := sess.jobManager.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	rec := recs[jobID]
	if rec == nil || rec.NotifyState != jobstore.NotifyDelivered {
		t.Fatalf("shell source not settled: %+v", rec)
	}
	id := "shell:" + jobID + ":" + rec.TerminalGen
	sess.attentionMu.Lock()
	fold, err := sess.delegateAttentionFoldLocked()
	_, observed := fold.observed[id]
	_, accepted := fold.content[id]
	resolution := fold.resolutions[id]
	sess.attentionMu.Unlock()
	if err != nil || !accepted || !observed || resolution != delegateAttentionConsumed {
		t.Fatalf("shell %s receipt: accepted=%t observed=%t resolution=%s err=%v", id, accepted, observed, resolution, err)
	}
	if sess.stateDir != "" {
		requireOwnedDrainAttentionObservedOnce(t, sess, id)
		return
	}
	// Independently inspect actual accepted/response turns without a transcript
	// file. Memory-only resolution updates live in the fold, not model history,
	// so their consumed disposition is checked above rather than invented here.
	acceptedCount, receiptCount := 0, 0
	sess.mu.Lock()
	for _, turn := range sess.history {
		if turn.AttentionID == id {
			acceptedCount++
		}
		for _, observed := range turn.ObservedNotificationIDs {
			if observed == id {
				receiptCount++
			}
		}
	}
	sess.mu.Unlock()
	if acceptedCount != 1 || receiptCount != 1 {
		t.Fatalf("shell %s lifetime accepted=%d receipts=%d, want 1/1", id, acceptedCount, receiptCount)
	}
}

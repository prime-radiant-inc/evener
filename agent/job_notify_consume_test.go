package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/jobstore"
	tooldefs "primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// startBackgroundShellJob runs a real background shell command through the real
// tool registry and returns its job id. The command is short but not instant:
// the job must survive long enough to be a durable background job rather than
// an ephemeral inline completion.
func startBackgroundShellJob(t *testing.T, s *Session, command string) string {
	t.Helper()
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "bg1",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":` + mustJSONString(command) + `,"mode":"background"}`),
	})
	if res.IsError {
		t.Fatalf("background shell returned error: %s", res.Output)
	}
	var out struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal shell result: %v (output: %s)", err, res.Output)
	}
	if out.JobID == "" {
		t.Fatalf("background shell returned no job_id: %s", res.Output)
	}
	return out.JobID
}

func mustJSONString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// jobNotifyWatcher awaits a condition over job-notify state by re-checking it
// each time the session's own notify wake fires, rather than on a fixed
// polling interval. That wake (session.go's notify(), invoked by
// enqueueNotifications/enqueueJobNotificationAndNotify whenever a job
// notification becomes pending) is the production completion signal these
// tests actually care about: it is what wakes an idle session to run a
// notification turn.
type jobNotifyWatcher struct {
	woken chan struct{}
}

// newJobNotifyWatcher installs s's notify callback and returns a watcher that
// re-tests conditions each time it fires. onWake, if non-nil, runs on every
// wake before the watcher signals it (e.g. to record what the queue held at
// that instant). Call this before starting the job whose completion the test
// awaits, or an early wake can be missed.
func newJobNotifyWatcher(s *Session, onWake func()) *jobNotifyWatcher {
	w := &jobNotifyWatcher{woken: make(chan struct{}, 1)}
	s.SetNotifyFunc(func() {
		if onWake != nil {
			onWake()
		}
		select {
		case w.woken <- struct{}{}:
		default:
		}
	})
	return w
}

// await blocks until cond returns true, re-testing it once up front and again
// every time the session's notify wake fires.
func (w *jobNotifyWatcher) await(t *testing.T, what string, cond func() bool) {
	t.Helper()
	if cond() {
		return
	}
	// TRIPWIRE: the driving job is a scripted local shell run (a short
	// process or a temp-file gate) with no real network I/O; this only fires
	// on a genuine hang, not scheduling delay.
	deadline := time.After(30 * time.Second)
	for {
		select {
		case <-w.woken:
			if cond() {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func newStatusNotificationSession(t *testing.T, persistent bool) (*Session, *fakeAdapter) {
	t.Helper()
	adapter := &fakeAdapter{name: "openai"}
	cfg := SessionConfig{}
	if persistent {
		cfg.StateDir = t.TempDir()
	}
	return newSession(t, withAdapter(adapter), withConfig(cfg)), adapter
}

func awaitCanonicalShellCustody(t *testing.T, watcher *jobNotifyWatcher, s *Session, jobID string) string {
	t.Helper()
	waitForShellDone(t, s.jobManager, jobID)
	rec := loadShellRecord(t, s.jobManager, jobID)
	id := "shell:" + jobID + ":" + rec.TerminalGen
	watcher.await(t, "exact canonical shell custody", func() bool {
		ids, err := s.pendingNotificationIDs()
		return err == nil && slices.Contains(ids, id)
	})
	return requireCanonicalShellNotification(t, s, jobID, 0)
}

func terminalStatusCall(jobID string) llm.ToolCallData {
	return llm.ToolCallData{ID: "status", Name: "job_status", Type: "function", Arguments: json.RawMessage(`{"target":` + mustJSONString(jobID) + `}`)}
}

func requireTerminalStatusResult(t *testing.T, req llm.Request, callID, jobID string) {
	t.Helper()
	for _, message := range req.Messages {
		for _, part := range message.Content {
			if part.ToolResult == nil || part.ToolResult.ToolCallID != callID {
				continue
			}
			var result jobStatusResult
			if err := json.Unmarshal(part.ToolResult.ToolState, &result); err != nil {
				t.Fatal(err)
			}
			if result.JobID != jobID || result.Status != string(jobstore.StatusCompleted) {
				t.Fatalf("terminal job_status state=%+v, want completed %s", result, jobID)
			}
			return
		}
	}
	t.Fatalf("missing actual job_status result for call %s", callID)
}

func runOwnerStatusResponse(t *testing.T, s *Session, adapter *fakeAdapter, jobID string) {
	t.Helper()
	call := terminalStatusCall(jobID)
	adapter.steps = []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			observationRequest(t, req, []string{fmt.Sprintf(`job_id=%q`, jobID)}, nil)
			requireCanonicalShellNotification(t, s, jobID, 0)
			return toolCallResponse(call)
		},
		func(req llm.Request) llm.Response {
			requireTerminalStatusResult(t, req, call.ID, jobID)
			requireCanonicalShellNotification(t, s, jobID, 1)
			return finalResponse("continued")
		},
	}
	output, err := s.ProcessInput(context.Background(), "finish after checking the job", nil)
	if err != nil || output != "continued" {
		t.Fatalf("ProcessInput=%q error=%v", output, err)
	}
	requireCanonicalShellNotification(t, s, jobID, 2)
	if len(adapter.Requests()) != 2 {
		t.Fatalf("provider requests=%d, want 2", len(adapter.Requests()))
	}
	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	if len(adapter.Requests()) != 2 {
		t.Fatal("an already observed completion started an extra response")
	}
	requireCanonicalShellNotification(t, s, jobID, 2)
}

// A real status tool round continues the current response. Receiver custody
// alone is not observation, and persisted successful responses leave no extra
// notification response to run.
func TestPersistedTerminalJobStatusReadConsumesPendingNotification(t *testing.T) {
	t.Parallel()
	s, adapter := newStatusNotificationSession(t, true)
	watcher := newJobNotifyWatcher(s, nil)
	jobID := startBackgroundShellJob(t, s, "printf 'consume me\\n'")
	awaitCanonicalShellCustody(t, watcher, s, jobID)
	runOwnerStatusResponse(t, s, adapter, jobID)
}

func TestPersistedTerminalJobStatusPreservesQueuedSelfWatchNotification(t *testing.T) {
	t.Parallel()
	s, adapter := newStatusNotificationSession(t, true)
	if _, err := jobWatchTool(s, map[string]any{
		"operation": "create", "source": "self", "events": []any{"job.notification"},
	}, jobToolResultDefaultMaxChar); err != nil {
		t.Fatal(err)
	}
	watcher := newJobNotifyWatcher(s, nil)
	gate := filepath.Join(t.TempDir(), "release")
	jobID := startBackgroundShellJob(t, s, "while [ ! -f "+gate+" ]; do sleep 0.02; done; printf 'watched done\\n'")
	if err := os.WriteFile(gate, []byte("go\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	awaitCanonicalShellCustody(t, watcher, s, jobID)
	source := producerSourceEvents(t, s.jobManager, jobstore.EventWatchSendPending)
	if len(source) != 1 || source[0].WatchSend == nil {
		t.Fatalf("watch source = %+v", source)
	}
	frame := source[0].WatchSend
	watchID := fmt.Sprintf("watch:%s:%d", frame.DeliveryID, frame.UpdateSeq)
	shellID := requireCanonicalShellNotification(t, s, jobID, 0)
	ids, err := s.pendingNotificationIDs()
	if err != nil || len(ids) != 2 || !slices.Contains(ids, shellID) || !slices.Contains(ids, watchID) {
		t.Fatalf("pending identities=%v error=%v, want exact shell and watch", ids, err)
	}
	call := terminalStatusCall(jobID)
	result := s.reg.ExecuteCall(context.Background(), s.env, call)
	if result.IsError {
		t.Fatalf("job_status: %s", result.Output)
	}
	if err := s.persistToolResults(context.Background(), []llm.ToolCallData{call}, []tooldefs.ExecResult{result}); err != nil {
		t.Fatal(err)
	}
	requireCanonicalShellNotification(t, s, jobID, 0)
	assertNotificationPending(t, s, ids)
	adapter.steps = []func(llm.Request) llm.Response{func(req llm.Request) llm.Response {
		observationRequest(t, req, []string{frame.Frame, fmt.Sprintf(`job_id=%q`, jobID)}, nil)
		assertNotificationPending(t, s, ids)
		return finalResponse("both notifications observed")
	}}
	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	requireCanonicalShellNotification(t, s, jobID, 1)
	assertNotificationObservedOnce(t, s.TranscriptPath(), watchID)
	assertNotificationPending(t, s, []string{})
	if len(adapter.Requests()) != 1 {
		t.Fatalf("provider requests=%d, want 1", len(adapter.Requests()))
	}
}

func TestTerminalJobStatusTranscriptFailureLeavesNotificationPending(t *testing.T) {
	t.Parallel()
	s, adapter := newStatusNotificationSession(t, true)
	watcher := newJobNotifyWatcher(s, nil)
	jobID := startBackgroundShellJob(t, s, "printf 'persist me\\n'")
	id := awaitCanonicalShellCustody(t, watcher, s, jobID)
	fs := statusWriteFailureFS{&transcriptWriteFailFS{Fs: afero.NewOsFs()}}
	old := s.attachedTranscript()
	writer, entries, err := transcript.OpenWriterForSessionWithFS(fs, s.TranscriptPath(), s.ID())
	if err != nil {
		t.Fatal(err)
	}
	writer.TrackFailures(entries, s.fork.divergence)
	s.mu.Lock()
	s.setTranscriptLocked(writer)
	s.mu.Unlock()
	t.Cleanup(func() { fs.fail = false; _ = old.Close() })
	fs.fail = true
	call := terminalStatusCall(jobID)
	result := s.reg.ExecuteCall(context.Background(), s.env, call)
	if result.IsError {
		t.Fatalf("job_status: %s", result.Output)
	}
	historyLen := len(s.history)
	err = s.persistToolResults(context.Background(), []llm.ToolCallData{call}, []tooldefs.ExecResult{result})
	if !errors.Is(err, errInjectedTranscriptWrite) {
		t.Fatalf("persistToolResults error=%v, want exact Write failure", err)
	}
	requireCanonicalShellNotification(t, s, jobID, 0)
	assertNotificationPending(t, s, []string{id})
	if len(s.history) != historyLen {
		t.Fatal("failed tool result changed history")
	}
	if _, found := findToolResultInHistory(s.history, call.ID); found {
		t.Fatal("failed result entered history")
	}
	for _, entry := range notificationRawEntries(t, s.TranscriptPath()) {
		if _, found := findToolResultInHistory([]schema.Turn{entry.Turn}, call.ID); found {
			t.Fatal("failed result entered disk transcript")
		}
	}
	fs.fail = false
	adapter.steps = []func(llm.Request) llm.Response{func(req llm.Request) llm.Response {
		observationRequest(t, req, []string{fmt.Sprintf(`job_id=%q`, jobID)}, nil)
		requireCanonicalShellNotification(t, s, jobID, 0)
		return finalResponse("recovered notification observed")
	}}
	if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	requireCanonicalShellNotification(t, s, jobID, 1)
	if len(adapter.Requests()) != 1 {
		t.Fatalf("provider requests=%d want 1", len(adapter.Requests()))
	}
}

type statusWriteFailureFS struct{ *transcriptWriteFailFS }

func (fs statusWriteFailureFS) OpenFile(path string, flag int, mode os.FileMode) (afero.File, error) {
	file, err := fs.Fs.OpenFile(path, flag, mode)
	if err != nil {
		return nil, err
	}
	return &transcriptWriteFailFile{File: file, fs: fs.transcriptWriteFailFS}, nil
}

func TestParentTerminalJobStatusReadLeavesChildNotificationPending(t *testing.T) {
	t.Parallel()
	const marker = "STATUS-ISOLATION-CHILD-731"
	adapter := newMessagingAdapter(marker)
	parent := newMessagingRoot(t, adapter, nil)
	gate := filepath.Join(t.TempDir(), "release-shell")
	responseGate := make(chan struct{})
	releaseResponse := sync.OnceFunc(func() { close(responseGate) })
	defer releaseResponse()
	started := make(chan string, 1)
	var child *Session
	var jobID string
	adapter.script("root", func(llm.Request) llm.Response { return delegateToolResponse("status-child", marker) }, func(llm.Request) llm.Response { return finalResponse("child created") })
	adapter.script(marker,
		func(llm.Request) llm.Response {
			return toolCallResponse(llm.ToolCallData{ID: "child-shell", Name: "shell", Type: "function", Arguments: json.RawMessage(`{"command":` + mustJSONString("while [ ! -f "+gate+" ]; do sleep 0.02; done; printf 'child done\\n'") + `,"mode":"background"}`)})
		},
		func(req llm.Request) llm.Response {
			var result struct {
				JobID string `json:"job_id"`
			}
			for _, message := range req.Messages {
				for _, part := range message.Content {
					if part.ToolResult != nil && part.ToolResult.ToolCallID == "child-shell" {
						if err := json.Unmarshal(part.ToolResult.ToolState, &result); err != nil {
							t.Errorf("decode actual shell tool state: %v", err)
						}
					}
				}
			}
			started <- result.JobID
			awaitSignal(t, responseGate, "parent status read before child response")
			return finalResponse("continue after held response")
		},
		func(req llm.Request) llm.Response {
			observationRequest(t, req, []string{fmt.Sprintf(`job_id=%q`, jobID)}, nil)
			requireCanonicalShellNotification(t, child, jobID, 0)
			return finalResponse("child observed own completion")
		},
	)
	if _, err := parent.ProcessInput(context.Background(), "create a child status owner", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case jobID = <-started:
	case <-time.After(30 * time.Second): // TRIPWIRE: local scripted child provider and shell only.
		t.Fatal("child never reached the held provider response")
	}
	children := parent.subagents.sessions()
	if len(children) != 1 || jobID == "" {
		t.Fatalf("children=%d jobID=%q", len(children), jobID)
	}
	child = children[0]
	child.mu.Lock()
	resume := child.notifyFunc
	child.mu.Unlock()
	if resume == nil {
		t.Fatal("initialized child has no production wake callback")
	}
	watcher := newJobNotifyWatcher(child, nil)
	if err := os.WriteFile(gate, []byte("go\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := awaitCanonicalShellCustody(t, watcher, child, jobID)
	call := terminalStatusCall(jobID)
	adapter.script("root", func(llm.Request) llm.Response {
		requireCanonicalShellNotification(t, child, jobID, 0)
		return toolCallResponse(call)
	}, func(req llm.Request) llm.Response {
		requireTerminalStatusResult(t, req, call.ID, jobID)
		requireCanonicalShellNotification(t, child, jobID, 0)
		return finalResponse("parent read child status")
	})
	if _, err := parent.ProcessInput(context.Background(), "read the child's terminal status", nil); err != nil {
		t.Fatal(err)
	}
	requireCanonicalShellNotification(t, child, jobID, 0)
	assertNotificationPending(t, child, []string{id})
	for _, entry := range notificationRawEntries(t, parent.TranscriptPath()) {
		if entry.Turn.AttentionID == id || slices.Contains(entry.Turn.ObservedNotificationIDs, id) {
			t.Fatal("parent adopted or observed child-owned attention")
		}
	}
	child.SetNotifyFunc(resume)
	releaseResponse()
	waitForSoleChildIdle(t, parent)
	waitForStableSupervisionRun(t, parent, child.ID())
	requireCanonicalShellNotification(t, child, jobID, 1)
	if len(adapter.requestsFor(marker)) != 3 || len(adapter.requestsFor("root")) != 4 {
		t.Fatalf("requests child=%d parent=%d, want 3 and 4", len(adapter.requestsFor(marker)), len(adapter.requestsFor("root")))
	}
}

func TestTerminalJobStatusReadContinuesCurrentTurn(t *testing.T) {
	t.Parallel()
	s, adapter := newStatusNotificationSession(t, false)
	watcher := newJobNotifyWatcher(s, nil)
	jobID := startBackgroundShellJob(t, s, "printf 'done\\n'")
	awaitCanonicalShellCustody(t, watcher, s, jobID)
	runOwnerStatusResponse(t, s, adapter, jobID)
}

// TestRunningJobStatusReadLeavesNotificationArmed is the other half of the
// rule: reading a job that has NOT ended teaches the caller nothing about its
// completion, so the terminal notification stays armed.
func TestRunningJobStatusReadLeavesNotificationArmed(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	jobID := startBackgroundShellJob(t, s, "sleep 30")
	t.Cleanup(func() { _, _ = s.jobManager.stop(jobID) })

	if _, err := jobStatusTool(s, map[string]any{"target": jobID}, 8000); err != nil {
		t.Fatalf("job_status: %v", err)
	}

	rec := loadShellRecord(t, s.jobManager, jobID)
	if rec.NotifyState == jobstore.NotifyConsumed {
		t.Fatal("a running job's status read must not consume its terminal notification")
	}
}

// keptForegroundShellCall runs a real foreground shell whose output is too big
// to ride inline, so the tool keeps it as a durable job and returns a digest
// with its job_id and terminal status.
func keptForegroundShellCall(t *testing.T, s *Session) (llm.ToolCallData, tooldefs.ExecResult, string) {
	t.Helper()
	call := llm.ToolCallData{
		ID:        "kept",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"head -c 70000 </dev/zero | tr '\\0' 'x'"}`),
	}
	result := s.reg.ExecuteCall(context.Background(), s.env, call)
	if result.IsError {
		t.Fatalf("shell: %s", result.Output)
	}
	var out struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(toolResultJSON(result), &out); err != nil || out.JobID == "" {
		t.Fatalf("the large-output command was not kept as a job: %v (%s)", err, result.Output)
	}
	return call, result, out.JobID
}

// TestPersistedKeptForegroundShellIsNotRenotifiedOnRestore pins #4290. A
// kept foreground shell already told the caller, in its own tool reply, that
// the job ended. Once that reply is durable the job's notification is
// consumed, so a restore does not re-announce it and wake the session for a
// turn nobody asked for.
func TestPersistedKeptForegroundShellIsNotRenotifiedOnRestore(t *testing.T) {
	t.Parallel()
	s := newQueuePersistTestSession(t, t.TempDir())
	call, result, jobID := keptForegroundShellCall(t, s)
	if err := s.persistToolResults(context.Background(), []llm.ToolCallData{call}, []tooldefs.ExecResult{result}); err != nil {
		t.Fatalf("persistToolResults: %v", err)
	}
	s.Close()

	meta, err := schema.LoadSessionMeta(s.stateDir, s.id)
	if err != nil {
		t.Fatalf("load meta: %v", err)
	}
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	restored, err := RestoreSessionFromMetaWithConfig(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{StateDir: s.stateDir})
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	t.Cleanup(func() { restored.Close() })
	restored.mu.Lock()
	_, replied := findToolResultInHistory(restored.history, call.ID)
	restored.mu.Unlock()
	if !replied {
		t.Fatal("the kept shell's reply did not survive the restore")
	}
	if rec := loadShellRecord(t, restored.jobManager, jobID); rec.NotifyState != jobstore.NotifyConsumed {
		t.Fatalf("restored terminal_notification_state = %q, want %q", rec.NotifyState, jobstore.NotifyConsumed)
	}
	if got := restored.peekNotifications(); got != 0 {
		t.Fatalf("restore queued %d notification(s) for a result the caller already read", got)
	}
}

// TestUnpersistedKeptForegroundShellIsRenotifiedOnRestore pins the other side
// of #4290's fix: when the process stops before the kept shell's reply reaches
// the transcript, the caller never learned the job ended, so a restore still
// arms its notification.
func TestUnpersistedKeptForegroundShellIsRenotifiedOnRestore(t *testing.T) {
	t.Parallel()
	s := newQueuePersistTestSession(t, t.TempDir())
	call, _, jobID := keptForegroundShellCall(t, s)
	if err := s.jobManager.armPendingTerminalNotifications(); err != nil {
		t.Fatal(err)
	}
	id := requireCanonicalShellNotification(t, s, jobID, 0)
	s.Close()
	meta, err := schema.LoadSessionMeta(s.stateDir, s.id)
	if err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{name: "openai"}
	client := llm.NewClient()
	client.Register(adapter)
	restored, err := RestoreSessionFromMetaWithConfig(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{StateDir: s.stateDir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restored.Close)
	if _, found := findToolResultInHistory(restored.history, call.ID); found {
		t.Fatal("unpersisted foreground reply survived restore")
	}
	if got := requireCanonicalShellNotification(t, restored, jobID, 0); got != id {
		t.Fatalf("restored ID=%q want %q", got, id)
	}
	adapter.steps = []func(llm.Request) llm.Response{func(req llm.Request) llm.Response {
		observationRequest(t, req, []string{fmt.Sprintf(`job_id=%q`, jobID)}, nil)
		requireCanonicalShellNotification(t, restored, jobID, 0)
		return finalResponse("restored foreground completion observed")
	}}
	if _, err := restored.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	requireCanonicalShellNotification(t, restored, jobID, 1)
	if _, err := restored.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	if len(adapter.Requests()) != 1 {
		t.Fatalf("provider requests=%d want 1", len(adapter.Requests()))
	}
}

// TestKeptForegroundShellTranscriptFailureLeavesNotificationUnsettled pins
// that the settlement waits for a reply that is actually durable: when the
// transcript write of a kept shell's reply fails, the caller never got it, so
// the job is left for restore to arm.
func TestKeptForegroundShellTranscriptFailureLeavesNotificationUnsettled(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	call, result, jobID := keptForegroundShellCall(t, s)

	fs := &transcriptWriteFailFS{Fs: afero.NewMemMapFs()}
	writer, err := transcript.NewWriterWithFS(fs, "/session.jsonl", transcript.Header{SessionID: s.ID()})
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	s.transcript = writer
	fs.fail = true

	err = s.persistToolResults(context.Background(), []llm.ToolCallData{call}, []tooldefs.ExecResult{result})
	if !errors.Is(err, errInjectedTranscriptWrite) {
		t.Errorf("persistToolResults error = %v, want %v", err, errInjectedTranscriptWrite)
	}
	if rec := loadShellRecord(t, s.jobManager, jobID); rec.NotifyState != jobstore.NotifyNotArmed {
		t.Fatalf("terminal_notification_state = %q after a failed reply write, want %q", rec.NotifyState, jobstore.NotifyNotArmed)
	}
}

// TestKeptForegroundShellUnsyncedReplyLeavesNotificationUnsettled pins that a
// reply whose line reached the file but could not be synced (fsync and the
// rollback both failed, so the durable append retains it and reports success)
// does not settle the job: a crash could still lose that line, and restore
// must then be free to arm the notification.
func TestKeptForegroundShellUnsyncedReplyLeavesNotificationUnsettled(t *testing.T) {
	t.Parallel()
	s := newQueuePersistTestSession(t, t.TempDir())
	t.Cleanup(func() { s.Close() })
	call, result, jobID := keptForegroundShellCall(t, s)

	fs := attachEnvironmentFailureFS(t, s)
	syncFailure := errors.New("kept shell reply sync failure")
	fs.mu.Lock()
	fs.failure = syncFailure
	fs.rollbackFailure = errors.New("kept shell reply rollback failure")
	fs.onFailure = func() {
		fs.mu.Lock()
		fs.failure = syncFailure
		fs.mu.Unlock()
	}
	fs.mu.Unlock()

	_ = s.persistToolResults(context.Background(), []llm.ToolCallData{call}, []tooldefs.ExecResult{result})
	if rec := loadShellRecord(t, s.jobManager, jobID); rec.NotifyState != jobstore.NotifyNotArmed {
		t.Fatalf("terminal_notification_state = %q after an unsynced reply, want %q", rec.NotifyState, jobstore.NotifyNotArmed)
	}
}

// TestBackgroundShellReplyWithTerminalStatusLeavesNotificationPending pins
// that only a foreground completion settles a job's notification. A
// foreground command whose runtime timeout could not be finalized replies
// status failed / finalize_failed in background mode, which tells the caller
// the job carries on and will notify; if the retried finalization lands before
// that reply is persisted, its completion notification must stay pending.
func TestBackgroundShellReplyWithTerminalStatusLeavesNotificationPending(t *testing.T) {
	t.Parallel()
	for _, beforeCustody := range []bool{false, true} {
		t.Run(fmt.Sprintf("before_custody=%t", beforeCustody), func(t *testing.T) {
			s, adapter := newStatusNotificationSession(t, true)
			var jobID, id string
			if beforeCustody {
				// A real kept foreground completion is terminal but not yet armed.
				// Persisting a background reply must not consume this source either.
				_, _, jobID = keptForegroundShellCall(t, s)
				rec := loadShellRecord(t, s.jobManager, jobID)
				if !rec.Status.IsTerminal() || rec.TerminalGen == "" || rec.NotifyState != jobstore.NotifyNotArmed {
					t.Fatalf("kept terminal source = %+v, want unarmed terminal generation", rec)
				}
				id = "shell:" + jobID + ":" + rec.TerminalGen
				assertNotificationPending(t, s, []string{})
			} else {
				watcher := newJobNotifyWatcher(s, nil)
				jobID = startBackgroundShellJob(t, s, "printf 'finishes later\\n'")
				id = awaitCanonicalShellCustody(t, watcher, s, jobID)
			}

			reply, err := marshalShellToolResult(shellResult{
				JobID:               jobID,
				Type:                string(jobstore.JobShell),
				Status:              string(jobstore.StatusFailed),
				Reason:              "finalize_failed",
				RunningInBackground: true,
			}, 100000, false)
			if err != nil {
				t.Fatal(err)
			}
			state, err := json.Marshal(reply.State)
			if err != nil {
				t.Fatal(err)
			}
			call := llm.ToolCallData{ID: "fg-timeout", Name: "shell", Arguments: json.RawMessage(`{"command":"sleep 999"}`)}
			result := tooldefs.ExecResult{ToolName: "shell", CallID: call.ID, Output: reply.Output, ToolState: state}
			if err := s.persistToolResults(context.Background(), []llm.ToolCallData{call}, []tooldefs.ExecResult{result}); err != nil {
				t.Fatalf("persistToolResults: %v", err)
			}
			if beforeCustody {
				if rec := loadShellRecord(t, s.jobManager, jobID); rec.NotifyState != jobstore.NotifyNotArmed {
					t.Fatalf("terminal_notification_state = %q after durable background reply, want %q", rec.NotifyState, jobstore.NotifyNotArmed)
				}
				assertNotificationPending(t, s, []string{})
				persisted := false
				for _, entry := range notificationRawEntries(t, s.TranscriptPath()) {
					if entry.Turn.AttentionID == id || slices.Contains(entry.Turn.ObservedNotificationIDs, id) {
						t.Fatal("unarmed completion was accepted or observed before arming")
					}
					if _, found := findToolResultInHistory([]schema.Turn{entry.Turn}, call.ID); found {
						persisted = true
					}
				}
				if !persisted {
					t.Fatal("background reply did not reach the disk transcript")
				}
				if err := s.jobManager.armPendingTerminalNotifications(); err != nil {
					t.Fatal(err)
				}
				if got := requireCanonicalShellNotification(t, s, jobID, 0); got != id {
					t.Fatalf("armed ID=%q want original terminal ID %q", got, id)
				}
			}
			requireCanonicalShellNotification(t, s, jobID, 0)
			assertNotificationPending(t, s, []string{id})
			adapter.steps = []func(llm.Request) llm.Response{func(req llm.Request) llm.Response {
				observationRequest(t, req, []string{fmt.Sprintf(`job_id=%q`, jobID)}, nil)
				requireCanonicalShellNotification(t, s, jobID, 0)
				return finalResponse("later completion observed")
			}}
			if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
				t.Fatal(err)
			}
			requireCanonicalShellNotification(t, s, jobID, 1)
			if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
				t.Fatal(err)
			}
			if len(adapter.Requests()) != 1 {
				t.Fatalf("provider requests=%d want 1", len(adapter.Requests()))
			}
		})
	}
}

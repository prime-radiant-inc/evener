package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

// TestTerminalJobStatusReadConsumesPendingNotification pins the consume rule: a
// caller that reads a terminal job_status has LEARNED the job ended, so the
// queued terminal notification must not interrupt it later with the same news.
//
// The learning is durable and has its own terminal-notification state
// ("consumed"), distinct from "delivered": the jobstore's told-the-caller
// invariant stays truthful, and forensics can still tell a notification the
// model was shown from one the model went and looked up itself.
func TestPersistedTerminalJobStatusReadConsumesPendingNotification(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	watcher := newJobNotifyWatcher(s, nil)
	jobID := startBackgroundShellJob(t, s, "printf 'consume me\\n'")

	watcher.await(t, "terminal notification queued", func() bool {
		return s.peekNotifications() > 0
	})

	call := llm.ToolCallData{
		ID:        "status",
		Name:      "job_status",
		Arguments: json.RawMessage(`{"target":` + mustJSONString(jobID) + `}`),
	}
	result := s.reg.ExecuteCall(context.Background(), s.env, call)
	if result.IsError {
		t.Fatalf("job_status: %s", result.Output)
	}

	rec := loadShellRecord(t, s.jobManager, jobID)
	if !rec.Status.IsTerminal() {
		t.Fatalf("job status = %q, want terminal before asserting the consume", rec.Status)
	}
	if rec.NotifyState != jobstore.NotifyPending {
		t.Fatalf("terminal_notification_state before result persistence = %q, want %q",
			rec.NotifyState, jobstore.NotifyPending)
	}
	if err := s.persistToolResults(context.Background(), []llm.ToolCallData{call}, []tooldefs.ExecResult{result}); err != nil {
		t.Fatalf("persistToolResults: %v", err)
	}

	rec = loadShellRecord(t, s.jobManager, jobID)
	if rec.NotifyState != jobstore.NotifyConsumed {
		t.Fatalf("terminal_notification_state = %q, want %q after a terminal job_status read",
			rec.NotifyState, jobstore.NotifyConsumed)
	}
	if got := s.peekNotifications(); got != 0 {
		t.Fatalf("pending notifications = %d, want 0 after persisted terminal status", got)
	}

	// The session goes idle and takes its notification turn: there must be
	// nothing left to tell it about this job.
	if proceed := s.acceptNotificationInput(context.Background(), ""); proceed {
		t.Fatal("a notification turn ran after the caller already read the terminal status")
	}
}

func TestPersistedTerminalJobStatusPreservesQueuedSelfWatchNotification(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	if _, err := jobWatchTool(s, map[string]any{
		"operation": "create",
		"source":    "self",
		"events":    []any{"job.notification"},
	}, jobToolResultDefaultMaxChar); err != nil {
		t.Fatalf("job_watch create: %v", err)
	}

	watcher := newJobNotifyWatcher(s, nil)
	gate := filepath.Join(t.TempDir(), "release")
	jobID := startBackgroundShellJob(t, s, "while [ ! -f "+gate+" ]; do sleep 0.02; done; printf 'watched done\\n'")
	if err := os.WriteFile(gate, []byte("go\n"), 0o600); err != nil {
		t.Fatalf("release job: %v", err)
	}
	waitForShellDone(t, s.jobManager, jobID)
	watcher.await(t, "self watch and terminal notification queued", func() bool {
		s.pendingJobNotifsMu.Lock()
		defer s.pendingJobNotifsMu.Unlock()
		if len(s.pendingJobNotifs) != 2 {
			return false
		}
		for _, notification := range s.pendingJobNotifs {
			if notification.JobID != jobID {
				return false
			}
		}
		return true
	})

	call := llm.ToolCallData{
		ID:        "status",
		Name:      "job_status",
		Arguments: json.RawMessage(`{"target":` + mustJSONString(jobID) + `}`),
	}
	result := s.reg.ExecuteCall(context.Background(), s.env, call)
	if result.IsError {
		t.Fatalf("job_status: %s", result.Output)
	}
	if err := s.persistToolResults(context.Background(), []llm.ToolCallData{call}, []tooldefs.ExecResult{result}); err != nil {
		t.Fatalf("persistToolResults: %v", err)
	}

	rec := loadShellRecord(t, s.jobManager, jobID)
	if rec.NotifyState != jobstore.NotifyConsumed {
		t.Errorf("terminal_notification_state = %q, want %q after owner status read",
			rec.NotifyState, jobstore.NotifyConsumed)
	}
	if got := s.peekNotifications(); got != 1 {
		t.Errorf("pending notifications = %d, want the self-watch notification to remain", got)
	}

	historyLen := len(s.history)
	if proceed := s.acceptNotificationInput(context.Background(), ""); !proceed {
		t.Error("self-watch notification was not accepted after terminal status consumption")
	}
	if got := len(s.history); got != historyLen+1 {
		t.Errorf("history turns = %d, want %d after self-watch delivery", got, historyLen+1)
	} else if s.history[got-1].Kind != schema.TurnSteering {
		t.Errorf("delivered self-watch turn kind = %q, want %q", s.history[got-1].Kind, schema.TurnSteering)
	}
}

func TestTerminalJobStatusTranscriptFailureLeavesNotificationPending(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	watcher := newJobNotifyWatcher(s, nil)
	jobID := startBackgroundShellJob(t, s, "printf 'persist me\\n'")

	watcher.await(t, "terminal notification queued", func() bool {
		return s.peekNotifications() > 0
	})

	fs := &transcriptWriteFailFS{Fs: afero.NewMemMapFs()}
	writer, err := transcript.NewWriterWithFS(fs, "/session.jsonl", transcript.Header{SessionID: s.ID()})
	if err != nil {
		t.Fatalf("NewWriterWithFS: %v", err)
	}
	s.transcript = writer
	fs.fail = true

	call := llm.ToolCallData{
		ID:        "status",
		Name:      "job_status",
		Arguments: json.RawMessage(`{"target":` + mustJSONString(jobID) + `}`),
	}
	result := s.reg.ExecuteCall(context.Background(), s.env, call)
	if result.IsError {
		t.Fatalf("job_status: %s", result.Output)
	}

	historyLen := len(s.history)
	err = s.persistToolResults(context.Background(), []llm.ToolCallData{call}, []tooldefs.ExecResult{result})
	if !errors.Is(err, errInjectedTranscriptWrite) {
		t.Errorf("persistToolResults error = %v, want %v", err, errInjectedTranscriptWrite)
	}

	rec := loadShellRecord(t, s.jobManager, jobID)
	if rec.NotifyState != jobstore.NotifyPending {
		t.Errorf("terminal_notification_state = %q, want %q after transcript failure",
			rec.NotifyState, jobstore.NotifyPending)
	}
	if got := s.peekNotifications(); got != 1 {
		t.Errorf("pending notifications = %d, want 1 after transcript failure", got)
	}
	if got := len(s.history); got != historyLen {
		t.Errorf("history turns = %d, want %d after transcript failure", got, historyLen)
	}
	if _, ok := findToolResultInHistory(s.history, call.ID); ok {
		t.Error("failed terminal job_status result entered live history")
	}
}

func TestParentTerminalJobStatusReadLeavesChildNotificationPending(t *testing.T) {
	t.Parallel()
	parent := newTestSession(t)
	child := newTestSession(t)
	child.jobManager.forward = parent.jobManager.forwardEvent
	child.jobManager.setParentJobID("job_PARENT")
	parent.subagents.track(&subagent{id: child.ID(), sess: child, status: SubagentRunning})

	watcher := newJobNotifyWatcher(child, nil)
	jobID := startBackgroundShellJob(t, child, "printf 'child done\\n'")
	watcher.await(t, "child terminal notification queued", func() bool {
		return child.peekNotifications() > 0
	})

	call := llm.ToolCallData{
		ID:        "status",
		Name:      "job_status",
		Arguments: json.RawMessage(`{"target":` + mustJSONString(jobID) + `}`),
	}
	result := parent.reg.ExecuteCall(context.Background(), parent.env, call)
	if result.IsError {
		t.Fatalf("parent job_status: %s", result.Output)
	}
	if err := parent.persistToolResults(context.Background(), []llm.ToolCallData{call}, []tooldefs.ExecResult{result}); err != nil {
		t.Fatalf("persistToolResults: %v", err)
	}

	rec := loadShellRecord(t, child.jobManager, jobID)
	if rec.NotifyState != jobstore.NotifyPending {
		t.Errorf("child terminal_notification_state = %q, want %q after parent status read",
			rec.NotifyState, jobstore.NotifyPending)
	}
	if got := child.peekNotifications(); got != 1 {
		t.Errorf("child pending notifications = %d, want 1 after parent status read", got)
	}
}

func TestTerminalJobStatusReadContinuesCurrentTurn(t *testing.T) {
	t.Parallel()

	client := llm.NewClient()
	adapter := &fakeAdapter{name: "openai"}
	client.Register(adapter)
	s, err := NewSession(
		client,
		NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()),
		SessionConfig{
			testOnly: testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
			},
		},
	)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	watcher := newJobNotifyWatcher(s, nil)
	jobID := startBackgroundShellJob(t, s, "printf 'done\\n'")
	watcher.await(t, "terminal notification queued", func() bool {
		return s.peekNotifications() > 0
	})

	adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Message{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{{
					Kind: llm.ContentToolCall,
					ToolCall: &llm.ToolCallData{
						ID:        "status",
						Name:      "job_status",
						Arguments: json.RawMessage(`{"target":` + mustJSONString(jobID) + `}`),
						Type:      "function",
					},
				}},
			}}
		},
		func(llm.Request) llm.Response { return finalResponse("continued") },
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	output, err := s.ProcessInput(ctx, "finish after checking the job", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if output != "continued" {
		t.Fatalf("ProcessInput output = %q, want continued", output)
	}
	if got := len(adapter.Requests()); got != 2 {
		t.Fatalf("provider requests = %d, want 2", got)
	}
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
	s := newTestSession(t)
	_, _, jobID := keptForegroundShellCall(t, s)
	if err := s.jobManager.armPendingTerminalNotifications(); err != nil {
		t.Fatalf("arm pending: %v", err)
	}
	if rec := loadShellRecord(t, s.jobManager, jobID); rec.NotifyState != jobstore.NotifyPending {
		t.Fatalf("terminal_notification_state = %q, want %q for a reply that never persisted", rec.NotifyState, jobstore.NotifyPending)
	}
	if got := s.peekNotifications(); got != 1 {
		t.Fatalf("restore queued %d notification(s), want 1 for a reply that never persisted", got)
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
	s := newTestSession(t)
	watcher := newJobNotifyWatcher(s, nil)
	jobID := startBackgroundShellJob(t, s, "printf 'finishes later\\n'")
	watcher.await(t, "terminal notification queued", func() bool {
		return s.peekNotifications() > 0
	})

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
	if rec := loadShellRecord(t, s.jobManager, jobID); rec.NotifyState != jobstore.NotifyPending {
		t.Fatalf("terminal_notification_state = %q after a background reply, want %q", rec.NotifyState, jobstore.NotifyPending)
	}
}

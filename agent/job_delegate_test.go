package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

func newDelegateTestSession(t *testing.T, c *llm.Client) *Session {
	t.Helper()
	return newSession(t, withClient(c), withConfig(SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
	}))
}

func newDelegateRestorePreflightSession(t *testing.T, c *llm.Client) *Session {
	t.Helper()
	return newSession(t, withClient(c), withConfig(SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
	}))
}

func seedStoppedDelegateRestoreRecord(t *testing.T, s *Session) *jobstore.JobRecord {
	t.Helper()
	childID, childWorkDir := seedRetainedChildSessionWithWorkingDir(t, s)
	delegateID := jobstore.NewDelegateID()
	generation := jobstore.NewDelegateGeneration()
	jobID := jobstore.NewJobID()
	now := time.Now().UTC()
	ref := encodeRef("", childID)
	desc := &jobstore.DelegateRestoreDescriptor{
		Version:           1,
		ChildSessionID:    childID,
		TranscriptRef:     ref,
		ParentSessionID:   s.ID(),
		ParentJobID:       jobID,
		OwnerSessionID:    s.ID(),
		VisibleSessionID:  s.ID(),
		Task:              "retained delegate",
		ResolvedProfileID: "openai",
		ResolvedModel:     "gpt-5.2",
		WorkingDir:        childWorkDir,
		LocalEnvPolicy:    "default",
	}
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind:       jobstore.EventDelegateCreated,
		TS:         now,
		DelegateID: delegateID,
		Delegate: &jobstore.DelegateEvent{
			ChildSessionID:   childID,
			TranscriptRef:    ref,
			OwnerSessionID:   s.ID(),
			VisibleSessionID: s.ID(),
			Generation:       generation,
			Resumable:        true,
		},
	}); err != nil {
		t.Fatalf("append delegate created: %v", err)
	}
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               now,
		JobID:            jobID,
		DelegateID:       delegateID,
		Type:             jobstore.JobDelegate,
		Task:             desc.Task,
		OwnerSessionID:   s.ID(),
		VisibleToSession: s.ID(),
		StartedAt:        &now,
		TranscriptRef:    ref,
		DelegateRestore:  desc,
	}); err != nil {
		t.Fatalf("append delegate start: %v", err)
	}
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          now,
		JobID:       jobID,
		Status:      jobstore.StatusStopped,
		Reason:      "runtime_lost",
		EndedAt:     &now,
		TerminalGen: jobstore.NewWatchGeneration(),
	}); err != nil {
		t.Fatalf("append delegate stopped: %v", err)
	}
	return loadShellRecord(t, s.jobManager, jobID)
}

func markStoredDelegateResumable(t *testing.T, s *Session, rec *jobstore.JobRecord) {
	t.Helper()
	resumable := true
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind:          jobstore.EventJobSessionAssigned,
		TS:            time.Now().UTC(),
		JobID:         rec.JobID,
		TranscriptRef: rec.TranscriptRef,
		Resumable:     &resumable,
	}); err != nil {
		t.Fatalf("append delegate resumable assignment: %v", err)
	}
}

func setStoredDelegateTerminalStatus(t *testing.T, s *Session, rec *jobstore.JobRecord, status jobstore.Status, reason string) {
	t.Helper()
	events := loadJobStoreEvents(t, s.jobManager)
	for i := range events {
		if events[i].JobID == rec.JobID && events[i].Kind == jobstore.EventJobFinished {
			events[i].Status = status
			events[i].Reason = reason
			rewriteJobStoreEvents(t, s.jobManager, events)
			return
		}
	}
	t.Fatalf("terminal event for delegate job %s not found", rec.JobID)
}

func requireDelegateRestorePreflight(t *testing.T, s *Session, rec *jobstore.JobRecord) *delegateRestorePreflight {
	t.Helper()
	assessment := s.assessDelegateResumability(rec, delegateResumabilityPreflight)
	if !assessment.Resumable || assessment.Preflight == nil {
		t.Fatalf("delegate restore preflight = %+v, want resumable with preflight", assessment)
	}
	return assessment.Preflight
}

func seedRetainedChildSessionWithWorkingDir(t *testing.T, parent *Session) (string, string) {
	t.Helper()
	workDir := t.TempDir()
	cfg := SessionConfig{
		StateDir:         parent.stateDir,
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
	}
	cfg.spawn.depth = parent.depth + 1
	cfg.spawn.parentSessionID = parent.ID()
	child, err := NewSession(parent.client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), cfg)
	if err != nil {
		t.Fatalf("NewSession child: %v", err)
	}
	if child.transcript != nil {
		if err := child.transcript.Close(); err != nil {
			t.Fatalf("close child transcript: %v", err)
		}
		child.transcript = nil
	}
	t.Cleanup(func() { child.Close() })
	return child.ID(), workDir
}

func replaceStoredDelegateRecord(t *testing.T, s *Session, rec *jobstore.JobRecord) {
	t.Helper()
	events := loadJobStoreEvents(t, s.jobManager)
	for i := range events {
		if events[i].JobID == rec.JobID && events[i].Kind == jobstore.EventJobStarted {
			events[i].DelegateRestore = rec.DelegateRestore
			events[i].TranscriptRef = rec.TranscriptRef
			events[i].OwnerSessionID = rec.OwnerSessionID
			events[i].VisibleToSession = rec.VisibleToSession
			break
		}
	}
	rewriteJobStoreEvents(t, s.jobManager, events)
}

func rewriteJobStoreEvents(t *testing.T, jm *jobManager, events []jobstore.Event) {
	t.Helper()
	path := filepath.Join(jm.dir, "jobs.jsonl")
	var lines []string
	for _, event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatalf("marshal job event: %v", err)
		}
		lines = append(lines, string(data))
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("rewrite jobs.jsonl: %v", err)
	}
}

func childSessionMetaPath(s *Session, rec *jobstore.JobRecord) string {
	return filepath.Join(s.stateDir, sessionsSubdir, rec.DelegateRestore.ChildSessionID+".meta.json")
}

func childTranscriptPath(s *Session, rec *jobstore.JobRecord) string {
	return filepath.Join(s.stateDir, sessionsSubdir, rec.DelegateRestore.ChildSessionID+".transcript.jsonl")
}

func removeChildSessionMeta(t *testing.T, s *Session, rec *jobstore.JobRecord) {
	t.Helper()
	if err := os.Remove(childSessionMetaPath(s, rec)); err != nil {
		t.Fatalf("remove child meta: %v", err)
	}
}

func writeChildSessionMeta(t *testing.T, s *Session, rec *jobstore.JobRecord, data []byte) {
	t.Helper()
	if err := os.WriteFile(childSessionMetaPath(s, rec), data, 0o644); err != nil {
		t.Fatalf("write child meta: %v", err)
	}
}

func removeChildTranscript(t *testing.T, s *Session, rec *jobstore.JobRecord) {
	t.Helper()
	if err := os.Remove(childTranscriptPath(s, rec)); err != nil {
		t.Fatalf("remove child transcript: %v", err)
	}
}

func writeChildTranscript(t *testing.T, s *Session, rec *jobstore.JobRecord, data []byte) {
	t.Helper()
	if err := os.WriteFile(childTranscriptPath(s, rec), data, 0o644); err != nil {
		t.Fatalf("write child transcript: %v", err)
	}
}

func appendChildTranscript(t *testing.T, s *Session, rec *jobstore.JobRecord, data string) {
	t.Helper()
	f, err := os.OpenFile(childTranscriptPath(s, rec), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open child transcript: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(data); err != nil {
		t.Fatalf("append child transcript: %v", err)
	}
}

func appendChildTranscriptTurn(t *testing.T, s *Session, rec *jobstore.JobRecord, turn schema.Turn) {
	t.Helper()
	entry := transcript.Entry{
		Kind: "entry",
		Seq:  1,
		Turn: turn,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal child transcript entry: %v", err)
	}
	appendChildTranscript(t, s, rec, string(data)+"\n")
}

func appendSessionJobEvents(t *testing.T, stateDir, sessionID string, events ...jobstore.Event) {
	t.Helper()
	store, err := jobstore.Open(filepath.Join(jobsDir(stateDir, sessionID), "jobs.jsonl"))
	if err != nil {
		t.Fatalf("open session job store: %v", err)
	}
	defer store.Close()
	for _, event := range events {
		if err := store.Append(event); err != nil {
			t.Fatalf("append session job event %s: %v", event.Kind, err)
		}
	}
}

func appendChildCompletedJobNeedingNotification(t *testing.T, stateDir, childID, jobID string, ts time.Time) {
	t.Helper()
	terminalGen := jobstore.NewTerminalGeneration()
	endedAt := ts.Add(time.Millisecond)
	appendSessionJobEvents(t, stateDir, childID,
		jobstore.Event{
			Kind:             jobstore.EventJobStarted,
			TS:               ts,
			JobID:            jobID,
			Type:             jobstore.JobShell,
			Command:          "true",
			Description:      "completed child job needing notification arm",
			OwnerSessionID:   childID,
			VisibleToSession: childID,
			StartedAt:        &ts,
		},
		jobstore.Event{
			Kind:        jobstore.EventJobFinished,
			TS:          ts.Add(time.Millisecond),
			JobID:       jobID,
			Status:      jobstore.StatusCompleted,
			Reason:      "exit_zero",
			EndedAt:     &endedAt,
			TerminalGen: terminalGen,
		},
	)
}

func loadSessionJobStoreEvents(t *testing.T, stateDir, sessionID string) []jobstore.Event {
	t.Helper()
	path := filepath.Join(jobsDir(stateDir, sessionID), "jobs.jsonl")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read session jobs.jsonl: %v", err)
	}
	var events []jobstore.Event
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event jobstore.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("parse session job event %q: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

func loadSessionWatchSendRecord(t *testing.T, stateDir, sessionID string) jobstore.WatchSendRecord {
	t.Helper()
	return jobstore.FoldWatchSends(loadSessionJobStoreEvents(t, stateDir, sessionID))
}

func hasWatchSendEvent(events []jobstore.Event, kind jobstore.EventKind, deliveryID string) bool {
	for _, event := range events {
		if event.Kind == kind && event.WatchSend != nil && event.WatchSend.DeliveryID == deliveryID {
			return true
		}
	}
	return false
}

func hasJobEvent(events []jobstore.Event, kind jobstore.EventKind, jobID string) bool {
	for _, event := range events {
		if event.Kind == kind && event.JobID == jobID {
			return true
		}
	}
	return false
}

func sessionStartHookPlugin(t *testing.T, command string) string {
	t.Helper()
	dir := makePluginDir(t, "delegate-restore-hook")
	hooksDir := filepath.Join(dir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		t.Fatalf("mkdir hooks: %v", err)
	}
	raw := map[string]any{
		"hooks": map[string]any{
			"SessionStart": []any{
				map[string]any{
					"matcher": "resume",
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": command,
						},
					},
				},
			},
		},
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("marshal hooks: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "hooks.json"), data, 0o644); err != nil {
		t.Fatalf("write hooks: %v", err)
	}
	return dir
}

func countSteeringEntriesContaining(s *Session, text string) int {
	return len(steeringEntriesContaining(s, text))
}

func drainEventWarningsContaining(s *Session, text string) int {
	count := 0
	for {
		select {
		case ev := <-s.Events():
			if ev.Kind != events.EventWarning {
				continue
			}
			if data, ok := ev.Data.(events.WarningData); ok && strings.Contains(data.Message, text) {
				count++
			}
		default:
			return count
		}
	}
}

func newPersistentTestSession(t *testing.T) *Session {
	t.Helper()
	return newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		StateDir:         t.TempDir(),
	}))
}

func waitForSteeringEntryContaining(t *testing.T, s *Session, text string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if entries := steeringEntriesContaining(s, text); len(entries) > 0 {
			return entries[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no steering entry containing %q; queue = %+v", text, s.SteeringQueueSnapshot())
	return ""
}

// waitForJobNotification blocks until at least one job notification (e.g. a
// caller watch-send wake token enqueued by asynchronous observation) is pending,
// so a test can then accept it the way the live loop would on wake.
func waitForJobNotification(t *testing.T, s *Session) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s.peekNotifications() > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no job notification became pending within deadline")
}

func steeringEntriesContaining(s *Session, text string) []string {
	var entries []string
	for _, entry := range s.SteeringQueueSnapshot() {
		if strings.Contains(entry.Text, text) {
			entries = append(entries, entry.Text)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, turn := range s.history {
		if turn.Kind == schema.TurnSteering && strings.Contains(turn.Message.Text(), text) {
			entries = append(entries, turn.Message.Text())
		}
	}
	return entries
}

func countFileLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) != "" {
			count++
		}
	}
	return count
}

func completedDelegateSubagent(child *Session, result string) *subagent {
	return &subagent{
		id:      child.ID(),
		sess:    child,
		running: false,
		status:  SubagentCompleted,
		result:  result,
		done:    make(chan struct{}),
	}
}

func runningDelegateJob(t *testing.T, jm *jobManager, jobID string) *runningJob {
	t.Helper()
	jm.mu.Lock()
	defer jm.mu.Unlock()
	run := jm.running[jobID]
	if run == nil {
		t.Fatalf("delegate job %s is not running", jobID)
	}
	return run
}

func waitForRunningDelegateJob(t *testing.T, jm *jobManager) *runningJob {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		jm.mu.Lock()
		for _, run := range jm.running {
			if run.rec.Type == jobstore.JobDelegate {
				jm.mu.Unlock()
				return run
			}
		}
		jm.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting for running delegate job")
	return nil
}

type cancelAwareDelegateAdapter struct {
	name    string
	started chan struct{}
	once    sync.Once
}

func (a *cancelAwareDelegateAdapter) Name() string { return a.name }

func (a *cancelAwareDelegateAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.once.Do(func() { close(a.started) })
	<-ctx.Done()
	return llm.Response{Provider: a.name, Model: req.Model}, ctx.Err()
}

func (a *cancelAwareDelegateAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, llm.ErrStreamUnsupported
}

type resumeBlockingDelegateAdapter struct {
	name          string
	secondStarted chan struct{}
	once          sync.Once
	mu            sync.Mutex
	calls         int
}

func (a *resumeBlockingDelegateAdapter) Name() string { return a.name }

func (a *resumeBlockingDelegateAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	a.calls++
	call := a.calls
	a.mu.Unlock()

	if call == 1 {
		resp := communicateWithDefaultOutput("first complete")
		resp.Provider = a.name
		if resp.Model == "" {
			resp.Model = req.Model
		}
		return resp, nil
	}

	a.once.Do(func() { close(a.secondStarted) })
	<-ctx.Done()
	return llm.Response{Provider: a.name, Model: req.Model}, ctx.Err()
}

func (a *resumeBlockingDelegateAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, llm.ErrStreamUnsupported
}

func communicateWithStructured(message string, output map[string]any) llm.Response {
	args, _ := json.Marshal(map[string]any{
		"message":  message,
		"end_turn": true,
		"output":   output,
	})
	return toolCallResponse(llm.ToolCallData{
		ID:        "delegate_communicate",
		Name:      "communicate",
		Arguments: args,
		Type:      "function",
	})
}

func communicateWithoutStructured(message string) llm.Response {
	args, _ := json.Marshal(map[string]any{
		"message":  message,
		"end_turn": true,
		"output": map[string]any{
			"message":   "",
			"data":      map[string]any{},
			"artifacts": []string{},
		},
	})
	return toolCallResponse(llm.ToolCallData{
		ID:        "delegate_communicate",
		Name:      "communicate",
		Arguments: args,
		Type:      "function",
	})
}

func communicateWithDefaultOutput(message string) llm.Response {
	return communicateWithStructured(message, map[string]any{
		"message":   message,
		"data":      map[string]any{},
		"artifacts": []string{},
	})
}

func requestHasTool(req llm.Request, name string) bool {
	for _, td := range req.Tools {
		if td.Name == name {
			return true
		}
	}
	return false
}

func communicateOutputSchemaHasProperty(req llm.Request, property string) bool {
	for _, td := range req.Tools {
		if td.Name != "communicate" {
			continue
		}
		params, ok := td.Parameters["properties"].(map[string]any)
		if !ok {
			return false
		}
		output, ok := params["output"].(map[string]any)
		if !ok {
			return false
		}
		props, ok := output["properties"].(map[string]any)
		if !ok {
			return false
		}
		_, ok = props[property]
		return ok
	}
	return false
}

func requestMessagesContain(req llm.Request, text string) bool {
	for _, msg := range req.Messages {
		if strings.Contains(msg.Text(), text) {
			return true
		}
	}
	return false
}

func countRequestMessagesContaining(req llm.Request, text string) int {
	var count int
	for _, msg := range req.Messages {
		if strings.Contains(msg.Text(), text) {
			count++
		}
	}
	return count
}

func lastUserMessageText(req llm.Request) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == llm.RoleUser {
			return strings.TrimSpace(req.Messages[i].Text())
		}
	}
	return ""
}

func findJobByDescription(t *testing.T, jm *jobManager, description string) *jobstore.JobRecord {
	t.Helper()
	jobs := jm.list(listFilter{IncludeNested: true})
	for _, rec := range jobs {
		if rec.Description == description {
			return rec
		}
	}
	t.Fatalf("jobs = %+v, want job with description %q", jobs, description)
	return nil
}

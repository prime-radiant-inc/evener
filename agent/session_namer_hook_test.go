package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestSessionNameFromPrompt_UpdatesMetaAndAdvisoryLog(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				if req.Model != "gpt-5.2" {
					t.Fatalf("model = %q, want active model", req.Model)
				}
				return llm.Response{
					Message: llm.Assistant(`{"name":"Fix Flaky Test"}`),
					Usage:   llm.Usage{TotalTokens: 7},
				}
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	profile := NewOpenAIProfile("gpt-5.2")
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if err := sess.nameSessionFromText(context.Background(), sessionNameSourcePrompt, "fix the flaky test"); err != nil {
		t.Fatalf("nameSessionFromText: %v", err)
	}

	meta := sess.Meta()
	if meta.Name != "Fix Flaky Test" {
		t.Fatalf("Name = %q, want Fix Flaky Test", meta.Name)
	}
	if meta.NameSource != sessionNameSourcePrompt {
		t.Fatalf("NameSource = %q, want prompt", meta.NameSource)
	}
	if meta.NameUpdatedAt.IsZero() {
		t.Fatal("NameUpdatedAt is zero")
	}

	persisted, err := schema.LoadSessionMeta(dir, sess.ID())
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if persisted.Name != "Fix Flaky Test" {
		t.Fatalf("persisted Name = %q, want Fix Flaky Test", persisted.Name)
	}

	log := mustNewSessionLog(t, filepath.Join(dir, sessionsSubdir, sess.ID()+".log.jsonl"))
	entries := log.Entries()
	if len(entries) != 1 {
		t.Fatalf("log entries = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Kind != "advisory" || entry.Action != "session_namer" || entry.Outcome != "success" {
		t.Fatalf("advisory entry mismatch: %+v", entry)
	}
	if !strings.Contains(entry.Summary, "Fix Flaky Test") || !strings.Contains(entry.Summary, "prompt-derived") {
		t.Fatalf("summary = %q, want prompt-derived name", entry.Summary)
	}
}

func TestSessionNameFromPrompt_LogsAdvisoryFailureWithoutFailingSession(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	adapter := &fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant(`{"name":"!!!"}`)} },
	}}
	client := llm.NewClient()
	client.Register(adapter)
	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), "gpt-4.1-nano")
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if err := sess.nameSessionFromText(context.Background(), sessionNameSourcePrompt, "!!!"); err == nil {
		t.Fatal("expected naming error")
	}

	if got := sess.Meta().Name; got != "" {
		t.Fatalf("Name = %q, want empty after failure", got)
	}
	log := mustNewSessionLog(t, filepath.Join(dir, sessionsSubdir, sess.ID()+".log.jsonl"))
	entries := log.Entries()
	if len(entries) != 1 {
		t.Fatalf("log entries = %d, want 1", len(entries))
	}
	entry := entries[0]
	if entry.Kind != "advisory" || entry.Action != "session_namer" || entry.Outcome != "failure" {
		t.Fatalf("failure advisory mismatch: %+v", entry)
	}
	if len(entry.Failures) != 1 || !strings.Contains(entry.Failures[0], "generated name is empty") {
		t.Fatalf("failures = %#v, want generated-name error", entry.Failures)
	}
}

func TestSessionLaunchesInitialPromptNamerAsynchronously(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), "gpt-4.1-nano")
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	started := make(chan struct{}, 1)
	done := make(chan struct{})
	sess.nameSessionFromTextFunc = func(ctx context.Context, source, text string) error {
		if source != sessionNameSourcePrompt {
			t.Errorf("source = %q, want prompt", source)
		}
		if text != "initial task" {
			t.Errorf("text = %q, want initial task", text)
		}
		started <- struct{}{}
		<-done
		return nil
	}

	sess.launchInitialPromptNamer(context.Background(), "initial task")
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("initial prompt namer did not start")
	}
	close(done)
}

func TestSessionInitialPromptNamerSkipsWhilePending(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), "gpt-4.1-nano")
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// entered is buffered to count namer goroutine launches. Each goroutine sends
	// once on entry (before blocking on done) so we can tally them deterministically.
	entered := make(chan struct{}, 2)
	done := make(chan struct{})
	sess.nameSessionFromTextFunc = func(ctx context.Context, source, text string) error {
		entered <- struct{}{}
		<-done
		return nil
	}

	sess.launchInitialPromptNamer(context.Background(), "initial task")
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("initial prompt namer did not start")
	}

	// While the first namer goroutine is still in flight, a second launch must
	// be suppressed by the promptPending guard. Call it synchronously — if it
	// bypasses the guard it will have spawned a goroutine before returning.
	sess.launchInitialPromptNamer(context.Background(), "later task")

	// Unblock goroutines then join via sess.Close() (which drains sendersWG).
	// After Close() returns every spawned goroutine has run to completion, so
	// any spurious second goroutine would already have sent to entered.
	close(done)
	sess.Close()

	select {
	case <-entered:
		t.Fatal("second namer goroutine was started while first was still pending")
	default:
	}
}

func TestSessionProcessInput_LaunchesInitialPromptNamer(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai", steps: []func(req llm.Request) llm.Response{
		func(req llm.Request) llm.Response {
			return toolCallResponse(communicateCall("c1", "done"))
		},
	}})
	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), "gpt-4.1-nano")
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	started := make(chan string, 1)
	sess.nameSessionFromTextFunc = func(ctx context.Context, source, text string) error {
		if source != sessionNameSourcePrompt {
			t.Errorf("source = %q, want prompt", source)
		}
		started <- text
		return nil
	}

	if out, err := sess.ProcessInput(context.Background(), "ship the router cleanup", nil); err != nil || strings.TrimSpace(out) != "done" {
		t.Fatalf("ProcessInput = %q, %v; want done, nil", out, err)
	}
	select {
	case got := <-started:
		if got != "ship the router cleanup" {
			t.Fatalf("namer text = %q, want initial prompt", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("initial prompt namer did not start from ProcessInput")
	}
}

func TestSessionNameFromCompactionTurn_RefreshesPromptName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), "gpt-4.1-nano")
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	sess.mu.Lock()
	sess.naming.value = "Initial Prompt Name"
	sess.naming.source = sessionNameSourcePrompt
	sess.naming.updated = time.Now().UTC()
	sess.mu.Unlock()

	var gotSource, gotText string
	sess.nameSessionFromTextFunc = func(ctx context.Context, source, text string) error {
		gotSource = source
		gotText = text
		sess.mu.Lock()
		sess.naming.value = "Compacted Parser Fixes"
		sess.naming.source = source
		sess.naming.updated = time.Now().UTC()
		sess.mu.Unlock()
		return nil
	}

	turn := schema.NewTurn(schema.TurnSummary, llm.User("[CONTEXT SUMMARY]\nImplemented parser fixes and regression tests."))
	if err := sess.nameSessionFromCompactionTurn(context.Background(), turn); err != nil {
		t.Fatalf("nameSessionFromCompactionTurn: %v", err)
	}

	if gotSource != sessionNameSourceCompaction {
		t.Fatalf("source = %q, want compaction", gotSource)
	}
	if !strings.Contains(gotText, "parser fixes") {
		t.Fatalf("text = %q, want compaction turn text", gotText)
	}
	meta := sess.Meta()
	if meta.Name != "Compacted Parser Fixes" || meta.NameSource != sessionNameSourceCompaction {
		t.Fatalf("meta name/source = %q/%q, want compaction refresh", meta.Name, meta.NameSource)
	}
}

func TestSessionNameFromCompactionTurn_SkipsNonCompactionAndManualName(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), "gpt-4.1-nano")
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	calls := 0
	sess.nameSessionFromTextFunc = func(ctx context.Context, source, text string) error {
		calls++
		return nil
	}

	if err := sess.nameSessionFromCompactionTurn(context.Background(), schema.NewTurn(schema.TurnAssistant, llm.Assistant("not compaction"))); err != nil {
		t.Fatalf("non-compaction turn error: %v", err)
	}
	if calls != 0 {
		t.Fatalf("calls after non-compaction turn = %d, want 0", calls)
	}

	sess.mu.Lock()
	sess.naming.value = "Manual Release Name"
	sess.naming.source = "manual"
	sess.naming.updated = time.Now().UTC()
	sess.mu.Unlock()
	if err := sess.nameSessionFromCompactionTurn(context.Background(), schema.NewTurn(schema.TurnCheckpoint, llm.User("[CONTEXT CHECKPOINT]\nRelease work"))); err != nil {
		t.Fatalf("manual-name compaction turn error: %v", err)
	}
	if calls != 0 {
		t.Fatalf("calls after manual name = %d, want 0", calls)
	}
	if got := sess.Meta().Name; got != "Manual Release Name" {
		t.Fatalf("Name = %q, want manual name preserved", got)
	}
}

func TestSessionLaunchesCompactionNamerAsynchronously(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), "gpt-4.1-nano")
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	started := make(chan struct{}, 1)
	done := make(chan struct{})
	sess.nameSessionFromTextFunc = func(ctx context.Context, source, text string) error {
		if source != sessionNameSourceCompaction {
			t.Errorf("source = %q, want compaction", source)
		}
		if !strings.Contains(text, "router cleanup") {
			t.Errorf("text = %q, want compaction text", text)
		}
		started <- struct{}{}
		<-done
		return nil
	}

	sess.launchCompactionNamer(context.Background(), schema.NewTurn(schema.TurnCheckpoint, llm.User("[CONTEXT CHECKPOINT]\nrouter cleanup")))
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("compaction namer did not start")
	}
	close(done)
}

func TestRestoreSessionFromMeta_PreservesManualNameAgainstCompaction(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	meta := schema.SessionMeta{
		ID:            "01TESTSESSIONMANUALNAME000000",
		ProfileID:     "openai",
		Model:         "gpt-5.2",
		CreatedAt:     time.Now().UTC(),
		Name:          "Manual Release Name",
		NameSource:    "manual",
		NameUpdatedAt: time.Now().UTC(),
	}
	sess, err := RestoreSessionFromMeta(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer sess.Close()

	calls := 0
	sess.nameSessionFromTextFunc = func(ctx context.Context, source, text string) error {
		calls++
		return nil
	}
	if err := sess.nameSessionFromCompactionTurn(context.Background(), schema.NewTurn(schema.TurnSummary, llm.User("[CONTEXT SUMMARY]\nrelease work"))); err != nil {
		t.Fatalf("nameSessionFromCompactionTurn: %v", err)
	}
	if calls != 0 {
		t.Fatalf("namer calls = %d, want 0 for restored manual name", calls)
	}
	got := sess.Meta()
	if got.Name != "Manual Release Name" || got.NameSource != "manual" {
		t.Fatalf("restored name/source = %q/%q, want manual name preserved", got.Name, got.NameSource)
	}
}

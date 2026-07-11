//go:build serffuzz

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/clock"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// This file covers session-tool contracts that need a real Session-owned
// registry or a reconstructed transcript, but must remain entirely offline:
//
//   - stmRunRestoreContracts is a differential between live ask-user
//     argument parsing and restore-time reconstruction. It proves a completed,
//     valid ask survives replay in call order while malformed/error-only calls
//     do not create a false pending-user hold.
//   - stmRunRoundContracts drives real registry dispatch, parallel
//     read batching, result persistence, steering, task/goal/compact state,
//     custom-tool cache refresh, and the image-description request shape. Its
//     semantic oracles check identities, persisted result order, first-write
//     read tracking, and the scripted vision request, not mocked internals.
//   - stmRunJobTranscriptContracts seeds a durable job record and
//     output under a test temp dir, then reads it through read_transcript. It
//     asserts that the rendered transcript remains a well-formed markdown
//     envelope and that unsupported formats stay clean errors.
//
// Every Session uses only a ScriptedAdapter, FakeClock, DenyEnv, and test-owned
// temp directories. DenyEnv never reads or writes a real workspace, executes a
// process, or opens a socket. The helpers intentionally do not run shell,
// delegate, worktree, web, or job-control handlers; those have dedicated safe
// harnesses and are outside this tool-contract surface.

func stmRunRestoreContracts(t *testing.T, program []byte) {
	t.Helper()
	r := &stmReader{data: program}
	validArgs := stmAskArgs(r)
	validRaw := stmMarshal(t, validArgs)
	want, err := parseAskQuestions(validArgs)
	if err != nil || len(want) == 0 {
		t.Fatalf("fixture valid ask did not parse: questions=%#v err=%v", validArgs, err)
	}

	invalidRaw := json.RawMessage(`{"questions":[{"header":"bad","question":"bad","options":[{"label":"same","detail":"one"},{"label":"same","detail":"two"}]}]}`)
	if r.bool() {
		invalidRaw = json.RawMessage(`{`)
	}

	assistant := stmAssistantTurn(
		llm.ToolCallData{ID: "ask-valid", Name: "ask_user", Arguments: validRaw, Type: "function"},
		llm.ToolCallData{ID: "ask-invalid", Name: "ask_user", Arguments: invalidRaw, Type: "function"},
	)
	completed := stmToolResultsTurn(
		stmToolResult("ask-valid", "ask_user", false),
		stmToolResult("ask-invalid", "ask_user", false),
	)
	history := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("earlier user input")},
		assistant,
		completed,
	}

	if state := deriveRestoredState(history); state != SessionAwaiting {
		t.Fatalf("completed ask round restored as %q, want %q", state, SessionAwaiting)
	}
	pending, isAskRound := deriveRestoredAskPending(history)
	if !isAskRound {
		t.Fatal("completed ask round was not recognized during restore")
	}
	if !reflect.DeepEqual(pending, want) {
		t.Fatalf("restored questions = %#v, want live parsed questions %#v", pending, want)
	}

	// A later user input resolves the hold regardless of earlier completed asks.
	history = append(history, schema.Turn{Kind: schema.TurnUserInput, Message: llm.User("answer")})
	if state := deriveRestoredState(history); state != SessionIdle {
		t.Fatalf("user reply restored as %q, want %q", state, SessionIdle)
	}
	if pending, isAskRound := deriveRestoredAskPending(history); isAskRound || len(pending) != 0 {
		t.Fatalf("user reply left pending ask state: pending=%#v isAskRound=%v", pending, isAskRound)
	}

	// Error-only results are repair placeholders, not a completed ask. They
	// must scan past the issuing assistant call and settle at the prior input.
	errorOnly := []schema.Turn{
		{Kind: schema.TurnUserInput, Message: llm.User("before interrupted ask")},
		assistant,
		stmToolResultsTurn(stmToolResult("ask-valid", "ask_user", true)),
	}
	if state := deriveRestoredState(errorOnly); state != SessionIdle {
		t.Fatalf("error-only ask round restored as %q, want %q", state, SessionIdle)
	}
	if pending, isAskRound := deriveRestoredAskPending(errorOnly); isAskRound || len(pending) != 0 {
		t.Fatalf("error-only ask round created pending state: pending=%#v isAskRound=%v", pending, isAskRound)
	}

	// A generic completed tool round rests awaiting but never fabricates an
	// ask-user hold.
	generic := []schema.Turn{
		stmAssistantTurn(llm.ToolCallData{ID: "communicate", Name: "communicate", Arguments: json.RawMessage(`{"message":"done","end_turn":true}`), Type: "function"}),
		stmToolResultsTurn(stmToolResult("communicate", "communicate", false)),
	}
	if state := deriveRestoredState(generic); state != SessionAwaiting {
		t.Fatalf("generic completed round restored as %q, want %q", state, SessionAwaiting)
	}
	if pending, isAskRound := deriveRestoredAskPending(generic); isAskRound || len(pending) != 0 {
		t.Fatalf("generic completion created ask state: pending=%#v isAskRound=%v", pending, isAskRound)
	}
}

func stmRunRoundContracts(t *testing.T, program []byte) {
	t.Helper()
	r := &stmReader{data: program}
	s, env, adapter := stmNewSession(t, program)
	ctx := context.Background()

	value := r.word()
	s.RegisterTool("fuzz_echo", "deterministic fuzz echo", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"value": map[string]any{"type": "string"},
		},
		"required": []string{"value"},
	}, func(_ context.Context, args any) (any, error) {
		m, ok := args.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("fuzz_echo arguments have type %T", args)
		}
		return fmt.Sprint(m["value"]), nil
	})
	if !stmHasDefinition(s.ToolDefinitions(), "fuzz_echo") {
		t.Fatal("runtime-registered tool was absent from cached definitions")
	}
	echo := stmExec(t, s, ctx, "echo", "fuzz_echo", map[string]any{"value": value})
	if echo.IsError || echo.Output != value || echo.FullOutput != value {
		t.Fatalf("custom registry tool result = %#v, want %q", echo, value)
	}

	calls := []llm.ToolCallData{
		stmCall(t, "read", "read_file", map[string]any{"file_path": value + ".txt", "purpose": "inspect " + value}),
		stmCall(t, "list", "list_dir", map[string]any{"path": ".", "depth": float64(r.next()%3 + 1), "offset": float64(r.next() % 3), "limit": float64(r.next()%4 + 1)}),
		stmCall(t, "grep", "grep", map[string]any{"pattern": value, "path": ".", "output_mode": []string{"content", "count", "files_with_matches"}[int(r.next())%3]}),
		stmCall(t, "glob", "glob", map[string]any{"pattern": "*." + value, "path": "."}),
		stmCall(t, "task", "task_list", map[string]any{"action": "append", "tasks": []any{map[string]any{"type": "implement", "description": value, "prompt": "do " + value}}}),
	}
	results, err := s.execToolBatch(ctx, calls, s.currentProfile())
	if err != nil {
		t.Fatalf("safe registry batch returned error: %v", err)
	}
	if len(results) != len(calls) {
		t.Fatalf("batch result count = %d, want %d", len(results), len(calls))
	}
	for i, result := range results {
		stmAssertResult(t, calls[i], result)
	}
	if tasks := s.Tasks(); len(tasks) != 1 || tasks[0].Description != value {
		t.Fatalf("task_list mutation = %#v, want one task %q", tasks, value)
	}

	before := len(stmHistory(s))
	if err := s.persistToolResults(ctx, calls, results); err != nil {
		t.Fatalf("persist tool results: %v", err)
	}
	history := stmHistory(s)
	if len(history) != before+1 || history[len(history)-1].Kind != schema.TurnToolResults {
		t.Fatalf("persisted history tail = %#v, want one tool-results turn after %d entries", history, before)
	}
	parts := history[len(history)-1].Message.Content
	if len(parts) != len(results) {
		t.Fatalf("persisted tool result count = %d, want %d", len(parts), len(results))
	}
	for i, part := range parts {
		if part.ToolResult == nil || part.ToolResult.ToolCallID != calls[i].ID || part.ToolResult.Name != calls[i].Name || part.ToolResult.IsError != results[i].IsError {
			t.Fatalf("persisted result %d = %#v, want call=%#v result=%#v", i, part.ToolResult, calls[i], results[i])
		}
	}

	// Updated hook input must merge valid JSON without losing prior fields, and
	// must reject malformed source arguments without mutating them.
	updated := llm.ToolCallData{Arguments: json.RawMessage(`{"before":"kept"}`)}
	if err := applyUpdatedToolInput(&updated, map[string]any{"after": value}); err != nil {
		t.Fatalf("apply valid updated input: %v", err)
	}
	var merged map[string]any
	if err := json.Unmarshal(updated.Arguments, &merged); err != nil || merged["before"] != "kept" || merged["after"] != value {
		t.Fatalf("updated input = %s decoded=%#v err=%v", updated.Arguments, merged, err)
	}
	broken := llm.ToolCallData{Arguments: json.RawMessage(`{`)}
	original := string(broken.Arguments)
	if err := applyUpdatedToolInput(&broken, map[string]any{"after": value}); err == nil || string(broken.Arguments) != original {
		t.Fatalf("invalid updated input err=%v args=%q want original=%q", err, broken.Arguments, original)
	}

	// A recorded read suppresses the read-before-write warning for exactly the
	// resolved path, independent of DenyEnv's deterministic existence draw.
	path := value + ".txt"
	if got := s.resolveFilePath(path); got != filepath.Join(env.WorkDir, path) {
		t.Fatalf("resolveFilePath(%q) = %q, want %q", path, got, filepath.Join(env.WorkDir, path))
	}
	s.trackReadFile(path)
	if warning := s.readBeforeWriteWarning(path); warning != "" {
		t.Fatalf("read file still produced write warning: %q", warning)
	}

	ask := stmExec(t, s, ctx, "ask", "ask_user", stmAskArgs(r))
	if ask.IsError || !s.HasPendingAsk() {
		t.Fatalf("valid ask did not produce a pending hold: %#v", ask)
	}
	s.clearAskPending()
	if s.HasPendingAsk() {
		t.Fatal("clearAskPending left an unresolved ask hold")
	}

	if started, err := s.SetGoal(ctx, "complete "+value); err != nil || started {
		t.Fatalf("SetGoal = started=%v err=%v, want armed active goal", started, err)
	}
	goal := stmExec(t, s, ctx, "goal", "update_goal", map[string]any{"status": "complete"})
	if goal.IsError || !strings.Contains(goal.Output, "Goal marked complete") {
		t.Fatalf("update_goal completion = %#v", goal)
	}
	if status, _, ok := s.GoalStatus(); !ok || status != "complete" {
		t.Fatalf("goal status = %q ok=%v, want complete", status, ok)
	}

	compact := stmExec(t, s, ctx, "compact", "compact_context", map[string]any{"note_to_self": value, "compaction_instructions": "retain " + value})
	if compact.IsError || s.PinnedNote() != value {
		t.Fatalf("compact request = %#v pinned=%q", compact, s.PinnedNote())
	}
	if instructions, ok := s.takeForceRequest(); !ok || instructions != "retain "+value {
		t.Fatalf("force compact request = %q %v", instructions, ok)
	}

	s.mu.Lock()
	s.totalRounds = 10
	s.taskToolEverUsed = false
	s.taskNudgeFired = false
	s.mu.Unlock()
	if reminder := s.maybeInjectTaskReminder(); reminder == "" {
		t.Fatal("ten rounds without task_list use did not inject a task reminder")
	}
	if reminder := s.maybeInjectTaskReminder(); reminder != "" {
		t.Fatalf("task reminder was not one-shot: %q", reminder)
	}

	s.Steer("queued " + value)
	var sigs []string
	if _, err := s.injectPostToolSteering(ctx, calls, &sigs); err != nil {
		t.Fatalf("inject post-tool steering: %v", err)
	}
	if len(sigs) != len(calls) {
		t.Fatalf("tool signatures = %#v, want %d", sigs, len(calls))
	}
	if !stmHistoryContains(stmHistory(s), "queued "+value) {
		t.Fatalf("queued steering was not persisted in history: %#v", stmHistory(s))
	}
	if err := s.notifyStrategyAfterAction(ctx); err != nil {
		t.Fatalf("strategy after action: %v", err)
	}

	mediaType := "image/png"
	wantKind := llm.ContentImage
	if r.bool() {
		mediaType = "application/pdf"
		wantKind = llm.ContentDocument
	}
	if description := s.describeImage(ctx, tool.ExecResult{ImageData: []byte("fixture-" + value), ImageMediaType: mediaType, ImagePurpose: "describe " + value}); description != "scripted vision" {
		t.Fatalf("scripted image description = %q", description)
	}
	requests := adapter.Requests()
	if len(requests) != 1 || len(requests[0].Tools) != 0 || len(requests[0].Messages) != 1 || len(requests[0].Messages[0].Content) != 2 || requests[0].Messages[0].Content[1].Kind != wantKind {
		t.Fatalf("vision request shape = %#v, want one no-tool %s request", requests, wantKind)
	}

	communicate := stmExec(t, s, ctx, "communicate", "communicate", map[string]any{
		"message":  "done " + value,
		"end_turn": true,
		"output": map[string]any{
			"message":   "done " + value,
			"data":      map[string]any{},
			"artifacts": []any{},
		},
	})
	if communicate.IsError {
		t.Fatalf("communicate = %#v", communicate)
	}
	s.mu.Lock()
	s.state = SessionProcessing
	s.mu.Unlock()
	done, reply := s.deliverIfCommunicated(ctx, false)
	if !done || reply == "" || s.State() != SessionIdle {
		t.Fatalf("communicate boundary = done=%v reply=%q state=%q", done, reply, s.State())
	}
}

func stmRunJobTranscriptContracts(t *testing.T, program []byte) {
	t.Helper()
	r := &stmReader{data: program}
	s, _, _ := stmNewSession(t, program)
	jm := s.jobManager
	jobID := "job_transcript_" + r.word()
	now := s.sclock().Now()
	started := now
	if err := jm.appendEvent(jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               now,
		JobID:            jobID,
		Type:             jobstore.JobShell,
		OwnerSessionID:   s.ID(),
		VisibleToSession: s.ID(),
		StartedAt:        &started,
		Command:          "echo `" + r.word() + "`",
		Description:      "fuzz transcript fixture",
	}); err != nil {
		t.Fatalf("seed job start: %v", err)
	}
	output := "line one " + r.word() + "\nline two " + r.word()
	logPath := filepath.Join(jm.dir, "jobs", jobID+".log")
	if err := os.WriteFile(logPath, []byte(output), 0o644); err != nil {
		t.Fatalf("write job output fixture: %v", err)
	}
	ended := now
	exit := int(r.next() % 3)
	if err := jm.appendEvent(jobstore.Event{
		Kind:        jobstore.EventJobFinished,
		TS:          now,
		JobID:       jobID,
		Status:      jobstore.StatusCompleted,
		EndedAt:     &ended,
		ExitCode:    &exit,
		OutputBytes: int64(len(output)),
		TerminalGen: jobstore.NewTerminalGeneration(),
	}); err != nil {
		t.Fatalf("seed job finish: %v", err)
	}

	result := stmExec(t, s, context.Background(), "job-read", "read_transcript", map[string]any{"transcript_ref": "job:" + jobID, "format": formatMarkdown})
	if result.IsError {
		t.Fatalf("read job transcript: %#v", result)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(result.Output), &envelope); err != nil {
		t.Fatalf("job transcript output is not JSON: %v\n%s", err, result.Output)
	}
	if got, _ := envelope["transcript_ref"].(string); got != "job:"+jobID {
		t.Fatalf("job transcript ref = %q, want %q", got, "job:"+jobID)
	}
	content, _ := envelope["content"].(string)
	if !strings.Contains(content, "# Shell Job "+jobID) || !strings.Contains(content, output) || !strings.Contains(content, "echo \\`") {
		t.Fatalf("job transcript content lost shell facts: %q", content)
	}

	unsupported := stmExec(t, s, context.Background(), "job-unsupported", "read_transcript", map[string]any{"transcript_ref": "job:" + jobID, "format": formatJSONL})
	if !unsupported.IsError || !strings.Contains(unsupported.Output, "not supported") {
		t.Fatalf("unsupported job transcript format = %#v", unsupported)
	}
}

type stmReader struct {
	data []byte
	pos  int
}

func (r *stmReader) next() byte {
	if r.pos >= len(r.data) {
		return 0
	}
	b := r.data[r.pos]
	r.pos++
	return b
}

func (r *stmReader) bool() bool { return r.next()&1 != 0 }

func (r *stmReader) word() string {
	words := []string{"alpha", "coverage", "unicode", "state", "tool"}
	return words[int(r.next())%len(words)]
}

func stmAskArgs(r *stmReader) map[string]any {
	word := r.word()
	return map[string]any{
		"questions": []any{map[string]any{
			// DefAskUser limits headers to 12 characters. Keep the fuzzed word in
			// the body/detail fields while holding this structural field valid so
			// the real handler, rather than argument repair, is the exercised seam.
			"header":   "Choice",
			"question": "Choose " + word,
			"options": []any{
				map[string]any{"label": "first", "detail": "first " + word, "recommended": true},
				map[string]any{"label": "second", "detail": "second " + word},
			},
		}},
	}
}

func stmMarshal(t *testing.T, value any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return json.RawMessage(b)
}

func stmAssistantTurn(calls ...llm.ToolCallData) schema.Turn {
	parts := make([]llm.ContentPart, 0, len(calls))
	for i := range calls {
		call := calls[i]
		parts = append(parts, llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &call})
	}
	return schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: parts}}
}

func stmToolResult(id, name string, isError bool) llm.ContentPart {
	return llm.ContentPart{
		Kind: llm.ContentToolResult,
		ToolResult: &llm.ToolResultData{
			ToolCallID: id,
			Name:       name,
			Content:    name + " result",
			IsError:    isError,
		},
	}
}

func stmToolResultsTurn(parts ...llm.ContentPart) schema.Turn {
	return schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Role: llm.RoleTool, Content: parts}}
}

func stmNewSession(t *testing.T, program []byte) (*Session, *agenttest.DenyEnv, *agenttest.ScriptedAdapter) {
	t.Helper()
	root := t.TempDir()
	env := &agenttest.DenyEnv{WorkDir: root, Seed: stmSeed(program)}
	adapter := &agenttest.ScriptedAdapter{
		Provider: "openai",
		Responder: func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("scripted vision")}
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	cfg := SessionConfig{
		MaxSubagentDepth: 1,
		NoProjectPrompts: true,
		StateDir:         root,
		clock:            agenttest.NewFakeClock(),
	}
	cfg.testOnly = testConfig{
		skipGitSnapshot:     true,
		environmentInfo:     stmEnvironmentInfo,
		minimalSystemPrompt: true,
		noSyncJobStore:      true,
	}
	s, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), env, cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(s.Close)
	return s, env, adapter
}

func stmEnvironmentInfo(env execenv.ExecutionEnvironment, clk clock.Clock) schema.EnvironmentInfo {
	return schema.EnvironmentInfo{
		WorkingDir: env.WorkingDirectory(),
		Platform:   "session-tool-fuzz",
		OSVersion:  "session-tool-fuzz",
		Today:      clk.Now().UTC().Format("2006-01-02"),
	}
}

func stmSeed(data []byte) uint64 {
	var out uint64
	for i, b := range data {
		if i == 8 {
			break
		}
		out |= uint64(b) << (8 * i)
	}
	return out
}

func stmHasDefinition(defs []llm.ToolDefinition, name string) bool {
	for _, def := range defs {
		if def.Name == name {
			return true
		}
	}
	return false
}

func stmCall(t *testing.T, id, name string, args map[string]any) llm.ToolCallData {
	t.Helper()
	return llm.ToolCallData{ID: id, Name: name, Arguments: stmMarshal(t, args), Type: "function"}
}

func stmExec(t *testing.T, s *Session, ctx context.Context, id, name string, args map[string]any) tool.ExecResult {
	t.Helper()
	result := s.execTool(ctx, stmCall(t, id, name, args))
	stmAssertResult(t, llm.ToolCallData{ID: id, Name: name}, result)
	return result
}

func stmAssertResult(t *testing.T, call llm.ToolCallData, result tool.ExecResult) {
	t.Helper()
	if result.ToolName != call.Name || result.CallID != call.ID {
		t.Fatalf("tool result identity = %#v, want name=%q id=%q", result, call.Name, call.ID)
	}
	if !utf8.ValidString(result.Output) || !utf8.ValidString(result.FullOutput) {
		t.Fatalf("tool result has invalid UTF-8: %#v", result)
	}
	if len(result.ToolState) > 0 && !json.Valid(result.ToolState) {
		t.Fatalf("tool result state is not JSON: %q", result.ToolState)
	}
}

func stmHistory(s *Session) []schema.Turn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]schema.Turn(nil), s.history...)
}

func stmHistoryContains(history []schema.Turn, needle string) bool {
	for _, turn := range history {
		if strings.Contains(turn.Message.Text(), needle) {
			return true
		}
	}
	return false
}

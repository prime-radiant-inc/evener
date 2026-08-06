package doctor

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// healthToolCall and healthToolResult build tool call/result parts with an
// explicit, caller-chosen id — unlike transcript_test.go's toolCall/
// toolResult (which derive a fixed "tc-"+name id), a health fixture needs
// several distinct calls to the SAME tool (the identical-run scenario), so
// every occurrence needs its own id to pair correctly with its result.
func healthToolCall(id, name, args string) llm.ContentPart {
	return llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
		ID: id, Name: name, Arguments: json.RawMessage(args)}}
}

func healthToolResult(id, name string, content any, isError bool) llm.ContentPart {
	return llm.ContentPart{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
		ToolCallID: id, Name: name, Content: content, IsError: isError}}
}

func steeringTurn(text, kind string) schema.Turn {
	t := schema.NewTurn(schema.TurnSteering, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{assistantText(text)}})
	t.SteeringKind = kind
	return t
}

// healthFixtureTurns builds one session's turns exercising every
// TranscriptHealth metric in a single pass:
//   - tool_calls / tool_errors by class: read_file (success), shell (denied
//     x4 identical + timeout x1), grep (success x2 identical, shorter run),
//     some_tool (schema-rejection, other).
//   - longest_identical_run: 4 identical failing "shell" calls (same
//     args/signature) — longer than grep's 2-call identical successful run.
//   - truncation_warnings: one read_file result carrying the registry's
//     truncation banner.
//   - steering: one loop-detected steering turn (mid-session, BEFORE the
//     final end_turn=true communicate — must not count toward
//     stale_notifications/user_corrections) and one notification steering
//     turn AFTER it (stale).
//   - stale_notifications / user_corrections: the notification steering turn
//     and a plain USER_INPUT turn, both after the final end_turn=true
//     communicate call.
func healthFixtureTurns() []schema.Turn {
	return []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{assistantText("please help")}}),

		// read_file: one clean success.
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			healthToolCall("c1", "read_file", `{"path":"a"}`),
		}}),
		schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			healthToolResult("c1", "read_file", "file a contents", false),
		}}),

		// shell: 4 identical failing calls (same args -> same signature),
		// the longest identical run in the fixture, all "denied" class.
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			healthToolCall("c2", "shell", `{"cmd":"cat /root/secret"}`),
		}}),
		schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			healthToolResult("c2", "shell", "permission denied: /root/secret", true),
		}}),
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			healthToolCall("c3", "shell", `{"cmd":"cat /root/secret"}`),
		}}),
		schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			healthToolResult("c3", "shell", "permission denied: /root/secret", true),
		}}),
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			healthToolCall("c4", "shell", `{"cmd":"cat /root/secret"}`),
		}}),
		schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			healthToolResult("c4", "shell", "permission denied: /root/secret", true),
		}}),
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			healthToolCall("c5", "shell", `{"cmd":"cat /root/secret"}`),
		}}),
		schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			healthToolResult("c5", "shell", "permission denied: /root/secret", true),
		}}),

		// the runtime's own loop-detected steering, mid-session — before the
		// final end_turn=true communicate.
		steeringTurn("you've repeated this tool call; try something else", events.SteeringKindLoopDetected),

		// shell: one more call, different args, times out (a distinct
		// signature so it does not extend the identical run above).
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			healthToolCall("c10", "shell", `{"cmd":"long-process"}`),
		}}),
		schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			healthToolResult("c10", "shell", "command timed out after 30000ms", true),
		}}),

		// grep: 2 identical successful calls — a shorter identical run than
		// shell's failing 4, and not an error run at all.
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			healthToolCall("c6", "grep", `{"q":"TODO"}`),
		}}),
		schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			healthToolResult("c6", "grep", "no matches", false),
		}}),
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			healthToolCall("c7", "grep", `{"q":"TODO"}`),
		}}),
		schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			healthToolResult("c7", "grep", "no matches", false),
		}}),

		// read_file: a truncated result (not an error).
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			healthToolCall("c8", "read_file", `{"path":"huge.log"}`),
		}}),
		schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			healthToolResult("c8", "read_file",
				"[WARNING: Tool output was truncated. First 100 characters were removed. The full output is available in the event stream.]\n\nrest of file", false),
		}}),

		// some_tool: a schema-rejection error and an unclassified ("other") error.
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			healthToolCall("c9", "some_tool", `{"bad":true}`),
		}}),
		schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			healthToolResult("c9", "some_tool", "tool args schema validation failed: bad is not allowed", true),
		}}),
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			healthToolCall("c11", "some_tool", `{"weird":true}`),
		}}),
		schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			healthToolResult("c11", "some_tool", "boom, unexpected failure", true),
		}}),

		// the final end_turn=true communicate — everything after this line is
		// "post-done" activity.
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			communicateCall(true, ""),
		}}),

		// stale: a notification steering turn delivered after the session
		// already declared itself done.
		steeringTurn("job_42 finished", events.SteeringKindNotification),
		// a post-done user message: the session kept going after end_turn=true.
		schema.NewTurn(schema.TurnUserInput, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{assistantText("wait, actually...")}}),
	}
}

// healthFixtureJobsEvents builds a run_timeout job with zero output bytes
// (the 2026-07-31 diagnosis shape) alongside a completed job with real
// output, so ZeroOutputTerminal and ByTerminalReason must each pick out only
// the timeout job.
func healthFixtureJobsEvents() []jobstore.Event {
	exitTimeout, exitOK := -1, 0
	return []jobstore.Event{
		{Kind: jobstore.EventJobStarted, JobID: "job_health_timeout", Type: jobstore.JobShell, Command: "npm run dev",
			OwnerSessionID: sidA, VisibleToSession: sidA, StartedAt: &jobStartedAt},
		{Kind: jobstore.EventJobFinished, JobID: "job_health_timeout", Status: jobstore.StatusStopped, Reason: "run_timeout",
			ExitCode: &exitTimeout, EndedAt: &jobEndedAt, OutputBytes: 0},

		{Kind: jobstore.EventJobStarted, JobID: "job_health_ok", Type: jobstore.JobShell, Command: "make test",
			OwnerSessionID: sidA, VisibleToSession: sidA, StartedAt: &jobStartedAt},
		{Kind: jobstore.EventJobFinished, JobID: "job_health_ok", Status: jobstore.StatusCompleted,
			ExitCode: &exitOK, EndedAt: &jobEndedAt, OutputBytes: 4096},
	}
}

func healthFixture(t *testing.T) (base, sid string) {
	t.Helper()
	base = t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid = sidA
	writeRichSession(t, bucket, sid, healthFixtureTurns(), nil, schema.SessionMeta{})
	jobsPath := filepath.Join(bucket, "sessions", sid, "jobs.jsonl")
	// jobstore.OpenNoSync does not create the per-session subdir, so lay down
	// the (possibly empty) jobs.jsonl first — mirroring
	// writeSessionsFixtureSession's convention in sessions_test.go.
	writeFile(t, jobsPath, "")
	writeJobsEvents(t, jobsPath, healthFixtureJobsEvents())
	return base, sid
}

func TestTranscriptHealth_ToolCallsAndErrorClasses(t *testing.T) {
	base, sid := healthFixture(t)
	h, err := TranscriptHealth(base, sid)
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := map[string]int{"read_file": 2, "shell": 5, "grep": 2, "some_tool": 2, "communicate": 1}
	for name, want := range wantCalls {
		if got := h.ToolCalls[name]; got != want {
			t.Errorf("ToolCalls[%q] = %d, want %d (all: %+v)", name, got, want, h.ToolCalls)
		}
	}

	shellErrs := h.ToolErrors["shell"]
	if shellErrs["denied"] != 4 {
		t.Errorf("shell denied errors = %d, want 4: %+v", shellErrs["denied"], shellErrs)
	}
	if shellErrs["timeout"] != 1 {
		t.Errorf("shell timeout errors = %d, want 1: %+v", shellErrs["timeout"], shellErrs)
	}

	someErrs := h.ToolErrors["some_tool"]
	if someErrs["schema-rejection"] != 1 {
		t.Errorf("some_tool schema-rejection errors = %d, want 1: %+v", someErrs["schema-rejection"], someErrs)
	}
	if someErrs["other"] != 1 {
		t.Errorf("some_tool other errors = %d, want 1: %+v", someErrs["other"], someErrs)
	}

	if _, ok := h.ToolErrors["read_file"]; ok {
		t.Errorf("read_file should have no errors, got: %+v", h.ToolErrors["read_file"])
	}
}

func TestTranscriptHealth_LongestIdenticalRun(t *testing.T) {
	base, sid := healthFixture(t)
	h, err := TranscriptHealth(base, sid)
	if err != nil {
		t.Fatal(err)
	}
	run := h.LongestIdenticalRun
	if run.Tool != "shell" {
		t.Errorf("LongestIdenticalRun.Tool = %q, want shell", run.Tool)
	}
	if run.Length != 4 {
		t.Errorf("LongestIdenticalRun.Length = %d, want 4", run.Length)
	}
	if !run.AllErrors {
		t.Error("LongestIdenticalRun.AllErrors = false, want true (all 4 shell calls errored)")
	}
}

func TestTranscriptHealth_TruncationWarnings(t *testing.T) {
	base, sid := healthFixture(t)
	h, err := TranscriptHealth(base, sid)
	if err != nil {
		t.Fatal(err)
	}
	if h.TruncationWarnings != 1 {
		t.Errorf("TruncationWarnings = %d, want 1", h.TruncationWarnings)
	}
}

func TestTranscriptHealth_SteeringByKind(t *testing.T) {
	base, sid := healthFixture(t)
	h, err := TranscriptHealth(base, sid)
	if err != nil {
		t.Fatal(err)
	}
	if h.Steering[events.SteeringKindLoopDetected] != 1 {
		t.Errorf("Steering[loop-detected] = %d, want 1: %+v", h.Steering[events.SteeringKindLoopDetected], h.Steering)
	}
	if h.Steering[events.SteeringKindNotification] != 1 {
		t.Errorf("Steering[notification] = %d, want 1: %+v", h.Steering[events.SteeringKindNotification], h.Steering)
	}
}

func TestTranscriptHealth_Jobs(t *testing.T) {
	base, sid := healthFixture(t)
	h, err := TranscriptHealth(base, sid)
	if err != nil {
		t.Fatal(err)
	}
	if h.Jobs.ByTerminalReason["run_timeout"] != 1 {
		t.Errorf("Jobs.ByTerminalReason[run_timeout] = %d, want 1: %+v", h.Jobs.ByTerminalReason["run_timeout"], h.Jobs.ByTerminalReason)
	}
	if h.Jobs.ZeroOutputTerminal != 1 {
		t.Errorf("Jobs.ZeroOutputTerminal = %d, want 1 (only the run_timeout job has zero output)", h.Jobs.ZeroOutputTerminal)
	}
}

func TestTranscriptHealth_StaleNotificationsAndUserCorrections(t *testing.T) {
	base, sid := healthFixture(t)
	h, err := TranscriptHealth(base, sid)
	if err != nil {
		t.Fatal(err)
	}
	if h.StaleNotifications != 1 {
		t.Errorf("StaleNotifications = %d, want 1", h.StaleNotifications)
	}
	// user_corrections is the proxy count: the stale notification steering
	// turn AND the post-done USER_INPUT turn, both after the final
	// end_turn=true communicate. The mid-session loop-detected steering turn
	// (before the final communicate) must NOT count.
	if h.UserCorrections != 2 {
		t.Errorf("UserCorrections = %d, want 2", h.UserCorrections)
	}
}

func TestTranscriptHealth_JSONFields(t *testing.T) {
	base, sid := healthFixture(t)
	h, err := TranscriptHealth(base, sid)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"session_id", "tool_calls", "tool_errors", "longest_identical_run",
		"truncation_warnings", "steering", "jobs", "stale_notifications", "user_corrections",
	} {
		if _, ok := m[key]; !ok {
			t.Errorf("HealthResult JSON missing field %q: %s", key, string(b))
		}
	}
}

func TestRenderHealth_CompactTable(t *testing.T) {
	base, sid := healthFixture(t)
	h, err := TranscriptHealth(base, sid)
	if err != nil {
		t.Fatal(err)
	}
	out := RenderHealth(h)
	for _, want := range []string{
		sid, "shell", "denied=4", "timeout=1", "longest_identical_run: tool=shell length=4 all_errors=true",
		"truncation_warnings: 1", "loop-detected=1", "notification=1",
		"run_timeout=1", "zero_output_terminal=1", "stale_notifications: 1", "user_corrections (proxy): 2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered health missing %q:\n%s", want, out)
		}
	}
}

// TestTranscriptHealth_NoResultToolCall covers the healthy-empty shape: a
// session with no result-tool call at all has no "final end_turn=true" anchor,
// so stale_notifications/user_corrections must both read zero, not guess.
func TestTranscriptHealth_NoResultToolCall(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid := sidB
	turns := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{assistantText("hi")}}),
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{assistantText("hello")}}),
		steeringTurn("a notification with no anchor", events.SteeringKindNotification),
	}
	writeRichSession(t, bucket, sid, turns, nil, schema.SessionMeta{})
	jobsPath := filepath.Join(bucket, "sessions", sid, "jobs.jsonl")
	writeFile(t, jobsPath, "")

	h, err := TranscriptHealth(base, sid)
	if err != nil {
		t.Fatal(err)
	}
	if h.StaleNotifications != 0 {
		t.Errorf("StaleNotifications = %d, want 0 (no end_turn=true anchor)", h.StaleNotifications)
	}
	if h.UserCorrections != 0 {
		t.Errorf("UserCorrections = %d, want 0 (no end_turn=true anchor)", h.UserCorrections)
	}
	if h.LongestIdenticalRun.Length != 0 {
		t.Errorf("LongestIdenticalRun.Length = %d, want 0 (no tool calls)", h.LongestIdenticalRun.Length)
	}
}

func TestTranscriptHealth_UnreadableTranscriptIsLoudError(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	writeFile(t, filepath.Join(bucket, "sessions", sidA+".transcript.jsonl"), "not valid json\n")
	_, err := TranscriptHealth(base, sidA)
	if err == nil {
		t.Fatal("want error for unparseable transcript, got nil")
	}
}

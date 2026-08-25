package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	taskpkg "primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/llm"
)

// fullLadderProfile returns an OpenAI profile whose effort ladder includes all
// six levels, so nothing in these tests is clamped away.
func fullLadderProfile(model string) *provider.Profile {
	return provider.NewOpenAIProfile(model).
		WithLiveModelInfo(llm.ModelInfo{ReasoningEffortLevels: []string{"minimal", "low", "medium", "high", "xhigh", "max"}})
}

func taskEffortToolCall(id, args string) llm.Response {
	return llm.Response{Message: llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{{
			Kind:     llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{ID: id, Name: "task_list", Arguments: json.RawMessage(args)},
		}},
	}}
}

func requireTaskEffortSchemaLevel(t *testing.T, req llm.Request, operation, level string) {
	t.Helper()
	for _, def := range req.Tools {
		if def.Name != "task_list" {
			continue
		}
		properties, ok := def.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("task_list properties = %#v", def.Parameters["properties"])
		}
		operationSchema, ok := properties[operation].(map[string]any)
		if !ok {
			t.Fatalf("task_list %s schema = %#v", operation, properties[operation])
		}
		items, ok := operationSchema["items"].(map[string]any)
		if !ok {
			t.Fatalf("task_list %s items = %#v", operation, operationSchema["items"])
		}
		itemProperties, ok := items["properties"].(map[string]any)
		if !ok {
			t.Fatalf("task_list %s item properties = %#v", operation, items["properties"])
		}
		effortSchema, ok := itemProperties["reasoning_effort"].(map[string]any)
		if !ok {
			t.Fatalf("task_list %s reasoning_effort schema = %#v", operation, itemProperties["reasoning_effort"])
		}
		levels, ok := effortSchema["enum"].([]string)
		if !ok {
			t.Fatalf("task_list %s reasoning_effort enum = %#v", operation, effortSchema["enum"])
		}
		if slices.Contains(levels, level) {
			return
		}
		t.Fatalf("task_list %s reasoning_effort enum = %v, want stale level %q admitted by metadata", operation, levels, level)
	}
	t.Fatal("request has no task_list tool definition")
}

func requireTaskHandlerError(t *testing.T, req llm.Request, callID string) {
	t.Helper()
	for _, message := range req.Messages {
		for _, part := range message.Content {
			if part.ToolResult == nil || part.ToolResult.ToolCallID != callID {
				continue
			}
			if !part.ToolResult.IsError {
				t.Fatalf("task_list result %s is not an error: %#v", callID, part.ToolResult)
			}
			if part.ToolResult.PrevalOnly {
				t.Fatalf("task_list result %s was rejected before the handler: %#v", callID, part.ToolResult)
			}
			return
		}
	}
	t.Fatalf("request has no task_list result for %s", callID)
}

func TestValidateTaskEffortNormalizesCanonicalValues(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, input, want string
	}{
		{name: "canonical", input: " HIGH ", want: "high"},
		{name: "inherit", input: " InHerIt ", want: ""},
		{name: "disable alias", input: " none ", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validateTaskEffort(tc.input)
			if err != nil {
				t.Fatalf("validateTaskEffort(%q): %v", tc.input, err)
			}
			if got != tc.want {
				t.Fatalf("validateTaskEffort(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func requestEfforts(t *testing.T, reqs []llm.Request) []string {
	t.Helper()
	out := make([]string, len(reqs))
	for i, r := range reqs {
		if r.ReasoningEffort != nil {
			out[i] = *r.ReasoningEffort
		}
	}
	return out
}

// A per-task reasoning_effort must apply only to the rounds while that task is
// in progress: the wire sequence for a max session working task #1 (low) then
// task #2 (high) is max, max, low, high, then max again once the list is
// done — and the session's configured effort must survive untouched (issue
// #330: the override was written into s.cfg and the launch value was gone
// forever).
func TestSession_TaskEffortOverride_AppliesPerRoundOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// req0 (expect max): create both tasks.
			func(req llm.Request) llm.Response {
				return taskEffortToolCall("c1", `{"action":"append","tasks":[{"type":"implement","description":"cheap step","prompt":"do cheap thing","reasoning_effort":"low"},{"type":"implement","description":"hard step","prompt":"do hard thing","reasoning_effort":"high"}]}`)
			},
			// req1 (expect max, nothing in progress yet): start task 1.
			func(req llm.Request) llm.Response {
				return taskEffortToolCall("c2", `{"action":"update","updates":[{"id":1,"status":"in_progress"}]}`)
			},
			// req2 (expect low, task 1 in progress): finish task 1 -> auto-advance starts task 2.
			func(req llm.Request) llm.Response {
				return taskEffortToolCall("c3", `{"action":"update","updates":[{"id":1,"status":"done"}]}`)
			},
			// req3 (expect high, task 2 in progress): finish task 2.
			func(req llm.Request) llm.Response {
				return taskEffortToolCall("c4", `{"action":"update","updates":[{"id":2,"status":"done"}]}`)
			},
			// req4 (expect max again: no task in progress).
			func(req llm.Request) llm.Response { return finalResponse("done") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, fullLadderProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		ReasoningEffort: "max",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "work the plan", nil); err != nil {
		t.Fatal(err)
	}

	reqs := f.Requests()
	if len(reqs) != 5 {
		t.Fatalf("expected 5 requests, got %d (efforts=%v)", len(reqs), requestEfforts(t, reqs))
	}
	want := []string{"max", "max", "low", "high", "max"}
	got := requestEfforts(t, reqs)
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("request efforts = %v, want %v (mismatch at request %d)", got, want, i)
		}
	}
	if effort := sess.ReasoningEffort(); effort != "max" {
		t.Fatalf("session configured effort = %q after tasks, want %q (task effort must not persist into session config)", effort, "max")
	}
}

// An invalid task effort must reject the entire task update before it can
// activate the task. Otherwise the next model request carries the invalid
// override and a provider that rejects it can leave autonomous wakeups retrying
// the same permanently poisoned request.
func TestSession_TaskListRejectsInvalidEffortBeforeActivatingTask(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				requireTaskEffortSchemaLevel(t, req, "updates", "ultra")
				return taskEffortToolCall("c1", `{"action":"append","tasks":[{"type":"implement","description":"first guard","prompt":"keep the first request valid","reasoning_effort":"high"},{"type":"implement","description":"second guard","prompt":"keep the second request valid","reasoning_effort":"low"}]}`)
			},
			func(req llm.Request) llm.Response {
				return taskEffortToolCall("c2", `{"action":"update","updates":[{"id":1,"status":"done","reasoning_effort":"max"},{"id":2,"status":"in_progress","reasoning_effort":"ultra"}]}`)
			},
			func(req llm.Request) llm.Response {
				requireTaskHandlerError(t, req, "c2")
				return finalResponse("done")
			},
		},
	}
	c.Register(f)

	// Model metadata is an external boundary and may advertise a stale level.
	// The task handler must still enforce Evener's canonical vocabulary rather
	// than trusting the schema enum as its only validation.
	profile := provider.NewOpenAIProfile("gpt-5.2").WithLiveModelInfo(llm.ModelInfo{
		ReasoningEffortLevels: []string{"minimal", "low", "medium", "high", "xhigh", "max", "ultra"},
	})
	sess, err := NewSession(c, profile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		ReasoningEffort: "max",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "work the plan", nil); err != nil {
		t.Fatal(err)
	}

	tasks := sess.getOrCreateTaskStore().View()
	if len(tasks) != 2 {
		t.Fatalf("tasks = %#v, want the two valid appended tasks", tasks)
	}
	if tasks[0].Status != taskpkg.TaskOpen || tasks[0].ReasoningEffort != "high" || tasks[1].Status != taskpkg.TaskOpen || tasks[1].ReasoningEffort != "low" {
		t.Fatalf("tasks after rejected update = %#v, want both original open tasks unchanged", tasks)
	}
	if got, want := requestEfforts(t, f.Requests()), []string{"max", "max", "max"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("request efforts = %v, want %v (invalid task effort must not cross the provider boundary)", got, want)
	}
}

func TestSession_TaskListRejectsInvalidEffortBeforeAppendingTask(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				requireTaskEffortSchemaLevel(t, req, "tasks", "ultra")
				return taskEffortToolCall("c1", `{"action":"append","tasks":[{"type":"implement","description":"valid step","prompt":"must roll back with the batch","reasoning_effort":"high"},{"type":"implement","description":"poisoned step","prompt":"must not persist","reasoning_effort":"ultra"}]}`)
			},
			func(req llm.Request) llm.Response {
				requireTaskHandlerError(t, req, "c1")
				return finalResponse("done")
			},
		},
	}
	c.Register(f)
	profile := provider.NewOpenAIProfile("gpt-5.2").WithLiveModelInfo(llm.ModelInfo{
		ReasoningEffortLevels: []string{"minimal", "low", "medium", "high", "xhigh", "max", "ultra"},
	})
	sess, err := NewSession(c, profile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{ReasoningEffort: "max"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "work the plan", nil); err != nil {
		t.Fatal(err)
	}
	if tasks := sess.getOrCreateTaskStore().View(); len(tasks) != 0 {
		t.Fatalf("tasks after rejected append = %#v, want none", tasks)
	}
}

// A task whose reasoning_effort is the "inherit" sentinel (needed because
// OpenAI strict mode force-requires the property, so the model cannot omit it)
// must not override anything: requests while it is in progress stay at the
// session's configured effort.
func TestSession_TaskEffortInherit_KeepsSessionEffort(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return taskEffortToolCall("c1", `{"action":"append","tasks":[{"type":"implement","description":"a step","prompt":"do the thing","reasoning_effort":"inherit"}]}`)
			},
			func(req llm.Request) llm.Response {
				return taskEffortToolCall("c2", `{"action":"update","updates":[{"id":1,"status":"in_progress"}]}`)
			},
			// req2: task 1 in progress with effort "inherit" -> session effort.
			func(req llm.Request) llm.Response { return finalResponse("done") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, fullLadderProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		ReasoningEffort: "high",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "work the plan", nil); err != nil {
		t.Fatal(err)
	}

	reqs := f.Requests()
	if len(reqs) != 3 {
		t.Fatalf("expected 3 requests, got %d (efforts=%v)", len(reqs), requestEfforts(t, reqs))
	}
	// The append and start must actually be accepted: "inherit" is a legal
	// enum value, stored as "no override".
	current, ok := sess.getOrCreateTaskStore().CurrentInProgress()
	if !ok || current.ID != 1 {
		t.Fatalf("task 1 is not in progress — a task with reasoning_effort %q must be accepted", "inherit")
	}
	if current.ReasoningEffort != "" {
		t.Fatalf("stored task effort = %q, want %q (inherit normalizes to no-override)", current.ReasoningEffort, "")
	}
	if got := requestEfforts(t, reqs); got[2] != "high" {
		t.Fatalf("request efforts = %v, want final request at session effort %q (inherit must mean no override)", got, "high")
	}
}

// Once loop detection escalates the session's effort ("Your reasoning effort
// has been increased"), a lower per-task override must not silently undo the
// escalation — the effective effort is the higher-ranked of the escalated
// config and the task override, so the message never lies.
func TestSession_LoopDetectEscalation_WinsOverLowerTaskEffort(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return taskEffortToolCall("c1", `{"action":"append","tasks":[{"type":"implement","description":"cheap step","prompt":"do cheap thing","reasoning_effort":"low"}]}`)
			},
			func(req llm.Request) llm.Response {
				return taskEffortToolCall("c2", `{"action":"update","updates":[{"id":1,"status":"in_progress"}]}`)
			},
			// req2: task low in progress, no escalation yet -> low.
			func(req llm.Request) llm.Response { return finalResponse("still working") },
			// req3 (second input, after escalation): cfg high beats task low.
			func(req llm.Request) llm.Response { return finalResponse("done") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, fullLadderProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		ReasoningEffort: "medium",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "work the plan", nil); err != nil {
		t.Fatal(err)
	}

	// The loop detector fires: first escalation bumps effort medium -> high.
	_ = sess.stuckEscalation(1)

	if _, err := sess.ProcessInput(ctx, "keep going", nil); err != nil {
		t.Fatal(err)
	}

	reqs := f.Requests()
	if len(reqs) != 4 {
		t.Fatalf("expected 4 requests, got %d (efforts=%v)", len(reqs), requestEfforts(t, reqs))
	}
	got := requestEfforts(t, reqs)
	if got[2] != "low" {
		t.Fatalf("request efforts = %v, want request 2 at task override %q (no escalation active yet)", got, "low")
	}
	if got[3] != "high" {
		t.Fatalf("request efforts = %v, want request 3 at escalated %q (escalation must beat a lower task override)", got, "high")
	}
}

func TestEffectiveReasoningEffort(t *testing.T) {
	t.Parallel()
	cases := []struct {
		cfg, override string
		escalated     bool
		want          string
	}{
		{"max", "", false, "max"},
		{"max", "low", false, "low"},
		{"max", "low", true, "max"},
		{"low", "high", true, "high"},
		{"max", "", true, "max"},
		{"", "low", false, "low"},
		{"", "low", true, "low"},
		{"", "", false, ""},
	}
	for _, tc := range cases {
		if got := effectiveReasoningEffort(tc.cfg, tc.override, tc.escalated); got != tc.want {
			t.Errorf("effectiveReasoningEffort(%q, %q, %v) = %q, want %q", tc.cfg, tc.override, tc.escalated, got, tc.want)
		}
	}
}

func TestSession_TaskEffortSurvivesCompaction(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("done") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, fullLadderProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		ReasoningEffort: "max",
		testOnly:        testConfig{contextStrategyOverride: compactionEventStrategy{emitCompaction: true}},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	store := sess.getOrCreateTaskStore()
	if _, err := store.Append([]taskpkg.TaskInput{{
		Type:            taskpkg.TaskTypeImplement,
		Description:     "compact step",
		Prompt:          "do the compact step",
		ReasoningEffort: "low",
	}}); err != nil {
		t.Fatalf("append task: %v", err)
	}
	if err := store.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress}}); err != nil {
		t.Fatalf("start task: %v", err)
	}

	ctx := context.Background()
	if _, err := sess.ProcessInput(ctx, "work through compaction", nil); err != nil {
		t.Fatal(err)
	}
	reqs := f.Requests()
	if len(reqs) != 1 {
		t.Fatalf("expected one request, got %d", len(reqs))
	}
	if got := requestEfforts(t, reqs)[0]; got != "low" {
		t.Fatalf("request effort after compaction = %q, want task override %q", got, "low")
	}
	if got := sess.ReasoningEffort(); got != "max" {
		t.Fatalf("session configured effort after compaction = %q, want %q", got, "max")
	}
}

func TestSession_TaskEffortSurvivesResumeWithoutClobberingConfig(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("before resume") },
			func(req llm.Request) llm.Response { return finalResponse("after resume") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, fullLadderProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(stateDir), SessionConfig{
		ReasoningEffort: "max",
		StateDir:        stateDir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	store := sess.getOrCreateTaskStore()
	if _, err := store.Append([]taskpkg.TaskInput{{
		Type:            taskpkg.TaskTypeImplement,
		Description:     "resume step",
		Prompt:          "do the resume step",
		ReasoningEffort: "low",
	}}); err != nil {
		t.Fatalf("append task: %v", err)
	}
	if err := store.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress}}); err != nil {
		t.Fatalf("start task: %v", err)
	}
	ctx := context.Background()
	if _, err := sess.ProcessInput(ctx, "work before resume", nil); err != nil {
		t.Fatal(err)
	}
	if got := sess.ReasoningEffort(); got != "max" {
		t.Fatalf("configured effort before resume = %q, want %q", got, "max")
	}
	sessionID := sess.id
	sess.Close()

	meta, err := schema.LoadSessionMeta(stateDir, sessionID)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if got := meta.Config.ReasoningEffort; got != "max" {
		t.Fatalf("persisted configured effort = %q, want %q", got, "max")
	}
	restored, err := RestoreSessionFromMeta(c, fullLadderProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(stateDir), meta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	if _, err := restored.ProcessInput(ctx, "continue after resume", nil); err != nil {
		t.Fatal(err)
	}
	reqs := f.Requests()
	if len(reqs) != 2 {
		t.Fatalf("expected two requests across resume, got %d (efforts=%v)", len(reqs), requestEfforts(t, reqs))
	}
	if got := requestEfforts(t, reqs)[1]; got != "low" {
		t.Fatalf("resumed request effort = %q, want task override %q", got, "low")
	}
	if got := restored.ReasoningEffort(); got != "max" {
		t.Fatalf("resumed configured effort = %q, want %q", got, "max")
	}
}

func TestSession_LoopDetectEscalationSurvivesResume(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("before escalation") },
			func(req llm.Request) llm.Response { return finalResponse("after resume") },
		},
	}
	c.Register(f)
	sess, err := NewSession(c, fullLadderProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(stateDir), SessionConfig{
		ReasoningEffort: "medium",
		StateDir:        stateDir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	store := sess.getOrCreateTaskStore()
	if _, err := store.Append([]taskpkg.TaskInput{{
		Type:            taskpkg.TaskTypeImplement,
		Description:     "escalation step",
		Prompt:          "do the escalation step",
		ReasoningEffort: "low",
	}}); err != nil {
		t.Fatalf("append task: %v", err)
	}
	if err := store.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress}}); err != nil {
		t.Fatalf("start task: %v", err)
	}
	ctx := context.Background()
	if _, err := sess.ProcessInput(ctx, "work before escalation", nil); err != nil {
		t.Fatal(err)
	}
	_ = sess.stuckEscalation(1)
	sess.maybeAutoSave()
	sessionID := sess.id
	sess.Close()

	meta, err := schema.LoadSessionMeta(stateDir, sessionID)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	restored, err := RestoreSessionFromMeta(c, fullLadderProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(stateDir), meta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	if _, err := restored.ProcessInput(ctx, "continue after escalation", nil); err != nil {
		t.Fatal(err)
	}
	reqs := f.Requests()
	if len(reqs) != 2 {
		t.Fatalf("expected two requests across escalation resume, got %d (efforts=%v)", len(reqs), requestEfforts(t, reqs))
	}
	if got := requestEfforts(t, reqs)[1]; got != "high" {
		t.Fatalf("resumed request effort = %q, want sticky escalated effort %q", got, "high")
	}
}

// A plugin agent's template task with its own reasoning_effort must behave the
// same way as a model-authored task: the first request runs at the task's
// effort, but the session's configured effort survives (issue #330 clobber
// site 3: session_init wrote the template's effort into s.cfg).
func TestSession_PluginAgentTemplateTaskEffort_DoesNotClobberSessionEffort(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	pluginDir := filepath.Join(root, "plugin")
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(pluginDir, ".claude-plugin", "plugin.json"), `{"name":"eff-plugin","version":"1.0.0"}`)
	write(filepath.Join(pluginDir, "agents", "eff-agent.md"), `---
name: eff-agent
description: effort template test agent
tasks:
  - title: Cheap step
    prompt: do the cheap thing
    reasoning_effort: low
---
EFF_AGENT_ROLE`)

	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			// req0: template task 1 (low) is auto-started at init.
			func(req llm.Request) llm.Response { return finalResponse("done") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, fullLadderProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(root), SessionConfig{
		ReasoningEffort: "max",
		NonInteractive:  true,
		PluginDirs:      []string{pluginDir},
		AgentName:       "eff-plugin:eff-agent",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if effort := sess.ReasoningEffort(); effort != "max" {
		t.Fatalf("session configured effort = %q after template init, want %q", effort, "max")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "work the plan", nil); err != nil {
		t.Fatal(err)
	}

	reqs := f.Requests()
	if len(reqs) == 0 {
		t.Fatal("no requests recorded")
	}
	if reqs[0].ReasoningEffort == nil || *reqs[0].ReasoningEffort != "low" {
		t.Fatalf("first request effort = %#v, want %q (template task override applies per-round)", reqs[0].ReasoningEffort, "low")
	}
	if effort := sess.ReasoningEffort(); effort != "max" {
		t.Fatalf("session configured effort = %q after first round, want %q", effort, "max")
	}
}

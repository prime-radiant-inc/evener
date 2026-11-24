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
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	taskpkg "primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/llm"
)

// fullLadderProfile returns an OpenAI profile whose effort ladder includes all
// six levels, so the profile clamps nothing away in these tests.
func fullLadderProfile(model string) *provider.Profile {
	return withEffortLevels(provider.NewOpenAIProfile(model), "minimal", "low", "medium", "high", "xhigh", "max")
}

// sessionMaxOnTheWire is what a session configured for "max" effort sends to
// an openai instance. The client shapes every request against its registry
// row before the adapter sees it (spec §7.5) and openai's effort ladder tops
// out at xhigh, so these tests read the clamped value where they mean "the
// session's own effort, unmodified by a task override".
const sessionMaxOnTheWire = "xhigh"

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
		{name: "explicit off", input: " none ", want: "none"},
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

	f := newFakeAdapter("openai", func(req llm.Request) llm.Response {
		return taskEffortToolCall("c1", `{"add":[{"type":"implement","description":"cheap step","prompt":"do cheap thing","reasoning_effort":"low"},{"type":"implement","description":"hard step","prompt":"do hard thing","reasoning_effort":"high"}]}`)
	}, func(req llm.Request) llm.Response {
		return taskEffortToolCall("c2", `{"update":[{"id":1,"status":"in_progress"}]}`)
	}, func(req llm.Request) llm.Response {
		return taskEffortToolCall("c3", `{"update":[{"id":1,"status":"done"}]}`)
	}, func(req llm.Request) llm.Response {
		return taskEffortToolCall("c4", `{"update":[{"id":2,"status":"done"}]}`)
	}, func(req llm.Request) llm.Response { return agenttest.FinalResponse("done") })
	c.Register(f)

	sess, err := NewSession(c, withTestSessionNamer(c, fullLadderProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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
	want := []string{sessionMaxOnTheWire, sessionMaxOnTheWire, "low", "high", sessionMaxOnTheWire}
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

func TestSession_RestoreTaskEffortMigration_ReachesProviderSafely(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name           string
		legacyEffort   string
		wantEffort     string
		wantTaskEffort string
	}{
		{name: "invalid inherits session", legacyEffort: "ultra", wantEffort: sessionMaxOnTheWire, wantTaskEffort: ""},
		{name: "valid override wins", legacyEffort: " HIGH ", wantEffort: "high", wantTaskEffort: "high"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stateDir := t.TempDir()
			// The stale level proves restore sanitization does not trust the
			// row's ladder to validate the task override.
			profile := withEffortLevels(provider.NewOpenAIProfile("gpt-5.2"),
				"minimal", "low", "medium", "high", "xhigh", "max", "ultra")

			seedClient := llm.NewClient()
			seedClient.Register(&fakeAdapter{name: "openai"})
			seedProfile := withTestSessionNamer(seedClient, profile)
			seed, err := NewSession(seedClient, seedProfile, execenv.NewLocalExecutionEnvironment(stateDir), SessionConfig{
				StateDir:        stateDir,
				ReasoningEffort: "max",
			})
			if err != nil {
				t.Fatalf("NewSession: %v", err)
			}
			store := seed.getOrCreateTaskStore()
			if _, err := store.Append([]taskpkg.TaskInput{{
				Type:        taskpkg.TaskTypeImplement,
				Description: "persisted task",
				Prompt:      "resume this task",
			}}); err != nil {
				seed.Close()
				t.Fatalf("Append: %v", err)
			}
			if err := store.Update([]taskpkg.TaskUpdate{{ID: 1, Status: taskpkg.TaskInProgress}}); err != nil {
				seed.Close()
				t.Fatalf("Update: %v", err)
			}
			meta := seed.Meta()
			seed.Close()

			// Install a legacy file over the real saved task JSON, preserving the
			// task's identity and in-progress state while changing only effort.
			taskPath := filepath.Join(stateDir, "tasks", meta.ID+".json")
			legacyJSON, err := os.ReadFile(taskPath)
			if err != nil {
				t.Fatalf("read saved task JSON: %v", err)
			}
			var legacyTasks []taskpkg.Task
			if err := json.Unmarshal(legacyJSON, &legacyTasks); err != nil {
				t.Fatalf("unmarshal saved task JSON: %v", err)
			}
			legacyTasks[0].ReasoningEffort = tc.legacyEffort
			legacyJSON, err = json.Marshal(legacyTasks)
			if err != nil {
				t.Fatalf("marshal legacy task JSON: %v", err)
			}
			if err := os.WriteFile(taskPath, legacyJSON, 0o644); err != nil {
				t.Fatalf("install legacy task JSON: %v", err)
			}

			adapter := newFakeAdapter("openai", func(llm.Request) llm.Response { return agenttest.FinalResponse("resumed") })
			client := llm.NewClient()
			client.Register(adapter)
			restoredProfile := withTestSessionNamer(client, profile)
			restored, err := RestoreSessionFromMeta(client, restoredProfile, execenv.NewLocalExecutionEnvironment(stateDir), meta, stateDir)
			if err != nil {
				t.Fatalf("RestoreSessionFromMeta: %v", err)
			}
			defer restored.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter; only fires on a genuine restore hang.
			defer cancel()
			if _, err := restored.ProcessInput(ctx, "continue the persisted task", nil); err != nil {
				t.Fatalf("ProcessInput after restore: %v", err)
			}
			reqs := adapter.Requests()
			if len(reqs) != 1 {
				t.Fatalf("provider requests = %d, want one; efforts=%v", len(reqs), requestEfforts(t, reqs))
			}
			if got := requestEfforts(t, reqs)[0]; got != tc.wantEffort {
				t.Fatalf("provider effort = %q, want %q", got, tc.wantEffort)
			}
			for _, req := range reqs {
				if req.ReasoningEffort != nil && *req.ReasoningEffort == "ultra" {
					t.Fatal("invalid persisted effort reached provider request")
				}
			}
			tasks := restored.getOrCreateTaskStore().View()
			if len(tasks) != 1 || tasks[0].ID != 1 || tasks[0].Status != taskpkg.TaskInProgress || tasks[0].ReasoningEffort != tc.wantTaskEffort {
				t.Fatalf("restored task = %+v, want preserved identity/status and effort %q", tasks, tc.wantTaskEffort)
			}
		})
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
	var sess *Session

	f := newFakeAdapter("openai", func(req llm.Request) llm.Response {
		if sess != nil {
			sess.mu.Lock()
			if def, ok := sess.deferredToolDefinitionLocked("task_list"); ok {
				req.Tools = append(slices.Clone(req.Tools), def)
			}
			sess.mu.Unlock()
		}
		requireTaskEffortSchemaLevel(t, req, "update", "ultra")
		return taskEffortToolCall("c1", `{"add":[{"type":"implement","description":"first guard","prompt":"keep the first request valid","reasoning_effort":"high"},{"type":"implement","description":"second guard","prompt":"keep the second request valid","reasoning_effort":"low"}]}`)
	}, func(req llm.Request) llm.Response {
		return taskEffortToolCall("c2", `{"update":[{"id":1,"status":"done","reasoning_effort":"max"},{"id":2,"status":"in_progress","reasoning_effort":"ultra"}]}`)
	}, func(req llm.Request) llm.Response {
		requireTaskHandlerError(t, req, "c2")
		return agenttest.FinalResponse("done")
	})
	c.Register(f)

	// Model metadata is an external boundary and may advertise a stale level.
	// The task handler must still enforce Evener's canonical vocabulary rather
	// than trusting the schema enum as its only validation.
	profile := withEffortLevels(provider.NewOpenAIProfile("gpt-5.2"),
		"minimal", "low", "medium", "high", "xhigh", "max", "ultra")
	var err error
	sess, err = NewSession(c, profile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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
	if got, want := requestEfforts(t, f.Requests()), []string{sessionMaxOnTheWire, sessionMaxOnTheWire, sessionMaxOnTheWire}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("request efforts = %v, want %v (invalid task effort must not cross the provider boundary)", got, want)
	}
}

func TestSession_TaskListRejectsInvalidEffortBeforeAppendingTask(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	var sess *Session
	f := newFakeAdapter("openai", func(req llm.Request) llm.Response {
		if sess != nil {
			sess.mu.Lock()
			if def, ok := sess.deferredToolDefinitionLocked("task_list"); ok {
				req.Tools = append(slices.Clone(req.Tools), def)
			}
			sess.mu.Unlock()
		}
		requireTaskEffortSchemaLevel(t, req, "add", "ultra")
		return taskEffortToolCall("c1", `{"add":[{"type":"implement","description":"valid step","prompt":"must roll back with the batch","reasoning_effort":"high"},{"type":"implement","description":"poisoned step","prompt":"must not persist","reasoning_effort":"ultra"}]}`)
	}, func(req llm.Request) llm.Response {
		requireTaskHandlerError(t, req, "c1")
		return agenttest.FinalResponse("done")
	})
	c.Register(f)
	profile := withEffortLevels(provider.NewOpenAIProfile("gpt-5.2"),
		"minimal", "low", "medium", "high", "xhigh", "max", "ultra")
	var err error
	sess, err = NewSession(c, profile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{ReasoningEffort: "max"})
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

	f := newFakeAdapter("openai", func(req llm.Request) llm.Response {
		return taskEffortToolCall("c1", `{"add":[{"type":"implement","description":"a step","prompt":"do the thing","reasoning_effort":"inherit"}]}`)
	}, func(req llm.Request) llm.Response {
		return taskEffortToolCall("c2", `{"update":[{"id":1,"status":"in_progress"}]}`)
	}, func(req llm.Request) llm.Response { return agenttest.FinalResponse("done") })
	c.Register(f)

	sess, err := NewSession(c, withTestSessionNamer(c, fullLadderProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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

// Loop detection steers the model toward a different approach, but it must not
// silently raise the session's reasoning effort. A lower per-task override
// therefore stays in effect for the round after a detection.
func TestSession_LoopDetect_DoesNotEscalateTaskEffort(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	f := newFakeAdapter("openai", func(req llm.Request) llm.Response {
		return taskEffortToolCall("c1", `{"add":[{"type":"implement","description":"cheap step","prompt":"do cheap thing","reasoning_effort":"low"}]}`)
	}, func(req llm.Request) llm.Response {
		return taskEffortToolCall("c2", `{"update":[{"id":1,"status":"in_progress"}]}`)
	}, func(req llm.Request) llm.Response { return agenttest.FinalResponse("still working") }, func(req llm.Request) llm.Response { return agenttest.FinalResponse("done") })
	c.Register(f)

	sess, err := NewSession(c, withTestSessionNamer(c, fullLadderProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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

	// The loop detector fires. It steers, and leaves the configured effort alone.
	_ = sess.stuckEscalation(1)
	if got := sess.cfg.ReasoningEffort; got != "medium" {
		t.Fatalf("ReasoningEffort after loop detection = %q, want %q (no hidden escalation)", got, "medium")
	}

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
	if got[3] != "low" {
		t.Fatalf("request efforts = %v, want request 3 at task override %q (loop detection must not escalate effort)", got, "low")
	}
}

func TestEffectiveReasoningEffort(t *testing.T) {
	t.Parallel()
	cases := []struct {
		cfg, override string
		want          string
	}{
		{"max", "", "max"},
		{"max", "low", "low"},
		{"low", "high", "high"},
		{"", "low", "low"},
		{"", "", ""},
	}
	for _, tc := range cases {
		if got := effectiveReasoningEffort(tc.cfg, tc.override); got != tc.want {
			t.Errorf("effectiveReasoningEffort(%q, %q) = %q, want %q", tc.cfg, tc.override, got, tc.want)
		}
	}
}

func TestSession_TaskEffortSurvivesCompaction(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	f := newFakeAdapter("openai", func(req llm.Request) llm.Response { return agenttest.FinalResponse("done") })
	c.Register(f)

	sess, err := NewSession(c, withTestSessionNamer(c, fullLadderProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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
	f := newFakeAdapter("openai", func(req llm.Request) llm.Response { return agenttest.FinalResponse("before resume") }, func(req llm.Request) llm.Response { return agenttest.FinalResponse("after resume") })
	c.Register(f)

	sess, err := NewSession(c, withTestSessionNamer(c, fullLadderProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(stateDir), SessionConfig{
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
	restored, err := RestoreSessionFromMeta(c, withTestSessionNamer(c, fullLadderProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(stateDir), meta, stateDir)
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

func TestSession_LoopDetect_NoEscalationSurvivesResume(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	c := llm.NewClient()
	f := newFakeAdapter("openai", func(req llm.Request) llm.Response { return agenttest.FinalResponse("before escalation") }, func(req llm.Request) llm.Response { return agenttest.FinalResponse("after resume") })
	c.Register(f)
	sess, err := NewSession(c, withTestSessionNamer(c, fullLadderProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(stateDir), SessionConfig{
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
	if got := sess.cfg.ReasoningEffort; got != "medium" {
		t.Fatalf("ReasoningEffort after loop detection = %q, want %q (no hidden escalation)", got, "medium")
	}
	sess.maybeAutoSave()
	sessionID := sess.id
	sess.Close()

	meta, err := schema.LoadSessionMeta(stateDir, sessionID)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	restored, err := RestoreSessionFromMeta(c, withTestSessionNamer(c, fullLadderProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(stateDir), meta, stateDir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	if got := restored.cfg.ReasoningEffort; got != "medium" {
		t.Fatalf("resumed ReasoningEffort = %q, want %q (no sticky escalation)", got, "medium")
	}
	if _, err := restored.ProcessInput(ctx, "continue after escalation", nil); err != nil {
		t.Fatal(err)
	}
	reqs := f.Requests()
	if len(reqs) != 2 {
		t.Fatalf("expected two requests across escalation resume, got %d (efforts=%v)", len(reqs), requestEfforts(t, reqs))
	}
	if got := requestEfforts(t, reqs)[1]; got != "low" {
		t.Fatalf("resumed request effort = %q, want the task override %q (loop detection must not escalate effort)", got, "low")
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
	f := newFakeAdapter("openai", func(req llm.Request) llm.Response { return agenttest.FinalResponse("done") })
	c.Register(f)

	sess, err := NewSession(c, withTestSessionNamer(c, fullLadderProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(root), SessionConfig{
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

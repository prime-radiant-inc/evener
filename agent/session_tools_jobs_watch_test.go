package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
	tooldefs "primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

func TestJobWatchToolConfiguresWatch(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","background":true}`),
	})
	if shellRes.IsError {
		t.Fatalf("shell returned error: %s", shellRes.Output)
	}
	var shellOut struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(toolResultJSON(shellRes), &shellOut); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, shellRes.Output)
	}
	if shellOut.JobID == "" {
		t.Fatal("background shell returned no job_id")
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch",
		Name:      "job_watch",
		Arguments: json.RawMessage(fmt.Sprintf(`{"operation":"create","source":%q,"output_match":"(?i)ready"}`, shellOut.JobID)),
	})
	if res.IsError {
		t.Fatalf("job_watch returned error: %s", res.Output)
	}
	watchJSON := string(toolResultJSON(res))
	if !strings.Contains(watchJSON, `"watching":true`) {
		t.Fatalf("job_watch state = %s, want watching true", watchJSON)
	}
	// The contract's install example shows replaced_existing explicitly false,
	// not omitted (docs/job-control.md § job_watch result).
	if !strings.Contains(watchJSON, `"replaced_existing":false`) {
		t.Fatalf("job_watch state = %s, want explicit replaced_existing:false", watchJSON)
	}
	if s.jobManager.watchCount() != 1 {
		t.Fatalf("watch count = %d, want 1", s.jobManager.watchCount())
	}
}

func TestJobWatchToolTreatsNullOptionalIntegersAsOmitted(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)

	rec, err := s.jobManager.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, s.jobManager, rec.JobID) })

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:   "watch",
		Name: "job_watch",
		Arguments: json.RawMessage(fmt.Sprintf(
			`{"operation":"create","source":%q,"output_match":"ready","progress_interval_ms":null,"every":null}`,
			rec.JobID,
		)),
	})
	if res.IsError {
		t.Fatalf("job_watch returned error for null optional integers: %s", res.Output)
	}

	var out jobWatchToolResult
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal job_watch output: %v (output: %s)", err, res.Output)
	}
	if !out.Watching || out.ProgressIntervalMS != 0 {
		t.Fatalf("job_watch output = %+v, want null integers omitted", out)
	}
}

func TestWatchArgsFromToolArgsUsesSource(t *testing.T) {
	t.Parallel()
	got, err := watchArgsFromToolArgs(map[string]any{
		"operation": "create",
		"source":    "parent",
	})
	if err != nil {
		t.Fatalf("watchArgsFromToolArgs returned error: %v", err)
	}
	if got.Source != "parent" {
		t.Fatalf("Source = %q, want parent", got.Source)
	}
	if got.Target != "" {
		t.Fatalf("legacy Target = %q, want empty model-facing parse", got.Target)
	}
}

func TestWatchArgsFromToolArgsRejectsLegacyTargetAndSend(t *testing.T) {
	t.Parallel()
	for name, args := range map[string]map[string]any{
		"target": {
			"operation": "create",
			"target":    "caller",
		},
		"send": {
			"operation": "create",
			"source":    "parent",
			"send":      map[string]any{"to": "dlg_old"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := watchArgsFromToolArgs(args)
			if err == nil {
				t.Fatal("watchArgsFromToolArgs succeeded, want invalid_request")
			}
			if !strings.Contains(err.Error(), "invalid_request") {
				t.Fatalf("error = %v, want invalid_request", err)
			}
		})
	}
}

func TestJobWatchCreateReturnsIDAndClearUsesIDOnly(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","background":true}`),
	})
	if shellRes.IsError {
		t.Fatalf("shell returned error: %s", shellRes.Output)
	}
	var shellOut struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(toolResultJSON(shellRes), &shellOut); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, shellRes.Output)
	}
	if shellOut.JobID == "" {
		t.Fatal("background shell returned no job_id")
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	createRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch",
		Name:      "job_watch",
		Arguments: json.RawMessage(fmt.Sprintf(`{"operation":"create","source":%q,"output_match":"ready"}`, shellOut.JobID)),
	})
	if createRes.IsError {
		t.Fatalf("job_watch create returned error: %s", createRes.Output)
	}
	var created struct {
		WatchID  string `json:"watch_id"`
		Watching bool   `json:"watching"`
	}
	if err := json.Unmarshal(toolResultJSON(createRes), &created); err != nil {
		t.Fatalf("unmarshal create watch output: %v (output: %s)", err, createRes.Output)
	}
	if !strings.HasPrefix(created.WatchID, "watch_") {
		t.Fatalf("watch_id = %q, want watch_ prefix", created.WatchID)
	}
	if !created.Watching {
		t.Fatalf("create result = %+v, want watching=true", created)
	}

	clearRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "clear",
		Name:      "job_watch",
		Arguments: json.RawMessage(fmt.Sprintf(`{"operation":"clear","watch_id":%q}`, created.WatchID)),
	})
	if clearRes.IsError {
		t.Fatalf("job_watch clear returned error: %s", clearRes.Output)
	}
	var cleared struct {
		WatchID  string `json:"watch_id"`
		Watching bool   `json:"watching"`
	}
	if err := json.Unmarshal(toolResultJSON(clearRes), &cleared); err != nil {
		t.Fatalf("unmarshal clear watch output: %v (output: %s)", err, clearRes.Output)
	}
	if cleared.WatchID != created.WatchID {
		t.Fatalf("cleared watch_id = %q, want %q", cleared.WatchID, created.WatchID)
	}
	if cleared.Watching {
		t.Fatalf("clear result = %+v, want watching=false", cleared)
	}
	watches, err := s.jobManager.store.LoadWatches()
	if err != nil {
		t.Fatalf("LoadWatches: %v", err)
	}
	if w := watches[created.WatchID]; w == nil || w.Active || w.EndReason != "cleared" {
		t.Fatalf("durable watch %s = %+v, want inactive cleared", created.WatchID, w)
	}

	staleClearRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "clear-again",
		Name:      "job_watch",
		Arguments: json.RawMessage(fmt.Sprintf(`{"operation":"clear","watch_id":%q}`, created.WatchID)),
	})
	if staleClearRes.IsError {
		t.Fatalf("stale job_watch clear returned error: %s", staleClearRes.Output)
	}
	if !strings.Contains(staleClearRes.Output, created.WatchID) || strings.Contains(staleClearRes.Output, "watch on  cleared") {
		t.Fatalf("stale clear output = %q, want watch_id and no empty target footer", staleClearRes.Output)
	}
}

func TestJobWatchRejectsRemovedPublicShapes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args string
		want string
	}{
		{name: "missing operation", args: `{"source":"self","events":["job.notification"]}`, want: "missing properties: 'operation'"},
		{name: "unsupported operation", args: `{"operation":"pause","source":"self","events":["job.notification"]}`, want: "value must be one of"},
		{name: "create without source", args: `{"operation":"create","events":["job.notification"]}`, want: "source is required"},
		{name: "inspect without watch id", args: `{"operation":"inspect"}`, want: "watch_id is required"},
		{name: "clear without watch id", args: `{"operation":"clear"}`, want: "watch_id is required"},
		{name: "source wildcard", args: `{"operation":"create","source":"*","events":["job.notification"]}`, want: "wildcard watch target is not supported"},
		{name: "legacy target rejected", args: `{"operation":"create","target":"caller","events":["job.notification"]}`, want: "additionalProperties 'target' not allowed"},
		{name: "legacy send rejected", args: `{"operation":"create","source":"self","events":["job.notification"],"send":{"to":"job_observer","message":"observe"}}`, want: "additionalProperties 'send' not allowed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSession(t)
			res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
				ID:        "watch",
				Name:      "job_watch",
				Arguments: json.RawMessage(tc.args),
			})
			if !res.IsError {
				t.Fatalf("job_watch succeeded, want error containing %q: %s", tc.want, res.Output)
			}
			if !strings.Contains(res.Output, tc.want) {
				t.Fatalf("job_watch error = %q, want %q", res.Output, tc.want)
			}
			if s.jobManager.watchCount() != 0 {
				t.Fatalf("watch count = %d, want 0", s.jobManager.watchCount())
			}
		})
	}
}

func TestJobWatchValidationGuidesObserversToParentSource(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args string
		want string
	}{
		{
			name: "session output_match",
			args: `{"operation":"create","source":"self","events":["communicate"],"output_match":"ready"}`,
			want: `source="parent"`,
		},
		{
			name: "communicate event filter",
			args: `{"operation":"create","source":"self","events":["communicate"],"event_filter":{"tool_name":"read_file","status":"ok"}}`,
			want: `source="parent"`,
		},
		{
			name: "self tool event loop",
			args: `{"operation":"create","source":"self","events":["assistant.tool"]}`,
			want: `delegate(watch_parent=true)`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSession(t)
			res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
				ID:        "watch",
				Name:      "job_watch",
				Arguments: json.RawMessage(tc.args),
			})
			if !res.IsError {
				t.Fatalf("job_watch succeeded, want validation guidance error: %s", res.Output)
			}
			if !strings.Contains(res.Output, tc.want) {
				t.Fatalf("job_watch error = %q, want repair guidance %q", res.Output, tc.want)
			}
			if strings.Contains(res.Output, "send.to") {
				t.Fatalf("job_watch error leaks removed send.to observer guidance: %q", res.Output)
			}
			if s.jobManager.watchCount() != 0 {
				t.Fatalf("watch count = %d, want 0", s.jobManager.watchCount())
			}
		})
	}
}

func TestJobWatchDuplicateCreateReturnsSameIDAndChangedConfigReturnsNewID(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","background":true}`),
	})
	if shellRes.IsError {
		t.Fatalf("shell returned error: %s", shellRes.Output)
	}
	var shellOut struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(toolResultJSON(shellRes), &shellOut); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, shellRes.Output)
	}
	if shellOut.JobID == "" {
		t.Fatal("background shell returned no job_id")
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	createWatch := func(outputMatch string) string {
		t.Helper()
		res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
			ID:        "watch-" + outputMatch,
			Name:      "job_watch",
			Arguments: json.RawMessage(fmt.Sprintf(`{"operation":"create","source":%q,"output_match":%q}`, shellOut.JobID, outputMatch)),
		})
		if res.IsError {
			t.Fatalf("job_watch create %q returned error: %s", outputMatch, res.Output)
		}
		var out struct {
			WatchID string `json:"watch_id"`
		}
		if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
			t.Fatalf("unmarshal watch output: %v (output: %s)", err, res.Output)
		}
		if !strings.HasPrefix(out.WatchID, "watch_") {
			t.Fatalf("watch_id = %q, want watch_ prefix", out.WatchID)
		}
		return out.WatchID
	}

	firstID := createWatch("ready")
	duplicateID := createWatch("ready")
	if duplicateID != firstID {
		t.Fatalf("duplicate create watch_id = %q, want %q", duplicateID, firstID)
	}

	changedID := createWatch("done")
	if changedID == firstID {
		t.Fatalf("changed config reused watch_id %q", changedID)
	}
}

func TestJobWatchListAndInspectReturnWatchIDs(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","background":true}`),
	})
	if shellRes.IsError {
		t.Fatalf("shell returned error: %s", shellRes.Output)
	}
	var shellOut struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(toolResultJSON(shellRes), &shellOut); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, shellRes.Output)
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	createRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch",
		Name:      "job_watch",
		Arguments: json.RawMessage(fmt.Sprintf(`{"operation":"create","source":%q,"output_match":"ready"}`, shellOut.JobID)),
	})
	if createRes.IsError {
		t.Fatalf("job_watch create returned error: %s", createRes.Output)
	}
	var created struct {
		WatchID string `json:"watch_id"`
	}
	if err := json.Unmarshal(toolResultJSON(createRes), &created); err != nil {
		t.Fatalf("unmarshal create watch output: %v (output: %s)", err, createRes.Output)
	}

	listRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "list",
		Name:      "job_watch",
		Arguments: json.RawMessage(`{"operation":"list"}`),
	})
	if listRes.IsError {
		t.Fatalf("job_watch list returned error: %s", listRes.Output)
	}
	var listed struct {
		Watches []struct {
			WatchID  string `json:"watch_id"`
			Source   string `json:"source"`
			Watching bool   `json:"watching"`
		} `json:"watches"`
	}
	if err := json.Unmarshal(toolResultJSON(listRes), &listed); err != nil {
		t.Fatalf("unmarshal list watch output: %v (output: %s)", err, listRes.Output)
	}
	if len(listed.Watches) != 1 || listed.Watches[0].WatchID != created.WatchID || listed.Watches[0].Source != shellOut.JobID || !listed.Watches[0].Watching {
		t.Fatalf("list result = %+v, want active watch %s", listed, created.WatchID)
	}

	inspectRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "inspect",
		Name:      "job_watch",
		Arguments: json.RawMessage(fmt.Sprintf(`{"operation":"inspect","watch_id":%q}`, created.WatchID)),
	})
	if inspectRes.IsError {
		t.Fatalf("job_watch inspect returned error: %s", inspectRes.Output)
	}
	var inspected struct {
		WatchID  string `json:"watch_id"`
		Source   string `json:"source"`
		Watching bool   `json:"watching"`
	}
	if err := json.Unmarshal(toolResultJSON(inspectRes), &inspected); err != nil {
		t.Fatalf("unmarshal inspect watch output: %v (output: %s)", err, inspectRes.Output)
	}
	if inspected.WatchID != created.WatchID || inspected.Source != shellOut.JobID || !inspected.Watching {
		t.Fatalf("inspect result = %+v, want active watch %s", inspected, created.WatchID)
	}
}

func TestJobWatchCanImmediatelyWatchReturnedBackgroundShellJob(t *testing.T) {
	t.Parallel()
	s := newPersistentTestSession(t)
	const token = "WATCH_OUTPUT_TOKEN_ONCE"

	// Use a long-running shell so the job stays alive without a timing
	// dependency. The token is injected directly via the output pipeline below
	// to avoid the wall-clock race that made the original sleep-based approach
	// flaky in CI.
	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"sleep 30","background":true}`),
	})
	if shellRes.IsError {
		t.Fatalf("shell returned error: %s", shellRes.Output)
	}
	var shellOut struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(toolResultJSON(shellRes), &shellOut); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, shellRes.Output)
	}
	if shellOut.JobID == "" {
		t.Fatal("background shell returned no job_id")
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	watchArgs, err := json.Marshal(map[string]any{
		"operation":    "create",
		"source":       shellOut.JobID,
		"output_match": token,
	})
	if err != nil {
		t.Fatal(err)
	}
	watchRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch",
		Name:      "job_watch",
		Arguments: watchArgs,
	})
	if watchRes.IsError {
		t.Fatalf("job_watch returned error for returned job_id %s: %s", shellOut.JobID, watchRes.Output)
	}

	// Inject the token through the full output pipeline so watch matchers fire
	// deterministically, without relying on the real shell process timing.
	s.jobManager.mu.Lock()
	run := s.jobManager.running[shellOut.JobID]
	s.jobManager.mu.Unlock()
	if run == nil {
		t.Fatalf("job %s is not running after watch install", shellOut.JobID)
	}
	if _, err := s.jobManager.appendJobOutput(shellOut.JobID, run.output, []byte(token+"\n")); err != nil {
		t.Fatalf("inject token into job output: %v", err)
	}

	// Observation enqueues a caller wake token on the notification queue (no
	// synchronous delivery, spec §3). The live loop would wake and accept; here
	// we wait for the wake and accept it, which renders the frame into the
	// notification turn (a TurnSteering in history) and settles the pending.
	waitForJobNotification(t, s)
	drainAndAccept(t, s)

	first := waitForSteeringEntryContaining(t, s, token)
	if !strings.Contains(first, token) {
		t.Fatalf("watch delivery = %q, want token", first)
	}
	if got := countSteeringEntriesContaining(s, token); got != 1 {
		t.Fatalf("watch deliveries containing %q = %d, want 1", token, got)
	}
}

// TestJobWatchTerminalOutputMatchCatchupThroughTool drives spec §7.1's terminal
// catch-up end to end through the job_watch tool: an output_match-only watch on an
// already-terminal job whose retained output matches returns terminal_catchup with
// fired=true (no live watch installed), and the new fields surface in the tool
// JSON. A non-matching catch-up reports terminal_catchup with an explicit
// fired=false — contract §7.1 promises "fired=false on none", not omission.

// TestJobWatchTerminalOutputMatchCatchupThroughTool drives spec §7.1's terminal
// catch-up end to end through the job_watch tool: an output_match-only watch on an
// already-terminal job whose retained output matches returns terminal_catchup with
// fired=true (no live watch installed), and the new fields surface in the tool
// JSON. A non-matching catch-up reports terminal_catchup with an explicit
// fired=false — contract §7.1 promises "fired=false on none", not omission.
func TestJobWatchTerminalOutputMatchCatchupThroughTool(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)

	// Use a manual job so we have a durable record with known output. Fast
	// commands that produce small output return ephemeral (no job_id) after the
	// complete-or-handle invariant lands; creating the record directly avoids
	// that path and keeps the test focused on §7.1 terminal catch-up.
	rec := newManualRunningJob(t, s)
	appendManualJobOutput(s.jobManager, rec.JobID, "already-done\n")
	if err := s.jobManager.finalize(rec.JobID, jobstore.StatusCompleted, "", nil); err != nil {
		t.Fatalf("finalize manual job: %v", err)
	}
	waitForShellDone(t, s.jobManager, rec.JobID)
	watchedJobID := rec.JobID

	watchRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch",
		Name:      "job_watch",
		Arguments: json.RawMessage(fmt.Sprintf(`{"operation":"create","source":%q,"output_match":"already-done"}`, watchedJobID)),
	})
	if watchRes.IsError {
		t.Fatalf("terminal output_match catch-up must not error: %s", watchRes.Output)
	}
	var matched struct {
		Watching        bool   `json:"watching"`
		Fired           bool   `json:"fired"`
		TerminalCatchup bool   `json:"terminal_catchup"`
		Status          string `json:"status"`
	}
	if err := json.Unmarshal(toolResultJSON(watchRes), &matched); err != nil {
		t.Fatalf("unmarshal watch output: %v (%s)", err, watchRes.Output)
	}
	if matched.Watching || !matched.Fired || !matched.TerminalCatchup || matched.Status != "completed" {
		t.Fatalf("matched catch-up tool result = %+v, want fired+terminal_catchup+completed", matched)
	}

	// A non-matching output_match-only watch on the same terminal job catches up
	// without firing.
	noMatchRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch2",
		Name:      "job_watch",
		Arguments: json.RawMessage(fmt.Sprintf(`{"operation":"create","source":%q,"output_match":"never-printed"}`, watchedJobID)),
	})
	if noMatchRes.IsError {
		t.Fatalf("non-matching terminal catch-up must not error: %s", noMatchRes.Output)
	}
	noMatchJSON := string(toolResultJSON(noMatchRes))
	if !strings.Contains(noMatchJSON, `"terminal_catchup":true`) {
		t.Fatalf("non-matching catch-up state = %s, want terminal_catchup", noMatchJSON)
	}
	if !strings.Contains(noMatchJSON, `"fired":false`) {
		t.Fatalf("non-matching catch-up must report explicit fired:false (contract §7.1): %s", noMatchJSON)
	}
}

func TestJobWatchNoConditionErrors(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	rec, err := s.jobManager.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	t.Cleanup(func() { finishRunningTestJob(t, s.jobManager, rec.JobID) })

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch",
		Name:      "job_watch",
		Arguments: json.RawMessage(fmt.Sprintf(`{"operation":"create","source":%q}`, rec.JobID)),
	})
	if !res.IsError {
		t.Fatalf("job_watch succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "invalid_request: nothing to watch") {
		t.Fatalf("job_watch error = %q, want no-condition error", res.Output)
	}
}

func TestJobWatchToolMainAliasTargetFailsTargetNotFound(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch",
		Name:      "job_watch",
		Arguments: json.RawMessage(`{"operation":"create","target":"main"}`),
	})

	if !res.IsError {
		t.Fatalf("job_watch succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "additionalProperties 'target' not allowed") {
		t.Fatalf("job_watch error = %q, want public target rejection", res.Output)
	}
	if s.jobManager.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", s.jobManager.watchCount())
	}
}

func TestJobWatchToolWatchedTargetWithoutContextFailsTargetNotFound(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch",
		Name:      "job_watch",
		Arguments: json.RawMessage(`{"operation":"create","target":"watched"}`),
	})

	if !res.IsError {
		t.Fatalf("job_watch succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "additionalProperties 'target' not allowed") {
		t.Fatalf("job_watch error = %q, want public target rejection", res.Output)
	}
	if s.jobManager.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", s.jobManager.watchCount())
	}
}

func TestJobWatchToolSendToMainAliasFailsTargetNotFound(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch",
		Name:      "job_watch",
		Arguments: json.RawMessage(`{"operation":"create","target":"caller","events":["communicate"],"send":{"to":"main","message":"observe"}}`),
	})

	if !res.IsError {
		t.Fatalf("job_watch succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "additionalProperties") ||
		!strings.Contains(res.Output, "'target'") ||
		!strings.Contains(res.Output, "'send'") {
		t.Fatalf("job_watch error = %q, want public target/send rejection", res.Output)
	}
	if s.jobManager.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", s.jobManager.watchCount())
	}
}

func TestJobWatchSendToRequired(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		send string
	}{
		{name: "message without target", send: `{"message":"observe"}`},
		{name: "excerpt without target", send: `{"to":"   ","include_excerpt":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSession(t)
			res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
				ID:        "watch",
				Name:      "job_watch",
				Arguments: json.RawMessage(fmt.Sprintf(`{"operation":"create","source":"self","events":["communicate"],"send":%s}`, tc.send)),
			})
			if !res.IsError {
				t.Fatalf("job_watch succeeded, want public send rejection: %s", res.Output)
			}
			if !strings.Contains(res.Output, "additionalProperties 'send' not allowed") {
				t.Fatalf("job_watch error = %q, want public send rejection", res.Output)
			}
			if s.jobManager.watchCount() != 0 {
				t.Fatalf("watch count = %d, want 0", s.jobManager.watchCount())
			}
		})
	}
}

func TestJobWatchEmptySendPlaceholderIsOmitted(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch",
		Name:      "job_watch",
		Arguments: json.RawMessage(`{"operation":"create","source":"self","events":["job.notification"],"send":{"to":"","message":"","include_excerpt":false}}`),
	})
	if !res.IsError {
		t.Fatalf("job_watch accepted public send placeholder, want schema rejection: %s", res.Output)
	}
	if !strings.Contains(res.Output, "additionalProperties 'send' not allowed") {
		t.Fatalf("job_watch error = %q, want public send rejection", res.Output)
	}
}

func TestMarshalWatchResultSurfacesEventFilter(t *testing.T) {
	t.Parallel()
	res, err := marshalWatchResult(watchResult{
		Target:   runtimeMessageAliasCaller,
		Watching: true,
		Events:   []string{"assistant.tool"},
		EventFilter: &watchEventFilter{
			ToolName: "read_file",
			Status:   "ok",
		},
	}, 4096)
	if err != nil {
		t.Fatalf("marshal watch result: %v", err)
	}
	var out jobWatchToolResult
	if err := json.Unmarshal(handlerJSON(t, res), &out); err != nil {
		t.Fatalf("unmarshal watch result: %v (%s)", err, res)
	}
	if out.EventFilter == nil || out.EventFilter.ToolName != "read_file" || out.EventFilter.Status != "ok" {
		t.Fatalf("event_filter = %+v, want read_file/ok", out.EventFilter)
	}
	state, ok := res.(tooldefs.StateResult)
	if !ok {
		t.Fatalf("result type = %T, want StateResult", res)
	}
	if !strings.Contains(state.Output, "where tool_name=read_file,status=ok") {
		t.Fatalf("model output = %q, want event filter summary", state.Output)
	}
}

func TestMarshalWatchResultSurfacesWatchIDInModelOutput(t *testing.T) {
	t.Parallel()
	created, err := marshalWatchResult(watchResult{
		WatchID:  "watch_visible",
		Target:   runtimeMessageAliasCaller,
		Watching: true,
		Events:   []string{"assistant.tool"},
	}, 4096)
	if err != nil {
		t.Fatalf("marshal created watch result: %v", err)
	}
	createdState, ok := created.(tooldefs.StateResult)
	if !ok {
		t.Fatalf("created result type = %T, want StateResult", created)
	}
	if !strings.Contains(createdState.Output, "watch_id watch_visible") {
		t.Fatalf("created model output = %q, want watch_id", createdState.Output)
	}

	cleared, err := marshalWatchResult(watchResult{
		WatchID:  "watch_visible",
		Target:   runtimeMessageAliasCaller,
		Watching: false,
	}, 4096)
	if err != nil {
		t.Fatalf("marshal cleared watch result: %v", err)
	}
	clearedState, ok := cleared.(tooldefs.StateResult)
	if !ok {
		t.Fatalf("cleared result type = %T, want StateResult", cleared)
	}
	if !strings.Contains(clearedState.Output, "watch_id watch_visible") {
		t.Fatalf("cleared model output = %q, want watch_id", clearedState.Output)
	}
}

// TestMarshalWatchResultSurfacesFired pins the tool-JSON projection of an
// attach-time fire: a watchResult with Fired=true renders "fired":true, and
// Fired=false omits the field (omitempty), so the agent learns its condition was
// already true without waiting a turn (spec §7.1).

// TestMarshalWatchResultSurfacesFired pins the tool-JSON projection of an
// attach-time fire: a watchResult with Fired=true renders "fired":true, and
// Fired=false omits the field (omitempty), so the agent learns its condition was
// already true without waiting a turn (spec §7.1).
func TestMarshalWatchResultSurfacesFired(t *testing.T) {
	t.Parallel()
	firedOut, err := marshalWatchResult(watchResult{
		Target:      "job_1",
		Watching:    true,
		OutputMatch: "ready",
		Fired:       true,
	}, 4096)
	if err != nil {
		t.Fatalf("marshal fired result: %v", err)
	}
	var fired jobWatchToolResult
	if err := json.Unmarshal(handlerJSON(t, firedOut), &fired); err != nil {
		t.Fatalf("unmarshal fired result: %v (%s)", err, firedOut)
	}
	if !fired.Fired {
		t.Fatalf("fired result must project fired=true, got %s", firedOut)
	}
	if !strings.Contains(string(handlerJSON(t, firedOut)), `"fired":true`) {
		t.Fatalf("fired result JSON = %s, want it to contain \"fired\":true", firedOut)
	}

	notFiredOut, err := marshalWatchResult(watchResult{
		Target:      "job_1",
		Watching:    true,
		OutputMatch: "ready",
		Fired:       false,
	}, 4096)
	if err != nil {
		t.Fatalf("marshal not-fired result: %v", err)
	}
	// Contract §7.1: fired serializes explicitly even when false.
	if !strings.Contains(string(handlerJSON(t, notFiredOut)), `"fired":false`) {
		t.Fatalf("not-fired result JSON = %s, want explicit \"fired\":false", notFiredOut)
	}
	var notFired jobWatchToolResult
	if err := json.Unmarshal(handlerJSON(t, notFiredOut), &notFired); err != nil {
		t.Fatalf("unmarshal not-fired result: %v (%s)", err, notFiredOut)
	}
	if notFired.Fired {
		t.Fatal("not-fired result must project fired=false")
	}
}

func TestJobWatchAdvertisedDefinitionUsesCanonicalEventKinds(t *testing.T) {
	t.Parallel()
	want := tooldefs.DefJobWatch(WatchEventKindNames)
	var got *llm.ToolDefinition
	for _, def := range NewOpenAIProfile("gpt-5.2").ToolDefinitions() {
		if def.Name == "job_watch" {
			got = &def
			break
		}
	}
	if got == nil {
		t.Fatal("OpenAI profile does not advertise job_watch")
	}
	if got.Description != want.Description {
		t.Fatalf("job_watch description drifted from WatchEventKindNames\n got: %q\nwant: %q", got.Description, want.Description)
	}
}

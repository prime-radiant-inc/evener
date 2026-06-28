package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	tooldefs "primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/provenance"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestJobToolsRejectDelegateIDWithActionableGuidance(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(req llm.Request) llm.Response { return communicateWithDefaultOutput("done") },
	}})
	s := newDelegateTestSession(t, c)
	res := s.createDelegate(context.Background(), delegateArgs{Task: "finish", Background: false, BlockTimeoutMS: 5000})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}

	for _, tc := range []struct {
		name string
		tool string
		args string
		want string
	}{
		{"status", "job_status", fmt.Sprintf(`{"job_id":%q}`, res.DelegateID), "delegate_id is a conversation handle; inspect a concrete job_id"},
		{"stop", "job_stop", fmt.Sprintf(`{"job_id":%q}`, res.DelegateID), "delegate_id is a conversation handle; stop a concrete job_id"},
		{"watch", "job_watch", fmt.Sprintf(`{"operation":"create","source":%q,"events":["communicate"]}`, res.DelegateID), "delegate_id is a conversation handle; watch source self, parent, or a concrete job_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
				ID:        tc.name,
				Name:      tc.tool,
				Arguments: json.RawMessage(tc.args),
			})
			if !call.IsError {
				t.Fatalf("%s succeeded, want error: %s", tc.tool, call.Output)
			}
			if !strings.Contains(call.Output, tc.want) {
				t.Fatalf("%s error = %q, want %q", tc.tool, call.Output, tc.want)
			}
		})
	}
}

// TestJobReadOutputUnknownIDPointsToJobList pins the not-found recovery hint:
// when a job_id resolves to nothing (a guessed id, or a foreground command whose
// output rode inline and kept no durable job), the error must redirect the model
// to job_list rather than dead-ending on a bare "not found".

func TestJobToolsControlBackgroundShellJob(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"printf 'ready-line\n'; sleep 30","background":true}`),
	})
	if shellRes.IsError {
		t.Fatalf("shell returned error: %s", shellRes.Output)
	}
	var shellOut struct {
		JobID  string `json:"job_id"`
		Type   string `json:"type"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(toolResultJSON(shellRes), &shellOut); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, shellRes.Output)
	}
	if shellOut.JobID == "" || shellOut.Type != string(jobstore.JobShell) || shellOut.Status != string(jobstore.StatusRunning) {
		t.Fatalf("shell output = %+v, want running shell job", shellOut)
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	listRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "list",
		Name:      "job_list",
		Arguments: json.RawMessage(`{"status":["running"],"type":["shell"],"limit":10}`),
	})
	if listRes.IsError {
		t.Fatalf("job_list returned error: %s", listRes.Output)
	}
	var listOut struct {
		Jobs []struct {
			JobID               string  `json:"job_id"`
			Type                string  `json:"type"`
			Status              string  `json:"status"`
			Reason              *string `json:"reason"`
			Description         string  `json:"description"`
			ParentJobID         *string `json:"parent_job_id"`
			OwnerSessionID      string  `json:"owner_session_id"`
			VisibleToSessionID  string  `json:"visible_to_session_id"`
			TranscriptRef       *string `json:"transcript_ref"`
			Resumable           *bool   `json:"resumable"`
			NotResumableReason  *string `json:"not_resumable_reason"`
			StartedAt           string  `json:"started_at"`
			EndedAt             *string `json:"ended_at"`
			ExitCode            *int    `json:"exit_code"`
			TotalBytes          int64   `json:"total_bytes"`
			TerminalGeneration  *string `json:"terminal_generation"`
			TerminalNotifyState string  `json:"terminal_notification_state"`
		} `json:"jobs"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(toolResultJSON(listRes), &listOut); err != nil {
		t.Fatalf("unmarshal job_list output: %v (output: %s)", err, listRes.Output)
	}
	if listOut.Count != len(listOut.Jobs) {
		t.Fatalf("job_list output = %+v, want count=len(jobs)", listOut)
	}
	if len(listOut.Jobs) != 1 {
		t.Fatalf("job_list jobs = %+v, want one running shell job", listOut.Jobs)
	}
	job := listOut.Jobs[0]
	if job.JobID != shellOut.JobID ||
		job.Type != string(jobstore.JobShell) ||
		job.Status != string(jobstore.StatusRunning) ||
		job.Reason != nil ||
		job.ParentJobID != nil ||
		job.OwnerSessionID == "" ||
		job.StartedAt == "" ||
		job.EndedAt != nil ||
		job.ExitCode != nil {
		t.Fatalf("job_list job = %+v, want running shell projection for %s", job, shellOut.JobID)
	}

	readOut := waitForJobOutput(t, s, shellOut.JobID, "ready-line")
	if readOut.JobID != shellOut.JobID ||
		readOut.Type != string(jobstore.JobShell) ||
		readOut.Status != string(jobstore.StatusRunning) ||
		readOut.Reason != nil ||
		!strings.Contains(readOut.Content, "ready-line") ||
		readOut.TotalBytes == 0 ||
		readOut.Truncated ||
		readOut.ExitCode != nil ||
		readOut.Grep != "ready" ||
		len(readOut.Matches) != 1 ||
		!strings.Contains(readOut.Matches[0].Line, "ready-line") ||
		readOut.Matches[0].ByteOffset == nil {
		t.Fatalf("job_read_output = %+v, want running shell output with grep match", readOut)
	}

	stopRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "stop",
		Name:      "job_stop",
		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"max_wait_ms":1000}`, shellOut.JobID)),
	})
	if stopRes.IsError {
		t.Fatalf("job_stop returned error: %s", stopRes.Output)
	}
	var stopOut struct {
		JobID  string  `json:"job_id"`
		Status string  `json:"status"`
		Reason *string `json:"reason"`
	}
	if err := json.Unmarshal(toolResultJSON(stopRes), &stopOut); err != nil {
		t.Fatalf("unmarshal job_stop output: %v (output: %s)", err, stopRes.Output)
	}
	if stopOut.JobID != shellOut.JobID || stopOut.Status != string(jobstore.StatusCancelled) || stopOut.Reason == nil || *stopOut.Reason != "stopped_by_parent" {
		t.Fatalf("job_stop = %+v, want cancelled/stopped_by_parent", stopOut)
	}

	waitForShellDone(t, s.jobManager, shellOut.JobID)
	rec := loadShellRecord(t, s.jobManager, shellOut.JobID)
	if rec.Status != jobstore.StatusCancelled || rec.Reason != "stopped_by_parent" {
		t.Fatalf("durable job after stop = %+v, want cancelled/stopped_by_parent", rec)
	}
}

func TestDelegateSendToolMainAliasFailsInvalidRequestWithoutSideEffects(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	called := false
	s.cfg.spawn.parentSteer = func(string, *provenance.Causal) { called = true }

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "send",
		Name:      "delegate_send",
		Arguments: json.RawMessage(`{"to":"main","message":"hello"}`),
	})

	if !res.IsError {
		t.Fatalf("delegate_send succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "invalid_request") {
		t.Fatalf("delegate_send error = %q, want invalid_request", res.Output)
	}
	if called {
		t.Fatal("main alias called parentSteer")
	}
	if queue := s.SteeringQueueSnapshot(); len(queue) != 0 {
		t.Fatalf("steering queue = %+v, want no side effects", queue)
	}
	if jobs := s.jobManager.list(listFilter{}); len(jobs) != 0 {
		t.Fatalf("jobs = %+v, want no jobs created", jobs)
	}
	s.jobManager.mu.Lock()
	runningJobs := len(s.jobManager.running)
	s.jobManager.mu.Unlock()
	if runningJobs != 0 {
		t.Fatalf("running jobs = %d, want no runs created", runningJobs)
	}
}

func TestJobStopDefaultReturnsRequestedCancellation(t *testing.T) {
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

	stopRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "stop",
		Name:      "job_stop",
		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q}`, shellOut.JobID)),
	})
	if stopRes.IsError {
		t.Fatalf("job_stop returned error: %s", stopRes.Output)
	}
	var stopOut struct {
		JobID  string  `json:"job_id"`
		Status string  `json:"status"`
		Reason *string `json:"reason"`
	}
	if err := json.Unmarshal(toolResultJSON(stopRes), &stopOut); err != nil {
		t.Fatalf("unmarshal job_stop output: %v (output: %s)", err, stopRes.Output)
	}
	if stopOut.JobID != shellOut.JobID || stopOut.Status != string(jobstore.StatusCancelled) || stopOut.Reason == nil || *stopOut.Reason != "stopped_by_parent" {
		t.Fatalf("job_stop = %+v, want immediate cancelled/stopped_by_parent", stopOut)
	}

	waitForShellDone(t, s.jobManager, shellOut.JobID)
}

func TestJobStopBlockTimeoutReturnsStopPending(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	rec, err := s.jobManager.createShell(createShellOpts{Command: "sleep 30"})
	if err != nil {
		t.Fatalf("create shell: %v", err)
	}
	t.Cleanup(func() {
		if err := s.jobManager.finalize(rec.JobID, jobstore.StatusCancelled, "stopped_by_parent", nil); err != nil {
			t.Fatalf("cleanup finalize: %v", err)
		}
		waitForShellDone(t, s.jobManager, rec.JobID)
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := jobStopTool(ctx, s, map[string]any{
		"job_id":      rec.JobID,
		"max_wait_ms": 60000,
	}, 20000)
	if err != nil {
		t.Fatalf("jobStopTool returned error: %v", err)
	}
	var stopOut struct {
		JobID  string  `json:"job_id"`
		Status string  `json:"status"`
		Reason *string `json:"reason"`
	}
	if err := json.Unmarshal(handlerJSON(t, out), &stopOut); err != nil {
		t.Fatalf("unmarshal job_stop output: %v (output: %s)", err, out)
	}
	if stopOut.JobID != rec.JobID || stopOut.Status != string(jobstore.StatusRunning) || stopOut.Reason == nil || *stopOut.Reason != "stop_pending" {
		t.Fatalf("job_stop = %+v, want running/stop_pending", stopOut)
	}
}

func TestDelegateToolForegroundReturnsStructuredResult(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithStructured("done report", map[string]any{
					"summary": "fixed",
				})
			},
		},
	})
	s := newDelegateTestSession(t, c)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:   "delegate",
		Name: "delegate",
		Arguments: json.RawMessage(`{
			"task":"fix the issue",
			"max_wait_ms":5000,
			"result_schema":{
				"type":"object",
				"properties":{"summary":{"type":"string"}},
				"required":["summary"]
			}
		}`),
	})
	if res.IsError {
		t.Fatalf("delegate returned error: %s", res.Output)
	}
	var out struct {
		JobID                 string         `json:"job_id"`
		Type                  string         `json:"type"`
		Status                string         `json:"status"`
		TranscriptRef         string         `json:"transcript_ref"`
		StructuredResult      map[string]any `json:"structured_result"`
		StructuredResultValid bool           `json:"structured_result_valid"`
	}
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal delegate output: %v (output: %s)", err, res.Output)
	}
	if out.JobID == "" ||
		out.Type != string(jobstore.JobDelegate) ||
		out.Status != string(jobstore.StatusCompleted) ||
		out.TranscriptRef == "" {
		t.Fatalf("delegate output = %+v, want completed delegate job", out)
	}
	if !out.StructuredResultValid || out.StructuredResult["summary"] != "fixed" {
		t.Fatalf("structured result = %+v valid=%v, want summary=fixed", out.StructuredResult, out.StructuredResultValid)
	}
}

func TestDelegateToolForegroundSchemaResultMissingProjectsReason(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithoutStructured("done without structured result")
			},
		},
	})
	s := newDelegateTestSession(t, c)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:   "delegate",
		Name: "delegate",
		Arguments: json.RawMessage(`{
			"task":"return a structured result",
			"max_wait_ms":5000,
			"result_schema":{
				"type":"object",
				"properties":{"summary":{"type":"string"}},
				"required":["summary"]
			}
		}`),
	})
	if res.IsError {
		t.Fatalf("delegate returned error: %s", res.Output)
	}
	assertStructuredResultInvalidReason(t, string(toolResultJSON(res)), "schema_result_missing")
}

func TestDelegateToolForegroundNoSchemaNoStructuredOmitsStructuredFields(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithoutStructured("plain delegate result")
			},
		},
	})
	s := newDelegateTestSession(t, c)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "delegate",
		Name:      "delegate",
		Arguments: json.RawMessage(`{"task":"return prose only","max_wait_ms":5000}`),
	})
	if res.IsError {
		t.Fatalf("delegate returned error: %s", res.Output)
	}
	assertStructuredResultFieldsAbsent(t, res.Output)
}

func TestDelegateSendForegroundStartReturnsTerminalResult(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("first complete")
			},
		},
	}
	c.Register(adapter)
	s := newDelegateTestSession(t, c)

	first := s.createDelegate(context.Background(), delegateArgs{
		Task:           "finish first",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	adapter.mu.Lock()
	adapter.steps = append(adapter.steps, func(req llm.Request) llm.Response {
		return communicateWithDefaultOutput("second complete")
	})
	adapter.mu.Unlock()

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "send",
		Name:      "delegate_send",
		Arguments: json.RawMessage(fmt.Sprintf(`{"to":%q,"message":"run again","on_idle":"start","max_wait_ms":5000}`, first.DelegateID)),
	})
	if res.IsError {
		t.Fatalf("delegate_send returned error: %s", res.Output)
	}
	var out struct {
		DelegateID          string `json:"delegate_id"`
		StartedJobID        string `json:"started_job_id"`
		CurrentJobID        string `json:"current_job_id"`
		LatestJobID         string `json:"latest_job_id"`
		Type                string `json:"type"`
		Status              string `json:"status"`
		RunningInBackground bool   `json:"running_in_background"`
		TimedOut            bool   `json:"timed_out"`
		Action              string `json:"action"`
		TranscriptRef       string `json:"transcript_ref"`
		Output              string `json:"output"`
		Truncated           bool   `json:"truncated"`
	}
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal delegate_send output: %v (output: %s)", err, res.Output)
	}
	if out.DelegateID != first.DelegateID ||
		out.StartedJobID == "" ||
		out.StartedJobID == first.JobID ||
		out.CurrentJobID != out.StartedJobID ||
		out.LatestJobID != out.StartedJobID ||
		out.Type != string(jobstore.JobDelegate) ||
		out.Status != string(jobstore.StatusCompleted) ||
		out.RunningInBackground ||
		out.TimedOut ||
		out.Action != "started" ||
		out.TranscriptRef != first.TranscriptRef ||
		!strings.Contains(out.Output, "second complete") ||
		out.Truncated {
		t.Fatalf("delegate_send output = %+v, want foreground terminal started result", out)
	}
	rec := loadShellRecord(t, s.jobManager, out.CurrentJobID)
	if rec.Status != jobstore.StatusCompleted || rec.TranscriptRef != first.TranscriptRef {
		t.Fatalf("started record = %+v, want completed with same transcript ref", rec)
	}
}

func TestDelegateSendRejectsJobIDTargetWithGuidance(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("first complete")
			},
		},
	})
	s := newDelegateTestSession(t, c)

	first := s.createDelegate(context.Background(), delegateArgs{
		Task:           "finish first",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "send",
		Name:      "delegate_send",
		Arguments: json.RawMessage(fmt.Sprintf(`{"to":%q,"message":"run again"}`, first.JobID)),
	})
	if !res.IsError {
		t.Fatalf("delegate_send succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "job_id is a job/turn handle") || !strings.Contains(res.Output, first.DelegateID) {
		t.Fatalf("delegate_send error = %q, want job_id guidance with delegate_id %q", res.Output, first.DelegateID)
	}
}

func TestDelegateSendIdleDefaultFailsAndOnIdleStartResumes(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("first complete")
			},
		},
	}
	c.Register(adapter)
	s := newDelegateTestSession(t, c)

	first := s.createDelegate(context.Background(), delegateArgs{
		Task:           "finish first",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	adapter.mu.Lock()
	adapter.steps = append(adapter.steps, func(req llm.Request) llm.Response {
		return communicateWithDefaultOutput("second complete")
	})
	adapter.mu.Unlock()

	idle := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "send-idle",
		Name:      "delegate_send",
		Arguments: json.RawMessage(fmt.Sprintf(`{"to":%q,"message":"run again"}`, first.DelegateID)),
	})
	if !idle.IsError {
		t.Fatalf("delegate_send idle default succeeded, want error: %s", idle.Output)
	}
	if !strings.Contains(idle.Output, "target_idle") {
		t.Fatalf("delegate_send idle error = %q, want target_idle", idle.Output)
	}

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "send-start",
		Name:      "delegate_send",
		Arguments: json.RawMessage(fmt.Sprintf(`{"to":%q,"message":"run again","on_idle":"start","max_wait_ms":5000}`, first.DelegateID)),
	})
	if res.IsError {
		t.Fatalf("delegate_send returned error: %s", res.Output)
	}
	var out struct {
		DelegateID   string `json:"delegate_id"`
		StartedJobID string `json:"started_job_id"`
		CurrentJobID string `json:"current_job_id"`
		LatestJobID  string `json:"latest_job_id"`
		Action       string `json:"action"`
		Output       string `json:"output"`
	}
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal delegate_send output: %v (output: %s)", err, res.Output)
	}
	if out.DelegateID != first.DelegateID ||
		out.StartedJobID == "" ||
		out.StartedJobID == first.JobID ||
		out.CurrentJobID != out.StartedJobID ||
		out.LatestJobID != out.StartedJobID ||
		out.Action != "started" ||
		!strings.Contains(out.Output, "second") {
		t.Fatalf("delegate_send output = %+v, want started resumed delegate result", out)
	}
}

func TestJobSendMessageRestoreRuntimeLostStructuredInvalidResult(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithStructured("old structured output", map[string]any{"summary": "old"})
			},
			func(req llm.Request) llm.Response {
				return communicateWithoutStructured("resumed prose without structured output")
			},
		},
	}
	c := llm.NewClient()
	c.Register(adapter)
	s := newDelegateTestSession(t, c)
	resultSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary": map[string]any{"type": "string"},
		},
		"required": []string{"summary"},
	}

	first := s.createDelegate(context.Background(), delegateArgs{
		Task:           "runtime-lost structured delegate",
		Background:     false,
		BlockTimeoutMS: 5000,
		ResultSchema:   resultSchema,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	rec := loadShellRecord(t, s.jobManager, first.JobID)
	setStoredDelegateTerminalStatus(t, s, rec, jobstore.StatusStopped, "runtime_lost")
	parentMeta := s.Meta()
	stateDir := s.stateDir
	s.Close()

	restored, err := RestoreSessionFromMetaWithConfig(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), parentMeta, RestoreSessionConfig{StateDir: stateDir})
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	t.Cleanup(func() { restored.Close() })

	res := restored.reg.ExecuteCall(context.Background(), restored.env, llm.ToolCallData{
		ID:        "send",
		Name:      "delegate_send",
		Arguments: json.RawMessage(fmt.Sprintf(`{"to":%q,"message":"resume without structured output","on_idle":"start","max_wait_ms":5000}`, first.DelegateID)),
	})
	if res.IsError {
		t.Fatalf("delegate_send returned error: %s", res.Output)
	}
	var out struct {
		DelegateID             string         `json:"delegate_id"`
		StartedJobID           string         `json:"started_job_id"`
		CurrentJobID           string         `json:"current_job_id"`
		LatestJobID            string         `json:"latest_job_id"`
		Type                   string         `json:"type"`
		Status                 string         `json:"status"`
		Action                 string         `json:"action"`
		TranscriptRef          string         `json:"transcript_ref"`
		Output                 string         `json:"output"`
		StructuredResult       map[string]any `json:"structured_result"`
		StructuredResultValid  *bool          `json:"structured_result_valid"`
		StructuredResultReason string         `json:"structured_result_reason"`
		RunningInBackground    bool           `json:"running_in_background"`
	}
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal delegate_send output: %v (output: %s)", err, res.Output)
	}
	if out.DelegateID != first.DelegateID ||
		out.StartedJobID == "" ||
		out.StartedJobID == first.JobID ||
		out.CurrentJobID != out.StartedJobID ||
		out.LatestJobID != out.StartedJobID ||
		out.Type != string(jobstore.JobDelegate) ||
		out.Action != "started" ||
		out.TranscriptRef != first.TranscriptRef ||
		out.RunningInBackground {
		t.Fatalf("delegate_send output = %+v, want foreground restored start", out)
	}
	if out.Status != string(jobstore.StatusFailed) || !strings.Contains(out.Output, "model returned bare text") {
		t.Fatalf("delegate_send output = %+v, want failed missing-structured started turn", out)
	}
	if out.StructuredResult != nil {
		t.Fatalf("structured_result = %+v, want omitted", out.StructuredResult)
	}
	if out.StructuredResultValid == nil || *out.StructuredResultValid {
		t.Fatalf("structured_result_valid = %v, want false", out.StructuredResultValid)
	}
	if out.StructuredResultReason != "schema_result_missing" {
		t.Fatalf("structured_result_reason = %q, want schema_result_missing", out.StructuredResultReason)
	}
	newRec := loadShellRecord(t, restored.jobManager, out.CurrentJobID)
	if newRec.DelegateRestore == nil || newRec.DelegateRestore.ResultSchema == nil || newRec.TranscriptRef != first.TranscriptRef {
		t.Fatalf("resumed record = %+v, want inherited schema and transcript ref", newRec)
	}
	if newRec.StructuredResult != nil ||
		newRec.StructuredResultValid == nil ||
		*newRec.StructuredResultValid ||
		newRec.StructuredResultReason != "schema_result_missing" {
		t.Fatalf("durable structured result = value:%+v valid:%v reason:%q, want missing schema result", newRec.StructuredResult, newRec.StructuredResultValid, newRec.StructuredResultReason)
	}
}

func TestMarshalDelegateResultsBoundLargeOutput(t *testing.T) {
	t.Parallel()
	largeOutput := strings.Repeat("prefix-", 200) + "delegate-tail"
	out, err := marshalDelegateResult(delegateResult{
		JobID:               "job_delegate",
		Type:                string(jobstore.JobDelegate),
		Status:              jobstore.StatusCompleted,
		RunningInBackground: false,
		TranscriptRef:       "local:child",
		Output:              largeOutput,
	}, jobToolResultMinJSONChars)
	if err != nil {
		t.Fatalf("marshalDelegateResult: %v", err)
	}
	if !json.Valid(handlerJSON(t, out)) {
		t.Fatalf("delegate result returned invalid JSON: %s", out)
	}
	if len([]rune(out)) > jobToolResultMinJSONChars {
		t.Fatalf("delegate result length = %d, want <= %d", len([]rune(out)), jobToolResultMinJSONChars)
	}
	var parsed struct {
		Output    string `json:"output"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal(handlerJSON(t, out), &parsed); err != nil {
		t.Fatalf("unmarshal delegate result: %v", err)
	}
	if !parsed.Truncated {
		t.Fatalf("truncated = false, want true in %s", out)
	}
	if !strings.Contains(parsed.Output, "delegate-tail") {
		t.Fatalf("output tail not retained: %q", parsed.Output)
	}
}

func TestDelegateSendNegativeBlockTimeoutDoesNotStart(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("first complete")
			},
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("must not resume")
			},
		},
	})
	s := newDelegateTestSession(t, c)

	first := s.createDelegate(context.Background(), delegateArgs{
		Task:           "finish first",
		Background:     false,
		BlockTimeoutMS: 5000,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	if first.Status != jobstore.StatusCompleted {
		t.Fatalf("first result = %+v, want completed", first)
	}
	before := s.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	if len(before) != 1 {
		t.Fatalf("delegate jobs before send = %+v, want original terminal job only", before)
	}

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "send",
		Name:      "delegate_send",
		Arguments: json.RawMessage(fmt.Sprintf(`{"to":%q,"message":"run again","max_wait_ms":-1}`, first.DelegateID)),
	})
	if !res.IsError {
		t.Fatalf("delegate_send succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "max_wait_ms must be non-negative") {
		t.Fatalf("delegate_send error = %q, want non-negative max_wait_ms error", res.Output)
	}
	after := s.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	if len(after) != len(before) {
		t.Fatalf("delegate jobs grew from %d to %d; jobs = %+v", len(before), len(after), after)
	}
}

func TestDelegateSendToShellJobIDRejectsJobHandle(t *testing.T) {
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
		ID:        "send",
		Name:      "delegate_send",
		Arguments: json.RawMessage(fmt.Sprintf(`{"to":%q,"message":"hi"}`, shellOut.JobID)),
	})
	if !res.IsError {
		t.Fatalf("delegate_send succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "job_id is a job/turn handle") {
		t.Fatalf("delegate_send error = %q, want job_id handle rejection", res.Output)
	}
}

func TestDelegateSendCallerTargetRejectsPublicAlias(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.cfg.spawn.parentSteer = s.SteerWithProvenance

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "send",
		Name:      "delegate_send",
		Arguments: json.RawMessage(`{"to":"caller","message":"runtime advisory"}`),
	})
	if !res.IsError {
		t.Fatalf("delegate_send(to=caller) succeeded, want public alias rejection: %s", res.Output)
	}
	if !strings.Contains(res.Output, "communicate(end_turn=true)") {
		t.Fatalf("delegate_send error = %q, want communicate guidance", res.Output)
	}
	queue := s.SteeringQueueSnapshot()
	if len(queue) != 0 {
		t.Fatalf("steering queue = %+v, want no public caller delivery", queue)
	}
}

func TestJobToolsDefinitions(t *testing.T) {
	t.Parallel()
	required := func(t *testing.T, def llm.ToolDefinition, name string, want []string) {
		t.Helper()
		if def.Name != name {
			t.Fatalf("definition name = %q, want %q", def.Name, name)
		}
		var required []string
		if raw := def.Parameters["required"]; raw != nil {
			required = requiredParams(t, name, raw)
		} else if len(want) > 0 {
			t.Fatalf("%s required = <nil>, want %v", name, want)
		}
		for _, param := range want {
			if !containsString(required, param) {
				t.Fatalf("%s required = %v, want %q", name, required, param)
			}
		}
		props, ok := def.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s properties = %T, want map[string]any", name, def.Parameters["properties"])
		}
		for _, param := range want {
			if _, ok := props[param]; !ok {
				t.Fatalf("%s missing required property %q", name, param)
			}
		}
	}

	required(t, tooldefs.DefJobStatus(), "job_status", []string{"job_id"})
	required(t, tooldefs.DefJobStop(), "job_stop", []string{"job_id"})
	required(t, tooldefs.DefJobList(), "job_list", nil)
	required(t, tooldefs.DefReadTranscript(), "read_transcript", nil)
	required(t, tooldefs.DefDelegate([]string{"reviewer"}), "delegate", []string{"task"})
	required(t, tooldefs.DefJobWatch(WatchEventKindNames), "job_watch", []string{"operation"})
	required(t, tooldefs.DefDelegateSend(), "delegate_send", []string{"to", "message"})

	readProps := tooldefs.DefReadTranscript().Parameters["properties"].(map[string]any)
	for _, param := range []string{"transcript_ref", "format", "range", "expand_turn"} {
		if _, ok := readProps[param]; !ok {
			t.Fatalf("read_transcript missing param %q", param)
		}
	}
	listProps := tooldefs.DefJobList().Parameters["properties"].(map[string]any)
	for _, param := range []string{"status", "type", "include_nested", "limit"} {
		if _, ok := listProps[param]; !ok {
			t.Fatalf("job_list missing param %q", param)
		}
	}
	if _, ok := listProps["cursor"]; ok {
		t.Fatalf("job_list schema unexpectedly contains removed cursor param")
	}
	stopProps := tooldefs.DefJobStop().Parameters["properties"].(map[string]any)
	for _, param := range []string{"job_id", "max_wait_ms", "include_children"} {
		if _, ok := stopProps[param]; !ok {
			t.Fatalf("job_stop missing param %q", param)
		}
	}
	if _, ok := stopProps["signal"]; ok {
		t.Fatalf("job_stop exposes unsupported signal parameter")
	}
	delegateProps := tooldefs.DefDelegate([]string{"reviewer"}).Parameters["properties"].(map[string]any)
	for _, param := range []string{"task", "agent_type", "model", "reasoning_effort", "max_wait_ms", "result_schema"} {
		if _, ok := delegateProps[param]; !ok {
			t.Fatalf("delegate missing param %q", param)
		}
	}
	sendProps := tooldefs.DefDelegateSend().Parameters["properties"].(map[string]any)
	for _, param := range []string{"to", "message", "on_idle", "max_wait_ms"} {
		if _, ok := sendProps[param]; !ok {
			t.Fatalf("delegate_send missing param %q", param)
		}
	}
	watchProps := tooldefs.DefJobWatch(WatchEventKindNames).Parameters["properties"].(map[string]any)
	for _, param := range []string{"operation", "watch_id", "source", "output_match", "progress_interval_ms", "events", "event_filter", "every"} {
		if _, ok := watchProps[param]; !ok {
			t.Fatalf("job_watch missing param %q", param)
		}
	}
	if _, ok := watchProps["target"]; ok {
		t.Fatalf("job_watch exposes removed target param")
	}
	if _, ok := watchProps["send"]; ok {
		t.Fatalf("job_watch exposes removed send param")
	}
	if _, ok := watchProps["clear"]; ok {
		t.Fatalf("job_watch exposes removed clear param")
	}
}

func TestJobToolOutputLimitsHaveJSONMinimum(t *testing.T) {
	t.Parallel()
	s := newShellToolTestSession(t, SessionConfig{
		ToolOutputLimits: map[string]schema.ToolOutputLimit{
			"job_status":    {MaxChars: 1, Strategy: schema.TruncHeadTail},
			"job_list":      {MaxChars: 1, Strategy: schema.TruncHeadTail},
			"job_stop":      {MaxChars: 1, Strategy: schema.TruncHeadTail},
			"delegate":      {MaxChars: 1, Strategy: schema.TruncHeadTail},
			"job_watch":     {MaxChars: 1, Strategy: schema.TruncHeadTail},
			"delegate_send": {MaxChars: 1, Strategy: schema.TruncHeadTail},
		},
	})

	for _, name := range []string{"job_status", "job_list", "job_stop", "delegate", "job_watch", "delegate_send"} {
		rt := s.reg.Get(name)
		if rt == nil {
			t.Fatalf("%s not registered", name)
		}
		if rt.Limit.MaxChars != jobToolResultMinJSONChars {
			t.Fatalf("%s MaxChars = %d, want JSON minimum %d", name, rt.Limit.MaxChars, jobToolResultMinJSONChars)
		}
	}
}

func TestJobStopSchemaRejectsUnsupportedSignal(t *testing.T) {
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

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "stop",
		Name:      "job_stop",
		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"signal":"KILL"}`, shellOut.JobID)),
	})
	if !res.IsError {
		t.Fatalf("job_stop accepted unsupported signal argument: %s", res.Output)
	}
	rec := loadShellRecord(t, s.jobManager, shellOut.JobID)
	if rec.Status != jobstore.StatusRunning {
		t.Fatalf("job_stop with unsupported signal changed job state to %+v, want still running", rec)
	}
}

func TestJobStopAcceptsIncludeChildrenThroughRegistry(t *testing.T) {
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

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "stop",
		Name:      "job_stop",
		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"include_children":true}`, shellOut.JobID)),
	})
	if res.IsError {
		t.Fatalf("job_stop rejected include_children through registry: %s", res.Output)
	}
}

// TestDelegateMaxWaitMSDecodeTable pins spec §2 delegate decode: negative
// max_wait_ms is rejected; the old background+block_timeout_ms combo rejection
// is gone (spec §3 — combo is inexpressible).
func TestDelegateMaxWaitMSDecodeTable(t *testing.T) {
	t.Parallel()
	s := newDelegateTestSession(t, llm.NewClient())

	// Negative max_wait_ms → invalid_request.
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "delegate",
		Name:      "delegate",
		Arguments: json.RawMessage(`{"task":"x","max_wait_ms":-5}`),
	})
	if !res.IsError {
		t.Fatalf("delegate with max_wait_ms=-5 should return error, got success: %s", res.Output)
	}
	if !strings.Contains(res.Output, "max_wait_ms must be non-negative") {
		t.Fatalf("delegate error = %q, want max_wait_ms must be non-negative", res.Output)
	}
}

// TestDelegateSendMaxWaitMSDecodeTable pins spec §2 delegate_send decode:
// negative max_wait_ms is rejected. The old combo rejection is gone (spec §3).

// TestDelegateSendMaxWaitMSDecodeTable pins spec §2 delegate_send decode:
// negative max_wait_ms is rejected. The old combo rejection is gone (spec §3).
func TestDelegateSendMaxWaitMSDecodeTable(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("done")
			},
		},
	}
	c := llm.NewClient()
	c.Register(adapter)
	s := newDelegateTestSession(t, c)

	first := s.createDelegate(context.Background(), delegateArgs{
		Task:       "finish first",
		Background: false,
	})
	if first.Err != nil {
		t.Fatalf("createDelegate returned error: %v", first.Err)
	}
	waitForShellDone(t, s.jobManager, first.JobID)

	// Negative max_wait_ms → invalid_request.
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "send",
		Name:      "delegate_send",
		Arguments: json.RawMessage(fmt.Sprintf(`{"to":%q,"message":"m","max_wait_ms":-5}`, first.DelegateID)),
	})
	if !res.IsError {
		t.Fatalf("delegate_send with max_wait_ms=-5 should return error, got success: %s", res.Output)
	}
	if !strings.Contains(res.Output, "max_wait_ms must be non-negative") {
		t.Fatalf("delegate_send error = %q, want max_wait_ms must be non-negative", res.Output)
	}
}

// TestDelegateAndDelegateSendAcceptZeroMaxWaitMS pins spec §2: max_wait_ms=0 is
// accepted (strict-provider safe — zero reads as unset on all five tools).

// TestDelegateAndDelegateSendAcceptZeroMaxWaitMS pins spec §2: max_wait_ms=0 is
// accepted (strict-provider safe — zero reads as unset on all five tools).
func TestDelegateAndDelegateSendAcceptZeroMaxWaitMS(t *testing.T) {
	t.Parallel()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithDefaultOutput("done")
			},
		},
	}
	c := llm.NewClient()
	c.Register(adapter)
	s := newDelegateTestSession(t, c)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "d0",
		Name:      "delegate",
		Arguments: json.RawMessage(`{"task":"zero is unset","max_wait_ms":0}`),
	})
	if res.IsError {
		t.Fatalf("delegate with max_wait_ms=0 (unset) failed unexpectedly: %s", res.Output)
	}
	var spawned struct {
		DelegateID          string `json:"delegate_id"`
		JobID               string `json:"job_id"`
		RunningInBackground bool   `json:"running_in_background"`
	}
	if err := json.Unmarshal(toolResultJSON(res), &spawned); err != nil || spawned.JobID == "" || spawned.DelegateID == "" {
		t.Fatalf("delegate result missing job_id: %s", res.Output)
	}
	if !spawned.RunningInBackground {
		t.Fatalf("delegate with max_wait_ms=0 should be running_in_background, got: %s", res.Output)
	}
	waitForShellDone(t, s.jobManager, spawned.JobID)
	adapter.mu.Lock()
	adapter.steps = append(adapter.steps, func(req llm.Request) llm.Response {
		return communicateWithDefaultOutput("done again")
	})
	adapter.mu.Unlock()

	res2 := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "send0",
		Name:      "delegate_send",
		Arguments: json.RawMessage(fmt.Sprintf(`{"to":%q,"message":"m","max_wait_ms":0,"on_idle":"start"}`, spawned.DelegateID)),
	})
	if res2.IsError {
		t.Fatalf("delegate_send with max_wait_ms=0 (unset) failed unexpectedly: %s", res2.Output)
	}
}

// TestMaxWaitMSDecoders covers spec §2's decode table for delegate,
// delegate_send, job_read_output, and job_stop: negative max_wait_ms must
// return invalid_request; 0/absent must succeed with unset behavior.

// TestMaxWaitMSDecoders covers spec §2's decode table for delegate,
// delegate_send, job_read_output, and job_stop: negative max_wait_ms must
// return invalid_request; 0/absent must succeed with unset behavior.
func TestMaxWaitMSDecoders(t *testing.T) {
	t.Parallel()
	const wantNegErr = "invalid_request: max_wait_ms must be non-negative"

	t.Run("delegate_negative", func(t *testing.T) {
		s := newDelegateTestSession(t, llm.NewClient())
		res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
			ID:        "d",
			Name:      "delegate",
			Arguments: json.RawMessage(`{"task":"x","max_wait_ms":-1}`),
		})
		if !res.IsError {
			t.Fatalf("delegate with max_wait_ms=-1: want error, got success: %s", res.Output)
		}
		if !strings.Contains(res.Output, wantNegErr) {
			t.Fatalf("delegate error = %q, want %q", res.Output, wantNegErr)
		}
	})

	t.Run("delegate_zero_is_unset", func(t *testing.T) {
		adapter := &fakeAdapter{
			name: "openai",
			steps: []func(req llm.Request) llm.Response{
				func(req llm.Request) llm.Response {
					return communicateWithDefaultOutput("done")
				},
			},
		}
		c := llm.NewClient()
		c.Register(adapter)
		s := newDelegateTestSession(t, c)
		res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
			ID:        "d",
			Name:      "delegate",
			Arguments: json.RawMessage(`{"task":"x","max_wait_ms":0}`),
		})
		if res.IsError {
			t.Fatalf("delegate with max_wait_ms=0: want no-wait success, got error: %s", res.Output)
		}
		// With unset (0), should return immediately with running_in_background:true.
		var out struct {
			RunningInBackground bool   `json:"running_in_background"`
			JobID               string `json:"job_id"`
		}
		if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !out.RunningInBackground || out.JobID == "" {
			t.Fatalf("delegate with max_wait_ms=0 = %+v, want running_in_background with job_id", out)
		}
		waitForShellDone(t, s.jobManager, out.JobID)
	})

	t.Run("delegate_send_negative", func(t *testing.T) {
		adapter := &fakeAdapter{
			name: "openai",
			steps: []func(req llm.Request) llm.Response{
				func(req llm.Request) llm.Response {
					return communicateWithDefaultOutput("done")
				},
			},
		}
		c := llm.NewClient()
		c.Register(adapter)
		s := newDelegateTestSession(t, c)
		first := s.createDelegate(context.Background(), delegateArgs{Task: "finish", Background: false})
		if first.Err != nil {
			t.Fatalf("createDelegate: %v", first.Err)
		}
		waitForShellDone(t, s.jobManager, first.JobID)
		res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
			ID:        "send",
			Name:      "delegate_send",
			Arguments: json.RawMessage(fmt.Sprintf(`{"to":%q,"message":"m","max_wait_ms":-1}`, first.DelegateID)),
		})
		if !res.IsError {
			t.Fatalf("delegate_send with max_wait_ms=-1: want error, got success: %s", res.Output)
		}
		if !strings.Contains(res.Output, wantNegErr) {
			t.Fatalf("delegate_send error = %q, want %q", res.Output, wantNegErr)
		}
	})

	t.Run("job_read_output_negative", func(t *testing.T) {
		s := newTestSession(t)
		// Use internal API to create a running job (avoids shell decoder chicken-and-egg).
		rec := newManualRunningJob(t, s)
		res := executeJobReadOutputForTest(t, s, llm.ToolCallData{
			ID: "read",

			Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"max_wait_ms":-1}`, rec.JobID)),
		})
		if !res.IsError {
			t.Fatalf("job_read_output with max_wait_ms=-1: want error, got success: %s", res.Output)
		}
		if !strings.Contains(res.Output, wantNegErr) {
			t.Fatalf("job_read_output error = %q, want %q", res.Output, wantNegErr)
		}
	})

	t.Run("job_read_output_zero_is_snapshot", func(t *testing.T) {
		s := newTestSession(t)
		// Use internal API to create a running job.
		rec := newManualRunningJob(t, s)
		// max_wait_ms=0: snapshot now (should not block).
		started := time.Now()
		res := executeJobReadOutputForTest(t, s, llm.ToolCallData{
			ID: "read",

			Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"max_wait_ms":0}`, rec.JobID)),
		})
		if res.IsError {
			t.Fatalf("job_read_output with max_wait_ms=0: %s", res.Output)
		}
		if elapsed := time.Since(started); elapsed > 2*time.Second {
			t.Fatalf("job_read_output with max_wait_ms=0 blocked for %s, want immediate snapshot", elapsed)
		}
	})

	t.Run("job_stop_negative", func(t *testing.T) {
		s := newTestSession(t)
		rec := newManualRunningJob(t, s)
		res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
			ID:        "stop",
			Name:      "job_stop",
			Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"max_wait_ms":-1}`, rec.JobID)),
		})
		if !res.IsError {
			t.Fatalf("job_stop with max_wait_ms=-1: want error, got success: %s", res.Output)
		}
		if !strings.Contains(res.Output, wantNegErr) {
			t.Fatalf("job_stop error = %q, want %q", res.Output, wantNegErr)
		}
	})

	t.Run("job_stop_zero_is_return_now", func(t *testing.T) {
		s := newTestSession(t)
		rec := newManualRunningJob(t, s)
		// max_wait_ms=0: request stop and return without waiting.
		started := time.Now()
		res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
			ID:        "stop",
			Name:      "job_stop",
			Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"max_wait_ms":0}`, rec.JobID)),
		})
		if res.IsError {
			t.Fatalf("job_stop with max_wait_ms=0: %s", res.Output)
		}
		if elapsed := time.Since(started); elapsed > 2*time.Second {
			t.Fatalf("job_stop with max_wait_ms=0 blocked for %s, want return immediately", elapsed)
		}
	})
}

// TestGrantedReadBlockUnsupportedErrReword verifies that the production code
// path — a job_read_output call with max_wait_ms>0 against a watch-granted
// cross-session job — emits grantedReadBlockUnsupportedErr (spec §3). A
// mutation that replaces errors.New(grantedReadBlockUnsupportedErr) with a
// different message escapes a constant-equality check but is caught here
// because the actual error returned by jobReadOutputTool is asserted.
func TestGrantedReadBlockUnsupportedErrReword(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	// Inject a parentGrantedJobRead that grants one specific job_id. The job
	// does not exist in the local store, so nestedOrLocalJobManager fails and
	// the granted path is reached.
	const grantedID = "job_fakeForCrossSessionRead"
	s.cfg.spawn.parentGrantedJobRead = func(_ string, jobID string) (*grantedJobRead, bool) {
		if jobID != grantedID {
			return nil, false
		}
		return &grantedJobRead{record: &jobstore.JobRecord{JobID: jobID}}, true
	}
	_, err := jobReadOutputTool(context.Background(), s, map[string]any{
		"job_id":      grantedID,
		"max_wait_ms": float64(1000),
	}, jobToolResultDefaultMaxChar)
	if err == nil {
		t.Fatal("jobReadOutputTool with max_wait_ms on cross-session read succeeded, want error")
	}
	if !strings.Contains(err.Error(), grantedReadBlockUnsupportedErr) {
		t.Fatalf("error = %q, want it to contain grantedReadBlockUnsupportedErr", err.Error())
	}
}

// TestJobReadOutputHeadBytesReadsFromStart verifies that head_lines reads from
// the beginning of retained output — the symmetric counterpart to tail_lines.
// A job whose head output was pushed out of the default tail window is only
// reachable by grep or by head_lines; this test closes that gap.

// TestDelegateToolParsesDelegationAllowance proves the grant knob is reachable
// from the model: delegation_allowance flows through the registered delegate
// tool's JSON boundary into the grant rule. A grant >= the caller's own
// allowance is rejected through the tool; a negative value is rejected as
// non-negative.
func TestDelegateToolParsesDelegationAllowance(t *testing.T) {
	t.Parallel()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai"})
	s := newDelegateTestSession(t, c)
	s.mu.Lock()
	s.delegationAllowance = 1 // default root with MaxSubagentDepth=1
	s.mu.Unlock()

	// Grant equal to own allowance (1) is rejected with the grant-rule message,
	// proving the parsed value reached createDelegate.
	overGrant := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "d1",
		Name:      "delegate",
		Arguments: json.RawMessage(`{"task":"recurse","delegation_allowance":1}`),
	})
	if !overGrant.IsError {
		t.Fatalf("delegate with delegation_allowance=1 and own allowance 1 succeeded, want error; output: %s", overGrant.Output)
	}
	if !strings.Contains(overGrant.Output, "must be less than your own allowance (1)") {
		t.Fatalf("error = %q, want grant-rule rejection naming allowance 1", overGrant.Output)
	}

	// Negative grant is rejected at the boundary.
	negGrant := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "d2",
		Name:      "delegate",
		Arguments: json.RawMessage(`{"task":"recurse","delegation_allowance":-1}`),
	})
	if !negGrant.IsError {
		t.Fatalf("delegate with delegation_allowance=-1 succeeded, want error; output: %s", negGrant.Output)
	}
	if !strings.Contains(negGrant.Output, "invalid_request") || !strings.Contains(negGrant.Output, "non-negative") {
		t.Fatalf("error = %q, want invalid_request non-negative", negGrant.Output)
	}
}

// TestDelegateToolParsesWatchParent proves that watch_parent=true in the
// delegate tool's JSON arguments reaches the child session's spawn config as
// parentWatchGranted=true. The production path is exercised through
// s.reg.ExecuteCall so the shellBoolArg JSON-boundary parse and the
// ctxWatchParent context plumbing are both included. A mutation that drops the
// WatchParent field assignment in delegateTool is caught because
// child.cfg.spawn.parentWatchGranted would be false.
func TestDelegateToolParsesWatchParent(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	proceed := make(chan struct{})
	var startedOnce sync.Once
	var proceedOnce sync.Once
	// Register cleanup before newDelegateTestSession so it runs first (LIFO),
	// unblocking the child adapter before Close() is called.
	t.Cleanup(func() { proceedOnce.Do(func() { close(proceed) }) })

	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(_ llm.Request) llm.Response {
				startedOnce.Do(func() { close(started) })
				<-proceed
				return communicateWithDefaultOutput("done")
			},
		},
	})
	s := newDelegateTestSession(t, c)

	// Call through the registered tool; background delegate returns immediately.
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "d1",
		Name:      "delegate",
		Arguments: json.RawMessage(`{"task":"observe","watch_parent":true}`),
	})
	if res.IsError {
		t.Fatalf("delegate returned error: %s", res.Output)
	}

	// Wait for the child session to reach its first LLM call; until then the
	// child goroutine is alive and visible in liveDescendantSessions.
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("child delegate session did not start within 2s")
	}

	children := s.liveDescendantSessions()
	if len(children) == 0 {
		t.Fatal("no live child sessions while adapter was blocking")
	}
	child := children[0]
	child.mu.Lock()
	granted := child.cfg.spawn.parentWatchGranted
	child.mu.Unlock()
	proceedOnce.Do(func() { close(proceed) }) // unblock child before asserting

	if !granted {
		t.Fatal("parentWatchGranted = false on child spawned with watch_parent=true; shellBoolArg parse or context plumbing is broken")
	}
}

func TestLiveSteerWaitIgnoredReason(t *testing.T) {
	t.Parallel()
	const want = "live steer returns on delivery; max_wait_ms applies only to started jobs"
	cases := []struct {
		name    string
		ms      int
		status  jobstore.Status
		action  string
		wantStr string
	}{
		{"live steer with wait flagged", 5000, jobstore.StatusRunning, "steered", want},
		{"no wait requested not flagged", 0, jobstore.StatusRunning, "steered", ""},
		{"started running not flagged", 5000, jobstore.StatusRunning, "started", ""},
		{"started terminal not flagged", 5000, jobstore.StatusCompleted, "started", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := liveSteerWaitIgnoredReason(tc.ms, tc.status, tc.action)
			if got != tc.wantStr {
				t.Fatalf("liveSteerWaitIgnoredReason(%d,%q,%q) = %q, want %q", tc.ms, tc.status, tc.action, got, tc.wantStr)
			}
		})
	}
}

func TestMarshalDelegateSendResultCarriesWaitIgnoredReason(t *testing.T) {
	t.Parallel()
	const reason = "live steer returns on delivery; max_wait_ms applies only to started jobs"
	res := sendMessageResult{
		Target:              "dlg_x",
		DelegateID:          "dlg_x",
		JobID:               "job_x",
		Type:                string(jobstore.JobDelegate),
		Status:              jobstore.StatusRunning,
		RunningInBackground: true,
		Action:              "steered",
		WaitIgnoredReason:   reason,
	}
	out, err := marshalDelegateSendResult(res, 1<<20)
	if err != nil {
		t.Fatalf("marshalDelegateSendResult: %v", err)
	}
	var got struct {
		WaitIgnoredReason string `json:"wait_ignored_reason"`
	}
	if err := json.Unmarshal(handlerJSON(t, out), &got); err != nil {
		t.Fatalf("unmarshal: %v (out=%s)", err, out)
	}
	if got.WaitIgnoredReason != reason {
		t.Fatalf("wait_ignored_reason = %q, want %q", got.WaitIgnoredReason, reason)
	}

	res.WaitIgnoredReason = ""
	out2, err := marshalDelegateSendResult(res, 1<<20)
	if err != nil {
		t.Fatalf("marshalDelegateSendResult (empty): %v", err)
	}
	if strings.Contains(string(handlerJSON(t, out2)), "wait_ignored_reason") {
		t.Fatalf("wait_ignored_reason must be omitted when empty, got %s", out2)
	}
}

func TestClassifyStopOutcome(t *testing.T) {
	t.Parallel()
	run := jobstore.StatusRunning
	cases := []struct {
		name string
		prev jobstore.Status
		rec  *jobstore.JobRecord
		want string
	}{
		{"already terminal", jobstore.StatusCompleted, &jobstore.JobRecord{Status: jobstore.StatusCancelled}, "already_terminal"},
		{"still running", run, &jobstore.JobRecord{Status: jobstore.StatusRunning}, "stop_requested"},
		{"nil record", run, nil, "stop_requested"},
		{"cancelled by request", run, &jobstore.JobRecord{Status: jobstore.StatusCancelled}, "cancelled_by_request"},
		{"completed during stop", run, &jobstore.JobRecord{Status: jobstore.StatusCompleted}, "completed_during_stop"},
		{"failed during stop", run, &jobstore.JobRecord{Status: jobstore.StatusFailed}, "completed_during_stop"},
	}
	for _, tc := range cases {
		if got := classifyStopOutcome(tc.prev, tc.rec); got != tc.want {
			t.Errorf("%s: classifyStopOutcome(%q,rec) = %q, want %q", tc.name, tc.prev, got, tc.want)
		}
	}
}

func TestJobStopReportsOutcomeAndPreviousStatus(t *testing.T) {
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

	out, err := jobStopTool(context.Background(), s, map[string]any{"job_id": shellOut.JobID}, 1<<20)
	if err != nil {
		t.Fatalf("jobStopTool: %v", err)
	}
	var stopOut struct {
		Status         string `json:"status"`
		PreviousStatus string `json:"previous_status"`
		Outcome        string `json:"outcome"`
	}
	if err := json.Unmarshal(handlerJSON(t, out), &stopOut); err != nil {
		t.Fatalf("unmarshal job_stop output: %v (out=%s)", err, out)
	}
	if stopOut.Outcome != "cancelled_by_request" {
		t.Fatalf("outcome = %q, want cancelled_by_request (out=%s)", stopOut.Outcome, out)
	}
	if stopOut.PreviousStatus != string(jobstore.StatusRunning) {
		t.Fatalf("previous_status = %q, want running", stopOut.PreviousStatus)
	}

	waitForShellDone(t, s.jobManager, shellOut.JobID)
}

func TestJobToolRequiredArgErrorsCarryInvalidRequestPrefix(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)

	if res := s.createDelegate(context.Background(), delegateArgs{Task: ""}); res.Err == nil ||
		!strings.HasPrefix(res.Err.Error(), "invalid_request:") {
		t.Fatalf("empty task error = %v, want invalid_request: prefix", res.Err)
	}
	if res := s.sendDelegateMessage(context.Background(), sendMessageArgs{Target: "", Message: "hi"}); res.Err == nil ||
		!strings.HasPrefix(res.Err.Error(), "invalid_request:") {
		t.Fatalf("empty target error = %v, want invalid_request: prefix", res.Err)
	}
	if res := s.sendDelegateMessage(context.Background(), sendMessageArgs{Target: "job_x", Message: ""}); res.Err == nil ||
		!strings.HasPrefix(res.Err.Error(), "invalid_request:") {
		t.Fatalf("empty message error = %v, want invalid_request: prefix", res.Err)
	}
	if _, err := jobStopTool(context.Background(), s, map[string]any{"job_id": ""}, 1<<20); err == nil ||
		!strings.HasPrefix(err.Error(), "invalid_request:") {
		t.Fatalf("stop empty job_id error = %v, want invalid_request: prefix", err)
	}
	if _, err := jobReadOutputTool(context.Background(), s, map[string]any{"job_id": ""}, 1<<20); err == nil ||
		!strings.HasPrefix(err.Error(), "invalid_request:") {
		t.Fatalf("read empty job_id error = %v, want invalid_request: prefix", err)
	}
}

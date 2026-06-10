package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
	tooldefs "primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

func TestJobToolsControlBackgroundShellJob(t *testing.T) {
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
	if err := json.Unmarshal([]byte(shellRes.Output), &shellOut); err != nil {
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
			OutputBytes         int64   `json:"output_bytes"`
			TerminalGeneration  *string `json:"terminal_generation"`
			TerminalNotifyState string  `json:"terminal_notification_state"`
		} `json:"jobs"`
		Count      int     `json:"count"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(listRes.Output), &listOut); err != nil {
		t.Fatalf("unmarshal job_list output: %v (output: %s)", err, listRes.Output)
	}
	if listOut.Count != len(listOut.Jobs) || listOut.NextCursor != nil {
		t.Fatalf("job_list output = %+v, want count=len(jobs) and null next_cursor", listOut)
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
		job.VisibleToSessionID == "" ||
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
		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"block":true,"block_timeout_ms":1000}`, shellOut.JobID)),
	})
	if stopRes.IsError {
		t.Fatalf("job_stop returned error: %s", stopRes.Output)
	}
	var stopOut struct {
		JobID  string  `json:"job_id"`
		Status string  `json:"status"`
		Reason *string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(stopRes.Output), &stopOut); err != nil {
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

func TestJobListToolIncludeNestedSurfacesForwardedRecords(t *testing.T) {
	parent := newTestSession(t)
	child := newTestSession(t)
	child.jobManager.forward = parent.jobManager.forwardEvent
	child.jobManager.parentJobID = "job_PARENTDELEGATE"

	parentRec, err := parent.jobManager.createShell(createShellOpts{Command: "sleep 1", Description: "parent"})
	if err != nil {
		t.Fatalf("create parent shell: %v", err)
	}
	childRec, err := child.jobManager.createShell(createShellOpts{Command: "sleep 1", Description: "nested"})
	if err != nil {
		t.Fatalf("create child shell: %v", err)
	}
	t.Cleanup(func() {
		finishRunningTestJob(t, parent.jobManager, parentRec.JobID)
		finishRunningTestJob(t, child.jobManager, childRec.JobID)
	})

	runList := func(args string) jobListToolOutput {
		t.Helper()
		res := parent.reg.ExecuteCall(context.Background(), parent.env, llm.ToolCallData{
			ID:        "list",
			Name:      "job_list",
			Arguments: json.RawMessage(args),
		})
		if res.IsError {
			t.Fatalf("job_list returned error: %s", res.Output)
		}
		var out jobListToolOutput
		if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
			t.Fatalf("unmarshal job_list output: %v (output: %s)", err, res.Output)
		}
		return out
	}

	defaultOut := runList(`{}`)
	if jobListToolOutputContains(defaultOut.Jobs, childRec.JobID) {
		t.Fatalf("default job_list jobs = %+v, want nested job hidden", defaultOut.Jobs)
	}
	if !jobListToolOutputContains(defaultOut.Jobs, parentRec.JobID) {
		t.Fatalf("default job_list jobs = %+v, want parent job %q", defaultOut.Jobs, parentRec.JobID)
	}

	nestedOut := runList(`{"include_nested":true}`)
	nestedJob := findJobListToolOutput(nestedOut.Jobs, childRec.JobID)
	if nestedJob == nil {
		t.Fatalf("include_nested job_list jobs = %+v, want nested job %q", nestedOut.Jobs, childRec.JobID)
	}
	if nestedJob.ParentJobID == nil || *nestedJob.ParentJobID != "job_PARENTDELEGATE" {
		t.Fatalf("nested job parent_job_id = %v, want job_PARENTDELEGATE", nestedJob.ParentJobID)
	}
}

func TestJobWatchToolConfiguresWatch(t *testing.T) {
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
	if err := json.Unmarshal([]byte(shellRes.Output), &shellOut); err != nil {
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
		Arguments: json.RawMessage(fmt.Sprintf(`{"target":%q,"output_match":"(?i)ready"}`, shellOut.JobID)),
	})
	if res.IsError {
		t.Fatalf("job_watch returned error: %s", res.Output)
	}
	if !strings.Contains(res.Output, `"watching":true`) {
		t.Fatalf("job_watch output = %s, want watching true", res.Output)
	}
	if s.jobManager.watchCount() != 1 {
		t.Fatalf("watch count = %d, want 1", s.jobManager.watchCount())
	}
}

func TestJobWatchNoConditionErrors(t *testing.T) {
	s := newTestSession(t)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch",
		Name:      "job_watch",
		Arguments: json.RawMessage(`{"target":"caller"}`),
	})
	if !res.IsError {
		t.Fatalf("job_watch succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "invalid_request: nothing to watch") {
		t.Fatalf("job_watch error = %q, want no-condition error", res.Output)
	}
}

func TestJobWatchToolMainAliasTargetFailsTargetNotFound(t *testing.T) {
	s := newTestSession(t)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch",
		Name:      "job_watch",
		Arguments: json.RawMessage(`{"target":"main"}`),
	})

	if !res.IsError {
		t.Fatalf("job_watch succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "target_not_found") {
		t.Fatalf("job_watch error = %q, want target_not_found", res.Output)
	}
	if s.jobManager.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", s.jobManager.watchCount())
	}
}

func TestJobWatchToolWatchedTargetWithoutContextFailsTargetNotFound(t *testing.T) {
	s := newTestSession(t)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch",
		Name:      "job_watch",
		Arguments: json.RawMessage(`{"target":"watched"}`),
	})

	if !res.IsError {
		t.Fatalf("job_watch succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "target_not_found") {
		t.Fatalf("job_watch error = %q, want target_not_found", res.Output)
	}
	if s.jobManager.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", s.jobManager.watchCount())
	}
}

func TestJobWatchToolSendToMainAliasFailsTargetNotFound(t *testing.T) {
	s := newTestSession(t)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch",
		Name:      "job_watch",
		Arguments: json.RawMessage(`{"target":"caller","events":["assistant.message"],"send":{"to":"main","message":"observe"}}`),
	})

	if !res.IsError {
		t.Fatalf("job_watch succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "target_not_found") {
		t.Fatalf("job_watch error = %q, want target_not_found", res.Output)
	}
	if s.jobManager.watchCount() != 0 {
		t.Fatalf("watch count = %d, want 0", s.jobManager.watchCount())
	}
}

func TestJobSendMessageToolMainAliasFailsTargetNotFoundWithoutSideEffects(t *testing.T) {
	s := newTestSession(t)
	called := false
	s.cfg.spawn.parentSteer = func(string) { called = true }

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "send",
		Name:      "job_send_message",
		Arguments: json.RawMessage(`{"target":"main","message":"hello"}`),
	})

	if !res.IsError {
		t.Fatalf("job_send_message succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "target_not_found") {
		t.Fatalf("job_send_message error = %q, want target_not_found", res.Output)
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

func TestJobWatchLargeEchoFieldsReturnBoundedSuccess(t *testing.T) {
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
	if err := json.Unmarshal([]byte(shellRes.Output), &shellOut); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, shellRes.Output)
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	largeMessage := strings.Repeat("watch-message-", jobToolResultDefaultMaxChar)
	args, err := json.Marshal(map[string]any{
		"target":       shellOut.JobID,
		"output_match": "ready",
		"send": map[string]any{
			"to":      "caller",
			"message": largeMessage,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch",
		Name:      "job_watch",
		Arguments: args,
	})
	if res.IsError {
		t.Fatalf("job_watch returned error after installing watch candidate: %s", res.Output)
	}
	if len([]rune(res.Output)) > jobToolResultDefaultMaxChar {
		t.Fatalf("job_watch output length = %d, want <= %d", len([]rune(res.Output)), jobToolResultDefaultMaxChar)
	}
	var out struct {
		Target   string `json:"target"`
		Watching bool   `json:"watching"`
		Send     *struct {
			To      string `json:"to"`
			Message string `json:"message"`
		} `json:"send"`
	}
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatalf("unmarshal job_watch output: %v (output: %s)", err, res.Output)
	}
	if out.Target != shellOut.JobID || !out.Watching || out.Send == nil || out.Send.To != "caller" {
		t.Fatalf("job_watch output = %+v, want watching response for %s", out, shellOut.JobID)
	}
	if out.Send.Message == largeMessage {
		t.Fatal("job_watch output echoed unbounded send.message")
	}
	if s.jobManager.watchCount() != 1 {
		t.Fatalf("watch count = %d, want 1", s.jobManager.watchCount())
	}
}

func TestJobWatchSendToRequired(t *testing.T) {
	for _, tc := range []struct {
		name string
		send string
	}{
		{name: "message without target", send: `{"message":"observe"}`},
		{name: "frame without target", send: `{"to":"   ","include_frame":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSession(t)
			res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
				ID:        "watch",
				Name:      "job_watch",
				Arguments: json.RawMessage(fmt.Sprintf(`{"target":"caller","events":["assistant.message"],"send":%s}`, tc.send)),
			})
			if !res.IsError {
				t.Fatalf("job_watch succeeded, want send.to error: %s", res.Output)
			}
			if !strings.Contains(res.Output, "invalid_request: send.to is required") {
				t.Fatalf("job_watch error = %q, want send.to validation", res.Output)
			}
			if s.jobManager.watchCount() != 0 {
				t.Fatalf("watch count = %d, want 0", s.jobManager.watchCount())
			}
		})
	}
}

func TestJobWatchEmptySendPlaceholderIsOmitted(t *testing.T) {
	s := newTestSession(t)
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "watch",
		Name:      "job_watch",
		Arguments: json.RawMessage(`{"target":"caller","events":["assistant.message"],"send":{"to":"","message":"","include_frame":false,"include_excerpt":false}}`),
	})
	if res.IsError {
		t.Fatalf("job_watch returned error for empty send placeholder: %s", res.Output)
	}
	var out jobWatchToolResult
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatalf("unmarshal job_watch output: %v (output: %s)", err, res.Output)
	}
	if out.Send != nil {
		t.Fatalf("send placeholder should be omitted from result, got %+v", out.Send)
	}
}

func TestJobWatchAdvertisedDefinitionUsesCanonicalEventKinds(t *testing.T) {
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

func TestJobStopDefaultReturnsRequestedCancellation(t *testing.T) {
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
	if err := json.Unmarshal([]byte(shellRes.Output), &shellOut); err != nil {
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
	if err := json.Unmarshal([]byte(stopRes.Output), &stopOut); err != nil {
		t.Fatalf("unmarshal job_stop output: %v (output: %s)", err, stopRes.Output)
	}
	if stopOut.JobID != shellOut.JobID || stopOut.Status != string(jobstore.StatusCancelled) || stopOut.Reason == nil || *stopOut.Reason != "stopped_by_parent" {
		t.Fatalf("job_stop = %+v, want immediate cancelled/stopped_by_parent", stopOut)
	}

	waitForShellDone(t, s.jobManager, shellOut.JobID)
}

func TestDelegateToolForegroundReturnsStructuredResult(t *testing.T) {
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
			"background":false,
			"block_timeout_ms":5000,
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
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
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
			"background":false,
			"block_timeout_ms":5000,
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
	assertStructuredResultInvalidReason(t, res.Output, "schema_result_missing")
}

func TestDelegateToolForegroundNoSchemaNoStructuredOmitsStructuredFields(t *testing.T) {
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
		Arguments: json.RawMessage(`{"task":"return prose only","background":false,"block_timeout_ms":5000}`),
	})
	if res.IsError {
		t.Fatalf("delegate returned error: %s", res.Output)
	}
	assertStructuredResultFieldsAbsent(t, res.Output)
}

func TestJobReadOutputReturnsBackgroundDelegateStructuredResult(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithStructured("background report", map[string]any{
					"summary": "persisted",
				})
			},
		},
	})
	s := newDelegateTestSession(t, c)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:   "delegate",
		Name: "delegate",
		Arguments: json.RawMessage(`{
			"task":"produce a structured result",
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
	var delegateOut struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(res.Output), &delegateOut); err != nil {
		t.Fatalf("unmarshal delegate output: %v (output: %s)", err, res.Output)
	}
	if delegateOut.JobID == "" || delegateOut.Status != string(jobstore.StatusRunning) {
		t.Fatalf("delegate output = %+v, want running background job", delegateOut)
	}
	waitForShellDone(t, s.jobManager, delegateOut.JobID)

	readRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "read",
		Name:      "job_read_output",
		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"max_chars":20000}`, delegateOut.JobID)),
	})
	if readRes.IsError {
		t.Fatalf("job_read_output returned error: %s", readRes.Output)
	}
	var readOut jobReadOutputTestResult
	if err := json.Unmarshal([]byte(readRes.Output), &readOut); err != nil {
		t.Fatalf("unmarshal job_read_output: %v (output: %s)", err, readRes.Output)
	}
	if readOut.Status != string(jobstore.StatusCompleted) ||
		!readOut.StructuredResultValid ||
		readOut.StructuredResult["summary"] != "persisted" {
		t.Fatalf("job_read_output = %+v, want persisted structured result", readOut)
	}
}

func TestJobReadOutputReturnsBackgroundDelegateSchemaResultMissingReason(t *testing.T) {
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithoutStructured("background missing structured result")
			},
		},
	})
	s := newDelegateTestSession(t, c)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:   "delegate",
		Name: "delegate",
		Arguments: json.RawMessage(`{
			"task":"produce a structured result",
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
	var delegateOut struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(res.Output), &delegateOut); err != nil {
		t.Fatalf("unmarshal delegate output: %v (output: %s)", err, res.Output)
	}
	if delegateOut.JobID == "" || delegateOut.Status != string(jobstore.StatusRunning) {
		t.Fatalf("delegate output = %+v, want running background job", delegateOut)
	}
	waitForShellDone(t, s.jobManager, delegateOut.JobID)

	readRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "read",
		Name:      "job_read_output",
		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"max_chars":20000}`, delegateOut.JobID)),
	})
	if readRes.IsError {
		t.Fatalf("job_read_output returned error: %s", readRes.Output)
	}
	assertStructuredResultInvalidReason(t, readRes.Output, "schema_result_missing")
}

func TestJobReadOutputBlockReturnsOnNewOutput(t *testing.T) {
	s := newTestSession(t)
	rec, err := s.jobManager.createShell(createShellOpts{Command: "manual running job"})
	if err != nil {
		t.Fatalf("create shell job: %v", err)
	}
	t.Cleanup(func() {
		_ = s.jobManager.finalize(rec.JobID, jobstore.StatusCancelled, "test_cleanup", nil)
		waitForShellDone(t, s.jobManager, rec.JobID)
	})

	go func() {
		time.Sleep(50 * time.Millisecond)
		s.jobManager.mu.Lock()
		run := s.jobManager.running[rec.JobID]
		s.jobManager.mu.Unlock()
		if run != nil {
			_, _ = run.output.Append([]byte("new output\n"))
		}
	}()

	started := time.Now()
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "read",
		Name:      "job_read_output",
		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"block":true,"block_timeout_ms":1000}`, rec.JobID)),
	})
	if res.IsError {
		t.Fatalf("job_read_output returned error: %s", res.Output)
	}
	if elapsed := time.Since(started); elapsed >= 900*time.Millisecond {
		t.Fatalf("job_read_output blocked for %s, want return on output before timeout", elapsed)
	}
	var out jobReadOutputTestResult
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatalf("unmarshal job_read_output: %v (output: %s)", err, res.Output)
	}
	if !strings.Contains(out.Content, "new output") || out.Status != string(jobstore.StatusRunning) {
		t.Fatalf("job_read_output = %+v, want running job with new output", out)
	}
}

func TestJobSendMessageForegroundResumeReturnsTerminalResult(t *testing.T) {
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
		Name:      "job_send_message",
		Arguments: json.RawMessage(fmt.Sprintf(`{"target":%q,"message":"run again","background":false,"block_timeout_ms":5000}`, first.JobID)),
	})
	if res.IsError {
		t.Fatalf("job_send_message returned error: %s", res.Output)
	}
	var out struct {
		Target              string `json:"target"`
		JobID               string `json:"job_id"`
		Type                string `json:"type"`
		Status              string `json:"status"`
		RunningInBackground bool   `json:"running_in_background"`
		TimedOut            bool   `json:"timed_out"`
		Action              string `json:"action"`
		ResumedFromJobID    string `json:"resumed_from_job_id"`
		TranscriptRef       string `json:"transcript_ref"`
		Output              string `json:"output"`
		Truncated           bool   `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatalf("unmarshal job_send_message output: %v (output: %s)", err, res.Output)
	}
	if out.Target != first.JobID ||
		out.JobID == "" ||
		out.JobID == first.JobID ||
		out.Type != string(jobstore.JobDelegate) ||
		out.Status != string(jobstore.StatusCompleted) ||
		out.RunningInBackground ||
		out.TimedOut ||
		out.Action != "resumed" ||
		out.ResumedFromJobID != first.JobID ||
		out.TranscriptRef != first.TranscriptRef ||
		!strings.Contains(out.Output, "second complete") ||
		out.Truncated {
		t.Fatalf("job_send_message output = %+v, want foreground terminal resumed result", out)
	}
	rec := loadShellRecord(t, s.jobManager, out.JobID)
	if rec.Status != jobstore.StatusCompleted || rec.TranscriptRef != first.TranscriptRef {
		t.Fatalf("resumed record = %+v, want completed with same transcript ref", rec)
	}
}

func TestMarshalDelegateResultsBoundLargeOutput(t *testing.T) {
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
	if !json.Valid([]byte(out)) {
		t.Fatalf("delegate result returned invalid JSON: %s", out)
	}
	if len([]rune(out)) > jobToolResultMinJSONChars {
		t.Fatalf("delegate result length = %d, want <= %d", len([]rune(out)), jobToolResultMinJSONChars)
	}
	var parsed struct {
		Output    string `json:"output"`
		Truncated bool   `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("unmarshal delegate result: %v", err)
	}
	if !parsed.Truncated {
		t.Fatalf("truncated = false, want true in %s", out)
	}
	if !strings.Contains(parsed.Output, "delegate-tail") {
		t.Fatalf("output tail not retained: %q", parsed.Output)
	}

	sendOut, err := marshalSendMessageResult(sendMessageResult{
		Target:              "job_old",
		JobID:               "job_new",
		Type:                string(jobstore.JobDelegate),
		Status:              jobstore.StatusCompleted,
		RunningInBackground: false,
		Action:              "resumed",
		ResumedFromJobID:    "job_old",
		TranscriptRef:       "local:child",
		Output:              strings.Repeat("prefix-", 200) + "send-tail",
	}, jobToolResultMinJSONChars)
	if err != nil {
		t.Fatalf("marshalSendMessageResult: %v", err)
	}
	if !json.Valid([]byte(sendOut)) {
		t.Fatalf("send result returned invalid JSON: %s", sendOut)
	}
	if len([]rune(sendOut)) > jobToolResultMinJSONChars {
		t.Fatalf("send result length = %d, want <= %d", len([]rune(sendOut)), jobToolResultMinJSONChars)
	}
	if err := json.Unmarshal([]byte(sendOut), &parsed); err != nil {
		t.Fatalf("unmarshal send result: %v", err)
	}
	if !parsed.Truncated {
		t.Fatalf("truncated = false, want true in %s", sendOut)
	}
	if !strings.Contains(parsed.Output, "send-tail") {
		t.Fatalf("output tail not retained: %q", parsed.Output)
	}
}

func TestJobSendMessageNegativeBlockTimeoutDoesNotResume(t *testing.T) {
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
		Name:      "job_send_message",
		Arguments: json.RawMessage(fmt.Sprintf(`{"target":%q,"message":"run again","background":false,"block_timeout_ms":-1}`, first.JobID)),
	})
	if !res.IsError {
		t.Fatalf("job_send_message succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "block_timeout_ms must be non-negative") {
		t.Fatalf("job_send_message error = %q, want non-negative block_timeout_ms error", res.Output)
	}
	after := s.jobManager.list(listFilter{Type: jobstore.JobDelegate})
	if len(after) != len(before) {
		t.Fatalf("delegate jobs grew from %d to %d; jobs = %+v", len(before), len(after), after)
	}
}

func TestJobSendMessageToShellJobNotMessageable(t *testing.T) {
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
	if err := json.Unmarshal([]byte(shellRes.Output), &shellOut); err != nil {
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
		Name:      "job_send_message",
		Arguments: json.RawMessage(fmt.Sprintf(`{"target":%q,"message":"hi"}`, shellOut.JobID)),
	})
	if !res.IsError {
		t.Fatalf("job_send_message succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "target_not_messageable") {
		t.Fatalf("job_send_message error = %q, want target_not_messageable", res.Output)
	}
}

func TestJobSendMessageAliasTargetReturnsRuntimeShape(t *testing.T) {
	s := newTestSession(t)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "send",
		Name:      "job_send_message",
		Arguments: json.RawMessage(`{"target":"caller","message":"runtime advisory"}`),
	})
	if res.IsError {
		t.Fatalf("job_send_message returned error: %s", res.Output)
	}
	var out struct {
		Target      string `json:"target"`
		Delivered   bool   `json:"delivered"`
		Action      string `json:"action"`
		MessageType string `json:"message_type"`
		JobID       string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatalf("unmarshal job_send_message output: %v (output: %s)", err, res.Output)
	}
	if out.Target != "caller" || !out.Delivered || out.Action != "sent" || out.MessageType != "runtime" {
		t.Fatalf("job_send_message output = %+v, want runtime sent shape", out)
	}
	if out.JobID != "" {
		t.Fatalf("job_send_message alias output included job_id %q", out.JobID)
	}
	queue := s.SteeringQueueSnapshot()
	if len(queue) != 1 || queue[0].Text != "runtime advisory" {
		t.Fatalf("steering queue = %+v, want runtime advisory", queue)
	}
}

func TestJobToolsDefinitions(t *testing.T) {
	required := func(t *testing.T, def llm.ToolDefinition, name string, want []string) {
		t.Helper()
		if def.Name != name {
			t.Fatalf("definition name = %q, want %q", def.Name, name)
		}
		required := requiredParams(t, name, def.Parameters["required"])
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

	required(t, tooldefs.DefJobReadOutput(), "job_read_output", []string{"job_id"})
	required(t, tooldefs.DefJobStop(), "job_stop", []string{"job_id"})
	required(t, tooldefs.DefJobList(), "job_list", nil)
	required(t, tooldefs.DefDelegate([]string{"reviewer"}), "delegate", []string{"task"})
	required(t, tooldefs.DefJobWatch(WatchEventKindNames), "job_watch", []string{"target"})
	required(t, tooldefs.DefJobSendMessage(), "job_send_message", []string{"target", "message"})

	readProps := tooldefs.DefJobReadOutput().Parameters["properties"].(map[string]any)
	for _, param := range []string{"tail_bytes", "grep", "block", "block_timeout_ms", "limit_bytes", "max_chars"} {
		if _, ok := readProps[param]; !ok {
			t.Fatalf("job_read_output missing param %q", param)
		}
	}
	listProps := tooldefs.DefJobList().Parameters["properties"].(map[string]any)
	for _, param := range []string{"status", "type", "include_nested", "limit", "cursor"} {
		if _, ok := listProps[param]; !ok {
			t.Fatalf("job_list missing param %q", param)
		}
	}
	cursor, ok := listProps["cursor"].(map[string]any)
	if !ok {
		t.Fatalf("job_list cursor schema = %T, want map[string]any", listProps["cursor"])
	}
	cursorTypes, ok := cursor["type"].([]any)
	if !ok || !containsAnyString(cursorTypes, "string") || !containsAnyString(cursorTypes, "null") {
		t.Fatalf("job_list cursor type = %#v, want string/null", cursor["type"])
	}
	stopProps := tooldefs.DefJobStop().Parameters["properties"].(map[string]any)
	for _, param := range []string{"job_id", "signal", "block", "block_timeout_ms", "include_children"} {
		if _, ok := stopProps[param]; !ok {
			t.Fatalf("job_stop missing param %q", param)
		}
	}
	delegateProps := tooldefs.DefDelegate([]string{"reviewer"}).Parameters["properties"].(map[string]any)
	for _, param := range []string{"task", "background", "agent_type", "model", "reasoning_effort", "block_timeout_ms", "result_schema"} {
		if _, ok := delegateProps[param]; !ok {
			t.Fatalf("delegate missing param %q", param)
		}
	}
	sendProps := tooldefs.DefJobSendMessage().Parameters["properties"].(map[string]any)
	for _, param := range []string{"target", "message", "on_finished", "background", "block_timeout_ms"} {
		if _, ok := sendProps[param]; !ok {
			t.Fatalf("job_send_message missing param %q", param)
		}
	}
	watchProps := tooldefs.DefJobWatch(WatchEventKindNames).Parameters["properties"].(map[string]any)
	for _, param := range []string{"target", "output_match", "progress_interval_ms", "events", "trigger", "send", "clear"} {
		if _, ok := watchProps[param]; !ok {
			t.Fatalf("job_watch missing param %q", param)
		}
	}
}

func TestJobReadOutputRejectsInvalidArgs(t *testing.T) {
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
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(shellRes.Output), &shellOut); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, shellRes.Output)
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	for _, tc := range []struct {
		name string
		args string
	}{
		{"tail_bytes", fmt.Sprintf(`{"job_id":%q,"tail_bytes":0}`, shellOut.JobID)},
		{"grep", fmt.Sprintf(`{"job_id":%q,"grep":"["}`, shellOut.JobID)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
				ID:        "read",
				Name:      "job_read_output",
				Arguments: json.RawMessage(tc.args),
			})
			if !res.IsError {
				t.Fatalf("job_read_output succeeded, want error: %s", res.Output)
			}
		})
	}
}

func TestJobReadOutputGrepSearchesRetainedOutputBeyondTail(t *testing.T) {
	s := newTestSession(t)

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"printf 'needle-start\n'; yes filler-line | head -c 70000; sleep 30","background":true}`),
	})
	if shellRes.IsError {
		t.Fatalf("shell returned error: %s", shellRes.Output)
	}
	var shellOut struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(shellRes.Output), &shellOut); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, shellRes.Output)
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	readOut := waitForJobGrepMatch(t, s, shellOut.JobID, "needle-start", 1024)
	if strings.Contains(readOut.Content, "needle-start") {
		t.Fatalf("tail content unexpectedly contains retained-only match: %q", readOut.Content)
	}
	if len(readOut.Matches) != 1 || !strings.Contains(readOut.Matches[0].Line, "needle-start") {
		t.Fatalf("matches = %+v, want retained output match", readOut.Matches)
	}
	if readOut.Matches[0].ByteOffset == nil || *readOut.Matches[0].ByteOffset != 0 {
		t.Fatalf("match byte offset = %+v, want 0", readOut.Matches[0].ByteOffset)
	}
}

func TestJobReadOutputSmallMaxCharsReturnsValidJSON(t *testing.T) {
	s := newTestSession(t)

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"yes filler-line | head -c 10000; sleep 30","background":true}`),
	})
	if shellRes.IsError {
		t.Fatalf("shell returned error: %s", shellRes.Output)
	}
	var shellOut struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(shellRes.Output), &shellOut); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, shellRes.Output)
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	res := waitForJobOutputBytes(t, s, shellOut.JobID, 1000, 100)
	if !json.Valid([]byte(res)) {
		t.Fatalf("job_read_output returned invalid JSON: %s", res)
	}
	if strings.HasPrefix(res, "[WARNING: Tool output was truncated.") {
		t.Fatalf("job_read_output was registry-truncated: %s", res)
	}
	if len([]rune(res)) > jobToolResultMinJSONChars {
		t.Fatalf("job_read_output length = %d, want <= effective bound %d", len([]rune(res)), jobToolResultMinJSONChars)
	}
}

func TestJobReadOutputDropsOversizedStructuredResultWhenBounding(t *testing.T) {
	valid := true
	reason := "schema_result_too_large"
	out, err := marshalBoundedJobReadOutputResult(jobReadOutputResult{
		JobID:                  "job_delegate",
		Type:                   string(jobstore.JobDelegate),
		Status:                 string(jobstore.StatusCompleted),
		Content:                strings.Repeat("output-", 200),
		TotalBytes:             1400,
		Truncated:              true,
		StructuredResult:       map[string]any{"payload": strings.Repeat("x", jobToolResultMinJSONChars)},
		StructuredResultValid:  &valid,
		StructuredResultReason: reason,
	}, jobToolResultMinJSONChars)
	if err != nil {
		t.Fatalf("marshalBoundedJobReadOutputResult: %v", err)
	}
	if !json.Valid([]byte(out)) {
		t.Fatalf("job_read_output projection returned invalid JSON: %s", out)
	}
	if len([]rune(out)) > jobToolResultMinJSONChars {
		t.Fatalf("job_read_output projection length = %d, want <= %d", len([]rune(out)), jobToolResultMinJSONChars)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("unmarshal bounded projection: %v", err)
	}
	if _, ok := parsed["structured_result"]; ok {
		t.Fatalf("bounded projection kept oversized structured_result: %s", out)
	}
	if parsed["structured_result_valid"] != false {
		t.Fatalf("structured_result_valid = %v, want false", parsed["structured_result_valid"])
	}
	if parsed["structured_result_reason"] != "projection_too_large" {
		t.Fatalf("reason = %v, want projection_too_large", parsed["structured_result_reason"])
	}
}

func TestJobReadOutputProjectionTooLargeDoesNotMutateDurableStructuredResult(t *testing.T) {
	payload := strings.Repeat("x", jobToolResultMinJSONChars)
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return communicateWithStructured("background report", map[string]any{
					"payload": payload,
				})
			},
		},
	})
	s := newDelegateTestSession(t, c)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:   "delegate",
		Name: "delegate",
		Arguments: json.RawMessage(`{
			"task":"produce a large structured result",
			"result_schema":{
				"type":"object",
				"properties":{"payload":{"type":"string"}},
				"required":["payload"]
			}
		}`),
	})
	if res.IsError {
		t.Fatalf("delegate returned error: %s", res.Output)
	}
	var delegateOut struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(res.Output), &delegateOut); err != nil {
		t.Fatalf("unmarshal delegate output: %v (output: %s)", err, res.Output)
	}
	waitForShellDone(t, s.jobManager, delegateOut.JobID)

	readRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "read",
		Name:      "job_read_output",
		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"max_chars":%d}`, delegateOut.JobID, jobToolResultMinJSONChars)),
	})
	if readRes.IsError {
		t.Fatalf("job_read_output returned error: %s", readRes.Output)
	}
	assertStructuredResultInvalidReason(t, readRes.Output, "projection_too_large")

	rec := loadShellRecord(t, s.jobManager, delegateOut.JobID)
	structured, ok := rec.StructuredResult.(map[string]any)
	if !ok || structured["payload"] != payload {
		t.Fatalf("durable structured_result = %+v, want original payload", rec.StructuredResult)
	}
	if rec.StructuredResultValid == nil || !*rec.StructuredResultValid {
		t.Fatalf("durable structured_result_valid = %v, want true", rec.StructuredResultValid)
	}
	if rec.StructuredResultReason != "" {
		t.Fatalf("durable structured_result_reason = %q, want empty", rec.StructuredResultReason)
	}
}

func TestJobToolOutputLimitsHaveJSONMinimum(t *testing.T) {
	s := newShellToolTestSession(t, SessionConfig{
		ToolOutputLimits: map[string]schema.ToolOutputLimit{
			"job_read_output":  {MaxChars: 1, Strategy: schema.TruncHeadTail},
			"job_list":         {MaxChars: 1, Strategy: schema.TruncHeadTail},
			"job_stop":         {MaxChars: 1, Strategy: schema.TruncHeadTail},
			"delegate":         {MaxChars: 1, Strategy: schema.TruncHeadTail},
			"job_watch":        {MaxChars: 1, Strategy: schema.TruncHeadTail},
			"job_send_message": {MaxChars: 1, Strategy: schema.TruncHeadTail},
		},
	})

	for _, name := range []string{"job_read_output", "job_list", "job_stop", "delegate", "job_watch", "job_send_message"} {
		rt := s.reg.Get(name)
		if rt == nil {
			t.Fatalf("%s not registered", name)
		}
		if rt.Limit.MaxChars != jobToolResultMinJSONChars {
			t.Fatalf("%s MaxChars = %d, want JSON minimum %d", name, rt.Limit.MaxChars, jobToolResultMinJSONChars)
		}
	}
}

func TestJobReadOutputGrepSearchesTerminalOutputFileBeyondTail(t *testing.T) {
	s := newTestSession(t)

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"printf 'needle-start\n'; yes filler-line | head -c 70000","background":true}`),
	})
	if shellRes.IsError {
		t.Fatalf("shell returned error: %s", shellRes.Output)
	}
	var shellOut struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(shellRes.Output), &shellOut); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, shellRes.Output)
	}
	waitForShellDone(t, s.jobManager, shellOut.JobID)

	readOut := waitForJobGrepMatch(t, s, shellOut.JobID, "needle-start", 1024)
	if readOut.Status != string(jobstore.StatusCompleted) {
		t.Fatalf("status = %q, want completed", readOut.Status)
	}
	if strings.Contains(readOut.Content, "needle-start") {
		t.Fatalf("tail content unexpectedly contains retained-only match: %q", readOut.Content)
	}
	if len(readOut.Matches) != 1 || !strings.Contains(readOut.Matches[0].Line, "needle-start") {
		t.Fatalf("matches = %+v, want terminal retained output match", readOut.Matches)
	}
	if readOut.Matches[0].ByteOffset == nil || *readOut.Matches[0].ByteOffset != 0 {
		t.Fatalf("match byte offset = %+v, want 0", readOut.Matches[0].ByteOffset)
	}
}

func TestJobListAcceptsNullCursor(t *testing.T) {
	s := newTestSession(t)

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "list",
		Name:      "job_list",
		Arguments: json.RawMessage(`{"cursor":null}`),
	})
	if res.IsError {
		t.Fatalf("job_list returned error for null cursor: %s", res.Output)
	}
	var out struct {
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
		t.Fatalf("unmarshal job_list output: %v (output: %s)", err, res.Output)
	}
	if out.NextCursor != nil {
		t.Fatalf("next_cursor = %q, want null", *out.NextCursor)
	}
}

func TestJobStopRejectsUnsupportedSignal(t *testing.T) {
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
	if err := json.Unmarshal([]byte(shellRes.Output), &shellOut); err != nil {
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
		t.Fatalf("job_stop succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "signal is not supported") {
		t.Fatalf("job_stop error = %q, want unsupported signal", res.Output)
	}
}

func TestJobStopAcceptsIncludeChildrenThroughRegistry(t *testing.T) {
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
	if err := json.Unmarshal([]byte(shellRes.Output), &shellOut); err != nil {
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

func TestJobReadOutputRejectsLargeGrepBeforeRegistryTruncation(t *testing.T) {
	s := newTestSession(t)

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"printf 'ready-line\n'","background":true}`),
	})
	if shellRes.IsError {
		t.Fatalf("shell returned error: %s", shellRes.Output)
	}
	var shellOut struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(shellRes.Output), &shellOut); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, shellRes.Output)
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "read",
		Name:      "job_read_output",
		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"grep":%q}`, shellOut.JobID, strings.Repeat("a", maxJobGrepPatternBytes+1))),
	})
	if !res.IsError {
		t.Fatalf("job_read_output succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "grep must be at most") {
		t.Fatalf("job_read_output error = %q, want grep limit", res.Output)
	}
}

func TestJobReadOutputRejectsJSONExpandedGrepBeforeRegistryTruncation(t *testing.T) {
	s := newTestSession(t)

	shellRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "shell",
		Name:      "shell",
		Arguments: json.RawMessage(`{"command":"printf 'ready-line\n'","background":true}`),
	})
	if shellRes.IsError {
		t.Fatalf("shell returned error: %s", shellRes.Output)
	}
	var shellOut struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(shellRes.Output), &shellOut); err != nil {
		t.Fatalf("unmarshal shell output: %v (output: %s)", err, shellRes.Output)
	}
	t.Cleanup(func() {
		_, _ = s.jobManager.stop(shellOut.JobID)
		waitForShellDone(t, s.jobManager, shellOut.JobID)
	})

	patternJSON, err := json.Marshal(strings.Repeat("\x00", maxJobGrepPatternJSONChars(jobToolResultDefaultMaxChar)/4))
	if err != nil {
		t.Fatalf("marshal grep pattern: %v", err)
	}
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "read",
		Name:      "job_read_output",
		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"grep":%s}`, shellOut.JobID, patternJSON)),
	})
	if !res.IsError {
		t.Fatalf("job_read_output succeeded, want error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "grep is too large after JSON escaping") {
		t.Fatalf("job_read_output error = %q, want JSON escaping limit", res.Output)
	}
}

type jobReadOutputTestResult struct {
	JobID   string  `json:"job_id"`
	Type    string  `json:"type"`
	Status  string  `json:"status"`
	Reason  *string `json:"reason"`
	Content string  `json:"content"`
	Grep    string  `json:"grep"`
	Matches []struct {
		ByteOffset *int64 `json:"byte_offset"`
		Line       string `json:"line"`
	} `json:"matches"`
	TotalBytes            int64          `json:"total_bytes"`
	Truncated             bool           `json:"truncated"`
	ExitCode              *int           `json:"exit_code"`
	StructuredResult      map[string]any `json:"structured_result"`
	StructuredResultValid bool           `json:"structured_result_valid"`
}

func assertStructuredResultInvalidReason(t *testing.T, out, reason string) {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed["structured_result"]; ok {
		t.Fatal("structured_result present for invalid/missing schema result")
	}
	if parsed["structured_result_valid"] != false {
		t.Fatalf("structured_result_valid = %v, want false", parsed["structured_result_valid"])
	}
	if parsed["structured_result_reason"] != reason {
		t.Fatalf("reason = %v, want %s", parsed["structured_result_reason"], reason)
	}
}

func assertStructuredResultFieldsAbsent(t *testing.T, out string) {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed["structured_result"]; ok {
		t.Fatal("structured_result present without schema or structured output")
	}
	if _, ok := parsed["structured_result_valid"]; ok {
		t.Fatal("structured_result_valid present without schema or structured output")
	}
	if _, ok := parsed["structured_result_reason"]; ok {
		t.Fatal("structured_result_reason present without schema or structured output")
	}
}

func waitForJobOutput(t *testing.T, s *Session, jobID, want string) jobReadOutputTestResult {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
			ID:        "read",
			Name:      "job_read_output",
			Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"tail_bytes":65536,"grep":"ready","max_chars":20000}`, jobID)),
		})
		if res.IsError {
			t.Fatalf("job_read_output returned error: %s", res.Output)
		}
		last = res.Output
		var out jobReadOutputTestResult
		if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
			t.Fatalf("unmarshal job_read_output: %v (output: %s)", err, res.Output)
		}
		if strings.Contains(out.Content, want) {
			return out
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job_read_output never contained %q; last output: %s", want, last)
	return jobReadOutputTestResult{}
}

func waitForJobGrepMatch(t *testing.T, s *Session, jobID, want string, tailBytes int) jobReadOutputTestResult {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
			ID:        "read",
			Name:      "job_read_output",
			Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"tail_bytes":%d,"grep":%q,"limit_bytes":4096,"max_chars":20000}`, jobID, tailBytes, want)),
		})
		if res.IsError {
			t.Fatalf("job_read_output returned error: %s", res.Output)
		}
		last = res.Output
		var out jobReadOutputTestResult
		if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
			t.Fatalf("unmarshal job_read_output: %v (output: %s)", err, res.Output)
		}
		if out.TotalBytes > int64(tailBytes) && len(out.Matches) > 0 {
			return out
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job_read_output never found grep match %q; last output: %s", want, last)
	return jobReadOutputTestResult{}
}

func waitForJobOutputBytes(t *testing.T, s *Session, jobID string, wantBytes int64, maxChars int) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
			ID:        "read",
			Name:      "job_read_output",
			Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"tail_bytes":65536,"max_chars":%d}`, jobID, maxChars)),
		})
		if res.IsError {
			t.Fatalf("job_read_output returned error: %s", res.Output)
		}
		last = res.Output
		var out jobReadOutputTestResult
		if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
			t.Fatalf("unmarshal job_read_output: %v (output: %s)", err, res.Output)
		}
		if out.TotalBytes >= wantBytes {
			return res.Output
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("job_read_output never reached %d bytes; last output: %s", wantBytes, last)
	return ""
}

type jobListToolOutput struct {
	Jobs []jobListToolEntry `json:"jobs"`
}

type jobListToolEntry struct {
	JobID       string  `json:"job_id"`
	ParentJobID *string `json:"parent_job_id"`
}

func jobListToolOutputContains(records []jobListToolEntry, jobID string) bool {
	return findJobListToolOutput(records, jobID) != nil
}

func findJobListToolOutput(records []jobListToolEntry, jobID string) *jobListToolEntry {
	for i := range records {
		if records[i].JobID == jobID {
			return &records[i]
		}
	}
	return nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsAnyString(values []any, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func requiredParams(t *testing.T, name string, raw any) []string {
	t.Helper()
	switch values := raw.(type) {
	case []string:
		return values
	case []any:
		required := make([]string, 0, len(values))
		for _, value := range values {
			required = append(required, fmt.Sprint(value))
		}
		return required
	default:
		t.Fatalf("%s required = %T, want array", name, raw)
		return nil
	}
}

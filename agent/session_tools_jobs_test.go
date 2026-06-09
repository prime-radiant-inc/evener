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
	if stopOut.JobID != shellOut.JobID || stopOut.Status != string(jobstore.StatusStopped) || stopOut.Reason == nil || *stopOut.Reason != "stopped" {
		t.Fatalf("job_stop = %+v, want stopped/stopped", stopOut)
	}

	waitForShellDone(t, s.jobManager, shellOut.JobID)
	rec := loadShellRecord(t, s.jobManager, shellOut.JobID)
	if rec.Status != jobstore.StatusStopped || rec.Reason != "stopped" {
		t.Fatalf("durable job after stop = %+v, want stopped/stopped", rec)
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

	readProps := tooldefs.DefJobReadOutput().Parameters["properties"].(map[string]any)
	for _, param := range []string{"tail_bytes", "grep", "block", "block_timeout_ms", "limit_bytes", "max_chars"} {
		if _, ok := readProps[param]; !ok {
			t.Fatalf("job_read_output missing param %q", param)
		}
	}
	listProps := tooldefs.DefJobList().Parameters["properties"].(map[string]any)
	for _, param := range []string{"status", "type", "limit", "cursor"} {
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
	for _, param := range []string{"job_id", "signal", "block", "block_timeout_ms"} {
		if _, ok := stopProps[param]; !ok {
			t.Fatalf("job_stop missing param %q", param)
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
	TotalBytes int64 `json:"total_bytes"`
	Truncated  bool  `json:"truncated"`
	ExitCode   *int  `json:"exit_code"`
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

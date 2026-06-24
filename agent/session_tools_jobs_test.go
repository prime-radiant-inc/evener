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

// toolResultJSON returns the structured JSON for a tool result: the StateResult
// ride-along (tool_state) when the tool sets one — the case for shell and the job
// tools, which now return plain text to the model and the struct as state — else
// the model-facing output (tools that still return JSON directly, e.g. delegate).
func toolResultJSON(res tooldefs.ExecResult) []byte {
	if len(res.ToolState) > 0 {
		return res.ToolState
	}
	return []byte(res.Output)
}

func executeJobReadOutputForTest(t *testing.T, s *Session, call llm.ToolCallData) tooldefs.ExecResult {
	t.Helper()
	args := map[string]any{}
	if len(call.Arguments) > 0 {
		if err := json.Unmarshal(call.Arguments, &args); err != nil {
			msg := fmt.Sprintf("invalid tool arguments JSON: %v", err)
			return tooldefs.ExecResult{
				ToolName:   "job_read_output",
				CallID:     call.ID,
				Output:     msg,
				FullOutput: msg,
				IsError:    true,
			}
		}
	}
	callID := call.ID
	if strings.TrimSpace(callID) == "" {
		callID = "job_read_output_test"
	}
	v, err := jobReadOutputTool(context.Background(), s, args, jobToolResultDefaultMaxChar)
	if err != nil {
		msg := fmt.Sprintf("%v", err)
		return tooldefs.ExecResult{
			ToolName:   "job_read_output",
			CallID:     callID,
			Output:     msg,
			FullOutput: msg,
			IsError:    true,
		}
	}
	if st, ok := v.(tooldefs.StateResult); ok {
		res := tooldefs.ExecResult{
			ToolName:   "job_read_output",
			CallID:     callID,
			Output:     st.Output,
			FullOutput: st.Output,
		}
		if st.State != nil {
			data, err := json.Marshal(st.State)
			if err != nil {
				t.Fatalf("marshal job_read_output test state: %v", err)
			}
			res.ToolState = data
		}
		return res
	}
	out := fmt.Sprint(v)
	return tooldefs.ExecResult{
		ToolName:   "job_read_output",
		CallID:     callID,
		Output:     out,
		FullOutput: out,
	}
}

// handlerJSON returns the structured JSON from a tool handler called directly
// (not via the registry), whose return is now a tooldefs.StateResult: it marshals
// the State. Tools still returning a JSON string yield that string verbatim.
func handlerJSON(t *testing.T, v any) []byte {
	t.Helper()
	switch r := v.(type) {
	case tooldefs.StateResult:
		b, err := json.Marshal(r.State)
		if err != nil {
			t.Fatalf("marshal handler state: %v", err)
		}
		return b
	case string:
		return []byte(r)
	default:
		t.Fatalf("unexpected handler result type %T", v)
		return nil
	}
}

type jobListDescendantEntry struct {
	JobID              string  `json:"job_id"`
	DelegateID         string  `json:"delegate_id"`
	Status             string  `json:"status"`
	OwnerSessionID     string  `json:"owner_session_id"`
	Depth              int     `json:"depth"`
	Resumable          *bool   `json:"resumable"`
	NotResumableReason *string `json:"not_resumable_reason"`
}

type jobListDescendantOutput struct {
	Jobs  []jobListDescendantEntry `json:"jobs"`
	Count int                      `json:"count"`
}

func findDescendantRow(rows []jobListDescendantEntry, jobID string) *jobListDescendantEntry {
	for i := range rows {
		if rows[i].JobID == jobID {
			return &rows[i]
		}
	}
	return nil
}

func newWalkJobManager(t *testing.T, sessionID string) *jobManager {
	t.Helper()
	jm, err := newJobManager(t.TempDir(), sessionID, func(jobNotification) {})
	if err != nil {
		t.Fatalf("new jobManager %q: %v", sessionID, err)
	}
	return jm
}

func stringPtrEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func boolPtrEqual(a, b *bool) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func decodeJobListArgs(t *testing.T, args string) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal([]byte(args), &decoded); err != nil {
		t.Fatalf("decode job_list args %s: %v", args, err)
	}
	return decoded
}

func runJobListTool(t *testing.T, s *Session) jobListToolOutput {
	t.Helper()
	res := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
		ID:        "list",
		Name:      "job_list",
		Arguments: json.RawMessage(`{}`),
	})
	if res.IsError {
		t.Fatalf("job_list returned error: %s", res.Output)
	}
	var out jobListToolOutput
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal job_list output: %v (output: %s)", err, res.Output)
	}
	return out
}

func newManualRunningJob(t *testing.T, s *Session) *jobstore.JobRecord {
	t.Helper()
	rec, err := s.jobManager.createShell(createShellOpts{Command: "manual running job"})
	if err != nil {
		t.Fatalf("create shell job: %v", err)
	}
	t.Cleanup(func() {
		_ = s.jobManager.finalize(rec.JobID, jobstore.StatusCancelled, "test_cleanup", nil)
		waitForShellDone(t, s.jobManager, rec.JobID)
	})
	return rec
}

func appendManualJobOutput(jm *jobManager, jobID string, output string) {
	jm.mu.Lock()
	run := jm.running[jobID]
	jm.mu.Unlock()
	if run != nil {
		_, _ = run.output.Append([]byte(output))
	}
}

func blockingGrepRead(t *testing.T, s *Session, jobID, grep string, timeoutMS int) (jobReadOutputTestResult, time.Duration) {
	t.Helper()
	started := time.Now()
	res := executeJobReadOutputForTest(t, s, llm.ToolCallData{
		ID: "read",

		Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"grep":%q,"max_wait_ms":%d}`, jobID, grep, timeoutMS)),
	})
	elapsed := time.Since(started)
	if res.IsError {
		t.Fatalf("job_read_output returned error: %s", res.Output)
	}
	var out jobReadOutputTestResult
	if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
		t.Fatalf("unmarshal job_read_output: %v (output: %s)", err, res.Output)
	}
	return out, elapsed
}

type jobReadOutputTestResult struct {
	JobID   string  `json:"job_id"`
	Type    string  `json:"type"`
	Status  string  `json:"status"`
	Reason  *string `json:"reason"`
	Content string  `json:"output"`
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
	LastActivity          *string        `json:"last_activity"`
}

func assertStructuredResultInvalidReason(t *testing.T, out, reason string) {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal(handlerJSON(t, out), &parsed); err != nil {
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
	if err := json.Unmarshal(handlerJSON(t, out), &parsed); err != nil {
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
	return waitForJobOutputWithGrep(t, s, jobID, "ready", want)
}

func waitForJobOutputWithGrep(t *testing.T, s *Session, jobID, grep, want string) jobReadOutputTestResult {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		res := executeJobReadOutputForTest(t, s, llm.ToolCallData{
			ID: "read",

			Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"tail_lines":65536,"grep":%q}`, jobID, grep)),
		})
		if res.IsError {
			t.Fatalf("job_read_output returned error: %s", res.Output)
		}
		last = res.Output
		var out jobReadOutputTestResult
		if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
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

func waitForJobGrepMatchResult(t *testing.T, s *Session, jobID, want string, tailBytes int) jobReadOutputTestResult {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		res := executeJobReadOutputForTest(t, s, llm.ToolCallData{
			ID: "read",

			Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"tail_lines":%d,"grep":%q}`, jobID, tailBytes, want)),
		})
		if res.IsError {
			t.Fatalf("job_read_output returned error: %s", res.Output)
		}
		last = res.Output
		var out jobReadOutputTestResult
		if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
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

type jobListToolOutput struct {
	Jobs    []jobListToolEntry `json:"jobs"`
	Watches []jobListToolWatch `json:"watches"`
}

type jobListToolEntry struct {
	JobID              string  `json:"job_id"`
	Kind               string  `json:"kind"`
	Type               string  `json:"type"`
	Phase              string  `json:"phase"`
	ParentJobID        *string `json:"parent_job_id"`
	Resumable          *bool   `json:"resumable"`
	NotResumableReason *string `json:"not_resumable_reason"`
	StartedAt          string  `json:"started_at"`
	EndedAt            *string `json:"ended_at"`
	LastActivity       *string `json:"last_activity"`
	RunningForMS       int64   `json:"running_for_ms"`
	DurationMS         int64   `json:"duration_ms"`
	QuietForMS         int64   `json:"quiet_for_ms"`
	LastEventAt        string  `json:"last_event_at"`
	TranscriptRef      *string `json:"transcript_ref"`
}

type jobListToolWatch struct {
	Source     string `json:"source"`
	Condition  string `json:"condition"`
	Deliveries int    `json:"deliveries"`
	CreatedAt  string `json:"created_at"`
}

func findJobListToolWatch(watches []jobListToolWatch, source, condition string) *jobListToolWatch {
	for i := range watches {
		if watches[i].Source == source && watches[i].Condition == condition {
			return &watches[i]
		}
	}
	return nil
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

func waitForJobOutputContent(t *testing.T, s *Session, jobID, want string) jobReadOutputTestResult {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		res := executeJobReadOutputForTest(t, s, llm.ToolCallData{
			ID: "read-wait",

			Arguments: json.RawMessage(fmt.Sprintf(`{"job_id":%q,"tail_lines":65536}`, jobID)),
		})
		if res.IsError {
			t.Fatalf("job_read_output returned error: %s", res.Output)
		}
		last = res.Output
		var out jobReadOutputTestResult
		if err := json.Unmarshal(toolResultJSON(res), &out); err != nil {
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

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
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

type jobListToolOutput struct {
	Jobs    []jobListToolEntry `json:"jobs"`
	Watches []jobListToolWatch `json:"watches"`
}

type jobListToolEntry struct {
	JobID              string  `json:"job_id"`
	Kind               string  `json:"kind"`
	Type               string  `json:"type"`
	Status             string  `json:"status"`
	Phase              string  `json:"phase"`
	ParentJobID        *string `json:"parent_job_id"`
	ExhaustionBudget   string  `json:"exhaustion_budget"`
	ExhaustionLimit    int     `json:"exhaustion_limit"`
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

type exhaustedJobToolProjection struct {
	Status           string `json:"status"`
	ExhaustionBudget string `json:"exhaustion_budget"`
	ExhaustionLimit  int    `json:"exhaustion_limit"`
	Resumable        *bool  `json:"resumable"`
}

func assertExhaustedJobToolProjection(t *testing.T, surface string, got exhaustedJobToolProjection) {
	t.Helper()
	if got.Status != "exhausted" || got.ExhaustionBudget != "max_turns" || got.ExhaustionLimit != 500 || got.Resumable == nil || *got.Resumable {
		t.Fatalf("%s projection = %+v, want status=exhausted budget=max_turns limit=500 resumable=false", surface, got)
	}
}

func seedExhaustedDelegateJobForTools(t *testing.T, s *Session) *jobstore.JobRecord {
	t.Helper()
	const jobID = "job_exhausted"
	started := time.Unix(1000, 0).UTC()
	ended := time.Unix(1001, 0).UTC()
	resumable := false
	outputPath := filepath.Join(s.jobManager.dir, "jobs", jobID+".log")
	output, err := jobstore.OpenOutput(outputPath, maxJobOutputRetentionBytes)
	if err != nil {
		t.Fatalf("open exhausted delegate output: %v", err)
	}
	if err := output.Close(); err != nil {
		t.Fatalf("close exhausted delegate output: %v", err)
	}
	if err := s.jobManager.appendJobEvents([]jobstore.Event{
		{
			Kind:             jobstore.EventJobStarted,
			TS:               started,
			JobID:            jobID,
			Type:             jobstore.JobDelegate,
			Description:      "exhausted delegate",
			OwnerSessionID:   s.ID(),
			VisibleToSession: s.ID(),
			DelegateID:       "dlg_exhausted",
			TranscriptRef:    encodeRef("", "child_exhausted"),
			StartedAt:        &started,
			OutputPath:       outputPath,
		},
		{
			Kind:             jobstore.EventJobFinished,
			TS:               ended,
			JobID:            jobID,
			Status:           jobstore.StatusExhausted,
			ExhaustionBudget: "max_turns",
			ExhaustionLimit:  500,
			Resumable:        &resumable,
			EndedAt:          &ended,
			TerminalGen:      "GEN_EXHAUSTED",
		},
	}); err != nil {
		t.Fatalf("seed exhausted delegate job: %v", err)
	}
	recs, err := s.jobManager.store.Load()
	if err != nil {
		t.Fatalf("load exhausted delegate job: %v", err)
	}
	return recs[jobID]
}

func TestJobTools_ExhaustedStateAgreesAcrossStatusListAndDelegate(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	rec := seedExhaustedDelegateJobForTools(t, s)

	t.Run("job_status", func(t *testing.T) {
		result, err := jobStatusTool(s, map[string]any{"job_id": rec.JobID}, jobToolResultDefaultMaxChar)
		if err != nil {
			t.Fatalf("job_status: %v", err)
		}
		var got exhaustedJobToolProjection
		if err := json.Unmarshal(handlerJSON(t, result), &got); err != nil {
			t.Fatalf("decode job_status: %v", err)
		}
		assertExhaustedJobToolProjection(t, "job_status", got)
	})

	t.Run("job_list", func(t *testing.T) {
		result, err := jobListTool(s, map[string]any{"status": []any{"exhausted"}}, jobToolResultDefaultMaxChar)
		if err != nil {
			t.Fatalf("job_list exhausted filter: %v", err)
		}
		var got struct {
			Jobs []exhaustedJobToolProjection `json:"jobs"`
		}
		if err := json.Unmarshal(handlerJSON(t, result), &got); err != nil {
			t.Fatalf("decode job_list: %v", err)
		}
		if len(got.Jobs) != 1 {
			t.Fatalf("job_list returned %d jobs, want 1", len(got.Jobs))
		}
		assertExhaustedJobToolProjection(t, "job_list", got.Jobs[0])
	})

	t.Run("delegate", func(t *testing.T) {
		result := delegateTerminalResult(s, s.jobManager, &runningJob{rec: rec})
		encoded, err := marshalDelegateResult(result, jobToolResultDefaultMaxChar)
		if err != nil {
			t.Fatalf("marshal delegate result: %v", err)
		}
		var got exhaustedJobToolProjection
		if err := json.Unmarshal([]byte(encoded), &got); err != nil {
			t.Fatalf("decode delegate result: %v", err)
		}
		assertExhaustedJobToolProjection(t, "delegate", got)
	})

	t.Run("job_list_schema", func(t *testing.T) {
		properties := tooldefs.DefJobList().Parameters["properties"].(map[string]any)
		status := properties["status"].(map[string]any)
		items := status["items"].(map[string]any)
		values := items["enum"].([]any)
		for _, value := range values {
			if value == "exhausted" {
				return
			}
		}
		t.Fatalf("job_list status enum = %v, want exhausted", values)
	})
}

func TestMarshalDelegateSendResult_ExhaustionMetadataInToolState(t *testing.T) {
	t.Parallel()
	resumable := true
	result, err := marshalDelegateSendResult(sendMessageResult{
		DelegateID:       "dlg_exhausted",
		JobID:            "job_exhausted",
		Type:             string(jobstore.JobDelegate),
		Status:           jobstore.StatusExhausted,
		Action:           "started",
		ExhaustionBudget: "max_tool_rounds_per_input",
		ExhaustionLimit:  1,
		Resumable:        &resumable,
	}, jobToolResultDefaultMaxChar)
	if err != nil {
		t.Fatalf("marshalDelegateSendResult: %v", err)
	}
	stateResult, ok := result.(tooldefs.StateResult)
	if !ok {
		t.Fatalf("marshalDelegateSendResult result = %T, want tool.StateResult", result)
	}
	state, ok := stateResult.State.(delegateSendResult)
	if !ok {
		t.Fatalf("tool state = %T, want delegateSendResult", stateResult.State)
	}
	if state.Status != "exhausted" || state.ExhaustionBudget != "max_tool_rounds_per_input" || state.ExhaustionLimit != 1 || state.Resumable == nil || !*state.Resumable {
		t.Fatalf("typed tool state = %+v, want exhausted budget metadata and resumable=true", state)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal delegate_send tool state: %v", err)
	}
	for _, want := range []string{`"status":"exhausted"`, `"exhaustion_budget":"max_tool_rounds_per_input"`, `"exhaustion_limit":1`, `"resumable":true`} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("delegate_send tool state %s missing %s", encoded, want)
		}
	}
	rendered := renderToolCardForStateResult("delegate_send", "send", stateResult.Output, encoded)
	for _, want := range []string{
		"job_id=job_exhausted",
		"status=exhausted",
		"exhaustion_budget=max_tool_rounds_per_input",
		"exhaustion_limit=1",
		"resumable=true",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered delegate_send tool state missing %q:\n%s", want, rendered)
		}
	}
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
	return slices.Contains(values, want)
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

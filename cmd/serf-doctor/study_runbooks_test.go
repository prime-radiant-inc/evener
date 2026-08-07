package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
)

// This file is WS9 Task 5's fixture set for the four standing runbooks
// (error-loop, stale-notification, run-timeout-waste, truncation-waste)
// bundled under internal/bundled/skills/doctoring-serf/runbooks/. Unlike
// audit_test.go's tests, none of these substitute bundledSkills -- they run
// serf-doctor audit against the REAL embedded runbook files, the first
// end-to-end exercise of the bundled-name loading seam (loadRunbook in
// main.go) with production runbook content rather than an inline fixture.

// studyProjectID mirrors fixture()'s bucket dir name — identifier.
// ValidateProjectID requires a readable prefix plus an exactly-10-character
// base62 suffix, so (unlike a per-session bucket) every study session shares
// this one bucket, keyed apart by session id instead.
const studyProjectID = "project-test-0123456789"

func studySessionBucket(base, _ string) string {
	return filepath.Join(base, "serf", "projects", studyProjectID)
}

// writeStudySession writes a session's semantic transcript + meta through
// serf's own writer types (transcript.NewWriter, schema.SaveSessionMeta),
// the same durable-format path production writes through, plus an empty
// jobs.jsonl a caller can overwrite with raw job events. cmd/ cannot import
// agent/internal/jobstore (the internal wall), so job events are written as
// raw JSONL by writeRunTimeoutJobs below, matching audit_test.go's own
// auditSessionFixture convention.
func writeStudySession(t *testing.T, base, sid string, turns []schema.Turn) (bucket string) {
	t.Helper()
	bucket = studySessionBucket(base, sid)
	sess := filepath.Join(bucket, "sessions")
	if err := os.MkdirAll(filepath.Join(sess, sid), 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := transcript.NewWriter(filepath.Join(sess, sid+".transcript.jsonl"), transcript.Header{SessionID: sid})
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range turns {
		if err := w.Append(turn); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(bucket, schema.SessionMeta{ID: sid}); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(sess, sid, "jobs.jsonl"), "")
	return bucket
}

func studyToolCall(id, name, args string) llm.ContentPart {
	return llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
		ID: id, Name: name, Arguments: json.RawMessage(args)}}
}

func studyToolResult(id, name string, content any, isError bool) llm.ContentPart {
	return llm.ContentPart{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
		ToolCallID: id, Name: name, Content: content, IsError: isError}}
}

func studySteeringTurn(kind, text string) schema.Turn {
	turn := schema.NewTurn(schema.TurnSteering, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: text}}})
	turn.SteeringKind = kind
	return turn
}

func studyCommunicateEndTurn(id string, endTurn bool) llm.ContentPart {
	args, err := json.Marshal(map[string]any{"message": "done", "end_turn": endTurn})
	if err != nil {
		panic(err)
	}
	return studyToolCall(id, "communicate", string(args))
}

// writeRunTimeoutJobs writes n terminal, zero-output run_timeout jobs owned
// by sid as raw JSONL -- the shape audit_test.go's fiveRunTimeoutJobsFor
// builds through jobstore.Event, reproduced here in wire form since cmd/
// cannot import agent/internal/jobstore.
func writeRunTimeoutJobs(t *testing.T, jobsPath, sid string, n int) {
	t.Helper()
	var lines []string
	for i := range n {
		id := fmt.Sprintf("job_%s_%d", sid, i)
		lines = append(lines,
			`{"kind":"job_started","job_id":"`+id+`","type":"shell","command":"x","owner_session_id":"`+sid+`","visible_to_session_id":"`+sid+`","started_at":"2026-08-01T00:00:00Z"}`,
			`{"kind":"job_finished","job_id":"`+id+`","status":"stopped","reason":"run_timeout","exit_code":-1,"ended_at":"2026-08-01T00:01:00Z","output_bytes":0}`,
		)
	}
	mustWrite(t, jobsPath, strings.Join(lines, "\n")+"\n")
}

// studyAuditJSON runs `serf-doctor audit --runbook name --sessions
// sessions... --json` against the REAL bundled runbook (no bundledSkills
// substitution) and decodes the result.
func studyAuditJSON(t *testing.T, base, runbookName string, sessions ...string) struct {
	SessionsChecked int `json:"sessions_checked"`
	Findings        []struct {
		Title    string `json:"title"`
		Severity string `json:"severity"`
		Category string `json:"category"`
		Evidence struct {
			SessionRefs []string `json:"sessionRefs"`
		} `json:"evidence"`
	} `json:"findings"`
} {
	t.Helper()
	var out, errb bytes.Buffer
	args := []string{"audit", "--runbook", runbookName, "--sessions", strings.Join(sessions, ","), "--json", "--state-dir", base}
	if code := run(args, &out, &errb); code != 0 {
		t.Fatalf("audit --runbook %s exit %d, stderr=%s", runbookName, code, errb.String())
	}
	var res struct {
		SessionsChecked int `json:"sessions_checked"`
		Findings        []struct {
			Title    string `json:"title"`
			Severity string `json:"severity"`
			Category string `json:"category"`
			Evidence struct {
				SessionRefs []string `json:"sessionRefs"`
			} `json:"evidence"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("audit --runbook %s: invalid json: %v\n%s", runbookName, err, out.String())
	}
	return res
}

// --- error-loop ---

func errorLoopTripTurns() []schema.Turn {
	var turns []schema.Turn
	for i := range 4 {
		id := fmt.Sprintf("el%d", i)
		turns = append(turns,
			schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				studyToolCall(id, "shell", `{"cmd":"flaky"}`),
			}}),
			schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
				studyToolResult(id, "shell", "boom", true),
			}}),
		)
	}
	return turns
}

func studyHealthyTurns() []schema.Turn {
	return []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			studyToolCall("h1", "read_file", `{"path":"a"}`),
		}}),
		schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			studyToolResult("h1", "read_file", "ok", false),
		}}),
	}
}

func TestRun_AuditErrorLoopBundledRunbook(t *testing.T) {
	base := t.TempDir()
	tripSID, healthySID := "02wLIRxqmq3AUo6vl2OW51", "02wLIRxqmq3AUo6vl2OW52"
	writeStudySession(t, base, tripSID, errorLoopTripTurns())
	writeStudySession(t, base, healthySID, studyHealthyTurns())

	res := studyAuditJSON(t, base, "error-loop", tripSID, healthySID)
	if res.SessionsChecked != 2 {
		t.Fatalf("SessionsChecked = %d, want 2", res.SessionsChecked)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("Findings = %+v, want exactly 1 (only tripSID trips)", res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != "medium" || f.Category != "error_loop" {
		t.Errorf("finding severity/category = %q/%q, want medium/error_loop", f.Severity, f.Category)
	}
	if f.Title != "Long identical-error tool-call run" {
		t.Errorf("finding title = %q", f.Title)
	}
	if len(f.Evidence.SessionRefs) != 1 || !strings.Contains(f.Evidence.SessionRefs[0], tripSID) {
		t.Errorf("finding sessionRefs = %v, want only %s", f.Evidence.SessionRefs, tripSID)
	}
}

func TestRun_AuditErrorLoopBundledRunbook_HealthyEmitsZero(t *testing.T) {
	base := t.TempDir()
	healthySID := "02wLIRxqmq3AUo6vl2OW53"
	writeStudySession(t, base, healthySID, studyHealthyTurns())

	res := studyAuditJSON(t, base, "error-loop", healthySID)
	if len(res.Findings) != 0 {
		t.Errorf("Findings = %+v, want none — a healthy run emits zero findings", res.Findings)
	}
}

// inBandMCPErrorLoopTurns reproduces the real-world session shape the
// 2026-08-06 final review found the identical-run check blind to: a tool
// call (like use_browser) whose arguments carry a free-text field the model
// varies every call (here "purpose"), fragmenting longest_identical_run's
// signature well below the check's threshold even though the underlying
// action repeats; and a failure reported in-band inside a successful result
// (is_error=false, "Error: ..." text) rather than via the transport error
// flag, so longest_identical_run.errors can never be true either. A
// steering turn carrying SteeringKindLoopDetected is the one signal that
// does fire for this shape -- the runtime's own live loop detector, not
// derived from either of the above.
func inBandMCPErrorLoopTurns() []schema.Turn {
	var turns []schema.Turn
	purposes := []string{
		"checking initial page state", "confirming the viewport resized",
		"verifying the element is visible", "re-checking after the click",
	}
	for i, purpose := range purposes {
		id := fmt.Sprintf("mcp%d", i)
		args := fmt.Sprintf(`{"action":"screenshot","purpose":%q}`, purpose)
		turns = append(turns,
			schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				studyToolCall(id, "use_browser", args),
			}}),
			schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
				// is_error=false: the MCP tool reports the failure in the
				// body text, not via the transport error flag.
				studyToolResult(id, "use_browser", "Error: element not found", false),
			}}),
		)
	}
	turns = append(turns, studySteeringTurn(events.SteeringKindLoopDetected, "loop detected: repeated use_browser calls"))
	return turns
}

func TestRun_AuditErrorLoopBundledRunbook_LoopDetectorCatchesInBandMCPLoop(t *testing.T) {
	base := t.TempDir()
	sid := "02wLIRxqmq3AUo6vl2OW6A"
	writeStudySession(t, base, sid, inBandMCPErrorLoopTurns())

	res := studyAuditJSON(t, base, "error-loop", sid)
	if res.SessionsChecked != 1 {
		t.Fatalf("SessionsChecked = %d, want 1", res.SessionsChecked)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("Findings = %+v, want exactly 1 (the loop-detector check only -- free-text args and in-band errors defeat the identical-run check)", res.Findings)
	}
	f := res.Findings[0]
	if f.Title != "Runtime loop-detector fired" {
		t.Errorf("finding title = %q, want the loop-detector check -- the identical-run check must NOT have tripped for this session", f.Title)
	}
	if f.Severity != "medium" || f.Category != "error_loop" {
		t.Errorf("finding severity/category = %q/%q, want medium/error_loop", f.Severity, f.Category)
	}
}

// TestRun_AuditErrorLoopBundledRunbook_ChecksAreIndependent proves the two
// error-loop checks fire independently: errorLoopTripTurns' plain
// transport-level-error run trips only the identical-run check (no
// steering.loop-detected turn), inBandMCPErrorLoopTurns trips only the
// loop-detector check (fragmented signature + in-band error body) -- each
// finding names exactly the session that tripped its own check.
func TestRun_AuditErrorLoopBundledRunbook_ChecksAreIndependent(t *testing.T) {
	base := t.TempDir()
	identicalSID, loopDetectorSID := "02wLIRxqmq3AUo6vl2OW6B", "02wLIRxqmq3AUo6vl2OW6C"
	writeStudySession(t, base, identicalSID, errorLoopTripTurns())
	writeStudySession(t, base, loopDetectorSID, inBandMCPErrorLoopTurns())

	res := studyAuditJSON(t, base, "error-loop", identicalSID, loopDetectorSID)
	if len(res.Findings) != 2 {
		t.Fatalf("Findings = %+v, want exactly 2 (one per check, each tripped by exactly one session)", res.Findings)
	}
	byTitle := map[string][]string{}
	for _, f := range res.Findings {
		byTitle[f.Title] = f.Evidence.SessionRefs
	}
	identicalRefs := byTitle["Long identical-error tool-call run"]
	if len(identicalRefs) != 1 || !strings.Contains(identicalRefs[0], identicalSID) {
		t.Errorf("identical-run finding sessionRefs = %v, want only %s", identicalRefs, identicalSID)
	}
	loopRefs := byTitle["Runtime loop-detector fired"]
	if len(loopRefs) != 1 || !strings.Contains(loopRefs[0], loopDetectorSID) {
		t.Errorf("loop-detector finding sessionRefs = %v, want only %s", loopRefs, loopDetectorSID)
	}
}

// --- stale-notification ---

func staleNotificationTripTurns() []schema.Turn {
	return []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			studyCommunicateEndTurn("done1", true),
		}}),
		studySteeringTurn(events.SteeringKindNotification, "job_1 finished"),
		studySteeringTurn(events.SteeringKindNotification, "job_2 finished"),
	}
}

func staleNotificationHealthyTurns() []schema.Turn {
	return []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			studyCommunicateEndTurn("done1", true),
		}}),
	}
}

func TestRun_AuditStaleNotificationBundledRunbook(t *testing.T) {
	base := t.TempDir()
	tripSID, healthySID := "02wLIRxqmq3AUo6vl2OW54", "02wLIRxqmq3AUo6vl2OW55"
	writeStudySession(t, base, tripSID, staleNotificationTripTurns())
	writeStudySession(t, base, healthySID, staleNotificationHealthyTurns())

	res := studyAuditJSON(t, base, "stale-notification", tripSID, healthySID)
	if res.SessionsChecked != 2 {
		t.Fatalf("SessionsChecked = %d, want 2", res.SessionsChecked)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("Findings = %+v, want exactly 1 (only tripSID trips)", res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != "low" || f.Category != "stale_notification" {
		t.Errorf("finding severity/category = %q/%q, want low/stale_notification", f.Severity, f.Category)
	}
	if len(f.Evidence.SessionRefs) != 1 || !strings.Contains(f.Evidence.SessionRefs[0], tripSID) {
		t.Errorf("finding sessionRefs = %v, want only %s", f.Evidence.SessionRefs, tripSID)
	}
}

func TestRun_AuditStaleNotificationBundledRunbook_HealthyEmitsZero(t *testing.T) {
	base := t.TempDir()
	healthySID := "02wLIRxqmq3AUo6vl2OW56"
	writeStudySession(t, base, healthySID, staleNotificationHealthyTurns())

	res := studyAuditJSON(t, base, "stale-notification", healthySID)
	if len(res.Findings) != 0 {
		t.Errorf("Findings = %+v, want none — a healthy run emits zero findings", res.Findings)
	}
}

// --- run-timeout-waste ---

func TestRun_AuditRunTimeoutWasteBundledRunbook(t *testing.T) {
	base := t.TempDir()
	tripSID, healthySID := "02wLIRxqmq3AUo6vl2OW57", "02wLIRxqmq3AUo6vl2OW58"
	tripBucket := writeStudySession(t, base, tripSID, studyHealthyTurns())
	writeRunTimeoutJobs(t, filepath.Join(tripBucket, "sessions", tripSID, "jobs.jsonl"), tripSID, 5)
	writeStudySession(t, base, healthySID, studyHealthyTurns())

	res := studyAuditJSON(t, base, "run-timeout-waste", tripSID, healthySID)
	if res.SessionsChecked != 2 {
		t.Fatalf("SessionsChecked = %d, want 2", res.SessionsChecked)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("Findings = %+v, want exactly 1 (only tripSID trips)", res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != "high" || f.Category != "timeout" {
		t.Errorf("finding severity/category = %q/%q, want high/timeout", f.Severity, f.Category)
	}
	if len(f.Evidence.SessionRefs) != 1 || !strings.Contains(f.Evidence.SessionRefs[0], tripSID) {
		t.Errorf("finding sessionRefs = %v, want only %s", f.Evidence.SessionRefs, tripSID)
	}
}

func TestRun_AuditRunTimeoutWasteBundledRunbook_HealthyEmitsZero(t *testing.T) {
	base := t.TempDir()
	healthySID := "02wLIRxqmq3AUo6vl2OW59"
	writeStudySession(t, base, healthySID, studyHealthyTurns())

	res := studyAuditJSON(t, base, "run-timeout-waste", healthySID)
	if len(res.Findings) != 0 {
		t.Errorf("Findings = %+v, want none — a healthy run emits zero findings", res.Findings)
	}
}

// --- truncation-waste ---

// studyTruncationBanner mirrors the registry's truncation marker
// (agent/doctor/health.go's truncationBanner / agent/internal/tool/
// registry.go's truncateChars, ~:596), reproduced as a literal since cmd/
// doesn't import agent/doctor's unexported constant.
const studyTruncationBanner = "[WARNING: Tool output was truncated. First 100 characters were removed. The full output is available in the event stream.]"

func truncationWasteTripTurns() []schema.Turn {
	var turns []schema.Turn
	for i := range 3 {
		id := fmt.Sprintf("tw%d", i)
		turns = append(turns,
			schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				studyToolCall(id, "grep_files", `{"q":"x"}`),
			}}),
			schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
				studyToolResult(id, "grep_files", studyTruncationBanner+"\nrest of results", false),
			}}),
		)
	}
	return turns
}

func truncationWasteHealthyTurns() []schema.Turn {
	return []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			studyToolCall("h1", "grep_files", `{"q":"x"}`),
		}}),
		schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			studyToolResult("h1", "grep_files", "clean result, not truncated", false),
		}}),
	}
}

func TestRun_AuditTruncationWasteBundledRunbook(t *testing.T) {
	base := t.TempDir()
	tripSID, healthySID := "02wLIRxqmq3AUo6vl2OW60", "02wLIRxqmq3AUo6vl2OW61"
	writeStudySession(t, base, tripSID, truncationWasteTripTurns())
	writeStudySession(t, base, healthySID, truncationWasteHealthyTurns())

	res := studyAuditJSON(t, base, "truncation-waste", tripSID, healthySID)
	if res.SessionsChecked != 2 {
		t.Fatalf("SessionsChecked = %d, want 2", res.SessionsChecked)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("Findings = %+v, want exactly 1 (only tripSID trips)", res.Findings)
	}
	f := res.Findings[0]
	if f.Severity != "medium" || f.Category != "truncation" {
		t.Errorf("finding severity/category = %q/%q, want medium/truncation", f.Severity, f.Category)
	}
	if len(f.Evidence.SessionRefs) != 1 || !strings.Contains(f.Evidence.SessionRefs[0], tripSID) {
		t.Errorf("finding sessionRefs = %v, want only %s", f.Evidence.SessionRefs, tripSID)
	}
}

func TestRun_AuditTruncationWasteBundledRunbook_HealthyEmitsZero(t *testing.T) {
	base := t.TempDir()
	healthySID := "02wLIRxqmq3AUo6vl2OW62"
	writeStudySession(t, base, healthySID, truncationWasteHealthyTurns())

	res := studyAuditJSON(t, base, "truncation-waste", healthySID)
	if len(res.Findings) != 0 {
		t.Errorf("Findings = %+v, want none — a healthy run emits zero findings", res.Findings)
	}
}

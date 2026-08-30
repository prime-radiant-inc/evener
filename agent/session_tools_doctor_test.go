package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/doctor"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// writeTestFile writes content creating parent dirs.
func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// doctorToolForTest builds a session with a real state dir (one bucket, one
// session fixture written by the caller) and returns its doctor_evener
// registered tool plus the state root to pass.
func doctorToolForTest(t *testing.T, stateHome string) tool.RegisteredTool {
	t.Helper()
	client := llm.NewClient()
	client.Register(&agenttest.ScriptedAdapter{Provider: "openai"})
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		StateDir:         stateHome,
		NoProjectPrompts: true,
		clock:            agenttest.NewFakeClock(),
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	drainSessionEvents(sess)

	var registered tool.RegisteredTool
	for _, rt := range doctorTools(newToolDeps(sess)) {
		if rt.Definition.Name == "doctor_evener" {
			registered = rt
			break
		}
	}
	if registered.Definition.Name == "" {
		t.Fatal("doctor_evener tool not registered")
	}
	return registered
}

// TestDoctorEvener_LocateResolvesSelector proves the tool executes a locate
// against the session's own state root with no state_dir argument: the
// invocation class that failed with `evener: command not found` when the
// skill shelled out.
func TestDoctorEvener_LocateResolvesSelector(t *testing.T) {
	stateHome := newStateHome(t)
	bucket := newBucketUnder(t, stateHome)
	sid := writeDoctorFixtureSession(t, bucket)

	rt := doctorToolForTest(t, stateHome)
	out, err := rt.Exec(context.Background(), nil, map[string]any{
		"command":  "locate",
		"selector": sid,
	})
	if err != nil {
		t.Fatalf("doctor_evener locate: %v", err)
	}
	paths, ok := out.(doctor.Paths)
	if !ok {
		t.Fatalf("locate result = %T, want doctor.Paths", out)
	}
	if paths.SessionID != sid {
		t.Errorf("session_id = %q, want fixture sid %q", paths.SessionID, sid)
	}
	if !strings.HasSuffix(paths.TranscriptPath, sid+".transcript.jsonl") {
		t.Errorf("transcript_path = %q, want flat SID-prefixed path", paths.TranscriptPath)
	}
}

// TestDoctorEvener_StateDirOverride proves an explicit state_dir beats the
// session default.
func TestDoctorEvener_StateDirOverride(t *testing.T) {
	stateHome := newStateHome(t)
	bucket := newBucketUnder(t, stateHome)
	sid := writeDoctorFixtureSession(t, bucket)

	other := newStateHome(t)
	rt := doctorToolForTest(t, other)

	// against the session's own (empty) root: not found
	_, err := rt.Exec(context.Background(), nil, map[string]any{
		"command": "locate", "selector": sid,
	})
	if err == nil {
		t.Fatal("locate against empty root should fail")
	}

	// with explicit state_dir: resolves
	out, err := rt.Exec(context.Background(), nil, map[string]any{
		"command": "locate", "selector": sid, "state_dir": stateHome,
	})
	if err != nil {
		t.Fatalf("locate with state_dir: %v", err)
	}
	paths, ok := out.(doctor.Paths)
	if !ok {
		t.Fatalf("locate result = %T, want doctor.Paths", out)
	}
	if paths.SessionID != sid {
		t.Errorf("session_id = %q, want fixture sid %q", paths.SessionID, sid)
	}
}

// TestDoctorEvener_RejectsUnknownCommand proves bad commands are surfaced as
// errors, not silent defaults.
func TestDoctorEvener_RejectsUnknownCommand(t *testing.T) {
	stateHome := newStateHome(t)
	rt := doctorToolForTest(t, stateHome)
	_, err := rt.Exec(context.Background(), nil, map[string]any{"command": "bogus"})
	if err == nil {
		t.Fatal("unknown command should error")
	}
	if !strings.Contains(err.Error(), "unknown doctor command") {
		t.Errorf("err = %v, want unknown doctor command", err)
	}
}

// TestDoctorEvener_TranscriptCount proves a per-command option flows through:
// transcript --count delegate_send is the skill's structural-invocation
// oracle.
func TestDoctorEvener_TranscriptCount(t *testing.T) {
	stateHome := newStateHome(t)
	bucket := newBucketUnder(t, stateHome)
	sid := writeDoctorFixtureSession(t, bucket)

	rt := doctorToolForTest(t, stateHome)
	out, err := rt.Exec(context.Background(), nil, map[string]any{
		"command": "transcript", "selector": sid,
		"count": "delegate_send",
	})
	if err != nil {
		t.Fatalf("doctor_evener transcript count: %v", err)
	}
	count, ok := out.(doctor.CountResult)
	if !ok {
		t.Fatalf("count result = %T, want doctor.CountResult", out)
	}
	if count.Calls != 0 {
		t.Errorf("calls = %d, want 0 (fixture has no delegate_send calls)", count.Calls)
	}
}

// TestDoctorEvener_SessionsAndAudit prove the batch commands work through the
// tool with the option mapping (since, runbook).
func TestDoctorEvener_SessionsAndAudit(t *testing.T) {
	stateHome := newStateHome(t)
	bucket := newBucketUnder(t, stateHome)
	sid := writeDoctorFixtureSession(t, bucket)

	rt := doctorToolForTest(t, stateHome)
	out, err := rt.Exec(context.Background(), nil, map[string]any{
		"command": "sessions", "since": "24h",
	})
	if err != nil {
		t.Fatalf("doctor_evener sessions: %v", err)
	}
	sres, ok := out.(doctor.SessionsResult)
	if !ok {
		t.Fatalf("sessions result = %T, want doctor.SessionsResult", out)
	}
	if len(sres.Sessions) != 1 {
		t.Errorf("sessions = %d, want 1 (fixture session is recent)", len(sres.Sessions))
	}

	// audit with an explicit session list
	out, err = rt.Exec(context.Background(), nil, map[string]any{
		"command": "audit", "runbook": "error-loop", "sessions": sid,
	})
	if err != nil {
		t.Fatalf("doctor_evener audit: %v", err)
	}
	ares, ok := out.(doctor.AuditResult)
	if !ok {
		t.Fatalf("audit result = %T, want doctor.AuditResult", out)
	}
	if ares.Runbook != "error-loop" {
		t.Errorf("runbook = %q, want error-loop", ares.Runbook)
	}
	if ares.SessionsChecked != 1 {
		t.Errorf("sessions_checked = %d, want 1", ares.SessionsChecked)
	}
}

// TestDoctorEvener_ReadOnlyEnforced proves the tool is registered read-only:
// the registry must reject a write-class mutation attempt structurally.
func TestDoctorEvener_ReadOnlyEnforced(t *testing.T) {
	stateHome := newStateHome(t)
	rt := doctorToolForTest(t, stateHome)
	if !rt.ReadOnly {
		t.Fatal("doctor_evener must be registered ReadOnly:true")
	}
}

// doctorTestSID mints a validator-passing session id for the tool fixtures.
func doctorTestSID(t *testing.T) string {
	t.Helper()
	sid, err := identifier.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	return sid
}

// writeDoctorFixtureSession writes a minimal valid session (transcript with
// header + one user turn, meta, empty jobs.jsonl) into bucket/sessions/.
// Returns the minted session id.
func writeDoctorFixtureSession(t *testing.T, bucket string) string {
	t.Helper()
	sid := doctorTestSID(t)
	sess := bucket + "/sessions"
	writeTestFile(t, sess+"/"+sid+".transcript.jsonl",
		"{\"kind\":\"header\",\"format_version\":2,\"session_id\":\""+sid+"\",\"created_at\":\"2026-08-29T19:00:00Z\",\"model\":\"test-model\"}\n"+
			"{\"kind\":\"entry\",\"seq\":1,\"turn\":{\"kind\":\"USER_INPUT\",\"message\":{\"role\":\"user\",\"content\":[{\"kind\":\"text\",\"text\":\"hi\"}]},\"timestamp\":\"2026-08-29T19:00:01Z\"}}\n")
	writeTestFile(t, sess+"/"+sid+".meta.json",
		"{\"id\":\""+sid+"\",\"model\":\"test-model\",\"updated_at\":\"2026-08-29T19:00:02Z\",\"turn_count\":1}")
	writeTestFile(t, sess+"/"+sid+"/jobs.jsonl", "")
	return sid
}

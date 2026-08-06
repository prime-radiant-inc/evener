package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sessionsFixture writes two sessions under one bucket, using raw JSONL like
// fixture() above: cmd/ cannot import agent/internal/jobstore, so the wiring
// test writes the on-disk bytes directly — the enumeration semantics
// (filtering, ordering, parent linkage, outcome hint) are covered by
// agent/doctor's TestListSessions_* tests.
//
//   - root ends its last assistant turn with communicate(end_turn=true).
//   - child is a subagent spawned by root (transcript header
//     parent_session_id), meta is_subagent=true.
//
// Both transcript files' mtimes are stamped to "now" so a real --since window
// includes them deterministically regardless of how long the test takes to run.
func sessionsFixture(t *testing.T) (base, root, child string) {
	t.Helper()
	base = t.TempDir()
	root = "02wLIRxqmq3AUo6vl2OW37"
	child = "02wLIRxqmq3AUo6vl2OW38"
	bucket := filepath.Join(base, "serf", "projects", "project-test-0123456789")
	sess := filepath.Join(bucket, "sessions")
	for _, sid := range []string{root, child} {
		if err := os.MkdirAll(filepath.Join(sess, sid), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	mustWrite(t, filepath.Join(sess, root+".transcript.jsonl"), strings.Join([]string{
		`{"kind":"header","format_version":2,"session_id":"` + root + `","created_at":"2026-08-01T00:00:00Z","model":"anthropic/claude-a"}`,
		`{"kind":"entry","seq":1,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[{"kind":"text","text":"go"}]}}}`,
		`{"kind":"entry","seq":2,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"tool_call","tool_call":{"id":"tc1","name":"communicate","arguments":{"message":"done","end_turn":true}}}]}}}`,
	}, "\n")+"\n")
	mustWrite(t, filepath.Join(sess, root+".meta.json"), `{"id":"`+root+`","model":"anthropic/claude-a","turn_count":2}`)
	mustWrite(t, filepath.Join(sess, root, "jobs.jsonl"), "")

	mustWrite(t, filepath.Join(sess, child+".transcript.jsonl"), strings.Join([]string{
		`{"kind":"header","format_version":2,"session_id":"` + child + `","parent_session_id":"` + root + `","created_at":"2026-08-01T00:30:00Z","model":"anthropic/claude-a"}`,
		`{"kind":"entry","seq":1,"turn":{"kind":"ASSISTANT","message":{"role":"assistant","content":[{"kind":"text","text":"still working"}]}}}`,
	}, "\n")+"\n")
	mustWrite(t, filepath.Join(sess, child+".meta.json"), `{"id":"`+child+`","model":"anthropic/claude-a","turn_count":1,"is_subagent":true}`)
	mustWrite(t, filepath.Join(sess, child, "jobs.jsonl"), "")

	now := time.Now()
	for _, sid := range []string{root, child} {
		p := filepath.Join(sess, sid+".transcript.jsonl")
		if err := os.Chtimes(p, now, now); err != nil {
			t.Fatal(err)
		}
	}
	return base, root, child
}

func TestRun_SessionsHuman(t *testing.T) {
	base, root, child := sessionsFixture(t)
	var out, errb bytes.Buffer
	if code := run([]string{"sessions", "--state-dir", base}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	got := out.String()
	for _, want := range []string{root, child, "sessions=2", "end_turn=true"} {
		if !strings.Contains(got, want) {
			t.Errorf("sessions output missing %q:\n%s", want, got)
		}
	}
}

func TestRun_SessionsJSON(t *testing.T) {
	base, root, child := sessionsFixture(t)
	var out, errb bytes.Buffer
	if code := run([]string{"sessions", "--json", "--state-dir", base}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	var rows []struct {
		SessionID       string `json:"session_id"`
		IsSubagent      bool   `json:"is_subagent"`
		ParentSessionID string `json:"parent_session_id"`
		Outcome         string `json:"outcome"`
	}
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out.String())
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2:\n%s", len(rows), out.String())
	}
	byID := map[string]struct {
		SessionID       string `json:"session_id"`
		IsSubagent      bool   `json:"is_subagent"`
		ParentSessionID string `json:"parent_session_id"`
		Outcome         string `json:"outcome"`
	}{}
	for _, r := range rows {
		byID[r.SessionID] = r
	}
	if !byID[child].IsSubagent || byID[child].ParentSessionID != root {
		t.Errorf("child row = %+v, want is_subagent=true parent_session_id=%s", byID[child], root)
	}
	if byID[root].Outcome != "end_turn=true" {
		t.Errorf("root outcome = %q, want end_turn=true", byID[root].Outcome)
	}
}

func TestRun_SessionsSinceFilter(t *testing.T) {
	base, root, _ := sessionsFixture(t)
	var out, errb bytes.Buffer
	if code := run([]string{"sessions", "--since", "1h", "--state-dir", base}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), root) {
		t.Errorf("--since 1h should still include the just-written fixture:\n%s", out.String())
	}
}

func TestRun_SessionsSinceInvalidDuration(t *testing.T) {
	base, _, _ := sessionsFixture(t)
	var out, errb bytes.Buffer
	if code := run([]string{"sessions", "--since", "not-a-duration", "--state-dir", base}, &out, &errb); code == 0 {
		t.Fatalf("invalid --since should not exit 0; stdout=%s", out.String())
	}
	if !strings.Contains(errb.String(), "not-a-duration") {
		t.Errorf("stderr should name the invalid value, got: %s", errb.String())
	}
}

func TestRun_SessionsBucketFilter(t *testing.T) {
	base, root, child := sessionsFixture(t)
	var out, errb bytes.Buffer
	if code := run([]string{"sessions", "--bucket", "project-test-0123456789", "--state-dir", base}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	got := out.String()
	if !strings.Contains(got, root) || !strings.Contains(got, child) {
		t.Errorf("--bucket matching the only bucket should still list both sessions:\n%s", got)
	}
}

func TestRun_SessionsBucketAndAllMutuallyExclusive(t *testing.T) {
	base, _, _ := sessionsFixture(t)
	var out, errb bytes.Buffer
	if code := run([]string{"sessions", "--bucket", "x", "--all", "--state-dir", base}, &out, &errb); code == 0 {
		t.Fatalf("--bucket and --all together should not exit 0; stdout=%s", out.String())
	}
	if !strings.Contains(errb.String(), "mutually exclusive") {
		t.Errorf("stderr should explain the conflict, got: %s", errb.String())
	}
}

func TestRun_SessionsRejectsStraySelector(t *testing.T) {
	base, _, _ := sessionsFixture(t)
	var out, errb bytes.Buffer
	if code := run([]string{"sessions", "some-selector", "--state-dir", base}, &out, &errb); code != 2 {
		t.Errorf("stray positional arg should exit 2, got %d; stderr=%s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "takes no selector") {
		t.Errorf("stderr should explain sessions takes no selector, got: %s", errb.String())
	}
}

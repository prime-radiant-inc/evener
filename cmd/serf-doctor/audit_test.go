package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// auditFixtureRunbookMD is a minimal runbook exercising one mechanical check
// and one manual (prose) CLASSIFY step. The wiring here only needs to prove
// loadRunbook resolves --runbook by name and RunAudit gets driven end to
// end; agent/doctor's audit_test.go covers the block schema and dedup
// semantics exhaustively.
const auditFixtureRunbookMD = `# Runbook: cli-fixture

## CLASSIFY
` + "```" + `yaml
audit:
  - title: "Run-timeout jobs wasting budget"
    severity: high
    category: timeout
    metric: jobs.run_timeout
    op: ">="
    value: 5
` + "```" + `
- Review manually before filing a fix.
`

// withFixtureRunbook substitutes bundledSkills with an in-memory FS carrying
// one runbook, named name, restoring the real bundled assets on cleanup.
func withFixtureRunbook(t *testing.T, name, content string) {
	t.Helper()
	prev := bundledSkills
	bundledSkills = func() fs.FS {
		return fstest.MapFS{
			"doctoring-serf/runbooks/" + name + ".md": &fstest.MapFile{Data: []byte(content)},
		}
	}
	t.Cleanup(func() { bundledSkills = prev })
}

// auditSessionFixture writes one session under a bucket with five
// run_timeout terminal jobs -- enough to trip the fixture runbook's check --
// via raw JSONL (cmd/ cannot import agent/internal/jobstore; see
// sessions_test.go's sessionsFixture for the same constraint).
func auditSessionFixture(t *testing.T) (base, sid string) {
	t.Helper()
	base = t.TempDir()
	sid = "02wLIRxqmq3AUo6vl2OW40"
	bucket := filepath.Join(base, "serf", "projects", "project-test-0123456789")
	sess := filepath.Join(bucket, "sessions")
	if err := os.MkdirAll(filepath.Join(sess, sid), 0o755); err != nil {
		t.Fatal(err)
	}

	mustWrite(t, filepath.Join(sess, sid+".transcript.jsonl"), strings.Join([]string{
		`{"kind":"header","format_version":2,"session_id":"` + sid + `","created_at":"2026-08-01T00:00:00Z","model":"anthropic/claude-a"}`,
		`{"kind":"entry","seq":1,"turn":{"kind":"USER_INPUT","message":{"role":"user","content":[{"kind":"text","text":"go"}]}}}`,
	}, "\n")+"\n")
	mustWrite(t, filepath.Join(sess, sid+".meta.json"), `{"id":"`+sid+`","model":"anthropic/claude-a","turn_count":1}`)

	var jobLines []string
	for i := range 5 {
		id := "job_" + string(rune('a'+i))
		jobLines = append(jobLines,
			`{"kind":"job_started","job_id":"`+id+`","type":"shell","command":"x","owner_session_id":"`+sid+`","visible_to_session_id":"`+sid+`","started_at":"2026-08-01T00:00:00Z"}`,
			`{"kind":"job_finished","job_id":"`+id+`","status":"stopped","reason":"run_timeout","exit_code":-1,"ended_at":"2026-08-01T00:01:00Z","output_bytes":0}`,
		)
	}
	mustWrite(t, filepath.Join(sess, sid, "jobs.jsonl"), strings.Join(jobLines, "\n")+"\n")

	now := time.Now()
	if err := os.Chtimes(filepath.Join(sess, sid+".transcript.jsonl"), now, now); err != nil {
		t.Fatal(err)
	}
	return base, sid
}

func TestRun_AuditHuman(t *testing.T) {
	withFixtureRunbook(t, "cli-fixture", auditFixtureRunbookMD)
	base, sid := auditSessionFixture(t)

	var out, errb bytes.Buffer
	if code := run([]string{"audit", "--runbook", "cli-fixture", "--sessions", sid, "--state-dir", base}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	got := out.String()
	for _, want := range []string{"cli-fixture", "sessions_checked=1", "findings=1", "Run-timeout jobs wasting budget", "manual step", "Review manually", "\"signature\"", "\"suggestedFix\""} {
		if !strings.Contains(got, want) {
			t.Errorf("audit output missing %q:\n%s", want, got)
		}
	}
}

func TestRun_AuditJSON(t *testing.T) {
	withFixtureRunbook(t, "cli-fixture", auditFixtureRunbookMD)
	base, sid := auditSessionFixture(t)

	var out, errb bytes.Buffer
	if code := run([]string{"audit", "--runbook", "cli-fixture", "--sessions", sid, "--json", "--state-dir", base}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	var res struct {
		Runbook         string `json:"runbook"`
		SessionsChecked int    `json:"sessions_checked"`
		Findings        []struct {
			Signature string `json:"signature"`
			Evidence  struct {
				SessionRefs []string `json:"sessionRefs"`
			} `json:"evidence"`
		} `json:"findings"`
		Manual []string `json:"manual"`
	}
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out.String())
	}
	if res.Runbook != "cli-fixture" || res.SessionsChecked != 1 {
		t.Errorf("res = %+v", res)
	}
	if len(res.Findings) != 1 || len(res.Findings[0].Evidence.SessionRefs) != 1 {
		t.Fatalf("Findings = %+v", res.Findings)
	}
	if !strings.Contains(res.Findings[0].Evidence.SessionRefs[0], sid) {
		t.Errorf("finding sessionRefs = %v, want to include %s", res.Findings[0].Evidence.SessionRefs, sid)
	}
	if len(res.Manual) != 1 {
		t.Errorf("Manual = %v, want 1 (never silently skipped)", res.Manual)
	}
}

func TestRun_AuditSinceWindow(t *testing.T) {
	withFixtureRunbook(t, "cli-fixture", auditFixtureRunbookMD)
	base, sid := auditSessionFixture(t)

	var out, errb bytes.Buffer
	if code := run([]string{"audit", "--runbook", "cli-fixture", "--since", "1h", "--state-dir", base}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), sid) && !strings.Contains(out.String(), "findings=1") {
		t.Errorf("--since 1h should reach the just-written fixture session:\n%s", out.String())
	}
}

func TestRun_AuditRequiresRunbook(t *testing.T) {
	base, sid := auditSessionFixture(t)
	var out, errb bytes.Buffer
	if code := run([]string{"audit", "--sessions", sid, "--state-dir", base}, &out, &errb); code == 0 {
		t.Fatalf("missing --runbook should not exit 0; stdout=%s", out.String())
	}
	if !strings.Contains(errb.String(), "--runbook is required") {
		t.Errorf("stderr should explain --runbook is required, got: %s", errb.String())
	}
}

func TestRun_AuditRequiresExactlyOneOfSessionsOrSince(t *testing.T) {
	withFixtureRunbook(t, "cli-fixture", auditFixtureRunbookMD)
	base, sid := auditSessionFixture(t)

	var out, errb bytes.Buffer
	if code := run([]string{"audit", "--runbook", "cli-fixture", "--state-dir", base}, &out, &errb); code == 0 {
		t.Fatalf("neither --sessions nor --since should not exit 0; stdout=%s", out.String())
	}
	if !strings.Contains(errb.String(), "exactly one of --sessions or --since") {
		t.Errorf("stderr should explain, got: %s", errb.String())
	}

	out.Reset()
	errb.Reset()
	if code := run([]string{"audit", "--runbook", "cli-fixture", "--sessions", sid, "--since", "1h", "--state-dir", base}, &out, &errb); code == 0 {
		t.Fatalf("both --sessions and --since should not exit 0; stdout=%s", out.String())
	}
	if !strings.Contains(errb.String(), "exactly one of --sessions or --since") {
		t.Errorf("stderr should explain, got: %s", errb.String())
	}
}

func TestRun_AuditUnknownRunbookErrors(t *testing.T) {
	base, sid := auditSessionFixture(t)
	var out, errb bytes.Buffer
	if code := run([]string{"audit", "--runbook", "does-not-exist", "--sessions", sid, "--state-dir", base}, &out, &errb); code == 0 {
		t.Fatalf("unknown runbook should not exit 0; stdout=%s", out.String())
	}
	if !strings.Contains(errb.String(), "does-not-exist") {
		t.Errorf("stderr should name the runbook, got: %s", errb.String())
	}
}

func TestRun_AuditRejectsStraySelector(t *testing.T) {
	withFixtureRunbook(t, "cli-fixture", auditFixtureRunbookMD)
	base, _ := auditSessionFixture(t)
	var out, errb bytes.Buffer
	if code := run([]string{"audit", "--runbook", "cli-fixture", "--since", "1h", "some-selector", "--state-dir", base}, &out, &errb); code != 2 {
		t.Errorf("stray positional arg should exit 2, got %d; stderr=%s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "takes no selector") {
		t.Errorf("stderr should explain audit takes no selector, got: %s", errb.String())
	}
}

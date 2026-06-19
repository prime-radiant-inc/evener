package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture writes a session state tree with a self-loop watch delivery, using raw
// JSONL. The cmd/ layer cannot import agent/internal/jobstore (the internal wall
// — exactly why agent/doctor is the facade), so this wiring test writes the
// on-disk bytes directly; the fold semantics are covered by agent/doctor tests.
func fixture(t *testing.T) (base, sid string) {
	t.Helper()
	base = t.TempDir()
	sid = "01CMDTESTSESSIONXXXXXXXXXXX"
	bucket := filepath.Join(base, "serf", "projects", "00aa00bb00cc00dd")
	sess := filepath.Join(bucket, "sessions")
	if err := os.MkdirAll(filepath.Join(sess, sid), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(sess, sid+".transcript.jsonl"), `{"kind":"header","session_id":"`+sid+`"}`+"\n")
	mustWrite(t, filepath.Join(sess, sid+".meta.json"), `{"id":"`+sid+`"}`)

	jobs := strings.Join([]string{
		`{"kind":"watch_registered","seq":1,"job_id":"","watch_id":"w1","watch":{"generation":"g1","target":"job:x"}}`,
		// dprior: a genuine prior DELIVERED delivery (its own slot).
		`{"kind":"watch_send_delivered","seq":2,"job_id":"","watch_id":"w1","watch_send":{"key":{"watch_id":"w1","watch_target":"prior"},"delivery_id":"dprior","provenance":{"watch_keys":[{"watch_id":"w1","watch_generation":"g1"}],"chain":[{"kind":"watch","watch_id":"w1","delivery_id":"dprior"}]}}}`,
		// dl: caused by a prior hop of dprior (which delivered) — a genuine self-loop.
		`{"kind":"watch_send_delivered","seq":3,"job_id":"","watch_id":"w1","watch_send":{"key":{"watch_id":"w1","watch_target":"loop"},"delivery_id":"dl","provenance":{"watch_keys":[{"watch_id":"w1","watch_generation":"g1"}],"chain":[{"kind":"watch","watch_id":"w1","delivery_id":"dprior"},{"kind":"watch","watch_id":"w1","delivery_id":"dl"}]}}}`,
	}, "\n") + "\n"
	mustWrite(t, filepath.Join(sess, sid, "jobs.jsonl"), jobs)
	return base, sid
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRun_LocateHuman(t *testing.T) {
	base, sid := fixture(t)
	var out, errb bytes.Buffer
	if code := run([]string{"locate", "--state-dir", base, sid}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	if !strings.Contains(out.String(), filepath.Join("sessions", sid, "jobs.jsonl")) {
		t.Errorf("locate output missing jobs subdir path:\n%s", out.String())
	}
}

func TestRun_LocateJSON(t *testing.T) {
	base, sid := fixture(t)
	var out, errb bytes.Buffer
	if code := run([]string{"locate", "--json", "--state-dir", base, sid}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	var p struct {
		JobsPath string `json:"jobs_path"`
	}
	if err := json.Unmarshal(out.Bytes(), &p); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out.String())
	}
	if !strings.HasSuffix(p.JobsPath, filepath.Join("sessions", sid, "jobs.jsonl")) {
		t.Errorf("jobs_path = %q, want the subdir form", p.JobsPath)
	}
}

func TestRun_WatchesSelfLoop(t *testing.T) {
	base, sid := fixture(t)
	var out, errb bytes.Buffer
	if code := run([]string{"watches", "--state-dir", base, "--self-loops", sid}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	if !strings.Contains(out.String(), "SELF-LOOP") {
		t.Errorf("watches --self-loops should surface the loop:\n%s", out.String())
	}
}

// Flags must parse when they follow the selector — the documented
// `serf-doctor <cmd> <selector> [flags]` form. Go's flag package stops at the
// first non-flag arg, so without the leading-selector peel these are dropped.
func TestRun_FlagsAfterSelector(t *testing.T) {
	base, sid := fixture(t)

	// Both --state-dir and --count follow the selector here.
	var out, errb bytes.Buffer
	if code := run([]string{"transcript", sid, "--state-dir", base, "--count", "communicate"}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	if !strings.Contains(out.String(), "communicate: 0 calls") {
		t.Errorf("--count after the selector was not applied; got:\n%s", out.String())
	}

	out.Reset()
	errb.Reset()
	if code := run([]string{"watches", sid, "--state-dir", base, "--watch", "nonexistent"}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	if !strings.Contains(out.String(), "watch nonexistent not found") {
		t.Errorf("--watch after the selector was not applied; got:\n%s", out.String())
	}
}

func TestRun_UnknownSubcommand(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"frobnicate"}, &out, &errb); code != 2 {
		t.Errorf("unknown subcommand exit = %d, want 2", code)
	}
}

func TestRun_Help(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--help"}, &out, &errb); code != 0 {
		t.Errorf("help exit = %d, want 0", code)
	}
	if !strings.Contains(out.String(), "watches") {
		t.Error("help should list subcommands")
	}
}

func TestRun_NoSelectorErrors(t *testing.T) {
	base, _ := fixture(t)
	var out, errb bytes.Buffer
	if code := run([]string{"locate", "--state-dir", base}, &out, &errb); code != 1 {
		t.Errorf("no selector should exit 1, got %d", code)
	}
}

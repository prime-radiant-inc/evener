package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture writes a session state tree with a runaway-fuse drop, using raw JSONL.
// The cmd/ layer cannot import agent/internal/jobstore (the internal wall —
// exactly why agent/doctor is the facade), so this wiring test writes the on-disk
// bytes directly; the fold semantics are covered by agent/doctor tests.
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
		`{"kind":"watch_registered","seq":1,"job_id":"","watch_id":"w1","watch":{"generation":"g1","owner_session_id":"o","visible_session_id":"v","target":"job:x","config_hash":"h"}}`,
		// A delivered send the runtime stamped self-influenced (bounded, depth 2).
		`{"kind":"watch_send_delivered","seq":2,"job_id":"","watch_id":"w1","watch_send":{"key":{"watch_id":"w1","watch_target":"infl"},"delivery_id":"dl","self_influence_depth":2}}`,
		// dr: the runaway fuse firing — a dropped send rejected at depth 8.
		`{"kind":"watch_send_dropped","seq":3,"job_id":"","watch_id":"w1","watch_send":{"key":{"watch_id":"w1","watch_target":"runaway"},"delivery_id":"dr","self_influence_depth":8,"diagnostic_reason":"runaway"}}`,
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

func TestRun_WatchesRunawayFuse(t *testing.T) {
	base, sid := fixture(t)
	var out, errb bytes.Buffer
	if code := run([]string{"watches", "--state-dir", base, "--self-loops", sid}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	if !strings.Contains(out.String(), "runaway") {
		t.Errorf("watches --self-loops should surface the fired runaway fuse:\n%s", out.String())
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
	got := out.String()
	for _, sub := range []string{"locate", "transcript", "apilog", "watches", "tree"} {
		if !strings.Contains(got, sub) {
			t.Errorf("help should list subcommand %q; got:\n%s", sub, got)
		}
	}
}

func TestRun_NoSelectorErrors(t *testing.T) {
	base, _ := fixture(t)
	var out, errb bytes.Buffer
	if code := run([]string{"locate", "--state-dir", base}, &out, &errb); code != 1 {
		t.Errorf("no selector should exit 1, got %d", code)
	}
}

// fixtureWithAPILogData writes a session state tree with api_call transcript
// lines so that cmdAPILog has data to aggregate and filter.
func fixtureWithAPILogData(t *testing.T) (base, sid string) {
	t.Helper()
	base = t.TempDir()
	sid = "01CMDTESTSESSIONXXXXXXXXXXX"
	bucket := filepath.Join(base, "serf", "projects", "00aa00bb00cc00dd")
	sess := filepath.Join(bucket, "sessions")
	if err := os.MkdirAll(filepath.Join(sess, sid), 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"kind":"header","session_id":"` + sid + `"}`,
		// Normal call with text and one tool call.
		`{"kind":"api_call","seq":2,"round":1,"latency_ms":120,"request":{"model":"gpt-test","provider":"openai","message_count":3,"tool_count":5,"endpoint_family":"chat","history_mode":"full_history"},"response":{"model":"gpt-test","finish_reason":"stop","text_length":42,"tool_call_count":1,"usage":{"input_tokens":1000,"output_tokens":200,"total_tokens":1200,"cache_read_tokens":200,"cache_write_tokens":100}}}`,
		// Empty call (no text, no tools) — used for --empty filter.
		`{"kind":"api_call","seq":3,"round":2,"latency_ms":80,"request":{"model":"gpt-test","provider":"openai","message_count":2,"tool_count":5,"endpoint_family":"chat","history_mode":"full_history"},"response":{"model":"gpt-test","finish_reason":"stop","text_length":0,"tool_call_count":0,"usage":{"input_tokens":500,"output_tokens":0,"total_tokens":500,"cache_read_tokens":100,"cache_write_tokens":50}}}`,
		// Error call — used for --errors filter.
		`{"kind":"api_call","seq":4,"round":3,"latency_ms":200,"request":{"model":"gpt-test","provider":"openai","message_count":2,"tool_count":5,"endpoint_family":"chat","history_mode":"full_history"},"error":"ERROR:"}`,
		// High uncached input call — used for --cache-spikes filter.
		`{"kind":"api_call","seq":5,"round":4,"latency_ms":300,"request":{"model":"gpt-test","provider":"openai","message_count":10,"tool_count":5,"endpoint_family":"chat","history_mode":"full_history"},"response":{"model":"gpt-test","finish_reason":"length","text_length":100,"tool_call_count":2,"usage":{"input_tokens":60000,"output_tokens":500,"total_tokens":60500,"cache_read_tokens":1000,"cache_write_tokens":500}}}`,
	}
	mustWrite(t, filepath.Join(sess, sid+".transcript.jsonl"), strings.Join(lines, "\n")+"\n")
	// Minimal meta.
	mustWrite(t, filepath.Join(sess, sid+".meta.json"), `{"id":"`+sid+`"}`)
	// Minimal jobs (no delegate events needed for apilog).
	mustWrite(t, filepath.Join(sess, sid, "jobs.jsonl"), "")
	return base, sid
}

// treeGrandchildSID is the delegate two hops below the root in
// fixtureWithTreeData; it is only reachable when the depth limit allows a
// second expansion hop.
const treeGrandchildSID = "01CMDGRANDCHILD0000000001"

// fixtureWithTreeData writes a session state tree with delegate_created events
// in jobs.jsonl and observed_by in meta.json so cmdTree has edges to walk. The
// tree is two delegate hops deep (root -> child -> grandchild) so depth limits
// have a visible effect.
func fixtureWithTreeData(t *testing.T) (base, sid string) {
	t.Helper()
	base = t.TempDir()
	sid = "01CMDTESTSESSIONXXXXXXXXXXX"
	childSID := "01CMDCHILD000000000000001"
	grandchildSID := treeGrandchildSID
	observerSID := "01CMDOBSERVER000000000001"
	bucket := filepath.Join(base, "serf", "projects", "00aa00bb00cc00dd")
	sess := filepath.Join(bucket, "sessions")
	if err := os.MkdirAll(filepath.Join(sess, sid), 0o755); err != nil {
		t.Fatal(err)
	}
	// Create a child session directory so the tree can resolve it.
	if err := os.MkdirAll(filepath.Join(sess, childSID), 0o755); err != nil {
		t.Fatal(err)
	}
	// Create a grandchild session directory (delegate under the child).
	if err := os.MkdirAll(filepath.Join(sess, grandchildSID), 0o755); err != nil {
		t.Fatal(err)
	}
	// Header + one api_call for the root transcript.
	rootLines := []string{
		`{"kind":"header","session_id":"` + sid + `"}`,
		`{"kind":"api_call","seq":2,"round":1,"latency_ms":100,"request":{"model":"gpt-test","provider":"openai","message_count":2,"tool_count":5},"response":{"model":"gpt-test","finish_reason":"stop","text_length":10,"tool_call_count":0,"usage":{"input_tokens":100,"output_tokens":50,"total_tokens":150}}}`,
	}
	mustWrite(t, filepath.Join(sess, sid+".transcript.jsonl"), strings.Join(rootLines, "\n")+"\n")
	// Child transcript (minimal).
	mustWrite(t, filepath.Join(sess, childSID+".transcript.jsonl"),
		`{"kind":"header","session_id":"`+childSID+`"}`+"\n")
	// Grandchild transcript (minimal).
	mustWrite(t, filepath.Join(sess, grandchildSID+".transcript.jsonl"),
		`{"kind":"header","session_id":"`+grandchildSID+`"}`+"\n")
	// Root meta with observed_by for observer edges.
	mustWrite(t, filepath.Join(sess, sid+".meta.json"),
		`{"id":"`+sid+`","observed_by":["`+observerSID+`"]}`)
	// Child meta (minimal).
	mustWrite(t, filepath.Join(sess, childSID+".meta.json"), `{"id":"`+childSID+`"}`)
	// Grandchild meta (minimal).
	mustWrite(t, filepath.Join(sess, grandchildSID+".meta.json"), `{"id":"`+grandchildSID+`"}`)
	// Jobs with a delegate_created event linking root -> child.
	jobs := strings.Join([]string{
		`{"kind":"delegate_created","seq":1,"delegate_id":"d1","delegate":{"child_session_id":"` + childSID + `","transcript_ref":"` + childSID + `","agent_type":"test-agent","owner_session_id":"` + sid + `","resumable":true}}`,
	}, "\n") + "\n"
	mustWrite(t, filepath.Join(sess, sid, "jobs.jsonl"), jobs)
	// Child jobs with a delegate_created event linking child -> grandchild, so
	// the tree has a second hop that depth limits can elide.
	childJobs := strings.Join([]string{
		`{"kind":"delegate_created","seq":1,"delegate_id":"d2","delegate":{"child_session_id":"` + grandchildSID + `","transcript_ref":"` + grandchildSID + `","agent_type":"test-agent","owner_session_id":"` + childSID + `","resumable":true}}`,
	}, "\n") + "\n"
	mustWrite(t, filepath.Join(sess, childSID, "jobs.jsonl"), childJobs)
	return base, sid
}

func TestRun_APILogHuman(t *testing.T) {
	base, sid := fixtureWithAPILogData(t)
	var out, errb bytes.Buffer
	if code := run([]string{"apilog", "--state-dir", base, sid}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	outStr := out.String()
	if !strings.Contains(outStr, "session") {
		t.Errorf("apilog output missing session line:\n%s", outStr)
	}
	if !strings.Contains(outStr, "calls=4") {
		t.Errorf("apilog output missing totals; got:\n%s", outStr)
	}
}

func TestRun_APILogJSON(t *testing.T) {
	base, sid := fixtureWithAPILogData(t)
	var out, errb bytes.Buffer
	if code := run([]string{"apilog", "--json", "--state-dir", base, sid}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	var res struct {
		SessionID string `json:"session_id"`
		Totals    struct {
			Calls int `json:"calls"`
		} `json:"totals"`
	}
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out.String())
	}
	if res.SessionID != sid {
		t.Errorf("session_id = %q, want %q", res.SessionID, sid)
	}
	if res.Totals.Calls != 4 {
		t.Errorf("totals.calls = %d, want 4", res.Totals.Calls)
	}
}

func TestRun_APILogFlags(t *testing.T) {
	base, sid := fixtureWithAPILogData(t)

	// Each row in the fixture carries a marker that appears only in that row's
	// rendered table line (never in the always-unfiltered totals block): the
	// normal round-1 call shows txt length "42", the empty round-2 call shows
	// "(empty)", the error round-3 call shows "ERROR:", and the cache-spike
	// round-4 call shows uncached input "59000". Each filter must render its own
	// row's marker and exclude every other row's marker; a no-op filter that
	// rendered all four rows would leak the others.
	t.Run("empty", func(t *testing.T) {
		var out, errb bytes.Buffer
		if code := run([]string{"apilog", "--state-dir", base, "--empty", sid}, &out, &errb); code != 0 {
			t.Fatalf("exit %d, stderr=%s", code, errb.String())
		}
		got := out.String()
		if !strings.Contains(got, "(empty)") {
			t.Errorf("--empty should surface round 2; got:\n%s", got)
		}
		if strings.Contains(got, "42") {
			t.Errorf("--empty must exclude the normal round-1 call (txt=42); got:\n%s", got)
		}
		if strings.Contains(got, "ERROR:") {
			t.Errorf("--empty must exclude the error round-3 call; got:\n%s", got)
		}
		if strings.Contains(got, "59000") {
			t.Errorf("--empty must exclude the cache-spike round-4 call; got:\n%s", got)
		}
	})

	t.Run("errors", func(t *testing.T) {
		var out, errb bytes.Buffer
		if code := run([]string{"apilog", "--state-dir", base, "--errors", sid}, &out, &errb); code != 0 {
			t.Fatalf("exit %d, stderr=%s", code, errb.String())
		}
		got := out.String()
		if !strings.Contains(got, "ERROR:") {
			t.Errorf("--errors should surface rate limit error; got:\n%s", got)
		}
		if strings.Contains(got, "42") {
			t.Errorf("--errors must exclude the normal round-1 call (txt=42); got:\n%s", got)
		}
		if strings.Contains(got, "(empty)") {
			t.Errorf("--errors must exclude the empty round-2 call; got:\n%s", got)
		}
		if strings.Contains(got, "59000") {
			t.Errorf("--errors must exclude the cache-spike round-4 call; got:\n%s", got)
		}
	})

	t.Run("cache-spikes", func(t *testing.T) {
		var out, errb bytes.Buffer
		if code := run([]string{"apilog", "--state-dir", base, "--cache-spikes", sid}, &out, &errb); code != 0 {
			t.Fatalf("exit %d, stderr=%s", code, errb.String())
		}
		got := out.String()
		if !strings.Contains(got, "59000") {
			t.Errorf("--cache-spikes should surface round 4; got:\n%s", got)
		}
		if strings.Contains(got, "42") {
			t.Errorf("--cache-spikes must exclude the normal round-1 call (txt=42); got:\n%s", got)
		}
		if strings.Contains(got, "(empty)") {
			t.Errorf("--cache-spikes must exclude the empty round-2 call; got:\n%s", got)
		}
		if strings.Contains(got, "ERROR:") {
			t.Errorf("--cache-spikes must exclude the error round-3 call; got:\n%s", got)
		}
	})

	t.Run("summary", func(t *testing.T) {
		// The per-call table emits a header column "uncached" and per-call model
		// rows ("gpt-test"). The summary view must omit the whole table.
		var full, fullErr bytes.Buffer
		if code := run([]string{"apilog", "--state-dir", base, sid}, &full, &fullErr); code != 0 {
			t.Fatalf("full run exit %d, stderr=%s", code, fullErr.String())
		}
		if !strings.Contains(full.String(), "uncached") {
			t.Fatalf("full apilog should render the per-call table header; got:\n%s", full.String())
		}

		var out, errb bytes.Buffer
		if code := run([]string{"apilog", "--state-dir", base, "--summary", sid}, &out, &errb); code != 0 {
			t.Fatalf("exit %d, stderr=%s", code, errb.String())
		}
		if strings.Contains(out.String(), "uncached") {
			t.Errorf("--summary should omit the per-call table header; got:\n%s", out.String())
		}
		if strings.Contains(out.String(), "gpt-test") {
			t.Errorf("--summary should not list per-call model rows; got:\n%s", out.String())
		}
		if !strings.Contains(out.String(), "calls=4") {
			t.Errorf("--summary must still include the aggregate totals line; got:\n%s", out.String())
		}
	})
}

func TestRun_APILogNoSelector(t *testing.T) {
	base, _ := fixtureWithAPILogData(t)
	var out, errb bytes.Buffer
	if code := run([]string{"apilog", "--state-dir", base}, &out, &errb); code != 1 {
		t.Errorf("no selector should exit 1, got %d", code)
	}
}

func TestRun_TreeHuman(t *testing.T) {
	base, sid := fixtureWithTreeData(t)
	var out, errb bytes.Buffer
	if code := run([]string{"tree", "--state-dir", base, sid}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	outStr := out.String()
	if !strings.Contains(outStr, sid) {
		t.Errorf("tree output missing root session id:\n%s", outStr)
	}
	if !strings.Contains(outStr, "delegate") {
		t.Errorf("tree output missing delegate edge:\n%s", outStr)
	}
}

func TestRun_TreeJSON(t *testing.T) {
	base, sid := fixtureWithTreeData(t)
	var out, errb bytes.Buffer
	if code := run([]string{"tree", "--json", "--state-dir", base, sid}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	var root struct {
		SessionID string `json:"session_id"`
		Children  []struct {
			Edge string `json:"edge"`
		} `json:"children"`
	}
	if err := json.Unmarshal(out.Bytes(), &root); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out.String())
	}
	if root.SessionID != sid {
		t.Errorf("session_id = %q, want %q", root.SessionID, sid)
	}
	hasDelegate := false
	for _, c := range root.Children {
		if c.Edge == "delegate" {
			hasDelegate = true
		}
	}
	if !hasDelegate {
		t.Errorf("no delegate child in tree; got:\n%s", out.String())
	}
}

func TestRun_TreeDepthAndObservers(t *testing.T) {
	base, sid := fixtureWithTreeData(t)

	t.Run("depth", func(t *testing.T) {
		// At --depth=1 the child is shown but its grandchild is elided, with a
		// depth-limit note marking the truncation.
		var shallow, shallowErr bytes.Buffer
		if code := run([]string{"tree", "--state-dir", base, "--depth", "1", sid}, &shallow, &shallowErr); code != 0 {
			t.Fatalf("exit %d, stderr=%s", code, shallowErr.String())
		}
		if !strings.Contains(shallow.String(), "depth limit (children not expanded)") {
			t.Errorf("--depth=1 should note the elided grandchild; got:\n%s", shallow.String())
		}
		if strings.Contains(shallow.String(), treeGrandchildSID) {
			t.Errorf("--depth=1 should not expand to the grandchild; got:\n%s", shallow.String())
		}

		// At --depth=2 the grandchild is reached and printed.
		var deep, deepErr bytes.Buffer
		if code := run([]string{"tree", "--state-dir", base, "--depth", "2", sid}, &deep, &deepErr); code != 0 {
			t.Fatalf("exit %d, stderr=%s", code, deepErr.String())
		}
		if !strings.Contains(deep.String(), treeGrandchildSID) {
			t.Errorf("--depth=2 should expand to the grandchild; got:\n%s", deep.String())
		}
	})

	t.Run("observers", func(t *testing.T) {
		var out, errb bytes.Buffer
		if code := run([]string{"tree", "--state-dir", base, "--observers", sid}, &out, &errb); code != 0 {
			t.Fatalf("exit %d, stderr=%s", code, errb.String())
		}
		if !strings.Contains(out.String(), "observer") {
			t.Errorf("--observers should show observer edge; got:\n%s", out.String())
		}
	})
}

func TestRun_TreeNoSelector(t *testing.T) {
	base, _ := fixtureWithTreeData(t)
	var out, errb bytes.Buffer
	if code := run([]string{"tree", "--state-dir", base}, &out, &errb); code != 1 {
		t.Errorf("no selector should exit 1, got %d", code)
	}
}

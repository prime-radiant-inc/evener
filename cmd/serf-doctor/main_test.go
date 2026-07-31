package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/internal/plugins"
	"primeradiant.com/serf/llm/apilog"
)

// fixture writes a session state tree with a runaway-fuse drop, using raw JSONL.
// The cmd/ layer cannot import agent/internal/jobstore (the internal wall —
// exactly why agent/doctor is the facade), so this wiring test writes the on-disk
// bytes directly; the fold semantics are covered by agent/doctor tests.
func fixture(t *testing.T) (base, sid string) {
	t.Helper()
	base = t.TempDir()
	sid = "02wLIRxqmq3AUo6vl2OW37"
	bucket := filepath.Join(base, "serf", "projects", "project-test-0123456789")
	sess := filepath.Join(bucket, "sessions")
	if err := os.MkdirAll(filepath.Join(sess, sid), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(sess, sid+".transcript.jsonl"), `{"kind":"header","format_version":2,"session_id":"`+sid+`"}`+"\n")
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
	if !strings.Contains(out.String(), filepath.Join("sessions", sid+".api.jsonl")) {
		t.Errorf("locate output missing canonical API log path:\n%s", out.String())
	}
}

func TestRun_LocateJSON(t *testing.T) {
	base, sid := fixture(t)
	var out, errb bytes.Buffer
	if code := run([]string{"locate", "--json", "--state-dir", base, sid}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	var p struct {
		JobsPath   string `json:"jobs_path"`
		APILogPath string `json:"api_log_path"`
	}
	if err := json.Unmarshal(out.Bytes(), &p); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out.String())
	}
	if !strings.HasSuffix(p.JobsPath, filepath.Join("sessions", sid, "jobs.jsonl")) {
		t.Errorf("jobs_path = %q, want the subdir form", p.JobsPath)
	}
	if !strings.HasSuffix(p.APILogPath, filepath.Join("sessions", sid+".api.jsonl")) {
		t.Errorf("api_log_path = %q, want the sibling canonical API log", p.APILogPath)
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
	// Match the SUBCOMMANDS block's own line shape ("  <name>  <description>"),
	// not a bare substring: "jobs" and "watches" also appear in the prose above it.
	for _, sub := range []string{"locate", "transcript", "apilog", "jobs", "watches", "tree", "plugins"} {
		if !strings.Contains(got, "\n  "+sub+" ") {
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

// fixtureWithAPILogData writes canonical attempts and settlements beside a
// semantic transcript so cmdAPILog can aggregate and filter them.
const (
	normalAPIAttemptID     = "att_02wMz5TxuyedWwYBSOAa00"
	emptyAPIAttemptID      = "att_02wMz5TxuyedWwYBSOAa42"
	errorAPIAttemptID      = "att_02wMz5TxuyedWwYBSOAa01"
	cacheSpikeAPIAttemptID = "att_02wMz5TxuyedWwYBSOAa02"
)

func fixtureWithAPILogData(t *testing.T) (base, sid string) {
	t.Helper()
	base = t.TempDir()
	sid = "02wLIRxqmq3AUo6vl2OW37"
	bucket := filepath.Join(base, "serf", "projects", "project-test-0123456789")
	sess := filepath.Join(bucket, "sessions")
	if err := os.MkdirAll(filepath.Join(sess, sid), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(sess, sid+".transcript.jsonl"), `{"kind":"header","format_version":2,"session_id":"`+sid+`"}`+"\n")

	attempts := []apilog.APIAttemptRecord{
		commandAPIAttempt(1, apilog.AttemptSuccess, 120, 1000, 200, 200, 42, 1),
		commandAPIAttempt(2, apilog.AttemptSuccess, 80, 500, 0, 100, 0, 0),
		commandAPIAttempt(3, apilog.AttemptProviderTimeout, 200, 0, 0, 0, 0, 0),
		commandAPIAttempt(4, apilog.AttemptSuccess, 300, 60000, 500, 1000, 100, 2),
	}
	// Keep the attempt IDs stable so filter assertions can identify table rows.
	// The empty row intentionally contains the normal row's text-length marker;
	// unrelated identity fields must not be mistaken for filtered-out row data.
	attempts[0].AttemptID = normalAPIAttemptID
	attempts[1].AttemptID = emptyAPIAttemptID
	attempts[2].AttemptID = errorAPIAttemptID
	attempts[3].AttemptID = cacheSpikeAPIAttemptID
	var lines []string
	for _, attempt := range attempts {
		attemptLine, err := json.Marshal(attempt)
		if err != nil {
			t.Fatal(err)
		}
		settlement := apilog.APIAttemptGroupSettlement{
			Kind:              "attempt_group_settlement",
			SchemaVersion:     1,
			AttemptGroupID:    attempt.AttemptGroupID,
			FinalAttemptID:    attempt.AttemptID,
			FinalAttemptCount: 1,
			Outcome:           attempt.Outcome,
			SettledAt:         attempt.Timestamp.Add(time.Second),
		}
		settlementLine, err := json.Marshal(settlement)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(attemptLine), string(settlementLine))
	}
	mustWrite(t, filepath.Join(sess, sid+".api.jsonl"), strings.Join(lines, "\n")+"\n")
	mustWrite(t, filepath.Join(sess, sid+".meta.json"), `{"id":"`+sid+`"}`)
	mustWrite(t, filepath.Join(sess, sid, "jobs.jsonl"), "")
	return base, sid
}

func commandIntPointer(value int) *int { return &value }

func commandAPIAttempt(index int, outcome apilog.AttemptOutcomeClass, latency int64, input, output, cacheRead, textLength, toolCalls int) apilog.APIAttemptRecord {
	attempt := apilog.APIAttemptRecord{
		Kind:             "api_attempt",
		SchemaVersion:    1,
		AttemptID:        identifier.MustNewAPIAttemptID(),
		AttemptGroupID:   fmt.Sprintf("ag_command_%d", index),
		AttemptIndex:     1,
		Timestamp:        time.Unix(int64(index), 0).UTC(),
		LatencyMS:        latency,
		ProviderInstance: "openai",
		RequestModel:     "gpt-test",
		Request: apilog.APIAttemptRequest{
			Method:         "POST",
			Endpoint:       "https://provider.test/v1/responses",
			Body:           apilog.EncodeBody([]byte("{}")),
			Model:          "gpt-test",
			EndpointFamily: "chat",
			HistoryMode:    "full_history",
		},
		Outcome: outcome,
	}
	if outcome == apilog.AttemptSuccess {
		attempt.Response = &apilog.APIAttemptResponse{
			StatusCode:    commandIntPointer(200),
			Body:          apilog.EncodeBody([]byte("{}")),
			Model:         "gpt-test",
			FinishReason:  "stop",
			TextLength:    commandIntPointer(textLength),
			ToolCallCount: commandIntPointer(toolCalls),
			Usage: apilog.Usage{
				InputTokens:     commandIntPointer(input),
				OutputTokens:    commandIntPointer(output),
				TotalTokens:     commandIntPointer(input + output),
				CacheReadTokens: &cacheRead,
			},
		}
	} else {
		attempt.ErrorClass = "timeout"
		attempt.ErrorMessage = "provider-body-sentinel: quota detail"
	}
	return attempt
}

func requireAPILogTableRow(t *testing.T, output, wantAttemptID, wantMarker string) {
	t.Helper()
	var rows []string
	for line := range strings.SplitSeq(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && identifier.ValidateAPIAttemptID(fields[0]) == nil {
			rows = append(rows, line)
		}
	}
	if len(rows) != 1 {
		t.Fatalf("rendered API log table has %d rows, want 1:\n%s", len(rows), output)
	}
	fields := strings.Fields(rows[0])
	if fields[0] != wantAttemptID {
		t.Errorf("rendered API log attempt = %q, want %q; row:\n%s", fields[0], wantAttemptID, rows[0])
	}
	if !strings.Contains(rows[0], wantMarker) {
		t.Errorf("rendered API log row missing %q:\n%s", wantMarker, rows[0])
	}
}

func requireAPILogTableColumn(t *testing.T, output, attemptID, column, want string) {
	t.Helper()
	lines := strings.Split(output, "\n")
	var header, row string
	for _, line := range lines {
		if strings.HasPrefix(line, "attempt_id ") {
			header = line
		}
		if strings.HasPrefix(line, attemptID+" ") {
			row = line
		}
	}
	if header == "" || row == "" {
		t.Fatalf("API-log table missing header or row for %s:\n%s", attemptID, output)
	}
	start := strings.Index(header, column)
	if start < 0 {
		t.Fatalf("API-log table has no %q column:\n%s", column, output)
	}
	end := len(row)
	for field := range strings.FieldsSeq(header) {
		position := strings.Index(header, field)
		if position > start && position < end {
			end = position
		}
	}
	if start >= len(row) {
		t.Fatalf("API-log row ends before %q column:\n%s", column, output)
	}
	if end > len(row) {
		end = len(row)
	}
	if got := strings.TrimSpace(row[start:end]); got != want {
		t.Fatalf("API-log %s column = %q, want %q:\n%s", column, got, want, output)
	}
}

// treeGrandchildSID is the delegate two hops below the root in
// fixtureWithTreeData; it is only reachable when the depth limit allows a
// second expansion hop.
const treeGrandchildSID = "02wLIRxqmq3AUo6vl2OW38"

// fixtureWithTreeData writes a session state tree with delegate_created events
// in jobs.jsonl and observed_by in meta.json so cmdTree has edges to walk. The
// tree is two delegate hops deep (root -> child -> grandchild) so depth limits
// have a visible effect.
func fixtureWithTreeData(t *testing.T) (base, sid string) {
	t.Helper()
	base = t.TempDir()
	sid = "02wLIRxqmq3AUo6vl2OW37"
	childSID := "02wLIRxqmq3AUo6vl2OW39"
	grandchildSID := treeGrandchildSID
	observerSID := "02wLIRxqmq3AUo6vl2OW3A"
	bucket := filepath.Join(base, "serf", "projects", "project-test-0123456789")
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
	mustWrite(t, filepath.Join(sess, sid+".transcript.jsonl"),
		`{"kind":"header","format_version":2,"session_id":"`+sid+`"}`+"\n")
	// Child transcript (minimal).
	mustWrite(t, filepath.Join(sess, childSID+".transcript.jsonl"),
		`{"kind":"header","format_version":2,"session_id":"`+childSID+`"}`+"\n")
	// Grandchild transcript (minimal).
	mustWrite(t, filepath.Join(sess, grandchildSID+".transcript.jsonl"),
		`{"kind":"header","format_version":2,"session_id":"`+grandchildSID+`"}`+"\n")
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
	if !strings.Contains(outStr, "settlements=4/4") {
		t.Errorf("apilog output missing settlement truth; got:\n%s", outStr)
	}
	if strings.Contains(outStr, "provider-body-sentinel") || strings.Contains(outStr, "quota detail") {
		t.Errorf("apilog human output exposed provider body-derived error text:\n%s", outStr)
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
		Calls     []struct {
			Outcome    apilog.AttemptOutcomeClass `json:"outcome"`
			ErrorClass string                     `json:"error_class"`
			StatusCode int                        `json:"status_code"`
		} `json:"calls"`
		Settlements struct {
			Total     int  `json:"total"`
			Truncated bool `json:"truncated"`
			Records   []struct {
				AttemptGroupID     string `json:"attempt_group_id"`
				ForensicIncomplete bool   `json:"forensic_incomplete"`
			} `json:"records"`
		} `json:"settlements"`
		Totals struct {
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
	if res.Settlements.Total != 4 || res.Settlements.Truncated || len(res.Settlements.Records) != 4 {
		t.Errorf("settlements = %+v, want four complete records", res.Settlements)
	}
	if strings.Contains(out.String(), "provider-body-sentinel") || strings.Contains(out.String(), "quota detail") {
		t.Errorf("apilog JSON exposed provider body-derived error text:\n%s", out.String())
	}
}

func TestRun_APILogFlags(t *testing.T) {
	base, sid := fixtureWithAPILogData(t)

	t.Run("empty", func(t *testing.T) {
		var out, errb bytes.Buffer
		if code := run([]string{"apilog", "--state-dir", base, "--empty", sid}, &out, &errb); code != 0 {
			t.Fatalf("exit %d, stderr=%s", code, errb.String())
		}
		requireAPILogTableColumn(t, out.String(), emptyAPIAttemptID, "empty", "true")
	})

	t.Run("errors", func(t *testing.T) {
		var out, errb bytes.Buffer
		if code := run([]string{"apilog", "--state-dir", base, "--errors", sid}, &out, &errb); code != 0 {
			t.Fatalf("exit %d, stderr=%s", code, errb.String())
		}
		requireAPILogTableColumn(t, out.String(), errorAPIAttemptID, "outcome", string(apilog.AttemptProviderTimeout))
		requireAPILogTableColumn(t, out.String(), errorAPIAttemptID, "error_class", "timeout")
		if strings.Contains(out.String(), "provider-body-sentinel") || strings.Contains(out.String(), "quota detail") {
			t.Fatalf("--errors output exposed provider body-derived error text:\n%s", out.String())
		}
	})

	t.Run("cache-spikes", func(t *testing.T) {
		var out, errb bytes.Buffer
		if code := run([]string{"apilog", "--state-dir", base, "--cache-spikes", sid}, &out, &errb); code != 0 {
			t.Fatalf("exit %d, stderr=%s", code, errb.String())
		}
		requireAPILogTableRow(t, out.String(), cacheSpikeAPIAttemptID, "60000")
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

// fixtureWithCorruptAPILogData writes a session whose API log has one valid
// attempt followed by one corrupt raw line, for exercising `apilog
// --validate`'s problem-reporting and nonzero-exit path.
func fixtureWithCorruptAPILogData(t *testing.T) (base, sid string) {
	t.Helper()
	base = t.TempDir()
	sid = "02wLIRxqmq3AUo6vl2OW38"
	bucket := filepath.Join(base, "serf", "projects", "project-test-0123456789")
	sess := filepath.Join(bucket, "sessions")
	if err := os.MkdirAll(filepath.Join(sess, sid), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(sess, sid+".transcript.jsonl"), `{"kind":"header","format_version":2,"session_id":"`+sid+`"}`+"\n")
	mustWrite(t, filepath.Join(sess, sid+".meta.json"), `{"id":"`+sid+`"}`)
	mustWrite(t, filepath.Join(sess, sid, "jobs.jsonl"), "")

	attempt := commandAPIAttempt(1, apilog.AttemptSuccess, 100, 10, 5, 0, 1, 0)
	attemptLine, err := json.Marshal(attempt)
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(sess, sid+".api.jsonl"), string(attemptLine)+"\n"+"{bad json}\n")
	return base, sid
}

func TestRun_APILogValidateHuman(t *testing.T) {
	base, sid := fixtureWithAPILogData(t)
	var out, errb bytes.Buffer
	if code := run([]string{"apilog", "--state-dir", base, "--validate", sid}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	outStr := out.String()
	if !strings.Contains(outStr, "records_ok=8") {
		t.Errorf("validate output missing records_ok=8 (4 attempts + 4 settlements); got:\n%s", outStr)
	}
	if !strings.Contains(outStr, "clean: every complete record decoded through EOF") {
		t.Errorf("validate output missing clean marker; got:\n%s", outStr)
	}
}

func TestRun_APILogValidateJSON(t *testing.T) {
	base, sid := fixtureWithAPILogData(t)
	var out, errb bytes.Buffer
	if code := run([]string{"apilog", "--json", "--state-dir", base, "--validate", sid}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	var res struct {
		SessionID    string `json:"session_id"`
		RecordsOK    int    `json:"records_ok"`
		Problems     []any  `json:"problems"`
		ProblemCount int    `json:"problem_count"`
		Clean        bool   `json:"clean"`
	}
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out.String())
	}
	if res.SessionID != sid || res.RecordsOK != 8 || !res.Clean || res.ProblemCount != 0 {
		t.Errorf("validate json = %+v", res)
	}
}

// TestRun_APILogValidateReportsProblemsAndNonzeroExit is the load-bearing CLI
// test: --validate is the first serf-doctor subcommand whose exit code
// signals "findings", not just "the tool ran" (recorded in kata 7x84's
// exit-code decision).
func TestRun_APILogValidateReportsProblemsAndNonzeroExit(t *testing.T) {
	base, sid := fixtureWithCorruptAPILogData(t)
	var out, errb bytes.Buffer
	code := run([]string{"apilog", "--state-dir", base, "--validate", sid}, &out, &errb)
	if code != 1 {
		t.Fatalf("exit = %d, want 1 (structural problems found); stdout=%s stderr=%s", code, out.String(), errb.String())
	}
	outStr := out.String()
	if !strings.Contains(outStr, "not clean") {
		t.Errorf("validate output missing not-clean marker; got:\n%s", outStr)
	}
	if !strings.Contains(outStr, "offset") {
		t.Errorf("validate output missing offset column; got:\n%s", outStr)
	}
}

func TestRun_APILogValidateNoSelector(t *testing.T) {
	base, _ := fixtureWithAPILogData(t)
	var out, errb bytes.Buffer
	if code := run([]string{"apilog", "--state-dir", base, "--validate"}, &out, &errb); code != 1 {
		t.Errorf("no selector should exit 1, got %d", code)
	}
}

// fixtureWithJobsData writes a session whose jobs.jsonl records one completed
// job and one the run timeout stopped with no output — the shape the 2026-07-31
// diagnosis could only reach through the live daemon's /status endpoint.
const (
	commandCompletedJobID = "job_02wLIRxqmq3AUo6vl2OW40"
	commandTimeoutJobID   = "job_02wLIRxqmq3AUo6vl2OW41"
)

func fixtureWithJobsData(t *testing.T) (base, sid string) {
	t.Helper()
	base = t.TempDir()
	sid = "02wLIRxqmq3AUo6vl2OW37"
	bucket := filepath.Join(base, "serf", "projects", "project-test-0123456789")
	sess := filepath.Join(bucket, "sessions")
	if err := os.MkdirAll(filepath.Join(sess, sid), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(sess, sid+".transcript.jsonl"), `{"kind":"header","format_version":2,"session_id":"`+sid+`"}`+"\n")
	mustWrite(t, filepath.Join(sess, sid+".meta.json"), `{"id":"`+sid+`"}`)

	jobs := strings.Join([]string{
		`{"kind":"job_started","seq":1,"job_id":"` + commandCompletedJobID + `","type":"shell","command":"make test","started_at":"2026-07-31T18:00:00Z"}`,
		`{"kind":"job_finished","seq":2,"job_id":"` + commandCompletedJobID + `","status":"completed","exit_code":0,"ended_at":"2026-07-31T18:01:00Z","output_bytes":4096}`,
		`{"kind":"job_started","seq":3,"job_id":"` + commandTimeoutJobID + `","type":"shell","command":"npm run dev","started_at":"2026-07-31T18:00:00Z"}`,
		`{"kind":"job_finished","seq":4,"job_id":"` + commandTimeoutJobID + `","status":"stopped","reason":"run_timeout","exit_code":-1,"ended_at":"2026-07-31T18:02:00Z","output_bytes":0}`,
		// A watch on the stopped job, ended unfired: the row whose target state
		// is the answer to "why didn't my watch fire".
		`{"kind":"watch_registered","seq":5,"job_id":"","watch_id":"w1","watch":{"generation":"g1","owner_session_id":"` + sid + `","visible_session_id":"` + sid + `","target":"` + commandTimeoutJobID + `","send_to":"caller","condition":"output_match:ready","config_hash":"h"}}`,
		`{"kind":"watch_cleared","seq":6,"job_id":"","watch_id":"w1","watch":{"generation":"g1","end_reason":"auto_removed_terminal"}}`,
	}, "\n") + "\n"
	mustWrite(t, filepath.Join(sess, sid, "jobs.jsonl"), jobs)
	return base, sid
}

func TestRun_JobsHuman(t *testing.T) {
	base, sid := fixtureWithJobsData(t)
	var out, errb bytes.Buffer
	if code := run([]string{"jobs", "--state-dir", base, sid}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	got := out.String()
	for _, want := range []string{
		"job " + commandCompletedJobID + "  (completed)",
		"job " + commandTimeoutJobID + "  (stopped: run_timeout)",
		"exit=-1",
		"output_bytes=0",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("jobs output missing %q:\n%s", want, got)
		}
	}
}

func TestRun_JobsJSON(t *testing.T) {
	base, sid := fixtureWithJobsData(t)
	var out, errb bytes.Buffer
	if code := run([]string{"jobs", "--json", "--state-dir", base, sid}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	var res struct {
		SessionID string `json:"session_id"`
		Jobs      []struct {
			JobID       string `json:"job_id"`
			Status      string `json:"status"`
			Reason      string `json:"reason"`
			ExitCode    *int   `json:"exit_code"`
			OutputBytes int64  `json:"output_bytes"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out.String())
	}
	if res.SessionID != sid {
		t.Errorf("session_id = %q, want %q", res.SessionID, sid)
	}
	if len(res.Jobs) != 2 {
		t.Fatalf("jobs = %d, want 2:\n%s", len(res.Jobs), out.String())
	}
	stopped := res.Jobs[1]
	if stopped.JobID != commandTimeoutJobID || stopped.Status != "stopped" || stopped.Reason != "run_timeout" {
		t.Errorf("stopped job = %+v", stopped)
	}
	if stopped.ExitCode == nil || *stopped.ExitCode != -1 || stopped.OutputBytes != 0 {
		t.Errorf("stopped job exit/output = %v/%d, want -1/0", stopped.ExitCode, stopped.OutputBytes)
	}
}

// --job must parse after the selector, the documented flag position.
func TestRun_JobsJobFilterAfterSelector(t *testing.T) {
	base, sid := fixtureWithJobsData(t)
	var out, errb bytes.Buffer
	if code := run([]string{"jobs", sid, "--state-dir", base, "--job", commandTimeoutJobID}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	if strings.Contains(out.String(), commandCompletedJobID) {
		t.Errorf("--job should scope to one job; got:\n%s", out.String())
	}
	if !strings.Contains(out.String(), commandTimeoutJobID) {
		t.Errorf("--job should render the named job; got:\n%s", out.String())
	}

	out.Reset()
	errb.Reset()
	if code := run([]string{"jobs", sid, "--state-dir", base, "--job", "job_nonexistent"}, &out, &errb); code != 0 {
		t.Fatal(errb.String())
	}
	if !strings.Contains(out.String(), "job job_nonexistent not found") {
		t.Errorf("unmatched --job should say so; got:\n%s", out.String())
	}
}

func TestRun_JobsNoSelector(t *testing.T) {
	base, _ := fixtureWithJobsData(t)
	var out, errb bytes.Buffer
	if code := run([]string{"jobs", "--state-dir", base}, &out, &errb); code != 1 {
		t.Errorf("no selector should exit 1, got %d", code)
	}
}

// The watch row must carry its target job's state all the way out to stdout:
// ended unfired, zero deliveries, target already stopped with no output.
func TestRun_WatchesShowsTargetJobState(t *testing.T) {
	base, sid := fixtureWithJobsData(t)
	var out, errb bytes.Buffer
	if code := run([]string{"watches", "--state-dir", base, sid}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	got := out.String()
	for _, want := range []string{
		"(ended: auto_removed_terminal)",
		"target job: status=stopped  reason=run_timeout  exit=-1  output_bytes=0",
		"deliveries: 0 distinct",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("watches output missing %q:\n%s", want, got)
		}
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

// pluginsFixture writes a plugin store (installed_plugins.json only — the
// registry format is a stable on-disk contract, §5.2 of the design spec) with
// one healthy, in-place plugin and one orphaned registry entry (install path
// never created), so cmdPlugins has both an OK and a FAIL to render.
func pluginsFixture(t *testing.T) (root string) {
	t.Helper()
	root = t.TempDir()
	dir := filepath.Join(t.TempDir(), "widget")
	if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"),
		[]byte(`{"name":"widget","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := plugins.Registry{Plugins: map[string][]plugins.InstallEntry{
		"widget@acme": {{
			InstallPath: dir, Version: "1.0.0", Enabled: true,
			Source: plugins.Source{Kind: plugins.SourceDirectory, Path: dir},
		}},
		"broken@acme": {{
			InstallPath: filepath.Join(root, "nonexistent"), Version: "1.0.0", Enabled: true,
			Source: plugins.Source{Kind: plugins.SourceDirectory, Path: filepath.Join(root, "nonexistent")},
		}},
	}}
	if err := plugins.SaveRegistry(filepath.Join(root, "installed_plugins.json"), reg); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRun_PluginsHuman(t *testing.T) {
	root := pluginsFixture(t)
	var out, errb bytes.Buffer
	if code := run([]string{"plugins", "--store-root", root}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	got := out.String()
	if !strings.Contains(got, "widget@acme") {
		t.Errorf("plugins output missing healthy plugin:\n%s", got)
	}
	if !strings.Contains(got, "broken@acme") {
		t.Errorf("plugins output missing orphaned entry:\n%s", got)
	}
	if !strings.Contains(got, "FAIL") {
		t.Errorf("plugins output missing a FAIL marker:\n%s", got)
	}
}

func TestRun_PluginsJSON(t *testing.T) {
	root := pluginsFixture(t)
	var out, errb bytes.Buffer
	if code := run([]string{"plugins", "--json", "--store-root", root}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	var findings []plugins.DoctorFinding
	if err := json.Unmarshal(out.Bytes(), &findings); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out.String())
	}
	foundFail, foundOK := false, false
	for _, f := range findings {
		switch f.Level {
		case plugins.LevelFail:
			foundFail = true
		case plugins.LevelOK:
			foundOK = true
		}
	}
	if !foundFail {
		t.Errorf("expected at least one FAIL finding; got %+v", findings)
	}
	if !foundOK {
		t.Errorf("expected at least one OK finding; got %+v", findings)
	}
}

func TestRun_PluginsUnwritableStoreRoot(t *testing.T) {
	// A store root inside a nonexistent parent dir with no way to create it
	// isn't representative on most CI, so this instead checks the flag wires
	// through to a fresh, empty store rather than the real default root: an
	// empty temp dir must produce only environment-category findings and no
	// registry/marketplace/component findings at all.
	root := t.TempDir()
	var out, errb bytes.Buffer
	if code := run([]string{"plugins", "--json", "--store-root", root}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, errb.String())
	}
	var findings []plugins.DoctorFinding
	if err := json.Unmarshal(out.Bytes(), &findings); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, out.String())
	}
	for _, f := range findings {
		if f.Category != "environment" {
			t.Errorf("empty store should only produce environment findings; got %+v", f)
		}
	}
}

package doctor

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
)

// fixtureRunbookMD is the Task 3 Step 1 fixture runbook: two mechanical
// checks (a single-condition threshold and a compound "all" threshold) plus
// one CLASSIFY prose bullet that only an LLM operator can judge — exercising
// dedup, the summary table, contract-valid Finding JSON, and the
// never-silently-skipped manual step in one document.
const fixtureRunbookMD = `# Runbook: fixture-runbook

**Question:** did this session waste budget on run-timeout jobs, or get stuck
in a long identical-error tool-call loop?

## HEALTHY
- No run_timeout terminal jobs at or above the threshold.
- No identical-error tool-call run at or above the threshold.

## INSPECT
` + "```" + `
evener-doctor transcript <selector> --health --json
` + "```" + `

## CLASSIFY
` + "```" + `yaml
audit:
  - title: "Run-timeout jobs wasting budget"
    severity: high
    category: timeout
    metric: jobs.run_timeout
    op: ">="
    value: 5
  - title: "Long identical-error tool-call run"
    severity: medium
    category: provider_error
    all:
      - metric: longest_identical_run.errors
        op: "=="
        value: true
      - metric: longest_identical_run.length
        op: ">="
        value: 3
` + "```" + `
- Review any flagged session's transcript manually to confirm root cause before filing a fix.
`

func mustParseFixtureRunbook(t *testing.T) Runbook {
	t.Helper()
	rb, err := ParseRunbook("fixture-runbook", []byte(fixtureRunbookMD))
	if err != nil {
		t.Fatal(err)
	}
	return rb
}

func TestParseRunbook_AuditBlockAndManualSteps(t *testing.T) {
	rb := mustParseFixtureRunbook(t)
	if len(rb.Checks) != 2 {
		t.Fatalf("Checks = %d, want 2: %+v", len(rb.Checks), rb.Checks)
	}

	c0 := rb.Checks[0]
	if c0.Title != "Run-timeout jobs wasting budget" || c0.Severity != "high" || c0.Category != "timeout" {
		t.Errorf("check[0] = %+v", c0)
	}
	if len(c0.Conditions) != 1 || c0.Conditions[0].Metric != "jobs.run_timeout" || c0.Conditions[0].Op != ">=" {
		t.Errorf("check[0].Conditions = %+v", c0.Conditions)
	}
	if c0.SuggestedFix != "diagnosis" {
		t.Errorf("check[0].SuggestedFix = %q, want default diagnosis", c0.SuggestedFix)
	}

	c1 := rb.Checks[1]
	if c1.Title != "Long identical-error tool-call run" || c1.Severity != "medium" || c1.Category != "provider_error" {
		t.Errorf("check[1] = %+v", c1)
	}
	if len(c1.Conditions) != 2 {
		t.Fatalf("check[1].Conditions = %d, want 2 (the \"all\" compound form)", len(c1.Conditions))
	}
	if c1.Conditions[0].Metric != "longest_identical_run.errors" || c1.Conditions[1].Metric != "longest_identical_run.length" {
		t.Errorf("check[1].Conditions = %+v", c1.Conditions)
	}

	if len(rb.ManualSteps) != 1 {
		t.Fatalf("ManualSteps = %v, want exactly 1 (the prose step must never be silently skipped)", rb.ManualSteps)
	}
	if !strings.Contains(rb.ManualSteps[0], "Review any flagged session") {
		t.Errorf("ManualSteps[0] = %q", rb.ManualSteps[0])
	}
}

// TestParseRunbook_WrappedBulletJoinsContinuationLines is the final-review
// I2 fix: a CLASSIFY bullet wrapped across several physical lines (every
// standing runbook's prose bullets are written this way) must survive whole
// as one ManualStep, not get truncated to its first line.
func TestParseRunbook_WrappedBulletJoinsContinuationLines(t *testing.T) {
	md := `# Runbook: wrapped

## HEALTHY
- fine

## INSPECT
` + "```" + `
evener-doctor transcript <selector> --health --json
` + "```" + `

## CLASSIFY
- Read the flagged session's transcript around the identical run
  (` + "`evener-doctor transcript <sel> --format outline`" + `) to confirm the
  calls really are identical retries, not a legitimate scripted retry.
- A run below the threshold is not a Finding.
`
	rb, err := ParseRunbook("wrapped", []byte(md))
	if err != nil {
		t.Fatal(err)
	}
	if len(rb.ManualSteps) != 2 {
		t.Fatalf("ManualSteps = %v, want exactly 2", rb.ManualSteps)
	}
	want := "Read the flagged session's transcript around the identical run (`evener-doctor transcript <sel> --format outline`) to confirm the calls really are identical retries, not a legitimate scripted retry."
	if rb.ManualSteps[0] != want {
		t.Errorf("ManualSteps[0] = %q, want %q", rb.ManualSteps[0], want)
	}
	if rb.ManualSteps[1] != "A run below the threshold is not a Finding." {
		t.Errorf("ManualSteps[1] = %q", rb.ManualSteps[1])
	}
}

func TestParseRunbook_MissingCategoryErrors(t *testing.T) {
	bad := "## CLASSIFY\n```yaml\naudit:\n  - title: x\n    severity: high\n    metric: jobs.run_timeout\n    op: \">=\"\n    value: 5\n```\n"
	if _, err := ParseRunbook("bad", []byte(bad)); err == nil {
		t.Fatal("want error for missing category")
	}
}

func TestParseRunbook_InvalidSeverityErrors(t *testing.T) {
	bad := "## CLASSIFY\n```yaml\naudit:\n  - title: x\n    severity: extreme\n    category: timeout\n    metric: jobs.run_timeout\n    op: \">=\"\n    value: 5\n```\n"
	if _, err := ParseRunbook("bad", []byte(bad)); err == nil {
		t.Fatal("want error for invalid severity")
	}
}

func TestParseRunbook_InvalidOpErrors(t *testing.T) {
	bad := "## CLASSIFY\n```yaml\naudit:\n  - title: x\n    severity: high\n    category: timeout\n    metric: jobs.run_timeout\n    op: \"~=\"\n    value: 5\n```\n"
	if _, err := ParseRunbook("bad", []byte(bad)); err == nil {
		t.Fatal("want error for invalid op")
	}
}

func TestParseRunbook_EmptyRunbookErrors(t *testing.T) {
	if _, err := ParseRunbook("empty", []byte("# Runbook: empty\n\nnothing here\n")); err == nil {
		t.Fatal("want error: no audit: block and no CLASSIFY prose, so nothing is audit-executable")
	}
}

// TestParseRunbook_DuplicateCategoryTitleErrors is the fix-round-1 Important
// finding's regression test: auditSignature keys on (category, title), so
// two checks sharing that pair would silently collapse into one Finding at
// audit time -- the second check's tripped sessions merging into the
// first's evidence and freezing the wrong severity. That must be caught at
// parse time instead.
func TestParseRunbook_DuplicateCategoryTitleErrors(t *testing.T) {
	bad := "## CLASSIFY\n```yaml\n" +
		"audit:\n" +
		"  - title: \"Run-timeout jobs wasting budget\"\n" +
		"    severity: high\n" +
		"    category: timeout\n" +
		"    metric: jobs.run_timeout\n" +
		"    op: \">=\"\n" +
		"    value: 5\n" +
		"  - title: \"Run-timeout jobs wasting budget\"\n" +
		"    severity: medium\n" +
		"    category: timeout\n" +
		"    metric: jobs.other_reason\n" +
		"    op: \">=\"\n" +
		"    value: 1\n" +
		"```\n"
	_, err := ParseRunbook("dup", []byte(bad))
	if err == nil {
		t.Fatal("want error for duplicate (category, title) pair")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error should say duplicate, got: %v", err)
	}
	for _, want := range []string{"Run-timeout jobs wasting budget", "timeout", "high", "medium"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name both colliding checks (want %q), got: %v", want, err)
		}
	}
}

// TestParseRunbook_DuplicateTitleDifferentCategoryIsAllowed proves the
// uniqueness constraint is scoped to (category, title), not title alone --
// two checks with the same title but different categories don't collide,
// since auditSignature includes category.
func TestParseRunbook_DuplicateTitleDifferentCategoryIsAllowed(t *testing.T) {
	ok := "## CLASSIFY\n```yaml\n" +
		"audit:\n" +
		"  - title: \"Budget waste\"\n" +
		"    severity: high\n" +
		"    category: timeout\n" +
		"    metric: jobs.run_timeout\n" +
		"    op: \">=\"\n" +
		"    value: 5\n" +
		"  - title: \"Budget waste\"\n" +
		"    severity: medium\n" +
		"    category: provider_error\n" +
		"    metric: longest_identical_run.length\n" +
		"    op: \">=\"\n" +
		"    value: 3\n" +
		"```\n"
	rb, err := ParseRunbook("ok", []byte(ok))
	if err != nil {
		t.Fatal(err)
	}
	if len(rb.Checks) != 2 {
		t.Fatalf("Checks = %d, want 2 (same title, different category, not a collision)", len(rb.Checks))
	}
}

// TestParseRunbook_AuditBlockOutsideClassifyErrors is the fix-round-1 Minor
// finding's regression test: an audit: block must live inside CLASSIFY, per
// writing-runbooks.md -- one placed elsewhere (e.g. under INSPECT) must be
// caught loudly, not silently accepted.
func TestParseRunbook_AuditBlockOutsideClassifyErrors(t *testing.T) {
	bad := "## INSPECT\n```yaml\n" +
		"audit:\n" +
		"  - title: x\n" +
		"    severity: high\n" +
		"    category: timeout\n" +
		"    metric: jobs.run_timeout\n" +
		"    op: \">=\"\n" +
		"    value: 5\n" +
		"```\n"
	_, err := ParseRunbook("misplaced", []byte(bad))
	if err == nil {
		t.Fatal("want error: audit: block outside CLASSIFY")
	}
	if !strings.Contains(err.Error(), "CLASSIFY") {
		t.Errorf("error should name CLASSIFY, got: %v", err)
	}
}

// TestParseRunbook_AuditBlockWithNoHeadingErrors covers the no-heading-yet
// case (an audit: block appearing before any "## " heading at all) --
// inClassify starts false, so this must also error rather than accepting a
// block with no section context.
func TestParseRunbook_AuditBlockWithNoHeadingErrors(t *testing.T) {
	bad := "```yaml\n" +
		"audit:\n" +
		"  - title: x\n" +
		"    severity: high\n" +
		"    category: timeout\n" +
		"    metric: jobs.run_timeout\n" +
		"    op: \">=\"\n" +
		"    value: 5\n" +
		"```\n"
	_, err := ParseRunbook("no-heading", []byte(bad))
	if err == nil {
		t.Fatal("want error: audit: block with no CLASSIFY heading in scope")
	}
}

// TestMetricSourceResolve_TrailingJunkIsLoudError is the final-review minor
// fix: writing-runbooks.md promises a malformed metric path is a loud
// parse/eval error, never a silent zero. Trailing junk on a scalar metric
// (no sub-path of its own) and on jobs.zero_output_terminal (a scalar
// wrapped inside the jobs.* namespace) both used to resolve silently via
// the jobs map's zero-value-on-miss behavior; both must now error.
func TestMetricSourceResolve_TrailingJunkIsLoudError(t *testing.T) {
	src := metricSource{health: HealthResult{Jobs: JobsHealth{ByTerminalReason: map[string]int{}}}}
	for _, path := range []string{
		"truncation_warnings.junk",
		"stale_notifications.x",
		"user_corrections.x",
		"jobs.zero_output_terminal.x",
	} {
		if _, err := src.resolve(path); err == nil {
			t.Errorf("resolve(%q): want error, got nil", path)
		}
	}
	// A legitimate jobs.<reason> path (a reason name is just a bare string,
	// never dotted) still resolves, silently zero when absent.
	if v, err := src.resolve("jobs.run_timeout"); err != nil || v != 0 {
		t.Errorf("resolve(\"jobs.run_timeout\") = %v, %v; want 0, nil", v, err)
	}
}

// fiveRunTimeoutJobsFor builds five terminal, zero-output run_timeout jobs
// owned by sid -- enough to trip the fixture's `jobs.run_timeout >= 5` check.
func fiveRunTimeoutJobsFor(sid string) []jobstore.Event {
	var events []jobstore.Event
	exitTimeout := -1
	for i := range 5 {
		id := fmt.Sprintf("job_%s_%d", sid, i)
		events = append(events,
			jobstore.Event{Kind: jobstore.EventJobStarted, JobID: id, Type: jobstore.JobShell, Command: "x",
				OwnerSessionID: sid, VisibleToSession: sid, StartedAt: &jobStartedAt},
			jobstore.Event{Kind: jobstore.EventJobFinished, JobID: id, Status: jobstore.StatusStopped, Reason: "run_timeout",
				ExitCode: &exitTimeout, EndedAt: &jobEndedAt, OutputBytes: 0},
		)
	}
	return events
}

// fourIdenticalFailingShellTurns builds four identical, failing "shell"
// tool-call turns -- enough to trip the fixture's
// `longest_identical_run.errors && length >= 3` compound check.
func fourIdenticalFailingShellTurns() []schema.Turn {
	var turns []schema.Turn
	for i := range 4 {
		id := fmt.Sprintf("c%d", i)
		turns = append(turns,
			schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
				healthToolCall(id, "shell", `{"cmd":"flaky"}`),
			}}),
			schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
				healthToolResult(id, "shell", "boom", true),
			}}),
		)
	}
	return turns
}

func oneCleanReadFileTurns() []schema.Turn {
	return []schema.Turn{
		schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			healthToolCall("h1", "read_file", `{"path":"a"}`),
		}}),
		schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{
			healthToolResult("h1", "read_file", "ok", false),
		}}),
	}
}

func writeAuditSession(t *testing.T, bucket, sid string, turns []schema.Turn, jobEvents []jobstore.Event) {
	t.Helper()
	writeRichSession(t, bucket, sid, turns, nil, schema.SessionMeta{})
	jobsPath := filepath.Join(bucket, "sessions", sid, "jobs.jsonl")
	writeFile(t, jobsPath, "")
	if len(jobEvents) > 0 {
		writeJobsEvents(t, jobsPath, jobEvents)
	}
}

// auditFixture builds the Task 3 Step 1 two-session set: tripSID trips BOTH
// fixture checks (five run_timeout jobs, a four-call identical failing
// shell run), healthySID trips neither.
func auditFixture(t *testing.T) (base, tripSID, healthySID string) {
	t.Helper()
	base = t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	tripSID, healthySID = sidA, sidB

	writeAuditSession(t, bucket, tripSID, fourIdenticalFailingShellTurns(), fiveRunTimeoutJobsFor(tripSID))
	writeAuditSession(t, bucket, healthySID, oneCleanReadFileTurns(), nil)

	return base, tripSID, healthySID
}

func TestRunAudit_DedupAndSummary(t *testing.T) {
	base, tripSID, healthySID := auditFixture(t)
	rb := mustParseFixtureRunbook(t)

	res, err := RunAudit(base, rb, AuditOpts{Sessions: []string{tripSID, healthySID}})
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionsChecked != 2 {
		t.Fatalf("SessionsChecked = %d, want 2", res.SessionsChecked)
	}
	if len(res.Unreadable) != 0 {
		t.Fatalf("Unreadable = %+v, want none", res.Unreadable)
	}
	if len(res.Findings) != 2 {
		t.Fatalf("Findings = %d, want 2 (one per tripped check): %+v", len(res.Findings), res.Findings)
	}
	for _, f := range res.Findings {
		if len(f.Evidence.SessionRefs) != 1 || !strings.Contains(f.Evidence.SessionRefs[0], tripSID) {
			t.Errorf("finding %q sessionRefs = %v, want exactly one ref naming %s", f.Title, f.Evidence.SessionRefs, tripSID)
		}
		if f.Signature == "" {
			t.Errorf("finding %q missing signature", f.Title)
		}
		if f.Category == "" {
			t.Errorf("finding %q missing category", f.Title)
		}
		if f.Evidence.DoctorCommand == "" {
			t.Errorf("finding %q missing evidence.doctorCommand", f.Title)
		}
		if f.SuggestedFix.Type != "diagnosis" {
			t.Errorf("finding %q suggestedFix.type = %q, want diagnosis", f.Title, f.SuggestedFix.Type)
		}
	}
	if len(res.Summary) != 2 {
		t.Fatalf("Summary = %d rows, want 2", len(res.Summary))
	}
	for _, s := range res.Summary {
		if s.Sessions != 1 {
			t.Errorf("summary %q sessions = %d, want 1", s.Title, s.Sessions)
		}
	}
	if len(res.Manual) != 1 {
		t.Fatalf("Manual = %v, want the runbook's one prose step surfaced, never silently skipped", res.Manual)
	}
}

func TestRunAudit_HealthySessionEmitsZeroFindings(t *testing.T) {
	base, _, healthySID := auditFixture(t)
	rb := mustParseFixtureRunbook(t)
	res, err := RunAudit(base, rb, AuditOpts{Sessions: []string{healthySID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 0 {
		t.Errorf("Findings = %+v, want none — a healthy run emits zero findings", res.Findings)
	}
}

// TestRunAudit_DedupAcrossMultipleSessions is the load-bearing dedup
// assertion beyond the brief's baseline: a third session trips only the
// run_timeout check, so that Finding's evidence must list BOTH sessions
// (one Finding, N affected sessions), while the identical-run Finding still
// lists only tripSID.
func TestRunAudit_DedupAcrossMultipleSessions(t *testing.T) {
	base, tripSID, healthySID := auditFixture(t)
	bucket := stateHomeBucket(base, hash1)
	thirdSID := newSessionsTestSID(t)
	writeAuditSession(t, bucket, thirdSID, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(thirdSID))

	rb := mustParseFixtureRunbook(t)
	res, err := RunAudit(base, rb, AuditOpts{Sessions: []string{tripSID, healthySID, thirdSID}})
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionsChecked != 3 {
		t.Fatalf("SessionsChecked = %d, want 3", res.SessionsChecked)
	}

	var runTimeout, identicalRun *Finding
	for i := range res.Findings {
		switch {
		case strings.Contains(res.Findings[i].Title, "Run-timeout"):
			runTimeout = &res.Findings[i]
		case strings.Contains(res.Findings[i].Title, "identical-error"):
			identicalRun = &res.Findings[i]
		}
	}
	if runTimeout == nil {
		t.Fatalf("no run-timeout finding: %+v", res.Findings)
	}
	if len(runTimeout.Evidence.SessionRefs) != 2 {
		t.Errorf("run-timeout finding sessionRefs = %v, want 2 (tripSID and thirdSID deduped into one Finding)", runTimeout.Evidence.SessionRefs)
	}
	if identicalRun == nil {
		t.Fatalf("no identical-run finding: %+v", res.Findings)
	}
	if len(identicalRun.Evidence.SessionRefs) != 1 {
		t.Errorf("identical-run finding sessionRefs = %v, want 1 (only tripSID)", identicalRun.Evidence.SessionRefs)
	}
}

// apiHealthAuditRunbookMD checks every WS9 Task 4 apilog.* health metric
// added to the audit metric namespace: recorded_empty, retry_storm_groups,
// unsettled_groups, and errors_by_class.<class>.
const apiHealthAuditRunbookMD = `# Runbook: api-health-fixture

**Question:** does this session show provider retry-storm, unsettled-group,
recorded-empty, or permanent-error strain?

## HEALTHY
- No recorded-empty responses, no attempt group has 3+ attempts, every
  attempt group settled, no permanent-class provider error.

## INSPECT
` + "```" + `
evener-doctor apilog <selector> --health --json
` + "```" + `

## CLASSIFY
` + "```" + `yaml
audit:
  - title: "Recorded-empty response"
    severity: low
    category: provider_error
    metric: apilog.recorded_empty
    op: ">="
    value: 1
  - title: "Retry storm"
    severity: medium
    category: provider_error
    metric: apilog.retry_storm_groups
    op: ">="
    value: 1
  - title: "Unsettled attempt group"
    severity: medium
    category: provider_error
    metric: apilog.unsettled_groups
    op: ">="
    value: 1
  - title: "Permanent provider error"
    severity: high
    category: provider_error
    metric: apilog.errors_by_class.permanent
    op: ">="
    value: 1
` + "```" + `
`

// TestRunAudit_APIHealthMetricsAreAddressable builds one session tripping
// all four apilog.* health checks: an empty response, a 4-attempt group
// (retry storm), an unsettled tail (no settlement record), and a settled
// 403 (permanent).
func TestRunAudit_APIHealthMetricsAreAddressable(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	var records []apilog.APILogRecord

	var stormFinal apilog.APIAttemptRecord
	for i := 1; i <= 4; i++ {
		outcome := apilog.AttemptProviderTimeout
		if i == 4 {
			outcome = apilog.AttemptSuccess
		}
		attempt := apiHealthAttempt("ag_storm", i, outcome)
		if outcome == apilog.AttemptSuccess {
			attempt.Response = &apilog.APIAttemptResponse{StatusCode: new(200), Body: apilog.EncodeBody([]byte("{}")), TextLength: new(1), ToolCallCount: new(0)}
			stormFinal = attempt
		} else {
			attempt.ErrorClass = "timeout"
		}
		records = append(records, attempt)
	}
	records = append(records, doctorSettlement(stormFinal, 4))

	tail := apiHealthAttempt("ag_tail", 1, apilog.AttemptSuccess)
	tail.Response = &apilog.APIAttemptResponse{StatusCode: new(200), Body: apilog.EncodeBody([]byte("{}")), TextLength: new(1), ToolCallCount: new(0)}
	records = append(records, tail)

	forbidden := apiHealthAttempt("ag_403", 1, apilog.AttemptProviderReject)
	forbidden.ErrorClass = "access_denied"
	forbidden.Response = &apilog.APIAttemptResponse{StatusCode: new(403), Body: apilog.EncodeBody([]byte("{}"))}
	records = append(records, forbidden, doctorSettlement(forbidden, 1))

	empty := apiHealthAttempt("ag_empty", 1, apilog.AttemptSuccess)
	empty.Response = &apilog.APIAttemptResponse{StatusCode: new(200), Body: apilog.EncodeBody([]byte("{}")), TextLength: new(0), ToolCallCount: new(0)}
	records = append(records, empty, doctorSettlement(empty, 1))

	writeRichSession(t, bucket, sidA, nil, records, schema.SessionMeta{})

	rb, err := ParseRunbook("api-health-fixture", []byte(apiHealthAuditRunbookMD))
	if err != nil {
		t.Fatal(err)
	}
	res, err := RunAudit(base, rb, AuditOpts{Sessions: []string{sidA}})
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionsChecked != 1 {
		t.Fatalf("SessionsChecked = %d, want 1", res.SessionsChecked)
	}
	wantTitles := map[string]bool{"Recorded-empty response": false, "Retry storm": false, "Unsettled attempt group": false, "Permanent provider error": false}
	for _, f := range res.Findings {
		if _, ok := wantTitles[f.Title]; !ok {
			t.Errorf("unexpected finding %q", f.Title)
			continue
		}
		wantTitles[f.Title] = true
	}
	for title, tripped := range wantTitles {
		if !tripped {
			t.Errorf("check %q did not trip: %+v", title, res.Findings)
		}
	}
}

func TestRunAudit_UnreadableExplicitSessionSurfacedNotSkipped(t *testing.T) {
	base, tripSID, _ := auditFixture(t)
	rb := mustParseFixtureRunbook(t)
	res, err := RunAudit(base, rb, AuditOpts{Sessions: []string{tripSID, "02wMz5TxvEMoJEDTDGOTix"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Unreadable) != 1 {
		t.Fatalf("Unreadable = %+v, want 1 (the nonexistent session, never silently dropped)", res.Unreadable)
	}
	if res.SessionsChecked != 1 {
		t.Errorf("SessionsChecked = %d, want 1 (only tripSID actually resolved)", res.SessionsChecked)
	}
}

func TestRunAudit_SinceSurfacesSweepUnreadable(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	writeSessionsFixtureSession(t, bucket, sidB,
		transcript.Header{CreatedAt: time.Now(), Model: "m"}, nil, schema.SessionMeta{Model: "m"}, nil, time.Now())
	corruptPath := filepath.Join(bucket, "sessions", sidA+".transcript.jsonl")
	writeFile(t, corruptPath, "not valid json\n")
	if err := schema.SaveSessionMeta(bucket, schema.SessionMeta{ID: sidA}); err != nil {
		t.Fatal(err)
	}

	rb := mustParseFixtureRunbook(t)
	res, err := RunAudit(base, rb, AuditOpts{Since: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Unreadable) != 1 || res.Unreadable[0].SessionID != sidA {
		t.Fatalf("Unreadable = %+v, want sidA listed (the sweep's unreadable session, surfaced the same way `sessions` surfaces it)", res.Unreadable)
	}
	if res.SessionsChecked != 1 {
		t.Errorf("SessionsChecked = %d, want 1 (sidB only)", res.SessionsChecked)
	}
}

func TestRunAudit_RequiresSessionsOrSince(t *testing.T) {
	rb := mustParseFixtureRunbook(t)
	if _, err := RunAudit(t.TempDir(), rb, AuditOpts{}); err == nil {
		t.Fatal("want error when neither --sessions nor --since is given")
	}
}

// TestRunAudit_SinceAuditsLegacyNamedBuckets proves the --since audit set
// covers buckets whose directory names the agent ref grammar cannot consume
// (round 3's refFor emits no TranscriptRef for them): the session is audited
// via its bare id — never recorded as a blank-identity unreadable with the
// misleading "no session selector" error — so the operator keeps both the
// coverage and the identity of every swept session.
func TestRunAudit_SinceAuditsLegacyNamedBuckets(t *testing.T) {
	base := t.TempDir()
	legacyBucket := stateHomeBucket(base, "0123456789abcdef")
	writeSessionsFixtureSession(t, legacyBucket, sidA,
		transcript.Header{CreatedAt: time.Now(), Model: "m"}, nil, schema.SessionMeta{Model: "m"}, nil, time.Now())

	rb := mustParseFixtureRunbook(t)
	res, err := RunAudit(base, rb, AuditOpts{Since: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionsChecked != 1 {
		t.Fatalf("SessionsChecked = %d, want 1 — the legacy-named bucket's session must be audited", res.SessionsChecked)
	}
	// Invariant: no Unreadable entry ever carries a blank identity.
	for _, u := range res.Unreadable {
		if u.SessionID == "" {
			t.Errorf("Unreadable entry with a blank session_id: %+v", u)
		}
	}
	if len(res.Unreadable) != 0 {
		t.Fatalf("Unreadable = %+v, want none — a session with no emitted ref is audited, not skipped as unreadable", res.Unreadable)
	}
}

// TestRunAudit_LegacyBucketEvidenceNamesSessions covers the evidence path for
// buckets whose directory names fail every qualifier: refFor emits no
// TranscriptRef (identifier.ValidateProjectID rejects the backslash), and
// safeTokenForRepro also rejects the backslash (path separator), so
// followSelector falls back to a bare id — the pre-FU2 behavior, now
// scoped to this reproduction-unsafe class only. (Shell-safe legacy names
// like "0123456789abcdef" now get proj: refs via safeTokenForRepro — see
// TestRunAudit_HexNamedLegacyBucketsAuditedWithProjRefs.) A check tripping
// in such sessions must name EVERY affected session by bare id — not
// collapse them into one empty-string entry — so the affected-session
// count, the --sessions reproduction line, and a re-run of that line
// against RunAudit all keep working.
func TestRunAudit_LegacyBucketEvidenceNamesSessions(t *testing.T) {
	base := t.TempDir()
	legacyBucket := stateHomeBucket(base, "back\\slash-bucket")
	writeAuditSession(t, legacyBucket, sidA, fourIdenticalFailingShellTurns(), fiveRunTimeoutJobsFor(sidA))
	writeAuditSession(t, legacyBucket, sidB, fourIdenticalFailingShellTurns(), fiveRunTimeoutJobsFor(sidB))

	rb := mustParseFixtureRunbook(t)
	res, err := RunAudit(base, rb, AuditOpts{Since: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionsChecked != 2 {
		t.Fatalf("SessionsChecked = %d, want 2", res.SessionsChecked)
	}
	if len(res.Findings) != 2 {
		t.Fatalf("Findings = %d, want 2 (both checks trip in both sessions): %+v", len(res.Findings), res.Findings)
	}
	for _, f := range res.Findings {
		if len(f.Evidence.SessionRefs) != 2 {
			t.Errorf("finding %q SessionRefs = %v, want both affected sessions listed", f.Title, f.Evidence.SessionRefs)
		}
		named := map[string]bool{}
		for _, ref := range f.Evidence.SessionRefs {
			if ref == "" {
				t.Errorf("finding %q carries a blank evidence ref: %v", f.Title, f.Evidence.SessionRefs)
				continue
			}
			named[ref] = true
		}
		if !named[sidA] || !named[sidB] {
			t.Errorf("finding %q SessionRefs = %v, want %q and %q by bare id", f.Title, f.Evidence.SessionRefs, sidA, sidB)
		}
		if !strings.Contains(f.Evidence.DoctorCommand, sidA) || !strings.Contains(f.Evidence.DoctorCommand, sidB) {
			t.Errorf("finding %q DoctorCommand = %q, want the reproduction line to name both sessions by bare id", f.Title, f.Evidence.DoctorCommand)
		}
		// The reproduction line must actually re-run: feeding the evidence
		// refs back as --sessions audits both sessions again, with no
		// blank-identity unreadable rows.
		res2, err := RunAudit(base, rb, AuditOpts{Sessions: f.Evidence.SessionRefs})
		if err != nil {
			t.Fatalf("re-running %q DoctorCommand refs: %v", f.Title, err)
		}
		if res2.SessionsChecked != 2 || len(res2.Unreadable) != 0 {
			t.Errorf("re-running %q evidence refs: SessionsChecked=%d Unreadable=%+v, want 2 and none", f.Title, res2.SessionsChecked, res2.Unreadable)
		}
	}
}

// TestRunAudit_DuplicateSIDAcrossBucketsAuditsBoth is the FU2 RED case: when
// the SAME session id is present in two different project buckets whose
// directory names pass identifier.ValidateProjectID (so refFor emits proj:
// refs and followSelector returns them), each row is addressed precisely
// via locateInBucket, so both audit and evidence names each by its proj:
// ref. ListSessions coverage (two rows, one per bucket) must match audit
// coverage (two checked).
//
// Note: refFor already emitted proj: refs for ValidateProjectID-valid names
// before FU2, so this test is base-immune — it passes on pre-FU2 code. It
// guards the feature (proj: ref routing for duplicate SIDs across valid
// buckets) by failing if followSelector were changed to return bare ids:
// SessionsChecked would drop to 0 (bare id ambiguous across two buckets).
// The companion TestRunAudit_DuplicateSIDAcrossBucketsBareIDFallback guards
// the other side of the boundary (safeTokenForRepro-unsafe names → bare
// id → both Unreadable). TestRunAudit_HexNamedLegacyBucketsAuditedWithProjRefs
// guards the middle branch (shell-safe non-canonical names → proj: refs).
func TestRunAudit_DuplicateSIDAcrossBucketsAuditsBoth(t *testing.T) {
	base := t.TempDir()
	// Both names pass identifier.ValidateProjectID (readable-<10 base62>
	// structure), so refFor emits proj: refs and followSelector returns them
	// — addressing each row precisely via locateInBucket.
	bucketA := stateHomeBucket(base, hash1)
	bucketB := stateHomeBucket(base, hash2)
	writeAuditSession(t, bucketA, sidA, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(sidA))
	writeAuditSession(t, bucketB, sidA, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(sidA))

	// ListSessions enumerates one row per (bucket, session): two rows for the
	// duplicate sid across two buckets — the coverage the audit must match.
	sweep, err := ListSessions(base, SessionsOpts{Since: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if len(sweep.Sessions) != 2 {
		t.Fatalf("ListSessions enumerated %d rows, want 2 (one per bucket for the duplicate sid)", len(sweep.Sessions))
	}

	rb := mustParseFixtureRunbook(t)
	res, err := RunAudit(base, rb, AuditOpts{Since: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	// Base-immune: hash1/hash2 are ValidateProjectID-valid, so refFor
	// already emitted proj: refs before FU2 — both rows always audited
	// here. The test guards that followSelector keeps routing them via
	// proj: refs (not bare ids, which would be ambiguous across two
	// buckets and drop SessionsChecked to 0).
	if res.SessionsChecked != 2 {
		t.Fatalf("SessionsChecked = %d, want 2 — the duplicate sid across two selector-safe buckets must both audit (FU2)", res.SessionsChecked)
	}
	if len(res.Unreadable) != 0 {
		t.Fatalf("Unreadable = %+v, want none — neither row should be skipped as ambiguous", res.Unreadable)
	}
	// Coverage match: audit checks exactly the sessions the sweep enumerated.
	if res.SessionsChecked != len(sweep.Sessions) {
		t.Errorf("audit coverage %d != ListSessions coverage %d", res.SessionsChecked, len(sweep.Sessions))
	}
	// The run-timeout check trips in both; evidence must name each by its
	// bucket-qualified proj: ref, not collapse to one bare-id entry.
	var runTimeout *Finding
	for i := range res.Findings {
		if strings.Contains(res.Findings[i].Title, "Run-timeout") {
			runTimeout = &res.Findings[i]
		}
	}
	if runTimeout == nil {
		t.Fatalf("no run-timeout finding: %+v", res.Findings)
	}
	if len(runTimeout.Evidence.SessionRefs) != 2 {
		t.Fatalf("run-timeout finding SessionRefs = %v, want 2 (one proj: ref per bucket)", runTimeout.Evidence.SessionRefs)
	}
	wantA := "proj:" + hash1 + ":" + sidA
	wantB := "proj:" + hash2 + ":" + sidA
	refs := map[string]bool{}
	for _, ref := range runTimeout.Evidence.SessionRefs {
		refs[ref] = true
	}
	if !refs[wantA] || !refs[wantB] {
		t.Errorf("run-timeout SessionRefs = %v, want both %q and %q", runTimeout.Evidence.SessionRefs, wantA, wantB)
	}
	// The reproduction line must re-run: feeding the evidence refs back as
	// --sessions audits both again, with no unreadable rows.
	res2, err := RunAudit(base, rb, AuditOpts{Sessions: runTimeout.Evidence.SessionRefs})
	if err != nil {
		t.Fatalf("re-running evidence refs: %v", err)
	}
	if res2.SessionsChecked != 2 || len(res2.Unreadable) != 0 {
		t.Errorf("re-running evidence refs: SessionsChecked=%d Unreadable=%+v, want 2 and none", res2.SessionsChecked, res2.Unreadable)
	}
}

// TestRunAudit_DuplicateSIDAcrossBucketsBareIDFallback guards the feature
// boundary: the same session id in two buckets whose names FAIL
// safeTokenForRepro (so followSelector falls back to the bare session id).
// The bare id is ambiguous across the two buckets, so both rows land
// Unreadable and SessionsChecked=0 — the pre-FU2 behavior this boundary
// preserves. This negative case contrasts with
// TestRunAudit_HexNamedLegacyBucketsAuditedWithProjRefs (shell-safe legacy
// names → proj: refs → both audit). Together they guard both sides of the
// feature: safe names get precise proj: refs, unsafe names fall back to
// bare ids.
func TestRunAudit_DuplicateSIDAcrossBucketsBareIDFallback(t *testing.T) {
	base := t.TempDir()
	// Both names fail safeTokenForRepro: backslash is a path separator, so
	// followSelector falls back to the bare session id.
	bucketA := stateHomeBucket(base, "back\\slash-a")
	bucketB := stateHomeBucket(base, "back\\slash-b")
	writeAuditSession(t, bucketA, sidA, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(sidA))
	writeAuditSession(t, bucketB, sidA, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(sidA))

	rb := mustParseFixtureRunbook(t)
	res, err := RunAudit(base, rb, AuditOpts{Since: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	// The bare id is ambiguous across the two buckets: both rows Unreadable.
	if res.SessionsChecked != 0 {
		t.Fatalf("SessionsChecked = %d, want 0 — the bare id is ambiguous across two safeTokenForRepro-unsafe buckets, so both rows must land Unreadable", res.SessionsChecked)
	}
	if len(res.Unreadable) != 2 {
		t.Fatalf("Unreadable = %d rows, want 2 (one per bucket)", len(res.Unreadable))
	}
}

// TestRunAudit_DoctorCommandCommaSafeForUnsafeBucketNames is the roborev fix
// round 1 RED case: projectTokenOK admits commas, spaces, and shell
// metacharacters in legacy bucket names, so followSelector emits a proj: ref
// the CLI splits --sessions on ',' (cmd/evener-doctor/main.go:524), so a ref
// containing a comma breaks the reproduction line (it splits into two
// invalid selectors), and a name with a space or shell metacharacter is a
// shell injection vector. The fix narrows followSelector's proj: emission to
// names that safely round-trip the comma-joined --sessions reproduction
// line (safeTokenForRepro: comma-free, whitespace-free, free of shell
// metacharacters, non-empty, not a path separator or NUL); everything else
// falls back to the bare session id — the pre-FU2 behavior for those names.
// This test exercises the comma-join + CLI-split layer (not the structured
// slice) and must fail on current head (the comma-bucket ref enters
// DoctorCommand).
func TestRunAudit_DoctorCommandCommaSafeForUnsafeBucketNames(t *testing.T) {
	// Bucket names that pass projectTokenOK but are unsafe in a comma-joined
	// --sessions reproduction line: a comma (CLI splits it), a space (shell
	// word-break), and a '$' (shell expansion).
	// --sessions reproduction line: a comma (CLI splits it), a space (shell
	// word-break), a '$' (shell expansion), and a '*' (shell glob expansion).
	for _, bucketName := range []string{"a,b", "has space", "dollar$bucket", "a*b", "a%PATH%b", "a^b"} {
		t.Run(bucketName, func(t *testing.T) {
			base := t.TempDir()
			bucket := stateHomeBucket(base, bucketName)
			writeAuditSession(t, bucket, sidA, fourIdenticalFailingShellTurns(), fiveRunTimeoutJobsFor(sidA))

			rb := mustParseFixtureRunbook(t)
			res, err := RunAudit(base, rb, AuditOpts{Since: 24 * time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			if res.SessionsChecked != 1 {
				t.Fatalf("SessionsChecked = %d, want 1", res.SessionsChecked)
			}
			if len(res.Findings) == 0 {
				t.Fatalf("no findings — the check should trip")
			}
			dc := res.Findings[0].Evidence.DoctorCommand

			// Extract the --sessions value from DoctorCommand and split it
			// the way the CLI does: plain strings.Split on ','. Each element
			// must resolve back to the session via Locate, or the
			// reproduction line is broken.
			//   evener doctor audit --runbook NAME --sessions <value>
			prefix := "evener doctor audit --runbook fixture-runbook --sessions "
			if !strings.HasPrefix(dc, prefix) {
				t.Fatalf("DoctorCommand = %q, want prefix %q", dc, prefix)
			}
			sessionsValue := strings.TrimPrefix(dc, prefix)
			splitRefs := strings.Split(sessionsValue, ",")
			for _, ref := range splitRefs {
				if _, err := Locate(base, ref); err != nil {
					t.Errorf("DoctorCommand %q: splitting --sessions on ',' yields %q, which does not resolve: %v (comma-join + CLI-split layer is broken)", dc, ref, err)
				}
			}
			// The ref must not contain a comma (which would split into an
			// invalid selector) or a space/shell metacharacter (injection).
			for _, ref := range splitRefs {
				if strings.ContainsAny(ref, ", \t$\x00*?[]%^") {
					t.Errorf("DoctorCommand %q: ref %q contains a character unsafe for the CLI's comma-joined --sessions grammar", dc, ref)
				}
			}
		})
	}
}

// TestRunAudit_HexNamedLegacyBucketsAuditedWithProjRefs verifies that
// hex-style legacy bucket names (e.g. 0123456789abcdef) — shell- and
// comma-safe but failing identifier.ValidateProjectID — are audited via
// followSelector's middle branch (safeTokenForRepro): the DoctorCommand
// reproduction line carries doctor-consumable proj:<hex>:<sid> refs that
// the doctor CLI round-trips fine. Round 6 (finding 3) decoupled
// SessionRefs from DoctorCommand: SessionRefs now carries the honest
// session identifier (the bare session id — agent-side read_transcript
// rejects proj:<hex>:<sid> via ValidateProjectID), because the
// proj:<hex>:<sid> form is rejected by the agent's transcript tools
// (ValidateProjectID fails for hex names). The bare id is the honest
// session handle and a doctor-CLI/human identifier on this tree
// (cross-bucket agent resolution arrives with #2205). It is shared
// across both buckets, so it appears once (deduped); the true count (2)
// is in the Description and Summary, and the DoctorCommand carries both
// proj: refs for reproduction.
func TestRunAudit_HexNamedLegacyBucketsAuditedWithProjRefs(t *testing.T) {
	base := t.TempDir()
	// Hex-style legacy bucket names: shell- and comma-safe, but fail
	// ValidateProjectID (no readable-<10 base62> structure).
	bucketA := stateHomeBucket(base, "0123456789abcdef")
	bucketB := stateHomeBucket(base, "fedcba9876543210")
	writeAuditSession(t, bucketA, sidA, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(sidA))
	writeAuditSession(t, bucketB, sidA, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(sidA))

	rb := mustParseFixtureRunbook(t)
	res, err := RunAudit(base, rb, AuditOpts{Since: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	// Both rows must audit — the hex names are safe enough for proj: refs.
	if res.SessionsChecked != 2 {
		t.Fatalf("SessionsChecked = %d, want 2 — hex-named legacy buckets should both audit via proj: refs", res.SessionsChecked)
	}
	if len(res.Unreadable) != 0 {
		t.Fatalf("Unreadable = %+v, want none", res.Unreadable)
	}
	// Evidence must name each by its bucket-qualified proj: ref.
	var runTimeout *Finding
	for i := range res.Findings {
		if strings.Contains(res.Findings[i].Title, "Run-timeout") {
			runTimeout = &res.Findings[i]
		}
	}
	if runTimeout == nil {
		t.Fatalf("no run-timeout finding: %+v", res.Findings)
	}
	// SessionRefs carries the honest session identifier: the bare session
	// id (the proj:<hex>:<sid> form is rejected by agent-side read_transcript
	// because ValidateProjectID fails for hex names — finding 3). The bare
	// id is the honest session handle and a doctor-CLI/human identifier on
	// this tree (cross-bucket agent resolution arrives with #2205). It is
	// shared across both buckets, so it appears once (deduped).
	if len(runTimeout.Evidence.SessionRefs) != 1 || runTimeout.Evidence.SessionRefs[0] != sidA {
		t.Errorf("run-timeout SessionRefs = %v, want [%q] (honest bare id, deduped across both hex buckets)", runTimeout.Evidence.SessionRefs, sidA)
	}
	// The summary count must reflect 2 sessions, not 1 (the bare id is
	// deduped in SessionRefs, but both sessions were audited).
	for _, s := range res.Summary {
		if strings.Contains(s.Title, "Run-timeout") && s.Sessions != 2 {
			t.Errorf("summary %q sessions = %d, want 2 — both hex-named sessions were audited (finding 2)", s.Title, s.Sessions)
		}
	}
	// DoctorCommand keeps the doctor-consumable proj:<hex>:<sid> form for
	// both buckets — the doctor CLI round-trips it fine (finding 3).
	dc := runTimeout.Evidence.DoctorCommand
	wantA := "proj:0123456789abcdef:" + sidA
	wantB := "proj:fedcba9876543210:" + sidA
	if !strings.Contains(dc, wantA) || !strings.Contains(dc, wantB) {
		t.Errorf("DoctorCommand %q must carry both %q and %q (doctor-consumable proj: refs)", dc, wantA, wantB)
	}
	// The reproduction line must re-run: feeding the DoctorCommand's
	// --sessions refs back audits both again, with no unreadable rows.
	prefix := "evener doctor audit --runbook fixture-runbook --sessions "
	sessionsValue, ok := strings.CutPrefix(dc, prefix)
	if !ok {
		t.Fatalf("DoctorCommand %q missing prefix %q", dc, prefix)
	}
	reproRefs := strings.Split(sessionsValue, ",")
	res2, err := RunAudit(base, rb, AuditOpts{Sessions: reproRefs})
	if err != nil {
		t.Fatalf("re-running evidence refs: %v", err)
	}
	if res2.SessionsChecked != 2 || len(res2.Unreadable) != 0 {
		t.Errorf("re-running evidence refs: SessionsChecked=%d Unreadable=%+v, want 2 and none", res2.SessionsChecked, res2.Unreadable)
	}
}

// TestRunAudit_ExplicitProjSelectorForLegacyBucket verifies that an
// explicit --sessions proj:<legacy-name>:<sid> selector audits the
// selected session (Locate resolves it; the reads use the user-supplied
// selector). Round 3 fixed the discard bug (the selector is preserved for
// reading). Round 6 (finding 3) decoupled SessionRefs from DoctorCommand:
// SessionRefs carries the honest bare sid (the proj:<hex>:<sid>
// form is rejected by agent-side read_transcript), while DoctorCommand
// carries the doctor-consumable proj: ref.
func TestRunAudit_ExplicitProjSelectorForLegacyBucket(t *testing.T) {
	base := t.TempDir()
	// Hex-style legacy bucket name: shell- and comma-safe, but fails
	// ValidateProjectID, so refFor emits no ref.
	bucket := stateHomeBucket(base, "0123456789abcdef")
	writeAuditSession(t, bucket, sidA, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(sidA))

	// Also seed a second bucket with the same sid to make the bare id
	// ambiguous — proving the discard bug.
	bucket2 := stateHomeBucket(base, "fedcba9876543210")
	writeAuditSession(t, bucket2, sidA, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(sidA))

	rb := mustParseFixtureRunbook(t)
	sel := "proj:0123456789abcdef:" + sidA
	res, err := RunAudit(base, rb, AuditOpts{Sessions: []string{sel}})
	if err != nil {
		t.Fatal(err)
	}
	// The explicitly selected session must audit — the selector already
	// resolved via Locate, so RunAudit must not discard it.
	if res.SessionsChecked != 1 {
		t.Fatalf("SessionsChecked = %d, want 1 — the explicit proj:<legacy>:<sid> selector should audit exactly that session", res.SessionsChecked)
	}
	if len(res.Unreadable) != 0 {
		t.Fatalf("Unreadable = %+v, want none — the explicitly selected session must not be reported unreadable", res.Unreadable)
	}
	// Evidence must carry the original proj: selector, not a bare id.
	if len(res.Findings) == 0 {
		t.Fatalf("no findings — the run-timeout check should trip")
	}
	var runTimeout *Finding
	for i := range res.Findings {
		if strings.Contains(res.Findings[i].Title, "Run-timeout") {
			runTimeout = &res.Findings[i]
		}
	}
	if runTimeout == nil {
		t.Fatalf("no run-timeout finding: %+v", res.Findings)
	}
	// SessionRefs carries the honest bare sid (the proj:<hex>:<sid>
	// form is rejected by agent-side read_transcript — finding 3).
	if len(runTimeout.Evidence.SessionRefs) != 1 || runTimeout.Evidence.SessionRefs[0] != sidA {
		t.Errorf("run-timeout SessionRefs = %v, want [%q] — the honest bare id (finding 3)", runTimeout.Evidence.SessionRefs, sidA)
	}
	// DoctorCommand carries the doctor-consumable proj: ref.
	if !strings.Contains(runTimeout.Evidence.DoctorCommand, sel) {
		t.Errorf("DoctorCommand %q must carry the doctor-consumable %q (finding 3)", runTimeout.Evidence.DoctorCommand, sel)
	}
}

// TestRunAudit_ExplicitUnsafeSelectorEmitsSafeEvidence is the roborev fix
// round 4 RED case: when an explicit --sessions proj:<unsafe-name>:<sid>
// selector is supplied (a name that passes projectTokenOK so parseSelector
// and Locate accept it, but fails safeTokenForRepro so it is unsafe in the
// comma-joined --sessions reproduction line), RunAudit reads the session
// via the user-supplied ref (honoring the selection) but must NOT emit the
// raw selector into SessionRefs or DoctorCommand — it would word-split (a
// space), comma-split (the CLI), or shell-expand ($, backtick, ;) in the
// "runnable" reproduction line. The fix derives the evidence selector from
// paths via followSelector (same safe proj:/bare form the sweep emits).
func TestRunAudit_ExplicitUnsafeSelectorEmitsSafeEvidence(t *testing.T) {
	// Bucket names that pass projectTokenOK but fail safeTokenForRepro:
	// a space (shell word-break), a '$' (shell expansion).
	for _, bucketName := range []string{"has space", "dollar$bucket"} {
		t.Run(bucketName, func(t *testing.T) {
			base := t.TempDir()
			bucket := stateHomeBucket(base, bucketName)
			writeAuditSession(t, bucket, sidA, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(sidA))

			rb := mustParseFixtureRunbook(t)
			sel := "proj:" + bucketName + ":" + sidA
			res, err := RunAudit(base, rb, AuditOpts{Sessions: []string{sel}})
			if err != nil {
				t.Fatal(err)
			}
			// The explicitly selected session must still be read — the
			// user's selection is honored via Locate.
			if res.SessionsChecked != 1 {
				t.Fatalf("SessionsChecked = %d, want 1 — the explicitly selected session must still be audited", res.SessionsChecked)
			}
			if len(res.Unreadable) != 0 {
				t.Fatalf("Unreadable = %+v, want none", res.Unreadable)
			}
			// The DoctorCommand reproduction line must be shell-safe.
			if len(res.Findings) == 0 {
				t.Fatalf("no findings — the run-timeout check should trip")
			}
			var runTimeout *Finding
			for i := range res.Findings {
				if strings.Contains(res.Findings[i].Title, "Run-timeout") {
					runTimeout = &res.Findings[i]
				}
			}
			if runTimeout == nil {
				t.Fatalf("no run-timeout finding: %+v", res.Findings)
			}
			dc := runTimeout.Evidence.DoctorCommand
			prefix := "evener doctor audit --runbook fixture-runbook --sessions "
			if !strings.HasPrefix(dc, prefix) {
				t.Fatalf("DoctorCommand = %q, want prefix %q", dc, prefix)
			}
			sessionsValue := strings.TrimPrefix(dc, prefix)
			// The --sessions value must survive the CLI's comma-split and
			// contain no shell-word-break or shell-expansion characters.
			for ref := range strings.SplitSeq(sessionsValue, ",") {
				if strings.ContainsAny(ref, ", \t\r\n$`;|&()<>=!#~\"'{}*?[]") {
					t.Errorf("DoctorCommand %q: ref %q contains a character unsafe for a shell reproduction line", dc, ref)
				}
			}
			// The evidence SessionRefs must likewise be safe.
			for _, ref := range runTimeout.Evidence.SessionRefs {
				if strings.ContainsAny(ref, ", \t\r\n$`;|&()<>=!#~\"'{}*?[]") {
					t.Errorf("SessionRefs %q contains a character unsafe for a shell reproduction line", ref)
				}
			}
		})
	}
}

// TestRunAudit_ExplicitAmbiguousUnsafeBucketOmitsNonReproducingToken is the
// roborev fix round 5 RED case: when a bucket name fails safeTokenForRepro
// (e.g. a space that word-breaks a shell line) AND the same session id
// exists in multiple such buckets, an explicit --sessions proj:<unsafe-name>:<sid>
// audit reads the session fine (Locate resolves the proj: selector via
// locateInBucket) but evidenceSel downgrades to the bare sid — which is
// ambiguous across the buckets. The DoctorCommand reproduction line carries
// that bare sid, which cannot reproduce the selected session (Locate
// returns an ambiguity error). The fix must omit the misleading token from
// DoctorCommand and disclose the non-reproducibility honestly (bucket names
// as context, never as fake refs — mirroring FU3 round 2's ambiguity-as-
// context pattern).
func TestRunAudit_ExplicitAmbiguousUnsafeBucketOmitsNonReproducingToken(t *testing.T) {
	base := t.TempDir()
	// Two buckets whose names pass projectTokenOK (so parseSelector and
	// Locate accept proj:<name>:<sid>) but fail safeTokenForRepro (space
	// word-breaks in a shell line), both containing the same session id.
	// The bare sid is ambiguous across the two buckets.
	bucketA := stateHomeBucket(base, "has space-a")
	bucketB := stateHomeBucket(base, "has space-b")
	writeAuditSession(t, bucketA, sidA, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(sidA))
	writeAuditSession(t, bucketB, sidA, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(sidA))

	rb := mustParseFixtureRunbook(t)
	// Explicitly select the session in bucketA via proj:<unsafe-name>:<sid>.
	// parseSelector accepts the space-containing name via projectTokenOK
	// (space is not a path separator or NUL), and Locate resolves it via
	// locateInBucket.
	sel := "proj:" + "has space-a" + ":" + sidA
	res, err := RunAudit(base, rb, AuditOpts{Sessions: []string{sel}})
	if err != nil {
		t.Fatal(err)
	}
	// The explicitly selected session must be audited (reading via the
	// user's selector is honored).
	if res.SessionsChecked != 1 {
		t.Fatalf("SessionsChecked = %d, want 1 — the explicitly selected session must be audited", res.SessionsChecked)
	}
	if len(res.Unreadable) != 0 {
		t.Fatalf("Unreadable = %+v, want none", res.Unreadable)
	}
	if len(res.Findings) == 0 {
		t.Fatal("no findings — the run-timeout check should trip")
	}
	var runTimeout *Finding
	for i := range res.Findings {
		if strings.Contains(res.Findings[i].Title, "Run-timeout") {
			runTimeout = &res.Findings[i]
		}
	}
	if runTimeout == nil {
		t.Fatalf("no run-timeout finding: %+v", res.Findings)
	}
	dc := runTimeout.Evidence.DoctorCommand

	// The DoctorCommand must not carry a token that cannot reproduce the
	// session. When non-empty, extract the --sessions value and verify every
	// ref resolves via Locate — the bare sid is ambiguous across two buckets
	// and would error on re-run. When all sessions are non-reproducible,
	// DoctorCommand is empty (round 9 finding 2: no # shell comment, which
	// cmd.exe ignores).
	prefix := "evener doctor audit --runbook fixture-runbook --sessions "
	if sessionsValue, ok := strings.CutPrefix(dc, prefix); ok {
		for ref := range strings.SplitSeq(sessionsValue, ",") {
			ref = strings.TrimSpace(ref)
			if ref == "" {
				continue
			}
			if _, err := Locate(base, ref); err != nil {
				t.Errorf("DoctorCommand %q: ref %q does not resolve via Locate: %v (reproduction line carries a non-reproducing token)", dc, ref, err)
			}
		}
	}
	// The non-reproducibility disclosure must live in Description prose
	// (round 9 finding 2: moved from DoctorCommand's # comment to Description).
	if !strings.Contains(runTimeout.Description, "not reproducible") && !strings.Contains(runTimeout.Description, "ambiguous") {
		t.Errorf("Description %q must disclose the non-reproducible session (bucket name is shell-unsafe, bare id is ambiguous)", runTimeout.Description)
	}
}

// TestRunAudit_AllNonReproducibleOmitsEmptySessionsFlag is the roborev fix
// round 6 finding-1 RED case: when EVERY affected session of a finding is
// non-reproducible (bare sid ambiguous across buckets), reproRefs is empty
// and strings.Join(nil, ",") yields "", so the command becomes
// "evener doctor audit --runbook X --sessions  # not reproducible: ..." —
// the --sessions flag has no value and the parser rejects it. The fix must
// omit the --sessions segment entirely (empty DoctorCommand, not a # shell
// comment — round 9 finding 2: cmd.exe does not treat # as a comment) so
// the command is never mistaken for runnable with an empty flag value.
func TestRunAudit_AllNonReproducibleOmitsEmptySessionsFlag(t *testing.T) {
	base := t.TempDir()
	// Two unsafe buckets with the same sid — explicit selection of one
	// makes it non-reproducible (bare sid ambiguous across buckets).
	bucketA := stateHomeBucket(base, "has space-a")
	bucketB := stateHomeBucket(base, "has space-b")
	writeAuditSession(t, bucketA, sidA, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(sidA))
	writeAuditSession(t, bucketB, sidA, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(sidA))

	rb := mustParseFixtureRunbook(t)
	sel := "proj:has space-a:" + sidA
	res, err := RunAudit(base, rb, AuditOpts{Sessions: []string{sel}})
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionsChecked != 1 {
		t.Fatalf("SessionsChecked = %d, want 1 — the explicitly selected session must be audited", res.SessionsChecked)
	}
	if len(res.Findings) == 0 {
		t.Fatal("no findings — the run-timeout check should trip")
	}
	var runTimeout *Finding
	for i := range res.Findings {
		if strings.Contains(res.Findings[i].Title, "Run-timeout") {
			runTimeout = &res.Findings[i]
		}
	}
	if runTimeout == nil {
		t.Fatalf("no run-timeout finding: %+v", res.Findings)
	}
	dc := runTimeout.Evidence.DoctorCommand
	// When all sessions are non-reproducible, DoctorCommand must be empty
	// (round 9 finding 2: no # shell comment, which cmd.exe ignores). When
	// non-empty, the --sessions flag must not carry an empty value (the
	// parser rejects a flag with no value).
	if dc != "" {
		runnablePrefix := "evener doctor audit --runbook fixture-runbook --sessions "
		if rest, ok := strings.CutPrefix(dc, runnablePrefix); ok {
			if strings.TrimSpace(rest) == "" {
				t.Fatalf("DoctorCommand %q has --sessions with empty value — the parser rejects a flag with no value (finding 1)", dc)
			}
		} else {
			t.Fatalf("DoctorCommand %q must be empty (all sessions non-reproducible) or a runnable command (round 9 finding 2)", dc)
		}
	}
	// The non-reproducibility disclosure must live in Description prose
	// (round 9 finding 2: moved from DoctorCommand's # comment to Description).
	if !strings.Contains(runTimeout.Description, "not reproducible") {
		t.Errorf("Description %q must disclose non-reproducibility (finding 1)", runTimeout.Description)
	}
}

// TestRunAudit_DistinctSessionsSharingSIDAcrossUnsafeBucketsEachCounted is
// the roborev fix round 6 finding-2 RED case: distinct sessions sharing one
// SID across multiple shell-unsafe buckets all downgrade to the same bare
// SID in evidenceSel, and appendUniqueString collapses them — the audit
// checked two sessions while SessionRefs (and the summary count) show one,
// and the disclosure comment names the SID once. The fix tracks each
// non-reproducible session individually (with bucket context) so the count
// and disclosure reflect both.
func TestRunAudit_DistinctSessionsSharingSIDAcrossUnsafeBucketsEachCounted(t *testing.T) {
	base := t.TempDir()
	// Two unsafe buckets with the same sid: both explicitly selected, both
	// non-reproducible (bare sid ambiguous across the two buckets).
	bucketA := stateHomeBucket(base, "has space-a")
	bucketB := stateHomeBucket(base, "has space-b")
	writeAuditSession(t, bucketA, sidA, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(sidA))
	writeAuditSession(t, bucketB, sidA, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(sidA))

	rb := mustParseFixtureRunbook(t)
	res, err := RunAudit(base, rb, AuditOpts{Sessions: []string{
		"proj:has space-a:" + sidA,
		"proj:has space-b:" + sidA,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionsChecked != 2 {
		t.Fatalf("SessionsChecked = %d, want 2 — both explicitly selected sessions must be audited", res.SessionsChecked)
	}
	if len(res.Findings) == 0 {
		t.Fatal("no findings — the run-timeout check should trip")
	}
	var runTimeout *Finding
	for i := range res.Findings {
		if strings.Contains(res.Findings[i].Title, "Run-timeout") {
			runTimeout = &res.Findings[i]
		}
	}
	if runTimeout == nil {
		t.Fatalf("no run-timeout finding: %+v", res.Findings)
	}
	// The summary count must reflect 2 sessions, not 1 (appendUniqueString
	// collapses the shared bare SID, but both sessions were audited).
	for _, s := range res.Summary {
		if strings.Contains(s.Title, "Run-timeout") && s.Sessions != 2 {
			t.Errorf("summary %q sessions = %d, want 2 — two distinct sessions sharing one SID across different unsafe buckets must each be counted (finding 2)", s.Title, s.Sessions)
		}
	}
	// The disclosure must name each session with its bucket, not the SID
	// once — two distinct sessions are non-reproducible. Round 9 finding 2
	// moved the disclosure to Description prose.
	desc := runTimeout.Description
	if !strings.Contains(desc, "has space-a") || !strings.Contains(desc, "has space-b") {
		t.Errorf("Description %q must disclose each non-reproducible session with its bucket name so two sessions sharing one SID are distinguishable (finding 2)", desc)
	}
}

// TestRunAudit_SessionRefsHonestFormForNonCanonicalBuckets verifies that
// hex-style legacy bucket names (e.g. 0123456789abcdef) — shell- and
// comma-safe but failing identifier.ValidateProjectID — produce a bare
// session id in SessionRefs (the honest session identifier and a
// doctor-CLI/human handle on this tree) while DoctorCommand keeps the
// proj:<hex>:<sid> form (the doctor CLI round-trips it fine). The
// proj:<hex>:<sid> form is rejected by agent-side read_transcript
// (ValidateProjectID fails for hex names), so it must NOT appear in
// SessionRefs. Cross-bucket agent resolution of bare sids from
// non-canonical buckets arrives when #2205 (fu-transcript-lookup)
// removes the ValidateProjectID filter from enumerateBuckets.
func TestRunAudit_SessionRefsHonestFormForNonCanonicalBuckets(t *testing.T) {
	base := t.TempDir()
	// Hex-style legacy bucket names: shell- and comma-safe, but fail
	// ValidateProjectID (no readable-<10 base62> structure).
	bucketA := stateHomeBucket(base, "0123456789abcdef")
	bucketB := stateHomeBucket(base, "fedcba9876543210")
	writeAuditSession(t, bucketA, sidA, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(sidA))
	writeAuditSession(t, bucketB, sidA, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(sidA))

	rb := mustParseFixtureRunbook(t)
	res, err := RunAudit(base, rb, AuditOpts{Since: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionsChecked != 2 {
		t.Fatalf("SessionsChecked = %d, want 2 — hex-named legacy buckets should both audit via proj: refs", res.SessionsChecked)
	}
	var runTimeout *Finding
	for i := range res.Findings {
		if strings.Contains(res.Findings[i].Title, "Run-timeout") {
			runTimeout = &res.Findings[i]
		}
	}
	if runTimeout == nil {
		t.Fatalf("no run-timeout finding: %+v", res.Findings)
	}
	// SessionRefs must carry the honest session identifier: the bare
	// session id. The proj:<hex>:<sid> form is rejected by the agent's
	// transcript tools (ValidateProjectID fails for hex names), so it must
	// NOT appear in SessionRefs (finding 3).
	for _, ref := range runTimeout.Evidence.SessionRefs {
		if strings.HasPrefix(ref, "proj:") {
			t.Errorf("SessionRef %q is a proj: ref — agent-side read_transcript rejects non-canonical project ids; SessionRefs must carry the bare session id for non-canonical buckets (finding 3)", ref)
		}
	}
	// The bare session id must be present (the sessions were audited).
	found := false
	for _, ref := range runTimeout.Evidence.SessionRefs {
		if ref == sidA {
			found = true
		}
	}
	if !found {
		t.Errorf("SessionRefs %v must contain the honest bare session id %q (finding 3)", runTimeout.Evidence.SessionRefs, sidA)
	}
	// DoctorCommand must keep the proj:<hex>:<sid> form (the doctor CLI
	// round-trips it fine — finding 3).
	dc := runTimeout.Evidence.DoctorCommand
	wantA := "proj:0123456789abcdef:" + sidA
	wantB := "proj:fedcba9876543210:" + sidA
	if !strings.Contains(dc, wantA) || !strings.Contains(dc, wantB) {
		t.Errorf("DoctorCommand %q must carry the doctor-consumable proj: refs %q and %q (finding 3)", dc, wantA, wantB)
	}
}

// TestRunAudit_DoctorCommandCappedAtEvidenceSessionRefCap is the roborev
// fix round 7 finding-1 RED case: DoctorCommand is built from
// doctorRefsBySig, which is never capped. Before round 6 it was built from
// f.Evidence.SessionRefs AFTER the evidenceSessionRefCap truncation, so it
// was bounded to 200 refs. A fleet-wide finding (evidenceSessionRefCap+5
// sessions in one canonical bucket, each with a distinct proj: ref) now
// emits a DoctorCommand with all 205 comma-joined selectors — the mid-JSON
// overflow the cap exists to prevent. The fix must cap reproRefs at
// evidenceSessionRefCap before joining, with an honest omission note.
func TestRunAudit_DoctorCommandCappedAtEvidenceSessionRefCap(t *testing.T) {
	base := t.TempDir()
	// One canonical bucket (ValidateProjectID-valid) with cap+5 distinct
	// sessions — each gets a unique proj: ref via refFor, so doctorRefsBySig
	// grows to cap+5 entries (all reproducible, no non-reproducible).
	bucket := stateHomeBucket(base, hash1)
	const extra = 5
	total := evidenceSessionRefCap + extra
	sids := make([]string, total)
	for i := range sids {
		sids[i] = newSessionsTestSID(t)
		writeAuditSession(t, bucket, sids[i], oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(sids[i]))
	}

	rb := mustParseFixtureRunbook(t)
	res, err := RunAudit(base, rb, AuditOpts{Since: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionsChecked != total {
		t.Fatalf("SessionsChecked = %d, want %d", res.SessionsChecked, total)
	}
	var runTimeout *Finding
	for i := range res.Findings {
		if strings.Contains(res.Findings[i].Title, "Run-timeout") {
			runTimeout = &res.Findings[i]
		}
	}
	if runTimeout == nil {
		t.Fatalf("no run-timeout finding: %+v", res.Findings)
	}
	dc := runTimeout.Evidence.DoctorCommand
	// DoctorCommand is a pure runnable command (round 9 finding 2: no #
	// shell comment). Extract the --sessions value directly.
	prefix := "evener doctor audit --runbook fixture-runbook --sessions "
	sessionsValue, ok := strings.CutPrefix(dc, prefix)
	if !ok {
		t.Fatalf("DoctorCommand %q missing prefix %q", dc, prefix)
	}
	sessionsValue = strings.TrimSpace(sessionsValue)
	refs := strings.Split(sessionsValue, ",")
	// DoctorCommand must be capped: at most evidenceSessionRefCap refs in
	// the --sessions value, not all cap+5.
	if len(refs) > evidenceSessionRefCap {
		t.Errorf("DoctorCommand --sessions has %d refs, want <= %d (evidenceSessionRefCap) — DoctorCommand must be bounded like SessionRefs (round 7 finding 1)", len(refs), evidenceSessionRefCap)
	}
	// The cap must be disclosed in Description prose (round 9 finding 2/3:
	// moved from DoctorCommand's # comment to Description's "…and N more"
	// marker) so the finding is not mistaken for a complete reproduction.
	if !strings.Contains(runTimeout.Description, "more") {
		t.Errorf("Description %q must disclose that reproducible refs were omitted past the cap (round 7 finding 1)", runTimeout.Description)
	}
}

func TestRunAudit_SessionsAndSinceMutuallyExclusive(t *testing.T) {
	rb := mustParseFixtureRunbook(t)
	if _, err := RunAudit(t.TempDir(), rb, AuditOpts{Sessions: []string{"x"}, Since: time.Hour}); err == nil {
		t.Fatal("want error when both --sessions and --since are given")
	}
}

// TestFinding_JSONContractShape asserts the emitted Finding JSON matches
// finding-contract.md's exact field spelling (camelCase evidence/suggestedFix),
// not this package's usual snake_case.
func TestFinding_JSONContractShape(t *testing.T) {
	base, tripSID, healthySID := auditFixture(t)
	rb := mustParseFixtureRunbook(t)
	res, err := RunAudit(base, rb, AuditOpts{Sessions: []string{tripSID, healthySID}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) == 0 {
		t.Fatal("no findings to check shape of")
	}
	b, err := json.Marshal(res.Findings[0])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"signature", "severity", "category", "title", "description", "evidence", "suggestedFix"} {
		if _, ok := m[key]; !ok {
			t.Errorf("Finding JSON missing %q: %s", key, b)
		}
	}
	evidence, _ := m["evidence"].(map[string]any)
	if _, ok := evidence["sessionRefs"]; !ok {
		t.Errorf("evidence missing sessionRefs: %s", b)
	}
	if _, ok := evidence["doctorCommand"]; !ok {
		t.Errorf("evidence missing doctorCommand: %s", b)
	}
	fix, _ := m["suggestedFix"].(map[string]any)
	if _, ok := fix["type"]; !ok {
		t.Errorf("suggestedFix missing type: %s", b)
	}
}

func TestRenderAudit_SummaryTableFindingsManualUnreadable(t *testing.T) {
	base, tripSID, healthySID := auditFixture(t)
	rb := mustParseFixtureRunbook(t)
	res, err := RunAudit(base, rb, AuditOpts{Sessions: []string{tripSID, healthySID, "02wMz5TxvEMoJEDTDGOTix"}})
	if err != nil {
		t.Fatal(err)
	}
	out := RenderAudit(res)
	for _, want := range []string{
		"fixture-runbook", "sessions_checked=2", "findings=2",
		"high", "medium", "Run-timeout jobs wasting budget", "Long identical-error tool-call run",
		"manual step", "Review any flagged session",
		"could not be read", "02wMz5TxvEMoJEDTDGOTix",
		"\"signature\"", "\"suggestedFix\"",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered audit missing %q:\n%s", want, out)
		}
	}
}

func TestRenderAudit_HealthyRunSaysSo(t *testing.T) {
	base, _, healthySID := auditFixture(t)
	rb := mustParseFixtureRunbook(t)
	res, err := RunAudit(base, rb, AuditOpts{Sessions: []string{healthySID}})
	if err != nil {
		t.Fatal(err)
	}
	out := RenderAudit(res)
	if !strings.Contains(out, "healthy") {
		t.Errorf("rendered audit for a healthy session should say so:\n%s", out)
	}
}

// TestRunAudit_R9F1_SinceUnsafeBucketCollisionAudited is the roborev round 9
// finding-1 RED case: a --since audit of two sessions sharing one SID across
// two shell-unsafe buckets (names passing projectTokenOK but failing
// safeTokenForRepro) currently yields both as Unreadable because
// followSelector falls back to the bare SID, which is ambiguous. After the
// fix, the internal read selector uses the bucket-qualified form
// (proj:<name>:<sid>) that Locate accepts, so both sessions are audited.
// DoctorCommand still uses the shell-safe emission selector (bare SID, since
// the bucket name is unsafe for a shell line).
func TestRunAudit_R9F1_SinceUnsafeBucketCollisionAudited(t *testing.T) {
	base := t.TempDir()
	// Dollar-sign bucket names: pass projectTokenOK (no path separators),
	// fail safeTokenForRepro ($ is a shell metacharacter), so followSelector
	// falls back to the bare session id. The bare id is ambiguous across
	// the two buckets, so both rows currently land Unreadable.
	bucketA := stateHomeBucket(base, "dollar$bucket-a")
	bucketB := stateHomeBucket(base, "dollar$bucket-b")
	writeAuditSession(t, bucketA, sidA, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(sidA))
	writeAuditSession(t, bucketB, sidA, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(sidA))

	// Verify Locate accepts the bucket-qualified form for these names.
	if _, err := Locate(base, "proj:dollar$bucket-a:"+sidA); err != nil {
		t.Fatalf("Locate should accept proj:dollar$bucket-a:%s but got: %v (verify-first: projectTokenOK admits $)", sidA, err)
	}
	if _, err := Locate(base, "proj:dollar$bucket-b:"+sidA); err != nil {
		t.Fatalf("Locate should accept proj:dollar$bucket-b:%s but got: %v", sidA, err)
	}

	rb := mustParseFixtureRunbook(t)
	res, err := RunAudit(base, rb, AuditOpts{Since: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	// After the fix, both sessions must be audited, not Unreadable.
	if res.SessionsChecked != 2 {
		t.Fatalf("SessionsChecked = %d, want 2 — both colliding unsafe-bucket sessions must be audited via the bucket-qualified internal read selector (round 9 finding 1)", res.SessionsChecked)
	}
	if len(res.Unreadable) != 0 {
		t.Fatalf("Unreadable = %+v, want none — the bucket-qualified read selector resolves each session unambiguously (round 9 finding 1)", res.Unreadable)
	}
}

// TestRunAudit_R9F2_DoctorCommandRunnableOrEmpty is the roborev round 9
// finding-2 RED case: the non-reproducibility disclosure uses # shell
// comments inside DoctorCommand, but Windows cmd.exe does not treat # as a
// comment. After the fix, DoctorCommand is either a pure runnable command
// or EMPTY when nothing is reproducible; the disclosure moves to the
// Description prose.
func TestRunAudit_R9F2_DoctorCommandRunnableOrEmpty(t *testing.T) {
	base := t.TempDir()
	// Two unsafe buckets with the same sid — explicit selection makes the
	// bare sid ambiguous, so all sessions are non-reproducible.
	bucketA := stateHomeBucket(base, "has space-a")
	bucketB := stateHomeBucket(base, "has space-b")
	writeAuditSession(t, bucketA, sidA, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(sidA))
	writeAuditSession(t, bucketB, sidA, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(sidA))

	rb := mustParseFixtureRunbook(t)
	sel := "proj:has space-a:" + sidA
	res, err := RunAudit(base, rb, AuditOpts{Sessions: []string{sel}})
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionsChecked != 1 {
		t.Fatalf("SessionsChecked = %d, want 1", res.SessionsChecked)
	}
	if len(res.Findings) == 0 {
		t.Fatal("no findings — the run-timeout check should trip")
	}
	var runTimeout *Finding
	for i := range res.Findings {
		if strings.Contains(res.Findings[i].Title, "Run-timeout") {
			runTimeout = &res.Findings[i]
		}
	}
	if runTimeout == nil {
		t.Fatalf("no run-timeout finding: %+v", res.Findings)
	}
	dc := runTimeout.Evidence.DoctorCommand
	// DoctorCommand must NOT start with # — cmd.exe does not treat # as a
	// comment and would attempt to execute it.
	if strings.HasPrefix(dc, "#") {
		t.Errorf("DoctorCommand %q starts with '#' — cmd.exe does not treat '#' as a comment (round 9 finding 2); must be empty when nothing is reproducible", dc)
	}
	// When all sessions are non-reproducible, DoctorCommand must be empty
	// (the disclosure moves to the Description prose).
	if dc != "" {
		t.Errorf("DoctorCommand %q must be empty when nothing is reproducible (round 9 finding 2); disclosure moves to Description", dc)
	}
	// The non-reproducibility disclosure must appear in the Description.
	if !strings.Contains(runTimeout.Description, "not reproducible") {
		t.Errorf("Description %q must disclose non-reproducibility (round 9 finding 2)", runTimeout.Description)
	}
}

// TestRunAudit_R9F2_DoctorCommandNoShellCommentInRunnable is the roborev
// round 9 finding-2 RED case for the mixed form: when some sessions are
// reproducible and some are not, the current code appends a # comment to
// the runnable command. On Windows cmd.exe, the # is not a comment — it
// breaks the command. After the fix, the # comment must not appear in
// DoctorCommand; the disclosure moves to the Description.
func TestRunAudit_R9F2_DoctorCommandNoShellCommentInRunnable(t *testing.T) {
	base := t.TempDir()
	// One safe bucket (canonical name, reproducible) + one unsafe bucket
	// with a shared sid (non-reproducible: bare sid ambiguous).
	bucketSafe := stateHomeBucket(base, hash1)
	bucketUnsafe := stateHomeBucket(base, "has space")
	writeAuditSession(t, bucketSafe, sidA, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(sidA))
	writeAuditSession(t, bucketUnsafe, sidA, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(sidA))

	rb := mustParseFixtureRunbook(t)
	res, err := RunAudit(base, rb, AuditOpts{Since: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionsChecked != 2 {
		t.Fatalf("SessionsChecked = %d, want 2", res.SessionsChecked)
	}
	var runTimeout *Finding
	for i := range res.Findings {
		if strings.Contains(res.Findings[i].Title, "Run-timeout") {
			runTimeout = &res.Findings[i]
		}
	}
	if runTimeout == nil {
		t.Fatalf("no run-timeout finding: %+v", res.Findings)
	}
	dc := runTimeout.Evidence.DoctorCommand
	// DoctorCommand must be a pure runnable command with no # comment.
	if strings.Contains(dc, "#") {
		t.Errorf("DoctorCommand %q contains '#' — cmd.exe does not treat '#' as a comment (round 9 finding 2); disclosure must move to Description, not the command", dc)
	}
	// The non-reproducibility disclosure must be in Description.
	if !strings.Contains(runTimeout.Description, "not reproducible") {
		t.Errorf("Description %q must disclose non-reproducibility (round 9 finding 2)", runTimeout.Description)
	}
}

// TestRunAudit_R9F3_DescriptionEmitsCapMarker is the roborev round 9
// finding-3 RED case: the caller truncates SessionRefs to
// evidenceSessionRefCap before calling joinSessionRefs, so the
// "…and N more" marker is never emitted in Description. A 205-session
// finding reads "…in 205 session(s): <200 refs>" with no cut marker.
// After the fix, Description is built from the pre-truncation list so the
// marker is emitted.
func TestRunAudit_R9F3_DescriptionEmitsCapMarker(t *testing.T) {
	base := t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	const extra = 5
	total := evidenceSessionRefCap + extra
	sids := make([]string, total)
	for i := range sids {
		sids[i] = newSessionsTestSID(t)
		writeAuditSession(t, bucket, sids[i], oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(sids[i]))
	}

	rb := mustParseFixtureRunbook(t)
	res, err := RunAudit(base, rb, AuditOpts{Since: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionsChecked != total {
		t.Fatalf("SessionsChecked = %d, want %d", res.SessionsChecked, total)
	}
	var runTimeout *Finding
	for i := range res.Findings {
		if strings.Contains(res.Findings[i].Title, "Run-timeout") {
			runTimeout = &res.Findings[i]
		}
	}
	if runTimeout == nil {
		t.Fatalf("no run-timeout finding: %+v", res.Findings)
	}
	// The Description must carry a cut marker when the session count exceeds
	// the cap. Currently the marker is unreachable because SessionRefs is
	// truncated before joinSessionRefs.
	if !strings.Contains(runTimeout.Description, "and ") || !strings.Contains(runTimeout.Description, " more") {
		t.Errorf("Description %q must emit a cut marker (…and N more) when session count exceeds the cap (round 9 finding 3)", runTimeout.Description)
	}
}

// TestRunAudit_R10F1_CmdExeMetacharactersRejectedFromDoctorCommand is the
// roborev round 10 finding-1 RED case: safeTokenForRepro's ContainsAny set
// lacks '%' and '^'. A bucket named "%PATH%" or "a^b" passes BOTH
// projectTokenOK (only rejects /, \, NUL) and safeTokenForRepro (no % or ^
// in the reject set), so followSelector emits proj:%PATH%:<sid> or
// proj:a^b:<sid> into DoctorCommand's --sessions value. On cmd.exe, %VAR%
// is expanded and ^ escapes the next char, silently altering the
// reproduction line. After the fix, safeTokenForRepro rejects % and ^ so
// followSelector falls back to the bare sid (non-reproducible, disclosed).
func TestRunAudit_R10F1_CmdExeMetacharactersRejectedFromDoctorCommand(t *testing.T) {
	// %VAR% and ^ are cmd.exe metacharacters that pass the current
	// safeTokenForRepro predicate.
	for _, bucketName := range []string{"%PATH%", "a^b"} {
		t.Run(bucketName, func(t *testing.T) {
			base := t.TempDir()
			bucket := stateHomeBucket(base, bucketName)
			writeAuditSession(t, bucket, sidA, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(sidA))

			rb := mustParseFixtureRunbook(t)
			res, err := RunAudit(base, rb, AuditOpts{Since: 24 * time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			if res.SessionsChecked != 1 {
				t.Fatalf("SessionsChecked = %d, want 1", res.SessionsChecked)
			}
			if len(res.Findings) == 0 {
				t.Fatalf("no findings — the run-timeout check should trip")
			}
			dc := res.Findings[0].Evidence.DoctorCommand
			// The bucket name must NOT appear in DoctorCommand: % and ^ are
			// cmd.exe metacharacters. If safeTokenForRepro accepted the name,
			// followSelector emitted proj:<bucket>:<sid> and the bucket name
			// is in the command.
			if strings.Contains(dc, bucketName) {
				t.Errorf("DoctorCommand %q contains bucket name %q with cmd.exe metacharacters — safeTokenForRepro must reject %% and ^ (round 10 finding 1)", dc, bucketName)
			}
		})
	}
}

// TestRunAudit_R10F2_EmptyDoctorCommandAlwaysSerialized is the roborev
// round 10 finding-2 RED case: the DoctorCommand field carries omitempty,
// so when all sessions are non-reproducible (DoctorCommand is ""), the
// field is dropped from JSON output entirely. finding-contract.md says
// "Always include doctorCommand" — the field must be present (empty
// string), not absent. After the fix, omitempty is dropped and the JSON
// always contains "doctorCommand":"".
func TestRunAudit_R10F2_EmptyDoctorCommandAlwaysSerialized(t *testing.T) {
	base := t.TempDir()
	// Two unsafe buckets with the same sid: all sessions non-reproducible,
	// so DoctorCommand is empty.
	bucketA := stateHomeBucket(base, "has space-a")
	bucketB := stateHomeBucket(base, "has space-b")
	writeAuditSession(t, bucketA, sidA, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(sidA))
	writeAuditSession(t, bucketB, sidA, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(sidA))

	rb := mustParseFixtureRunbook(t)
	sel := "proj:has space-a:" + sidA
	res, err := RunAudit(base, rb, AuditOpts{Sessions: []string{sel}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) == 0 {
		t.Fatal("no findings")
	}
	dc := res.Findings[0].Evidence.DoctorCommand
	if dc != "" {
		t.Fatalf("DoctorCommand = %q, want empty (all sessions non-reproducible)", dc)
	}
	// Marshal the finding and verify doctorCommand is present in JSON
	// (not omitted by omitempty when empty).
	b, err := json.Marshal(res.Findings[0])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	evidence, _ := m["evidence"].(map[string]any)
	if _, ok := evidence["doctorCommand"]; !ok {
		t.Errorf("Finding JSON %s omits doctorCommand — the field must always be present (round 10 finding 2: omitempty drops empty)", b)
	}
}

// TestRunAudit_R10F3_DescriptionSharedBudgetForReproAndNonRepro is the
// roborev round 10 finding-3 RED case: Description independently includes
// up to 200 reproducible session refs (joinSessionRefs cap) AND up to 200
// non-reproducible disclosures (formatNonReproSessions cap), so a finding
// with 150 reproducible + 100 non-reproducible sessions renders all 250
// session entries in Description. The documented cap is 200 total. After
// the fix, a single shared budget of 200 covers both, with a combined
// omission count. Finding 4 (command-side omission disclosure) also
// applies when the reproducible selector count exceeds the command cap
// but SessionRefs dedups to fewer entries.
func TestRunAudit_R10F3_DescriptionSharedBudgetForReproAndNonRepro(t *testing.T) {
	base := t.TempDir()
	// 150 reproducible sessions in a canonical bucket (hash1):
	// followSelector emits proj:hash1:<sid>, all resolve via Locate.
	reproBucket := stateHomeBucket(base, hash1)
	const reproCount = 150
	for range reproCount {
		s := newSessionsTestSID(t)
		writeAuditSession(t, reproBucket, s, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(s))
	}

	// 100 non-reproducible sessions across two unsafe buckets sharing
	// the same sid: followSelector falls back to bare sid, ambiguous
	// across buckets, non-reproducible. Each has its own sid so we
	// create 50 distinct sids, each in both buckets = 100 sessions.
	const nonReproPairs = 50
	nonReproBucketA := stateHomeBucket(base, "has space-a")
	nonReproBucketB := stateHomeBucket(base, "has space-b")
	for range nonReproPairs {
		s := newSessionsTestSID(t)
		writeAuditSession(t, nonReproBucketA, s, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(s))
		writeAuditSession(t, nonReproBucketB, s, oneCleanReadFileTurns(), fiveRunTimeoutJobsFor(s))
	}

	rb := mustParseFixtureRunbook(t)
	res, err := RunAudit(base, rb, AuditOpts{Since: 24 * time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	totalSessions := reproCount + nonReproPairs*2
	if res.SessionsChecked != totalSessions {
		t.Fatalf("SessionsChecked = %d, want %d", res.SessionsChecked, totalSessions)
	}
	var runTimeout *Finding
	for i := range res.Findings {
		if strings.Contains(res.Findings[i].Title, "Run-timeout") {
			runTimeout = &res.Findings[i]
		}
	}
	if runTimeout == nil {
		t.Fatalf("no run-timeout finding")
	}
	desc := runTimeout.Description
	// The Description must not exceed a single 200-entry budget for
	// session references. Currently joinSessionRefs emits up to 200
	// reproducible refs AND formatNonReproSessions emits up to 200
	// non-reproducible entries independently, so 150+100=250 entries
	// appear — 50 over the 200 cap with no honest omission count.
	//
	// Count how many "in bucket" entries appear in the non-repro
	// section (each non-reproducible session is "sid in bucket \"name\"").
	// Plus the reproducible refs before the non-repro section.
	// The total must not exceed evidenceSessionRefCap.
	// Count reproducible refs in Description: the comma-separated refs
	// before the "not reproducible" section (if any).
	reproPart := desc
	nonReproEntries := 0
	if idx := strings.Index(desc, "not reproducible"); idx >= 0 {
		reproPart = desc[:idx]
		nonReproEntries = strings.Count(desc[idx:], " in bucket \"")
	}
	// Count comma-separated refs in the reproducible portion.
	reproEntries := strings.Count(reproPart, ", ") + 1
	if reproPart == "" {
		reproEntries = 0
	}
	totalEntries := reproEntries + nonReproEntries
	if totalEntries > evidenceSessionRefCap {
		t.Errorf("Description has %d total session entries (%d repro + %d non-repro), want <= %d — a single shared budget must cover both reproducible and non-reproducible session references (round 10 finding 3)", totalEntries, reproEntries, nonReproEntries, evidenceSessionRefCap)
	}
}

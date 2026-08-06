package doctor

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/agent/transcript"
	"primeradiant.com/serf/llm"
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
serf-doctor transcript <selector> --health --json
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

// fiveRunTimeoutJobsFor builds five terminal, zero-output run_timeout jobs
// owned by sid -- enough to trip the fixture's `jobs.run_timeout >= 5` check.
func fiveRunTimeoutJobsFor(sid string) []jobstore.Event {
	var events []jobstore.Event
	exitTimeout := -1
	for i := 0; i < 5; i++ {
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
	for i := 0; i < 4; i++ {
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

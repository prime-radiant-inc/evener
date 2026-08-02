# Open Local Job Transcript Reads Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** Replace observer-specific job read grants with exact-reference reads of any local job transcript under the same Serf state home.

**Architecture:** Jobs receive a flag-day identifier containing their complete owner session ID and a short random suffix. read_transcript(job:...) parses that owner, checks the current project first, then performs a bounded exact-owner lookup across local sibling projects and reads a non-mutating output snapshot; every discovery and control tool keeps its existing scope. Watch delivery retains SessionMeta.ObservedBy only as best-effort UI metadata while the durable grant event, runtime callbacks, and hub grant inversion are deleted.

**Tech Stack:** Go 1.24 workspace modules, append-only JSONL jobstore, filesystem-backed output retention metadata, React/TypeScript, Vitest.

## Global Constraints

- The sole valid job ID is job_<22-character-owner-session-id>_<12-character-base62-suffix>, exactly 39 characters.
- Collision safety covers ordinary Serf concurrency and names present at the
  defined creation checks; adversarial same-user pathname substitution after
  Serf creates an artifact or between cleanup observation and removal is out of
  scope.
- This is a flag day: no old-ID parser, migration, state detection, fallback scan, or compatibility branch may remain.
- An exact job read checks the current project first; an exact current-project record wins immediately.
- Sibling lookup examines at most 256 project entries, checks only sessions/<owner-session-id>/jobs.jsonl, and returns lookup_limit_exceeded if the bounded scan cannot establish a complete answer.
- Flat or scratch state directories read only their current bucket. Remote projects and transcript providers are excluded.
- Job reads are point-in-time snapshots and never sleep, poll, block for output, or wait for completion.
- Only read_transcript broadens. job_list, job_status, job_stop, delegate_send, and job_watch retain their current owner/descendant rules.
- Watch frames carrying a structured job notification always name read_transcript(transcript_ref="job:<job-id>"); frames without a structured job do not.
- SessionMeta.ObservedBy is append-only, deduplicated, best-effort UI metadata. Whole-meta saves preserve prior entries, and delivery-time metadata work is scheduled only after the message is sent and its delivered state is durable. It never gates, delays, or alters delivery or read access.
- Every abbreviated job ID in a web or TUI summary retains all 12 random-suffix characters. Full detail and copy surfaces retain the complete ID.
- Fuzz execution belongs only to make fuzz; default test targets must remain deterministic and non-fuzz.
- Do not touch port 9180, ~/.serf/, ~/.local/state/serf/, Jesse's live askDock/layoutguard files, or notes.txt.
- Do not widen a timeout, add a sleep, use npm ci, stash, or stage a directory.
- New production code must be substantially outweighed by deletion of the grant subsystem. Stop for design reassessment if the scoped production diff is not materially net-negative.

---

### Task 1: Session-scoped job IDs and collision-safe output creation

**Files:**

- Create: identifier/job.go
- Create: identifier/job_test.go
- Modify: identifier/domains.go
- Modify: identifier/domains_test.go
- Modify: agent/internal/jobstore/record.go
- Modify: agent/internal/jobstore/output.go
- Modify: agent/internal/jobstore/output_test.go
- Modify: agent/jobs.go
- Modify: agent/job_shell.go
- Modify: agent/job_delegate.go
- Modify: the exact Go test call sites reported by the no-argument generator search in Step 6

**Interfaces:**

- Produces: identifier.NewJobID(ownerSessionID string) (string, error).
- Produces: identifier.ValidateJobID(jobID string) error.
- Produces: identifier.JobOwnerSessionID(jobID string) (string, error).
- Produces: identifier.MustNewJobID(ownerSessionID string) string.
- Produces: jobstore.CreateOutput and jobstore.CreateOutputNoSync, both using exclusive file creation.
- Produces: (*jobManager).createJobOutput() for bounded allocation of an unbound shell job ID, returning job ID, derived output path, and opened OutputStore.
- Produces: (*jobManager).createJobOutputForID(jobID string) for one exclusive open of a delegate ID already bound into child context.
- Consumes: the existing 22-character UUIDv7 session validator and OutputStore durability path.

- [ ] **Step 1: Write failing identifier tests for the exact shape**

Add identifier/job_test.go with a valid owner such as 02wMz5TxvEMoJEDTDGOTil. Drive an unexported generator with a deterministic byte reader and assert length, owner extraction, and validation:

~~~go
func TestJobIDCarriesCompleteOwnerAndRandomSuffix(t *testing.T) {
	const owner = "02wMz5TxvEMoJEDTDGOTil"
	id, err := newJobID(owner, bytes.NewReader(bytes.Repeat([]byte{0}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	if len(id) != 39 {
		t.Fatalf("len(%q) = %d, want 39", id, len(id))
	}
	if got, err := JobOwnerSessionID(id); err != nil || got != owner {
		t.Fatalf("owner = %q, err=%v", got, err)
	}
	if err := ValidateJobID(id); err != nil {
		t.Fatalf("ValidateJobID(%q): %v", id, err)
	}
}
~~~

The rejection table includes the old job_<uuid-payload> shape, invalid/truncated owner, missing separator, short/long suffix, and non-base62 suffix bytes.

- [ ] **Step 2: Run the identifier tests and confirm RED**

Run:

~~~bash
go test ./identifier -run 'TestJobIDCarriesCompleteOwnerAndRandomSuffix|TestJobIDRejectsMalformedShapes' -count=1
~~~

Expected: FAIL because the owner extractor and owner-aware generator do not exist.

- [ ] **Step 3: Implement one job-ID definition**

Move only the job domain out of identifier/domains.go into identifier/job.go. Keep other domain IDs unchanged. Use crypto/rand.Reader and rejection sampling against the existing base62 alphabet.

~~~go
const (
	jobIDPrefix     = "job_"
	jobIDSuffixSize = 12
	jobIDSize       = len(jobIDPrefix) + base62Width + 1 + jobIDSuffixSize
)

func NewJobID(ownerSessionID string) (string, error) {
	return newJobID(ownerSessionID, rand.Reader)
}

func JobOwnerSessionID(jobID string) (string, error) {
	if err := ValidateJobID(jobID); err != nil {
		return "", err
	}
	return jobID[len(jobIDPrefix) : len(jobIDPrefix)+base62Width], nil
}
~~~

The job manager already receives a valid production session ID, so generation need not redundantly repair or normalize its caller. ValidateJobID and JobOwnerSessionID must validate the embedded owner before any filesystem access. Do not retain a no-argument or legacy-shape generator.

- [ ] **Step 4: Write failing exclusive-create and collision tests**

In output_test.go, table-test every path removed by RemoveOutputArtifacts: the log, final metadata, final metadata temporary file, pending metadata, and pending metadata temporary file. For each case, create only that artifact, prove CreateOutput refuses the candidate, and prove the artifact remains byte-identical. In jobs_test.go, inject a generator that yields an occupied ID and then a fresh ID; assert createJobOutput chooses the fresh ID and leaves the occupied artifact byte-identical.

In delegate tests, bind an occupied ID before subagent preparation, reach the attach path, and prove the exclusive open fails without changing the occupied artifact, leaking a new output sidecar, or leaving the prepared subagent/worktree alive. Add a focused pre-durable-start failure table for shell and delegate paths: after an opened output fails before EventJobStarted is appended, every path in RemoveOutputArtifacts is absent. Once EventJobStarted is durable, forward/finalization failures retain artifacts as evidence.

~~~go
func TestCreateJobOutputRetriesWithoutOverwritingCollision(t *testing.T) {
	const owner = "02wMz5TxvEMoJEDTDGOTil"
	jm, err := newJobManagerNoSync(t.TempDir(), owner, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = jm.store.Close() })
	first := "job_" + owner + "_000000000000"
	second := "job_" + owner + "_000000000001"
	occupied := filepath.Join(jm.dir, "jobs", first+".log")
	if err := os.WriteFile(occupied, []byte("keep me\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ids := []string{first, second}
	jm.newJobID = func(string) (string, error) {
		id := ids[0]
		ids = ids[1:]
		return id, nil
	}
	jobID, _, output, err := jm.createJobOutput()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = output.Close() })
	if jobID != second {
		t.Fatalf("job id = %q, want %q", jobID, second)
	}
	if got, _ := os.ReadFile(occupied); string(got) != "keep me\n" {
		t.Fatalf("occupied artifact changed: %q", got)
	}
}
~~~

Use a valid test session fixture. Do not add production behavior that tolerates an invalid session ID merely to preserve old test labels.

- [ ] **Step 5: Run the allocation tests and confirm RED**

~~~bash
go test ./agent/internal/jobstore -run 'TestCreateOutput' -count=1
go test ./agent -run 'TestCreateJobOutputRetriesWithoutOverwritingCollision|TestAttachDelegateJobCollisionDoesNotOverwriteOrLeakArtifacts|TestPreDurableJobStartFailureRemovesOutputArtifacts' -count=1
~~~

Expected: FAIL because output creation still uses create-or-append and jobManager has no collision-aware allocator.

- [ ] **Step 6: Implement collision-safe allocation at all three creation sites**

Refactor the output opener around an internal flags parameter. OpenOutput keeps reopen semantics. CreateOutput must preserve fs.ErrExist through wrapping and treat any pre-existing path in RemoveOutputArtifacts' five-path set as a collision without overwriting it. Perform one genuine non-following preflight across all five names, then reserve the log with O_CREATE|O_EXCL|O_RDWR|O_APPEND as the same-ID serialization point, reuse normal metadata persistence, and remove the five-path set if initialization fails. Add createOutput and newJobID fields to jobManager, populated by sync and no-sync constructors.

createJobOutput performs a small bounded retry for shell jobs whose ID has not escaped. For each candidate it checks the durable record map first, then calls exclusive CreateOutput on the derived path. It retries only record/artifact collisions; random-source, store, and ordinary filesystem errors return immediately. Exhaustion returns a named error.

createJobOutputForID validates the caller's exact owner-bound ID, checks the durable record map, derives the output path, and calls exclusive CreateOutput exactly once. A collision is returned without selecting a replacement because the delegate child context already names this ID.

In startDelegate, generate the ID before worktree/subagent preparation and put it in ctxParentJobID, but do not create any output artifact yet. The attach path calls createJobOutputForID only after preparation. If it collides, return the attach error and let the existing caller cancel and close the prepared child and roll back its worktree; never substitute a different ID after the child has observed the original. Delegate/watch helper paths that generate their ID internally must use the manager's owner-aware generator and propagate generation errors.

Replace job allocation in jobs.go, job_shell.go, and job_delegate.go. Every error after an output is created but before EventJobStarted is durably appended must close it and call RemoveOutputArtifacts, not os.Remove. After EventJobStarted is durable, retain output artifacts on later failures as evidence. Change agent/internal/jobstore/record.go to require ownerSessionID and update every test call with the owner already in its fixture.

Run the completeness search:

~~~bash
rg -n 'jobstore\.NewJobID\(\)|identifier\.NewJobID\(\)|MustNewJobID\(\)' --glob '*.go'
~~~

Expected: no output and exit 1.

- [ ] **Step 7: Verify GREEN and mutation-test both contracts**

~~~bash
go test ./identifier -count=1
go test ./agent/internal/jobstore -run 'TestCreateOutput' -count=1
go test ./agent -run 'TestCreateJobOutputRetriesWithoutOverwritingCollision|TestAttachDelegateJobCollisionDoesNotOverwriteOrLeakArtifacts|TestPreDurableJobStartFailureRemovesOutputArtifacts|TestShell|TestDelegate' -count=1
~~~

Temporarily shorten JobOwnerSessionID's slice by one character and confirm the owner test fails; restore it. Temporarily remove O_EXCL and confirm the log preservation test fails; restore it. Temporarily bypass the sidecar collision check and confirm the corresponding table rows fail; restore it. Temporarily replace the delegate exact-ID open with the retry allocator and confirm the bound-ID collision test fails; restore it.

- [ ] **Step 8: Commit the slice**

Stage the named production and focused test files individually. Use git status --short to identify signature-only test edits and run git add once per exact filename. Never stage a directory.

~~~bash
git commit -m "feat: scope job identifiers to owner sessions"
~~~

### Task 2: Non-mutating output snapshots with one immediate retry

**Files:**

- Create: agent/internal/jobstore/output_snapshot.go
- Create: agent/internal/jobstore/output_snapshot_test.go

**Interfaces:**

- Produces: jobstore.OutputSnapshot with Content, TotalBytes, RetainedStart, and Truncated.
- Produces: jobstore.ReadOutputSnapshot(path string, maxBytes int, fromHead bool) (jobstore.OutputSnapshot, error).
- Produces: jobstore.ErrOutputChangedDuringRead.
- Consumes: existing metadata validation, rune-edge trimming, and ErrInvalidLimit.

- [ ] **Step 1: Write failing snapshot and retry tests**

Cover a full tail, truncated head, retention-pruned file, malformed metadata, missing artifact, one changed attempt followed by success, and two changed attempts followed by ErrOutputChangedDuringRead.

~~~go
func TestReadOutputSnapshotRetriesOneChangedAttempt(t *testing.T) {
	attempts := 0
	got, err := readOutputSnapshotWithRetry(func() (OutputSnapshot, error) {
		attempts++
		if attempts == 1 {
			return OutputSnapshot{}, errOutputChanged
		}
		return OutputSnapshot{Content: []byte("stable\n"), TotalBytes: 7}, nil
	})
	if err != nil || attempts != 2 || string(got.Content) != "stable\n" {
		t.Fatalf("snapshot=%+v attempts=%d err=%v", got, attempts, err)
	}
}
~~~

- [ ] **Step 2: Confirm RED**

~~~bash
go test ./agent/internal/jobstore -run 'TestReadOutputSnapshot' -count=1
~~~

Expected: FAIL because the snapshot API does not exist.

- [ ] **Step 3: Implement the minimal read-only helper**

One attempt reads validated output metadata, reads the requested retained window without opening a Store or append-capable file, then re-reads the validated total/retained-start pair. A changed pair or inconsistent retained length returns private errOutputChanged.

ReadOutputSnapshot calls that attempt at most twice, immediately. It contains no timer, sleep, or loop waiting on external state. Apply the same runetrim edge handling as OutputStore.Head/Tail. Truncated is true when retention dropped a prefix or the requested window omitted retained bytes.

- [ ] **Step 4: Verify GREEN and mutation-test retry**

~~~bash
go test ./agent/internal/jobstore -run 'TestReadOutputSnapshot|TestOutputFileStats|TestOutputStore.*Window' -count=1
~~~

Temporarily reduce the retry count to one and confirm the one-change test fails; restore it. Temporarily skip the post-read metadata comparison and confirm the consistency test fails; restore it.

- [ ] **Step 5: Commit**

~~~bash
git add agent/internal/jobstore/output_snapshot.go agent/internal/jobstore/output_snapshot_test.go
git commit -m "feat: read stable job output snapshots"
~~~

If output.go changed, stage that exact file before committing.

### Task 3: Bounded exact-owner lookup for read_transcript

**Files:**

- Create: agent/job_transcript_read.go
- Create: agent/job_transcript_read_test.go
- Modify: agent/session_tools_transcript.go
- Modify: agent/session_tools_transcript_job_read_test.go
- Modify: agent/session_tools_jobs.go
- Modify: agent/session_tool_registry.go
- Modify: agent/transcript_test.go
- Modify: agent/transcript_read_tools_exact_fuzz_test.go only to update its compile-time fixture; do not add default fuzz execution

**Interfaces:**

- Consumes: identifier.JobOwnerSessionID, jobstore.ReadEvents, jobstore.Fold, and jobstore.ReadOutputSnapshot.
- Produces: locateLocalJob(currentStateDir, jobID) returning state directory, owner session, and folded record.
- Produces: readLocalJobSnapshot(currentStateDir, jobID, readBytes) returning the existing jobReadOutputSnapshot shape.
- Preserves: readMarkdownEnvelope and the shell/delegate renderers.

- [ ] **Step 1: Write failing bounded-lookup tests**

Use valid project/session IDs beneath t.TempDir. Add these named cases:

~~~go
func TestLocateLocalJobCurrentProjectWins(t *testing.T) {}
func TestLocateLocalJobFindsExactOwnerInSiblingProject(t *testing.T) {}
func TestLocateLocalJobRejectsAmbiguousSiblingOwners(t *testing.T) {}
func TestLocateLocalJobReturnsLimitExceededEvenAfterPartialMatch(t *testing.T) {}
func TestLocateLocalJobFlatStateDirDoesNotSearchSiblings(t *testing.T) {}
func TestLocateLocalJobDoesNotReadUnrelatedSessionStores(t *testing.T) {}
func TestReadLocalJobSnapshotIgnoresPersistedAbsoluteOutputPath(t *testing.T) {}
~~~

Drive the sibling iterator through an unexported directory-reader seam so entry order is deterministic without depending on filesystem ordering. The limit case supplies 257 valid sibling entries, puts one match inside the processed prefix, and still expects lookup_limit_exceeded because an unvisited duplicate is possible. The exact-owner test places corrupt stores under unrelated session IDs and proves they are never opened.

- [ ] **Step 2: Write failing real-tool owner/foreign tests**

Replace grant fixtures in session_tools_transcript_job_read_test.go with two project buckets under one temporary state home. Drive execReadTranscript and prove owner and foreign reads return the same durable status/reason, output, total/dropped/truncation metadata, and delegate structured result for both running and terminal jobs.

Also prove an old ID is rejected before I/O, a decoy absolute OutputPath is ignored, middle-line corruption errors, and a trailing partial event is tolerated.

~~~go
value, err := execReadTranscript(&toolDeps{
	stateDir:  callerProject,
	sessionID: callerSessionID,
}, map[string]any{"transcript_ref": "job:" + foreignJobID})
~~~

- [ ] **Step 3: Confirm the preserved behavioral RED**

~~~bash
go test ./agent -run 'TestLocateLocalJob|TestReadLocalJobSnapshot|TestReadTranscript.*LocalJob' -count=1
~~~

Expected: the foreign exact-reference read returns the existing not-found error, while locator tests fail because no bounded locator exists.

- [ ] **Step 4: Implement current-first, streaming bounded lookup**

Validate/extract the owner before filesystem access. Check only the current project's exact owner store and return immediately on an exact record.

For siblings, open the projects directory and use the directory handle's bounded ReadDir method behind the narrow test seam from Step 1. Do not use filepath.Glob, os.ReadDir, or any helper that materializes the whole directory. Count every raw sibling entry toward the 256-entry bound, skip the already-checked current project, reject non-directory, symlink, and syntactically invalid project entries, and use one bounded sentinel read only to determine whether unexamined entries remain. Return ambiguity immediately on a second exact record. If the bound is reached with zero or one match and another entry remains, return lookup_limit_exceeded. At EOF return not found or the sole match.

Require rec.JobID and rec.OwnerSessionID to match the validated coordinates. Derive output from those coordinates:

~~~go
outputPath := filepath.Join(
	jobsDir(location.StateDir, location.OwnerSessionID),
	"jobs",
	jobID+".log",
)
~~~

Never use rec.OutputPath on this path.

- [ ] **Step 5: Connect only read_transcript to the new reader**

Change the toolDeps job-read closure to call readLocalJobSnapshot with the session state directory. If removing the closure deletes more code cleanly, call the same helper directly from readJobTranscript instead.

Do not route job_status, job_list, job_stop, job_watch, delegate_send, or the unregistered historical jobReadOutputTool through locateLocalJob. Remove only the grant arm from resolveJobRead in Task 5; retain its local/descendant behavior for internal callers.

Validate terminal JobRecord.OutputBytes against snapshot TotalBytes. Preserve the current markdown envelope and structured result. Keep malformed ID, not-found, ambiguity, lookup-limit, missing/pruned output, corrupt record/metadata, and repeated-race errors distinguishable. Map the snapshot sentinel to output_changed_during_read and do not retry elsewhere.

- [ ] **Step 6: Verify GREEN and mutation-test all lookup boundaries**

~~~bash
go test ./agent -run 'TestLocateLocalJob|TestReadLocalJobSnapshot|TestReadTranscript.*Job|TestReadTranscriptRejectsJobOnlyParams' -count=1
~~~

Perform and restore these mutations one at a time:

- reverse current-project precedence;
- scan a session directory instead of opening the exact owner path;
- return a partial match at the 256-entry boundary;
- trust rec.OutputPath.

Each corresponding test must fail.

- [ ] **Step 7: Commit**

~~~bash
git add agent/job_transcript_read.go agent/job_transcript_read_test.go agent/session_tools_transcript.go agent/session_tools_transcript_job_read_test.go agent/session_tools_jobs.go agent/session_tool_registry.go agent/transcript_test.go agent/transcript_read_tools_exact_fuzz_test.go
git commit -m "feat: read exact local job transcripts"
~~~

### Task 4: Remove hub grant inversion and preserve distinguishing ID suffixes

**Files:**

- Create: agent/historical_jobs.go from retained non-grant code in agent/observer_grants.go
- Delete: agent/observer_grants.go
- Modify or rename: agent/cov_s1_observer_grants_test.go, retaining only historical-record tests
- Delete: agent/observer_grants_test.go
- Modify: cmd/serf-hub/internal/hubcore/past.go
- Delete: cmd/serf-hub/internal/hubcore/past_observers_test.go
- Modify: cmd/serf-hub/web_workspace.go
- Modify: cmd/serf-hub/web_test.go
- Modify: identifier/job.go
- Modify: identifier/job_test.go
- Modify: cmd/serf-tui/hub_status.go
- Modify: cmd/serf-tui/hub_status_test.go
- Modify: cmd/serf-tui/internal/msgrender/tool_renderers.go
- Modify: cmd/serf-tui/internal/msgrender/tool_renderers_test.go
- Modify: cmd/serf-tui/internal/msgrender/tool_bodies.go
- Modify: cmd/serf-tui/internal/msgrender/tool_bodies_test.go
- Modify: cmd/serf-hub/frontend/src/panes/session/transcript/tools/helpers.ts
- Modify: cmd/serf-hub/frontend/src/panes/session/transcript/tools/helpers.test.ts
- Modify: cmd/serf-hub/frontend/src/panes/session/transcript/tools/jobTools.tsx
- Modify: cmd/serf-hub/frontend/src/panes/session/transcript/tools/jobTools.test.tsx
- Modify: cmd/serf-hub/frontend/src/panes/session/transcript/tools/readTranscript.tsx
- Modify: cmd/serf-hub/frontend/src/panes/session/transcript/tools/readTranscript.test.tsx

**Interfaces:**

- Preserves: LoadSessionHistoricalJobRecords.
- Deletes: LoadSessionObserverGrants, PastIndex.observers, PastIndex.ObserversOf, project grant folding, and grant-history union.
- Produces: identifier.AbbreviateJobID and the matching frontend clipJobID, retaining all 12 suffix characters in every abbreviated job summary.
- Consumes: only SessionMeta.ObservedBy for auto-open.

- [ ] **Step 1: Pin the surviving hub source and write failing UI tests**

Retain tests proving live and ended IDs from SessionMeta.ObservedBy reach ObserverRouteIDs. Delete grant-history-only fixtures. Add an assertion that empty ObservedBy produces no observer routes even when unrelated jobs.jsonl exists.

For Vitest, drive the registered job_status renderer summary with two IDs sharing an owner:

~~~ts
const owner = "02wMz5TxvEMoJEDTDGOTil";
const first = "job_" + owner + "_000000000001";
const second = "job_" + owner + "_000000000002";
const d = toolRendererFor("job_status");
const summary = (jobID: string) =>
  d.summary(
    item({
      toolName: "job_status",
      argumentsJSON: JSON.stringify({ job_id: jobID }),
      output: "",
    }),
  );

expect(summary(first)).toContain("000000000001");
expect(summary(second)).toContain("000000000002");
expect(summary(first)).not.toBe(summary(second));
~~~

Add the same distinction assertions for job_stop and read_transcript job-log summaries. Test the shared helper through its existing helpers test module rather than exporting a file-local implementation solely for testing.

In identifier/job_test.go, prove AbbreviateJobID leaves short strings unchanged and renders both complete 12-character suffixes for two valid 39-character job IDs sharing an owner. Drive the TUI hub-status, job-control target, delegate-body job label, and SubagentRun job label with those same IDs and assert their abbreviated renderings differ and contain the complete suffix. Delegate identifiers continue to use the existing generic prefix abbreviation.

- [ ] **Step 2: Confirm frontend RED and pin forward metadata GREEN**

~~~bash
go test ./cmd/serf-hub -run 'TestWeb_WorkspaceData_.*Observer' -count=1
go test ./identifier ./cmd/serf-tui ./cmd/serf-tui/internal/msgrender -run 'Test.*Job.*Abbreviat|Test.*Job.*Suffix' -count=1
(cd cmd/serf-hub/frontend && npm test -- src/panes/session/transcript/tools/helpers.test.ts src/panes/session/transcript/tools/jobTools.test.tsx src/panes/session/transcript/tools/readTranscript.test.tsx)
~~~

Expected: suffix assertions fail because the TUI and job tools use prefix clipping and read_transcript does not abbreviate its job-log target. Forward ObservedBy tests should already pass and protect the source being retained.

- [ ] **Step 3: Remove historical grant inversion**

Move the retained historical record loader without rewriting it. Delete grant loaders and worker-resolution helpers.

Remove PastIndex's observer map, rebuild accumulation, session-directory scan, dedup helper, and ObserversOf. Change fillObserverLink to consume only a copied/deduplicated m.ObservedBy slice while preserving live/past filtering. Add no replacement index.

- [ ] **Step 4: Implement suffix-preserving clipping**

Keep generic clip and shortID unchanged for non-job identifiers. Add identifier.AbbreviateJobID(jobID, max) beside the canonical job-ID constants. At the 26-character summary budget it returns a prefix, one ellipsis, and the complete 12-character suffix; at or under budget it returns the input unchanged. Use it in hub status, job-control targets, delegate-body job labels, and SubagentRun job labels. Continue using shortID for delegate IDs.

Add the matching shared frontend helper in helpers.ts:

~~~ts
export function clipJobID(jobID: string): string {
  const max = 26;
  if (jobID.length <= max) return jobID;
  const suffix = jobID.slice(-12);
  const prefixLength = max - suffix.length - 1;
  return jobID.slice(0, prefixLength) + "…" + suffix;
}
~~~

Use it for job_status, job_stop, and read_transcript's job-log target summary. Delegate IDs keep generic clipping. Detail/copy surfaces and job-list rows retain full IDs.

- [ ] **Step 5: Verify and mutation-test**

~~~bash
go test ./agent -run 'TestS1Cov_LoadSessionHistoricalJobRecords' -count=1
go test ./cmd/serf-hub/internal/hubcore -count=1
go test ./cmd/serf-hub -run 'TestWeb_WorkspaceData_.*Observer' -count=1
go test ./identifier ./cmd/serf-tui ./cmd/serf-tui/internal/msgrender -run 'Test.*Job.*Abbreviat|Test.*Job.*Suffix' -count=1
(cd cmd/serf-hub/frontend && npm test -- src/panes/session/transcript/tools/helpers.test.ts src/panes/session/transcript/tools/jobTools.test.tsx src/panes/session/transcript/tools/readTranscript.test.tsx)
~~~

Temporarily restore head-only job clipping in each shared Go/TypeScript helper and confirm the corresponding suffix tests fail; restore. Temporarily ignore ObservedBy and confirm the forward-source test fails; restore.

- [ ] **Step 6: Commit**

Stage the exact created, modified, renamed, and deleted files individually, then:

~~~bash
git commit -m "refactor: derive observers only from session metadata"
~~~

### Task 5: Delete read grants while preserving lossless watch annotations

**Files:**

- Modify: agent/schema/snapshot.go
- Create: agent/schema/snapshot_observed_by_test.go
- Modify: agent/internal/jobstore/event.go
- Modify: agent/internal/jobstore/fold.go
- Modify: agent/internal/jobstore/record.go
- Modify: agent/internal/jobstore/store.go
- Modify: grant-related cases in agent/internal/jobstore/*_test.go and *_fuzz_test.go
- Modify: agent/jobs.go
- Modify: agent/session_config.go
- Modify: agent/subagents.go
- Modify: agent/session_tools_jobs.go
- Modify: agent/job_watch.go
- Modify: agent/job_watch_observer_test.go
- Modify: agent/job_watch_test.go
- Modify: agent/job_watch_loopguard_test.go
- Modify: agent/job_watch_timers_observe_fuzz_test.go
- Modify: agent/watch_observer_fuzz_test.go
- Delete: agent/job_read_recovery_grant_fuzz_test.go
- Delete: agent/watch_grant_lifecycle_fuzz_test.go

**Interfaces:**

- Deletes: EventWatchReadGrant, ObserverSessionID, FoldGrants, LoadGrants, grantedJobRead, lookupGrantedJobRead, parentGrantedJobRead, jobStatusDeniedError, watchGrantKey, and grantsMinted.
- Produces: schema.AppendSessionObservedBy(dir, workerSessionID, observerSessionID string) error, serialized with whole SessionMeta saves so existing observer links cannot be lost.
- Produces: appendWatchFrameJobRead for every structured job notification.
- Produces: WatchSendState.NotificationJobID and NotificationDelegateID, which carry structured frame identity across durable delivery retries but confer no permission.
- Preserves: install-time best-effort ObservedBy stamping and schedules delivery-time stamping only after successful delivery settlement.
- Preserves: watch delivery, coalescing, provenance, runaway protection, and scoped controls.

- [ ] **Step 1: Write failing lossless-frame and metadata tests**

Add focused tests:

~~~go
func TestStructuredJobNotificationAlwaysNamesTranscriptRead(t *testing.T) {}
func TestNonJobWatchFrameNamesNoTranscriptRead(t *testing.T) {}
func TestConcreteDelegateWatchStampsObservedByAtInstall(t *testing.T) {}
func TestSessionWatchStampsDeliveredWorkerObservedBy(t *testing.T) {}
func TestSessionWatchSchedulesObservedByAfterDeliveredState(t *testing.T) {}
func TestSessionWatchSkipsShellAndSelfObserverLinks(t *testing.T) {}
func TestObservedByWriteFailureDoesNotChangeDelivery(t *testing.T) {}
~~~

The first case forces the old grant append to fail but still expects the canonical read line. The non-job case uses communicate data. Rename and retain the existing install-time ObservedBy dedup regression; do not replace it with an implementation-string assertion.

The delivery-order test injects a scheduler that captures but does not run the metadata closure. Its fake sender records delivery, and the scheduler asserts the durable pending fold is already settled before accepting the closure. deliverPendingWatchSend must return delivered without executing the captured metadata closure; the test then runs it explicitly and inspects only temporary SessionMeta files. Exercise the same state through the persisted-pending restore path so the structured notification identity is not an in-memory-only convenience. A metadata write failure occurs inside the captured closure after delivery and cannot change the returned delivery result. Use channels/callback seams for positive handoffs only; add no sleeps or timeout-based races.

In agent/schema, add:

~~~go
func TestSaveSessionMetaPreservesObservedBy(t *testing.T) {}
func TestAppendSessionObservedByPreservesFieldsAndDeduplicates(t *testing.T) {}
func TestSessionMetaWritesSerializeObserverAppend(t *testing.T) {}
~~~

The first writes an observer link, then performs the same whole-meta save that Session.maybeAutoSave performs with ObservedBy absent; the link must survive while the new turn/name/usage fields win. The second appends twice to an existing rich meta and proves one observer entry plus byte-for-value preservation of unrelated fields.

The serialization test uses two blocking-rename afero wrappers over one MemMapFs. Seed a rich meta, start SaveSessionMetaWithFS with updated ordinary fields, wait on its positive "entered rename" channel, and prove the package write mutex is held with TryLock. Start appendSessionObservedByWithFS against the second wrapper while the save is paused, then release and await the save. Next await the append wrapper's rename hook, again prove that the same mutex is held, release it, and await completion. The final meta must contain the save's new ordinary fields and the observer union exactly once. Every handoff is a channel receive caused by the operation being tested; use no sleep, polling loop, or timeout assertion. Buffered hook channels and cleanup releases must let a broken implementation fail rather than strand its goroutines.

- [ ] **Step 2: Confirm RED**

~~~bash
go test ./agent/schema -run 'TestSaveSessionMetaPreservesObservedBy|TestAppendSessionObservedByPreservesFieldsAndDeduplicates|TestSessionMetaWritesSerializeObserverAppend' -count=1
go test ./agent -run 'TestStructuredJobNotificationAlwaysNamesTranscriptRead|TestNonJobWatchFrameNamesNoTranscriptRead|TestConcreteDelegateWatchStampsObservedByAtInstall|TestSessionWatchStampsDeliveredWorkerObservedBy|TestSessionWatchSchedulesObservedByAfterDeliveredState|TestSessionWatchSkipsShellAndSelfObserverLinks|TestObservedByWriteFailureDoesNotChangeDelivery' -count=1
~~~

Expected: the forced-grant-failure frame omits its read line, the save-after-stamp test erases ObservedBy, the append helper does not exist, and delivery-time metadata remains coupled to minting before pending persistence.

- [ ] **Step 3: Make ObservedBy append-only across whole-meta saves**

Put the SessionMeta read/merge/write sequence behind one package-level write mutex shared by SaveSessionMeta, SaveSessionMetaWithFS, and AppendSessionObservedBy. Keep the existing atomic temp-file/rename implementation below that lock; do not add a second persistence format or lock file. Factor an unexported appendSessionObservedByWithFS for the deterministic test; the exported OS-filesystem wrapper remains the only new production API.

Before a whole-meta save, load the current file if it exists and stable-union its ObservedBy entries with the incoming entries, preserving prior links and deduplicating both slices. A missing file remains an ordinary first save; any other read error is returned. AppendSessionObservedBy loads the current meta and changes only the stable-deduplicated ObservedBy slice under the same lock before calling the internal save function. This prevents a watch append from clobbering unrelated fields and prevents the next Session.maybeAutoSave snapshot, whose Session.Meta currently has no observer state, from erasing the append.

Change the SessionMeta field comment so it describes UI relationships, not read grants. jobManager receives an appendObservedBy function seeded with schema.AppendSessionObservedBy; focused tests may replace that one seam.

- [ ] **Step 4: Make structured frame annotation unconditional and metadata post-delivery**

Rename watchGrantableJob by its new domain purpose, such as watchFrameJob. In recordWatchSend, independently of target resolution and before pending persistence:

~~~go
notificationJobID, notificationDelegateID := "", ""
if jobID, delegateID, ok := watchFrameJob(d.eventData); ok {
	d.frame = appendWatchFrameJobRead(d.frame, jobID)
	notificationJobID, notificationDelegateID = jobID, delegateID
}
state = jm.watchSendState(d, target)
state.NotificationJobID = notificationJobID
state.NotificationDelegateID = notificationDelegateID
~~~

Persist the two notification fields with the pending send. The annotation applies to shell, delegate, and self-callback structured job notifications because it grants nothing, and it is formed independently of route resolution. There is no SessionMeta read or write in recordWatchSend.

Add a jobManager scheduleObserverLink seam whose production implementation launches its closure asynchronously. In the watchSendDelivered arm, first recheck currency and durably settle EventWatchSendDelivered exactly as today. Only after settlement succeeds, schedule a closure from the persisted WatchSendState. That closure stamps only a resolvable delegate worker for a session-level watch with a resolved observer session, suppresses shell jobs and self-links, and swallows every resolve/write error. Busy, failed, dropped, and unsettled sends schedule nothing. The scheduler itself performs no metadata I/O, so a slow or failed metadata file cannot delay message delivery or durable settlement.

Keep concrete delegate install-time stamping, routed through appendObservedBy. Rename watchReadGrantObserver to watchObserverSessionID; it may resolve a dlg_ target from durable delegate records but cannot create or check permission.

- [ ] **Step 5: Delete runtime grant plumbing from the outside inward**

Delete the child callback and assignment, the granted resolution arm, grant-aware job_status error, granted snapshot, mint/dedup functions, and grant-only tests. Compile after each deletion cluster:

~~~bash
go test ./agent -run 'TestStructuredJobNotification|TestSessionWatch|TestJobStatus' -count=1
go test -tags serffuzz ./agent -run '^$'
~~~

- [ ] **Step 6: Delete durable grant events and folds**

Remove the event kind/field, FoldGrants, Store.LoadGrants, and grant arms from mixed fuzz models. Delete grant-only fuzz targets rather than translating them into compatibility tests. Preserve unrelated cases in mixed fuzz programs.

~~~bash
rg -n 'watch_read_grant|EventWatchReadGrant|ObserverSessionID|FoldGrants|LoadGrants|LoadSessionObserverGrants|parentGrantedJobRead|grantedJobRead|lookupGrantedJobRead|jobStatusDeniedError|appendWatchReadGrant|mintWatchSendReadGrant|watchReadGrantObserver|watchGrantableJob|appendWatchFrameGrantedRead|grantsMinted|watchGrantKey' agent cmd/serf-hub --glob '*.go'
~~~

Expected: no output and exit 1.

- [ ] **Step 7: Verify and mutation-test the frame and both metadata paths**

~~~bash
go test ./agent/schema -run 'TestSaveSessionMetaPreservesObservedBy|TestAppendSessionObservedByPreservesFieldsAndDeduplicates|TestSessionMetaWritesSerializeObserverAppend' -count=1
go test ./agent/internal/jobstore -count=1
go test ./agent -run 'Test.*Watch|Test.*ObservedBy|TestReadTranscript|TestJobStatus' -count=1
go test -tags serffuzz ./agent -run '^$'
~~~

Temporarily skip the frame annotation, the whole-save ObservedBy merge, the install stamp, and the post-settlement schedule one at a time. Each focused test must fail; restore every mutation. Temporarily execute the metadata closure inline before send and confirm the delivery-order test fails; restore it. Remove the lock from each SessionMeta write path in turn and confirm the deterministic interleaving test fails at the lock assertion or final state; restore it after each mutation.

- [ ] **Step 8: Commit**

Stage every exact modified/deleted filename individually, including mixed fuzz files, then:

~~~bash
git commit -m "refactor: remove observer job read grants"
~~~

### Task 6: Prove scope, flag-day cleanup, and net-negative complexity

**Files:**

- Modify: agent/job_transcript_read_test.go
- Modify: agent/session_tools_transcript_job_read_test.go

**Interfaces:**

- Consumes: Tasks 1-5.
- Produces: an end-to-end regression proving exact foreign reads grant no discovery/control.
- Produces: no old identifier/grant vocabulary and a materially net-negative production diff relative to design commit 8be010b2d.

- [ ] **Step 1: Write the cross-tool capability regression**

Build a foreign project with a running delegate and retained output. From a sibling-project caller, prove read_transcript succeeds. Then invoke real handlers and assert:

~~~go
func TestForeignTranscriptReadDoesNotBroadenJobTools(t *testing.T) {
	value, err := execReadTranscript(&toolDeps{
		stateDir: callerProject, sessionID: callerSessionID,
	}, map[string]any{"transcript_ref": "job:" + foreignJobID})
	if err != nil {
		t.Fatalf("foreign read: %v", err)
	}
	envelope := value.(readMarkdownEnvelope)
	if !strings.Contains(envelope.Content, "FOREIGN_MARKER") {
		t.Fatalf("content = %q", envelope.Content)
	}

	listedValue, err := jobListTool(caller, map[string]any{}, jobToolResultDefaultMaxChar)
	if err != nil {
		t.Fatalf("job_list: %v", err)
	}
	listed := listedValue.(tool.StateResult).State.(jobListResult)
	for _, job := range listed.Jobs {
		if job.JobID == foreignJobID {
			t.Fatalf("job_list disclosed foreign job %q", foreignJobID)
		}
	}
	if _, err := jobStatusTool(caller, map[string]any{"job_id": foreignJobID}, jobToolResultDefaultMaxChar); !isJobNotFoundErr(err) {
		t.Fatalf("job_status error = %v, want scoped not found", err)
	}
	if _, err := jobStopTool(context.Background(), caller, map[string]any{"job_id": foreignJobID}, jobToolResultDefaultMaxChar); !isJobNotFoundErr(err) {
		t.Fatalf("job_stop error = %v, want scoped not found", err)
	}
	if _, err := jobWatchTool(caller, map[string]any{
		"operation": "create", "source": foreignJobID,
		"events": []any{"job.notification"},
	}, jobToolResultDefaultMaxChar); err == nil {
		t.Fatal("job_watch accepted foreign source")
	}
	if _, err := delegateSendTool(context.Background(), caller, map[string]any{
		"to": foreignDelegateID, "message": "ping",
	}, jobToolResultDefaultMaxChar); err == nil {
		t.Fatal("delegate_send accepted foreign delegate")
	}
}
~~~

Use the fixture's real concrete values in place of the local variable names above. Keep the one short marker plus structured envelope assertions; do not add a large rendered snapshot.

- [ ] **Step 2: Run the regression**

~~~bash
go test ./agent -run 'TestForeignTranscriptReadDoesNotBroadenJobTools|TestReadTranscript.*LocalJob|TestSessionWatch.*ObservedBy' -count=1
~~~

Any failure is a product defect to root-cause. Do not weaken assertions or add waits.

- [ ] **Step 3: Run compile and vocabulary audits**

~~~bash
go test -tags serffuzz ./agent -run '^$'
go test -tags eval ./agent -run '^$'
rg -n 'watch_read_grant|EventWatchReadGrant|ObserverSessionID|FoldGrants|LoadGrants|LoadSessionObserverGrants|parentGrantedJobRead|grantedJobRead|lookupGrantedJobRead|jobStatusDeniedError|appendWatchReadGrant|mintWatchSendReadGrant|watchReadGrantObserver|watchGrantableJob|appendWatchFrameGrantedRead|grantsMinted|watchGrantKey' agent cmd/serf-hub --glob '*.go'
rg -n 'NewJobID\(\)|MustNewJobID\(\)' --glob '*.go'
~~~

Both searches must produce no output and exit 1. Review remaining comments for stale permission language.

- [ ] **Step 4: Enforce the production code budget**

~~~bash
git diff --numstat 8be010b2d -- identifier agent cmd/serf-tui cmd/serf-hub/internal/hubcore cmd/serf-hub/web_workspace.go cmd/serf-hub/frontend/src/panes/session/transcript/tools ':!**/*_test.go' ':!**/*_fuzz_test.go'
git diff --stat 8be010b2d -- identifier agent cmd/serf-tui cmd/serf-hub/internal/hubcore cmd/serf-hub/web_workspace.go cmd/serf-hub/frontend/src/panes/session/transcript/tools ':!**/*_test.go' ':!**/*_fuzz_test.go'
~~~

Deletion must substantially exceed addition. New production code is limited to IDs/allocation and their abbreviation helpers, bounded lookup, read-only snapshot, and direct observer stamps. If not, stop and report the exact excess to Jesse.

- [ ] **Step 5: Format exact files and run focused package tests**

Run gofmt only on exact modified Go filenames. Do not stage or format directories.

~~~bash
(cd cmd/serf-hub/frontend && npx biome check --write src/panes/session/transcript/tools/helpers.ts src/panes/session/transcript/tools/helpers.test.ts src/panes/session/transcript/tools/jobTools.tsx src/panes/session/transcript/tools/jobTools.test.tsx src/panes/session/transcript/tools/readTranscript.tsx src/panes/session/transcript/tools/readTranscript.test.tsx)
go test ./identifier -count=1
go test ./agent/internal/jobstore -count=1
go test ./agent -count=1
go test ./cmd/serf-tui -count=1
go test ./cmd/serf-tui/internal/msgrender -count=1
go test ./cmd/serf-hub/internal/hubcore -count=1
go test ./cmd/serf-hub -count=1
(cd cmd/serf-hub/frontend && npm test -- src/panes/session/transcript/tools/helpers.test.ts src/panes/session/transcript/tools/jobTools.test.tsx src/panes/session/transcript/tools/readTranscript.test.tsx)
~~~

Do not run make fuzz as part of default checks.

- [ ] **Step 6: Commit final contract evidence**

Stage only the exact test/doc files changed in this task:

~~~bash
git add agent/job_transcript_read_test.go agent/session_tools_transcript_job_read_test.go
git commit -m "test: prove open job reads remain read only"
~~~

- [ ] **Step 7: Hand back for central gates and typed kata evidence**

Workers stop after focused verification and mutation reports. The controller reviews each commit, then runs from the fleet worktree:

~~~bash
make lint
make build
ROOT_FULL=1 make test
~~~

Each bare exit code is the verdict. Do not pipe or grep a gate, and do not substitute make fuzz. Only after all gates are green may the controller close vg1k and any other explicitly matched kata with typed evidence and retire the implementation worktree.

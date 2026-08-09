package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/identifier"
)

func readLocalJobTranscriptForTest(t *testing.T, stateDir, sessionID, jobID string) (readMarkdownEnvelope, error) {
	t.Helper()
	value, err := execReadTranscript(&toolDeps{
		stateDir:  stateDir,
		sessionID: sessionID,
	}, map[string]any{"transcript_ref": "job:" + jobID})
	if err != nil {
		return readMarkdownEnvelope{}, err
	}
	envelope, ok := value.(readMarkdownEnvelope)
	if !ok {
		t.Fatalf("read_transcript returned %T, want readMarkdownEnvelope", value)
	}
	return envelope, nil
}

func TestReadTranscriptLocalJobOwnerAndForeignSnapshots(t *testing.T) {
	stateHome := t.TempDir()
	current := localJobProjectBucket(t, stateHome, localJobCurrentProject)
	foreign := localJobProjectBucket(t, stateHome, localJobSiblingProject)
	callerSessionID := identifier.MustNewSessionID()
	foreignSessionID := identifier.MustNewSessionID()
	descendantSessionID := identifier.MustNewSessionID()

	for _, scope := range []struct {
		name     string
		stateDir string
		owner    string
	}{
		{name: "owner", stateDir: current, owner: callerSessionID},
		{name: "descendant", stateDir: current, owner: descendantSessionID},
		{name: "foreign", stateDir: foreign, owner: foreignSessionID},
	} {
		for _, jobType := range []jobstore.JobType{jobstore.JobShell, jobstore.JobDelegate} {
			for _, terminal := range []bool{false, true} {
				name := fmt.Sprintf("%s/%s/terminal=%t", scope.name, jobType, terminal)
				t.Run(name, func(t *testing.T) {
					jobID := identifier.MustNewJobID(scope.owner)
					marker := "MARKER " + name + "\n"
					var structured map[string]any
					if jobType == jobstore.JobDelegate && terminal {
						structured = map[string]any{"verdict": "clean"}
					}
					seedLocalJobRecord(t, scope.stateDir, scope.owner, jobID, "/untrusted/decoy.log", marker, maxJobOutputRetentionBytes, jobType, terminal, int64(len(marker)), structured)

					envelope, err := readLocalJobTranscriptForTest(t, current, callerSessionID, jobID)
					if err != nil {
						t.Fatalf("read local job: %v", err)
					}
					for _, want := range []string{
						jobID,
						marker,
						fmt.Sprintf("total_bytes: %d", len(marker)),
					} {
						if !strings.Contains(envelope.Content, want) {
							t.Fatalf("content = %q, want %q", envelope.Content, want)
						}
					}
					wantStatus := "status: running"
					if terminal {
						wantStatus = "status: completed"
						if !strings.Contains(envelope.Content, "reason: exit_zero") {
							t.Fatalf("terminal content = %q, want durable reason", envelope.Content)
						}
					}
					if !strings.Contains(envelope.Content, wantStatus) {
						t.Fatalf("content = %q, want %q", envelope.Content, wantStatus)
					}
					if jobType == jobstore.JobDelegate && terminal && !strings.Contains(envelope.Content, `structured_result (valid=true): {"verdict":"clean"}`) {
						t.Fatalf("delegate content = %q, want structured result", envelope.Content)
					}
					if envelope.TranscriptRef != "job:"+jobID || envelope.Meta.Range != "shell-log" || envelope.Meta.Truncated {
						t.Fatalf("envelope = %+v", envelope)
					}
				})
			}
		}
	}
}

func TestReadTranscriptLocalJobUsesDerivedOutputAfterStoreClose(t *testing.T) {
	stateHome := t.TempDir()
	current := localJobProjectBucket(t, stateHome, localJobCurrentProject)
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	decoy := filepath.Join(t.TempDir(), "decoy.log")
	if err := os.WriteFile(decoy, []byte("DECOY\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedLocalJob(t, current, owner, jobID, decoy, "CLOSED_STORE_REAL\n", true)
	store, err := jobstore.OpenNoSync(filepath.Join(jobsDir(current, owner), "jobs.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	envelope, err := readLocalJobTranscriptForTest(t, current, owner, jobID)
	if err != nil {
		t.Fatalf("read after store close: %v", err)
	}
	if !strings.Contains(envelope.Content, "CLOSED_STORE_REAL") || strings.Contains(envelope.Content, "DECOY") {
		t.Fatalf("content = %q, want derived retained output after store close", envelope.Content)
	}
}

func TestForeignTranscriptReadDoesNotBroadenJobTools(t *testing.T) {
	stateHome := t.TempDir()
	current := localJobProjectBucket(t, stateHome, localJobCurrentProject)
	foreign := localJobProjectBucket(t, stateHome, localJobSiblingProject)
	caller := newSession(t, withConfig(SessionConfig{StateDir: current, MaxSubagentDepth: 1}))
	foreignOwner := identifier.MustNewSessionID()
	foreignJobID := identifier.MustNewJobID(foreignOwner)
	seedLocalJobRecord(t, foreign, foreignOwner, foreignJobID, "/untrusted/decoy.log", "FOREIGN_MARKER\n", maxJobOutputRetentionBytes, jobstore.JobDelegate, false, 0, nil)
	location, found, err := findLocalJobInProject(foreign, foreignOwner, foreignJobID)
	if err != nil || !found {
		t.Fatalf("load foreign fixture: found=%t err=%v", found, err)
	}
	foreignDelegateID := location.Record.DelegateID

	value, err := execReadTranscript(&toolDeps{
		stateDir:  current,
		sessionID: caller.ID(),
	}, map[string]any{"transcript_ref": "job:" + foreignJobID})
	if err != nil {
		t.Fatalf("foreign read: %v", err)
	}
	envelope := value.(readMarkdownEnvelope)
	if !strings.Contains(envelope.Content, "FOREIGN_MARKER") || envelope.TranscriptRef != "job:"+foreignJobID {
		t.Fatalf("foreign envelope = %+v", envelope)
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

func TestReadTranscriptLocalJobOwnerAndForeignRetainedMetadata(t *testing.T) {
	stateHome := t.TempDir()
	current := localJobProjectBucket(t, stateHome, localJobCurrentProject)
	foreign := localJobProjectBucket(t, stateHome, localJobSiblingProject)
	callerSessionID := identifier.MustNewSessionID()

	for _, scope := range []struct {
		name     string
		stateDir string
		owner    string
	}{
		{name: "owner", stateDir: current, owner: callerSessionID},
		{name: "foreign", stateDir: foreign, owner: identifier.MustNewSessionID()},
	} {
		t.Run(scope.name, func(t *testing.T) {
			const output = "DROP_ME:RETAINED"
			jobID := identifier.MustNewJobID(scope.owner)
			seedLocalJobRecord(t, scope.stateDir, scope.owner, jobID, "/untrusted/decoy.log", output, int64(len("RETAINED")), jobstore.JobShell, true, int64(len(output)), nil)

			envelope, err := readLocalJobTranscriptForTest(t, current, callerSessionID, jobID)
			if err != nil {
				t.Fatalf("read retained local job: %v", err)
			}
			for _, want := range []string{"RETAINED", "total_bytes: 16", "dropped_bytes: 8"} {
				if !strings.Contains(envelope.Content, want) {
					t.Fatalf("content = %q, want %q", envelope.Content, want)
				}
			}
			if strings.Contains(envelope.Content, "DROP_ME") {
				t.Fatalf("content = %q, retained pruned prefix", envelope.Content)
			}
			if !envelope.Meta.Truncated {
				t.Fatalf("meta = %+v, want truncated retained snapshot", envelope.Meta)
			}
		})
	}
}

func TestReadTranscriptLocalJobRejectsOldIDBeforeIO(t *testing.T) {
	oldOpen := openLocalJobProjectDirectory
	openLocalJobProjectDirectory = func(string) (localJobProjectDirectory, error) {
		t.Fatal("malformed job identifier reached filesystem lookup")
		return nil, nil
	}
	t.Cleanup(func() { openLocalJobProjectDirectory = oldOpen })

	_, err := readLocalJobTranscriptForTest(t, filepath.Join(t.TempDir(), "serf", "projects", localJobCurrentProject), identifier.MustNewSessionID(), "job_legacy")
	if err == nil || !strings.Contains(err.Error(), "invalid job identifier") {
		t.Fatalf("old job ID error = %v, want invalid job identifier", err)
	}
}

func TestReadTranscriptLocalJobRejectsMiddleCorruption(t *testing.T) {
	flat := t.TempDir()
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	seedLocalJob(t, flat, owner, jobID, "/decoy", "content\n", true)
	path := filepath.Join(jobsDir(flat, owner), "jobs.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("seeded event lines = %d, want 2", len(lines))
	}
	corrupt := lines[0] + "\n{not-json}\n" + lines[1] + "\n"
	if err := os.WriteFile(path, []byte(corrupt), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = readLocalJobTranscriptForTest(t, flat, owner, jobID)
	if err == nil || !strings.Contains(err.Error(), "parse event line 2") {
		t.Fatalf("middle-corrupt read error = %v", err)
	}
}

func TestReadTranscriptLocalJobToleratesTrailingPartialEvent(t *testing.T) {
	flat := t.TempDir()
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	seedLocalJob(t, flat, owner, jobID, "/decoy", "still running\n", false)
	path := filepath.Join(jobsDir(flat, owner), "jobs.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"kind":"job_finished"`); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	envelope, err := readLocalJobTranscriptForTest(t, flat, owner, jobID)
	if err != nil {
		t.Fatalf("trailing partial read: %v", err)
	}
	if !strings.Contains(envelope.Content, "still running") || !strings.Contains(envelope.Content, "status: running") {
		t.Fatalf("content = %q", envelope.Content)
	}
}

func TestReadTranscriptLocalJobRejectsNewlineTerminatedTrailingCorruption(t *testing.T) {
	flat := t.TempDir()
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	seedLocalJob(t, flat, owner, jobID, "/decoy", "still running\n", false)
	path := filepath.Join(jobsDir(flat, owner), "jobs.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{\"kind\":\"job_finished\"\n"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = readLocalJobTranscriptForTest(t, flat, owner, jobID)
	if err == nil || !strings.Contains(err.Error(), "parse event line 2") {
		t.Fatalf("newline-terminated trailing corruption error = %v", err)
	}
}

func TestReadTranscriptLocalJobReportsMissingOutput(t *testing.T) {
	flat := t.TempDir()
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	seedLocalJob(t, flat, owner, jobID, "/decoy", "gone\n", true)
	if err := os.Remove(filepath.Join(jobsDir(flat, owner), "jobs", jobID+".log")); err != nil {
		t.Fatal(err)
	}

	_, err := readLocalJobTranscriptForTest(t, flat, owner, jobID)
	if err == nil || !strings.Contains(err.Error(), "output_unavailable") {
		t.Fatalf("missing-output error = %v, want output_unavailable", err)
	}
}

func appendLocalJobOutputForTranscriptTest(t *testing.T, stateDir, owner, jobID string, retention int64, output string) {
	t.Helper()
	path := filepath.Join(jobsDir(stateDir, owner), "jobs", jobID+".log")
	store, err := jobstore.OpenOutputNoSync(path, retention)
	if err != nil {
		t.Fatalf("open output for append: %v", err)
	}
	if _, err := store.Append([]byte(output)); err != nil {
		_ = store.Close()
		t.Fatalf("append output: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close appended output: %v", err)
	}
}

func TestReadTranscriptJobRawPageRunningAppendContinuation(t *testing.T) {
	stateDir := t.TempDir()
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	initial := strings.Repeat("x", retainedOutputPageBytes) + "before"
	seedLocalJobRecord(t, stateDir, owner, jobID, "/decoy", initial, maxJobOutputRetentionBytes, jobstore.JobShell, false, 0, nil)
	deps := &toolDeps{stateDir: stateDir, sessionID: owner}

	first := execRead(t, deps, map[string]any{"transcript_ref": "job:" + jobID, "offset_bytes": float64(0)})
	requireExactKeys(t, first, "transcript_ref", "representation", "content_type", "page", "retained_start_bytes", "job_status", "continuation")
	requirePage(t, first, 0, initial[:retainedOutputPageBytes])
	if first["job_status"] != "running" || first["retained_start_bytes"] != float64(0) {
		t.Fatalf("running page metadata = %#v", first)
	}
	page := first["page"].(map[string]any)
	if page["total_bytes"] != float64(len(initial)) {
		t.Fatalf("first snapshot total_bytes = %v, want %d", page["total_bytes"], len(initial))
	}
	continuation := first["continuation"].(map[string]any)["offset_bytes"]

	appendLocalJobOutputForTranscriptTest(t, stateDir, owner, jobID, maxJobOutputRetentionBytes, "-after")
	second := execRead(t, deps, map[string]any{"transcript_ref": "job:" + jobID, "offset_bytes": continuation})
	requirePage(t, second, retainedOutputPageBytes, "before-after")
	if second["job_status"] != "running" || second["page"].(map[string]any)["total_bytes"] != float64(len(initial)+len("-after")) {
		t.Fatalf("continued running snapshot = %#v", second)
	}

	markdown, err := readLocalJobTranscriptForTest(t, stateDir, owner, jobID)
	if err != nil || !strings.Contains(markdown.Content, "before-after") || !strings.Contains(markdown.Content, "status: running") {
		t.Fatalf("no-argument markdown changed: envelope=%#v err=%v", markdown, err)
	}
}

func TestReadTranscriptJobContinuationOutrunByPruning(t *testing.T) {
	stateDir := t.TempDir()
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	const retention = int64(20 << 10)
	initial := strings.Repeat("a", int(retention))
	seedLocalJobRecord(t, stateDir, owner, jobID, "/decoy", initial, retention, jobstore.JobShell, false, 0, nil)
	deps := &toolDeps{stateDir: stateDir, sessionID: owner}
	first := execRead(t, deps, map[string]any{"transcript_ref": "job:" + jobID, "offset_bytes": float64(0)})
	continuation := first["continuation"].(map[string]any)["offset_bytes"]

	appendLocalJobOutputForTranscriptTest(t, stateDir, owner, jobID, retention, strings.Repeat("b", int(retention)))
	_, err := execReadTranscript(deps, map[string]any{"transcript_ref": "job:" + jobID, "offset_bytes": continuation})
	if err == nil || !strings.Contains(err.Error(), "output_unavailable") || !strings.Contains(err.Error(), "first available offset is 20480") {
		t.Fatalf("pruned continuation error = %v", err)
	}
}

func TestReadTranscriptJobSearchDefersRunningEOFThenMatchesAfterAppend(t *testing.T) {
	stateDir := t.TempDir()
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	const output = "head\nneedle"
	seedLocalJobRecord(t, stateDir, owner, jobID, "/decoy", output, maxJobOutputRetentionBytes, jobstore.JobShell, false, 0, nil)
	deps := &toolDeps{stateDir: stateDir, sessionID: owner}

	deferred := execRead(t, deps, map[string]any{"transcript_ref": "job:" + jobID, "output_match": "needle"})
	requireExactKeys(t, deferred,
		"transcript_ref", "output_match", "context_lines", "offset_bytes",
		"retained_start_bytes", "total_bytes", "job_status", "search_complete",
		"skipped_partial_prefix", "matches", "continuation",
	)
	if deferred["job_status"] != "running" || len(deferred["matches"].([]any)) != 0 || deferred["search_complete"] != true {
		t.Fatalf("deferred search = %#v", deferred)
	}
	continuation := deferred["continuation"].(map[string]any)["offset_bytes"]
	if continuation != float64(len("head\n")) {
		t.Fatalf("deferred continuation = %v, want %d", continuation, len("head\n"))
	}

	appendLocalJobOutputForTranscriptTest(t, stateDir, owner, jobID, maxJobOutputRetentionBytes, " complete\n")
	completed := execRead(t, deps, map[string]any{"transcript_ref": "job:" + jobID, "output_match": "needle", "offset_bytes": continuation})
	requireMatch(t, completed, int64(len("head\n")), nil, "needle complete", nil)
	if completed["job_status"] != "running" || completed["search_complete"] != true || completed["total_bytes"] != float64(len(output)+len(" complete\n")) {
		t.Fatalf("completed running search = %#v", completed)
	}
}

func TestReadTranscriptJobSearchEvaluatesTerminalUnterminatedEOF(t *testing.T) {
	stateDir := t.TempDir()
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	const output = "head\nneedle"
	seedLocalJobRecord(t, stateDir, owner, jobID, "/decoy", output, maxJobOutputRetentionBytes, jobstore.JobShell, true, int64(len(output)), nil)
	search := execRead(t, &toolDeps{stateDir: stateDir, sessionID: owner}, map[string]any{"transcript_ref": "job:" + jobID, "output_match": "needle"})
	requireMatch(t, search, int64(len("head\n")), nil, "needle", nil)
	if search["job_status"] != "terminal" || search["search_complete"] != true {
		t.Fatalf("terminal search = %#v", search)
	}
}

func TestReadTranscriptJobSearchPruneBoundaryHonesty(t *testing.T) {
	for _, tc := range []struct {
		name                string
		output              string
		retention           int64
		match               string
		wantLines           []string
		wantSkippedFragment bool
	}{
		{
			name:                "line aligned retained start includes first matching line",
			output:              "discard\nMATCH\n",
			retention:           int64(len("MATCH\n")),
			match:               `^MATCH$`,
			wantLines:           []string{"MATCH"},
			wantSkippedFragment: false,
		},
		{
			name:                "mid line retained start skips fragment",
			output:              "discard\nPARTIAL-MATCH\nHIT\n",
			retention:           int64(len("MATCH\nHIT\n")),
			match:               `MATCH|HIT`,
			wantLines:           []string{"HIT"},
			wantSkippedFragment: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateDir := t.TempDir()
			owner := identifier.MustNewSessionID()
			jobID := identifier.MustNewJobID(owner)
			seedLocalJobRecord(t, stateDir, owner, jobID, "/decoy", tc.output, tc.retention, jobstore.JobShell, true, int64(len(tc.output)), nil)

			search := execRead(t, &toolDeps{stateDir: stateDir, sessionID: owner}, map[string]any{
				"transcript_ref": "job:" + jobID,
				"output_match":   tc.match,
			})
			matches := search["matches"].([]any)
			if len(matches) != len(tc.wantLines) {
				t.Fatalf("matches = %#v, want lines %q", matches, tc.wantLines)
			}
			for i, want := range tc.wantLines {
				if got := matches[i].(map[string]any)["line"]; got != want {
					t.Fatalf("match %d line = %q, want %q", i, got, want)
				}
			}
			if got := search["skipped_partial_prefix"]; got != tc.wantSkippedFragment {
				t.Fatalf("skipped_partial_prefix = %v, want %t; search = %#v", got, tc.wantSkippedFragment, search)
			}
		})
	}
}

func TestReadTranscriptJobPageAndSearchRetainedFailuresArePathFree(t *testing.T) {
	stateDir := t.TempDir()
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	seedLocalJobRecord(t, stateDir, owner, jobID, "/decoy", "ready\n", maxJobOutputRetentionBytes, jobstore.JobShell, true, int64(len("ready\n")), nil)
	deps := &toolDeps{stateDir: stateDir, sessionID: owner}
	outputPath := filepath.Join(jobsDir(stateDir, owner), "jobs", jobID+".log")

	assertFailure := func(t *testing.T, args map[string]any, want string, forbidden string) {
		t.Helper()
		_, err := execReadTranscript(deps, args)
		if err == nil {
			t.Fatal("read succeeded, want retained-output failure")
		}
		if got := err.Error(); got != want {
			t.Fatalf("error = %q, want %q", got, want)
		}
		if strings.Contains(err.Error(), forbidden) {
			t.Fatalf("error leaked path %q: %q", forbidden, err)
		}
	}

	t.Run("deleted output", func(t *testing.T) {
		t.Cleanup(func() {
			if err := os.WriteFile(outputPath, []byte("ready\n"), 0o644); err != nil {
				t.Errorf("restore output: %v", err)
			}
		})
		if err := os.Remove(outputPath); err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("output_unavailable: job %q retained output is missing or pruned", jobID)
		for _, args := range []map[string]any{
			{"transcript_ref": "job:" + jobID, "offset_bytes": float64(0)},
			{"transcript_ref": "job:" + jobID, "output_match": "ready"},
		} {
			assertFailure(t, args, want, outputPath)
		}
	})

	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "open failure", err: &os.PathError{Op: "open", Path: "/sentinel/absolute/job-output.log", Err: os.ErrPermission}},
		{name: "read failure", err: &os.PathError{Op: "read", Path: "/sentinel/absolute/job-output.log", Err: io.ErrUnexpectedEOF}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldWindow := readLocalJobOutputWindowSnapshot
			readLocalJobOutputWindowSnapshot = func(string, int64, int) (jobstore.OutputWindowSnapshot, error) {
				return jobstore.OutputWindowSnapshot{}, tc.err
			}
			t.Cleanup(func() { readLocalJobOutputWindowSnapshot = oldWindow })
			want := fmt.Sprintf("output_unavailable: job %q retained output could not be read", jobID)
			for _, args := range []map[string]any{
				{"transcript_ref": "job:" + jobID, "offset_bytes": float64(0)},
				{"transcript_ref": "job:" + jobID, "output_match": "ready"},
			} {
				assertFailure(t, args, want, "/sentinel/absolute/job-output.log")
			}
		})
	}
}

func TestReadTranscriptJobPageAndSearchValidation(t *testing.T) {
	stateDir := t.TempDir()
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	const output = "line\n"
	seedLocalJobRecord(t, stateDir, owner, jobID, "/decoy", output, maxJobOutputRetentionBytes, jobstore.JobShell, false, 0, nil)
	deps := &toolDeps{stateDir: stateDir, sessionID: owner}
	tests := []struct {
		name string
		args map[string]any
		want []string
	}{
		{name: "outline format", args: map[string]any{"transcript_ref": "job:" + jobID, "format": "outline"}, want: []string{"invalid_request", "format"}},
		{name: "markdown plus page", args: map[string]any{"transcript_ref": "job:" + jobID, "format": "markdown", "offset_bytes": float64(0)}, want: []string{"invalid_request", "format", "offset_bytes"}},
		{name: "markdown plus search", args: map[string]any{"transcript_ref": "job:" + jobID, "format": "markdown", "output_match": "line"}, want: []string{"invalid_request", "format", "output_match"}},
		{name: "range", args: map[string]any{"transcript_ref": "job:" + jobID, "range": "1-2"}, want: []string{"invalid_request", "range", "session"}},
		{name: "expand turn", args: map[string]any{"transcript_ref": "job:" + jobID, "expand_turn": float64(0)}, want: []string{"invalid_request", "expand_turn", "session"}},
		{name: "invalid re2", args: map[string]any{"transcript_ref": "job:" + jobID, "output_match": "["}, want: []string{"invalid_request", "output_match"}},
		{name: "beyond eof", args: map[string]any{"transcript_ref": "job:" + jobID, "offset_bytes": float64(6)}, want: []string{"invalid_request", "valid byte interval is [0,5]", "job_status=running"}},
		{name: "empty job ref", args: map[string]any{"transcript_ref": "job:"}, want: []string{"invalid_request", "job:<job_id>"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := execReadTranscript(deps, tc.args)
			if err == nil {
				t.Fatal("read succeeded, want error")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %v, want substring %q", err, want)
				}
			}
		})
	}
}

func TestReadTranscriptJobOffsetBeforeRetentionReportsFirstAvailable(t *testing.T) {
	stateDir := t.TempDir()
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	const output = "DROP_ME:RETAINED"
	seedLocalJobRecord(t, stateDir, owner, jobID, "/decoy", output, int64(len("RETAINED")), jobstore.JobShell, true, int64(len(output)), nil)
	deps := &toolDeps{stateDir: stateDir, sessionID: owner}
	_, err := execReadTranscript(deps, map[string]any{"transcript_ref": "job:" + jobID, "offset_bytes": float64(0)})
	if err == nil || !strings.Contains(err.Error(), "output_unavailable") || !strings.Contains(err.Error(), "first available offset is 8") {
		t.Fatalf("before-retention error = %v", err)
	}
	page := execRead(t, deps, map[string]any{"transcript_ref": "job:" + jobID, "offset_bytes": float64(8)})
	requirePage(t, page, 8, "RETAINED")
	if page["retained_start_bytes"] != float64(8) || page["job_status"] != "terminal" {
		t.Fatalf("retained page = %#v", page)
	}
}

func TestReadTranscriptDelegateMarkdownCompatibilityAndRawIsolation(t *testing.T) {
	stateDir := t.TempDir()
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	const report = "delegate report\n"
	seedLocalJobRecord(t, stateDir, owner, jobID, "/decoy", report, maxJobOutputRetentionBytes, jobstore.JobDelegate, true, int64(len(report)), map[string]any{"verdict": "clean"})
	deps := &toolDeps{stateDir: stateDir, sessionID: owner}

	markdown, err := readLocalJobTranscriptForTest(t, stateDir, owner, jobID)
	if err != nil || !strings.Contains(markdown.Content, report) || !strings.Contains(markdown.Content, `structured_result (valid=true): {"verdict":"clean"}`) {
		t.Fatalf("delegate markdown compatibility: envelope=%#v err=%v", markdown, err)
	}
	raw := execRead(t, deps, map[string]any{"transcript_ref": "job:" + jobID, "offset_bytes": float64(0)})
	requirePage(t, raw, 0, report)
	if strings.Contains(raw["page"].(map[string]any)["data"].(string), "structured_result") {
		t.Fatalf("raw delegate output synthesized structured result: %#v", raw)
	}
}

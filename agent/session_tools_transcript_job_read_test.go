package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
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

	for _, scope := range []struct {
		name     string
		stateDir string
		owner    string
	}{
		{name: "owner", stateDir: current, owner: callerSessionID},
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

package agent

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/identifier"
)

const (
	localJobCurrentProject = "current-0000000001"
	localJobSiblingProject = "sibling-0000000002"
)

func localJobProjectBucket(t *testing.T, stateHome, projectID string) string {
	t.Helper()
	bucket := filepath.Join(stateHome, "serf", "projects", projectID)
	if err := os.MkdirAll(bucket, 0o755); err != nil {
		t.Fatalf("create project bucket: %v", err)
	}
	return bucket
}

func seedLocalJob(t *testing.T, stateDir, ownerSessionID, jobID, outputPath, output string, terminal bool) {
	seedLocalJobRecord(t, stateDir, ownerSessionID, jobID, outputPath, output, jobstore.JobShell, terminal, int64(len(output)), nil)
}

func seedLocalJobRecord(t *testing.T, stateDir, ownerSessionID, jobID, outputPath, output string, jobType jobstore.JobType, terminal bool, terminalOutputBytes int64, structuredResult map[string]any) {
	t.Helper()
	derivedOutputPath := filepath.Join(jobsDir(stateDir, ownerSessionID), "jobs", jobID+".log")
	if err := os.MkdirAll(filepath.Dir(derivedOutputPath), 0o755); err != nil {
		t.Fatalf("create job output dir: %v", err)
	}
	outputStore, err := jobstore.OpenOutputNoSync(derivedOutputPath, maxJobOutputRetentionBytes)
	if err != nil {
		t.Fatalf("open output: %v", err)
	}
	if _, err := outputStore.Append([]byte(output)); err != nil {
		_ = outputStore.Close()
		t.Fatalf("append output: %v", err)
	}
	if err := outputStore.Close(); err != nil {
		t.Fatalf("close output: %v", err)
	}

	store, err := jobstore.OpenNoSync(filepath.Join(jobsDir(stateDir, ownerSessionID), "jobs.jsonl"))
	if err != nil {
		t.Fatalf("open event store: %v", err)
	}
	started := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	startedEvent := jobstore.Event{
		Kind:             jobstore.EventJobStarted,
		TS:               started,
		JobID:            jobID,
		Type:             jobType,
		OwnerSessionID:   ownerSessionID,
		VisibleToSession: ownerSessionID,
		StartedAt:        &started,
		OutputPath:       outputPath,
		Command:          "printf marker",
	}
	if jobType == jobstore.JobDelegate {
		startedEvent.Command = ""
		startedEvent.Task = "review the local job"
		startedEvent.DelegateID = identifier.MustNewDelegateID()
		startedEvent.TranscriptRef = encodeRef("", identifier.MustNewSessionID())
	}
	if err := store.Append(startedEvent); err != nil {
		_ = store.Close()
		t.Fatalf("append job start: %v", err)
	}
	if terminal {
		ended := started.Add(time.Second)
		valid := structuredResult != nil
		if err := store.Append(jobstore.Event{
			Kind:                  jobstore.EventJobFinished,
			TS:                    ended,
			JobID:                 jobID,
			Status:                jobstore.StatusCompleted,
			Reason:                "exit_zero",
			EndedAt:               &ended,
			OutputBytes:           terminalOutputBytes,
			TerminalGen:           identifier.MustNewTerminalGeneration(),
			StructuredResult:      structuredResult,
			StructuredResultValid: &valid,
		}); err != nil {
			_ = store.Close()
			t.Fatalf("append job finish: %v", err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close event store: %v", err)
	}
}

func TestLocateLocalJobCurrentProjectWins(t *testing.T) {
	stateHome := t.TempDir()
	current := localJobProjectBucket(t, stateHome, localJobCurrentProject)
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	seedLocalJob(t, current, owner, jobID, "/decoy/current.log", "current\n", false)

	oldOpen := openLocalJobProjectDirectory
	openLocalJobProjectDirectory = func(string) (localJobProjectDirectory, error) {
		t.Fatal("current-project match enumerated siblings")
		return nil, nil
	}
	t.Cleanup(func() { openLocalJobProjectDirectory = oldOpen })

	location, err := locateLocalJob(current, jobID)
	if err != nil {
		t.Fatalf("locate current job: %v", err)
	}
	if location.StateDir != current || location.OwnerSessionID != owner || location.Record.JobID != jobID {
		t.Fatalf("location = %+v", location)
	}
}

func TestLocateLocalJobFindsExactOwnerInSiblingProject(t *testing.T) {
	stateHome := t.TempDir()
	current := localJobProjectBucket(t, stateHome, localJobCurrentProject)
	sibling := localJobProjectBucket(t, stateHome, localJobSiblingProject)
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	seedLocalJob(t, sibling, owner, jobID, "/decoy/sibling.log", "sibling\n", false)

	location, err := locateLocalJob(current, jobID)
	if err != nil {
		t.Fatalf("locate sibling job: %v", err)
	}
	if location.StateDir != sibling || location.OwnerSessionID != owner || location.Record.JobID != jobID {
		t.Fatalf("location = %+v", location)
	}
}

func TestLocateLocalJobRejectsAmbiguousSiblingOwners(t *testing.T) {
	stateHome := t.TempDir()
	current := localJobProjectBucket(t, stateHome, localJobCurrentProject)
	first := localJobProjectBucket(t, stateHome, "first-0000000003")
	second := localJobProjectBucket(t, stateHome, "second-0000000004")
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	seedLocalJob(t, first, owner, jobID, "/decoy/first.log", "first\n", false)
	seedLocalJob(t, second, owner, jobID, "/decoy/second.log", "second\n", false)

	_, err := locateLocalJob(current, jobID)
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("locate duplicate job error = %v, want ambiguous", err)
	}
}

func TestLocateLocalJobReturnsLimitExceededEvenAfterPartialMatch(t *testing.T) {
	stateHome := t.TempDir()
	current := localJobProjectBucket(t, stateHome, localJobCurrentProject)
	matchProject := "match-0000000005"
	matchBucket := localJobProjectBucket(t, stateHome, matchProject)
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	seedLocalJob(t, matchBucket, owner, jobID, "/decoy/match.log", "match\n", false)

	entries := make([]fs.DirEntry, 0, 257)
	entries = append(entries, localJobDirEntry{name: matchProject, dir: true})
	for i := 1; i < 257; i++ {
		entries = append(entries, localJobDirEntry{name: fmt.Sprintf("p%03d-0000000000", i), dir: true})
	}
	reader := &localJobDirReader{entries: entries}
	oldOpen := openLocalJobProjectDirectory
	openLocalJobProjectDirectory = func(path string) (localJobProjectDirectory, error) {
		want := filepath.Join(stateHome, "serf", "projects")
		if path != want {
			t.Fatalf("projects path = %q, want %q", path, want)
		}
		return reader, nil
	}
	t.Cleanup(func() { openLocalJobProjectDirectory = oldOpen })

	_, err := locateLocalJob(current, jobID)
	if err == nil || !strings.Contains(err.Error(), "lookup_limit_exceeded") {
		t.Fatalf("locate bounded job error = %v, want lookup_limit_exceeded", err)
	}
	if reader.readCounts[len(reader.readCounts)-1] != 1 {
		t.Fatalf("ReadDir calls = %v, want one-entry sentinel last", reader.readCounts)
	}
}

func TestLocateLocalJobFlatStateDirDoesNotSearchSiblings(t *testing.T) {
	flat := t.TempDir()
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	oldOpen := openLocalJobProjectDirectory
	openLocalJobProjectDirectory = func(string) (localJobProjectDirectory, error) {
		t.Fatal("flat state dir enumerated siblings")
		return nil, nil
	}
	t.Cleanup(func() { openLocalJobProjectDirectory = oldOpen })

	_, err := locateLocalJob(flat, jobID)
	if err == nil || !isJobNotFoundErr(err) {
		t.Fatalf("flat locate error = %v, want job not found", err)
	}
}

func TestLocateLocalJobDoesNotReadUnrelatedSessionStores(t *testing.T) {
	flat := t.TempDir()
	owner := identifier.MustNewSessionID()
	unrelated := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	path := filepath.Join(jobsDir(flat, unrelated), "jobs.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{corrupt middle record}\n{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := locateLocalJob(flat, jobID)
	if err == nil || !isJobNotFoundErr(err) {
		t.Fatalf("exact-owner locate error = %v, want job not found", err)
	}
}

func TestReadLocalJobSnapshotIgnoresPersistedAbsoluteOutputPath(t *testing.T) {
	flat := t.TempDir()
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	decoy := filepath.Join(t.TempDir(), "decoy.log")
	if err := os.WriteFile(decoy, []byte("DECOY\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedLocalJob(t, flat, owner, jobID, decoy, "REAL\n", true)

	snapshot, err := readLocalJobSnapshot(flat, jobID, 1024)
	if err != nil {
		t.Fatalf("read local snapshot: %v", err)
	}
	if snapshot.Content != "REAL\n" || strings.Contains(snapshot.Content, "DECOY") {
		t.Fatalf("snapshot content = %q, want derived output", snapshot.Content)
	}
	if snapshot.TotalBytes != 5 || snapshot.DroppedBytes != 0 || snapshot.Record.JobID != jobID {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestReadLocalJobSnapshotRejectsTerminalByteMismatch(t *testing.T) {
	flat := t.TempDir()
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	seedLocalJobRecord(t, flat, owner, jobID, "/decoy", "bytes\n", jobstore.JobShell, true, 99, nil)

	_, err := readLocalJobSnapshot(flat, jobID, 1024)
	if err == nil || !strings.Contains(err.Error(), "terminal output_bytes 99 does not match snapshot total_bytes 6") {
		t.Fatalf("terminal byte mismatch error = %v", err)
	}
}

type localJobDirEntry struct {
	name string
	dir  bool
	mode fs.FileMode
}

func (e localJobDirEntry) Name() string               { return e.name }
func (e localJobDirEntry) IsDir() bool                { return e.dir }
func (e localJobDirEntry) Type() fs.FileMode          { return e.mode }
func (e localJobDirEntry) Info() (fs.FileInfo, error) { return nil, errorsForUnusedInfo{} }

type errorsForUnusedInfo struct{}

func (errorsForUnusedInfo) Error() string { return "Info must not be called" }

type localJobDirReader struct {
	entries    []fs.DirEntry
	readCounts []int
	closed     bool
}

func (r *localJobDirReader) ReadDir(n int) ([]fs.DirEntry, error) {
	r.readCounts = append(r.readCounts, n)
	if len(r.entries) == 0 {
		return nil, io.EOF
	}
	take := min(n, len(r.entries))
	entries := append([]fs.DirEntry(nil), r.entries[:take]...)
	r.entries = r.entries[take:]
	if len(r.entries) == 0 {
		return entries, io.EOF
	}
	return entries, nil
}

func (r *localJobDirReader) Close() error {
	r.closed = true
	return nil
}

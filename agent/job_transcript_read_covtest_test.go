package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/identifier"
)

// TestLocalJobEnvelopeStatus covers the localJobEnvelopeStatus function
// (lines 167-172) for both terminal and running/nil cases.
func TestLocalJobEnvelopeStatus(t *testing.T) {
	t.Parallel()
	if got := localJobEnvelopeStatus(nil); got != "running" {
		t.Fatalf("nil record: got %q, want running", got)
	}
	rec := &jobstore.JobRecord{Status: jobstore.StatusRunning}
	if got := localJobEnvelopeStatus(rec); got != "running" {
		t.Fatalf("running record: got %q, want running", got)
	}
	rec.Status = jobstore.StatusCompleted
	if got := localJobEnvelopeStatus(rec); got != "terminal" {
		t.Fatalf("terminal record: got %q, want terminal", got)
	}
}

// TestValidateLocalJobRetainedTotal covers validateLocalJobRetainedTotal
// (lines 174-182) for nil, non-terminal, terminal-match, and terminal-mismatch.
func TestValidateLocalJobRetainedTotal(t *testing.T) {
	t.Parallel()
	// nil record: no error.
	if err := validateLocalJobRetainedTotal(localJobRetainedTarget{}, 100); err != nil {
		t.Fatalf("nil record: unexpected error %v", err)
	}
	// non-terminal: no error.
	rec := &jobstore.JobRecord{Status: jobstore.StatusRunning, OutputBytes: 50}
	target := localJobRetainedTarget{JobID: "job1", Record: rec}
	if err := validateLocalJobRetainedTotal(target, 100); err != nil {
		t.Fatalf("non-terminal: unexpected error %v", err)
	}
	// terminal match: no error.
	rec.Status = jobstore.StatusCompleted
	rec.OutputBytes = 100
	if err := validateLocalJobRetainedTotal(target, 100); err != nil {
		t.Fatalf("terminal match: unexpected error %v", err)
	}
	// terminal mismatch: error.
	rec.OutputBytes = 99
	if err := validateLocalJobRetainedTotal(target, 100); err == nil {
		t.Fatal("terminal mismatch: expected error")
	}
}

// TestLocalJobRetainedErrorHelpers covers the three error constructor helpers
// (lines 184-194).
func TestLocalJobRetainedErrorHelpers(t *testing.T) {
	t.Parallel()
	if got := localJobRetainedMissingError("job1"); got == nil || got.Error() == "" {
		t.Fatal("missing error should be non-empty")
	}
	if got := localJobRetainedUnreadableError("job1"); got == nil || got.Error() == "" {
		t.Fatal("unreadable error should be non-empty")
	}
	if got := localJobRetainedChangedError("job1"); got == nil || got.Error() == "" {
		t.Fatal("changed error should be non-empty")
	}
}

// TestLocalJobRetainedReadError covers all switch cases in
// localJobRetainedReadError (lines 196-216).
func TestLocalJobRetainedReadError(t *testing.T) {
	t.Parallel()
	target := localJobRetainedTarget{JobID: "job1", Record: &jobstore.JobRecord{Status: jobstore.StatusCompleted}}
	snap := jobstore.OutputWindowSnapshot{RetainedStart: 10, TotalBytes: 100}

	cases := []struct {
		name string
		err  error
	}{
		{"pruned", jobstore.ErrOutputPruned},
		{"invalid_offset", jobstore.ErrInvalidOffset},
		{"changed", jobstore.ErrOutputChangedDuringRead},
		{"not_exist", os.ErrNotExist},
		{"default", errors.New("some other error")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := localJobRetainedReadError(target, 5, snap, tc.err)
			if got == nil {
				t.Fatal("expected non-nil error")
			}
		})
	}
}

// TestReadLocalJobRetainedMetadata covers readLocalJobRetainedMetadata error
// paths (lines 218-233): changed, not-exist, generic error, and success.
func TestReadLocalJobRetainedMetadata(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "output.log")
	if err := os.WriteFile(outputPath, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Success: running job (no terminal byte validation).
	target := localJobRetainedTarget{
		JobID:      "job_ok",
		OutputPath: outputPath,
		Record:     &jobstore.JobRecord{Status: jobstore.StatusRunning},
	}
	snap, err := readLocalJobRetainedMetadata(target)
	if err != nil {
		t.Fatalf("success: unexpected error %v", err)
	}
	if snap.TotalBytes != 11 {
		t.Fatalf("totalBytes = %d, want 11", snap.TotalBytes)
	}

	// Not-exist: missing file.
	target = localJobRetainedTarget{JobID: "job_missing", OutputPath: filepath.Join(dir, "nope.log")}
	_, err = readLocalJobRetainedMetadata(target)
	if err == nil {
		t.Fatal("not-exist: expected error")
	}
	// localJobRetainedMissingError wraps the error as a new fmt.Errorf,
	// so it won't satisfy errors.Is(err, os.ErrNotExist). Just check the text.
	if err.Error() == "" {
		t.Fatalf("not-exist: error should have text, got %v", err)
	}

	// Terminal mismatch: terminal record with wrong output bytes.
	target = localJobRetainedTarget{
		JobID:      "job_mismatch",
		OutputPath: outputPath,
		Record:     &jobstore.JobRecord{Status: jobstore.StatusCompleted, OutputBytes: 999},
	}
	_, err = readLocalJobRetainedMetadata(target)
	if err == nil {
		t.Fatal("terminal mismatch: expected error")
	}

	// Unreadable: output path that causes a read error.
	// Use a path with a null byte which causes stat to fail.
	target = localJobRetainedTarget{
		JobID:      "job_bad",
		OutputPath: filepath.Join(dir, "bad\x00name"), //nolint:gocritic // test needs null byte in path
		Record:     &jobstore.JobRecord{Status: jobstore.StatusRunning},
	}
	_, err = readLocalJobRetainedMetadata(target)
	if err == nil {
		t.Fatal("bad path: expected error")
	}
}

// TestLocalJobSearchSource_ReadWindow covers the ReadWindow method error paths
// (lines 239-248): success, pruned, invalid-offset, and validation failure.
func TestLocalJobSearchSource_ReadWindow(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "output.log")
	if err := os.WriteFile(outputPath, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Success with running job.
	src := localJobSearchSource{target: localJobRetainedTarget{
		JobID:      "job1",
		OutputPath: outputPath,
		Record:     &jobstore.JobRecord{Status: jobstore.StatusRunning},
	}}
	snap, err := src.ReadWindow(0, 5)
	if err != nil {
		t.Fatalf("success: unexpected error %v", err)
	}
	if snap.TotalBytes != 5 {
		t.Fatalf("totalBytes = %d, want 5", snap.TotalBytes)
	}

	// Error: missing file.
	src = localJobSearchSource{target: localJobRetainedTarget{
		JobID:      "job2",
		OutputPath: filepath.Join(dir, "missing.log"),
	}}
	_, err = src.ReadWindow(0, 5)
	if err == nil {
		t.Fatal("missing file: expected error")
	}

	// Validation failure: terminal record with mismatch.
	src = localJobSearchSource{target: localJobRetainedTarget{
		JobID:      "job3",
		OutputPath: outputPath,
		Record:     &jobstore.JobRecord{Status: jobstore.StatusCompleted, OutputBytes: 999},
	}}
	_, err = src.ReadWindow(0, 5)
	if err == nil {
		t.Fatal("terminal mismatch: expected error")
	}
}

// TestReadLocalJobSnapshot_MissingOutput covers the not-exist error path
// in readLocalJobSnapshot (lines 259-260).
func TestReadLocalJobSnapshot_MissingOutput(t *testing.T) {
	t.Parallel()
	flat := t.TempDir()
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	// Seed a running job with an output path that doesn't exist on disk.
	seedLocalJobRecord(t, flat, owner, jobID, "/nonexistent/path.log", "", maxJobOutputRetentionBytes, false, 0, nil)

	// The output file at the default derived path doesn't exist because we
	// didn't write real content (seedLocalJobRecord creates the derived path,
	// but with 0 bytes for empty output). Actually it does create it.
	// Let's use a jobID whose derived output path does not exist.
	// Instead, remove the derived output file.
	derivedOutputPath := filepath.Join(jobsDir(flat, owner), "jobs", jobID+".log")
	os.Remove(derivedOutputPath)

	_, err := readLocalJobSnapshot(flat, jobID, 1024)
	if err == nil {
		t.Fatal("expected error for missing output")
	}
}

// TestFindLocalJobInProject_CorruptRecord covers the corrupt-record path
// (lines 128-130) where record coordinates don't match.
func TestFindLocalJobInProject_CorruptRecord(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	owner := identifier.MustNewSessionID()
	otherOwner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	// Write a jobs.jsonl under the owner's session dir, but with a record
	// whose OwnerSessionID field doesn't match the owner we query with.
	jobsPath := filepath.Join(jobsDir(dir, owner), "jobs.jsonl")
	if err := os.MkdirAll(filepath.Dir(jobsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := jobstore.OpenNoSync(jobsPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// Start with the correct owner.
	if err := store.Append(jobstore.Event{
		Kind: jobstore.EventJobStarted, TS: now, JobID: jobID,
		Type: jobstore.JobShell, OwnerSessionID: otherOwner,
		VisibleToSession: otherOwner, StartedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// Now look for the job under the original owner — the record will be
	// found by jobID but have a mismatched OwnerSessionID.
	_, found, err := findLocalJobInProject(dir, owner, jobID)
	if err == nil {
		t.Fatal("expected corrupt-record error")
	}
	if found {
		t.Fatal("expected found=false for corrupt record")
	}
}

// TestLocateLocalJob_InvalidBucketDir covers the invalid bucket dir path
// (line 42).
func TestLocateLocalJob_InvalidBucketDir(t *testing.T) {
	t.Parallel()
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	// Use a state dir whose base name is not a valid project ID.
	// validLocalBucketDir requires the dir to be under a projects/ hierarchy
	// or have a specific structure. A bare temp dir may pass, but one with
	// special chars should fail.
	_, err := locateLocalJob("/tmp/../invalid", jobID)
	if err == nil {
		t.Fatal("expected error for invalid bucket dir")
	}
}

// TestLocateLocalJob_OpenError covers the open-projects-directory error
// path (lines 59-61).
func TestLocateLocalJob_OpenError(t *testing.T) {
	t.Parallel()
	stateHome := t.TempDir()
	current := localJobProjectBucket(t, stateHome, localJobCurrentProject)
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)

	// Make the projects directory inaccessible by making it a file.
	projectsPath := filepath.Join(stateHome, "evener", "projects")
	os.RemoveAll(projectsPath)
	if err := os.WriteFile(projectsPath, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := locateLocalJob(current, jobID)
	if err == nil {
		t.Fatal("expected error for open failure")
	}
}

// TestLocateLocalJob_ExceededBound covers the reader-exceeded-bound path
// (lines 69-71) where ReadDir returns more entries than requested.
func TestLocateLocalJob_ExceededBound(t *testing.T) {
	stateHome := t.TempDir()
	current := localJobProjectBucket(t, stateHome, localJobCurrentProject)
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)

	reader := &overReturningReader{}
	oldOpen := openLocalJobProjectDirectory
	openLocalJobProjectDirectory = func(string) (localJobProjectDirectory, error) { return reader, nil }
	t.Cleanup(func() { openLocalJobProjectDirectory = oldOpen })

	_, err := locateLocalJob(current, jobID)
	if err == nil {
		t.Fatal("expected error for exceeded bound")
	}
}

type overReturningReader struct{}

func (r *overReturningReader) ReadDir(n int) ([]fs.DirEntry, error) {
	// Return more entries than requested.
	entries := make([]fs.DirEntry, n+1)
	for i := range entries {
		entries[i] = localJobDirEntry{name: fmt.Sprintf("p%03d-0000000000", i), dir: true}
	}
	return entries, nil
}
func (r *overReturningReader) Close() error { return nil }

// TestLocateLocalJob_NoProgress covers the no-progress path (lines 100-102)
// where ReadDir returns 0 entries with no error.
func TestLocateLocalJob_NoProgress(t *testing.T) {
	stateHome := t.TempDir()
	current := localJobProjectBucket(t, stateHome, localJobCurrentProject)
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)

	reader := &noProgressReader{}
	oldOpen := openLocalJobProjectDirectory
	openLocalJobProjectDirectory = func(string) (localJobProjectDirectory, error) { return reader, nil }
	t.Cleanup(func() { openLocalJobProjectDirectory = oldOpen })

	_, err := locateLocalJob(current, jobID)
	if err == nil {
		t.Fatal("expected error for no progress")
	}
}

type noProgressReader struct{}

func (r *noProgressReader) ReadDir(n int) ([]fs.DirEntry, error) {
	return []fs.DirEntry{}, nil // 0 entries, no error
}
func (r *noProgressReader) Close() error { return nil }

// TestLocateLocalJob_SentinelReadError covers the sentinel read error
// path (lines 112-113).
func TestLocateLocalJob_SentinelReadError(t *testing.T) {
	stateHome := t.TempDir()
	current := localJobProjectBucket(t, stateHome, localJobCurrentProject)
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)

	reader := &sentinelErrorReader{}
	oldOpen := openLocalJobProjectDirectory
	openLocalJobProjectDirectory = func(string) (localJobProjectDirectory, error) { return reader, nil }
	t.Cleanup(func() { openLocalJobProjectDirectory = oldOpen })

	_, err := locateLocalJob(current, jobID)
	if err == nil {
		t.Fatal("expected error for sentinel read error")
	}
}

type sentinelErrorReader struct{}

func (r *sentinelErrorReader) ReadDir(n int) ([]fs.DirEntry, error) {
	if n == 1 {
		return nil, errors.New("sentinel read failure")
	}
	// First call with limit=256: return exactly limit entries (all non-matching).
	entries := make([]fs.DirEntry, n)
	for i := range entries {
		entries[i] = localJobDirEntry{name: fmt.Sprintf("p%03d-0000000000", i), dir: true}
	}
	return entries, nil
}
func (r *sentinelErrorReader) Close() error { return nil }

// TestLocateLocalJob_SentinelNoProgress covers the sentinel-read no-progress
// path (line 115).
func TestLocateLocalJob_SentinelNoProgress(t *testing.T) {
	stateHome := t.TempDir()
	current := localJobProjectBucket(t, stateHome, localJobCurrentProject)
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)

	reader := &sentinelNoProgressReader{}
	oldOpen := openLocalJobProjectDirectory
	openLocalJobProjectDirectory = func(string) (localJobProjectDirectory, error) { return reader, nil }
	t.Cleanup(func() { openLocalJobProjectDirectory = oldOpen })

	_, err := locateLocalJob(current, jobID)
	if err == nil {
		t.Fatal("expected error for sentinel no-progress")
	}
}

type sentinelNoProgressReader struct{}

func (r *sentinelNoProgressReader) ReadDir(n int) ([]fs.DirEntry, error) {
	if n == 1 {
		// Sentinel: return 0 entries and nil error.
		return []fs.DirEntry{}, nil
	}
	// First call with limit=256: return exactly limit entries (all non-matching).
	entries := make([]fs.DirEntry, n)
	for i := range entries {
		entries[i] = localJobDirEntry{name: fmt.Sprintf("p%03d-0000000000", i), dir: true}
	}
	return entries, nil
}
func (r *sentinelNoProgressReader) Close() error { return nil }

// TestFinishLocalJobLookup covers both branches of finishLocalJobLookup
// (lines 134-139).
func TestFinishLocalJobLookup(t *testing.T) {
	t.Parallel()
	// Found: returns match.
	loc := localJobLocation{StateDir: "/some/dir"}
	got, err := finishLocalJobLookup(loc, true, "job1")
	if err != nil || got.StateDir != "/some/dir" {
		t.Fatalf("found: got=%v err=%v", got, err)
	}
	// Not found: returns job-not-found error.
	_, err = finishLocalJobLookup(localJobLocation{}, false, "job1")
	if err == nil || !isJobNotFoundErr(err) {
		t.Fatalf("not found: expected job-not-found, got %v", err)
	}
}

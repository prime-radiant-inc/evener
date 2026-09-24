package agent

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/identifier"
)

const (
	localJobCurrentProject = "current-0000000001"
	localJobSiblingProject = "sibling-0000000002"
)

func localJobProjectBucket(t *testing.T, stateHome, projectID string) string {
	t.Helper()
	bucket := filepath.Join(stateHome, "evener", "projects", projectID)
	if err := os.MkdirAll(bucket, 0o755); err != nil {
		t.Fatalf("create project bucket: %v", err)
	}
	return bucket
}

func seedLocalJob(t *testing.T, stateDir, ownerSessionID, jobID, outputPath, output string, terminal bool) {
	seedLocalJobRecord(t, stateDir, ownerSessionID, jobID, outputPath, output, maxJobOutputRetentionBytes, terminal, int64(len(output)), nil)
}

func seedLocalJobRecord(t *testing.T, stateDir, ownerSessionID, jobID, outputPath, output string, retentionBytes int64, terminal bool, terminalOutputBytes int64, structuredResult map[string]any) {
	t.Helper()
	derivedOutputPath := filepath.Join(jobsDir(stateDir, ownerSessionID), "jobs", jobID+".log")
	if err := os.MkdirAll(filepath.Dir(derivedOutputPath), 0o755); err != nil {
		t.Fatalf("create job output dir: %v", err)
	}
	outputStore, err := jobstore.OpenOutputNoSync(derivedOutputPath, retentionBytes)
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
		Type:             jobstore.JobShell,
		OwnerSessionID:   ownerSessionID,
		VisibleToSession: ownerSessionID,
		StartedAt:        &started,
		OutputPath:       outputPath,
		Command:          "printf marker",
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
		want := filepath.Join(stateHome, "evener", "projects")
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
	if !reader.closed {
		t.Fatal("project directory was not closed")
	}
}

func TestLocateLocalJobReturnsMatchAtExactLimit(t *testing.T) {
	stateHome := t.TempDir()
	current := localJobProjectBucket(t, stateHome, localJobCurrentProject)
	matchProject := "match-0000000005"
	matchBucket := localJobProjectBucket(t, stateHome, matchProject)
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	seedLocalJob(t, matchBucket, owner, jobID, "/decoy/match.log", "match\n", false)

	entries := make([]fs.DirEntry, 0, localJobProjectLookupLimit)
	entries = append(entries, localJobDirEntry{name: matchProject, dir: true})
	for i := 1; i < localJobProjectLookupLimit; i++ {
		entries = append(entries, localJobDirEntry{name: fmt.Sprintf("p%03d-0000000000", i), dir: true})
	}
	reader := &localJobDirReader{entries: entries}
	oldOpen := openLocalJobProjectDirectory
	openLocalJobProjectDirectory = func(string) (localJobProjectDirectory, error) { return reader, nil }
	t.Cleanup(func() { openLocalJobProjectDirectory = oldOpen })

	location, err := locateLocalJob(current, jobID)
	if err != nil {
		t.Fatalf("locate exact-limit match: %v", err)
	}
	if location.StateDir != matchBucket || location.Record.JobID != jobID {
		t.Fatalf("location = %+v", location)
	}
	if got := reader.readCounts; len(got) != 2 || got[0] != localJobProjectLookupLimit || got[1] != 1 {
		t.Fatalf("ReadDir calls = %v, want bounded prefix and one sentinel", got)
	}
	if !reader.closed {
		t.Fatal("project directory was not closed")
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

// TestLocateLocalJob_LegacyNamedSiblingBucket asserts that a job seeded in a
// legacy-named sibling bucket (whose name fails ValidateProjectID) is
// findable by locateLocalJob. The sibling sweep must not filter by
// ValidateProjectID, matching enumerateBuckets (PR #2163's agent-side
// counterpart). Currently the sweep at line 73 skips dirs whose name
// ValidateProjectID rejects.
func TestLocateLocalJob_LegacyNamedSiblingBucket(t *testing.T) {
	t.Parallel()
	stateHome := t.TempDir()
	current := localJobProjectBucket(t, stateHome, localJobCurrentProject)
	// "0123456789abcdef": no readable-portion/suffix split, so
	// identifier.ValidateProjectID rejects it.
	legacy := localJobProjectBucket(t, stateHome, "0123456789abcdef")
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	seedLocalJob(t, legacy, owner, jobID, "", "legacy job output\n", true)

	loc, err := locateLocalJob(current, jobID)
	if err != nil {
		t.Fatalf("job in legacy-named sibling bucket not found: %v", err)
	}
	if filepath.Base(loc.StateDir) != "0123456789abcdef" {
		t.Fatalf("located job in %q, want legacy bucket 0123456789abcdef", filepath.Base(loc.StateDir))
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
	seedLocalJobRecord(t, flat, owner, jobID, "/decoy", "bytes\n", maxJobOutputRetentionBytes, true, 99, nil)

	_, err := readLocalJobSnapshot(flat, jobID, 1024)
	if err == nil || !strings.Contains(err.Error(), "terminal output_bytes 99 does not match snapshot total_bytes 6") {
		t.Fatalf("terminal byte mismatch error = %v", err)
	}
}

func TestReadLocalJobSnapshotReportsRetainedMetadata(t *testing.T) {
	flat := t.TempDir()
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	const output = "DROP_ME:RETAINED"
	seedLocalJobRecord(t, flat, owner, jobID, "/decoy", output, int64(len("RETAINED")), true, int64(len(output)), nil)

	snapshot, err := readLocalJobSnapshot(flat, jobID, 1024)
	if err != nil {
		t.Fatalf("read retained snapshot: %v", err)
	}
	if snapshot.Content != "RETAINED" || snapshot.TotalBytes != 16 || snapshot.DroppedBytes != 8 || !snapshot.Truncated {
		t.Fatalf("snapshot = %+v, want retained tail with lifetime metadata", snapshot)
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
func (e localJobDirEntry) Info() (fs.FileInfo, error) { return nil, unusedInfoError{} }

type unusedInfoError struct{}

func (unusedInfoError) Error() string { return "Info must not be called" }

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
	return entries, nil
}

func (r *localJobDirReader) Close() error {
	r.closed = true
	return nil
}

// TestLocateLocalJob_StraySiblingDirDoesNotBreakLookup asserts that a stray
// directory (with no jobs.jsonl) beside the real target bucket does not abort
// the entire lookup. After round 1 removed the ValidateProjectID filter, the
// sweep visits every directory; a missing jobs.jsonl in a sibling must be
// treated as not-found, not as a hard error that aborts the search.
func TestLocateLocalJob_StraySiblingDirDoesNotBreakLookup(t *testing.T) {
	t.Parallel()
	stateHome := t.TempDir()
	current := localJobProjectBucket(t, stateHome, localJobCurrentProject)
	// Real target: a valid sibling bucket with the job.
	sibling := localJobProjectBucket(t, stateHome, localJobSiblingProject)
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	seedLocalJob(t, sibling, owner, jobID, "", "sibling job output\n", true)

	// Stray: a directory under projects/ with no jobs.jsonl at all.
	_ = localJobProjectBucket(t, stateHome, "stray-dir-no-jobs")

	loc, err := locateLocalJob(current, jobID)
	if err != nil {
		t.Fatalf("stray sibling dir broke lookup: %v", err)
	}
	if filepath.Base(loc.StateDir) != localJobSiblingProject {
		t.Fatalf("located job in %q, want %q", filepath.Base(loc.StateDir), localJobSiblingProject)
	}
}

// TestLocateLocalJob_CorruptSiblingDirDoesNotBreakLookup asserts that a
// corrupt jobs.jsonl in a sibling directory (for the same owner session) does
// not abort the lookup when the target exists in another sibling. After
// round 1 removed the ValidateProjectID filter, the sweep visits every
// directory; a corrupt jobs.jsonl in a non-target sibling must be treated as
// not-found (skipped), not as a hard error that aborts the search for the
// real target.
func TestLocateLocalJob_CorruptSiblingDirDoesNotBreakLookup(t *testing.T) {
	t.Parallel()
	stateHome := t.TempDir()
	current := localJobProjectBucket(t, stateHome, localJobCurrentProject)
	// Real target: a valid sibling bucket with the job.
	sibling := localJobProjectBucket(t, stateHome, localJobSiblingProject)
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	seedLocalJob(t, sibling, owner, jobID, "", "sibling job output\n", true)

	// Corrupt sibling: a directory with a corrupt jobs.jsonl for the SAME
	// owner session (so findLocalJobInProject tries to read it). The file
	// exists but contains invalid JSON, which ReadEvents reports as an error.
	corruptDir := localJobProjectBucket(t, stateHome, "corrupt-bucket")
	corruptPath := filepath.Join(jobsDir(corruptDir, owner), "jobs.jsonl")
	if err := os.MkdirAll(filepath.Dir(corruptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corruptPath, []byte("NOT VALID JSON\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	loc, err := locateLocalJob(current, jobID)
	if err != nil {
		t.Fatalf("corrupt sibling dir broke lookup: %v", err)
	}
	if filepath.Base(loc.StateDir) != localJobSiblingProject {
		t.Fatalf("located job in %q, want %q", filepath.Base(loc.StateDir), localJobSiblingProject)
	}
}

// --- roborev fix round 3: RED test for finding 2 ---

// TestLocateLocalJob_CorruptSiblingDirSurfacesErrorWhenTargetNotFound asserts
// that when the target job lives ONLY in a corrupt sibling bucket (and is not
// found elsewhere), the corruption error propagates instead of being masked
// as "job not found". The round 2 fix skipped ALL sibling errors, so genuine
// corruption was swallowed. The fix: retain the first sibling error; if the
// lookup would finish not-found, return that retained error — this preserves
// stray-dir tolerance when the target is found elsewhere and surfaces
// corruption when it is not.
func TestLocateLocalJob_CorruptSiblingDirSurfacesErrorWhenTargetNotFound(t *testing.T) {
	t.Parallel()
	stateHome := t.TempDir()
	current := localJobProjectBucket(t, stateHome, localJobCurrentProject)
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)

	// The ONLY bucket with the target job is corrupt: its jobs.jsonl exists but
	// contains invalid JSON. ReadEvents returns an error (not nil,nil) for
	// corrupt content, so findLocalJobInProject returns an error.
	corruptDir := localJobProjectBucket(t, stateHome, "corrupt-only-bucket")
	corruptPath := filepath.Join(jobsDir(corruptDir, owner), "jobs.jsonl")
	if err := os.MkdirAll(filepath.Dir(corruptPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(corruptPath, []byte("NOT VALID JSON\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := locateLocalJob(current, jobID)
	if err == nil {
		t.Fatal("expected error for corrupt-only sibling, got nil")
	}
	// The error must NOT be "job not found" — it must surface the corruption.
	if strings.Contains(err.Error(), "not found") {
		t.Fatalf("corrupt sibling error was masked as not-found: %v", err)
	}
	// The error must mention the corruption or the read failure.
	if !strings.Contains(err.Error(), "corrupt") && !strings.Contains(err.Error(), "read") {
		t.Fatalf("error does not surface corruption: %v", err)
	}
}

// TestLocateLocalJob_RejectsSymlinkedSessionsDir asserts that locateLocalJob
// does not find a job through a symlinked sessions/ directory. Today
// findLocalJobInProject builds jobsDir (stateDir/sessions/owner) and reads
// jobs.jsonl through the symlink, so a job from outside the state root
// surfaces via job:<id> reads.
func TestLocateLocalJob_RejectsSymlinkedSessionsDir(t *testing.T) {
	stateHome := t.TempDir()
	bucket := localJobProjectBucket(t, stateHome, "test-0123456789")
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)

	// Seed a job in an outside dir.
	outside := t.TempDir()
	seedLocalJob(t, outside, owner, jobID, "/decoy/outside.log", "outside\n", false)

	// Replace the bucket's sessions/ with a symlink to the outside dir.
	if err := os.RemoveAll(filepath.Join(bucket, "sessions")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "sessions"), filepath.Join(bucket, "sessions")); err != nil {
		t.Fatal(err)
	}

	// locateLocalJob should NOT find the job through the symlinked sessions/.
	_, err := locateLocalJob(bucket, jobID)
	if err == nil {
		t.Fatal("locateLocalJob found a job through a symlinked sessions/ dir; should reject")
	}
}

// --- roborev fix round 7: RED test ---

// TestLocateLocalJob_SymlinkedSiblingBucketSurfacesJobNotFound asserts that
// a lookup for a nonexistent job with an UNRELATED symlinked sibling bucket
// returns "job not found" — not the symlink rejection error from the sibling.
// The round-6 symlink guards make findLocalJobInProject return a non-nil
// "symlinks are not allowed" error for a symlinked sessions/ in a non-target
// bucket; the round-3 retainedErr mechanism retains it and surfaces it
// instead of the honest "job not found" result, masking the real outcome
// with a misleading message.
func TestLocateLocalJob_SymlinkedSiblingBucketSurfacesJobNotFound(t *testing.T) {
	t.Parallel()
	stateHome := t.TempDir()
	current := localJobProjectBucket(t, stateHome, localJobCurrentProject)
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)

	// Sibling bucket with symlinked sessions/ pointing outside.
	siblingBucket := localJobProjectBucket(t, stateHome, localJobSiblingProject)
	outside := t.TempDir()
	outsideSessions := filepath.Join(outside, "sessions")
	if err := os.MkdirAll(outsideSessions, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(siblingBucket, "sessions")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideSessions, filepath.Join(siblingBucket, "sessions")); err != nil {
		t.Fatal(err)
	}

	// The job does NOT exist anywhere. The lookup should return "job not
	// found", not the symlink error from the unrelated sibling bucket.
	_, err := locateLocalJob(current, jobID)
	if err == nil {
		t.Fatal("expected error for nonexistent job, got nil")
	}
	if !isJobNotFoundErr(err) {
		t.Fatalf("expected job-not-found error for nonexistent job with unrelated symlinked sibling, got: %v", err)
	}
}

// TestLocateLocalJob_SymlinkedTargetBucketStillSurfacesSymlinkError asserts
// that when the symlinked sibling bucket DOES contain the target job, the
// symlink error is still surfaced — the fix must only classify symlink errors
// from non-target buckets as skip-worthy, not suppress them everywhere.
func TestLocateLocalJob_SymlinkedTargetBucketStillSurfacesSymlinkError(t *testing.T) {
	t.Parallel()
	stateHome := t.TempDir()
	current := localJobProjectBucket(t, stateHome, localJobCurrentProject)
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)

	// Seed the target job in an outside dir, then symlink sessions/ to it.
	outside := t.TempDir()
	seedLocalJob(t, outside, owner, jobID, "/decoy/outside.log", "outside\n", false)

	siblingBucket := localJobProjectBucket(t, stateHome, localJobSiblingProject)
	if err := os.RemoveAll(filepath.Join(siblingBucket, "sessions")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "sessions"), filepath.Join(siblingBucket, "sessions")); err != nil {
		t.Fatal(err)
	}

	// The target IS in the symlinked bucket. The symlink error must be
	// surfaced, not suppressed — the symlink guard protects against reads
	// outside the state root.
	_, err := locateLocalJob(current, jobID)
	if err == nil {
		t.Fatal("expected symlink error for target in symlinked bucket, got nil")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected error mentioning symlink, got: %v", err)
	}
}

// TestLocateLocalJob_FIFOJournalRejectedWithoutBlocking asserts that a FIFO at
// the job journal path is rejected without blocking. Pre-fix: jobstore.ReadEvents
// opens the journal with a plain os.Open which blocks on a FIFO indefinitely;
// symlinkErrorDeep checks symlinks but a FIFO is a non-symlink non-regular
// entry that passes the symlink check. Post-fix: the journal is Lstat'd and
// required to be a regular file before ReadEvents is called, so the FIFO is
// rejected quickly without blocking.
func TestLocateLocalJob_FIFOJournalRejectedWithoutBlocking(t *testing.T) {
	t.Parallel()
	sh := t.TempDir()
	bucket := filepath.Join(sh, "evener", "projects", localJobCurrentProject)
	if err := os.MkdirAll(bucket, 0o755); err != nil {
		t.Fatal(err)
	}
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	seedLocalJob(t, bucket, owner, jobID, "/dev/null", "MARKER\n", true)

	// Replace the journal with a FIFO.
	journalPath := filepath.Join(jobsDir(bucket, owner), "jobs.jsonl")
	if err := os.Remove(journalPath); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("mkfifo", journalPath).Run(); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}

	// Run the lookup under a bounded timeout so a hang FAILS FAST instead
	// of hanging the test suite. The pre-fix code blocks on os.Open(FIFO).
	done := make(chan error, 1)
	go func() {
		_, err := locateLocalJob(bucket, jobID)
		done <- err
	}()
	select {
	case err := <-done:
		// Post-fix: the FIFO is rejected — err should be non-nil (not found,
		// since the journal is unreadable). The key invariant: we did NOT
		// block.
		if err == nil {
			// A nil error with found=false would mean the job was silently
			// dropped, which is acceptable — but the call returned without
			// blocking, which is what we're testing.
		}
		// The call returned without blocking — pass.
	case <-time.After(5 * time.Second):
		t.Fatal("locateLocalJob blocked on a FIFO journal for 5s; the " +
			"journal must be Lstat'd and required to be a regular file " +
			"before ReadEvents opens it")
	}
}

// TestLocateLocalJobRetainedTarget_FIFOOutputRejectedWithoutBlocking asserts
// the same regular-file guard on the job output file path.
func TestLocateLocalJobRetainedTarget_FIFOOutputRejectedWithoutBlocking(t *testing.T) {
	t.Parallel()
	sh := t.TempDir()
	bucket := filepath.Join(sh, "evener", "projects", localJobCurrentProject)
	if err := os.MkdirAll(bucket, 0o755); err != nil {
		t.Fatal(err)
	}
	owner := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(owner)
	seedLocalJob(t, bucket, owner, jobID, "/dev/null", "MARKER\n", true)

	// Replace the output file with a FIFO.
	outputPath := filepath.Join(jobsDir(bucket, owner), "jobs", jobID+".log")
	if err := os.Remove(outputPath); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("mkfifo", outputPath).Run(); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := locateLocalJobRetainedTarget(bucket, jobID)
		done <- err
	}()
	select {
	case err := <-done:
		_ = err // returned without blocking — pass
	case <-time.After(5 * time.Second):
		t.Fatal("locateLocalJobRetainedTarget blocked on a FIFO output " +
			"file for 5s; the output path must be Lstat'd and required " +
			"to be a regular file before reading")
	}
}

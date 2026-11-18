//go:build unix

package agent

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/identifier"
)

// TestLoadSessionJobGetRejectsFIFOJournal proves the past-session reader does
// not hand a FIFO to jobstore's plain os.Open, which blocks until a writer
// connects. The symlink half of the same guard is portable and lives in
// TestLoadSessionJobGetRejectsUnsafeJournal.
func TestLoadSessionJobGetRejectsFIFOJournal(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sessionID := identifier.MustNewSessionID()
	journalDir := jobsDir(dir, sessionID)
	if err := os.MkdirAll(journalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(journalDir, "jobs.jsonl")
	if err := syscall.Mkfifo(journal, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, found, err := LoadSessionJobGet(dir, sessionID, "job_x", nil)
		if found {
			err = errors.New("FIFO journal was read as a job")
		}
		done <- err
	}()
	select {
	case err := <-done:
		_ = err // not-found or an error both reject the FIFO safely.
	// TRIPWIRE: this short bound is the assertion that os.Open did not block on the FIFO.
	case <-time.After(100 * time.Millisecond):
		// Pair with a writer to release the baseline's blocked os.Open, then
		// fail explicitly instead of leaving a goroutine behind.
		writerDone := make(chan error, 1)
		go func() {
			f, err := os.OpenFile(journal, os.O_WRONLY, 0)
			if err == nil {
				err = f.Close()
			}
			writerDone <- err
		}()
		select {
		case <-done:
		// TRIPWIRE: this bound prevents a broken FIFO-release guard from hanging the suite.
		case <-time.After(time.Second):
			t.Fatal("LoadSessionJobGet remained blocked after opening FIFO writer")
		}
		if err := <-writerDone; err != nil {
			t.Fatalf("release FIFO reader: %v", err)
		}
		t.Fatal("LoadSessionJobGet blocked opening FIFO journal")
	}
}

// TestLoadSessionJobOutputPageFencesAliasedStateHomePath pins the fence against
// a state home reached through a SYMLINK, which is a layout the rest of this
// path already supports. The hub is started with the physical bucket directory,
// while a legacy row's explicit output path was written under an alias — the
// same directory, spelled through a symlinked ancestor. Comparing the two
// LEXICALLY says the path is outside every session work directory, so the fence
// never names the session that owns the bytes and the log is served while that
// session's deletion is durable.
//
// The session a path belongs to is a fact about the file, not about the string,
// so the answer has to come from filesystem identity.
func TestLoadSessionJobOutputPageFencesAliasedStateHomePath(t *testing.T) {
	t.Parallel()
	// A state home laid out as stateHome/evener/projects/<bucket>, reached two
	// ways: physically, and through a symlink to the same home.
	physicalHome := t.TempDir()
	bucket := filepath.Join(physicalHome, "evener", "projects", "bucket")
	if err := os.MkdirAll(bucket, 0o755); err != nil {
		t.Fatal(err)
	}
	aliasHome := filepath.Join(t.TempDir(), "aliased-home")
	if err := os.Symlink(physicalHome, aliasHome); err != nil {
		t.Fatalf("create state-home alias: %v", err)
	}
	aliasBucket := filepath.Join(aliasHome, "evener", "projects", "bucket")
	if resolved, err := filepath.EvalSymlinks(aliasBucket); err != nil || resolved != bucket {
		t.Fatalf("fixture invalid: alias resolves to %q (err %v), want %q", resolved, err, bucket)
	}

	requestedSession := identifier.MustNewSessionID()
	originSession := identifier.MustNewSessionID()
	jobID := "job_legacy_aliased_9f21"
	started := time.Date(2026, 11, 19, 15, 0, 0, 0, time.UTC)

	// The log lives under the origin session, and the row reaches it through the
	// ALIAS spelling of the bucket.
	originLogPath := filepath.Join(aliasBucket, "sessions", originSession, "jobs", jobID+".log")
	physicalLogPath := filepath.Join(bucket, "sessions", originSession, "jobs", jobID+".log")
	if err := os.MkdirAll(filepath.Dir(physicalLogPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(physicalLogPath, []byte("aliased origin bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(originLogPath); err != nil {
		t.Fatalf("fixture invalid: the aliased path does not reach the log: %v", err)
	}

	// The forwarded row states no owner and carries the ALIASED path.
	writeJobJournalLines(t, jobsDir(bucket, requestedSession),
		`{"kind":"job_started","seq":1,"ts":"`+started.Format(time.RFC3339)+
			`","job_id":"`+jobID+`","type":"shell","status":"running","visible_to_session_id":"`+requestedSession+
			`","started_at":"`+started.Format(time.RFC3339)+
			`","output_path":"`+originLogPath+`"}`+"\n",
	)
	writeJobJournalLines(t, jobsDir(bucket, originSession), "")

	// The origin session's deletion is durable; its log has not been removed.
	fence := func(sessionID string) (bool, error) { return sessionID == originSession, nil }
	page, found, err := LoadSessionJobOutputPage(bucket, requestedSession, jobID, nil, 64, fence)
	if err != nil {
		t.Fatalf("LoadSessionJobOutputPage: %v", err)
	}
	if found || page.Data != "" {
		t.Fatalf("found = %v, data = %q: the path reaches a fenced session through a state-home alias, "+
			"so those bytes must not be served", found, page.Data)
	}

	// Abandoning the deletion clears the record and the read resumes, which is
	// the same guarantee the lexically-spelled case has.
	page, found, err = LoadSessionJobOutputPage(bucket, requestedSession, jobID, nil, 64,
		func(string) (bool, error) { return false, nil })
	if err != nil {
		t.Fatalf("LoadSessionJobOutputPage after abandonment: %v", err)
	}
	if !found || page.Data != "aliased origin bytes" {
		t.Fatalf("found = %v, data = %q: an abandoned deletion must not make the job unreadable",
			found, page.Data)
	}
}

// TestJobOutputPathSessionNamesAliasedSession: the helper that answers "whose
// session directory contains this path" must answer by identity. A path spelled
// through a symlinked state home names the same session as the physical
// spelling, and a path outside the root names none. The second assertion is what
// keeps the identity walk from becoming a blanket "some ancestor matched".
func TestJobOutputPathSessionNamesAliasedSession(t *testing.T) {
	t.Parallel()
	physicalHome := t.TempDir()
	bucket := filepath.Join(physicalHome, "evener", "projects", "bucket")
	if err := os.MkdirAll(bucket, 0o755); err != nil {
		t.Fatal(err)
	}
	aliasHome := filepath.Join(t.TempDir(), "aliased-home")
	if err := os.Symlink(physicalHome, aliasHome); err != nil {
		t.Fatalf("create state-home alias: %v", err)
	}
	aliasBucket := filepath.Join(aliasHome, "evener", "projects", "bucket")

	sessionID := identifier.MustNewSessionID()
	physicalPath := filepath.Join(jobsDir(bucket, sessionID), "jobs", "job_x.log")
	if err := os.MkdirAll(filepath.Dir(physicalPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(physicalPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	aliasedPath := filepath.Join(aliasBucket, "sessions", sessionID, "jobs", "job_x.log")

	for name, path := range map[string]string{"physical": physicalPath, "aliased": aliasedPath} {
		if got := jobOutputPathSession(bucket, path); got != sessionID {
			t.Fatalf("%s spelling: session = %q, want %q", name, got, sessionID)
		}
	}

	// A launcher-chosen path outside every session directory names no session.
	outside := filepath.Join(t.TempDir(), "chosen.log")
	if err := os.WriteFile(outside, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := jobOutputPathSession(bucket, outside); got != "" {
		t.Fatalf("session = %q, want none for a path outside the state root", got)
	}
}

// TestLoadSessionJobGetReportsUnopenableOwnerJournalAsUnavailable pins the
// diagnostic a client acts on when a journal it could not OPEN is reported as
// damaged. The owner's journal passes path validation — it is a regular file at
// the expected path — and then cannot be opened, which is an access failure on
// this machine rather than anything about the bytes. Reporting
// journal_scan_incomplete says the scan stopped at a corrupt line, which is a
// claim that nothing read supports, and it hides the fact that matters:
// owner_unavailable, "the lookup could not obtain a usable owner record".
//
// The two codes are compared as VALUES, not against the constant the
// implementation uses to emit them, so the test pins the wire contract.
func TestLoadSessionJobGetReportsUnopenableOwnerJournalAsUnavailable(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root can open a 0000 file, so the fixture cannot produce an open failure")
	}
	stateDir := t.TempDir()
	requestedSession := identifier.MustNewSessionID()
	ownerSession := identifier.MustNewSessionID()
	jobID := identifier.MustNewJobID(ownerSession)
	started := time.Date(2026, 11, 19, 16, 0, 0, 0, time.UTC)

	// The requested session holds a forwarded start for the job; the encoded
	// owner's journal is present and unreadable.
	writeJobJournalLines(t, jobsDir(stateDir, requestedSession), jobStartedLine(jobID, ownerSession, started)+"\n")
	ownerJournal := filepath.Join(jobsDir(stateDir, ownerSession), "jobs.jsonl")
	if err := os.MkdirAll(filepath.Dir(ownerJournal), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ownerJournal, []byte(jobStartedLine(jobID, ownerSession, started)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(ownerJournal, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ownerJournal, 0o600) })
	if f, err := os.Open(ownerJournal); err == nil {
		_ = f.Close()
		t.Skip("the fixture cannot produce an open failure in this environment")
	}

	job, found, err := LoadSessionJobGet(stateDir, requestedSession, jobID, nil)
	if err != nil {
		t.Fatalf("LoadSessionJobGet: %v", err)
	}
	if !found {
		t.Fatal("found = false, want the forwarded start served as flagged fallback")
	}
	if !slices.Contains(job.Diagnostics, "owner_unavailable") {
		t.Fatalf("Diagnostics = %v, want owner_unavailable: the owner journal could not be opened", job.Diagnostics)
	}
	if slices.Contains(job.Diagnostics, "journal_scan_incomplete") {
		t.Fatalf("Diagnostics = %v, want no journal_scan_incomplete: an open failure observes nothing about the "+
			"journal's bytes, and claiming durable damage tells the client the history is corrupt", job.Diagnostics)
	}

	// The other direction, from the same fixture: once the file is readable the
	// damage code is still not claimed for a journal that reads cleanly.
	if err := os.Chmod(ownerJournal, 0o600); err != nil {
		t.Fatal(err)
	}
	job, found, err = LoadSessionJobGet(stateDir, requestedSession, jobID, nil)
	if err != nil {
		t.Fatalf("LoadSessionJobGet after chmod: %v", err)
	}
	if !found {
		t.Fatal("found = false after the journal became readable")
	}
	if slices.Contains(job.Diagnostics, "journal_scan_incomplete") {
		t.Fatalf("Diagnostics = %v, want no journal_scan_incomplete for a journal that reads cleanly", job.Diagnostics)
	}
}

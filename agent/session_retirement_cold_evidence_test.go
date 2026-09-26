package agent

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/internal/jobstore"
)

// TestRetirementColdEvidenceValidatesRestoreCriticalStores proves a cold
// member's retirement evidence rejects the same malformed durable stores its
// next restore would fail on. The cold path folds the job journal, but a
// restore also opens the client-mutation snapshot and the task store, so a
// corrupt one must block retirement here rather than fail the later restore.
func TestRetirementColdEvidenceValidatesRestoreCriticalStores(t *testing.T) {
	t.Parallel()
	const (
		sessionID  = "cold-restore-critical"
		delegateID = "dlg_cold_restore_critical"
	)
	// Evidence collection reads the member's job journal first, so every case
	// needs a regular (empty) journal to reach the store checks.
	writeJobJournal := func(t *testing.T, stateDir string) {
		t.Helper()
		dir := jobsDir(stateDir, sessionID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "jobs.jsonl"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	corrupt := func(t *testing.T, stateDir, rel string) {
		t.Helper()
		path := filepath.Join(stateDir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("healthy cold member passes", func(t *testing.T) {
		stateDir := t.TempDir()
		writeJobJournal(t, stateDir)
		blockers, err := retirementColdEvidence(stateDir, sessionID, delegateID)
		if err != nil {
			t.Fatalf("retirementColdEvidence rejected a healthy cold member: %v", err)
		}
		if len(blockers) != 0 {
			t.Fatalf("blockers = %+v, want none", blockers)
		}
	})

	for _, tc := range []struct {
		name string
		rel  string
	}{
		{"client mutation snapshot", filepath.Join(clientMutationPersistSubdir, sessionID+".json")},
		{"task store", filepath.Join("tasks", sessionID+".json")},
	} {
		t.Run("malformed "+tc.name+" blocks", func(t *testing.T) {
			stateDir := t.TempDir()
			writeJobJournal(t, stateDir)
			corrupt(t, stateDir, tc.rel)
			blockers, err := retirementColdEvidence(stateDir, sessionID, delegateID)
			if err == nil {
				t.Fatalf("retirementColdEvidence accepted a malformed %s: blockers=%+v", tc.name, blockers)
			}
			if !hasColdEvidenceBlocker(blockers, sessionID, delegateID) {
				t.Fatalf("blockers = %+v, want an unsupported blocker for %s/%s", blockers, sessionID, delegateID)
			}
		})
	}
}

func hasColdEvidenceBlocker(blockers []RetirementBlocker, sessionID, delegateID string) bool {
	for _, blocker := range blockers {
		if blocker.Category == "unsupported" && blocker.SessionID == sessionID && blocker.DelegateID == delegateID {
			return true
		}
	}
	return false
}

func hasColdEvidenceCategory(blockers []RetirementBlocker, category, sessionID, delegateID string) bool {
	for _, blocker := range blockers {
		if blocker.Category == category && blocker.SessionID == sessionID && blocker.DelegateID == delegateID {
			return true
		}
	}
	return false
}

// TestRetirementColdEvidenceMissingJobJournalIsEligible is the round-19
// regression test for the cold-eligibility inversion: retirementJobJournal
// opened with os.Stat and turned any error into an "unsupported" blocker, so a
// member that never ran a job — and legitimately has no jobs.jsonl — could
// never become retirement-eligible, while a restore of that same member reads a
// missing journal as an empty one (jobstore.ScanEventsFrom tolerates a missing
// path, and jobstore.Store.Load reads the same file through the same reader).
// Only a member that never materialized a job manager may read absence as an
// empty journal; a member that carries the job output directory its journal was
// created beside has lost durable evidence, and that case must stay unsupported.
// Every other outcome stays exactly as it was: a real journal's non-terminal job
// still blocks, a malformed journal is still unsupported, and an unreadable
// journal (a stat failure that is not absence) is still unsupported rather than
// silently empty.
func TestRetirementColdEvidenceMissingJobJournalIsEligible(t *testing.T) {
	const (
		sessionID  = "cold-missing-journal"
		delegateID = "dlg_cold_missing_journal"
	)
	t.Run("no journal is eligible", func(t *testing.T) {
		stateDir := t.TempDir()
		blockers, err := retirementColdEvidence(stateDir, sessionID, delegateID)
		if err != nil {
			t.Fatalf("retirementColdEvidence rejected a cold member with no job journal: %v", err)
		}
		if len(blockers) != 0 {
			t.Fatalf("blockers = %+v, want none for a member that never ran a job", blockers)
		}
	})

	t.Run("non-terminal job still blocks", func(t *testing.T) {
		stateDir := t.TempDir()
		journalDir := jobsDir(stateDir, sessionID)
		if err := os.MkdirAll(journalDir, 0o755); err != nil {
			t.Fatal(err)
		}
		store, err := jobstore.OpenNoSync(filepath.Join(journalDir, "jobs.jsonl"))
		if err != nil {
			t.Fatalf("open job journal: %v", err)
		}
		defer store.Close()
		if err := store.Append(jobstore.Event{
			Kind:           jobstore.EventJobStarted,
			JobID:          "job_cold_missing_journal",
			OwnerSessionID: sessionID,
			Status:         jobstore.StatusRunning,
		}); err != nil {
			t.Fatalf("append job_started: %v", err)
		}
		blockers, err := retirementColdEvidence(stateDir, sessionID, delegateID)
		if err != nil {
			t.Fatalf("retirementColdEvidence failed on a real journal: %v", err)
		}
		if !hasColdEvidenceCategory(blockers, "job", sessionID, delegateID) {
			t.Fatalf("blockers = %+v, want a job blocker for the member's non-terminal job", blockers)
		}
		if hasColdEvidenceBlocker(blockers, sessionID, delegateID) {
			t.Fatalf("blockers = %+v, want no unsupported blocker for a decodable journal", blockers)
		}
	})

	t.Run("malformed journal is still unsupported", func(t *testing.T) {
		stateDir := t.TempDir()
		journalDir := jobsDir(stateDir, sessionID)
		if err := os.MkdirAll(journalDir, 0o755); err != nil {
			t.Fatal(err)
		}
		// A newline-terminated invalid record is durable corruption, not an
		// in-flight append the scanner is allowed to tolerate.
		if err := os.WriteFile(filepath.Join(journalDir, "jobs.jsonl"), []byte("{not valid json}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		blockers, err := retirementColdEvidence(stateDir, sessionID, delegateID)
		if err == nil {
			t.Fatalf("retirementColdEvidence accepted a malformed journal: blockers=%+v", blockers)
		}
		if !hasColdEvidenceBlocker(blockers, sessionID, delegateID) {
			t.Fatalf("blockers = %+v, want an unsupported blocker", blockers)
		}
	})

	t.Run("unreadable journal is still unsupported", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("a directory mode that denies the owner cannot be created as root")
		}
		stateDir := t.TempDir()
		journalDir := jobsDir(stateDir, sessionID)
		if err := os.MkdirAll(journalDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(journalDir, "jobs.jsonl"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		// Without search permission the journal cannot be stat'ed at all: this is
		// a permission failure, not an absent journal, and must stay unsupported.
		if err := os.Chmod(journalDir, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(journalDir, 0o755) })
		blockers, err := retirementColdEvidence(stateDir, sessionID, delegateID)
		if err == nil {
			t.Fatalf("retirementColdEvidence treated an unreadable journal as empty: blockers=%+v", blockers)
		}
		if !hasColdEvidenceBlocker(blockers, sessionID, delegateID) {
			t.Fatalf("blockers = %+v, want an unsupported blocker", blockers)
		}
	})

	t.Run("journal lost after materializing is still unsupported", func(t *testing.T) {
		stateDir := t.TempDir()
		journalDir := jobsDir(stateDir, sessionID)
		// newJobManagerWithRestore (agent/jobs.go) creates the job output
		// directory and the journal together, so a member that still carries the
		// output directory lost its journal rather than never having one: the
		// missing journal is unreadable evidence, and the member must stay
		// resident.
		if err := os.MkdirAll(filepath.Join(journalDir, "jobs"), 0o755); err != nil {
			t.Fatal(err)
		}
		blockers, err := retirementColdEvidence(stateDir, sessionID, delegateID)
		if err == nil {
			t.Fatalf("retirementColdEvidence accepted a member whose journal was removed: blockers=%+v", blockers)
		}
		if !hasColdEvidenceBlocker(blockers, sessionID, delegateID) {
			t.Fatalf("blockers = %+v, want an unsupported blocker", blockers)
		}
	})
}

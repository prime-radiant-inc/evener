package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRetirementColdEvidenceValidatesRestoreCriticalStores proves a cold
// member's retirement evidence rejects the same malformed durable stores its
// next restore would fail on. The cold path folds the job journal, but a
// restore also opens the client-mutation snapshot and the task store, so a
// corrupt one must block retirement here rather than fail the later restore.
func TestRetirementColdEvidenceValidatesRestoreCriticalStores(t *testing.T) {
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

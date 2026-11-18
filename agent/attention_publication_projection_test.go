package agent

import (
	"testing"

	"primeradiant.com/evener/llm"
)

// TestPublicationWaitsForTheProjectionItSharesAFactWith pins that a delivery's
// report cannot enter resident history outside the lock a projection captures
// with.
//
// A report is recorded before its acknowledgment and published after it, because
// the acknowledgment is what raises the release floor the report is presented
// against. The projection copies the holds that filter the request and the turns
// they filter under one attentionMu hold. Publication must take that same lock:
// otherwise it can land between the two copies, and the request includes the
// newly published report while the copied holds still exclude the report that
// same acknowledgment released — a report presented whose release is unread.
//
// The proof is a lock probe, not a scheduling race. The seam fires inside
// publication's critical section, immediately before the report reaches
// history, and the probe asks TryLock whether attentionMu is held there. A
// publication that takes no lock reports the lock as free, which is the failure.
// An earlier version of this test signalled a goroutine before calling publish
// and checked that it had not finished: a publication that took no lock could
// still be between the signal and its write, so the test passed with the lock
// removed.
func TestPublicationWaitsForTheProjectionItSharesAFactWith(t *testing.T) {
	const newlyID = "delegate:delivery-new-report"
	profile := namedInstanceProfile("ant", "anthropic", "claude-sonnet-4-5")
	stateDir := realTempDirForTest(t)
	runtime := newSession(t, withAdapter(&fakeAdapter{name: "ant"}), withProfile(profile), withDir(stateDir), withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}))

	newTurn, _, err := runtime.appendDelegateAttentionMessageDurablyDeferred(newlyID, llm.User("the newly acknowledged report"))
	if err != nil {
		t.Fatalf("deposit the new report: %v", err)
	}

	// The probe asks the question at the write, inside whatever hold the
	// publication took. TryLock succeeding means the lock was not held there.
	sawTheWrite := false
	heldAtTheWrite := false
	updateSessionTestConfig(runtime, func(cfg *testConfig) {
		cfg.delegateAttentionBeforeReportPublication = func() {
			sawTheWrite = true
			if runtime.attentionMu.TryLock() {
				runtime.attentionMu.Unlock()
				return
			}
			heldAtTheWrite = true
		}
	})
	if err := runtime.publishAcknowledgedDelegateAttentionReport(newlyID, newTurn); err != nil {
		t.Fatalf("publish the acknowledged report: %v", err)
	}
	updateSessionTestConfig(runtime, func(cfg *testConfig) {
		cfg.delegateAttentionBeforeReportPublication = nil
	})
	if !sawTheWrite {
		t.Fatal("the publication reached history without passing the seam: this test cannot see whether the write was guarded, so it proves nothing")
	}
	if !heldAtTheWrite {
		t.Fatal("publication wrote the report while attentionMu was free: it must take the same lock the projection's capture takes, or it can land between the held IDs and the turns and the request presents the new report while withholding the one its acknowledgment released")
	}

	// Taking the lock is only half of it: the report also has to land.
	runtime.mu.Lock()
	published := false
	for _, turn := range runtime.history {
		if turn.AttentionID == newlyID {
			published = true
		}
	}
	runtime.mu.Unlock()
	if !published {
		t.Fatalf("publication completed without putting %q into resident history", newlyID)
	}
}

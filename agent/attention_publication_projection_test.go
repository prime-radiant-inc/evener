package agent

import (
	"testing"

	"primeradiant.com/evener/llm"
)

// TestPublicationWaitsForTheProjectionItSharesAFactWith pins that a delivery's
// report cannot enter resident history while a projection is capturing.
//
// A report is recorded before its acknowledgment and published after it, because
// the acknowledgment is what raises the release floor the report is presented
// against. The projection copies the holds that filter the request and the turns
// they filter under one attentionMu hold. Publication must take that same lock:
// otherwise it can land between the two copies, and the request includes the
// newly published report while the copied holds still exclude the report that
// same acknowledgment released — a report presented whose release is unread.
//
// The test holds attentionMu as a projection does and starts a publication that
// signals before it publishes. Receiving that signal proves the publication
// reached the call; the non-blocking check that follows proves it has not
// finished. A publication that does not take the lock finishes here.
func TestPublicationWaitsForTheProjectionItSharesAFactWith(t *testing.T) {
	const newlyID = "delegate:delivery-new-report"
	profile := namedInstanceProfile("ant", "anthropic", "claude-sonnet-4-5")
	stateDir := realTempDirForTest(t)
	runtime := newSession(t, withAdapter(&fakeAdapter{name: "ant"}), withProfile(profile), withDir(stateDir), withConfig(SessionConfig{StateDir: stateDir, MaxSubagentDepth: 1}))

	newTurn, _, err := runtime.appendDelegateAttentionMessageDurablyDeferred(newlyID, llm.User("the newly acknowledged report"))
	if err != nil {
		t.Fatalf("deposit the new report: %v", err)
	}

	// Stand in for the projection's capture: it holds attentionMu across the
	// held IDs and the turns they filter.
	runtime.attentionMu.Lock()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- runtime.publishAcknowledgedDelegateAttentionReport(newlyID, newTurn)
	}()
	<-started
	select {
	case err := <-done:
		runtime.attentionMu.Unlock()
		t.Fatalf("publication completed while the projection's capture held attentionMu (err=%v): it must take the same lock, or it can land between the held IDs and the turns and the request presents the new report while withholding the one its acknowledgment released", err)
	default:
		// Still publishing: blocked on the capture, as it must be.
	}
	runtime.attentionMu.Unlock()

	if err := <-done; err != nil {
		t.Fatalf("publish the acknowledged report: %v", err)
	}
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

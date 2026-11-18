package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// TestAnUnavailableFoldKeepsAnUnadmittedFrameWithheld pins the direction the
// empty-fold fix must not break: a fold that could not run is not an answer.
//
// The gate withholds a stable watch frame only while it is still pending, and
// it reads the pending set as authoritative whenever the set is non-nil. An
// empty set is therefore a positive statement — nothing is pending — so a frame
// missing from it reads as resolved and presents. That is right for a fold that
// ran and found nothing, and wrong for a fold that never ran: a transient
// transcript read failure would present a frame whose wake nothing admitted,
// and its delayed arm would find the row resolved and skip the release it owed
// an earlier stopped report. The two answers keep different identities, nil for
// unavailable and a non-nil empty set for answered, and this pins the
// unavailable side through the presentation predicate itself.
func TestAnUnavailableFoldKeepsAnUnadmittedFrameWithheld(t *testing.T) {
	t.Parallel()
	sess, fixtureReopenID, _ := deriveHeldFixtureWithJournal(t, delegatestore.OutcomeCancelled, "cancelled")
	// The fixture leaves its own reopened row pending and binds the session to
	// its delegate, which the gate requires before it withholds anything.
	if err := sess.resolveAttentionDurablyForGeneration([]string{fixtureReopenID}, delegateAttentionConsumed, 0); err != nil {
		t.Fatalf("clear the fixture's pending row: %v", err)
	}
	const frameID = "watch:wd_unavailable:1"
	if _, err := sess.appendDelegateNotificationDurably(frameID, "a frame whose arm never ran"); err != nil {
		t.Fatalf("append the watch frame: %v", err)
	}
	sess.attentionMu.Lock()
	answered := delegateAttentionPendingSet(sess.delegateAttentionPendingIDsLocked())
	withheldAnswered := sess.delegateAttentionAwaitingAdmissionLocked(frameID, answered)
	sess.attentionMu.Unlock()
	if answered == nil {
		t.Fatal("premise broken: the fold did not run, so this is not the answered case")
	}
	if !withheldAnswered {
		t.Fatalf("a pending unadmitted frame must be withheld by the answered fold: %q", frameID)
	}
	// The unanswered case: nil is the fold that could not run, and the frame must
	// stay withheld under the stricter reading the gate applies without a set.
	sess.attentionMu.Lock()
	withheldUnavailable := sess.delegateAttentionAwaitingAdmissionLocked(frameID, delegateAttentionPendingSet(nil))
	sess.attentionMu.Unlock()
	if !withheldUnavailable {
		t.Fatalf("an unavailable fold admitted %q: a transient read failure would present a frame whose wake nothing admitted", frameID)
	}
	// The builder must keep the two apart. Folding unavailable into empty is the
	// defect, and folding empty into unavailable is the regression this fix was
	// for — the gate would fall back to the wake set and drop the trigger
	// acceptance just consumed.
	if unavailable := delegateAttentionPendingSet(nil); unavailable != nil {
		t.Fatalf("the builder turned an unavailable fold into an authoritative empty set: the gate then reads every row as resolved and admits frames nothing confirmed")
	}
	if answeredEmpty := delegateAttentionPendingSet([]string{}); answeredEmpty == nil {
		t.Fatal("the builder turned an answered empty fold into an unavailable one: the gate falls back to the wake set and drops the trigger acceptance just consumed")
	}
}

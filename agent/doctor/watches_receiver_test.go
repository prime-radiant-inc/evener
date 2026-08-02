package doctor

import (
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/internal/jobstore"
)

const (
	receiverWatchID  = "watch_02wMz5TxvEMoJEDTDGOTa4"
	ownWatchID       = "watch_02wMz5TxvEMoJEDTDGOTa5"
	receiverJobID    = "job_02wMz5TxvEMoJEDTDGOTb3"
	receiverSession  = "01SESSIONOBSERVER00000000"
	receiverDelegate = "dlg_02wMz5TxvEMoJEDTDGOTd1"
)

// receiverWatchFixture puts the two shapes side by side that owner/visible alone
// cannot tell apart: a cross-session receiver watch, installed on the OWNER's
// manager so both session fields name the owner, and the owner's own watch on
// the same job.
func receiverWatchFixture(t *testing.T) (base, sid string) {
	t.Helper()
	base = t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	sid = sidA
	writeSession(t, bucket, sid)

	writeJobsEvents(t, filepath.Join(bucket, "sessions", sid, "jobs.jsonl"), []jobstore.Event{
		{Kind: jobstore.EventJobStarted, JobID: receiverJobID, Type: jobstore.JobShell,
			Command: "npm run dev", OwnerSessionID: sidA, VisibleToSession: sidA, StartedAt: &jobStartedAt},

		{Kind: jobstore.EventWatchRegistered, WatchID: receiverWatchID, Watch: &jobstore.WatchEvent{
			Generation: watchGenerationID, OwnerSessionID: sidA, VisibleSessionID: sidA,
			Target: receiverJobID, Condition: "output_match:ready", ConfigHash: "cfg4",
			Config: &jobstore.WatchConfigSnapshot{
				Target: receiverJobID, OutputMatch: "ready",
				ReceiverSessionID: receiverSession, ReceiverDelegateID: receiverDelegate}}},

		{Kind: jobstore.EventWatchRegistered, WatchID: ownWatchID, Watch: &jobstore.WatchEvent{
			Generation: watchGenerationID, OwnerSessionID: sidA, VisibleSessionID: sidA,
			Target: receiverJobID, Condition: "output_match:ready", ConfigHash: "cfg5",
			Config: &jobstore.WatchConfigSnapshot{Target: receiverJobID, OutputMatch: "ready"}}},
	})
	return base, sid
}

// "Who receives this watch" is the one field an operator needs when a receiver link
// or a delivery is missing, and owner/visible cannot answer it: both name the
// owner on a receiver watch just as they do on the owner's own.
func TestWatches_ReceiverIdentityReported(t *testing.T) {
	base, sid := receiverWatchFixture(t)
	r, err := Watches(base, sid, WatchOpts{})
	if err != nil {
		t.Fatalf("Watches: %v", err)
	}
	receiver := findWatch(r, receiverWatchID)
	if receiver == nil {
		t.Fatalf("receiver watch missing from report %+v", r.Watches)
	}
	if receiver.ReceiverSessionID != receiverSession || receiver.ReceiverDelegateID != receiverDelegate {
		t.Errorf("receiver watch receiver = %q/%q, want %q/%q",
			receiver.ReceiverSessionID, receiver.ReceiverDelegateID, receiverSession, receiverDelegate)
	}
	own := findWatch(r, ownWatchID)
	if own == nil {
		t.Fatalf("owner watch missing from report %+v", r.Watches)
	}
	if own.ReceiverSessionID != "" || own.ReceiverDelegateID != "" {
		t.Errorf("owner watch receiver = %q/%q, want both empty", own.ReceiverSessionID, own.ReceiverDelegateID)
	}
}

// The rendered row says it too, and only when there is a receiver to name — an
// owner's own watch reads exactly as it did.
func TestWatches_RenderNamesTheReceiverOnlyWhenThereIsOne(t *testing.T) {
	base, sid := receiverWatchFixture(t)
	r, err := Watches(base, sid, WatchOpts{WatchID: receiverWatchID})
	if err != nil {
		t.Fatalf("Watches: %v", err)
	}
	out := RenderWatches(r)
	for _, want := range []string{"receiver session=" + receiverSession, "receiver delegate=" + receiverDelegate} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q; got:\n%s", want, out)
		}
	}

	own, err := Watches(base, sid, WatchOpts{WatchID: ownWatchID})
	if err != nil {
		t.Fatalf("Watches: %v", err)
	}
	if ownOut := RenderWatches(own); strings.Contains(ownOut, "receiver") {
		t.Errorf("owner watch render mentions a receiver; got:\n%s", ownOut)
	}
}

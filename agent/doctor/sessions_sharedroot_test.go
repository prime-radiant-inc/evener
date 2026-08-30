package doctor

import (
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
)

// sharedRootFixture builds one root session with a delegates.jsonl and N child
// sessions whose meta.JobTreeRootSessionID all point at that root — the shape
// ListSessions sweeps on a real state root, where one multi-megabyte delegate
// journal is shared by many child rows.
// sharedRootFixtureTB builds the shared-root fixture over testing.TB so both
// the test and the benchmark can build the same shape.
func sharedRootFixtureTB(t testing.TB, children int) (base, root string) {
	t.Helper()
	base = t.TempDir()
	bucket := stateHomeBucket(base, hash1)
	root = newSessionsTestSID(t)
	now := time.Now()

	// root session with a delegate journal (one created delegate)
	sharedChild := newSessionsTestSID(t)
	writeSessionsFixtureSession(t, bucket, root,
		transcript.Header{CreatedAt: now.Add(-2 * time.Hour), Model: "anthropic/claude-a"},
		nil,
		schema.SessionMeta{Model: "anthropic/claude-a", TurnCount: 0},
		nil,
		now.Add(-1*time.Hour),
	)
	writeDelegateEvents(t, bucket+"/sessions/"+root+"/delegates.jsonl", []delegatestore.Event{
		{Kind: delegatestore.EventDelegateCreated, DelegateID: "del1", Created: &delegatestore.DelegateCreated{Descriptor: delegatestore.Descriptor{
			ChildSessionID:   sharedChild,
			TranscriptRef:    "proj:" + hash1 + ":" + sharedChild,
			OwnerSessionID:   root,
			VisibleSessionID: root,
			Task:             "shared root child",
			AgentType:        "subagent",
			ToolNameCeiling:  []string{"communicate"},
			Resumable:        true,
		}}},
	})

	for range children {
		sid := newSessionsTestSID(t)
		writeSessionsFixtureSession(t, bucket, sid,
			transcript.Header{CreatedAt: now.Add(-90 * time.Minute), Model: "anthropic/claude-a", ParentSessionID: root},
			nil,
			schema.SessionMeta{Model: "anthropic/claude-a", TurnCount: 0, IsSubagent: true, JobTreeRootSessionID: root},
			nil,
			now.Add(-1*time.Hour),
		)
	}
	return base, root
}

// TestListSessions_SharedRootDelegatesReadOnce proves the shared-root delegates
// journal is read and folded once per ListSessions sweep, not once per child
// session. On the pre-fix code every child row re-reads and re-folds the same
// file (the measured 18x refold on a real state root), so delegateJournalReads
// grows linearly with child count; the fix must collapse it to one read for the
// shared root plus one per session with no root override (self).
func TestListSessions_SharedRootDelegatesReadOnce(t *testing.T) {
	const children = 8
	base, _ := sharedRootFixtureTB(t, children)

	reset := instrumentDelegateJournalReads(t)
	res, err := ListSessions(base, SessionsOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Sessions) != children+1 {
		t.Fatalf("sessions = %d, want %d", len(res.Sessions), children+1)
	}
	reads := reset()
	// root + shared root read once = 2 distinct delegate journal reads;
	// the pre-fix code performs one per session (children+1 = 9).
	if reads > 2 {
		t.Fatalf("shared-root delegates journal read %d times during one sweep, want <= 2 (root + shared root, each once)", reads)
	}
}

// instrumentDelegateJournalReads swaps the delegate-journal read hook for a
// counting version and returns a function reporting the count of reads that
// occurred since the swap. Mirrors the openDoctorTranscriptFile hook
// convention: a package-level function var, production code never assigns it.
func instrumentDelegateJournalReads(t *testing.T) (report func() int) {
	t.Helper()
	orig := delegateJournalRead
	var reads int
	delegateJournalRead = func(path string) {
		reads++
		orig(path)
	}
	t.Cleanup(func() { delegateJournalRead = orig })
	return func() int { return reads }
}

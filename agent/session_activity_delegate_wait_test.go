package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func delegateActivityRows(t *testing.T, s *Session) []appwire.SessionDelegate {
	t.Helper()
	page, err := s.ListActivityDelegates(t.Context(), appwire.SessionActivityListParams{
		Ref:   encodeRef("", s.ID()),
		Scope: appwire.SessionActivityScopeSubtree,
	})
	if err != nil {
		t.Fatalf("read delegate activity: %v", err)
	}
	return page.Delegates
}

func requireDelegateActivityRow(t *testing.T, rows []appwire.SessionDelegate, delegateID string) appwire.SessionDelegate {
	t.Helper()
	for _, row := range rows {
		if row.DelegateID == delegateID {
			return row
		}
	}
	t.Fatalf("no activity row for delegate %s among %d rows", delegateID, len(rows))
	return appwire.SessionDelegate{}
}

// A delegate whose run is held open by its finalization drain because its own
// background shell is still running must say so on the row a client reads
// (#6693). Before the join, the row read exactly like a delegate mid-turn: the
// same running lifecycle, with LatestActivityAt frozen for the whole hold.
//
// The wait is not synthesized: the fixture drives a real delegate through a
// real background shell that is genuinely still running, and the assertion
// reads the same collection read the hub serves. Nothing completes the job.
func TestDelegateActivityRowReportsTheOwnedJobItsDrainWaitsOn(t *testing.T) {
	t.Parallel()
	fixture := newOwnedJobDrainFixture(t)

	row := requireDelegateActivityRow(t, delegateActivityRows(t, fixture.parent), fixture.result.DelegateID)
	if row.Terminal {
		t.Fatal("the held delegate's row reads terminal; the case under test is a live row that shows no reason")
	}
	if row.RunningJobCount != 1 {
		t.Fatalf("row runningJobCount = %d, want 1 while the drain holds the run open", row.RunningJobCount)
	}
	started, err := time.Parse(time.RFC3339Nano, row.OldestRunningJobStartedAt)
	if err != nil {
		t.Fatalf("row oldestRunningJobStartedAt = %q, want an RFC3339Nano start: %v", row.OldestRunningJobStartedAt, err)
	}
	if started.IsZero() {
		t.Fatal("row oldestRunningJobStartedAt is the zero time; a running job always states when it started")
	}
}

// The other direction: a delegate that is genuinely mid-turn with no job of
// its own reports no wait, so the field cannot be satisfied by always
// reporting something. The delegate is held inside its provider call, which is
// the state the old row could not distinguish from the drain hold.
func TestDelegateActivityRowWithNoRunningJobReportsNoWait(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	hold := make(chan struct{})
	var enteredOnce sync.Once
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(hold) }) }
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response {
				enteredOnce.Do(func() { close(entered) })
				<-hold
				return agenttest.FinalResponse("thought with no job")
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	root, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 1,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() {
		release()
		root.Close()
	})

	result := root.createDelegate(context.Background(), delegateArgs{Task: "think with no job"})
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}
	select {
	case <-entered:
	case <-time.After(30 * time.Second): // TRIPWIRE: scripted provider steps answer in process; only a hang reaches it.
		t.Fatal("the delegate never reached its provider call")
	}

	row := requireDelegateActivityRow(t, delegateActivityRows(t, root), result.DelegateID)
	if row.Terminal {
		t.Fatal("the mid-turn delegate's row reads terminal; the negative needs a live row")
	}
	if row.RunningJobCount != 0 {
		t.Fatalf("row runningJobCount = %d on a delegate with no job, want the field absent", row.RunningJobCount)
	}
	if row.OldestRunningJobStartedAt != "" {
		t.Fatalf("row oldestRunningJobStartedAt = %q on a delegate with no job, want it absent", row.OldestRunningJobStartedAt)
	}
}

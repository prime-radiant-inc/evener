package agent

import (
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

// newTestSessionForState builds a fresh idle session (no events drained) for
// direct testing of the settle-state helpers, mirroring the construction
// pattern used by session_lifecycle_test.go / session_goal_test.go.
func newTestSessionForState(t *testing.T) *Session {
	t.Helper()
	c := llm.NewClient()
	c.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{}})
	sess, err := NewSession(c, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

func TestSettleTerminalState(t *testing.T) {
	cases := []struct {
		name                                                             string
		hadOutput, goalKicked, notifsPending, queuePending, childrenLive bool
		want                                                             SessionState
	}{
		{"clean turn with output arms awaiting", true, false, false, false, false, SessionAwaiting},
		{"no user-visible output stays idle", false, false, false, false, false, SessionIdle},
		{"goal kick suppresses", true, true, false, false, false, SessionIdle},
		{"pending notifications suppress", true, false, true, false, false, SessionIdle},
		{"queued input suppresses", true, false, false, true, false, SessionIdle},
		{"live children suppress", true, false, false, false, true, SessionIdle},
		{"all suppressors at once", true, true, true, true, true, SessionIdle},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := settleTerminalState(c.hadOutput, c.goalKicked, c.notifsPending, c.queuePending, c.childrenLive)
			if got != c.want {
				t.Fatalf("settleTerminalState(%v,%v,%v,%v,%v) = %q, want %q",
					c.hadOutput, c.goalKicked, c.notifsPending, c.queuePending, c.childrenLive, got, c.want)
			}
		})
	}
}

func TestSettleGoalOnIdle_ReportsKick(t *testing.T) {
	sess := newTestSessionForState(t)
	// No goal set: settle must report kicked=false.
	if kicked := sess.settleGoalOnIdle(); kicked {
		t.Fatal("settleGoalOnIdle with no goal reported kicked=true")
	}
	// Active goal + wired kick: settle must kick and report it.
	kickCh := make(chan string, 1)
	sess.SetKickFunc(func(p string) { kickCh <- p })
	if _, err := sess.SetGoal(nil, "test objective"); err != nil { //nolint:staticcheck // ctx unused by SetGoal
		t.Fatal(err)
	}
	<-kickCh // drain the SetGoal idle-kick itself
	sess.mu.Lock()
	sess.goalInTurn = true // simulate the turn-tail window
	sess.mu.Unlock()
	if kicked := sess.settleGoalOnIdle(); !kicked {
		t.Fatal("settleGoalOnIdle with an active goal did not report kicked=true")
	}
	select {
	case <-kickCh:
	default:
		t.Fatal("settleGoalOnIdle reported kicked but no kick arrived")
	}
}

package agent

import (
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

// TestRun_ArmsOneNotificationOnTerminal proves a child reaching a terminal run
// state arms exactly one metadata notification on the parent. The notification
// carries the child's agent_id, the completed status/reason, a transcript_ref,
// and turns_used — and no child output. Arming happens once per run: a second
// drain returns nothing.
//
// Load-bearing: the arm site in run's finalize is what enqueues the entry. If it
// were removed, the first drainNotifications would return 0 and the len==1
// assertion would fail.
func TestRun_ArmsOneNotificationOnTerminal(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("child done") },
		},
	})
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		MaxSubagentDepth: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	childID := spawnCompletedChild(t, sess, "n1", "do the thing")

	notifs := sess.drainNotifications()
	if len(notifs) != 1 {
		t.Fatalf("drainNotifications after terminal run = %d entries, want 1", len(notifs))
	}
	n := notifs[0]
	if n.AgentID != childID {
		t.Errorf("notification AgentID = %q, want %q", n.AgentID, childID)
	}
	if n.Status != string(SubagentCompleted) {
		t.Errorf("notification Status = %q, want %q", n.Status, SubagentCompleted)
	}
	if n.Reason != string(SubagentCompleted) {
		t.Errorf("notification Reason = %q, want %q", n.Reason, SubagentCompleted)
	}
	if want := encodeRef("", childID); n.TranscriptRef != want {
		t.Errorf("notification TranscriptRef = %q, want %q", n.TranscriptRef, want)
	}
	if n.TurnsUsed < 0 {
		t.Errorf("notification TurnsUsed = %d, want >= 0", n.TurnsUsed)
	}

	// Armed at most once per run: the queue is now empty.
	if again := sess.drainNotifications(); len(again) != 0 {
		t.Fatalf("second drainNotifications = %d entries, want 0 (armed once)", len(again))
	}
}

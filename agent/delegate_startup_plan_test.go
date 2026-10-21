package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	taskpkg "primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/llm"
)

// A delegate works its seeded list in order, and a later step's instructions
// often decide what an earlier step must build. The child's first model request
// therefore carries every step's title and prompt, not only the first.
func TestDelegateStartupRequestCarriesTheWholePlan(t *testing.T) {
	t.Parallel()
	root, adapter := newDelegateStartupFixture(t)
	plan := []taskpkg.TaskTemplate{
		{Title: "First step", Prompt: "PROMPT_FIRST: implement the parser."},
		{Title: "Second step", Prompt: "PROMPT_SECOND: render the result."},
		{Title: "Third step", Prompt: "PROMPT_THIRD: document the format."},
	}
	spawnDelegateWithPlan(t, root, "do the plan", plan)

	first := messagesText(adapter.waitFirstRequest(t).Messages)
	for _, want := range []string{
		"First step", "Second step", "Third step",
		"PROMPT_FIRST", "PROMPT_SECOND", "PROMPT_THIRD",
	} {
		if !strings.Contains(first, want) {
			t.Errorf("first child request is missing %q:\n%s", want, first)
		}
	}
}

// A delegate with no tasks is unaffected: no plan reminder is injected, and its
// first request carries only its brief.
func TestDelegateStartupWithoutTasksHasNoPlanReminder(t *testing.T) {
	t.Parallel()
	empty := taskpkg.NewTaskStore(t.TempDir(), "empty")
	if got := taskReminderFullWithPrompts(empty.View(), 0); got != "" {
		t.Fatalf("empty store rendered a plan: %s", got)
	}

	root, adapter := newDelegateStartupFixture(t)
	spawnDelegateWithPlan(t, root, "BRIEF_ONLY", nil)

	first := messagesText(adapter.waitFirstRequest(t).Messages)
	if !strings.Contains(first, "BRIEF_ONLY") {
		t.Fatalf("first child request is missing its brief:\n%s", first)
	}
	if strings.Contains(first, "Task list:") {
		t.Fatalf("a delegate with no tasks received a plan reminder:\n%s", first)
	}
}

// newDelegateStartupFixture builds a delegate-capable session whose scripted
// adapter answers one final report and keeps every request it saw, so a test
// can read the child's first turn. The session options mirror the shared
// bootstrap fixture; only the adapter differs, because these tests assert on
// what it recorded.
func newDelegateStartupFixture(t *testing.T) (*Session, *fakeAdapter) {
	t.Helper()
	workspace := t.TempDir()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return finalResponse("child done") },
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	sess, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
		StateDir:         t.TempDir(),
		MaxSubagentDepth: 2,
		ForceRealIO:      true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			sandboxProber:       bwrapCapableProber(workspace),
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	return sess, adapter
}

// waitFirstRequest waits for the first request the adapter answered.
func (a *fakeAdapter) waitFirstRequest(t *testing.T) llm.Request {
	t.Helper()
	var first llm.Request
	// TRIPWIRE: the child's adapter is scripted in process and answers in
	// milliseconds; 30s only fires on a genuine hang.
	waitForCondition(t, 30*time.Second, "the child's first model request", func() bool {
		requests := a.Requests()
		if len(requests) == 0 {
			return false
		}
		first = requests[0]
		return true
	})
	return first
}

// spawnDelegateWithPlan starts one leaf delegate through the production path.
func spawnDelegateWithPlan(t *testing.T, root *Session, brief string, plan []taskpkg.TaskTemplate) string {
	t.Helper()
	created := root.createDelegate(context.Background(), delegateArgs{
		Task:                brief,
		TaskList:            plan,
		DelegationAllowance: new(0),
	})
	if created.Err != nil {
		t.Fatalf("createDelegate: %v", created.Err)
	}
	return created.ChildSessionID
}

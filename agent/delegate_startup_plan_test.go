package agent

import (
	"context"
	"strings"
	"sync"
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
	root, requests := newDelegateStartupSession(t)
	plan := []taskpkg.TaskTemplate{
		{Title: "First step", Prompt: "PROMPT_FIRST: implement the parser."},
		{Title: "Second step", Prompt: "PROMPT_SECOND: render the result."},
		{Title: "Third step", Prompt: "PROMPT_THIRD: document the format."},
	}
	spawnDelegateWithPlan(t, root, "do the plan", plan)

	first := messagesText(requests.waitFirst(t).Messages)
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
	if got := taskReminderFullWithPrompts(empty); got != "" {
		t.Fatalf("empty store rendered a plan: %s", got)
	}

	root, requests := newDelegateStartupSession(t)
	spawnDelegateWithPlan(t, root, "BRIEF_ONLY", nil)

	first := messagesText(requests.waitFirst(t).Messages)
	if !strings.Contains(first, "BRIEF_ONLY") {
		t.Fatalf("first child request is missing its brief:\n%s", first)
	}
	if strings.Contains(first, "Task list:") {
		t.Fatalf("a delegate with no tasks received a plan reminder:\n%s", first)
	}
}

// childRequests records the requests a bootstrap session's scripted adapter
// answers, so a test can read the child's first turn.
type childRequests struct {
	mu       sync.Mutex
	requests []llm.Request
}

func (c *childRequests) record(request llm.Request) {
	c.mu.Lock()
	c.requests = append(c.requests, request)
	c.mu.Unlock()
}

func (c *childRequests) waitFirst(t *testing.T) llm.Request {
	t.Helper()
	var first llm.Request
	waitForCondition(t, 30*time.Second, "the child's first model request", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		if len(c.requests) == 0 {
			return false
		}
		first = c.requests[0]
		return true
	})
	return first
}

// newDelegateStartupSession builds the delegate-capable bootstrap session with
// an adapter that records every request and answers with a final report.
func newDelegateStartupSession(t *testing.T) (*Session, *childRequests) {
	t.Helper()
	stateDir := t.TempDir()
	workspace := t.TempDir()
	requests := &childRequests{}
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(request llm.Request) llm.Response {
				requests.record(request)
				return finalResponse("child done")
			},
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	profile := withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2"))
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
		StateDir:         stateDir,
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
	return sess, requests
}

// spawnDelegateWithPlan starts one leaf delegate through the production path
// and waits for its run to settle.
func spawnDelegateWithPlan(t *testing.T, root *Session, brief string, plan []taskpkg.TaskTemplate) {
	t.Helper()
	created := root.createDelegate(context.Background(), delegateArgs{
		Task:                brief,
		TaskList:            plan,
		DelegationAllowance: new(0),
	})
	if created.Err != nil {
		t.Fatalf("createDelegate: %v", created.Err)
	}
	waitForCondition(t, 30*time.Second, "the delegate to finish", func() bool {
		sub := root.getSub(created.ChildSessionID)
		if sub == nil {
			return false
		}
		sub.mu.Lock()
		defer sub.mu.Unlock()
		return sub.status != SubagentRunning
	})
}

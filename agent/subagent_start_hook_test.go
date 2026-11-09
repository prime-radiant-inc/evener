package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/llm"
)

// subagentStartSentinel is the only text the fixture hook injects, so a
// request that carries it carries the hook's additionalContext and nothing the
// model or the test itself wrote.
const subagentStartSentinel = "SUBAGENT-START-SENTINEL"

// subagentStartFixture is a root session whose plugin declares a SubagentStart
// command hook matching the default child ("subagent"), the hook appending each
// run and its stdin to files so the test can count runs and read the identity
// the hook saw.
type subagentStartFixture struct {
	root    *Session
	adapter *agenttest.ScriptedAdapter
	marker  string
	stdin   string
}

func newSubagentStartFixture(t *testing.T) subagentStartFixture {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "subagent-start-runs")
	stdinPath := filepath.Join(dir, "subagent-start-input.jsonl")
	output := `{"hookSpecificOutput":{"hookEventName":"SubagentStart","additionalContext":"` + subagentStartSentinel + `"}}`
	command := "cat >> " + shellquote.Literal(stdinPath) +
		"; printf 'run\\n' >> " + shellquote.Literal(marker) +
		"; printf '%s' " + shellquote.Literal(output)
	hooksJSON, err := json.Marshal(map[string]any{"hooks": map[string]any{"SubagentStart": []any{
		map[string]any{"matcher": "subagent", "hooks": []any{map[string]any{"type": "command", "command": command}}},
	}}})
	if err != nil {
		t.Fatalf("encode hooks.json: %v", err)
	}
	pluginDir := writePluginHooks(t, "toolsmith", string(hooksJSON))

	client := llm.NewClient()
	adapter := &agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
		return finalResponse("child done")
	}}
	client.Register(adapter)
	root := newSession(t, withClient(client), withConfig(SessionConfig{
		StateDir:         t.TempDir(),
		PluginDirs:       []string{pluginDir},
		MaxSubagentDepth: 2,
		ForceRealIO:      true,
		AgentsDocPath:    filepath.Join(t.TempDir(), "no-personal-AGENTS.md"),
		testOnly: testConfig{
			skipGitSnapshot:            true,
			minimalSystemPrompt:        true,
			sandboxProber:              bwrapCapableProber(t.TempDir()),
			disableDelegateIdleRelease: true,
		},
	}))
	return subagentStartFixture{root: root, adapter: adapter, marker: marker, stdin: stdinPath}
}

// createDelegateThroughTheTool drives the `delegate` tool the model calls, not
// the legacy spawnAgent path, and returns the tool's decoded creation result.
func (f subagentStartFixture) createDelegateThroughTheTool(t *testing.T) stableDelegateCreateResult {
	t.Helper()
	res := f.root.execTool(context.Background(), llm.ToolCallData{
		ID:        "subagent-start-create",
		Name:      "delegate",
		Arguments: json.RawMessage(`{"prompt":"harness task","delegation_allowance":0}`),
	}, "")
	if res.IsError {
		t.Fatalf("delegate tool failed: %s", res.Output)
	}
	var created stableDelegateCreateResult
	if err := json.Unmarshal([]byte(res.Output), &created); err != nil {
		t.Fatalf("decode delegate tool result: %v (%s)", err, res.Output)
	}
	if created.ChildSessionID == "" || created.DelegateID == "" {
		t.Fatalf("delegate tool returned no ids: %s", res.Output)
	}
	return created
}

// waitForChildRunComplete waits for the child's current run to finish. The
// session reaches SessionIdle inside the run's settle, before the run's finalize
// tail clears sub.running, and a launch that lands in that window is steered
// into the run instead of starting a new one — so the test waits on the run
// itself.
func (f subagentStartFixture) waitForChildRunComplete(t *testing.T, childID string) *subagent {
	t.Helper()
	sub := f.root.getSub(childID)
	if sub == nil {
		t.Fatalf("delegate child %q is not retained", childID)
	}
	sub.mu.Lock()
	done := sub.done
	sub.mu.Unlock()
	select {
	case <-done:
	// TRIPWIRE: every model answer is scripted in process and the hook is a
	// local shell command, so only a genuine hang reaches the timeout.
	case <-time.After(30 * time.Second):
		t.Fatalf("timed out waiting for delegate child %q to finish its run", childID)
	}
	return sub
}

// firstRequestFor returns the first provider request the child made.
func firstRequestFor(t *testing.T, adapter *agenttest.ScriptedAdapter, childID string) llm.Request {
	t.Helper()
	var request llm.Request
	waitForCondition(t, 30*time.Second, "the child's first provider request", func() bool {
		for _, candidate := range adapter.Requests() {
			if candidate.SessionID == childID {
				request = candidate
				return true
			}
		}
		return false
	})
	return request
}

// subagentStartRunCount counts the runs the fixture hook recorded.
func subagentStartRunCount(t *testing.T, marker string) int {
	t.Helper()
	raw, err := os.ReadFile(marker)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read hook marker: %v", err)
	}
	return len(strings.Fields(string(raw)))
}

// subagentStartHookContextTurns counts the child's hook-context steering turns
// that carry the sentinel, and reports whether each one is the wrapped text
// deliverHookContext queues.
func subagentStartHookContextTurns(child *Session) (int, bool) {
	child.mu.Lock()
	defer child.mu.Unlock()
	count := 0
	wrapped := true
	for _, turn := range child.history {
		if turn.Kind != schema.TurnSteering || turn.SteeringKind != events.SteeringKindHookContext {
			continue
		}
		if !strings.Contains(turn.Message.Text(), subagentStartSentinel) {
			continue
		}
		count++
		if turn.Message.Text() != wrapHookContext(subagentStartSentinel) {
			wrapped = false
		}
	}
	return count, wrapped
}

// TestSubagentStart_FiresOnceForADelegateToolSpawn proves the hook fires for a
// delegate the model created through the delegate tool, not only the legacy
// spawnAgent path, and that its context reaches the child's first request.
func TestSubagentStart_FiresOnceForADelegateToolSpawn(t *testing.T) {
	fixture := newSubagentStartFixture(t)
	created := fixture.createDelegateThroughTheTool(t)

	request := firstRequestFor(t, fixture.adapter, created.ChildSessionID)
	if got := requestText(request); !strings.Contains(got, subagentStartSentinel) {
		t.Fatalf("the child's first request omitted the SubagentStart context:\n%s", got)
	}

	if got := subagentStartRunCount(t, fixture.marker); got != 1 {
		t.Fatalf("SubagentStart hook runs = %d, want exactly one for one delegate", got)
	}

	child := fixture.root.getSub(created.ChildSessionID)
	if child == nil || child.sess == nil {
		t.Fatalf("delegate child %q is not retained", created.ChildSessionID)
	}
	if count, wrapped := subagentStartHookContextTurns(child.sess); count != 1 || !wrapped {
		t.Fatalf("child hook-context turns = %d (wrapped=%v), want exactly one wrapped copy", count, wrapped)
	}

	// The hook runs on the parent's runner but must see the CHILD's identity:
	// the agent type a matcher targets, and the child session id.
	raw, err := os.ReadFile(fixture.stdin)
	if err != nil {
		t.Fatalf("read hook input: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	var in struct {
		AgentID   string `json:"agent_id"`
		AgentType string `json:"agent_type"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &in); err != nil {
		t.Fatalf("decode hook input: %v (%s)", err, lines[len(lines)-1])
	}
	if in.AgentID != created.ChildSessionID {
		t.Errorf("hook agent_id = %q, want the child session id %q", in.AgentID, created.ChildSessionID)
	}
	if in.AgentType != "subagent" {
		t.Errorf("hook agent_type = %q, want subagent", in.AgentType)
	}
}

// TestSubagentStart_SecondLaunchDoesNotDuplicateContext proves the delivery
// dedupe: the hook fires again for a second run on the same child, but a child
// that already holds its context receives no copy.
func TestSubagentStart_SecondLaunchDoesNotDuplicateContext(t *testing.T) {
	fixture := newSubagentStartFixture(t)
	created := fixture.createDelegateThroughTheTool(t)
	sub := fixture.waitForChildRunComplete(t, created.ChildSessionID)
	child := sub.sess

	started, err := fixture.root.startOrSteerSubagentRun(sub, "second assignment")
	if err != nil {
		t.Fatalf("second launch: %v", err)
	}
	if !started {
		t.Fatal("the second launch was steered into the first run instead of starting a new one")
	}
	waitForCondition(t, 30*time.Second, "the second SubagentStart hook run", func() bool {
		return subagentStartRunCount(t, fixture.marker) == 2
	})
	fixture.waitForChildRunComplete(t, created.ChildSessionID)

	if got := subagentStartRunCount(t, fixture.marker); got != 2 {
		t.Fatalf("SubagentStart hook runs = %d, want one per launch (2)", got)
	}
	if count, wrapped := subagentStartHookContextTurns(child); count != 1 || !wrapped {
		t.Fatalf("child hook-context turns after the second launch = %d (wrapped=%v), want the single copy it already held", count, wrapped)
	}
}

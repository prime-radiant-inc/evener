package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

// newSubagentStartFixture builds the root with one command hook per spec. A
// spec with an empty marker is the plain context hook; any spec may carry its
// own script.
func newSubagentStartFixture(t *testing.T, specs ...subagentStartHookSpec) subagentStartFixture {
	t.Helper()
	dir := t.TempDir()
	marker := filepath.Join(dir, "subagent-start-runs")
	stdinPath := filepath.Join(dir, "subagent-start-input.jsonl")
	if len(specs) == 0 {
		specs = []subagentStartHookSpec{{}}
	}
	hooks := make([]any, 0, len(specs))
	for _, spec := range specs {
		hooks = append(hooks, map[string]any{"type": "command", "command": spec.script(marker, stdinPath)})
	}
	hooksJSON, err := json.Marshal(map[string]any{"hooks": map[string]any{"SubagentStart": []any{
		map[string]any{"matcher": "subagent", "hooks": hooks},
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

// subagentStartHookSpec is one SubagentStart command hook. When command is set
// it is used verbatim; otherwise the hook is the plain sentinel-context hook and
// output is printed after the run marker.
type subagentStartHookSpec struct {
	command string
}

func (spec subagentStartHookSpec) script(marker, stdinPath string) string {
	if spec.command != "" {
		return spec.command
	}
	output := `{"hookSpecificOutput":{"hookEventName":"SubagentStart","additionalContext":"` + subagentStartSentinel + `"}}`
	return "cat >> " + shellquote.Literal(stdinPath) +
		"; printf 'run\\n' >> " + shellquote.Literal(marker) +
		"; printf '%s' " + shellquote.Literal(output)
}

// subagentStartWarnings collects the root's warnings while the session runs,
// so a test can assert on the operator-facing lines without closing the root.
type subagentStartWarnings struct {
	mu   sync.Mutex
	seen []events.WarningData
}

func collectSubagentStartWarnings(sess *Session) *subagentStartWarnings {
	collected := &subagentStartWarnings{}
	go func() {
		for ev := range sess.Events() {
			if warning, ok := ev.Data.(events.WarningData); ok {
				collected.mu.Lock()
				collected.seen = append(collected.seen, warning)
				collected.mu.Unlock()
			}
		}
	}()
	return collected
}

func (c *subagentStartWarnings) snapshot() []events.WarningData {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]events.WarningData(nil), c.seen...)
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
	// TRIPWIRE: in-process scripted models and local shell hooks, so only a
	// genuine hang reaches 30s.
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
	// TRIPWIRE: in-process scripted models and local shell hooks, so only a
	// genuine hang reaches 30s.
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

// subagentStartChildRequests counts the provider requests the child made. A
// stopped launch must never produce one.
func subagentStartChildRequests(adapter *agenttest.ScriptedAdapter, childID string) int {
	count := 0
	for _, request := range adapter.Requests() {
		if request.SessionID == childID {
			count++
		}
	}
	return count
}

// TestSubagentStart_ContinueFalseStopsTheChildRun proves Claude's
// "continue:false stops processing entirely": the child never launches, so it
// makes no model request; the run still settles once (done closes), the
// senders barrier is released, the delegate tool call returns normally, and the
// reason reaches the operator.
func TestSubagentStart_ContinueFalseStopsTheChildRun(t *testing.T) {
	const stopReason = "SUBAGENT-START-STOPPED-BY-HOOK"
	fixture := newSubagentStartFixture(t, subagentStartHookSpec{
		command: `printf '%s' '{"continue":false,"stopReason":"` + stopReason + `"}'`,
	})
	warnings := collectSubagentStartWarnings(fixture.root)

	created := fixture.createDelegateThroughTheTool(t)
	sub := fixture.waitForChildRunComplete(t, created.ChildSessionID)

	if got := subagentStartChildRequests(fixture.adapter, created.ChildSessionID); got != 0 {
		t.Fatalf("a continue:false launch made %d model requests, want zero", got)
	}
	sub.mu.Lock()
	status, result := sub.status, sub.result
	sub.mu.Unlock()
	if status != SubagentCompleted || result != stopReason {
		t.Fatalf("stopped child settled as %q/%q, want %q/%q", status, result, SubagentCompleted, stopReason)
	}
	// TRIPWIRE: in-process scripted models and local shell hooks, so only a
	// genuine hang reaches 30s.
	waitForCondition(t, 30*time.Second, "the stopped reason to reach the operator", func() bool {
		for _, warning := range warnings.snapshot() {
			if strings.Contains(warning.Message, stopReason) {
				return true
			}
		}
		return false
	})
	// done is closed and the senders barrier is released; waiting on a closed
	// channel twice must not panic, and Close (registered by newSession) joins
	// the barrier. A second run on the settled child proves it is still usable.
	sub.mu.Lock()
	done := sub.done
	sub.mu.Unlock()
	select {
	case <-done:
	default:
		t.Fatal("sub.done was not closed for a hook-stopped launch")
	}
	started, err := fixture.root.startOrSteerSubagentRun(sub, "assignment after a hook stop")
	if err != nil || !started {
		t.Fatalf("launch after a hook stop = started %t, err %v; want a new run", started, err)
	}
	fixture.waitForChildRunComplete(t, created.ChildSessionID)
}

// TestSubagentStart_FailedHookLeavesTheChildRunning proves a SubagentStart hook
// that exits 2 does not fail the spawn: the child's first request happens and
// the operator got its one summarized line.
func TestSubagentStart_FailedHookLeavesTheChildRunning(t *testing.T) {
	fixture := newSubagentStartFixture(t, subagentStartHookSpec{
		command: `echo "spawn warning" >&2; exit 2`,
	})
	warnings := collectSubagentStartWarnings(fixture.root)
	created := fixture.createDelegateThroughTheTool(t)

	firstRequestFor(t, fixture.adapter, created.ChildSessionID)
	// TRIPWIRE: in-process scripted models and local shell hooks, so only a
	// genuine hang reaches 30s.
	waitForCondition(t, 30*time.Second, "the failed-hook warning", func() bool {
		for _, warning := range warnings.snapshot() {
			// The line names the event, the hook's configured identity, the exit
			// code, and the first line of its stderr — one operator line, never
			// the child's model context.
			message := warning.Message
			if strings.HasPrefix(message, "SubagentStart hook ") &&
				strings.Contains(message, "failed (exit 2)") &&
				strings.Contains(message, "spawn warning") &&
				!strings.Contains(message, "\n") {
				return true
			}
		}
		return false
	})
	if got := subagentStartChildRequests(fixture.adapter, created.ChildSessionID); got == 0 {
		t.Fatal("an exit-2 SubagentStart hook stopped the child from running")
	}
}

// TestSubagentStart_DecisionBlockLeavesTheChildRunning pins Claude's contract
// that SubagentStart has no decision control: a decision:"block" has no meaning
// here and must not stop the child.
func TestSubagentStart_DecisionBlockLeavesTheChildRunning(t *testing.T) {
	fixture := newSubagentStartFixture(t, subagentStartHookSpec{
		command: `printf '%s' '{"decision":"block","reason":"no spawn"}'`,
	})
	created := fixture.createDelegateThroughTheTool(t)
	firstRequestFor(t, fixture.adapter, created.ChildSessionID)

	sub := fixture.waitForChildRunComplete(t, created.ChildSessionID)
	sub.mu.Lock()
	status := sub.status
	sub.mu.Unlock()
	if status != SubagentCompleted {
		t.Fatalf("child status = %q, want the block decision ignored", status)
	}
	if got := subagentStartChildRequests(fixture.adapter, created.ChildSessionID); got == 0 {
		t.Fatal("a decision:block SubagentStart hook stopped the child from running")
	}
}

// TestSubagentStart_TwoHooksInOneLaunchDeliverOneCopy proves the same-launch
// dedupe: two hooks returning identical additionalContext produce one steering
// turn, because neither is in history when the other delivers.
func TestSubagentStart_TwoHooksInOneLaunchDeliverOneCopy(t *testing.T) {
	fixture := newSubagentStartFixture(t, subagentStartHookSpec{}, subagentStartHookSpec{})
	created := fixture.createDelegateThroughTheTool(t)
	request := firstRequestFor(t, fixture.adapter, created.ChildSessionID)
	if !strings.Contains(requestText(request), subagentStartSentinel) {
		t.Fatal("the child's first request omitted the hook context")
	}
	fixture.waitForChildRunComplete(t, created.ChildSessionID)

	child := fixture.root.getSub(created.ChildSessionID)
	if child == nil || child.sess == nil {
		t.Fatalf("delegate child %q is not retained", created.ChildSessionID)
	}
	if count, wrapped := subagentStartHookContextTurns(child.sess); count != 1 || !wrapped {
		t.Fatalf("child hook-context turns = %d (wrapped=%v), want one copy for two identical hooks", count, wrapped)
	}
}

// TestSubagentStart_ReinjectsAfterTheCopyIsCompactedAway proves the dedupe's
// other half: a real compaction folds the child's history and drops the copy, so
// the next launch delivers it again.
func TestSubagentStart_ReinjectsAfterTheCopyIsCompactedAway(t *testing.T) {
	fixture := newSubagentStartFixture(t)
	created := fixture.createDelegateThroughTheTool(t)
	sub := fixture.waitForChildRunComplete(t, created.ChildSessionID)
	child := sub.sess

	if count, _ := subagentStartHookContextTurns(child); count != 1 {
		t.Fatalf("the child held %d hook-context copies before compaction, want one", count)
	}
	// A compaction preserves its most recent turns verbatim, so the steer must be
	// older than that tail for a real fold to drop it.
	for i := range 10 {
		child.appendTurn(schema.TurnUserInput, llm.User("filler assignment "+strconv.Itoa(i)))
	}
	if err := child.Compact(context.Background()); err != nil {
		t.Fatalf("compact the child: %v", err)
	}
	if count, _ := subagentStartHookContextTurns(child); count != 0 {
		t.Fatalf("the child still holds %d hook-context copies after compaction, want none", count)
	}

	started, err := fixture.root.startOrSteerSubagentRun(sub, "second assignment")
	if err != nil || !started {
		t.Fatalf("second launch = started %t, err %v; want a new run", started, err)
	}
	// TRIPWIRE: in-process scripted models and local shell hooks, so only a
	// genuine hang reaches 30s.
	waitForCondition(t, 30*time.Second, "the second SubagentStart hook run", func() bool {
		return subagentStartRunCount(t, fixture.marker) == 2
	})
	fixture.waitForChildRunComplete(t, created.ChildSessionID)

	if count, wrapped := subagentStartHookContextTurns(child); count != 1 || !wrapped {
		t.Fatalf("child hook-context turns after re-injection = %d (wrapped=%v), want one re-delivered copy", count, wrapped)
	}
	// The re-delivered copy reached the model, not just history: the second run's
	// request carries the sentinel.
	requests := fixture.adapter.Requests()
	if got := requests[len(requests)-1]; !strings.Contains(requestText(got), subagentStartSentinel) {
		t.Fatalf("the post-compaction request omitted the re-injected context:\n%s", requestText(got))
	}
}

// TestSubagentStart_StoppedLaunchStillRearmsPendingAttention proves the stop
// path shares subagent.run's finalize tail: attention the child's transcript
// still owes is re-armed on the owning delegate's controller, so a hook-stopped
// launch cannot strand a wake the parent would never drive.
func TestSubagentStart_StoppedLaunchStillRearmsPendingAttention(t *testing.T) {
	const (
		stopReason = "SUBAGENT-START-STOPPED-WITH-ATTENTION"
		attention  = "watch:subagent-start-stop:1"
	)
	fixture := newSubagentStartFixture(t, subagentStartHookSpec{
		command: `printf '%s' '{"continue":false,"stopReason":"` + stopReason + `"}'`,
	})
	created := fixture.createDelegateThroughTheTool(t)
	sub := fixture.waitForChildRunComplete(t, created.ChildSessionID)
	child := sub.sess

	// The attention is durable in the child's transcript and NOT yet noted as a
	// wake on the controller: only the finalize tail's re-arm can arm it.
	if appended, err := child.appendDelegateNotificationDurably(attention, "owed attention content"); err != nil || !appended {
		t.Fatalf("append pending attention = appended:%t err:%v", appended, err)
	}
	owner := child.owningDelegateID
	if owner == "" {
		t.Fatal("the delegate child carries no owning delegate id")
	}
	fixture.root.delegateController.mu.Lock()
	state := fixture.root.delegateController.attention[owner]
	armedBefore := false
	if state != nil {
		_, armedBefore = state.wakeIDs[attention]
	}
	fixture.root.delegateController.mu.Unlock()
	if armedBefore {
		t.Fatalf("attention %q was already noted as a wake before the stopped launch; the test cannot attribute the re-arm", attention)
	}

	// delegate_send is the stable launch path: its run carries a lease, which is
	// what selects the controller-owning half of the finalize tail.
	outcome := (delegateRuntime{owner: fixture.root}).send(context.Background(), created.DelegateID, "assignment that the hook stops", 60_000)
	if outcome.result.Err != nil {
		t.Fatalf("stopped delegate_send: %v", outcome.result.Err)
	}
	if outcome.commit != nil {
		abortUnpersistedStableDelegateOutcome(t, outcome)
	}

	// TRIPWIRE: in-process scripted models and local shell hooks, so only a
	// genuine hang reaches 30s.
	waitForCondition(t, 30*time.Second, "the pending attention to be re-armed", func() bool {
		fixture.root.delegateController.mu.Lock()
		defer fixture.root.delegateController.mu.Unlock()
		state := fixture.root.delegateController.attention[owner]
		if state == nil {
			return false
		}
		_, armed := state.wakeIDs[attention]
		return armed
	})
	// The hook stops every launch, so the child never reached the model at all;
	// the re-arm above is the finalize tail's work on a run that did.
	if got := subagentStartChildRequests(fixture.adapter, created.ChildSessionID); got != 0 {
		t.Fatalf("a hook-stopped child made %d model requests, want zero", got)
	}
}

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/execsupport/shellquote"
	"primeradiant.com/evener/llm"
)

// postToolBatchSentinel is the only text the fixture hook injects, so a request
// carrying it carries the hook's additionalContext and nothing the prompt or a
// steering turn wrote.
const postToolBatchSentinel = "POST-TOOL-BATCH-SENTINEL"

// postToolBatchFixture is a session whose plugin declares one PostToolBatch
// command hook. The hook appends a line to a marker file on every run and, when
// context is requested, prints additionalContext; a block script prints its
// reason to stderr and exits 2.
type postToolBatchFixture struct {
	sess   *Session
	choice *choiceAdapter
}

// choiceAdapter answers from a per-request responder, so a test can branch on
// how many model requests have happened so far.
type choiceAdapter struct {
	name     string
	respond  func(round int, req llm.Request) llm.Response
	mu       sync.Mutex
	requests []llm.Request
}

func newChoiceAdapter(name string, respond func(round int, req llm.Request) llm.Response) *choiceAdapter {
	return &choiceAdapter{name: name, respond: respond}
}

func (a *choiceAdapter) Name() string { return a.name }

func (a *choiceAdapter) Complete(_ context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	round := len(a.requests)
	a.requests = append(a.requests, req)
	a.mu.Unlock()

	resp := a.respond(round, req)
	resp.Provider = a.name
	if resp.Model == "" {
		resp.Model = req.Model
	}
	return resp, nil
}

func (a *choiceAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *choiceAdapter) Requests() []llm.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]llm.Request(nil), a.requests...)
}

// newPostToolBatchFixture builds the session with one PostToolBatch hook whose
// command is supplied by the caller and whose runs are recorded in marker.
func newPostToolBatchFixture(t *testing.T, mats []any, respond func(round int, req llm.Request) llm.Response) postToolBatchFixture {
	t.Helper()
	hooksJSON, err := json.Marshal(map[string]any{"hooks": map[string]any{"PostToolBatch": mats}})
	if err != nil {
		t.Fatalf("encode hooks.json: %v", err)
	}
	pluginDir := writePluginHooks(t, "post-tool-batch-plugin", string(hooksJSON))

	adapter := newChoiceAdapter("openai", respond)
	client := llm.NewClient()
	client.Register(adapter)
	sess := newSession(t, withClient(client), withConfig(SessionConfig{
		StateDir:   t.TempDir(),
		PluginDirs: []string{pluginDir},
		testOnly:   testConfig{skipGitSnapshot: true},
	}))
	if sess.hookRunner == nil {
		t.Fatal("test setup: the plugin's PostToolBatch hook did not reach a hook runner")
	}
	return postToolBatchFixture{sess: sess, choice: adapter}
}

// runCount counts the lines the fixture hook appended.
func postToolBatchRunCount(t *testing.T, marker string) int {
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

// batchShellCall is one successful shell call, the round's tool work.
func batchShellCall(id string) llm.ToolCallData {
	raw, _ := json.Marshal(map[string]any{"command": "echo ok", "description": "batch test"})
	return llm.ToolCallData{ID: id, Name: "exec_command", Arguments: raw, Type: "function"}
}

// TestPostToolBatch_ContextReachesTheSameNextRequest proves the hook's
// additionalContext is in the model request that follows the batch, not the one
// after it.
//
// Round 1 runs one tool. The hook fires in the round tail; its context must be
// drained by injectPostToolSteering, the last drain before round 2's request.
// Fired after that drain instead, the context would still be queued when round
// 2's request is built and would only reach round 3.
func TestPostToolBatch_ContextReachesTheSameNextRequest(t *testing.T) {
	t.Parallel()
	marker := filepath.Join(t.TempDir(), "runs")
	contextCmd := fmt.Sprintf("printf 'run\\n' >> %s; printf '%%s' %s",
		shellquote.Literal(marker),
		shellquote.Literal(`{"hookSpecificOutput":{"hookEventName":"PostToolBatch","additionalContext":"`+postToolBatchSentinel+`"}}`))
	mats := []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": contextCmd}}}}

	// Round 0 asks for a tool, round 1 asks for a tool again, round 2 answers
	// with communicate (itself a tool call). Two tool rounds before the answer
	// guarantee a third request exists, so "the request after the first batch"
	// and "the one after that" are different requests.
	f := newPostToolBatchFixture(t, mats, func(round int, req llm.Request) llm.Response {
		switch round {
		case 0, 1:
			return toolCallResponse(batchShellCall(fmt.Sprintf("shell-%d", round)))
		default:
			return finalResponse("done")
		}
	})

	// TRIPWIRE: scripted in-process adapter and one local shell hook; only a
	// genuine hang reaches the timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := f.sess.ProcessInput(ctx, "run a tool", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// Three rounds executed tools (two shell rounds and the communicate round),
	// so the event fires three times.
	if got := postToolBatchRunCount(t, marker); got != 3 {
		t.Fatalf("hook runs = %d, want 3 (once per round that executed tools)", got)
	}
	requests := f.choice.Requests()
	if len(requests) < 3 {
		t.Fatalf("model requests = %d, want at least 3 (two tool rounds plus an answer)", len(requests))
	}
	if requestContainsText(requests[0], postToolBatchSentinel) {
		t.Fatalf("the first request already carries the hook context: %v", requests[0].Messages)
	}
	if !requestContainsText(requests[1], postToolBatchSentinel) {
		t.Fatalf("the request after the first batch does not carry the hook context.\nrequests[1] = %#v", requests[1].Messages)
	}
}

// TestPostToolBatch_BlockDoesNotSwallowCommunicateReply proves a block on a
// round that also called communicate returns the reply, not a failure boundary.
// The same round's reply owns the turn; the hook's block is recorded and then
// discarded once deliverIfCommunicated reports done.
func TestPostToolBatch_BlockDoesNotSwallowCommunicateReply(t *testing.T) {
	t.Parallel()
	blockCmd := `printf 'batch blocks the loop\n' >&2; exit 2`
	mats := []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": blockCmd}}}}

	reply := "Here is your answer."
	f := newPostToolBatchFixture(t, mats, func(round int, req llm.Request) llm.Response {
		if round > 0 {
			t.Fatalf("a communicate round must end the turn; reached request %d", round)
		}
		return toolCallResponse(batchShellCall("shell-0"), communicateCall("c1", reply))
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := f.sess.ProcessInput(ctx, "do it", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if strings.TrimSpace(out) != reply {
		t.Fatalf("ProcessInput returned %q, want the communicate reply %q (the block must not pre-empt it)", out, reply)
	}
	if got := f.sess.State(); got != SessionIdle {
		t.Fatalf("state = %q, want %q: a blocked batch on a communicate round settles idle", got, SessionIdle)
	}
}

// TestPostToolBatch_BlockDoesNotStrandPendingAsk proves a round posting ask_user
// stays awaiting when the batch hook blocks: the ask boundary wins, so the
// question is not abandoned behind a failure boundary.
func TestPostToolBatch_BlockDoesNotStrandPendingAsk(t *testing.T) {
	t.Parallel()
	blockCmd := `printf 'batch blocks the loop\n' >&2; exit 2`
	mats := []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": blockCmd}}}}

	f := newPostToolBatchFixture(t, mats, func(round int, req llm.Request) llm.Response {
		if round > 0 {
			t.Fatalf("an ask round must end the turn; reached request %d", round)
		}
		return toolCallResponse(askUserCall("ask1", askUserArgsValid()))
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := f.sess.ProcessInput(ctx, "which db?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := f.sess.State(); got != SessionAwaiting {
		t.Fatalf("state = %q, want %q: a batch block must not strand the posted question", got, SessionAwaiting)
	}
	if got := f.sess.askPendingCount(); got != 1 {
		t.Fatalf("askPendingCount = %d, want 1", got)
	}
}

// TestPostToolBatch_BlockEndsTheLoopWhenNothingElseDoes proves the block is
// applied on an ordinary round: the reason reaches the user and the loop stops
// before another request, with the tool results already persisted.
func TestPostToolBatch_BlockEndsTheLoopWhenNothingElseDoes(t *testing.T) {
	t.Parallel()
	blockCmd := `printf 'batch blocks the loop\n' >&2; exit 2`
	mats := []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": blockCmd}}}}

	f := newPostToolBatchFixture(t, mats, func(round int, req llm.Request) llm.Response {
		if round > 0 {
			t.Fatalf("the batch block must end the loop; reached request %d", round)
		}
		return toolCallResponse(batchShellCall("shell-0"))
	})

	var warnings []events.WarningData
	warningsDone := make(chan struct{})
	go func() {
		defer close(warningsDone)
		for ev := range f.sess.Events() {
			if w, ok := ev.Data.(events.WarningData); ok {
				warnings = append(warnings, w)
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := f.sess.ProcessInput(ctx, "run a tool", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	f.sess.Close()
	<-warningsDone

	if got := len(f.choice.Requests()); got != 1 {
		t.Fatalf("model requests = %d, want 1: the block stops the loop before the next request", got)
	}
	if !warningContains(warnings, "batch blocks the loop") {
		t.Fatalf("warnings = %+v, want the block reason delivered as a user-visible warning", warnings)
	}
	if _, ok := findToolResultInHistory(f.sess.history, "shell-0"); !ok {
		t.Fatal("the round's tool result is missing from history: results are persisted before the hook fires")
	}
	var steering []schema.Turn
	for _, turn := range f.sess.history {
		if turn.Kind == schema.TurnSteering && turn.SteeringKind == events.SteeringKindHookContext {
			steering = append(steering, turn)
		}
	}
	if len(steering) != 1 || !strings.Contains(steering[0].Message.Text(), "batch blocks the loop") {
		t.Fatalf("hook-context steering turns = %+v, want one carrying the block reason", steering)
	}
}

func warningContains(warnings []events.WarningData, want string) bool {
	for _, w := range warnings {
		if strings.Contains(w.Message, want) {
			return true
		}
	}
	return false
}

// TestPostToolBatch_NoCallsMeansNoHook pins the "batch of nothing has no
// tool_calls" rule: a round that executed no tools must not fire the event. The
// rounds after the shell round answer with bare text, which execute nothing (and
// retry until the bare-text budget is spent), so the hook must fire exactly once.
func TestPostToolBatch_NoCallsMeansNoHook(t *testing.T) {
	t.Parallel()
	marker := filepath.Join(t.TempDir(), "runs")
	contextCmd := fmt.Sprintf("printf 'run\\n' >> %s", shellquote.Literal(marker))
	mats := []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": contextCmd}}}}

	f := newPostToolBatchFixture(t, mats, func(round int, req llm.Request) llm.Response {
		if round == 0 {
			return toolCallResponse(batchShellCall("shell-0"))
		}
		return textResponse("bare text, no tool call")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// The bare-text rounds exhaust their retry budget, so the turn ends in an
	// error; that is both expected and irrelevant to the hook count.
	if _, err := f.sess.ProcessInput(ctx, "run a tool", nil); err == nil {
		t.Fatal("ProcessInput: want the bare-text retry budget to exhaust, got nil")
	}
	if got := postToolBatchRunCount(t, marker); got != 1 {
		t.Fatalf("hook runs = %d, want 1: only the shell round fires the event, not the tool-free rounds", got)
	}
}

// TestPostToolBatch_RunnerIsWired proves the session's runner actually offers
// RunPostToolBatch with the registered hook, so a wiring regression fails here
// rather than as a silent missing context.
func TestPostToolBatch_RunnerIsWired(t *testing.T) {
	t.Parallel()
	mats := []any{map[string]any{"matcher": "Bash", "hooks": []any{map[string]any{
		"type": "command", "timeout": 5,
		"command": `printf '%s' '{"hookSpecificOutput":{"hookEventName":"PostToolBatch","additionalContext":"wired"}}'`,
	}}}}
	f := newPostToolBatchFixture(t, mats, func(round int, req llm.Request) llm.Response { return finalResponse("done") })
	res := f.sess.hookRunner.RunPostToolBatch(context.Background(), hooks.Input{HookEventName: string(plugin.HookPostToolBatch)})
	if len(res.ModelContext) != 1 || res.ModelContext[0] != "wired" {
		t.Fatalf("RunPostToolBatch = %+v, want the hook's context despite its Bash matcher", res)
	}
}

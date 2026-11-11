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
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/internal/tool"
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

// capturedBatchCall is one tool_calls entry decoded from a fixture hook's
// captured batch input.
type capturedBatchCall struct {
	ToolName     string         `json:"tool_name"`
	ToolInput    map[string]any `json:"tool_input"`
	ToolUseID    string         `json:"tool_use_id"`
	ToolResponse string         `json:"tool_response"`
}

// newPostToolBatchCaptureFixture builds the fixture whose hook appends every
// batch input to a capture file, and returns a reader for the first captured
// batch. It is the shared capture protocol for tests that assert what the
// session rendered into PostToolBatch.
func newPostToolBatchCaptureFixture(t *testing.T, respond func(round int, req llm.Request) llm.Response) (postToolBatchFixture, func() []capturedBatchCall) {
	t.Helper()
	capture := filepath.Join(t.TempDir(), "batch-input.jsonl")
	captureCmd := fmt.Sprintf("cat >> %[1]s; echo >> %[1]s", shellquote.Literal(capture))
	mats := []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": captureCmd}}}}
	f := newPostToolBatchFixture(t, mats, respond)
	read := func() []capturedBatchCall {
		t.Helper()
		raw, err := os.ReadFile(capture)
		if err != nil {
			t.Fatalf("read captured hook input: %v", err)
		}
		first, _, _ := strings.Cut(strings.TrimSpace(string(raw)), "\n")
		var decoded struct {
			ToolCalls []capturedBatchCall `json:"tool_calls"`
		}
		if err := json.Unmarshal([]byte(first), &decoded); err != nil {
			t.Fatalf("decode captured hook input: %v (raw: %s)", err, first)
		}
		return decoded.ToolCalls
	}
	return f, read
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
			return agenttest.ToolCallResponse(batchShellCall(fmt.Sprintf("shell-%d", round)))
		default:
			return agenttest.FinalResponse("done")
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
		return agenttest.ToolCallResponse(batchShellCall("shell-0"), communicateCall("c1", reply))
	})

	// TRIPWIRE: a scripted in-process adapter, so only a genuine hang reaches 30s.
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
		return agenttest.ToolCallResponse(askUserCall("ask1", askUserArgsValid()))
	})

	// TRIPWIRE: a scripted in-process adapter, so only a genuine hang reaches 30s.
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
		return agenttest.ToolCallResponse(batchShellCall("shell-0"))
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

	// TRIPWIRE: a scripted in-process adapter, so only a genuine hang reaches 30s.
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
	contextCmd := "printf 'run\\n' >> " + shellquote.Literal(marker)
	mats := []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": contextCmd}}}}

	f := newPostToolBatchFixture(t, mats, func(round int, req llm.Request) llm.Response {
		if round == 0 {
			return agenttest.ToolCallResponse(batchShellCall("shell-0"))
		}
		return textResponse("bare text, no tool call")
	})

	// TRIPWIRE: a scripted in-process adapter, so only a genuine hang reaches 30s.
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
	f := newPostToolBatchFixture(t, mats, func(round int, req llm.Request) llm.Response { return agenttest.FinalResponse("done") })
	res := f.sess.hookRunner.RunPostToolBatch(context.Background(), hooks.Input{HookEventName: string(plugin.HookPostToolBatch)})
	if len(res.ModelContext) != 1 || res.ModelContext[0] != "wired" {
		t.Fatalf("RunPostToolBatch = %+v, want the hook's context despite its Bash matcher", res)
	}
}

// TestPostToolBatch_ToolCallsCarryClaudeNameAndModelOutput covers the session's
// own batch rendering, which the hand-built Input in the runner test cannot:
// tool_name is the Claude name for the canonical tool, and tool_response is
// res.Output (the model-facing text), not res.FullOutput.
//
// The probe is a TextResult whose Output and FullOutput differ, so swapping one
// for the other here fails. It also proves the round's batch hook saw both calls.
func TestPostToolBatch_ToolCallsCarryClaudeNameAndModelOutput(t *testing.T) {
	t.Parallel()
	f, capturedCalls := newPostToolBatchCaptureFixture(t, func(round int, req llm.Request) llm.Response {
		if round == 0 {
			return agenttest.ToolCallResponse(batchShellCall("shell-0"))
		}
		return agenttest.FinalResponse("done")
	})
	f.sess.registerTool("read_file", "returns distinct model and event text",
		map[string]any{
			"type":       "object",
			"properties": map[string]any{"file_path": map[string]any{"type": "string"}},
		},
		func(context.Context, any) (any, error) {
			return tool.TextResult{Output: "model-facing text", FullOutput: "event-facing text"}, nil
		})
	res := f.sess.execTool(context.Background(), llm.ToolCallData{
		ID:        "read-0",
		Name:      "read_file",
		Arguments: json.RawMessage(`{"file_path":"/tmp/x"}`),
	}, "")
	if res.IsError {
		t.Fatalf("read_file probe failed: %s", res.Output)
	}
	if res.Output == res.FullOutput {
		t.Fatalf("probe Output %q equals FullOutput; the assertion could not tell them apart", res.Output)
	}

	// Render the batch the round tail renders, so the assertion covers the
	// session's mapping rather than a hand-built Input.
	hi := f.sess.hookInput(plugin.HookPostToolBatch)
	hi.ToolCalls = f.sess.batchToolCalls([]llm.ToolCallData{{
		ID:        "read-0",
		Name:      "read_file",
		Arguments: json.RawMessage(`{"file_path":"/tmp/x"}`),
	}}, []tool.ExecResult{res})
	batchRes := f.sess.hookRunner.RunPostToolBatch(context.Background(), hi)
	if len(batchRes.UserMessages) != 0 || len(batchRes.ModelContext) != 0 {
		t.Fatalf("capture hook produced output: %+v", batchRes)
	}

	captured := capturedCalls()
	if len(captured) != 1 {
		t.Fatalf("tool_calls = %+v, want one entry", captured)
	}
	call := captured[0]
	if call.ToolName != "Read" {
		t.Errorf("tool_calls[0].tool_name = %q, want the Claude name %q", call.ToolName, "Read")
	}
	if call.ToolResponse != "model-facing text" {
		t.Errorf("tool_calls[0].tool_response = %q, want the model-facing Output %q", call.ToolResponse, "model-facing text")
	}
	if call.ToolUseID != "read-0" {
		t.Errorf("tool_calls[0].tool_use_id = %q, want read-0", call.ToolUseID)
	}
	if got := call.ToolInput["file_path"]; got != "/tmp/x" {
		t.Errorf("tool_calls[0].tool_input.file_path = %v, want /tmp/x", got)
	}
}

// TestPostToolBatch_RejectedArgumentsAndSynthesizedCallID pins #6020's two
// edge cases in the session's own batch rendering: a call whose raw arguments
// fail validation carries no tool_input (the same omission PostToolUse makes),
// and a call the provider sent without an ID resolves the CallID dispatch
// synthesized for it, so the event names the call that actually ran. The
// batch is sent through the registered PostToolBatch hook, and the captured
// hook input is asserted, so a regression fails where the hook sees it.
func TestPostToolBatch_RejectedArgumentsAndSynthesizedCallID(t *testing.T) {
	t.Parallel()
	f, capturedCalls := newPostToolBatchCaptureFixture(t, func(round int, req llm.Request) llm.Response {
		return agenttest.FinalResponse("done")
	})

	const synthesized = "batch-synthesized-1"
	// Dispatch records the arguments a call actually ran with under the
	// synthesized ID. Resolving that ID after the batch lookup would show the
	// originals instead, so the repaired command below is what the event must
	// carry for the id-less call.
	f.sess.recordDispatchedToolArguments(llm.ToolCallData{
		ID:        synthesized,
		Arguments: json.RawMessage(`{"command":"echo repaired","description":"batch test"}`),
	})
	valid := batchShellCall("batch-valid-0")
	calls := []llm.ToolCallData{
		valid,
		// Invalid UTF-8 is the shape only the prevalidation bound rejects;
		// json.Unmarshal would lossily replace it and decode the rest.
		{ID: "batch-invalid-1", Name: "exec_command", Type: "function", Arguments: json.RawMessage("{\"command\":\"\xff\"}")},
		{Name: "exec_command", Type: "function", Arguments: valid.Arguments},
	}
	results := []tool.ExecResult{
		{CallID: "batch-valid-0", Output: "ok"},
		{CallID: "batch-invalid-1", Output: "invalid arguments", IsError: true},
		{CallID: synthesized, Output: "ok"},
	}

	batch := f.sess.batchToolCalls(calls, results)
	if len(batch) != len(calls) {
		t.Fatalf("batchToolCalls returned %d entries, want %d", len(batch), len(calls))
	}
	if batch[0].ToolInput["command"] != "echo ok" {
		t.Fatalf("valid call tool_input = %v, want the decoded arguments", batch[0].ToolInput)
	}
	if batch[1].ToolInput != nil {
		t.Fatalf("rejected raw arguments produced tool_input %v, want nil", batch[1].ToolInput)
	}
	if batch[2].ToolUseID != synthesized {
		t.Fatalf("id-less call tool_use_id = %q, want the synthesized CallID %q", batch[2].ToolUseID, synthesized)
	}
	if got := batch[2].ToolInput["command"]; got != "echo repaired" {
		t.Fatalf("id-less call tool_input = %v, want the dispatched repaired arguments", batch[2].ToolInput)
	}

	hi := f.sess.hookInput(plugin.HookPostToolBatch)
	hi.ToolCalls = batch
	batchRes := f.sess.hookRunner.RunPostToolBatch(context.Background(), hi)
	if len(batchRes.UserMessages) != 0 || len(batchRes.ModelContext) != 0 {
		t.Fatalf("capture hook produced output: %+v", batchRes)
	}

	captured := capturedCalls()
	if len(captured) != len(calls) {
		t.Fatalf("captured tool_calls = %+v, want %d entries", captured, len(calls))
	}
	if captured[1].ToolInput != nil {
		t.Errorf("captured rejected call tool_input = %v, want nil", captured[1].ToolInput)
	}
	if captured[2].ToolUseID != synthesized {
		t.Errorf("captured id-less call tool_use_id = %q, want %q", captured[2].ToolUseID, synthesized)
	}
	if got := captured[2].ToolInput["command"]; got != "echo repaired" {
		t.Errorf("captured id-less call tool_input = %v, want the dispatched repaired arguments", captured[2].ToolInput)
	}
}

//go:build serffuzz

package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/clock"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// FuzzResumeTurnQueueProgram drives a persisted session through the first
// user-turn boundary that follows restore. The fixture deliberately puts the
// provider at the only model boundary: prompt hooks and regular turns are
// answered by one scripted adapter, while the real Session owns persistence,
// hook deferral/delivery, history ordering, event emission, cancellation, and
// queued-input recovery. It never uses a network, shell, Git, MCP server, or
// ambient configuration.
//
// The program has three semantic paths:
//   - an ordinary first user turn, followed by a MaxTurns acceptance/rejection;
//   - a canceled turn whose queued input is deliberately continued under root;
//   - a canceled turn whose queued input must be put back before recovery.
//
// Across all paths, a deferred resume hook must run exactly once on the first
// accepted user input, inject its model context before that input, surface its
// user message as a diagnostic event, and never resurrect the interrupted
// input as a model-visible user turn.
func FuzzResumeTurnQueueProgram(f *testing.F) {
	for _, seed := range [][]byte{
		{0, 0, 0, 0}, // direct first turn, unlimited
		{0, 1, 1, 2}, // direct first turn, MaxTurns rejection, image
		{1, 0, 1, 3}, // canceled turn drains queued input under root
		{2, 2, 0, 4}, // canceled turn requeues input before recovery
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		program := rttDecodeProgram(data)
		sess, adapter := rttRestoredSession(t, program)
		defer sess.Close()

		// Restore itself only records the pending resume hook; it must not make a
		// provider request or mutate history before an accepted user input exists.
		if got := len(adapter.Requests()); got != 0 {
			t.Fatalf("restore invoked scripted provider %d times before a user turn", got)
		}
		if got := rttHistoryText(sess); strings.Contains(got, rttHookContext) {
			t.Fatalf("restore injected resume context before user input: %q", got)
		}
		rttDrainEvents(sess)

		// A pre-existing steering entry proves that the hook context is persisted
		// directly before the user turn while ordinary steering still drains through
		// the normal first-turn path.
		sess.Steer(rttManualSteering)

		switch program.mode {
		case rttDirect:
			rttRunDirect(t, sess, adapter, program)
		case rttDrainInterrupted:
			rttRunInterruptedDrain(t, sess, adapter, program)
		case rttRequeueInterrupted:
			rttRunInterruptedRequeue(t, sess, adapter, program)
		default:
			t.Fatalf("unknown program mode %d", program.mode)
		}

		rttAssertResumeHookLifecycle(t, sess, adapter)
	})
}

type rttMode int

const (
	rttDirect rttMode = iota
	rttDrainInterrupted
	rttRequeueInterrupted
)

const (
	rttHookPrompt     = "RTH_HOOK"
	rttHookContext    = "RTH_HOOK_CONTEXT"
	rttHookUser       = "RTH_HOOK_USER"
	rttManualSteering = "RTH_MANUAL_STEERING"
	rttPriorUser      = "RTH_PRIOR_USER"
	rttPriorAssistant = "RTH_PRIOR_ASSISTANT"
	rttQueuedInput    = "RTH_QUEUED_INPUT"
	rttInterrupted    = "RTH_INTERRUPTED_INPUT"
	rttRecoveryInput  = "RTH_RECOVERY_INPUT"
	rttDone           = "RTH_DONE"
)

var rttUserInputs = []string{
	"RTH_USER_ALPHA",
	"RTH_USER_BRAVO",
	"RTH_USER_CHARLIE",
}

type rttProgram struct {
	mode      rttMode
	maxTurns  int
	withImage bool
	userInput string
}

func rttDecodeProgram(data []byte) rttProgram {
	byteAt := func(i int) byte {
		if i >= len(data) {
			return 0
		}
		return data[i]
	}
	maxTurns := []int{0, 1, 2}[int(byteAt(1))%3]
	mode := rttMode(int(byteAt(0)) % 3)
	// The requeue path intentionally completes two recovery turns. Keeping it
	// unlimited lets the oracle distinguish a requeued input from a dropped one.
	if mode == rttRequeueInterrupted {
		maxTurns = 0
	}
	return rttProgram{
		mode:      mode,
		maxTurns:  maxTurns,
		withImage: byteAt(2)&1 == 1,
		userInput: rttUserInputs[int(byteAt(3))%len(rttUserInputs)],
	}
}

func rttRestoredSession(t *testing.T, program rttProgram) (*Session, *agenttest.ScriptedAdapter) {
	t.Helper()
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	pluginDir := rttWriteResumeHookPlugin(t, root)
	clk := agenttest.NewFakeClock()
	cfg := SessionConfig{
		StateDir:         stateDir,
		MaxTurns:         program.maxTurns,
		PluginDirs:       []string{pluginDir},
		NoProjectPrompts: true,
		clock:            clk,
		testOnly:         rttTestConfig(),
	}

	// The initial session creates the real metadata/transcript pair. Its plugin
	// matches only resume, so construction cannot consume a hook response.
	freshClient, _ := rttClient()
	fresh, err := NewSession(freshClient, NewOpenAIProfile("gpt-5.2"), &agenttest.DenyEnv{WorkDir: workspace}, cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	meta := fresh.Meta()
	fresh.Close()

	restoreClient, adapter := rttClient()
	restored, err := RestoreSessionFromMetaWithConfig(
		restoreClient,
		NewOpenAIProfile("gpt-5.2"),
		&agenttest.DenyEnv{WorkDir: workspace},
		meta,
		RestoreSessionConfig{
			StateDir:                stateDir,
			deferRestoreSideEffects: true,
			resumeHistory: []schema.Turn{
				schema.NewTurn(schema.TurnUserInput, llm.User(rttPriorUser)),
				schema.NewTurn(schema.TurnAssistant, llm.Assistant(rttPriorAssistant)),
			},
			clock:    clk,
			testOnly: rttTestConfig(),
		},
	)
	if err != nil {
		t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
	}
	return restored, adapter
}

func rttClient() (*llm.Client, *agenttest.ScriptedAdapter) {
	adapter := &agenttest.ScriptedAdapter{
		Provider: "openai",
		Responder: func(req llm.Request) llm.Response {
			if rttIsHookRequest(req) {
				return llm.Response{Message: llm.Assistant(`{"systemMessage":"RTH_HOOK_USER","hookSpecificOutput":{"additionalContext":"RTH_HOOK_CONTEXT"}}`)}
			}
			return agenttest.FinalResponse(rttDone)
		},
	}
	client := llm.NewClient()
	client.Register(adapter)
	return client, adapter
}

func rttTestConfig() testConfig {
	return testConfig{
		skipGitSnapshot: true,
		noSyncJobStore:  true,
		environmentInfo: func(env execenv.ExecutionEnvironment, clk clock.Clock) schema.EnvironmentInfo {
			return schema.EnvironmentInfo{
				WorkingDir: env.WorkingDirectory(),
				Platform:   "rtt",
				OSVersion:  "deny-env",
				Today:      clk.Now().UTC().Format("2006-01-02"),
			}
		},
	}
}

func rttWriteResumeHookPlugin(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, "plugin")
	for _, path := range []string{
		filepath.Join(dir, ".claude-plugin"),
		filepath.Join(dir, "hooks"),
	} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude-plugin", "plugin.json"), []byte(`{"name":"rtt-resume-plugin","version":"1.0.0"}`), 0o600); err != nil {
		t.Fatalf("write plugin manifest: %v", err)
	}
	hooks := `{"SessionStart":[{"matcher":"resume","hooks":[{"type":"prompt","prompt":"RTH_HOOK $MESSAGE"}]}]}`
	if err := os.WriteFile(filepath.Join(dir, "hooks", "hooks.json"), []byte(hooks), 0o600); err != nil {
		t.Fatalf("write hook config: %v", err)
	}
	return dir
}

func rttRunDirect(t *testing.T, sess *Session, adapter *agenttest.ScriptedAdapter, program rttProgram) {
	t.Helper()
	images := rttImages(program.withImage)
	out, err := sess.ProcessInput(context.Background(), program.userInput, images)
	if err != nil {
		t.Fatalf("first ProcessInput: %v", err)
	}
	if !strings.Contains(out, rttDone) {
		t.Fatalf("first ProcessInput output = %q, want %q", out, rttDone)
	}
	requests := rttModelRequests(adapter.Requests())
	if len(requests) != 1 {
		t.Fatalf("first direct turn model requests = %d, want 1", len(requests))
	}
	rttAssertRequestOrder(t, requests[0], rttPriorUser, rttPriorAssistant, rttHookContext, program.userInput, rttManualSteering)
	if program.withImage && !rttRequestHasImage(requests[0]) {
		t.Fatal("first direct request lost its user image")
	}

	// The second input proves both sides of the MaxTurns gate and that a drained
	// resume hook cannot run a second time after its first accepted delivery.
	out, err = sess.ProcessInput(context.Background(), "RTH_SECOND_INPUT", nil)
	requests = rttModelRequests(adapter.Requests())
	if program.maxTurns == 1 {
		requireBudgetExhaustion(t, err, exhaustedBudgetTurns, 1, false)
		if out != "" || len(requests) != 1 {
			t.Fatalf("MaxTurns=1 accepted a second model turn: output=%q requests=%d", out, len(requests))
		}
		return
	}
	if err != nil {
		t.Fatalf("second ProcessInput: %v", err)
	}
	if !strings.Contains(out, rttDone) || len(requests) != 2 {
		t.Fatalf("unlimited/2-turn session second output=%q requests=%d, want another model turn", out, len(requests))
	}
	if got := strings.Count(rttRequestText(requests[1]), rttHookContext); got != 1 {
		t.Fatalf("second direct request resume context count = %d, want 1", got)
	}
}

func rttRunInterruptedDrain(t *testing.T, sess *Session, adapter *agenttest.ScriptedAdapter, program rttProgram) {
	t.Helper()
	if err := sess.EnqueueWithImages(context.Background(), rttQueuedInput, rttImages(program.withImage)); err != nil {
		t.Fatalf("EnqueueWithImages: %v", err)
	}
	if got := sess.QueueDepth(); got != 1 {
		t.Fatalf("queue depth after enqueue = %d, want 1", got)
	}
	turnCtx, cancelTurn := context.WithCancel(context.Background())
	marked := WithQueuedInputDrainOnInterruptHandler(turnCtx, context.Background(), func(root context.Context) (context.Context, context.CancelFunc) {
		return root, func() {}
	})
	cancelTurn()
	out, err := sess.ProcessInput(marked, rttInterrupted, nil)
	if err != nil {
		t.Fatalf("interrupted queued drain: %v", err)
	}
	if !strings.Contains(out, rttDone) || sess.QueueDepth() != 0 {
		t.Fatalf("interrupted queued drain output=%q queue=%d, want completed queued turn", out, sess.QueueDepth())
	}
	requests := rttModelRequests(adapter.Requests())
	if len(requests) != 1 {
		t.Fatalf("queued drain model requests = %d, want 1", len(requests))
	}
	rttAssertRequestOrder(t, requests[0], rttPriorUser, rttHookContext, rttQueuedInput, rttManualSteering)
	if strings.Contains(rttRequestText(requests[0]), rttInterrupted) {
		t.Fatalf("canceled input leaked into drained model request: %q", rttRequestText(requests[0]))
	}
}

func rttRunInterruptedRequeue(t *testing.T, sess *Session, adapter *agenttest.ScriptedAdapter, program rttProgram) {
	t.Helper()
	if err := sess.EnqueueWithImages(context.Background(), rttQueuedInput, rttImages(program.withImage)); err != nil {
		t.Fatalf("EnqueueWithImages: %v", err)
	}
	rootCtx, cancelRoot := context.WithCancel(context.Background())
	cancelRoot()
	turnCtx, cancelTurn := context.WithCancel(context.Background())
	marked := WithQueuedInputDrainOnInterrupt(turnCtx, rootCtx)
	cancelTurn()
	if _, err := sess.ProcessInput(marked, rttInterrupted, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("non-drain interrupt error = %v, want context canceled", err)
	}
	if got := sess.QueueDepth(); got != 1 {
		t.Fatalf("non-drain interrupt queue depth = %d, want requeued entry", got)
	}
	if got := sess.QueuePreview(); len(got) != 1 || got[0] != rttQueuedInput {
		t.Fatalf("requeued preview = %v, want [%q]", got, rttQueuedInput)
	}
	if got := len(rttModelRequests(adapter.Requests())); got != 0 {
		t.Fatalf("canceled input unexpectedly reached model %d times", got)
	}

	out, err := sess.ProcessInput(context.Background(), rttRecoveryInput, nil)
	if err != nil {
		t.Fatalf("recovery ProcessInput: %v", err)
	}
	if strings.Count(out, rttDone) != 2 || sess.QueueDepth() != 0 {
		t.Fatalf("recovery output=%q queue=%d, want recovery plus requeued completion", out, sess.QueueDepth())
	}
	requests := rttModelRequests(adapter.Requests())
	if len(requests) != 2 {
		t.Fatalf("recovery model requests = %d, want 2", len(requests))
	}
	rttAssertRequestOrder(t, requests[0], rttPriorUser, rttHookContext, rttRecoveryInput, rttManualSteering)
	if !strings.Contains(rttRequestText(requests[1]), rttQueuedInput) {
		t.Fatalf("requeued input missing from second recovery request: %q", rttRequestText(requests[1]))
	}
}

func rttAssertResumeHookLifecycle(t *testing.T, sess *Session, adapter *agenttest.ScriptedAdapter) {
	t.Helper()
	requests := adapter.Requests()
	hookCount := 0
	for _, req := range requests {
		if rttIsHookRequest(req) {
			hookCount++
		}
	}
	if hookCount != 1 {
		t.Fatalf("resume hook requests = %d, want exactly 1 (requests=%d)", hookCount, len(requests))
	}
	sess.mu.Lock()
	pending := sess.pendingSessionStartKind
	sess.mu.Unlock()
	if pending != nil {
		t.Fatalf("resume hook remained pending after accepted user input: %q", *pending)
	}
	if got := strings.Count(rttHistoryText(sess), rttHookContext); got != 1 {
		t.Fatalf("persisted resume context count = %d, want 1", got)
	}

	seenContext := false
	seenUser := false
	for _, ev := range rttDrainEvents(sess) {
		switch data := ev.Data.(type) {
		case events.SteeringInjectedData:
			seenContext = seenContext || data.Text == "<SYSTEM-REMINDER>"+rttHookContext+"</SYSTEM-REMINDER>"
		case events.WarningData:
			seenUser = seenUser || data.Message == rttHookUser
		}
	}
	if !seenContext || !seenUser {
		t.Fatalf("resume hook events: context=%v user=%v", seenContext, seenUser)
	}
}

func rttImages(include bool) []ImageAttachment {
	if !include {
		return nil
	}
	return []ImageAttachment{{MediaType: "image/png", Data: []byte{0x89, 0x50, 0x4e, 0x47}, Name: "rtt.png"}}
}

func rttRequestText(req llm.Request) string {
	parts := make([]string, 0, len(req.Messages))
	for _, msg := range req.Messages {
		parts = append(parts, msg.Text())
	}
	return strings.Join(parts, "\n---\n")
}

func rttIsHookRequest(req llm.Request) bool {
	return len(req.Messages) == 1 && strings.Contains(rttRequestText(req), rttHookPrompt)
}

func rttModelRequests(requests []llm.Request) []llm.Request {
	out := make([]llm.Request, 0, len(requests))
	for _, req := range requests {
		if !rttIsHookRequest(req) {
			out = append(out, req)
		}
	}
	return out
}

func rttAssertRequestOrder(t *testing.T, req llm.Request, needles ...string) {
	t.Helper()
	text := rttRequestText(req)
	from := 0
	for _, needle := range needles {
		idx := strings.Index(text[from:], needle)
		if idx < 0 {
			t.Fatalf("request missing %q after offset %d:\n%s", needle, from, text)
		}
		from += idx + len(needle)
	}
}

func rttRequestHasImage(req llm.Request) bool {
	for _, msg := range req.Messages {
		for _, part := range msg.Content {
			if part.Kind == llm.ContentImage && part.Image != nil && part.Image.MediaType == "image/png" && len(part.Image.Data) > 0 {
				return true
			}
		}
	}
	return false
}

func rttHistoryText(sess *Session) string {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	parts := make([]string, 0, len(sess.history))
	for _, turn := range sess.history {
		parts = append(parts, turn.Message.Text())
	}
	return strings.Join(parts, "\n---\n")
}

func rttDrainEvents(sess *Session) []events.SessionEvent {
	var out []events.SessionEvent
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				return out
			}
			out = append(out, ev)
		default:
			return out
		}
	}
}
